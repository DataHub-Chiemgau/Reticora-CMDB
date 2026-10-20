package rls_test

// WP-345 (TEN-02, TEN-05, IMP-07; F-050): the derived scope of an edge
// follows a CI moved by a client- or site-scoped principal. Migration 000141
// runs propagate_ci_scope and derive_edge_scope with the client and site
// scope widened (app_scope_widen, 000139); before, they ran under the RLS of
// the mover and skipped the edge whose far endpoint it could not see, so the
// edge kept the old source_client_id/source_site_id and observers saw an
// edge naming a CI they may not see.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//     go test -count=1 -v -run '^TestEdgeScopeFollowsScopedMove$' ./internal/tenant/rls/

import (
	"context"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestEdgeScopeFollowsScopedMove(t *testing.T) {
	w := newT1World(t, "dc")
	f := w.f
	bg := context.Background()
	client3 := w.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'Client 3', 'scopetest-dc-c3') RETURNING id::text`, f.OrgA)
	inTenant := func(scope *database.TenantScope, query string, args ...any) (int, error) {
		var n int
		err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, args...).Scan(&n)
		})
		return n, err
	}
	scoped := func(clients, sites []string) *database.TenantScope {
		s := database.OrgWideScope(f.OrgA, w.userA)
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		return &s
	}
	var suppressionType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`).Scan(&suppressionType); err != nil {
		t.Fatalf("relationship type: %v", err)
	}

	t.Run("client move by a client-1/client-2 principal", func(t *testing.T) {
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-x")
		y := f.CI(t, f.OrgA, client3, "t1-edge-y")
		w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": x, "target_ci_id": y, "rel_type": "depends_on"})
		mover := w.as("clients-1-2", only(f.Client1, f.Client2), all())
		if code, body := w.do(mover, http.MethodPatch, "/api/v1/cis/"+x, map[string]any{"client_id": f.Client2}); code != http.StatusOK {
			t.Fatalf("move X to client 2 as clients-1-2: %d %s", code, trim(body))
		}
		if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != f.Client2 {
			t.Errorf("TEN-02/AC2: edge source_client_id after the move = %s, want client 2 (%s)", got, f.Client2)
		}
		if got := w.scalar(`SELECT COALESCE(target_client_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != client3 {
			t.Errorf("TEN-02/AC2: edge target_client_id after the move = %s, want client 3 (%s)", got, client3)
		}
		observer := scoped([]string{f.Client1, client3}, nil)
		if n, err := inTenant(observer, `SELECT count(*) FROM ci WHERE id = $1`, x); err != nil || n != 0 {
			t.Fatalf("setup: observer clients 1+3 sees CI X: %d %v", n, err)
		}
		if n, err := inTenant(observer, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1`, x); err != nil || n != 0 {
			t.Errorf("TEN-05/AC3: observer clients 1+3 sees %d edges naming the invisible CI X (err %v), want 0", n, err)
		}
	})

	t.Run("site move by a site-1/site-2 principal", func(t *testing.T) {
		room1, room2, room3 := f.Room(t, f.Client1, "t1-edge-s1"), f.Room(t, f.Client1, "t1-edge-s2"), f.Room(t, f.Client1, "t1-edge-s3")
		s1, s2, s3 := w.siteOfRoom(room1), w.siteOfRoom(room2), w.siteOfRoom(room3)
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-sx")
		y := f.CI(t, f.OrgA, f.Client1, "t1-edge-sy")
		for ci, site := range map[string]string{x: s1, y: s3} {
			if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/cis/"+ci, map[string]any{"location_id": site}); code != http.StatusOK {
				t.Fatalf("place CI: %d %s", code, trim(body))
			}
		}
		w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": x, "target_ci_id": y, "rel_type": "depends_on"})
		mover := w.as("sites-1-2", all(), only(s1, s2))
		if code, body := w.do(mover, http.MethodPatch, "/api/v1/cis/"+x, map[string]any{"location_id": s2}); code != http.StatusOK {
			t.Fatalf("move X to site 2 as sites-1-2: %d %s", code, trim(body))
		}
		if got := w.scalar(`SELECT COALESCE(source_site_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != s2 {
			t.Errorf("TEN-02/AC2: edge source_site_id after the move = %s, want site 2 (%s)", got, s2)
		}
		if got := w.scalar(`SELECT COALESCE(target_site_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != s3 {
			t.Errorf("TEN-02/AC2: edge target_site_id after the move = %s, want site 3 (%s)", got, s3)
		}
		observer := scoped(nil, []string{s1, s3})
		if n, err := inTenant(observer, `SELECT count(*) FROM ci WHERE id = $1`, x); err != nil || n != 0 {
			t.Fatalf("setup: observer sites 1+3 sees CI X: %d %v", n, err)
		}
		if n, err := inTenant(observer, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1`, x); err != nil || n != 0 {
			t.Errorf("TEN-05/AC3: observer sites 1+3 sees %d edges naming the invisible CI X (err %v), want 0", n, err)
		}
	})

	// probe_edge_stale_after_move.sql (probe_edge_stale_site.sql is the site
	// subtest above): the move as a plain UPDATE in the mover's transaction.
	// The moved CI is the target of the edge and the source of a
	// suppression; the mover gets only its own row back, its scope is
	// restored inside the transaction, and it still does not see the edge to
	// the CI outside its scope.
	t.Run("database level: target endpoint and suppression, mover scope restored", func(t *testing.T) {
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-dx")
		y := f.CI(t, f.OrgA, client3, "t1-edge-dy")
		edge := w.scalar(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, 'depends_on') RETURNING id::text`, f.OrgA, y, x)
		suppression := w.scalar(`INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id)
			VALUES ($1, $2, $3, $4) RETURNING id::text`, f.OrgA, x, y, suppressionType)
		mover := scoped([]string{f.Client1, f.Client2}, nil)
		if err := database.WithTenant(bg, f.App, mover, func(ctx context.Context, tx pgx.Tx) error {
			scopeQuery := `SELECT current_setting('app.client_scope', true) || '/' || current_setting('app.site_scope', true)`
			var before, after string
			if err := tx.QueryRow(ctx, scopeQuery).Scan(&before); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `UPDATE ci SET client_id = $2 WHERE id = $1 RETURNING id::text`, x, f.Client2)
			if err != nil {
				return err
			}
			var returned []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				returned = append(returned, id)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if len(returned) != 1 || returned[0] != x {
				t.Errorf("move returned %v, want only CI X", returned)
			}
			if err := tx.QueryRow(ctx, scopeQuery).Scan(&after); err != nil {
				return err
			}
			if after != before || before == "/" {
				t.Errorf("mover client/site scope after the move = %q, want the scope before it (%q)", after, before)
			}
			var edges, suppressions int
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM ci_relationship WHERE id = $1), (SELECT count(*) FROM relationship_suppression WHERE id = $2)`,
				edge, suppression).Scan(&edges, &suppressions); err != nil {
				return err
			}
			if edges != 0 || suppressions != 0 {
				t.Errorf("mover clients 1+2 sees %d edges and %d suppressions to the client-3 CI, want 0", edges, suppressions)
			}
			return nil
		}); err != nil {
			t.Fatalf("scoped move: %v", err)
		}
		if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') || '>' || COALESCE(target_client_id::text, '') FROM ci_relationship WHERE id = $1`, edge); got != client3+">"+f.Client2 {
			t.Errorf("edge scope after the move = %s, want client 3 > client 2", got)
		}
		if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') || '>' || COALESCE(target_client_id::text, '') FROM relationship_suppression WHERE id = $1`, suppression); got != f.Client2+">"+client3 {
			t.Errorf("suppression scope after the move = %s, want client 2 > client 3", got)
		}
		observer := scoped([]string{f.Client1, client3}, nil)
		if n, err := inTenant(observer, `SELECT (SELECT count(*) FROM ci_relationship WHERE id = $1) + (SELECT count(*) FROM relationship_suppression WHERE id = $2)`,
			edge, suppression); err != nil || n != 0 {
			t.Errorf("observer clients 1+3 sees %d edges/suppressions naming the invisible CI X (err %v), want 0", n, err)
		}
	})

	t.Run("positive control: org-wide move re-derives the edge", func(t *testing.T) {
		x := f.CI(t, f.OrgA, f.Client1, "t1-edge-cx")
		y := f.CI(t, f.OrgA, client3, "t1-edge-cy")
		w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": x, "target_ci_id": y, "rel_type": "depends_on"})
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/cis/"+x, map[string]any{"client_id": f.Client2}); code != http.StatusOK {
			t.Fatalf("org-wide move: %d %s", code, trim(body))
		}
		if got := w.scalar(`SELECT COALESCE(source_client_id::text, '') FROM ci_relationship WHERE source_ci_id = $1`, x); got != f.Client2 {
			t.Errorf("edge source_client_id after the org-wide move = %s, want client 2", got)
		}
	})
}
