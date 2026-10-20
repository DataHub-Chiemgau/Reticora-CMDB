package rls_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestModuleTablesInheritObjectScope covers migration 000064 (WP-028,
// MGT-01..04, TKT-01): tickets, comments, document links, maintenance window
// CIs and key assignments derive client and site from the referenced object,
// follow it when it moves, and the policies hide and reject rows outside the
// principal's scope. Documents and maintenance windows are visible without
// links or through at least one visible link (migration 000064); a principal
// restricted in the client or site scope writes them only when it sees all
// of their links, cannot create or link an unlinked (org-wide) one, and the
// link counters stay exact and are not writable (migration 000139, WP-341,
// F-004, F-048, F-185); links to org-wide objects do not count as writable
// (migration 000146). Disposal records derive client and site from their
// asset or CI (migration 000145, WP-353, MGT-02, F-186), and the derived
// columns of every module table follow a site or client move of the
// referenced object (moduleObjectPropagation, WP-028 open evidence).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestModuleTablesInheritObjectScope(t *testing.T) {
	f := scopetest.Seed(t, "48")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "module-object")
	ci1 := f.CI(t, f.OrgA, f.Client1, "mod-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "mod-c2")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "mod-a2")
	ci1b := f.CI(t, f.OrgA, f.Client1, "mod-c1b")

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
		// E-09: linking an org-wide document would take it from other clients.
		{"link of an org-wide document", `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, []any{f.OrgA, ids["doc0"], ci1}},
		{"CI into an org-wide window", `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`, []any{f.OrgA, ids["window0"], ci1}},
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
		_, err := tx.Exec(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, ids["doc1"], ci1b)
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
	if err := f.Admin.QueryRow(bg, `SELECT link_count FROM document WHERE id = $1`, ids["doc1"]).Scan(&count); err != nil || count != 2 {
		t.Errorf("doc1 link_count = %d, %v; want 2", count, err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT link_count FROM document WHERE id = $1`, ids["doc0"]).Scan(&count); err != nil || count != 0 {
		t.Errorf("doc0 link_count = %d, %v; want 0", count, err)
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

	moduleObjectWrites(t, f, user)
	disposalRecordScope(t, f, user)
	moduleObjectPropagation(t, f, user)
}

// moduleObjectWrites covers the write rules of migration 000139 (WP-341) as
// the application role: UPDATE and DELETE of shared and unlinked documents
// and windows by a client-1 principal (F-004, F-048), creation of unlinked
// rows by scoped principals (E-09), the counter integrity of
// probe_doc_link_count.sql (F-185) and a cascaded link delete when a scoped
// principal deletes a CI.
func moduleObjectWrites(t *testing.T, f *scopetest.Fixture, user string) {
	t.Helper()
	bg := context.Background()
	ciA := f.CI(t, f.OrgA, f.Client1, "mod-w-c1")
	ciDel := f.CI(t, f.OrgA, f.Client1, "mod-w-c1-del")
	ciB := f.CI(t, f.OrgA, f.Client2, "mod-w-c2")
	assetB := f.Asset(t, f.OrgA, f.Client2, "mod-w-a2")
	site1 := scalarText(t, f, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, f.Room(t, f.Client1, "mod-w-s1"))

	orgWide := database.OrgWideScope(f.OrgA, user)
	c1 := database.OrgWideScope(f.OrgA, user)
	c1.Clients = database.ScopeIDs(f.Client1)
	c2 := database.OrgWideScope(f.OrgA, user)
	c2.Clients = database.ScopeIDs(f.Client2)
	s1 := database.OrgWideScope(f.OrgA, user)
	s1.Sites = database.ScopeIDs(site1)
	// exec runs one statement in its own transaction (deferred triggers fire
	// at its commit) and returns the affected rows.
	exec := func(scope database.TenantScope, sql string, args ...any) (int64, error) {
		var n int64
		err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	insertID := func(scope database.TenantScope, sql string, args ...any) string {
		t.Helper()
		var id string
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, sql+` RETURNING id::text`, args...).Scan(&id)
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	newDoc := func(title string, links ...[2]string) string {
		t.Helper()
		id := insertID(orgWide, `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, $2, 'f', '', $3)`, f.OrgA, title, user)
		for _, l := range links {
			insertID(orgWide, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, $3, $4)`, f.OrgA, id, l[0], l[1])
		}
		return id
	}
	newWindow := func(title string, cis ...string) string {
		t.Helper()
		id := insertID(orgWide, `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at) VALUES ($1, $2, now(), now() + interval '1 hour')`, f.OrgA, title)
		for _, c := range cis {
			insertID(orgWide, `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`, f.OrgA, id, c)
		}
		return id
	}
	ciOrg := f.CI(t, f.OrgA, "", "mod-w-org")
	docShared := newDoc("w-shared", [2]string{"ci", ciA}, [2]string{"asset", assetB})
	docOrgLinked := newDoc("w-org-linked", [2]string{"ci", ciA}, [2]string{"ci", ciOrg})
	winOrgLinked := newWindow("w-org-linked", ciA, ciOrg)
	docOrg := newDoc("w-org")
	docOwn := newDoc("w-own", [2]string{"ci", ciA})
	docProbe := newDoc("w-probe", [2]string{"ci", ciA}, [2]string{"asset", assetB})
	winShared := newWindow("w-shared", ciA, ciB)
	winOrg := newWindow("w-org")
	winOwn := newWindow("w-own", ciA)
	winCascade := newWindow("w-cascade", ciDel, ciB)
	denied := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "42501"
	}

	// Shared and org-wide rows: client 1 changes and deletes nothing.
	for _, c := range []struct {
		name, sql, id string
	}{
		{"UPDATE shared document", `UPDATE document SET title = 'x' WHERE id = $1`, docShared},
		{"DELETE shared document", `DELETE FROM document WHERE id = $1`, docShared},
		{"UPDATE org-wide document", `UPDATE document SET title = 'x' WHERE id = $1`, docOrg},
		{"DELETE org-wide document", `DELETE FROM document WHERE id = $1`, docOrg},
		{"UPDATE shared window", `UPDATE maintenance_window SET title = 'x', status = 'cancelled' WHERE id = $1`, winShared},
		{"DELETE shared window", `DELETE FROM maintenance_window WHERE id = $1`, winShared},
		{"UPDATE org-wide window", `UPDATE maintenance_window SET title = 'x' WHERE id = $1`, winOrg},
		{"DELETE org-wide window", `DELETE FROM maintenance_window WHERE id = $1`, winOrg},
		// An org-wide link is visible but not writable (E-09, 000146).
		{"UPDATE document linked to an org-wide CI", `UPDATE document SET title = 'x' WHERE id = $1`, docOrgLinked},
		{"DELETE document linked to an org-wide CI", `DELETE FROM document WHERE id = $1`, docOrgLinked},
		{"UPDATE window with an org-wide CI", `UPDATE maintenance_window SET title = 'x' WHERE id = $1`, winOrgLinked},
		{"DELETE window with an org-wide CI", `DELETE FROM maintenance_window WHERE id = $1`, winOrgLinked},
	} {
		if n, err := exec(c1, c.sql, c.id); err != nil || n != 0 {
			t.Errorf("client 1: %s: %d rows, %v; want 0 rows", c.name, n, err)
		}
	}
	if got := scalarText(t, f, `SELECT (SELECT string_agg(title, ',' ORDER BY title) FROM document WHERE id IN ($1, $2)) || '|' ||
		(SELECT count(*) FROM document_link WHERE document_id = $1)::text`, docShared, docOrg); got != "w-org,w-shared|2" {
		t.Errorf("documents|shared links after the client-1 writes: %s, want w-org,w-shared|2", got)
	}
	if got := scalarText(t, f, `SELECT (SELECT string_agg(title || ':' || link_count::text, ',') FROM document WHERE id = $1) || '|' ||
		(SELECT string_agg(title || ':' || ci_count::text, ',') FROM maintenance_window WHERE id = $2)`, docOrgLinked, winOrgLinked); got != "w-org-linked:2|w-org-linked:2" {
		t.Errorf("rows linked to an org-wide CI after the client-1 writes: %s, want w-org-linked:2|w-org-linked:2", got)
	}
	if got := scalarText(t, f, `SELECT (SELECT string_agg(title || ':' || status, ',' ORDER BY title) FROM maintenance_window WHERE id IN ($1, $2)) || '|' ||
		(SELECT count(*) FROM maintenance_window_ci WHERE maintenance_window_id = $1)::text`, winShared, winOrg); got != "w-org:scheduled,w-shared:scheduled|2" {
		t.Errorf("windows|shared CIs after the client-1 writes: %s, want w-org:scheduled,w-shared:scheduled|2", got)
	}

	// Scoped principals create no unlinked row (E-09) and set no counter.
	for _, scope := range []struct {
		name  string
		scope database.TenantScope
	}{{"client 1", c1}, {"site 1", s1}} {
		for _, c := range []struct{ name, sql string }{
			{"document without link", `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'w-new', 'f', '', $2)`},
			{"window without CI", `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at, created_by) VALUES ($1, 'w-new', now(), now() + interval '1 hour', $2)`},
		} {
			if _, err := exec(scope.scope, c.sql, f.OrgA, user); !denied(err) {
				t.Errorf("%s: %s: %v, want 42501", scope.name, c.name, err)
			}
		}
	}
	if _, err := exec(orgWide, `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by, link_count) VALUES ($1, 'w-forged', 'f', '', $2, 3)`, f.OrgA, user); err == nil {
		t.Error("document inserted with a forged link_count")
	}
	for _, c := range []struct {
		name  string
		scope database.TenantScope
		sql   string
		id    string
	}{
		// probe_doc_link_count.sql part B: the counter is not writable.
		{"client 1 sets link_count", c1, `UPDATE document SET link_count = 0 WHERE id = $1`, docOwn},
		{"org-wide principal sets link_count", orgWide, `UPDATE document SET link_count = 0 WHERE id = $1`, docShared},
		{"client 1 sets ci_count", c1, `UPDATE maintenance_window SET ci_count = 0 WHERE id = $1`, winOwn},
		{"org-wide principal sets ci_count", orgWide, `UPDATE maintenance_window SET ci_count = 5 WHERE id = $1`, winOrg},
	} {
		if _, err := exec(c.scope, c.sql, c.id); !denied(err) {
			t.Errorf("%s: %v, want 42501", c.name, err)
		}
	}
	if got := scalarText(t, f, `SELECT (SELECT link_count FROM document WHERE id = $1)::text || '|' || (SELECT link_count FROM document WHERE id = $2)::text || '|' ||
		(SELECT ci_count FROM maintenance_window WHERE id = $3)::text || '|' || (SELECT ci_count FROM maintenance_window WHERE id = $4)::text`,
		docOwn, docShared, winOwn, winOrg); got != "1|2|1|0" {
		t.Errorf("counters after the refused writes: %s, want 1|2|1|0", got)
	}

	// In scope, client 1 writes its own rows and creates linked ones in one
	// transaction; the org-wide principal writes everything.
	for _, c := range []struct {
		name  string
		scope database.TenantScope
		sql   string
		id    string
	}{
		{"client 1 renames its document", c1, `UPDATE document SET title = 'w-own-2' WHERE id = $1`, docOwn},
		{"client 1 cancels its window", c1, `UPDATE maintenance_window SET status = 'cancelled' WHERE id = $1`, winOwn},
		{"org-wide principal renames the shared document", orgWide, `UPDATE document SET title = 'w-shared-2' WHERE id = $1`, docShared},
		{"org-wide principal renames the org-wide window", orgWide, `UPDATE maintenance_window SET title = 'w-org-2' WHERE id = $1`, winOrg},
		{"client 1 deletes its window", c1, `DELETE FROM maintenance_window WHERE id = $1`, winOwn},
	} {
		if n, err := exec(c.scope, c.sql, c.id); err != nil || n != 1 {
			t.Errorf("%s: %d rows, %v; want 1", c.name, n, err)
		}
	}
	var created [2]string
	if err := database.WithTenant(bg, f.App, &c1, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at) VALUES ($1, 'w-c1', now(), now() + interval '1 hour') RETURNING id::text`, f.OrgA).Scan(&created[0]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`, f.OrgA, created[0], ciA); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'w-c1', 'f', '', $2) RETURNING id::text`, f.OrgA, user).Scan(&created[1]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, created[1], ciA)
		return err
	}); err != nil {
		t.Errorf("client 1 creates a window and a document with own links: %v", err)
	} else if got := scalarText(t, f, `SELECT (SELECT ci_count FROM maintenance_window WHERE id = $1)::text || '|' || (SELECT link_count FROM document WHERE id = $2)::text`, created[0], created[1]); got != "1|1" {
		t.Errorf("counters of the rows client 1 created: %s, want 1|1", got)
	}

	// probe_doc_link_count.sql part A: every client removes its own link and
	// the counter follows, so the document becomes org-wide again.
	if n, err := exec(c1, `DELETE FROM document_link WHERE document_id = $1 AND entity_id = $2`, docProbe, ciA); err != nil || n != 1 {
		t.Errorf("client 1 removes its link: %d rows, %v", n, err)
	}
	if got := scalarText(t, f, `SELECT (SELECT count(*) FROM document_link WHERE document_id = $1)::text || '|' || link_count::text FROM document WHERE id = $1`, docProbe); got != "1|1" {
		t.Errorf("A1 links|link_count after client 1 removed its link: %s, want 1|1", got)
	}
	if n, err := exec(c2, `DELETE FROM document_link WHERE document_id = $1 AND entity_id = $2`, docProbe, assetB); err != nil || n != 1 {
		t.Errorf("client 2 removes the last link: %d rows, %v", n, err)
	}
	if got := scalarText(t, f, `SELECT (SELECT count(*) FROM document_link WHERE document_id = $1)::text || '|' || link_count::text FROM document WHERE id = $1`, docProbe); got != "0|0" {
		t.Errorf("A2 links|link_count after client 2 removed the last link: %s, want 0|0", got)
	}
	var visible int
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM document WHERE id = $1`, docProbe).Scan(&visible)
	}); err != nil || visible != 1 {
		t.Errorf("A3 org-wide principal sees the unlinked document: %d, %v; want 1", visible, err)
	}

	// Deleting a CI cascades to its window entries; the counter of a window
	// shared with client 2 follows and the delete of the scoped principal is
	// not aborted.
	if n, err := exec(c1, `DELETE FROM ci WHERE id = $1`, ciDel); err != nil || n != 1 {
		t.Errorf("client 1 deletes its CI of a shared window: %d rows, %v", n, err)
	}
	if got := scalarText(t, f, `SELECT (SELECT count(*) FROM maintenance_window_ci WHERE maintenance_window_id = $1)::text || '|' || ci_count::text FROM maintenance_window WHERE id = $1`, winCascade); got != "1|1" {
		t.Errorf("CIs|ci_count of the shared window after the CI delete: %s, want 1|1", got)
	}
}

// disposalRecordScope covers migration 000145 (WP-353, MGT-02, F-186) as the
// application role without the Go predicate of the disposal repository:
// disposal records derive client and site from the asset (site of its
// location) or the CI, a client-1 principal neither reads the record of a
// client-2 asset nor records one (42501), a site principal only writes
// records at its site, and the records stay append-only.
func disposalRecordScope(t *testing.T, f *scopetest.Fixture, user string) {
	t.Helper()
	bg := context.Background()
	siteA, siteB := f.ID(), f.ID()
	for _, s := range []string{siteA, siteB} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, s, f.OrgA, f.Client1, "disp "+s); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	own := f.Asset(t, f.OrgA, f.Client1, "disp-own")
	other := f.Asset(t, f.OrgA, f.Client1, "disp-site-b")
	foreign := f.Asset(t, f.OrgA, f.Client2, "disp-foreign")
	ciForeign := f.CI(t, f.OrgA, f.Client2, "disp-ci-c2")
	for asset, site := range map[string]string{own: siteA, other: siteB} {
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, asset, site); err != nil {
			t.Fatalf("place asset: %v", err)
		}
	}
	orgWide := database.OrgWideScope(f.OrgA, user)
	c1 := database.OrgWideScope(f.OrgA, user)
	c1.Clients = database.ScopeIDs(f.Client1)
	sA := database.OrgWideScope(f.OrgA, user)
	sA.Sites = database.ScopeIDs(siteA)
	insert := func(scope database.TenantScope, column, ref string) (string, error) {
		var id string
		err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			// The writer's client is ignored.
			return tx.QueryRow(ctx, `INSERT INTO disposal_record (organization_id, `+column+`, method, performed_at, client_id)
				VALUES ($1, $2, 'recycling', now(), $3) RETURNING id::text`, f.OrgA, ref, f.Client1).Scan(&id)
		})
		return id, err
	}
	mustInsert := func(scope database.TenantScope, column, ref string) string {
		t.Helper()
		id, err := insert(scope, column, ref)
		if err != nil {
			t.Fatalf("disposal record of %s %s: %v", column, ref, err)
		}
		return id
	}
	recForeign := mustInsert(orgWide, "asset_id", foreign)
	recCI := mustInsert(orgWide, "ci_id", ciForeign)
	recOwn := mustInsert(c1, "asset_id", own)
	recSiteA := mustInsert(sA, "asset_id", own)
	scope := func(id string) string {
		return scalarText(t, f, `SELECT COALESCE(client_id::text, '-') || '|' || COALESCE(site_id::text, '-') FROM disposal_record WHERE id = $1`, id)
	}
	for _, c := range []struct{ name, id, want string }{
		{"client-2 asset", recForeign, f.Client2 + "|-"},
		{"client-2 CI", recCI, f.Client2 + "|-"},
		{"own asset at site A (client 1)", recOwn, f.Client1 + "|" + siteA},
		{"own asset at site A (site A)", recSiteA, f.Client1 + "|" + siteA},
	} {
		if got := scope(c.id); got != c.want {
			t.Errorf("disposal record of the %s: client|site %s, want %s", c.name, got, c.want)
		}
	}

	visible := func(scope database.TenantScope, id string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM disposal_record WHERE id = $1`, id).Scan(&n)
		}); err != nil {
			t.Fatalf("read disposal record: %v", err)
		}
		return n
	}
	// F-186 retest: client 1 selects the records of client 2 directly.
	for _, c := range []struct {
		name  string
		scope database.TenantScope
		id    string
		want  int
	}{
		{"client 1 reads the record of a client-2 asset", c1, recForeign, 0},
		{"client 1 reads the record of a client-2 CI", c1, recCI, 0},
		{"client 1 reads its own record", c1, recOwn, 1},
		{"site A reads the record at site A", sA, recOwn, 1},
		{"org-wide principal reads the record of a client-2 asset", orgWide, recForeign, 1},
	} {
		if got := visible(c.scope, c.id); got != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, got, c.want)
		}
	}
	var pgErr *pgconn.PgError
	for _, c := range []struct {
		name        string
		scope       database.TenantScope
		column, ref string
	}{
		{"client 1 records a client-2 asset", c1, "asset_id", foreign},
		{"client 1 records a client-2 CI", c1, "ci_id", ciForeign},
		{"site A records an asset at site B", sA, "asset_id", other},
		{"client 1 records a disposal without object (org-wide)", c1, "notes", "x"},
	} {
		if _, err := insert(c.scope, c.column, c.ref); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: %v, want 42501", c.name, err)
		}
	}
	// Append-only (000049): the derivation exception covers only nested
	// re-derivations.
	if _, err := f.Admin.Exec(bg, `UPDATE disposal_record SET client_id = $2 WHERE id = $1`, recOwn, f.Client2); err == nil {
		t.Error("disposal record updated directly")
	}
	if _, err := f.Admin.Exec(bg, `UPDATE disposal_record SET notes = 'x' WHERE id = $1`, recOwn); err == nil {
		t.Error("disposal record notes updated")
	}

	// Propagation: the asset moves to site B, then to client 2; the CI to
	// client 1. The principals of the old scope see the records no longer.
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, own, siteB); err != nil {
		t.Fatalf("move asset to site B: %v", err)
	}
	if got := scope(recOwn); got != f.Client1+"|"+siteB {
		t.Errorf("record after the asset moved to site B: %s, want client 1|site B", got)
	}
	if got := visible(sA, recOwn); got != 0 {
		t.Errorf("site A reads the record after the asset moved to site B: %d rows, want 0", got)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET client_id = $2, location_id = NULL WHERE id = $1`, own, f.Client2); err != nil {
		t.Fatalf("move asset to client 2: %v", err)
	}
	if got := scope(recOwn); got != f.Client2+"|-" {
		t.Errorf("record after the asset moved to client 2: %s, want client 2|-", got)
	}
	if got := visible(c1, recOwn); got != 0 {
		t.Errorf("client 1 reads the record after the asset moved to client 2: %d rows, want 0", got)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, ciForeign, f.Client1); err != nil {
		t.Fatalf("move ci to client 1: %v", err)
	}
	if got := scope(recCI); got != f.Client1+"|-" {
		t.Errorf("record after the CI moved to client 1: %s, want client 1|-", got)
	}
	if got := visible(c1, recCI); got != 1 {
		t.Errorf("client 1 reads the record after the CI moved to client 1: %d rows, want 1", got)
	}
}

// moduleObjectPropagation covers the open evidence of WP-028 (MGT-01..04,
// TKT-01): tickets, comments, document links and window CIs take the site
// and client of their CI, key assignments the client of their key item (key
// items have no site); when the referenced object moves, the derived columns
// follow and the principal of the old scope sees 0 rows.
func moduleObjectPropagation(t *testing.T, f *scopetest.Fixture, user string) {
	t.Helper()
	bg := context.Background()
	siteA, siteB, siteC := f.ID(), f.ID(), f.ID()
	for site, client := range map[string]string{siteA: f.Client1, siteB: f.Client1, siteC: f.Client2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, site, f.OrgA, client, "prop "+site); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	ci := f.CI(t, f.OrgA, f.Client1, "prop-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ci, siteA); err != nil {
		t.Fatalf("place ci: %v", err)
	}
	ids := map[string]string{}
	seed := func(key, sql string, args ...any) {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, sql+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
		ids[key] = id
	}
	seed("ticket", `INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id) VALUES ($1, 'prop', $2, $3)`, f.OrgA, user, ci)
	seed("ticket_comment", `INSERT INTO ticket_comment (organization_id, ticket_id, author_id, content) VALUES ($1, $2, $3, 'c')`, f.OrgA, ids["ticket"], user)
	seed("document", `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, 'prop', 'f', '', $2)`, f.OrgA, user)
	seed("document_link", `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, ids["document"], ci)
	seed("ticket_link", `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ticket', $3)`, f.OrgA, ids["document"], ids["ticket"])
	seed("window", `INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at) VALUES ($1, 'prop', now(), now() + interval '1 hour')`, f.OrgA)
	seed("maintenance_window_ci", `INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3)`, f.OrgA, ids["window"], ci)
	seed("key_item", `INSERT INTO key_item (organization_id, client_id, name) VALUES ($1, $2, 'prop')`, f.OrgA, f.Client1)
	seed("key_assignment", `INSERT INTO key_assignment (organization_id, key_item_id, assigned_to) VALUES ($1, $2, $3)`, f.OrgA, ids["key_item"], user)

	scoped := func(clients, sites []string) database.TenantScope {
		s := database.OrgWideScope(f.OrgA, user)
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		return s
	}
	c1, c2 := scoped([]string{f.Client1}, nil), scoped([]string{f.Client2}, nil)
	sA, sB, sC := scoped(nil, []string{siteA}), scoped(nil, []string{siteB}), scoped(nil, []string{siteC})
	// table, key, whether the row carries a site.
	rows := []struct {
		table, key string
		site       bool
	}{
		{"ticket", "ticket", true},
		{"ticket_comment", "ticket_comment", true},
		{"document_link", "document_link", true},
		{"document_link", "ticket_link", true},
		{"maintenance_window_ci", "maintenance_window_ci", true},
		{"key_assignment", "key_assignment", false},
	}
	derived := func(table, key string, site bool) string {
		col := "'-'"
		if site {
			col = "COALESCE(site_id::text, '-')"
		}
		return scalarText(t, f, `SELECT COALESCE(client_id::text, '-') || '|' || `+col+` FROM `+table+` WHERE id = $1`, ids[key])
	}
	visible := func(scope database.TenantScope, table, key string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, ids[key]).Scan(&n)
		}); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		return n
	}
	type expect struct {
		client, site string
		see          []database.TenantScope
		blind        []database.TenantScope
	}
	check := func(stage string, siteRows, keyRows expect) {
		t.Helper()
		for _, r := range rows {
			e, want := siteRows, siteRows.client+"|"+siteRows.site
			if !r.site {
				e, want = keyRows, keyRows.client+"|-"
			}
			if got := derived(r.table, r.key, r.site); got != want {
				t.Errorf("%s: %s %s client|site %s, want %s", stage, r.table, r.key, got, want)
			}
			for i, s := range e.see {
				if n := visible(s, r.table, r.key); n != 1 {
					t.Errorf("%s: %s %s: principal %d of the scope sees %d rows, want 1", stage, r.table, r.key, i, n)
				}
			}
			for i, s := range e.blind {
				if n := visible(s, r.table, r.key); n != 0 {
					t.Errorf("%s: %s %s: principal %d of another scope sees %d rows, want 0", stage, r.table, r.key, i, n)
				}
			}
		}
	}
	keyClient1 := expect{client: f.Client1, see: []database.TenantScope{c1}, blind: []database.TenantScope{c2}}
	check("client 1, site A", expect{client: f.Client1, site: siteA, see: []database.TenantScope{c1, sA}, blind: []database.TenantScope{c2, sB}}, keyClient1)

	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ci, siteB); err != nil {
		t.Fatalf("move ci to site B: %v", err)
	}
	check("CI moved to site B", expect{client: f.Client1, site: siteB, see: []database.TenantScope{c1, sB}, blind: []database.TenantScope{sA, c2}}, keyClient1)

	if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2, location_id = $3 WHERE id = $1`, ci, f.Client2, siteC); err != nil {
		t.Fatalf("move ci to client 2: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE key_item SET client_id = $2 WHERE id = $1`, ids["key_item"], f.Client2); err != nil {
		t.Fatalf("move key item to client 2: %v", err)
	}
	check("CI and key item moved to client 2",
		expect{client: f.Client2, site: siteC, see: []database.TenantScope{c2, sC}, blind: []database.TenantScope{c1, sB}},
		expect{client: f.Client2, see: []database.TenantScope{c2}, blind: []database.TenantScope{c1}})
}

// scalarText runs a query on the maintenance pool (no RLS) and returns the
// single value as text.
func scalarText(t *testing.T, f *scopetest.Fixture, query string, args ...any) string {
	t.Helper()
	var v string
	if err := f.Admin.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}
