package database_test

// WP-533 (TEN-02, evidence gap of WP-025): the backfill of migration 000061
// (section 4, "Existing rows are backfilled") was only shown by replaying its
// statements. This test runs the migration itself: in a database of its own
// it migrates to 000060, seeds rows of all twelve CI child tables that
// reference CIs, interfaces, subnets and collectors of two clients and two
// sites (plus org-wide parents), applies 000061 and compares client_id/site_id
// (source_/target_ for the edge tables) of every row with its parent rows and
// with the explicitly expected values. The shared database of
// TEST_DATABASE_URL is not changed; the scratch database is dropped.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//     go test -count=1 -v -run 'TestMigration000061Backfill' ./internal/database/

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// backfillIDs names the seeded rows; the values are fixed uuids so the
// expectations below can refer to them.
var backfillIDs = func() map[string]string {
	names := []string{
		"org", "c1", "c2", "s1", "s2",
		"ciA", "ciB", "ciC", "ciO",
		"subA", "subB", "ifA", "ifB",
		"ipIfSub", "ipSubB", "ipIfB", "ipNone", "ipSubA",
		"contact", "ccA", "ccB",
		"chA", "chB", "chO",
		"fvA", "fvB",
		"fdA", "fdC",
		"bld", "room", "rack", "rmA", "rmB",
		"rule", "crA", "crB",
		"sfA", "sfB", "sfNone",
		"colA", "colB", "colO", "jobA", "jobB", "jobO",
		"drMatchedB", "drColA", "drColB", "drColO", "drMatchedA",
		"relAB", "relBO", "relCA",
		"rt", "supAB", "supOC",
	}
	ids := make(map[string]string, len(names))
	for i, n := range names {
		ids[n] = fmt.Sprintf("00000000-0000-4000-8000-%012x", 0x533000+i)
	}
	return ids
}()

