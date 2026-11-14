package vulnfeed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fixtureFingerprint is the fingerprint of testdata/csaf/signing-key.asc,
// the key example-advisory.json.asc is signed with.
const fixtureFingerprint = "3C59E1FA06833A69AAD7DFA1FD2433A45D3A7AD4"

// memStore is the catalog in memory.
type memStore struct {
	mu       sync.Mutex
	advisory map[string]Advisory
	affected map[string][]Affected
	sources  map[string]Source
	writes   int
}

func newMemStore() *memStore {
	return &memStore{advisory: map[string]Advisory{}, affected: map[string][]Affected{}, sources: map[string]Source{}}
}

func (m *memStore) ReplaceAdvisory(_ context.Context, adv *Advisory, affected []Affected) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if adv.ID == "" || adv.SourceID == "" {
		return ErrInvalidAdvisory
	}
	// vuln_advisory.source_id references vuln_source (foreign key).
	if _, ok := m.sources[adv.SourceID]; !ok {
		return fmt.Errorf("advisory %s: source %s not stored (foreign key)", adv.ID, adv.SourceID)
	}
	if cur, ok := m.advisory[adv.ID]; ok && cur.SourceID != adv.SourceID {
		return ErrAdvisoryOwnedByOtherSource
	}
	// The KEV flag only rises on a write, like PGRepository: a stored
	// flag or another flagged advisory of one of its CVEs.
	stored := *adv
	if cur, ok := m.advisory[adv.ID]; ok && cur.KEV {
		stored.KEV = true
	}
	for _, o := range m.advisory {
		for _, c := range o.CVEIDs {
			if o.KEV && slices.Contains(adv.CVEIDs, c) {
				stored.KEV = true
			}
		}
	}
	m.advisory[adv.ID] = stored
	out := make([]Affected, len(affected))
	for i, a := range affected {
		a.AdvisoryID, a.Vendor, a.Product = adv.ID, Normalize(a.Vendor), Normalize(a.Product)
		if a.VersionScheme == "" {
			a.VersionScheme = "generic"
		}
		out[i] = a
	}
	m.affected[adv.ID] = out
	m.writes++
	return nil
}

func (m *memStore) UpdateSource(ctx context.Context, src *Source) error {
	if err := ctx.Err(); err != nil {
		return err // like PGRepository with a cancelled context
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src.UpdatedAt = time.Now()
	m.sources[src.ID] = *src
	return nil
}

func (m *memStore) GetSource(_ context.Context, id string) (*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sources[id]
	if !ok {
		return nil, fmt.Errorf("source %s: %w", id, pgx.ErrNoRows)
	}
	return &s, nil
}

func (m *memStore) advisories() map[string]Advisory {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Advisory{}
	for k, v := range m.advisory {
		out[k] = v
	}
	return out
}

// feedServer is an HTTPS server of feed files with ETags and request
// counts.
type feedServer struct {
	*httptest.Server
	mu        sync.Mutex
	files     map[string][]byte
	redirects map[string]string
	requests  map[string]int
	notMod    map[string]int
	status    map[string]int // a status answered instead of the file
}

func newFeedServer(t *testing.T) *feedServer {
	t.Helper()
	s := &feedServer{files: map[string][]byte{}, redirects: map[string]string{}, requests: map[string]int{}, notMod: map[string]int{},
		status: map[string]int{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *feedServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[r.URL.Path]++
	if code, ok := s.status[r.URL.Path]; ok {
		w.WriteHeader(code)
		return
	}
	if to, ok := s.redirects[r.URL.Path]; ok {
		http.Redirect(w, r, to, http.StatusFound)
		return
	}
	body, ok := s.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		s.notMod[r.URL.Path]++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	_, _ = w.Write(body)
}

func (s *feedServer) put(path string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = body
}

func (s *feedServer) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
}

func (s *feedServer) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[path]
}

// publish serves a document with its .sha512 and its signature by signer
// (nil: the fixture signature of example-advisory.json).
func (s *feedServer) publish(t *testing.T, path string, doc []byte, signer *openpgp.Entity) {
	t.Helper()
	s.put(path, doc)
	sum := sha512.Sum512(doc)
	s.put(path+".sha512", []byte(hex.EncodeToString(sum[:])+"  "+filepath.Base(path)+"\n"))
	if signer == nil {
		s.put(path+".asc", readFixture(t, "csaf/example-advisory.json.asc"))
		return
	}
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, signer, bytes.NewReader(doc), nil); err != nil {
		t.Fatal(err)
	}
	s.put(path+".asc", sig.Bytes())
}

