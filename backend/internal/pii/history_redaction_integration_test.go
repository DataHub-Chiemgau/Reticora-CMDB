package pii_test

// SEC-05 [P3] closure (WP-1159, CH16): historical data never shows the
// personal data of a person once it is erased, and nothing of a purged
// organization can be resolved. After privacy.subject_erase (mode shred,
// WP-1158) of a contact, every history source shows no plaintext over its
// read path: the audit changes (GET /api/v1/audit-log), the CI history
// (GET /api/v1/history/ci/{id}, the value null), the notification
// deliveries (GET /api/v1/notification-deliveries, address empty) and the
// event outbox (null). The outbox has no read API: its only reader is the
// publisher, which resolves the stored references with pii.RevealTx
// (events.publisher), so the test reads it the same way. Every source is
// checked to hold the value before the erasure. The holder of an ADMP
// connection (WP-1076) is NULL and its holder route 404. The hash chain of
// audit_log stays verifiable (audit-verify).
// After the purge of an organization (WP-1154, WP-1155) no surrogate of the
// organization resolves and org_dek holds no row of it.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestShreddedPersonInvisibleInHistory|TestPurgedOrganizationUnresolvable' ./internal/pii/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

const (
	erasedName  = "Hedwig Redaktion"
	erasedEmail = "hedwig.redaktion@example.org"
	erasedPhone = "+49 89 1159"
)

// redactionEnv is an organization with a vault of its own master key.
type redactionEnv struct {
	f     *scopetest.Fixture
	vault *pii.Vault
	admin string
	ctx   context.Context
}

func newRedactionEnv(t *testing.T) *redactionEnv {
	t.Helper()
	f := scopetest.Seed(t, "b6")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	vault := pii.NewVault(f.App, enc)
	pii.SetDefault(vault)
	t.Cleanup(func() { pii.SetDefault(nil) })
	admin := f.AppUser(t, f.OrgA, "wp1159-admin")
	scope := database.OrgWideScope(f.OrgA, admin)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
	return &redactionEnv{f: f, vault: vault, admin: admin, ctx: ctx}
}

func (e *redactionEnv) inTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := database.WithRequestTenant(e.ctx, e.f.App, e.f.OrgA, fn); err != nil {
		t.Fatal(err)
	}
}

// revealed resolves stored JSON in the tenant like the read paths do.
func (e *redactionEnv) revealed(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
			return err
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		shown, err := pii.RevealTx(ctx, tx, e.f.OrgA, doc)
		if err != nil {
			return err
		}
		b, err := json.Marshal(shown)
		out = string(b)
		return err
	})
	return out
}

// plain reports whether text holds a value of the erased person.
func plain(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "hedwig") || strings.Contains(lower, "redaktion") || strings.Contains(lower, "1159")
}

// auditListed reads the audit entries of the organization over GET
// /api/v1/audit-log as an auditor sees them.
func (e *redactionEnv) auditListed(t *testing.T) string {
	t.Helper()
	mux := chi.NewRouter()
	audit.NewHandler(e.f.App).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?limit=200", nil).WithContext(e.ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /audit-log: %d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// apiContext is the context of an org-wide reader of the history and the
// deliveries (ci:read, notification:manage) without field restrictions.
func (e *redactionEnv) apiContext() context.Context {
	p := identity.Principal{Subject: e.admin, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermCIRead, identity.PermNotificationManage}}
	ctx := identity.WithPermissions(identity.WithPrincipal(e.ctx, p), p.Permissions)
	return permission.WithPolicy(ctx, permission.UnrestrictedPolicy(nil))
}

// historyListed reads the CI history over GET /api/v1/history/ci/{id}.
func (e *redactionEnv) historyListed(t *testing.T, ciID string) string {
	t.Helper()
	mux := chi.NewRouter()
	history.NewHandler(history.NewPGRepository(e.f.App)).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/history/ci/"+ciID+"?limit=200", nil).WithContext(e.apiContext()))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /history/ci: %d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// deliveriesListed reads the deliveries over GET
// /api/v1/notification-deliveries and returns the one with id.
func (e *redactionEnv) deliveriesListed(t *testing.T, id string) string {
	t.Helper()
	mux := chi.NewRouter()
	notify.NewHandler(&notify.Center{Deliveries: notify.NewPGDeliveryStore(e.f.App)}).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/notification-deliveries?limit=200", nil).WithContext(e.apiContext()))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /notification-deliveries: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Data {
		var d struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(item, &d) == nil && d.ID == id {
			return string(item)
		}
	}
	t.Fatalf("delivery %s not in the list: %s", id, w.Body.String())
	return ""
}

