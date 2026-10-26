package main

import (
	"context"
	"crypto/md5" //nolint:gosec // deterministic ids, not a security use
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// counts reads the seeded rows of one organization through the maintenance
// pool.
func counts(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for table, query := range map[string]string{
		"client":            `SELECT count(*) FROM client WHERE organization_id = $1`,
		"rack":              `SELECT count(*) FROM rack WHERE organization_id = $1`,
		"ci":                `SELECT count(*) FROM ci WHERE organization_id = $1`,
		"ci_location":       `SELECT count(*) FROM ci WHERE organization_id = $1 AND location_id IS NOT NULL`,
		"ci_site":           `SELECT count(*) FROM ci WHERE organization_id = $1 AND site_id IS NOT NULL`,
		"ci_room":           `SELECT count(*) FROM ci WHERE organization_id = $1 AND room_id IS NOT NULL`,
		"ci_document":       `SELECT count(*) FROM search_document WHERE organization_id = $1 AND entity_type = 'ci'`,
		"ci_attribute":      `SELECT count(*) FROM ci WHERE organization_id = $1 AND attributes->>'` + AttributeKey + `' LIKE 'CC-___'`,
		"rack_mount":        `SELECT count(*) FROM rack_mount WHERE organization_id = $1`,
		"network_interface": `SELECT count(*) FROM network_interface WHERE organization_id = $1`,
		"ip_address":        `SELECT count(*) FROM ip_address WHERE organization_id = $1`,
		"lldp":              `SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND source_system = 'lldp'`,
		"ci_relationship":   `SELECT count(*) FROM ci_relationship WHERE organization_id = $1`,
		"metric_sample":     `SELECT count(*) FROM metric_sample WHERE organization_id = $1`,
		"collector":         `SELECT count(*) FROM collector WHERE organization_id = $1`,
		"discovery_scope":   `SELECT count(*) FROM discovery_scope WHERE organization_id = $1`,
		"review_item":       `SELECT count(*) FROM review_item WHERE organization_id = $1 AND status = 'open'`,
		"webhook":           `SELECT count(*) FROM webhook_subscription WHERE organization_id = $1`,
		"vrf":               `SELECT count(*) FROM vrf WHERE organization_id = $1`,
		"entitlement":       `SELECT count(*) FROM entitlement WHERE organization_id = $1 AND plan = 'pro'`,
		"org_admin": `SELECT count(*) FROM role_assignment a JOIN role r ON r.id = a.role_id
			WHERE a.organization_id = $1 AND r.name = 'org_admin'`,
		"attribute_definition": `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND key = '` + AttributeKey + `'
			AND ci_type_id IS NULL AND scope = 'global' AND data_type = 'text' AND searchable`,
	} {
		var n int
		if err := f.Admin.QueryRow(context.Background(), query, orgID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// seedCounts is what counts finds in an organization seeded with objects
// CIs and metrics samples and without the volumes of NFR-01: the SIM-01
// data set, scaled.
func seedCounts(objects, metrics int) map[string]int {
	return map[string]int{
		"client": 2, "rack": 2, "ci": objects, "ci_location": objects, "ci_site": objects, "ci_room": objects,
		"ci_document": objects, "ci_attribute": objects, "attribute_definition": 1, "rack_mount": 40, "network_interface": objects,
		"ip_address": objects, "lldp": objects - 4 + 1, "ci_relationship": 2*(objects-4) + 1,
		"metric_sample": metrics, "collector": 1, "discovery_scope": 2, "review_item": 3, "webhook": 1,
		"vrf": 2, "entitlement": len(entitlement.PlanFeatures(entitlement.PlanPro)), "org_admin": 1,
	}
}

// TestSeedSIM01 covers SIM-01 and the load seed of NFR-10 (WP-203, WP-433):
// the demo organization and acme hold the SIM-01 data set, the demo
// organization scales with -objects (here the 10.000 objects of NFR-10),
// every id is deterministic and a second run changes nothing. Every CI has a
// location, from which the database derives site and room (F-278), and a
// document in the search index, so the global search finds it (F-277).
// For the load scenarios of WP-437 (F-116) every CI holds a value of the
// searchable attribute cost_center, whose definition and index the seed
// creates, so the CI list filters on it; the relationships of a server and
// of the core switch are found per CI. tests/integrity-check.sql finds no
// violation in either organization. The seed runs through the application
// pool, subject to the tenant policies.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSeedSIM01(t *testing.T) {
	f := scopetest.Seed(t, "c3")
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	opts := Options{Prefix: "seedtest-", Objects: 10000, Metrics: 2000, At: at}
	// The attribute indexes are schema objects; they go with the test.
	for _, prefix := range []string{"seedtest-demo", "seedtest-acme"} {
		index := citype.IndexName(ID(prefix, "attribute", AttributeKey), false)
		t.Cleanup(func() { _, _ = f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+index) })
	}

	summary, err := Run(ctx, f.App, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(summary.Orgs) != 2 || summary.Orgs[0].Slug != "seedtest-demo" || summary.Orgs[0].Admin != "admin@demo.local" ||
		summary.Orgs[1].Slug != "seedtest-acme" || summary.Orgs[1].Admin != "admin@acme.local" {
		t.Fatalf("organizations = %+v", summary.Orgs)
	}
	demo, acme := summary.Orgs[0].ID, summary.Orgs[1].ID
	if demo != ID("seedtest-", "org", "demo") || acme != ID("seedtest-", "org", "acme") {
		t.Errorf("organization ids %s, %s are not deterministic", demo, acme)
	}

	check := func(round string) {
		for org, expected := range map[string]map[string]int{demo: seedCounts(10000, 2000), acme: seedCounts(40, 40)} {
			got := counts(t, f, org)
			for table, n := range expected {
				if got[table] != n {
					t.Errorf("%s, org %s: %s = %d, want %d", round, org, table, got[table], n)
				}
			}
		}
	}
	check("first run")

	// Determinism: the Go ids and the ids PostgreSQL derives agree.
	var name string
	if err := f.Admin.QueryRow(ctx, `SELECT name FROM ci WHERE id = $1`, ID("seedtest-demo", "ci", "7")).Scan(&name); err != nil || name != "srv-00007" {
		t.Errorf("ci 7: name %q, err %v", name, err)
	}
	var rackID string
	var position int
	if err := f.Admin.QueryRow(ctx, `SELECT rack_id::text, position_u FROM rack_mount WHERE ci_id = $1`,
		ID("seedtest-demo", "ci", "21")).Scan(&rackID, &position); err != nil ||
		rackID != ID("seedtest-demo", "rack", "2") || position != 1 {
		t.Errorf("ci 21: rack %s position %d (err %v), want rack 2 position 1", rackID, position, err)
	}

	// Location chain (F-278): a mounted CI is located at its rack like a
	// mount through the API places it (RCK-03), the database derives the
	// server room hq and its site; the other CIs are in the server room of
	// their client.
	for _, c := range []struct{ ci, location, kind, client string }{
		{"21", ID("seedtest-demo", "rack", "2"), "rack", "hq"},
		{"42", ID("seedtest-demo", "room", "hq"), "room", "hq"},
		{"41", ID("seedtest-demo", "room", "branch"), "room", "branch"},
	} {
		var location, kind, room, roomName, site, client string
		if err := f.Admin.QueryRow(ctx, `SELECT COALESCE(c.location_id::text, ''), COALESCE(l.kind, ''), COALESCE(c.room_id::text, ''), COALESCE(r.name, ''),
				COALESCE(c.site_id::text, ''), c.client_id::text
			FROM ci c LEFT JOIN location l ON l.id = c.location_id LEFT JOIN room r ON r.id = c.room_id WHERE c.id = $1`,
			ID("seedtest-demo", "ci", c.ci)).Scan(&location, &kind, &room, &roomName, &site, &client); err != nil {
			t.Errorf("ci %s: location: %v", c.ci, err)
			continue
		}
		if location != c.location || kind != c.kind || room != ID("seedtest-demo", "room", c.client) ||
			roomName != "Server room "+c.client || site != ID("seedtest-demo", "site", c.client) ||
			client != ID("seedtest-demo", "client", c.client) {
			t.Errorf("ci %s: location %s (%s), room %s (%q), site %s, client %s; want %s %s in server room %s",
				c.ci, location, kind, room, roomName, site, client, c.kind, c.location, c.client)
		}
	}

	// Search (F-277): the global search of each organization finds its own
	// srv-00021, as created through the API, and nothing of the other one.
	for _, org := range []struct{ id, prefix, other string }{{demo, "seedtest-demo", "seedtest-acme"}, {acme, "seedtest-acme", "seedtest-demo"}} {
		scope := database.OrgWideScope(org.id, "")
		res, err := search.NewPGRepository(f.App).Query(database.ContextWithTenantScope(ctx, &scope),
			search.Query{OrganizationID: org.id, Text: "srv-00021"})
		if err != nil {
			t.Fatalf("search in %s: %v", org.prefix, err)
		}
		id := ID(org.prefix, "ci", "21")
		if res.Total != 1 || len(res.Data) != 1 || res.Data[0].EntityType != "ci" || res.Data[0].EntityID != id ||
			res.Data[0].Title != "srv-00021" || res.Data[0].URL != "/cmdb/"+id {
			t.Errorf("search srv-00021 in %s: %+v, want only ci %s", org.prefix, res.Data, id)
		}
		for _, hit := range res.Data {
			if hit.EntityID == ID(org.other, "ci", "21") {
				t.Errorf("search in %s finds ci 21 of %s", org.prefix, org.other)
			}
		}
	}

	// Attribute filter and relationships of the load test (F-116, WP-437):
	// the searchable attribute has its index (as the attribute.index job
	// builds it), the CI list filters on it like the attr_filter scenario of
	// tests/load/api.js, and the relationships of a CI are found like the
	// relationships scenario asks for them.
	ctxOf := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	relationship.NewHandler(relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	get := func(org, target string, out any) int {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctxOf(org)))
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Errorf("GET %s: %d %s", target, w.Code, w.Body.String())
		}
		return w.Code
	}
	for _, c := range []struct {
		org, prefix, value string
		want               int
	}{{demo, "seedtest-demo", "CC-001", 10000 / CostCenters}, {acme, "seedtest-acme", "CC-002", 1}} {
		var valid bool
		index := citype.IndexName(ID(c.prefix, "attribute", AttributeKey), false)
		if err := f.Admin.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class x ON x.oid = i.indexrelid
			WHERE x.relname = $1`, index).Scan(&valid); err != nil || !valid {
			t.Errorf("%s: attribute index %s valid %v, err %v", c.prefix, index, valid, err)
		}
		var page struct {
			Data []struct {
				Name       string         `json:"name"`
				Attributes map[string]any `json:"attributes"`
			} `json:"data"`
			Total int `json:"total"`
		}
		target := "/api/v1/cis?limit=50&" + url.Values{"filter[attributes." + AttributeKey + "]": {c.value}}.Encode()
		if code := get(c.org, target, &page); code != http.StatusOK || page.Total != c.want || len(page.Data) != min(c.want, 50) {
			t.Errorf("%s: %s: %d, total %d, %d rows; want %d", c.prefix, target, code, page.Total, len(page.Data), c.want)
		}
		for _, row := range page.Data {
			if row.Attributes[AttributeKey] != c.value {
				t.Errorf("%s: %s holds %v, filtered for %s", c.prefix, row.Name, row.Attributes[AttributeKey], c.value)
			}
		}
	}
	type relationships struct {
		Data []struct {
			SourceCIID string `json:"source_ci_id"`
			TargetCIID string `json:"target_ci_id"`
			RelType    string `json:"rel_type"`
		} `json:"data"`
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	}
	// srv-00042 (server room hq): LLDP to sw-00001, power from pdu-00003 and
	// its room as the projected located_in.
	var rels relationships
	srv42 := ID("seedtest-demo", "ci", "42")
	if code := get(demo, "/api/v1/cis/"+srv42+"/relationships", &rels); code != http.StatusOK || rels.Total != 3 || len(rels.Data) != 3 {
		t.Errorf("relationships of srv-00042: %d, %+v", code, rels)
	}
	edges := map[string]string{}
	for _, r := range rels.Data {
		if r.SourceCIID == srv42 {
			edges[r.RelType] = r.TargetCIID
		}
	}
	if _, located := edges["located_in"]; !located || edges["connected_to"] != ID("seedtest-demo", "ci", "1") ||
		edges["powered_by"] != ID("seedtest-demo", "ci", "3") {
		t.Errorf("relationships of srv-00042: %v, want connected_to sw-00001, powered_by pdu-00003 and located_in", edges)
	}
	// sw-00001 is the hub: the uplink, every even server over LLDP and its
	// projected edges, one page at a time.
	var projected int
	sw1 := ID("seedtest-demo", "ci", "1")
	if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM ci_structure_edge WHERE source_ci_id = $1 OR target_ci_id = $1`, sw1).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if code := get(demo, "/api/v1/cis/"+sw1+"/relationships", &rels); code != http.StatusOK ||
		rels.Total != (10000-4)/2+1+projected || len(rels.Data) != 50 || !rels.HasMore {
		t.Errorf("relationships of sw-00001: %d, total %d (%d rows), want %d", code, rels.Total, len(rels.Data), (10000-4)/2+1+projected)
	}

	// Data integrity of the load seed (ABN-03, NFR-10).
	for _, org := range []string{demo, acme} {
		if violations := integrityViolations(t, f, org); len(violations) > 0 {
			t.Errorf("integrity check of org %s: %v", org, violations)
		}
	}

	// A second run with the same options adds nothing and rewrites no
	// search document.
	documents := func() (n int, latest time.Time) {
		if err := f.Admin.QueryRow(ctx, `SELECT count(*), COALESCE(max(updated_at), 'epoch') FROM search_document
			WHERE organization_id IN ($1, $2) AND entity_type = 'ci'`, demo, acme).Scan(&n, &latest); err != nil {
			t.Fatalf("search documents: %v", err)
		}
		return n, latest
	}
	n, latest := documents()
	if _, err := Run(ctx, f.App, opts); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	check("second run")
	if n2, latest2 := documents(); n2 != n || !latest2.Equal(latest) {
		t.Errorf("second run: %d search documents, last update %s; want %d, %s", n2, latest2, n, latest)
	}

	// The organizations are isolated: the demo data is invisible in acme.
	var leaked int
	if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM ci_relationship r JOIN ci c ON c.id = r.target_ci_id
		WHERE r.organization_id = $1 AND c.organization_id <> $1`, acme).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("cross-organization relationships: %d, err %v", leaked, err)
	}
}

// TestSeedNFR01Volumes covers the volume options of NFR-01 (WP-439, F-279):
// with InterfacesPerCI, IPsPerCI and RelationshipsPerCI the demo
// organization holds 5 interfaces, 20 addresses and 10 relationships per
// CI, the ratios of NFR-01 (50.000, 200.000 and 100.000 at 10.000 objects),
// here on 1.000 objects; Orgs adds small organizations org-0001 and up with
// the SIM-01 data set of acme. The rows are spread as the load test
// expects: eth0 keeps the SIM-01 interface with the management address
// (tests/load/lib.js seedDevice), the other addresses are unique in the
// VRF and take client and site of their CI, and the dependencies join
// servers only, so the impact of the core switch stays the organization
// but itself and the PDUs (tests/load/api.js). Every id is deterministic,
// tests/integrity-check.sql finds nothing and a second run adds nothing.
// The job nfr01-volume of .github/workflows/load.yml seeds 10.000 objects
// and 500 organizations and records the counts.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSeedNFR01Volumes(t *testing.T) {
	f := scopetest.Seed(t, "fa")
	ctx := context.Background()
	const objects, small = 1000, 3
	opts := Options{Prefix: "seedvol-", Objects: objects, Metrics: 0, InterfacesPerCI: 5, IPsPerCI: 20,
		RelationshipsPerCI: 10, Orgs: small, At: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	keys := []string{"demo", "acme"}
	for k := 1; k <= small; k++ {
		keys = append(keys, fmt.Sprintf("org-%04d", k))
	}
	for _, key := range keys {
		index := citype.IndexName(ID("seedvol-"+key, "attribute", AttributeKey), false)
		t.Cleanup(func() { _, _ = f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+index) })
	}

	summary, err := Run(ctx, f.App, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(summary.Orgs) != len(keys) {
		t.Fatalf("organizations = %+v, want %v", summary.Orgs, keys)
	}
	for i, key := range keys {
		if o := summary.Orgs[i]; o.Slug != "seedvol-"+key || o.ID != ID("seedvol-", "org", key) || o.Admin != "admin@"+key+".local" {
			t.Errorf("organization %d = %+v, want seedvol-%s with admin@%s.local", i, o, key, key)
		}
	}
	demo := summary.Orgs[0].ID

	// Quantities: NFR-01 per object in the demo organization, SIM-01 in
	// acme and in every small organization.
	volume := seedCounts(objects, 0)
	volume["network_interface"] = 5 * objects
	volume["ip_address"] = 20 * objects
	volume["ci_relationship"] = 10 * objects
	check := func(round string) {
		for i, o := range summary.Orgs {
			expected := seedCounts(40, 40)
			if i == 0 {
				expected = volume
			}
			got := counts(t, f, o.ID)
			for table, n := range expected {
				if got[table] != n {
					t.Errorf("%s, %s: %s = %d, want %d", round, o.Slug, table, got[table], n)
				}
			}
		}
	}
	check("first run")

	// The spread of the rows in the demo organization; every query counts
	// the rows that break a rule.
	for name, query := range map[string]string{
		"CIs without the interfaces eth0 to eth4": `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND
			(SELECT array_agg(n.name ORDER BY n.if_index) FROM network_interface n WHERE n.ci_id = c.id) IS DISTINCT FROM '{eth0,eth1,eth2,eth3,eth4}'`,
		"management interfaces other than eth0": `SELECT count(*) FROM network_interface WHERE organization_id = $1 AND is_management <> (name = 'eth0')`,
		"interfaces without four addresses": `SELECT count(*) FROM network_interface n WHERE n.organization_id = $1 AND
			(SELECT count(*) FROM ip_address a WHERE a.interface_id = n.id) <> 4`,
		"CIs whose eth0 lacks the management address": `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND NOT EXISTS (
			SELECT 1 FROM network_interface n JOIN ip_address a ON a.interface_id = n.id
			WHERE n.ci_id = c.id AND n.name = 'eth0' AND a.address = c.management_ip AND a.subnet_id = md5('seedvol-demo/subnet/management')::uuid)`,
		"further addresses outside the server networks": `SELECT count(*) FROM ip_address a WHERE a.organization_id = $1
			AND a.subnet_id IS DISTINCT FROM md5('seedvol-demo/subnet/management')::uuid
			AND NOT (a.address << '100.64.0.0/10' AND a.subnet_id = md5('seedvol-demo/subnet/servers')::uuid)`,
		"addresses twice in the VRF": `SELECT count(*) - count(DISTINCT (vrf_id, address)) FROM ip_address WHERE organization_id = $1`,
		"addresses without the client and site of their CI": `SELECT count(*) FROM ip_address a
			JOIN network_interface n ON n.id = a.interface_id JOIN ci c ON c.id = n.ci_id
			WHERE a.organization_id = $1 AND (a.client_id IS DISTINCT FROM c.client_id OR a.site_id IS DISTINCT FROM c.site_id)`,
		"dependencies not between two servers": `SELECT count(*) FROM ci_relationship r
			JOIN ci s ON s.id = r.source_ci_id JOIN ci d ON d.id = r.target_ci_id
			WHERE r.organization_id = $1 AND r.rel_type = 'depends_on'
			AND (s.name NOT LIKE 'srv-%' OR d.name NOT LIKE 'srv-%' OR s.id = d.id)`,
		"dependencies twice": `SELECT count(*) - count(DISTINCT (source_ci_id, target_ci_id)) FROM ci_relationship
			WHERE organization_id = $1 AND rel_type = 'depends_on'`,
	} {
		var n int
		if err := f.Admin.QueryRow(ctx, query, demo).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d (err %v)", name, n, err)
		}
	}
	var dependencies int
	if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND rel_type = 'depends_on'`,
		demo).Scan(&dependencies); err != nil || dependencies != 10*objects-baseRelationships(objects) {
		t.Errorf("dependencies: %d (err %v), want %d", dependencies, err, 10*objects-baseRelationships(objects))
	}

	// Determinism: the Go ids and values and those PostgreSQL derives agree.
	// eth0 keeps the id and MAC of the SIM-01 seed; eth3 of CI 7 is
	// interface 4; address 13 of CI 7 sits on interface (13-1) mod 5 + 1 = 3
	// at 100.64.0.0 + 6·19 + 11; dependency 1000 runs from server
	// 5 + 999 mod 996 = 8 to the server 1 + 999 div 996 = 2 places further.
	mac := func(key string) string {
		sum := md5.Sum([]byte(key)) //nolint:gosec // deterministic test data
		h := hex.EncodeToString(sum[:])
		return "02:" + h[0:2] + ":" + h[2:4] + ":" + h[4:6] + ":" + h[6:8] + ":" + h[8:10]
	}
	for _, c := range []struct{ id, name, mac string }{
		{ID("seedvol-demo", "if", "7"), "eth0", mac("seedvol-demo/mac/7")},
		{ID("seedvol-demo", "if", "7", "4"), "eth3", mac("seedvol-demo/mac/7/4")},
	} {
		var name, got string
		if err := f.Admin.QueryRow(ctx, `SELECT name, mac::text FROM network_interface WHERE id = $1 AND ci_id = $2`,
			c.id, ID("seedvol-demo", "ci", "7")).Scan(&name, &got); err != nil || name != c.name || got != c.mac {
			t.Errorf("interface %s: %s %s (err %v), want %s %s", c.id, name, got, err, c.name, c.mac)
		}
	}
	var iface, address string
	if err := f.Admin.QueryRow(ctx, `SELECT interface_id::text, host(address) FROM ip_address WHERE id = $1`,
		ID("seedvol-demo", "ip", "7", "13")).Scan(&iface, &address); err != nil ||
		iface != ID("seedvol-demo", "if", "7", "3") || address != "100.64.0.125" {
		t.Errorf("address 13 of CI 7: %s on %s (err %v), want 100.64.0.125 on eth2", address, iface, err)
	}
	var source, target string
	if err := f.Admin.QueryRow(ctx, `SELECT source_ci_id::text, target_ci_id::text FROM ci_relationship WHERE id = $1`,
		ID("seedvol-demo", "rel", "dependency", "1000")).Scan(&source, &target); err != nil ||
		source != ID("seedvol-demo", "ci", "8") || target != ID("seedvol-demo", "ci", "10") {
		t.Errorf("dependency 1000: %s -> %s (err %v), want srv-00008 -> srv-00010", source, target, err)
	}

	// Data integrity of every organization (ABN-03).
	for _, o := range summary.Orgs {
		if violations := integrityViolations(t, f, o.ID); len(violations) > 0 {
			t.Errorf("integrity check of %s: %v", o.Slug, violations)
		}
	}

	// A second run with the same options adds nothing.
	if _, err := Run(ctx, f.App, opts); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	check("second run")
}

