package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// The finding model of phase 3 (SEC-02, EVT-03, RBA-05, API-06, WP-1116,
// migration 000315_finding_extend): origin, CVE fields, the upsert on the
// fingerprint, the status transitions and the finding.* events. The
// repository cases run against PostgreSQL (the restricted runtime login)
// and the MemoryRepository alike; events, legacy rows and routes need the
// database.

type findingEnv struct {
	name  string
	repo  security.Repository
	ctx   context.Context
	orgID string
	ciID  string
	f     *scopetest.Fixture
}

const findingTag = "1d"

func pgFindingEnv(t *testing.T) *findingEnv {
	t.Helper()
	f := scopetest.Seed(t, findingTag)
	return &findingEnv{name: "postgres", repo: security.NewPGRepository(f.App), ctx: f.OrgCtx(f.OrgA), orgID: f.OrgA,
		ciID: f.CI(t, f.OrgA, f.Client1, "finding-ci"), f: f}
}

// eachRepo runs fn against the MemoryRepository and PostgreSQL.
func eachRepo(t *testing.T, fn func(t *testing.T, e *findingEnv)) {
	t.Run("memory", func(t *testing.T) {
		fn(t, &findingEnv{name: "memory", repo: security.NewMemoryRepository(), ctx: context.Background(),
			orgID: "org-memory", ciID: "00000000-0000-4000-8000-0000000000c1"})
	})
	t.Run("postgres", func(t *testing.T) { fn(t, pgFindingEnv(t)) })
}

func (e *findingEnv) report(t *testing.T, severity string) (*security.Finding, security.UpsertResult) {
	t.Helper()
	score := 7.5
	f := &security.Finding{OrganizationID: e.orgID, CIID: e.ciID, Kind: "vulnerability", Severity: severity,
		Title: "OpenSSL CVE-2024-0001", PackageName: "openssl", InstalledVersion: "3.0.2", FixedVersion: "3.0.7",
		Source: security.SourceCVEFeed, AdvisoryID: "BSI-2024-0001", CVEIDs: []string{"CVE-2024-0001"}, CVSSScore: &score}
	res, err := e.repo.Upsert(e.ctx, f)
	if err != nil {
		t.Fatalf("%s: upsert: %v", e.name, err)
	}
	return f, res
}

func (e *findingEnv) setStatus(t *testing.T, id, status string, reason *string) (*security.Finding, error) {
	t.Helper()
	return e.repo.Update(e.ctx, e.orgID, id, security.UpdateFindingRequest{Status: &status, StatusReason: reason})
}

func ptr(s string) *string { return &s }

func TestFindingUpsertIsIdempotent(t *testing.T) {
	eachRepo(t, func(t *testing.T, e *findingEnv) {
		first, res := e.report(t, "high")
		if !res.Created || res.EventType() != events.FindingCreated || first.Status != security.StatusOpen ||
			first.Source != security.SourceCVEFeed || len(first.Fingerprint) != 64 {
			t.Fatalf("first report: %+v, %+v", res, first)
		}
		time.Sleep(10 * time.Millisecond)
		second, res := e.report(t, "high")
		if res.Created || res.EventType() != "" {
			t.Errorf("same report again: %+v, want no change and no event", res)
		}
		if second.ID != first.ID || !second.LastSeenAt.After(first.LastSeenAt) || !second.UpdatedAt.Equal(first.UpdatedAt) ||
			second.Status != first.Status || second.Severity != first.Severity || second.Title != first.Title ||
			!slices.Equal(second.CVEIDs, first.CVEIDs) {
			t.Errorf("same report again changed more than last_seen_at:\n first %+v\nsecond %+v", first, second)
		}
		_, total, err := e.repo.List(e.ctx, e.orgID, &security.FilterParams{CIID: e.ciID}, api.PaginationParams{Limit: 10})
		if err != nil || total != 1 {
			t.Errorf("findings of the CI: %d, %v; want one", total, err)
		}
		// A changed severity is an update of the same finding.
		third, res := e.report(t, "critical")
		if res.Created || !res.SeverityChanged || res.EventType() != events.FindingChanged || third.ID != first.ID {
			t.Errorf("new severity: %+v, %+v", res, third)
		}
		// Filters source and cve.
		for name, tc := range map[string]struct {
			f    security.FilterParams
			want int
		}{
			"source":       {security.FilterParams{Source: security.SourceCVEFeed}, 1},
			"other source": {security.FilterParams{Source: security.SourceAgent}, 0},
			"cve":          {security.FilterParams{CVE: "CVE-2024-0001"}, 1},
			"other cve":    {security.FilterParams{CVE: "CVE-2024-9999"}, 0},
		} {
			if _, total, err := e.repo.List(e.ctx, e.orgID, &tc.f, api.PaginationParams{Limit: 10}); err != nil || total != tc.want {
				t.Errorf("filter %s: %d, %v; want %d", name, total, err, tc.want)
			}
		}
	})
}

