package topology_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// TestAnalyzerOrgWideScopeInWorker covers WP-1025 AK6 (IMP-05): the
// analyzer runs in the scope of its context. A worker under
// database.OrgWideScope sees the CIs of every client: the router outage
// reaches the CPEs of client 2 and Roots assigns them to the router, and
// the second PDU of client 2 keeps the server up. A context restricted to
// client 1 sees only its CIs (RLS): the CPEs are neither impacted nor
// readable as failed CIs, and the PDU of client 2 keeps the server up
// without being named. A context restricted to site 1 likewise sees
// neither the CPEs nor PDU 2 of site 2. management_ip stays empty for a principal without a
// resolvable field policy.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAnalyzerOrgWideScopeInWorker(t *testing.T) {
	f := scopetest.Seed(t, "a7")
	bg := context.Background()
	link := func(source, relType, target string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, $4)`,
			f.OrgA, source, target, relType); err != nil {
			t.Fatal(err)
		}
	}
	// Site 1 (client 1) holds router, switch, server and PDU 1, site 2
	// (client 2) the CPEs and PDU 2; the CIs are placed before the edges,
	// which take the sites of their ends.
	site := func(clientID, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
			f.Room(t, clientID, name)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site1, site2 := site(f.Client1, "analyzer-site-1"), site(f.Client2, "analyzer-site-2")
	placed := func(id, siteID string) string {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, id, siteID); err != nil {
			t.Fatal(err)
		}
		return id
	}
	router := placed(f.CI(t, f.OrgA, f.Client1, "analyzer-router"), site1)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET management_ip = '192.0.2.1' WHERE id = $1`, router); err != nil {
		t.Fatal(err)
	}
	sw := placed(f.CI(t, f.OrgA, f.Client1, "analyzer-switch"), site1)
	link(sw, "uplink_to", router)
	cpes := []string{placed(f.CI(t, f.OrgA, f.Client2, "analyzer-cpe-1"), site2), placed(f.CI(t, f.OrgA, f.Client2, "analyzer-cpe-2"), site2)}
	for _, cpe := range cpes {
		link(cpe, "uplink_to", sw)
	}
	server := placed(f.CI(t, f.OrgA, f.Client1, "analyzer-server"), site1)
	pdu1 := placed(f.CI(t, f.OrgA, f.Client1, "analyzer-pdu-1"), site1)
	pdu2 := placed(f.CI(t, f.OrgA, f.Client2, "analyzer-pdu-2"), site2)
	link(server, "powered_by", pdu1)
	link(server, "powered_by", pdu2)

	a := topology.NewAnalyzer(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App))
	worker := database.OrgWideScope(f.OrgA, "")
	workerCtx := database.ContextWithTenantScope(bg, &worker)

	res, err := a.Impact(workerCtx, f.OrgA, []string{router}, topology.ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Certain != 3 {
		t.Errorf("org-wide: %d certain impacts, want switch and both CPEs", res.Summary.Certain)
	}
	// A worker without a principal reads as the system; a principal whose
	// field policy cannot be resolved does not see management_ip
	// (fail-closed).
	if res.Failed[0].ManagementIP != "192.0.2.1" {
		t.Errorf("worker: management_ip %q", res.Failed[0].ManagementIP)
	}
	userCtx := identity.WithPrincipal(workerCtx, identity.Principal{Subject: "user-1", OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser})
	withUser, err := a.Impact(userCtx, f.OrgA, []string{router}, topology.ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if withUser.Failed[0].ManagementIP != "" {
		t.Errorf("management_ip without a resolvable field policy: %q", withUser.Failed[0].ManagementIP)
	}
	roots, err := a.Roots(workerCtx, f.OrgA, append([]string{cpes[1], router, sw}, cpes[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append([]string{router, sw}, cpes...) {
		if roots[id] != router {
			t.Errorf("org-wide: root of %s is %s, want the router", id, roots[id])
		}
	}
	protected, err := a.Impact(workerCtx, f.OrgA, []string{pdu1}, topology.ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(protected.Protected) != 1 || protected.Protected[0].ID != server || len(protected.Protected[0].Providers) != 1 ||
		protected.Protected[0].Providers[0].CIID != pdu2 {
		t.Errorf("org-wide: protected %+v, want the server with PDU 2", protected.Protected)
	}

	// Client 1 sees neither the CPEs nor PDU 2.
	client1 := f.ClientCtx(f.Client1)
	res, err = a.Impact(client1, f.OrgA, []string{router}, topology.ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Certain != 1 || res.Impacted[0].ID != sw {
		t.Errorf("client 1: impacted %+v, want the switch only", res.Impacted)
	}
	if _, err = a.Roots(client1, f.OrgA, []string{router, cpes[0]}); !errors.Is(err, topology.ErrNotFound) {
		t.Errorf("client 1 with a CPE of client 2: %v, want ErrNotFound", err)
	}
	protected, err = a.Impact(client1, f.OrgA, []string{pdu1}, topology.ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(protected.Impacted) != 0 || len(protected.Protected) != 1 || len(protected.Protected[0].Providers) != 0 {
		t.Errorf("client 1: impacted %v, protected %+v, want the server kept up by an unnamed provider", protected.Impacted, protected.Protected)
	}
	// Client 2 does not see the router.
	if _, err = a.Impact(f.ClientCtx(f.Client2), f.OrgA, []string{router}, topology.ImpactOptions{}); !errors.Is(err, topology.ErrNotFound) {
		t.Errorf("client 2: %v, want ErrNotFound", err)
	}

	// A principal restricted to site 1 (clients unrestricted) sees neither
	// the CPEs nor PDU 2 of site 2.
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	siteCtx := database.ContextWithTenantScope(bg, &siteScope)
	res, err = a.Impact(siteCtx, f.OrgA, []string{router}, topology.ImpactOptions{ServiceChain: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Certain != 1 || len(res.Impacted) != 1 || res.Impacted[0].ID != sw {
		t.Errorf("site 1: impacted %+v, want the switch only", res.Impacted)
	}
	for _, id := range res.SiteIDs {
		if id != site1 {
			t.Errorf("site 1: the service chain names site %s", id)
		}
	}
	if _, err = a.Roots(siteCtx, f.OrgA, []string{router, cpes[0]}); !errors.Is(err, topology.ErrNotFound) {
		t.Errorf("site 1 with a CPE of site 2: %v, want ErrNotFound", err)
	}
	roots, err = a.Roots(siteCtx, f.OrgA, []string{router, sw})
	if err != nil || len(roots) != 2 || roots[sw] != router {
		t.Errorf("site 1: roots %v %v, want both on the router", roots, err)
	}
	protected, err = a.Impact(siteCtx, f.OrgA, []string{pdu1}, topology.ImpactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(protected.Impacted) != 0 || len(protected.Protected) != 1 || len(protected.Protected[0].Providers) != 0 {
		t.Errorf("site 1: impacted %v, protected %+v, want the server kept up by an unnamed provider", protected.Impacted, protected.Protected)
	}
	if _, err = a.Upstream(siteCtx, f.OrgA, cpes[1], 0); !errors.Is(err, topology.ErrNotFound) {
		t.Errorf("site 1: upstream of a CPE of site 2: %v, want ErrNotFound", err)
	}
}

// TestAnalyzerRootsBudget covers WP-1025 AK7: Roots over 2,000 failed CIs
// (a hub and 1,999 CPEs uplink_to it) costs the statements of a multiple
// impact of the same set, and every CPE has the hub as root.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAnalyzerRootsBudget(t *testing.T) {
	f := scopetest.Seed(t, "a8")
	bg := context.Background()
	hub := f.CI(t, f.OrgA, f.Client1, "roots-hub")
	rows, err := f.Admin.Query(bg, `WITH leaf AS (
			INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
			SELECT $1, $2, (SELECT ci_type_id FROM ci WHERE id = $3), 'roots-cpe-' || n, 'active' FROM generate_series(1, 1999) AS n
			RETURNING id),
		rel AS (INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			SELECT $1, leaf.id, $3, 'uplink_to' FROM leaf)
		SELECT id::text FROM leaf`, f.OrgA, f.Client1, hub)
	if err != nil {
		t.Fatal(err)
	}
	failed := []string{hub}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			t.Fatal(scanErr)
		}
		failed = append(failed, id)
	}
	rows.Close()
	if len(failed) != 2000 {
		t.Fatalf("%d failed CIs, want 2000", len(failed))
	}
	if _, err = f.Admin.Exec(bg, `ANALYZE ci; ANALYZE ci_relationship`); err != nil {
		t.Fatal(err)
	}

	cfg := f.App.Config()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a := topology.NewAnalyzer(ci.NewPGRepository(pool), relationship.NewPGRepository(pool)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(pool))
	ctx := f.OrgCtx(f.OrgA)
	// Warm the pool: the first connection switches its role.
	if _, err = a.Upstream(ctx, f.OrgA, hub, 1); err != nil {
		t.Fatal(err)
	}

	before := counter.n.Load()
	if _, err = a.Impact(ctx, f.OrgA, failed, topology.ImpactOptions{}); err != nil {
		t.Fatal(err)
	}
	impactStatements := counter.n.Load() - before

	before, start := counter.n.Load(), time.Now()
	roots, err := a.Roots(ctx, f.OrgA, failed)
	if err != nil {
		t.Fatal(err)
	}
	rootStatements, elapsed := counter.n.Load()-before, time.Since(start)
	if rootStatements != impactStatements {
		t.Errorf("Roots over 2,000 CIs: %d statements, the multiple impact of the set %d", rootStatements, impactStatements)
	}
	if len(roots) != 2000 {
		t.Fatalf("%d assignments, want 2000", len(roots))
	}
	for id, root := range roots {
		if root != hub {
			t.Fatalf("root of %s is %s, want the hub", id, root)
		}
	}
	t.Logf("Roots over 2,000 failed CIs: %d statements (impact %d) in %v", rootStatements, impactStatements, elapsed)
}
