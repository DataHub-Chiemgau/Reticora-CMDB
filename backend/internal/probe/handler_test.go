package probe_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// REST of the probe definitions on the memory store (PRB-01, PRT-01,
// WP-939): type and limit checks with 422 per field, the parent chain
// (cycle 409, depth 422), the default collector group, the list with its
// filters and total and the probe type register.

const (
	hOrg     = "11111111-1111-4111-8111-111111111111"
	hClient  = "22222222-2222-4222-8222-222222222222"
	hClient2 = "22222222-2222-4222-8222-222222222223"
	hGroup   = "33333333-3333-4333-8333-333333333333"
	hGroup2  = "33333333-3333-4333-8333-333333333334"
	hCI      = "44444444-4444-4444-8444-444444444444"
	hUser    = "55555555-5555-4555-8555-555555555555"
)

type handlerEnv struct {
	t    *testing.T
	repo *probe.MemoryRepository
	mux  *chi.Mux
}

func newHandlerEnv(t *testing.T) *handlerEnv {
	t.Helper()
	repo := probe.NewMemoryRepository().WithCIs(func(_ context.Context, orgID, id string) (string, string, bool) {
		return hClient, "", orgID == hOrg && id == hCI
	})
	repo.AddCollectorGroup(hOrg, hGroup, hClient)
	mux := chi.NewRouter()
	probe.NewHandler(repo).RegisterRoutes(mux)
	return &handlerEnv{t: t, repo: repo, mux: mux}
}

type reply struct {
	Code  int
	Body  string
	Probe probe.Probe
	Field string
}

func (e *handlerEnv) do(method, path, body string) reply {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	scope := database.OrgWideScope(hOrg, hUser)
	ctx := database.ContextWithTenantScope(req.Context(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: hOrg, UserID: hUser})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	r := reply{Code: rec.Code, Body: rec.Body.String()}
	var problem struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	r.Field = problem.Field
	_ = json.Unmarshal(rec.Body.Bytes(), &r.Probe)
	return r
}

func (e *handlerEnv) create(body string) reply {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/monitoring/probes", body)
}

func (e *handlerEnv) mustCreate(body string) probe.Probe {
	e.t.Helper()
	r := e.create(body)
	if r.Code != http.StatusCreated {
		e.t.Fatalf("create %s: %d %s", body, r.Code, r.Body)
	}
	return r.Probe
}

func want(t *testing.T, what string, r reply, code int, field string) {
	t.Helper()
	if r.Code != code || (field != "" && r.Field != field) {
		t.Fatalf("%s: %d field %q (%s), want %d field %q", what, r.Code, r.Field, r.Body, code, field)
	}
}

