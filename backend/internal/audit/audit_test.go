package audit

import (
	"crypto/sha256"
	"encoding/hex"
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
// reports the first gap or reordering in the sequence.
func TestChainFormulaAndSequence(t *testing.T) {
	ts := time.Date(2026, 10, 13, 12, 0, 0, 123456789, time.UTC)
	e := Entry{OrganizationID: "org-1", Seq: 1, Timestamp: ts, ActorID: "u-1", ActorType: "user", Action: "ci.created",
		ResourceType: "ci", ResourceID: "ci-1", Changes: map[string]interface{}{"name": "a"}, PreviousHash: GenesisHash}
	canonical := `{"organization_id":"org-1","seq":1,"timestamp":"2026-10-13T12:00:00.123456Z","actor_id":"u-1",` +
		`"actor_type":"user","action":"ci.created","resource_type":"ci","resource_id":"ci-1","changes":{"name":"a"},` +
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
