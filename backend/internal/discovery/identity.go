package discovery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// Identity resolution (REC-02, CH29). Candidates come from an index-backed
// query over the whole inventory of the organization and are bound to the
// record's client; ResolveIdentity decides in this order:
//
//  1. serial number with the same CI type or generic_device; placeholder
//     serials are ignored;
//  2. hardware UUID; known clone UUIDs are ignored, virtual types also need
//     a MAC or hostname match;
//  3. a unique intersection of MAC addresses; locally administered and
//     VRRP/HSRP/CARP MACs are ignored, several CIs give ambiguous_identity;
//  4. management IP and sysObjectID within the same VRF;
//  5. hostname/FQDN never merges: the device becomes a new CI and the hit
//     a probable_duplicate review.

// placeholderSerials are serial numbers firmware reports when none is set.
var placeholderSerials = map[string]bool{
	"0": true, "n/a": true, "na": true, "none": true, "null": true, "unknown": true,
	"to be filled by o.e.m.": true, "default string": true, "system serial number": true,
	"not specified": true, "0123456789": true, "123456789": true,
}

// cloneUUIDs are hardware UUIDs shared by many devices.
var cloneUUIDs = map[string]bool{
	"00000000-0000-0000-0000-000000000000": true,
	"03000200-0400-0500-0006-000700080009": true,
	"ffffffff-ffff-ffff-ffff-ffffffffffff": true,
}

// virtualTypeKeys are the CI types whose hardware UUID alone does not
// identify a device (REC-02).
var virtualTypeKeys = []string{"vm", "virtual_machine", "container"}

// usableSerial reports whether a serial number may identify a device.
func usableSerial(serial string) bool {
	s := strings.ToLower(strings.TrimSpace(serial))
	if s == "" || placeholderSerials[s] {
		return false
	}
	// Repeated single characters ("00000000", "xxxx") are placeholders too.
	return strings.Trim(s, s[:1]) != ""
}

// usableUUID reports whether a hardware UUID may identify a device.
func usableUUID(uuid string) bool {
	u := strings.ToLower(strings.TrimSpace(uuid))
	return u != "" && !cloneUUIDs[u]
}

// canonicalMAC returns the MAC in aa:bb:cc:dd:ee:ff form, or "" when the
// value is no MAC or one that does not identify a device: locally
// administered (also multicast) addresses and the virtual router MACs of
// VRRP/CARP (00:00:5e:00:01/02:xx) and HSRP (00:00:0c:07:ac:xx,
// 00:00:0c:9f:fx:xx).
func canonicalMAC(value string) string {
	raw := normalizeMACValue(value)
	if raw == "" {
		return ""
	}
	mac := raw[0:2] + ":" + raw[2:4] + ":" + raw[4:6] + ":" + raw[6:8] + ":" + raw[8:10] + ":" + raw[10:12]
	first, err := strconv.ParseUint(raw[0:2], 16, 8)
	if err != nil {
		return ""
	}
	switch {
	case first&0x02 != 0, first&0x01 != 0:
		return ""
	case strings.HasPrefix(mac, "00:00:5e:00:01:"), strings.HasPrefix(mac, "00:00:5e:00:02:"):
		return ""
	case strings.HasPrefix(mac, "00:00:0c:07:ac:"), strings.HasPrefix(mac, "00:00:0c:9f:f"):
		return ""
	}
	return mac
}

// identityMACs returns the usable MACs of a set of raw values.
func identityMACs(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		if mac := canonicalMAC(v); mac != "" {
			out[mac] = true
		}
	}
	return out
}

func incomingMACs(item *IngestItem) map[string]bool {
	values := append([]string{item.PrimaryMAC}, fingerprintSlice(item.Fingerprint["mac_addresses"])...)
	for i := range item.Interfaces {
		values = append(values, item.Interfaces[i].MAC)
	}
	return identityMACs(values...)
}

