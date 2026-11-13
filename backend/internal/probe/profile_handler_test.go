package probe_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// REST of the monitoring profiles, the monitoring groups and the effective
// values on the memory store (PRB-01, WP-946 AK1-AK3, AK6, AK8).

const (
	pCI2 = "44444444-4444-4444-8444-444444444445" // a CI of hClient2
)

// profileEnv is the probe API on the memory store with the CIs hCI
// (client 1) and pCI2 (client 2) and the clients hClient and hClient2.
type profileEnv struct {
	t    *testing.T
	repo *probe.MemoryRepository
	mux  *chi.Mux
}

func newProfileEnv(t *testing.T, gate probe.BudgetGate) *profileEnv {
	t.Helper()
	repo := probe.NewMemoryRepository().WithCIs(func(_ context.Context, orgID, id string) (string, string, bool) {
		switch {
		case orgID != hOrg:
			return "", "", false
		case id == hCI:
			return hClient, "", true
		case id == pCI2:
			return hClient2, "", true
		}
		return "", "", false
	})
	repo.AddClient(hOrg, hClient)
	repo.AddClient(hOrg, hClient2)
	repo.AddCollectorGroup(hOrg, hGroup, hClient)
	h := probe.NewHandler(repo)
	if gate != nil {
		h.WithBudget(gate)
	}
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return &profileEnv{t: t, repo: repo, mux: mux}
}

// do sends a request of an org-wide principal, or one restricted to
// clients.
func (e *profileEnv) do(method, path, body string, clients ...string) (int, string, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	scope := database.OrgWideScope(hOrg, hUser)
	if len(clients) > 0 {
		scope.Clients = database.ScopeIDs(clients...)
	}
	ctx := database.ContextWithTenantScope(req.Context(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: hOrg, UserID: hUser})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	var body2 map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body2)
	return rec.Code, rec.Body.String(), body2
}

// must sends a request and fails unless it answers code.
func (e *profileEnv) must(code int, method, path, body string) map[string]any {
	e.t.Helper()
	got, raw, out := e.do(method, path, body)
	if got != code {
		e.t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, got, raw, code)
	}
	return out
}

// wantField fails unless the request answers code with field.
func (e *profileEnv) wantField(what string, code int, field, method, path, body string, clients ...string) {
	e.t.Helper()
	got, raw, out := e.do(method, path, body, clients...)
	if got != code || (field != "" && out["field"] != field) {
		e.t.Errorf("%s: %d %s, want %d field %q", what, got, raw, code, field)
	}
}

func TestProfileCRUD(t *testing.T) {
	e := newProfileEnv(t, nil)
	created := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"client","level_id":"`+hClient+`","probe_type":"icmp","params":{"interval_s":60}}`)
	id, _ := created["id"].(string)
	if created["level_id"] != hClient || created["level"] != "client" || created["client_id"] != hClient {
		t.Fatalf("created %v", created)
	}
	org := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{}}`)
	if org["level_id"] != nil {
		t.Errorf("org profile level_id %v, want null", org["level_id"])
	}
	got := e.must(http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles/"+id, "")
	if got["probe_type"] != "icmp" {
		t.Errorf("get %v", got)
	}
	list := e.must(http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles?level=client&level_id="+hClient, "")
	if list["total"] != float64(1) {
		t.Errorf("filtered list %v, want 1", list)
	}
	if all := e.must(http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles?probe_type=icmp", ""); all["total"] != float64(2) {
		t.Errorf("list by type %v, want 2", all)
	}
	updated := e.must(http.StatusOK, http.MethodPatch, "/api/v1/monitoring/profiles/"+id, `{"params":{"interval_s":120}}`)
	if p, _ := updated["params"].(map[string]any); p["interval_s"] != float64(120) {
		t.Errorf("updated %v", updated)
	}
	e.must(http.StatusNoContent, http.MethodDelete, "/api/v1/monitoring/profiles/"+id, "")
	e.wantField("deleted profile", http.StatusNotFound, "", http.MethodGet, "/api/v1/monitoring/profiles/"+id, "")
	e.wantField("level_id at org", http.StatusUnprocessableEntity, "level_id", http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"org","level_id":"`+hClient+`","probe_type":"icmp","params":{}}`)
	e.wantField("missing level_id", http.StatusUnprocessableEntity, "level_id", http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"ci","probe_type":"icmp","params":{}}`)
	e.wantField("unknown level", http.StatusUnprocessableEntity, "level", http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"site","probe_type":"icmp","params":{}}`)
	e.wantField("unknown ci", http.StatusNotFound, "", http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"ci","level_id":"`+hGroup2+`","probe_type":"icmp","params":{}}`)
	// A client-restricted principal does not see another client (404) and
	// writes no org profile (403).
	e.wantField("other client", http.StatusNotFound, "", http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"client","level_id":"`+hClient2+`","probe_type":"icmp","params":{}}`, hClient)
	e.wantField("org profile of a scoped principal", http.StatusForbidden, "level", http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"org","probe_type":"tcp","params":{}}`, hClient)
}

