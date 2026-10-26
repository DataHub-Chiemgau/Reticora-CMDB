package main

import (
	"context"
	"encoding/json"
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

	features := len(entitlement.PlanFeatures(entitlement.PlanPro))
	want := func(objects, metrics int) map[string]int {
		return map[string]int{
			"client": 2, "rack": 2, "ci": objects, "ci_location": objects, "ci_site": objects, "ci_room": objects,
			"ci_document": objects, "ci_attribute": objects, "attribute_definition": 1, "rack_mount": 40, "network_interface": objects,
			"ip_address": objects, "lldp": objects - 4 + 1, "ci_relationship": 2*(objects-4) + 1,
			"metric_sample": metrics, "collector": 1, "discovery_scope": 2, "review_item": 3, "webhook": 1,
			"vrf": 2, "entitlement": features, "org_admin": 1,
		}
	}
	check := func(round string) {
		for org, expected := range map[string]map[string]int{demo: want(10000, 2000), acme: want(40, 40)} {
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

func TestSeedOptionsAreBounded(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, o := range []Options{{Objects: 39, At: at}, {Objects: MaxObjects + 1, At: at}, {Objects: 40}, {Objects: 40, Metrics: -1, At: at}} {
		if err := o.Validate(); err == nil {
			t.Errorf("options %+v accepted", o)
		}
	}
	if err := (&Options{Objects: 10000, Metrics: 2000, At: at}).Validate(); err != nil {
		t.Errorf("the NFR-10 seed is refused: %v", err)
	}
	if got := ID("demo", "ci", "1"); len(got) != 36 || got != ID("demo", "ci", "1") {
		t.Errorf("ID = %q", got)
	}
}
