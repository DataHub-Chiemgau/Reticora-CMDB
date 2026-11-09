package search_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// TestSearchFieldScopeFollowsDocument covers migration 000280 (SRC-01,
// RBA-07, NFR-02): the field rows of a search document carry its client and
// site, and the policy of search_document_field checks them like the policy
// of search_document. When the CI moves to another client and site, the
// field rows of its document follow in the same statement: the principal of
// the old client and of the old site reads none of them, those of the new
// ones read all. A writer cannot set other values (search_field_scope), and
// a client-scoped principal writes no field row of a document of another
// client.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -run 'TestSearchFieldScopeFollowsDocument' ./internal/search/
func TestSearchFieldScopeFollowsDocument(t *testing.T) {
	f := scopetest.Seed(t, "bb")
	bg := context.Background()
	// A room of each client; the CI takes the site of its location
	// (ci_derive_location).
	room1, room2 := f.Room(t, f.Client1, "wp280 site 1"), f.Room(t, f.Client2, "wp280 site 2")
	siteOf := func(location string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM location WHERE id = $1`, location).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site1, site2 := siteOf(room1), siteOf(room2)
	alpha := f.CI(t, f.OrgA, f.Client1, "wp280 alpha")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2, hostname = 'wp280host', model = 'wp280model' WHERE id = $1`, alpha, room1); err != nil {
		t.Fatal(err)
	}
	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	var doc string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM search_document WHERE entity_type = 'ci' AND entity_id = $1`, alpha).Scan(&doc); err != nil {
		t.Fatal(err)
	}

	// rows counts the field rows of the document a principal reads.
	rows := func(scope *database.TenantScope) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM search_document_field WHERE document_id = $1`, doc).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	scoped := func(clients, sites []string) *database.TenantScope {
		s := database.OrgWideScope(f.OrgA, f.User)
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		return &s
	}
	// mirrored reports the field rows whose client or site differ from
	// their document's.
	mirrored := func(stage string) {
		t.Helper()
		var total, diverged int
		if err := f.Admin.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE f.client_id IS DISTINCT FROM d.client_id OR f.site_id IS DISTINCT FROM d.site_id)
			FROM search_document_field f JOIN search_document d ON d.id = f.document_id WHERE d.id = $1`, doc).Scan(&total, &diverged); err != nil {
			t.Fatal(err)
		}
		if total < 2 || diverged != 0 {
			t.Errorf("%s: %d of %d field rows differ from the client and site of their document", stage, diverged, total)
		}
	}
	mirrored("reindex")
	all := rows(&orgWide)
	if all < 2 {
		t.Fatalf("org-wide principal reads %d field rows, want hostname and model", all)
	}
	for _, c := range []struct {
		name  string
		scope *database.TenantScope
		want  int
	}{
		{"client 1", scoped([]string{f.Client1}, nil), all},
		{"client 2", scoped([]string{f.Client2}, nil), 0},
		{"site 1", scoped(nil, []string{site1}), all},
		{"site 2", scoped(nil, []string{site2}), 0},
	} {
		if got := rows(c.scope); got != c.want {
			t.Errorf("before the move: %s reads %d field rows, want %d", c.name, got, c.want)
		}
	}

	// The CI moves to client 2 and site 2 (ci_search_scope_propagation,
	// search_field_scope_propagation).
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ci SET client_id = $2, location_id = $3 WHERE id = $1`, alpha, f.Client2, room2)
		return err
	}); err != nil {
		t.Fatalf("move: %v", err)
	}
	mirrored("move")
	for _, c := range []struct {
		name  string
		scope *database.TenantScope
		want  int
	}{
		{"client 1", scoped([]string{f.Client1}, nil), 0},
		{"client 2", scoped([]string{f.Client2}, nil), all},
		{"site 1", scoped(nil, []string{site1}), 0},
		{"site 2", scoped(nil, []string{site2}), all},
		{"client 1 and site 2", scoped([]string{f.Client1}, []string{site2}), 0},
	} {
		if got := rows(c.scope); got != c.want {
			t.Errorf("after the move: %s reads %d field rows, want %d", c.name, got, c.want)
		}
	}

	// A writer cannot set other values: the trigger takes those of the
	// document.
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE search_document_field SET client_id = NULL, site_id = NULL WHERE document_id = $1`, doc)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mirrored("update of the scope columns")
	if got := rows(scoped([]string{f.Client1}, nil)); got != 0 {
		t.Errorf("after clearing the columns: client 1 reads %d field rows, want 0", got)
	}

	// A client-1 principal writes no field row of the client-2 document.
	if err := database.WithTenant(bg, f.App, scoped([]string{f.Client1}, nil), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO search_document_field (organization_id, document_id, field, content) VALUES ($1, $2, 'core.os_name', 'wp280')`,
			f.OrgA, doc)
		return err
	}); err == nil {
		t.Error("client-1 principal wrote a field row of the client-2 document")
	}
	if got := rows(&orgWide); got != all {
		t.Errorf("after the refused insert: %d field rows, want %d", got, all)
	}
}
