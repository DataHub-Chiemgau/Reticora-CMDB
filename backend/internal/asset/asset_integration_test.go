package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// Retest (WP-271, AST-01, DB-05):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssetAST01' ./internal/asset/

const assetsPath = "/api/v1/assets"

// assetWorld mounts the asset routes over PostgreSQL.
type assetWorld struct {
	f   *scopetest.Fixture
	mux *chi.Mux
}

func newAssetWorld(t *testing.T) *assetWorld {
	t.Helper()
	f := scopetest.Seed(t, "2b")
	mux := chi.NewRouter()
	asset.NewHandler(asset.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &assetWorld{f: f, mux: mux}
}

// call is one request; ctx carries the tenant scope (organization A
// org-wide without one) and the field policy.
type call struct {
	ctx          context.Context
	method, path string
	body         string
	want         int
	wantField    string
}

func (w *assetWorld) do(t *testing.T, c *call) *httptest.ResponseRecorder {
	t.Helper()
	ctx := c.ctx
	if ctx == nil {
		ctx = w.f.OrgCtx(w.f.OrgA)
	}
	req := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(c.body))
	req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User}))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	if c.want != 0 && rec.Code != c.want {
		t.Fatalf("%s %s %s: got %d %s, want %d", c.method, c.path, c.body, rec.Code, rec.Body.String(), c.want)
	}
	if c.wantField != "" {
		var p api.FieldProblem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Field != c.wantField {
			t.Fatalf("%s %s %s: problem %s, want field %q", c.method, c.path, c.body, rec.Body.String(), c.wantField)
		}
	}
	return rec
}

func decodeAsset(t *testing.T, rec *httptest.ResponseRecorder) asset.Asset {
	t.Helper()
	var a asset.Asset
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode asset %s: %v", rec.Body.String(), err)
	}
	return a
}

func (w *assetWorld) create(t *testing.T, body string) asset.Asset {
	t.Helper()
	return decodeAsset(t, w.do(t, &call{method: http.MethodPost, path: assetsPath, body: body, want: http.StatusCreated}))
}

func (w *assetWorld) patch(t *testing.T, id, body string) asset.Asset {
	t.Helper()
	return decodeAsset(t, w.do(t, &call{method: http.MethodPatch, path: assetsPath + "/" + id, body: body, want: http.StatusOK}))
}

