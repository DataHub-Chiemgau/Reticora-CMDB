package rack_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// scopeRefusal is the fixed 409 detail of a placement that would change the
// location of a row outside the principal's scope (F-379, F-380).
const scopeRefusal = "the asset of the CI or another CI of the asset is outside your scope; " +
	"the location cannot follow the placement"

// setAssetClient sets the client of an asset through the maintenance pool
// (empty for an org-wide asset) and checks it.
func setAssetClient(t *testing.T, f *scopetest.Fixture, assetID, clientID string) {
	t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	var got string
	if err := f.Admin.QueryRow(context.Background(), `UPDATE asset SET client_id = $2 WHERE id = $1
		RETURNING COALESCE(client_id::text, '')`, assetID, client).Scan(&got); err != nil || got != clientID {
		t.Fatalf("client of asset %s: %q err %v, want %q", assetID, got, err, clientID)
	}
}

// placementSnapshot is the state a refused placement must leave untouched:
// the location of the asset, the placement state of each CI and the mounts.
type placementSnapshot struct {
	asset  string
	cis    map[string]placementState
	mounts []string
}

func snapshotPlacement(t *testing.T, f *scopetest.Fixture, assetID string, ciIDs ...string) placementSnapshot {
	t.Helper()
	s := placementSnapshot{asset: assetLocation(t, f, assetID), cis: map[string]placementState{}}
	for _, id := range ciIDs {
		s.cis[id] = readPlacementState(t, f, id)
	}
	rows, err := f.Admin.Query(context.Background(), `SELECT ci_id::text || '@' || rack_id::text || ':' || position_u
		FROM rack_mount WHERE ci_id = ANY($1::uuid[]) ORDER BY ci_id, rack_id`, ciIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if err = rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		s.mounts = append(s.mounts, m)
	}
	return s
}

// unchangedSince fails when anything of before changed: asset location,
// location, version, ci_change and audit entries of the CIs, mounts.
func unchangedSince(t *testing.T, f *scopetest.Fixture, name, assetID string, before placementSnapshot) {
	t.Helper()
	ids := make([]string, 0, len(before.cis))
	for id := range before.cis {
		ids = append(ids, id)
	}
	after := snapshotPlacement(t, f, assetID, ids...)
	if after.asset != before.asset {
		t.Errorf("%s: asset location %q, want %q", name, after.asset, before.asset)
	}
	for id, b := range before.cis {
		a := after.cis[id]
		if a.location != b.location || a.version != b.version || !equalStrings(a.changes, b.changes) ||
			!equalStrings(a.audits, b.audits) {
			t.Errorf("%s: CI %s changed:\n before %+v\n after  %+v", name, id, b, a)
		}
	}
	if !equalStrings(after.mounts, before.mounts) {
		t.Errorf("%s: mounts %v, want %v", name, after.mounts, before.mounts)
	}
}

// wantScopeRefusal checks that err is the 409 refusal of a placement outside
// the principal's scope.
func wantScopeRefusal(t *testing.T, name string, err error) {
	t.Helper()
	var rule *rack.RuleError
	if !errors.As(err, &rule) || !errors.Is(err, rack.ErrConflict) || rule.Detail != scopeRefusal {
		t.Errorf("%s: got %v, want the 409 scope refusal %q", name, err, scopeRefusal)
	}
}

// TestRackPlacementRefusesAssetOutsideScope covers F-379 (WP-650; RCK-03,
// TEN-02) against PostgreSQL as the runtime role: a principal restricted to
// client 1 mounts its CI Z whose asset has no client. asset_scoped_update
// lets only org-wide principals update that asset, so the location of the
// asset (and with it Z's) cannot become the rack. The mount is refused with
// 409 and a fixed text, also through PUT /racks/{id}/mounts/{ci_id}; no
// mount, location, version, ci_change or audit entry is stored. An org-wide
// principal mounts Z and the location follows; the scoped principal may then
// reposition Z within the rack (no location change), but moving it to
// another rack or unmounting it is refused the same way and keeps mount and
// location.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestRackPlacementRefuses' ./internal/rack/
func TestRackPlacementRefusesAssetOutsideScope(t *testing.T) {
	f := scopetest.Seed(t, "69")
	loc := newPlacementTree(t, f, f.Client1, "scope-a")
	z := f.CI(t, f.OrgA, f.Client1, "scope-a-z")
	assetID := sharedAsset(t, f, "SCOPE-A", z)
	setAssetClient(t, f, assetID, "")
	repo := rack.NewPGRepository(f.App)
	scoped := f.ClientCtx(f.Client1)

	before := snapshotPlacement(t, f, assetID, z)
	err := repo.CreateMount(scoped, &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack1, CIID: z,
		PositionU: 1, HeightU: 1, Face: "front"})
	wantScopeRefusal(t, "scoped mount", err)
	unchangedSince(t, f, "scoped mount", assetID, before)

	// The same refusal over HTTP: 409 problem with the fixed text.
	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/racks/"+loc.rack1+"/mounts/"+z,
		strings.NewReader(`{"position_u":1,"height_u":1,"face":"front"}`))
	req = req.WithContext(tenant.WithTenant(scoped, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	if w.Code != http.StatusConflict || problem.Detail != scopeRefusal ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Errorf("PUT mount as the scoped principal: %d %s %s, want 409 problem %q", w.Code,
			w.Header().Get("Content-Type"), w.Body.String(), scopeRefusal)
	}
	unchangedSince(t, f, "scoped PUT mount", assetID, before)

	// An org-wide principal mounts Z; the location follows.
	m := &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack1, CIID: z, PositionU: 1, HeightU: 1, Face: "front"}
	if err = repo.CreateMount(f.OrgCtx(f.OrgA), m); err != nil {
		t.Fatalf("org-wide mount: %v", err)
	}
	if a, s := assetLocation(t, f, assetID), readPlacementState(t, f, z); a != loc.rack1 || s.location != loc.rack1 {
		t.Fatalf("org-wide mount: asset %q Z %q, want %q", a, s.location, loc.rack1)
	}

	// Repositioning within the rack changes no location and stays allowed.
	pos := 7
	if _, err = repo.UpdateMount(scoped, f.OrgA, m.ID, rack.UpdateMountRequest{PositionU: &pos}); err != nil {
		t.Fatalf("scoped repositioning within the rack: %v", err)
	}
	before = snapshotPlacement(t, f, assetID, z)
	_, err = repo.UpdateMount(scoped, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &loc.rack2})
	wantScopeRefusal(t, "scoped move", err)
	unchangedSince(t, f, "scoped move", assetID, before)
	wantScopeRefusal(t, "scoped unmount", repo.DeleteMount(scoped, f.OrgA, m.ID))
	unchangedSince(t, f, "scoped unmount", assetID, before)
}

