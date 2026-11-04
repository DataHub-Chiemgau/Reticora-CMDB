package asset_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Retest (WP-739, AST-01, AST-04, E-122):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssetAPINames' ./internal/asset/

// TestAssetAPINames covers the AST-01 names of /api/v1/assets (WP-739):
// create and PATCH with inventory_no and warranty_until; purchase_price is
// a decimal string that comes back unchanged (no float rounding); the old
// names asset_tag, purchase_cost, warranty_end, supplier and ci_id are
// unknown members (400); primary_ci_id of an unknown CI is 422 on
// primary_ci_id; a taken inventory number is 409 on inventory_no;
// sort_by=inventory_no sorts by the inventory number.
func TestAssetAPINames(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f

	ciID := f.CI(t, f.OrgA, "", "wp739-ci")
	a := w.create(t, `{"inventory_no":"API-739-B","name":"api names","warranty_until":"2028-02-29",
		"purchase_price":"1234567890.12","primary_ci_id":"`+ciID+`"}`)
	if a.AssetTag != "API-739-B" || a.WarrantyEnd != "2028-02-29" || a.PurchaseCost != "1234567890.12" || a.CIID != ciID {
		t.Fatalf("created: %+v", a)
	}
	path := assetsPath + "/" + a.ID

	// The answer carries the AST-01 names only.
	rec := w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK})
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if body, ok := raw["asset"]; ok { // the detail with the linked CI
		raw = nil
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decode asset %s: %v", body, err)
		}
	}
	for _, name := range []string{"inventory_no", "purchase_price", "warranty_until", "primary_ci_id"} {
		if _, ok := raw[name]; !ok {
			t.Errorf("answer without %s: %s", name, rec.Body.String())
		}
	}
	for _, name := range []string{"asset_tag", "purchase_cost", "warranty_end", "supplier", "ci_id"} {
		if _, ok := raw[name]; ok {
			t.Errorf("answer with the old name %s: %s", name, rec.Body.String())
		}
	}
	if string(raw["purchase_price"]) != `"1234567890.12"` {
		t.Errorf("purchase_price %s, want the decimal string \"1234567890.12\"", raw["purchase_price"])
	}

	// PATCH with the new names; a price is stored with two decimals.
	got := w.patch(t, a.ID, `{"inventory_no":"API-739-B2","warranty_until":"2029-12-31","purchase_price":"999999999999.99"}`)
	if got.AssetTag != "API-739-B2" || got.WarrantyEnd != "2029-12-31" || got.PurchaseCost != "999999999999.99" {
		t.Fatalf("patched: %+v", got)
	}
	if got = w.patch(t, a.ID, `{"purchase_price":"12.5"}`); got.PurchaseCost != "12.50" {
		t.Fatalf("purchase_price 12.5: %q, want 12.50", got.PurchaseCost)
	}
	if got = w.patch(t, a.ID, `{"purchase_price":""}`); got.PurchaseCost != "" {
		t.Fatalf("empty purchase_price: %q, want none", got.PurchaseCost)
	}
	for _, bad := range []string{`"1234567890123"`, `"1.234"`, `"-1"`, `"1e3"`, `"x"`} {
		w.do(t, &call{method: http.MethodPatch, path: path, body: `{"purchase_price":` + bad + `}`,
			want: http.StatusBadRequest, wantField: "purchase_price"})
	}
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"purchase_price":12.5}`, want: http.StatusBadRequest})

	// The old names, ci_id included, are unknown members.
	for _, old := range []string{`"asset_tag":"API-739-X"`, `"purchase_cost":1`, `"warranty_end":"2030-01-01"`,
		`"supplier":"Freitext"`, `"ci_id":"` + ciID + `"`} {
		w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"inventory_no":"API-739-X","name":"x",` + old + `}`,
			want: http.StatusBadRequest})
		w.do(t, &call{method: http.MethodPatch, path: path, body: `{` + old + `}`, want: http.StatusBadRequest})
	}

	// An unknown CI is 422 on primary_ci_id, on create and on PATCH.
	unknown := f.ID()
	w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"inventory_no":"API-739-Y","name":"y","primary_ci_id":"` + unknown + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "primary_ci_id"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"primary_ci_id":"` + unknown + `"}`,
		want: http.StatusUnprocessableEntity, wantField: "primary_ci_id"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"primary_ci_id":"no-uuid"}`,
		want: http.StatusUnprocessableEntity, wantField: "primary_ci_id"})
	if got = w.patch(t, a.ID, `{"primary_ci_id":""}`); got.CIID != "" {
		t.Fatalf("unlinked: %+v", got)
	}

	// A taken inventory number is 409 on inventory_no.
	w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"inventory_no":"API-739-B2","name":"twin"}`,
		want: http.StatusConflict, wantField: "inventory_no"})
	w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"name":"no number"}`, want: http.StatusBadRequest})

	// sort_by=inventory_no sorts by the inventory number.
	w.create(t, `{"inventory_no":"API-739-A","name":"zz last by name"}`)
	w.create(t, `{"inventory_no":"API-739-C","name":"aa first by name"}`)
	list := w.do(t, &call{method: http.MethodGet, path: assetsPath + "?search=API-739&sort_by=inventory_no&sort_dir=asc", want: http.StatusOK})
	var page struct {
		Data []struct {
			InventoryNo string `json:"inventory_no"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list %s: %v", list.Body.String(), err)
	}
	nos := make([]string, 0, len(page.Data))
	for _, item := range page.Data {
		nos = append(nos, item.InventoryNo)
	}
	if !slices.Equal(nos, []string{"API-739-A", "API-739-B2", "API-739-C"}) {
		t.Fatalf("sort_by=inventory_no: %v", nos)
	}
	if strings.Contains(list.Body.String(), `"asset_tag"`) {
		t.Fatalf("list with the old name: %s", list.Body.String())
	}
}
