package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"testing"
	"time"
)

func TestAuditHashChain(t *testing.T) {
	log := NewLog()

	log.Append(&Entry{
		ID:             "1",
		OrganizationID: "org-1",
		Timestamp:      time.Now(),
		ActorID:        "user-1",
		Action:         "create",
		ResourceType:   "ci",
		ResourceID:     "ci-100",
	})

	log.Append(&Entry{
		ID:             "2",
		OrganizationID: "org-1",
		Timestamp:      time.Now(),
		ActorID:        "user-2",
		Action:         "update",
		ResourceType:   "ci",
		ResourceID:     "ci-100",
		Changes:        map[string]interface{}{"status": "active"},
	})

	if !log.Verify() {
		t.Error("expected hash chain to be valid")
	}

	entries := log.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// The first entry links to the genesis hash (32 zero bytes).
	if entries[0].PreviousHash != GenesisHash || entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Errorf("chain start: previous %q, seq %d/%d", entries[0].PreviousHash, entries[0].Seq, entries[1].Seq)
	}

	// Second entry should chain from first
	if entries[1].PreviousHash != entries[0].Hash {
		t.Error("expected second entry to chain from first")
	}
}

func TestAuditTamperDetection(t *testing.T) {
	log := NewLog()

	log.Append(&Entry{
		ID:             "1",
		OrganizationID: "org-1",
		Timestamp:      time.Now(),
		ActorID:        "user-1",
		Action:         "create",
		ResourceType:   "ci",
		ResourceID:     "ci-100",
	})

	log.Append(&Entry{
		ID:             "2",
		OrganizationID: "org-1",
		Timestamp:      time.Now(),
		ActorID:        "user-2",
		Action:         "delete",
		ResourceType:   "ci",
		ResourceID:     "ci-100",
	})

	// Tamper with first entry
	log.entries[0].Action = "tampered"

	if log.Verify() {
		t.Error("expected tampered chain to be invalid")
	}
}

func TestVerifyEntriesReportsBrokenLink(t *testing.T) {
	log := NewLog()
	now := time.Now()
	log.Append(&Entry{ID: "1", OrganizationID: "org-1", Timestamp: now, ActorID: "user-1", Action: "create", ResourceType: "ci", ResourceID: "ci-100"})
	log.Append(&Entry{ID: "2", OrganizationID: "org-1", Timestamp: now.Add(time.Second), ActorID: "user-1", Action: "update", ResourceType: "ci", ResourceID: "ci-100"})

	entries := log.Entries()
	entries[1].PreviousHash = "broken"

	result := VerifyEntries(entries)
	if result.Intact {
		t.Fatal("expected chain to be invalid")
	}
	if result.BrokenID != "2" || result.BrokenAt != 2 {
		t.Fatalf("expected second entry to be reported broken, got id=%q at=%d", result.BrokenID, result.BrokenAt)
	}
}

// TestChainFormulaAndSequence covers AUD-04 (WP-132): the hash is
// SHA-256(previous_hash || SHA-256(canonical JSON)) over the binary values,
// the canonical JSON carries organization and sequence, and verification
// reports the first gap or reordering in the sequence. The changes are in
// their jsonb text form, as the database hashes them (WP-423).
func TestChainFormulaAndSequence(t *testing.T) {
	ts := time.Date(2026, 10, 13, 12, 0, 0, 123456789, time.UTC)
	e := Entry{OrganizationID: "org-1", Seq: 1, Timestamp: ts, ActorID: "u-1", ActorType: "user", Action: "ci.created",
		ResourceType: "ci", ResourceID: "ci-1", Changes: map[string]interface{}{"name": "a"}, PreviousHash: GenesisHash}
	canonical := `{"organization_id":"org-1","seq":1,"timestamp":"2026-10-13T12:00:00.123456Z","actor_id":"u-1",` +
		`"actor_type":"user","action":"ci.created","resource_type":"ci","resource_id":"ci-1","changes":{"name": "a"},` +
		`"source":"system","request_id":"","correlation_id":""}`
	inner := sha256.Sum256([]byte(canonical))
	want := sha256.Sum256(append(make([]byte, 32), inner[:]...))
	if got := ComputeHash(&e); got != hex.EncodeToString(want[:]) {
		t.Errorf("hash %s, want %s", got, hex.EncodeToString(want[:]))
	}
	moved := e
	moved.Seq = 2
	other := e
	other.OrganizationID = "org-2"
	if ComputeHash(&moved) == ComputeHash(&e) || ComputeHash(&other) == ComputeHash(&e) {
		t.Error("sequence and organization do not enter the hash")
	}

	log := NewLog()
	for i := range 4 {
		log.Append(&Entry{ID: string(rune('a' + i)), OrganizationID: "org-1", Timestamp: ts, Action: "x", ResourceType: "ci"})
	}
	entries := log.Entries()
	if r := VerifyEntries(entries); !r.Intact || r.Checked != 4 {
		t.Fatalf("intact chain: %+v", r)
	}
	// A removed entry leaves a gap.
	gap := append(append([]Entry{}, entries[:1]...), entries[2:]...)
	if r := VerifyEntries(gap); r.Intact || r.BrokenReason != "sequence gap" || r.BrokenID != "c" || r.BrokenAt != 2 || r.Checked != 1 {
		t.Errorf("gap: %+v", r)
	}
	// A wrong genesis.
	first := append([]Entry{}, entries...)
	first[0].PreviousHash = ""
	if r := VerifyEntries(first); r.Intact || r.BrokenReason != "previous_hash mismatch" || r.BrokenAt != 1 {
		t.Errorf("genesis: %+v", r)
	}
}

