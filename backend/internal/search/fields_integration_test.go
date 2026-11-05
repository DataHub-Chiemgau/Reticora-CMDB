package search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// sfWorld is the fixture of the search over identifiers and custom fields
// (SRC-02, WP-308).
type sfWorld struct {
	f      *scopetest.Fixture
	repo   *search.PGRepository
	parent string // CI type of the organization
	child  string // CI type below parent
	alpha  string // CI of type child, client 1: management IP, primary MAC, interface, attributes
	beta   string // CI of client 1 linked to the asset
	gamma  string // CI of client 1 linked to the asset
	delta  string // CI of client 2 with a management IP
	asset  string // asset of client 1 with inventory number and custom fields
}

func newSFWorld(t *testing.T) *sfWorld {
	t.Helper()
	f := scopetest.Seed(t, "38")
	bg := context.Background()
	w := &sfWorld{f: f, repo: search.NewPGRepository(f.App), parent: f.ID(), child: f.ID()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO ci_type (id, organization_id, key, name) VALUES ($1, $2, 'wp308-parent', 'WP-308 Parent')`, w.parent, f.OrgA)
	exec(`INSERT INTO ci_type (id, organization_id, key, name, parent_type_id) VALUES ($1, $2, 'wp308-child', 'WP-308 Child', $3)`,
		w.child, f.OrgA, w.parent)
	exec(`INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, searchable, inheritable) VALUES
		($1, $2, 'type', 'wp308_type', 'string', true, false),
		($1, $3, 'type', 'wp308_parent', 'string', true, true),
		($1, NULL, 'global', 'wp308_global', 'string', true, true),
		($1, $2, 'type', 'wp308_hidden', 'string', false, true)`, f.OrgA, w.child, w.parent)
	w.alpha = f.CI(t, f.OrgA, f.Client1, "wp308 alpha")
	exec(`UPDATE ci SET ci_type_id = $2, management_ip = '10.38.0.5', primary_mac = '08:00:2b:38:00:05',
		attributes = '{"wp308_type": "typeval38", "wp308_parent": "parentval38", "wp308_global": "globalval38", "wp308_hidden": "hiddenval38"}'
		WHERE id = $1`, w.alpha, w.child)
	w.asset = f.Asset(t, f.OrgA, f.Client1, "INV-38-A")
	exec(`UPDATE asset SET name = 'wp308 asset', custom_fields = '{"cf38": "customval38", "cflist": ["x38", 7]}' WHERE id = $1`, w.asset)
	w.beta = f.CI(t, f.OrgA, f.Client1, "wp308 beta")
	w.gamma = f.CI(t, f.OrgA, f.Client1, "wp308 gamma")
	exec(`UPDATE ci SET asset_id = $2 WHERE id = ANY($1::uuid[])`, []string{w.beta, w.gamma}, w.asset)
	w.delta = f.CI(t, f.OrgA, f.Client2, "wp308 delta")
	exec(`UPDATE ci SET management_ip = '10.38.9.9' WHERE id = $1`, w.delta)
	var ifc string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name, mac) VALUES ($1, $2, 'eth38', '08:00:2b:38:aa:01')
		RETURNING id::text`, f.OrgA, w.alpha).Scan(&ifc); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ip_address (organization_id, interface_id, address) VALUES ($1, $2, '192.168.38.10')`, f.OrgA, ifc)
	if _, err := w.repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	return w
}

// exec runs a statement through the maintenance pool.
func (w *sfWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// search runs GET /api/v1/search?q= as a principal of the scope with the
// policy, with or without ipam:read, and returns the entity ids of the
// hits.
func (w *sfWorld) search(t *testing.T, scope database.TenantScope, policy *permission.FieldPolicy, ipamRead bool, text string) map[string]bool {
	t.Helper()
	granted := map[string]bool{"ci:read": true, "asset:read": true, "ipam:read": ipamRead}
	mux := chi.NewRouter()
	search.NewHandler(w.repo, permissionStub{granted: granted}).WithGates(allFeatures{}, allModules).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+url.Values{"q": {text}, "limit": {"100"}}.Encode(), nil)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User})
	ctx = permission.WithPolicy(database.ContextWithTenantScope(ctx, &scope), policy)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	var body struct {
		Data []search.Hit `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("GET /api/v1/search?q=%s: %d %s", text, rec.Code, rec.Body.String())
	}
	out := map[string]bool{}
	for i := range body.Data {
		out[body.Data[i].EntityID] = true
	}
	return out
}

