package discovery

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// TestCanonicalUUID covers WP-333: the batch keys a hardware UUID exactly
// when PostgreSQL reads it as a uuid, in one spelling.
func TestCanonicalUUID(t *testing.T) {
	const want = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	for _, in := range []string{
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11",
		"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}", "a0eebc999c0b4ef8bb6d6bb9bd380a11",
		"a0ee-bc99-9c0b-4ef8-bb6d-6bb9-bd38-0a11",
	} {
		if got, ok := canonicalUUID(in); !ok || got != want {
			t.Errorf("canonicalUUID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// Each of these is refused by uuid_in as well.
	for _, in := range []string{
		"", " a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11 ",
		"a0eebc99--9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11-",
		"a0eeb-c99-9c0b-4ef8-bb6d-6bb9bd380a11", "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"urn:uuid:a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "-a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1", "{}", "{",
	} {
		if got, ok := canonicalUUID(in); ok {
			t.Errorf("canonicalUUID(%q) = %q, want refused", in, got)
		}
	}
	if !batchable(&IdentityQuery{}) || !batchable(&IdentityQuery{HardwareUUID: "{A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11}"}) ||
		batchable(&IdentityQuery{HardwareUUID: "not-a-uuid"}) {
		t.Error("batchable: a record without UUID or with one PostgreSQL reads is batchable, another is not")
	}
}

// TestBatchKeys covers WP-333: each value once per kind, every record with
// references to its values, nothing for records left out or without keys.
func TestBatchKeys(t *testing.T) {
	qs := []IdentityQuery{
		{SerialNumber: "SN-1", HardwareUUID: "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", MACs: []string{"00:1b:21:00:00:01"},
			ManagementIP: "192.0.2.1/24", Hostnames: []string{"web-1", "web-1.example.test"}},
		{SerialNumber: "SN-2", HardwareUUID: "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}", ManagementIP: "192.0.2.1"},
		{},
		{SerialNumber: "SN-3"},
	}
	keys, refs := batchKeys(qs, []bool{true, true, true, false})
	want := &IdentityKeys{
		Serials: []string{"SN-1", "SN-2"}, UUIDs: []string{"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		MACs: []string{"00:1b:21:00:00:01"}, IPs: []string{"192.0.2.1"}, Hostnames: []string{"web-1", "web-1.example.test"},
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys %+v, want %+v", keys, want)
	}
	wantRefs := [][]IdentityKey{
		{{keySerial, 0}, {keyUUID, 0}, {keyMAC, 0}, {keyIP, 0}, {keyHost, 0}, {keyHost, 1}},
		{{keySerial, 1}, {keyUUID, 0}, {keyIP, 0}},
		nil, nil,
	}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("refs %v, want %v", refs, wantRefs)
	}
	if keys, _ := batchKeys(qs[2:], []bool{true, false}); !keys.empty() {
		t.Errorf("keys of a record without values and a left out one: %+v", keys)
	}
}

// TestAssignCandidates covers WP-333: a record gets each CI matching one of
// its values once, in the order of the hits (created_at, id), as the query
// of the record alone returns them.
func TestAssignCandidates(t *testing.T) {
	refs := [][]IdentityKey{
		{{keySerial, 0}, {keyIP, 0}},
		{{keyIP, 0}},
		nil,
		{{keyHost, 0}},
	}
	hits := []IdentityHit{
		{"ci-1", IdentityKey{keyIP, 0}},
		{"ci-2", IdentityKey{keySerial, 0}},
		{"ci-2", IdentityKey{keyIP, 0}},
		{"ci-3", IdentityKey{keyMAC, 0}},
		{"ci-4", IdentityKey{keyIP, 0}},
	}
	got := assignCandidates(refs, hits)
	want := [][]string{{"ci-1", "ci-2", "ci-4"}, {"ci-1", "ci-2", "ci-4"}, nil, nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates %v, want %v", got, want)
	}
}

// TestAssignDeleted covers WP-333: the soft-deleted CIs of a record in the
// order of the hits, each once, at most deletedCandidateLimit.
func TestAssignDeleted(t *testing.T) {
	refs := [][]IdentityKey{{{keyIP, 0}, {keySerial, 0}}, {{keySerial, 0}}}
	var hits []DeletedIdentityHit
	for i := range deletedCandidateLimit + 5 {
		hits = append(hits, DeletedIdentityHit{CI: ci.Item{ID: "gone-" + strconv.Itoa(i)}, Key: IdentityKey{keyIP, 0}})
	}
	hits = append([]DeletedIdentityHit{
		{CI: ci.Item{ID: "gone-0"}, Key: IdentityKey{keySerial, 0}},
	}, hits...)
	got := assignDeleted(refs, hits)
	if len(got[0]) != deletedCandidateLimit || got[0][0].ID != "gone-0" || got[0][1].ID != "gone-1" ||
		got[0][deletedCandidateLimit-1].ID != "gone-"+strconv.Itoa(deletedCandidateLimit-1) {
		t.Errorf("record 0: %d deleted candidates, first %v", len(got[0]), got[0][:2])
	}
	if len(got[1]) != 1 || got[1][0].ID != "gone-0" {
		t.Errorf("record 1: %v, want gone-0", got[1])
	}
}

// TestIdentityClusters covers WP-333: records that share a normalized
// identity value or a candidate CI form one group, whose first record alone
// may use the batch; records left out belong to none.
func TestIdentityClusters(t *testing.T) {
	qs := []IdentityQuery{
		{SerialNumber: "SN-A"},
		{SerialNumber: " sn-a "},        // same serial
		{ManagementIP: "192.0.2.10/24"}, // same IP as 3
		{ManagementIP: "192.0.2.10"},    //
		{HardwareUUID: "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"},
		{HardwareUUID: "a0eebc999c0b4ef8bb6d6bb9bd380a11"}, // same UUID
		{Hostnames: []string{"db-1"}},
		{MACs: []string{"00:1b:21:00:00:01"}}, // same candidate as 6
		{SerialNumber: "SN-B"},
		{SerialNumber: "SN-A"}, // left out
	}
	candidates := make([][]string, len(qs))
	candidates[6], candidates[7] = []string{"CI-1"}, []string{"ci-1"}
	include := []bool{true, true, true, true, true, true, true, true, true, false}
	got := identityClusters(qs, include, candidates)
	want := []bool{true, false, true, false, true, false, true, false, true, false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("first %v, want %v", got, want)
	}
	// A later record that links two groups joins them: the first record of
	// the first group alone is first.
	chain := []IdentityQuery{{SerialNumber: "X"}, {ManagementIP: "192.0.2.1"}, {SerialNumber: "X", ManagementIP: "192.0.2.1"}}
	if got := identityClusters(chain, []bool{true, true, true}, make([][]string, 3)); !reflect.DeepEqual(got, []bool{true, false, false}) {
		t.Errorf("chain: %v", got)
	}
}

// TestCloneItem covers WP-333: the batch changes a copy; the record keeps
// what it reported for the path record by record.
func TestCloneItem(t *testing.T) {
	item := IngestItem{SerialNumber: "SN-1", Attributes: map[string]any{"rack": "R1"},
		Fingerprint: map[string]any{"hardware_uuid": "x"}, RawData: map[string]any{"sys_descr": "d"}}
	c := cloneItem(&item)
	delete(c.Attributes, "rack")
	c.Fingerprint["hardware_uuid"] = "y"
	c.RawData["sys_descr"] = "e"
	if item.Attributes["rack"] != "R1" || item.Fingerprint["hardware_uuid"] != "x" || item.RawData["sys_descr"] != "d" {
		t.Errorf("the record changed with its copy: %+v", item)
	}
}

// countingRules counts the field resolutions of the observed-value check.
type countingRules struct {
	resolved map[string]int
	checked  int
}

func (c *countingRules) InvalidObserved(context.Context, string, string, string, map[string]any) ([]string, error) {
	c.checked++
	return nil, nil
}

func (c *countingRules) ObservedRules(_ context.Context, _, ciTypeID string) (func(map[string]any) []string, error) {
	c.resolved[ciTypeID]++
	return func(values map[string]any) []string {
		if _, ok := values["bad"]; ok {
			return []string{"bad"}
		}
		return nil
	}, nil
}

// TestObservedMemo covers WP-333: the new CIs of a batch resolve the field
// rules once per CI type; an existing CI is checked as before.
func TestObservedMemo(t *testing.T) {
	src := &countingRules{resolved: map[string]int{}}
	h := &Handler{observed: src}
	v := h.batchValidator()
	for range 3 {
		for _, typeID := range []string{"server", "switch"} {
			item := IngestItem{Name: "n", Attributes: map[string]any{"bad": 1, "ok": 2}}
			dropped, err := dropInvalidWith(context.Background(), v, "org", typeID, "", &item)
			if err != nil || dropped != 1 || item.Attributes["bad"] != nil || item.Attributes["ok"] != 2 {
				t.Fatalf("%s: dropped %d, err %v, attributes %v", typeID, dropped, err, item.Attributes)
			}
		}
	}
	if !reflect.DeepEqual(src.resolved, map[string]int{"server": 1, "switch": 1}) || src.checked != 0 {
		t.Errorf("rules resolved %v and %d checks of their own, want once per type and none", src.resolved, src.checked)
	}
	item := IngestItem{Name: "n"}
	if _, err := dropInvalidWith(context.Background(), v, "org", "server", "ci-1", &item); err != nil || src.checked != 1 {
		t.Errorf("existing CI: %d checks of their own (%v), want 1", src.checked, err)
	}
}
