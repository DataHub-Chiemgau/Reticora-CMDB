package discovery

import (
	"context"
	"fmt"
	"sort"
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

	// ValueConflicts lists fields where the incoming item carries a different
	// non-empty identity value than the matched CI (e.g. a changed serial
	// number). Callers queue these for operator review instead of silently
	// overwriting (spec §5.3: Konfliktlösung + Review-Queue).
	ValueConflicts []string `json:"value_conflicts,omitempty"`
}

// Reconcile matches an incoming discovery item against existing CIs using the
// configured identity priority order (spec §5.3):
//
//  1. Seriennummer
//  2. Chassis-/Hardware-UUID
//  3. MAC-Adresse(n)
//  4. Management-IP + sysObjectID/CI-Typ
//  5. Hostname/FQDN
//
// On a match, ValueConflicts reports identity fields where the incoming item
// contradicts the stored CI; whether an incoming value is written is decided
// per field by override.DecideAutomatedWrite (REC-03).
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
				return normalizedEqual(existingFingerprintValue(item, "hardware_uuid"), fingerprintString(incoming.Fingerprint["hardware_uuid"])) ||
					normalizedEqual(item.HardwareUUID, incoming.HardwareUUID)
			},
		},
		{
			name: "mac_addresses",
			match: func(item ci.Item) bool {
				return macAddressOverlap(existingFingerprintSlice(item, "mac_addresses"), fingerprintSlice(incoming.Fingerprint["mac_addresses"])) ||
					normalizedMACEqual(item.PrimaryMAC, incoming.PrimaryMAC)
			},
		},
		{
			name: "management_ip_ci_type",
			match: func(item ci.Item) bool {
				return normalizedEqual(item.ManagementIP, incoming.ManagementIP) && normalizedEqual(item.CITypeID, incoming.CITypeName)
			},
		},
		{
			name: "hostname_fqdn",
			match: func(item ci.Item) bool {
				if normalizedEqual(item.FQDN, incoming.FQDN) {
					return true
				}
				if normalizedEqual(item.Hostname, incoming.Hostname) {
					return true
				}
				// Backwards compatibility: the item name carries the hostname
				// for sweeps and SSH collections.
				return normalizedEqual(item.Name, incoming.Name)
			},
		},
	}

	for _, criterion := range criteria {
		matches := matchingItems(existing, criterion.match)
		if len(matches) == 1 {
			result := ReconcileResult{Action: ReconcileMatched, MatchedCIID: matches[0].ID, Criterion: criterion.name}
			result.ValueConflicts = identityConflicts(matches[0], incoming)
			return result
		}
		if len(matches) > 1 {
			return ReconcileResult{Action: ReconcileConflict, CandidateCIIDs: collectIDs(matches), Criterion: criterion.name}
		}
	}

	return ReconcileResult{Action: ReconcileCreated}
}

// identityConflicts compares the strong identity fields of a matched CI with
// the incoming item and lists the fields that carry differing non-empty
// values. These are candidates for the review queue rather than silent
// overwrites, because a changed serial number or hardware UUID on an existing
// identity usually means replaced hardware or a mis-keyed fingerprint.
func identityConflicts(item ci.Item, incoming IngestItem) []string {
	var conflicts []string
	addIfDiffering := func(field, existing, incomingValue string) {
		if !normalizedEqual(existing, incomingValue) && strings.TrimSpace(existing) != "" && strings.TrimSpace(incomingValue) != "" {
			conflicts = append(conflicts, field)
		}
	}
	addIfDiffering("serial_number", item.SerialNumber, incoming.SerialNumber)
	if existingUUID := existingFingerprintValue(item, "hardware_uuid"); existingUUID != "" {
		addIfDiffering("hardware_uuid", existingUUID, fingerprintString(incoming.Fingerprint["hardware_uuid"]))
	} else {
		addIfDiffering("hardware_uuid", item.HardwareUUID, incoming.HardwareUUID)
	}
	addIfDiffering("management_ip", item.ManagementIP, incoming.ManagementIP)
	addIfDiffering("hostname", item.Hostname, incoming.Hostname)
	addIfDiffering("fqdn", item.FQDN, incoming.FQDN)
	return conflicts
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

// normalizedMACEqual compares two MAC addresses case-insensitively, ignoring
// the usual separator variations (":" vs "-" vs raw hex).
func normalizedMACEqual(left, right string) bool {
	return normalizeMACValue(left) != "" && normalizeMACValue(left) == normalizeMACValue(right)
}

func normalizeMACValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(":", "", "-", "", ".", "").Replace(value)
	if len(value) != 12 {
		return ""
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return value
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
	case nil:
		// A missing fingerprint key must not become the string "<nil>" —
		// otherwise two items that both lack hardware_uuid compare equal and
		// the identity resolution collapses them into one CI.
		return ""
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

// InstanceFieldSource lists the names of the instance attributes defined for
// a single CI (MET-14). Satisfied by the CI type repository.
type InstanceFieldSource interface {
	InstanceFieldNames(ctx context.Context, orgID, ciID string) ([]string, error)
}

// WithoutInstanceFields removes from attributes every attribute defined as an
// instance attribute of the CI and returns the removed names, sorted.
// Instance attributes are maintained by hand for this one CI; discovery and
// ingest never write them (MET-14).
func WithoutInstanceFields(attributes map[string]any, instance []string) []string {
	var removed []string
	for _, name := range instance {
		if _, ok := attributes[name]; ok {
			delete(attributes, name)
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed
}
