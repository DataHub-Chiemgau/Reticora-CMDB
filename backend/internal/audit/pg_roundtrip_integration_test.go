package audit_test

import (
	"context"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// These tests run against the migrated CI PostgreSQL (see the migrations CI
// job). They are skipped locally unless TEST_DATABASE_URL is set.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

// TestPGRecorderChainRoundtripPG is the regression test for the verified
// defect where server-written audit entries failed verification: the entry
// timestamp was hashed with nanosecond precision but persisted with
// microsecond precision and then re-read, so the recomputed hash diverged.
// The test records three chained entries through the real PG recorder and
// requires Verify to report an intact chain. It then tampers with an entry
// and requires Verify to detect the break.
func TestPGRecorderChainRoundtripPG(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	orgID := "33333333-3333-3333-3333-333333333333"
	if _, err := pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'audit-roundtrip', 'audit-roundtrip') ON CONFLICT (id) DO NOTHING`, orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	// Repeat runs of this test share the org; start from an empty chain.
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE organization_id = $1`, orgID); err != nil {
		t.Fatalf("reset audit log: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM organization WHERE id = $1`, orgID)
	})

	recorder := audit.NewPGRecorder()
	for i, action := range []string{"ci.created", "ci.updated", "ci.deleted"} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		_, err = recorder.Record(ctx, tx, audit.Entry{
			OrganizationID: orgID,
			ActorID:        "44444444-4444-4444-4444-444444444444",
			ActorType:      "user",
			Action:         action,
			ResourceType:   "ci",
			ResourceID:     "55555555-5555-5555-5555-555555555555",
			Changes:        map[string]interface{}{"seq": i, "note": "roundtrip"},
		})
		if err != nil {
			t.Fatalf("record %s: %v", action, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	result, err := audit.Verify(ctx, pool, orgID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Intact {
		t.Fatalf("expected intact chain, got %+v", result)
	}
	if result.Checked != 3 {
		t.Fatalf("expected 3 checked entries, got %d", result.Checked)
	}

	// Tamper with the second entry: the chain must now verify as broken at
	// exactly that entry.
	var tamperedID string
	err = pool.QueryRow(ctx, `
		UPDATE audit_log SET action = 'ci.tampered'
		WHERE organization_id = $1
		ORDER BY timestamp ASC, id ASC
		OFFSET 1 LIMIT 1
		RETURNING id::text
	`, orgID).Scan(&tamperedID)
	if err != nil {
		// OFFSET ... LIMIT in UPDATE is not supported; do it in two steps.
		err = pool.QueryRow(ctx, `SELECT id::text FROM audit_log WHERE organization_id = $1 ORDER BY timestamp ASC, id ASC OFFSET 1 LIMIT 1`, orgID).Scan(&tamperedID)
		if err != nil {
			t.Fatalf("pick tamper target: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'ci.tampered' WHERE id = $1`, tamperedID); err != nil {
			t.Fatalf("tamper: %v", err)
		}
	}

	result, err = audit.Verify(ctx, pool, orgID)
	if err != nil {
		t.Fatalf("verify after tamper: %v", err)
	}
	if result.Intact {
		t.Fatal("expected broken chain after tampering")
	}
	if result.BrokenID != tamperedID {
		t.Fatalf("expected broken_id %s, got %s (result %+v)", tamperedID, result.BrokenID, result)
	}
}
