package disposal_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

const (
	testOrg      = "00000000-0000-4000-8000-0000000000a1"
	testAsset    = "00000000-0000-4000-8000-0000000000b1"
	testSupplier = "00000000-0000-4000-8000-0000000000c1"
	testDocument = "00000000-0000-4000-8000-0000000000d1"
)

// post sends body to POST /api/v1/disposal-records of a handler on a
// MemoryRepository and returns the status and the problem field.
func post(t *testing.T, body map[string]any) (status int, field string, rec map[string]any) {
	t.Helper()
	h := disposal.NewHandler(disposal.NewMemoryRepository())
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/disposal-records", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: testOrg}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	field, _ = out["field"].(string)
	return w.Code, field, out
}

// base is a complete request for method: the common fields and every field
// of the method's row in disposal.RequiredFields.
func base(method string) map[string]any {
	b := map[string]any{
		"method": method, "asset_id": testAsset, "performed_at": "2026-11-02T10:00:00Z", "performed_by": "Erika Muster",
	}
	switch method {
	case "destruction", "physical_destruction":
		maps.Copy(b, map[string]any{"protection_class": 2, "security_level": "P-4", "data_carrier": "SSD SN-1",
			"supplier_id": testSupplier, "evidence_document_id": testDocument})
	case "secure_erasure":
		maps.Copy(b, map[string]any{"data_carrier": "HDD SN-2", "certificate_ref": "BSI CON.6 overwrite 3x",
			"evidence_document_id": testDocument})
	}
	return b
}

func without(b map[string]any, fields ...string) map[string]any {
	out := maps.Clone(b)
	for _, f := range fields {
		delete(out, f)
	}
	return out
}

func with(b map[string]any, kv map[string]any) map[string]any {
	out := maps.Clone(b)
	maps.Copy(out, kv)
	return out
}

// TestCreateRequiresFieldsPerMethod covers WP-751 (MGT-02, E-55): a request
// without a required field of its method is refused with 400 naming the
// field; the complete request of every method is accepted.
func TestCreateRequiresFieldsPerMethod(t *testing.T) {
	for _, method := range disposal.Methods {
		if status, field, _ := post(t, base(method)); status != http.StatusCreated {
			t.Errorf("%s: complete request = %d (field %q), want 201", method, status, field)
		}
		required := append([]string{}, disposal.CommonRequiredFields...)
		required = append(required, disposal.RequiredFields[method]...)
		for _, entry := range required {
			alternatives := strings.Split(entry, "|")
			status, field, _ := post(t, without(base(method), alternatives...))
			if status != http.StatusBadRequest || field != alternatives[0] {
				t.Errorf("%s without %s: %d field %q, want 400 naming %s", method, entry, status, field, alternatives[0])
			}
		}
	}
	// Only the common fields for reuse, recycling and return to vendor.
	for _, method := range []string{"reuse", "recycling", "return_to_vendor"} {
		if got := disposal.RequiredFields[method]; len(got) != 0 {
			t.Errorf("%s requires %v, want only the common fields", method, got)
		}
	}
	for _, c := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"unknown method", with(base("reuse"), map[string]any{"method": "burn"}), "method"},
		{"ci instead of asset", with(without(base("reuse"), "asset_id"), map[string]any{"ci_id": testAsset}), ""},
		{"blank performer", with(base("reuse"), map[string]any{"performed_by": "  "}), "performed_by"},
		{"performed_at not RFC3339", with(base("reuse"), map[string]any{"performed_at": "02.11.2026"}), "performed_at"},
		{"asset id not a UUID", with(base("reuse"), map[string]any{"asset_id": "asset-1"}), "asset_id"},
	} {
		status, field, _ := post(t, c.body)
		want := http.StatusBadRequest
		if c.field == "" {
			want = http.StatusCreated
		}
		if status != want || field != c.field {
			t.Errorf("%s: %d field %q, want %d field %q", c.name, status, field, want, c.field)
		}
	}
}

