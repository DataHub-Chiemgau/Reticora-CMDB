package ci_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// integrityCounts runs tests/integrity-check.sql in tx for one organization
// and returns every check with its count, in the order of the script.
func integrityCounts(t *testing.T, tx pgx.Tx, orgID string) (names []string, counts map[string]int) {
	t.Helper()
	script, err := os.ReadFile("../../../tests/integrity-check.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = tx.Exec(ctx, `SELECT set_config('integrity.org', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, string(script))
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer rows.Close()
	counts = map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		counts[name] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names, counts
}

// stmt is one statement of an injection.
type stmt struct {
	sql  string
	args []any
}

func sq(sql string, args ...any) stmt { return stmt{sql, args} }

// TestIntegrityCheckCountsEveryViolation is the self-test of
// tests/integrity-check.sql (ABN-03, F-281, WP-437): on a clean fixture of
// two organizations every check is 0; then each check gets one injected
// violation of its class, written as the owner in a transaction of its own
// that is rolled back, and must count it. Classes the schema guards with a
// foreign key are written with session_replication_role = replica, as a
// restore or a replication apply writes them (the foreign keys do not
// fire); the CI bound to another organization's room is written with a
// plain owner UPDATE, as in the reproduction of F-281. The exact duplicates
// of serial number, hardware UUID and primary MAC have no check: their
// unique indexes refuse them even then. A check without an injection fails
// the test, so a new check needs its case. The weak-identity checks spare
// what REC-02 allows: a strong identity, another VRF, another sysObjectID
// and a hostname duplicate under a probable_duplicate review.
//
// The checks of phase 2 (WP-328) get their cases as well: reserved of a
// stock row other than its active quantity reservations, an asset reserved
// for one user and held by another, a holder cache without the movements
// that give it, a cycle of the type inheritance and the normalized serial
// number of two assets. Negative stock and reserved above on_hand are
// refused by the CHECKs of stock_level; their cases drop the CHECKs in the
// rolled-back transaction, so the rows still prove they count. A disposed
// asset and a holder change booked as return and assignment at one
// occurred_at are spared.
//
// The cross-tenant references of WP-827 too: an asset of a CI type of
// organization B, a document link to the document or to the target of
// another organization and to a target no table holds, a reference
// attribute value naming a CI of organization B and one naming no row, and
// a single-column foreign key without the organization (the uploader of a
// document) pointing at a user of organization B, and a stock movement with a
// document of organization B.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIntegrityCheckCountsEveryViolation(t *testing.T) {
	f := scopetest.Seed(t, "ec")
	bg := context.Background()
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}

	// Organization A: two CIs of client 1 with a relationship, a CI without
	// client, a rack in a room of client 1 with a mount, an interface with
	// an address, two own CI types. Organization B: a client with a site
	// chain, a CI with an interface and an own CI type.
	a := f.CI(t, f.OrgA, f.Client1, "ic-a")
	b := f.CI(t, f.OrgA, f.Client1, "ic-b")
	loose := f.CI(t, f.OrgA, "", "ic-loose")
	scalar(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		VALUES ($1, $2, $3, 'connected_to', 'manual') RETURNING id::text`, f.OrgA, a, b)
	roomA := f.Room(t, f.Client1, "ic-room-a")
	rackA := scalar(`INSERT INTO rack (organization_id, client_id, room_id, name, height_units)
		VALUES ($1, $2, $3, 'ic-rack-a', 42) RETURNING id::text`, f.OrgA, f.Client1, roomA)
	mounted := f.CI(t, f.OrgA, f.Client1, "ic-mounted")
	scalar(`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face)
		VALUES ($1, $2, $3, 1, 1, 'front') RETURNING id::text`, f.OrgA, rackA, mounted)
	ifA := scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`, f.OrgA, a)
	ipA := scalar(`INSERT INTO ip_address (organization_id, interface_id, address, vrf_id)
		SELECT $1, $2, '10.236.0.10', v.id FROM vrf v WHERE v.organization_id = $1 AND v.is_default RETURNING id::text`, f.OrgA, ifA)
	typeA1 := scalar(`INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'ic-type-1', 'ic-type-1') RETURNING id::text`, f.OrgA)
	typeA2 := scalar(`INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'ic-type-2', 'ic-type-2') RETURNING id::text`, f.OrgA)
	buildingA := scalar(`SELECT building_id::text FROM room WHERE id = $1`, roomA)

	clientB := scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'Client B', 'scopetest-ec-b-c1') RETURNING id::text`, f.OrgB)
	var siteB, roomB string
	if err := f.Admin.QueryRow(bg, `
		WITH s AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'ic-site-b') RETURNING id),
		     bd AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, 'ic-building-b' FROM s RETURNING id, site_id)
		INSERT INTO room (organization_id, building_id, name) SELECT $1, id, 'ic-room-b' FROM bd
		RETURNING site_id::text, id::text`, f.OrgB, clientB).Scan(&siteB, &roomB); err != nil {
		t.Fatalf("site chain of organization B: %v", err)
	}
	foreign := f.CI(t, f.OrgB, clientB, "ic-foreign")
	ifB := scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`, f.OrgB, foreign)
	typeB := scalar(`INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'ic-type-b', 'ic-type-b') RETURNING id::text`, f.OrgB)

	// Phase 2 of organization A (WP-328): a bin with a stock row of 5 of a
	// consumable, 2 of them reserved by an active quantity reservation; an
	// asset reserved for user 1 and a free asset.
	bin := icBin(t, f)
	cons := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "ic-toner"}
	if err := consumable.NewPGRepository(f.App).Create(f.OrgCtx(f.OrgA), cons); err != nil {
		t.Fatalf("consumable: %v", err)
	}
	scalar(`INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand, reserved)
		VALUES ($1, $2, $3, 5, 2) RETURNING id::text`, f.OrgA, cons.ID, bin)
	scalar(`INSERT INTO reservation (organization_id, item_kind, consumable_id, location_id, quantity, state, project_ref)
		VALUES ($1, 'consumable', $2, $3, 2, 'active', 'ic') RETURNING id::text`, f.OrgA, cons.ID, bin)
	user1 := f.AppUser(t, f.OrgA, "ic-user-1")
	user2 := f.AppUser(t, f.OrgA, "ic-user-2")
	reserved := f.Asset(t, f.OrgA, f.Client1, "ic-asset-reserved")
	scalar(`INSERT INTO reservation (organization_id, item_kind, asset_id, assignee_id, state)
		VALUES ($1, 'asset', $2, $3, 'active') RETURNING id::text`, f.OrgA, reserved, user1)
	free := f.Asset(t, f.OrgA, f.Client1, "ic-asset-free")

	// Cross-tenant references of phase 2 (WP-827): a document of
	// organization A linked to an asset, a CI of an own type with a ci_ref
	// attribute referencing a CI of A; an asset and a document of
	// organization B.
	assetB := f.Asset(t, f.OrgB, clientB, "ic-asset-b")
	userB := f.AppUser(t, f.OrgB, "ic-user-b")
	docA := scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'ic-doc-a', 'ic-a.txt', '', $2) RETURNING id::text`, f.OrgA, user1)
	docB := scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'ic-doc-b', 'ic-b.txt', '', $2) RETURNING id::text`, f.OrgB, userB)
	linkA := scalar(`INSERT INTO document_link (organization_id, document_id, entity_type, entity_id)
		VALUES ($1, $2, 'asset', $3) RETURNING id::text`, f.OrgA, docA, reserved)
	scalar(`INSERT INTO attribute_definition (organization_id, ci_type_id, key, data_type, display_name)
		VALUES ($1, $2, 'ic_ref', 'ci_ref', '{"de":"ic_ref"}') RETURNING id::text`, f.OrgA, typeA1)
	refCI := scalar(`INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
		VALUES ($1, $2, $3, 'ic-ref', 'active', jsonb_build_object('ic_ref', $4::text)) RETURNING id::text`, f.OrgA, f.Client1, typeA1, b)

	// inTx runs fn in a transaction of the owner that is rolled back.
	inTx := func(t *testing.T, replica bool, fn func(tx pgx.Tx)) {
		t.Helper()
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if replica {
			if _, err := tx.Exec(bg, `SET LOCAL session_replication_role = replica`); err != nil {
				t.Fatal(err)
			}
		}
		fn(tx)
	}
	apply := func(t *testing.T, tx pgx.Tx, stmts []stmt) {
		t.Helper()
		for _, s := range stmts {
			if _, err := tx.Exec(bg, s.sql, s.args...); err != nil {
				t.Fatalf("inject %q: %v", s.sql, err)
			}
		}
	}

	// The clean fixture: every check 0 in both organizations.
	var checks []string
	inTx(t, false, func(tx pgx.Tx) {
		for _, org := range []string{f.OrgA, f.OrgB} {
			names, counts := integrityCounts(t, tx, org)
			if org == f.OrgA {
				checks = names
			}
			for name, n := range counts {
				if n != 0 {
					t.Errorf("clean fixture, org %s: %s = %d", org, name, n)
				}
			}
		}
	})
	if len(checks) == 0 {
		t.Fatal("integrity check returned no checks")
	}

	weakPair := func(column, va, vb string) []stmt {
		return []stmt{sq(`UPDATE ci SET `+column+` = $2 WHERE id = $1`, a, va), sq(`UPDATE ci SET `+column+` = $2 WHERE id = $1`, b, vb)}
	}
	ipPair := append(weakPair("management_ip", "10.236.1.5", "10.236.1.5"), weakPair("sys_object_id", "1.3.6.1.4.1.9.1.1", " 1.3.6.1.4.1.9.1.1")...)
	cases := []struct {
		check   string
		replica bool
		inject  []stmt
	}{
		{"orphan: relationship to a deleted or merged CI", true, []stmt{sq(`UPDATE ci SET deleted_at = now() WHERE id = $1`, b)}},
		{"orphan: merged CI still active", true, []stmt{sq(`UPDATE ci SET merged_into_id = $2 WHERE id = $1`, b, a)}},
		{"orphan: rack mount of a deleted CI", true, []stmt{sq(`UPDATE ci SET deleted_at = now() WHERE id = $1`, mounted)}},
		// Case and blanks differ: the unique index lets both through.
		{"duplicate: serial number of active CIs", false, weakPair("serial_number", "IC-SN-1", " ic-sn-1")},
		{"duplicate: management IP and sysObjectID of weak identities in one VRF", false, ipPair},
		// No unique index on the serial number of assets (AST-03).
		{"duplicate: normalized serial number of assets not disposed", false, []stmt{
			sq(`UPDATE asset SET serial_number = 'IC-AS-1' WHERE id = $1`, reserved), sq(`UPDATE asset SET serial_number = ' ic-as-1' WHERE id = $1`, free)}},
		{"duplicate: hostname or FQDN of weak identities without review", false, weakPair("hostname", "ic-host", "IC-Host")},
		{"cross-tenant: relationship endpoint", true, []stmt{sq(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id,
			rel_type, source) VALUES ($1, $2, $3, 'connected_to', 'manual')`, f.OrgA, a, foreign)}},
		{"cross-tenant: rack mount", true, []stmt{sq(`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face)
			VALUES ($1, $2, $3, 10, 1, 'front')`, f.OrgA, rackA, foreign)}},
		{"cross-tenant: network interface", true, []stmt{sq(`INSERT INTO network_interface (organization_id, ci_id, name)
			VALUES ($1, $2, 'eth9')`, f.OrgA, foreign)}},
		{"cross-tenant: ip address", true, []stmt{sq(`UPDATE ip_address SET interface_id = $2 WHERE id = $1`, ipA, ifB)}},
		{"cross-tenant: field value", true, []stmt{sq(`INSERT INTO ci_field_value (organization_id, ci_id, field_name)
			VALUES ($1, $2, 'model')`, f.OrgA, foreign)}},
		{"cross-tenant: CI client", true, []stmt{sq(`UPDATE ci SET client_id = $2 WHERE id = $1`, a, clientB)}},
		// F-281: a CI without client in a room of another organization, with
		// the site and room the trigger derived from it. Since migration 000194
		// (WP-838) ci_location_id_fkey carries the organization and refuses the
		// plain owner UPDATE (checked below); such a row can only predate it.
		{"cross-tenant: CI location", true, []stmt{sq(`UPDATE ci SET location_id = $2, room_id = $2, site_id = $3 WHERE id = $1`,
			loose, roomB, siteB)}},
		{"cross-tenant: CI site", true, []stmt{sq(`UPDATE ci SET site_id = $2 WHERE id = $1`, a, siteB)}},
		{"cross-tenant: CI room", true, []stmt{sq(`UPDATE ci SET room_id = $2 WHERE id = $1`, a, roomB)}},
		{"cross-tenant: CI type", true, []stmt{sq(`UPDATE ci SET ci_type_id = $2 WHERE id = $1`, a, typeB)}},
		{"cross-tenant: rack room", true, []stmt{sq(`UPDATE rack SET room_id = $2 WHERE id = $1`, rackA, roomB)}},
		// WP-827: references without a composite foreign key.
		{"cross-tenant: asset type", true, []stmt{sq(`UPDATE asset SET asset_type_id = $2 WHERE id = $1`, free, typeB)}},
		{"cross-tenant: document link document", true, []stmt{sq(`UPDATE document_link SET document_id = $2 WHERE id = $1`, linkA, docB)}},
		{"cross-tenant: document link target", true, []stmt{sq(`UPDATE document_link SET entity_id = $2 WHERE id = $1`, linkA, assetB)}},
		{"orphan: document link without target", true, []stmt{sq(`UPDATE document_link SET entity_id = gen_random_uuid() WHERE id = $1`, linkA)}},
		{"cross-tenant: CI reference attribute", true, []stmt{sq(`UPDATE ci SET attributes = jsonb_build_object('ic_ref', $2::text)
			WHERE id = $1`, refCI, foreign)}},
		{"orphan: CI reference attribute without target", true, []stmt{sq(`UPDATE ci SET attributes = jsonb_build_object('ic_ref',
			gen_random_uuid()::text) WHERE id = $1`, refCI)}},
		// One row for every single-column foreign key between tenant tables
		// without the organization. Since migration 000288 (WP-877, SR-03)
		// there is none (rls.ReferencesWithoutOrganization is empty); the
		// case adds one in its rolled-back transaction and writes past the
		// composite key as a replica would.
		{"cross-tenant: reference without the organization", true, []stmt{
			sq(`ALTER TABLE document ADD CONSTRAINT document_uploaded_by_loose_fkey FOREIGN KEY (uploaded_by) REFERENCES app_user (id)`),
			sq(`UPDATE document SET uploaded_by = $2 WHERE id = $1`, docA, userB)}},
		{"cross-tenant: stock movement context", true, []stmt{sq(`INSERT INTO stock_movement (organization_id, item_kind, asset_id,
			movement_type, assignee_user_id, document_id) VALUES ($1, 'asset', $2, 'assignment', $3, $4)`, f.OrgA, free, user1, docB)}},
		{"cycle: CI type inheritance", true, []stmt{sq(`UPDATE ci_type SET parent_type_id = $2 WHERE id = $1`, typeA1, typeA2),
			sq(`UPDATE ci_type SET parent_type_id = $2 WHERE id = $1`, typeA2, typeA1)}},
		{"cycle: CI type lineage", true, []stmt{sq(`UPDATE ci_type SET cloned_from_id = $2 WHERE id = $1`, typeA1, typeA2),
			sq(`UPDATE ci_type SET cloned_from_id = $2 WHERE id = $1`, typeA2, typeA1)}},
		{"cycle: location tree", true, []stmt{sq(`UPDATE location SET parent_id = $2 WHERE id = $1`, buildingA, roomA)}},
		{"cycle: composition", true, []stmt{sq(`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, a, b),
			sq(`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, b, a)}},
		// The CHECKs of stock_level refuse negative stock; the case drops
		// them in its rolled-back transaction.
		{"stock: negative on_hand", false, []stmt{sq(`ALTER TABLE stock_level DROP CONSTRAINT stock_level_on_hand_check,
			DROP CONSTRAINT stock_level_reserved_check`), sq(`UPDATE stock_level SET on_hand = -1, reserved = 0 WHERE consumable_id = $1`, cons.ID)}},
		{"stock: reserved below zero or above on_hand", false, []stmt{sq(`ALTER TABLE stock_level DROP CONSTRAINT stock_level_reserved_check`),
			sq(`UPDATE stock_level SET reserved = 6 WHERE consumable_id = $1`, cons.ID)}},
		{"stock: reserved other than the active quantity reservations", false, []stmt{
			sq(`UPDATE stock_level SET reserved = 1 WHERE consumable_id = $1`, cons.ID)}},
		{"reservation: active reservation of an asset held by another holder", false, []stmt{
			sq(`UPDATE asset SET assigned_user_id = $2 WHERE id = $1`, reserved, user2)}},
		{"assignment: holder cache other than the assignment and return movements", false, []stmt{
			sq(`UPDATE asset SET assigned_user_id = $2 WHERE id = $1`, free, user2)}},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.check] = true
		t.Run(c.check, func(t *testing.T) {
			inTx(t, c.replica, func(tx pgx.Tx) {
				apply(t, tx, c.inject)
				if _, counts := integrityCounts(t, tx, f.OrgA); counts[c.check] == 0 {
					t.Errorf("%s: the injected violation is not counted (%v)", c.check, counts)
				}
			})
		})
	}
	for _, name := range checks {
		if !covered[name] {
			t.Errorf("check %q has no injected violation in this test", name)
		}
	}
	for name := range covered {
		if !slices.Contains(checks, name) {
			t.Errorf("case %q names no check of the script", name)
		}
	}

	// The plain owner UPDATE of F-281 fails since migration 000194 (WP-838);
	// the site and room of the F-281 case are counted as well.
	inTx(t, false, func(tx pgx.Tx) {
		_, err := tx.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, loose, roomB)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "ci_location_id_fkey" {
			t.Errorf("CI in the room of organization B: %v, want 23503 on ci_location_id_fkey", err)
		}
	})
	inTx(t, true, func(tx pgx.Tx) {
		apply(t, tx, []stmt{sq(`UPDATE ci SET location_id = $2, room_id = $2, site_id = $3 WHERE id = $1`, loose, roomB, siteB)})
		_, counts := integrityCounts(t, tx, f.OrgA)
		if counts["cross-tenant: CI site"] == 0 || counts["cross-tenant: CI room"] == 0 {
			t.Errorf("CI in the room of organization B: site %d, room %d", counts["cross-tenant: CI site"], counts["cross-tenant: CI room"])
		}
	})

	// The weak-identity checks spare what REC-02 allows.
	for _, c := range []struct {
		name   string
		inject []stmt
		check  string
	}{
		{"strong identity", append(slices.Clone(ipPair), sq(`UPDATE ci SET serial_number = 'IC-SN-2' WHERE id = $1`, b)),
			"duplicate: management IP and sysObjectID of weak identities in one VRF"},
		{"another VRF", append(slices.Clone(ipPair), sq(`UPDATE ci SET attributes = jsonb_build_object('raw_data', jsonb_build_object('vrf_id', $2::text))
			WHERE id = $1`, b, f.OrgB)), "duplicate: management IP and sysObjectID of weak identities in one VRF"},
		{"another sysObjectID", append(weakPair("management_ip", "10.236.1.5", "10.236.1.5"),
			weakPair("sys_object_id", "1.3.6.1.4.1.9.1.1", "1.3.6.1.4.1.9.1.2")...), "duplicate: management IP and sysObjectID of weak identities in one VRF"},
		{"interface MAC", append(weakPair("hostname", "ic-host", "ic-host"), sq(`UPDATE ci SET attributes =
			'{"fingerprint":{"mac_addresses":["3c:ec:ef:00:00:01"]}}' WHERE id = $1`, b)), "duplicate: hostname or FQDN of weak identities without review"},
		{"probable_duplicate review", append(weakPair("hostname", "ic-host", "ic-host"), sq(`INSERT INTO review_item (organization_id, kind, payload,
			candidate_ci_ids) VALUES ($1, 'probable_duplicate', jsonb_build_object('ci_id', $2::text), ARRAY[$3::uuid])`, f.OrgA, b, a)),
			"duplicate: hostname or FQDN of weak identities without review"},
		{"disposed asset", []stmt{sq(`UPDATE asset SET serial_number = 'IC-AS-2' WHERE id = $1`, reserved),
			sq(`UPDATE asset SET serial_number = 'ic-as-2', status = 'disposed', lifecycle_state = 'disposed' WHERE id = $1`, free)},
			"duplicate: normalized serial number of assets not disposed"},
		// A holder change (WP-275) books the return of the old and the
		// assignment of the new holder at the same occurred_at.
		{"holder change at one occurred_at", []stmt{
			sq(`INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, assignee_user_id, occurred_at)
				VALUES ($1, 'asset', $2, 'assignment', $3, now() - interval '1 day')`, f.OrgA, free, user1),
			sq(`INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, assignee_user_id, occurred_at)
				VALUES ($1, 'asset', $2, 'return', $3, now())`, f.OrgA, free, user1),
			sq(`INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, assignee_user_id, occurred_at)
				VALUES ($1, 'asset', $2, 'assignment', $3, now())`, f.OrgA, free, user2),
			sq(`UPDATE asset SET assigned_user_id = $2 WHERE id = $1`, free, user2)},
			"assignment: holder cache other than the assignment and return movements"},
	} {
		t.Run("allowed: "+c.name, func(t *testing.T) {
			inTx(t, false, func(tx pgx.Tx) {
				apply(t, tx, c.inject)
				if _, counts := integrityCounts(t, tx, f.OrgA); counts[c.check] != 0 {
					t.Errorf("%s: %s = %d, want 0", c.name, c.check, counts[c.check])
				}
			})
		})
	}

	// The exact duplicates are the unique indexes' job, also for a writer
	// without foreign keys and triggers.
	for _, c := range []struct{ column, value, index string }{
		{"serial_number", "IC-SN-3", "uq_ci_org_serial"},
		{"hardware_uuid", "4c4c4544-0000-1000-8000-00000000ec01", "uq_ci_org_hardware_uuid"},
		{"primary_mac", "3c:ec:ef:00:00:02", "uq_ci_org_primary_mac"},
	} {
		inTx(t, true, func(tx pgx.Tx) {
			apply(t, tx, []stmt{sq(`UPDATE ci SET `+c.column+` = $2 WHERE id = $1`, a, c.value)})
			_, err := tx.Exec(bg, `UPDATE ci SET `+c.column+` = $2 WHERE id = $1`, b, c.value)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != c.index {
				t.Errorf("duplicate %s: %v, want 23505 on %s", c.column, err, c.index)
			}
		})
	}
}

// icBin creates a storage branch site > warehouse > zone > shelf > bin of
// client 1 in organization A through the location repository and returns
// the bin.
func icBin(t *testing.T, f *scopetest.Fixture) string {
	t.Helper()
	repo := locations.NewPGRepository(f.App)
	parent := ""
	for _, kind := range []locations.Kind{locations.KindSite, locations.KindWarehouse, locations.KindZone, locations.KindShelf, locations.KindBin} {
		req := locations.CreateRequest{Kind: kind, ParentID: parent, Name: "ic-" + string(kind)}
		if kind == locations.KindSite {
			req.ClientID = f.Client1
		}
		l, err := repo.Create(f.OrgCtx(f.OrgA), f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		parent = l.ID
	}
	return parent
}