// TestCreateProbeValidation: an unknown and a planned type, the timeout
// equal to the interval, an interval below the type minimum and an invalid
// target are 422 with their field; a probe of the defaults is created.
func TestCreateProbeValidation(t *testing.T) {
	e := newHandlerEnv(t)
	cases := []struct {
		name, body, field string
	}{
		{"unknown type", `{"type":"nope","target":"10.0.0.1","client_id":"` + hClient + `"}`, "type"},
		{"planned type", `{"type":"snmp_status","target":"192.0.2.10","client_id":"` + hClient + `"}`, "type"},
		{"timeout = interval", `{"type":"icmp","target":"10.0.0.1","client_id":"` + hClient + `","params":{"interval_s":10,"timeout_s":10}}`, "timeout_s"},
		{"interval below minimum", `{"type":"icmp","target":"10.0.0.1","client_id":"` + hClient + `","params":{"interval_s":5}}`, "interval_s"},
		{"interval above maximum", `{"type":"icmp","target":"10.0.0.1","client_id":"` + hClient + `","params":{"interval_s":3601}}`, "interval_s"},
		{"unknown parameter", `{"type":"icmp","target":"10.0.0.1","client_id":"` + hClient + `","params":{"bogus":1}}`, "params.bogus"},
		{"invalid icmp target", `{"type":"icmp","target":"not a host","client_id":"` + hClient + `"}`, "target"},
		{"icmp target with bad label", `{"type":"icmp","target":"-bad-.example","client_id":"` + hClient + `"}`, "target"},
		{"tcp without port", `{"type":"tcp","target":"db.example","client_id":"` + hClient + `"}`, "params.port"},
		{"tcp port out of range", `{"type":"tcp","target":"db.example","client_id":"` + hClient + `","params":{"port":70000}}`, "params.port"},
		{"no client", `{"type":"icmp","target":"10.0.0.1"}`, "client_id"},
		{"unknown ci", `{"type":"icmp","target":"10.0.0.1","ci_id":"` + hGroup2 + `"}`, "ci_id"},
		{"unknown collector group", `{"type":"icmp","target":"10.0.0.1","client_id":"` + hClient + `","collector_group_id":"` + hGroup2 + `"}`, "collector_group_id"},
		{"unknown parent", `{"type":"icmp","target":"10.0.0.1","client_id":"` + hClient + `","parent_probe_id":"` + hGroup2 + `"}`, "parent_probe_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := e.create(c.body)
			want(t, c.name, r, http.StatusUnprocessableEntity, c.field)
			if !strings.Contains(r.Body, `"violations"`) && (c.field == "type" || c.field == "timeout_s" || c.field == "interval_s") {
				t.Errorf("%s: no violations list: %s", c.name, r.Body)
			}
		})
	}
	// The defaults of icmp (30 s / 10 s / 5) and a tcp probe with its
	// port in the parameters are valid; the CI gives the client.
	p := e.mustCreate(`{"type":"icmp","target":"10.0.0.1","ci_id":"` + hCI + `"}`)
	if p.ClientID != hClient || p.CollectorGroupID != hGroup || !p.Enabled || string(p.Params) != "{}" {
		t.Fatalf("created %+v", p)
	}
	e.mustCreate(`{"type":"tcp","target":"db.example","client_id":"` + hClient + `","params":{"port":5432}}`)
	e.mustCreate(`{"type":"tcp","target":"2001:db8::1","client_id":"` + hClient + `","params":{"port":443}}`)
	// The same CI, type and target again: 409.
	want(t, "duplicate", e.create(`{"type":"icmp","target":"10.0.0.1","ci_id":"`+hCI+`"}`), http.StatusConflict, "target")
	// A change keeps the checks: timeout = interval is 422, the type is
	// fixed.
	want(t, "patch timeout", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+p.ID, `{"params":{"interval_s":20,"timeout_s":20}}`),
		http.StatusUnprocessableEntity, "timeout_s")
	want(t, "patch type", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+p.ID, `{"type":"tcp"}`), http.StatusUnprocessableEntity, "type")
	r := e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+p.ID, `{"params":{"interval_s":60},"enabled":false}`)
	want(t, "patch", r, http.StatusOK, "")
	if r.Probe.Enabled || !strings.Contains(string(r.Probe.Params), `"interval_s":60`) {
		t.Fatalf("patched %+v", r.Probe)
	}
	want(t, "delete", e.do(http.MethodDelete, "/api/v1/monitoring/probes/"+p.ID, ""), http.StatusNoContent, "")
	want(t, "get deleted", e.do(http.MethodGet, "/api/v1/monitoring/probes/"+p.ID, ""), http.StatusNotFound, "")
}

// chain creates n probes, each the child of the one before, and returns
// their ids from the root down.
func (e *handlerEnv) chain(prefix string, n int) []string {
	e.t.Helper()
	ids := make([]string, 0, n)
	parent := ""
	for i := 0; i < n; i++ {
		body := fmt.Sprintf(`{"type":"icmp","target":"%s-%d.example","client_id":"%s"`, prefix, i, hClient)
		if parent != "" {
			body += `,"parent_probe_id":"` + parent + `"`
		}
		p := e.mustCreate(body + "}")
		ids = append(ids, p.ID)
		parent = p.ID
	}
	return ids
}