func storedMACs(item *ci.Item) map[string]bool {
	return identityMACs(append([]string{item.PrimaryMAC}, existingFingerprintSlice(*item, "mac_addresses")...)...)
}

func storedUUID(item *ci.Item) string {
	if item.HardwareUUID != "" {
		return item.HardwareUUID
	}
	return existingFingerprintValue(*item, "hardware_uuid")
}

func incomingUUID(item *IngestItem) string {
	if item.HardwareUUID != "" {
		return item.HardwareUUID
	}
	return fingerprintString(item.Fingerprint["hardware_uuid"])
}

func incomingSysObjectID(item *IngestItem) string {
	if item.SysObjectID != "" {
		return item.SysObjectID
	}
	if v, ok := item.RawData["sys_object_id"].(string); ok {
		return v
	}
	return ""
}

// storedVRF is the VRF the CI's management IP was last reported in.
func storedVRF(item *ci.Item) string {
	if raw, ok := item.Attributes["raw_data"].(map[string]any); ok {
		if v, ok := raw["vrf_id"].(string); ok {
			return v
		}
	}
	return ""
}

func sameIP(a, b string) bool {
	ipA, ipB := net.ParseIP(strings.TrimSpace(a)), net.ParseIP(strings.TrimSpace(b))
	if strings.Contains(a, "/") {
		ipA, _, _ = net.ParseCIDR(strings.TrimSpace(a))
	}
	if strings.Contains(b, "/") {
		ipB, _, _ = net.ParseCIDR(strings.TrimSpace(b))
	}
	return ipA != nil && ipB != nil && ipA.Equal(ipB)
}

// IdentityContext carries what ResolveIdentity needs besides the records:
// the type ids of generic_device and the virtual types, and the
// organization's default VRF (records without vrf_id are in it).
type IdentityContext struct {
	GenericDeviceTypeID string
	VirtualTypeIDs      map[string]bool
	DefaultVRF          string
}

func (c *IdentityContext) vrf(id string) string {
	if id == "" {
		return c.DefaultVRF
	}
	return id
}

