package search_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// realOpenSearch talks to the OpenSearch service of TEST_OPENSEARCH_URL
// directly, to read what the backend wrote (mapping, documents) and to make
// it searchable (refresh).
type realOpenSearch struct {
	url, index string
	client     *http.Client
}

// do sends a request and decodes the JSON answer into out (when not nil); it
// returns the status code.
func (o *realOpenSearch) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("opensearch %s %s: marshal: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, o.url+path, reader)
	if err != nil {
		t.Fatalf("opensearch %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := o.client.Do(req)
	if err != nil {
		t.Fatalf("opensearch %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("opensearch %s %s: read answer: %v", method, path, err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("opensearch %s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return res.StatusCode
}

// refresh makes every document written so far searchable. The index
// refreshes itself only once a second; without the explicit refresh the
// queries below could miss a write and pass for the wrong reason.
func (o *realOpenSearch) refresh(t *testing.T) {
	t.Helper()
	if code := o.do(t, http.MethodPost, "/"+o.index+"/_refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("refresh index %s: status %d", o.index, code)
	}
}

// source returns the stored document of a CI as OpenSearch holds it.
func (o *realOpenSearch) source(t *testing.T, orgID, ciID string) search.Document {
	t.Helper()
	var got struct {
		Found  bool            `json:"found"`
		Source search.Document `json:"_source"`
	}
	if code := o.do(t, http.MethodGet, fmt.Sprintf("/%s/_doc/%s-ci-%s", o.index, orgID, ciID), nil, &got); code != http.StatusOK || !got.Found {
		t.Fatalf("CI %s is not in the OpenSearch index (status %d)", ciID, code)
	}
	return got.Source
}

// TestOpenSearchScopeAgainstRealService covers WP-534 (TEC-12; WP-033)
// against a real OpenSearch service and PostgreSQL: the CIs are indexed
// through HybridBackend as cmd/server/main.go wires it, the index carries
// the explicit mapping (organization, client and site as keyword), and the
// term filters on client_id and site_id run in OpenSearch: for every
// principal (org-wide, client 1, site 1, organization B) OpenSearch counts
// exactly the hits of its scope, the backend's post-filter drops none, and
// the hybrid backend returns the same set. After a rack mount moves a CI
// into site B its OpenSearch document carries site B, the site-1 principal
// no longer finds it and the site-B principal does. Without a tenant scope,
// or with the scope of another organization, the query fails closed.
//
// It runs only with TEST_OPENSEARCH_URL (the DB job of
// .github/workflows/ci.yml starts the service) and TEST_DATABASE_URL (see
// scopetest.Seed).
func TestOpenSearchScopeAgainstRealService(t *testing.T) {
	osURL := strings.TrimRight(os.Getenv("TEST_OPENSEARCH_URL"), "/")
	if osURL == "" {
		t.Skip("TEST_OPENSEARCH_URL not set; skipping OpenSearch integration test")
	}
	f := scopetest.Seed(t, "df")
	bg := context.Background()
	ctxA, ctxB := f.OrgCtx(f.OrgA), f.OrgCtx(f.OrgB)

	o := &realOpenSearch{url: osURL, index: fmt.Sprintf("reticora-wp534-%d", time.Now().UnixNano()), client: &http.Client{Timeout: 70 * time.Second}}
	var health struct {
		Status   string `json:"status"`
		TimedOut bool   `json:"timed_out"`
	}
	if code := o.do(t, http.MethodGet, "/_cluster/health?wait_for_status=yellow&timeout=60s", nil, &health); code != http.StatusOK || health.TimedOut {
		t.Fatalf("OpenSearch at %s is not ready: status %d, cluster %q", osURL, code, health.Status)
	}
	t.Cleanup(func() {
		o.do(t, http.MethodDelete, "/"+o.index, nil, nil)
		o.do(t, http.MethodDelete, "/_index_template/"+o.index, nil, nil)
	})

	remote := search.NewOpenSearchBackend(search.OpenSearchConfig{URL: osURL, Index: o.index}, nil)
	if err := remote.Ping(bg); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := remote.EnsureIndexTemplate(bg); err != nil {
		t.Fatalf("index template: %v", err)
	}
	source := search.NewPGRepository(f.App)
	hybrid := &search.HybridBackend{Remote: remote, Source: source}
	// The wiring of cmd/server/main.go.
	cis := ci.NewIndexingRepository(ci.NewPGRepository(f.App), search.NewCIIndexer(hybrid))
	racks := rack.NewIndexingRepository(rack.NewPGRepository(f.App), hybrid)

	roomA, roomB := f.Room(t, f.Client1, "wp534-a"), f.Room(t, f.Client1, "wp534-b")
	siteOf := func(room string) string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM location WHERE id = $1`, room).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	site1, siteB := siteOf(roomA), siteOf(roomB)

	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	// All CI names carry the token wp534, the text of every query.
	newCI := func(ctx context.Context, org, name, client, location string) string {
		t.Helper()
		item := &ci.Item{OrganizationID: org, ClientID: client, CITypeID: typeID, LocationID: location, Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatalf("create CI %s: %v", name, err)
		}
		return item.ID
	}
	orgWide := newCI(ctxA, f.OrgA, "wp534 org-wide", "", "")
	c1Site1 := newCI(ctxA, f.OrgA, "wp534 client-1 site-1", f.Client1, roomA)
	c1SiteB := newCI(ctxA, f.OrgA, "wp534 client-1 site-b", f.Client1, roomB)
	c2 := newCI(ctxA, f.OrgA, "wp534 client-2", f.Client2, "")
	orgB := newCI(ctxB, f.OrgB, "wp534 org-b", "", "")
	names := map[string]string{orgWide: "org-wide", c1Site1: "client-1 site-1", c1SiteB: "client-1 site-b", c2: "client-2", orgB: "org-b"}

	// The index carries the mapping of the template: a term filter on an
	// analyzed text field would not match a UUID.
	var mapping map[string]struct {
		Mappings struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if code := o.do(t, http.MethodGet, "/"+o.index+"/_mapping", nil, &mapping); code != http.StatusOK {
		t.Fatalf("mapping of %s: status %d", o.index, code)
	}
	for _, field := range []string{"organization_id", "client_id", "site_id", "entity_type", "entity_id"} {
		if got := mapping[o.index].Mappings.Properties[field].Type; got != "keyword" {
			t.Errorf("mapping: %s has type %q, want keyword", field, got)
		}
	}
	// The documents carry the client and site of their CI.
	for _, d := range []struct{ org, id, client, site string }{
		{f.OrgA, orgWide, "", ""},
		{f.OrgA, c1Site1, f.Client1, site1},
		{f.OrgA, c1SiteB, f.Client1, siteB},
		{f.OrgA, c2, f.Client2, ""},
		{f.OrgB, orgB, "", ""},
	} {
		if got := o.source(t, d.org, d.id); got.ClientID != d.client || got.SiteID != d.site {
			t.Errorf("OpenSearch document of %s: client %q, site %q; want %q, %q", names[d.id], got.ClientID, got.SiteID, d.client, d.site)
		}
	}
	o.refresh(t)

	scopeOf := func(org, client, site string) *database.TenantScope {
		scope := database.OrgWideScope(org, f.User)
		if client != "" {
			scope.Clients = database.ScopeIDs(client)
		}
		if site != "" {
			scope.Sites = database.ScopeIDs(site)
		}
		return &scope
	}
	sorted := func(ids []string) string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, names[id])
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	// expect checks the hits of one principal: OpenSearch itself counts
	// exactly the hits of the scope (Total comes from OpenSearch, before the
	// backend's post-filter), the post-filter drops none of them, and the
	// hybrid backend and the PostgreSQL index return the same set.
	expect := func(step, principal string, scope *database.TenantScope, want ...string) {
		t.Helper()
		ctx := database.ContextWithTenantScope(bg, scope)
		q := search.Query{OrganizationID: scope.OrgID, Text: "wp534", EntityTypes: []string{"ci"}, Limit: 50}
		ids := func(res search.Result) []string {
			var out []string
			for _, h := range res.Data {
				out = append(out, h.EntityID)
			}
			return out
		}
		raw, err := remote.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %s principal: OpenSearch query: %v", step, principal, err)
		}
		if raw.Total != len(want) || len(raw.Data) != raw.Total {
			t.Errorf("%s: %s principal: OpenSearch counts %d hits and returns %d (%s), want %d (%s)",
				step, principal, raw.Total, len(raw.Data), sorted(ids(raw)), len(want), sorted(want))
		}
		if got := sorted(ids(raw)); got != sorted(want) {
			t.Errorf("%s: %s principal: OpenSearch hits [%s], want [%s]", step, principal, got, sorted(want))
		}
		res, err := hybrid.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %s principal: hybrid query: %v", step, principal, err)
		}
		if got := sorted(ids(res)); got != sorted(want) || res.Total != len(want) {
			t.Errorf("%s: %s principal: hybrid hits [%s] (total %d), want [%s]", step, principal, got, res.Total, sorted(want))
		}
		pg, err := source.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %s principal: PostgreSQL query: %v", step, principal, err)
		}
		if got := sorted(ids(pg)); got != sorted(want) {
			t.Errorf("%s: %s principal: PostgreSQL index hits [%s], want [%s]", step, principal, got, sorted(want))
		}
	}

	expect("before the mount", "org-wide", scopeOf(f.OrgA, "", ""), orgWide, c1Site1, c1SiteB, c2)
	expect("before the mount", "client-1", scopeOf(f.OrgA, f.Client1, ""), orgWide, c1Site1, c1SiteB)
	expect("before the mount", "site-1", scopeOf(f.OrgA, "", site1), orgWide, c1Site1, c2)
	expect("before the mount", "site-b", scopeOf(f.OrgA, "", siteB), orgWide, c1SiteB, c2)
	expect("before the mount", "organization B", scopeOf(f.OrgB, "", ""), orgB)

	// POST /api/v1/racks/{id}/mounts: the site-1 CI moves into a rack of
	// site B (WP-346).
	rackB := &rack.Rack{OrganizationID: f.OrgA, RoomID: roomB, Name: "wp534-rack-b", HeightUnits: 42}
	if err := racks.CreateRack(ctxA, rackB); err != nil {
		t.Fatalf("create rack: %v", err)
	}
	if err := racks.CreateMount(ctxA, &rack.RackMount{OrganizationID: f.OrgA, RackID: rackB.ID, CIID: c1Site1, PositionU: 1, HeightU: 1, Face: "front"}); err != nil {
		t.Fatalf("mount CI: %v", err)
	}
	var dbSite string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(site_id::text, '') FROM ci WHERE id = $1`, c1Site1).Scan(&dbSite); err != nil {
		t.Fatal(err)
	}
	if dbSite != siteB {
		t.Fatalf("setup: ci.site_id after the mount = %q, want site B %s", dbSite, siteB)
	}
	if got := o.source(t, f.OrgA, c1Site1).SiteID; got != siteB {
		t.Errorf("after the mount: the OpenSearch document carries site %q, want site B %s", got, siteB)
	}
	o.refresh(t)
	expect("after the mount", "site-1", scopeOf(f.OrgA, "", site1), orgWide, c2)
	expect("after the mount", "site-b", scopeOf(f.OrgA, "", siteB), orgWide, c1Site1, c1SiteB, c2)
	expect("after the mount", "client-1", scopeOf(f.OrgA, f.Client1, ""), orgWide, c1Site1, c1SiteB)
	expect("after the mount", "org-wide", scopeOf(f.OrgA, "", ""), orgWide, c1Site1, c1SiteB, c2)

	// Fail-closed: no tenant scope, or the scope of another organization.
	q := search.Query{OrganizationID: f.OrgA, Text: "wp534", Limit: 50}
	for _, b := range []struct {
		name    string
		backend search.Backend
	}{{"OpenSearch", remote}, {"hybrid", hybrid}} {
		if res, err := b.backend.Query(bg, q); !errors.Is(err, database.ErrNoTenantScope) || len(res.Data) != 0 {
			t.Errorf("%s query without tenant scope: %d hits, error %v; want none and ErrNoTenantScope", b.name, len(res.Data), err)
		}
		other := database.ContextWithTenantScope(bg, scopeOf(f.OrgB, "", ""))
		if res, err := b.backend.Query(other, q); !errors.Is(err, database.ErrTenantMismatch) || len(res.Data) != 0 {
			t.Errorf("%s query for organization A with the scope of organization B: %d hits, error %v; want none and ErrTenantMismatch", b.name, len(res.Data), err)
		}
	}
}
