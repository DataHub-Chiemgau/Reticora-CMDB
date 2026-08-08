package compliance

import (
	"context"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"testing"
)

func TestEvaluatorScoresFailures(t *testing.T) {
	repo := NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	_ = cis.Create(context.Background(), &ci.Item{OrganizationID: "org-1", Name: "srv", CITypeID: "type-server", Status: "active", Attributes: map[string]any{"encrypted": false}})
	rule := &Rule{OrganizationID: "org-1", CITypeID: "type-server", Name: "encrypted", Severity: "high", Category: "ISO27001", Expression: JSONMap{"field": "attributes.encrypted", "op": "eq", "value": true}, RemediationHint: "Enable encryption", Active: true}
	_ = repo.CreateRule(context.Background(), rule)
	resp, err := NewEvaluator(repo, cis).Evaluate(context.Background(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Overall.Failed != 1 || resp.Overall.Score != 0 {
		t.Fatalf("unexpected score %#v", resp.Overall)
	}
}