// ResolveIdentity applies REC-02 to the candidates of one incoming item;
// incoming.CITypeName is the resolved ci_type id.
func ResolveIdentity(candidates []ci.Item, incoming *IngestItem, ic *IdentityContext) ReconcileResult {
	if ic == nil {
		ic = &IdentityContext{}
	}
	// Client binding: a record of a client scope matches only that client's
	// CIs; an org-wide record matches across the organization.
	var pool []*ci.Item
	for i := range candidates {
		if incoming.ClientID == "" || candidates[i].ClientID == incoming.ClientID {
			pool = append(pool, &candidates[i])
		}
	}
	decide := func(criterion string, matches []*ci.Item) (ReconcileResult, bool) {
		switch len(matches) {
		case 0:
			return ReconcileResult{}, false
		case 1:
			res := ReconcileResult{Action: ReconcileMatched, MatchedCIID: matches[0].ID, Criterion: criterion}
			res.ValueConflicts = identityConflicts(*matches[0], *incoming)
			return res, true
		}
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		return ReconcileResult{Action: ReconcileConflict, CandidateCIIDs: ids, Criterion: criterion}, true
	}
	filter := func(match func(*ci.Item) bool) []*ci.Item {
		var out []*ci.Item
		for _, c := range pool {
			if match(c) {
				out = append(out, c)
			}
		}
		return out
	}

	macs := incomingMACs(incoming)
	macMatch := func(c *ci.Item) bool {
		for mac := range storedMACs(c) {
			if macs[mac] {
				return true
			}
		}
		return false
	}
	hostnameMatch := func(c *ci.Item) bool {
		return normalizedEqual(c.Hostname, incoming.Hostname) || normalizedEqual(c.FQDN, incoming.FQDN)
	}

	// 1. Serial number of the same type or generic_device.
	if usableSerial(incoming.SerialNumber) {
		matches := filter(func(c *ci.Item) bool {
			if !normalizedEqual(c.SerialNumber, incoming.SerialNumber) {
				return false
			}
			return c.CITypeID == incoming.CITypeName || (ic.GenericDeviceTypeID != "" &&
				(c.CITypeID == ic.GenericDeviceTypeID || incoming.CITypeName == ic.GenericDeviceTypeID))
		})
		if res, ok := decide("serial_number", matches); ok {
			return res
		}
	}

	// 2. Hardware UUID; virtual types need a MAC or hostname match as well.
	if uuid := incomingUUID(incoming); usableUUID(uuid) {
		matches := filter(func(c *ci.Item) bool {
			if !normalizedEqual(storedUUID(c), uuid) {
				return false
			}
			if ic.VirtualTypeIDs[c.CITypeID] || ic.VirtualTypeIDs[incoming.CITypeName] {
				return macMatch(c) || hostnameMatch(c)
			}
			return true
		})
		if res, ok := decide("hardware_uuid", matches); ok {
			return res
		}
	}

	// 3. Unique MAC intersection.
	if len(macs) > 0 {
		if res, ok := decide("mac_addresses", filter(macMatch)); ok {
			return res
		}
	}

	// 4. Management IP and sysObjectID in the same VRF.
	if sysObjectID := incomingSysObjectID(incoming); incoming.ManagementIP != "" && sysObjectID != "" {
		matches := filter(func(c *ci.Item) bool {
			return sameIP(c.ManagementIP, incoming.ManagementIP) && normalizedEqual(c.SysObjectID, sysObjectID) &&
				ic.vrf(storedVRF(c)) == ic.vrf(incoming.VRFID)
		})
		if res, ok := decide("management_ip_sys_object_id", matches); ok {
			return res
		}
	}

	// 5. Hostname/FQDN: never a merge (CH29).
	res := ReconcileResult{Action: ReconcileCreated}
	if incoming.Hostname != "" || incoming.FQDN != "" {
		for _, c := range filter(hostnameMatch) {
			res.ProbableDuplicates = append(res.ProbableDuplicates, c.ID)
		}
	}
	return res
}

// withoutTakenIdentity clears the identity values of a new CI that another
// CI already holds: serial number, hardware UUID and primary MAC are unique
// per organization, and a value that did not match (another type, another
// client, a virtual type) must not block the new CI.
func withoutTakenIdentity(item *IngestItem, candidates []ci.Item) {
	for i := range candidates {
		c := &candidates[i]
		if item.SerialNumber != "" && strings.EqualFold(strings.TrimSpace(c.SerialNumber), strings.TrimSpace(item.SerialNumber)) {
			item.SerialNumber = ""
		}
		if item.HardwareUUID != "" && strings.EqualFold(c.HardwareUUID, item.HardwareUUID) {
			item.HardwareUUID = ""
		}
		if item.PrimaryMAC != "" && normalizedMACEqual(c.PrimaryMAC, item.PrimaryMAC) {
			item.PrimaryMAC = ""
		}
	}
}

// withoutBlocklistedIdentity clears identity values that identify nothing:
// placeholder serial numbers, clone hardware UUIDs and MACs that are locally
// administered or belong to a virtual router. Many devices report the same
// such value, so it must not take the organization-unique identity column
// of one CI (a second device would be refused); the fingerprint keeps the
// reported UUID and MACs (TST-01).
func withoutBlocklistedIdentity(item *IngestItem) {
	if item.SerialNumber != "" && !usableSerial(item.SerialNumber) {
		item.SerialNumber = ""
	}
	if item.HardwareUUID != "" && !usableUUID(item.HardwareUUID) {
		item.HardwareUUID = ""
	}
	if item.PrimaryMAC != "" && canonicalMAC(item.PrimaryMAC) == "" {
		item.PrimaryMAC = ""
	}
}

// IdentityQuery holds the values candidate CIs are looked up by.
type IdentityQuery struct {
	SerialNumber string
	HardwareUUID string
	MACs         []string
	ManagementIP string
	Hostnames    []string
}

