package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Collector groups on the memory store of --no-db (PRB-03, COL-06,
// WP-947): CRUD, the checks of mode, liveness timeout and anchor probe and
// the move of a collector between groups.

const (
	cgOrg    = "11111111-1111-4111-8111-111111111111"
	cgClient = "22222222-2222-4222-8222-222222222222"
	cgOther  = "22222222-2222-4222-8222-222222222223"
	cgUser   = "55555555-5555-4555-8555-555555555555"
	cgProbe  = "66666666-6666-4666-8666-666666666666"
	cgProbe2 = "66666666-6666-4666-8666-666666666667"
)

// fakeGroupProbes is the probe source: one probe of cgClient, one of
// cgOther, and a number of probes per group.
type fakeGroupProbes struct{ counts map[string]int }

func (f *fakeGroupProbes) ProbeClient(_ context.Context, orgID, id string) (string, bool, error) {
	switch {
	case orgID != cgOrg:
		return "", false, nil
	case id == cgProbe:
		return cgClient, true, nil
	case id == cgProbe2:
		return cgOther, true, nil
	}
	return "", false, nil
}

func (f *fakeGroupProbes) CountGroupProbes(_ context.Context, _, groupID string) (int, error) {
	return f.counts[groupID], nil
}

type cgMemEnv struct {
	t      *testing.T
	repo   *discovery.MemoryRepository
	probes *fakeGroupProbes
	mux    *chi.Mux
}

func newCGMemEnv(t *testing.T) *cgMemEnv {
	t.Helper()
	probes := &fakeGroupProbes{counts: map[string]int{}}
	repo := discovery.NewMemoryRepository().WithGroupProbes(probes)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, nil).RegisterCollectorGroupRoutes(mux)
	return &cgMemEnv{t: t, repo: repo, probes: probes, mux: mux}
}

