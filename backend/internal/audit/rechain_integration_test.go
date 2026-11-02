package audit_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// hashFixMigration is the migration that hashes the canonical JSON as UTF-8.
const hashFixMigration = "000158_audit_hash_utf8"

// TestAuditRechainAfterHashFix covers migration 000158 (AUD-04, AUD-09,
// WP-423) in a database of its own, migrated up to the version before it.
// Chains written with the former hash keep their hashes unless they hold a
// backslash; such a chain is re-chained from its first differing entry and
// verifies afterwards (audit.Verify, cmd/audit-verify exit 0), also after a
// retention run, where the re-chain starts at the first remaining entry with
// prev_hash = anchor_hash. A chain tampered before the migration stays
// broken at the tampered entry, entries the former hash could not chain do
// not stop the migration, and an entry manipulated after it is reported
// (exit 1). Down restores the former hashes, the second up the new ones.
func TestAuditRechainAfterHashFix(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	dbURL, pool := rechainDatabase(ctx, t, dsn)
	verifier := auditVerifyBinary(t)

	org := func(n int) string { return fmt.Sprintf("42300000-0000-4000-8000-0000000001%02d", n) }
	backslash, plain, purged, tampered, corrupt, notHex := org(1), org(2), org(3), org(4), org(5), org(6)
	for _, id := range []string{backslash, plain, purged, tampered, corrupt, notHex} {
		if _, err := pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1::uuid, $1::text, $1::text)`, id); err != nil {
			t.Fatalf("insert org: %v", err)
		}
	}
	recorder := audit.NewPGRecorder()
	record := func(orgID string, e audit.Entry) *audit.Entry {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
			t.Fatal(err)
		}
		e.OrganizationID, e.ResourceType = orgID, "ci"
		if e.Action == "" {
			e.Action = "ci.updated"
		}
		stored, err := recorder.Record(ctx, tx, e)
		if err != nil {
			t.Fatalf("record in %s: %v", orgID, err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return stored
	}
	unguarded := func(sql string, args ...any) {
		t.Helper()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		if _, err = conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = conn.Exec(ctx, `SET session_replication_role = origin`) }()
		if _, err = conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	n := func(i int) map[string]interface{} { return map[string]interface{}{"n": i} }

	// Written with the former hash (::bytea cast, migration 000118).
	record(backslash, audit.Entry{Changes: n(1)})
	record(backslash, audit.Entry{Changes: map[string]interface{}{"name": `C:\Racks`}})
	record(backslash, audit.Entry{Changes: n(3)})
	record(backslash, audit.Entry{Changes: n(4), RequestID: `req\4`})
	for i := 1; i <= 3; i++ {
		record(plain, audit.Entry{Changes: n(i)})
	}
	old := time.Now().AddDate(-11, 0, 0)
	for i := 1; i <= 3; i++ {
		record(purged, audit.Entry{Changes: n(i), Timestamp: old})
	}
	record(purged, audit.Entry{Changes: map[string]interface{}{"name": `a\b`}})
	ptx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ptx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", purged); err != nil {
		t.Fatal(err)
	}
	if removed, purgeErr := audit.Purge(ctx, ptx, purged, time.Now().AddDate(-10, 0, 0)); purgeErr != nil || removed != 3 {
		t.Fatalf("retention run: removed %d, %v", removed, purgeErr)
	}
	if err = ptx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	record(purged, audit.Entry{Changes: n(6)})
	record(tampered, audit.Entry{Changes: n(1)})
	record(tampered, audit.Entry{Changes: map[string]interface{}{"name": `x\y`}})
	record(tampered, audit.Entry{Changes: n(3)})
	record(tampered, audit.Entry{Changes: n(4)})
	unguarded(`UPDATE audit_log SET action = 'ci.tampered' WHERE organization_id = $1 AND seq = 3`, tampered)
	first := record(corrupt, audit.Entry{Changes: n(1)})
	// Entries the former hash never chained: a quote, a hash that is no hash,
	// a previous hash that is not hex.
	unguarded(`INSERT INTO audit_log (organization_id, seq, action, resource_type, changes, previous_hash, hash)
		VALUES ($1, 2, 'ci.updated', 'ci', '{"name": "R \"7\""}', $2, 'bogus')`, corrupt, first.Hash)
	unguarded(`INSERT INTO audit_log (organization_id, seq, action, resource_type, changes, previous_hash, hash)
		VALUES ($1, 1, 'ci.updated', 'ci', '{"name": "a\\b"}', 'not hex', 'bogus')`, notHex)

	former := chainHashes(ctx, t, pool)
	for _, id := range []string{backslash, plain, purged} {
		if r, verifyErr := audit.Verify(ctx, pool, id); verifyErr != nil || !r.Intact {
			t.Fatalf("org %s before the migration: %+v %v", id, r, verifyErr)
		}
	}

	applyMigration(ctx, t, pool, hashFixMigration+".up.sql")
	fixed := chainHashes(ctx, t, pool)
	checkRechained := func(when string, got map[string][2]string) {
		t.Helper()
		entries := map[string][]audit.Entry{}
		for _, id := range []string{backslash, plain, purged, tampered, corrupt, notHex} {
			entries[id] = readEntries(ctx, t, pool, id)
		}
		changed := func(orgID string, seq int64) bool {
			for _, e := range entries[orgID] {
				if e.Seq == seq {
					return got[e.ID][1] != former[e.ID][1]
				}
			}
			t.Fatalf("%s: org %s has no seq %d", when, orgID, seq)
			return false
		}
		// The backslash chain from its first backslash entry on.
		for seq, want := range map[int64]bool{1: false, 2: true, 3: true, 4: true} {
			if changed(backslash, seq) != want {
				t.Errorf("%s: backslash chain seq %d changed %v, want %v", when, seq, !want, want)
			}
		}
		for _, e := range entries[plain] {
			if got[e.ID] != former[e.ID] {
				t.Errorf("%s: chain without a backslash changed at seq %d", when, e.Seq)
			}
		}
		// The purged chain from its first remaining entry, linked to the anchor.
		rest := entries[purged]
		if len(rest) != 3 {
			t.Fatalf("%s: purged chain %+v", when, rest)
		}
		anchor, _ := rest[1].Changes["anchor_hash"].(string)
		if rest[0].Seq != 4 || rest[1].Action != audit.RetentionAction || anchor == "" ||
			rest[0].PreviousHash != anchor || !changed(purged, 4) || !changed(purged, 5) || !changed(purged, 6) {
			t.Errorf("%s: purged chain %+v", when, rest)
		}
		// A tampered entry ends the re-chain: it and its successors keep the
		// former hashes, so the break is still reported where it is.
		for seq, want := range map[int64]bool{1: false, 2: true, 3: false, 4: false} {
			if changed(tampered, seq) != want {
				t.Errorf("%s: tampered chain seq %d changed %v, want %v", when, seq, !want, want)
			}
		}
		if r, verifyErr := audit.Verify(ctx, pool, tampered); verifyErr != nil || r.Intact || r.BrokenAt != 3 {
			t.Errorf("%s: tampered chain verifies as %+v %v, want broken at seq 3", when, r, verifyErr)
		}
		for _, id := range []string{corrupt, notHex} {
			for _, e := range entries[id] {
				if got[e.ID] != former[e.ID] {
					t.Errorf("%s: unchained entry of %s changed at seq %d", when, id, e.Seq)
				}
			}
		}
		for _, id := range []string{backslash, plain, purged} {
			if r, verifyErr := audit.Verify(ctx, pool, id); verifyErr != nil || !r.Intact || r.Checked != len(entries[id]) {
				t.Errorf("%s: org %s verifies as %+v %v", when, id, r, verifyErr)
			}
			for _, e := range entries[id] {
				if audit.ComputeHash(&e) != e.Hash {
					t.Errorf("%s: org %s seq %d: stored hash is not ComputeHash", when, id, e.Seq)
				}
			}
			if code, out := runAuditVerify(t, verifier, dbURL, id); code != 0 ||
				!strings.Contains(out, fmt.Sprintf("audit chain intact for organization %s (%d entries verified)", id, len(entries[id]))) {
				t.Errorf("%s: audit-verify -org %s: exit %d\n%s", when, id, code, out)
			}
		}
	}
	checkRechained("after up", fixed)

	// Down/up roundtrip: down re-chains back to the former hashes.
	applyMigration(ctx, t, pool, hashFixMigration+".down.sql")
	if got := chainHashes(ctx, t, pool); !sameHashes(got, former) {
		t.Errorf("after down: the former hashes are not restored")
	}
	for _, id := range []string{backslash, plain, purged} {
		if r, verifyErr := audit.Verify(ctx, pool, id); verifyErr != nil || !r.Intact {
			t.Errorf("after down: org %s verifies as %+v %v", id, r, verifyErr)
		}
	}
	applyMigration(ctx, t, pool, hashFixMigration+".up.sql")
	again := chainHashes(ctx, t, pool)
	if !sameHashes(again, fixed) {
		t.Errorf("second up: the hashes differ from the first up")
	}
	checkRechained("after down and up", again)

	// New entries are chained with the UTF-8 hash, a quote included.
	record(plain, audit.Entry{Changes: map[string]interface{}{"name": "R740 \"XL\"\n\t"}})
	if r, verifyErr := audit.Verify(ctx, pool, plain); verifyErr != nil || !r.Intact || r.Checked != 4 {
		t.Errorf("entry with a quote after the migration: %+v %v", r, verifyErr)
	}

	// An entry manipulated after the migration is reported (negative).
	var target string
	if err = pool.QueryRow(ctx, `SELECT id::text FROM audit_log WHERE organization_id = $1 AND seq = 3`, backslash).Scan(&target); err != nil {
		t.Fatal(err)
	}
	unguarded(`UPDATE audit_log SET changes = '{"n": 33}' WHERE id = $1`, target)
	if r, verifyErr := audit.Verify(ctx, pool, backslash); verifyErr != nil || r.Intact || r.BrokenID != target ||
		r.BrokenReason != "hash mismatch" {
		t.Errorf("manipulated entry: %+v %v", r, verifyErr)
	}
	if code, out := runAuditVerify(t, verifier, dbURL, backslash); code != 1 ||
		!strings.Contains(out, fmt.Sprintf("audit chain BROKEN for organization %s: entry %s (seq 3): hash mismatch", backslash, target)) {
		t.Errorf("audit-verify after the manipulation: exit %d\n%s", code, out)
	}
}

// rechainDatabase creates a database of its own, migrated up to the version
// before the hash fix, and returns its URL and a maintenance pool. It is
// dropped when the test ends.
func rechainDatabase(ctx context.Context, t *testing.T, dsn string) (string, *pgxpool.Pool) {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URL must be a URL: %v", err)
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("reticora_wp423_rechain_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, dropErr := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); dropErr != nil {
			t.Errorf("drop database %s: %v", name, dropErr)
		}
	})
	u.Path = "/" + name
	pool, err := database.NewMaintenancePool(ctx, u.String())
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(pool.Close)
	ups, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		if filepath.Base(up) >= hashFixMigration {
			break
		}
		applyMigration(ctx, t, pool, filepath.Base(up))
	}
	return u.String(), pool
}

// applyMigration runs a migration file as golang-migrate does: one
// simple-protocol statement, an implicit transaction.
func applyMigration(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	sql, err := os.ReadFile("../../migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// chainHashes returns previous_hash and hash of every entry by id.
func chainHashes(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string][2]string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id::text, previous_hash, hash FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var id, prev, hash string
		if err := rows.Scan(&id, &prev, &hash); err != nil {
			t.Fatal(err)
		}
		out[id] = [2]string{prev, hash}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameHashes(a, b map[string][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for id, h := range a {
		if b[id] != h {
			return false
		}
	}
	return true
}

// auditVerifyBinary builds cmd/audit-verify once for the test.
func auditVerifyBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "audit-verify")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/audit-verify").CombinedOutput(); err != nil {
		t.Fatalf("go build audit-verify: %v\n%s", err, out)
	}
	return bin
}

// runAuditVerify runs audit-verify for one organization and returns its exit
// code and output.
func runAuditVerify(t *testing.T, bin, dbURL, orgID string) (int, string) {
	t.Helper()
	out, err := exec.Command(bin, "-database-url", dbURL, "-org", orgID).CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), string(out)
	default:
		t.Fatalf("run audit-verify: %v", err)
		return -1, ""
	}
}