// IdentityCandidateFinder finds candidate CIs over the whole inventory with
// the indexes on serial number, hardware UUID, MACs, management IP and
// hostname/FQDN (REC-02, NFR-01). The PostgreSQL repository implements it.
type IdentityCandidateFinder interface {
	IdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]string, error)
	DefaultVRF(ctx context.Context, orgID string) (string, error)
}

func identityQuery(item *IngestItem) IdentityQuery {
	q := IdentityQuery{ManagementIP: item.ManagementIP}
	if usableSerial(item.SerialNumber) {
		q.SerialNumber = strings.TrimSpace(item.SerialNumber)
	}
	if uuid := incomingUUID(item); usableUUID(uuid) {
		q.HardwareUUID = strings.ToLower(uuid)
	}
	for mac := range incomingMACs(item) {
		q.MACs = append(q.MACs, mac)
	}
	for _, h := range []string{item.Hostname, item.FQDN} {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			q.Hostnames = append(q.Hostnames, h)
		}
	}
	return q
}

// IdentityKeys are the identity values of the records of a batch, each
// once per kind, in the form identityFilter binds them (NFR-03, WP-333).
type IdentityKeys struct {
	Serials   []string // serial_number = v
	UUIDs     []string // hardware_uuid = v, canonical lower case
	MACs      []string // primary_mac = v or a fingerprint MAC, canonical
	IPs       []string // management_ip = v, host address only
	Hostnames []string // hostname or FQDN in lower case
}

func (k *IdentityKeys) empty() bool {
	return len(k.Serials)+len(k.UUIDs)+len(k.MACs)+len(k.IPs)+len(k.Hostnames) == 0
}

// Kinds of identity values in IdentityKey.
const (
	keySerial = "serial"
	keyUUID   = "uuid"
	keyMAC    = "mac"
	keyIP     = "ip"
	keyHost   = "host"
)

// IdentityKey names one value of IdentityKeys: its kind and its position in
// that kind's list.
type IdentityKey struct {
	Kind  string
	Index int
}

// IdentityHit is a live CI the batch candidate query found, with the value
// it matched.
type IdentityHit struct {
	CIID string
	Key  IdentityKey
}

// DeletedIdentityHit is a soft-deleted CI the batch query found, with the
// value it matched.
type DeletedIdentityHit struct {
	CI  ci.Item
	Key IdentityKey
}

// BatchIdentityFinder finds the identity candidates of all records of a
// batch with one query each for live and for soft-deleted CIs (NFR-03,
// WP-333); which record a CI is a candidate of is decided in Go from the
// value it matched. The PostgreSQL repository implements it.
type BatchIdentityFinder interface {
	// IdentityHits returns every live CI matching a value, once per
	// matching value, in (created_at, id) order like IdentityCandidates and
	// without an upper bound.
	IdentityHits(ctx context.Context, orgID string, keys *IdentityKeys) ([]IdentityHit, error)
	// DeletedIdentityHits returns the soft-deleted CIs matching a value, at
	// most 1000 per value, in (deleted_at DESC, id) order, with the fields
	// identity resolution compares.
	DeletedIdentityHits(ctx context.Context, orgID string, keys *IdentityKeys) ([]DeletedIdentityHit, error)
}

// deletedCandidateLimit is the number of soft-deleted CIs a record is
// compared with, as in DeletedIdentityCandidates.
const deletedCandidateLimit = 1000

// batchable reports whether the batch query binds the values of q exactly
// as identityFilter does: the hardware UUID must be one PostgreSQL reads.
// A window with another record is processed record by record, where the
// query of that record fails as before.
func batchable(q *IdentityQuery) bool {
	_, ok := canonicalUUID(q.HardwareUUID)
	return q.HardwareUUID == "" || ok
}