// expect checks that the hits of the search contain want and none of
// unwanted.
func (w *sfWorld) expect(t *testing.T, label string, hits map[string]bool, want, unwanted []string) {
	t.Helper()
	for _, id := range want {
		if !hits[id] {
			t.Errorf("%s: %s is no hit (%v)", label, w.name(id), hits)
		}
	}
	for _, id := range unwanted {
		if hits[id] {
			t.Errorf("%s: %s is a hit", label, w.name(id))
		}
	}
}

func (w *sfWorld) name(id string) string {
	return map[string]string{w.alpha: "alpha", w.beta: "beta", w.gamma: "gamma", w.delta: "delta", w.asset: "asset"}[id] + " " + id
}

// open searches org-wide without field restrictions and with ipam:read.
func (w *sfWorld) open(t *testing.T, text string) map[string]bool {
	t.Helper()
	return w.search(t, database.OrgWideScope(w.f.OrgA, w.f.User), permission.UnrestrictedPolicy(frPerms), true, text)
}

// identifierRows returns the identifier rows of organization A, sorted.
func identifierRows(t *testing.T, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, orgID string) []string {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT concat_ws('|', entity_type, entity_id, field, COALESCE(source_id::text, '-'), norm, COALESCE(addr::text, '-'))
		FROM search_identifier WHERE organization_id = $1 ORDER BY 1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fieldRows returns the field rows of the ci and asset documents of
// organization A, sorted.
func fieldRows(t *testing.T, pool *pgxpool.Pool, orgID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT concat_ws('|', d.entity_type, d.entity_id, f.field, f.content, f.source)
		FROM search_document_field f JOIN search_document d ON d.id = f.document_id
		WHERE f.organization_id = $1 AND d.entity_type IN ('ci', 'asset') ORDER BY 1`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSearchFields covers WP-308 (SRC-02): GET /api/v1/search finds CIs and
// assets by inventory number (at the asset and at every CI linked to it),
// management IP, interface IP (exact, partial, CIDR), MAC in four formats,
// searchable attributes (type, parent type, org-global) and asset custom
// fields; a non-searchable attribute is no hit. The triggers and the
// indexer keep the rows current (asset update, inventory number, asset.ci_id,
// asset deletion, interface and address, MAC change, removal by
// missAndRemove, an unchanged second sync changes nothing), ReindexTenant
// and the backfill of migration 000233 write the same rows. Field grant
// none on core.management_ip or on the inventory number, a missing
// ipam:read and a client scope hide the hits.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchFields(t *testing.T) {
	w := newSFWorld(t)
	f := w.f
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)

	for _, c := range []struct {
		q    string
		want []string
	}{
		{"INV-38-A", []string{w.asset, w.beta, w.gamma}},
		{"inv-38", []string{w.asset, w.beta, w.gamma}},
		{"10.38.0.5", []string{w.alpha}},
		{"192.168.38.10", []string{w.alpha}},
		{"192.168.38", []string{w.alpha}},
		{"192.168.38.0/24", []string{w.alpha}},
		{"08:00:2b:38:aa:01", []string{w.alpha}},
		{"08-00-2B-38-AA-01", []string{w.alpha}},
		{"0800.2b38.aa01", []string{w.alpha}},
		{"08002b38aa01", []string{w.alpha}},
		{"08:00:2b:38:00:05", []string{w.alpha}},
		{"typeval38", []string{w.alpha}},
		{"parentval38", []string{w.alpha}},
		{"globalval38", []string{w.alpha}},
		{"customval38", []string{w.asset}},
		{"x38", []string{w.asset}},
	} {
		w.expect(t, c.q, w.open(t, c.q), c.want, nil)
	}
	w.expect(t, "not searchable attribute", w.open(t, "hiddenval38"), nil, []string{w.alpha})
	w.expect(t, "other subnet", w.open(t, "192.168.39.0/24"), nil, []string{w.alpha})
	w.expect(t, "MAC prefix below six digits", w.open(t, "08:00:2"), nil, []string{w.alpha})

	// Rights.
	fr := &frWorld{f: f, repo: w.repo}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	_, noIP := fr.role(t, "wp308-noip", [4]string{"ci", "", "core.management_ip", "none"})
	w.expect(t, "management IP none", w.search(t, orgWide, noIP, true, "10.38.0.5"), nil, []string{w.alpha})
	w.expect(t, "management IP none, interface IP", w.search(t, orgWide, noIP, true, "192.168.38.10"), []string{w.alpha}, nil)
	open := permission.UnrestrictedPolicy(frPerms)
	w.expect(t, "without ipam:read, interface IP", w.search(t, orgWide, open, false, "192.168.38.10"), nil, []string{w.alpha})
	w.expect(t, "without ipam:read, interface MAC", w.search(t, orgWide, open, false, "08:00:2b:38:aa:01"), nil, []string{w.alpha})
	w.expect(t, "without ipam:read, management IP", w.search(t, orgWide, open, false, "10.38.0.5"), []string{w.alpha}, nil)
	inventoryKey, _, _ := permission.FieldKeyOf(permission.KindAsset, "inventory_no")
	_, noInventory := fr.role(t, "wp308-noinv", [4]string{"asset", "", inventoryKey, "none"})
	w.expect(t, "inventory none", w.search(t, orgWide, noInventory, true, "INV-38-A"), nil, []string{w.asset, w.beta, w.gamma})
	_, ciRestricted := fr.role(t, "wp308-cirestricted", [4]string{"ci", "", "core.fqdn", "none"})
	w.expect(t, "restricted CI, open asset", w.search(t, orgWide, ciRestricted, true, "INV-38-A"), []string{w.asset, w.beta, w.gamma}, nil)
	w.expect(t, "restricted CI, attributes", w.search(t, orgWide, ciRestricted, true, "typeval38"), []string{w.alpha}, nil)
	w.expect(t, "restricted CI, interface MAC", w.search(t, orgWide, ciRestricted, true, "0800.2b38.aa01"), []string{w.alpha}, nil)
	_, both := fr.role(t, "wp308-both", [4]string{"ci", "", "core.fqdn", "none"}, [4]string{"asset", "", inventoryKey, "none"})
	w.expect(t, "restricted CI, inventory none", w.search(t, orgWide, both, true, "INV-38-A"), nil, []string{w.asset, w.beta, w.gamma})
	_, noAttr := fr.role(t, "wp308-noattr", [4]string{"ci", "", "attributes.wp308_type", "none"})
	w.expect(t, "attribute none", w.search(t, orgWide, noAttr, true, "typeval38"), nil, []string{w.alpha})
	w.expect(t, "other attribute", w.search(t, orgWide, noAttr, true, "parentval38"), []string{w.alpha}, nil)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	w.expect(t, "client 1, CI of client 2", w.search(t, client1, open, true, "10.38.9.9"), nil, []string{w.delta})
	w.expect(t, "org-wide, CI of client 2", w.search(t, orgWide, open, true, "10.38.9.9"), []string{w.delta}, nil)
	w.expect(t, "client 1, own CI", w.search(t, client1, open, true, "10.38.0.5"), []string{w.alpha}, nil)

	// The asset trigger: custom fields, inventory number, asset.ci_id.
	w.exec(t, `UPDATE asset SET custom_fields = '{"cf38": "newcf38"}' WHERE id = $1`, w.asset)
	w.expect(t, "new custom field", w.open(t, "newcf38"), []string{w.asset}, nil)
	w.expect(t, "old custom field", w.open(t, "customval38"), nil, []string{w.asset})
	w.exec(t, `UPDATE asset SET inventory_no = 'INV-38-B' WHERE id = $1`, w.asset)
	w.expect(t, "new inventory number", w.open(t, "INV-38-B"), []string{w.asset, w.beta, w.gamma}, nil)
	w.expect(t, "old inventory number", w.open(t, "INV-38-A"), nil, []string{w.asset, w.beta, w.gamma})
	w.exec(t, `UPDATE asset SET ci_id = $2 WHERE id = $1`, w.asset, w.alpha)
	w.expect(t, "asset.ci_id set", w.open(t, "INV-38-B"), []string{w.asset, w.alpha, w.beta, w.gamma}, nil)
	// Clearing asset.ci_id keeps the CI linked (WP-272); unlinking the CI
	// clears asset.ci_id (ci_sync_asset_primary).
	w.exec(t, `UPDATE ci SET asset_id = NULL WHERE id = $1`, w.alpha)
	w.expect(t, "asset.ci_id cleared", w.open(t, "INV-38-B"), []string{w.asset, w.beta, w.gamma}, []string{w.alpha})

	// The indexer: a CI read back and indexed like the CI service does.
	cis := ci.NewPGRepository(f.App)
	indexer := search.NewCIIndexer(w.repo)
	reindexCI := func(id string) {
		t.Helper()
		item, err := cis.GetByID(orgCtx, f.OrgA, id)
		if err != nil {
			t.Fatal(err)
		}
		if err = indexer.IndexDocument(orgCtx, ptr(ci.IndexDocumentFor(item))); err != nil {
			t.Fatal(err)
		}
	}
	w.exec(t, `UPDATE ci SET management_ip = '10.38.0.6', attributes = attributes || '{"wp308_type": "typeval38b"}' WHERE id = $1`, w.alpha)
	reindexCI(w.alpha)
	reindexCI(w.beta)
	w.expect(t, "indexer: new management IP", w.open(t, "10.38.0.6"), []string{w.alpha}, nil)
	w.expect(t, "indexer: old management IP", w.open(t, "10.38.0.5"), nil, []string{w.alpha})
	w.expect(t, "indexer: new attribute", w.open(t, "typeval38b"), []string{w.alpha}, nil)
	w.expect(t, "indexer: inventory number of the asset", w.open(t, "INV-38-B"), []string{w.beta}, nil)

	// Interfaces and addresses.
	var ifc string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name, mac) VALUES ($1, $2, 'eth38b', '08:00:2b:38:bb:01')
		RETURNING id::text`, f.OrgA, w.beta).Scan(&ifc); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `INSERT INTO ip_address (organization_id, interface_id, address) VALUES ($1, $2, '192.168.38.20')`, f.OrgA, ifc)
	w.expect(t, "new interface", w.open(t, "08:00:2b:38:bb:01"), []string{w.beta}, nil)
	w.expect(t, "new address", w.open(t, "192.168.38.20"), []string{w.beta}, nil)
	w.exec(t, `UPDATE network_interface SET mac = '08:00:2b:38:bb:02' WHERE id = $1`, ifc)
	w.expect(t, "new MAC", w.open(t, "08:00:2b:38:bb:02"), []string{w.beta}, nil)
	w.expect(t, "old MAC", w.open(t, "08:00:2b:38:bb:01"), nil, []string{w.beta})
	w.exec(t, `UPDATE network_interface SET ci_id = $2 WHERE id = $1`, ifc, w.gamma)
	w.expect(t, "moved interface", w.open(t, "192.168.38.20"), []string{w.gamma}, []string{w.beta})

	// Discovery: an unchanged second cycle changes no identifier row,
	// missAndRemove removes them.
	ipamRepo := ipam.NewPGRepository(f.App)
	observed := []ipam.ObservedInterface{{Name: "eth38s", MAC: "08:00:2b:38:cc:01", IPs: []string{"192.168.38.30"}}}
	if _, err := ipamRepo.SyncDiscoveredInterfaces(orgCtx, f.OrgA, w.gamma, observed); err != nil {
		t.Fatal(err)
	}
	w.expect(t, "discovered MAC", w.open(t, "08:00:2b:38:cc:01"), []string{w.gamma}, nil)
	w.expect(t, "discovered address", w.open(t, "192.168.38.30"), []string{w.gamma}, nil)
	versions := func() []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text || ':' || xmin::text FROM search_identifier WHERE organization_id = $1 ORDER BY 1`, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := versions()
	if _, err := ipamRepo.SyncDiscoveredInterfaces(orgCtx, f.OrgA, w.gamma, observed); err != nil {
		t.Fatal(err)
	}
	if after := versions(); !reflect.DeepEqual(before, after) {
		t.Errorf("an unchanged second sync changed search_identifier:\n%v\n%v", before, after)
	}
	for range 3 {
		if _, err := ipamRepo.SyncDiscoveredInterfaces(orgCtx, f.OrgA, w.gamma, []ipam.ObservedInterface{{Name: "eth38t"}}); err != nil {
			t.Fatal(err)
		}
	}
	w.expect(t, "removed MAC", w.open(t, "08:00:2b:38:cc:01"), nil, []string{w.gamma})
	w.expect(t, "removed address", w.open(t, "192.168.38.30"), nil, []string{w.gamma})

	// ReindexTenant writes the rows of the running maintenance.
	liveIdent, liveFields := identifierRows(t, f.Admin, f.OrgA), fieldRows(t, f.Admin, f.OrgA)
	if _, err := w.repo.ReindexTenant(orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if got := identifierRows(t, f.Admin, f.OrgA); !reflect.DeepEqual(got, liveIdent) {
		t.Errorf("identifier rows: reindex\n%s\nrunning maintenance\n%s", strings.Join(got, "\n"), strings.Join(liveIdent, "\n"))
	}
	if got := fieldRows(t, f.Admin, f.OrgA); !reflect.DeepEqual(got, liveFields) {
		t.Errorf("field rows: reindex\n%s\nrunning maintenance\n%s", strings.Join(got, "\n"), strings.Join(liveFields, "\n"))
	}
	for _, field := range []string{"core.management_ip", "core.primary_mac", "asset.inventory_no", "network.ip", "network.mac", "core.inventory_no"} {
		if !slices.ContainsFunc(liveIdent, func(r string) bool { return strings.Contains(r, "|"+field+"|") }) {
			t.Errorf("no identifier row of %s: %v", field, liveIdent)
		}
	}

	// The backfill of migration 000233 writes the same identifier rows.
	up, err := os.ReadFile("../../migrations/000233_search_fields.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	backfill := string(up[strings.LastIndex(string(up), "INSERT INTO search_identifier"):])
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, `DELETE FROM search_identifier`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, backfill); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := identifierRows(t, tx, f.OrgA); !reflect.DeepEqual(got, liveIdent) {
		t.Errorf("identifier rows: backfill\n%s\nreindex\n%s", strings.Join(got, "\n"), strings.Join(liveIdent, "\n"))
	}
	if err = tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}

	// Deleting the asset removes its document and its inventory number at
	// the CIs; Delete keeps the network rows of a CI.
	w.exec(t, `DELETE FROM asset WHERE id = $1`, w.asset)
	w.expect(t, "deleted asset", w.open(t, "INV-38-B"), nil, []string{w.asset, w.beta, w.gamma})
	if err = w.repo.Delete(orgCtx, f.OrgA, "ci", w.alpha); err != nil {
		t.Fatal(err)
	}
	var left []string
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(field ORDER BY field), '{}') FROM search_identifier WHERE entity_id = $1`, w.alpha).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, []string{"network.ip", "network.mac"}) {
		t.Errorf("identifier rows after Delete: %v", left)
	}
	w.expect(t, "deleted document", w.open(t, "192.168.38.10"), nil, []string{w.alpha})
}