func TestProfileRejectsOutOfBounds(t *testing.T) {
	e := newProfileEnv(t, nil)
	for _, c := range []struct{ name, body, field string }{
		{"interval below the type minimum", `{"level":"org","probe_type":"icmp","params":{"interval_s":5}}`, "interval_s"},
		{"interval above the type maximum", `{"level":"org","probe_type":"icmp","params":{"interval_s":3601}}`, "interval_s"},
		{"timeout equal to the interval", `{"level":"org","probe_type":"icmp","params":{"interval_s":10,"timeout_s":10}}`, "timeout_s"},
		{"down count above 20", `{"level":"org","probe_type":"icmp","params":{"down_count":21}}`, "down_count"},
		{"unknown parameter", `{"level":"org","probe_type":"icmp","params":{"bogus":1}}`, "params.bogus"},
		{"unknown type", `{"level":"org","probe_type":"nope","params":{}}`, "probe_type"},
		{"planned type", `{"level":"org","probe_type":"snmp_status","params":{}}`, "probe_type"},
		{"params no object", `{"level":"org","probe_type":"icmp","params":[1]}`, "params"},
	} {
		e.wantField(c.name, http.StatusUnprocessableEntity, c.field, http.MethodPost, "/api/v1/monitoring/profiles", c.body)
	}
	created := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{}}`)
	e.wantField("patch out of bounds", http.StatusUnprocessableEntity, "interval_s", http.MethodPatch,
		"/api/v1/monitoring/profiles/"+created["id"].(string), `{"params":{"interval_s":1}}`)
}

func TestProfileDuplicateLevelConflicts(t *testing.T) {
	e := newProfileEnv(t, nil)
	body := `{"level":"ci","level_id":"` + hCI + `","probe_type":"icmp","params":{"interval_s":60}}`
	e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", body)
	e.wantField("same level, target and type", http.StatusConflict, "level_id", http.MethodPost, "/api/v1/monitoring/profiles", body)
	// Another type of the same CI is another profile.
	e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"ci","level_id":"`+hCI+`","probe_type":"tcp","params":{}}`)
}