// TestParentProbeCycle: the probe itself as parent and a parent below the
// probe are 409.
func TestParentProbeCycle(t *testing.T) {
	e := newHandlerEnv(t)
	ids := e.chain("cycle", 3)
	want(t, "self", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+ids[0], `{"parent_probe_id":"`+ids[0]+`"}`),
		http.StatusConflict, "parent_probe_id")
	want(t, "cycle", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+ids[0], `{"parent_probe_id":"`+ids[2]+`"}`),
		http.StatusConflict, "parent_probe_id")
	// Removing the parent and setting a sibling chain is fine.
	want(t, "remove parent", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+ids[2], `{"parent_probe_id":null}`), http.StatusOK, "")
	want(t, "reverse", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+ids[0], `{"parent_probe_id":"`+ids[2]+`"}`), http.StatusOK, "")
}

// TestParentProbeDepthLimit: a chain of 8 levels is allowed, a 9th level is
// 422 parent_probe_id, also when a subtree is moved below a chain.
func TestParentProbeDepthLimit(t *testing.T) {
	e := newHandlerEnv(t)
	ids := e.chain("depth", probe.MaxProbeDepth)
	r := e.create(`{"type":"icmp","target":"depth-9.example","client_id":"` + hClient + `","parent_probe_id":"` + ids[len(ids)-1] + `"}`)
	want(t, "9th level", r, http.StatusUnprocessableEntity, "parent_probe_id")
	// Two levels moved below level 7 make 9 levels.
	sub := e.chain("sub", 2)
	want(t, "move subtree", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+sub[0], `{"parent_probe_id":"`+ids[6]+`"}`),
		http.StatusUnprocessableEntity, "parent_probe_id")
	want(t, "move leaf", e.do(http.MethodPatch, "/api/v1/monitoring/probes/"+sub[1], `{"parent_probe_id":"`+ids[6]+`"}`),
		http.StatusOK, "")
}

// TestCollectorGroupDefault: without collector_group_id the only group of
// the client is taken; none or several are 422 collector_group_id.
func TestCollectorGroupDefault(t *testing.T) {
	e := newHandlerEnv(t)
	p := e.mustCreate(`{"type":"icmp","target":"10.1.0.1","client_id":"` + hClient + `"}`)
	if p.CollectorGroupID != hGroup {
		t.Fatalf("collector group %q, want the client's only group %s", p.CollectorGroupID, hGroup)
	}
	want(t, "no group", e.create(`{"type":"icmp","target":"10.1.0.2","client_id":"`+hClient2+`"}`),
		http.StatusUnprocessableEntity, "collector_group_id")
	e.repo.AddCollectorGroup(hOrg, hGroup2, hClient)
	want(t, "two groups", e.create(`{"type":"icmp","target":"10.1.0.3","client_id":"`+hClient+`"}`),
		http.StatusUnprocessableEntity, "collector_group_id")
	// Named, a group of another client is 422, an org-wide one fits.
	orgWide := "33333333-3333-4333-8333-333333333335"
	other := "33333333-3333-4333-8333-333333333336"
	e.repo.AddCollectorGroup(hOrg, orgWide, "")
	e.repo.AddCollectorGroup(hOrg, other, hClient2)
	want(t, "group of another client", e.create(`{"type":"icmp","target":"10.1.0.4","client_id":"`+hClient+`","collector_group_id":"`+other+`"}`),
		http.StatusUnprocessableEntity, "collector_group_id")
	e.mustCreate(`{"type":"icmp","target":"10.1.0.5","client_id":"` + hClient + `","collector_group_id":"` + orgWide + `"}`)
}

