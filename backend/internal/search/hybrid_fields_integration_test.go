package search_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// fullRemote is an httptest OpenSearch that keeps the raw body of every PUT
// of a document and answers _search with every stored body as _source, as
// an index would that ignored _source.excludes.
type fullRemote struct {
	*httptest.Server
	mu       sync.Mutex
	docs     map[string]map[string]any
	puts     int
	searches []string
}

func newFullRemote(t *testing.T) *fullRemote {
	t.Helper()
	r := &fullRemote{docs: map[string]map[string]any{}}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		raw, _ := io.ReadAll(req.Body)
		switch {
		case req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/_doc/"):
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			r.docs[body["entity_type"].(string)+"/"+body["entity_id"].(string)] = body
			r.puts++
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(req.URL.Path, "/_search"):
			r.searches = append(r.searches, string(raw))
			hits := []map[string]any{}
			for _, d := range r.docs {
				hits = append(hits, map[string]any{"_score": 1, "_source": d})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"total": map[string]any{"value": len(hits)}, "hits": hits}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

// state returns the number of PUTs and of _search calls so far.
func (r *fullRemote) state() (puts, searches int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.puts, len(r.searches)
}

// doc returns the stored body of a document as JSON ("" without one).
func (r *fullRemote) doc(entityType, id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.docs[entityType+"/"+id]
	if !ok {
		return ""
	}
	raw, _ := json.Marshal(d)
	return string(raw)
}

// count counts the statements whose SQL contains a part.
func (l *sentLog) count(part string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for i := range l.stmt {
		if strings.Contains(l.stmt[i].sql, part) {
			n++
		}
	}
	return n
}

// TestHybridSendsFullDocuments covers WP-787 (SRC-02, TEC-12) against
// PostgreSQL with an OpenSearch fake: the hybrid backend sends the remote
// index the full documents of the PostgreSQL index, read back after the
// commit with one statement per batch: the field rows of both sources
// (indexer and triggers), the identifiers of the indexer and those the
// triggers of interfaces, addresses and assets keep. The reindex job and
// ReindexPlacement copy the same. A rollback sends nothing. A query with
// restricted field permissions is still answered from PostgreSQL, and no
// hit carries the field rows or identifiers.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHybridSendsFullDocuments(t *testing.T) {
	f := scopetest.Seed(t, "87")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, searchable, inheritable)
		VALUES ($1, NULL, 'global', 'wp787_role', 'string', true, true)`, f.OrgA)
	alpha := f.CI(t, f.OrgA, f.Client1, "wp787 alpha")
	exec(`UPDATE ci SET hostname = 'wp787host', management_ip = '10.87.0.5', primary_mac = '08:00:2b:87:00:05',
		attributes = '{"wp787_role": "frontend87"}' WHERE id = $1`, alpha)
	var ifc string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name, mac) VALUES ($1, $2, 'eth87', '08:00:2b:87:aa:01')
		RETURNING id::text`, f.OrgA, alpha).Scan(&ifc); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ip_address (organization_id, interface_id, address) VALUES ($1, $2, '192.168.87.10')`, f.OrgA, ifc)
	asset := f.Asset(t, f.OrgA, f.Client1, "INV-87-A")
	beta := f.CI(t, f.OrgA, f.Client1, "wp787 beta")
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, beta, asset)

	cfg := f.App.Config()
	log := &sentLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	traced, poolErr := pgxpool.NewWithConfig(bg, cfg)
	if poolErr != nil {
		t.Fatal(poolErr)
	}
	t.Cleanup(traced.Close)
	const fullRead = "jsonb_object_agg(f.field, f.content)"

	remote := newFullRemote(t)
	hybrid := &search.HybridBackend{
		Remote: search.NewOpenSearchBackend(&search.OpenSearchConfig{URL: remote.URL, Index: "wp787"}, remote.Client()),
		Source: search.NewPGRepository(traced),
	}
	ctx := f.OrgCtx(f.OrgA)
	docs := []search.Document{
		{OrganizationID: f.OrgA, EntityType: "ci", EntityID: alpha, Title: "wp787 alpha", URL: "/cmdb/" + alpha, Fields: map[string]string{
			"core.hostname": "wp787host", "core.management_ip": "10.87.0.5", "core.primary_mac": "08:00:2b:87:00:05",
			"attributes.wp787_role": "frontend87",
		}},
		{OrganizationID: f.OrgA, EntityType: "ci", EntityID: beta, Title: "wp787 beta", URL: "/cmdb/" + beta, Fields: map[string]string{
			search.IdentifierAssetInventory: "INV-87-A",
		}},
	}
	expectDoc := func(step, entityType, id string, want ...string) {
		t.Helper()
		body := remote.doc(entityType, id)
		if body == "" {
			t.Errorf("%s: %s %s is not in the remote index", step, entityType, id)
			return
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: remote document of %s %s lacks %s: %s", step, entityType, id, w, body)
			}
		}
	}
	alphaWant := []string{
		`"fields":{"attributes.wp787_role":"frontend87","core.hostname":"wp787host"}`,
		`{"addr":"10.87.0.5","field":"core.management_ip","norm":"10.87.0.5"}`,
		`{"field":"core.primary_mac","norm":"08002b870005"}`,
		// The rows of the triggers of the interface and its address.
		`{"addr":"192.168.87.10","field":"network.ip","norm":"192.168.87.10"}`,
		`{"field":"network.mac","norm":"08002b87aa01"}`,
		`"client_id":"` + f.Client1 + `"`,
	}

	// In a transaction: no remote call and no read before the commit, then
	// one read for all documents of the transaction.
	if err := database.WithRequestTenantTx(ctx, traced, f.OrgA, func(ctx context.Context) error {
		for i := range docs {
			if err := hybrid.IndexDocument(ctx, &docs[i]); err != nil {
				return err
			}
		}
		if puts, _ := remote.state(); puts != 0 || log.count(fullRead) != 0 {
			t.Errorf("inside the transaction: %d remote PUTs and %d reads, want none", puts, log.count(fullRead))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if puts, _ := remote.state(); puts != 2 || log.count(fullRead) != 1 {
		t.Errorf("after the commit: %d remote PUTs and %d reads, want 2 and 1", puts, log.count(fullRead))
	}
	expectDoc("commit", "ci", alpha, alphaWant...)
	expectDoc("commit", "ci", beta, `{"field":"asset.inventory_no","norm":"inv-87-a"}`)

	// A rollback sends nothing.
	errAbort := errors.New("abort")
	if err := database.WithRequestTenantTx(ctx, traced, f.OrgA, func(ctx context.Context) error {
		if err := hybrid.IndexDocument(ctx, &docs[0]); err != nil {
			return err
		}
		return errAbort
	}); !errors.Is(err, errAbort) {
		t.Fatal(err)
	}
	if puts, _ := remote.state(); puts != 2 || log.count(fullRead) != 1 {
		t.Errorf("after a rollback: %d remote PUTs and %d reads, want 2 and 1", puts, log.count(fullRead))
	}

	// A batch outside a transaction: one read, at once.
	if err := hybrid.IndexDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	if puts, _ := remote.state(); puts != 4 || log.count(fullRead) != 2 {
		t.Errorf("batch: %d remote PUTs and %d reads, want 4 and 2", puts, log.count(fullRead))
	}

	// The reindex job copies the full documents page by page, the asset
	// with the field rows and the inventory number of its trigger; a
	// client-1 administrator rebuilds the whole organization (E-08).
	remote.mu.Lock()
	remote.docs = map[string]map[string]any{}
	remote.mu.Unlock()
	reads := log.count(fullRead)
	if _, err := hybrid.ReindexTenant(f.ClientCtx(f.Client1), f.OrgA); err != nil {
		t.Fatal(err)
	}
	if n := log.count(fullRead) - reads; n != 1 {
		t.Errorf("reindex: %d reads, want one page", n)
	}
	expectDoc("reindex", "ci", alpha, alphaWant...)
	expectDoc("reindex", "ci", beta, `{"field":"asset.inventory_no","norm":"inv-87-a"}`)
	expectDoc("reindex", "asset", asset, `{"field":"core.inventory_no","norm":"inv-87-a"}`, `"core.asset_tag":"INV-87-A"`)

	// ReindexPlacement copies the placed CI, its asset and the asset's CIs
	// with one read.
	remote.mu.Lock()
	remote.docs = map[string]map[string]any{}
	remote.mu.Unlock()
	reads = log.count(fullRead)
	if err := hybrid.ReindexPlacement(ctx, f.OrgA, beta); err != nil {
		t.Fatal(err)
	}
	if n := log.count(fullRead) - reads; n != 1 {
		t.Errorf("placement: %d reads, want 1", n)
	}
	expectDoc("placement", "ci", beta, `{"field":"asset.inventory_no","norm":"inv-87-a"}`)
	expectDoc("placement", "asset", asset, `{"field":"core.inventory_no","norm":"inv-87-a"}`, `"core.asset_tag":"INV-87-A"`)
	if remote.doc("ci", alpha) != "" {
		t.Error("placement: the unrelated CI alpha was copied")
	}

	// A query answered by the remote index: the request leaves out the
	// field rows and identifiers, and no hit carries them even when the
	// index returns them.
	if err := hybrid.IndexDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	res, queryErr := hybrid.Query(ctx, &search.Query{OrganizationID: f.OrgA, Text: "frontend87", Limit: 50, Highlight: true})
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	raw, _ := json.Marshal(res)
	if len(res.Data) == 0 || strings.Contains(string(raw), `"identifiers"`) || strings.Contains(string(raw), `"fields"`) {
		t.Errorf("remote hits %s: want hits without fields and identifiers", raw)
	}
	remote.mu.Lock()
	request := remote.searches[len(remote.searches)-1]
	remote.mu.Unlock()
	if !strings.Contains(request, `"_source":{"excludes":["fields","identifiers"]}`) {
		t.Errorf("the search request does not exclude fields and identifiers: %s", request)
	}

	// Restricted field permissions: PostgreSQL answers, the remote index is
	// not asked.
	roleID := f.ID()
	perms, _ := json.Marshal(frPerms)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp787-restricted', $3::jsonb)`, roleID, f.OrgA, string(perms))
	exec(`INSERT INTO field_grant (organization_id, role_id, object_kind, ci_type_id, field_key, level) VALUES ($1, $2, 'ci', NULL, 'core.hostname', 'none')`,
		f.OrgA, roleID)
	policy, policyErr := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(bg, f.OrgA, []identity.Grant{{Permissions: frPerms, RoleID: roleID}}, nil)
	if policyErr != nil {
		t.Fatal(policyErr)
	}
	_, searches := remote.state()
	for _, text := range []string{"wp787host", "frontend87"} {
		q := search.Query{OrganizationID: f.OrgA, Text: text, Limit: 50, Highlight: true, Policy: policy}
		got, err := hybrid.Query(ctx, &q)
		if err != nil {
			t.Fatal(err)
		}
		want, err := hybrid.Source.Query(ctx, &q)
		if err != nil {
			t.Fatal(err)
		}
		gotRaw, _ := json.Marshal(got)
		wantRaw, _ := json.Marshal(want)
		if string(gotRaw) != string(wantRaw) {
			t.Errorf("restricted %q: hybrid %s, PostgreSQL %s", text, gotRaw, wantRaw)
		}
		for _, h := range got.Data {
			if h.EntityID == alpha && text == "wp787host" {
				t.Errorf("restricted: alpha found by its unreadable hostname")
			}
		}
	}
	if _, n := remote.state(); n != searches {
		t.Errorf("restricted queries reached the remote index (%d searches)", n-searches)
	}
}