// canonicalUUID returns value in the canonical lower-case 8-4-4-4-12 form
// when PostgreSQL reads it as a uuid (uuid_in: optional braces, 32 hex
// digits, a hyphen allowed after any group of four), so one device reported
// in two spellings has one batch key.
func canonicalUUID(value string) (string, bool) {
	s := value
	if strings.HasPrefix(s, "{") {
		if !strings.HasSuffix(s, "}") {
			return "", false
		}
		s = s[1 : len(s)-1]
	}
	digits := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			digits = append(digits, c)
		case c >= 'A' && c <= 'F':
			digits = append(digits, c+'a'-'A')
		case c == '-' && len(digits) > 0 && len(digits)%4 == 0 && len(digits) < 32 && s[i-1] != '-':
		default:
			return "", false
		}
	}
	if len(digits) != 32 {
		return "", false
	}
	d := string(digits)
	return d[0:8] + "-" + d[8:12] + "-" + d[12:16] + "-" + d[16:20] + "-" + d[20:32], true
}

// batchKeys collects the identity values of the records with include[i],
// each once per kind; refs[i] lists the values of record i (nil without
// values or when not included). The values are those identityFilter binds
// for the record.
func batchKeys(qs []IdentityQuery, include []bool) (*IdentityKeys, [][]IdentityKey) {
	keys := &IdentityKeys{}
	values := map[string]map[string]int{}
	lists := map[string]*[]string{keySerial: &keys.Serials, keyUUID: &keys.UUIDs, keyMAC: &keys.MACs, keyIP: &keys.IPs, keyHost: &keys.Hostnames}
	refs := make([][]IdentityKey, len(qs))
	for i := range qs {
		if !include[i] {
			continue
		}
		q := &qs[i]
		seen := map[IdentityKey]bool{}
		add := func(kind, value string) {
			if value == "" {
				return
			}
			if values[kind] == nil {
				values[kind] = map[string]int{}
			}
			n, ok := values[kind][value]
			if !ok {
				list := lists[kind]
				n = len(*list)
				*list = append(*list, value)
				values[kind][value] = n
			}
			key := IdentityKey{Kind: kind, Index: n}
			if !seen[key] {
				seen[key] = true
				refs[i] = append(refs[i], key)
			}
		}
		add(keySerial, q.SerialNumber)
		if uuid, ok := canonicalUUID(q.HardwareUUID); ok {
			add(keyUUID, uuid)
		}
		for _, mac := range q.MACs {
			add(keyMAC, mac)
		}
		add(keyIP, hostOnly(q.ManagementIP))
		for _, host := range q.Hostnames {
			add(keyHost, host)
		}
	}
	return keys, refs
}

// byKey maps each identity value to the records that carry it.
func byKey(refs [][]IdentityKey) map[IdentityKey][]int {
	out := map[IdentityKey][]int{}
	for i, keys := range refs {
		for _, k := range keys {
			out[k] = append(out[k], i)
		}
	}
	return out
}

// assignCandidates gives each record the CIs that matched one of its
// values, each once, in the order of hits: exactly the candidates and the
// order IdentityCandidates returns for the record alone.
func assignCandidates(refs [][]IdentityKey, hits []IdentityHit) [][]string {
	records := byKey(refs)
	out := make([][]string, len(refs))
	seen := make([]map[string]bool, len(refs))
	for _, hit := range hits {
		for _, i := range records[hit.Key] {
			if seen[i] == nil {
				seen[i] = map[string]bool{}
			}
			if !seen[i][hit.CIID] {
				seen[i][hit.CIID] = true
				out[i] = append(out[i], hit.CIID)
			}
		}
	}
	return out
}

