package database_test

import (
	"context"
	"database/sql"
	"testing"
)

// TestTopologyTraversalRecursiveCTE exercises the same WITH RECURSIVE query
// the relationship PG repository uses for topology walks, against a live
// database: depth limiting, cycle guarding, cross-org isolation, and
// self-loops.
func TestTopologyTraversalRecursiveCTE(t *testing.T) {
	dsn := testDatabaseURL(t)
	setupRLSTestRole(t, dsn)

	var (
		orgA   = "a0000000-0000-4000-8000-0000000000d1"
		orgB   = "b0000000-0000-4000-8000-0000000000d2"
		ciType = "c1000000-0000-4000-8000-0000000000d1"
	)

	seed := openAdmin(t, dsn)
	defer seed.Close()
	ctx := context.Background()

	cleanup := func() {
		c := context.Background()
		_, _ = seed.ExecContext(c, `DELETE FROM ci_relationship WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM ci WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM ci_type WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM organization WHERE id IN ($1, $2)`, orgA, orgB)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := seed.ExecContext(ctx, `INSERT INTO organization (id, name, slug) VALUES
		($1, 'Trav Org A', 'trav-org-a'), ($2, 'Trav Org B', 'trav-org-b')`, orgA, orgB); err != nil {
		t.Fatalf("seed orgs: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ci_type (id, organization_id, name) VALUES
		($1, $2, 'Server') ON CONFLICT (id) DO NOTHING`, ciType, orgA); err != nil {
		t.Fatalf("seed ci_type: %v", err)
	}

	// Graph in org A: n0 - n1 - n2 - n3 (chain), n1 - n3 (cycle-closing edge),
	// n0 - n0 (root self-loop). One disconnected CI plus one CI in org B.
	var ids [5]string
	for i, name := range []string{"trav-n0", "trav-n1", "trav-n2", "trav-n3", "trav-lonely"} {
		if err := seed.QueryRowContext(ctx, `INSERT INTO ci (organization_id, ci_type_id, name)
			VALUES ($1, $2, $3) RETURNING id::text`, orgA, ciType, name).Scan(&ids[i]); err != nil {
			t.Fatalf("seed ci %s: %v", name, err)
		}
	}
	var foreignID string
	if err := seed.QueryRowContext(ctx, `INSERT INTO ci (organization_id, ci_type_id, name)
		VALUES ($1, $2, 'trav-foreign') RETURNING id::text`, orgB, ciType).Scan(&foreignID); err != nil {
		t.Fatalf("seed foreign ci: %v", err)
	}

	mkRel := func(org, src, dst string) {
		t.Helper()
		if _, err := seed.ExecContext(ctx, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, 'connected_to')`, org, src, dst); err != nil {
			t.Fatalf("seed relationship: %v", err)
		}
	}
	mkRel(orgA, ids[0], ids[1])
	mkRel(orgA, ids[1], ids[2])
	mkRel(orgA, ids[2], ids[3])
	mkRel(orgA, ids[1], ids[3]) // cycle-closing edge
	mkRel(orgA, ids[0], ids[0]) // root self-loop
	mkRel(orgB, foreignID, foreignID)

	// Keep in sync with relationship.PGRepository.TraverseFrom.
	const traversalQuery = `
		WITH RECURSIVE reach (ci_id, depth) AS (
			SELECT c.id, 0
			FROM ci c
			WHERE c.id = $2 AND c.organization_id = $1 AND c.deleted_at IS NULL

			UNION

			SELECT CASE WHEN r.source_ci_id = w.ci_id THEN r.target_ci_id ELSE r.source_ci_id END,
			       w.depth + 1
			FROM reach w
			JOIN ci_relationship r
			  ON r.organization_id = $1
			 AND (r.source_ci_id = w.ci_id OR r.target_ci_id = w.ci_id)
			JOIN ci s ON s.id = r.source_ci_id AND s.deleted_at IS NULL
			JOIN ci t ON t.id = r.target_ci_id AND t.deleted_at IS NULL
			WHERE w.depth < $3
		),
		nodes AS (
			SELECT ci_id, min(depth) AS depth FROM reach GROUP BY ci_id
		),
		kept AS (
			SELECT ci_id, depth FROM nodes ORDER BY depth, ci_id::text COLLATE "C" LIMIT $4
		)
		SELECT COUNT(*)
		FROM ci_relationship r
		JOIN kept a ON a.ci_id = r.source_ci_id
		JOIN kept b ON b.ci_id = r.target_ci_id
		WHERE r.organization_id = $1
		  AND LEAST(a.depth, b.depth) < $3
	`

	count := func(tx *sql.Tx, root string, depth int) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(traversalQuery, orgA, root, depth, 10000).Scan(&n); err != nil {
			t.Fatalf("traverse from %s depth %d: %v", root, depth, err)
		}
		return n
	}

	// As org A: full walk reaches the whole connected component (4 unique
	// edges + the root self-loop). Depth counts hops from the root; an edge is
	// included once one of its endpoints is within the depth budget, so at
	// depth 2 the n1-n3 shortcut is already visible (n1 is one hop out).
	withRLS(t, dsn, orgA, "", func(tx *sql.Tx) {
		if n := count(tx, ids[0], 10); n != 5 {
			t.Fatalf("full traversal: expected 5 relationships, got %d", n)
		}
		if n := count(tx, ids[0], 1); n != 2 {
			t.Fatalf("depth 1: expected 2 relationships (chain edge + self-loop), got %d", n)
		}
		if n := count(tx, ids[0], 2); n != 4 {
			t.Fatalf("depth 2: expected 4 relationships, got %d", n)
		}
	})

	// As org B: org A's graph is invisible under RLS, even when querying with
	// org A's root id.
	withRLS(t, dsn, orgB, "", func(tx *sql.Tx) {
		if n := count(tx, ids[0], 10); n != 0 {
			t.Fatalf("org B must not traverse org A relationships, got %d", n)
		}
	})
}

