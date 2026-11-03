package metamodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// testAPI is draftAPI with the job service of the test run (WP-721) and
// the job routes.
type testAPI struct {
	*draftAPI
	jobs *jobs.Service
}

func newTestAPI(t *testing.T, f *scopetest.Fixture) *testAPI {
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	svc.Register(jobs.Kind{Name: metamodel.TestJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true})
	mux := chi.NewRouter()
	citype.NewHandler(citype.NewPGRepository(f.App)).RegisterRoutes(mux)
	metamodel.NewHandler(metamodel.NewPGRepository(f.App)).WithJobs(svc).RegisterRoutes(mux)
	jobs.NewAPIHandler(svc).WithKind(metamodel.TestJobKind, identity.PermMetamodelPublish, nil).RegisterRoutes(mux)
	return &testAPI{draftAPI: &draftAPI{t: t, f: f, mux: mux, user: f.AppUser(t, f.OrgA, "wp721-admin")}, jobs: svc}
}

// start posts …/test and returns the job id.
func (a *testAPI) start(draftID string) string {
	a.t.Helper()
	w := a.call(a.f.OrgA, http.MethodPost, "/api/v1/metamodel/drafts/"+draftID+"/test", "")
	if w.Code != http.StatusAccepted {
		a.t.Fatalf("POST test of %s: %d %s", draftID, w.Code, w.Body.String())
	}
	var out struct {
		JobID string `json:"job_id"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Kind != metamodel.TestJobKind ||
		w.Header().Get("Location") != "/api/v1/jobs/"+out.JobID {
		a.t.Fatalf("POST test: %s (Location %s)", w.Body.String(), w.Header().Get("Location"))
	}
	return out.JobID
}

// wait waits until the job ended and returns it.
func (a *testAPI) wait(jobID string) *jobs.Job {
	a.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		job, err := a.jobs.Get(a.f.OrgCtx(a.f.OrgA), a.f.OrgA, jobID)
		if err != nil {
			a.t.Fatal(err)
		}
		if job.Status != jobs.StatusQueued && job.Status != jobs.StatusRunning {
			return job
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("test job %s still %s", jobID, job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// run tests the draft, waits for the job and returns the report with all
// violations (paged through GET …/test).
func (a *testAPI) run(draftID string) (*metamodel.TestReport, *jobs.Job) {
	a.t.Helper()
	job := a.wait(a.start(draftID))
	if job.Status != jobs.StatusSucceeded {
		a.t.Fatalf("test job: %s %s", job.Status, job.Error)
	}
	return a.report(draftID, 2), job
}

// report reads the report with its violations, limit per page.
func (a *testAPI) report(draftID string, limit int) *metamodel.TestReport {
	a.t.Helper()
	var out *metamodel.TestReport
	path := "/api/v1/metamodel/drafts/" + draftID + "/test?limit=" + itoa(limit)
	for pages := 0; ; pages++ {
		var page metamodel.TestReportPage
		a.must(http.StatusOK, a.f.OrgA, http.MethodGet, path, "", &page)
		if out == nil {
			r := page.TestReport
			out = &r
		}
		if page.Total != out.Totals.Violations || len(page.Data) > limit {
			a.t.Fatalf("page: total %d (counted %d), %d rows for limit %d", page.Total, out.Totals.Violations, len(page.Data), limit)
		}
		out.Violations = append(out.Violations, page.Data...)
		if page.NextCursor == "" {
			break
		}
		if pages > 1000 {
			a.t.Fatal("no end of the pages")
		}
		path = "/api/v1/metamodel/drafts/" + draftID + "/test?limit=" + itoa(limit) + "&cursor=" + page.NextCursor
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// violations lists "<ci>/<field>/<code>" of a report, sorted.
func violations(r *metamodel.TestReport, names map[string]string) []string {
	var out []string
	for _, v := range r.Violations {
		name := names[v.CIID]
		if name == "" {
			name = v.CIID
		}
		out = append(out, name+"/"+v.Field+"/"+v.Code)
	}
	sort.Strings(out)
	return out
}

// ci inserts a CI of type typeID with attributes and returns its id.
func ci(t *testing.T, f *scopetest.Fixture, typeID, clientID, name string, attributes map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(attributes)
	if err != nil {
		t.Fatal(err)
	}
	id := f.ID()
	if _, err = f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, attributes)
		VALUES ($1, $2, $3, $4, $5, 'active', $6)`, id, f.OrgA, clientID, typeID, name, raw); err != nil {
		t.Fatalf("insert ci %s: %v", name, err)
	}
	return id
}

