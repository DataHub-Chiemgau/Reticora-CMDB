package audit

import (
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

	// First entry should have empty previous hash
	if entries[0].PreviousHash != "" {
		t.Error("expected first entry to have empty previous hash")
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
