package audit_test

import (
	"context"
	"fmt"
	"os"
	"sync"
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
	// The audit chain fixture rewrites and deletes audit rows, which the
	// restricted application role is not permitted to do (migration 000056).
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Registered first so it runs last (t.Cleanup is LIFO) and the trigger
	// guards are re-enabled before the pool goes away.
	t.Cleanup(pool.Close)

	orgID := "33333333-3333-3333-3333-333333333333"
	if _, err := pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'audit-roundtrip', 'audit-roundtrip') ON CONFLICT (id) DO NOTHING`, orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	// audit_log is append-only (migration 000056). This fixture needs to reset
	// and later tamper with the chain, which it does under
	// session_replication_role = replica: that skips triggers for this session
	// only, so it cannot race with other packages sharing the database.
	unguarded := func(sql string, args ...any) error {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
			return err
		}
		defer conn.Exec(ctx, `SET session_replication_role = origin`)
		_, err = conn.Exec(ctx, sql, args...)
		return err
	}
	// Repeat runs of this test share the org; start from an empty chain.
	if err := unguarded(`DELETE FROM audit_log WHERE organization_id = $1`, orgID); err != nil {
		t.Fatalf("reset audit log: %v", err)
	}
	t.Cleanup(func() {
		unguarded(`DELETE FROM audit_log WHERE organization_id = $1`, orgID)
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
	// exactly that entry. PostgreSQL does not support ORDER BY/OFFSET in an
	// UPDATE, so the target row is selected first, then updated.
	var tamperedID string
	err = pool.QueryRow(ctx, `SELECT id::text FROM audit_log WHERE organization_id = $1 AND seq = 2`, orgID).Scan(&tamperedID)
	if err != nil {
		t.Fatalf("pick tamper target: %v", err)
	}
	if err := unguarded(`UPDATE audit_log SET action = 'ci.tampered' WHERE id = $1`, tamperedID); err != nil {
		t.Fatalf("tamper: %v", err)
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

// TestAuditChainPerOrganizationPG covers AUD-04 (WP-132) against
// PostgreSQL: every organization has its own chain from seq 1 with the
// genesis hash; concurrent writers append without a gap or fork; the hash
// the database stores equals ComputeHash of the stored entry; verification
// reports a removed entry as a sequence gap; migration 000117 re-chains
// existing entries per organization.
func TestAuditChainPerOrganizationPG(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	pool, connErr := database.NewMaintenancePool(ctx, dsn)
	if connErr != nil {
		t.Fatalf("connect: %v", connErr)
	}
	t.Cleanup(pool.Close)
	unguarded := func(sql string, args ...any) error {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		if _, setErr := conn.Exec(ctx, `SET session_replication_role = replica`); setErr != nil {
			return setErr
		}
		defer conn.Exec(ctx, `SET session_replication_role = origin`)
		_, err = conn.Exec(ctx, sql, args...)
		return err
	}
	orgs := []string{"66666666-6666-6666-6666-666666666601", "66666666-6666-6666-6666-666666666602"}
	for _, org := range orgs {
		if _, err := pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1::uuid, $1::text, $1::text) ON CONFLICT (id) DO NOTHING`, org); err != nil {
			t.Fatalf("insert org: %v", err)
		}
		if err := unguarded(`DELETE FROM audit_log WHERE organization_id = $1`, org); err != nil {
			t.Fatalf("reset audit log: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, org := range orgs {
			_ = unguarded(`DELETE FROM audit_log WHERE organization_id = $1`, org)
			_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE id = $1`, org)
		}
	})

	// Concurrent writers in both organizations.
	recorder := audit.NewPGRecorder()
	const perOrg = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2*perOrg)
	for _, org := range orgs {
		for i := range perOrg {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tx, err := pool.Begin(ctx)
				if err != nil {
					errs <- err
					return
				}
				defer func() { _ = tx.Rollback(ctx) }()
				if _, err = tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", org); err != nil {
					errs <- err
					return
				}
				if _, err = recorder.Record(ctx, tx, audit.Entry{OrganizationID: org, Action: "ci.updated",
					ResourceType: "ci", Changes: map[string]interface{}{"n": i}}); err != nil {
					errs <- err
					return
				}
				errs <- tx.Commit(ctx)
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent record: %v", err)
		}
	}
	for _, org := range orgs {
		result, err := audit.Verify(ctx, pool, org)
		if err != nil || !result.Intact || result.Checked != perOrg {
			t.Fatalf("org %s after concurrent writes: %+v %v", org, result, err)
		}
		var first string
		var maxSeq, distinct int
		if err := pool.QueryRow(ctx, `SELECT (SELECT previous_hash FROM audit_log WHERE organization_id = $1 AND seq = 1),
			max(seq), count(DISTINCT previous_hash) FROM audit_log WHERE organization_id = $1`, org).Scan(&first, &maxSeq, &distinct); err != nil {
			t.Fatal(err)
		}
		if first != audit.GenesisHash || maxSeq != perOrg || distinct != perOrg {
			t.Errorf("org %s: genesis %q, max seq %d, distinct links %d (a fork repeats a link)", org, first, maxSeq, distinct)
		}
	}

	// The stored hash is the Go formula over the stored entry. The database
	// hashes changes in its jsonb text form, which equals Go's compact JSON
	// only for an empty payload; stored rows are therefore verified with
	// audit_entry_hash.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgs[0]); err != nil {
		t.Fatal(err)
	}
	stored, err := recorder.Record(ctx, tx, audit.Entry{OrganizationID: orgs[0], ActorID: "44444444-4444-4444-4444-444444444444",
		ActorType: "user", Action: "ci.created", ResourceType: "ci", ResourceID: "55555555-5555-5555-5555-555555555555"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if stored.Seq != perOrg+1 || audit.ComputeHash(stored) != stored.Hash {
		t.Errorf("Go/SQL parity: seq %d, Go %s, stored %s", stored.Seq, audit.ComputeHash(stored), stored.Hash)
	}

	// A removed entry is a gap at its position.
	var removed string
	if err = pool.QueryRow(ctx, `SELECT id::text FROM audit_log WHERE organization_id = $1 AND seq = 7`, orgs[1]).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if err = unguarded(`DELETE FROM audit_log WHERE id = $1`, removed); err != nil {
		t.Fatal(err)
	}
	result, err := audit.Verify(ctx, pool, orgs[1])
	if err != nil || result.Intact || result.BrokenReason != "sequence gap" || result.BrokenAt != 7 || result.Checked != 6 {
		t.Errorf("gap: %+v %v", result, err)
	}

	// Migration 000117 re-chains existing entries: back to the former
	// chain, add entries, forward again.
	// 000118 (audit fields, WP-133) sits on top and is undone and redone
	// around it.
	read := func(name string) string {
		b, readErr := os.ReadFile("../../migrations/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(b)
	}
	mtx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mtx.Rollback(ctx) }()
	for _, name := range []string{"000118_audit_fields.down.sql", "000117_audit_chain_per_org.down.sql"} {
		if _, err = mtx.Exec(ctx, read(name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for i := range 3 {
		if _, err = mtx.Exec(ctx, `INSERT INTO audit_log (organization_id, timestamp, action, resource_type, previous_hash, hash)
			VALUES ($1, now() + $2::interval, 'legacy', 'ci', '', '')`, orgs[0], fmt.Sprintf("%d minutes", i+1)); err != nil {
			t.Fatalf("legacy entry: %v", err)
		}
	}
	for _, name := range []string{"000117_audit_chain_per_org.up.sql", "000118_audit_fields.up.sql"} {
		if _, err = mtx.Exec(ctx, read(name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var legacy, chained int
	if err = mtx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'legacy'),
		count(*) FILTER (WHERE hash = audit_entry_hash(organization_id, seq, timestamp, actor_id, actor_type, action,
			resource_type, resource_id, changes, source, request_id, correlation_id, previous_hash))
		FROM audit_log WHERE organization_id = $1`, orgs[0]).Scan(&legacy, &chained); err != nil {
		t.Fatal(err)
	}
	var maxSeq int
	if err = mtx.QueryRow(ctx, `SELECT max(seq) FROM audit_log WHERE organization_id = $1`, orgs[0]).Scan(&maxSeq); err != nil {
		t.Fatal(err)
	}
	if legacy != 3 || chained != perOrg+4 || maxSeq != perOrg+4 {
		t.Errorf("after re-chaining: legacy %d, chained %d, max seq %d", legacy, chained, maxSeq)
	}
}
