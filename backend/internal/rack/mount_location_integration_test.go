package rack_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/jackc/pgx/v5"
)

// TestMountSetsLocationWithHistory covers WP-078 (RCK-03, DB-05) against
// PostgreSQL: mounting sets the CI's location to the rack, moving to another
// rack follows, unmounting sets the rack's room; for a CI linked to an asset
// the asset's location is set and mirrored to the CI. Every change writes
// ci_change (movement, location_change) and an audit entry in the same
// transaction.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMountSetsLocationWithHistory(t *testing.T) {
	f := scopetest.Seed(t, "69")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	tree := locations.NewPGRepository(f.App)
	newLocation := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	site := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "mount-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "mount-b1"})
	room := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "mount-r1"})
	rack1 := newLocation(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "mount-k1"})
	rack2 := newLocation(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "mount-k2"})

	repo := rack.NewPGRepository(f.App)
	locationOf := func(ciID string) string {
		t.Helper()
		var loc string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM ci WHERE id = $1`, ciID).Scan(&loc); err != nil {
			t.Fatal(err)
		}
		return loc
	}
	history := func(ciID string) (changes, audits []string) {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT change_type || ':' || COALESCE(field_name, '') FROM ci_change
			WHERE ci_id = $1 AND change_type IN ('movement', 'location_change') ORDER BY created_at`, ciID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			changes = append(changes, c)
		}
		rows.Close()
		rows, err = f.Admin.Query(bg, `SELECT action FROM audit_log WHERE resource_id = $1 AND action LIKE 'ci.rack_%' ORDER BY timestamp`, ciID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			audits = append(audits, a)
		}
		rows.Close()
		return changes, audits
	}

	// A CI without an asset: mount, reposition, move, unmount.
	ciID := f.CI(t, f.OrgA, f.Client1, "mount-ci")
	m := &rack.RackMount{OrganizationID: f.OrgA, RackID: rack1, CIID: ciID, PositionU: 1, HeightU: 2, Face: "front"}
	if err := repo.CreateMount(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != rack1 {
		t.Fatalf("location after mount: %q, want rack %q", got, rack1)
	}
	pos := 10
	if _, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{PositionU: &pos}); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != rack1 {
		t.Errorf("location after repositioning: %q", got)
	}
	if _, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &rack2}); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != rack2 {
		t.Errorf("location after moving to rack 2: %q, want %q", got, rack2)
	}
	if err := repo.DeleteMount(ctx, f.OrgA, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != room {
		t.Errorf("location after unmount: %q, want the room %q", got, room)
	}
	changes, audits := history(ciID)
	// Four movements; the location changed on mount, move and unmount.
	count := map[string]int{}
	for _, c := range changes {
		count[c]++
	}
	if count["movement:rack_mount"] != 4 || count["location_change:location_id"] != 3 || len(changes) != 7 {
		t.Errorf("ci_change rows %v", changes)
	}
	wantAudits := []string{"ci.rack_mounted", "ci.rack_moved", "ci.rack_moved", "ci.rack_unmounted"}
	if len(audits) != len(wantAudits) {
		t.Fatalf("audit entries %v, want %v", audits, wantAudits)
	}
	for i := range wantAudits {
		if audits[i] != wantAudits[i] {
			t.Errorf("audit %d: %s, want %s", i, audits[i], wantAudits[i])
		}
	}

	// A CI linked to an asset: the asset's location is set and mirrored.
	linked := f.CI(t, f.OrgA, f.Client1, "mount-linked")
	var assetID string
	err := database.WithTenant(bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO asset (organization_id, asset_tag, name, ci_id) VALUES ($1, 'MOUNT-A', 'MOUNT-A', $2)
			RETURNING id::text`, f.OrgA, linked).Scan(&assetID)
	})
	if err != nil {
		t.Fatal(err)
	}
	lm := &rack.RackMount{OrganizationID: f.OrgA, RackID: rack1, CIID: linked, PositionU: 20, HeightU: 1, Face: "front"}
	if err = repo.CreateMount(ctx, lm); err != nil {
		t.Fatal(err)
	}
	var assetLocation string
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, assetID).Scan(&assetLocation); err != nil {
		t.Fatal(err)
	}
	if assetLocation != rack1 || locationOf(linked) != rack1 {
		t.Errorf("linked CI after mount: asset %q, CI %q, want rack %q", assetLocation, locationOf(linked), rack1)
	}
	if _, audits = history(linked); len(audits) != 1 || audits[0] != "ci.rack_mounted" {
		t.Errorf("linked CI audit %v", audits)
	}
}

func scopePtr(orgID, userID string) *database.TenantScope {
	scope := database.OrgWideScope(orgID, userID)
	return &scope
}

// placementFixture is a location tree of organization A for the placement
// tests: a site of a client with one building, one room and two racks.
type placementFixture struct {
	room, rack1, rack2 string
}

func newPlacementTree(t *testing.T, f *scopetest.Fixture, clientID, prefix string) placementFixture {
	t.Helper()
	tree := locations.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	create := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s %s: %v", req.Kind, req.Name, err)
		}
		return loc.ID
	}
	site := create(locations.CreateRequest{Kind: locations.KindSite, ClientID: clientID, Name: prefix + "-s"})
	building := create(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: prefix + "-b"})
	room := create(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: prefix + "-r"})
	return placementFixture{
		room:  room,
		rack1: create(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: prefix + "-k1"}),
		rack2: create(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: prefix + "-k2"}),
	}
}

// sharedAsset links the CIs to one new asset as the runtime role: the first
// one is the asset's primary CI (asset.ci_id), the others link through
// ci.asset_id (000088).
func sharedAsset(t *testing.T, f *scopetest.Fixture, tag string, ciIDs ...string) string {
	t.Helper()
	var assetID string
	err := database.WithTenant(context.Background(), f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO asset (organization_id, asset_tag, name, ci_id) VALUES ($1, $2, $2, $3)
			RETURNING id::text`, f.OrgA, tag, ciIDs[0]).Scan(&assetID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE ci SET asset_id = $2 WHERE organization_id = $1 AND id = ANY($3::uuid[])`,
			f.OrgA, assetID, ciIDs[1:])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return assetID
}

// placementState is the location and version of a CI and its placement
// history: the ci_change rows (movement, location_change) as
// "type:version:old->new" and the audit entries as "action:to".
type placementState struct {
	location string
	version  int64
	changes  []string
	audits   []string
}

func readPlacementState(t *testing.T, f *scopetest.Fixture, ciID string) placementState {
	t.Helper()
	bg := context.Background()
	var s placementState
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, ''), version FROM ci WHERE id = $1`, ciID).
		Scan(&s.location, &s.version); err != nil {
		t.Fatal(err)
	}
	rows, err := f.Admin.Query(bg, `SELECT change_type || ':' || COALESCE(ci_version::text, '') || ':' ||
			CASE WHEN change_type = 'location_change'
			     THEN COALESCE(old_value #>> '{}', '') || '->' || COALESCE(new_value #>> '{}', '') ELSE '' END
		FROM ci_change WHERE ci_id = $1 AND change_type IN ('movement', 'location_change')
		ORDER BY ci_version, created_at, change_type DESC`, ciID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		if err = rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		s.changes = append(s.changes, c)
	}
	rows.Close()
	rows, err = f.Admin.Query(bg, `SELECT action || ':' || COALESCE(changes #>> '{location_id,to}', '')
		FROM audit_log WHERE resource_id = $1 AND resource_type = 'ci' ORDER BY seq`, ciID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		s.audits = append(s.audits, a)
	}
	rows.Close()
	return s
}

