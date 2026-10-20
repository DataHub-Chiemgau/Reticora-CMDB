package search_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// remoteIndex records the documents sent to the remote index.
type remoteIndex struct {
	search.Backend
	indexed []string
	fail    error
}

func (r *remoteIndex) IndexDocument(_ context.Context, doc search.Document) error {
	r.indexed = append(r.indexed, doc.EntityID)
	return r.fail
}

// TestHybridIndexesRemoteAfterCommit covers WP-332 (NFR-03) against
// PostgreSQL: inside a transaction in the context (the ingest's record
// transaction) the remote index, an HTTP call, is updated only after the
// commit and not at all after a rollback; outside a transaction it is
// updated at once and its error is returned.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHybridIndexesRemoteAfterCommit(t *testing.T) {
	f := scopetest.Seed(t, "e5")
	ctx := f.OrgCtx(f.OrgA)
	remote := &remoteIndex{}
	backend := &search.HybridBackend{Remote: remote, Source: search.NewPGRepository(f.App)}
	// The PostgreSQL index takes entity ids (uuid) only.
	ids := map[string]string{"committed": f.ID(), "rolled-back": f.ID(), "direct": f.ID()}
	doc := func(name string) search.Document {
		return search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: ids[name], Title: name}
	}
	inPG := func(name string) bool {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM search_document WHERE entity_id = $1`, ids[name]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := backend.IndexDocument(ctx, doc("committed")); err != nil {
			return err
		}
		if len(remote.indexed) != 0 {
			t.Error("remote index called inside the transaction")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	errAbort := errors.New("abort")
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := backend.IndexDocument(ctx, doc("rolled-back")); err != nil {
			return err
		}
		return errAbort
	}); !errors.Is(err, errAbort) {
		t.Fatal(err)
	}
	if len(remote.indexed) != 1 || remote.indexed[0] != ids["committed"] {
		t.Errorf("remote index after commit and rollback: %v, want only the committed document", remote.indexed)
	}
	// The PostgreSQL index, the source of the hit check (WP-346), takes
	// the document in the transaction.
	if !inPG("committed") || inPG("rolled-back") {
		t.Errorf("PostgreSQL index after commit and rollback: committed %v, rolled back %v; want true, false", inPG("committed"), inPG("rolled-back"))
	}

	remote.fail = errors.New("remote down")
	if err := backend.IndexDocument(ctx, doc("direct")); !errors.Is(err, remote.fail) || len(remote.indexed) != 2 {
		t.Errorf("outside a transaction: %v, %v; want the remote call at once with its error", err, remote.indexed)
	}
	if !inPG("direct") {
		t.Error("outside a transaction: the PostgreSQL index misses the document")
	}
}

// scriptedRemote is a remote index whose _search answers with fixed hits and
// which records the documents it is sent.
type scriptedRemote struct {
	search.Backend
	hits    []search.Hit
	indexed []search.Document
}

func (r *scriptedRemote) IndexDocument(_ context.Context, doc search.Document) error {
	r.indexed = append(r.indexed, doc)
	return nil
}

func (r *scriptedRemote) Query(_ context.Context, q search.Query) (search.Result, error) {
	return search.Result{Data: append([]search.Hit(nil), r.hits...), Total: len(r.hits) + 5, Limit: q.Limit}, nil
}

// TestHybridReindexCarriesScope covers WP-033/WP-346 (TEC-12) against
// PostgreSQL: the reindex copies every document to the remote index with
// the client and site that PostgreSQL derived from the entity, so remote
// queries can filter on them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHybridReindexCarriesScope(t *testing.T) {
	f := scopetest.Seed(t, "e5")
	room := f.Room(t, f.Client1, "hy-r1")
	var site string
	if err := f.Admin.QueryRow(context.Background(), `SELECT site_id::text FROM location WHERE id = $1`, room).Scan(&site); err != nil {
		t.Fatal(err)
	}
	ciID := f.CI(t, f.OrgA, f.Client1, "hy-ci")
	if _, err := f.Admin.Exec(context.Background(), `UPDATE ci SET location_id = $2 WHERE id = $1`, ciID, room); err != nil {
		t.Fatal(err)
	}
	orgCI := f.CI(t, f.OrgA, "", "hy-org-ci")
	assetID := f.Asset(t, f.OrgA, f.Client2, "HY-A1")

	remote := &scriptedRemote{}
	backend := &search.HybridBackend{Remote: remote, Source: search.NewPGRepository(f.App)}
	if _, err := backend.ReindexTenant(f.ClientCtx(f.Client1), f.OrgA); err != nil {
		t.Fatal(err)
	}
	got := map[string]search.Document{}
	for _, d := range remote.indexed {
		got[d.EntityType+"/"+d.EntityID] = d
	}
	for _, c := range []struct {
		key, client, site string
	}{
		{"ci/" + ciID, f.Client1, site},
		{"ci/" + orgCI, "", ""},
		// A client-1 administrator rebuilds the whole organization (E-08).
		{"asset/" + assetID, f.Client2, ""},
		{"location/" + room, f.Client1, site},
	} {
		d, ok := got[c.key]
		if !ok {
			t.Errorf("%s: not sent to the remote index", c.key)
			continue
		}
		if d.ClientID != c.client || d.SiteID != c.site || d.OrganizationID != f.OrgA {
			t.Errorf("%s: remote document scope org=%s client=%q site=%q, want client=%q site=%q", c.key, d.OrganizationID, d.ClientID, d.SiteID, c.client, c.site)
		}
	}
}

// TestHybridQueryChecksHitsAgainstPostgres covers WP-346 (TEC-12, TEN-05)
// against PostgreSQL: remote hits are returned only when search_document
// holds them visible under the request's tenant scope. Hits of every entity
// type that PostgreSQL does not know, hits whose entity left the scope and
// hits of another organization are dropped; total and has_more shrink with
// them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHybridQueryChecksHitsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e5")
	own := f.CI(t, f.OrgA, f.Client1, "hy-own")
	foreign := f.CI(t, f.OrgA, f.Client2, "hy-foreign")
	orgWide := f.CI(t, f.OrgA, "", "hy-orgwide")
	asset := f.Asset(t, f.OrgA, f.Client1, "HY-Q1")
	pg := search.NewPGRepository(f.App)
	backend := &search.HybridBackend{Remote: &scriptedRemote{}, Source: pg}
	if _, err := backend.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatal(err)
	}

	hit := func(entityType, id, client string) search.Hit {
		return search.Hit{Document: search.Document{OrganizationID: f.OrgA, EntityType: entityType, EntityID: id, Title: "hy", ClientID: client}}
	}
	// Stale remote documents: the foreign CI claims client 1 or no client.
	hits := []search.Hit{
		hit("ci", own, f.Client1),
		hit("ci", strings.ToUpper(orgWide), ""),
		hit("asset", asset, f.Client1),
		hit("ci", foreign, f.Client1),
		hit("ci", foreign, ""),
	}
	// Entities PostgreSQL does not hold, of every indexed type.
	for _, entityType := range []string{"ci", "asset", "document", "ticket", "contact", "compliance", "location", "reservation"} {
		hits = append(hits, hit(entityType, f.ID(), ""))
	}
	backend.Remote = &scriptedRemote{hits: hits}

	res, err := backend.Query(f.ClientCtx(f.Client1), search.Query{OrganizationID: f.OrgA, Text: "hy", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range res.Data {
		got = append(got, h.EntityType+"/"+strings.ToLower(h.EntityID))
	}
	want := []string{"ci/" + own, "ci/" + orgWide, "asset/" + asset}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("client-1 hits %v, want %v", got, want)
	}
	if dropped := len(hits) - len(want); res.Total != len(hits)+5-dropped || !res.HasMore {
		t.Errorf("total %d has_more %v, want %d and true", res.Total, res.HasMore, len(hits)+5-dropped)
	}

	// The org-wide principal gets the foreign CI as well; organization B
	// gets nothing of organization A.
	res, err = backend.Query(f.OrgCtx(f.OrgA), search.Query{OrganizationID: f.OrgA, Text: "hy"})
	if err != nil || len(res.Data) != 5 {
		t.Errorf("org-wide principal: %d hits (%v), want 5", len(res.Data), err)
	}
	res, err = backend.Query(f.OrgCtx(f.OrgB), search.Query{OrganizationID: f.OrgB, Text: "hy"})
	if err != nil || len(res.Data) != 0 {
		t.Errorf("organization B: %d hits (%v), want none", len(res.Data), err)
	}
}
