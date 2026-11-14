package tenantapi_test

// WP-1155 (TLC-06, SEC-05): the purge outside the tables, on PostgreSQL
// with a file store, the vault with a test master key, an OpenSearch stub
// (httptest) and, with TEST_NATS_URL, the event stream on JetStream. The
// purge shreds the vault (org_dek first), removes the objects below
// org/<id>/ in the three buckets and the keys outside it (the CI exports
// <id>/<job>.<ext> with manifest, document versions from before 000066
// below documents/<id>/), the documents of the search index and
// the messages under events.<id>.>, invalidates the caches, writes
// org.deletion.external_purged with the counts and sets purged only when
// every step succeeded; the neighbouring organization keeps its objects,
// its vault and its messages.
//
// Retest: cd backend && TEST_DATABASE_URL=… TEST_NATS_URL=… go test -count=1 -run 'TestPurgeExternal' ./internal/tenantapi/

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

var purgeBuckets = []string{"exports", "reticora-documents", "reticora"}

// purgePrefixes are the keys of an organization outside org/<id>/, as the
// server wires them (workers.go).
var purgePrefixes = []tenantapi.ObjectPrefix{
	{Name: "exports", Bucket: export.ExportBucket, Prefix: export.JobPrefix},
	{Name: "legacy-documents", Bucket: document.Bucket, Prefix: document.LegacyPrefix},
}

// outsideKeys are the seeded keys of org outside org/<id>/ by bucket: a CI
// export with its manifest and a document version from before 000066.
func outsideKeys(org string) map[string][]string {
	return map[string][]string{
		export.ExportBucket: {export.JobPrefix(org) + "11111111-1111-4111-8111-111111111111.csv",
			export.JobPrefix(org) + "11111111-1111-4111-8111-111111111111.csv.manifest.json"},
		document.Bucket: {document.LegacyPrefix(org) + "d0/v0"},
	}
}

// searchStub is an OpenSearch stub of _delete_by_query; failing answers
// 500.
type searchStub struct {
	mu      sync.Mutex
	failing bool
	bodies  []string
	srv     *httptest.Server
}

