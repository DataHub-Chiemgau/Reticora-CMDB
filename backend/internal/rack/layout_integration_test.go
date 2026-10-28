package rack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRackLayoutAgainstPostgres covers the rack contract of RCK-02 (WP-175,
// WP-579, F-252, F-253) on PostgreSQL through the handler: GET
// /racks/{id}/layout of an empty rack answers "mounts": [] and of a filled
// one the mounts by position with the names of the CI and its type (joined
// under RLS: a client-restricted principal sees neither the mount nor the
// name of a CI of another client); PUT /racks/{id}/mounts/{ci_id} answers
// 201 for a new mount, 200 for a moved one and 409 for an overlap, a face
// collision and a position beyond the rack height, and stores nothing on
// 409. The old rack paths answer with the Deprecation, Sunset and Link
// headers of api/openapi.yaml, each path on its own; the paths of RCK-02
// carry none.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestRackLayoutAgainstPostgres' ./internal/rack/
func TestRackLayoutAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "db")
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := rack.NewPGRepository(f.App)
	room := f.Room(t, f.Client1, "layout-db-room")
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "layout-db-rack", HeightUnits: 10, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(orgCtx, rk); err != nil {
		t.Fatal(err)
	}
	ci1 := f.CI(t, f.OrgA, f.Client1, "layout-db-ci-1")
	ci2 := f.CI(t, f.OrgA, f.Client1, "layout-db-ci-2")
	ci3 := f.CI(t, f.OrgA, f.Client1, "layout-db-ci-3")
	ci4 := f.CI(t, f.OrgA, f.Client1, "layout-db-ci-4")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "layout-db-ci-client-2")

	validator := middleware.NewOpenAPIValidator()
	if loaded, err := validator.CheckSpec(true); !loaded || err != nil {
		t.Fatalf("api/openapi.yaml not loaded: %v", err)
	}
	mux := chi.NewRouter()
	mux.Use(validator.DeprecationHeaders)
	rack.NewHandler(repo).RegisterRoutes(mux)
	do := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r.WithContext(ctx))
		return w
	}
	type layoutMount struct {
		ID        string  `json:"id"`
		CIID      string  `json:"ci_id"`
		PositionU int     `json:"position_u"`
		HeightU   int     `json:"height_u"`
		Face      string  `json:"face"`
		CIName    *string `json:"ci_name"`
		CIType    *string `json:"ci_type"`
	}
	type layoutBody struct {
		Rack   *rack.Rack       `json:"rack"`
		Mounts []layoutMount    `json:"mounts"`
		Free   map[string][]int `json:"free"`
	}
	layout := func(ctx context.Context) (layoutBody, string) {
		t.Helper()
		w := do(ctx, http.MethodGet, "/api/v1/racks/"+rk.ID+"/layout", "")
		var l layoutBody
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &l) != nil {
			t.Fatalf("GET layout = %d %.300s, want 200", w.Code, w.Body.String())
		}
		return l, w.Body.String()
	}
	put := func(ci, body string) *httptest.ResponseRecorder {
		t.Helper()
		return do(orgCtx, http.MethodPut, "/api/v1/racks/"+rk.ID+"/mounts/"+ci, body)
	}
	mountsOf := func(ci string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM rack_mount WHERE ci_id = $1`, ci).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("empty rack", func(t *testing.T) {
		w := do(orgCtx, http.MethodGet, "/api/v1/racks/"+rk.ID+"/layout", "")
		var raw map[string]json.RawMessage
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &raw) != nil {
			t.Fatalf("GET layout of the empty rack = %d %.300s", w.Code, w.Body.String())
		}
		if got := string(raw["mounts"]); got != "[]" {
			t.Errorf("mounts of the empty rack = %s, want [] (F-253)", got)
		}
		l, _ := layout(orgCtx)
		if l.Rack == nil || l.Rack.ID != rk.ID || len(l.Free["front"]) != 10 || len(l.Free["rear"]) != 10 {
			t.Errorf("layout of the empty rack: rack %+v, free %v; want the rack and 10 free units per face", l.Rack, l.Free)
		}
	})

	var ci1Mount rack.RackMount
	t.Run("PUT mounts and moves", func(t *testing.T) {
		w := put(ci1, `{"position_u":1,"height_u":2}`)
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &ci1Mount) != nil || ci1Mount.CIID != ci1 || ci1Mount.RackID != rk.ID {
			t.Fatalf("PUT new mount of ci 1 = %d %.300s, want 201 with the mount", w.Code, w.Body.String())
		}
		if w = put(ci2, `{"position_u":5,"face":"rear"}`); w.Code != http.StatusCreated {
			t.Fatalf("PUT new rear mount of ci 2 = %d %.300s, want 201", w.Code, w.Body.String())
		}
		w = put(ci1, `{"position_u":7,"height_u":2}`)
		var moved rack.RackMount
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &moved) != nil || moved.ID != ci1Mount.ID || moved.PositionU != 7 {
			t.Fatalf("PUT move of ci 1 = %d %.300s, want 200 with the same mount at U7", w.Code, w.Body.String())
		}
		if n := mountsOf(ci1); n != 1 {
			t.Errorf("ci 1 has %d mounts after the move, want 1", n)
		}
	})

	t.Run("PUT conflicts are 409 and store nothing", func(t *testing.T) {
		for _, tc := range []struct {
			name, ci, body string
		}{
			{"overlap on the front", ci3, `{"position_u":8}`},
			{"both faces over a rear mount", ci3, `{"position_u":5,"face":"both"}`},
			{"beyond the rack height", ci3, `{"position_u":10,"height_u":2}`},
			{"move onto an occupied unit", ci1, `{"position_u":5,"face":"rear"}`},
			{"move beyond the rack height", ci1, `{"position_u":9,"height_u":3}`},
		} {
			w := put(tc.ci, tc.body)
			if w.Code != http.StatusConflict || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("%s: PUT %s = %d %q %.300s, want 409 problem+json", tc.name, tc.body, w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
		}
		if n := mountsOf(ci3); n != 0 {
			t.Errorf("ci 3 has %d mounts after the refused PUTs, want 0", n)
		}
		var pos int
		if err := f.Admin.QueryRow(context.Background(), `SELECT position_u FROM rack_mount WHERE id = $1`, ci1Mount.ID).Scan(&pos); err != nil || pos != 7 {
			t.Errorf("ci 1 after the refused moves: position %d (%v), want 7", pos, err)
		}
	})

	t.Run("filled layout names CI and type under RLS", func(t *testing.T) {
		// A client-2 CI in the client-1 rack: the org-wide principal sees it
		// with its name, the client-1 principal neither the mount nor the name.
		if err := repo.CreateMount(orgCtx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: foreignCI, PositionU: 3, HeightU: 1, Face: "front"}); err != nil {
			t.Fatalf("org-wide mount of the client-2 CI: %v", err)
		}
		type entry struct {
			ci, name, face string
			pos            int
		}
		want := map[string][]entry{
			"org-wide": {
				{foreignCI, "layout-db-ci-client-2", "front", 3},
				{ci2, "layout-db-ci-2", "rear", 5},
				{ci1, "layout-db-ci-1", "front", 7},
			},
			"client 1": {
				{ci2, "layout-db-ci-2", "rear", 5},
				{ci1, "layout-db-ci-1", "front", 7},
			},
		}
		for name, ctx := range map[string]context.Context{"org-wide": orgCtx, "client 1": client1Ctx} {
			l, body := layout(ctx)
			var got []entry
			for _, m := range l.Mounts {
				if m.CIName == nil || m.CIType == nil {
					t.Errorf("%s: mount of %s without ci_name or ci_type: %.300s", name, m.CIID, body)
					continue
				}
				if *m.CIType != "server" {
					t.Errorf("%s: ci_type of %s = %q, want server", name, m.CIID, *m.CIType)
				}
				got = append(got, entry{m.CIID, *m.CIName, m.Face, m.PositionU})
			}
			if len(got) != len(want[name]) {
				t.Errorf("%s: layout mounts %+v, want %+v", name, got, want[name])
				continue
			}
			for i := range got {
				if got[i] != want[name][i] {
					t.Errorf("%s: layout mount %d = %+v, want %+v", name, i, got[i], want[name][i])
				}
			}
			if name == "client 1" && strings.Contains(body, "layout-db-ci-client-2") {
				t.Errorf("client 1: the layout names the client-2 CI: %.300s", body)
			}
		}
		l, _ := layout(orgCtx)
		for _, u := range []int{3, 7, 8} {
			if slices.Contains(l.Free["front"], u) {
				t.Errorf("free front %v holds the occupied unit %d", l.Free["front"], u)
			}
		}
		if slices.Contains(l.Free["rear"], 5) || len(l.Free["rear"]) != 9 {
			t.Errorf("free rear %v, want every unit but 5", l.Free["rear"])
		}
	})

	t.Run("old rack paths carry Deprecation and Sunset", func(t *testing.T) {
		spec, err := os.ReadFile("../../../api/openapi.yaml")
		if err != nil {
			t.Fatal(err)
		}
		deprecated, err := middleware.ParseDeprecations(spec)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			operation, method, path, body string
			status                        int
			successor                     string
		}{
			{"GET /api/v1/racks/{id}/mounts", http.MethodGet, "/api/v1/racks/" + rk.ID + "/mounts", "", http.StatusOK, "/api/v1/racks/" + rk.ID + "/layout"},
			{"POST /api/v1/racks/{id}/mounts", http.MethodPost, "/api/v1/racks/" + rk.ID + "/mounts", `{"ci_id":"` + ci4 + `","position_u":1}`, http.StatusCreated, "/api/v1/racks/" + rk.ID + "/mounts/{ci_id}"},
			{"GET /api/v1/rack-mounts/{id}", http.MethodGet, "/api/v1/rack-mounts/" + ci1Mount.ID, "", http.StatusOK, "/api/v1/mounts/" + ci1Mount.ID},
			{"PATCH /api/v1/rack-mounts/{id}", http.MethodPatch, "/api/v1/rack-mounts/" + ci1Mount.ID, `{"position_u":7}`, http.StatusOK, "/api/v1/mounts/" + ci1Mount.ID},
			{"DELETE /api/v1/rack-mounts/{id}", http.MethodDelete, "/api/v1/rack-mounts/" + ci1Mount.ID, "", http.StatusNoContent, "/api/v1/mounts/" + ci1Mount.ID},
		} {
			d, ok := deprecated[tc.operation]
			if !ok {
				t.Errorf("%s is not deprecated in api/openapi.yaml", tc.operation)
				continue
			}
			w := do(orgCtx, tc.method, tc.path, tc.body)
			if w.Code != tc.status {
				t.Errorf("%s = %d %.300s, want %d", tc.operation, w.Code, w.Body.String(), tc.status)
			}
			wantDeprecation := "@" + strconv.FormatInt(d.DeprecatedAt.Unix(), 10)
			wantSunset := d.Sunset.UTC().Format(http.TimeFormat)
			if got := w.Header().Get("Deprecation"); got != wantDeprecation {
				t.Errorf("%s: Deprecation %q, want %q", tc.operation, got, wantDeprecation)
			}
			if got := w.Header().Get("Sunset"); got != wantSunset {
				t.Errorf("%s: Sunset %q, want %q", tc.operation, got, wantSunset)
			}
			// A successor with a placeholder the old path does not fill
			// ({ci_id}) has no Link.
			wantLink := ""
			if !strings.Contains(tc.successor, "{") {
				wantLink = "<" + tc.successor + `>; rel="successor-version"`
			}
			if got := w.Header().Get("Link"); got != wantLink {
				t.Errorf("%s: Link %q, want %q", tc.operation, got, wantLink)
			}
		}
		for _, path := range []string{
			"GET /api/v1/racks/" + rk.ID + "/layout",
			"PUT /api/v1/racks/" + rk.ID + "/mounts/" + ci3,
			"DELETE /api/v1/racks/" + rk.ID + "/mounts/" + ci3,
		} {
			method, target, _ := strings.Cut(path, " ")
			body := ""
			if method == http.MethodPut {
				body = `{"position_u":9}`
			}
			w := do(orgCtx, method, target, body)
			if w.Code >= 300 || w.Header().Get("Deprecation") != "" || w.Header().Get("Sunset") != "" {
				t.Errorf("%s = %d, Deprecation %q, Sunset %q; want success without lifecycle headers (RCK-02)",
					path, w.Code, w.Header().Get("Deprecation"), w.Header().Get("Sunset"))
			}
		}
	})
}