func assetLocation(t *testing.T, f *scopetest.Fixture, assetID string) string {
	t.Helper()
	var loc string
	if err := f.Admin.QueryRow(context.Background(), `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, assetID).
		Scan(&loc); err != nil {
		t.Fatal(err)
	}
	return loc
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRackPlacementHistorizesEveryCIOfTheAsset covers WP-578 (RCK-03, DB-05;
// retest F-240, F-241) against PostgreSQL as the runtime role in the tenant
// context: X and Y are CIs of one asset. Mounting X, moving it to another
// rack and unmounting it move both; each of these location changes writes,
// for X and for Y, one location_change row in ci_change with the raised
// version and one audit entry, and raises ci.version of both by exactly 1.
// Repositioning X within its rack changes no location: no version, no
// location_change, no entry for Y. The unmount of the linked CI sets the
// asset's location to the rack's room.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestRackPlacement' ./internal/rack/
func TestRackPlacementHistorizesEveryCIOfTheAsset(t *testing.T) {
	f := scopetest.Seed(t, "69")
	ctx := f.OrgCtx(f.OrgA)
	loc := newPlacementTree(t, f, f.Client1, "sib")
	x := f.CI(t, f.OrgA, f.Client1, "sib-x")
	y := f.CI(t, f.OrgA, f.Client1, "sib-y")
	assetID := sharedAsset(t, f, "SIB-A", x, y)
	x0, y0 := readPlacementState(t, f, x), readPlacementState(t, f, y)

	repo := rack.NewPGRepository(f.App)
	m := &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack1, CIID: x, PositionU: 1, HeightU: 2, Face: "front"}
	// step runs one placement and checks the location of X, Y and the asset
	// and the version of X and Y (raised by 1 when the location changed).
	step := func(name string, op func() error, want string, moved bool) {
		t.Helper()
		before := map[string]int64{x: readPlacementState(t, f, x).version, y: readPlacementState(t, f, y).version}
		if err := op(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := assetLocation(t, f, assetID); got != want {
			t.Errorf("%s: asset location %q, want %q", name, got, want)
		}
		for _, id := range []string{x, y} {
			s := readPlacementState(t, f, id)
			wantVersion := before[id]
			if moved {
				wantVersion++
			}
			if s.location != want || s.version != wantVersion {
				t.Errorf("%s: CI %s location %q version %d, want %q version %d", name, id, s.location, s.version, want, wantVersion)
			}
		}
	}
	step("mount", func() error { return repo.CreateMount(ctx, m) }, loc.rack1, true)
	pos := 10
	step("reposition", func() error {
		_, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{PositionU: &pos})
		return err
	}, loc.rack1, false)
	step("move", func() error {
		_, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &loc.rack2})
		return err
	}, loc.rack2, true)
	step("unmount", func() error { return repo.DeleteMount(ctx, f.OrgA, m.ID) }, loc.room, true)

	v := func(base int64, n int) string { return fmt.Sprint(base + int64(n)) }
	xs, ys := readPlacementState(t, f, x), readPlacementState(t, f, y)
	wantX := []string{
		"movement:" + v(x0.version, 1) + ":", "location_change:" + v(x0.version, 1) + ":->" + loc.rack1,
		"movement:" + v(x0.version, 1) + ":",
		"movement:" + v(x0.version, 2) + ":", "location_change:" + v(x0.version, 2) + ":" + loc.rack1 + "->" + loc.rack2,
		"movement:" + v(x0.version, 3) + ":", "location_change:" + v(x0.version, 3) + ":" + loc.rack2 + "->" + loc.room,
	}
	if !equalStrings(xs.changes, wantX) {
		t.Errorf("ci_change of X:\n got %v\nwant %v", xs.changes, wantX)
	}
	wantY := []string{
		"location_change:" + v(y0.version, 1) + ":->" + loc.rack1,
		"location_change:" + v(y0.version, 2) + ":" + loc.rack1 + "->" + loc.rack2,
		"location_change:" + v(y0.version, 3) + ":" + loc.rack2 + "->" + loc.room,
	}
	if !equalStrings(ys.changes, wantY) {
		t.Errorf("ci_change of Y:\n got %v\nwant %v", ys.changes, wantY)
	}
	wantXAudit := []string{"ci.rack_mounted:" + loc.rack1, "ci.rack_moved:" + loc.rack1, "ci.rack_moved:" + loc.rack2,
		"ci.rack_unmounted:" + loc.room}
	if !equalStrings(xs.audits, wantXAudit) {
		t.Errorf("audit of X:\n got %v\nwant %v", xs.audits, wantXAudit)
	}
	wantYAudit := []string{"ci.location_changed:" + loc.rack1, "ci.location_changed:" + loc.rack2, "ci.location_changed:" + loc.room}
	if !equalStrings(ys.audits, wantYAudit) {
		t.Fatalf("audit of Y:\n got %v\nwant %v", ys.audits, wantYAudit)
	}
	// The entry of Y names the placement that moved it.
	var placed, cause, asset string
	if err := f.Admin.QueryRow(context.Background(), `SELECT changes->>'placed_ci_id', changes->>'cause', changes->>'asset_id'
		FROM audit_log WHERE resource_id = $1 ORDER BY seq LIMIT 1`, y).Scan(&placed, &cause, &asset); err != nil {
		t.Fatal(err)
	}
	if placed != x || cause != "ci.rack_mounted" || asset != assetID {
		t.Errorf("audit of Y: placed_ci_id %q cause %q asset_id %q, want %q ci.rack_mounted %q", placed, cause, asset, x, assetID)
	}
}

// TestRackPlacementIsAtomic covers WP-578 (RCK-03) against PostgreSQL: the
// audit entry of the sibling Y is the last write of a placement; a test
// constraint makes it fail after the location was set and the history of X
// was written. The mount fails and leaves no mount, no location of X, Y or
// the asset, no version change and no history; the unmount of an existing
// mount fails the same way and keeps the mount and the rack as location.
// Without the fault both succeed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRackPlacementIsAtomic(t *testing.T) {
	f := scopetest.Seed(t, "69")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	loc := newPlacementTree(t, f, f.Client1, "atom")
	x := f.CI(t, f.OrgA, f.Client1, "atom-x")
	y := f.CI(t, f.OrgA, f.Client1, "atom-y")
	assetID := sharedAsset(t, f, "ATOM-A", x, y)

	dropFault := func() {
		if _, err := f.Admin.Exec(bg, `ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS wp578_audit_fails`); err != nil {
			t.Fatal(err)
		}
	}
	addFault := func() {
		dropFault()
		if _, err := f.Admin.Exec(bg, `ALTER TABLE audit_log ADD CONSTRAINT wp578_audit_fails
			CHECK (resource_id IS DISTINCT FROM '`+y+`'::uuid) NOT VALID`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(dropFault)
	mounted := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM rack_mount WHERE ci_id = $1`, x).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	unchanged := func(name string, bx, by placementState, wantAsset string, wantMounts int) {
		t.Helper()
		ax, ay := readPlacementState(t, f, x), readPlacementState(t, f, y)
		for _, c := range []struct {
			id            string
			before, after placementState
		}{{x, bx, ax}, {y, by, ay}} {
			if c.after.location != c.before.location || c.after.version != c.before.version ||
				!equalStrings(c.after.changes, c.before.changes) || !equalStrings(c.after.audits, c.before.audits) {
				t.Errorf("%s: CI %s changed: before %+v after %+v", name, c.id, c.before, c.after)
			}
		}
		if got := assetLocation(t, f, assetID); got != wantAsset {
			t.Errorf("%s: asset location %q, want %q", name, got, wantAsset)
		}
		if n := mounted(); n != wantMounts {
			t.Errorf("%s: %d mounts of X, want %d", name, n, wantMounts)
		}
	}

	repo := rack.NewPGRepository(f.App)
	bx, by := readPlacementState(t, f, x), readPlacementState(t, f, y)
	addFault()
	m := &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack1, CIID: x, PositionU: 1, HeightU: 1, Face: "front"}
	if err := repo.CreateMount(ctx, m); err == nil {
		t.Fatal("mount with a failing audit entry of Y succeeded")
	}
	unchanged("failed mount", bx, by, "", 0)

	dropFault()
	m = &rack.RackMount{OrganizationID: f.OrgA, RackID: loc.rack1, CIID: x, PositionU: 1, HeightU: 1, Face: "front"}
	if err := repo.CreateMount(ctx, m); err != nil {
		t.Fatalf("mount without fault: %v", err)
	}
	if s := readPlacementState(t, f, y); s.location != loc.rack1 || s.version != by.version+1 || len(s.changes) != 1 || len(s.audits) != 1 {
		t.Fatalf("Y after the mount without fault: %+v", s)
	}

	bx, by = readPlacementState(t, f, x), readPlacementState(t, f, y)
	addFault()
	if err := repo.DeleteMount(ctx, f.OrgA, m.ID); err == nil {
		t.Fatal("unmount with a failing audit entry of Y succeeded")
	}
	unchanged("failed unmount", bx, by, loc.rack1, 1)
	dropFault()
	if err := repo.DeleteMount(ctx, f.OrgA, m.ID); err != nil {
		t.Fatalf("unmount without fault: %v", err)
	}
	if got := assetLocation(t, f, assetID); got != loc.room {
		t.Errorf("asset location after the unmount: %q, want the room %q", got, loc.room)
	}
}