func newSearchStub(t *testing.T) *searchStub {
	t.Helper()
	s := &searchStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/_delete_by_query") {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.bodies = append(s.bodies, string(body))
		if s.failing {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"deleted": 7, "failures": []}`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *searchStub) setFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = failing
}

func (s *searchStub) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

type externalEnv struct {
	*purgeEnv
	blobs    *blob.FileStore
	vault    *pii.Vault
	search   *searchStub
	stream   *events.Publisher
	js       jetstream.JetStream
	subjectA string
	subjectB string
	cached   []string
	// cacheFails makes the cache invalidation fail.
	cacheFails bool
	mu         sync.Mutex
}

// newExternalEnv seeds both organizations with objects in the three
// buckets and a vault subject each, and puts the deletion of A due.
func newExternalEnv(t *testing.T) *externalEnv {
	t.Helper()
	f := scopetest.Seed(t, "21")
	e := &externalEnv{purgeEnv: &purgeEnv{f: f, user: f.AppUser(t, f.OrgA, "wp1155-admin")}, blobs: blob.NewFileStore(t.TempDir()),
		search: newSearchStub(t)}
	ctx := context.Background()
	for _, org := range []string{f.OrgA, f.OrgB} {
		for _, bucket := range purgeBuckets {
			for _, key := range []string{"documents/d1/v1", "documents/d1/v2", "exports/full.zip"} {
				if err := e.blobs.Put(ctx, bucket, blob.OrgKey(org, key), strings.NewReader("x"), 1, ""); err != nil {
					t.Fatal(err)
				}
			}
		}
		for bucket, keys := range outsideKeys(org) {
			for _, key := range keys {
				if err := e.blobs.Put(ctx, bucket, key, strings.NewReader("x"), 1, ""); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	e.vault = pii.NewVault(f.App, enc)
	if e.subjectA, err = e.vault.Create(orgCtx(f.OrgA), f.OrgA, pii.KindPerson, pii.Data{"name": "Erika Mustermann"}); err != nil {
		t.Fatal(err)
	}
	if e.subjectB, err = e.vault.Create(orgCtx(f.OrgB), f.OrgB, pii.KindPerson, pii.Data{"name": "Max Muster"}); err != nil {
		t.Fatal(err)
	}
	if url := os.Getenv("TEST_NATS_URL"); url != "" {
		nc, cerr := nats.Connect(url, nats.Name("wp1155"))
		if cerr != nil {
			t.Fatal(cerr)
		}
		t.Cleanup(nc.Close)
		if e.js, err = jetstream.New(nc); err != nil {
			t.Fatal(err)
		}
		e.stream = events.NewPublisher(nil, e.js)
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err = e.stream.Setup(sctx); err != nil {
			t.Fatal(err)
		}
		for _, org := range []string{f.OrgA, f.OrgB} {
			for range 3 {
				if _, perr := e.js.Publish(sctx, events.SubjectPrefix+org+".ci.created", []byte(`{}`)); perr != nil {
					t.Fatal(perr)
				}
			}
		}
		t.Cleanup(func() {
			_, _ = e.stream.PurgeOrganization(context.Background(), f.OrgB)
		})
	}
	e.requestDue(t)
	return e
}

func orgCtx(org string) context.Context {
	scope := database.OrgWideScope(org, "")
	return database.ContextWithTenantScope(context.Background(), &scope)
}

// purger is the purge with every external store of the env.
func (e *externalEnv) purger() *tenantapi.Purger {
	x := &tenantapi.ExternalPurge{Blobs: e.blobs, Buckets: purgeBuckets, Prefixes: purgePrefixes,
		Search: search.NewOpenSearchBackend(&search.OpenSearchConfig{URL: e.search.srv.URL, Index: "reticora-search"}, nil),
		Caches: []tenantapi.CacheInvalidator{func(_ context.Context, orgID string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.cacheFails {
				return errors.New("cache unavailable")
			}
			e.cached = append(e.cached, orgID)
			return nil
		}}}
	if e.stream != nil {
		x.Stream = e.stream
	}
	return tenantapi.NewPurger(e.f.App, purgeTables).WithExternal(x)
}

func (e *externalEnv) keys(t *testing.T, org string) []string {
	t.Helper()
	var out []string
	for _, bucket := range purgeBuckets {
		keys, err := e.blobs.List(context.Background(), bucket, blob.OrgPrefix(org))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, keys...)
	}
	for _, p := range purgePrefixes {
		keys, err := e.blobs.List(context.Background(), p.Bucket, p.Prefix(org))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, keys...)
	}
	return out
}

func (e *externalEnv) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT deletion_state FROM organization WHERE id = $1`, e.f.OrgA).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// externalEntries returns the details of the entries
// org.deletion.external_purged of A written after id.
func (e *externalEnv) externalEntries(t *testing.T, after int64) []map[string]any {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT details FROM operator_audit
		WHERE target_org = $1 AND action = 'org.deletion.external_purged' AND id > $2 ORDER BY id`, e.f.OrgA, after)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (e *externalEnv) lastAudit(t *testing.T) int64 {
	t.Helper()
	var last int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	return last
}

// TestPurgeExternalRemovesObjects: no file below org/<A>/ in the three
// buckets, no CI export of A and no document version of A from before
// 000066; the objects of B stay; the caches of A are invalidated.
func TestPurgeExternalRemovesObjects(t *testing.T) {
	e := newExternalEnv(t)
	if _, err := e.purger().PurgeOrganization(context.Background(), e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if left := e.keys(t, e.f.OrgA); len(left) != 0 {
		t.Errorf("objects of A left: %v", left)
	}
	if kept := e.keys(t, e.f.OrgB); len(kept) != 12 {
		t.Errorf("objects of B: %v, want its 12", kept)
	}
	if !slices.Equal(e.cached, []string{e.f.OrgA}) {
		t.Errorf("caches invalidated for %v, want A once", e.cached)
	}
	if got := e.state(t); got != tenantapi.DeletionPurged {
		t.Errorf("state %s, want purged", got)
	}
}

// TestPurgeExternalShredsVault: after the purge the former surrogate of A
// resolves to no value and org_dek has no row of A; B keeps its subject.
func TestPurgeExternalShredsVault(t *testing.T) {
	e := newExternalEnv(t)
	bg := context.Background()
	if _, err := e.purger().PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	var keys, subjects int
	if err := e.f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM org_dek WHERE organization_id = $1),
		(SELECT count(*) FROM pii_subject WHERE organization_id = $1)`, e.f.OrgA).Scan(&keys, &subjects); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || subjects != 0 {
		t.Fatalf("vault of A: %d keys, %d subjects, want none", keys, subjects)
	}
	var resolved map[string]*pii.Subject
	if err := database.WithRequestTenant(orgCtx(e.f.OrgA), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		var rerr error
		resolved, rerr = e.vault.ResolveTx(ctx, tx, e.f.OrgA, []string{e.subjectA})
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 0 {
		t.Errorf("the surrogate of A resolves to %v after the shredding", resolved)
	}
	b, err := e.vault.Resolve(orgCtx(e.f.OrgB), e.f.OrgB, e.subjectB)
	if err != nil || b.Data["name"] != "Max Muster" {
		t.Errorf("subject of B: %+v %v, want it intact", b, err)
	}
}