func TestFindingReappearsAfterResolve(t *testing.T) {
	eachRepo(t, func(t *testing.T, e *findingEnv) {
		f, _ := e.report(t, "high")
		resolved, err := e.setStatus(t, f.ID, security.StatusResolved, nil)
		if err != nil || resolved.ResolvedAt == nil {
			t.Fatalf("resolve: %+v, %v", resolved, err)
		}
		again, res := e.report(t, "high")
		if !res.Reappeared || res.EventType() != events.FindingChanged || again.ID != f.ID ||
			again.Status != security.StatusOpen || again.StatusReason != security.ReasonReappeared || again.ResolvedAt != nil {
			t.Errorf("report after resolve: %+v, %+v; want the same finding open again (reappeared)", res, again)
		}
	})
}

func TestFindingFalsePositiveStaysOnUpsert(t *testing.T) {
	eachRepo(t, func(t *testing.T, e *findingEnv) {
		f, _ := e.report(t, "high")
		if _, err := e.setStatus(t, f.ID, security.StatusFalsePositive, ptr("vendor backport")); err != nil {
			t.Fatal(err)
		}
		again, res := e.report(t, "high")
		if res.Reappeared || again.Status != security.StatusFalsePositive || again.StatusReason != "vendor backport" || again.ResolvedAt != nil {
			t.Errorf("false positive after a report: %+v, %+v", res, again)
		}
		if _, err := e.setStatus(t, f.ID, security.StatusOpen, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.setStatus(t, f.ID, security.StatusAcknowledged, nil); err != nil {
			t.Fatal(err)
		}
		if again, _ = e.report(t, "high"); again.Status != security.StatusAcknowledged {
			t.Errorf("acknowledged after a report: %s", again.Status)
		}
	})
}

// fieldOf returns the field and status of a field error, else "".
func fieldOf(err error) (string, int) {
	var fe *api.FieldError
	if errors.As(err, &fe) {
		return fe.Field, fe.Status
	}
	return "", 0
}

func TestFindingStatusTransitions(t *testing.T) {
	allowed := map[string][]string{
		security.StatusOpen:          {security.StatusAcknowledged, security.StatusFalsePositive, security.StatusResolved},
		security.StatusAcknowledged:  {security.StatusOpen, security.StatusFalsePositive, security.StatusResolved},
		security.StatusFalsePositive: {security.StatusOpen},
		security.StatusResolved:      {security.StatusOpen},
	}
	// path leads from open to each status.
	path := map[string][]string{
		security.StatusOpen:          nil,
		security.StatusAcknowledged:  {security.StatusAcknowledged},
		security.StatusFalsePositive: {security.StatusFalsePositive},
		security.StatusResolved:      {security.StatusResolved},
	}
	statuses := []string{security.StatusOpen, security.StatusAcknowledged, security.StatusFalsePositive, security.StatusResolved}
	eachRepo(t, func(t *testing.T, e *findingEnv) {
		n := 0
		for _, from := range statuses {
			for _, to := range statuses {
				n++
				f := &security.Finding{OrganizationID: e.orgID, CIID: e.ciID, Kind: "missing_patch", Title: fmt.Sprintf("transition %d", n)}
				if _, err := e.repo.Upsert(e.ctx, f); err != nil {
					t.Fatal(err)
				}
				for _, step := range path[from] {
					if _, err := e.setStatus(t, f.ID, step, ptr("setup")); err != nil {
						t.Fatalf("setup %s: %v", step, err)
					}
				}
				got, err := e.setStatus(t, f.ID, to, ptr("reason"))
				if !slices.Contains(allowed[from], to) {
					if field, status := fieldOf(err); field != "status" || status != 422 {
						t.Errorf("%s -> %s: %v, want 422 on status", from, to, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s -> %s: %v", from, to, err)
					continue
				}
				if got.Status != to || (to == security.StatusResolved) != (got.ResolvedAt != nil) || got.StatusReason != "reason" {
					t.Errorf("%s -> %s: status %s, resolved_at %v, reason %q", from, to, got.Status, got.ResolvedAt, got.StatusReason)
				}
			}
		}
	})
}

func TestFindingFalsePositiveNeedsReason(t *testing.T) {
	eachRepo(t, func(t *testing.T, e *findingEnv) {
		f, _ := e.report(t, "high")
		for name, reason := range map[string]*string{"none": nil, "blank": ptr("  "), "too long": ptr(strings.Repeat("x", 1001))} {
			if _, err := e.setStatus(t, f.ID, security.StatusFalsePositive, reason); err == nil {
				t.Errorf("false_positive with reason %s accepted", name)
			} else if field, status := fieldOf(err); field != "status_reason" || status != 422 {
				t.Errorf("false_positive with reason %s: %v, want 422 on status_reason", name, err)
			}
		}
		got, err := e.setStatus(t, f.ID, security.StatusFalsePositive, ptr(strings.Repeat("x", 1000)))
		if err != nil || got.Status != security.StatusFalsePositive || got.ResolvedAt != nil {
			t.Errorf("false_positive with a reason of 1000 characters: %+v, %v; want no resolved_at", got, err)
		}
	})
}

// outboxEvents returns type and data of the events of an entity.
func outboxEvents(t *testing.T, f *scopetest.Fixture, entity string) ([]string, []map[string]any) {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT type, data FROM event_outbox WHERE entity_id = $1 ORDER BY sequence`, entity)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	var data []map[string]any
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		types, data = append(types, typ), append(data, d)
	}
	return types, data
}

func TestFindingEventsInOutbox(t *testing.T) {
	e := pgFindingEnv(t)
	f, _ := e.report(t, "high")
	if _, err := e.setStatus(t, f.ID, security.StatusAcknowledged, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setStatus(t, f.ID, security.StatusResolved, nil); err != nil {
		t.Fatal(err)
	}
	e.report(t, "high") // reappears
	e.report(t, "high") // unchanged: no event
	types, data := outboxEvents(t, e.f, f.ID)
	want := []string{events.FindingCreated, events.FindingChanged, events.FindingResolved, events.FindingChanged}
	if !slices.Equal(types, want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	first := data[0]
	if first["id"] != f.ID || first["ci_id"] != e.ciID || first["client_id"] != e.f.Client1 || first["severity"] != "high" ||
		first["status"] != "open" || first["source"] != "cve_feed" || fmt.Sprint(first["cve_ids"]) != "[CVE-2024-0001]" {
		t.Errorf("finding.created data %v", first)
	}
	if data[2]["status"] != "resolved" || data[3]["status"] != "open" {
		t.Errorf("statuses of the events: %v, %v", data[2]["status"], data[3]["status"])
	}

	// A refused event rolls the change back.
	bg := context.Background()
	fn := fmt.Sprintf("wp1116_refuse_%d", time.Now().UnixNano())
	if _, err := e.f.Admin.Exec(bg, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.type LIKE 'finding.%' AND NEW.organization_id = '`+e.orgID+`' THEN
		RAISE EXCEPTION 'wp1116: the outbox refuses %', NEW.type;
	END IF;
	RETURN NEW;
END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`) })
	if _, err := e.f.Admin.Exec(bg, `CREATE TRIGGER `+fn+` BEFORE INSERT ON event_outbox FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setStatus(t, f.ID, security.StatusAcknowledged, nil); err == nil {
		t.Error("status change committed although its event was refused")
	}
	got, err := e.repo.GetByID(e.ctx, e.orgID, f.ID)
	if err != nil || got.Status != security.StatusOpen {
		t.Errorf("status after the refused event: %v, %v; want open (rolled back)", got, err)
	}
	refused := &security.Finding{OrganizationID: e.orgID, CIID: e.ciID, Kind: "vulnerability", Title: "refused", Reference: "CVE-2024-7777"}
	if _, err := e.repo.Upsert(e.ctx, refused); err == nil {
		t.Error("new finding committed although its event was refused")
	}
	var n int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM security_finding WHERE organization_id = $1 AND title = 'refused'`, e.orgID).Scan(&n); err != nil || n != 0 {
		t.Errorf("refused finding rows: %d, %v", n, err)
	}
}

