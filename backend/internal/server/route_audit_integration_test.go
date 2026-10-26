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
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestAuditDefectRouteAuditFailure|TestRouteIntentCompletionRacesScheduler' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