// assignDeleted gives each record the soft-deleted CIs that matched one of
// its values, each once, in the order of hits and at most
// deletedCandidateLimit: the result DeletedIdentityCandidates returns for
// the record alone (the newest 1000 of a record are among the newest 1000
// of each of its values).
func assignDeleted(refs [][]IdentityKey, hits []DeletedIdentityHit) [][]ci.Item {
	records := byKey(refs)
	out := make([][]ci.Item, len(refs))
	seen := make([]map[string]bool, len(refs))
	for h := range hits {
		hit := &hits[h]
		for _, i := range records[hit.Key] {
			if seen[i] == nil {
				seen[i] = map[string]bool{}
			}
			if !seen[i][hit.CI.ID] && len(out[i]) < deletedCandidateLimit {
				seen[i][hit.CI.ID] = true
				out[i] = append(out[i], hit.CI)
			}
		}
	}
	return out
}

// identityClusters groups the records with include[i] that can change each
// other's identity resolution: records sharing a normalized identity value
// (serial number and hardware UUID case-insensitive, MAC, IP address, host
// name or FQDN) or a live candidate CI. first[i] is true for the first
// record of its group. Only a first record may use the candidates of the
// batch query or be created in the batch: the others follow one by one
// after it, with a fresh query, as without the batch. A false link only
// costs a record its batch.
func identityClusters(qs []IdentityQuery, include []bool, candidates [][]string) []bool {
	parent := make([]int, len(qs))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	link := func(i int, key string) {
		if key == "" {
			return
		}
		j, ok := owner[key]
		if !ok {
			owner[key] = i
			return
		}
		a, b := find(i), find(j)
		if a > b {
			a, b = b, a
		}
		parent[b] = a
	}
	for i := range qs {
		if !include[i] {
			continue
		}
		q := &qs[i]
		if q.SerialNumber != "" {
			link(i, "s|"+strings.ToLower(strings.TrimSpace(q.SerialNumber)))
		}
		if uuid, ok := canonicalUUID(q.HardwareUUID); ok {
			link(i, "u|"+uuid)
		}
		for _, mac := range q.MACs {
			link(i, "m|"+mac)
		}
		if ip := net.ParseIP(hostOnly(q.ManagementIP)); ip != nil {
			link(i, "i|"+ip.String())
		}
		for _, host := range q.Hostnames {
			link(i, "h|"+host)
		}
		for _, id := range candidates[i] {
			link(i, "c|"+strings.ToLower(id))
		}
	}
	first := make([]bool, len(qs))
	for i := range qs {
		first[i] = include[i] && find(i) == i
	}
	return first
}

// loadCandidates reads the CIs of all records with one read and returns
// them per record in the order of ids; a CI that cannot be read fails like
// in identityCandidates.
func (h *Handler) loadCandidates(ctx context.Context, orgID string, ids [][]string) ([][]ci.Item, error) {
	var all []string
	seen := map[string]bool{}
	for _, list := range ids {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
			}
		}
	}
	byID := make(map[string]ci.Item, len(all))
	if reader, ok := h.ciRepo.(ci.BatchReader); ok && len(all) > 0 {
		items, err := reader.GetByIDs(ctx, orgID, all)
		if err != nil {
			return nil, fmt.Errorf("load candidates: %w", err)
		}
		for i := range items {
			byID[items[i].ID] = items[i]
		}
	} else {
		for _, id := range all {
			item, err := h.ciRepo.GetByID(ctx, orgID, id)
			if err != nil {
				return nil, fmt.Errorf("load candidate %s: %w", id, err)
			}
			byID[id] = *item
		}
	}
	out := make([][]ci.Item, len(ids))
	for i, list := range ids {
		for _, id := range list {
			item, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("load candidate %s: not found", id)
			}
			out[i] = append(out[i], item)
		}
	}
	return out, nil
}

// CITypeBatchLookup resolves several CI type names or keys at once. The
// discovery PG repository implements it with one statement (NFR-03).
type CITypeBatchLookup interface {
	// LookupCITypeIDs returns the type id per name; unknown names are
	// missing from the map.
	LookupCITypeIDs(ctx context.Context, orgID string, namesOrKeys []string) (map[string]string, error)
}