func TestFindingLegacyRowsKeepUniqueFingerprint(t *testing.T) {
	e := pgFindingEnv(t)
	bg := context.Background()
	// Rows written without fingerprint (as before 000315) get one of
	// their own; the migration gave the existing rows legacy:<id>.
	var a, b string
	for _, p := range []*string{&a, &b} {
		if err := e.f.Admin.QueryRow(bg, `INSERT INTO security_finding (organization_id, ci_id, kind, title) VALUES ($1, $2, 'vulnerability', 'legacy')
			RETURNING fingerprint`, e.orgID, e.ciID).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	if a == "" || a == b {
		t.Errorf("fingerprints of two raw rows: %q, %q; want two distinct", a, b)
	}
	var legacy string
	if err := e.f.Admin.QueryRow(bg, `UPDATE security_finding SET fingerprint = 'legacy:' || id::text WHERE fingerprint = $1 RETURNING fingerprint`, a).
		Scan(&legacy); err != nil || !strings.HasPrefix(legacy, "legacy:") {
		t.Fatalf("legacy fingerprint: %q, %v", legacy, err)
	}
	// An upsert with the same content keys by its own fingerprint and adds
	// a row instead of hitting the legacy one.
	f := &security.Finding{OrganizationID: e.orgID, CIID: e.ciID, Kind: "vulnerability", Title: "legacy"}
	res, err := e.repo.Upsert(e.ctx, f)
	if err != nil || !res.Created || strings.HasPrefix(f.Fingerprint, "legacy:") {
		t.Errorf("upsert next to legacy rows: %+v, %v, %q", res, err, f.Fingerprint)
	}
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO security_finding (organization_id, kind, title, fingerprint) VALUES ($1, 'vulnerability', 'dup', $2)`,
		e.orgID, legacy); err == nil {
		t.Error("a second row with a taken fingerprint was accepted")
	}
	var nullable string
	if err := e.f.Admin.QueryRow(bg, `SELECT is_nullable FROM information_schema.columns WHERE table_name = 'security_finding' AND column_name = 'fingerprint'`).
		Scan(&nullable); err != nil || nullable != "NO" {
		t.Errorf("fingerprint nullable %q, %v", nullable, err)
	}
}

// findingRouter is the router of the server with the PostgreSQL
// repositories of the fixture.
func findingRouter(t *testing.T, f *scopetest.Fixture) http.Handler {
	t.Helper()
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	policy := egress.Options{AllowPrivate: true}
	mux, _, err := server.NewRouter(repos, server.Options{
		Credentials: credential.NewService(repos.Credential, encryptor), Egress: policy,
		Dispatcher: webhook.NewDispatcher(repos.Webhook, egress.NewClient(policy), webhook.DispatcherOptions{
			Deliveries: repos.WebhookDeliveries, PollInterval: -1}),
		Version: "wp1116", Entitlements: entitlements,
		CIService: ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		OIDC:      identity.NewOIDCProvider(identity.OIDCConfig{}), AuditPool: f.App, Audit: audit.NewHandler(f.App),
		Events: events.NewPGEmitter(f.App),
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return middleware.RequestID(middleware.TenantMiddleware(mux))
}

func call(t *testing.T, h http.Handler, orgID, userID string, perms []identity.Permission, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: userID, OrganizationID: orgID, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "wp1116"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	return w.Code, w.Body.Bytes()
}

func TestFindingRoutesNeedFindingPermissions(t *testing.T) {
	e := pgFindingEnv(t)
	h := findingRouter(t, e.f)
	user := e.f.AppUser(t, e.orgID, "wp1116")
	reader := []identity.Permission{identity.PermFindingRead}
	writer := []identity.Permission{identity.PermFindingRead, identity.PermFindingWrite}
	security := []identity.Permission{"security:read", "security:write"}

	code, body := call(t, h, e.orgID, user, writer, http.MethodPost, "/api/v1/security/findings",
		`{"ci_id":"`+e.ciID+`","kind":"vulnerability","severity":"high","title":"route finding","reference":"CVE-2024-4242"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST with finding:write = %d %s", code, body)
	}
	var created struct {
		ID     string   `json:"id"`
		Source string   `json:"source"`
		CVEIDs []string `json:"cve_ids"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Source != "manual" || created.CVEIDs == nil {
		t.Fatalf("created %s: %v", body, err)
	}
	if code, body = call(t, h, e.orgID, user, writer, http.MethodPost, "/api/v1/security/findings",
		`{"ci_id":"`+e.ciID+`","kind":"vulnerability","severity":"high","title":"route finding","reference":"CVE-2024-4242"}`); code != http.StatusOK {
		t.Errorf("POST of the same finding = %d %s, want 200 (upsert)", code, body)
	}
	// The CI id in upper case names the same CI: same fingerprint, no second finding.
	if code, body = call(t, h, e.orgID, user, writer, http.MethodPost, "/api/v1/security/findings",
		`{"ci_id":"`+strings.ToUpper(e.ciID)+`","kind":"vulnerability","severity":"high","title":"route finding","reference":"CVE-2024-4242"}`); code != http.StatusOK {
		t.Errorf("POST with the CI id in upper case = %d %s, want 200 (same finding)", code, body)
	}
	for _, c := range []struct {
		perms  []identity.Permission
		method string
		path   string
		body   string
		want   int
	}{
		{reader, http.MethodGet, "/api/v1/security/findings?source=manual&cve=CVE-2024-4242", "", http.StatusOK},
		{reader, http.MethodGet, "/api/v1/security/findings/" + created.ID, "", http.StatusOK},
		{reader, http.MethodGet, "/api/v1/security/findings/summary", "", http.StatusOK},
		{reader, http.MethodPatch, "/api/v1/security/findings/" + created.ID, `{"status":"acknowledged"}`, http.StatusForbidden},
		{reader, http.MethodPost, "/api/v1/security/findings", `{"kind":"vulnerability","title":"x"}`, http.StatusForbidden},
		{security, http.MethodGet, "/api/v1/security/findings", "", http.StatusForbidden},
		{security, http.MethodPatch, "/api/v1/security/findings/" + created.ID, `{"status":"acknowledged"}`, http.StatusForbidden},
		{writer, http.MethodPatch, "/api/v1/security/findings/" + created.ID, `{"status":"false_positive"}`, http.StatusUnprocessableEntity},
		{writer, http.MethodPatch, "/api/v1/security/findings/" + created.ID, `{"status":"open"}`, http.StatusUnprocessableEntity},
		{writer, http.MethodPatch, "/api/v1/security/findings/" + created.ID,
			`{"status":"false_positive","status_reason":"compensating control"}`, http.StatusOK},
	} {
		code, body := call(t, h, e.orgID, user, c.perms, c.method, c.path, c.body)
		if code != c.want {
			t.Errorf("%s %s %s with %v = %d %s, want %d", c.method, c.path, c.body, c.perms, code, body, c.want)
		}
		if c.want == http.StatusUnprocessableEntity {
			var p struct {
				Field string `json:"field"`
			}
			if err := json.Unmarshal(body, &p); err != nil || (p.Field != "status" && p.Field != "status_reason") {
				t.Errorf("422 problem %s: field %q", body, p.Field)
			}
		}
	}
	// The audit snapshot of the PATCH holds status and reason.
	var changes string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT changes::text FROM audit_log WHERE organization_id = $1 AND resource_type = 'security'
		AND resource_id = $2 AND changes->>'route' LIKE 'PATCH %' ORDER BY seq DESC LIMIT 1`, e.orgID, created.ID).Scan(&changes); err != nil {
		t.Fatalf("audit entry of the PATCH: %v", err)
	}
	if !strings.Contains(changes, "false_positive") || !strings.Contains(changes, "compensating control") {
		t.Errorf("audit snapshot %s lacks status and reason", changes)
	}
}

// TestFindingAgentUpsertNoDuplicate drives the agent path itself: the same
// inventory telemetry three times through agent.Handler with the
// PostgreSQL finding recorder gives one agent finding per vulnerable
// package; a CVE vendor marker fills reference and cve_ids. A later report
// of the CVE on an upgraded package updates package and version of the
// same finding.
func TestFindingAgentUpsertNoDuplicate(t *testing.T) {
	e := pgFindingEnv(t)
	repo := security.NewPGRepository(e.f.App)
	agents := agent.NewPGRepository(e.f.App)
	ctx := tenant.WithTenant(e.ctx, tenant.TenantInfo{OrganizationID: e.orgID})
	const host, agentID = "wp1116-laptop", "wp1116-agent"
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET hostname = $2 WHERE id = $1`, e.ciID, host); err != nil {
		t.Fatal(err)
	}
	if err := agents.Register(ctx, &agent.Agent{OrganizationID: e.orgID, AgentID: agentID, Hostname: host, Status: "online",
		Policy: agent.DefaultPolicy()}); err != nil {
		t.Fatal(err)
	}
	if err := agents.SetCI(ctx, e.orgID, agentID, e.ciID); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	agent.NewHandler(agents, nil, ci.NewPGRepository(e.f.App), discovery.NewPGRepository(e.f.App)).
		WithFindings(repo).RegisterRoutes(mux)
	telemetry := func(software []agent.SoftwareItem) {
		t.Helper()
		body, err := json.Marshal(agent.TelemetryPayload{AgentID: agentID, Hostname: host, OS: "windows", Version: "1.0", Software: software})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/telemetry", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("telemetry: %d %s", w.Code, w.Body.String())
		}
	}
	inventory := []agent.SoftwareItem{
		{Name: "openssl", Version: "1.0", Vendor: "cve-2024-0001"},
		{Name: "zlib", Version: "1.2", Vendor: "vuln"},
		{Name: "curl", Version: "8.0", Vendor: "vuln"},
		{Name: "notepad", Version: "1.0", Vendor: "Example Corp"},
	}
	for range 3 {
		telemetry(inventory)
	}
	agentFindings := func() map[string]security.Finding {
		t.Helper()
		items, total, err := repo.List(e.ctx, e.orgID, &security.FilterParams{Source: security.SourceAgent}, api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]security.Finding{}
		for _, f := range items {
			out[f.PackageName] = f
		}
		if total != len(out) {
			t.Errorf("agent findings: %d rows for %d packages", total, len(out))
		}
		return out
	}
	got := agentFindings()
	if len(got) != 3 {
		t.Fatalf("agent findings after three reports: %v; want openssl, zlib and curl once each", got)
	}
	for name, f := range got {
		if f.Source != security.SourceAgent || f.CIID != e.ciID || f.Status != security.StatusOpen {
			t.Errorf("%s: source %s, ci %s, status %s", name, f.Source, f.CIID, f.Status)
		}
	}
	if f := got["openssl"]; f.Reference != "CVE-2024-0001" || !slices.Equal(f.CVEIDs, []string{"CVE-2024-0001"}) {
		t.Errorf("openssl: reference %q, cve_ids %v; want CVE-2024-0001", f.Reference, f.CVEIDs)
	}
	if f := got["zlib"]; f.Reference != "" || len(f.CVEIDs) != 0 {
		t.Errorf("zlib: reference %q, cve_ids %v; want none", f.Reference, f.CVEIDs)
	}
	// The same CVE on the upgraded package is the same finding with the new
	// package version (title, package and version agree).
	inventory[0].Version = "1.1"
	telemetry(inventory)
	got = agentFindings()
	if f := got["openssl"]; len(got) != 3 || f.InstalledVersion != "1.1" || !strings.Contains(f.Title, "1.1") {
		t.Errorf("after the upgrade: %d findings, openssl %q %q; want version 1.1 in package and title", len(got), f.InstalledVersion, f.Title)
	}
}
