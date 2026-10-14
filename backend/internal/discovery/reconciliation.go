package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

// MaxFutureSkew is how far observed_at may lie after the arrival time before
// a record is rejected (GLO-12).
const MaxFutureSkew = 5 * time.Minute

var futureRecordsRejected atomic.Int64

// FutureRecordsRejected is the number of records rejected since start because
// their observed_at lay more than MaxFutureSkew in the future.
func FutureRecordsRejected() int64 { return futureRecordsRejected.Load() }

// admitObservedAt prepares the source times of an ingest (GLO-12): items
// without observed_at (the deprecated items contract) take the arrival time;
// items observed more than MaxFutureSkew after arrival are removed and
// counted. It returns the admitted items and the number removed.
func admitObservedAt(items []IngestItem, arrival time.Time) (admitted []IngestItem, rejected int) {
	admitted = items[:0]
	for i := range items {
		item := items[i]
		if item.ObservedAt.IsZero() {
			item.ObservedAt = arrival
		}
		if item.ObservedAt.After(arrival.Add(MaxFutureSkew)) {
			rejected++
			futureRecordsRejected.Add(1)
			slog.Warn("ingest record rejected: observed_at lies in the future",
				"event", "ingest.record.future", "record_id", item.RecordID,
				"observed_at", item.ObservedAt, "arrival", arrival)
			continue
		}
		admitted = append(admitted, item)
	}
	return admitted, rejected
}

// InterfaceSyncer applies the interfaces a record reports to its CI and
// removes those missed three times in a row (REC-07). The IPAM repository
// implements it.
type InterfaceSyncer interface {
	SyncDiscoveredInterfaces(ctx context.Context, orgID, ciID string, observed []ipam.ObservedInterface) (ipam.SyncResult, error)
}

// WithInterfaces attaches the interface synchronization of ingest.
func (h *Handler) WithInterfaces(syncer InterfaceSyncer) *Handler {
	h.interfaces = syncer
	return h
}

// syncInterfaces applies the interfaces of an item to its CI. Items that
// report no interfaces leave them untouched: only an observation that lists
// interfaces can miss one.
func (h *Handler) syncInterfaces(ctx context.Context, orgID, ciID string, item *IngestItem) error {
	if h.interfaces == nil || ciID == "" || len(item.Interfaces) == 0 {
		return nil
	}
	observed := make([]ipam.ObservedInterface, 0, len(item.Interfaces))
	for i := range item.Interfaces {
		ifc := &item.Interfaces[i]
		if strings.TrimSpace(ifc.Name) == "" {
			continue
		}
		observed = append(observed, ipam.ObservedInterface{
			Name: ifc.Name, IfIndex: ifc.IfIndex, MAC: canonicalMAC(ifc.MAC), SpeedMbps: ifc.SpeedMbps, MTU: ifc.MTU,
			AdminStatus: ifc.AdminStatus, OperStatus: ifc.OperStatus, IsManagement: ifc.Management, IPs: ifc.IPs,
		})
	}
	_, err := h.interfaces.SyncDiscoveredInterfaces(ctx, orgID, ciID, observed)
	return err
}

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
	// ProbableDuplicates are CIs sharing the hostname or FQDN of a new CI
	// (REC-02, CH29); they are reviewed, never merged.
	ProbableDuplicates []string `json:"probable_duplicates,omitempty"`
	Criterion          string   `json:"criterion,omitempty"`

	// ValueConflicts lists fields where the incoming item carries a different
	// non-empty identity value than the matched CI (e.g. a changed serial
	// number). Callers queue these for operator review instead of silently
	// overwriting (spec §5.3: Konfliktlösung + Review-Queue).
	ValueConflicts []string `json:"value_conflicts,omitempty"`
}

