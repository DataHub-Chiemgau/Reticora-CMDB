package discovery

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// TestResolveIdentity covers WP-086 (REC-02, CH29, NFR-01): the identity
// rules in their order, the blocklists, the client binding, the VRF of the
// IP match and hostnames that never merge.
func TestResolveIdentity(t *testing.T) {
	ic := &IdentityContext{GenericDeviceTypeID: "generic", VirtualTypeIDs: map[string]bool{"vm": true}, DefaultVRF: "vrf-default"}
	inventory := []ci.Item{
		{ID: "srv", CITypeID: "server", SerialNumber: "SN-100", ClientID: "c1"},
		{ID: "gen", CITypeID: "generic", SerialNumber: "SN-200"},
		{ID: "placeholder", CITypeID: "server", SerialNumber: "To be filled by O.E.M."},
		{ID: "uuid", CITypeID: "server", HardwareUUID: "4c4c4544-0042-3510-8051-b4c04f4a4e33"},
		{ID: "clone", CITypeID: "server", HardwareUUID: "03000200-0400-0500-0006-000700080009"},
		{ID: "vm-1", CITypeID: "vm", HardwareUUID: "11111111-2222-3333-4444-555555555555", PrimaryMAC: "00:50:56:00:00:01"},
		{ID: "mac", CITypeID: "switch", PrimaryMAC: "00:1b:21:00:00:10"},
		{ID: "mac-a", CITypeID: "switch", PrimaryMAC: "00:1b:21:00:00:20"},
		{ID: "mac-b", CITypeID: "switch", PrimaryMAC: "00:1b:21:00:00:21"},
		{ID: "vrrp", CITypeID: "router", PrimaryMAC: "00:00:5e:00:01:07"},
		{ID: "ip", CITypeID: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1"},
		{ID: "ip-vrf", CITypeID: "switch", ManagementIP: "10.1.0.6", SysObjectID: "1.3.6.1.4.1.9.1.1",
			Attributes: map[string]any{"raw_data": map[string]any{"vrf_id": "vrf-blue"}}},
		{ID: "host", CITypeID: "server", Hostname: "web-01", FQDN: "web-01.example.net"},
		{ID: "srv-c2", CITypeID: "server", SerialNumber: "SN-300", ClientID: "c2"},
	}

	for _, c := range []struct {
		name      string
		in        IngestItem
		action    ReconcileAction
		matched   string
		criterion string
		dupes     int
	}{
		{"serial, same type", IngestItem{CITypeName: "server", SerialNumber: "sn-100"}, ReconcileMatched, "srv", "serial_number", 0},
		{"serial, other type", IngestItem{CITypeName: "switch", SerialNumber: "SN-100"}, ReconcileCreated, "", "", 0},
		{"serial of a generic_device", IngestItem{CITypeName: "switch", SerialNumber: "SN-200"}, ReconcileMatched, "gen", "serial_number", 0},
		{"incoming generic_device", IngestItem{CITypeName: "generic", SerialNumber: "SN-100"}, ReconcileMatched, "srv", "serial_number", 0},
		{"placeholder serial is ignored", IngestItem{CITypeName: "server", SerialNumber: "to be filled by o.e.m."}, ReconcileCreated, "", "", 0},
		{"repeated-character serial is ignored", IngestItem{CITypeName: "server", SerialNumber: "00000000"}, ReconcileCreated, "", "", 0},
		{"hardware uuid", IngestItem{CITypeName: "server", HardwareUUID: "4C4C4544-0042-3510-8051-B4C04F4A4E33"}, ReconcileMatched, "uuid", "hardware_uuid", 0},
		{"clone uuid is ignored", IngestItem{CITypeName: "server", HardwareUUID: "03000200-0400-0500-0006-000700080009"}, ReconcileCreated, "", "", 0},
		{"null uuid is ignored", IngestItem{CITypeName: "server", Fingerprint: map[string]any{"hardware_uuid": "00000000-0000-0000-0000-000000000000"}}, ReconcileCreated, "", "", 0},
		{"virtual uuid alone does not match", IngestItem{CITypeName: "vm", HardwareUUID: "11111111-2222-3333-4444-555555555555"}, ReconcileCreated, "", "", 0},
		{"virtual uuid with mac matches", IngestItem{CITypeName: "vm", HardwareUUID: "11111111-2222-3333-4444-555555555555", PrimaryMAC: "00-50-56-00-00-01"}, ReconcileMatched, "vm-1", "hardware_uuid", 0},
		{"mac intersection", IngestItem{CITypeName: "switch", Fingerprint: map[string]any{"mac_addresses": []any{"02:00:00:00:00:01", "00:1B:21:00:00:10"}}}, ReconcileMatched, "mac", "mac_addresses", 0},
		{"several mac hits are ambiguous", IngestItem{CITypeName: "switch", Fingerprint: map[string]any{"mac_addresses": []any{"00:1b:21:00:00:20", "00:1b:21:00:00:21"}}}, ReconcileConflict, "", "mac_addresses", 0},
		{"vrrp mac is ignored", IngestItem{CITypeName: "router", PrimaryMAC: "00:00:5e:00:01:07"}, ReconcileCreated, "", "", 0},
		{"locally administered mac is ignored", IngestItem{CITypeName: "switch", PrimaryMAC: "02:1b:21:00:00:10"}, ReconcileCreated, "", "", 0},
		{"ip and sysObjectID in the default vrf", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1"}, ReconcileMatched, "ip", "management_ip_sys_object_id", 0},
		{"explicit default vrf equals none", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1", VRFID: "vrf-default"}, ReconcileMatched, "ip", "management_ip_sys_object_id", 0},
		{"ip in another vrf", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.6", SysObjectID: "1.3.6.1.4.1.9.1.1"}, ReconcileCreated, "", "", 0},
		{"ip in the same non-default vrf", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.6", SysObjectID: "1.3.6.1.4.1.9.1.1", VRFID: "vrf-blue"}, ReconcileMatched, "ip-vrf", "management_ip_sys_object_id", 0},
		{"ip without sysObjectID", IngestItem{CITypeName: "switch", ManagementIP: "10.1.0.5"}, ReconcileCreated, "", "", 0},
		{"hostname never merges", IngestItem{CITypeName: "server", Hostname: "WEB-01"}, ReconcileCreated, "", "", 1},
		{"fqdn never merges", IngestItem{CITypeName: "server", FQDN: "web-01.example.net"}, ReconcileCreated, "", "", 1},
		{"client binding: own client", IngestItem{CITypeName: "server", SerialNumber: "SN-100", ClientID: "c1"}, ReconcileMatched, "srv", "serial_number", 0},
		{"client binding: other client", IngestItem{CITypeName: "server", SerialNumber: "SN-300", ClientID: "c1"}, ReconcileCreated, "", "", 0},
		{"org-wide record matches any client", IngestItem{CITypeName: "server", SerialNumber: "SN-300"}, ReconcileMatched, "srv-c2", "serial_number", 0},
		{"serial before mac", IngestItem{CITypeName: "server", SerialNumber: "SN-100", PrimaryMAC: "00:1b:21:00:00:10"}, ReconcileMatched, "srv", "serial_number", 0},
	} {
		in := c.in
		got := ResolveIdentity(inventory, &in, ic)
		if got.Action != c.action || got.MatchedCIID != c.matched || got.Criterion != c.criterion || len(got.ProbableDuplicates) != c.dupes {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// TestWithoutTakenIdentity: a new CI does not take identity values another
// CI holds (they are unique per organization).
func TestWithoutTakenIdentity(t *testing.T) {
	item := IngestItem{SerialNumber: "SN-300", HardwareUUID: "AA", PrimaryMAC: "00-1b-21-00-00-10", Hostname: "h"}
	withoutTakenIdentity(&item, []ci.Item{{SerialNumber: "sn-300"}, {HardwareUUID: "aa"}, {PrimaryMAC: "00:1b:21:00:00:10"}})
	if item.SerialNumber != "" || item.HardwareUUID != "" || item.PrimaryMAC != "" || item.Hostname != "h" {
		t.Errorf("item %+v", item)
	}
}

func TestCanonicalMAC(t *testing.T) {
	for in, want := range map[string]string{
		"00:1B:21:0A:0B:0C": "00:1b:21:0a:0b:0c",
		"001b.210a.0b0c":    "00:1b:21:0a:0b:0c",
		"02:00:00:00:00:01": "", // locally administered
		"01:00:5e:00:00:01": "", // multicast
		"00:00:0c:07:ac:01": "", // HSRP
		"00:00:0c:9f:f0:01": "", // HSRPv2
		"00:00:5e:00:02:01": "", // VRRP IPv6
		"zz":                "",
	} {
		if got := canonicalMAC(in); got != want {
			t.Errorf("canonicalMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

// identityStore is an in-memory discovery repository with the candidate
// queries of the ingest, record by record (IdentityCandidateFinder,
// DeletedCandidateFinder) and batched (BatchIdentityFinder), and the type
// lookup (CITypeBatchLookup), over the CIs of a ci.MemoryRepository.
type identityStore struct {
	*MemoryRepository
	items []*ci.Item // in creation order, live and deleted
	types map[string]ResolvedType
}

func (s *identityStore) LookupCITypeIDs(_ context.Context, _ string, names []string) (map[string]ResolvedType, error) {
	out := map[string]ResolvedType{}
	for _, n := range names {
		if t, ok := s.types[n]; ok {
			out[n] = t
		}
	}
	return out, nil
}

func (s *identityStore) DefaultVRF(context.Context, string) (string, error) {
	return "vrf-default", nil
}

// identityMatches reports whether item carries value of kind as the
// candidate queries bind it.
func identityMatches(item *ci.Item, kind, value string) bool {
	switch kind {
	case keySerial:
		return strings.ToLower(strings.TrimSpace(item.SerialNumber)) == value
	case keyUUID:
		u, ok := canonicalUUID(storedUUID(item))
		return ok && u == value
	case keyMAC:
		return storedMACs(item)[value]
	case keyIP:
		return value != "" && sameIP(item.ManagementIP, value)
	case keyHost:
		return strings.EqualFold(item.Hostname, value) || strings.EqualFold(item.FQDN, value)
	}
	return false
}

func keyValues(keys *IdentityKeys) map[string][]string {
	return map[string][]string{keySerial: keys.Serials, keyUUID: keys.UUIDs, keyMAC: keys.MACs, keyIP: keys.IPs, keyHost: keys.Hostnames}
}

func (s *identityStore) IdentityHits(_ context.Context, _ string, keys *IdentityKeys) ([]IdentityHit, error) {
	var hits []IdentityHit
	for _, item := range s.items {
		for kind, values := range keyValues(keys) {
			for i, v := range values {
				if item.DeletedAt == nil && identityMatches(item, kind, v) {
					hits = append(hits, IdentityHit{CIID: item.ID, Key: IdentityKey{Kind: kind, Index: i}})
				}
			}
		}
	}
	return hits, nil
}

func (s *identityStore) DeletedIdentityHits(_ context.Context, _ string, keys *IdentityKeys) ([]DeletedIdentityHit, error) {
	var hits []DeletedIdentityHit
	for _, item := range s.items {
		for kind, values := range keyValues(keys) {
			for i, v := range values {
				if item.DeletedAt != nil && identityMatches(item, kind, v) {
					hits = append(hits, DeletedIdentityHit{CI: *item, Key: IdentityKey{Kind: kind, Index: i}})
				}
			}
		}
	}
	return hits, nil
}

// single returns the candidates of one record: the CIs matching a value of
// q alone.
func (s *identityStore) single(q *IdentityQuery, deleted bool) []*ci.Item {
	keys, _ := batchKeys([]IdentityQuery{*q}, []bool{true})
	var out []*ci.Item
	for _, item := range s.items {
		if (item.DeletedAt != nil) != deleted {
			continue
		}
		for kind, values := range keyValues(keys) {
			if slices.ContainsFunc(values, func(v string) bool { return identityMatches(item, kind, v) }) {
				out = append(out, item)
				break
			}
		}
	}
	return out
}

func (s *identityStore) IdentityCandidates(_ context.Context, _ string, q *IdentityQuery) ([]string, error) {
	var ids []string
	for _, item := range s.single(q, false) {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

func (s *identityStore) DeletedIdentityCandidates(_ context.Context, _ string, q *IdentityQuery) ([]ci.Item, error) {
	var out []ci.Item
	for _, item := range s.single(q, true) {
		out = append(out, *item)
	}
	return out, nil
}

// recordStore is identityStore without the batch queries: the facade then
// reads record by record.
type recordStore struct {
	*MemoryRepository
	s *identityStore
}

func (r recordStore) LookupCITypeIDs(ctx context.Context, orgID string, names []string) (map[string]ResolvedType, error) {
	return r.s.LookupCITypeIDs(ctx, orgID, names)
}

func (r recordStore) DefaultVRF(ctx context.Context, orgID string) (string, error) {
	return r.s.DefaultVRF(ctx, orgID)
}

func (r recordStore) IdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]string, error) {
	return r.s.IdentityCandidates(ctx, orgID, q)
}

func (r recordStore) DeletedIdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]ci.Item, error) {
	return r.s.DeletedIdentityCandidates(ctx, orgID, q)
}

// TestResolveRecordsMatchesIngest covers the identity facade of WP-302
// (REC-02, CI-13, CH29): for the same records ResolveRecords gives the
// result of the ingest's record path (identityCandidates, ResolveIdentity,
// resurrection) — a match per criterion, ambiguity, a soft-deleted CI and
// host name candidates — with the batch queries and without them.
func TestResolveRecordsMatchesIngest(t *testing.T) {
	ctx := context.Background()
	const org = "org-1"
	cis := ci.NewMemoryRepository()
	store := &identityStore{MemoryRepository: NewMemoryRepository(), types: map[string]ResolvedType{
		"server": {ID: "t-server", Active: true}, "switch": {ID: "t-switch", Active: true},
		ci.GenericDeviceType: {ID: "t-generic", Active: true}, "vm": {ID: "t-vm", Active: true},
	}}
	add := func(item ci.Item, deleted bool) string {
		item.OrganizationID = org
		if err := cis.Create(ctx, &item); err != nil {
			t.Fatal(err)
		}
		stored := item
		if deleted {
			if err := cis.Delete(ctx, org, item.ID); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			stored.DeletedAt = &now
		}
		store.items = append(store.items, &stored)
		return item.ID
	}
	srv := add(ci.Item{CITypeID: "t-server", SerialNumber: "SN-1"}, false)
	uuid := add(ci.Item{CITypeID: "t-server", HardwareUUID: "4c4c4544-0042-3510-8051-b4c04f4a4e33"}, false)
	mac := add(ci.Item{CITypeID: "t-switch", PrimaryMAC: "00:1b:21:00:00:10"}, false)
	ip := add(ci.Item{CITypeID: "t-switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1"}, false)
	add(ci.Item{CITypeID: "t-server", SerialNumber: "SN-TWIN"}, false)
	add(ci.Item{CITypeID: "t-server", SerialNumber: "SN-TWIN"}, false)
	host := add(ci.Item{CITypeID: "t-server", Hostname: "web-01"}, false)
	gone := add(ci.Item{CITypeID: "t-server", SerialNumber: "SN-GONE"}, true)

	records := []IngestItem{
		{CITypeName: "server", SerialNumber: "sn-1"},
		{CITypeName: "server", HardwareUUID: "4C4C4544-0042-3510-8051-B4C04F4A4E33"},
		{CITypeName: "switch", PrimaryMAC: "00-1B-21-00-00-10"},
		{CITypeName: "switch", ManagementIP: "10.1.0.5", SysObjectID: "1.3.6.1.4.1.9.1.1"},
		{CITypeName: "server", SerialNumber: "SN-TWIN"},
		{CITypeName: "server", Hostname: "WEB-01", SerialNumber: "SN-NEW"},
		{CITypeName: "server", SerialNumber: "SN-GONE"},
		{CITypeName: "unknown", SerialNumber: "SN-1"},
	}
	want := []struct {
		action      ReconcileAction
		matched     string
		dupes       []string
		resurrected []string
	}{
		{ReconcileMatched, srv, nil, nil},
		{ReconcileMatched, uuid, nil, nil},
		{ReconcileMatched, mac, nil, nil},
		{ReconcileMatched, ip, nil, nil},
		{ReconcileConflict, "", nil, nil},
		{ReconcileCreated, "", []string{host}, nil},
		{ReconcileCreated, "", nil, []string{gone}},
		{"", "", nil, nil},
	}

	for _, repo := range []Repository{store, recordStore{MemoryRepository: store.MemoryRepository, s: store}} {
		h := NewHandler(repo, cis)
		got, err := h.ResolveRecords(ctx, org, records)
		if err != nil {
			t.Fatal(err)
		}
		types, err := h.lookupTypes(ctx, org, frameTypeNames(records))
		if err != nil {
			t.Fatal(err)
		}
		ic, err := h.identityContext(ctx, org, types)
		if err != nil {
			t.Fatal(err)
		}
		for i := range records {
			g, w := &got[i], want[i]
			if g.Result.Action != w.action || g.Result.MatchedCIID != w.matched ||
				!slices.Equal(g.Result.ProbableDuplicates, w.dupes) || !slices.Equal(g.Resurrected, w.resurrected) {
				t.Errorf("%T, record %d: %+v, resurrected %v", repo, i, g.Result, g.Resurrected)
			}
			if w.action == "" {
				continue
			}
			// The ingest's record path for the same record.
			item := records[i]
			item.CITypeName = types[item.CITypeName].ID
			candidates, candErr := h.identityCandidates(ctx, org, &item)
			if candErr != nil {
				t.Fatal(candErr)
			}
			ingest := ResolveIdentity(candidates, &item, ic)
			if ingest.Action != g.Result.Action || ingest.MatchedCIID != g.Result.MatchedCIID || ingest.Criterion != g.Result.Criterion ||
				!slices.Equal(ingest.CandidateCIIDs, g.Result.CandidateCIIDs) || !slices.Equal(ingest.ProbableDuplicates, g.Result.ProbableDuplicates) {
				t.Errorf("%T, record %d: facade %+v, ingest %+v", repo, i, g.Result, ingest)
			}
			if ingest.Action == ReconcileCreated {
				held, _, resErr := h.resurrection(ctx, org, &item, ic)
				if resErr != nil {
					t.Fatal(resErr)
				}
				if held != (len(g.Resurrected) > 0) {
					t.Errorf("%T, record %d: ingest holds %t, facade resurrected %v", repo, i, held, g.Resurrected)
				}
			}
		}
	}
}
