package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/bootstrap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// templateMigrateKind is the kind as registered by the server
// (server/jobkinds.go).
var templateMigrateKind = jobs.Kind{Name: ci.TemplateMigrateJobKind, MaxAttempts: 3, Timeout: time.Hour, Cancellable: true}

// flakyRepository wraps the CI repository chain: it forwards the template
// migration and fails or interrupts chosen block transactions (RunInTx).
type flakyRepository struct {
	ci.Repository
	calls atomic.Int32
	// failCall fails the RunInTx call with that number (1: the count,
	// 2: the first block, …) once; 0: none.
	failCall atomic.Int32
	// afterCall runs after the RunInTx call with that number committed.
	afterCall int32
	after     func()
}

func (r *flakyRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	n := r.calls.Add(1)
	if r.failCall.CompareAndSwap(n, 0) {
		return errors.New("wp712: block transaction lost")
	}
	err := r.Repository.(ci.TxRunner).RunInTx(ctx, orgID, fn)
	if err == nil && n == r.afterCall && r.after != nil {
		r.after()
	}
	return err
}

func (r *flakyRepository) MigrateTemplate(ctx context.Context, orgID, ciID string, plan ci.TemplateMigrationPlan,
	dryRun bool) (*ci.TemplateMigrationResult, error) {
	return r.Repository.(ci.TemplateMigrator).MigrateTemplate(ctx, orgID, ciID, plan, dryRun)
}

// fakeDelivery is a message of the job queue without NATS.
type fakeDelivery struct {
	data  []byte
	acked bool
}

func (d *fakeDelivery) Data() []byte                     { return d.data }
func (d *fakeDelivery) Ack() error                       { d.acked = true; return nil }
func (d *fakeDelivery) NakWithDelay(time.Duration) error { return nil }

