package discovery

import (
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// ReconcileAction represents the reconciliation outcome.
type ReconcileAction string

const (
	ReconcileMatched  ReconcileAction = "matched"
	ReconcileCreated  ReconcileAction = "created"
	ReconcileConflict ReconcileAction = "conflict"
)

// ReconcileResult reports how an incoming discovery item should be handled.
type ReconcileResult struct {
	Action         ReconcileAction `json:"action"`
	MatchedCIID    string          `json:"matched_ci_id,omitempty"`
	CandidateCIIDs []string        `json:"candidate_ci_ids,omitempty"`
	Criterion      string          `json:"criterion,omitempty"`
}

// Reconcile matches an incoming discovery item against existing CIs using the
// configured identity priority order.
func Reconcile(existing []ci.Item, incoming IngestItem) ReconcileResult {
	criteria := []struct {
		name  string
		match func(ci.Item) bool
	}{
		{
			name: "serial_number",
			match: func(item ci.Item) bool {
				return normalizedEqual(item.SerialNumber, incoming.SerialNumber)
			},
		},
		{
			name: "hardware_uuid",
			match: func(item ci.Item) bool {
				return normalizedEqual(existingFingerprintValue(item, "hardware_uuid"), fingerprintString(incoming.Fingerprint["hardware_uuid"]))
			},
		},
		{
			name: "mac_addresses",
			match: func(item ci.Item) bool {
				return macAddressOverlap(existingFingerprintSlice(item, "mac_addresses"), fingerprintSlice(incoming.Fingerprint["mac_addresses"]))
			},
		},
		{
			name: "management_ip_ci_type",
			match: func(item ci.Item) bool {
				return normalizedEqual(item.ManagementIP, incoming.ManagementIP) && normalizedEqual(item.CITypeID, incoming.CITypeName)
			},
		},
		{
			name: "name",
			match: func(item ci.Item) bool {
				return normalizedEqual(item.Name, incoming.Name)
			},
		},
	}

	for _, criterion := range criteria {
		matches := matchingItems(existing, criterion.match)
		if len(matches) == 1 {
			return ReconcileResult{Action: ReconcileMatched, MatchedCIID: matches[0].ID, Criterion: criterion.name}
		}
		if len(matches) > 1 {
			return ReconcileResult{Action: ReconcileConflict, CandidateCIIDs: collectIDs(matches), Criterion: criterion.name}
		}
	}

	return ReconcileResult{Action: ReconcileCreated}
}

func matchingItems(existing []ci.Item, match func(ci.Item) bool) []ci.Item {
	var matches []ci.Item
	for _, item := range existing {
		if match(item) {
			matches = append(matches, item)
		}
	}
	return matches
}

func collectIDs(items []ci.Item) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func normalizedEqual(left, right string) bool {
	left = strings.TrimSpace(strings.ToLower(left))
	right = strings.TrimSpace(strings.ToLower(right))
	return left != "" && right != "" && left == right
}

func existingFingerprintValue(item ci.Item, key string) string {
	if item.Attributes == nil {
		return ""
	}
	if direct, ok := item.Attributes[key]; ok {
		return fingerprintString(direct)
	}
	if nested, ok := item.Attributes["fingerprint"]; ok {
		if fingerprintMap, ok := nested.(map[string]any); ok {
			return fingerprintString(fingerprintMap[key])
		}
	}
	return ""
}

func existingFingerprintSlice(item ci.Item, key string) []string {
	if item.Attributes == nil {
		return nil
	}
	if direct, ok := item.Attributes[key]; ok {
		return fingerprintSlice(direct)
	}
	if nested, ok := item.Attributes["fingerprint"]; ok {
		if fingerprintMap, ok := nested.(map[string]any); ok {
			return fingerprintSlice(fingerprintMap[key])
		}
	}
	return nil
}

func fingerprintString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func fingerprintSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return normalizeSlice(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			values = append(values, fingerprintString(item))
		}
		return normalizeSlice(values)
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return normalizeSlice(strings.Split(typed, ","))
	default:
		return nil
	}
}

func normalizeSlice(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(strings.ToLower(value))
		if normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func macAddressOverlap(existing, incoming []string) bool {
	if len(existing) == 0 || len(incoming) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	for _, value := range incoming {
		if _, ok := seen[value]; ok {
			return true
		}
	}
	return false
}
