// Route audit through the production router on PostgreSQL (AUD-05, AUD-07,
// WP-504, F-149): a write route without a domain recorder stores its audit
// intent before the handler runs; the entry follows the response or, for an
// intent no response completed, the scheduler run (audit.route_intents).
// Harness: audit_t6_harness_test.go.
//
//   - TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation (formerly
//     audit_t6_router_auditdefect_test.go, WP-133, F-149): audit store down
//     before the mutation → 503 and no VRF; down after the mutation → after
//     the scheduler run exactly one entry (outcome unknown); the regular
//     path writes exactly one entry. Entries are read through
//     GET /api/v1/audit-log?resource_type=vrfs with audit:read.
//   - TestRouteIntentCompletionRacesScheduler: a late regular completion and
//     the scheduler take the same intents concurrently; every intent gets
//     exactly one audit_log entry.
//   - TestAuditDefectRouteAuditStoresOldAndNewValues (formerly
//     audit_t6_router_auditdefect_test.go, WP-133, F-148, fixed by WP-505):
//     create, rename (also with quotes) and delete of a VLAN store
//     {field, old, new} per changed column; the chain stays intact.
//   - TestRouteAuditPIIIsSurrogate (WP-505): name, e-mail and phone of a
//     contact appear only as the contact id; webhook secrets and the
//     channel configuration never.
//   - TestRouteAuditNulPathIDIsClientError (WP-662, F-391): a path id with a
//     NUL byte or invalid UTF-8 is answered 404 before the audit intent,
//     not 503 with an ERROR log; an intent that cannot be stored still
//     refuses the write (503).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestAuditDefectRouteAuditFailure|TestRouteIntentCompletionRacesScheduler|TestAuditDefectRouteAuditStores|TestRouteAuditPIIIsSurrogate|TestRouteAuditNulPathIDIsClientError' ./internal/server/
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// t6FailingAuditor is the production route auditor with an audit store that
// fails before (Intend) or after (Complete) the mutation.
type t6FailingAuditor struct {
	*audit.PGRouteAuditor
	failIntend, failComplete bool
}

var errT6AuditStore = errors.New("audit-t6: audit store unavailable")

func (a *t6FailingAuditor) Intend(ctx context.Context, intent *audit.RouteIntent) error {
	if a.failIntend {
		return errT6AuditStore
	}
	return a.PGRouteAuditor.Intend(ctx, intent)
}

func (a *t6FailingAuditor) Complete(ctx context.Context, intent *audit.RouteIntent, entry *audit.Entry) error {
	if a.failComplete {
		return errT6AuditStore
	}
	return a.PGRouteAuditor.Complete(ctx, intent, entry)
}

