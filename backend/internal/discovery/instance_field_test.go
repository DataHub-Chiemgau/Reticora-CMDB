package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// stubInstanceFields returns the instance attribute names per CI or an error.
type stubInstanceFields struct {
	names map[string][]string
	err   error
}

func (s stubInstanceFields) InstanceFieldNames(_ context.Context, _, ciID string) ([]string, error) {
	return s.names[ciID], s.err
}

// instanceFieldCI creates a CI matched by serial SN-1 with manually
// maintained attributes.
func instanceFieldCI(t *testing.T, ciRepo *ci.MemoryRepository) *ci.Item {
	t.Helper()
	existing := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "srv-01", SerialNumber: "SN-1",
		Attributes: map[string]any{"rack_label": "manual R7", "vlan": "10"}}
	if err := ciRepo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	return existing
}

const instanceFieldPayload = `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv-01","serial_number":"SN-1",
	"attributes":{"rack_label":"discovered R9","vlan":"20"}}]}`

// TestIngestNeverWritesInstanceFields covers WP-056 (MET-14): a discovered
// value for an attribute defined as instance attribute of the CI is not
// written, other attributes are, and the response counts the CI.
func TestIngestNeverWritesInstanceFields(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	existing := instanceFieldCI(t, ciRepo)
	prov := &stubProvenance{protected: map[string]bool{}}
	h := NewHandler(discoveryRepo, ciRepo).WithProvenance(prov).
		WithInstanceFields(stubInstanceFields{names: map[string][]string{existing.ID: {"rack_label"}}})

	resp := ingest(t, h, instanceFieldPayload)
	if resp.Updated != 1 || resp.ProtectedInstanceFields != 1 {
		t.Fatalf("response %+v, want 1 updated and 1 protected instance field CI", resp)
	}
	got, err := ciRepo.GetByID(context.Background(), "org-1", existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["rack_label"] != "manual R7" {
		t.Errorf("instance attribute overwritten by discovery: %v", got.Attributes["rack_label"])
	}
	if got.Attributes["vlan"] != "20" {
		t.Errorf("ordinary attribute not updated: %v", got.Attributes["vlan"])
	}
	// The discovered value stays visible as provenance.
	if prov.recorded[existing.ID+"/rack_label"] != "discovered R9" {
		t.Errorf("provenance of the instance attribute: %v", prov.recorded[existing.ID+"/rack_label"])
	}
}

// TestIngestWithholdsAttributesWithoutDefinitions: when the instance
// definitions cannot be read, ingest fails closed for custom attributes and
// still updates the core fields.
func TestIngestWithholdsAttributesWithoutDefinitions(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	existing := instanceFieldCI(t, ciRepo)
	h := NewHandler(discoveryRepo, ciRepo).WithInstanceFields(stubInstanceFields{err: errors.New("database down")})

	resp := ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv-01","serial_number":"SN-1","manufacturer":"Acme",
		"attributes":{"rack_label":"discovered R9","vlan":"20"}}]}`)
	if resp.Updated != 1 || resp.ProtectedInstanceFields != 1 {
		t.Fatalf("response %+v", resp)
	}
	got, err := ciRepo.GetByID(context.Background(), "org-1", existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["rack_label"] != "manual R7" || got.Attributes["vlan"] != "10" {
		t.Errorf("custom attributes written without definitions: %v", got.Attributes)
	}
	if got.Manufacturer != "Acme" {
		t.Errorf("core field not updated: %q", got.Manufacturer)
	}
}

func TestWithoutInstanceFields(t *testing.T) {
	attrs := map[string]any{"a": 1, "b": 2, "fingerprint": "x"}
	removed := WithoutInstanceFields(attrs, []string{"b", "missing", "a"})
	if !reflect.DeepEqual(removed, []string{"a", "b"}) || !reflect.DeepEqual(attrs, map[string]any{"fingerprint": "x"}) {
		t.Errorf("removed %v, left %v", removed, attrs)
	}
}