func TestGroupMembersClientBinding(t *testing.T) {
	e := newProfileEnv(t, nil)
	g := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"core","client_id":"`+hClient+`"}`)
	gid := g["id"].(string)
	e.wantField("duplicate name", http.StatusConflict, "name", http.MethodPost, "/api/v1/monitoring/groups", `{"name":"core"}`)
	e.wantField("CI of another client", http.StatusUnprocessableEntity, "ci_ids", http.MethodPost,
		"/api/v1/monitoring/groups/"+gid+"/members", `{"ci_ids":["`+pCI2+`"]}`)
	e.wantField("invisible CI", http.StatusNotFound, "", http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members",
		`{"ci_ids":["`+hGroup2+`"]}`)
	added := e.must(http.StatusOK, http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members", `{"ci_ids":["`+hCI+`"]}`)
	if data, _ := added["data"].([]any); len(data) != 1 {
		t.Fatalf("added %v", added)
	}
	if got := e.must(http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+gid, ""); got["member_count"] != float64(1) {
		t.Errorf("member_count %v, want 1", got["member_count"])
	}
	if list := e.must(http.StatusOK, http.MethodGet, "/api/v1/monitoring/groups/"+gid+"/members", ""); list["total"] != float64(1) {
		t.Errorf("members %v", list)
	}
	// An org-wide group takes CIs of every client.
	ow := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"all"}`)
	e.must(http.StatusOK, http.MethodPost, "/api/v1/monitoring/groups/"+ow["id"].(string)+"/members", `{"ci_ids":["`+hCI+`","`+pCI2+`"]}`)
	// Its members are changed only by an org-wide principal, even for CIs of
	// the caller's own client (AK5, AK8).
	e.wantField("client principal adds to an org-wide group", http.StatusForbidden, "level", http.MethodPost,
		"/api/v1/monitoring/groups/"+ow["id"].(string)+"/members", `{"ci_ids":["`+hCI+`"]}`, hClient)
	e.wantField("client principal removes from an org-wide group", http.StatusForbidden, "level", http.MethodDelete,
		"/api/v1/monitoring/groups/"+ow["id"].(string)+"/members/"+hCI, "", hClient)
	tooMany := make([]string, probe.MaxGroupMembersPerRequest+1)
	for i := range tooMany {
		tooMany[i] = `"` + hCI + `"`
	}
	e.wantField("more than 500 CIs", http.StatusUnprocessableEntity, "ci_ids", http.MethodPost,
		"/api/v1/monitoring/groups/"+gid+"/members", `{"ci_ids":[`+strings.Join(tooMany, ",")+`]}`)
	// A group with profiles goes with them.
	e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"group","level_id":"`+gid+`","probe_type":"icmp","params":{"interval_s":45}}`)
	e.must(http.StatusNoContent, http.MethodDelete, "/api/v1/monitoring/groups/"+gid+"/members/"+hCI, "")
	e.wantField("removed twice", http.StatusNotFound, "", http.MethodDelete, "/api/v1/monitoring/groups/"+gid+"/members/"+hCI, "")
	if del := e.must(http.StatusOK, http.MethodDelete, "/api/v1/monitoring/groups/"+gid, ""); del["deleted_profiles"] != float64(1) {
		t.Errorf("delete %v, want deleted_profiles 1", del)
	}
	if list := e.must(http.StatusOK, http.MethodGet, "/api/v1/monitoring/profiles?level=group", ""); list["total"] != float64(0) {
		t.Errorf("profiles of the deleted group remain: %v", list)
	}
}

// effectiveField reads one field of GET /effective.
func effectiveField(t *testing.T, eff map[string]any, name string) (value any, origin string) {
	t.Helper()
	f, ok := eff[name].(map[string]any)
	if !ok {
		t.Fatalf("effective %s missing in %v", name, eff)
	}
	origin, _ = f["origin"].(string)
	return f["value"], origin
}

func TestEffectiveReportsOriginAndClamped(t *testing.T) {
	e := newProfileEnv(t, nil)
	created := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/probes",
		`{"type":"icmp","target":"10.0.0.1","ci_id":"`+hCI+`"}`)
	path := "/api/v1/monitoring/probes/" + created["id"].(string) + "/effective"
	eff := e.must(http.StatusOK, http.MethodGet, path, "")
	for _, c := range []struct {
		field string
		value float64
	}{{"interval_s", 30}, {"timeout_s", 10}, {"down_count", 5}, {"up_count", 1}} {
		if v, origin := effectiveField(t, eff, c.field); v != c.value || origin != probe.OriginDefault {
			t.Errorf("%s = %v from %s, want %v from default", c.field, v, origin, c.value)
		}
	}
	e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"client","level_id":"`+hClient+`","probe_type":"icmp","params":{"interval_s":60}}`)
	eff = e.must(http.StatusOK, http.MethodGet, path, "")
	if v, origin := effectiveField(t, eff, "interval_s"); v != float64(60) || origin != probe.LevelClient {
		t.Errorf("interval_s = %v from %s, want 60 from client", v, origin)
	}
	g := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"fast","client_id":"`+hClient+`"}`)
	gid := g["id"].(string)
	e.must(http.StatusOK, http.MethodPost, "/api/v1/monitoring/groups/"+gid+"/members", `{"ci_ids":["`+hCI+`"]}`)
	e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles",
		`{"level":"group","level_id":"`+gid+`","probe_type":"icmp","params":{"interval_s":20}}`)
	eff = e.must(http.StatusOK, http.MethodGet, path, "")
	f := eff["interval_s"].(map[string]any)
	if f["value"] != float64(20) || f["origin"] != probe.LevelGroup || f["group_id"] != gid || f["clamped"] != false {
		t.Errorf("interval_s = %v, want 20 from group %s", f, gid)
	}
}

func TestProfileIntervalBelowPlanMinimum(t *testing.T) {
	ctx := context.Background()
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	if _, err := svc.Grant(ctx, &entitlement.Entitlement{OrganizationID: hOrg, FeatureKey: entitlement.FeatureMonitoring,
		Plan: entitlement.PlanStandard, Enabled: true, Limits: map[string]int64{entitlement.LimitMinProbeIntervalS: 30}}); err != nil {
		t.Fatal(err)
	}
	e := newProfileEnv(t, svc)
	g := e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/groups", `{"name":"g","client_id":"`+hClient+`"}`)
	for _, level := range []string{`"level":"org"`, `"level":"client","level_id":"` + hClient + `"`,
		`"level":"group","level_id":"` + g["id"].(string) + `"`, `"level":"ci","level_id":"` + hCI + `"`} {
		e.wantField(level, http.StatusUnprocessableEntity, "interval_s", http.MethodPost, "/api/v1/monitoring/profiles",
			`{`+level+`,"probe_type":"icmp","params":{"interval_s":10}}`)
	}
	e.must(http.StatusCreated, http.MethodPost, "/api/v1/monitoring/profiles", `{"level":"org","probe_type":"icmp","params":{"interval_s":30}}`)
}
