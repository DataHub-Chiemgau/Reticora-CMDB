package privacy_test

// WP-1158 (SEC-05 [P3]): report and erasure of a single person over every
// surrogate reference — the report job finds a contact's values in audit
// changes, the CI history, events and notification deliveries and a user
// as actor; the erasure shreds (references resolve to null, deliveries lose
// the address, a neighbour stays) or pseudonymizes (surrogates stay, the
// display shows the pseudonym); a person of another organization is 404 and
// the audit never holds a value. PostgreSQL integration with a vault of its
// own master key.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestSubject' ./internal/privacy/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// subjectEnv is a seeded organization with a vault, the subject service on
// the job service and its routes.
type subjectEnv struct {
	f     *scopetest.Fixture
	ctx   context.Context
	svc   *privacy.SubjectService
	jobs  *jobs.Service
	mux   *chi.Mux
	vault *pii.Vault
	// admin is a second user of organization A who runs the requests.
	admin string
}

func newSubjectEnv(t *testing.T) *subjectEnv {
	t.Helper()
	f := scopetest.Seed(t, "19")
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
	admin := f.AppUser(t, f.OrgA, "privacy-admin")
	scope := database.OrgWideScope(f.OrgA, admin)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: privacy.SubjectReportJobKind, MaxAttempts: 3, Timeout: time.Hour, Cancellable: true})
	jobSvc.Register(jobs.Kind{Name: privacy.SubjectEraseJobKind, MaxAttempts: 3, Timeout: time.Hour})
	svc := privacy.NewSubjectService(f.App, user.NewPGRepository(f.App)).WithJobs(jobSvc)
	mux := chi.NewRouter()
	privacy.NewHandler(nil).WithSubjects(svc).RegisterRoutes(mux)
	return &subjectEnv{f: f, ctx: ctx, svc: svc, jobs: jobSvc, mux: mux, vault: vault, admin: admin}
}

func (e *subjectEnv) inTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := database.WithRequestTenant(e.ctx, e.f.App, e.f.OrgA, fn); err != nil {
		t.Fatal(err)
	}
}

// post sends a request as the admin of organization A.
func (e *subjectEnv) post(t *testing.T, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	e.mux.ServeHTTP(w, req)
	return w
}