func (w *assetWorld) admin(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// TestAssetAST01UniquenessAndCurrency covers AST-01 (WP-271): a taken
// inventory number, barcode or RFID tag is 409 naming the field; a missing
// or empty currency is the currency of the organization on create, update
// and the internal create of the inventory conversion (assetCreator), an
// invalid one 400 on currency; a new asset starts in the initial state of
// physical_asset; location_id and lifecycle_state are read-only (422).
func TestAssetAST01UniquenessAndCurrency(t *testing.T) {
	w := newAssetWorld(t)
	w.admin(t, `UPDATE organization SET currency = 'CHF' WHERE id = $1`, w.f.OrgA)

	a := w.create(t, `{"asset_tag":"AST-1","name":"Laptop","barcode":"BC-1","rfid_tag":"RF-1","manufacturer":"Lenovo",
		"model":"T14","cost_center":"KST-4711"}`)
	if a.Currency != "CHF" || a.LifecycleState != "ordered" || a.Manufacturer != "Lenovo" || a.Model != "T14" ||
		a.CostCenter != "KST-4711" {
		t.Fatalf("created: %+v", a)
	}
	for _, dup := range []struct{ body, field string }{
		{`{"asset_tag":"AST-1","name":"x"}`, "asset_tag"},
		{`{"asset_tag":"AST-2","name":"x","barcode":"BC-1"}`, "barcode"},
		{`{"asset_tag":"AST-3","name":"x","rfid_tag":"RF-1"}`, "rfid_tag"},
	} {
		w.do(t, &call{method: http.MethodPost, path: assetsPath, body: dup.body, want: http.StatusConflict, wantField: dup.field})
	}
	other := w.create(t, `{"asset_tag":"AST-4","name":"Monitor","currency":"USD"}`)
	if other.Currency != "USD" {
		t.Fatalf("explicit currency: %+v", other)
	}
	path := assetsPath + "/" + other.ID
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"barcode":"BC-1"}`, want: http.StatusConflict, wantField: "barcode"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"rfid_tag":"RF-1"}`, want: http.StatusConflict, wantField: "rfid_tag"})
	if got := w.patch(t, other.ID, `{"currency":""}`); got.Currency != "CHF" {
		t.Fatalf("empty currency on update: %+v", got)
	}
	for _, bad := range []string{`{"asset_tag":"AST-5","name":"x","currency":"EURO"}`, `{"asset_tag":"AST-5","name":"x","currency":"eur"}`} {
		w.do(t, &call{method: http.MethodPost, path: assetsPath, body: bad, want: http.StatusBadRequest, wantField: "currency"})
	}
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"currency":"XYZ"}`, want: http.StatusBadRequest, wantField: "currency"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"location_id":"` + w.f.ID() + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "location_id"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"lifecycle_state":null}`,
		want: http.StatusUnprocessableEntity, wantField: "lifecycle_state"})

	// The internal create of the inventory conversion (assetCreator in
	// server/report_adapters.go): no currency, the location in the column.
	ctx := w.f.OrgCtx(w.f.OrgA)
	site, err := locations.NewPGRepository(w.f.App).Create(ctx, w.f.OrgA, locations.CreateRequest{
		Kind: locations.KindSite, ClientID: w.f.Client1, Name: "wp271-site"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	converted := &asset.Asset{OrganizationID: w.f.OrgA, ClientID: w.f.Client1, AssetTag: "AST-CONV", Name: "converted",
		Category: "hardware", Status: "in_stock", LocationID: site.ID, CustomFields: map[string]any{}}
	if err = asset.NewPGRepository(w.f.App).Create(ctx, converted); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted.Currency != "CHF" || converted.LocationID != site.ID || converted.SiteID != site.ID ||
		converted.CustomFields["location_id"] != nil || converted.LifecycleState != "ordered" {
		t.Fatalf("converted asset: %+v", converted)
	}
}

// TestAssetAST01References covers the references of AST-01 (WP-271): a
// supplier or contract of another client and an owner team or user of
// another organization are 422 on the field; org-wide and own objects are
// accepted; deleting the owner team clears owner_team_id, deleting the
// owner user is refused (ErrUserReferenced). The CI shows the new fields
// read-only under asset (DB-05).
func TestAssetAST01References(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	ids := map[string]string{
		"supOrg": f.ID(), "supC2": f.ID(), "conC1": f.ID(), "conC2": f.ID(), "teamA": f.ID(), "teamB": f.ID(),
	}
	userB := f.AppUser(t, f.OrgB, "wp271-b")
	userA := f.AppUser(t, f.OrgA, "wp271-a")
	w.admin(t, `INSERT INTO supplier (id, organization_id, client_id, name) VALUES ($1, $3, NULL, 'Org-weit'), ($2, $3, $4, 'Client 2')`,
		ids["supOrg"], ids["supC2"], f.OrgA, f.Client2)
	w.admin(t, `INSERT INTO contract (id, organization_id, client_id, number, currency) VALUES ($1, $3, $4, 'C-1', 'EUR'), ($2, $3, $5, 'C-2', 'EUR')`,
		ids["conC1"], ids["conC2"], f.OrgA, f.Client1, f.Client2)
	w.admin(t, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp271-a'), ($3, $4, 'wp271-b')`,
		ids["teamA"], f.OrgA, ids["teamB"], f.OrgB)

	base := `"asset_tag":"REF-%s","name":"ref","client_id":"` + f.Client1 + `"`
	body := func(tag, extra string) string { return "{" + strings.Replace(base, "%s", tag, 1) + "," + extra + "}" }
	for _, bad := range []struct{ extra, field string }{
		{`"supplier_id":"` + ids["supC2"] + `"`, "supplier_id"},
		{`"supplier_id":"` + f.ID() + `"`, "supplier_id"},
		{`"supplier_id":"no-uuid"`, "supplier_id"},
		{`"contract_id":"` + ids["conC2"] + `"`, "contract_id"},
		{`"owner_team_id":"` + ids["teamB"] + `"`, "owner_team_id"},
		{`"owner_user_id":"` + userB + `"`, "owner_user_id"},
	} {
		w.do(t, &call{method: http.MethodPost, path: assetsPath, body: body("BAD", bad.extra),
			want: http.StatusUnprocessableEntity, wantField: bad.field})
	}
	ciID := f.CI(t, f.OrgA, f.Client1, "wp271-ci")
	a := w.create(t, body("OK", `"supplier_id":"`+ids["supOrg"]+`","contract_id":"`+ids["conC1"]+`","owner_team_id":"`+
		ids["teamA"]+`","owner_user_id":"`+userA+`","manufacturer":"Dell","model":"R650","cost_center":"KST-1","ci_id":"`+ciID+`"`))
	if a.SupplierID != ids["supOrg"] || a.ContractID != ids["conC1"] || a.OwnerTeamID != ids["teamA"] || a.OwnerUserID != userA {
		t.Fatalf("created with references: %+v", a)
	}
	path := assetsPath + "/" + a.ID
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"contract_id":"` + ids["conC2"] + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "contract_id"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"supplier_id":"` + ids["supC2"] + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "supplier_id"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"owner_team_id":"` + ids["teamB"] + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "owner_team_id"})

	// The CI shows the AST-01 fields of its asset read-only (DB-05).
	ctx := f.OrgCtx(f.OrgA)
	item, err := ci.NewPGRepository(f.App).GetByID(ctx, f.OrgA, ciID)
	if err != nil {
		t.Fatalf("ci: %v", err)
	}
	if facts := item.Asset; facts == nil || facts.Manufacturer != "Dell" || facts.Model != "R650" || facts.CostCenter != "KST-1" ||
		facts.SupplierID != ids["supOrg"] || facts.ContractID != ids["conC1"] || facts.OwnerTeamID != ids["teamA"] ||
		facts.OwnerUserID != userA {
		t.Fatalf("asset facts on the CI: %+v", item.Asset)
	}

	// Deleting the owner user is refused; deleting the owner team clears
	// the reference and keeps the asset in its organization.
	if err = user.NewPGRepository(f.App).DeleteUser(ctx, f.OrgA, userA); !errors.Is(err, user.ErrUserReferenced) {
		t.Fatalf("delete owner user: %v, want ErrUserReferenced", err)
	}
	w.admin(t, `DELETE FROM team WHERE id = $1`, ids["teamA"])
	var team, org string
	if err = f.Admin.QueryRow(context.Background(), `SELECT COALESCE(owner_team_id::text, ''), organization_id::text FROM asset WHERE id = $1`,
		a.ID).Scan(&team, &org); err != nil || team != "" || org != f.OrgA {
		t.Fatalf("after team delete: team %q org %q (%v)", team, org, err)
	}
}

