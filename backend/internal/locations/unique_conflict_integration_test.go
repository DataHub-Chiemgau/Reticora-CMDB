package locations_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLocationsUniqueConflictNamesField covers LOC-03..06 and API-05
// (WP-107, WP-500, F-302): POST and PATCH /api/v1/locations answer a name
// that is taken below the same parent (site per client, building per site,
// room per building, rack per room) with 409 naming the field name, as
// POST /sites does, and store nothing; the same name below another parent
// is 201.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationsUniqueConflictNamesField(t *testing.T) {
	f := scopetest.Seed(t, "5e")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	mux := chi.NewRouter()
	locations.NewHandler(locations.NewPGRepository(f.App)).RegisterRoutes(mux)
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		out := map[string]any{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %d %q: %v", method, path, w.Code, w.Body.String(), err)
		}
		return w.Code, out
	}
	create := func(body string) string {
		t.Helper()
		code, out := do(http.MethodPost, "/api/v1/locations", body)
		if code != http.StatusCreated {
			t.Fatalf("POST %s = %d %v, want 201", body, code, out)
		}
		return out["id"].(string)
	}
	count := func(kind, name string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(t.Context(),
			`SELECT count(*) FROM location WHERE organization_id = $1 AND kind = $2 AND name = $3`, f.OrgA, kind, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	wantNameConflict := func(t *testing.T, what string, code int, out map[string]any) {
		t.Helper()
		if code != http.StatusConflict || out["field"] != "name" {
			t.Errorf("%s = %d %v, want 409 with field name", what, code, out)
		}
	}

	// Two branches site > building > room > rack of client 1; the second
	// site, building and room are the "other parent" of each level.
	site1 := create(`{"kind":"site","client_id":"` + f.Client1 + `","name":"uq-site"}`)
	site2 := create(`{"kind":"site","client_id":"` + f.Client1 + `","name":"uq-site-2"}`)
	building1 := create(`{"kind":"building","parent_id":"` + site1 + `","name":"uq-building"}`)
	building2 := create(`{"kind":"building","parent_id":"` + site2 + `","name":"uq-building-2"}`)
	room1 := create(`{"kind":"room","parent_id":"` + building1 + `","name":"uq-room"}`)
	room2 := create(`{"kind":"room","parent_id":"` + building2 + `","name":"uq-room-2"}`)
	rack1 := create(`{"kind":"rack","parent_id":"` + room1 + `","name":"uq-rack"}`)
	rack2 := create(`{"kind":"rack","parent_id":"` + room2 + `","name":"uq-rack-2"}`)

	for _, level := range []struct {
		kind, name string
		dup, other string // POST bodies: same parent, other parent
	}{
		{"site", "uq-site",
			`{"kind":"site","client_id":"` + f.Client1 + `","name":"uq-site"}`,
			`{"kind":"site","client_id":"` + f.Client2 + `","name":"uq-site"}`},
		{"building", "uq-building",
			`{"kind":"building","parent_id":"` + site1 + `","name":"uq-building"}`,
			`{"kind":"building","parent_id":"` + site2 + `","name":"uq-building"}`},
		{"room", "uq-room",
			`{"kind":"room","parent_id":"` + building1 + `","name":"uq-room"}`,
			`{"kind":"room","parent_id":"` + building2 + `","name":"uq-room"}`},
		{"rack", "uq-rack",
			`{"kind":"rack","parent_id":"` + room1 + `","name":"uq-rack"}`,
			`{"kind":"rack","parent_id":"` + room2 + `","name":"uq-rack"}`},
	} {
		t.Run(level.kind, func(t *testing.T) {
			code, out := do(http.MethodPost, "/api/v1/locations", level.dup)
			wantNameConflict(t, "POST duplicate "+level.kind, code, out)
			if n := count(level.kind, level.name); n != 1 {
				t.Errorf("%d %s rows named %s after the duplicate, want 1", n, level.kind, level.name)
			}
			if code, out = do(http.MethodPost, "/api/v1/locations", level.other); code != http.StatusCreated {
				t.Errorf("POST same %s name below another parent = %d %v, want 201", level.kind, code, out)
			}
		})
	}

	// PATCH: renaming onto a taken name and moving below a parent that has
	// a node of that name are 409 on name and change nothing.
	for _, c := range []struct {
		what, id, body, kind, keep string
	}{
		{"rename site onto a taken name", site2, `{"name":"uq-site"}`, "site", "uq-site-2"},
		{"rename building onto a taken name", building2, `{"name":"uq-building"}`, "building", "uq-building-2"},
		{"rename room onto a taken name", room2, `{"name":"uq-room"}`, "room", "uq-room-2"},
		{"rename rack onto a taken name", rack2, `{"name":"uq-rack"}`, "rack", "uq-rack-2"},
	} {
		t.Run(c.what, func(t *testing.T) {
			code, out := do(http.MethodPatch, "/api/v1/locations/"+c.id, c.body)
			wantNameConflict(t, "PATCH "+c.what, code, out)
			if n := count(c.kind, c.keep); n != 1 {
				t.Errorf("%s %s renamed despite the conflict (%d rows keep the name)", c.kind, c.id, n)
			}
		})
	}
	t.Run("move rack below a room with a rack of that name", func(t *testing.T) {
		// room2 holds "uq-rack" since the POST below another parent above.
		code, out := do(http.MethodPatch, "/api/v1/locations/"+rack1, `{"parent_id":"`+room2+`"}`)
		wantNameConflict(t, "PATCH move rack", code, out)
		var parent string
		if err := f.Admin.QueryRow(t.Context(), `SELECT room_id::text FROM rack WHERE id = $1`, rack1).Scan(&parent); err != nil {
			t.Fatal(err)
		}
		if parent != room1 {
			t.Errorf("rack moved to %s despite the conflict, want %s", parent, room1)
		}
	})
	t.Run("rename to a free name", func(t *testing.T) {
		if code, out := do(http.MethodPatch, "/api/v1/locations/"+rack2, `{"name":"uq-rack-3"}`); code != http.StatusOK {
			t.Errorf("PATCH rename rack to a free name = %d %v, want 200", code, out)
		}
	})
}
