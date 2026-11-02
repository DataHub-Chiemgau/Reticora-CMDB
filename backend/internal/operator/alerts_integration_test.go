package operator_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

// WP-448 (OPS-05, E-75, F-134): the Alertmanager bridge through the
// production router (server.NewRouter with the OpenAPI validation as route
// middleware) and the authentication chain of cmd/server, on PostgreSQL.
// Organization A of the scopetest fixture is the operator organization
// (RETICORA_OPS_ALERT_ORG_ID), organization B a customer.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestAlertBridge' ./internal/operator/

const (
	alertToken      = "ops-alert-token-for-the-integration-test"
	breakGlassToken = "break-glass-token-of-the-integration-test"
)

// alertStack is the production chain over the PostgreSQL repositories.
type alertStack struct {
	handler http.Handler
	issuer  *identity.SessionIssuer
}

func newAlertStack(t *testing.T, repos server.Repositories, enc *crypto.EnvelopeEncryptor, opts server.Options) *alertStack {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	store := cache.NewMemoryStore()
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{})
	opts.Version, opts.Entitlements = "wp448", entitlements
	opts.CIService = ci.NewServiceWithLimits(repos.CI, entitlements)
	opts.Dispatcher = webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries, PollInterval: -1})
	opts.Credentials = credential.NewService(repos.Credential, enc)
	opts.OIDC, opts.Sessions = identity.NewOIDCProvider(identity.OIDCConfig{}), issuer
	opts.Operator = operator.Config{BreakGlassToken: breakGlassToken}
	opts.RouteMiddleware = []func(http.Handler) http.Handler{middleware.OpenAPIValidation, middleware.IdempotencyWithStore(store)}
	mux, _, err := server.NewRouter(repos, opts)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	handler := middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.PreAuthRateLimiter(middleware.DefaultPreAuthPerMinute, store),
		middleware.AuthMiddlewareWithAPIKeys(issuer, identity.NewAPIKeyServiceWithStore(repos.APIKeys)),
		middleware.TenantMiddleware,
	)(mux)
	return &alertStack{handler: handler, issuer: issuer}
}