// permissionsOf are the permissions of a standard role.
func permissionsOf(t *testing.T, role string) []identity.Permission {
	t.Helper()
	keys, ok := permission.StandardRolePermissions(role)
	if !ok {
		t.Fatalf("no standard role %s", role)
	}
	out := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		out = append(out, identity.Permission(k))
	}
	return out
}

// authorized runs a request of org A through the authorization of the
// route pattern (server.AuthorizeRoute) with the permissions perms.
func (a *testAPI) authorized(perms []identity.Permission, method, pattern, path string) *httptest.ResponseRecorder {
	a.t.Helper()
	scope := database.OrgWideScope(a.f.OrgA, a.user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: a.f.OrgA, UserID: a.user})
	ctx = identity.WithPrincipal(identity.WithPermissions(ctx, perms),
		identity.Principal{Subject: a.user, OrganizationID: a.f.OrgA, Permissions: perms, Type: identity.PrincipalTypeUser})
	w := httptest.NewRecorder()
	server.AuthorizeRoute(method, pattern)(a.mux).ServeHTTP(w, httptest.NewRequest(method, path, nil).WithContext(ctx))
	return w
}

// TestMetamodelDraftTestRunAgainstPostgres covers MET-53 (WP-721): the test
// run of a draft against the inventory as a job (metamodel.test). A new
// required field and a narrowed enum report the CIs of every client; a
// change of a parent type reports the CIs of its subtype; the instance
// definitions of a CI take part (a conflict with them is a violation of the
// CI); the draft becomes tested. A MET-16 conflict (empty enum) blocks and
// the draft stays draft. A change of a set reports the CIs of every type it
// is assigned to, a new required rule the CIs of its type, a template item
// no CI. A second POST during the run answers the same job; a change of the
// draft during the run leaves it draft; a published draft is 409. The
// violations stand only in GET …/test (metamodel:publish): the job, its
// result and the draft carry the counters; engineer (job:read, without
// metamodel:publish) gets 403 on GET …/test, client_technician does not see
// the job.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMetamodelDraftTestRunAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e2")
	a := newTestAPI(t, f)
	bg := context.Background()
	orgA := f.OrgA

	// ── Fixtures: types, fields, a set, CIs of two clients ───────────────
	parent := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp721_parent","name":"wp721 parent"}`)
	child := a.id(orgA, http.MethodPost, "/api/v1/ci-types",
		`{"key":"wp721_child","name":"wp721 child","parent_type_id":"`+parent+`"}`)
	setType := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp721_set_type","name":"wp721 set type"}`)
	otherType := a.id(orgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp721_other","name":"wp721 other"}`)
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-types/"+parent+"/fields",
		`{"name":"tier","data_type":"enum","enum_values":["gold","silver","bronze"]}`, nil)
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-fields/global",
		`{"name":"wp721_region","data_type":"enum","enum_values":["eu","us"]}`, nil)
	set := a.id(orgA, http.MethodPost, "/api/v1/attribute-sets", `{"key":"wp721_set"}`)
	rack := a.id(orgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"attribute_set_id":"`+set+`","key":"wp721_rack","data_type":"text"}`)
	for _, typ := range []string{setType, otherType} {
		a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/ci-types/"+typ+"/attribute-sets",
			`{"attribute_sets":[{"attribute_set_id":"`+set+`","position":0}]}`, nil)
	}
	tpl := a.id(orgA, http.MethodPost, "/api/v1/templates", `{"key":"wp721_tpl","ci_type_id":"`+parent+`"}`)
	var tierDef, v1 string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM attribute_definition WHERE ci_type_id = $1 AND key = 'tier'`, parent).
		Scan(&tierDef); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM template_version WHERE template_id = $1 AND version = 1`, tpl).
		Scan(&v1); err != nil {
		t.Fatal(err)
	}

	names := map[string]string{}
	add := func(name, typ, client string, attrs map[string]any) string {
		id := ci(t, f, typ, client, name, attrs)
		names[id] = name
		return id
	}
	add("p1", parent, f.Client1, map[string]any{"tier": "gold"})
	p2 := add("p2", parent, f.Client2, map[string]any{"tier": "bronze"})
	add("c1", child, f.Client1, map[string]any{"tier": "bronze"})
	i1 := add("i1", parent, f.Client1, map[string]any{"tier": "bronze", "owner": "ops"})
	add("s1", setType, f.Client2, map[string]any{})
	add("o1", otherType, f.Client1, map[string]any{"wp721_rack": "R1"})
	deleted := add("d1", parent, f.Client1, map[string]any{})
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	// The instance definition of i1 narrows tier to bronze (MET-14).
	a.must(http.StatusOK, orgA, http.MethodPut, "/api/v1/cis/"+i1+"/field-definitions",
		`{"name":"tier","data_type":"enum","enum_values":["bronze"]}`, nil)

	// ── A new required field and a narrowed enum on the parent type ──────
	d := a.newDraft(orgA, "wp721 required and enum")
	for _, body := range []string{
		`{"kind":"ci_type_field","op":"upsert","target_id":"` + parent + `","payload":{"name":"owner","data_type":"text","required":true}}`,
		`{"kind":"ci_type_field","op":"upsert","target_id":"` + parent + `","payload":{"name":"tier","data_type":"enum","enum_values":["gold","silver"]}}`,
	} {
		if w := a.addItem(orgA, d.ID, body); w.Code != http.StatusCreated {
			t.Fatalf("item: %d %s", w.Code, w.Body.String())
		}
	}
	report, job := a.run(d.ID)
	want := []string{
		"c1/owner/", "c1/tier/", // the subtype of the changed type
		"i1/tier/enum-empty", // the instance definition of i1 meets the narrowed enum
		"p1/owner/",
		"p2/owner/", "p2/tier/", // a CI of the second client
	}
	if got := violations(report, names); !slices.Equal(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
	if report.Blocked || report.State != metamodel.TestStateCompleted || report.Totals.CIs != 4 || report.Totals.InvalidCIs != 4 ||
		report.Totals.Violations != len(want) || report.Truncated {
		t.Errorf("report = %+v", report.Summary())
	}
	for _, v := range report.Violations {
		if strings.Contains(v.Message, "bronze") {
			t.Errorf("a violation quotes the value: %+v", v)
		}
	}
	counted := map[string]int{}
	for _, c := range report.Types {
		for _, attr := range c.Attributes {
			counted[c.CITypeID+"/"+attr.Key] = attr.Violations
		}
	}
	if counted[parent+"/owner"] != 2 || counted[parent+"/tier"] != 2 || counted[child+"/owner"] != 1 || counted[child+"/tier"] != 1 {
		t.Errorf("counters per type and attribute = %v", counted)
	}
	if got := a.draft(orgA, d.ID); got.Status != metamodel.StatusTested || got.Version != d.Version+2 {
		t.Errorf("after the test: status %s version %d", got.Status, got.Version)
	}

	// ── Confidential report ──────────────────────────────────────────────
	ciIDs := slices.Collect(func(yield func(string) bool) {
		for id := range names {
			if !yield(id) {
				return
			}
		}
	})
	leaks := func(where, body string) {
		t.Helper()
		for _, id := range ciIDs {
			if strings.Contains(body, id) {
				t.Errorf("%s names the CI %s: %s", where, names[id], body)
			}
		}
	}
	engineer := permissionsOf(t, "engineer")
	if !identity.HasPermission(engineer, identity.PermJobRead) || identity.HasPermission(engineer, identity.PermMetamodelPublish) {
		t.Fatal("engineer: want job:read without metamodel:publish (E-106)")
	}
	leaks("the job result", string(job.Result))
	for _, path := range []string{"/api/v1/jobs", "/api/v1/jobs/" + job.ID, "/api/v1/jobs/" + job.ID + "/result"} {
		w := a.authorized(engineer, http.MethodGet, strings.Replace(path, job.ID, "{id}", 1), path)
		if w.Code != http.StatusOK {
			t.Errorf("engineer GET %s: %d %s", path, w.Code, w.Body.String())
		}
		leaks("engineer GET "+path, w.Body.String())
	}
	if w := a.authorized(engineer, http.MethodGet, "/api/v1/metamodel/drafts/{id}/test",
		"/api/v1/metamodel/drafts/"+d.ID+"/test"); w.Code != http.StatusForbidden {
		t.Errorf("engineer GET …/test = %d, want 403", w.Code)
	}
	// A role with citype:manage but without metamodel:publish: the draft
	// with the counters only, the report 403.
	manager := []identity.Permission{identity.PermCITypeManage, identity.PermCIRead}
	w := a.authorized(manager, http.MethodGet, "/api/v1/metamodel/drafts/{id}", "/api/v1/metamodel/drafts/"+d.ID)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"violations":`+itoa(len(want))) {
		t.Errorf("draft for citype:manage: %d %s", w.Code, w.Body.String())
	}
	leaks("GET draft", w.Body.String())
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w = a.authorized(manager, method, "/api/v1/metamodel/drafts/{id}/test",
			"/api/v1/metamodel/drafts/"+d.ID+"/test"); w.Code != http.StatusForbidden {
			t.Errorf("citype:manage %s …/test = %d, want 403", method, w.Code)
		}
	}
	leaks("the draft list", a.must(http.StatusOK, orgA, http.MethodGet, "/api/v1/metamodel/drafts", "", nil).Body.String())
	var audits []byte
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(jsonb_agg(changes), '[]') FROM audit_log WHERE resource_id = $1`, d.ID).
		Scan(&audits); err != nil {
		t.Fatal(err)
	}
	leaks("the audit of the draft", string(audits))
	// client_technician (scoped to a client) does not see the job of the
	// org-wide org_admin (000154).
	if _, err := a.jobs.Get(f.ClientCtx(f.Client1), orgA, job.ID); err == nil {
		t.Error("a client-scoped principal sees the test job")
	}
	// The report pages (limit 2 above); a forged cursor is 400.
	if w := a.call(orgA, http.MethodGet, "/api/v1/metamodel/drafts/"+d.ID+"/test?cursor=forged", ""); w.Code != http.StatusBadRequest {
		t.Errorf("forged cursor = %d, want 400", w.Code)
	}

	// ── A MET-16 conflict blocks: empty enum of the global and the type ──
	conflict := a.newDraft(orgA, "wp721 conflict")
	if w := a.addItem(orgA, conflict.ID, `{"kind":"ci_type_field","op":"upsert","target_id":"`+parent+
		`","payload":{"name":"wp721_region","data_type":"enum","enum_values":["apac"]}}`); w.Code != http.StatusCreated {
		t.Fatalf("conflict item: %d %s", w.Code, w.Body.String())
	}
	report, _ = a.run(conflict.ID)
	if !report.Blocked || len(report.Conflicts) == 0 || report.Totals.CIs != 0 || len(report.Violations) != 0 {
		t.Errorf("conflict report = %+v", report)
	}
	for _, c := range report.Conflicts {
		if c.Code != citype.CodeEnumEmpty || c.Key != "wp721_region" || (c.CITypeID != parent && c.CITypeID != child) {
			t.Errorf("conflict = %+v", c)
		}
	}
	if got := a.draft(orgA, conflict.ID); got.Status != metamodel.StatusDraft {
		t.Errorf("blocked draft: status %s, want draft", got.Status)
	}

	// ── A set change reports the CIs of every type it is assigned to ─────
	setDraft := a.newDraft(orgA, "wp721 set")
	if w := a.addItem(orgA, setDraft.ID, `{"kind":"attribute_definition","op":"update","target_id":"`+rack+
		`","payload":{"required":true}}`); w.Code != http.StatusCreated {
		t.Fatalf("set item: %d %s", w.Code, w.Body.String())
	}
	report, _ = a.run(setDraft.ID)
	if got := violations(report, names); !slices.Equal(got, []string{"s1/wp721_rack/"}) || report.Totals.CIs != 2 {
		t.Errorf("set change: %v (%+v)", got, report.Totals)
	}

	// ── A new required rule reports the CIs of its type ──────────────────
	ruleDraft := a.newDraft(orgA, "wp721 rule")
	if w := a.addItem(orgA, ruleDraft.ID, `{"kind":"rule","op":"create","payload":{"key":"wp721_need_serial","ci_type_id":"`+
		otherType+`","attribute_key":"serial","kind":"required"}}`); w.Code != http.StatusCreated {
		t.Fatalf("rule item: %d %s", w.Code, w.Body.String())
	}
	report, _ = a.run(ruleDraft.ID)
	if got := violations(report, names); !slices.Equal(got, []string{"o1/serial/required"}) || report.Violations[0].Rule != "wp721_need_serial" {
		t.Errorf("rule: %v %+v", got, report.Violations)
	}

	// ── A template item is checked for composition only ──────────────────
	tplDraft := a.newDraft(orgA, "wp721 template")
	if w := a.addItem(orgA, tplDraft.ID, `{"kind":"template_version","op":"update","target_id":"`+v1+
		`","payload":{"items":[{"attribute_definition_id":"`+tierDef+`","position":0,"required_override":true}]}}`); w.Code != http.StatusCreated {
		t.Fatalf("template item: %d %s", w.Code, w.Body.String())
	}
	report, _ = a.run(tplDraft.ID)
	if report.Blocked || report.Totals.CIs != 0 || len(report.Violations) != 0 {
		t.Errorf("template item: %+v", report)
	}
	if got := a.draft(orgA, tplDraft.ID); got.Status != metamodel.StatusTested {
		t.Errorf("template draft: status %s", got.Status)
	}
	// A template version item that empties the enum blocks.
	tplConflict := a.newDraft(orgA, "wp721 template conflict")
	if w := a.addItem(orgA, tplConflict.ID, `{"kind":"template_version","op":"update","target_id":"`+v1+
		`","payload":{"items":[{"attribute_definition_id":"`+tierDef+`","position":0,"validation_override":{"op":"enum","values":["platinum"]}}]}}`); w.Code != http.StatusCreated {
		t.Fatalf("template conflict item: %d %s", w.Code, w.Body.String())
	}
	report, _ = a.run(tplConflict.ID)
	if !report.Blocked || len(report.Conflicts) != 1 || report.Conflicts[0].TemplateVersionID != v1 {
		t.Errorf("template conflict: %+v", report)
	}

	// ── A second POST during the run answers the same job; a change of the
	//    draft during the run leaves it draft ───────────────────────────
	running := a.newDraft(orgA, "wp721 running")
	if w := a.addItem(orgA, running.ID, `{"kind":"ci_type_field","op":"upsert","target_id":"`+otherType+
		`","payload":{"name":"wp721_note","data_type":"text"}}`); w.Code != http.StatusCreated {
		t.Fatalf("running item: %d %s", w.Code, w.Body.String())
	}
	lock, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	// The job waits for the set assignments it reads first.
	if _, err = lock.Exec(bg, `LOCK TABLE ci_type_attribute_set IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	first := a.start(running.ID)
	if second := a.start(running.ID); second != first {
		t.Errorf("second POST during the run: job %s, want %s", second, first)
	}
	a.must(http.StatusOK, orgA, http.MethodPatch, "/api/v1/metamodel/drafts/"+running.ID, `{"name":"wp721 renamed while running"}`, nil)
	if err = lock.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if job = a.wait(first); job.Status != jobs.StatusSucceeded {
		t.Fatalf("running job: %s %s", job.Status, job.Error)
	}
	if got := a.draft(orgA, running.ID); got.Status != metamodel.StatusDraft {
		t.Errorf("draft changed during the run: status %s, want draft", got.Status)
	}
	if report = a.report(running.ID, 50); !report.Outdated {
		t.Errorf("report of a draft changed during the run: %+v", report)
	}
	// After the run a new POST starts a new job.
	if again := a.start(running.ID); again == first {
		t.Error("POST after the run answered the finished job")
	} else {
		a.wait(again)
	}

	// ── Published drafts are 409, unknown ones 404 ───────────────────────
	if _, err = f.Admin.Exec(bg, `UPDATE metamodel_draft SET status = 'published', published_at = now() WHERE id = $1`, running.ID); err != nil {
		t.Fatal(err)
	}
	if w := a.call(orgA, http.MethodPost, "/api/v1/metamodel/drafts/"+running.ID+"/test", ""); w.Code != http.StatusConflict {
		t.Errorf("test of a published draft = %d, want 409", w.Code)
	}
	if w := a.call(orgA, http.MethodGet, "/api/v1/metamodel/drafts/"+f.ID()+"/test", ""); w.Code != http.StatusNotFound {
		t.Errorf("report of an unknown draft = %d, want 404", w.Code)
	}
	// An item change drops the report (WP-260): 404.
	if w := a.addItem(orgA, setDraft.ID, `{"kind":"attribute_set","op":"update","target_id":"`+set+
		`","payload":{"description":"x"}}`); w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	if w := a.call(orgA, http.MethodGet, "/api/v1/metamodel/drafts/"+setDraft.ID+"/test", ""); w.Code != http.StatusNotFound {
		t.Errorf("report after an item change = %d, want 404", w.Code)
	}
	_ = p2
}

// TestMetamodelDraftTestWithoutDatabase: without a database both test
// routes answer 503, without the job service POST …/test as well.
func TestMetamodelDraftTestWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	metamodel.NewHandler(nil).RegisterRoutes(mux)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/metamodel/drafts/x/test", nil).WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s …/test without database: %d, want 503", method, w.Code)
		}
	}
	job, err := metamodel.NewHandler(nil).RunTestJob(context.Background(), &jobs.Job{OrganizationID: "org-1", Params: []byte(`{}`)})
	if err == nil || job != nil {
		t.Errorf("queued test without database: %v %v", job, err)
	}
}