// integrityViolations runs tests/integrity-check.sql for one organization
// and returns the checks with violations (ABN-03).
func integrityViolations(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	script, err := os.ReadFile("../../../tests/integrity-check.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('integrity.org', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, string(script))
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	checks := 0
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		checks++
		if n > 0 {
			out[name] = n
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checks == 0 {
		t.Fatal("integrity check returned no checks")
	}
	return out
}

// TestSeedOptionsAreBounded covers the bounds of the seed, also of the
// volume options of NFR-01 (WP-439): every option outside its bounds is
// refused before the seed touches the database, the NFR-10 seed, the
// NFR-01 volumes and the largest seed are accepted, and the largest seed
// still fits the address space of the server networks and the ring of the
// dependencies.
func TestSeedOptionsAreBounded(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, o := range []Options{
		{Objects: 39, At: at}, {Objects: MaxObjects + 1, At: at}, {Objects: 40}, {Objects: 40, Metrics: -1, At: at},
		{Objects: 40, InterfacesPerCI: -1, At: at}, {Objects: 40, InterfacesPerCI: MaxInterfacesPerCI + 1, At: at},
		{Objects: 40, IPsPerCI: -1, At: at}, {Objects: 40, IPsPerCI: MaxIPsPerCI + 1, At: at},
		{Objects: 40, RelationshipsPerCI: -1, At: at}, {Objects: 40, RelationshipsPerCI: 1, At: at},
		{Objects: 40, RelationshipsPerCI: MaxRelationshipsPerCI + 1, At: at},
		{Objects: 40, Orgs: -1, At: at}, {Objects: 40, Orgs: MaxOrgs + 1, At: at},
	} {
		if err := o.Validate(); err == nil {
			t.Errorf("options %+v accepted", o)
			continue
		}
		// Run validates first: the nil pool is never used.
		if _, err := Run(context.Background(), nil, o); err == nil {
			t.Errorf("Run with options %+v did not fail", o)
		}
	}
	for name, o := range map[string]Options{
		"the NFR-10 seed":    {Objects: 10000, Metrics: 2000, At: at},
		"the NFR-01 volumes": {Objects: 10000, Metrics: 2000, InterfacesPerCI: 5, IPsPerCI: 20, RelationshipsPerCI: 10, Orgs: 500, At: at},
		"the largest seed": {Objects: MaxObjects, Metrics: 2000, InterfacesPerCI: MaxInterfacesPerCI, IPsPerCI: MaxIPsPerCI,
			RelationshipsPerCI: MaxRelationshipsPerCI, Orgs: MaxOrgs, At: at},
		"one per CI and two relationships": {Objects: 40, InterfacesPerCI: 1, IPsPerCI: 1, RelationshipsPerCI: 2, At: at},
	} {
		if err := o.Validate(); err != nil {
			t.Errorf("%s is refused: %v", name, err)
		}
	}
	// The further addresses of the largest seed fit 100.64.0.0/10.
	if n := MaxObjects * (MaxIPsPerCI - 1); n > 1<<22 {
		t.Errorf("%d further addresses exceed the 4.194.304 of %s/10", n, ExtraAddressBase)
	}
	// No server depends on itself: the distance in the ring of the S
	// servers stays below S for every number of objects (the smallest seed
	// has the largest share of dependencies per server).
	for _, objects := range []int{baseObjects, 1000, MaxObjects} {
		servers := objects - 4
		dependencies := MaxRelationshipsPerCI*objects - baseRelationships(objects)
		if farthest := 1 + (dependencies-1)/servers; farthest >= servers {
			t.Errorf("%d objects: dependency distance %d reaches the %d servers", objects, farthest, servers)
		}
	}
	if got := ID("demo", "ci", "1"); len(got) != 36 || got != ID("demo", "ci", "1") {
		t.Errorf("ID = %q", got)
	}
}