// TestSearchFieldsPlanUnderRLS covers the index use of WP-308 (SR-04,
// E-133): at 3,000 CIs with management IP and primary MAC, the MAC, IP,
// CIDR and prefix search of GET /api/v1/search read search_identifier as
// reticora_app through idx_search_identifier_norm or
// idx_search_identifier_addr, never by a sequential scan, while the
// full-text match stays the documented filter over the documents of the
// organization. IndexDocuments sends as many statements for one document as
// for twenty (NFR-03).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchFieldsPlanUnderRLS(t *testing.T) {
	f := scopetest.Seed(t, "3a")
	bg := context.Background()
	const objects = 3000
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, management_ip, primary_mac)
		SELECT $1, `+server+`, 'wp308 plan ' || i, 'active', ('10.40.' || (i / 250) || '.' || (i % 250))::inet,
			('08:00:2b:' || lpad(to_hex(i / 65536), 2, '0') || ':' || lpad(to_hex((i / 256) % 256), 2, '0') || ':' || lpad(to_hex(i % 256), 2, '0'))::macaddr
		FROM generate_series(1, $2::int) AS i`, f.OrgA, objects); err != nil {
		t.Fatalf("seed CIs: %v", err)
	}
	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE search_identifier, search_document, search_document_field, ci`); err != nil {
		t.Fatal(err)
	}

	cfg := f.App.Config()
	log := &sentLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	traced, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	mux := chi.NewRouter()
	search.NewHandler(search.NewPGRepository(traced), permissionStub{granted: map[string]bool{"ci:read": true, "ipam:read": true}}).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, f.User)
	for _, c := range []struct {
		q, index string
		hits     int
	}{
		{"08:00:2b:00:04:d2", "idx_search_identifier_norm", 1},
		{"0800.2b00.04d2", "idx_search_identifier_norm", 1},
		{"10.40.4.234", "idx_search_identifier_addr", 1},
		{"10.40.4.0/24", "idx_search_identifier_addr", 250},
		{"10.40.4", "idx_search_identifier_norm", 250},
		{"08:00:2b:00:04", "idx_search_identifier_norm", 256},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+url.Values{"q": {c.q}, "limit": {"100"}}.Encode(), nil)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		ctx = permission.WithPolicy(database.ContextWithTenantScope(ctx, &scope), permission.UnrestrictedPolicy(frPerms))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.q, rec.Code, rec.Body.String())
		}
		stmt, ok := log.last(" FROM search_document WHERE ")
		if !ok {
			t.Fatalf("%s: no search statement", c.q)
		}
		var plan []byte
		var total int64
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, stmt.sql, stmt.args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			if rows.Next() {
				values, verr := rows.Values()
				if verr != nil {
					return verr
				}
				total, _ = values[len(values)-1].(int64)
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		nodes := searchPlanNodes(t, plan)
		uses, seq := false, false
		for _, n := range nodes {
			if n["Index Name"] == c.index {
				uses = true
			}
			if n["Relation Name"] == "search_identifier" && n["Node Type"] == "Seq Scan" {
				seq = true
			}
		}
		if !uses || seq {
			t.Errorf("%s: the plan does not read search_identifier through %s (sequential scan: %v):\n%s\n%s", c.q, c.index, seq,
				describeSearchPlan(nodes), stmt.sql)
		}
		if problem := ftsScanProblem(nodes); problem != "" {
			t.Errorf("%s: %s\n%s", c.q, problem, describeSearchPlan(nodes))
		}
		if total != int64(c.hits) {
			t.Errorf("%s: %d hits, want %d", c.q, total, c.hits)
		}
	}

	// The batch path sends a fixed number of statements.
	tracedRepo := search.NewPGRepository(traced)
	docs := func(n int) []search.Document {
		out := make([]search.Document, n)
		for i := range out {
			out[i] = search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: f.ID(), Title: fmt.Sprintf("batch %d", i),
				Fields: map[string]string{"core.hostname": "h", "core.management_ip": fmt.Sprintf("10.41.0.%d", i+1),
					"core.primary_mac": fmt.Sprintf("08:00:2b:41:00:%02x", i+1), "attributes.x": "y"}}
		}
		return out
	}
	sent := func(batch []search.Document) int {
		t.Helper()
		log.mu.Lock()
		start := len(log.stmt)
		log.mu.Unlock()
		if err := tracedRepo.IndexDocuments(f.OrgCtx(f.OrgA), batch); err != nil {
			t.Fatal(err)
		}
		log.mu.Lock()
		defer log.mu.Unlock()
		return len(log.stmt) - start
	}
	if one, twenty := sent(docs(1)), sent(docs(20)); one != twenty {
		t.Errorf("IndexDocuments: %d statements for one document, %d for twenty", one, twenty)
	}
}