// session signs an org-wide session of a user with permissions.
func (s *alertStack) session(t *testing.T, orgID, userID string, perms ...identity.Permission) string {
	t.Helper()
	now := time.Now().UTC()
	token, err := s.issuer.Issue(identity.SessionClaims{Subject: userID, OrganizationID: orgID, Permissions: perms, ID: "wp448-" + userID,
		Scope:    &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (s *alertStack) do(method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req.RemoteAddr = "198.51.100.48:41000"
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	return w
}

// alertPayload is an Alertmanager payload of version 4 for one group with
// one critical alert per instance.
func alertPayload(t *testing.T, status string, instances ...string) string {
	t.Helper()
	alerts := make([]map[string]any, 0, len(instances))
	for _, instance := range instances {
		alerts = append(alerts, map[string]any{"status": status, "startsAt": "2026-10-25T10:00:00Z", "endsAt": "0001-01-01T00:00:00Z",
			"labels":      map[string]string{"alertname": "ReticoraWorkerStopped", "worker": "notifier", "severity": "critical", "instance": instance},
			"annotations": map[string]string{"summary": "Worker notifier stopped on " + instance}, "fingerprint": "f-" + instance,
			"generatorURL": "https://prometheus.example.invalid/graph"})
	}
	raw, err := json.Marshal(map[string]any{
		"version": "4", "groupKey": `{}/{severity="critical"}:{alertname="ReticoraWorkerStopped", worker="notifier"}`,
		"truncatedAlerts": 0, "status": status, "receiver": "operators-critical",
		"groupLabels":  map[string]string{"alertname": "ReticoraWorkerStopped", "worker": "notifier"},
		"commonLabels": map[string]string{"alertname": "ReticoraWorkerStopped", "worker": "notifier", "severity": "critical"},
		"externalURL":  "https://alertmanager.example.invalid", "alerts": alerts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// hookReceiver records the posts of the operator organization's webhook
// channel.
type hookReceiver struct {
	mu    sync.Mutex
	posts []map[string]any
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var post map[string]any
	_ = json.Unmarshal(raw, &post)
	h.mu.Lock()
	h.posts = append(h.posts, post)
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *hookReceiver) received() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.posts)
}

// mailRecorder stands in for the central SMTP of the customer's channel.
type mailRecorder struct {
	mu   sync.Mutex
	sent int
}

func (m *mailRecorder) Send(_ context.Context, _ *notify.Channel, msg *notify.Message) (*notify.Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent++
	return &notify.Delivery{Recipients: len(msg.To), SentAt: time.Now().UTC()}, nil
}

type deliveryRow struct {
	channel, category, status, dedupKey string
	critical                            bool
	recipients                          []notify.Recipient
}

// TestAlertBridgeDeliversToTheOperatorOrganization covers the retest of
// F-134: a firing notification of Alertmanager becomes exactly one
// notification_delivery on the operator organization's webhook channel,
// addressed to its user with monitoring:write, and the notifier delivers
// it; the identical payload queues and delivers nothing more; the resolved
// group is delivered again. Each queued notification has an audit entry in
// the operator organization without the token. A missing or wrong token,
// the session and the API key of the customer organization (both valid on
// the tenant API) and the break-glass token get 401, recorded in
// operator_audit, also with a body the validation would refuse. The
// customer organization sees neither deliveries nor audit entries, neither
// through the API nor in a tenant transaction (RLS), and receives nothing.
func TestAlertBridgeDeliversToTheOperatorOrganization(t *testing.T) {
	f := scopetest.Seed(t, "66")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	opsRole, customerRole := f.ID(), f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp448-ops', 'org', $3::jsonb)`,
		opsRole, f.OrgA, `["monitoring:read", "monitoring:write", "notification:manage"]`)
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp448-customer', 'org', $3::jsonb)`,
		customerRole, f.OrgB, `["monitoring:read", "monitoring:write", "notification:manage"]`)
	ops := f.AppUser(t, f.OrgA, "wp448-ops")
	_ = f.AppUser(t, f.OrgA, "wp448-reader") // no monitoring:write: no recipient
	customer := f.AppUser(t, f.OrgB, "wp448-customer")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, ops, opsRole)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgB, customer, customerRole)

	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	hook := &hookReceiver{}
	hookServer := httptest.NewServer(hook)
	t.Cleanup(hookServer.Close)
	opsChannel := &notify.Channel{OrganizationID: f.OrgA, Name: "wp448-ops-webhook", Provider: notify.ProviderWebhook, Enabled: true,
		Config: map[string]any{"url": hookServer.URL + "/ops"}}
	customerChannel := &notify.Channel{OrganizationID: f.OrgB, Name: "wp448-customer-mail", Provider: notify.ProviderCentralSMTP, Enabled: true}
	for _, ch := range []*notify.Channel{opsChannel, customerChannel} {
		if err = center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	mail := &mailRecorder{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderWebhook:     notify.NewWebhookProvider(egress.NewClient(egress.Options{AllowPrivate: true})),
		notify.ProviderCentralSMTP: mail,
	}))

	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	s := newAlertStack(t, repos, enc, server.Options{OperatorPool: f.App, Notifications: center, Notifier: notifier,
		OpsAlerts: operator.AlertConfig{Token: alertToken, OrgID: f.OrgA}})
	auth := map[string]string{"Authorization": "Bearer " + alertToken}

	deliveries := func(orgID string) []deliveryRow {
		t.Helper()
		rows, qErr := f.Admin.Query(bg, `SELECT channel_id::text, category, status, COALESCE(dedup_key, ''), critical, recipients
			FROM notification_delivery WHERE organization_id = $1 ORDER BY created_at, id`, orgID)
		if qErr != nil {
			t.Fatal(qErr)
		}
		out, qErr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deliveryRow, error) {
			var d deliveryRow
			var recipients []byte
			if scanErr := row.Scan(&d.channel, &d.category, &d.status, &d.dedupKey, &d.critical, &recipients); scanErr != nil {
				return d, scanErr
			}
			return d, json.Unmarshal(recipients, &d.recipients)
		})
		if qErr != nil {
			t.Fatal(qErr)
		}
		return out
	}
	process := func() {
		t.Helper()
		if _, pErr := notifier.ProcessDue(bg); pErr != nil {
			t.Fatalf("notifier: %v", pErr)
		}
	}
	var lastOperatorAudit int64
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&lastOperatorAudit); err != nil {
		t.Fatal(err)
	}

	// firing: exactly one delivery on the operator channel, delivered.
	w := s.do(http.MethodPost, operator.AlertPath, alertPayload(t, "firing", "api-1", "api-2"), auth)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"notification":"queued"`) || !strings.Contains(w.Body.String(), `"deliveries":1`) {
		t.Fatalf("firing: %d %s, want 202 queued with 1 delivery", w.Code, w.Body.String())
	}
	got := deliveries(f.OrgA)
	if len(got) != 1 || got[0].channel != opsChannel.ID || got[0].category != operator.AlertCategory || !got[0].critical ||
		!strings.HasPrefix(got[0].dedupKey, "alertmanager:") || len(got[0].recipients) != 1 || got[0].recipients[0].UserID != ops {
		t.Fatalf("deliveries of the operator organization after firing: %+v; want one critical ops_alert on %s to %s", got, opsChannel.ID, ops)
	}
	firingKey := got[0].dedupKey
	// Delivery and audit entry come from one transaction of the operator
	// organization (same xmin, read before the notifier updates the row).
	var deliveryTx, auditTx string
	if err = f.Admin.QueryRow(bg, `SELECT d.xmin::text, a.xmin::text FROM notification_delivery d, audit_log a
		WHERE d.organization_id = $1 AND d.dedup_key = $2 AND a.organization_id = $1 AND a.action = $3`,
		f.OrgA, firingKey, operator.AlertAction).Scan(&deliveryTx, &auditTx); err != nil || deliveryTx != auditTx {
		t.Errorf("delivery written in transaction %s, audit entry in %s (%v); want one transaction", deliveryTx, auditTx, err)
	}
	process()
	posts := hook.received()
	if len(posts) != 1 || posts[0]["organization_id"] != f.OrgA || posts[0]["category"] != operator.AlertCategory ||
		posts[0]["subject"] != "Betriebsalarm (kritisch): ReticoraWorkerStopped (2 Alarme)" {
		t.Fatalf("webhook posts after firing: %v", posts)
	}
	if data, _ := posts[0]["data"].(map[string]any); data["status"] != "firing" || data["dedup_key"] != firingKey {
		t.Errorf("firing post data %v", data)
	}
	if got = deliveries(f.OrgA); got[0].status != notify.StatusSent {
		t.Errorf("firing delivery status %q, want sent", got[0].status)
	}

	// The identical payload again (Alertmanager's repetition): nothing more.
	w = s.do(http.MethodPost, operator.AlertPath, alertPayload(t, "firing", "api-1", "api-2"), auth)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"notification":"duplicate"`) || !strings.Contains(w.Body.String(), `"deliveries":0`) {
		t.Fatalf("repeated firing: %d %s, want 202 duplicate", w.Code, w.Body.String())
	}
	process()
	if n, p := len(deliveries(f.OrgA)), len(hook.received()); n != 1 || p != 1 {
		t.Fatalf("after the repetition: %d deliveries, %d posts; want 1 and 1", n, p)
	}

	// resolved: one more delivery, delivered.
	w = s.do(http.MethodPost, operator.AlertPath, alertPayload(t, "resolved", "api-1", "api-2"), auth)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"notification":"queued"`) {
		t.Fatalf("resolved: %d %s", w.Code, w.Body.String())
	}
	process()
	got, posts = deliveries(f.OrgA), hook.received()
	if len(got) != 2 || got[1].dedupKey == firingKey || got[1].critical || got[1].status != notify.StatusSent || len(posts) != 2 ||
		posts[1]["subject"] != "Betriebsalarm behoben: ReticoraWorkerStopped (2 Alarme)" {
		t.Fatalf("after resolved: deliveries %+v, posts %v", got, posts)
	}

	// Refused: no token, a wrong one, the customer's valid credentials, the
	// break-glass token; a garbage body without Content-Type is refused by
	// the bridge before the validation reads it.
	customerSession := s.session(t, f.OrgB, customer, identity.AllPermissions()...)
	generated, err := identity.GenerateAPIKey(identity.APIKeyEnvironmentLive)
	if err != nil {
		t.Fatal(err)
	}
	customerScope := database.OrgWideScope(f.OrgB, customer)
	if _, err = identity.NewPGAPIKeyStore(f.App).Create(database.ContextWithTenantScope(bg, &customerScope), &identity.StoredAPIKey{
		OrganizationID: f.OrgB, Name: "wp448-customer-key", KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
		Environment: generated.Environment, Permissions: []identity.Permission{identity.PermNotificationManage}, CreatedBy: customer}); err != nil {
		t.Fatal(err)
	}
	for name, header := range map[string]map[string]string{
		"customer session": {"Authorization": "Bearer " + customerSession},
		"customer API key": {"X-API-Key": generated.Plaintext},
	} {
		if w = s.do(http.MethodGet, "/api/v1/notification-deliveries", "", header); w.Code != http.StatusOK {
			t.Fatalf("control: %s on the tenant API: %d %s, want 200", name, w.Code, w.Body.String())
		}
	}
	refusals := map[string]map[string]string{
		"no token":                 nil,
		"wrong token":              {"Authorization": "Bearer " + strings.Repeat("x", len(alertToken))},
		"customer session":         {"Authorization": "Bearer " + customerSession},
		"customer API key":         {"X-API-Key": generated.Plaintext},
		"break-glass header":       {operator.BreakGlassHeader: breakGlassToken},
		"break-glass token bearer": {"Authorization": "Bearer " + breakGlassToken},
	}
	for name, header := range refusals {
		if w = s.do(http.MethodPost, operator.AlertPath, alertPayload(t, "firing", "api-9"), header); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", name, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, operator.AlertPath, strings.NewReader("not json"))
	req.RemoteAddr = "198.51.100.48:41000"
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("garbage without token and Content-Type: %d %s, want 401 before the validation", rec.Code, rec.Body.String())
	}
	if n := len(deliveries(f.OrgA)); n != 2 {
		t.Errorf("refused requests queued: %d deliveries, want 2", n)
	}
	var refused int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM operator_audit WHERE id > $1 AND operator_id = 'anonymous'
		AND action = 'POST /alerts/alertmanager' AND status = 401`, lastOperatorAudit).Scan(&refused); err != nil || refused != len(refusals)+1 {
		t.Errorf("operator_audit: %d refusals recorded (%v), want %d", refused, err, len(refusals)+1)
	}

	// The audit entries of the operator organization, without the token.
	rows, err := f.Admin.Query(bg, `SELECT actor_type, resource_type, changes FROM audit_log
		WHERE organization_id = $1 AND action = $2 ORDER BY seq`, f.OrgA, operator.AlertAction)
	if err != nil {
		t.Fatal(err)
	}
	type auditRow struct {
		actor, resource string
		changes         []byte
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (auditRow, error) {
		var a auditRow
		return a, row.Scan(&a.actor, &a.resource, &a.changes)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries %d, want 2 (firing, resolved)", len(entries))
	}
	for i, want := range []string{"firing", "resolved"} {
		var changes map[string]any
		if err = json.Unmarshal(entries[i].changes, &changes); err != nil {
			t.Fatal(err)
		}
		if entries[i].actor != "system" || entries[i].resource != "notification" || changes["status"] != want ||
			changes["deliveries"] != float64(1) || changes["alertname"] != "ReticoraWorkerStopped" || changes["dedup_key"] != got[i].dedupKey {
			t.Errorf("audit entry %d: %s %s %v", i, entries[i].actor, entries[i].resource, changes)
		}
		if strings.Contains(string(entries[i].changes), alertToken) {
			t.Errorf("audit entry %d holds the token", i)
		}
	}

	// The customer organization sees and receives nothing: API, RLS.
	if n := len(deliveries(f.OrgB)); n != 0 || mail.sent != 0 {
		t.Errorf("customer organization: %d deliveries, %d mails; want none", n, mail.sent)
	}
	for name, header := range map[string]map[string]string{
		"customer session":    {"Authorization": "Bearer " + customerSession},
		"customer API key":    {"X-API-Key": generated.Plaintext},
		"operator org (ctrl)": {"Authorization": "Bearer " + s.session(t, f.OrgA, ops, identity.PermNotificationManage)},
	} {
		w = s.do(http.MethodGet, "/api/v1/notification-deliveries", "", header)
		var list struct {
			Data []struct {
				Category string `json:"category"`
			} `json:"data"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatalf("%s: GET /notification-deliveries %d %s", name, w.Code, w.Body.String())
		}
		alerts := 0
		for _, d := range list.Data {
			if d.Category == operator.AlertCategory {
				alerts++
			}
		}
		if want := map[bool]int{true: 2, false: 0}[name == "operator org (ctrl)"]; alerts != want {
			t.Errorf("%s lists %d operations alerts, want %d", name, alerts, want)
		}
	}
	count := func(orgID, query string) int {
		t.Helper()
		scope := database.OrgWideScope(orgID, "")
		var n int
		if cErr := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, f.OrgA, operator.AlertCategory, operator.AlertAction).Scan(&n)
		}); cErr != nil {
			t.Fatal(cErr)
		}
		return n
	}
	const visible = `SELECT (SELECT count(*) FROM notification_delivery WHERE organization_id = $1 OR category = $2)
		+ (SELECT count(*) FROM audit_log WHERE organization_id = $1 OR action = $3)`
	if n := count(f.OrgB, visible); n != 0 {
		t.Errorf("a tenant transaction of the customer sees %d rows of the operator alerts, want 0", n)
	}
	if n := count(f.OrgA, visible); n < 4 {
		t.Errorf("control: the operator organization sees %d rows, want at least 4", n)
	}
}

// TestAlertBridgeBeforeTheValidation: in the production router the bridge
// authenticates before the OpenAPI validation reads the body: without the
// token a body of any size or form is 401; with it, a body over 1 MiB is
// 413 and a missing Content-Type the validation's 400. Without operator
// organization the route is 404. No database needed.
func TestAlertBridgeBeforeTheValidation(t *testing.T) {
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	s := newAlertStack(t, server.MemoryRepositories(), enc, server.Options{
		OpsAlerts: operator.AlertConfig{Token: alertToken, OrgID: "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"}})
	auth := map[string]string{"Authorization": "Bearer " + alertToken}
	huge := strings.Repeat(" ", operator.MaxAlertBody+1)

	for name, tc := range map[string]struct {
		body, contentType string
		header            map[string]string
		want              int
	}{
		"garbage without token":            {"not json", "", nil, http.StatusUnauthorized},
		"oversized without token":          {huge, "application/json", nil, http.StatusUnauthorized},
		"oversized with token":             {huge, "application/json", auth, http.StatusRequestEntityTooLarge},
		"no Content-Type with token":       {alertPayload(t, "firing", "api-1"), "", auth, http.StatusBadRequest},
		"valid with token, no database":    {alertPayload(t, "firing", "api-1"), "application/json", auth, http.StatusServiceUnavailable},
		"tenant API key instead of token":  {alertPayload(t, "firing", "api-1"), "application/json", map[string]string{"X-API-Key": "rk_live_" + strings.Repeat("a", 52)}, http.StatusUnauthorized},
		"operator break-glass, no token":   {alertPayload(t, "firing", "api-1"), "application/json", map[string]string{operator.BreakGlassHeader: breakGlassToken}, http.StatusUnauthorized},
		"operator break-glass as a bearer": {alertPayload(t, "firing", "api-1"), "application/json", map[string]string{"Authorization": "Bearer " + breakGlassToken}, http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodPost, operator.AlertPath, strings.NewReader(tc.body))
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		req.RemoteAddr = "198.51.100.49:41000"
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), tc.want)
		}
	}

	off := newAlertStack(t, server.MemoryRepositories(), enc, server.Options{OpsAlerts: operator.AlertConfig{Token: alertToken}})
	if w := off.do(http.MethodPost, operator.AlertPath, alertPayload(t, "firing", "api-1"), auth); w.Code != http.StatusNotFound {
		t.Errorf("without operator organization: %d %s, want 404", w.Code, w.Body.String())
	}
}
