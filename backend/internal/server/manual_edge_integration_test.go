// Manual port edges survive the interface sync of discovery (WP-424; F-042,
// REL-05, REL-02).
//
//   - TestAuditDefectManualEdgeSurvivesInterfaceSync – WP-128 (REL-02,
//     REL-05; moved from audit_t6_router_auditdefect_test.go): a manual edge
//     created through POST /relationships at port Gi1/0/1 survives three
//     interface syncs without Gi1/0/1 (REC-07) unchanged, and so does the
//     port, marked stale by its miss_count; so do a port with a verified
//     discovery edge and its edge. An unverified discovery edge at Gi1/0/1
//     is removed and recorded as ci_change relationship_change of the
//     switch. Once the manual edge is deleted, the next sync removes the
//     port.
//   - TestManualEdgeInterfaceKeysAllowCIHardDelete – migration 000159: the
//     interface ends of ci_relationship are NO ACTION, not deferrable; an
//     interface an edge names cannot be deleted on its own, while the hard
//     delete of a CI (jobs.HardDeleteCI) whose port carries an edge at the
//     source or the target end succeeds and removes the edge.
//
// The tests run on PostgreSQL; the first one through the complete router of
// audit_t6_harness_test.go.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditDefectManualEdge|TestManualEdgeInterfaceKeys' ./internal/server/
package server_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditDefectManualEdgeSurvivesInterfaceSync(t *testing.T) {
	e := t6NewEnv(t, "d4", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d4-admin"), identity.AllPermissions()}
	sw1, sw2 := f.CI(t, f.OrgA, f.Client1, "audit-d4-sw1"), f.CI(t, f.OrgA, f.Client1, "audit-d4-sw2")
	sw3 := f.CI(t, f.OrgA, f.Client1, "audit-d4-sw3")
	port := func(name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, sw1, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	discoveryEdge := func(target, state, portID string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, source_interface_id)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', $4, $5) RETURNING id::text`, f.OrgA, sw1, target, state, portID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gi1 := port("Gi1/0/1")
	r := e.Call(t, admin, http.MethodPost, "/api/v1/relationships",
		`{"source_ci_id":"`+sw1+`","target_ci_id":"`+sw2+`","rel_type":"connected_to","source":"manual","source_interface_id":"`+gi1+`"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /relationships = %d %s", r.Code, t6Short(r.Body))
	}
	edge := t6ID(r.Body)
	var before string
	if err := f.Admin.QueryRow(bg, `SELECT source || '/' || verification_state FROM ci_relationship WHERE id = $1`, edge).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Discovery's own edge at the same port, and a verified discovery edge
	// at another port the observations miss as well.
	discovered := discoveryEdge(sw3, "unverified", gi1)
	gi3 := port("Gi1/0/3")
	verified := discoveryEdge(sw2, "verified", gi3)

	// Discovery reports sw1 without Gi1/0/1 often enough for REC-07 to remove it.
	repo := ipam.NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA})
	sync := func(n int) {
		t.Helper()
		if _, err := repo.SyncDiscoveredInterfaces(ctx, f.OrgA, sw1, []ipam.ObservedInterface{{Name: "Gi1/0/2"}}); err != nil {
			t.Fatalf("sync %d: %v", n, err)
		}
	}
	for i := 0; i < ipam.MaxMissedObservations; i++ {
		sync(i + 1)
	}

	var after string
	err := f.Admin.QueryRow(bg, `SELECT source || '/' || verification_state FROM ci_relationship WHERE id = $1`, edge).Scan(&after)
	if err != nil {
		t.Fatalf("manual edge %s (%s) is gone after %d discovery syncs without its interface: %v – REL-05: manual edges never disappear through discovery",
			edge, before, ipam.MaxMissedObservations, err)
	}
	if after != before {
		t.Errorf("manual edge changed by discovery: %s → %s", before, after)
	}
	if n := e.Count(t, `SELECT count(*) FROM ci_relationship WHERE id = $1 AND source_interface_id = $2`, edge, gi1); n != 1 {
		t.Errorf("manual edge at Gi1/0/1: rows=%d, want 1", n)
	}
	for _, p := range []struct{ id, name, edge string }{{gi1, "Gi1/0/1", edge}, {gi3, "Gi1/0/3", verified}} {
		var misses int
		if err = f.Admin.QueryRow(bg, `SELECT miss_count FROM network_interface WHERE id = $1`, p.id).Scan(&misses); err != nil {
			t.Fatalf("interface %s of a kept edge is gone: %v", p.name, err)
		}
		if misses != ipam.MaxMissedObservations {
			t.Errorf("interface %s: miss_count %d, want %d (kept, marked stale)", p.name, misses, ipam.MaxMissedObservations)
		}
		if e.Count(t, `SELECT count(*) FROM ci_relationship WHERE id = $1`, p.edge) != 1 {
			t.Errorf("edge %s at %s is gone", p.edge, p.name)
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM ci_relationship WHERE id = $1`, discovered); n != 0 {
		t.Errorf("discovery edge %s at the missed Gi1/0/1 still present", discovered)
	}
	var comment string
	if err = f.Admin.QueryRow(bg, `SELECT comment FROM ci_change WHERE ci_id = $1 AND change_type = 'relationship_change'
		AND field_name = 'relationships' AND old_value->>'id' = $2 AND old_value->>'source' = 'discovery' AND new_value IS NULL`,
		sw1, discovered).Scan(&comment); err != nil {
		t.Fatalf("removal of discovery edge %s not recorded in ci_change: %v", discovered, err)
	}
	if !strings.Contains(comment, "Gi1/0/1") || !strings.Contains(comment, "REC-07") {
		t.Errorf("history comment %q names neither the interface nor REC-07", comment)
	}
	if n := e.Count(t, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'relationship_change'`, sw1); n != 1 {
		t.Errorf("ci_change relationship_change rows of sw1: %d, want 1 (only the discovery edge)", n)
	}
	if n := e.Count(t, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'interfaces'`, sw1); n != 0 {
		t.Errorf("history lists %d interface removals, but no interface was removed", n)
	}

	// A further miss keeps counting; without the manual edge the port goes.
	sync(ipam.MaxMissedObservations + 1)
	if n := e.Count(t, `SELECT count(*) FROM network_interface WHERE id = $1 AND miss_count = $2`, gi1, ipam.MaxMissedObservations+1); n != 1 {
		t.Errorf("Gi1/0/1 after a fourth miss: rows=%d with miss_count %d", n, ipam.MaxMissedObservations+1)
	}
	if r = e.Call(t, admin, http.MethodDelete, "/api/v1/relationships/"+edge, ``); r.Code != http.StatusNoContent {
		t.Fatalf("DELETE /relationships/%s = %d %s", edge, r.Code, t6Short(r.Body))
	}
	sync(ipam.MaxMissedObservations + 2)
	if n := e.Count(t, `SELECT count(*) FROM network_interface WHERE id IN ($1, $2)`, gi1, gi3); n != 1 {
		t.Errorf("after the manual edge was deleted: %d of Gi1/0/1 and Gi1/0/3 left, want only Gi1/0/3", n)
	}
	if n := e.Count(t, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'interfaces' AND old_value ? 'Gi1/0/1'
		AND NOT old_value ? 'Gi1/0/3'`, sw1); n != 1 {
		t.Errorf("history of the removal of Gi1/0/1: %d rows", n)
	}
}

func TestManualEdgeInterfaceKeysAllowCIHardDelete(t *testing.T) {
	f := scopetest.Seed(t, "e3")
	bg := context.Background()

	rows, err := f.Admin.Query(bg, `SELECT conname, confdeltype::text, condeferrable FROM pg_constraint
		WHERE conrelid = 'ci_relationship'::regclass AND contype = 'f'
		  AND conname IN ('ci_relationship_source_interface_id_fkey', 'ci_relationship_target_interface_id_fkey') ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		name, onDelete string
		deferrable     bool
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (key, error) {
		var k key
		scanErr := row.Scan(&k.name, &k.onDelete, &k.deferrable)
		return k, scanErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("interface keys of ci_relationship: %+v", keys)
	}
	for _, k := range keys {
		if k.onDelete != "a" || k.deferrable {
			t.Errorf("%s: ON DELETE %q deferrable %v, want NO ACTION (a), not deferrable", k.name, k.onDelete, k.deferrable)
		}
	}

	ci := func(name string) string { return f.CI(t, f.OrgA, f.Client1, name) }
	port := func(ciID string) string {
		t.Helper()
		var id string
		if qerr := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'Gi1/0/1') RETURNING id::text`,
			f.OrgA, ciID).Scan(&id); qerr != nil {
			t.Fatal(qerr)
		}
		return id
	}
	edge := func(src, dst, srcPort, dstPort string) string {
		t.Helper()
		var id string
		if qerr := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				source_interface_id, target_interface_id)
			VALUES ($1, $2, $3, 'connected_to', 'manual', NULLIF($4, '')::uuid, NULLIF($5, '')::uuid) RETURNING id::text`,
			f.OrgA, src, dst, srcPort, dstPort).Scan(&id); qerr != nil {
			t.Fatal(qerr)
		}
		return id
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if qerr := f.Admin.QueryRow(bg, q, args...).Scan(&n); qerr != nil {
			t.Fatalf("%s: %v", q, qerr)
		}
		return n
	}

	a, b, c, d := ci("fk-a"), ci("fk-b"), ci("fk-c"), ci("fk-d")
	aPort, bPort, cPort, dPort := port(a), port(b), port(c), port(d)
	sourceSide := edge(a, b, aPort, "")
	targetSide := edge(c, d, "", dPort)
	both := edge(b, c, bPort, cPort)

	// No silent cascade: the interface alone cannot go.
	for _, p := range []string{aPort, dPort, bPort, cPort} {
		_, err = f.Admin.Exec(bg, `DELETE FROM network_interface WHERE id = $1`, p)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.TableName != "ci_relationship" {
			t.Errorf("DELETE of interface %s with an edge: %v, want foreign key violation on ci_relationship", p, err)
		}
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id IN ($1, $2, $3)`, sourceSide, targetSide, both); n != 3 {
		t.Fatalf("edges after the refused interface deletes: %d, want 3", n)
	}

	// The hard delete of a CI takes its ports and their edges along: the
	// source end (a), then the target end (d), then both ends of b → c.
	scope := database.OrgWideScope(f.OrgA, "")
	hardDelete := func(ciID string) {
		t.Helper()
		if _, qerr := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, ciID); qerr != nil {
			t.Fatal(qerr)
		}
		if qerr := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return jobs.HardDeleteCI(ctx, tx, f.OrgA, ciID)
		}); qerr != nil {
			t.Fatalf("hard delete of CI %s with an edge at its port: %v", ciID, qerr)
		}
	}
	hardDelete(a)
	if count(`SELECT count(*) FROM ci_relationship WHERE id = $1`, sourceSide) != 0 || count(`SELECT count(*) FROM network_interface WHERE id = $1`, aPort) != 0 {
		t.Error("hard delete of the source CI left its edge or port")
	}
	hardDelete(d)
	if count(`SELECT count(*) FROM ci_relationship WHERE id = $1`, targetSide) != 0 || count(`SELECT count(*) FROM network_interface WHERE id = $1`, dPort) != 0 {
		t.Error("hard delete of the target CI left its edge or port")
	}
	hardDelete(b)
	if count(`SELECT count(*) FROM ci_relationship WHERE id = $1`, both) != 0 || count(`SELECT count(*) FROM network_interface WHERE id IN ($1, $2)`, bPort, cPort) != 1 {
		t.Error("hard delete of a CI with an edge between two ports left the edge, or took the other CI's port")
	}
}