// TestRackPlacementRefusesAssetWithCIsOutsideScope covers F-380 (WP-650;
// RCK-03, TEN-02) against PostgreSQL as the runtime role: the asset of CI Z
// (both client 1) also carries a CI the principal restricted to client 1
// cannot write: Y of client 2 (invisible) or W without client (visible,
// org-wide). trg_asset_propagate_location runs under the principal's RLS
// and would move only Z; asset and Z would disagree with Y or W. The mount is
// refused with 409 and nothing is stored; asset, Z and the other CI keep one
// location. The trigger refuses such an asset update for every writer
// (42501, asset_location_ci_scope), and a principal of clients 1 and 2, who
// sees Y, gets the colocation handling of WP-578: the placement is stored,
// the locations stay and the audit names the reason.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRackPlacementRefusesAssetWithCIsOutsideScope(t *testing.T) {
	f := scopetest.Seed(t, "69")
	loc := newPlacementTree(t, f, f.Client1, "scope-b")
	repo := rack.NewPGRepository(f.App)
	scoped := f.ClientCtx(f.Client1)

	for i, c := range []struct {
		name, otherClient string
	}{
		{"invisible CI of client 2", f.Client2},
		{"visible CI without client", ""},
	} {
		tag := []string{"scope-b-y", "scope-b-w"}[i]
		z := f.CI(t, f.OrgA, f.Client1, tag+"-z")
		other := f.CI(t, f.OrgA, c.otherClient, tag)
		assetID := sharedAsset(t, f, strings.ToUpper(tag), z, other)
		setAssetClient(t, f, assetID, f.Client1)

		before := snapshotPlacement(t, f, assetID, z, other)
		err := repo.CreateMount(scoped, &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack1, CIID: z,
			PositionU: 1 + 2*i, HeightU: 1, Face: "front"})
		wantScopeRefusal(t, c.name, err)
		unchangedSince(t, f, c.name, assetID, before)
		if a, zs, os := assetLocation(t, f, assetID), readPlacementState(t, f, z), readPlacementState(t, f, other); a != zs.location || a != os.location {
			t.Errorf("%s: asset %q, Z %q, other CI %q disagree", c.name, a, zs.location, os.location)
		}

		// The trigger refuses the asset update of any writer of the scope.
		err = database.WithRequestTenant(scoped, f.App, f.OrgA,
			func(ctx context.Context, tx pgx.Tx) error {
				_, uerr := tx.Exec(ctx, `UPDATE asset SET location_id = $2 WHERE id = $1`, assetID, loc.rack2)
				return uerr
			})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" || pgErr.ConstraintName != "asset_location_ci_scope" {
			t.Errorf("%s: scoped asset update: got %v, want 42501 asset_location_ci_scope", c.name, err)
		}
		unchangedSince(t, f, c.name+", direct asset update", assetID, before)
	}

	// A principal of clients 1 and 2 sees Y: the rack of client 1 is foreign
	// to Y, the placement is stored and the locations stay (colocation).
	both := database.OrgWideScope(f.OrgA, f.User)
	both.Clients = database.ScopeIDs(f.Client1, f.Client2)
	bothCtx := database.ContextWithTenantScope(context.Background(), &both)
	z := f.CI(t, f.OrgA, f.Client1, "scope-b-c-z")
	y := f.CI(t, f.OrgA, f.Client2, "scope-b-c-y")
	assetID := sharedAsset(t, f, "SCOPE-B-C", z, y)
	setAssetClient(t, f, assetID, f.Client1)
	bz, by := readPlacementState(t, f, z), readPlacementState(t, f, y)
	if err := repo.CreateMount(bothCtx, &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack2, CIID: z,
		PositionU: 1, HeightU: 1, Face: "front"}); err != nil {
		t.Fatalf("mount by a principal of clients 1 and 2: %v", err)
	}
	az, ay := readPlacementState(t, f, z), readPlacementState(t, f, y)
	if az.location != bz.location || ay.location != by.location || assetLocation(t, f, assetID) != "" ||
		az.version != bz.version || ay.version != by.version {
		t.Errorf("colocation by a principal of clients 1 and 2: Z %+v Y %+v asset %q", az, ay, assetLocation(t, f, assetID))
	}
	var reason string
	if err := f.Admin.QueryRow(context.Background(), `SELECT COALESCE(changes->>'location_unchanged', '')
		FROM audit_log WHERE resource_id = $1 ORDER BY seq DESC LIMIT 1`, z).Scan(&reason); err != nil ||
		reason != "the rack belongs to another client" {
		t.Errorf("colocation audit reason %q err %v", reason, err)
	}
}