// holderConnection seeds an ADMP connection whose holder is a person
// subject (WP-1076) and returns its id and the holder subject.
func (e *redactionEnv) holderConnection(t *testing.T) (connection, holder string) {
	t.Helper()
	credentialID := e.f.ID()
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO credential (id, organization_id, name, kind, ciphertext)
		VALUES ($1, $2, 'wp1159-token', 'api_token', '\x00')`, credentialID, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	account := &admp.Account{OrganizationID: e.f.OrgA, Name: "wp1159", BaseURL: "https://admp.example.com/api",
		CredentialID: credentialID, RateLimitRPS: 2, TimeoutMS: 10000, Enabled: true}
	if err := admp.NewPGAccountRepository(e.f.App).CreateAccount(e.f.OrgCtx(e.f.OrgA), account); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: account.ID, ExternalID: "wp1159-4711", ExpiresAt: now.Add(time.Hour),
		FetchedAt: now, HoldersKnown: true,
		Holders: []admp.Holder{{Type: "person", FirstName: "Hedwig", LastName: "Redaktion", Street: "Redaktionsweg", HouseNumber: "1159"}}}
	if err := admp.NewPGRepository(e.f.App).UpsertConnection(e.f.OrgCtx(e.f.OrgA), c); err != nil {
		t.Fatal(err)
	}
	if c.HolderSubjectID == "" {
		t.Fatalf("connection without holder subject: %+v", c)
	}
	return c.ID, c.HolderSubjectID
}

// holderRoute answers GET /api/v1/admp/connections/{id}/holder as a holder
// of admp:holder_read.
func (e *redactionEnv) holderRoute(connection string) int {
	h := admp.NewDataHandler(admp.NewPGRepository(e.f.App), admp.NewPGRepository(e.f.App), nil, nil)
	const holderPath = "/api/v1/admp/connections/{id}/holder"
	mux := chi.NewRouter()
	mux.With(server.AuthorizeRoute(http.MethodGet, holderPath)).Get(holderPath, func(w http.ResponseWriter, r *http.Request) {
		inner := chi.NewRouter()
		h.RegisterRoutes(inner)
		inner.ServeHTTP(w, r)
	})
	ctx := identity.WithPrincipal(e.ctx, identity.Principal{Subject: e.admin, OrganizationID: e.f.OrgA,
		Permissions: []identity.Permission{identity.PermADMPHolderRead}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admp/connections/"+connection+"/holder", nil).WithContext(ctx))
	return w.Code
}

func TestShreddedPersonInvisibleInHistory(t *testing.T) {
	e := newRedactionEnv(t)
	bg := context.Background()
	// The contact keeps its data in the vault (AUD-01).
	c := &contact.Contact{OrganizationID: e.f.OrgA, DisplayName: erasedName, Email: erasedEmail, Phone: erasedPhone}
	if err := contact.NewPGRepository(e.f.App).Create(e.ctx, c); err != nil {
		t.Fatal(err)
	}
	// Its values in the history: an audit change, the CI history, an event
	// and a delivery.
	var auditID string
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		entry, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: e.f.OrgA, ActorID: e.admin, ActorType: "user",
			Action: "contact.updated", ResourceType: "contact", ResourceID: c.ID,
			Changes: map[string]any{"after": map[string]any{"email": erasedEmail, "display_name": erasedName, "role": "owner"}}})
		if err != nil {
			return err
		}
		auditID = entry.ID
		return nil
	})
	ciID := e.f.CI(t, e.f.OrgA, "", "wp1159-ci")
	if _, err := ci.NewPGRepository(e.f.App).Update(e.ctx, e.f.OrgA, ciID, ci.UpdateRequest{Attributes: map[string]any{"contact_email": erasedEmail}}); err != nil {
		t.Fatal(err)
	}
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: e.f.OrgA, Type: events.InvitationCreated, EntityID: ciID,
			Data: map[string]any{"email": erasedEmail, "marker": "redaction-marker"}})
		return err
	})
	var eventID, channel, deliveryID string
	if err := e.f.Admin.QueryRow(bg, `SELECT id::text FROM event_outbox WHERE organization_id = $1 AND data ->> 'marker' = 'redaction-marker'
		ORDER BY id DESC LIMIT 1`, e.f.OrgA).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO notification_channel (organization_id, name, kind, provider)
		VALUES ($1, 'wp1159', 'email', 'central_smtp') RETURNING id::text`, e.f.OrgA).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_email, recipient_name,
			recipient_key, category, subject, status)
		VALUES ($1, $2, $3, $4, 'mail:' || lower($3), 'test', 'sec05-history', 'sent') RETURNING id::text`,
		e.f.OrgA, channel, erasedEmail, erasedName).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	connection, holder := e.holderConnection(t)

	// Before: every source holds the value and its read path shows it, so
	// the checks after the erasure cannot pass on an empty source.
	if !plain(e.auditListed(t)) {
		t.Fatal("the audit list shows no value of the person before the erasure")
	}
	var sealedChanges int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND new_value::text LIKE '%"$pii"%'`,
		ciID).Scan(&sealedChanges); err != nil || sealedChanges == 0 {
		t.Fatalf("ci_change rows with a $pii reference before the erasure: %d (%v)", sealedChanges, err)
	}
	if listed := e.historyListed(t, ciID); !strings.Contains(listed, erasedEmail) {
		t.Fatalf("the CI history shows no value of the person before the erasure: %s", listed)
	}
	if shown := e.revealed(t, `SELECT data FROM event_outbox WHERE id = $1`, eventID); !strings.Contains(shown, erasedEmail) {
		t.Fatalf("the event shows no value of the person before the erasure: %s", shown)
	}
	if listed := e.deliveriesListed(t, deliveryID); !strings.Contains(listed, erasedEmail) {
		t.Fatalf("the delivery shows no address before the erasure: %s", listed)
	}
	if code := e.holderRoute(connection); code != http.StatusOK {
		t.Fatalf("holder route before the erasure: %d", code)
	}

	svc := privacy.NewSubjectService(e.f.App, user.NewPGRepository(e.f.App))
	// The report lists the values found by the name alone as candidates;
	// the erasure takes them once confirmed (WP-1158).
	report, err := svc.RunReport(e.ctx, e.f.OrgA, e.admin, &privacy.SubjectRequest{ContactID: c.ID})
	if err != nil {
		t.Fatalf("report the contact: %v", err)
	}
	var confirmed []string
	for _, finding := range report.Findings {
		if finding.Candidate && finding.Surrogate != "" {
			confirmed = append(confirmed, finding.Surrogate)
		}
	}
	if _, err = svc.RunErase(e.ctx, e.f.OrgA, e.admin, &privacy.SubjectRequest{ContactID: c.ID, Mode: privacy.ModeShred,
		Confirmed: confirmed}); err != nil {
		t.Fatalf("erase the contact: %v", err)
	}
	if _, err = svc.RunErase(e.ctx, e.f.OrgA, e.admin, &privacy.SubjectRequest{SubjectID: holder, Mode: privacy.ModeShred}); err != nil {
		t.Fatalf("erase the holder: %v", err)
	}

	// After: no source shows a value.
	if listed := e.auditListed(t); plain(listed) {
		t.Errorf("GET /audit-log still shows the person: %s", listed)
	}
	if shown := e.revealed(t, `SELECT changes FROM audit_log WHERE id = $1`, auditID); plain(shown) || !strings.Contains(shown, `"email":null`) {
		t.Errorf("audit changes after the shred: %s", shown)
	}
	if listed := e.historyListed(t, ciID); plain(listed) || !strings.Contains(listed, `"contact_email":null`) {
		t.Errorf("GET /history/ci after the shred: %s", listed)
	}
	if shown := e.revealed(t, `SELECT new_value FROM ci_change WHERE ci_id = $1 AND new_value::text LIKE '%"$pii"%'
		ORDER BY created_at DESC, id DESC LIMIT 1`, ciID); plain(shown) || !strings.Contains(shown, `"contact_email":null`) {
		t.Errorf("ci_change after the shred: %s", shown)
	}
	if shown := e.revealed(t, `SELECT data FROM event_outbox WHERE id = $1`, eventID); plain(shown) || !strings.Contains(shown, `"email":null`) {
		t.Errorf("event after the shred: %s", shown)
	}
	var email, name *string
	var key string
	if err = e.f.Admin.QueryRow(bg, `SELECT recipient_email, recipient_name, recipient_key FROM notification_delivery WHERE id = $1`,
		deliveryID).Scan(&email, &name, &key); err != nil {
		t.Fatal(err)
	}
	if email != nil || name != nil || plain(key) {
		t.Errorf("delivery after the shred: %v %v %s", email, name, key)
	}
	if listed := e.deliveriesListed(t, deliveryID); plain(listed) {
		t.Errorf("GET /notification-deliveries after the shred: %s", listed)
	}
	var holderColumn *string
	if err = e.f.Admin.QueryRow(bg, `SELECT holder_subject_id::text FROM admp_connection WHERE id = $1`, connection).Scan(&holderColumn); err != nil {
		t.Fatal(err)
	}
	if holderColumn != nil {
		t.Errorf("admp_connection holder after the shred: %v", *holderColumn)
	}
	if code := e.holderRoute(connection); code != http.StatusNotFound {
		t.Errorf("holder route after the shred: %d, want 404", code)
	}
	// Nothing anywhere in the stored rows of the organization in plaintext.
	var rows int
	if err = e.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND changes::text ILIKE '%hedwig%'`,
		e.f.OrgA).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("%d audit entries hold the plaintext (%v)", rows, err)
	}
	// The hash chain stays verifiable (audit-verify).
	var res audit.VerifyResult
	res, err = audit.Verify(bg, e.f.App, e.f.OrgA)
	if err != nil || !res.Intact {
		t.Errorf("audit chain after the shred: %+v %v", res, err)
	}
}

func TestPurgedOrganizationUnresolvable(t *testing.T) {
	e := newRedactionEnv(t)
	bg := context.Background()
	c := &contact.Contact{OrganizationID: e.f.OrgA, DisplayName: erasedName, Email: erasedEmail, Phone: erasedPhone}
	if err := contact.NewPGRepository(e.f.App).Create(e.ctx, c); err != nil {
		t.Fatal(err)
	}
	var surrogates []string
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		entry, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: e.f.OrgA, ActorID: e.admin, ActorType: "user",
			Action: "contact.updated", ResourceType: "contact", ResourceID: c.ID,
			Changes: map[string]any{"after": map[string]any{"email": erasedEmail}}})
		if err != nil {
			return err
		}
		_ = entry
		return nil
	})
	rows, err := e.f.Admin.Query(bg, `SELECT surrogate_id::text FROM pii_subject WHERE organization_id = $1`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if surrogates, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || len(surrogates) < 2 {
		t.Fatalf("surrogates of the organization before the purge: %v (%v)", surrogates, err)
	}
	var keys int
	if err = e.f.Admin.QueryRow(bg, `SELECT count(*) FROM org_dek WHERE organization_id = $1`, e.f.OrgA).Scan(&keys); err != nil || keys == 0 {
		t.Fatalf("org_dek rows before the purge: %d (%v)", keys, err)
	}
	// The organization's grace period has ended.
	if _, err = e.f.Admin.Exec(bg, `UPDATE organization SET deletion_state = 'requested', deletion_requested_at = now() - interval '31 days',
		deletion_due_at = now() - interval '1 day', deletion_requested_by = $2 WHERE id = $1`, e.f.OrgA, e.admin); err != nil {
		t.Fatal(err)
	}
	purger := tenantapi.NewPurger(e.f.App, func(ctx context.Context, tx pgx.Tx) ([]string, error) {
		tables, terr := export.OrgPurgeTables(ctx, tx)
		if terr != nil {
			return nil, terr
		}
		names := make([]string, 0, len(tables))
		for i := range tables {
			names = append(names, tables[i].Name)
		}
		return names, nil
	}).WithExternal(&tenantapi.ExternalPurge{})
	if _, err = purger.PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err = e.f.Admin.QueryRow(bg, `SELECT count(*) FROM org_dek WHERE organization_id = $1`, e.f.OrgA).Scan(&keys); err != nil || keys != 0 {
		t.Errorf("org_dek rows after the purge: %d (%v)", keys, err)
	}
	var left int
	if err = e.f.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE organization_id = $1`, e.f.OrgA).Scan(&left); err != nil || left != 0 {
		t.Errorf("pii_subject rows after the purge: %d (%v)", left, err)
	}
	// No surrogate resolves, neither through the vault nor in a reference:
	// the reads succeed and find nothing (any error fails the test, it
	// would prove nothing).
	err = database.WithRequestTenant(e.f.OrgCtx(e.f.OrgA), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		found, rerr := e.vault.ResolveTx(ctx, tx, e.f.OrgA, surrogates)
		if rerr != nil {
			return fmt.Errorf("ResolveTx: %w", rerr)
		}
		for _, s := range surrogates {
			if found[s] != nil {
				t.Errorf("surrogate %s resolves after the purge", s)
			}
		}
		refs := make([]any, len(surrogates))
		for i, s := range surrogates {
			refs[i] = map[string]any{pii.RefKey: s}
		}
		shown, rerr := pii.RevealTx(ctx, tx, e.f.OrgA, refs)
		if rerr != nil {
			return fmt.Errorf("RevealTx: %w", rerr)
		}
		values, ok := shown.([]any)
		if !ok || len(values) != len(surrogates) {
			t.Fatalf("RevealTx returned %#v", shown)
		}
		for i, v := range values {
			if v != nil {
				t.Errorf("reference %s resolves after the purge: %v", surrogates[i], v)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("resolve after the purge: %v", err)
	}
}