// TestRackPlacementInAColocationRackKeepsTheLocation covers WP-578 (RCK-03,
// DB-05) against PostgreSQL: a rack of another client (colocation) cannot
// become the location of a CI (ci_location_client). The placement is
// stored; the location of the CI, of its asset and of the asset's other CIs
// stays, no version is raised, no location_change is written and the audit
// entry of the placed CI names the reason. The same holds when only another
// CI of the asset belongs to another client than the rack.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRackPlacementInAColocationRackKeepsTheLocation(t *testing.T) {
	f := scopetest.Seed(t, "69")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	own := newPlacementTree(t, f, f.Client1, "colo-own")
	foreign := newPlacementTree(t, f, f.Client2, "colo-foreign")
	repo := rack.NewPGRepository(f.App)
	reason := func(ciID string) string {
		t.Helper()
		var r string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(changes->>'location_unchanged', '') FROM audit_log
			WHERE resource_id = $1 ORDER BY seq DESC LIMIT 1`, ciID).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// keeps checks that the CI kept location and version and got no
	// location_change; lastAction is its one new audit entry with the
	// reason, or empty for none.
	keeps := func(name, ciID string, before placementState, lastAction string) {
		t.Helper()
		s := readPlacementState(t, f, ciID)
		if s.location != before.location || s.version != before.version {
			t.Errorf("%s: CI %s location %q version %d, want %q version %d", name, ciID, s.location, s.version,
				before.location, before.version)
		}
		for _, c := range s.changes[len(before.changes):] {
			if strings.HasPrefix(c, "location_change:") {
				t.Errorf("%s: CI %s has a new location_change %s", name, ciID, c)
			}
		}
		if lastAction == "" {
			if len(s.audits) != len(before.audits) {
				t.Errorf("%s: CI %s has new audit entries %v", name, ciID, s.audits[len(before.audits):])
			}
			return
		}
		if len(s.audits) != len(before.audits)+1 || !strings.HasPrefix(s.audits[len(s.audits)-1], lastAction+":") {
			t.Errorf("%s: CI %s audit %v, want one new %s entry", name, ciID, s.audits, lastAction)
		}
		if got := reason(ciID); got != "the rack belongs to another client" {
			t.Errorf("%s: audit reason %q", name, got)
		}
	}

	// A CI without an asset mounted in a rack of client 2.
	z := f.CI(t, f.OrgA, f.Client1, "colo-z")
	zBefore := readPlacementState(t, f, z)
	if err := repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: foreign.rack1, CIID: z,
		PositionU: 1, HeightU: 1, Face: "front"}); err != nil {
		t.Fatal(err)
	}
	keeps("standalone mount", z, zBefore, "ci.rack_mounted")

	// X and Y of one asset: mounted in their own rack, then moved to a rack
	// of client 2.
	x := f.CI(t, f.OrgA, f.Client1, "colo-x")
	y := f.CI(t, f.OrgA, f.Client1, "colo-y")
	assetID := sharedAsset(t, f, "COLO-A", x, y)
	m := &rack.RackMount{OrganizationID: f.OrgA, RackID: own.rack1, CIID: x, PositionU: 1, HeightU: 1, Face: "front"}
	if err := repo.CreateMount(ctx, m); err != nil {
		t.Fatal(err)
	}
	bx, by := readPlacementState(t, f, x), readPlacementState(t, f, y)
	if bx.location != own.rack1 || by.location != own.rack1 {
		t.Fatalf("own mount: X %q Y %q, want %q", bx.location, by.location, own.rack1)
	}
	if _, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &foreign.rack2}); err != nil {
		t.Fatalf("move to the colocation rack: %v", err)
	}
	keeps("colocation move of X", x, bx, "ci.rack_moved")
	keeps("colocation move, sibling Y", y, by, "")
	if got := assetLocation(t, f, assetID); got != own.rack1 {
		t.Errorf("asset location after the colocation move: %q, want %q", got, own.rack1)
	}
	var mountRack string
	if err := f.Admin.QueryRow(bg, `SELECT rack_id::text FROM rack_mount WHERE id = $1`, m.ID).Scan(&mountRack); err != nil || mountRack != foreign.rack2 {
		t.Errorf("placement after the colocation move: rack %q err %v, want %q", mountRack, err, foreign.rack2)
	}

	// P (client 1) and Q (client 2) of one asset mounted in a rack of client
	// 1: the rack is foreign to Q, the location of both stays.
	p := f.CI(t, f.OrgA, f.Client1, "colo-p")
	q := f.CI(t, f.OrgA, f.Client2, "colo-q")
	mixed := sharedAsset(t, f, "COLO-M", p, q)
	bp, bq := readPlacementState(t, f, p), readPlacementState(t, f, q)
	if err := repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: own.rack2, CIID: p,
		PositionU: 5, HeightU: 1, Face: "front"}); err != nil {
		t.Fatalf("mount of a CI whose asset has a CI of another client: %v", err)
	}
	keeps("mixed-client asset, placed P", p, bp, "ci.rack_mounted")
	keeps("mixed-client asset, sibling Q", q, bq, "")
	if got := assetLocation(t, f, mixed); got != "" {
		t.Errorf("asset location of the mixed-client asset: %q, want none", got)
	}
}
