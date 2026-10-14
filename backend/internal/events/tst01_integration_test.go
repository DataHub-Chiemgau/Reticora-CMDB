package events_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// TestTST01AuditChainAndOutbox is the audit-chain and outbox part of the
// TST-01 matrix (WP-202) against PostgreSQL: every CI mutation writes its
// audit entry and its outbox event in the same transaction, a refused
// mutation writes neither, the chain of the organization verifies after the
// mutations, and changing a stored entry breaks the chain at that entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTST01AuditChainAndOutbox(t *testing.T) {
	f := scopetest.Seed(t, "c1")
	bg := context.Background()
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	ctx := f.OrgCtx(f.OrgA)
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	counts := func(t *testing.T, ciID string) (auditRows, outboxRows int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT
			(SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id::text = $2),
			(SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND entity_id::text = $2)`,
			f.OrgA, ciID).Scan(&auditRows, &outboxRows); err != nil {
			t.Fatal(err)
		}
		return auditRows, outboxRows
	}

	item := &ci.Item{OrganizationID: f.OrgA, CITypeID: typeID, Name: "tst01-audit", Status: "active",
		SerialNumber: "TST01-AUDIT", Attributes: map[string]any{}}
	t.Run("create writes audit entry and event together", func(t *testing.T) {
		if err := repo.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		if a, o := counts(t, item.ID); a != 1 || o != 1 {
			t.Errorf("audit %d, outbox %d", a, o)
		}
	})
	t.Run("update writes audit entry and event together", func(t *testing.T) {
		status := "maintenance"
		if _, err := repo.Update(ctx, f.OrgA, item.ID, ci.UpdateRequest{Status: &status}); err != nil {
			t.Fatal(err)
		}
		if a, o := counts(t, item.ID); a != 2 || o != 2 {
			t.Errorf("audit %d, outbox %d", a, o)
		}
	})
	t.Run("a refused mutation writes neither", func(t *testing.T) {
		dup := &ci.Item{OrganizationID: f.OrgA, CITypeID: typeID, Name: "tst01-dup", Status: "active",
			SerialNumber: "TST01-AUDIT", Attributes: map[string]any{}}
		if err := repo.Create(ctx, dup); err == nil {
			t.Fatal("duplicate serial number accepted")
		}
		var auditRows, outboxRows int
		if err := f.Admin.QueryRow(bg, `SELECT
			(SELECT count(*) FROM audit_log WHERE organization_id = $1 AND changes::text LIKE '%tst01-dup%'),
			(SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND data::text LIKE '%tst01-dup%')`,
			f.OrgA).Scan(&auditRows, &outboxRows); err != nil {
			t.Fatal(err)
		}
		if auditRows != 0 || outboxRows != 0 {
			t.Errorf("refused create left audit %d, outbox %d", auditRows, outboxRows)
		}
	})
	t.Run("the chain verifies and detects a changed entry", func(t *testing.T) {
		result, err := audit.Verify(bg, f.App, f.OrgA)
		if err != nil || !result.Intact || result.Checked < 2 {
			t.Fatalf("chain: %+v %v", result, err)
		}
		var entry string
		if err = f.Admin.QueryRow(bg, `SELECT id::text FROM audit_log WHERE organization_id = $1 AND resource_id::text = $2
			ORDER BY seq LIMIT 1`, f.OrgA, item.ID).Scan(&entry); err != nil {
			t.Fatal(err)
		}
		conn, err := f.Admin.Acquire(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		// The append-only trigger is bypassed only to simulate tampering.
		if _, err = conn.Exec(bg, `SET session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		_, err = conn.Exec(bg, `UPDATE audit_log SET action = 'tampered' WHERE id = $1`, entry)
		_, _ = conn.Exec(bg, `SET session_replication_role = origin`)
		if err != nil {
			t.Fatal(err)
		}
		if result, err = audit.Verify(bg, f.App, f.OrgA); err != nil || result.Intact || result.BrokenID != entry {
			t.Errorf("after tampering: %+v %v", result, err)
		}
	})
}
