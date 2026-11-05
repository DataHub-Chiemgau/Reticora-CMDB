// POST /api/v1/assets/bulk without a database (WP-781): the validation of
// the whole request (action, status, params, 1–500 ids, the job from 100
// ids), the rights of each action before any change, the ports a profile
// without database lacks (503) and the per-id report on the
// MemoryRepository.
package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

const (
	bulkOrg  = "00000000-0000-4000-8000-0000000007a1"
	bulkUser = "00000000-0000-4000-8000-0000000007a2"
)

// fakeDelegates records the calls of the delegating ports.
type fakeDelegates struct{ calls int }

func (f *fakeDelegates) Transfer(context.Context, string, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeDelegates) Assign(context.Context, string, string, *asset.BulkAssignment) error {
	f.calls++
	return nil
}

func (f *fakeDelegates) Transition(context.Context, string, string, *ci.BulkTransition) error {
	f.calls++
	return nil
}

// bulkEnv is the asset handler on the MemoryRepository.
type bulkEnv struct {
	t    *testing.T
	repo *asset.MemoryRepository
	h    *asset.Handler
	mux  *chi.Mux
}

func newBulkEnv(t *testing.T) *bulkEnv {
	t.Helper()
	e := &bulkEnv{t: t, repo: asset.NewMemoryRepository()}
	e.h = asset.NewHandler(e.repo)
	e.mux = chi.NewRouter()
	e.h.RegisterRoutes(e.mux)
	return e
}

// asset creates an asset of the organization and returns its id.
func (e *bulkEnv) asset(tag string) string {
	e.t.Helper()
	a := &asset.Asset{OrganizationID: bulkOrg, AssetTag: tag, Name: tag, Category: "hardware", Status: "in_stock",
		CustomFields: map[string]any{}, Tags: []string{}}
	if err := e.repo.Create(context.Background(), a); err != nil {
		e.t.Fatal(err)
	}
	return a.ID
}

// post sends the bulk body as a principal with perms.
func (e *bulkEnv) post(body any, perms ...identity.Permission) *httptest.ResponseRecorder {
	e.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: bulkOrg, UserID: bulkUser})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: bulkUser, OrganizationID: bulkOrg,
		Type: identity.PrincipalTypeUser, Permissions: perms})
	ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(perms))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/assets/bulk", bytes.NewReader(raw)).WithContext(ctx)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// problem is the body of a refused request.
type problem struct {
	Status int      `json:"status"`
	Field  string   `json:"field"`
	Action string   `json:"action"`
	Fields []string `json:"fields"`
}

func decodeProblem(t *testing.T, w *httptest.ResponseRecorder) problem {
	t.Helper()
	var p problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return p
}

func report(t *testing.T, w *httptest.ResponseRecorder) ci.BulkResult {
	t.Helper()
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		t.Fatalf("bulk: %d %s, want 200 with the report", w.Code, w.Body.String())
	}
	return res
}

func uuids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", 900000+i)
	}
	return out
}

var allBulkPerms = []identity.Permission{identity.PermAssetWrite, identity.PermLifecycleTransition, identity.PermAssetMove,
	identity.PermAssignmentWrite, identity.PermExportRun}