// changes serves changes.csv of the directory with the entries (path →
// time), newest first like a provider.
func (s *feedServer) changes(dir string, entries map[string]time.Time) {
	type e struct {
		path string
		at   time.Time
	}
	var list []e
	for p, at := range entries {
		list = append(list, e{p, at})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.After(list[j].at) })
	var b strings.Builder
	for _, x := range list {
		fmt.Fprintf(&b, "%q,%q\n", x.path, x.at.UTC().Format(time.RFC3339))
	}
	s.put(dir+"changes.csv", []byte(b.String()))
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const (
	metadataPath = "/.well-known/csaf/provider-metadata.json"
	whiteDir     = "/.well-known/csaf/white/"
	keyPath      = "/.well-known/csaf/openpgp/signing-key.asc"
)

// serveProvider serves the provider metadata of the fixture with the key
// of signer (nil: the fixture key).
func serveProvider(t *testing.T, s *feedServer, signer *openpgp.Entity) {
	t.Helper()
	fp := fixtureFingerprint
	key := readFixture(t, "csaf/signing-key.asc")
	if signer != nil {
		fp = strings.ToUpper(hex.EncodeToString(signer.PrimaryKey.Fingerprint))
		var b bytes.Buffer
		w, err := armorPublic(&b)
		if err != nil {
			t.Fatal(err)
		}
		if err := signer.Serialize(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		key = b.Bytes()
	}
	meta := strings.NewReplacer("{{BASE}}", s.URL, "{{FINGERPRINT}}", fp).Replace(string(readFixture(t, "csaf/provider-metadata.json")))
	s.put(metadataPath, []byte(meta))
	s.put(keyPath, key)
}

// armorPublic armors a public key.
func armorPublic(w *bytes.Buffer) (interface {
	Write([]byte) (int, error)
	Close() error
}, error) {
	return armor.Encode(w, openpgp.PublicKeyType, nil)
}

// newSigner is an ephemeral signing key of a test.
func newSigner(t *testing.T) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity("Reticora CSAF test", "", "csaf@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// csafDoc is a minimal CSAF 2.0 document.
func csafDoc(id, cve string, released time.Time) []byte {
	return []byte(fmt.Sprintf(`{"document":{"category":"csaf_security_advisory","csaf_version":"2.0","title":"Advisory %[1]s",
"publisher":{"category":"vendor","name":"Example Vendor","namespace":"https://example.com"},
"tracking":{"id":"%[1]s","status":"final","version":"1","initial_release_date":"%[3]s","current_release_date":"%[3]s",
"revision_history":[{"number":"1","date":"%[3]s","summary":"Initial release"}]}},
"product_tree":{"branches":[{"category":"vendor","name":"Example Vendor","branches":[{"category":"product_name","name":"ExampleOS",
"branches":[{"category":"product_version","name":"1.0","product":{"name":"ExampleOS 1.0","product_id":"P1"}}]}]}]},
"vulnerabilities":[{"cve":"%[2]s","product_status":{"known_affected":["P1"]}}]}`, id, cve, released.UTC().Format(time.RFC3339)))
}

func newTestCSAF(t *testing.T, store Store, s *feedServer, providers ...string) *CSAF {
	t.Helper()
	if len(providers) == 0 {
		providers = []string{s.URL + metadataPath}
	}
	c, err := NewCSAF(store, &CSAFConfig{Providers: providers, Explicit: true, Client: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func docCount(result string) float64 {
	return testutil.ToFloat64(documentsTotal.WithLabelValues(KindCSAF, result))
}

// TestCSAFDiscoveryFromProviderMetadata covers AK1, AK2 and AK5: the
// provider metadata names the directory distribution and the key; the
// fixture advisory (signed with the fixture key) is imported, the source
// keeps cursor, sync time and counters; an aggregator mirrored on its own
// host leads to the same provider.
func TestCSAFDiscoveryFromProviderMetadata(t *testing.T) {
	s := newFeedServer(t)
	serveProvider(t, s, nil)
	released := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	s.publish(t, whiteDir+"2026/ex-2026-0001.json", readFixture(t, "csaf/example-advisory.json"), nil)
	s.changes(whiteDir, map[string]time.Time{"2026/ex-2026-0001.json": released})
	store := newMemStore()
	before := docCount(ResultImported)
	if err := newTestCSAF(t, store, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	adv, ok := store.advisories()["csaf:example.com:EX-2026-0001"]
	if !ok {
		t.Fatalf("advisory not imported: %v", store.advisories())
	}
	if adv.SourceID != "csaf:"+s.URL+metadataPath || adv.Title == "" || len(adv.CVEIDs) != 2 || adv.URL != s.URL+whiteDir+"2026/ex-2026-0001.json" {
		t.Fatalf("advisory %+v", adv)
	}
	src, err := store.GetSource(context.Background(), "csaf:"+s.URL+metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != KindCSAF || src.Cursor != released.Format(time.RFC3339Nano) || src.SyncedAt == nil || src.DocumentsTotal != 1 ||
		src.LastError != "" {
		t.Fatalf("source %+v", src)
	}
	if got := docCount(ResultImported) - before; got != 1 {
		t.Fatalf("documents imported metric +%v, want +1", got)
	}
	if age, ok := syncAge.age(KindCSAF, time.Now()); !ok || age > time.Minute {
		t.Fatalf("age of the sync %s (%v)", age, ok)
	}

	// An aggregator with the provider mirrored on its own host; a provider
	// on another host is skipped.
	s.put("/aggregator.json", []byte(fmt.Sprintf(`{"aggregator":{"category":"aggregator","name":"Test"},"aggregator_version":"2.0",
		"canonical_url":"%[1]s/aggregator.json","csaf_providers":[{"metadata":{"url":"https://other.example/provider-metadata.json"},
		"mirrors":["%[1]s%[2]s"]},{"metadata":{"url":"https://elsewhere.example/provider-metadata.json"}}]}`, s.URL, metadataPath)))
	agg := newMemStore()
	if err := newTestCSAF(t, agg, s, s.URL+"/aggregator.json").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := agg.advisories()["csaf:example.com:EX-2026-0001"]; !ok {
		t.Fatal("advisory not imported through the aggregator")
	}
}

// TestCSAFIncrementalFromChangesCSV covers AK1: the second run loads only
// the entries newer than the cursor.
func TestCSAFIncrementalFromChangesCSV(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1, t2, t3 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	s.publish(t, whiteDir+"2026/a.json", csafDoc("EX-A", "CVE-2026-0001", t1), signer)
	s.publish(t, whiteDir+"2026/b.json", csafDoc("EX-B", "CVE-2026-0002", t2), signer)
	s.changes(whiteDir, map[string]time.Time{"2026/a.json": t1, "2026/b.json": t2})
	store := newMemStore()
	c := newTestCSAF(t, store, s)
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.advisories()) != 2 {
		t.Fatalf("first run: %d advisories", len(store.advisories()))
	}
	s.publish(t, whiteDir+"2026/c.json", csafDoc("EX-C", "CVE-2026-0003", t3), signer)
	s.changes(whiteDir, map[string]time.Time{"2026/a.json": t1, "2026/b.json": t2, "2026/c.json": t3})
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.count(whiteDir+"2026/a.json") != 1 || s.count(whiteDir+"2026/b.json") != 1 || s.count(whiteDir+"2026/c.json") != 1 {
		t.Fatalf("documents fetched a %d, b %d, c %d; want each once", s.count(whiteDir+"2026/a.json"),
			s.count(whiteDir+"2026/b.json"), s.count(whiteDir+"2026/c.json"))
	}
	if len(store.advisories()) != 3 {
		t.Fatalf("second run: %d advisories, want 3", len(store.advisories()))
	}
	src, _ := store.GetSource(context.Background(), "csaf:"+s.URL+metadataPath)
	if src.Cursor != t3.Format(time.RFC3339Nano) || src.DocumentsTotal != 3 {
		t.Fatalf("source %+v", src)
	}
}

// TestCSAFConditionalGetWithETag covers AK1: an unchanged changes.csv or
// ROLIE feed answers 304 to the stored ETag; no document is fetched.
func TestCSAFConditionalGetWithETag(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.publish(t, whiteDir+"2026/a.json", csafDoc("EX-A", "CVE-2026-0001", t1), signer)
	s.changes(whiteDir, map[string]time.Time{"2026/a.json": t1})
	store := newMemStore()
	c := newTestCSAF(t, store, s)
	for range 2 {
		if err := c.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	notMod := s.notMod[whiteDir+"changes.csv"]
	s.mu.Unlock()
	if notMod != 1 || s.count(whiteDir+"2026/a.json") != 1 {
		t.Fatalf("changes.csv 304 answers %d, document fetches %d; want 1 and 1", notMod, s.count(whiteDir+"2026/a.json"))
	}

	// A ROLIE feed.
	r := newFeedServer(t)
	serveProvider(t, r, signer)
	meta := strings.Replace(string(r.files[metadataPath]), `"directory_url": "`+r.URL+`/.well-known/csaf/white/"`,
		`"rolie": {"feeds": [{"tlp_label": "WHITE", "url": "`+r.URL+`/feed/white.json"}]}`, 1)
	r.put(metadataPath, []byte(meta))
	r.publish(t, "/docs/a.json", csafDoc("EX-R", "CVE-2026-0009", t1), signer)
	r.put("/feed/white.json", []byte(`{"feed":{"id":"white","title":"white","entry":[{"id":"EX-R","updated":"2026-09-01T00:00:00Z",
		"link":[{"rel":"self","href":"`+r.URL+`/docs/a.json"}],"content":{"type":"application/json","src":"`+r.URL+`/docs/a.json"}}]}}`))
	rs := newMemStore()
	rc := newTestCSAF(t, rs, r)
	for range 2 {
		if err := rc.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	notMod = r.notMod["/feed/white.json"]
	r.mu.Unlock()
	if _, ok := rs.advisories()["csaf:example.com:EX-R"]; !ok || notMod != 1 || r.count("/docs/a.json") != 1 {
		t.Fatalf("ROLIE: advisories %v, 304 %d, fetches %d", rs.advisories(), notMod, r.count("/docs/a.json"))
	}
}

// TestCSAFRejectsInvalidSignature covers AK2: a document changed after
// signing (its hash file fits the change) and one without a signature are
// counted as invalid_signature and not written; the run goes on with the
// next document.
func TestCSAFRejectsInvalidSignature(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1, t2, t3 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	doc := csafDoc("EX-BAD", "CVE-2026-0001", t1)
	s.publish(t, whiteDir+"bad.json", doc, signer)
	tampered := bytes.Replace(doc, []byte("CVE-2026-0001"), []byte("CVE-2026-9999"), 1)
	s.put(whiteDir+"bad.json", tampered)
	sum := sha512.Sum512(tampered)
	s.put(whiteDir+"bad.json.sha512", []byte(hex.EncodeToString(sum[:])))
	s.publish(t, whiteDir+"unsigned.json", csafDoc("EX-UNSIGNED", "CVE-2026-0002", t2), signer)
	s.remove(whiteDir + "unsigned.json.asc")
	// Signed by another key than the provider's.
	s.publish(t, whiteDir+"foreign.json", csafDoc("EX-FOREIGN", "CVE-2026-0004", t2.Add(time.Hour)), newSigner(t))
	s.publish(t, whiteDir+"good.json", csafDoc("EX-GOOD", "CVE-2026-0003", t3), signer)
	s.changes(whiteDir, map[string]time.Time{"bad.json": t1, "unsigned.json": t2, "foreign.json": t2.Add(time.Hour), "good.json": t3})
	store := newMemStore()
	before := docCount(ResultInvalidSignature)
	if err := newTestCSAF(t, store, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := docCount(ResultInvalidSignature) - before; got != 3 {
		t.Fatalf("invalid_signature +%v, want +3", got)
	}
	advs := store.advisories()
	if len(advs) != 1 || advs["csaf:example.com:EX-GOOD"].ID == "" {
		t.Fatalf("advisories %v, want only EX-GOOD", advs)
	}
	src, _ := store.GetSource(context.Background(), "csaf:"+s.URL+metadataPath)
	if src.ErrorsTotal != 3 || src.DocumentsTotal != 1 || src.Cursor != t3.Format(time.RFC3339Nano) {
		t.Fatalf("source %+v", src)
	}
}

// TestCSAFRejectsInvalidHash covers AK2: a wrong .sha512 and a document
// without hash file are invalid_hash and not written; a valid .sha256
// without .sha512 is accepted.
func TestCSAFRejectsInvalidHash(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1, t2, t3 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	s.publish(t, whiteDir+"wrong.json", csafDoc("EX-WRONG", "CVE-2026-0001", t1), signer)
	s.put(whiteDir+"wrong.json.sha512", []byte(strings.Repeat("ab", 64)+"  wrong.json"))
	s.publish(t, whiteDir+"nohash.json", csafDoc("EX-NOHASH", "CVE-2026-0002", t2), signer)
	s.remove(whiteDir + "nohash.json.sha512")
	sha256Doc := csafDoc("EX-SHA256", "CVE-2026-0003", t3)
	s.publish(t, whiteDir+"sha256.json", sha256Doc, signer)
	s.remove(whiteDir + "sha256.json.sha512")
	sum := sha256.Sum256(sha256Doc)
	s.put(whiteDir+"sha256.json.sha256", []byte(strings.ToUpper(hex.EncodeToString(sum[:]))+"  sha256.json\n"))
	s.changes(whiteDir, map[string]time.Time{"wrong.json": t1, "nohash.json": t2, "sha256.json": t3})
	store := newMemStore()
	before := docCount(ResultInvalidHash)
	if err := newTestCSAF(t, store, s).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := docCount(ResultInvalidHash) - before; got != 2 {
		t.Fatalf("invalid_hash +%v, want +2", got)
	}
	if advs := store.advisories(); len(advs) != 1 || advs["csaf:example.com:EX-SHA256"].ID == "" {
		t.Fatalf("advisories %v, want only EX-SHA256", advs)
	}
}

// TestCSAFProductTreeVersionRanges covers AK3: the product tree becomes
// ranges per vendor and product with their inclusivity; vers expressions
// with several ranges, exact versions and *; interim documents are
// imported, a withdrawn revision marks the advisory.
func TestCSAFProductTreeVersionRanges(t *testing.T) {
	adv, affected, err := MapCSAF(readFixture(t, "csaf/example-advisory.json"), "csaf:test", "https://example.com/a.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if adv.ID != "csaf:example.com:EX-2026-0001" || adv.PublishedAt == nil || adv.ModifiedAt == nil || adv.Withdrawn {
		t.Fatalf("advisory %+v", adv)
	}
	if strings.Join(adv.CVEIDs, ",") != "CVE-2026-12345,CVE-2026-12346" {
		t.Fatalf("cves %v", adv.CVEIDs)
	}
	type key struct{ product, exact, start, end string }
	got := map[key]Affected{}
	for _, a := range affected {
		if a.Vendor != "Example Vendor" {
			t.Errorf("vendor %q", a.Vendor)
		}
		got[key{Normalize(a.Product), a.VersionExact, a.VersionStart, a.VersionEnd}] = a
	}
	if len(affected) != 3 {
		t.Fatalf("affected %+v, want 3 ranges", affected)
	}
	r1 := got[key{"exampleos", "", "7.0", "7.4.2"}]
	if !r1.VersionStartInclusive || r1.VersionEndInclusive || r1.VersionScheme != "generic" {
		t.Errorf(">=7.0|<7.4.2: %+v", r1)
	}
	r2, ok := got[key{"exampleos", "", "", "6.9.9"}]
	if !ok || !r2.VersionEndInclusive {
		t.Errorf("<=6.9.9: %+v", r2)
	}
	r3, ok := got[key{"example_router", "2.1", "", ""}]
	if !ok || r3.CPE != "cpe:2.3:h:example:router:2.1:*:*:*:*:*:*:*" {
		t.Errorf("router 2.1: %+v", r3)
	}

	for expr, want := range map[string][]VersRange{
		"vers:semver/>=1.0.0|<1.2.3|>=2.0.0|<=2.1.0": {{Scheme: "semver", Start: "1.0.0", StartInclusive: true, End: "1.2.3"},
			{Scheme: "semver", Start: "2.0.0", StartInclusive: true, End: "2.1.0", EndInclusive: true}},
		"vers:generic/>1.0":      {{Scheme: "generic", Start: "1.0"}},
		"vers:generic/1.5|=1.7":  {{Scheme: "generic", Exact: "1.5"}, {Scheme: "generic", Exact: "1.7"}},
		"vers:generic/*":         {{Scheme: "generic"}},
		"vers:deb/<2.0|!=1.5":    {{Scheme: "deb", End: "2.0"}},
		"vers:generic/<1.0%2Bb2": {{Scheme: "generic", End: "1.0+b2"}},
	} {
		ranges, err := ParseVers(expr)
		if err != nil || fmt.Sprint(ranges) != fmt.Sprint(want) {
			t.Errorf("ParseVers(%s) = %+v, %v; want %+v", expr, ranges, err, want)
		}
	}
	for _, bad := range []string{"<1.0", "vers:", "vers:generic", "vers:generic/", "vers:generic/>="} {
		if _, err := ParseVers(bad); err == nil {
			t.Errorf("ParseVers(%s) accepted", bad)
		}
	}

	interim := bytes.Replace(readFixture(t, "csaf/example-advisory.json"), []byte(`"status": "final"`), []byte(`"status": "interim"`), 1)
	if a, _, err := MapCSAF(interim, "csaf:test", "", ""); err != nil || a.Withdrawn {
		t.Fatalf("interim: %+v, %v", a, err)
	}
	withdrawn := bytes.Replace(readFixture(t, "csaf/example-advisory.json"), []byte(`"summary": "Initial release"`),
		[]byte(`"summary": "Initial release"},{"number":"2","date":"2026-10-05T10:00:00Z","summary":"Advisory withdrawn: duplicate"`), 1)
	if a, _, err := MapCSAF(withdrawn, "csaf:test", "", ""); err != nil || !a.Withdrawn {
		t.Fatalf("withdrawn revision: %+v, %v", a, err)
	}
	if _, _, err := MapCSAF([]byte(`{"document":{"csaf_version":"1.0","tracking":{"id":"X"}}}`), "csaf:test", "", ""); err == nil {
		t.Fatal("CSAF 1.0 accepted")
	}
}

// TestCSAFRemediationFixedVersion covers AK3: vendor_fix with the fixed
// product 7.4.2 → fixed_version of the ExampleOS ranges; the router has no
// fix.
func TestCSAFRemediationFixedVersion(t *testing.T) {
	_, affected, err := MapCSAF(readFixture(t, "csaf/example-advisory.json"), "csaf:test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range affected {
		want := ""
		if Normalize(a.Product) == "exampleos" {
			want = "7.4.2"
		}
		if a.FixedVersion != want {
			t.Errorf("%s %s-%s: fixed %q, want %q", a.Product, a.VersionStart, a.VersionEnd, a.FixedVersion, want)
		}
	}
	// The lowest fixed version compares by version, not as text: 9.4 before
	// 10.1.
	twoFixes := []byte(`{"document":{"csaf_version":"2.0","title":"t","tracking":{"id":"FX"}},
"product_tree":{"branches":[{"category":"vendor","name":"V","branches":[{"category":"product_name","name":"OS","branches":[
{"category":"product_version","name":"9.3","product":{"name":"OS 9.3","product_id":"A"}},
{"category":"product_version","name":"10.1","product":{"name":"OS 10.1","product_id":"F10"}},
{"category":"product_version","name":"9.4","product":{"name":"OS 9.4","product_id":"F9"}}]}]}]},
"vulnerabilities":[{"cve":"CVE-2026-9","product_status":{"known_affected":["A"],"fixed":["F10","F9"]},
"remediations":[{"category":"vendor_fix","product_ids":["F10","F9"]}]}]}`)
	_, affected, err = MapCSAF(twoFixes, "csaf:test", "", "https://v.example")
	if err != nil || len(affected) != 1 || affected[0].FixedVersion != "9.4" {
		t.Fatalf("fixed versions 9.4 and 10.1: %+v, %v; want 9.4", affected, err)
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"9.4", "10.1", -1}, {"10.1", "9.4", 1}, {"1.2.10", "1.2.9", 1}, {"1.0", "1.0.1", -1}, {"7.4.2", "7.4.2", 0},
		{"2.0-rc1", "2.0-rc2", -1}, {"1.0rc1", "1.0", -1}, {"01.2", "1.2", 0}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	// A fixed product without a vendor_fix remediation is no fixed version.
	noFix := bytes.Replace(readFixture(t, "csaf/example-advisory.json"), []byte(`"category": "vendor_fix"`), []byte(`"category": "workaround"`), 1)
	_, affected, _ = MapCSAF(noFix, "csaf:test", "", "")
	for _, a := range affected {
		if a.FixedVersion != "" {
			t.Errorf("workaround: fixed %q", a.FixedVersion)
		}
	}
}

// TestCSAFSeverityFromCVSS covers AK3: the highest CVSS v3 base score
// decides (8.8 → high) with its vector; without a score the aggregate
// severity (also the German texts of the BSI).
func TestCSAFSeverityFromCVSS(t *testing.T) {
	adv, _, err := MapCSAF(readFixture(t, "csaf/example-advisory.json"), "csaf:test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if adv.CVSSScore == nil || *adv.CVSSScore != 8.8 || adv.Severity != "high" || !strings.HasPrefix(adv.CVSSVector, "CVSS:3.1/AV:N/AC:L/PR:L") {
		t.Fatalf("severity %q score %v vector %q", adv.Severity, adv.CVSSScore, adv.CVSSVector)
	}
	for score, want := range map[float64]string{0: "", 0.1: "low", 3.9: "low", 4.0: "medium", 6.9: "medium", 7.0: "high", 8.9: "high", 9.0: "critical", 10: "critical"} {
		if got := SeverityFromScore(score); got != want {
			t.Errorf("SeverityFromScore(%v) = %q, want %q", score, got, want)
		}
	}
	for text, want := range map[string]string{"kritisch": "critical", "Hoch": "high", "mittel": "medium", "niedrig": "low", "Moderate": "medium", "unknown": ""} {
		doc := fmt.Sprintf(`{"document":{"csaf_version":"2.0","title":"t","aggregate_severity":{"text":%q},"tracking":{"id":"X"}},
			"vulnerabilities":[{"cve":"CVE-2026-1"}]}`, text)
		a, _, err := MapCSAF([]byte(doc), "csaf:test", "", "https://ns.example")
		if err != nil || a.Severity != want || a.CVSSScore != nil || a.ID != "csaf:ns.example:X" {
			t.Errorf("aggregate %q: %+v, %v; want %q", text, a, err, want)
		}
	}
}

// TestCSAFRefusesForeignHostAndRedirect covers AK4: a key or document on
// another host than the provider's and a redirect to another host are
// refused; nothing of them is written. A refused document is counted
// and passed over (it fails the same way in every run), the documents
// behind it are imported.
func TestCSAFRefusesForeignHostAndRedirect(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	t2, t3 := t1.Add(time.Hour), t1.Add(2*time.Hour)
	s.publish(t, whiteDir+"a.json", csafDoc("EX-A", "CVE-2026-0001", t1), signer)
	s.publish(t, whiteDir+"c.json", csafDoc("EX-C", "CVE-2026-0003", t3), signer)
	// The document redirects to the same server under another host name;
	// the next entry lies on another host.
	other := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	s.mu.Lock()
	s.redirects[whiteDir+"a.json"] = other + "/elsewhere/a.json"
	s.mu.Unlock()
	s.changes(whiteDir, map[string]time.Time{"a.json": t1, other + whiteDir + "b.json": t2, "c.json": t3})
	store := newMemStore()
	before := docCount(ResultParseError)
	if err := newTestCSAF(t, store, s).Run(context.Background()); err != nil {
		t.Fatalf("run with refused documents: %v, want the run to go on", err)
	}
	advs := store.advisories()
	if len(advs) != 1 || s.count("/elsewhere/a.json") != 0 {
		t.Fatalf("advisories %v: want only EX-C, the redirect not followed", advs)
	}
	if _, ok := advs["csaf:example.com:EX-C"]; !ok {
		t.Fatalf("the document behind the refused ones was not imported: %v", advs)
	}
	if got := docCount(ResultParseError) - before; got != 2 {
		t.Fatalf("parse_error +%v, want +2 (redirect, foreign host)", got)
	}
	src, _ := store.GetSource(context.Background(), "csaf:"+s.URL+metadataPath)
	if src == nil || src.Cursor != t3.Format(time.RFC3339Nano) || src.ErrorsTotal != 2 {
		t.Fatalf("source after the refusals %+v: want the cursor at the last document and 2 errors", src)
	}

	// A key on another host.
	meta := strings.Replace(string(s.files[metadataPath]), s.URL+keyPath, "https://keys.example/signing-key.asc", 1)
	s.put(metadataPath, []byte(meta))
	if err := newTestCSAF(t, newMemStore(), s).Run(context.Background()); !errors.Is(err, ErrForeignHost) {
		t.Fatalf("key on another host: %v, want ErrForeignHost", err)
	}

	// Plain HTTP is refused by the client as well.
	c := restrictHosts(s.Client(), []string{"127.0.0.1"})
	if _, err := c.Get(strings.Replace(s.URL, "https://", "http://", 1) + metadataPath); !errors.Is(err, ErrForeignHost) {
		t.Fatalf("http: %v, want ErrForeignHost", err)
	}
}

// TestCSAFAirGappedRunsOnlyWithExplicitProviders covers AK4 and E-235:
// air-gapped without an explicit list the import is off; with an own
// mirror it runs; without the air-gapped profile the default is the BSI
// provider; a non-https provider is refused.
func TestCSAFAirGappedRunsOnlyWithExplicitProviders(t *testing.T) {
	store := newMemStore()
	if _, err := NewCSAF(store, &CSAFConfig{AirGapped: true}); !errors.Is(err, ErrCSAFDisabled) {
		t.Fatalf("air-gapped without providers: %v, want ErrCSAFDisabled", err)
	}
	if _, err := NewCSAF(store, &CSAFConfig{AirGapped: true, Providers: []string{DefaultCSAFProvider}}); !errors.Is(err, ErrCSAFDisabled) {
		t.Fatalf("air-gapped with the default list: %v, want ErrCSAFDisabled", err)
	}
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.publish(t, whiteDir+"a.json", csafDoc("EX-MIRROR", "CVE-2026-0001", t1), signer)
	s.changes(whiteDir, map[string]time.Time{"a.json": t1})
	mirror, err := NewCSAF(store, &CSAFConfig{AirGapped: true, Explicit: true, Providers: []string{s.URL + metadataPath}, Client: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if rerr := mirror.Run(context.Background()); rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := store.advisories()["csaf:example.com:EX-MIRROR"]; !ok {
		t.Fatal("the mirror was not imported")
	}
	def, err := NewCSAF(store, &CSAFConfig{})
	if err != nil || len(def.providers) != 1 || def.providers[0] != DefaultCSAFProvider {
		t.Fatalf("default providers %v, %v", def, err)
	}
	if sched := def.Schedule(); sched.Kind != CSAFKind || sched.Spec != "@every 6h0m0s" {
		t.Fatalf("schedule %+v", sched)
	}
	if _, err := NewCSAF(store, &CSAFConfig{Explicit: true, Providers: []string{"http://csaf.example/provider-metadata.json"}}); !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("http provider: %v, want ErrNotHTTPS", err)
	}
	var meta json.RawMessage
	if err := json.Unmarshal(readFixture(t, "csaf/provider-metadata.json"), &meta); err != nil {
		t.Fatalf("provider-metadata.json is no JSON: %v", err)
	}
}

// TestCSAFBreakKeepsListETagAndSameSecondEntries: a transport error on a
// document breaks the run; the ETag of the list is not stored (else the
// unchanged list answers 304 and the entries behind the break are never
// seen), and the cursor does not pass a time whose entries are not all
// done (changes.csv gives several documents the same second).
func TestCSAFBreakKeepsListETagAndSameSecondEntries(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.publish(t, whiteDir+"a.json", csafDoc("EX-A", "CVE-2026-0001", t1), signer)
	s.publish(t, whiteDir+"b.json", csafDoc("EX-B", "CVE-2026-0002", t1), signer)
	s.publish(t, whiteDir+"c.json", csafDoc("EX-C", "CVE-2026-0003", t1), signer)
	s.changes(whiteDir, map[string]time.Time{"a.json": t1, "b.json": t1, "c.json": t1})
	s.mu.Lock()
	s.status[whiteDir+"b.json"] = http.StatusServiceUnavailable
	s.mu.Unlock()
	store := newMemStore()
	c := newTestCSAF(t, store, s)
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("run with a 503 document: no error")
	}
	src, _ := store.GetSource(context.Background(), "csaf:"+s.URL+metadataPath)
	if src == nil || src.Cursor != "" || src.ETag != "" {
		t.Fatalf("source after the break %+v: want neither cursor nor ETag", src)
	}
	s.mu.Lock()
	delete(s.status, whiteDir+"b.json")
	s.mu.Unlock()
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(store.advisories()); n != 3 {
		t.Fatalf("second run: %d advisories, want 3 (the list unchanged, the same second)", n)
	}
	s.mu.Lock()
	notMod := s.notMod[whiteDir+"changes.csv"]
	s.mu.Unlock()
	if notMod != 0 {
		t.Fatalf("changes.csv answered 304 %d times, want 0", notMod)
	}
}

// TestCSAFSkipsUnavailableDistribution: a feed that answers 4xx (TLP:GREEN
// feeds without an account, the BSI answers 404) is left out; the public
// feed of the provider is imported.
func TestCSAFSkipsUnavailableDistribution(t *testing.T) {
	s := newFeedServer(t)
	signer := newSigner(t)
	serveProvider(t, s, signer)
	meta := strings.Replace(string(s.files[metadataPath]), `"directory_url": "`+s.URL+`/.well-known/csaf/white/"`,
		`"rolie": {"feeds": [{"tlp_label": "GREEN", "url": "`+s.URL+`/feed/green.json"},
		{"tlp_label": "WHITE", "url": "`+s.URL+`/feed/white.json"}]}`, 1)
	s.put(metadataPath, []byte(meta))
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.publish(t, "/docs/a.json", csafDoc("EX-W", "CVE-2026-0010", t1), signer)
	s.put("/feed/white.json", []byte(`{"feed":{"id":"white","title":"white","entry":[{"id":"EX-W","updated":"2026-09-01T00:00:00.000+00:00",
		"link":[{"rel":"self","href":"`+s.URL+`/docs/a.json"}]}]}}`))
	store := newMemStore()
	if err := newTestCSAF(t, store, s).Run(context.Background()); err != nil {
		t.Fatalf("run with a missing feed: %v", err)
	}
	if _, ok := store.advisories()["csaf:example.com:EX-W"]; !ok {
		t.Fatalf("advisory of the public feed not imported: %v", store.advisories())
	}
}

// TestCSAFRefusedValuesDoNotStopTheRun: a document with values the catalog
// cannot hold (a negative CVSS score is left out of the mapping; a
// database refusal of the values counts as parse_error) is passed over.
func TestCSAFRefusedValuesDoNotStopTheRun(t *testing.T) {
	doc := []byte(`{"document":{"csaf_version":"2.0","title":"t","tracking":{"id":"NEG"}},
"vulnerabilities":[{"cve":"CVE-2026-11","scores":[{"cvss_v3":{"baseScore":-1,"vectorString":"x"}}]}]}`)
	adv, _, err := MapCSAF(doc, "csaf:test", "", "https://v.example")
	if err != nil || adv.CVSSScore != nil || adv.CVSSVector != "" {
		t.Fatalf("negative score: %+v, %v; want no score", adv, err)
	}
	if !isDataError(&pgconn.PgError{Code: "23514"}) || !isDataError(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "22001"})) ||
		isDataError(&pgconn.PgError{Code: "40001"}) || isDataError(errors.New("connection reset")) {
		t.Fatal("isDataError classifies wrongly")
	}
}

// TestVulnfeedFetchErrorsAreRedacted: a transport error names the URL
// without its query (a token there must not reach logs or
// vuln_source.last_error).
func TestVulnfeedFetchErrorsAreRedacted(t *testing.T) {
	s := newFeedServer(t)
	c := restrictHosts(s.Client(), []string{"127.0.0.1"})
	other := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	for _, u := range []string{other + "/feed.json?token=secret-1", s.URL + "/r?token=secret-1"} {
		s.mu.Lock()
		s.redirects["/r"] = other + "/x?token=secret-2"
		s.mu.Unlock()
		_, err := fetch(context.Background(), c, u, "", 1<<10, nil)
		if err == nil || !errors.Is(err, ErrForeignHost) {
			t.Fatalf("%s: %v, want ErrForeignHost", u, err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
			t.Fatalf("error carries the query: %v", err)
		}
	}
}