// collector registers a collector of the client (empty: without client);
// it gets a group of its own.
func (e *cgMemEnv) collector(clientID, name string) string {
	e.t.Helper()
	c := &discovery.Collector{OrganizationID: cgOrg, ClientID: clientID, Name: name}
	if err := e.repo.RegisterCollector(context.Background(), c); err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

type cgReply struct {
	Code  int
	Body  string
	Group discovery.CollectorGroup
	Field string
}

func (e *cgMemEnv) do(method, path, body string) cgReply {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	scope := database.OrgWideScope(cgOrg, cgUser)
	ctx := database.ContextWithTenantScope(req.Context(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: cgOrg, UserID: cgUser})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	r := cgReply{Code: rec.Code, Body: rec.Body.String()}
	var problem struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	r.Field = problem.Field
	_ = json.Unmarshal(rec.Body.Bytes(), &r.Group)
	return r
}

func cgWant(t *testing.T, what string, r cgReply, code int, field string) {
	t.Helper()
	if r.Code != code || (field != "" && r.Field != field) {
		t.Fatalf("%s: %d field %q (%s), want %d field %q", what, r.Code, r.Field, r.Body, code, field)
	}
}

// groupOf returns the group the collector is a member of.
func (e *cgMemEnv) groupOf(collectorID string) string {
	e.t.Helper()
	r := e.do(http.MethodGet, "/api/v1/collector-groups?limit=100", "")
	var list struct {
		Data []discovery.CollectorGroup `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.Body), &list); err != nil {
		e.t.Fatal(err)
	}
	for _, g := range list.Data {
		for _, m := range g.Members {
			if m.CollectorID == collectorID {
				return g.ID
			}
		}
	}
	return ""
}

// TestCollectorGroupCRUD: create with defaults, list with filter and total,
// read, change and delete; every collector has a group of its own.
func TestCollectorGroupCRUD(t *testing.T) {
	e := newCGMemEnv(t)
	col := e.collector(cgClient, "wp947-col")
	if e.groupOf(col) == "" {
		t.Fatal("a new collector has no group of its own")
	}
	r := e.do(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-a","client_id":"`+cgClient+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	g := r.Group
	if g.Mode != discovery.CollectorGroupDistributed || g.LivenessTimeoutS != 30 || g.Status != "reachable" ||
		len(g.Members) != 0 || g.ClientID != cgClient {
		t.Fatalf("created %+v", g)
	}
	cgWant(t, "create other", e.do(http.MethodPost, "/api/v1/collector-groups",
		`{"name":"wp947-b","client_id":"`+cgOther+`","mode":"failover","liveness_timeout_s":60}`), http.StatusCreated, "")
	list := e.do(http.MethodGet, "/api/v1/collector-groups?client_id="+cgClient, "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body, `"total":2`) {
		t.Fatalf("list of the client: %d %s, want the new group and the collector's", list.Code, list.Body)
	}
	cgWant(t, "get", e.do(http.MethodGet, "/api/v1/collector-groups/"+g.ID, ""), http.StatusOK, "")
	r = e.do(http.MethodPatch, "/api/v1/collector-groups/"+g.ID, `{"name":"wp947-a2","mode":"failover","liveness_timeout_s":120,"anchor_probe_id":"`+cgProbe+`"}`)
	cgWant(t, "patch", r, http.StatusOK, "")
	if r.Group.Name != "wp947-a2" || r.Group.Mode != "failover" || r.Group.LivenessTimeoutS != 120 || r.Group.AnchorProbeID != cgProbe {
		t.Fatalf("patched %+v", r.Group)
	}
	r = e.do(http.MethodPatch, "/api/v1/collector-groups/"+g.ID, `{"anchor_probe_id":null}`)
	cgWant(t, "remove anchor", r, http.StatusOK, "")
	if r.Group.AnchorProbeID != "" {
		t.Fatalf("anchor probe %q after removing it", r.Group.AnchorProbeID)
	}
	r = e.do(http.MethodPost, "/api/v1/collector-groups/"+g.ID+"/members", `{"collector_id":"`+col+`","priority":2,"weight":50}`)
	cgWant(t, "add member", r, http.StatusOK, "")
	if len(r.Group.Members) != 1 || r.Group.Members[0].Priority != 2 || r.Group.Members[0].Weight != 50 ||
		r.Group.Members[0].MemberStatus != "healthy" {
		t.Fatalf("members %+v", r.Group.Members)
	}
	cgWant(t, "remove member", e.do(http.MethodDelete, "/api/v1/collector-groups/"+g.ID+"/members/"+col, ""), http.StatusNoContent, "")
	cgWant(t, "remove again", e.do(http.MethodDelete, "/api/v1/collector-groups/"+g.ID+"/members/"+col, ""), http.StatusNotFound, "")
	// A group with probes cannot be deleted (409 with their number).
	e.probes.counts[g.ID] = 3
	r = e.do(http.MethodDelete, "/api/v1/collector-groups/"+g.ID, "")
	cgWant(t, "delete with probes", r, http.StatusConflict, "id")
	if !strings.Contains(r.Body, `"probes":3`) {
		t.Errorf("409 without the number of probes: %s", r.Body)
	}
	delete(e.probes.counts, g.ID)
	cgWant(t, "delete", e.do(http.MethodDelete, "/api/v1/collector-groups/"+g.ID, ""), http.StatusNoContent, "")
	cgWant(t, "get deleted", e.do(http.MethodGet, "/api/v1/collector-groups/"+g.ID, ""), http.StatusNotFound, "")
}

// TestCollectorGroupValidation: mode, liveness timeout 20-300 s and the
// anchor probe (same client) are checked with 422 and their field.
func TestCollectorGroupValidation(t *testing.T) {
	e := newCGMemEnv(t)
	base := `"name":"wp947-v","client_id":"` + cgClient + `"`
	for _, c := range []struct{ name, body, field string }{
		{"blank name", `{"name":"  "}`, "name"},
		{"mode", `{` + base + `,"mode":"active"}`, "mode"},
		{"liveness below 20", `{` + base + `,"liveness_timeout_s":19}`, "liveness_timeout_s"},
		{"liveness above 300", `{` + base + `,"liveness_timeout_s":301}`, "liveness_timeout_s"},
		{"anchor of another client", `{` + base + `,"anchor_probe_id":"` + cgProbe2 + `"}`, "anchor_probe_id"},
		{"unknown anchor", `{` + base + `,"anchor_probe_id":"66666666-6666-4666-8666-000000000000"}`, "anchor_probe_id"},
		{"anchor no uuid", `{` + base + `,"anchor_probe_id":"x"}`, "anchor_probe_id"},
	} {
		cgWant(t, c.name, e.do(http.MethodPost, "/api/v1/collector-groups", c.body), http.StatusUnprocessableEntity, c.field)
	}
	r := e.do(http.MethodPost, "/api/v1/collector-groups", `{`+base+`,"liveness_timeout_s":20,"anchor_probe_id":"`+cgProbe+`"}`)
	cgWant(t, "valid", r, http.StatusCreated, "")
	id := r.Group.ID
	cgWant(t, "patch liveness", e.do(http.MethodPatch, "/api/v1/collector-groups/"+id, `{"liveness_timeout_s":300}`), http.StatusOK, "")
	cgWant(t, "patch liveness 301", e.do(http.MethodPatch, "/api/v1/collector-groups/"+id, `{"liveness_timeout_s":301}`),
		http.StatusUnprocessableEntity, "liveness_timeout_s")
	cgWant(t, "patch mode", e.do(http.MethodPatch, "/api/v1/collector-groups/"+id, `{"mode":"x"}`), http.StatusUnprocessableEntity, "mode")
	cgWant(t, "patch anchor", e.do(http.MethodPatch, "/api/v1/collector-groups/"+id, `{"anchor_probe_id":"`+cgProbe2+`"}`),
		http.StatusUnprocessableEntity, "anchor_probe_id")
	cgWant(t, "patch status", e.do(http.MethodPatch, "/api/v1/collector-groups/"+id, `{"status":"unreachable"}`),
		http.StatusUnprocessableEntity, "status")
	cgWant(t, "empty patch", e.do(http.MethodPatch, "/api/v1/collector-groups/"+id, `{}`), http.StatusBadRequest, "")
	col := e.collector(cgClient, "wp947-v-col")
	cgWant(t, "weight 0", e.do(http.MethodPost, "/api/v1/collector-groups/"+id+"/members", `{"collector_id":"`+col+`","weight":0}`),
		http.StatusUnprocessableEntity, "weight")
	cgWant(t, "collector of another client", e.do(http.MethodPost, "/api/v1/collector-groups/"+id+"/members",
		`{"collector_id":"`+e.collector(cgOther, "wp947-other")+`"}`), http.StatusUnprocessableEntity, "collector_id")
	cgWant(t, "unknown collector", e.do(http.MethodPost, "/api/v1/collector-groups/"+id+"/members",
		`{"collector_id":"99999999-9999-4999-8999-999999999999"}`), http.StatusUnprocessableEntity, "collector_id")
}

// TestCollectorMovesBetweenGroupsAtomically: a collector of another group
// moves in one step; the last member of a group with probes stays (409)
// and nothing changes.
func TestCollectorMovesBetweenGroupsAtomically(t *testing.T) {
	e := newCGMemEnv(t)
	col := e.collector(cgClient, "wp947-move")
	partner := e.collector(cgClient, "wp947-partner")
	own := e.groupOf(col)
	r := e.do(http.MethodPost, "/api/v1/collector-groups", `{"name":"wp947-target","client_id":"`+cgClient+`"}`)
	cgWant(t, "create", r, http.StatusCreated, "")
	target := r.Group.ID

	// The collector's own group runs probes and it is the last member: it
	// stays, the target is unchanged.
	e.probes.counts[own] = 1
	cgWant(t, "move the last member", e.do(http.MethodPost, "/api/v1/collector-groups/"+target+"/members", `{"collector_id":"`+col+`"}`),
		http.StatusConflict, "collector_id")
	if got := e.groupOf(col); got != own {
		t.Fatalf("collector in %s after the refused move, want %s", got, own)
	}
	cgWant(t, "remove the last member", e.do(http.MethodDelete, "/api/v1/collector-groups/"+own+"/members/"+col, ""),
		http.StatusConflict, "collector_id")

	// With a second member the collector may leave: it moves in one step.
	cgWant(t, "partner joins", e.do(http.MethodPost, "/api/v1/collector-groups/"+own+"/members", `{"collector_id":"`+partner+`"}`),
		http.StatusOK, "")
	r = e.do(http.MethodPost, "/api/v1/collector-groups/"+target+"/members", `{"collector_id":"`+col+`","priority":1}`)
	cgWant(t, "move", r, http.StatusOK, "")
	if len(r.Group.Members) != 1 || r.Group.Members[0].CollectorID != col || r.Group.Members[0].Priority != 1 {
		t.Fatalf("target members %+v", r.Group.Members)
	}
	if got := e.groupOf(col); got != target {
		t.Fatalf("collector in %s, want %s", got, target)
	}
	g := e.do(http.MethodGet, "/api/v1/collector-groups/"+own, "")
	if len(g.Group.Members) != 1 || g.Group.Members[0].CollectorID != partner {
		t.Fatalf("old group members %+v, want only the partner", g.Group.Members)
	}
}