func TestAssetBulkValidation(t *testing.T) {
	e := newBulkEnv(t)
	id := e.asset("v-1")
	cases := []struct {
		name  string
		body  map[string]any
		code  int
		field string
	}{
		{"unknown action", map[string]any{"action": "explode", "ids": []string{id}}, 422, "action"},
		{"export without exporter", map[string]any{"action": "export", "ids": []string{id}, "params": map[string]any{"format": "csv"}}, 422, "action"},
		{"no ids", map[string]any{"status": "lost", "ids": []string{}}, 400, "ids"},
		{"501 ids", map[string]any{"status": "lost", "ids": uuids(asset.MaxBulkIDs + 1)}, 422, "ids"},
		{"invalid status", map[string]any{"status": "inactive", "ids": []string{id}}, 422, "status"},
		{"status of another action", map[string]any{"action": "archive", "status": "lost", "ids": []string{id}}, 422, "status"},
		{"unknown parameter", map[string]any{"action": "archive", "ids": []string{id}, "params": map[string]any{"x": 1}}, 422, "params.x"},
		{"set_fields status", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{"status": "lost"}}}, 422, "params.fields"},
		{"set_fields location", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{"location_id": id}}}, 422, "params.fields"},
		{"set_fields lifecycle", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{"lifecycle_state": "retired"}}}, 422, "params.fields"},
		{"set_fields tags", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{"tags": []string{"a"}}}}, 422, "params.fields"},
		{"set_fields unknown", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{"asset_tag": "x"}}}, 422, "params.fields"},
		{"set_fields type", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{"name": 7}}}, 422, "params.fields"},
		{"set_fields empty", map[string]any{"action": "set_fields", "ids": []string{id}, "params": map[string]any{"fields": map[string]any{}}}, 422, "params.fields"},
		{"set_location without target", map[string]any{"action": "set_location", "ids": []string{id}}, 422, "params.to_location_id"},
		{"set_location no uuid", map[string]any{"action": "set_location", "ids": []string{id}, "params": map[string]any{"to_location_id": "x"}}, 422, "params.to_location_id"},
		{"tags malformed", map[string]any{"action": "add_tags", "ids": []string{id}, "params": map[string]any{"tags": []string{"Not A Key"}}}, 422, "params.tags"},
		{"tags empty", map[string]any{"action": "remove_tags", "ids": []string{id}, "params": map[string]any{"tags": []string{}}}, 422, "params.tags"},
		{"assign two holders", map[string]any{"action": "assign", "ids": []string{id}, "params": map[string]any{"assigned_user_id": bulkUser, "assigned_team_id": bulkUser}}, 422, "params.assigned_user_id"},
		{"assign no holder", map[string]any{"action": "assign", "ids": []string{id}}, 422, "params.assigned_user_id"},
		{"assign due_at", map[string]any{"action": "assign", "ids": []string{id}, "params": map[string]any{"assigned_user_id": bulkUser, "due_at": "tomorrow"}}, 422, "params.due_at"},
		{"transition without target", map[string]any{"action": "lifecycle_transition", "ids": []string{id}}, 422, "params.to_state"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := e.post(c.body, allBulkPerms...)
			if p := decodeProblem(t, w); w.Code != c.code || p.Field != c.field {
				t.Fatalf("%d %s, want %d on %s", w.Code, w.Body.String(), c.code, c.field)
			}
		})
	}
	a, err := e.repo.GetByID(context.Background(), bulkOrg, id)
	if err != nil || a.Status != "in_stock" || a.Version != 1 {
		t.Errorf("asset after the refused requests: %+v %v", a, err)
	}
}

func TestAssetBulkSetStatusReport(t *testing.T) {
	e := newBulkEnv(t)
	a, b := e.asset("s-1"), e.asset("s-2")
	missing := "00000000-0000-4000-8000-0000000007ff"
	// The default action is set_status; an id in upper case counts once.
	res := report(t, e.post(map[string]any{"status": "maintenance", "ids": []string{a, strings.ToUpper(a), b, missing, "not-a-uuid"}},
		identity.PermAssetWrite))
	if res.Action != asset.BulkSetStatus || res.Total != 4 || res.Succeeded != 2 || res.Failed != 2 {
		t.Fatalf("report: %+v", res)
	}
	want := map[string]int{a: 200, b: 200, missing: 404, "not-a-uuid": 400}
	for _, it := range res.Items {
		if want[it.ID] != it.Code {
			t.Errorf("report of %s: %+v, want %d", it.ID, it, want[it.ID])
		}
	}
	for _, id := range []string{a, b} {
		if got, _ := e.repo.GetByID(context.Background(), bulkOrg, id); got.Status != "maintenance" || got.Version != 2 {
			t.Errorf("asset %s: status %s version %d", id, got.Status, got.Version)
		}
	}
	// A read-only member through set_fields is refused before; an empty
	// inventory number is 400 on inventory_no for the id, like PATCH.
	res = report(t, e.post(map[string]any{"action": "set_fields", "ids": []string{a},
		"params": map[string]any{"fields": map[string]any{"inventory_no": " "}}}, identity.PermAssetWrite))
	if it := res.Items[0]; it.Code != http.StatusBadRequest || !slices.Equal(it.Fields, []string{"inventory_no"}) {
		t.Errorf("empty inventory number: %+v", it)
	}
	res = report(t, e.post(map[string]any{"action": "set_fields", "ids": []string{a, b},
		"params": map[string]any{"fields": map[string]any{"notes": "bulk", "custom_fields": map[string]any{"room": "1"}}}}, identity.PermAssetWrite))
	if res.Succeeded != 2 {
		t.Fatalf("set_fields: %+v", res)
	}
	if got, _ := e.repo.GetByID(context.Background(), bulkOrg, b); got.Notes != "bulk" || got.CustomFields["room"] != "1" {
		t.Errorf("asset after set_fields: %+v", got)
	}
}

func TestAssetBulkTags(t *testing.T) {
	e := newBulkEnv(t)
	a := e.asset("t-1")
	body := map[string]any{"action": "add_tags", "ids": []string{a}, "params": map[string]any{"tags": []string{"red", "blue"}}}
	if res := report(t, e.post(body, identity.PermAssetWrite)); res.Succeeded != 1 {
		t.Fatalf("add_tags: %+v", res)
	}
	got, _ := e.repo.GetByID(context.Background(), bulkOrg, a)
	version := got.Version
	if !slices.Equal(got.Tags, []string{"blue", "red"}) {
		t.Fatalf("tags: %v", got.Tags)
	}
	// Idempotent: the same tags again change nothing.
	if res := report(t, e.post(body, identity.PermAssetWrite)); res.Succeeded != 1 {
		t.Fatalf("add_tags again: %+v", res)
	}
	if got, _ = e.repo.GetByID(context.Background(), bulkOrg, a); got.Version != version {
		t.Errorf("version after the same tags: %d, want %d", got.Version, version)
	}
	body["action"], body["params"] = "remove_tags", map[string]any{"tags": []string{"red", "green"}}
	if res := report(t, e.post(body, identity.PermAssetWrite)); res.Succeeded != 1 {
		t.Fatalf("remove_tags: %+v", res)
	}
	if got, _ = e.repo.GetByID(context.Background(), bulkOrg, a); !slices.Equal(got.Tags, []string{"blue"}) {
		t.Errorf("tags after remove_tags: %v", got.Tags)
	}
}