// runJob submits the request and waits for its job; it returns the result.
func (e *subjectEnv) runJob(t *testing.T, path, body string) json.RawMessage {
	t.Helper()
	w := e.post(t, e.ctx, path, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST %s %s: %d %s", path, body, w.Code, w.Body.String())
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.JobID == "" {
		t.Fatalf("answer %s: %v", w.Body.String(), err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		job, err := e.jobs.Get(e.ctx, e.f.OrgA, accepted.JobID)
		if err != nil {
			t.Fatal(err)
		}
		switch job.Status {
		case jobs.StatusSucceeded:
			return job.Result
		case jobs.StatusFailed, jobs.StatusCancelled:
			t.Fatalf("job %s: %s %s", job.Kind, job.Status, job.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", accepted.JobID)
	return nil
}

func (e *subjectEnv) report(t *testing.T, body string) privacy.SubjectReport {
	t.Helper()
	var r privacy.SubjectReport
	if err := json.Unmarshal(e.runJob(t, "/api/v1/privacy/subjects/report", body), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// contactOf creates a contact through the contact repository (its data in
// the vault) and returns its id and subject.
func (e *subjectEnv) contactOf(t *testing.T, name, email, phone string) (id, subject string) {
	t.Helper()
	c := &contact.Contact{OrganizationID: e.f.OrgA, DisplayName: name, Email: email, Phone: phone}
	if err := contact.NewPGRepository(e.f.App).Create(e.ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT pii_subject_id::text FROM contact WHERE id = $1`, c.ID).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	return c.ID, subject
}

// history writes a personal value of email into audit changes, the CI
// history, an event and a delivery; it returns the ids of audit entry, CI,
// event and delivery.
func (e *subjectEnv) history(t *testing.T, tag, email string) (auditID, ciID, eventID, deliveryID string) {
	t.Helper()
	bg := context.Background()
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		entry, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: e.f.OrgA, ActorID: e.admin, ActorType: "user",
			Action: "contact.updated", ResourceType: "contact",
			Changes: map[string]any{"after": map[string]any{"email": strings.ToUpper(email) + " ", "role": "admin"}}})
		if err != nil {
			return err
		}
		auditID = entry.ID
		return nil
	})
	ciID = e.f.CI(t, e.f.OrgA, "", "wp1158-"+tag)
	if _, err := ci.NewPGRepository(e.f.App).Update(e.ctx, e.f.OrgA, ciID, ci.UpdateRequest{Attributes: map[string]any{"contact_email": email}}); err != nil {
		t.Fatalf("update ci: %v", err)
	}
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: e.f.OrgA, Type: events.InvitationCreated,
			EntityID: ciID, Data: map[string]any{"email": email, "marker": "wp1158-" + tag}})
		return err
	})
	if err := e.f.Admin.QueryRow(bg, `SELECT id::text FROM event_outbox WHERE organization_id = $1 AND data ->> 'marker' = $2`,
		e.f.OrgA, "wp1158-"+tag).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	var channel string
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO notification_channel (organization_id, name, kind, provider)
		VALUES ($1, $2, 'email', 'central_smtp') RETURNING id::text`, e.f.OrgA, "wp1158-"+tag).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_email, recipient_name,
			recipient_key, category, subject, status)
		VALUES ($1, $2, $3, 'Person ' || $4, 'mail:' || lower($3), 'test', 'wp1158', 'sent') RETURNING id::text`,
		e.f.OrgA, channel, email, tag).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	return auditID, ciID, eventID, deliveryID
}

// has reports whether the report names the field of the row in the table.
func has(r *privacy.SubjectReport, table, rowID, fieldPrefix string) *privacy.SubjectFinding {
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.Table == table && (rowID == "" || strings.EqualFold(f.RowID, rowID)) && strings.HasPrefix(f.Field, fieldPrefix) {
			return f
		}
	}
	return nil
}

// revealed resolves the stored JSON of a query in the tenant (as the APIs
// show it).
func (e *subjectEnv) revealed(t *testing.T, query string, args ...any) any {
	t.Helper()
	var out any
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
			return err
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		var err error
		out, err = pii.RevealTx(ctx, tx, e.f.OrgA, doc)
		return err
	})
	return out
}

func TestSubjectReportFindsAllReferences(t *testing.T) {
	e := newSubjectEnv(t)
	contactID, subject := e.contactOf(t, "Erika Muster", "erika@example.org", "+49 89 1")
	auditID, ciID, eventID, deliveryID := e.history(t, "erika", "erika@example.org")

	r := e.report(t, `{"contact_id":"`+contactID+`"}`)
	if r.Person.Kind != privacy.PersonContact || r.Person.SubjectID != subject || r.Person.Data["email"] != "erika@example.org" {
		t.Fatalf("person %+v", r.Person)
	}
	if f := has(&r, "audit_log", auditID, "changes.after.email"); f == nil || f.Value != "ERIKA@EXAMPLE.ORG " || f.Surrogate == "" {
		t.Errorf("audit change not reported: %+v", r.Findings)
	}
	if has(&r, "ci_change", "", "new_value") == nil {
		t.Errorf("ci_change of %s not reported: %+v", ciID, r.Findings)
	}
	if f := has(&r, "event_outbox", eventID, "data.email"); f == nil || f.Value != "erika@example.org" {
		t.Errorf("event not reported: %+v", r.Findings)
	}
	if has(&r, "notification_delivery", deliveryID, "recipient_email") == nil || has(&r, "notification_delivery", deliveryID, "recipient_name") == nil {
		t.Errorf("delivery not reported: %+v", r.Findings)
	}
	if f := has(&r, "contact", contactID, "pii_subject_id"); f == nil || f.Surrogate != subject {
		t.Errorf("contact.pii_subject_id (foreign key on pii_subject) not reported: %+v", r.Findings)
	}
	if r.ValueSubjects < 3 || r.Counts["audit_log"] < 1 || r.Counts["event_outbox"] < 1 {
		t.Errorf("counts %+v, value subjects %d", r.Counts, r.ValueSubjects)
	}
	// Nothing of another person.
	for _, f := range r.Findings {
		if strings.Contains(strings.ToLower(f.Value), "max@") {
			t.Errorf("finding of another person: %+v", f)
		}
	}
}

func TestSubjectReportUser(t *testing.T) {
	e := newSubjectEnv(t)
	bg := context.Background()
	person := e.f.AppUser(t, e.f.OrgA, "wp1158-user")
	var auditID string
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		entry, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: e.f.OrgA, ActorID: person, ActorType: "user",
			Action: "ci.updated", ResourceType: "ci", Changes: map[string]any{"name": "x"}})
		if err == nil {
			auditID = entry.ID
		}
		return err
	})
	asset := e.f.Asset(t, e.f.OrgA, "", "wp1158-asset")
	var movement string
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, actor_id, assignee_user_id)
		VALUES ($1, 'asset', $2, 'assignment', $3, $3) RETURNING id::text`, e.f.OrgA, asset, person).Scan(&movement); err != nil {
		t.Fatal(err)
	}
	r := e.report(t, `{"user_id":"`+person+`"}`)
	if r.Person.Kind != privacy.PersonUser || !strings.Contains(r.Person.Data["email"], "wp1158-user") {
		t.Fatalf("person %+v", r.Person)
	}
	if has(&r, "audit_log", auditID, "actor_id") == nil {
		t.Errorf("audit actor not reported: %+v", r.Findings)
	}
	if has(&r, "stock_movement", movement, "actor_id") == nil || has(&r, "stock_movement", movement, "assignee_user_id") == nil {
		t.Errorf("stock movement not reported: %+v", r.Findings)
	}
	if has(&r, "app_user", person, "email") == nil {
		t.Errorf("account not reported: %+v", r.Findings)
	}
}

func TestSubjectShredHidesHistory(t *testing.T) {
	e := newSubjectEnv(t)
	bg := context.Background()
	contactID, subject := e.contactOf(t, "Erika Muster", "erika@example.org", "+49 89 1")
	auditID, ciID, eventID, deliveryID := e.history(t, "erika", "erika@example.org")
	// An earlier report holds the values unsealed.
	e.report(t, `{"contact_id":"`+contactID+`"}`)

	var res privacy.SubjectErasure
	if err := json.Unmarshal(e.runJob(t, "/api/v1/privacy/subjects/erase", `{"contact_id":"`+contactID+`","mode":"shred"}`), &res); err != nil {
		t.Fatal(err)
	}
	if res.ValueSubjects < 3 || res.Subjects < 1 || res.Deliveries != 1 || res.Reports != 1 {
		t.Fatalf("erasure %+v", res)
	}
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), "erika") {
		t.Fatalf("the erasure result holds a value: %s", raw)
	}

	changes := e.revealed(t, `SELECT changes FROM audit_log WHERE id = $1`, auditID).(map[string]any)
	if after := changes["after"].(map[string]any); after["email"] != nil || after["role"] != "admin" {
		t.Errorf("audit after the shred: %v", changes)
	}
	v := e.revealed(t, `SELECT new_value FROM ci_change WHERE ci_id = $1 AND new_value::text LIKE '%"$pii"%' ORDER BY created_at DESC LIMIT 1`, ciID)
	if raw, _ := json.Marshal(v); strings.Contains(string(raw), "erika") {
		t.Errorf("ci_change after the shred: %s", raw)
	}
	if data := e.revealed(t, `SELECT data FROM event_outbox WHERE id = $1`, eventID).(map[string]any); data["email"] != nil {
		t.Errorf("event after the shred: %v", data)
	}
	var email, name *string
	var key string
	if err := e.f.Admin.QueryRow(bg, `SELECT recipient_email, recipient_name, recipient_key FROM notification_delivery WHERE id = $1`,
		deliveryID).Scan(&email, &name, &key); err != nil {
		t.Fatal(err)
	}
	if email != nil || name != nil || strings.Contains(key, "erika") {
		t.Errorf("delivery after the shred: %v %v %s", email, name, key)
	}
	var left int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE surrogate_id = $1 OR parent_id = $1`, subject).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d subjects of the person left (%v)", left, err)
	}
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2 AND result::text ILIKE '%erika%'`,
		e.f.OrgA, privacy.SubjectReportJobKind).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d earlier reports still hold the values (%v)", left, err)
	}
	// The contact shows "deleted": no name, address or phone.
	got, err := contact.NewPGRepository(e.f.App).GetByID(e.ctx, e.f.OrgA, contactID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "" || got.Email != "" || got.Phone != "" {
		t.Errorf("contact after the shred: %+v", got)
	}
}

func TestSubjectShredLeavesNeighbour(t *testing.T) {
	e := newSubjectEnv(t)
	contactID, _ := e.contactOf(t, "Erika Muster", "erika@example.org", "")
	neighbourID, neighbourSubject := e.contactOf(t, "Max Other", "max@example.org", "")
	e.history(t, "erika", "erika@example.org")
	auditID, _, eventID, _ := e.history(t, "max", "max@example.org")

	e.runJob(t, "/api/v1/privacy/subjects/erase", `{"contact_id":"`+contactID+`","mode":"shred"}`)

	changes := e.revealed(t, `SELECT changes FROM audit_log WHERE id = $1`, auditID).(map[string]any)
	if after := changes["after"].(map[string]any); after["email"] != "MAX@EXAMPLE.ORG " {
		t.Errorf("neighbour's audit value: %v", changes)
	}
	if data := e.revealed(t, `SELECT data FROM event_outbox WHERE id = $1`, eventID).(map[string]any); data["email"] != "max@example.org" {
		t.Errorf("neighbour's event: %v", data)
	}
	got, err := contact.NewPGRepository(e.f.App).GetByID(e.ctx, e.f.OrgA, neighbourID)
	if err != nil || got.Email != "max@example.org" || got.DisplayName != "Max Other" {
		t.Errorf("neighbour after the shred: %+v %v", got, err)
	}
	if s, err := e.vault.Resolve(e.ctx, e.f.OrgA, neighbourSubject); err != nil || s.Data["email"] != "max@example.org" {
		t.Errorf("neighbour's subject: %+v %v", s, err)
	}
}

func TestSubjectPseudonymize(t *testing.T) {
	e := newSubjectEnv(t)
	bg := context.Background()
	contactID, subject := e.contactOf(t, "Erika Muster", "erika@example.org", "")
	auditID, _, eventID, deliveryID := e.history(t, "erika", "erika@example.org")
	var ref string
	if err := e.f.Admin.QueryRow(bg, `SELECT changes -> 'after' -> 'email' ->> '$pii' FROM audit_log WHERE id = $1`, auditID).Scan(&ref); err != nil {
		t.Fatal(err)
	}

	var res privacy.SubjectErasure
	if err := json.Unmarshal(e.runJob(t, "/api/v1/privacy/subjects/erase", `{"contact_id":"`+contactID+`","mode":"pseudonymize"}`), &res); err != nil {
		t.Fatal(err)
	}
	pseudonym := privacy.Pseudonym(subject)
	if !strings.HasPrefix(pseudonym, "person-") || len(pseudonym) != len("person-")+8 {
		t.Fatalf("pseudonym %q", pseudonym)
	}
	// The surrogates stay.
	var after, kept string
	if err := e.f.Admin.QueryRow(bg, `SELECT changes -> 'after' -> 'email' ->> '$pii', (SELECT pii_subject_id::text FROM contact WHERE id = $2)
		FROM audit_log WHERE id = $1`, auditID, contactID).Scan(&after, &kept); err != nil {
		t.Fatal(err)
	}
	if after != ref || kept != subject {
		t.Fatalf("surrogates changed: %s -> %s, contact %s -> %s", ref, after, subject, kept)
	}
	var left int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE surrogate_id = ANY($1::uuid[])`, []string{ref, subject}).Scan(&left); err != nil || left != 2 {
		t.Fatalf("subjects deleted by the pseudonymization: %d %v", left, err)
	}
	// The display shows the pseudonym.
	changes := e.revealed(t, `SELECT changes FROM audit_log WHERE id = $1`, auditID).(map[string]any)
	if v := changes["after"].(map[string]any)["email"]; v != pseudonym {
		t.Errorf("audit shows %v, want %s", v, pseudonym)
	}
	if data := e.revealed(t, `SELECT data FROM event_outbox WHERE id = $1`, eventID).(map[string]any); data["email"] != pseudonym {
		t.Errorf("event shows %v", data)
	}
	got, err := contact.NewPGRepository(e.f.App).GetByID(e.ctx, e.f.OrgA, contactID)
	if err != nil || got.DisplayName != pseudonym || got.Email != "" {
		t.Errorf("contact shows %+v %v", got, err)
	}
	var name string
	if err = e.f.Admin.QueryRow(bg, `SELECT recipient_name FROM notification_delivery WHERE id = $1`, deliveryID).Scan(&name); err != nil || name != pseudonym {
		t.Errorf("delivery name %q %v", name, err)
	}
	if res.ValueSubjects < 3 || res.Subjects < 1 {
		t.Errorf("pseudonymization %+v", res)
	}
}

func TestSubjectRoutesForeignOrg404(t *testing.T) {
	e := newSubjectEnv(t)
	bg := context.Background()
	// A contact and a user of organization B.
	var foreignContact string
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO contact (organization_id, display_name, email) VALUES ($1, 'B person', 'b@example.org')
		RETURNING id::text`, e.f.OrgB).Scan(&foreignContact); err != nil {
		t.Fatal(err)
	}
	foreignUser := e.f.AppUser(t, e.f.OrgB, "wp1158-foreign")
	for _, c := range []struct{ path, body string }{
		{"/api/v1/privacy/subjects/report", `{"contact_id":"` + foreignContact + `"}`},
		{"/api/v1/privacy/subjects/report", `{"user_id":"` + foreignUser + `"}`},
		{"/api/v1/privacy/subjects/erase", `{"contact_id":"` + foreignContact + `","mode":"shred"}`},
		{"/api/v1/privacy/subjects/erase", `{"user_id":"` + foreignUser + `","mode":"pseudonymize"}`},
		{"/api/v1/privacy/subjects/report", `{"subject_id":"` + e.f.ID() + `"}`},
	} {
		if w := e.post(t, e.ctx, c.path, c.body); w.Code != http.StatusNotFound {
			t.Errorf("POST %s %s: %d %s, want 404", c.path, c.body, w.Code, w.Body.String())
		}
	}
	// Field errors: no person, two persons, an unknown mode, the own account.
	for _, c := range []struct{ path, body, field string }{
		{"/api/v1/privacy/subjects/report", `{}`, "user_id"},
		{"/api/v1/privacy/subjects/report", `{"user_id":"` + e.admin + `","contact_id":"` + foreignContact + `"}`, "user_id"},
		{"/api/v1/privacy/subjects/erase", `{"contact_id":"` + foreignContact + `","mode":"burn"}`, "mode"},
		{"/api/v1/privacy/subjects/erase", `{"user_id":"` + e.admin + `","mode":"shred"}`, "user_id"},
	} {
		w := e.post(t, e.ctx, c.path, c.body)
		var problem api.ProblemDetail
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || !bytes.Contains(w.Body.Bytes(), []byte(`"`+c.field+`"`)) {
			t.Errorf("POST %s %s: %d %s, want 422 %s", c.path, c.body, w.Code, w.Body.String(), c.field)
		}
	}
	// Nothing of organization B changed.
	var name string
	if err := e.f.Admin.QueryRow(bg, `SELECT display_name FROM contact WHERE id = $1`, foreignContact).Scan(&name); err != nil || name != "B person" {
		t.Errorf("foreign contact: %q %v", name, err)
	}
}

func TestSubjectAuditWithoutPlaintext(t *testing.T) {
	e := newSubjectEnv(t)
	bg := context.Background()
	contactID, subject := e.contactOf(t, "Erika Muster", "erika@example.org", "+49 89 1")
	e.history(t, "erika", "erika@example.org")
	person := e.f.AppUser(t, e.f.OrgA, "wp1158-erased")
	e.report(t, `{"contact_id":"`+contactID+`"}`)
	e.runJob(t, "/api/v1/privacy/subjects/erase", `{"contact_id":"`+contactID+`","mode":"shred"}`)
	e.runJob(t, "/api/v1/privacy/subjects/erase", `{"user_id":"`+person+`","mode":"shred"}`)

	rows, err := e.f.Admin.Query(bg, `SELECT action, changes::text FROM audit_log
		WHERE organization_id = $1 AND action LIKE 'privacy.subject_%' ORDER BY seq`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct{ action, changes string }
	entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
		var v entry
		scanErr := r.Scan(&v.action, &v.changes)
		return v, scanErr
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each erasure is audited before its first batch and at its end.
	want := []string{"privacy.subject_report", "privacy.subject_erase.started", "privacy.subject_erase",
		"privacy.subject_erase.started", "privacy.subject_erase"}
	if len(entries) != len(want) {
		t.Fatalf("audit entries %+v, want %v", entries, want)
	}
	for i, a := range want {
		if entries[i].action != a {
			t.Errorf("audit entry %d: %s, want %s", i, entries[i].action, a)
		}
	}
	for _, en := range entries {
		low := strings.ToLower(en.changes)
		for _, plain := range []string{"erika", "muster", "+49", "wp1158-erased", "$pii"} {
			if strings.Contains(low, plain) {
				t.Errorf("audit entry %s holds %q: %s", en.action, plain, en.changes)
			}
		}
		if !strings.Contains(en.changes, `"counts"`) || !strings.Contains(en.changes, `"mode"`) {
			t.Errorf("audit entry %s without mode and counts: %s", en.action, en.changes)
		}
	}
	if !strings.Contains(entries[1].changes, subject) || !strings.Contains(entries[2].changes, subject) ||
		!strings.Contains(entries[3].changes, person) || !strings.Contains(entries[4].changes, person) {
		t.Errorf("audit entries do not name surrogate and id: %+v", entries)
	}
	// The erased user is anonymized and inactive.
	var email string
	var active bool
	if err = e.f.Admin.QueryRow(bg, `SELECT email, is_active FROM app_user WHERE id = $1`, person).Scan(&email, &active); err != nil {
		t.Fatal(err)
	}
	if active || email != user.SurrogateEmail(person) {
		t.Errorf("user after the erasure: %s active %v", email, active)
	}
}

// nameValue writes the name as a personal value into an audit change and
// returns the surrogate of its value subject.
func (e *subjectEnv) nameValue(t *testing.T, name string) string {
	t.Helper()
	var auditID, ref string
	e.inTx(t, func(ctx context.Context, tx pgx.Tx) error {
		entry, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: e.f.OrgA, ActorID: e.admin, ActorType: "user",
			Action: "contact.updated", ResourceType: "contact", Changes: map[string]any{"after": map[string]any{"display_name": name}}})
		if err != nil {
			return err
		}
		auditID = entry.ID
		return nil
	})
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT changes -> 'after' -> 'display_name' ->> '$pii' FROM audit_log WHERE id = $1`,
		auditID).Scan(&ref); err != nil || ref == "" {
		t.Fatalf("name value: %q %v", ref, err)
	}
	return ref
}

// TestSubjectShredKeepsNamesake: a value found by the name alone may be
// another person's (two contacts "Max Müller"): the report marks it as a
// candidate, the erasure keeps it unless the request confirms it.
func TestSubjectShredKeepsNamesake(t *testing.T) {
	e := newSubjectEnv(t)
	contactID, _ := e.contactOf(t, "Max Müller", "max.a@example.org", "")
	_, otherSubject := e.contactOf(t, "Max Müller", "max.b@example.org", "")
	e.history(t, "max-a", "max.a@example.org")
	confirmed, kept := e.nameValue(t, "Max Müller"), e.nameValue(t, "max müller ")

	r := e.report(t, `{"contact_id":"`+contactID+`"}`)
	if r.Candidates != 2 {
		t.Errorf("report candidates %d, want 2", r.Candidates)
	}
	for _, ref := range []string{confirmed, kept} {
		var found bool
		for _, f := range r.Findings {
			if f.Surrogate == ref {
				found = true
				if !f.Candidate {
					t.Errorf("finding %+v of a name alone is no candidate", f)
				}
			}
		}
		if !found {
			t.Errorf("report misses the name value %s", ref)
		}
	}
	if f := has(&r, "audit_log", "", "changes.after.email"); f == nil || f.Candidate {
		t.Errorf("e-mail finding %+v", f)
	}

	var res privacy.SubjectErasure
	if err := json.Unmarshal(e.runJob(t, "/api/v1/privacy/subjects/erase",
		`{"contact_id":"`+contactID+`","mode":"shred","confirmed_surrogates":["`+confirmed+`"]}`), &res); err != nil {
		t.Fatal(err)
	}
	if res.CandidatesKept != 1 || res.ValueSubjects < 4 {
		t.Errorf("erasure %+v, want one candidate kept", res)
	}
	if s, err := e.vault.Resolve(e.ctx, e.f.OrgA, kept); err != nil || s.Data["v"] != "max müller " {
		t.Errorf("the namesake's value: %+v %v", s, err)
	}
	if s, err := e.vault.Resolve(e.ctx, e.f.OrgA, confirmed); err == nil {
		t.Errorf("the confirmed candidate stays: %+v", s)
	}
	if s, err := e.vault.Resolve(e.ctx, e.f.OrgA, otherSubject); err != nil || s.Data["email"] != "max.b@example.org" {
		t.Errorf("the namesake's subject: %+v %v", s, err)
	}
	// A confirmed surrogate is a UUID; the report confirms nothing.
	for _, body := range []string{
		`{"contact_id":"` + contactID + `","mode":"shred","confirmed_surrogates":["x"]}`,
	} {
		if w := e.post(t, e.ctx, "/api/v1/privacy/subjects/erase", body); w.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(w.Body.String(), "confirmed_surrogates") {
			t.Errorf("POST erase %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := e.post(t, e.ctx, "/api/v1/privacy/subjects/report", `{"contact_id":"`+contactID+`","confirmed_surrogates":["`+kept+`"]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("report with confirmed surrogates: %d %s", w.Code, w.Body.String())
	}
}