// backfillSeed is valid SQL at version 000060; {name} stands for backfillIDs.
// client 1 has site 1, client 2 has site 2. ciA: client 1/site 1, ciB:
// client 2/site 2, ciC: client 1 without site, ciO: org-wide. Subnet subA
// belongs to client 1/site 1, subB to client 2/site 2. Collector colA belongs
// to client 1, colB to client 2, colO is org-wide.
const backfillSeed = `
INSERT INTO organization (id, name, slug) VALUES ({org}, 'wp533 backfill', 'wp533-backfill');
INSERT INTO client (id, organization_id, name, slug) VALUES
    ({c1}, {org}, 'client 1', 'wp533-c1'), ({c2}, {org}, 'client 2', 'wp533-c2');
INSERT INTO site (id, organization_id, client_id, name) VALUES
    ({s1}, {org}, {c1}, 'site 1'), ({s2}, {org}, {c2}, 'site 2');
INSERT INTO ci (id, organization_id, client_id, site_id, ci_type_id, name)
SELECT v.id, {org}, v.client, v.site, t.id, v.name
  FROM (VALUES ({ciA}::uuid, {c1}::uuid, {s1}::uuid, 'ciA'), ({ciB}, {c2}, {s2}, 'ciB'),
               ({ciC}, {c1}, NULL, 'ciC'), ({ciO}, NULL, NULL, 'ciO')) AS v(id, client, site, name),
       ci_type t WHERE t.key = 'server' AND t.organization_id IS NULL;
INSERT INTO subnet (id, organization_id, client_id, site_id, cidr) VALUES
    ({subA}, {org}, {c1}, {s1}, '10.53.1.0/24'), ({subB}, {org}, {c2}, {s2}, '10.53.2.0/24');
INSERT INTO network_interface (id, organization_id, ci_id, name) VALUES
    ({ifA}, {org}, {ciA}, 'eth0'), ({ifB}, {org}, {ciB}, 'eth0');
INSERT INTO ip_address (id, organization_id, interface_id, subnet_id, address) VALUES
    ({ipIfSub}, {org}, {ifA}, {subB}, '10.53.2.1'),
    ({ipSubB}, {org}, NULL, {subB}, '10.53.2.2'),
    ({ipIfB}, {org}, {ifB}, NULL, '10.53.9.1'),
    ({ipNone}, {org}, NULL, NULL, '10.53.9.2'),
    ({ipSubA}, {org}, NULL, {subA}, '10.53.1.1');
INSERT INTO contact (id, organization_id, client_id, display_name) VALUES ({contact}, {org}, {c1}, 'contact');
INSERT INTO ci_contact (id, organization_id, ci_id, contact_id) VALUES
    ({ccA}, {org}, {ciA}, {contact}), ({ccB}, {org}, {ciB}, {contact});
INSERT INTO ci_change (id, organization_id, ci_id, change_type) VALUES
    ({chA}, {org}, {ciA}, 'update'), ({chB}, {org}, {ciB}, 'update'), ({chO}, {org}, {ciO}, 'update');
INSERT INTO ci_field_value (id, organization_id, ci_id, field_name) VALUES
    ({fvA}, {org}, {ciA}, 'f'), ({fvB}, {org}, {ciB}, 'f');
INSERT INTO ci_instance_field_definition (id, organization_id, ci_id, name, data_type) VALUES
    ({fdA}, {org}, {ciA}, 'd', 'string'), ({fdC}, {org}, {ciC}, 'd', 'string');
INSERT INTO building (id, organization_id, site_id, name) VALUES ({bld}, {org}, {s1}, 'building');
INSERT INTO room (id, organization_id, building_id, name) VALUES ({room}, {org}, {bld}, 'room');
INSERT INTO rack (id, organization_id, room_id, name) VALUES ({rack}, {org}, {room}, 'rack');
INSERT INTO rack_mount (id, organization_id, rack_id, ci_id, position_u) VALUES
    ({rmA}, {org}, {rack}, {ciA}, 1), ({rmB}, {org}, {rack}, {ciB}, 3);
INSERT INTO compliance_rule (id, organization_id, name, severity, category) VALUES ({rule}, {org}, 'rule', 'low', 'hygiene');
INSERT INTO compliance_result (id, organization_id, rule_id, ci_id, status) VALUES
    ({crA}, {org}, {rule}, {ciA}, 'pass'), ({crB}, {org}, {rule}, {ciB}, 'fail');
INSERT INTO security_finding (id, organization_id, ci_id, kind, title) VALUES
    ({sfA}, {org}, {ciA}, 'vulnerability', 'a'), ({sfB}, {org}, {ciB}, 'vulnerability', 'b'),
    ({sfNone}, {org}, NULL, 'vulnerability', 'none');
INSERT INTO collector (id, organization_id, client_id, name) VALUES
    ({colA}, {org}, {c1}, 'colA'), ({colB}, {org}, {c2}, 'colB'), ({colO}, {org}, NULL, 'colO');
INSERT INTO discovery_job (id, organization_id, collector_id, job_type) VALUES
    ({jobA}, {org}, {colA}, 'sweep'), ({jobB}, {org}, {colB}, 'sweep'), ({jobO}, {org}, {colO}, 'sweep');
INSERT INTO discovery_result (id, organization_id, job_id, raw_data, matched_ci_id) VALUES
    ({drMatchedB}, {org}, {jobA}, '{}', {ciB}),
    ({drColA}, {org}, {jobA}, '{}', NULL),
    ({drColB}, {org}, {jobB}, '{}', NULL),
    ({drColO}, {org}, {jobO}, '{}', NULL),
    ({drMatchedA}, {org}, {jobB}, '{}', {ciA});
INSERT INTO ci_relationship (id, organization_id, source_ci_id, target_ci_id, rel_type) VALUES
    ({relAB}, {org}, {ciA}, {ciB}, 'connected_to'), ({relBO}, {org}, {ciB}, {ciO}, 'connected_to'),
    ({relCA}, {org}, {ciC}, {ciA}, 'connected_to');
INSERT INTO relationship_type (id, organization_id, key, forward_label, reverse_label) VALUES
    ({rt}, {org}, 'wp533', 'f', 'r');
INSERT INTO relationship_suppression (id, organization_id, source_ci_id, target_ci_id, relationship_type_id) VALUES
    ({supAB}, {org}, {ciA}, {ciB}, {rt}), ({supOC}, {org}, {ciO}, {ciC}, {rt});
`