// TestTemplateMigrationJobOnPostgreSQL covers WP-712 (API-09, JOB-04) for
// the job template.migrate against PostgreSQL, through the route
// authorization (server.AuthorizeRoute), the endpoint, the job API and the
// repository chain of bootstrap.NewCIRepository (WP-418):
//   - POST /templates/{id}/migrations answers 202 with Location and the
//     job; 404 for an unknown template; 422 for a target version that is
//     not published, a draft source, equal or unknown versions, empty or
//     too many ci_ids, ci_ids with where and an invalid where; 403 without
//     citype:manage; 503 without the job service;
//   - in-process run: a client-scoped trigger migrates only the CIs of its
//     client; ci_change carries the trigger as actor, the request id and
//     the job id as correlation id; the report (GET /jobs/{id}/result)
//     holds the totals and per CI defaults, overridden and findings; a dry
//     run writes nothing;
//   - a unique conflict of a default fails only that CI (error unique with
//     the attribute), the other CI of the block is migrated, an id without
//     the assignment is skipped;
//   - a job without scope snapshot fails and writes nothing;
//   - the job queue runs the job (jobs.Queue.Process; with TEST_NATS_URL
//     over NATS JetStream); a running job is cancellable: true;
//   - a cancel between two blocks stops the run after the first block and
//     leaves no result (409); a retry after a failure of the second block
//     writes nothing twice.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTemplateMigrationJobOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "7c")
	bg := context.Background()
	admin := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	admin = identity.WithPrincipal(admin, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
		Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})

	types := citype.NewPGRepository(f.App)
	base := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	chain := bootstrap.NewCIRepository(base, search.NewMemoryRepository(), nil, nil, nil, nil)
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(templateMigrateKind)

	// mount serves the endpoint and the job API behind the route
	// authorization, like the router.
	mount := func(m *ci.TemplateMigrationJobs) http.Handler {
		mux := chi.NewRouter()
		m.RegisterRoutes(mux)
		jobs.NewAPIHandler(jobSvc).WithKind(ci.TemplateMigrateJobKind, identity.PermCITypeManage, nil).RegisterRoutes(mux)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			server.AuthorizeRoute(r.Method, r.URL.Path)(mux).ServeHTTP(w, r)
		})
	}
	migrations := ci.NewTemplateMigrationJobs(chain, types).WithJobs(jobSvc)
	handler := mount(migrations)

	principal := func(user string, scope *database.TenantScope, client string, perms ...identity.Permission) context.Context {
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user, ClientID: client})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: perms})
	}
	managers := []identity.Permission{identity.PermCITypeManage, identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}
	orgUser := f.AppUser(t, f.OrgA, "w712-org")
	orgScope := database.OrgWideScope(f.OrgA, orgUser)
	orgCtx := principal(orgUser, &orgScope, "", managers...)
	clientUser := f.AppUser(t, f.OrgA, "w712-client1")
	clientScope := database.OrgWideScope(f.OrgA, clientUser)
	clientScope.Clients = database.ScopeIDs(f.Client1)
	clientCtx := principal(clientUser, &clientScope, f.Client1, managers...)

	do := func(h http.Handler, ctx context.Context, method, path, requestID string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		switch b := body.(type) {
		case nil:
		case string:
			raw = []byte(b)
		default:
			raw, _ = json.Marshal(b)
		}
		if requestID != "" {
			ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: requestID, CorrelationID: "corr-" + requestID, Source: "api"})
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	accepted := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var acc struct {
			JobID  string `json:"job_id"`
			Status string `json:"status"`
			Kind   string `json:"kind"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &acc); err != nil || w.Code != http.StatusAccepted || acc.JobID == "" ||
			acc.Kind != ci.TemplateMigrateJobKind || w.Header().Get("Location") != "/api/v1/jobs/"+acc.JobID {
			t.Fatalf("migration: %d %s (Location %q), want 202 with the job", w.Code, w.Body.String(), w.Header().Get("Location"))
		}
		return acc.JobID
	}
	waitTerminal := func(id string) *jobs.Job {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for {
			j, err := jobSvc.Get(bg, f.OrgA, id)
			if err == nil && j.Status.Terminal() {
				return j
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s not terminal (last %+v, %v)", id, j, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	reportOf := func(ctx context.Context, id string) ci.TemplateMigrationReport {
		t.Helper()
		w := do(handler, ctx, http.MethodGet, "/api/v1/jobs/"+id+"/result", "", nil)
		var report ci.TemplateMigrationReport
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != http.StatusOK {
			t.Fatalf("result of %s: %d %s", id, w.Code, w.Body.String())
		}
		return report
	}
	itemOf := func(report *ci.TemplateMigrationReport, ciID string) *ci.TemplateMigrationItem {
		for i := range report.Items {
			if report.Items[i].CIID == ciID {
				return &report.Items[i]
			}
		}
		return nil
	}

	// The metamodel: a type, its attributes and templates with the
	// published versions 1 and 2 (and a draft 3 of the main template).
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "w712-host", Name: "w712-host", IsObservable: true}
	if err := types.Create(admin, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	define := func(key, dataType string, unique bool) citype.AttributeDefinition {
		t.Helper()
		d := citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: dataType, Unique: unique}
		if unique {
			d.UniqueScope = "org"
		}
		if err := types.CreateAttributeDefinition(admin, f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		return d
	}
	owner, needed, serial := define("w712_owner", "text", false), define("w712_needed", "text", false), define("w712_serial", "text", true)
	item := func(def citype.AttributeDefinition, defaultValue string) citype.TemplateVersionItem {
		it := citype.TemplateVersionItem{AttributeDefinitionID: def.ID}
		if defaultValue != "" {
			it.DefaultOverride = json.RawMessage(defaultValue)
		}
		return it
	}
	publish := func(tpl *citype.Template, version int, items []citype.TemplateVersionItem) {
		t.Helper()
		for i := range items {
			items[i].Position = i
		}
		if _, err := types.SetTemplateVersionItems(admin, f.OrgA, tpl.ID, version, items); err != nil {
			t.Fatalf("template %s v%d items: %v", tpl.Key, version, err)
		}
		if _, err := types.PublishTemplateVersion(admin, f.OrgA, tpl.ID, version); err != nil {
			t.Fatalf("publish %s v%d: %v", tpl.Key, version, err)
		}
	}
	required := true
	template := func(key string, v2 []citype.TemplateVersionItem) citype.Template {
		t.Helper()
		tpl := citype.Template{Key: key, CITypeID: typ.ID, IsActive: true}
		if err := types.CreateTemplate(admin, f.OrgA, &tpl); err != nil {
			t.Fatalf("template %s: %v", key, err)
		}
		publish(&tpl, 1, nil)
		if _, err := types.CreateTemplateVersion(admin, f.OrgA, tpl.ID); err != nil {
			t.Fatalf("template %s v2: %v", key, err)
		}
		publish(&tpl, 2, v2)
		return tpl
	}
	main := template("w712-main", []citype.TemplateVersionItem{item(owner, `"ops"`),
		{AttributeDefinitionID: needed.ID, RequiredOverride: &required}})
	if _, err := types.CreateTemplateVersion(admin, f.OrgA, main.ID); err != nil {
		t.Fatalf("draft v3: %v", err)
	}
	unique := template("w712-unique", []citype.TemplateVersionItem{item(serial, `"SER-712"`)})
	blocks := template("w712-blocks", []citype.TemplateVersionItem{item(owner, `"blocks"`)})
	retry := template("w712-retry", []citype.TemplateVersionItem{item(owner, `"retry"`)})
	queued := template("w712-queue", []citype.TemplateVersionItem{item(owner, `"queue"`)})

	newCI := func(name, client string, attrs map[string]any, tpl citype.Template) string {
		t.Helper()
		c := &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typ.ID, Name: name, Status: "active", Attributes: attrs}
		if err := chain.Create(admin, c); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if tpl.ID != "" {
			if _, err := f.Admin.Exec(bg, `INSERT INTO ci_template (organization_id, ci_id, template_id, version, position)
				VALUES ($1, $2, $3, 1, 0)`, f.OrgA, c.ID, tpl.ID); err != nil {
				t.Fatal(err)
			}
		}
		return c.ID
	}
	// seed inserts n CIs of client 1 with the assignment (tpl, 1).
	seed := func(prefix string, n int, tpl citype.Template) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `WITH c AS (INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
				SELECT $1, $2, $3, $4 || g, 'active' FROM generate_series(1, $5::int) g RETURNING id)
			INSERT INTO ci_template (organization_id, ci_id, template_id, version, position)
			SELECT $1, c.id, $6, 1, 0 FROM c RETURNING ci_id::text`, f.OrgA, f.Client1, typ.ID, prefix, n, tpl.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	onVersion := func(tpl citype.Template, version int, ids ...string) int {
		return count(`SELECT count(*) FROM ci_template WHERE template_id = $1 AND version = $2 AND ci_id = ANY($3::uuid[])`,
			tpl.ID, version, ids)
	}
	migrationRows := func(ids ...string) int {
		return count(`SELECT count(*) FROM ci_change WHERE change_type = 'template_migration' AND ci_id = ANY($1::uuid[])`, ids)
	}
	path := func(tpl citype.Template) string { return "/api/v1/templates/" + tpl.ID + "/migrations" }

	// The CIs of the main template: client 1 with an empty field, client 1
	// with an override, client 2.
	plain := newCI("w712-plain", f.Client1, nil, main)
	locked := newCI("w712-locked", f.Client1, nil, main)
	if _, err := chain.Update(admin, f.OrgA, locked, ci.UpdateRequest{Attributes: map[string]any{"w712_owner": "manual"},
		Manual: &ci.ManualChange{Author: f.User}}); err != nil {
		t.Fatalf("override: %v", err)
	}
	foreign := newCI("w712-foreign", f.Client2, nil, main)
	mainCIs := []string{plain, locked, foreign}

	t.Run("refusals", func(t *testing.T) {
		before := count(`SELECT count(*) FROM job WHERE organization_id = $1`, f.OrgA)
		valid := map[string]any{"from_version": 1, "to_version": 2}
		many := make([]string, ci.MaxTemplateMigrationIDs+1)
		for i := range many {
			many[i] = fmt.Sprintf("c0de7c00-0000-4000-8000-%012x", i)
		}
		for _, tc := range []struct {
			name, path string
			ctx        context.Context
			body       any
			status     int
		}{
			{"unknown template", "/api/v1/templates/c0de7c00-0000-4000-8000-00000000dead/migrations", orgCtx, valid, http.StatusNotFound},
			{"no template id", "/api/v1/templates/not-a-uuid/migrations", orgCtx, valid, http.StatusNotFound},
			{"target draft", path(main), orgCtx, map[string]any{"from_version": 1, "to_version": 3}, http.StatusUnprocessableEntity},
			{"source draft", path(main), orgCtx, map[string]any{"from_version": 3, "to_version": 2}, http.StatusUnprocessableEntity},
			{"same versions", path(main), orgCtx, map[string]any{"from_version": 2, "to_version": 2}, http.StatusUnprocessableEntity},
			{"unknown version", path(main), orgCtx, map[string]any{"from_version": 1, "to_version": 9}, http.StatusUnprocessableEntity},
			{"empty ci_ids", path(main), orgCtx, map[string]any{"from_version": 1, "to_version": 2, "ci_ids": []string{}}, http.StatusUnprocessableEntity},
			{"501 ci_ids", path(main), orgCtx, map[string]any{"from_version": 1, "to_version": 2, "ci_ids": many}, http.StatusUnprocessableEntity},
			{"ci_ids and where", path(main), orgCtx, map[string]any{"from_version": 1, "to_version": 2, "ci_ids": []string{plain},
				"where": map[string]any{"field": "name", "op": "eq", "value": "x"}}, http.StatusUnprocessableEntity},
			{"invalid where", path(main), orgCtx, map[string]any{"from_version": 1, "to_version": 2,
				"where": map[string]any{"field": "nope", "op": "eq", "value": "x"}}, http.StatusUnprocessableEntity},
			{"unknown field", path(main), orgCtx, `{"from_version":1,"to_version":2,"bogus":true}`, http.StatusBadRequest},
			{"without citype:manage", path(main), principal(orgUser, &orgScope, "", identity.PermCIRead, identity.PermCIWrite,
				identity.PermJobRead), valid, http.StatusForbidden},
		} {
			w := do(handler, tc.ctx, http.MethodPost, tc.path, "", tc.body)
			if w.Code != tc.status {
				t.Errorf("%s: %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.status)
			} else if tc.status != http.StatusForbidden && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("%s: content type %q, want problem+json", tc.name, w.Header().Get("Content-Type"))
			}
		}
		if w := do(mount(ci.NewTemplateMigrationJobs(chain, types)), orgCtx, http.MethodPost, path(main), "", valid); w.Code != http.StatusServiceUnavailable {
			t.Errorf("without the job service: %d %s, want 503", w.Code, w.Body.String())
		}
		if after := count(`SELECT count(*) FROM job WHERE organization_id = $1`, f.OrgA); after != before {
			t.Errorf("refused requests queued %d jobs", after-before)
		}
		if n := onVersion(main, 1, mainCIs...); n != 3 {
			t.Errorf("refused requests migrated CIs: %d of 3 still on version 1", n)
		}
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		changes := count(`SELECT count(*) FROM ci_change WHERE ci_id = ANY($1::uuid[])`, mainCIs)
		id := accepted(do(handler, clientCtx, http.MethodPost, path(main), "req-712-dry", map[string]any{"from_version": 1, "to_version": 2,
			"dry_run": true}))
		if j := waitTerminal(id); j.Status != jobs.StatusSucceeded {
			t.Fatalf("dry run job ended %s (%s)", j.Status, j.Error)
		}
		report := reportOf(clientCtx, id)
		if !report.DryRun || report.Totals.Selected != 2 || report.Totals.Migrated != 2 || report.Totals.WithFindings != 2 {
			t.Errorf("dry run report %+v", report)
		}
		if p := itemOf(&report, plain); p == nil || !slices.Equal(p.Defaults, []string{"w712_owner"}) {
			t.Errorf("dry run item of the plain CI: %+v", p)
		}
		if onVersion(main, 1, mainCIs...) != 3 || count(`SELECT count(*) FROM ci_change WHERE ci_id = ANY($1::uuid[])`, mainCIs) != changes {
			t.Error("the dry run wrote")
		}
	})

	t.Run("client-scoped trigger in process", func(t *testing.T) {
		id := accepted(do(handler, clientCtx, http.MethodPost, path(main), "req-712-main", map[string]any{"from_version": 1, "to_version": 2}))
		if j := waitTerminal(id); j.Status != jobs.StatusSucceeded || j.Progress != 100 {
			t.Fatalf("job ended %s (%s), progress %d", j.Status, j.Error, j.Progress)
		}
		if onVersion(main, 2, plain, locked) != 2 || onVersion(main, 1, foreign) != 1 {
			t.Fatalf("migrated: %d of the client-1 CIs on v2, the client-2 CI on v1: %d; want 2 and 1",
				onVersion(main, 2, plain, locked), onVersion(main, 1, foreign))
		}
		report := reportOf(clientCtx, id)
		if report.DryRun || report.Truncated || report.Totals != (ci.TemplateMigrationTotals{Selected: 2, Migrated: 2, WithFindings: 2}) ||
			len(report.Items) != 2 {
			t.Fatalf("report %+v", report)
		}
		p, l := itemOf(&report, plain), itemOf(&report, locked)
		if p == nil || p.Outcome != ci.TemplateMigrated || !slices.Equal(p.Defaults, []string{"w712_owner"}) || len(p.Overridden) != 0 ||
			len(p.Findings) != 1 || p.Findings[0].Field != "w712_needed" {
			t.Errorf("item of the plain CI: %+v", p)
		}
		if l == nil || len(l.Defaults) != 0 || !slices.Equal(l.Overridden, []string{"w712_owner"}) || len(l.Findings) != 1 {
			t.Errorf("item of the overridden CI: %+v", l)
		}
		if itemOf(&report, foreign) != nil {
			t.Error("the report names the CI of client 2")
		}
		var actor, requestID, correlation, source string
		if err := f.Admin.QueryRow(bg, `SELECT actor_id::text, request_id, correlation_id, source FROM ci_change
			WHERE ci_id = $1 AND change_type = 'template_migration'`, plain).Scan(&actor, &requestID, &correlation, &source); err != nil {
			t.Fatal(err)
		}
		if actor != clientUser || requestID != "req-712-main" || correlation != id || source != "api" {
			t.Errorf("ci_change: actor %s, request %s, correlation %s, source %s; want %s, req-712-main, %s, api",
				actor, requestID, correlation, source, clientUser, id)
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.template_migrated'
			AND actor_id = $2 AND request_id = 'req-712-main' AND correlation_id = $3`, plain, clientUser, id); n != 1 {
			t.Errorf("%d audit entries ci.template_migrated of the trigger with request and job, want 1", n)
		}
		if n := count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'template_migration' AND comment::jsonb->>'job_id' = $2`,
			plain, id); n != 1 {
			t.Errorf("%d ci_change rows name the job, want 1", n)
		}
	})

	t.Run("unique conflict fails only its CI", func(t *testing.T) {
		newCI("w712-holder", f.Client1, map[string]any{"w712_serial": "SER-712"}, citype.Template{})
		clash := newCI("w712-clash", f.Client1, nil, unique)
		own := newCI("w712-own", f.Client1, map[string]any{"w712_serial": "own"}, unique)
		other := newCI("w712-other", f.Client1, nil, citype.Template{})
		id := accepted(do(handler, orgCtx, http.MethodPost, path(unique), "req-712-unique", map[string]any{"from_version": 1,
			"to_version": 2, "ci_ids": []string{clash, own, strings.ToUpper(other)}}))
		if j := waitTerminal(id); j.Status != jobs.StatusSucceeded {
			t.Fatalf("job ended %s (%s)", j.Status, j.Error)
		}
		report := reportOf(orgCtx, id)
		if report.Totals != (ci.TemplateMigrationTotals{Selected: 2, Migrated: 1, Skipped: 1, Failed: 1}) {
			t.Errorf("totals %+v", report.Totals)
		}
		if c := itemOf(&report, clash); c == nil || c.Outcome != ci.TemplateMigrationFailed || c.Error == nil ||
			c.Error.Code != "unique" || c.Error.Field != "w712_serial" {
			t.Errorf("item of the clashing CI: %+v", c)
		}
		if o := itemOf(&report, own); o == nil || o.Outcome != ci.TemplateMigrated {
			t.Errorf("item of the other CI of the block: %+v", o)
		}
		if s := itemOf(&report, other); s == nil || s.Outcome != ci.TemplateSkipped {
			t.Errorf("item of the id without the assignment: %+v", s)
		}
		if onVersion(unique, 1, clash) != 1 || onVersion(unique, 2, own) != 1 || migrationRows(clash) != 0 {
			t.Error("the clashing CI was migrated or the other one was not")
		}
	})

	t.Run("no scope snapshot", func(t *testing.T) {
		lone := newCI("w712-lone", f.Client1, nil, retry)
		job, _, err := jobSvc.Enqueue(bg, f.OrgA, ci.TemplateMigrateJobKind, map[string]any{"template_id": retry.ID,
			"from_version": 1, "to_version": 2, "ci_ids": []string{lone}}, jobs.EnqueueOptions{CreatedBy: orgUser})
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := jobSvc.ClaimJob(bg, f.OrgA, job.ID)
		if err != nil || claimed == nil || claimed.Scope != nil {
			t.Fatalf("claim: %+v %v", claimed, err)
		}
		if _, err = migrations.RunJob(bg, claimed); !errors.Is(err, jobs.ErrNoScopeSnapshot) {
			t.Errorf("run without snapshot: %v, want ErrNoScopeSnapshot", err)
		}
		if onVersion(retry, 1, lone) != 1 || migrationRows(lone) != 0 {
			t.Error("a job without scope snapshot migrated")
		}
		if _, err = jobSvc.Cancel(bg, f.OrgA, job.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Admin.Exec(bg, `DELETE FROM ci_template WHERE ci_id = $1`, lone); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("job queue", func(t *testing.T) {
		ids := []string{newCI("w712-q1", f.Client1, nil, queued), newCI("w712-q2", f.Client1, nil, queued)}
		queue := jobs.NewQueue(jobs.NewService(jobs.NewPGRepository(f.App)), nil)
		onQueue := ci.NewTemplateMigrationJobs(chain, types).WithJobs(jobSvc).WithQueue(queue)
		queue.Handle(templateMigrateKind, onQueue.RunJob)
		id := accepted(do(mount(onQueue), orgCtx, http.MethodPost, path(queued), "req-712-queue", map[string]any{"from_version": 1, "to_version": 2}))
		time.Sleep(50 * time.Millisecond)
		if j, _ := jobSvc.Get(bg, f.OrgA, id); j.Status != jobs.StatusQueued || onVersion(queued, 1, ids...) != 2 {
			t.Fatalf("submitted job %+v ran without the queue", j)
		}
		// A running job of the kind is cancellable.
		probe, _, err := jobSvc.Enqueue(orgCtx, f.OrgA, ci.TemplateMigrateJobKind, map[string]any{}, jobs.EnqueueOptions{CreatedBy: orgUser})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = jobSvc.ClaimJob(bg, f.OrgA, probe.ID); err != nil {
			t.Fatal(err)
		}
		var running struct {
			Status      jobs.Status `json:"status"`
			Cancellable bool        `json:"cancellable"`
		}
		if w := do(handler, orgCtx, http.MethodGet, "/api/v1/jobs/"+probe.ID, "", nil); json.Unmarshal(w.Body.Bytes(), &running) != nil ||
			running.Status != jobs.StatusRunning || !running.Cancellable {
			t.Errorf("GET /jobs/{id} of a running migration: %d %s, want cancellable: true", w.Code, w.Body.String())
		}
		if _, err = jobSvc.Cancel(bg, f.OrgA, probe.ID); err != nil {
			t.Errorf("cancel of a running migration: %v", err)
		}

		data, _ := json.Marshal(jobs.Message{OrganizationID: f.OrgA, JobID: id, Kind: ci.TemplateMigrateJobKind})
		d := &fakeDelivery{data: data}
		if out := queue.Process(bg, d); !out.Ran || !d.acked {
			t.Fatalf("queue: %+v", out)
		}
		if j := waitTerminal(id); j.Status != jobs.StatusSucceeded {
			t.Fatalf("job on the queue ended %s (%s)", j.Status, j.Error)
		}
		if report := reportOf(orgCtx, id); report.Totals.Migrated != 2 || onVersion(queued, 2, ids...) != 2 {
			t.Errorf("queue run: report %+v", report.Totals)
		}
	})

	t.Run("job queue on NATS", func(t *testing.T) {
		url := os.Getenv("TEST_NATS_URL")
		if url == "" {
			url = os.Getenv("NATS_URL")
		}
		if url == "" {
			t.Skip("TEST_NATS_URL not set")
		}
		nc, err := nats.Connect(url)
		if err != nil {
			t.Fatalf("connect NATS: %v", err)
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatal(err)
		}
		queue := jobs.NewQueue(jobs.NewService(jobs.NewPGRepository(f.App)), js)
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		queue.Stream, queue.SubjectPrefix = "JOBS_W712_"+suffix, "w712_"+suffix+"."
		if err = queue.Setup(bg); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = js.DeleteStream(bg, queue.Stream) }()
		onQueue := ci.NewTemplateMigrationJobs(chain, types).WithJobs(jobSvc).WithQueue(queue)
		queue.Handle(templateMigrateKind, onQueue.RunJob)
		runCtx, stop := context.WithCancel(bg)
		defer stop()
		go queue.Run(runCtx)
		tpl := template("w712-nats", []citype.TemplateVersionItem{item(owner, `"nats"`)})
		ids := []string{newCI("w712-n1", f.Client1, nil, tpl), newCI("w712-n2", f.Client1, nil, tpl)}
		id := accepted(do(mount(onQueue), orgCtx, http.MethodPost, path(tpl), "req-712-nats", map[string]any{"from_version": 1, "to_version": 2}))
		if j := waitTerminal(id); j.Status != jobs.StatusSucceeded || onVersion(tpl, 2, ids...) != 2 {
			t.Fatalf("job over NATS ended %s (%s)", j.Status, j.Error)
		}
	})

	t.Run("cancel between blocks", func(t *testing.T) {
		ids := seed("w712-block-", 150, blocks)
		var jobID atomic.Value
		flaky := &flakyRepository{Repository: chain, afterCall: 2}
		flaky.after = func() {
			if _, err := jobSvc.Cancel(bg, f.OrgA, jobID.Load().(string)); err != nil {
				t.Errorf("cancel after the first block: %v", err)
			}
		}
		m := ci.NewTemplateMigrationJobs(flaky, types).WithJobs(jobSvc)
		job, _, err := jobSvc.Enqueue(orgCtx, f.OrgA, ci.TemplateMigrateJobKind, map[string]any{"template_id": blocks.ID,
			"from_version": 1, "to_version": 2}, jobs.EnqueueOptions{CreatedBy: orgUser})
		if err != nil {
			t.Fatal(err)
		}
		jobID.Store(job.ID)
		claimed, err := jobSvc.ClaimJob(bg, f.OrgA, job.ID)
		if err != nil || claimed == nil {
			t.Fatalf("claim: %v", err)
		}
		if result, rerr := m.RunJob(bg, claimed); rerr != nil || result != nil {
			t.Fatalf("cancelled run: %v %v, want no result and no error", result, rerr)
		}
		if n := onVersion(blocks, 2, ids...); n != 100 {
			t.Errorf("%d CIs migrated, want the first block of 100", n)
		}
		if n := migrationRows(ids...); n != 100 {
			t.Errorf("%d ci_change rows, want 100", n)
		}
		if w := do(handler, orgCtx, http.MethodGet, "/api/v1/jobs/"+job.ID+"/result", "", nil); w.Code != http.StatusConflict {
			t.Errorf("result of the cancelled job: %d %s, want 409", w.Code, w.Body.String())
		}
	})

	t.Run("retry after a failed block writes nothing twice", func(t *testing.T) {
		ids := seed("w712-retry-", 150, retry)
		flaky := &flakyRepository{Repository: chain}
		flaky.failCall.Store(3) // 1: the count, 2: the first block, 3: the second block
		m := ci.NewTemplateMigrationJobs(flaky, types).WithJobs(jobSvc)
		job, _, err := jobSvc.Enqueue(orgCtx, f.OrgA, ci.TemplateMigrateJobKind, map[string]any{"template_id": retry.ID,
			"from_version": 1, "to_version": 2}, jobs.EnqueueOptions{CreatedBy: orgUser})
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := jobSvc.ClaimJob(bg, f.OrgA, job.ID)
		if err != nil || claimed == nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err = m.RunJob(bg, claimed); err == nil {
			t.Fatal("the run with a failing second block succeeded")
		}
		if n := onVersion(retry, 2, ids...); n != 100 {
			t.Fatalf("%d CIs migrated after the failed attempt, want the first block of 100", n)
		}
		failed, err := jobSvc.Fail(bg, f.OrgA, job.ID, errors.New("wp712: block transaction lost"))
		if err != nil || failed.Status != jobs.StatusQueued {
			t.Fatalf("failed attempt: %+v %v, want queued for a retry", failed, err)
		}
		if _, err = f.Admin.Exec(bg, `UPDATE job SET run_after = now() WHERE id = $1`, job.ID); err != nil {
			t.Fatal(err)
		}
		again, err := jobSvc.ClaimJob(bg, f.OrgA, job.ID)
		if err != nil || again == nil {
			t.Fatalf("claim the retry: %+v %v", again, err)
		}
		result, err := m.RunJob(bg, again)
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		report, _ := result.(*ci.TemplateMigrationReport)
		if report == nil || report.Totals.Selected != 50 || report.Totals.Migrated != 50 {
			t.Errorf("retry report %+v, want the 50 remaining CIs", report)
		}
		if n := onVersion(retry, 2, ids...); n != 150 {
			t.Errorf("%d of 150 CIs migrated after the retry", n)
		}
		if n := count(`SELECT count(*) FROM (SELECT ci_id FROM ci_change WHERE change_type = 'template_migration'
			AND ci_id = ANY($1::uuid[]) GROUP BY ci_id HAVING count(*) = 1) x`, ids); n != 150 {
			t.Errorf("%d of 150 CIs with exactly one ci_change template_migration", n)
		}
	})
}
