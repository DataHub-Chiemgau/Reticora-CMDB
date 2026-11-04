package search_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The secret of the tests: the serial number of the alpha CI.
const frSecret = "sn266secret"

// frWorld is the fixture of the field permission tests of the search
// (RBA-07, SRC-01, WP-266).
type frWorld struct {
	f      *scopetest.Fixture
	repo   *search.PGRepository
	server string // the global CI type "server"
	child  string // a CI type of the organization below "server"
	alpha  string // CI of type server, client 1, serial frSecret
	beta   string // CI of the child type, client 1
	gamma  string // CI of type server, client 2
	asset  string
	ticket string
	doc    string
}

var frPerms = []identity.Permission{identity.PermCIRead, identity.PermSearchRead, "asset:read", "ticket:read", "document:read"}

func newFRWorld(t *testing.T) *frWorld {
	t.Helper()
	f := scopetest.Seed(t, "bb")
	bg := context.Background()
	w := &frWorld{f: f, repo: search.NewPGRepository(f.App)}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&w.server); err != nil {
		t.Fatal(err)
	}
	w.child = f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_type (id, organization_id, key, name, parent_type_id) VALUES ($1, $2, 'wp266-child', 'WP-266 Child', $3)`,
		w.child, f.OrgA, w.server); err != nil {
		t.Fatal(err)
	}
	ciWith := func(client, name, hostname, serial string) string {
		id := f.CI(t, f.OrgA, client, name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET hostname = $2, manufacturer = 'acme', serial_number = $3 WHERE id = $1`, id, hostname, serial); err != nil {
			t.Fatal(err)
		}
		return id
	}
	w.alpha = ciWith(f.Client1, "wp266 alpha", "alphahost", frSecret)
	w.beta = ciWith(f.Client1, "wp266 beta", "betahost", "sn266child")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, w.beta, w.child); err != nil {
		t.Fatal(err)
	}
	w.gamma = ciWith(f.Client2, "wp266 gamma", "gammahost", "sn266gamma")
	w.asset = f.Asset(t, f.OrgA, f.Client1, "wp266-asset")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET serial_number = 'sn266asset', notes = 'assetnotes266' WHERE id = $1`, w.asset); err != nil {
		t.Fatal(err)
	}
	reporter := f.AppUser(t, f.OrgA, "wp266-reporter")
	if err := f.Admin.QueryRow(bg, `INSERT INTO ticket (organization_id, client_id, title, reporter_id, description, priority)
		VALUES ($1, $2, 'wp266 ticket', $3, 'ticketdesc266', 'high') RETURNING id::text`, f.OrgA, f.Client1, reporter).Scan(&w.ticket); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by, description, tags)
		VALUES ($1, 'wp266 document', 'doc266.pdf', '', $2, 'docdesc266', ARRAY['tag266']) RETURNING id::text`, f.OrgA, reporter).Scan(&w.doc); err != nil {
		t.Fatal(err)
	}
	if _, err := w.repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	return w
}

// fields returns the field rows of the document of the entity.
func (w *frWorld) fields(t *testing.T, entityID string) map[string]string {
	t.Helper()
	rows, err := w.f.Admin.Query(context.Background(), `SELECT f.field, f.content FROM search_document_field f
		JOIN search_document d ON d.id = f.document_id WHERE d.entity_id = $1`, entityID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

// role creates a role with the permissions of frPerms and the field grants
// (object kind, CI type or "", field key, level), the policy of a principal
// holding it.
func (w *frWorld) role(t *testing.T, name string, grants ...[4]string) (string, *permission.FieldPolicy) {
	t.Helper()
	bg := context.Background()
	id := w.f.ID()
	raw, _ := json.Marshal(frPerms)
	if _, err := w.f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, $4::jsonb)`,
		id, w.f.OrgA, name, string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		var typeID any
		if g[1] != "" {
			typeID = g[1]
		}
		if _, err := w.f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, ci_type_id, field_key, level) VALUES ($1, $2, $3, $4, $5, $6)`,
			w.f.OrgA, id, g[0], typeID, g[2], g[3]); err != nil {
			t.Fatal(err)
		}
	}
	return id, w.policy(t, id)
}