// TestAssetAST01FieldRights covers Konvention 11 for assets (RBA-07,
// WP-271): a field without read grant (core.purchase_cost, a custom field)
// is left out of the detail, the list, the answer of POST and PATCH; a
// write of a field the principal may not edit is 403 with the fields, the
// unchanged value of a readable, read-only field passes.
func TestAssetAST01FieldRights(t *testing.T) {
	w := newAssetWorld(t)
	bg := context.Background()
	a := w.create(t, `{"asset_tag":"FR-1","name":"geheim","purchase_cost":4711,"notes":"lesbar",
		"custom_fields":{"secret":"x-geheim","public":"offen"}}`)
	path := assetsPath + "/" + a.ID

	role := w.f.ID()
	raw, _ := json.Marshal([]string{"asset:read", "asset:write"})
	w.admin(t, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp271-reader', $3::jsonb)`, role, w.f.OrgA, string(raw))
	for key, level := range map[string]string{"core.purchase_cost": "none", "core.notes": "read", "attributes.secret": "none"} {
		w.admin(t, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'asset', $3, $4)`,
			w.f.OrgA, role, key, level)
	}
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(w.f.App), nil).
		PolicyForGrants(bg, w.f.OrgA, []identity.Grant{{Permissions: []identity.Permission{"asset:read", "asset:write"}, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	reader := permission.WithPolicy(w.f.OrgCtx(w.f.OrgA), policy)
	as := func(method, p, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, &call{ctx: reader, method: method, path: p, body: body, want: want})
	}
	hidden := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		s := rec.Body.String()
		if strings.Contains(s, "4711") || strings.Contains(s, "x-geheim") || !strings.Contains(s, `"public":"offen"`) ||
			!strings.Contains(s, `"organization_id"`) || !strings.Contains(s, `"created_at"`) {
			t.Fatalf("not projected: %s", s)
		}
	}
	wantFields := func(rec *httptest.ResponseRecorder, field string) {
		t.Helper()
		var p api.FieldForbiddenProblem
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &p); decodeErr != nil || len(p.Fields) != 1 || p.Fields[0] != field {
			t.Fatalf("403 %s, want fields [%s]", rec.Body.String(), field)
		}
	}

	hidden(as(http.MethodGet, path, "", http.StatusOK))
	hidden(as(http.MethodGet, assetsPath, "", http.StatusOK))
	hidden(as(http.MethodPatch, path, `{"notes":"lesbar","name":"umbenannt"}`, http.StatusOK))
	wantFields(as(http.MethodPatch, path, `{"notes":"neu"}`, http.StatusForbidden), "notes")
	wantFields(as(http.MethodPatch, path, `{"purchase_cost":4711}`, http.StatusForbidden), "purchase_cost")
	wantFields(as(http.MethodPatch, path, `{"custom_fields":{"secret":"x-geheim"}}`, http.StatusForbidden), "custom_fields.secret")
	wantFields(as(http.MethodPost, assetsPath, `{"asset_tag":"FR-2","name":"x","purchase_cost":1}`, http.StatusForbidden), "purchase_cost")
	created := as(http.MethodPost, assetsPath, `{"asset_tag":"FR-3","name":"x","custom_fields":{"public":"offen"}}`, http.StatusCreated)
	if strings.Contains(created.Body.String(), "purchase_cost") {
		t.Fatalf("created answer: %s", created.Body.String())
	}
	// The full DTO for a principal without restriction.
	if full := w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK}); !strings.Contains(full.Body.String(), "x-geheim") {
		t.Fatalf("unrestricted detail: %s", full.Body.String())
	}
}

// TestAssetAST01Migration covers the data migration of 000229 (WP-271):
// barcode duplicates of an organization keep the value on the oldest row,
// the others go to migration_quarantine and are cleared, then
// idx_asset_barcode_org holds; suppliers come from the free text;
// lifecycle_state follows status, a lost asset stays without a state and
// is quarantined; the location moves from custom_fields to location_id;
// rows without a currency get the organization's. It reruns the migration
// in a transaction that is rolled back.
func TestAssetAST01Migration(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	bg := context.Background()
	site, err := locations.NewPGRepository(f.App).Create(f.OrgCtx(f.OrgA), f.OrgA, locations.CreateRequest{
		Kind: locations.KindSite, ClientID: f.Client1, Name: "wp271-mig"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		b, readErr := os.ReadFile("../../migrations/000229_asset_ast01." + name + ".sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		// 000231 (WP-738) builds on the columns of 000229: it goes down
		// first and up last.
		names, readErr := os.ReadFile("../../migrations/000231_asset_field_names." + name + ".sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if name == "down" {
			return string(names) + "\n" + string(b)
		}
		return string(b) + "\n" + string(names)
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(step, sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", step, execErr)
		}
	}
	exec("down", read("down"))
	if _, err = tx.Exec(bg, `UPDATE organization SET currency = 'SEK' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	old, newer, lost, placed := f.ID(), f.ID(), f.ID(), f.ID()
	exec("legacy rows", `INSERT INTO asset (id, organization_id, asset_tag, name, barcode, status, supplier, currency, created_at, custom_fields) VALUES
		($1, $5, 'MIG-1', 'old', 'DUP', 'assigned', ' ACME ', NULL, now() - interval '2 days', '{}'),
		($2, $5, 'MIG-2', 'newer', 'DUP', 'maintenance', 'ACME', '', now() - interval '1 day', '{}'),
		($3, $5, 'MIG-3', 'lost', NULL, 'lost', NULL, 'EUR', now(), '{}'),
		($4, $5, 'MIG-4', 'placed', NULL, 'in_stock', NULL, 'EUR', now(), jsonb_build_object('location_id', $6::text))`,
		old, newer, lost, placed, f.OrgA, site.ID)
	exec("up", read("up"))

	type row struct{ barcode, state, supplier, currency, location, site string }
	get := func(id string) row {
		t.Helper()
		var r row
		if scanErr := tx.QueryRow(bg, `SELECT COALESCE(barcode, ''), COALESCE(lifecycle_state, ''), COALESCE(supplier_id::text, ''),
			COALESCE(currency, ''), COALESCE(location_id::text, ''), COALESCE(site_id::text, '') FROM asset WHERE id = $1`, id).
			Scan(&r.barcode, &r.state, &r.supplier, &r.currency, &r.location, &r.site); scanErr != nil {
			t.Fatal(scanErr)
		}
		return r
	}
	var acme string
	if err = tx.QueryRow(bg, `SELECT id::text FROM supplier WHERE organization_id = $1 AND name = 'ACME' AND client_id IS NULL`, f.OrgA).Scan(&acme); err != nil {
		t.Fatalf("supplier from free text: %v", err)
	}
	if r := get(old); r.barcode != "DUP" || r.state != "deployed" || r.supplier != acme || r.currency != "SEK" {
		t.Errorf("oldest row: %+v", r)
	}
	if r := get(newer); r.barcode != "" || r.state != "repair" || r.supplier != acme || r.currency != "SEK" {
		t.Errorf("newer duplicate: %+v", r)
	}
	if r := get(lost); r.state != "" || r.currency != "EUR" {
		t.Errorf("lost asset: %+v", r)
	}
	if r := get(placed); r.location != site.ID || r.site != site.ID || r.state != "in_stock" {
		t.Errorf("placed asset: %+v", r)
	}
	var quarantined int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000229' AND organization_id = $1
		AND ((reason = 'duplicate_barcode' AND row_data ->> 'id' = $2 AND row_data ->> 'barcode' = 'DUP')
		  OR (reason LIKE 'asset without lifecycle_state%' AND row_data ->> 'id' = $3))`, f.OrgA, newer, lost).Scan(&quarantined); err != nil || quarantined != 2 {
		t.Errorf("quarantine rows: %d (%v), want 2", quarantined, err)
	}
	if _, err = tx.Exec(bg, `SAVEPOINT dup`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(bg, `UPDATE asset SET barcode = 'DUP' WHERE id = $1`, newer)
	if !strings.Contains(errString(err), "idx_asset_barcode_org") {
		t.Errorf("duplicate after migration: %v, want idx_asset_barcode_org", err)
	}
	if _, err = tx.Exec(bg, `ROLLBACK TO SAVEPOINT dup`); err != nil {
		t.Fatal(err)
	}
	// The down migration gives the quarantined barcode back.
	exec("down again", read("down"))
	var barcode string
	if err = tx.QueryRow(bg, `SELECT COALESCE(barcode, '') FROM asset WHERE id = $1`, newer).Scan(&barcode); err != nil || barcode != "DUP" {
		t.Errorf("barcode after down: %q (%v)", barcode, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
