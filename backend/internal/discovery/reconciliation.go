package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
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

// seenAt is the time a record saw its device (GLO-12): its observed_at, so
// that a spooled record does not make the device look freshly seen; the
// current time only for a device without one (created from a review).
func seenAt(item *IngestItem) time.Time {
	if item.ObservedAt.IsZero() {
		return time.Now().UTC()
	}
	return item.ObservedAt.UTC()
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
// interfaces can miss one. Observed addresses take the VRF of the item; a
// reservation of an observed address in that VRF opens a duplicate_ip item
// (NET-09, WP-772), in the transaction of the record or batch.
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
			VRFID: item.VRFID,
		})
	}
	res, err := h.interfaces.SyncDiscoveredInterfaces(ctx, orgID, ciID, observed)
	if err != nil {
		return err
	}
	for i := range res.Conflicts {
		if _, err = h.ReviewService().OpenUnlessDismissed(ctx, reservationConflictReview(orgID, ciID, &res.Conflicts[i]), "conflict"); err != nil {
			return err
		}
	}
	// The written addresses join the duplicate check of the ingest call
	// once the record's or batch's transaction has committed (NET-06,
	// WP-300); a rolled back record contributes nothing.
	if keys := res.Observed; len(keys) > 0 {
		if c, ok := ctx.Value(duplicateKeysKey{}).(*duplicateKeys); ok {
			database.AfterCommit(ctx, func(context.Context) { c.add(keys) })
		}
	}
	return nil
}

// duplicateKeysKey carries the *duplicateKeys of an ingest call.
type duplicateKeysKey struct{}

// duplicateKeys collects the committed addresses of one ingest call
// (processItems) for its duplicate check (NET-06, WP-300).
type duplicateKeys struct {
	mu   sync.Mutex
	seen map[ipam.DuplicateKey]bool
	keys []ipam.DuplicateKey
}

func (c *duplicateKeys) add(keys []ipam.DuplicateKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		if !c.seen[k] {
			c.seen[k] = true
			c.keys = append(c.keys, k)
		}
	}
}

// collectDuplicates prepares the duplicate check of an ingest call: the
// returned context collects the addresses its records commit, and check,
// called once after the last record (also when a record failed), looks
// for duplicates among them with one query in a transaction of its own
// (ipam.CheckDuplicates) and opens the duplicate_ip items. A call within
// another one (processEach) adds to the outer call's check. Without an
// IPAM repository that finds duplicates nothing is checked. A failed check
// is logged; the next observation of the address checks again.
func (h *Handler) collectDuplicates(ctx context.Context, orgID string) (collecting context.Context, check func()) {
	finder, ok := h.interfaces.(ipam.DuplicateFinder)
	if !ok {
		return ctx, func() {}
	}
	if _, nested := ctx.Value(duplicateKeysKey{}).(*duplicateKeys); nested {
		return ctx, func() {}
	}
	c := &duplicateKeys{seen: map[ipam.DuplicateKey]bool{}}
	return context.WithValue(ctx, duplicateKeysKey{}, c), func() {
		c.mu.Lock()
		keys := c.keys
		c.mu.Unlock()
		if len(keys) == 0 {
			return
		}
		if err := ipam.CheckDuplicates(ctx, finder, h.ReviewService(), orgID, ipam.DuplicateSource{Keys: keys}); err != nil {
			slog.WarnContext(ctx, "duplicate address check failed; the next observation checks again", "event", "ingest.duplicate_ip",
				"organization_id", orgID, "addresses", len(keys), "error", err)
		}
	}
}