// policy resolves the policy of a principal with the role (fresh snapshot).
func (w *frWorld) policy(t *testing.T, roleID string) *permission.FieldPolicy {
	t.Helper()
	p, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(w.f.App), nil).
		PolicyForGrants(context.Background(), w.f.OrgA, []identity.Grant{{Permissions: frPerms, RoleID: roleID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func frQuery(ctx context.Context, b search.Backend, orgID string, policy *permission.FieldPolicy, text string) (search.Result, error) {
	return b.Query(ctx, &search.Query{OrganizationID: orgID, Text: text, Limit: 50, Highlight: true, Policy: policy})
}

// find runs the query against the PostgreSQL index and returns the hits by
// entity id.
func (w *frWorld) find(t *testing.T, policy *permission.FieldPolicy, text string) (map[string]search.Hit, search.Result) {
	t.Helper()
	res, err := frQuery(w.f.OrgCtx(w.f.OrgA), w.repo, w.f.OrgA, policy, text)
	if err != nil {
		t.Fatalf("query %q: %v", text, err)
	}
	out := map[string]search.Hit{}
	for _, h := range res.Data {
		out[h.EntityID] = h
	}
	return out, res
}

// leaks reports whether a hit shows the secret in summary or snippet.
func leaks(hits map[string]search.Hit, secret string) bool {
	for _, h := range hits {
		if strings.Contains(strings.ToLower(h.Summary), secret) || strings.Contains(strings.ToLower(strings.Join(h.Highlights, " ")), secret) {
			return true
		}
	}
	return false
}

// TestSearchFieldRows covers the field rows of WP-266: ReindexTenant,
// IndexDocument and IndexDocuments write them in the transaction of the
// document, Delete removes them by cascade, the backfill of migration 000228
// writes the same rows, a client-scoped principal reads none of another
// client, and a hit carries no fields.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchFieldRows(t *testing.T) {
	w := newFRWorld(t)
	f := w.f
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)

	want := map[string]map[string]string{
		w.alpha:  {"core.hostname": "alphahost", "core.manufacturer": "acme", "core.serial_number": frSecret},
		w.asset:  {"core.asset_tag": "wp266-asset", "core.serial_number": "sn266asset", "core.notes": "assetnotes266"},
		w.ticket: {"core.description": "ticketdesc266", "core.priority": "high"},
		w.doc:    {"core.description": "docdesc266", "core.file_name": "doc266.pdf", "core.tags": "tag266"},
	}
	check := func(stage string) {
		t.Helper()
		for id, fields := range want {
			got := w.fields(t, id)
			for k, v := range fields {
				if got[k] != v {
					t.Errorf("%s: field rows of %s: %v, want %s=%s", stage, id, got, k, v)
				}
			}
			for k := range got {
				if !slices.Contains(search.SummaryFields[map[string]string{w.alpha: "ci", w.asset: "asset", w.ticket: "ticket", w.doc: "document"}[id]], k) {
					t.Errorf("%s: field row %s of %s is no summary field", stage, k, id)
				}
			}
		}
	}
	check("reindex")

	// The backfill of the migration writes the same rows.
	up, err := os.ReadFile("../../migrations/000228_search_document_fields.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	backfill := string(up[strings.Index(string(up), "INSERT INTO search_document_field"):])
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	var before, after int
	if err := tx.QueryRow(bg, `SELECT count(*) FROM search_document_field WHERE organization_id = $1`, f.OrgA).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// The backfill reads ticket.tags, which migration 000230 (WP-764)
	// replaces by ticket_tag: inside this rolled back transaction the column
	// is rebuilt from the assignments, as it stood when 000228 ran.
	if _, err := tx.Exec(bg, `ALTER TABLE ticket ADD COLUMN IF NOT EXISTS tags text[]`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `UPDATE ticket SET tags = (SELECT array_agg(tg.key ORDER BY tg.key) FROM ticket_tag tt JOIN tag tg ON tg.id = tt.tag_id
		WHERE tt.ticket_id = ticket.id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `DELETE FROM search_document_field`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, backfill); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.QueryRow(bg, `SELECT count(*) FROM search_document_field WHERE organization_id = $1`, f.OrgA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if before == 0 || after != before {
		t.Errorf("backfill wrote %d field rows of the organization, the reindex %d", after, before)
	}

	// IndexDocument through the CI indexer replaces the rows: a changed
	// value is updated, a cleared one removed.
	indexer := search.NewCIIndexer(w.repo)
	item := &ci.Item{ID: w.alpha, OrganizationID: f.OrgA, Name: "wp266 alpha", Hostname: "alphahost2", SerialNumber: frSecret, ClientID: f.Client1}
	if err := indexer.IndexDocument(ctx, ptr(ci.IndexDocumentFor(item))); err != nil {
		t.Fatal(err)
	}
	if got := w.fields(t, w.alpha); got["core.hostname"] != "alphahost2" || got["core.manufacturer"] != "" || got["core.serial_number"] != frSecret || len(got) != 2 {
		t.Errorf("field rows after IndexDocument: %v", got)
	}
	// IndexDocuments (batch, WP-333) in one statement.
	itemB := &ci.Item{ID: w.beta, OrganizationID: f.OrgA, Name: "wp266 beta", Hostname: "betahost", Model: "m266", ClientID: f.Client1}
	item.Manufacturer = "acme"
	if err := indexer.IndexDocuments(ctx, []ci.Document{ci.IndexDocumentFor(item), ci.IndexDocumentFor(itemB)}); err != nil {
		t.Fatal(err)
	}
	if got := w.fields(t, w.alpha); got["core.manufacturer"] != "acme" || len(got) != 3 {
		t.Errorf("field rows of alpha after IndexDocuments: %v", got)
	}
	if got := w.fields(t, w.beta); got["core.model"] != "m266" || got["core.serial_number"] != "" || len(got) != 2 {
		t.Errorf("field rows of beta after IndexDocuments: %v", got)
	}
	// A document new to the index gets its rows in the same statement,
	// written by a client-scoped principal.
	delta := &ci.Item{ID: f.CI(t, f.OrgA, f.Client1, "wp266 delta"), OrganizationID: f.OrgA, Name: "wp266 delta", Hostname: "deltahost", ClientID: f.Client1}
	if err := indexer.IndexDocument(f.ClientCtx(f.Client1), ptr(ci.IndexDocumentFor(delta))); err != nil {
		t.Fatal(err)
	}
	if got := w.fields(t, delta.ID); got["core.hostname"] != "deltahost" || len(got) != 1 {
		t.Errorf("field rows of a new document: %v", got)
	}

	// A client-1 principal reads no field rows of client 2.
	count := func(scope *database.TenantScope, entityID string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM search_document_field
				WHERE document_id IN (SELECT id FROM search_document WHERE entity_id = $1)`, entityID).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var gammaDoc string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM search_document WHERE entity_id = $1`, w.gamma).Scan(&gammaDoc); err != nil {
		t.Fatal(err)
	}
	c1 := database.OrgWideScope(f.OrgA, f.User)
	c1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	if n := count(&orgWide, w.gamma); n == 0 {
		t.Error("org-wide principal reads no field rows of the client-2 CI")
	}
	var direct int
	if err := database.WithTenant(bg, f.App, &c1, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM search_document_field WHERE document_id = $1`, gammaDoc).Scan(&direct)
	}); err != nil || direct != 0 {
		t.Errorf("client-1 principal reads %d field rows of the client-2 CI, %v", direct, err)
	}
	if n := count(&c1, w.alpha); n == 0 {
		t.Error("client-1 principal reads no field rows of its own CI")
	}

	// Delete removes the field rows by cascade.
	if err := w.repo.Delete(ctx, f.OrgA, "ci", w.beta); err != nil {
		t.Fatal(err)
	}
	if got := w.fields(t, w.beta); len(got) != 0 {
		t.Errorf("field rows after Delete: %v", got)
	}

	// A hit carries no fields.
	_, res := w.find(t, permission.UnrestrictedPolicy(frPerms), "alpha")
	raw, _ := json.Marshal(res)
	if len(res.Data) == 0 || strings.Contains(strings.ToLower(string(raw)), "fields") {
		t.Errorf("hit: %s", raw)
	}
}