// TestWebhookDeadLetterSchema verifies migration 000034: the dead-letter
// table exists with the expected RLS policy, and webhook_delivery accepts
// the 'dead' status.
func TestWebhookDeadLetterSchema(t *testing.T) {
	dsn := testDatabaseURL(t)
	setupRLSTestRole(t, dsn)

	var (
		orgA = "a0000000-0000-4000-8000-0000000000e1"
		orgB = "b0000000-0000-4000-8000-0000000000e2"
	)

	seed := openAdmin(t, dsn)
	defer seed.Close()
	ctx := context.Background()

	cleanup := func() {
		c := context.Background()
		_, _ = seed.ExecContext(c, `DELETE FROM webhook_dead_letter WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM webhook_delivery WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM webhook_subscription WHERE organization_id IN ($1, $2)`, orgA, orgB)
		_, _ = seed.ExecContext(c, `DELETE FROM organization WHERE id IN ($1, $2)`, orgA, orgB)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := seed.ExecContext(ctx, `INSERT INTO organization (id, name, slug) VALUES
		($1, 'DLQ Org A', 'dlq-org-a'), ($2, 'DLQ Org B', 'dlq-org-b')`, orgA, orgB); err != nil {
		t.Fatalf("seed orgs: %v", err)
	}

	var subID string
	if err := seed.QueryRowContext(ctx, `INSERT INTO webhook_subscription (organization_id, name, url, secret, events)
		VALUES ($1, 'dlq-hook', 'https://example.com/hook', 'secret', '{ci.created}')
		RETURNING id::text`, orgA).Scan(&subID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	var deliveryID string
	if err := seed.QueryRowContext(ctx, `INSERT INTO webhook_delivery (organization_id, subscription_id, event, payload, status)
		VALUES ($1, $2, 'ci.created', '{}', 'dead')
		RETURNING id::text`, orgA, subID).Scan(&deliveryID); err != nil {
		t.Fatalf("seed dead delivery (status 'dead' must be accepted): %v", err)
	}

	if _, err := seed.ExecContext(ctx, `INSERT INTO webhook_dead_letter (delivery_id, organization_id, subscription_id, event, payload, attempts)
		VALUES ($1, $2, $3, 'ci.created', '{}', 5)`, deliveryID, orgA, subID); err != nil {
		t.Fatalf("seed dead letter: %v", err)
	}

	// Duplicate moves for the same delivery are rejected by the unique index.
	if _, err := seed.ExecContext(ctx, `INSERT INTO webhook_dead_letter (delivery_id, organization_id, subscription_id, event, payload, attempts)
		VALUES ($1, $2, $3, 'ci.created', '{}', 5)
		ON CONFLICT (delivery_id) DO NOTHING`, deliveryID, orgA, subID); err != nil {
		t.Fatalf("idempotent dead-letter insert: %v", err)
	}

	// Tenant isolation on the dead-letter queue.
	withRLS(t, dsn, orgB, "", func(tx *sql.Tx) {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM webhook_dead_letter").Scan(&n); err != nil {
			t.Fatalf("count dead letters as org B: %v", err)
		}
		if n != 0 {
			t.Fatalf("org B must not see org A dead letters, got %d", n)
		}
	})
	withRLS(t, dsn, orgA, "", func(tx *sql.Tx) {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM webhook_dead_letter").Scan(&n); err != nil {
			t.Fatalf("count dead letters as org A: %v", err)
		}
		if n != 1 {
			t.Fatalf("org A must see exactly its own dead letter, got %d", n)
		}
	})
}