// t6AuditList reads the audit entries of a resource type through
// GET /api/v1/audit-log as a principal with audit:read only (AUD-05).
func t6AuditList(t *testing.T, e *t6Env, reader t6As, resourceType string) []audit.Entry {
	t.Helper()
	r := e.Call(t, reader, http.MethodGet, "/api/v1/audit-log?resource_type="+resourceType+"&limit=100", ``)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /audit-log?resource_type=%s = %d %s", resourceType, r.Code, t6Short(r.Body))
	}
	var list struct {
		Data []audit.Entry `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &list); err != nil {
		t.Fatalf("GET /audit-log body: %v", err)
	}
	return list.Data
}

func TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation(t *testing.T) {
	ra := &t6FailingAuditor{failIntend: true}
	e := t6NewEnv(t, "d6", ra)
	f := e.F
	bg := context.Background()
	ra.PGRouteAuditor = audit.NewPGRouteAuditor(f.App)
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d6-admin"), identity.AllPermissions()}
	reader := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d6-reader"), []identity.Permission{identity.PermAuditRead}}
	vrfs := func(name string) int {
		return e.Count(t, `SELECT count(*) FROM vrf WHERE organization_id = $1 AND name = $2`, f.OrgA, name)
	}
	intents := func() int {
		return e.Count(t, `SELECT count(*) FROM audit_route_intent WHERE organization_id = $1`, f.OrgA)
	}

	// The audit store fails before the mutation: refused, nothing stored.
	r := e.Call(t, admin, http.MethodPost, "/api/v1/vrfs", `{"name":"audit-d6-vrf"}`)
	if r.Code != http.StatusServiceUnavailable || vrfs("audit-d6-vrf") != 0 || intents() != 0 {
		t.Fatalf("POST /vrfs with the audit store down before the mutation = %d %s; vrf rows %d, intents %d; want 503 and nothing stored",
			r.Code, t6Short(r.Body), vrfs("audit-d6-vrf"), intents())
	}
	if got := t6AuditList(t, e, reader, "vrfs"); len(got) != 0 {
		t.Fatalf("audit entries after a refused write: %+v", got)
	}

	// The audit store fails after the mutation: the intent stays and the
	// scheduler writes exactly one entry.
	ra.failIntend, ra.failComplete = false, true
	r = e.Call(t, admin, http.MethodPost, "/api/v1/vrfs", `{"name":"audit-d6-vrf"}`)
	persisted := vrfs("audit-d6-vrf")
	t.Logf("POST /vrfs with the audit store down after the mutation = %d; vrf rows %d; intents %d", r.Code, persisted, intents())
	if r.Code != http.StatusCreated || persisted != 1 || intents() != 1 {
		t.Fatalf("POST /vrfs = %d %s; vrf rows %d, intents %d; want 201, 1 and 1", r.Code, t6Short(r.Body), persisted, intents())
	}
	if got := t6AuditList(t, e, reader, "vrfs"); len(got) != 0 {
		t.Fatalf("audit entries before the scheduler run: %+v", got)
	}
	var requestID string
	if err := f.Admin.QueryRow(bg, `SELECT request_id FROM audit_route_intent WHERE organization_id = $1`, f.OrgA).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	// A young intent belongs to a request that may still complete it.
	if _, err := ra.SweepRouteIntents(bg); err != nil {
		t.Fatalf("scheduler run: %v", err)
	}
	if intents() != 1 {
		t.Fatalf("the scheduler took an intent younger than %s", audit.RouteIntentGrace)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE audit_route_intent SET created_at = now() - interval '3 minutes' WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if n, err := ra.SweepRouteIntents(bg); err != nil || n < 1 {
		t.Fatalf("scheduler run: %d entries, %v", n, err)
	}
	got := t6AuditList(t, e, reader, "vrfs")
	if len(got) != 1 || intents() != 0 {
		t.Fatalf("after the scheduler run: %d entries %+v, %d intents; want exactly one entry and no intent", len(got), got, intents())
	}
	if en := got[0]; en.Action != "vrfs.create" || en.Changes["outcome"] != audit.OutcomeUnknown || en.Changes["route"] != "POST /api/v1/vrfs" ||
		en.RequestID != requestID || en.ActorType != "user" || en.ActorID != admin.User {
		t.Errorf("entry of the scheduler: %+v", en)
	}
	if n, err := ra.SweepRouteIntents(bg); err != nil || len(t6AuditList(t, e, reader, "vrfs")) != 1 {
		t.Fatalf("second scheduler run: %d, %v; entries %d", n, err, len(t6AuditList(t, e, reader, "vrfs")))
	}

	// The regular path: exactly one entry, no intent left.
	ra.failComplete = false
	r = e.Call(t, admin, http.MethodPost, "/api/v1/vrfs", `{"name":"audit-d6-vrf-2"}`)
	if r.Code != http.StatusCreated || vrfs("audit-d6-vrf-2") != 1 || intents() != 0 {
		t.Fatalf("POST /vrfs = %d %s; intents %d", r.Code, t6Short(r.Body), intents())
	}
	got = t6AuditList(t, e, reader, "vrfs")
	if len(got) != 2 {
		t.Fatalf("entries after the regular write: %+v", got)
	}
	var regular *audit.Entry
	for i := range got {
		if got[i].ResourceID == t6ID(r.Body) {
			regular = &got[i]
		}
	}
	if regular == nil || regular.Changes["outcome"] != nil || regular.Changes["status"] != float64(http.StatusCreated) {
		t.Errorf("entry of the regular write: %+v", regular)
	}
	// A refused write deletes its intent without an entry.
	if r = e.Call(t, admin, http.MethodPost, "/api/v1/vrfs", `{"name":""}`); r.Code < 400 || intents() != 0 || len(t6AuditList(t, e, reader, "vrfs")) != 2 {
		t.Errorf("refused POST /vrfs = %d; intents %d", r.Code, intents())
	}
}

func TestRouteIntentCompletionRacesScheduler(t *testing.T) {
	e := t6NewEnv(t, "a6", nil)
	f := e.F
	bg := context.Background()
	ra := audit.NewPGRouteAuditor(f.App)
	const n = 40
	intents := make([]*audit.RouteIntent, n)
	for i := range intents {
		intents[i] = &audit.RouteIntent{OrganizationID: f.OrgA, RequestID: fmt.Sprintf("race-a6-%02d", i), Route: "POST /api/v1/vrfs",
			Action: "vrfs.create", ResourceType: "vrfs", ActorType: "user", ActorID: f.User, Source: audit.SourceUI, Fields: []string{"name"}}
		if err := ra.Intend(bg, intents[i]); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.Count(t, `SELECT count(*) FROM audit_route_intent WHERE organization_id = $1`, f.OrgA); got != n {
		t.Fatalf("stored intents: %d", got)
	}
	// Organization B neither sees nor deletes A's intents; the system flag
	// sees them but deletes none.
	scopeB := database.OrgWideScope(f.OrgB, "")
	if err := database.WithTenant(bg, f.App, &scopeB, func(ctx context.Context, tx pgx.Tx) error {
		var seen int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_route_intent`).Scan(&seen); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM audit_route_intent WHERE organization_id = $1`, f.OrgA)
		if err == nil && (seen != 0 || tag.RowsAffected() != 0) {
			err = fmt.Errorf("organization B saw %d and deleted %d intents of A", seen, tag.RowsAffected())
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	errRefused := errors.New("refused")
	if err := database.WithSystem(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		var seen int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_route_intent WHERE organization_id = $1`, f.OrgA).Scan(&seen); err != nil {
			return err
		}
		if seen != n {
			return fmt.Errorf("the system flag found %d of %d intents", seen, n)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM audit_route_intent WHERE organization_id = $1`, f.OrgA); err != nil {
			return errRefused
		}
		return errors.New("a system transaction deleted intents")
	}); !errors.Is(err, errRefused) {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE audit_route_intent SET created_at = now() - interval '3 minutes' WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n+2)
	for i, in := range intents {
		wg.Add(1)
		go func(i int, in *audit.RouteIntent) {
			defer wg.Done()
			<-start
			// Some completions come late, after the scheduler started.
			time.Sleep(time.Duration(i%10) * time.Millisecond)
			entry := audit.Entry{OrganizationID: in.OrganizationID, ActorID: in.ActorID, ActorType: in.ActorType, Action: in.Action,
				ResourceType: in.ResourceType, Source: in.Source, RequestID: in.RequestID,
				Changes: map[string]interface{}{"route": in.Route, "status": http.StatusCreated}}
			errs <- ra.Complete(bg, in, &entry)
		}(i, in)
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ra.SweepRouteIntents(bg)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("completion or scheduler: %v", err)
		}
	}
	for _, in := range intents {
		if got := e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id = $2`, f.OrgA, in.RequestID); got != 1 {
			t.Errorf("intent %s: %d audit entries, want exactly 1", in.RequestID, got)
		}
	}
	if got := e.Count(t, `SELECT count(*) FROM audit_route_intent WHERE organization_id = $1`, f.OrgA); got != 0 {
		t.Errorf("%d intents left", got)
	}

	// In sequence: the scheduler took the intent, the late completion
	// writes nothing; a completed intent gives the scheduler nothing.
	late := &audit.RouteIntent{OrganizationID: f.OrgA, RequestID: "race-a6-late", Route: "POST /api/v1/vrfs", Action: "vrfs.create",
		ResourceType: "vrfs", ActorType: "user", Source: audit.SourceUI}
	done := &audit.RouteIntent{OrganizationID: f.OrgA, RequestID: "race-a6-done", Route: "POST /api/v1/vrfs", Action: "vrfs.create",
		ResourceType: "vrfs", ActorType: "user", Source: audit.SourceUI}
	for _, in := range []*audit.RouteIntent{late, done} {
		if err := ra.Intend(bg, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Admin.Exec(bg, `UPDATE audit_route_intent SET created_at = now() - interval '3 minutes' WHERE id = $1`, late.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ra.SweepRouteIntents(bg); err != nil {
		t.Fatal(err)
	}
	for _, in := range []*audit.RouteIntent{late, done} {
		entry := audit.Entry{OrganizationID: f.OrgA, ActorType: "user", Action: in.Action, ResourceType: in.ResourceType, RequestID: in.RequestID,
			Changes: map[string]interface{}{"route": in.Route, "status": http.StatusCreated}}
		if err := ra.Complete(bg, in, &entry); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Admin.Exec(bg, `UPDATE audit_route_intent SET created_at = now() - interval '3 minutes' WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := ra.SweepRouteIntents(bg); err != nil {
		t.Fatal(err)
	}
	for in, outcome := range map[*audit.RouteIntent]string{late: audit.OutcomeUnknown, done: ""} {
		if got := e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id = $2 AND COALESCE(changes->>'outcome', '') = $3`,
			f.OrgA, in.RequestID, outcome); got != 1 ||
			e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id = $2`, f.OrgA, in.RequestID) != 1 {
			t.Errorf("intent %s: want exactly one entry with outcome %q", in.RequestID, outcome)
		}
	}

	unknown := e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id ~ '^race-a6-[0-9]+$' AND changes->>'outcome' = 'unknown'`, f.OrgA)
	t.Logf("%d of %d intents taken by the scheduler, %d by their completion", unknown, n, n-unknown)
}

// t6Values returns the {field, old, new} list of an entry's changes by
// field (WP-505).
func t6Values(t *testing.T, en audit.Entry) map[string][2]any {
	t.Helper()
	list, ok := en.Changes["values"].([]any)
	if !ok {
		t.Fatalf("entry %s %s has no values: %v", en.Action, en.ResourceID, en.Changes)
	}
	out := map[string][2]any{}
	for _, item := range list {
		v, _ := item.(map[string]any)
		field, _ := v["field"].(string)
		out[field] = [2]any{v["old"], v["new"]}
	}
	return out
}

// t6Entry is the only entry of action on resource id in entries.
func t6Entry(t *testing.T, entries []audit.Entry, action, id string) audit.Entry {
	t.Helper()
	var found []audit.Entry
	for _, en := range entries {
		if en.Action == action && en.ResourceID == id {
			found = append(found, en)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d entries %s on %s, want 1: %+v", len(found), action, id, entries)
	}
	return found[0]
}

// t6JSON is the JSON form of v.
func t6JSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestAuditDefectRouteAuditStoresOldAndNewValues(t *testing.T) {
	e := t6NewEnv(t, "d7", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d7-admin"), identity.AllPermissions()}
	reader := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d7-reader"), []identity.Permission{identity.PermAuditRead}}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/vlans", `{"vlan_id":217,"name":"audit-d7-old"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /vlans = %d %s", r.Code, t6Short(r.Body))
	}
	vlan := t6ID(r.Body)
	r = e.Call(t, admin, http.MethodPatch, "/api/v1/vlans/"+vlan, `{"name":"audit-d7-new"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("PATCH /vlans = %d %s", r.Code, t6Short(r.Body))
	}
	entries := t6AuditList(t, e, reader, "vlans")
	rename := t6Values(t, t6Entry(t, entries, "vlans.update", vlan))
	if rename["name"] != [2]any{"audit-d7-old", "audit-d7-new"} || len(rename) != 1 {
		t.Errorf("audit values of the rename = %v; AUD-07 requires old (audit-d7-old) and new (audit-d7-new) values of the changed field only", rename)
	}
	created := t6Values(t, t6Entry(t, entries, "vlans.create", vlan))
	if created["name"] != [2]any{nil, "audit-d7-old"} || created["vlan_id"] != [2]any{nil, 217.0} || created["id"] != [2]any{nil, vlan} {
		t.Errorf("audit values of the create = %v, want the new values", created)
	}

	// A name with quotes and a backslash: the entry is stored, the chain
	// stays intact (F-045), the values come back as written.
	const quoted = `audit "d7" \ quoted`
	r = e.Call(t, admin, http.MethodPatch, "/api/v1/vlans/"+vlan, `{"name":"audit \"d7\" \\ quoted","description":"d7"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("PATCH /vlans with quotes = %d %s", r.Code, t6Short(r.Body))
	}
	if r = e.Call(t, admin, http.MethodDelete, "/api/v1/vlans/"+vlan, ``); r.Code != http.StatusNoContent {
		t.Fatalf("DELETE /vlans = %d %s", r.Code, t6Short(r.Body))
	}
	entries = t6AuditList(t, e, reader, "vlans")
	var updates []map[string][2]any
	for _, en := range entries {
		if en.Action == "vlans.update" && en.ResourceID == vlan {
			updates = append(updates, t6Values(t, en))
		}
	}
	// The list is newest first.
	if len(updates) != 2 || updates[0]["name"] != [2]any{"audit-d7-new", quoted} || updates[0]["description"] != [2]any{nil, "d7"} {
		t.Errorf("audit values of the quoted rename = %v", updates)
	}
	deleted := t6Values(t, t6Entry(t, entries, "vlans.delete", vlan))
	if deleted["name"] != [2]any{quoted, nil} || deleted["vlan_id"] != [2]any{217.0, nil} {
		t.Errorf("audit values of the delete = %v, want the old values", deleted)
	}
	r = e.Call(t, admin, http.MethodPost, "/api/v1/audit-log/verify", `{}`)
	var verify audit.VerifyResult
	if err := json.Unmarshal(r.Body, &verify); err != nil || r.Code != http.StatusOK || !verify.Intact || verify.Checked == 0 {
		t.Errorf("POST /audit-log/verify = %d %s: the chain with quoted values must stay intact", r.Code, t6Short(r.Body))
	}
}

// TestRouteAuditPIIIsSurrogate: the route audit of a contact shows name,
// e-mail and phone only as the surrogate of the contact (CH16, AUD-04);
// webhook secrets and the configuration of a notification channel never
// appear. Read through GET /api/v1/audit-log with audit:read (AUD-05).
func TestRouteAuditPIIIsSurrogate(t *testing.T) {
	e := t6NewEnv(t, "d7", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d7-admin"), identity.AllPermissions()}
	reader := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d7-reader"), []identity.Permission{identity.PermAuditRead}}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/contacts",
		`{"display_name":"Erika D7-Muster","email":"erika.d7@example.com","phone":"+49 30 7777","role":"dba"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /contacts = %d %s", r.Code, t6Short(r.Body))
	}
	contact := t6ID(r.Body)
	r = e.Call(t, admin, http.MethodPatch, "/api/v1/contacts/"+contact, `{"email":"erika.d7-new@example.com","role":"lead"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("PATCH /contacts = %d %s", r.Code, t6Short(r.Body))
	}
	entries := t6AuditList(t, e, reader, "contacts")
	all := t6JSON(entries)
	for _, plain := range []string{"erika.d7", "Erika", "D7-Muster", "+49 30"} {
		if strings.Contains(all, plain) {
			t.Errorf("the contact audit holds %q in plain text: %s", plain, all)
		}
	}
	surrogate := t6JSON(map[string]any{audit.SurrogateKey: contact})
	update := t6Values(t, t6Entry(t, entries, "contacts.update", contact))
	if t6JSON(update["email"]) != "["+surrogate+","+surrogate+"]" {
		t.Errorf("email of the update = %s, want the contact id as surrogate on both sides", t6JSON(update["email"]))
	}
	if update["role"] != [2]any{"dba", "lead"} || len(update) != 2 {
		t.Errorf("values of the contact update = %s", t6JSON(update))
	}
	created := t6Values(t, t6Entry(t, entries, "contacts.create", contact))
	for _, field := range []string{"display_name", "email", "phone"} {
		if t6JSON(created[field]) != "[null,"+surrogate+"]" {
			t.Errorf("%s of the create = %s, want the surrogate", field, t6JSON(created[field]))
		}
	}

	// Secrets: the webhook secret (create, rotation) and the configuration
	// of a notification channel.
	const secret = "d7-webhook-s3cret-value-0123456789"
	r = e.Call(t, admin, http.MethodPost, "/api/v1/webhooks",
		`{"name":"d7-hook","url":"https://hook.example.com/d7","secret":"`+secret+`","events":["ci.created"]}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d %s", r.Code, t6Short(r.Body))
	}
	hook := t6ID(r.Body)
	if r = e.Call(t, admin, http.MethodPost, "/api/v1/webhooks/"+hook+"/rotate-secret", `{}`); r.Code >= 300 {
		t.Fatalf("POST /webhooks/%s/rotate-secret = %d %s", hook, r.Code, t6Short(r.Body))
	}
	var rotated struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(r.Body, &rotated)
	const smtpSecret = "d7-smtp-s3cret-password"
	r = e.Call(t, admin, http.MethodPost, "/api/v1/notification-channels", `{"name":"d7-mail","provider":"smtp",
		"config":{"host":"mail.corp.example.invalid","port":587,"tls":"starttls","from":"cmdb@corp.example.invalid",
		"username":"svc-d7","password":"`+smtpSecret+`"}}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /notification-channels = %d %s", r.Code, t6Short(r.Body))
	}
	webhooks := t6AuditList(t, e, reader, "webhooks")
	channels := t6AuditList(t, e, reader, "notification-channels")
	t6Values(t, t6Entry(t, webhooks, "webhooks.create", hook))
	t6Values(t, t6Entry(t, webhooks, "webhooks.rotate-secret", hook))
	t6Values(t, t6Entry(t, channels, "notification-channels.create", t6ID(r.Body)))
	all = t6JSON(append(webhooks, channels...))
	for _, plain := range []string{secret, smtpSecret, "svc-d7", "mail.corp.example.invalid"} {
		if strings.Contains(all, plain) {
			t.Errorf("the audit holds the secret %q: %s", plain, all)
		}
	}
	if rotated.Secret == "" || strings.Contains(all, rotated.Secret) {
		t.Errorf("rotated webhook secret %q missing from the answer or in the audit", rotated.Secret)
	}
}

// TestRouteAuditNulPathIDIsClientError (F-391, WP-662): a write route
// without a domain recorder answers a path id with a NUL byte or invalid
// UTF-8 like an unknown id (404) before the audit intent, instead of 503
// "the audit trail is unavailable" (SQLSTATE 22021 from the intent's
// resource key) with an ERROR log: core routes (assets, racks), module
// routes (tickets, orders, inventory items, saved views, IGA) and a second
// path parameter. Nothing is stored (no intent, no audit entry). AUD-07
// stays intact: with a valid id a write whose intent cannot be stored is
// still refused with 503.
func TestRouteAuditNulPathIDIsClientError(t *testing.T) {
	ra := &t6FailingAuditor{}
	e := t6NewEnv(t, "d9", ra)
	f := e.F
	// Delegates to the production auditor on the router's pool (AuditPool).
	ra.PGRouteAuditor = audit.NewPGRouteAuditor(f.App)
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d9-admin"), identity.AllPermissions()}
	reader := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d9-reader"), []identity.Permission{identity.PermAuditRead}}
	intents := func() int {
		return e.Count(t, `SELECT count(*) FROM audit_route_intent WHERE organization_id = $1`, f.OrgA)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const unknown = "00000000-0000-4000-8000-0000000000ff"
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/assets/%00", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/racks/%00", ``},
		{http.MethodPatch, "/api/v1/tickets/%00", `{"title":"x"}`},
		{http.MethodDelete, "/api/v1/tickets/%00", ``},
		{http.MethodPost, "/api/v1/orders/%00/submit", ``},
		{http.MethodPatch, "/api/v1/inventory/items/%00", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/saved-views/%00", ``},
		{http.MethodPost, "/api/v1/iga/access-reviews/" + unknown + "/items/%00/decision", `{"decision":"keep"}`},
		{http.MethodPatch, "/api/v1/assets/x%00y", `{"name":"x"}`},
		{http.MethodPatch, "/api/v1/assets/%FF", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/racks/%C3%28", ``},
	} {
		r := e.Call(t, admin, c.method, c.path, c.body)
		if r.Code != http.StatusNotFound && r.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d %s; want 404 or 400 (an id that is no text names no resource)", c.method, c.path, r.Code, t6Short(r.Body))
		}
	}
	if n := intents(); n != 0 {
		t.Errorf("%d audit intents left after refused writes", n)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("refused writes with an invalid path id logged an error:\n%s", logs.String())
	}

	// A valid unknown id still passes the intent; the handler's 404
	// discards it.
	if r := e.Call(t, admin, http.MethodPatch, "/api/v1/assets/"+unknown, `{"name":"x"}`); r.Code != http.StatusNotFound || intents() != 0 {
		t.Errorf("PATCH /assets/<unknown> = %d %s, intents %d; want 404 and none", r.Code, t6Short(r.Body), intents())
	}
	// AUD-07: a write whose intent cannot be stored is still refused.
	ra.failIntend = true
	r := e.Call(t, admin, http.MethodPost, "/api/v1/vrfs", `{"name":"audit-d9-vrf"}`)
	vrfs := e.Count(t, `SELECT count(*) FROM vrf WHERE organization_id = $1 AND name = 'audit-d9-vrf'`, f.OrgA)
	if r.Code != http.StatusServiceUnavailable || vrfs != 0 {
		t.Errorf("POST /vrfs with the audit store down = %d %s, vrf rows %d; want 503 and none", r.Code, t6Short(r.Body), vrfs)
	}
	ra.failIntend = false
	if got := t6AuditList(t, e, reader, "assets"); len(got) != 0 {
		t.Errorf("audit entries after refused writes: %s", t6JSON(got))
	}
}