func TestAssetBulkPermissions(t *testing.T) {
	e := newBulkEnv(t)
	fake := &fakeDelegates{}
	e.h.WithBulkDelegates(&asset.BulkDelegates{Movements: fake, Assignments: fake, Lifecycle: fake})
	id := e.asset("p-1")
	cases := []struct {
		action string
		params map[string]any
		need   identity.Permission
	}{
		{"archive", nil, identity.PermLifecycleTransition},
		{"set_location", map[string]any{"to_location_id": bulkOrg}, identity.PermAssetMove},
		{"assign", map[string]any{"assigned_user_id": bulkUser}, identity.PermAssignmentWrite},
		{"lifecycle_transition", map[string]any{"to_state": "retired"}, identity.PermLifecycleTransition},
	}
	for _, c := range cases {
		without := slices.DeleteFunc(slices.Clone(allBulkPerms), func(p identity.Permission) bool { return p == c.need })
		w := e.post(map[string]any{"action": c.action, "ids": []string{id}, "params": c.params}, without...)
		if p := decodeProblem(t, w); w.Code != http.StatusForbidden || p.Action != string(c.need) {
			t.Errorf("%s without %s: %d %s", c.action, c.need, w.Code, w.Body.String())
		}
	}
	if fake.calls != 0 {
		t.Fatalf("a refused request reached a port %d times", fake.calls)
	}
	// With the right each action reaches its port once.
	for _, c := range cases[1:] {
		if res := report(t, e.post(map[string]any{"action": c.action, "ids": []string{id}, "params": c.params}, allBulkPerms...)); res.Succeeded != 1 {
			t.Errorf("%s: %+v", c.action, res)
		}
	}
	if fake.calls != 3 {
		t.Errorf("port calls: %d, want 3", fake.calls)
	}
	// The field operations need asset:write alone (the route checks it).
	if res := report(t, e.post(map[string]any{"status": "lost", "ids": []string{id}}, identity.PermAssetWrite)); res.Succeeded != 1 {
		t.Errorf("set_status with asset:write: %+v", res)
	}
}

func TestAssetBulkMissingPortIs503(t *testing.T) {
	e := newBulkEnv(t)
	id := e.asset("m-1")
	for action, params := range map[string]map[string]any{
		"set_location":         {"to_location_id": bulkOrg},
		"assign":               {"assigned_team_id": bulkUser},
		"lifecycle_transition": {"to_state": "retired"},
		"archive":              nil,
	} {
		if w := e.post(map[string]any{"action": action, "ids": []string{id}, "params": params}, allBulkPerms...); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without its port: %d %s, want 503", action, w.Code, w.Body.String())
		}
	}
}

func TestAssetBulkJobFrom100IDs(t *testing.T) {
	e := newBulkEnv(t)
	// Without a job service 100 ids run in the request.
	if res := report(t, e.post(map[string]any{"status": "lost", "ids": uuids(asset.BulkJobFromIDs)}, identity.PermAssetWrite)); res.Total != asset.BulkJobFromIDs {
		t.Fatalf("100 ids without jobs: %+v", res)
	}
	svc := jobs.NewService(jobs.NewMemoryRepository())
	svc.Register(jobs.Kind{Name: asset.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	e.h.WithBulkJobs(svc)
	// 99 ids stay in the request.
	if res := report(t, e.post(map[string]any{"status": "lost", "ids": uuids(asset.BulkJobFromIDs - 1)}, identity.PermAssetWrite)); res.Total != asset.BulkJobFromIDs-1 {
		t.Fatalf("99 ids: %+v", res)
	}
	w := e.post(map[string]any{"status": "lost", "ids": uuids(asset.BulkJobFromIDs)}, identity.PermAssetWrite)
	var accepted struct {
		JobID string `json:"job_id"`
		Kind  string `json:"kind"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.Kind != asset.BulkJobKind ||
		w.Header().Get("Location") != "/api/v1/jobs/"+accepted.JobID {
		t.Fatalf("100 ids: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	// A queue job without the creator policy fails closed.
	job, err := svc.Get(context.Background(), bulkOrg, accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = asset.NewHandler(e.repo).RunBulkJob(context.Background(), job); err == nil {
		t.Error("queue job without creator policy ran")
	}
}
