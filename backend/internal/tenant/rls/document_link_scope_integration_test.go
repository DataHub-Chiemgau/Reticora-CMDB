package rls_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/jackc/pgx/v5"
)

// TestDocumentStaysScopedWithoutLinks covers SR-56 (WP-852, TEN-05, TEN-09,
// E-09) as the application role: a document that had links does not become
// org-wide when its last link goes. Before migration 000281 the policy of
// document showed every document with link_count = 0 to every principal of
// the organization, so a client-2 principal that removed the last link of a
// client-2 document (DELETE /api/v1/documents/{id}/links/{link_id}), or the
// deletion of the link target (delete_target_document_links, 000275;
// delete_contract_document_links, 000238), exposed the document, its
// versions and its content to client 1.
//
// Both ways are checked: the unlink through document.PGRepository as the
// client-2 principal, and the deletion of every deletable link target of
// 000238 and 000275 (CI, asset, ticket, location, contract, planned
// stocktake, whose link is org-wide). disposal_record and stock_movement are append-only: their
// guard refuses the delete, so their links cannot vanish. Afterwards the
// principals of client 1 and client 2 see neither the document nor its
// version, the org-wide principal sees both. A document an org-wide
// principal created without a link stays visible to everyone.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentStaysScopedWithoutLinks(t *testing.T) {
	f := scopetest.Seed(t, "52")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "doc-link-scope")
	repo := document.NewPGRepository(f.App)

	orgWide := database.OrgWideScope(f.OrgA, user)
	c1 := database.OrgWideScope(f.OrgA, user)
	c1.Clients = database.ScopeIDs(f.Client1)
	c2 := database.OrgWideScope(f.OrgA, user)
	c2.Clients = database.ScopeIDs(f.Client2)

	exec := func(scope database.TenantScope, sql string, args ...any) {
		t.Helper()
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	admin := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, sql+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	// newDoc creates a document with one version as the org-wide principal
	// and, with a target, its link (scope derived from the target).
	newDoc := func(title, entityType, entityID string) (doc, link string) {
		t.Helper()
		doc = admin(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, $2, 'f.txt', '', $3)`, f.OrgA, title, user)
		admin(`INSERT INTO document_version (organization_id, document_id, version, storage_key, size, mime) VALUES ($1, $2, 1, $3, 1, 'text/plain')`,
			f.OrgA, doc, "org/"+f.OrgA+"/documents/"+doc+"/"+f.ID())
		if entityType == "" {
			return doc, ""
		}
		if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				f.OrgA, doc, entityType, entityID).Scan(&link)
		}); err != nil {
			t.Fatalf("link %s to %s %s: %v", title, entityType, entityID, err)
		}
		return doc, link
	}
	// sees counts the document and its versions under scope.
	sees := func(scope database.TenantScope, doc string) (docs, versions int) {
		t.Helper()
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM document WHERE id = $1), (SELECT count(*) FROM document_version WHERE document_id = $1)`,
				doc).Scan(&docs, &versions)
		}); err != nil {
			t.Fatalf("read document %s: %v", doc, err)
		}
		return docs, versions
	}
	expect := func(name, doc string, wantC1, wantC2 bool) {
		t.Helper()
		for _, p := range []struct {
			kind  string
			scope database.TenantScope
			want  bool
		}{{"client 1", c1, wantC1}, {"client 2", c2, wantC2}, {"org-wide", orgWide, true}} {
			want := 0
			if p.want {
				want = 1
			}
			if docs, versions := sees(p.scope, doc); docs != want || versions != want {
				t.Errorf("%s: %s sees %d documents and %d versions, want %d", name, p.kind, docs, versions, want)
			}
		}
	}
	// detail checks GetByID of the repository (GET /api/v1/documents/{id})
	// as the principals of client 1 and client 2.
	detail := func(name, doc string, wantC1, wantC2 bool) {
		t.Helper()
		for client, want := range map[string]bool{f.Client1: wantC1, f.Client2: wantC2} {
			if _, err := repo.GetByID(f.ClientCtx(client), f.OrgA, doc); (err == nil) != want {
				t.Errorf("%s: GetByID as client %s: %v, want visible %v", name, client, err, want)
			}
		}
	}

	// A document never linked stays org-wide (E-09: only an org-wide
	// principal creates it).
	unlinked, _ := newDoc("dls-unlinked", "", "")
	expect("never linked document", unlinked, true, true)
	detail("never linked document", unlinked, true, true)

	// 1. The client-2 principal removes the last link of a client-2
	//    document through the repository (the API path).
	ci2 := f.CI(t, f.OrgA, f.Client2, "dls-ci2")
	doc, link := newDoc("dls-unlink", "ci", ci2)
	expect("linked document", doc, false, true)
	if err := repo.UnlinkDocument(f.ClientCtx(f.Client2), f.OrgA, doc, link); err != nil {
		t.Fatalf("client 2 removes the last link: %v", err)
	}
	if got := scalarText(t, f, `SELECT link_count::text FROM document WHERE id = $1`, doc); got != "0" {
		t.Fatalf("link_count after the unlink: %s, want 0", got)
	}
	expect("document unlinked by client 2", doc, false, false)
	detail("document unlinked by client 2", doc, false, false)

	// 2. The link target is deleted: the triggers of 000238 and 000275
	//    remove the link.
	site2 := scalarText(t, f, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, f.Room(t, f.Client2, "dls-room2"))
	warehouse := admin(`INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, 'warehouse', $2, 'dls-wh')`, f.OrgA, site2)
	stocktakeLoc := admin(`INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, 'warehouse', $2, 'dls-st')`, f.OrgA, site2)
	asset2 := f.Asset(t, f.OrgA, f.Client2, "dls-a2")
	// A link to a stocktake is org-wide (000217, 000238): every principal
	// sees the document until the stocktake is deleted.
	for _, target := range []struct {
		kind, id, del string
		orgWideLink   bool
	}{
		{"ci", f.CI(t, f.OrgA, f.Client2, "dls-ci2-del"), `DELETE FROM ci WHERE id = $1`, false},
		{"asset", f.Asset(t, f.OrgA, f.Client2, "dls-a2-del"), `DELETE FROM asset WHERE id = $1`, false},
		{"ticket", admin(`INSERT INTO ticket (organization_id, title, reporter_id, related_asset_id) VALUES ($1, 'dls-t', $2, $3)`, f.OrgA, user, asset2),
			`DELETE FROM ticket WHERE id = $1`, false},
		{"location", warehouse, `DELETE FROM location WHERE id = $1`, false},
		{"contract", admin(`INSERT INTO contract (organization_id, client_id, number, currency) VALUES ($1, $2, 'dls-c', 'EUR')`, f.OrgA, f.Client2),
			`DELETE FROM contract WHERE id = $1`, false},
		{"stocktake", admin(`INSERT INTO stocktake (organization_id, title, location_id) VALUES ($1, 'dls-st', $2)`, f.OrgA, stocktakeLoc),
			`DELETE FROM stocktake WHERE id = $1`, true},
	} {
		targetDoc, _ := newDoc("dls-target-"+target.kind, target.kind, target.id)
		expect("document of a "+target.kind, targetDoc, target.orgWideLink, true)
		exec(orgWide, target.del, target.id)
		if got := scalarText(t, f, `SELECT link_count::text FROM document WHERE id = $1`, targetDoc); got != "0" {
			t.Fatalf("link_count after deleting the %s: %s, want 0", target.kind, got)
		}
		expect("document of a deleted "+target.kind, targetDoc, false, false)
		detail("document of a deleted "+target.kind, targetDoc, false, false)
	}

	// The org-wide principal links the orphaned document again; the scoped
	// principals follow the new link. A scoped principal cannot link it.
	ci1 := f.CI(t, f.OrgA, f.Client1, "dls-ci1")
	if err := database.WithTenant(bg, f.App, &c1, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, doc, ci1)
		return err
	}); err == nil {
		t.Error("client 1 linked the orphaned document of client 2")
	}
	exec(orgWide, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, doc, ci1)
	expect("orphaned document linked to a client-1 CI", doc, true, false)
	detail("orphaned document linked to a client-1 CI", doc, true, false)
}
