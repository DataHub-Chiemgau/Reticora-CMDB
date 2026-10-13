package discovery

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// fixedFields resolves the same definitions for every CI type.
type fixedFields []fieldmeta.FieldDefinition

func (f fixedFields) ResolveFields(context.Context, string, string, string) ([]fieldmeta.FieldDefinition, error) {
	return f, nil
}

// TestIngestValidatesObservedValues covers WP-117 (CI-03, MET-11):
// discovery writes pass the field rules of manual writes. Values the
// metadata rejects are dropped (typed columns and attributes), valid ones
// are stored, a missing required field does not block the observation.
func TestIngestValidatesObservedValues(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	svc := ci.NewService(ciRepo).WithFieldResolver(fixedFields{
		{Name: "hostname", DataType: fieldmeta.TypeText, Validation: &fieldmeta.ValidationRules{Pattern: `^[a-z0-9-]+$`}},
		{Name: "rack_unit", DataType: fieldmeta.TypeInteger},
		{Name: "owner", DataType: fieldmeta.TypeEmail, Required: true},
	})
	h := NewHandler(discoveryRepo, ciRepo).WithObservedValidator(svc)

	resp := ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv-01","serial_number":"SN-9",
		"hostname":"Bad Host","attributes":{"rack_unit":"high","vlan":"10"}}]}`)
	if resp.Created != 1 || resp.InvalidValues != 2 {
		t.Fatalf("response %+v, want 1 created and 2 invalid values", resp)
	}
	items, _, err := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 50})
	if err != nil || len(items) != 1 {
		t.Fatalf("items %d (%v)", len(items), err)
	}
	got := items[0]
	if got.Hostname != "" || got.Attributes["rack_unit"] != nil {
		t.Errorf("invalid values stored: hostname %q rack_unit %v", got.Hostname, got.Attributes["rack_unit"])
	}
	if got.Attributes["vlan"] != "10" || got.SerialNumber != "SN-9" {
		t.Errorf("valid values lost: %+v", got)
	}

	// A matched CI: an invalid update value is dropped, a valid one written.
	resp = ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv-01","serial_number":"SN-9",
		"attributes":{"rack_unit":"x"}}]}`)
	if resp.Updated != 1 || resp.InvalidValues != 1 {
		t.Fatalf("update response %+v", resp)
	}
	resp = ingest(t, h, `{"collector_id":"col-1","items":[{"ci_type_name":"server","name":"srv-01","serial_number":"SN-9",
		"attributes":{"rack_unit":12}}]}`)
	if resp.Updated != 1 || resp.InvalidValues != 0 {
		t.Fatalf("update response %+v", resp)
	}
	updated, err := ciRepo.GetByID(context.Background(), "org-1", got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attributes["rack_unit"] != 12.0 {
		t.Errorf("valid update value not written: %v", updated.Attributes["rack_unit"])
	}
}