// reservationConflictReview is the duplicate_ip item of a reservation of an
// address that discovery observed at an interface of CI ciID in the same
// VRF (NET-09, E-53). Its object is the address in the VRF; conflict names
// the reservation and the interface, so that a dismissal holds until another
// interface reports the address or a new reservation exists.
func reservationConflictReview(orgID, ciID string, c *ipam.ReservationConflict) *ReviewItem {
	return &ReviewItem{
		OrganizationID: orgID,
		Kind:           reviews.KindDuplicateIP,
		Status:         ReviewStatusOpen,
		ObjectKey:      "ip:" + c.VRFID + "/" + c.Address,
		Payload: map[string]any{
			"reason":            ReasonReservation,
			"address":           c.Address,
			"vrf_id":            c.VRFID,
			"subnet_id":         c.SubnetID,
			"reservation_ip_id": c.ReservationIPID,
			"discovered_ip_id":  c.DiscoveredIPID,
			"interface_id":      c.InterfaceID,
			"ci_id":             ciID,
			"reserved_by":       c.ReservedBy,
			"conflict":          c.ReservationIPID + "/" + c.InterfaceID,
		},
		CandidateCIIDs: []string{ciID},
	}
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
	value = macSeparators.Replace(value)
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

// dropInvalidTyped removes the typed column values of an item that violate
// the column's MET-11 data type (a multi-line hostname, a hardware_uuid that
// is no UUID, a malformed MAC or IP), as a manual write refuses them, and
// returns how many it dropped (CI-03, MET-11, WP-501). It runs before the
// identities of a batch are resolved: a malformed identity value would
// otherwise reach the candidate query or the INSERT and fail the record.
// The fingerprint's hardware UUID is checked alike, and fingerprint MACs
// equal to a dropped primary MAC go with it. A dropped name falls back to
// the hostname or the management IP, as the name of a DeviceRecord does.
// The item's fingerprint map is replaced, not changed: callers share it.
func dropInvalidTyped(ctx context.Context, item *IngestItem) int {
	typed := observedTypedFields(item)
	values := map[string]any{}
	for name, ptr := range typed {
		if *ptr != "" {
			values[name] = *ptr
		}
	}
	invalid := ci.InvalidTypedColumns(values)
	dropped := len(invalid)
	droppedMAC := ""
	for _, field := range invalid {
		if field == "primary_mac" {
			droppedMAC = item.PrimaryMAC
		}
		*typed[field] = ""
		slog.WarnContext(ctx, "discovery value rejected by its data type", "field", field)
	}
	fpUUID := fingerprintString(item.Fingerprint["hardware_uuid"])
	fpInvalid := fpUUID != "" && len(ci.InvalidTypedColumns(map[string]any{"hardware_uuid": fpUUID})) > 0
	if fpInvalid || droppedMAC != "" {
		fingerprint := maps.Clone(item.Fingerprint)
		if fpInvalid {
			delete(fingerprint, "hardware_uuid")
			if fpUUID != values["hardware_uuid"] {
				dropped++
				slog.WarnContext(ctx, "discovery value rejected by its data type", "field", "fingerprint.hardware_uuid")
			}
		}
		if macs := fingerprintSlice(fingerprint["mac_addresses"]); droppedMAC != "" && len(macs) > 0 {
			kept := make([]any, 0, len(macs))
			for _, mac := range macs {
				if !strings.EqualFold(mac, strings.TrimSpace(droppedMAC)) {
					kept = append(kept, mac)
				}
			}
			if len(kept) > 0 {
				fingerprint["mac_addresses"] = kept
			} else {
				delete(fingerprint, "mac_addresses")
			}
		}
		item.Fingerprint = fingerprint
	}
	if item.Name == "" && values["name"] != nil {
		item.Name = firstNonEmpty(item.Hostname, item.ManagementIP)
	}
	return dropped + dropInvalidTags(ctx, item)
}

// dropInvalidTags removes attributes.tags of an item when it is no array of
// strings and returns 1 then, else 0 (E-125, WP-281). A valid list reaches
// the CI write, which assigns the tags in the record's transaction
// (ci.ExtractLegacyTags); it is no field value, so its provenance is not
// recorded (withoutLegacyTags). The attributes are replaced, not changed:
// callers share them.
func dropInvalidTags(ctx context.Context, item *IngestItem) int {
	if _, _, err := ci.ExtractLegacyTags(item.Attributes); err == nil {
		return 0
	}
	attributes := maps.Clone(item.Attributes)
	delete(attributes, ci.LegacyTagsKey)
	item.Attributes = attributes
	slog.WarnContext(ctx, "discovery value rejected by its data type", "field", "attributes."+ci.LegacyTagsKey)
	return 1
}

// withoutLegacyTags returns the discovered values without tags: the tag
// assignment is no field value (CHECK ci_field_value_no_tags, WP-281) and
// has no provenance.
func withoutLegacyTags(values map[string]any) map[string]any {
	if _, ok := values[ci.LegacyTagsKey]; !ok {
		return values
	}
	out := maps.Clone(values)
	delete(out, ci.LegacyTagsKey)
	return out
}
