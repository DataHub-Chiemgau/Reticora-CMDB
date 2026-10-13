package rack_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRackRulesAgainstPostgres covers RCK-01 (WP-126) against PostgreSQL:
// collisions and a mount beyond the rack are 409; the exclusion constraint
// is face-aware (front [1,2), rear [2,3), both [1,3)) and holds for
// concurrent writers and direct inserts (TST-01); a child CI of a
// composition is not mounted separately, and a mounted CI cannot become one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRackRulesAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "8f")
	bg := context.Background()
	var roomID string
	if err := f.Admin.QueryRow(bg, `
		WITH s AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'rck01') RETURNING id),
		     b AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, 'rck01' FROM s RETURNING id)
		INSERT INTO room (organization_id, building_id, name) SELECT $1, id, 'rck01' FROM b RETURNING id::text`,
		f.OrgA, f.Client1).Scan(&roomID); err != nil {
		t.Fatal(err)
	}
	repo := rack.NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: roomID, Name: "rck01-rack", HeightUnits: 10}
	if err := repo.CreateRack(ctx, rk); err != nil {
		t.Fatal(err)
	}
	ci := func(name string) string { return f.CI(t, f.OrgA, f.Client1, name) }
	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	mount := func(ciID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/racks/"+rk.ID+"/mounts",
			bytes.NewBufferString(`{"ci_id":"`+ciID+`",`+body+`}`)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Face ranges: front and rear share units, both collides with either.
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"front 1-2", `"position_u":1,"height_u":2,"face":"front"`, http.StatusCreated},
		{"rear 1-2 next to front", `"position_u":1,"height_u":2,"face":"rear"`, http.StatusCreated},
		{"both over front and rear", `"position_u":2,"height_u":1,"face":"both"`, http.StatusConflict},
		{"front overlapping front", `"position_u":2,"height_u":2,"face":"front"`, http.StatusConflict},
		{"both on free units", `"position_u":3,"height_u":2,"face":"both"`, http.StatusCreated},
		{"rear under both", `"position_u":4,"height_u":1,"face":"rear"`, http.StatusConflict},
		{"beyond the rack", `"position_u":9,"height_u":3,"face":"front"`, http.StatusConflict},
		{"position 0", `"position_u":0,"height_u":1,"face":"front"`, http.StatusBadRequest},
	} {
		if w := mount(ci("rck01-"+tc.name), tc.body); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.status)
		}
	}

	// The database holds the rule on its own (TST-01): a direct insert of a
	// both mount over the front mount is rejected by the exclusion.
	if _, err := f.Admin.Exec(bg, `INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face)
		VALUES ($1, $2, $3, 1, 1, 'both')`, f.OrgA, rk.ID, ci("rck01-direct")); err == nil {
		t.Error("direct insert of an overlapping both mount accepted")
	}

	// Concurrent writers for the same free units: exactly one wins, the
	// others get a conflict.
	var wg sync.WaitGroup
	codes := make(chan int, 4)
	for i := range 4 {
		ciID := ci("rck01-race-" + string(rune('a'+i)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- mount(ciID, `"position_u":7,"height_u":2,"face":"front"`).Code
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("concurrent mount: %d, want 201 or 409", code)
		}
	}
	if created != 1 {
		t.Errorf("concurrent mounts created %d, want exactly 1", created)
	}

	// Compositions (AST-05): the child is not mounted separately; a mounted
	// CI does not become a child.
	chassis, blade := ci("rck01-chassis"), ci("rck01-blade")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET parent_ci_id = $1 WHERE id = $2`, chassis, blade); err != nil {
		t.Fatal(err)
	}
	if w := mount(blade, `"position_u":6,"height_u":1,"face":"front"`); w.Code != http.StatusConflict {
		t.Errorf("mount a composition child: %d %s, want 409", w.Code, w.Body.String())
	}
	if w := mount(chassis, `"position_u":6,"height_u":1,"face":"front"`); w.Code != http.StatusCreated {
		t.Errorf("mount the composition parent: %d %s", w.Code, w.Body.String())
	}
	other := ci("rck01-other-chassis")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET parent_ci_id = $1 WHERE id = $2`, other, chassis); err == nil {
		t.Error("a mounted CI became part of a composition")
	}
}
