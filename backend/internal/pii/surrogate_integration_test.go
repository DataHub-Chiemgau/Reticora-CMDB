package pii_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type captured struct{ envs []*events.Envelope }

func (c *captured) Consume(_ context.Context, env *events.Envelope) error {
	c.envs = append(c.envs, env)
	return nil
}

// TestHistoryKeepsSurrogates covers WP-241 (SEC-05, AUD-04) against
// PostgreSQL: audit changes, the CI change history and event data store
// personal values only as vault surrogates, the audit API, the CI history
// and the in-process event consumers resolve them; existing plaintext
// entries are sealed by the backfill with the hash chain intact
// (audit-verify); a deleted vault entry, or the deleted person a value
// belongs to, leaves no personal data in the history.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHistoryKeepsSurrogates(t *testing.T) {
	f := scopetest.Seed(t, "a1")
	bg := context.Background()
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
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	inTx := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if terr := database.WithRequestTenant(ctx, f.App, f.OrgA, fn); terr != nil {
			t.Fatal(terr)
		}
	}
	plaintext := func(query string, args ...any) bool {
		t.Helper()
		var found bool
		if qerr := f.Admin.QueryRow(bg, `SELECT EXISTS (`+query+`)`, args...).Scan(&found); qerr != nil {
			t.Fatal(qerr)
		}
		return found
	}
	verify := func() {
		t.Helper()
		res, verr := audit.Verify(bg, f.App, f.OrgA)
		if verr != nil || !res.Intact {
			t.Fatalf("audit chain = %+v, %v", res, verr)
		}
	}

	// Audit: written sealed, listed resolved.
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, ActorID: f.User, ActorType: "user",
			Action: "contact.updated", ResourceType: "contact",
			Changes: map[string]interface{}{"after": map[string]any{"email": "erika@example.org", "display_name": "Erika M", "role": "admin"}}})
		return rerr
	})
	if plaintext(`SELECT 1 FROM audit_log WHERE organization_id = $1 AND changes::text ~ '(erika@example|Erika M)'`, f.OrgA) {
		t.Fatal("audit_log holds plaintext personal data")
	}
	mux := chi.NewRouter()
	audit.NewHandler(f.App).RegisterRoutes(mux)
	listAudit := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit?action=contact.updated", nil).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("list audit: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if body := listAudit(); !strings.Contains(body, "erika@example.org") || !strings.Contains(body, `"role":"admin"`) {
		t.Fatalf("audit API does not resolve the surrogates: %s", body)
	}

	// CI history: a personal attribute is sealed, the history resolves it.
	ciID := f.CI(t, f.OrgA, f.Client1, "wp241-ci")
	repo := ci.NewPGRepository(f.App)
	if _, err = repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{Attributes: map[string]any{"contact_email": "max@example.org"}}); err != nil {
		t.Fatalf("update ci: %v", err)
	}
	if plaintext(`SELECT 1 FROM ci_change WHERE ci_id = $1 AND (old_value::text || new_value::text) ~ 'max@example'`, ciID) {
		t.Fatal("ci_change holds plaintext personal data")
	}
	history, _, err := repo.ListChanges(ctx, f.OrgA, ciID, api.PaginationParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(history); !strings.Contains(string(raw), "max@example.org") {
		t.Fatalf("ci history does not resolve the surrogate: %s", raw)
	}

	// Events: the outbox keeps the surrogate, the consumers get the value.
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.InvitationCreated,
			EntityID: f.User, Data: map[string]any{"email": "new@example.org", "expires_at": "2026-12-01"}})
		return rerr
	})
	if plaintext(`SELECT 1 FROM event_outbox WHERE organization_id = $1 AND data::text ~ 'new@example'`, f.OrgA) {
		t.Fatal("event_outbox holds plaintext personal data")
	}
	consumer := &captured{}
	if _, err = events.NewPublisher(f.App, nil, consumer).PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	delivered := false
	for _, env := range consumer.envs {
		if env.OrgID == f.OrgA && env.Type == events.InvitationCreated {
			delivered = strings.Contains(string(env.Data), "new@example.org")
		}
	}
	if !delivered {
		t.Fatal("the consumer did not get the resolved personal value")
	}
	verify()

	// Backfill: plaintext written before migration 000125.
	if _, err = f.Admin.Exec(bg, `INSERT INTO audit_log (organization_id, timestamp, actor_type, action, resource_type, changes, hash)
		VALUES ($1, now(), 'system', 'user.updated', 'user', '{"email": "old@example.org", "locale": "de"}', '')`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, ActorType: "system",
			Action: "wp241.after", ResourceType: "organization"})
		return rerr
	})
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, old_value, new_value)
		VALUES ($1, $2, 'update', 'attributes.contact_phone', '"+49 1"', '"+49 2"')`, f.OrgA, ciID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO event_outbox (organization_id, sequence, type, entity_id, entity_seq, data)
		VALUES ($1, 0, 'invitation.created', NULL, NULL, '{"email": "legacy@example.org"}')`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	verify()
	res, err := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Audit != 1 || res.CIChanges != 1 || res.Events != 1 || res.Rechained < 2 {
		t.Fatalf("backfill = %+v", res)
	}
	if plaintext(`SELECT 1 FROM audit_log WHERE organization_id = $1 AND changes::text ~ 'old@example'`, f.OrgA) ||
		plaintext(`SELECT 1 FROM ci_change WHERE ci_id = $1 AND (old_value::text || new_value::text) ~ '\+49'`, ciID) ||
		plaintext(`SELECT 1 FROM event_outbox WHERE organization_id = $1 AND data::text ~ 'legacy@example'`, f.OrgA) {
		t.Fatal("plaintext left after the backfill")
	}
	verify()
	if again, aerr := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA); aerr != nil || again.Total() != 0 {
		t.Fatalf("second backfill = %+v, %v", again, aerr)
	}

	// A deleted vault entry shows nothing anymore.
	var ref string
	if err = f.Admin.QueryRow(bg, `SELECT changes -> 'after' -> 'email' ->> '$pii' FROM audit_log
		WHERE organization_id = $1 AND action = 'contact.updated'`, f.OrgA).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if err = vault.Delete(ctx, f.OrgA, ref); err != nil {
		t.Fatal(err)
	}
	if body := listAudit(); strings.Contains(body, "erika@example.org") || !strings.Contains(body, "Erika M") {
		t.Fatalf("after deleting the e-mail's entry: %s", body)
	}
	verify()

	// Values written for a person go with the person.
	person, err := vault.Create(ctx, f.OrgA, pii.KindContact, pii.Data{"name": "Erika M"})
	if err != nil {
		t.Fatal(err)
	}
	inTx(func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := audit.NewPGRecorder().Record(pii.WithAnchor(ctx, person), tx, audit.Entry{OrganizationID: f.OrgA,
			ActorType: "system", Action: "contact.renamed", ResourceType: "contact",
			Changes: map[string]interface{}{"display_name": "Erika Musterfrau"}})
		return rerr
	})
	if err = vault.Delete(ctx, f.OrgA, person); err != nil {
		t.Fatal(err)
	}
	var left int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE parent_id = $1`, person).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d values of the deleted person left (%v)", left, err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit?action=contact.renamed", nil).WithContext(ctx))
	if strings.Contains(w.Body.String(), "Musterfrau") {
		t.Fatalf("the deleted person's value is still shown: %s", w.Body.String())
	}
}
