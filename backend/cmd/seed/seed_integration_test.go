package main

import (
	"context"
	"crypto/md5" //nolint:gosec // deterministic ids, not a security use
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// baseTypeOnly restricts a query on the CIs c to the [B] data set: the
// system types switch, pdu and server without their subtypes, so the
// Phase-2 CIs of the derived types (WP-819) do not count.
const baseTypeOnly = `c.ci_type_id IN (SELECT t.id FROM ci_type t WHERE t.organization_id IS NULL
	AND t.key IN ('switch', 'pdu', 'server'))`

// counts reads the seeded rows of one organization through the maintenance
// pool; the CIs and their locations and documents count over the [B] types
// only (baseTypeOnly).
func counts(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for table, query := range map[string]string{
		"client":      `SELECT count(*) FROM client WHERE organization_id = $1`,
		"rack":        `SELECT count(*) FROM rack WHERE organization_id = $1`,
		"ci":          `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND ` + baseTypeOnly,
		"ci_location": `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND c.location_id IS NOT NULL AND ` + baseTypeOnly,
		"ci_site":     `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND c.site_id IS NOT NULL AND ` + baseTypeOnly,
		"ci_room":     `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND c.room_id IS NOT NULL AND ` + baseTypeOnly,
		"ci_document": `SELECT count(*) FROM search_document d JOIN ci c ON c.id = d.entity_id
			WHERE d.organization_id = $1 AND d.entity_type = 'ci' AND ` + baseTypeOnly,
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
	// The attribute index of the key is a schema object; it goes with the
	// test.
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+citype.KeyIndexName(AttributeKey))
	})

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
			&search.Query{OrganizationID: org.id, Text: "srv-00021"})
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
		index := citype.KeyIndexName(AttributeKey) // one index of the key for every organization (WP-607)
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
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+citype.KeyIndexName(AttributeKey))
	})

	summary, err := Run(ctx, f.App, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// One index of the searchable attribute for all organizations, none per
	// definition (WP-607; the cause U1 of the load runs of WP-439).
	ownIndexes := make([]string, 0, len(keys))
	for _, key := range keys {
		ownIndexes = append(ownIndexes, citype.IndexName(ID("seedvol-"+key, "attribute", AttributeKey), false))
	}
	var shared bool
	var own int
	if err = f.Admin.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL, count(to_regclass(n)) FROM unnest($2::text[]) AS n`,
		citype.KeyIndexName(AttributeKey), ownIndexes).Scan(&shared, &own); err != nil || !shared || own != 0 {
		t.Errorf("attribute indexes of %d organizations: index of the key %v, own indexes %d (%v); want the index of the key and no other",
			len(keys), shared, own, err)
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
		"CIs without the interfaces eth0 to eth4": `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND ` + baseTypeOnly + ` AND
			(SELECT array_agg(n.name ORDER BY n.if_index) FROM network_interface n WHERE n.ci_id = c.id) IS DISTINCT FROM '{eth0,eth1,eth2,eth3,eth4}'`,
		"management interfaces other than eth0": `SELECT count(*) FROM network_interface WHERE organization_id = $1 AND is_management <> (name = 'eth0')`,
		"interfaces without four addresses": `SELECT count(*) FROM network_interface n WHERE n.organization_id = $1 AND
			(SELECT count(*) FROM ip_address a WHERE a.interface_id = n.id) <> 4`,
		"CIs whose eth0 lacks the management address": `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND ` + baseTypeOnly + ` AND NOT EXISTS (
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
		{Objects: 40, At: at, APIKey: true, APIKeyRole: "auditor"}, {Objects: 40, At: at, APIKeyRole: "Viewer"},
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
		"the key of viewer":                {Objects: 40, At: at, APIKey: true, APIKeyRole: "viewer"},
		"the key of client_technician":     {Objects: 40, At: at, APIKey: true, APIKeyRole: "client_technician"},
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
	// An empty APIKeyRole is org_admin (WP-325).
	empty := Options{Objects: 40, At: at, APIKey: true}
	if err := empty.Validate(); err != nil || empty.keyRole() != "org_admin" || APIKeyName(empty.keyRole()) != "load-test" ||
		APIKeyName("viewer") != "load-test-viewer" {
		t.Errorf("empty APIKeyRole: %v, role %q, names %q, %q", err, empty.keyRole(), APIKeyName(empty.keyRole()), APIKeyName("viewer"))
	}
	if got := ID("demo", "ci", "1"); len(got) != 36 || got != ID("demo", "ci", "1") {
		t.Errorf("ID = %q", got)
	}
}

// phase2Counts reads the Phase-2 rows of one organization (WP-325) through
// the maintenance pool.
func phase2Counts(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for name, query := range map[string]string{
		"derived_type": `SELECT count(*) FROM ci_type c JOIN ci_type p ON p.id = c.parent_type_id
			WHERE c.organization_id = $1 AND p.organization_id IS NULL AND p.key = '` + ParentTypeKey + `'
			AND c.key IN ('` + ChassisTypeKey + `', '` + BladeTypeKey + `')`,
		"type_attribute": `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND scope = 'type'`,
		"attribute_set":  `SELECT count(*) FROM attribute_set WHERE organization_id = $1`,
		"set_attribute":  `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND scope = 'set'`,
		"type_attribute_set": `SELECT count(*) FROM ci_type_attribute_set a JOIN ci_type c ON c.id = a.ci_type_id
			WHERE c.organization_id = $1`,
		"template": `SELECT count(*) FROM template WHERE organization_id = $1`,
		"template_published": `SELECT count(*) FROM template_version v JOIN template t ON t.id = v.template_id
			WHERE t.organization_id = $1 AND v.status = 'published' AND v.published_at IS NOT NULL`,
		"template_version": `SELECT count(*) FROM template_version v JOIN template t ON t.id = v.template_id
			WHERE t.organization_id = $1`,
		"template_current": `SELECT count(*) FROM template t WHERE t.organization_id = $1
			AND t.current_version = (SELECT max(v.version) FROM template_version v WHERE v.template_id = t.id)`,
		"template_computed": `SELECT count(*) FROM template_version_item i JOIN template_version v ON v.id = i.template_version_id
			JOIN template t ON t.id = v.template_id JOIN attribute_definition d ON d.id = i.attribute_definition_id
			WHERE t.organization_id = $1 AND d.organization_id IS NULL AND d.key = '` + ComputedKey + `'`,
		"computed_definition": `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND key = '` + ComputedKey + `'`,
		"validation_rule": `SELECT count(*) FROM metamodel_rule WHERE organization_id = $1 AND kind = 'validation'
			AND ast IS NOT NULL AND ci_type_id IS NOT NULL`,
		"conditional_rule": `SELECT count(*) FROM metamodel_rule WHERE organization_id = $1 AND kind = 'required'
			AND condition IS NOT NULL AND ci_type_id IS NOT NULL`,
		"layout_engineer": `SELECT count(*) FROM form_layout l JOIN role r ON r.id = l.role_id
			WHERE l.organization_id = $1 AND r.name = 'engineer' AND l.template_id IS NOT NULL`,
		"layout_viewer": `SELECT count(*) FROM form_layout l JOIN role r ON r.id = l.role_id
			WHERE l.organization_id = $1 AND r.name = 'viewer' AND l.template_id IS NOT NULL`,
		"grant_none": `SELECT count(*) FROM field_grant g JOIN role r ON r.id = g.role_id
			WHERE g.organization_id = $1 AND r.name = 'viewer' AND g.object_kind = 'ci' AND g.level = 'none'
			AND g.field_key LIKE 'attributes.%'`,
		"grant_read": `SELECT count(*) FROM field_grant g JOIN role r ON r.id = g.role_id
			WHERE g.organization_id = $1 AND r.name = 'viewer' AND g.object_kind = 'ci' AND g.level = 'read'
			AND g.field_key LIKE 'attributes.%'`,
		"sensitive_without_grant": `SELECT count(*) FROM field_sensitivity s WHERE s.organization_id = $1 AND s.object_kind = 'ci'
			AND NOT EXISTS (SELECT 1 FROM field_grant g WHERE g.organization_id = s.organization_id AND g.field_key = s.field_key)`,
		"searchable": `SELECT count(*) FROM attribute_definition WHERE organization_id = $1 AND searchable`,
		"user":       `SELECT count(*) FROM app_user WHERE organization_id = $1`,
		"engineer": `SELECT count(*) FROM role_assignment a JOIN role r ON r.id = a.role_id JOIN app_user u ON u.id = a.user_id
			WHERE a.organization_id = $1 AND r.name = 'engineer' AND u.email LIKE 'engineer@%' AND a.scope_client_id IS NULL`,
		"viewer": `SELECT count(*) FROM role_assignment a JOIN role r ON r.id = a.role_id JOIN app_user u ON u.id = a.user_id
			WHERE a.organization_id = $1 AND r.name = 'viewer' AND u.email LIKE 'viewer@%' AND a.scope_client_id IS NULL`,
		"client_technician": `SELECT count(*) FROM role_assignment a JOIN role r ON r.id = a.role_id
			JOIN app_user u ON u.id = a.user_id JOIN client c ON c.id = a.scope_client_id
			WHERE a.organization_id = $1 AND r.name = 'client_technician' AND u.email LIKE 'client_technician@%' AND c.slug = 'hq'`,
		"metamodel_audit": `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type IN
			('ci_type', 'attribute_definition', 'attribute_set', 'template', 'template_version', 'metamodel_rule', 'form_layout')`,
	} {
		var n int
		if err := f.Admin.QueryRow(context.Background(), query, orgID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

// TestSeedSIM01Phase2 covers the Phase-2 part of SIM-01 (WP-325): through
// the services of the metamodel WPs the demo organization holds two types
// derived from server, two attribute sets assigned to them, two templates
// with two published versions each that use the computed system attribute
// warranty_days_left, a validation and a conditional rule, layouts of each
// template for engineer and viewer, field grants of viewer, a sensitive
// attribute without a grant and the users of the standard roles, the client
// technician scoped to the client hq; acme holds a derived type, a template
// with one published version and the user viewer. The seed defines no
// further searchable attribute and keeps the SIM-01 data set. The load-test
// key of APIKeyRole viewer belongs to the user viewer with the permissions
// of the role; a run for org_admin issues load-test and leaves
// load-test-viewer active. A further run changes no count and writes no
// metamodel audit entry.
//
// The assets and the stock (WP-819): demo holds 2 suppliers and 3
// contracts (one ends within 30 days of the anchor, one has ended), 60
// assets with the AST-01 fields, 40 of them linked to the mounted [B] CIs
// and 2 to the blade chassis through the CI service (ci.asset_id, primary
// CI, asset.linked, warranty_days_left), 18 in the bins; the chassis carry
// 4 configuration blades each without asset. The storage branch warehouse
// → zone → shelf → 10 bins lies below the site hq, the 20 consumables get
// their stock from receipts only, stock_level is the sum of the bookings
// per consumable and bin and one consumable is below its minimum. There is
// an internal order in each of the nine states, requested by engineer and
// approved by the administrator, the receipts of three of them booked with
// order_id, and a running stocktake of the warehouse with its frozen
// snapshot and three scans (two found, one unexpected). viewer sees neither
// purchase price nor cost center of an asset. acme holds 10 assets (5
// linked), a branch with one bin and 2 consumables with stock. No link
// crosses an organization, no serial_mismatch review arises, the integrity
// check stays empty and a further run books nothing.
//
// The tickets and documents (WP-820): demo holds the default business
// calendar in Europe/Berlin with weekdays and two holidays relative to the
// anchor, the client hq is in Europe/Berlin and every priority has a
// policy bound to the calendar; acme has the policies without a calendar
// (built-in standard in UTC). The 10 and 2 tickets relate to [B] CIs or
// assets, demo's are assigned to engineer, and their due times are those
// of the SLA package for their creation; at the anchor a ticket is overdue
// in each organization. The 3 and 1 documents carry two versions each with
// size and sha256 of their deterministic content, the storage key of the
// upload path and the scan status clean, linked to an asset, a contract
// and a ticket of their own organization; organization_usage is the sum of
// the sizes and a further run creates no version.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSeedSIM01Phase2(t *testing.T) {
	f := scopetest.Seed(t, "07")
	ctx := context.Background()
	opts := Options{Prefix: "seedp2-", Objects: baseObjects, Metrics: 0, At: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		APIKey: true, APIKeyRole: "viewer"}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+citype.KeyIndexName(AttributeKey))
	})

	summary, err := Run(ctx, f.App, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	demo, acme := summary.Orgs[0].ID, summary.Orgs[1].ID
	admin := ID("seedp2-demo", "user", "admin")
	expected := map[string]map[string]int{
		demo: {
			"derived_type": 2, "type_attribute": 5, "attribute_set": 2, "set_attribute": 3, "type_attribute_set": 3,
			"template": 2, "template_published": 4, "template_version": 4, "template_current": 2, "template_computed": 4,
			"computed_definition": 0, "validation_rule": 1, "conditional_rule": 1, "layout_engineer": 2, "layout_viewer": 2,
			"grant_none": 1, "grant_read": 1, "sensitive_without_grant": 1, "searchable": 1,
			"user": 4, "engineer": 1, "viewer": 1, "client_technician": 1,
		},
		acme: {
			"derived_type": 1, "type_attribute": 3, "attribute_set": 0, "set_attribute": 0, "type_attribute_set": 0,
			"template": 1, "template_published": 1, "template_version": 1, "template_current": 1, "template_computed": 1,
			"computed_definition": 0, "validation_rule": 0, "conditional_rule": 0, "layout_engineer": 0, "layout_viewer": 0,
			"grant_none": 0, "grant_read": 0, "sensitive_without_grant": 0, "searchable": 1,
			"user": 2, "engineer": 0, "viewer": 1, "client_technician": 0,
		},
	}
	// The assets and the stock (WP-819).
	stockExpected := map[string]map[string]int{
		demo: {
			"supplier": 2, "contract": 3, "contract_ending": 1, "contract_ended": 1,
			"asset": 60, "asset_ast01": 60, "asset_linked": 42, "ci_asset": 42, "asset_in_bin": 18, "asset_engineer": 15,
			"chassis": 2, "chassis_asset": 2, "configuration_child": 8, "phase2_ci_network": 0,
			"warranty_set": 42, "warranty_mismatch": 0, "warranty_expiring": 4, "serial_mismatch": 0,
			"warehouse": 1, "zone": 1, "shelf": 1, "bin": 10, "bin_hq": 13,
			"consumable": 20, "consumable_ast": 20, "below_min": 1, "stock_mismatch": 0, "cross_org": 0,
			"other_movement": 0, "movement": 26, "order_receipt": 3, "order_receipt_quantity": 24,
			"order": 9, "order_engineer": 9, "order_approved_by_admin": 6,
			"order_draft": 1, "order_submitted": 1, "order_approved": 1, "order_rejected": 1, "order_ordered": 1,
			"order_partially_received": 1, "order_received": 1, "order_closed": 1, "order_cancelled": 1,
			"stocktake_running": 1, "stocktake_item_mismatch": 0, "stocktake_asset_item": 18,
			"scan": 3, "scan_found": 2, "scan_unexpected": 1, "asset_grant_none": 2, "asset_link_audit": 42,
		},
		acme: {
			"supplier": 0, "contract": 0, "contract_ending": 0, "contract_ended": 0,
			"asset": 10, "asset_ast01": 0, "asset_linked": 5, "ci_asset": 5, "asset_in_bin": 5, "asset_engineer": 0,
			"chassis": 0, "chassis_asset": 0, "configuration_child": 0, "phase2_ci_network": 0,
			"warranty_set": 5, "warranty_mismatch": 0, "warranty_expiring": 4, "serial_mismatch": 0,
			"warehouse": 1, "zone": 1, "shelf": 1, "bin": 1, "bin_hq": 4,
			"consumable": 2, "consumable_ast": 0, "below_min": 0, "stock_mismatch": 0, "cross_org": 0,
			"other_movement": 0, "movement": 2, "order_receipt": 0, "order_receipt_quantity": 0,
			"order": 0, "order_engineer": 0, "order_approved_by_admin": 0,
			"order_draft": 0, "order_submitted": 0, "order_approved": 0, "order_rejected": 0, "order_ordered": 0,
			"order_partially_received": 0, "order_received": 0, "order_closed": 0, "order_cancelled": 0,
			"stocktake_running": 0, "stocktake_item_mismatch": 0, "stocktake_asset_item": 0,
			"scan": 0, "scan_found": 0, "scan_unexpected": 0, "asset_grant_none": 0, "asset_link_audit": 5,
		},
	}
	// The calendar, the policies, the tickets and the documents (WP-820).
	ticketExpected := map[string]map[string]int{
		demo: {
			"calendar": 1, "client_hq_timezone": 1, "sla": 4, "sla_calendar": 4,
			"ticket": 10, "ticket_new": 2, "ticket_open": 2, "ticket_in_progress": 2, "ticket_waiting": 1,
			"ticket_resolved": 2, "ticket_closed": 1, "ticket_related": 10, "ticket_related_asset": 3, "ticket_engineer": 10,
			"ticket_created_before_at": 10, "ticket_sla": 10, "ticket_sla_calendar": 10, "ticket_first_response": 6,
			"ticket_resolution": 3, "ticket_cross_org": 0,
			"document": 3, "document_version": 6, "version_clean": 6, "version_key_mismatch": 0, "document_mirror_mismatch": 0,
			"link_asset": 1, "link_contract": 1, "link_ticket": 1, "link_cross_org": 0, "usage_mismatch": 0,
		},
		acme: {
			"calendar": 0, "client_hq_timezone": 0, "sla": 4, "sla_calendar": 0,
			"ticket": 2, "ticket_new": 1, "ticket_open": 0, "ticket_in_progress": 1, "ticket_waiting": 0,
			"ticket_resolved": 0, "ticket_closed": 0, "ticket_related": 2, "ticket_related_asset": 1, "ticket_engineer": 0,
			"ticket_created_before_at": 2, "ticket_sla": 2, "ticket_sla_calendar": 0, "ticket_first_response": 1,
			"ticket_resolution": 0, "ticket_cross_org": 0,
			"document": 1, "document_version": 2, "version_clean": 2, "version_key_mismatch": 0, "document_mirror_mismatch": 0,
			"link_asset": 1, "link_contract": 0, "link_ticket": 0, "link_cross_org": 0, "usage_mismatch": 0,
		},
	}
	slugs := map[string]string{demo: "seedp2-demo", acme: "seedp2-acme"}
	usage := map[string]int64{}
	audits := map[string]int{}
	check := func(round string) {
		t.Helper()
		for org, want := range ticketExpected {
			got := ticketDocCounts(t, f, org, opts.At)
			for name, n := range want {
				if got[name] != n {
					t.Errorf("%s, org %s: %s = %d, want %d", round, org, name, got[name], n)
				}
			}
			if got["ticket_overdue"] < 1 {
				t.Errorf("%s, org %s: no ticket is overdue at the anchor", round, org)
			}
			checkDueTimes(t, f, org, opts.At, org == demo)
			checkChecksums(t, f, org, slugs[org])
			var stored int64
			if usageErr := f.Admin.QueryRow(ctx, `SELECT storage_bytes FROM organization_usage WHERE organization_id = $1`, org).
				Scan(&stored); usageErr != nil {
				t.Fatalf("usage of org %s: %v", org, usageErr)
			}
			if before, ok := usage[org]; !ok {
				usage[org] = stored
			} else if stored != before {
				t.Errorf("%s, org %s: storage_bytes = %d, %d after the first run", round, org, stored, before)
			}
		}
		for org, want := range stockExpected {
			got := stockCounts(t, f, org, opts.At)
			for name, n := range want {
				if got[name] != n {
					t.Errorf("%s, org %s: %s = %d, want %d", round, org, name, got[name], n)
				}
			}
		}
		for org, want := range expected {
			got := phase2Counts(t, f, org)
			for name, n := range want {
				if got[name] != n {
					t.Errorf("%s, org %s: %s = %d, want %d", round, org, name, got[name], n)
				}
			}
			if before, ok := audits[org]; !ok {
				audits[org] = got["metamodel_audit"]
			} else if got["metamodel_audit"] != before {
				t.Errorf("%s, org %s: %d metamodel audit entries, %d after the first run", round, org, got["metamodel_audit"], before)
			}
			// The SIM-01 data set stays as it is.
			metrics := 0
			if org == acme {
				metrics = baseObjects
			}
			base := counts(t, f, org)
			for table, n := range seedCounts(baseObjects, metrics) {
				if base[table] != n {
					t.Errorf("%s, org %s: %s = %d, want %d", round, org, table, base[table], n)
				}
			}
		}
	}
	check("first run")

	// The Phase-2 data keep the integrity of the data model (ABN-03).
	for _, org := range []string{demo, acme} {
		if violations := integrityViolations(t, f, org); len(violations) > 0 {
			t.Errorf("integrity check of org %s: %v", org, violations)
		}
	}

	// The administrator publishes the versions and is the actor of the
	// metamodel audit entries.
	var byAdmin, byOthers int
	if err = f.Admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE v.published_by = $2),
			count(*) FILTER (WHERE v.published_by IS DISTINCT FROM $2)
		FROM template_version v JOIN template t ON t.id = v.template_id WHERE t.organization_id = $1`,
		demo, admin).Scan(&byAdmin, &byOthers); err != nil || byAdmin != 4 || byOthers != 0 {
		t.Errorf("publishers: %d versions by the administrator, %d others (err %v)", byAdmin, byOthers, err)
	}
	var otherActors int
	if err = f.Admin.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE organization_id = $1
		AND resource_type IN ('ci_type', 'template_version', 'form_layout') AND actor_id IS DISTINCT FROM $2`,
		demo, admin).Scan(&otherActors); err != nil || otherActors != 0 {
		t.Errorf("metamodel audit entries of another actor: %d (err %v)", otherActors, err)
	}

	// The load-test key of viewer belongs to the user viewer and holds the
	// permissions of the role (AUT-04).
	type apiKey struct {
		owner       string
		permissions []string
	}
	keyOf := func(name string) (key apiKey, active int) {
		t.Helper()
		if countErr := f.Admin.QueryRow(ctx, `SELECT count(*) FROM api_key WHERE organization_id = $1 AND name = $2
			AND revoked_at IS NULL`, demo, name).Scan(&active); countErr != nil {
			t.Fatal(countErr)
		}
		if keyErr := f.Admin.QueryRow(ctx, `SELECT created_by::text, permissions FROM api_key
			WHERE organization_id = $1 AND name = $2 AND revoked_at IS NULL LIMIT 1`, demo, name).
			Scan(&key.owner, &key.permissions); keyErr != nil {
			t.Fatalf("api key %s: %v", name, keyErr)
		}
		return key, active
	}
	viewerPermissions, _ := permission.StandardRolePermissions("viewer")
	if key, active := keyOf("load-test-viewer"); key.owner != ID("seedp2-demo", "user", "viewer") || active != 1 ||
		!sameKeys(key.permissions, viewerPermissions) || summary.Orgs[0].APIKey == "" {
		t.Errorf("load-test-viewer: %+v, %d active; want the user viewer with %v", key, active, viewerPermissions)
	}

	// A run for org_admin issues load-test and keeps load-test-viewer.
	adminOpts := opts
	adminOpts.APIKeyRole = "org_admin"
	if _, err = Run(ctx, f.App, adminOpts); err != nil {
		t.Fatalf("seed for org_admin: %v", err)
	}
	adminPermissions, _ := permission.StandardRolePermissions("org_admin")
	if key, active := keyOf("load-test"); key.owner != admin || active != 1 || !sameKeys(key.permissions, adminPermissions) {
		t.Errorf("load-test: %+v, %d active; want the administrator", key, active)
	}
	if _, active := keyOf("load-test-viewer"); active != 1 {
		t.Errorf("load-test-viewer after the org_admin run: %d active", active)
	}
	check("second run")

	// A further run for viewer replaces load-test-viewer only.
	if _, err = Run(ctx, f.App, opts); err != nil {
		t.Fatalf("third seed: %v", err)
	}
	for _, name := range []string{"load-test-viewer", "load-test"} {
		if _, active := keyOf(name); active != 1 {
			t.Errorf("%s after the third run: %d active", name, active)
		}
	}
	check("third run")
}