func ptr[T any](v T) *T { return &v }

// TestSearchFieldPermissions covers the query of WP-266: none or a
// sensitive field without grant on core.serial_number hides the CI from a
// search for its serial number, no summary or snippet shows it and the
// total does not count it, while a query of name and readable field still
// finds it; a grant at the parent type applies at the subtype; an object
// grant none leaves the title only; without grants and sensitive fields the
// hits, their order and snippets are those of phase 1.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchFieldPermissions(t *testing.T) {
	w := newFRWorld(t)
	f := w.f
	bg := context.Background()

	// Regression: without grants the policy is unrestricted and the query
	// is the one of phase 1.
	for _, text := range []string{"wp266", "acme", frSecret, "alpha alphahost", ""} {
		_, phase1 := w.find(t, nil, text)
		_, open := w.find(t, permission.UnrestrictedPolicy(frPerms), text)
		if a, b := mustJSON(phase1), mustJSON(open); a != b {
			t.Errorf("%q: unrestricted policy %s, phase 1 %s", text, b, a)
		}
	}
	if hits, _ := w.find(t, permission.UnrestrictedPolicy(frPerms), frSecret); hits[w.alpha].EntityID == "" {
		t.Fatal("unrestricted: serial number finds no CI")
	}

	// A grant that leaves the summary fields readable restricts the kind:
	// hits, order and snippets stay those of phase 1.
	_, other := w.role(t, "wp266-other", [4]string{"ci", "", "core.fqdn", "none"}, [4]string{"asset", "", "core.barcode", "none"})
	for _, text := range []string{"wp266", "acme", frSecret, "alpha alphahost"} {
		_, phase1 := w.find(t, nil, text)
		_, restricted := w.find(t, other, text)
		if a, b := mustJSON(phase1), mustJSON(restricted); a != b {
			t.Errorf("%q: readable restriction %s, phase 1 %s", text, b, a)
		}
	}

	hidden := func(name string, policy *permission.FieldPolicy) {
		t.Helper()
		hits, res := w.find(t, policy, frSecret)
		if _, ok := hits[w.alpha]; ok || leaks(hits, frSecret) || res.Total != len(res.Data) || res.Total != 0 {
			t.Errorf("%s: serial number search: %s", name, mustJSON(res))
		}
		if hits, _ = w.find(t, policy, "alpha "+frSecret); len(hits) != 0 {
			t.Errorf("%s: name and serial number: %s", name, mustJSON(hits))
		}
		hits, res = w.find(t, policy, "alpha alphahost")
		if _, ok := hits[w.alpha]; !ok || leaks(hits, frSecret) {
			t.Errorf("%s: name and readable field: %s", name, mustJSON(res))
		}
		hits, _ = w.find(t, policy, "wp266")
		if _, ok := hits[w.alpha]; !ok || leaks(hits, frSecret) || !strings.Contains(hits[w.alpha].Summary, "alphahost") {
			t.Errorf("%s: name search: %s", name, mustJSON(hits[w.alpha]))
		}
		// A phrase or a negation does not tell an unreadable value either.
		if hits, _ = w.find(t, policy, "alpha -"+frSecret); hits[w.alpha].EntityID == "" {
			t.Errorf("%s: negation of the unreadable value hides the CI", name)
		}
	}
	_, none := w.role(t, "wp266-none", [4]string{"ci", "", "core.serial_number", "none"})
	hidden("field grant none", none)

	// A grant at the parent type applies at the subtype.
	_, parent := w.role(t, "wp266-parent", [4]string{"ci", w.server, "core.hostname", "none"})
	if hits, _ := w.find(t, parent, "betahost"); len(hits) != 0 {
		t.Errorf("grant at the parent type: subtype CI found by its hostname: %s", mustJSON(hits))
	}
	if hits, _ := w.find(t, parent, "beta"); hits[w.beta].EntityID == "" || strings.Contains(hits[w.beta].Summary, "betahost") {
		t.Errorf("grant at the parent type: %s", mustJSON(hits))
	}
	_, childOnly := w.role(t, "wp266-child", [4]string{"ci", w.child, "core.hostname", "none"})
	if hits, _ := w.find(t, childOnly, "alphahost"); hits[w.alpha].EntityID == "" {
		t.Error("grant at the subtype hides the hostname of the parent type")
	}

	// An object grant none leaves the title only.
	objRole, _ := w.role(t, "wp266-object")
	if _, err := f.Admin.Exec(bg, `INSERT INTO object_grant (organization_id, grantee_role_id, object_kind, object_id, level) VALUES ($1, $2, 'ci', $3, 'none')`,
		f.OrgA, objRole, w.alpha); err != nil {
		t.Fatal(err)
	}
	object := w.policy(t, objRole)
	if hits, _ := w.find(t, object, "alphahost"); len(hits) != 0 {
		t.Errorf("object grant none: found by a field: %s", mustJSON(hits))
	}
	if hits, _ := w.find(t, object, "alpha"); hits[w.alpha].EntityID == "" || hits[w.alpha].Summary != "" || leaks(hits, "alphahost") {
		t.Errorf("object grant none: title search %s", mustJSON(hits))
	}
	if hits, _ := w.find(t, object, "betahost"); hits[w.beta].EntityID == "" {
		t.Error("object grant none on alpha hides beta")
	}

	// A sensitive field without grant is not readable; a grant with its key
	// opens it.
	if _, err := f.Admin.Exec(bg, `INSERT INTO field_sensitivity (organization_id, object_kind, field_key) VALUES ($1, 'ci', 'core.serial_number')`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	plain, _ := w.role(t, "wp266-plain")
	hidden("sensitive", w.policy(t, plain))
	_, opened := w.role(t, "wp266-opened", [4]string{"ci", "", "core.serial_number", "read"})
	if hits, _ := w.find(t, opened, frSecret); hits[w.alpha].EntityID == "" {
		t.Error("sensitive field with grant: serial number finds no CI")
	}

	// Other kinds: a ticket field grant hides the description.
	_, ticket := w.role(t, "wp266-ticket", [4]string{"ticket", "", "core.description", "none"})
	if hits, _ := w.find(t, ticket, "ticketdesc266"); len(hits) != 0 {
		t.Errorf("ticket description none: %s", mustJSON(hits))
	}
	if hits, _ := w.find(t, ticket, "wp266 high"); hits[w.ticket].EntityID == "" {
		t.Error("ticket: title and readable priority find no ticket")
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// fieldRemote is a remote index that records writes and must not answer
// queries.
type fieldRemote struct {
	indexed, deleted []string
}

func (r *fieldRemote) Ping(context.Context) error { return nil }
func (r *fieldRemote) IndexDocument(_ context.Context, doc *search.Document) error {
	r.indexed = append(r.indexed, doc.EntityID)
	return nil
}
func (r *fieldRemote) Delete(_ context.Context, _, _, entityID string) error {
	r.deleted = append(r.deleted, entityID)
	return nil
}
func (r *fieldRemote) Query(context.Context, *search.Query) (search.Result, error) {
	return search.Result{}, errors.New("the remote index answered a query with restricted field permissions")
}
func (r *fieldRemote) ReindexTenant(context.Context, string) (search.ReindexResult, error) {
	return search.ReindexResult{}, nil
}

// TestSearchFieldPermissionsBackends covers the backends and channels of
// WP-266: the hybrid backend answers a restricted query from PostgreSQL,
// whose field rows its writes keep; OpenSearch alone answers no hits; the
// memory index applies the same rule; REST GET /api/v1/search and GraphQL
// searchEverything with the negative case.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchFieldPermissionsBackends(t *testing.T) {
	w := newFRWorld(t)
	f := w.f
	ctx := f.OrgCtx(f.OrgA)
	_, none := w.role(t, "wp266-none", [4]string{"ci", "", "core.serial_number", "none"})

	// Hybrid: field rows in Source after IndexDocument, restricted queries
	// from Source, Delete removes the rows and the hit.
	remote := &fieldRemote{}
	hybrid := &search.HybridBackend{Remote: remote, Source: w.repo}
	item := &ci.Item{ID: w.alpha, OrganizationID: f.OrgA, Name: "wp266 alpha", Hostname: "hybridhost", SerialNumber: frSecret, ClientID: f.Client1}
	if err := search.NewCIIndexer(hybrid).IndexDocument(ctx, ptr(ci.IndexDocumentFor(item))); err != nil {
		t.Fatal(err)
	}
	if got := w.fields(t, w.alpha); got["core.hostname"] != "hybridhost" || len(remote.indexed) != 1 {
		t.Errorf("hybrid write: field rows %v, remote %v", got, remote.indexed)
	}
	res, err := frQuery(ctx, hybrid, f.OrgA, none, frSecret)
	if err != nil || len(res.Data) != 0 {
		t.Errorf("hybrid restricted serial search: %s %v", mustJSON(res), err)
	}
	if res, err = frQuery(ctx, hybrid, f.OrgA, none, "hybridhost"); err != nil || len(res.Data) != 1 || leaks(map[string]search.Hit{"": res.Data[0]}, frSecret) {
		t.Errorf("hybrid restricted hostname search: %s %v", mustJSON(res), err)
	}
	if err = hybrid.Delete(ctx, f.OrgA, "ci", w.alpha); err != nil {
		t.Fatal(err)
	}
	if got := w.fields(t, w.alpha); len(got) != 0 || len(remote.deleted) != 1 {
		t.Errorf("hybrid delete: field rows %v, remote %v", got, remote.deleted)
	}
	if res, err = frQuery(ctx, hybrid, f.OrgA, none, "hybridhost"); err != nil || len(res.Data) != 0 {
		t.Errorf("deleted CI: %s %v", mustJSON(res), err)
	}

	// OpenSearch alone: no hits, no request.
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		t.Errorf("OpenSearch called with restricted field permissions: %s", r.URL.Path)
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	osb := search.NewOpenSearchBackend(&search.OpenSearchConfig{URL: srv.URL, Index: "wp266"}, srv.Client())
	if res, err = frQuery(ctx, osb, f.OrgA, none, "wp266"); err != nil || len(res.Data) != 0 || res.Total != 0 {
		t.Errorf("OpenSearch restricted: %s %v", mustJSON(res), err)
	}

	// The memory index applies the rule over Document.Fields.
	mem := search.NewMemoryRepository()
	doc := ci.IndexDocumentFor(&ci.Item{ID: w.beta, OrganizationID: f.OrgA, Name: "wp266 beta", Hostname: "memhost", SerialNumber: frSecret})
	if err = search.NewCIIndexer(mem).IndexDocument(ctx, &doc); err != nil {
		t.Fatal(err)
	}
	if res, err = frQuery(ctx, mem, f.OrgA, none, frSecret); err != nil || len(res.Data) != 0 {
		t.Errorf("memory restricted serial search: %s %v", mustJSON(res), err)
	}
	if res, err = frQuery(ctx, mem, f.OrgA, none, "memhost"); err != nil || len(res.Data) != 1 || strings.Contains(mustJSON(res), frSecret) {
		t.Errorf("memory restricted hostname search: %s %v", mustJSON(res), err)
	}
	if res, err = frQuery(ctx, mem, f.OrgA, nil, frSecret); err != nil || len(res.Data) != 1 {
		t.Errorf("memory without policy: %s %v", mustJSON(res), err)
	}

	// REST: the policy of the request.
	if _, err = w.repo.ReindexTenant(ctx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	rest := func(policy *permission.FieldPolicy, text string) string {
		t.Helper()
		mux := chi.NewRouter()
		search.NewHandler(w.repo, permissionStub{granted: map[string]bool{"ci:read": true}}).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(text), nil)
		rctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		scope := database.OrgWideScope(f.OrgA, f.User)
		rctx = permission.WithPolicy(database.ContextWithTenantScope(rctx, &scope), policy)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(rctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := rest(none, frSecret); strings.Contains(body, w.alpha) || !strings.Contains(body, `"total":0`) {
		t.Errorf("REST serial search: %s", body)
	}
	if body := rest(none, "wp266 alpha"); !strings.Contains(body, w.alpha) || strings.Contains(body, frSecret) {
		t.Errorf("REST name search: %s", body)
	}
	if body := rest(permission.UnrestrictedPolicy(frPerms), frSecret); !strings.Contains(body, w.alpha) {
		t.Errorf("REST unrestricted serial search: %s", body)
	}

	// GraphQL searchEverything: the policy of the BFF.
	bff := graphqlbff.NewHandler(ci.NewMemoryRepository(), relationship.NewMemoryRepository()).
		WithSources(&graphqlbff.Sources{Search: w.repo}).WithEntitlements(allFeatures{})
	gql := func(policy *permission.FieldPolicy, text string) string {
		t.Helper()
		mux := chi.NewRouter()
		bff.RegisterRoutes(mux)
		body, _ := json.Marshal(map[string]string{"query": `{ searchEverything(q: "` + text + `") { total hits { entityId title summary } } }`})
		req := httptest.NewRequest(http.MethodPost, graphqlbff.Path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		rctx = identity.WithPrincipal(rctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Permissions: frPerms, Type: identity.PrincipalTypeUser})
		scope := database.OrgWideScope(f.OrgA, f.User)
		rctx = permission.WithPolicy(database.ContextWithTenantScope(rctx, &scope), policy)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(rctx))
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"errors"`) {
			t.Fatalf("searchEverything: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := gql(none, frSecret); strings.Contains(body, w.alpha) || !strings.Contains(body, `"total":0`) {
		t.Errorf("GraphQL serial search: %s", body)
	}
	if body := gql(none, "wp266 alpha"); !strings.Contains(body, w.alpha) || strings.Contains(body, frSecret) {
		t.Errorf("GraphQL name search: %s", body)
	}
	if body := gql(permission.UnrestrictedPolicy(frPerms), frSecret); !strings.Contains(body, w.alpha) {
		t.Errorf("GraphQL unrestricted serial search: %s", body)
	}
}

// allFeatures enables every feature of the BFF.
type allFeatures struct{}

func (allFeatures) IsEnabled(context.Context, string, string) bool { return true }