// backfillParentMismatches selects, per table, the rows whose derived
// columns differ from their parent rows: the CI (ci_id, matched_ci_id, both
// endpoints), for ip_address the interface or else the subnet, for unmatched
// discovery results the collector of the job (client only).
var backfillParentMismatches = map[string]string{
	"network_interface":            ciParentMismatch("network_interface", "ci_id"),
	"ci_contact":                   ciParentMismatch("ci_contact", "ci_id"),
	"ci_change":                    ciParentMismatch("ci_change", "ci_id"),
	"ci_field_value":               ciParentMismatch("ci_field_value", "ci_id"),
	"ci_instance_field_definition": ciParentMismatch("ci_instance_field_definition", "ci_id"),
	"rack_mount":                   ciParentMismatch("rack_mount", "ci_id"),
	"compliance_result":            ciParentMismatch("compliance_result", "ci_id"),
	"security_finding":             ciParentMismatch("security_finding", "ci_id"),
	"ip_address": `SELECT x.id::text FROM ip_address x
		LEFT JOIN network_interface n ON n.id = x.interface_id
		LEFT JOIN subnet s ON s.id = x.subnet_id
		WHERE (x.interface_id IS NOT NULL AND (x.client_id IS DISTINCT FROM n.client_id OR x.site_id IS DISTINCT FROM n.site_id))
		   OR (x.interface_id IS NULL AND (x.client_id IS DISTINCT FROM s.client_id OR x.site_id IS DISTINCT FROM s.site_id))`,
	"discovery_result": `SELECT x.id::text FROM discovery_result x
		LEFT JOIN ci c ON c.id = x.matched_ci_id
		JOIN discovery_job j ON j.id = x.job_id JOIN collector col ON col.id = j.collector_id
		WHERE (x.matched_ci_id IS NOT NULL AND (x.client_id IS DISTINCT FROM c.client_id OR x.site_id IS DISTINCT FROM c.site_id))
		   OR (x.matched_ci_id IS NULL AND (x.client_id IS DISTINCT FROM col.client_id OR x.site_id IS NOT NULL))`,
	"ci_relationship":          edgeParentMismatch("ci_relationship"),
	"relationship_suppression": edgeParentMismatch("relationship_suppression"),
}

func ciParentMismatch(table, column string) string {
	return `SELECT x.id::text FROM ` + table + ` x LEFT JOIN ci c ON c.id = x.` + column + `
		WHERE x.client_id IS DISTINCT FROM c.client_id OR x.site_id IS DISTINCT FROM c.site_id`
}

func edgeParentMismatch(table string) string {
	return `SELECT x.id::text FROM ` + table + ` x
		JOIN ci s ON s.id = x.source_ci_id JOIN ci t ON t.id = x.target_ci_id
		WHERE x.source_client_id IS DISTINCT FROM s.client_id OR x.source_site_id IS DISTINCT FROM s.site_id
		   OR x.target_client_id IS DISTINCT FROM t.client_id OR x.target_site_id IS DISTINCT FROM t.site_id`
}

