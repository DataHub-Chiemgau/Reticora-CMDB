package export

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// The asset export of POST /api/v1/assets/bulk (WP-782): columns and
// encoding of CSV and JSON for asset rows, the projection by the asset
// field policy and the validation of filters.entity.

// fakeAssetRows answers the assets it holds that ids name.
type fakeAssetRows struct {
	items []asset.Asset
	asked []string
}

func (f *fakeAssetRows) GetByIDs(_ context.Context, _ string, ids []string) ([]asset.Asset, error) {
	f.asked = ids
	var out []asset.Asset
	for i := range f.items {
		if slices.Contains(ids, f.items[i].ID) {
			out = append(out, f.items[i])
		}
	}
	return out, nil
}

const (
	arA = "00000000-0000-4000-8000-0000000a0001"
	arB = "00000000-0000-4000-8000-0000000a0002"
	arC = "00000000-0000-4000-8000-0000000a0003"
)

// assetRowsWorld holds three assets, delivered out of order: B and C were
// created at the same time (C sorts after B by id), A later.
func assetRowsWorld() *fakeAssetRows {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	return &fakeAssetRows{items: []asset.Asset{
		{ID: arA, OrganizationID: frOrg, ClientID: frClient1, AssetTag: "INV-A", Name: "=SUM(A1)", Category: "laptop",
			Status: "in_stock", SerialNumber: "SN-A", PurchaseCost: "1234.50", Currency: "EUR", Tags: []string{"blue", "red"},
			CustomFields: map[string]any{"secret": "s-a", "color": "grey"}, CreatedAt: t0.Add(time.Hour), UpdatedAt: t0, Version: 3},
		{ID: arC, OrganizationID: frOrg, ClientID: frClient1, AssetTag: "INV-C", Name: "Monitor", Category: "monitor",
			Status: "assigned", SerialNumber: "SN-C", Tags: []string{}, CustomFields: map[string]any{}, CreatedAt: t0, UpdatedAt: t0, Version: 1},
		{ID: arB, OrganizationID: frOrg, ClientID: frClient2, AssetTag: "INV-B", Name: "Dock", Category: "dock",
			Status: "in_stock", SerialNumber: "SN-B", PurchaseCost: "99.00", Tags: []string{}, CustomFields: map[string]any{},
			CreatedAt: t0, UpdatedAt: t0, Version: 2},
	}}
}

func TestAssetCSVColumnsAndEncoding(t *testing.T) {
	src := assetRowsWorld()
	var out bytes.Buffer
	n, manifest, err := RenderAssets(context.Background(), src, frOrg, "csv", []string{arA, arB, arC},
		permission.UnrestrictedPolicy(identity.AllPermissions()), &out)
	if err != nil || n != 3 {
		t.Fatalf("render: %d %v", n, err)
	}
	if !strings.HasPrefix(out.String(), utf8BOM) {
		t.Error("CSV without BOM")
	}
	records := readCSV(t, out.String())
	header := records[0]
	if !slices.Equal(header, manifest.Fields) || !slices.Equal(header, assetCSVColumns) {
		t.Fatalf("header %v, manifest %v", header, manifest.Fields)
	}
	for _, want := range []string{"id", "inventory_no", "name", "status", "purchase_price", "serial_number", "tags", "version", "created_at"} {
		if !slices.Contains(header, want) {
			t.Errorf("header lacks %s: %v", want, header)
		}
	}
	for _, absent := range []string{"custom_fields", "primary_ci", "active_reservation", "asset_type_id", "supplier"} {
		if slices.Contains(header, absent) {
			t.Errorf("header carries %s", absent)
		}
	}
	if !slices.Equal(manifest.Omitted, []OmittedField{{Field: "custom_fields", Reason: ReasonLayout}}) {
		t.Errorf("omitted %+v, want custom_fields for layout", manifest.Omitted)
	}
	if !strings.Contains(strings.SplitN(out.String(), "\n", 2)[0], ";") {
		t.Error("header not separated by semicolons")
	}
	rows := rowsByName(t, records, "id")
	var order []string
	for _, rec := range records[1:] {
		order = append(order, rec[slices.Index(header, "id")])
	}
	if !slices.Equal(order, []string{arB, arC, arA}) {
		t.Errorf("order %v, want created_at then id", order)
	}
	a := rows[arA]
	if a["name"] != "'=SUM(A1)" || a["tags"] != "blue|red" || a["purchase_price"] != "1234.50" || a["version"] != "3" ||
		a["inventory_no"] != "INV-A" || a["created_at"] != "2026-03-01T09:00:00Z" || a["primary_ci_id"] != "" {
		t.Errorf("row A %v", a)
	}
}

func TestAssetJSONWritesTheDTO(t *testing.T) {
	src := assetRowsWorld()
	var out bytes.Buffer
	n, manifest, err := RenderAssets(context.Background(), src, frOrg, "json", []string{arA, arB},
		permission.UnrestrictedPolicy(identity.AllPermissions()), &out)
	if err != nil || n != 2 {
		t.Fatalf("render: %d %v", n, err)
	}
	if !slices.Contains(manifest.Fields, "custom_fields") || len(manifest.Omitted) != 0 {
		t.Errorf("manifest %+v", manifest)
	}
	var body struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", out.String(), err)
	}
	if body.Total != 2 || len(body.Data) != 2 || body.Data[0]["id"] != arB || body.Data[1]["id"] != arA {
		t.Fatalf("body %s", out.String())
	}
	got := body.Data[1]
	if got["inventory_no"] != "INV-A" || got["purchase_price"] != "1234.50" || got["custom_fields"].(map[string]any)["secret"] != "s-a" {
		t.Errorf("asset A %v", got)
	}
	if _, ok := got["primary_ci"]; ok {
		t.Errorf("asset A carries primary_ci: %v", got)
	}
}