// TestListProbesFilterAndTotal: the filters and total of the list, paged.
func TestListProbesFilterAndTotal(t *testing.T) {
	e := newHandlerEnv(t)
	for i := 0; i < 5; i++ {
		e.mustCreate(fmt.Sprintf(`{"type":"icmp","target":"10.2.0.%d","client_id":"%s"}`, i, hClient))
	}
	e.mustCreate(`{"type":"tcp","target":"10.2.0.9","client_id":"` + hClient + `","params":{"port":22},"enabled":false}`)
	e.mustCreate(`{"type":"icmp","target":"10.2.1.1","ci_id":"` + hCI + `"}`)
	list := func(query string) (total int, n int) {
		t.Helper()
		r := e.do(http.MethodGet, "/api/v1/monitoring/probes"+query, "")
		if r.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, r.Code, r.Body)
		}
		var body struct {
			Data  []probe.Probe `json:"data"`
			Total int           `json:"total"`
		}
		if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
			t.Fatal(err)
		}
		return body.Total, len(body.Data)
	}
	for _, c := range []struct {
		query    string
		total, n int
	}{
		{"", 7, 7},
		{"?limit=2", 7, 2},
		{"?type=icmp", 6, 6},
		{"?type=tcp", 1, 1},
		{"?enabled=false", 1, 1},
		{"?enabled=true&type=icmp&limit=4", 6, 4},
		{"?ci_id=" + hCI, 1, 1},
		{"?client_id=" + hClient, 7, 7},
		{"?client_id=" + hClient2, 0, 0},
		{"?collector_group_id=" + hGroup, 7, 7},
	} {
		if total, n := list(c.query); total != c.total || n != c.n {
			t.Errorf("list %q: total %d, %d rows; want %d, %d", c.query, total, n, c.total, c.n)
		}
	}
	want(t, "enabled not a bool", e.do(http.MethodGet, "/api/v1/monitoring/probes?enabled=maybe", ""), http.StatusBadRequest, "")
}

// TestProbeTypesEndpoint: GET /probe-types lists the register with key,
// status, defaults, limits and parameter schema; /probe-types/{key} one
// entry, an unknown key 404.
func TestProbeTypesEndpoint(t *testing.T) {
	e := newHandlerEnv(t)
	r := e.do(http.MethodGet, "/api/v1/monitoring/probe-types?limit=100", "")
	if r.Code != http.StatusOK {
		t.Fatalf("probe types: %d %s", r.Code, r.Body)
	}
	var body struct {
		Data []struct {
			Key              string          `json:"key"`
			Status           string          `json:"status"`
			DefaultIntervalS int64           `json:"default_interval_s"`
			MinIntervalS     int64           `json:"min_interval_s"`
			MaxTimeoutS      int64           `json:"max_timeout_s"`
			Params           json.RawMessage `json:"params"`
		} `json:"data"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != len(probewire.Types()) || len(body.Data) != body.Total {
		t.Fatalf("total %d, %d rows, want %d", body.Total, len(body.Data), len(probewire.Types()))
	}
	for i, typ := range probewire.Types() {
		got := body.Data[i]
		if got.Key != typ.Key || got.Status != typ.Status || got.DefaultIntervalS != typ.DefaultIntervalS ||
			got.MinIntervalS != typ.MinIntervalS || got.MaxTimeoutS != typ.MaxTimeoutS || len(got.Params) == 0 {
			t.Errorf("entry %d = %+v, want the register entry %s", i, got, typ.Key)
		}
	}
	one := e.do(http.MethodGet, "/api/v1/monitoring/probe-types/icmp", "")
	if one.Code != http.StatusOK || !strings.Contains(one.Body, `"key":"icmp"`) || !strings.Contains(one.Body, `"default_interval_s":30`) {
		t.Fatalf("icmp: %d %s", one.Code, one.Body)
	}
	want(t, "unknown type", e.do(http.MethodGet, "/api/v1/monitoring/probe-types/nope", ""), http.StatusNotFound, "")
}