// TestSubjectEraseStopsOpenReports: a report about the person still queued
// when the erasure commits is cancelled (it stores no values afterwards),
// and a report that finds an erasure of the person running stores none.
func TestSubjectEraseStopsOpenReports(t *testing.T) {
	e := newSubjectEnv(t)
	contactID, _ := e.contactOf(t, "Erika Muster", "erika@example.org", "")
	e.history(t, "erika", "erika@example.org")
	open, _, err := e.jobs.Enqueue(e.ctx, e.f.OrgA, privacy.SubjectReportJobKind, privacy.SubjectRequest{ContactID: contactID},
		jobs.EnqueueOptions{CreatedBy: e.admin})
	if err != nil {
		t.Fatal(err)
	}
	e.runJob(t, "/api/v1/privacy/subjects/erase", `{"contact_id":"`+contactID+`","mode":"pseudonymize"}`)
	got, err := e.jobs.Get(e.ctx, e.f.OrgA, open.ID)
	if err != nil || got.Status != jobs.StatusCancelled || len(got.Result) != 0 {
		t.Fatalf("open report after the erasure: %+v %v", got, err)
	}
	if claimed, cerr := e.jobs.ClaimJob(e.ctx, e.f.OrgA, open.ID); cerr != nil || claimed != nil {
		t.Errorf("the cancelled report was claimed: %+v %v", claimed, cerr)
	}

	// An erasure of the person runs: the report stores nothing.
	other, _ := e.contactOf(t, "Hans Probe", "hans@example.org", "")
	running, _, err := e.jobs.Enqueue(e.ctx, e.f.OrgA, privacy.SubjectEraseJobKind,
		privacy.SubjectRequest{ContactID: other, Mode: privacy.ModeShred}, jobs.EnqueueOptions{CreatedBy: e.admin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.jobs.ClaimJob(e.ctx, e.f.OrgA, running.ID); err != nil {
		t.Fatal(err)
	}
	if r, rerr := e.svc.RunReport(e.ctx, e.f.OrgA, e.admin, &privacy.SubjectRequest{ContactID: other}); rerr == nil {
		t.Errorf("report during a running erasure: %+v", r)
	}
}

// TestSubjectReportHiddenWithoutUserManage: the job of a report holds the
// person's values unsealed; a principal with job:read but without
// user:manage (an engineer) does not see it in the job API.
func TestSubjectReportHiddenWithoutUserManage(t *testing.T) {
	e := newSubjectEnv(t)
	contactID, _ := e.contactOf(t, "Erika Muster", "erika@example.org", "")
	e.history(t, "erika", "erika@example.org")
	e.report(t, `{"contact_id":"`+contactID+`"}`)
	var reportID string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM job WHERE organization_id = $1 AND kind = $2`,
		e.f.OrgA, privacy.SubjectReportJobKind).Scan(&reportID); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	jobs.NewAPIHandler(e.jobs).
		WithKind(privacy.SubjectReportJobKind, identity.PermUserManage, nil).
		WithReadKind(privacy.SubjectReportJobKind, identity.PermUserManage).
		RegisterRoutes(mux)
	engineer := e.f.AppUser(t, e.f.OrgA, "wp1158-engineer")
	get := func(user, path string, perms ...identity.Permission) *httptest.ResponseRecorder {
		scope := database.OrgWideScope(e.f.OrgA, user)
		ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
			tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: user})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: append([]identity.Permission{identity.PermJobRead}, perms...)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w
	}
	for _, path := range []string{"/api/v1/jobs/" + reportID, "/api/v1/jobs/" + reportID + "/result"} {
		if w := get(engineer, path); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "erika") {
			t.Errorf("engineer GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/jobs", "/api/v1/jobs?kind=" + privacy.SubjectReportJobKind} {
		if w := get(engineer, path); w.Code != http.StatusOK || strings.Contains(w.Body.String(), reportID) ||
			strings.Contains(w.Body.String(), "erika") {
			t.Errorf("engineer GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := get(e.admin, "/api/v1/jobs/"+reportID+"/result", identity.PermUserManage); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "erika") {
		t.Errorf("admin result: %d %s", w.Code, w.Body.String())
	}
	if w := get(e.admin, "/api/v1/jobs", identity.PermUserManage); !strings.Contains(w.Body.String(), reportID) {
		t.Errorf("admin list misses the report: %s", w.Body.String())
	}
}