// arPolicy closes purchase_cost for every asset type, makes the custom
// field secret sensitive and closes asset B by an object grant.
func arPolicy(t *testing.T) *permission.FieldPolicy {
	t.Helper()
	bg := context.Background()
	grants := permission.NewMemoryFieldGrants()
	if _, err := grants.CreateFieldGrant(bg, frOrg, &permission.FieldGrant{RoleID: ptrTo(frRole), ObjectKind: permission.KindAsset,
		FieldKey: ptrTo("core.purchase_cost"), Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.CreateSensitiveField(bg, frOrg, &permission.SensitiveField{ObjectKind: permission.KindAsset,
		FieldKey: "attributes.secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.CreateObjectGrant(bg, frOrg, &permission.ObjectGrant{GranteeRoleID: ptrTo(frRole),
		ObjectKind: permission.KindAsset, ObjectID: arB, Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	resolver := permission.NewFieldResolver(&frAccess{grants: []identity.Grant{frGrant(frRole)}}, nil, grants, nil)
	policy, err := resolver.Resolve(bg, identity.Principal{Subject: frUser, OrganizationID: frOrg,
		Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestAssetExportFieldPermissions(t *testing.T) {
	policy := arPolicy(t)
	permOmitted := OmittedField{Field: "purchase_price", Reason: ReasonFieldPermission}

	var csvOut bytes.Buffer
	_, csvManifest, err := RenderAssets(context.Background(), assetRowsWorld(), frOrg, "csv", []string{arA, arB}, policy, &csvOut)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(csvManifest.Fields, "purchase_price") || !slices.Contains(csvManifest.Omitted, permOmitted) {
		t.Errorf("csv manifest %+v, want purchase_price omitted for field_permission", csvManifest)
	}
	if strings.Contains(csvOut.String(), "1234.50") {
		t.Errorf("csv carries the closed purchase price: %s", csvOut.String())
	}
	if got := AssetManifestForPolicy("csv", policy); !slices.Equal(got.Fields, csvManifest.Fields) {
		t.Errorf("preview %v, file %v", got.Fields, csvManifest.Fields)
	}
	rows := rowsByName(t, readCSV(t, csvOut.String()), "id")
	if rows[arA]["serial_number"] != "SN-A" || rows[arB]["serial_number"] != "" || rows[arB]["name"] != "Dock" {
		t.Errorf("rows %v: serial number of A readable, of B (object grant none) empty, name structural", rows)
	}

	var jsonOut bytes.Buffer
	_, jsonManifest, err := RenderAssets(context.Background(), assetRowsWorld(), frOrg, "json", []string{arA, arB}, policy, &jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(jsonManifest.Omitted, permOmitted) ||
		!slices.Contains(jsonManifest.Omitted, OmittedField{Field: "custom_fields.secret", Reason: ReasonFieldPermission}) {
		t.Errorf("json manifest %+v, want purchase_price and custom_fields.secret omitted", jsonManifest)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Data {
		if _, ok := item["purchase_price"]; ok {
			t.Errorf("json asset %v carries purchase_price", item["id"])
		}
		if custom, ok := item["custom_fields"].(map[string]any); ok {
			if _, secret := custom["secret"]; secret {
				t.Errorf("json asset %v carries custom_fields.secret", item["id"])
			}
		}
	}
}

// A nil policy (no resolver) reads only the structural members.
func TestAssetExportWithoutPolicyIsClosed(t *testing.T) {
	var out bytes.Buffer
	_, manifest, err := RenderAssets(context.Background(), assetRowsWorld(), frOrg, "csv", []string{arA}, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range manifest.Fields {
		if _, structural, ok := permission.FieldKeyOf(permission.KindAsset, f); !ok || !structural {
			t.Errorf("field %s without policy", f)
		}
	}
	if strings.Contains(out.String(), "SN-A") {
		t.Errorf("nil policy exports the serial number: %s", out.String())
	}
}

func TestAssetEntityValidation(t *testing.T) {
	ids := []string{strings.ToUpper(arA), arA}
	ok := CreateJobRequest{Format: "csv", Filters: JobFilters{Entity: EntityAsset, IDs: ids}}
	if err := ok.Validate(); err != nil || !slices.Equal(ok.Filters.IDs, []string{arA}) {
		t.Fatalf("asset csv: %v %v", err, ok.Filters.IDs)
	}
	for name, req := range map[string]CreateJobRequest{
		"datev":          {Format: "datev", Filters: JobFilters{Entity: EntityAsset, IDs: []string{arA}}},
		"without ids":    {Format: "json", Filters: JobFilters{Entity: EntityAsset}},
		"with client":    {Format: "csv", Filters: JobFilters{Entity: EntityAsset, ClientID: frClient1}},
		"unknown entity": {Format: "csv", Filters: JobFilters{Entity: "ticket"}},
		"malformed id":   {Format: "csv", Filters: JobFilters{Entity: EntityAsset, IDs: []string{"x"}}},
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s: valid, want an error", name)
		}
	}
	ci := CreateJobRequest{Format: "csv", Filters: JobFilters{Entity: EntityCI, Status: "active"}}
	if err := ci.Validate(); err != nil {
		t.Errorf("entity ci: %v", err)
	}
}