// TestCreateDestructionSupplierOrWitness: destruction and physical
// destruction need the service provider or a witness, either suffices.
func TestCreateDestructionSupplierOrWitness(t *testing.T) {
	for _, method := range []string{"destruction", "physical_destruction"} {
		witnessOnly := with(without(base(method), "supplier_id"), map[string]any{"witness": "Max Zeuge"})
		if status, field, rec := post(t, witnessOnly); status != http.StatusCreated || rec["witness"] != "Max Zeuge" {
			t.Errorf("%s with witness only: %d field %q witness %v, want 201", method, status, field, rec["witness"])
		}
		if status, field, _ := post(t, base(method)); status != http.StatusCreated {
			t.Errorf("%s with supplier only: %d field %q, want 201", method, status, field)
		}
		if status, field, _ := post(t, without(base(method), "supplier_id", "witness")); status != http.StatusBadRequest || field != "supplier_id" {
			t.Errorf("%s with neither: %d field %q, want 400 naming supplier_id", method, status, field)
		}
	}
	// Secure erasure: the procedure in certificate_ref or notes.
	notesOnly := with(without(base("secure_erasure"), "certificate_ref"), map[string]any{"notes": "ATA Secure Erase"})
	if status, field, _ := post(t, notesOnly); status != http.StatusCreated {
		t.Errorf("secure_erasure with the procedure in notes: %d field %q, want 201", status, field)
	}
}

// TestCreateValueRanges: protection class 1-3 and security level after
// DIN 66399 (^[POTEFH]-[1-7]$), otherwise 400 naming the field.
func TestCreateValueRanges(t *testing.T) {
	for _, c := range []struct {
		value any
		ok    bool
	}{{1, true}, {3, true}, {0, false}, {4, false}, {-1, false}} {
		status, field, _ := post(t, with(base("destruction"), map[string]any{"protection_class": c.value}))
		if c.ok != (status == http.StatusCreated) || (!c.ok && field != "protection_class") {
			t.Errorf("protection_class %v: %d field %q", c.value, status, field)
		}
	}
	for _, c := range []struct {
		value string
		ok    bool
	}{{"P-1", true}, {"H-7", true}, {"O-3", true}, {"P-0", false}, {"P-8", false}, {"X-4", false}, {"p-4", false}, {"P4", false}, {"P-4 ", false}} {
		status, field, _ := post(t, with(base("physical_destruction"), map[string]any{"security_level": c.value}))
		if c.ok != (status == http.StatusCreated) || (!c.ok && field != "security_level") {
			t.Errorf("security_level %q: %d field %q", c.value, status, field)
		}
	}
	// The ranges apply to every method that sends the fields.
	if status, field, _ := post(t, with(base("reuse"), map[string]any{"security_level": "Z-9"})); status != http.StatusBadRequest || field != "security_level" {
		t.Errorf("reuse with security_level Z-9: %d field %q, want 400", status, field)
	}
}

// TestRequiredFieldsMatchOpenAPI: the required-field table of the handler
// is the one api/openapi.yaml describes at CreateDisposalRecordRequest
// (x-required-fields-by-method, "*" for every method).
func TestRequiredFieldsMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Table map[string][]string `yaml:"x-required-fields-by-method"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	documented := spec.Components.Schemas["CreateDisposalRecordRequest"].Table
	want := map[string][]string{"*": disposal.CommonRequiredFields}
	maps.Copy(want, disposal.RequiredFields)
	if !maps.EqualFunc(documented, want, slices.Equal[[]string]) {
		t.Errorf("x-required-fields-by-method = %v, want the handler table %v", documented, want)
	}
	if got := slices.Sorted(maps.Keys(disposal.RequiredFields)); !slices.Equal(got, slices.Sorted(slices.Values(disposal.Methods))) {
		t.Errorf("RequiredFields covers %v, want every method %v", got, disposal.Methods)
	}
}