// lookupTypes resolves the type names of a batch: with one lookup where the
// repository can, else name by name, skipping unknown names.
func (h *Handler) lookupTypes(ctx context.Context, orgID string, names []string) (map[string]string, error) {
	if batch, ok := h.repo.(CITypeBatchLookup); ok {
		types, err := batch.LookupCITypeIDs(ctx, orgID, names)
		if err != nil {
			return nil, fmt.Errorf("lookup ci types: %w", err)
		}
		return types, nil
	}
	types := map[string]string{}
	if h.typeLookup == nil {
		return types, nil
	}
	for _, name := range names {
		if id, err := h.typeLookup.LookupCITypeID(ctx, orgID, name); err == nil && id != "" {
			types[name] = id
		}
	}
	return types, nil
}

// identityContext resolves the identity context of an ingest from the types
// of its batch (lookupTypes) and the default VRF.
func (h *Handler) identityContext(ctx context.Context, orgID string, types map[string]string) (*IdentityContext, error) {
	ic := &IdentityContext{GenericDeviceTypeID: types[ci.GenericDeviceType], VirtualTypeIDs: map[string]bool{}}
	for _, key := range virtualTypeKeys {
		if id := types[key]; id != "" {
			ic.VirtualTypeIDs[id] = true
		}
	}
	if finder, ok := h.repo.(IdentityCandidateFinder); ok {
		vrf, err := finder.DefaultVRF(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("default VRF: %w", err)
		}
		ic.DefaultVRF = vrf
	}
	return ic, nil
}

// identityCandidates returns the CIs an item may be: through the index
// query, or the whole inventory for repositories without one.
func (h *Handler) identityCandidates(ctx context.Context, orgID string, item *IngestItem) ([]ci.Item, error) {
	finder, ok := h.repo.(IdentityCandidateFinder)
	if !ok {
		items, _, err := h.ciRepo.List(ctx, orgID, ci.FilterParams{}, api.PaginationParams{Limit: 1 << 30})
		return items, err
	}
	q := identityQuery(item)
	if q.SerialNumber == "" && q.HardwareUUID == "" && len(q.MACs) == 0 && q.ManagementIP == "" && len(q.Hostnames) == 0 {
		return nil, nil
	}
	ids, err := finder.IdentityCandidates(ctx, orgID, &q)
	if err != nil {
		return nil, fmt.Errorf("identity candidates: %w", err)
	}
	// One read for all candidates (NFR-03), in the order of the index query.
	if reader, ok := h.ciRepo.(ci.BatchReader); ok && len(ids) > 0 {
		items, err := reader.GetByIDs(ctx, orgID, ids)
		if err != nil {
			return nil, fmt.Errorf("load candidates: %w", err)
		}
		byID := make(map[string]ci.Item, len(items))
		for i := range items {
			byID[items[i].ID] = items[i]
		}
		out := make([]ci.Item, 0, len(ids))
		for _, id := range ids {
			item, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("load candidate %s: not found", id)
			}
			out = append(out, item)
		}
		return out, nil
	}
	out := make([]ci.Item, 0, len(ids))
	for _, id := range ids {
		item, err := h.ciRepo.GetByID(ctx, orgID, id)
		if err != nil {
			return nil, fmt.Errorf("load candidate %s: %w", id, err)
		}
		out = append(out, *item)
	}
	return out, nil
}

