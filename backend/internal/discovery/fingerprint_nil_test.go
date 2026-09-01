package discovery

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// Regression: two items that both lack hardware_uuid must NOT match on that
// criterion. Before the fix, fingerprintString(nil) returned "<nil>", so two
// empty fingerprints compared equal and distinct devices collapsed into one CI.
func TestReconcileDoesNotMatchOnMissingHardwareUUID(t *testing.T) {
	existing := []ci.Item{{ID: "ci-pdu", CITypeID: "pdu", SerialNumber: "SN-P", Attributes: map[string]any{"fingerprint": map[string]any{}}}}
	incoming := IngestItem{CITypeName: "server", SerialNumber: "SN-S", Fingerprint: map[string]any{}}
	result := Reconcile(existing, incoming)
	if result.Action != ReconcileCreated {
		t.Fatalf("expected ReconcileCreated, got %v (criterion %s)", result.Action, result.Criterion)
	}
}