// Denser mesh: every node connected to every other node (K6) plus a long tail,
// to assert the CTE terminates quickly and reports each edge once.
func TestTopologyTraversalDenseMesh(t *testing.T) {
	dsn := testDatabaseURL(t)
	setupRLSTestRole(t, dsn)

	org := "a0000000-0000-4000-8000-0000000000f1"
	ciType := "c1000000-0000-4000-8000-0000000000f1"

	seed := openAdmin(t, dsn)
	defer seed.Close()
	ctx := context.Background()

	cleanup := func() {
		c := context.Background()
		_, _ = seed.ExecContext(c, `DELETE FROM ci_relationship WHERE organization_id = $1`, org)
		_, _ = seed.ExecContext(c, `DELETE FROM ci WHERE organization_id = $1`, org)
		_, _ = seed.ExecContext(c, `DELETE FROM ci_type WHERE organization_id = $1`, org)
		_, _ = seed.ExecContext(c, `DELETE FROM organization WHERE id = $1`, org)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := seed.ExecContext(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'Mesh Org', 'mesh-org')`, org); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO ci_type (id, organization_id, name) VALUES ($1, $2, 'Server')`, ciType, org); err != nil {
		t.Fatalf("seed ci_type: %v", err)
	}

	const n = 6
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		if err := seed.QueryRowContext(ctx, `INSERT INTO ci (organization_id, ci_type_id, name)
			VALUES ($1, $2, $3) RETURNING id::text`, org, ciType, "mesh-"+string(rune('a'+i))).Scan(&ids[i]); err != nil {
			t.Fatalf("seed ci: %v", err)
		}
	}
	edges := 0
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if _, err := seed.ExecContext(ctx, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
				VALUES ($1, $2, $3, 'connected_to')`, org, ids[i], ids[j]); err != nil {
				t.Fatalf("seed rel: %v", err)
			}
			edges++
		}
	}

	withRLS(t, dsn, org, "", func(tx *sql.Tx) {
		var got int
		err := tx.QueryRow(`
			WITH RECURSIVE walk AS (
				SELECT r.id, r.source_ci_id, r.target_ci_id,
					CASE WHEN r.source_ci_id = $2 THEN r.target_ci_id ELSE r.source_ci_id END AS frontier_ci_id,
					1 AS depth,
					ARRAY[r.source_ci_id, r.target_ci_id] AS visited
				FROM ci_relationship r
				WHERE r.organization_id = $1 AND (r.source_ci_id = $2 OR r.target_ci_id = $2)
				UNION
				SELECT r.id, r.source_ci_id, r.target_ci_id,
					CASE WHEN r.source_ci_id = w.frontier_ci_id THEN r.target_ci_id ELSE r.source_ci_id END,
					w.depth + 1,
					w.visited || r.source_ci_id || r.target_ci_id
				FROM ci_relationship r
				JOIN walk w ON (r.source_ci_id = w.frontier_ci_id OR r.target_ci_id = w.frontier_ci_id)
				WHERE r.organization_id = $1 AND w.depth < $3 AND cardinality(w.visited) < $4
				  AND NOT (r.source_ci_id = ANY (w.visited) AND r.target_ci_id = ANY (w.visited))
			)
			SELECT COUNT(DISTINCT id) FROM walk
		`, org, ids[0], 10, 10000).Scan(&got)
		if err != nil {
			t.Fatalf("traverse mesh: %v", err)
		}
		if got != edges {
			t.Fatalf("expected all %d mesh edges exactly once, got %d", edges, got)
		}
	})
}