// TestCanonicalJSONIsJSONBText covers AUD-04 (WP-423): ComputeHash hashes
// the canonical JSON exactly as audit_entry_hash builds it, with the changes
// as jsonb::text prints them (keys by byte length, then bytewise; ", " and
// ": "; escape_json; numeric) and every string escaped like
// to_jsonb(text)::text: quotes, backslashes and control characters escaped,
// <, >, &, U+2028 and non-ASCII unchanged.
func TestCanonicalJSONIsJSONBText(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"key order", map[string]any{"bb": 1, "a": 2, "ccc": 3, "Z": 4, "é": 5},
			`{"Z": 4, "a": 2, "bb": 1, "é": 5, "ccc": 3}`},
		{"nested", map[string]any{"before": map[string]any{"name": "sw1", "description": "Zeile 1\nZeile 2"},
			"after": map[string]any{"name": "sw1b", "tags": []any{"a", nil, true, map[string]any{}, []any{}}}},
			`{"after": {"name": "sw1b", "tags": ["a", null, true, {}, []]}, "before": {"name": "sw1", "description": "Zeile 1\nZeile 2"}}`},
		{"escapes", map[string]any{"s": "q\"b\\n\nt\tr\rb\bf\f\x01\x1f\x7f<>& ü😀"},
			"{\"s\": \"q\\\"b\\\\n\\nt\\tr\\rb\\bf\\f\\u0001\\u001f\x7f<>& ü😀\"}"},
		{"numbers", map[string]any{"n": []any{1, -7, 1.5, 100.0, 1e21, 1.5e-7, -1.25e-3, math.Copysign(0, -1), 0.30000000000000004, int64(1) << 60}},
			`{"n": [1, -7, 1.5, 100, 1000000000000000000000, 0.00000015, -0.00125, 0, 0.30000000000000004, 1152921504606846976]}`},
	} {
		if got, err := jsonbText(tc.in); err != nil || got != tc.want {
			t.Errorf("%s: %s %v, want %s", tc.name, got, err, tc.want)
		}
	}
	for in, want := range map[string]string{
		"1.50": "1.50", "-0": "0", "-0.0": "0.0", "1E+2": "100", "0.000": "0.000", "12345678901234567890123": "12345678901234567890123",
		"1e-2": "0.01", "-5e0": "-5", "2.5e1": "25", "1e999999999": "1e999999999",
	} {
		if got := numericText(in); got != want {
			t.Errorf("numeric %s: %s, want %s", in, got, want)
		}
	}
	var sb strings.Builder
	writeJSONBString(&sb, "r\"x\\y\n")
	if sb.String() != `"r\"x\\y\n"` {
		t.Errorf("string: %s", sb.String())
	}
}

// TestChainAfterRetention covers AUD-09 (WP-135): a chain whose oldest
// entries a retention run removed verifies from the anchor its
// audit.retention entry records; without the anchor the start is a gap.
func TestChainAfterRetention(t *testing.T) {
	log := NewLog()
	ts := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for i := range 4 {
		log.Append(&Entry{ID: string(rune('a' + i)), OrganizationID: "org-1", Timestamp: ts, Action: "x", ResourceType: "ci"})
	}
	all := log.Entries()
	log.Append(&Entry{ID: "r", OrganizationID: "org-1", Timestamp: ts, Action: RetentionAction, ResourceType: "audit_log",
		Changes: map[string]interface{}{"deleted": float64(2), "deleted_through_seq": float64(2), "anchor_hash": all[1].Hash}})
	rest := log.Entries()[2:]
	if r := VerifyEntries(rest); !r.Intact || r.Checked != 3 {
		t.Errorf("chain after retention: %+v", r)
	}
	if r := VerifyEntries(rest[:2]); r.Intact || r.BrokenReason != "sequence gap" || r.BrokenAt != 1 {
		t.Errorf("without the anchor: %+v", r)
	}
	forged := append([]Entry{}, rest...)
	forged[2].Changes = map[string]interface{}{"deleted_through_seq": float64(2), "anchor_hash": GenesisHash}
	if r := VerifyEntries(forged); r.Intact {
		t.Errorf("a wrong anchor verifies: %+v", r)
	}
}