// TestMigration000061Backfill shows TEN-02 for existing rows: after 000061
// every row of the twelve child tables carries the client and site of its
// parent, including both endpoints of relationships and suppressions, the
// interface-before-subnet rule of ip_address and the collector fallback of
// unmatched discovery results.
func TestMigration000061Backfill(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	sharedVersion := func() string {
		t.Helper()
		shared, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect shared database: %v", err)
		}
		defer func() { _ = shared.Close(context.Background()) }()
		var v string
		if err := shared.QueryRow(ctx, `SELECT version::text || '/' || dirty::text FROM schema_migrations`).Scan(&v); err != nil {
			t.Fatalf("shared database version: %v", err)
		}
		return v
	}
	sharedBefore := sharedVersion()
	// Registered before scratchDatabase, so it runs after its drop.
	t.Cleanup(func() {
		admin, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			t.Errorf("connect: %v", err)
			return
		}
		defer func() { _ = admin.Close(context.Background()) }()
		var left []string
		rows, err := admin.Query(context.Background(), `SELECT datname FROM pg_database WHERE datname LIKE 'reticora\_wp427\_backfill061\_%'`)
		if err == nil {
			left, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil {
			t.Errorf("list scratch databases: %v", err)
		} else if len(left) > 0 {
			t.Errorf("scratch databases left after the test: %v", left)
		}
	})

	conn := scratchDatabase(ctx, t, dsn, "backfill061")
	m := &migrator{t: t, conn: conn, files: loadMigrations(t)}
	m.upTo(ctx, 60)

	pairs := make([]string, 0, 2*len(backfillIDs))
	for name, id := range backfillIDs {
		pairs = append(pairs, "{"+name+"}", "'"+id+"'")
	}
	if _, err := conn.Exec(ctx, strings.NewReplacer(pairs...).Replace(backfillSeed)); err != nil {
		t.Fatalf("seed at version 000060: %v", err)
	}
	m.up(ctx)
	if v := m.version(ctx); v != 61 {
		t.Fatalf("version after applying 000061: %d, want 61", v)
	}

	// Expected client|site per row ("" = NULL); edges list source and target.
	id := backfillIDs
	c1s1, c2s2, c1, c2, none := id["c1"]+"|"+id["s1"], id["c2"]+"|"+id["s2"], id["c1"]+"|", id["c2"]+"|", "|"
	want := map[string]map[string]string{
		"network_interface": {id["ifA"]: c1s1, id["ifB"]: c2s2},
		// The interface wins over the subnet (ipIfSub: interface of client 1
		// in a subnet of client 2).
		"ip_address":                   {id["ipIfSub"]: c1s1, id["ipSubB"]: c2s2, id["ipIfB"]: c2s2, id["ipNone"]: none, id["ipSubA"]: c1s1},
		"ci_contact":                   {id["ccA"]: c1s1, id["ccB"]: c2s2},
		"ci_change":                    {id["chA"]: c1s1, id["chB"]: c2s2, id["chO"]: none},
		"ci_field_value":               {id["fvA"]: c1s1, id["fvB"]: c2s2},
		"ci_instance_field_definition": {id["fdA"]: c1s1, id["fdC"]: c1},
		// The rack stands in site 1; the mount follows the CI.
		"rack_mount":        {id["rmA"]: c1s1, id["rmB"]: c2s2},
		"compliance_result": {id["crA"]: c1s1, id["crB"]: c2s2},
		"security_finding":  {id["sfA"]: c1s1, id["sfB"]: c2s2, id["sfNone"]: none},
		// A matched result follows the CI, not the collector of its job; an
		// unmatched one takes the collector's client and no site.
		"discovery_result": {id["drMatchedB"]: c2s2, id["drColA"]: c1, id["drColB"]: c2, id["drColO"]: none, id["drMatchedA"]: c1s1},
		"ci_relationship": {
			id["relAB"]: c1s1 + ">" + c2s2, id["relBO"]: c2s2 + ">" + none, id["relCA"]: c1 + ">" + c1s1,
		},
		"relationship_suppression": {id["supAB"]: c1s1 + ">" + c2s2, id["supOC"]: none + ">" + c1},
	}
	if len(want) != 12 || len(backfillParentMismatches) != 12 {
		t.Fatalf("the test covers %d/%d tables, want the 12 of migration 000061", len(want), len(backfillParentMismatches))
	}
	clients := map[string]bool{}
	for table, rows := range want {
		cols := `COALESCE(client_id::text, '') || '|' || COALESCE(site_id::text, '')`
		if table == "ci_relationship" || table == "relationship_suppression" {
			cols = `COALESCE(source_client_id::text, '') || '|' || COALESCE(source_site_id::text, '') || '>' ||
				COALESCE(target_client_id::text, '') || '|' || COALESCE(target_site_id::text, '')`
		}
		got := map[string]string{}
		r, err := conn.Query(ctx, `SELECT id::text, `+cols+` FROM `+table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for r.Next() {
			var rid, scope string
			if err = r.Scan(&rid, &scope); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			got[rid] = scope
		}
		if err = r.Err(); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if len(got) != len(rows) {
			t.Errorf("%s: %d rows after 000061, want the %d seeded", table, len(got), len(rows))
		}
		for rid, w := range rows {
			if got[rid] != w {
				t.Errorf("%s row %s: client|site after the backfill = %q, want %q", table, rid, got[rid], w)
			}
			for _, part := range strings.FieldsFunc(w, func(r rune) bool { return r == '|' || r == '>' }) {
				clients[part] = true
			}
		}
		// The same rows checked against their parents by join.
		mr, err := conn.Query(ctx, backfillParentMismatches[table])
		if err != nil {
			t.Fatalf("%s parent check: %v", table, err)
		}
		bad, err := pgx.CollectRows(mr, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("%s parent check: %v", table, err)
		}
		if len(bad) > 0 {
			slices.Sort(bad)
			t.Errorf("%s: rows whose client/site differ from their parent after 000061: %v", table, bad)
		}
	}
	for _, k := range []string{"c1", "c2", "s1", "s2"} {
		if !clients[id[k]] {
			t.Errorf("the expectations never name %s; the seed must cover two clients and two sites", k)
		}
	}

	if after := sharedVersion(); after != sharedBefore {
		t.Errorf("shared database version changed from %s to %s", sharedBefore, after)
	}
}