// TestPurgeExternalSearchAndStream: _delete_by_query names organization_id
// of A; no message under events.<A>.> is left, those of B stay.
func TestPurgeExternalSearchAndStream(t *testing.T) {
	e := newExternalEnv(t)
	bg := context.Background()
	last := e.lastAudit(t)
	if _, err := e.purger().PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if bodies := e.search.calls(); len(bodies) != 1 || !strings.Contains(bodies[0], `"organization_id":"`+e.f.OrgA+`"`) {
		t.Errorf("delete_by_query bodies %v, want one on organization_id of A", bodies)
	}
	entries := e.externalEntries(t, last)
	if len(entries) != 1 {
		t.Fatalf("external_purged entries %v, want one", entries)
	}
	steps, _ := entries[0]["steps"].(map[string]any)
	if s, _ := steps["search"].(map[string]any); s["documents"] != 7.0 {
		t.Errorf("search counts %v, want 7 documents", steps["search"])
	}
	if e.js == nil {
		t.Skip("TEST_NATS_URL not set: the event stream case is skipped")
	}
	stream, err := e.js.Stream(bg, events.StreamName)
	if err != nil {
		t.Fatal(err)
	}
	count := func(org string) uint64 {
		info, ierr := stream.Info(bg, jetstream.WithSubjectFilter(events.SubjectPrefix+org+".>"))
		if ierr != nil {
			t.Fatal(ierr)
		}
		var n uint64
		for _, c := range info.State.Subjects {
			n += c
		}
		return n
	}
	if n := count(e.f.OrgA); n != 0 {
		t.Errorf("messages of A left: %d", n)
	}
	if n := count(e.f.OrgB); n < 3 {
		t.Errorf("messages of B: %d, want its 3", n)
	}
	if s, _ := steps["stream"].(map[string]any); s["messages"] == nil || s["messages"].(float64) < 3 {
		t.Errorf("stream counts %v, want at least the 3 messages of A", steps["stream"])
	}
}

// TestPurgeExternalRetriesWithoutPurged: with OpenSearch failing the run
// ends in purging without external_purged; the next run with a healthy
// index repeats only the open steps, sets purged and records the counts of
// the first run (vault and objects unchanged).
func TestPurgeExternalRetriesWithoutPurged(t *testing.T) {
	e := newExternalEnv(t)
	bg := context.Background()
	last := e.lastAudit(t)
	e.search.setFailing(true)
	if _, err := e.purger().PurgeOrganization(bg, e.f.OrgA); err == nil {
		t.Fatal("the run with a failing index succeeded")
	}
	if got := e.state(t); got != tenantapi.DeletionPurging {
		t.Fatalf("state after the failure %s, want purging", got)
	}
	if entries := e.externalEntries(t, last); len(entries) != 0 {
		t.Fatalf("external_purged written after a failure: %v", entries)
	}
	var keys int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM org_dek WHERE organization_id = $1`, e.f.OrgA).Scan(&keys); err != nil || keys != 0 {
		t.Errorf("org_dek of A after the first run: %d %v, want shredded already", keys, err)
	}
	e.search.setFailing(false)
	if _, err := e.purger().PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if got := e.state(t); got != tenantapi.DeletionPurged {
		t.Fatalf("state after the retry %s, want purged", got)
	}
	entries := e.externalEntries(t, last)
	if len(entries) != 1 {
		t.Fatalf("external_purged entries %v, want one", entries)
	}
	steps, _ := entries[0]["steps"].(map[string]any)
	vault, _ := steps["vault"].(map[string]any)
	if vault["keys"] != 1.0 || vault["subjects"] != 1.0 {
		t.Errorf("vault counts %v, want the key and the subject of the first run", vault)
	}
	if objects, _ := steps["objects:exports"].(map[string]any); objects["objects"] != 3.0 {
		t.Errorf("object counts of exports %v, want the 3 of the first run", objects)
	}
	if entries[0]["objects"] != 12.0 {
		t.Errorf("objects %v, want 12 (9 below org/<id>/, 2 of the CI export, 1 legacy document version)", entries[0]["objects"])
	}
	if objects, _ := steps["objects:exports:exports"].(map[string]any); objects["objects"] != 2.0 {
		t.Errorf("object counts of the CI exports %v, want 2", objects)
	}
	if n := len(e.search.calls()); n != 2 {
		t.Errorf("delete_by_query calls %d, want the failed and the retried one", n)
	}
}

// TestPurgeExternalCacheFailureKeepsPurging: a failing cache invalidation
// leaves the organization in purging without external_purged (the step
// stays open); the next run with a healthy cache sets purged.
func TestPurgeExternalCacheFailureKeepsPurging(t *testing.T) {
	e := newExternalEnv(t)
	bg := context.Background()
	last := e.lastAudit(t)
	e.cacheFails = true
	if _, err := e.purger().PurgeOrganization(bg, e.f.OrgA); err == nil {
		t.Fatal("the run with a failing cache succeeded")
	}
	if got := e.state(t); got != tenantapi.DeletionPurging {
		t.Fatalf("state after the cache failure %s, want purging", got)
	}
	if entries := e.externalEntries(t, last); len(entries) != 0 {
		t.Fatalf("external_purged written after a cache failure: %v", entries)
	}
	e.cacheFails = false
	if _, err := e.purger().PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if got := e.state(t); got != tenantapi.DeletionPurged {
		t.Errorf("state after the retry %s, want purged", got)
	}
	if !slices.Equal(e.cached, []string{e.f.OrgA}) {
		t.Errorf("caches invalidated for %v, want A once", e.cached)
	}
}