// neighborCandidates returns the CIs topology neighbors can resolve to. A
// neighbor many records of the batch report (the switch of a rack) is
// looked up once (WP-608, NFR-03).
func (h *Handler) neighborCandidates(ctx context.Context, orgID string, items []IngestItem) []ci.Item {
	finder, ok := h.repo.(IdentityCandidateFinder)
	if !ok {
		all, _, _ := h.ciRepo.List(ctx, orgID, ci.FilterParams{}, api.PaginationParams{Limit: 1 << 30})
		return all
	}
	seen := map[string]bool{}
	queried := map[string]bool{}
	var out []ci.Item
	for i := range items {
		for _, rel := range items[i].Relationships {
			q := IdentityQuery{ManagementIP: rel.TargetIP}
			if mac := canonicalMAC(rel.TargetMAC); mac != "" {
				q.MACs = []string{mac}
			}
			if host := strings.ToLower(strings.TrimSpace(rel.TargetHostname)); host != "" {
				q.Hostnames = []string{host}
			}
			key := q.ManagementIP + "\x00" + strings.Join(q.MACs, ",") + "\x00" + strings.Join(q.Hostnames, ",")
			if queried[key] {
				continue
			}
			ids, err := finder.IdentityCandidates(ctx, orgID, &q)
			if err != nil {
				continue
			}
			queried[key] = true
			for _, id := range ids {
				if seen[id] {
					continue
				}
				seen[id] = true
				if c, err := h.ciRepo.GetByID(ctx, orgID, id); err == nil {
					out = append(out, *c)
				}
			}
		}
	}
	return out
}

// probableDuplicateReview is the review of a new CI that shares its
// hostname or FQDN with existing CIs.
func probableDuplicateReview(orgID, newCIID string, incoming *IngestItem, candidates []string) *ReviewItem {
	return &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindProbableDuplicate,
		Status:         ReviewStatusOpen,
		Payload: map[string]any{
			"criterion": "hostname_fqdn",
			"ci_id":     newCIID,
			"hostname":  incoming.Hostname,
			"fqdn":      incoming.FQDN,
			"name":      incoming.Name,
		},
		CandidateCIIDs: candidates,
		ObjectKey:      "ci:" + newCIID,
	}
}

// DeletedCandidateFinder finds soft-deleted CIs by the identity indexes
// (CI-13). The PostgreSQL repository implements it.
type DeletedCandidateFinder interface {
	DeletedIdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]ci.Item, error)
}

// resurrection checks a device without a live match against the
// soft-deleted CIs (CI-13). On a match by REC-02 it opens one
// resurrected_device review per deleted CI and reports that the device is
// held for it: reviewed reports whether a new review was opened.
func (h *Handler) resurrection(ctx context.Context, orgID string, item *IngestItem, ic *IdentityContext) (held, reviewed bool, err error) {
	finder, ok := h.repo.(DeletedCandidateFinder)
	if !ok {
		return false, false, nil
	}
	q := identityQuery(item)
	if !resurrectionGuard(&q) {
		return false, false, nil
	}
	deleted, err := finder.DeletedIdentityCandidates(ctx, orgID, &q)
	if err != nil {
		return false, false, fmt.Errorf("deleted identity candidates: %w", err)
	}
	return h.resurrectionWith(ctx, orgID, item, ic, deleted)
}

// resurrectionGuard reports whether a record without a live match is
// compared with the soft-deleted CIs: it carries an identity value besides
// host names.
func resurrectionGuard(q *IdentityQuery) bool {
	return q.SerialNumber != "" || q.HardwareUUID != "" || len(q.MACs) > 0 || q.ManagementIP != ""
}

// resurrectionWith is resurrection with the soft-deleted candidates already
// read (the batch query of WP-333).
func (h *Handler) resurrectionWith(ctx context.Context, orgID string, item *IngestItem, ic *IdentityContext, deleted []ci.Item) (held, reviewed bool, err error) {
	res := ResolveIdentity(deleted, item, ic)
	var candidates []string
	switch res.Action {
	case ReconcileMatched:
		candidates = []string{res.MatchedCIID}
	case ReconcileConflict:
		candidates = res.CandidateCIIDs
	default:
		return false, false, nil
	}
	// One open review per deleted CI: a device seen again and again is
	// not queued again (REC-08 deduplication).
	review := reviewItemFromConflict(orgID, *item, ReconcileResult{CandidateCIIDs: candidates, Criterion: res.Criterion})
	review.Kind = ReviewKindResurrectedDevice
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	review.ObjectKey = "ci:" + strings.Join(sorted, ",")
	opened, err := h.openReview(ctx, review)
	if err != nil {
		return false, false, err
	}
	return true, opened, nil
}