// stockCounts reads the asset and stock rows of one organization (WP-819)
// through the maintenance pool; at is the anchor of the seed. The
// mismatch, cross-organization and network queries count violations.
func stockCounts(t *testing.T, f *scopetest.Fixture, orgID string, at time.Time) map[string]int {
	t.Helper()
	queries := map[string]string{
		"supplier":        `SELECT count(*) FROM supplier WHERE organization_id = $1`,
		"contract":        `SELECT count(*) FROM contract WHERE organization_id = $1 AND start_date IS NOT NULL AND end_date IS NOT NULL`,
		"contract_ending": `SELECT count(*) FROM contract WHERE organization_id = $1 AND end_date BETWEEN $2::date AND $2::date + 30`,
		"contract_ended":  `SELECT count(*) FROM contract WHERE organization_id = $1 AND end_date < $2::date`,
		"asset":           `SELECT count(*) FROM asset WHERE organization_id = $1`,
		// The AST-01 fields of WP-271, supplier and contract included.
		"asset_ast01": `SELECT count(*) FROM asset WHERE organization_id = $1 AND manufacturer IS NOT NULL AND model IS NOT NULL
			AND cost_center IS NOT NULL AND supplier_id IS NOT NULL AND contract_id IS NOT NULL AND owner_user_id IS NOT NULL
			AND purchase_price IS NOT NULL AND warranty_until IS NOT NULL AND purchase_date < $2::date`,
		// Primary CI and ci.asset_id agree; the asset has the client, the
		// serial number and the location of its CI.
		"asset_linked": `SELECT count(*) FROM asset a JOIN ci c ON c.id = a.ci_id AND c.asset_id = a.id
			WHERE a.organization_id = $1 AND a.client_id = c.client_id AND a.serial_number = c.serial_number
			AND a.location_id = c.location_id`,
		"ci_asset": `SELECT count(*) FROM ci WHERE organization_id = $1 AND asset_id IS NOT NULL`,
		"asset_in_bin": `SELECT count(*) FROM asset a JOIN location l ON l.id = a.location_id
			WHERE a.organization_id = $1 AND l.kind = 'bin' AND a.ci_id IS NULL`,
		// The system view my_devices_asset of engineer (WP-307).
		"asset_engineer": `SELECT count(*) FROM asset a JOIN app_user u ON u.id = a.owner_user_id
			WHERE a.organization_id = $1 AND u.email LIKE 'engineer@%'`,
		"chassis": `SELECT count(*) FROM ci c JOIN ci_type t ON t.id = c.ci_type_id
			WHERE c.organization_id = $1 AND t.key = '` + ChassisTypeKey + `'`,
		"chassis_asset": `SELECT count(*) FROM ci c JOIN ci_type t ON t.id = c.ci_type_id JOIN asset a ON a.id = c.asset_id
			WHERE c.organization_id = $1 AND t.key = '` + ChassisTypeKey + `' AND a.client_id = c.client_id`,
		"configuration_child": `SELECT count(*) FROM ci c JOIN ci p ON p.id = c.parent_ci_id JOIN ci_type t ON t.id = c.ci_type_id
			WHERE c.organization_id = $1 AND c.child_mode = 'configuration' AND c.asset_id IS NULL AND t.key = '` + BladeTypeKey + `'
			AND c.client_id = p.client_id AND c.location_id = p.location_id`,
		"phase2_ci_network": `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND NOT ` + baseTypeOnly + ` AND (
				EXISTS (SELECT 1 FROM network_interface n WHERE n.ci_id = c.id)
				OR EXISTS (SELECT 1 FROM ci_relationship r WHERE r.source_ci_id = c.id OR r.target_ci_id = c.id))`,
		"warranty_set": `SELECT count(*) FROM ci WHERE organization_id = $1 AND asset_id IS NOT NULL
			AND jsonb_typeof(attributes->'` + ComputedKey + `') = 'number'`,
		// warranty_days_left is the days from today to the warranty end of
		// the asset (WP-717, WP-718); the system view warranty_expiring
		// (WP-307) finds the linked CIs whose warranty ends within its days
		// after the anchor, and more as the anchor ages.
		"warranty_mismatch": `SELECT count(*) FROM ci c JOIN asset a ON a.id = c.asset_id JOIN organization o ON o.id = c.organization_id
			WHERE c.organization_id = $1
			AND (c.attributes->>'` + ComputedKey + `')::int IS DISTINCT FROM a.warranty_until - (now() AT TIME ZONE o.timezone)::date`,
		"warranty_expiring": `SELECT count(*) FROM ci c JOIN asset a ON a.id = c.asset_id
			WHERE c.organization_id = $1 AND a.warranty_until BETWEEN $2::date AND $2::date + ` + fmt.Sprint(savedview.WarrantyExpiringDays) + `
			AND (c.attributes->>'` + ComputedKey + `')::int <= ` + fmt.Sprint(savedview.WarrantyExpiringDays),
		"serial_mismatch": `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'serial_mismatch'`,
		"warehouse":       `SELECT count(*) FROM location WHERE organization_id = $1 AND kind = 'warehouse'`,
		"zone":            `SELECT count(*) FROM location WHERE organization_id = $1 AND kind = 'zone'`,
		"shelf":           `SELECT count(*) FROM location WHERE organization_id = $1 AND kind = 'shelf'`,
		"bin":             `SELECT count(*) FROM location WHERE organization_id = $1 AND kind = 'bin'`,
		// The storage branch lies below the site hq.
		"bin_hq": `SELECT count(*) FROM location l JOIN client c ON c.id = l.client_id JOIN site s ON s.id = l.site_id
			WHERE l.organization_id = $1 AND l.kind IN ('warehouse', 'zone', 'shelf', 'bin') AND c.slug = 'hq'
			AND s.client_id = c.id`,
		"consumable":     `SELECT count(*) FROM consumable WHERE organization_id = $1 AND sku IS NOT NULL AND min_level > 0`,
		"consumable_ast": `SELECT count(*) FROM consumable WHERE organization_id = $1 AND supplier_id IS NOT NULL`,
		"below_min": `SELECT count(*) FROM consumable c WHERE c.organization_id = $1
			AND (SELECT COALESCE(sum(s.on_hand), 0) FROM stock_level s WHERE s.consumable_id = c.id) < c.min_level`,
		// stock_level.on_hand is the sum of the bookings per consumable and
		// bin (WP-283).
		"stock_mismatch": `SELECT count(*) FROM (
				SELECT consumable_id, bin, sum(delta) AS booked FROM (
					SELECT consumable_id, to_location_id AS bin, quantity AS delta FROM stock_movement
					WHERE organization_id = $1 AND consumable_id IS NOT NULL AND to_location_id IS NOT NULL
					UNION ALL
					SELECT consumable_id, from_location_id, -quantity FROM stock_movement
					WHERE organization_id = $1 AND consumable_id IS NOT NULL AND from_location_id IS NOT NULL) m
				GROUP BY consumable_id, bin) b
			FULL JOIN (SELECT consumable_id, location_id, on_hand FROM stock_level WHERE organization_id = $1) s
				ON s.consumable_id = b.consumable_id AND s.location_id = b.bin
			WHERE s.on_hand IS DISTINCT FROM b.booked`,
		"cross_org": `SELECT (SELECT count(*) FROM ci c JOIN asset a ON a.id = c.asset_id
				WHERE c.organization_id = $1 AND a.organization_id <> $1)
			+ (SELECT count(*) FROM stock_level s LEFT JOIN consumable k ON k.id = s.consumable_id
				LEFT JOIN location l ON l.id = s.location_id
				WHERE s.organization_id = $1 AND (k.organization_id IS DISTINCT FROM $1 OR l.organization_id IS DISTINCT FROM $1))
			+ (SELECT count(*) FROM stock_movement m LEFT JOIN consumable k ON k.id = m.consumable_id
				LEFT JOIN asset a ON a.id = m.asset_id LEFT JOIN location l ON l.id = m.to_location_id
				WHERE m.organization_id = $1 AND (k.organization_id <> $1 OR a.organization_id <> $1 OR l.organization_id <> $1))`,
		"other_movement": `SELECT count(*) FROM stock_movement WHERE organization_id = $1
			AND (movement_type <> 'receipt' OR item_kind <> 'consumable')`,
		"movement":      `SELECT count(*) FROM stock_movement WHERE organization_id = $1`,
		"order_receipt": `SELECT count(*) FROM stock_movement WHERE organization_id = $1 AND order_id IS NOT NULL`,
		"order_receipt_quantity": `SELECT COALESCE(sum(quantity), 0)::int FROM stock_movement
			WHERE organization_id = $1 AND order_id IS NOT NULL`,
		"order": `SELECT count(*) FROM internal_order WHERE organization_id = $1 AND supplier_id IS NOT NULL
			AND target_location_id IS NOT NULL`,
		"order_engineer": `SELECT count(*) FROM internal_order o JOIN app_user u ON u.id = o.requested_by
			WHERE o.organization_id = $1 AND u.email LIKE 'engineer@%'`,
		// Four eyes: the administrator approves or rejects what engineer
		// requested.
		"order_approved_by_admin": `SELECT count(*) FROM internal_order o JOIN app_user u ON u.id = o.approved_by
			WHERE o.organization_id = $1 AND u.email LIKE 'admin@%' AND o.approved_by <> o.requested_by`,
		"stocktake_running": `SELECT count(*) FROM stocktake WHERE organization_id = $1 AND status = 'in_progress'
			AND started_at IS NOT NULL AND location_id IS NOT NULL`,
		// The frozen snapshot holds the stock assets and the stock of every
		// bin of the branch.
		"stocktake_item_mismatch": `SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM stocktake WHERE organization_id = $1) THEN 0 ELSE
				(SELECT count(*) FROM stocktake_item WHERE organization_id = $1)
				- (SELECT count(*) FROM asset a JOIN location l ON l.id = a.location_id WHERE a.organization_id = $1 AND l.kind = 'bin')
				- (SELECT count(*) FROM stock_level WHERE organization_id = $1) END`,
		"stocktake_asset_item": `SELECT count(*) FROM stocktake_item WHERE organization_id = $1 AND item_kind = 'asset'`,
		"scan":                 `SELECT count(*) FROM stock_scan WHERE organization_id = $1`,
		"scan_found":           `SELECT count(*) FROM stock_scan WHERE organization_id = $1 AND scan_result = 'found'`,
		"scan_unexpected":      `SELECT count(*) FROM stock_scan WHERE organization_id = $1 AND scan_result = 'unexpected'`,
		"asset_grant_none": `SELECT count(*) FROM field_grant g JOIN role r ON r.id = g.role_id
			WHERE g.organization_id = $1 AND r.name = 'viewer' AND g.object_kind = 'asset' AND g.level = 'none'
			AND g.field_key IN ('core.purchase_cost', 'core.cost_center')`,
		"asset_link_audit": `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'asset.linked'`,
	}
	for _, status := range []string{"draft", "submitted", "approved", "rejected", "ordered", "partially_received", "received",
		"closed", "cancelled"} {
		queries["order_"+status] = `SELECT count(*) FROM internal_order WHERE organization_id = $1 AND status = '` + status + `'`
	}
	out := map[string]int{}
	for name, query := range queries {
		args := []any{orgID}
		if strings.Contains(query, "$2") {
			args = append(args, at)
		}
		var n int
		if err := f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

// sameKeys reports whether a and b hold the same keys.
func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, k := range a {
		seen[k]++
	}
	for _, k := range b {
		seen[k]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// ticketDocCounts reads the calendar, SLA, ticket and document rows of one
// organization (WP-820) through the maintenance pool; at is the anchor of
// the seed. The mismatch and cross-organization queries count violations.
func ticketDocCounts(t *testing.T, f *scopetest.Fixture, orgID string, at time.Time) map[string]int {
	t.Helper()
	queries := map[string]string{
		"calendar": `SELECT count(*) FROM business_calendar WHERE organization_id = $1 AND client_id IS NULL AND is_default
			AND timezone = '` + CalendarTimezone + `' AND jsonb_array_length(weekly_hours) = 5 AND jsonb_array_length(holidays) = 2`,
		"client_hq_timezone": `SELECT count(*) FROM client WHERE organization_id = $1 AND slug = 'hq'
			AND timezone = '` + CalendarTimezone + `'`,
		"sla": `SELECT count(DISTINCT priority) FROM sla WHERE organization_id = $1 AND client_id IS NULL AND business_calendar
			AND priority IN ('low', 'medium', 'high', 'critical')`,
		"sla_calendar": `SELECT count(*) FROM sla s JOIN business_calendar b ON b.id = s.calendar_id
			AND b.organization_id = s.organization_id WHERE s.organization_id = $1`,
		"ticket": `SELECT count(*) FROM ticket WHERE organization_id = $1`,
		"ticket_related": `SELECT count(*) FROM ticket WHERE organization_id = $1 AND client_id IS NOT NULL
			AND (related_ci_id IS NOT NULL OR related_asset_id IS NOT NULL)`,
		"ticket_related_asset": `SELECT count(*) FROM ticket WHERE organization_id = $1 AND related_asset_id IS NOT NULL`,
		"ticket_engineer": `SELECT count(*) FROM ticket t JOIN app_user u ON u.id = t.assignee_id
			WHERE t.organization_id = $1 AND u.email LIKE 'engineer@%'`,
		"ticket_created_before_at": `SELECT count(*) FROM ticket WHERE organization_id = $1 AND created_at < $2`,
		"ticket_sla":               `SELECT count(*) FROM ticket_sla WHERE organization_id = $1`,
		"ticket_sla_calendar":      `SELECT count(*) FROM ticket_sla WHERE organization_id = $1 AND calendar_id IS NOT NULL`,
		"ticket_first_response":    `SELECT count(*) FROM ticket_sla WHERE organization_id = $1 AND first_response_at IS NOT NULL`,
		"ticket_resolution": `SELECT count(*) FROM ticket_sla s JOIN ticket t ON t.id = s.ticket_id
			WHERE s.organization_id = $1 AND s.resolved_at IS NOT NULL AND t.resolved_at = s.resolved_at`,
		// At the anchor a response or a resolution is due and missing.
		"ticket_overdue": `SELECT count(*) FROM ticket_sla WHERE organization_id = $1
			AND ((first_response_at IS NULL AND response_due_at < $2) OR (resolved_at IS NULL AND resolution_due_at < $2))`,
		"ticket_cross_org": `SELECT count(*) FROM ticket t LEFT JOIN ci c ON c.id = t.related_ci_id
			LEFT JOIN asset a ON a.id = t.related_asset_id LEFT JOIN app_user u ON u.id = t.assignee_id
			LEFT JOIN ticket_sla s ON s.ticket_id = t.id LEFT JOIN sla p ON p.id = s.sla_id
			WHERE t.organization_id = $1 AND (c.organization_id <> $1 OR a.organization_id <> $1 OR u.organization_id <> $1
				OR s.organization_id <> $1 OR p.organization_id <> $1)`,
		"document":         `SELECT count(*) FROM document WHERE organization_id = $1`,
		"document_version": `SELECT count(*) FROM document_version WHERE organization_id = $1`,
		"version_clean": `SELECT count(*) FROM document_version WHERE organization_id = $1 AND scan_status = 'clean'
			AND scanned_at IS NOT NULL AND checksum_sha256 ~ '^[0-9a-f]{64}$' AND size > 0`,
		// The storage key of the upload path: org/<org>/documents/<document>/<version id>.
		"version_key_mismatch": `SELECT count(*) FROM document_version WHERE organization_id = $1
			AND storage_key <> 'org/' || organization_id || '/documents/' || document_id || '/' || id`,
		// The document mirrors its highest version.
		"document_mirror_mismatch": `SELECT count(*) FROM document d JOIN document_version v ON v.document_id = d.id
			AND v.version = (SELECT max(version) FROM document_version WHERE document_id = d.id)
			WHERE d.organization_id = $1 AND (d.version <> v.version OR d.storage_key <> v.storage_key
				OR d.file_size <> v.size OR d.mime_type <> v.mime)`,
		"link_asset":    `SELECT count(*) FROM document_link WHERE organization_id = $1 AND entity_type = 'asset'`,
		"link_contract": `SELECT count(*) FROM document_link WHERE organization_id = $1 AND entity_type = 'contract'`,
		"link_ticket":   `SELECT count(*) FROM document_link WHERE organization_id = $1 AND entity_type = 'ticket'`,
		"link_cross_org": `SELECT count(*) FROM document_link l JOIN document d ON d.id = l.document_id
			LEFT JOIN asset a ON l.entity_type = 'asset' AND a.id = l.entity_id
			LEFT JOIN contract c ON l.entity_type = 'contract' AND c.id = l.entity_id
			LEFT JOIN ticket k ON l.entity_type = 'ticket' AND k.id = l.entity_id
			WHERE l.organization_id = $1 AND (d.organization_id <> $1
				OR COALESCE(a.organization_id, c.organization_id, k.organization_id) IS DISTINCT FROM $1)`,
		// organization_usage is the sum of the sizes of the versions.
		"usage_mismatch": `SELECT count(*) FROM organization_usage u WHERE u.organization_id = $1
			AND u.storage_bytes <> (SELECT COALESCE(sum(size), 0) FROM document_version WHERE organization_id = $1)`,
	}
	for _, status := range ticket.Statuses {
		queries["ticket_"+status] = `SELECT count(*) FROM ticket WHERE organization_id = $1 AND status = '` + status + `'`
	}
	out := map[string]int{}
	for name, query := range queries {
		args := []any{orgID}
		if strings.Contains(query, "$2") {
			args = append(args, at)
		}
		var n int
		if err := f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

// checkDueTimes compares the due times of the tickets of the organization
// with the computation of the SLA package (WP-292) for their creation: in
// the seed calendar (Europe/Berlin, the holidays relative to at) with
// calendar, else in the built-in standard calendar in UTC, the zone of an
// organization without one; the targets are those of the seeded policy of
// the ticket's priority.
func checkDueTimes(t *testing.T, f *scopetest.Fixture, orgID string, at time.Time, calendar bool) {
	t.Helper()
	cal, loc := sla.DefaultCalendar(), time.UTC
	if calendar {
		cal = SeedCalendar(at)
		var err error
		if loc, err = time.LoadLocation(CalendarTimezone); err != nil {
			t.Fatal(err)
		}
	}
	schedule := cal.In(loc)
	targets := map[string]seedSLA{}
	for _, p := range seedSLAs {
		targets[p.priority] = p
	}
	rows, err := f.Admin.Query(context.Background(), `SELECT t.title, t.priority, t.created_at, s.response_due_at,
			s.resolution_due_at, p.name
		FROM ticket t JOIN ticket_sla s ON s.ticket_id = t.id JOIN sla p ON p.id = s.sla_id
		WHERE t.organization_id = $1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var title, priority, policy string
		var created, response, resolution time.Time
		if err = rows.Scan(&title, &priority, &created, &response, &resolution, &policy); err != nil {
			t.Fatal(err)
		}
		target := targets[priority]
		wantResponse, responseErr := schedule.Add(created, target.response)
		wantResolution, resolutionErr := schedule.Add(created, target.resolution)
		if responseErr != nil || resolutionErr != nil {
			t.Fatalf("org %s, %s: %v %v", orgID, title, responseErr, resolutionErr)
		}
		if policy != SLAName(priority) || !response.Equal(wantResponse) || !resolution.Equal(wantResolution) {
			t.Errorf("org %s, %s (%s, created %s): policy %s, due %s / %s; want %s, due %s / %s", orgID, title, priority,
				created.UTC().Format(time.RFC3339), policy, response.UTC().Format(time.RFC3339),
				resolution.UTC().Format(time.RFC3339), SLAName(priority), wantResponse.Format(time.RFC3339),
				wantResolution.Format(time.RFC3339))
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// checkChecksums compares size and checksum of every document version of
// the organization with its deterministic content.
func checkChecksums(t *testing.T, f *scopetest.Fixture, orgID, slug string) {
	t.Helper()
	o := &orgSeed{id: orgID, slug: slug}
	want := map[string][]byte{}
	for _, list := range [][]seedDocument{demoDocuments, acmeDocuments} {
		for i := range list {
			for n := 1; n <= DocumentVersions; n++ {
				want[fmt.Sprintf("%s/%d", list[i].title, n)] = o.documentContent(&list[i], n)
			}
		}
	}
	rows, err := f.Admin.Query(context.Background(), `SELECT d.title, v.version, v.size, v.checksum_sha256
		FROM document_version v JOIN document d ON d.id = v.document_id WHERE v.organization_id = $1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var title, checksum string
		var version int
		var size int64
		if err = rows.Scan(&title, &version, &size, &checksum); err != nil {
			t.Fatal(err)
		}
		content, ok := want[fmt.Sprintf("%s/%d", title, version)]
		if !ok || size != int64(len(content)) || checksum != sha256Hex(content) {
			t.Errorf("org %s, %s version %d: size %d, checksum %s; want the content of the seed", orgID, title, version, size, checksum)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// storedVersion is a document version as TestSeedDocumentContent reads it.
type storedVersion struct {
	key, checksum string
	size          int64
}

// TestSeedDocumentContent covers the content of the seeded documents
// (WP-820): with Options.Blobs every version lies under its storage key in
// the bucket reticora-documents, its sha256 is checksum_sha256 and its
// length the size of the version; a second run finds the same content and
// creates no version. Without a store only the metadata arise.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSeedDocumentContent(t *testing.T) {
	f := scopetest.Seed(t, "5d")
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+citype.KeyIndexName(AttributeKey))
	})
	dir := t.TempDir()
	store := blob.NewFileStore(dir)
	versionsOf := func(prefix string) map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, key := range []string{"demo", "acme"} {
			var n int
			if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM document_version WHERE organization_id = $1`,
				ID(prefix, "org", key)).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out[key] = n
		}
		return out
	}
	readVersions := func(orgs []Org) []storedVersion {
		t.Helper()
		rows, err := f.Admin.Query(ctx, `SELECT storage_key, size, checksum_sha256 FROM document_version
			WHERE organization_id IN ($1, $2)`, orgs[0].ID, orgs[1].ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []storedVersion
		for rows.Next() {
			var v storedVersion
			if err = rows.Scan(&v.key, &v.size, &v.checksum); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	opts := Options{Prefix: "seeddoc-", Objects: baseObjects, Metrics: 0, At: at, Blobs: store}
	for round := 1; round <= 2; round++ {
		summary, err := Run(ctx, f.App, opts)
		if err != nil {
			t.Fatalf("seed %d: %v", round, err)
		}
		if summary.DocumentVersions != 8 || summary.DocumentContents != 8 {
			t.Errorf("run %d: %d versions, %d contents; want 8 and 8", round, summary.DocumentVersions, summary.DocumentContents)
		}
		if got := versionsOf("seeddoc-"); got["demo"] != 6 || got["acme"] != 2 {
			t.Errorf("run %d: versions %v, want demo 6 and acme 2", round, got)
		}
		stored := readVersions(summary.Orgs)
		if len(stored) != 8 {
			t.Fatalf("run %d: %d versions in the database, want 8", round, len(stored))
		}
		for _, v := range stored {
			r, getErr := store.Get(ctx, document.Bucket, v.key)
			if getErr != nil {
				t.Errorf("run %d: content of %s: %v", round, v.key, getErr)
				continue
			}
			content, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil || int64(len(content)) != v.size || sha256Hex(content) != v.checksum {
				t.Errorf("run %d: content of %s: %d bytes, sha256 %s; want %d bytes, %s", round, v.key, len(content),
					sha256Hex(content), v.size, v.checksum)
			}
		}
	}

	// Without a store the seed writes the metadata only.
	plain := Options{Prefix: "seeddoc-plain-", Objects: baseObjects, Metrics: 0, At: at}
	summary, err := Run(ctx, f.App, plain)
	if err != nil {
		t.Fatalf("seed without store: %v", err)
	}
	if summary.DocumentVersions != 8 || summary.DocumentContents != 0 {
		t.Errorf("without store: %d versions, %d contents; want 8 and 0", summary.DocumentVersions, summary.DocumentContents)
	}
	if got := versionsOf("seeddoc-plain-"); got["demo"] != 6 || got["acme"] != 2 {
		t.Errorf("without store: versions %v, want demo 6 and acme 2", got)
	}
	for _, org := range summary.Orgs {
		if _, statErr := os.Stat(filepath.Join(dir, document.Bucket, "org", org.ID)); !os.IsNotExist(statErr) {
			t.Errorf("without store: content of org %s in the store (%v)", org.Slug, statErr)
		}
	}
}
