package rls_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestModuleTablesInheritObjectScope covers migration 000064 (WP-028,
// MGT-01..04, TKT-01): tickets, comments, document links, maintenance window
// CIs and key assignments derive client and site from the referenced object,
// follow it when it moves, and the policies hide and reject rows outside the
// principal's scope. Documents and maintenance windows are visible without
// links or through at least one visible link.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestModuleTablesInheritObjectScope(t *testing.T) {
	f := scopetest.Seed(t, "48")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "module-object")
	ci1 := f.CI(t, f.OrgA, f.Client1, "mod-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "mod-c2")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "mod-a2")

	ids := map[string]string{}
	insert := func(ctx context.Context, tx pgx.Tx, key, sql string, args ...any) error {
		var id string
		if err := tx.QueryRow(ctx, sql+` RETURNING id::text`, args...).Scan(&id); err != nil {
			return err
		}
		ids[key] = id
		return nil
	}
	orgWide := database.OrgWideScope(f.OrgA, user)
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		for _, s := range []struct {
			key, sql string
			args     []any
		}{
			// The writer claims client 2 for a ticket on a CI of client 1.
			{"ticket1", `INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id, client_id) VALUES ($1, 't1', $2, $3, $4)`, []any{f.OrgA, user, ci1, f.Client2}},
			{"ticket2", `INSERT INTO ticket (organization_id, title, reporter_id, related_asset_id) VALUES ($1, 't2', $2, $3)`, []any{f.OrgA, user, asset2}},
			{"ticket0", `INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 't0', $2)`, []any{f.OrgA, user}},
			{"comment1", `INSERT INTO ticket_comment (organization_id, ticket_id, author_id, content) VALUES ($1, $2, $3, 'c')`, nil},
			{"comment2", `INSERT INTO ticket_comment (organization_id, ticket_id, author_id, content) VALUES ($1, $2, $3, 'c')`, nil},
			{"doc1", `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'd1', 'f', '', $2)`, []any{f.OrgA, user}},
			{"doc2", `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'd2', 'f', '', $2)`, []any{f.OrgA, user}},
			{"doc12", `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'd12', 'f', '', $2)`, []any{f.OrgA, user}},
			{"doc0", `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'd0', 'f', '', $2)`, []any{f.OrgA, user}},
			{"window1", `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at) VALUES ($1, 'w1', now(), now() + interval '1 hour')`, []any{f.OrgA}},
			{"window2", `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at) VALUES ($1, 'w2', now(), now() + interval '1 hour')`, []any{f.OrgA}},
			{"window0", `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at) VALUES ($1, 'w0', now(), now() + interval '1 hour')`, []any{f.OrgA}},
			{"key1", `INSERT INTO key_item (organization_id, client_id, name) VALUES ($1, $2, 'k1')`, []any{f.OrgA, f.Client1}},
			{"key2", `INSERT INTO key_item (organization_id, client_id, name) VALUES ($1, $2, 'k2')`, []any{f.OrgA, f.Client2}},
		} {
			args := s.args
			switch s.key {
			case "comment1":
				args = []any{f.OrgA, ids["ticket1"], user}
			case "comment2":
				args = []any{f.OrgA, ids["ticket2"], user}
			}
			if err := insert(ctx, tx, s.key, s.sql, args...); err != nil {
				return errors.New(s.key + ": " + err.Error())
			}
		}
		for _, s := range []struct {
			key, doc, entityType, entity string
		}{
			{"link1", "doc1", "ci", ci1},
			{"link2", "doc2", "ci", ci2},
			{"link12a", "doc12", "ci", ci1},
			{"link12b", "doc12", "asset", asset2},
			{"linkT2", "doc2", "ticket", ids["ticket2"]},
		} {
			if err := insert(ctx, tx, s.key,
				`INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, $3, $4)`,
				f.OrgA, ids[s.doc], s.entityType, s.entity); err != nil {
				return errors.New(s.key + ": " + err.Error())
			}
		}
		for _, s := range []struct{ key, window, ci string }{{"wci1", "window1", ci1}, {"wci2", "window2", ci2}} {
			if err := insert(ctx, tx, s.key,
				`INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`,
				f.OrgA, ids[s.window], s.ci); err != nil {
				return errors.New(s.key + ": " + err.Error())
			}
		}
		for _, s := range []struct{ key, item string }{{"assign1", "key1"}, {"assign2", "key2"}} {
			if err := insert(ctx, tx, s.key,
				`INSERT INTO key_assignment (organization_id, key_item_id, assigned_to) VALUES ($1, $2, $3)`,
				f.OrgA, ids[s.item], user); err != nil {
				return errors.New(s.key + ": " + err.Error())
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("org-wide seed: %v", err)
	}

	// Derivation ignores the client the writer claimed.
	derived := func(table, id string) (client string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM `+table+` WHERE id = $1`, id).Scan(&client); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		return client
	}
	for _, c := range []struct{ table, key, want string }{
		{"ticket", "ticket1", f.Client1},
		{"ticket", "ticket2", f.Client2},
		{"ticket", "ticket0", ""},
		{"ticket_comment", "comment1", f.Client1},
		{"document_link", "link12b", f.Client2},
		{"document_link", "linkT2", f.Client2},
		{"maintenance_window_ci", "wci2", f.Client2},
		{"key_assignment", "assign1", f.Client1},
	} {
		if got := derived(c.table, ids[c.key]); got != c.want {
			t.Errorf("%s %s: client %q, want %q", c.table, c.key, got, c.want)
		}
	}

	// Reads of a principal of client 1.
	c1 := database.OrgWideScope(f.OrgA, user)
	c1.Clients = database.ScopeIDs(f.Client1)
	run := func(fn func(ctx context.Context, tx pgx.Tx) error) error {
		return database.WithTenant(bg, f.App, &c1, fn)
	}
	for _, c := range []struct {
		table, key string
		visible    bool
	}{
		{"ticket", "ticket1", true},
		{"ticket", "ticket2", false},
		{"ticket", "ticket0", true},
		{"ticket_comment", "comment1", true},
		{"ticket_comment", "comment2", false},
		{"document", "doc1", true},
		{"document", "doc2", false},
		{"document", "doc12", true},
		{"document", "doc0", true},
		{"document_link", "link12a", true},
		{"document_link", "link12b", false},
		{"maintenance_window", "window1", true},
		{"maintenance_window", "window2", false},
		{"maintenance_window", "window0", true},
		{"maintenance_window_ci", "wci2", false},
		{"key_assignment", "assign1", true},
		{"key_assignment", "assign2", false},
	} {
		var n int
		if err := run(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+c.table+` WHERE id = $1`, ids[c.key]).Scan(&n)
		}); err != nil {
			t.Fatalf("read %s: %v", c.table, err)
		}
		if (n == 1) != c.visible {
			t.Errorf("%s %s: visible=%v, want %v", c.table, c.key, n == 1, c.visible)
		}
	}

	// Writes of the client 1 principal on objects outside its scope.
	for _, c := range []struct {
		name string
		sql  string
		args []any
	}{
		{"ticket on a CI of client 2", `INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id) VALUES ($1, 'x', $2, $3)`, []any{f.OrgA, user, ci2}},
		{"ticket without object", `INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 'x', $2)`, []any{f.OrgA, user}},
		{"comment on a ticket of client 2", `INSERT INTO ticket_comment (organization_id, ticket_id, author_id, content) VALUES ($1, $2, $3, 'x')`, []any{f.OrgA, ids["ticket2"], user}},
		{"link to a CI of client 2", `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, []any{f.OrgA, ids["doc0"], ci2}},
		{"link of a hidden document", `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, []any{f.OrgA, ids["doc2"], ci1}},
		{"CI of client 2 in a window", `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`, []any{f.OrgA, ids["window1"], ci2}},
		{"CI into a hidden window", `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`, []any{f.OrgA, ids["window2"], ci1}},
		{"key of client 2", `INSERT INTO key_assignment (organization_id, key_item_id, assigned_to) VALUES ($1, $2, $3)`, []any{f.OrgA, ids["key2"], user}},
		{"move ticket to a CI of client 2", `UPDATE ticket SET related_ci_id = $2 WHERE id = $1`, []any{ids["ticket1"], ci2}},
	} {
		err := run(func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, c.sql, c.args...)
			if err == nil && tag.RowsAffected() == 0 {
				return errors.New("no row written")
			}
			return err
		})
		if err == nil {
			t.Errorf("%s: want rejection", c.name)
		}
	}
	// In scope, the same writes succeed.
	if err := run(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_comment (organization_id, ticket_id, author_id, content) VALUES ($1, $2, $3, 'ok')`, f.OrgA, ids["ticket1"], user); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, ids["doc0"], ci1)
		return err
	}); err != nil {
		t.Errorf("client 1 writes in scope: %v", err)
	}

	// Propagation: the CI of client 1 moves to client 2 and takes ticket,
	// comments, document links and window entries along.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, ci1, f.Client2); err != nil {
		t.Fatalf("move ci: %v", err)
	}
	for _, c := range []struct{ table, key string }{
		{"ticket", "ticket1"}, {"ticket_comment", "comment1"}, {"document_link", "link1"}, {"maintenance_window_ci", "wci1"},
	} {
		if got := derived(c.table, ids[c.key]); got != f.Client2 {
			t.Errorf("%s %s after CI move: client %q, want client 2", c.table, c.key, got)
		}
	}

	// Counters follow inserts and deletes, so a document with links never
	// looks unlinked.
	var count int
	if err := f.Admin.QueryRow(bg, `SELECT link_count FROM document WHERE id = $1`, ids["doc0"]).Scan(&count); err != nil || count != 1 {
		t.Errorf("doc0 link_count = %d, %v; want 1", count, err)
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM document_link WHERE id = $1`, ids["link12b"]); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT link_count FROM document WHERE id = $1`, ids["doc12"]).Scan(&count); err != nil || count != 1 {
		t.Errorf("doc12 link_count = %d, %v; want 1", count, err)
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM document WHERE id = $1`, ids["doc2"]); err != nil {
		t.Errorf("delete document with links: %v", err)
	}
}