// Reconcile matches an incoming discovery item against existing CIs by the
// identity rules of REC-02 (ResolveIdentity) without type or VRF context.
func Reconcile(existing []ci.Item, incoming IngestItem) ReconcileResult {
	return ResolveIdentity(existing, &incoming, nil)
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

// ObservedValidator checks observed values against the field metadata of
// the CI type with the rules of a manual write (CI-03, MET-11);
// ci.Service implements it.
type ObservedValidator interface {
	InvalidObserved(ctx context.Context, orgID, ciTypeID, ciID string, values map[string]any) ([]string, error)
}

// ObservedRulesSource resolves the field rules of a CI type for new CIs
// once; ci.Service implements it (WP-333).
type ObservedRulesSource interface {
	ObservedRules(ctx context.Context, orgID, ciTypeID string) (func(values map[string]any) []string, error)
}

// observedMemo validates the values of the new CIs of one batch with the
// rules of each CI type resolved once (NFR-03, WP-333); a CI that exists
// already is validated by next.
type observedMemo struct {
	next  ObservedValidator
	src   ObservedRulesSource
	rules map[string]func(values map[string]any) []string
}

// batchValidator is the validator of the new CIs of one batch.
func (h *Handler) batchValidator() ObservedValidator {
	if src, ok := h.observed.(ObservedRulesSource); ok {
		return &observedMemo{next: h.observed, src: src, rules: map[string]func(map[string]any) []string{}}
	}
	return h.observed
}

func (m *observedMemo) InvalidObserved(ctx context.Context, orgID, ciTypeID, ciID string, values map[string]any) ([]string, error) {
	if ciID != "" || len(values) == 0 {
		return m.next.InvalidObserved(ctx, orgID, ciTypeID, ciID, values)
	}
	rule, ok := m.rules[ciTypeID]
	if !ok {
		var err error
		if rule, err = m.src.ObservedRules(ctx, orgID, ciTypeID); err != nil {
			return nil, err
		}
		m.rules[ciTypeID] = rule
	}
	return rule(values), nil
}

// WithObservedValidator makes ingest validate discovered values: a value
// the field metadata rejects is dropped instead of stored.
func (h *Handler) WithObservedValidator(v ObservedValidator) *Handler {
	h.observed = v
	return h
}

// observedTypedFields are the typed CI columns of an ingest item by field
// name.
func observedTypedFields(item *IngestItem) map[string]*string {
	return map[string]*string{
		"name": &item.Name, "manufacturer": &item.Manufacturer, "model": &item.Model,
		"serial_number": &item.SerialNumber, "hardware_uuid": &item.HardwareUUID,
		"management_ip": &item.ManagementIP, "primary_mac": &item.PrimaryMAC,
		"hostname": &item.Hostname, "fqdn": &item.FQDN, "sys_object_id": &item.SysObjectID,
	}
}

// dropInvalidObserved removes the discovered values that violate the field
// metadata of the CI type (typed columns and attributes alike) and returns
// how many were dropped. Required fields do not apply to observations.
func (h *Handler) dropInvalidObserved(ctx context.Context, orgID, typeID, ciID string, item *IngestItem) (int, error) {
	return dropInvalidWith(ctx, h.observed, orgID, typeID, ciID, item)
}

// dropInvalidWith is dropInvalidObserved with validator v.
func dropInvalidWith(ctx context.Context, v ObservedValidator, orgID, typeID, ciID string, item *IngestItem) (int, error) {
	if v == nil || typeID == "" {
		return 0, nil
	}
	typed := observedTypedFields(item)
	values := map[string]any{}
	for k, v := range item.Attributes {
		values[k] = v
	}
	for name, ptr := range typed {
		if *ptr != "" {
			values[name] = *ptr
		}
	}
	invalid, err := v.InvalidObserved(ctx, orgID, typeID, ciID, values)
	if err != nil {
		return 0, err
	}
	for _, field := range invalid {
		if ptr, ok := typed[field]; ok {
			*ptr = ""
		}
		delete(item.Attributes, field)
		slog.WarnContext(ctx, "discovery value rejected by the field metadata", "field", field, "ci_type_id", typeID)
	}
	return len(invalid), nil
}
