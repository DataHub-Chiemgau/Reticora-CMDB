package vulnfeed

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// CSAFCovered implements NVDStore on the memory catalog.
func (m *memStore) CSAFCovered(_ context.Context, cves []string) (map[string]map[[2]string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[[2]string]bool{}
	for id, adv := range m.advisory {
		if !strings.HasPrefix(adv.SourceID, KindCSAF+":") {
			continue
		}
		for _, cve := range adv.CVEIDs {
			if !slices.Contains(cves, cve) {
				continue
			}
			for _, a := range m.affected[id] {
				if out[cve] == nil {
					out[cve] = map[[2]string]bool{}
				}
				out[cve][[2]string{a.Vendor, a.Product}] = true
			}
		}
	}
	return out, nil
}

// RemoveCoveredNVDRanges implements NVDStore on the memory catalog.
func (m *memStore) RemoveCoveredNVDRanges(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	covered := map[string]map[[2]string]bool{}
	for id, adv := range m.advisory {
		if strings.HasPrefix(adv.SourceID, KindCSAF+":") {
			for _, cve := range adv.CVEIDs {
				for _, a := range m.affected[id] {
					if covered[cve] == nil {
						covered[cve] = map[[2]string]bool{}
					}
					covered[cve][[2]string{a.Vendor, a.Product}] = true
				}
			}
		}
	}
	var n int64
	for id, adv := range m.advisory {
		if adv.SourceID != KindNVD {
			continue
		}
		kept := m.affected[id][:0]
		for _, a := range m.affected[id] {
			hit := false
			for _, cve := range adv.CVEIDs {
				hit = hit || covered[cve][[2]string{a.Vendor, a.Product}]
			}
			if hit {
				n++
			} else {
				kept = append(kept, a)
			}
		}
		m.affected[id] = kept
	}
	return n, nil
}

// SetKEV implements NVDStore on the memory catalog.
func (m *memStore) SetKEV(_ context.Context, cves []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, adv := range m.advisory {
		kev := false
		for _, c := range adv.CVEIDs {
			kev = kev || slices.Contains(cves, c)
		}
		if kev != adv.KEV {
			adv.KEV = kev
			m.advisory[id] = adv
			n++
		}
	}
	return n, nil
}

func (m *memStore) ranges(id string) []Affected {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Affected(nil), m.affected[id]...)
}

// fakeClock is the clock of the NVD tests: Sleep advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// nvdRequest is one request the fake NVD received.
type nvdRequest struct {
	start, end time.Time
	index      int
	perPage    string
	apiKey     string
}

// fakeNVD serves the CVE API and the KEV catalog.
type fakeNVD struct {
	*httptest.Server
	mu       sync.Mutex
	requests []nvdRequest
	answer   func(r nvdRequest, n int) (int, []byte)
	kev      []byte
	kevHits  int
}

func newFakeNVD(t *testing.T) *fakeNVD {
	t.Helper()
	f := &fakeNVD{kev: readFixture(t, "kev/known_exploited_vulnerabilities.json")}
	page1, page2 := readFixture(t, "nvd/cves-page-1.json"), readFixture(t, "nvd/cves-page-2.json")
	f.answer = func(r nvdRequest, _ int) (int, []byte) {
		switch r.index {
		case 0:
			return http.StatusOK, page1
		case 2:
			return http.StatusOK, page2
		}
		return http.StatusOK, []byte(`{"totalResults":0,"vulnerabilities":[]}`)
	}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/kev.json" {
			f.kevHits++
			if r.Header.Get("If-None-Match") == `"kev-1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"kev-1"`)
			_, _ = w.Write(f.kev)
			return
		}
		q := r.URL.Query()
		start, _ := time.Parse(nvdTimeFormat, q.Get("lastModStartDate"))
		end, _ := time.Parse(nvdTimeFormat, q.Get("lastModEndDate"))
		index, _ := strconv.Atoi(q.Get("startIndex"))
		req := nvdRequest{start: start, end: end, index: index, perPage: q.Get("resultsPerPage"), apiKey: r.Header.Get("apiKey")}
		f.requests = append(f.requests, req)
		status, body := f.answer(req, len(f.requests))
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeNVD) received() []nvdRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nvdRequest(nil), f.requests...)
}

var nvdNow = time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)

// newTestNVD is the import against f with the cursor at cursor.
func newTestNVD(t *testing.T, store *memStore, f *fakeNVD, clock *fakeClock, key string, cursor time.Time) *NVD {
	t.Helper()
	if !cursor.IsZero() {
		store.sources[KindNVD] = Source{ID: KindNVD, Kind: KindNVD, Cursor: cursor.Format(time.RFC3339Nano)}
	}
	n, err := NewNVD(store, &NVDConfig{APIKey: key, URL: f.URL + "/rest/json/cves/2.0", KEVURL: f.URL + "/kev.json",
		Client: f.Client(), Now: clock.Now, Sleep: clock.Sleep})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestNVDPagingFollowsTotalResults covers AK1 and AK3: the pages follow
// totalResults with startIndex and resultsPerPage 2000; every CVE is
// imported as nvd:<CVE-ID>; the cursor moves to the end of the window.
func TestNVDPagingFollowsTotalResults(t *testing.T) {
	f := newFakeNVD(t)
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	n := newTestNVD(t, store, f, clock, "", nvdNow.Add(-10*24*time.Hour))
	if err := n.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f.received()
	if len(reqs) != 2 || reqs[0].index != 0 || reqs[1].index != 2 || reqs[0].perPage != "2000" {
		t.Fatalf("requests %+v, want startIndex 0 and 2 with resultsPerPage 2000", reqs)
	}
	advs := store.advisories()
	for _, id := range []string{"nvd:CVE-2026-1001", "nvd:CVE-2026-1002", "nvd:CVE-2026-1003"} {
		if a, ok := advs[id]; !ok || a.SourceID != KindNVD || a.URL != "https://nvd.nist.gov/vuln/detail/"+strings.TrimPrefix(id, "nvd:") {
			t.Errorf("%s: %+v", id, a)
		}
	}
	src, _ := store.GetSource(context.Background(), KindNVD)
	if src.Cursor != nvdNow.Format(time.RFC3339Nano) || src.DocumentsTotal != 3 || src.SyncedAt == nil {
		t.Fatalf("source %+v", src)
	}
}

// TestNVDWindowAtMost120Days covers AK1: an older cursor is read in
// consecutive windows of at most 120 days up to now.
func TestNVDWindowAtMost120Days(t *testing.T) {
	f := newFakeNVD(t)
	f.answer = func(nvdRequest, int) (int, []byte) {
		return http.StatusOK, []byte(`{"totalResults":0,"vulnerabilities":[]}`)
	}
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	cursor := nvdNow.Add(-300 * 24 * time.Hour)
	if err := newTestNVD(t, store, f, clock, "", cursor).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f.received()
	if len(reqs) != 3 {
		t.Fatalf("%d windows, want 3", len(reqs))
	}
	prev := cursor
	for _, r := range reqs {
		if !r.start.Equal(prev) || r.end.Sub(r.start) > NVDMaxWindow || !r.end.After(r.start) {
			t.Fatalf("window %s – %s after %s", r.start, r.end, prev)
		}
		prev = r.end
	}
	if !prev.Equal(nvdNow) {
		t.Fatalf("last window ends %s, want %s", prev, nvdNow)
	}
	// Without a cursor the first window starts at the epoch.
	first := newFakeNVD(t)
	first.answer = f.answer
	if err := newTestNVD(t, newMemStore(), first, &fakeClock{now: nvdEpoch.Add(200 * 24 * time.Hour)}, "", time.Time{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := first.received(); len(r) != 2 || !r[0].start.Equal(nvdEpoch) {
		t.Fatalf("first sync windows %+v", r)
	}
}

// emptyAnswer answers every page without CVEs.
func emptyAnswer(nvdRequest, int) (int, []byte) {
	return http.StatusOK, []byte(`{"totalResults":0,"vulnerabilities":[]}`)
}

// TestNVDRateLimitWithoutKey covers AK1: without API key the sixth request
// within 30 s waits until the first leaves the window.
func TestNVDRateLimitWithoutKey(t *testing.T) {
	f := newFakeNVD(t)
	f.answer = emptyAnswer
	clock := &fakeClock{now: nvdNow}
	cursor := nvdNow.Add(-7 * NVDMaxWindow).Add(time.Hour) // 7 windows
	if err := newTestNVD(t, newMemStore(), f, clock, "", cursor).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(f.received()); got != 7 {
		t.Fatalf("%d requests, want 7", got)
	}
	if s := clock.slept(); len(s) != 1 || s[0] != 30*time.Second {
		t.Fatalf("waits %v, want one of 30 s before the sixth request", s)
	}
	for _, r := range f.received() {
		if r.apiKey != "" {
			t.Fatal("an API key was sent without one configured")
		}
	}
}

// TestNVDRateLimitWithKey covers AK1 and AK2: with API key 50 requests
// within 30 s go at once, the 51st waits; every request carries the key
// in the header apiKey.
func TestNVDRateLimitWithKey(t *testing.T) {
	f := newFakeNVD(t)
	f.answer = emptyAnswer
	clock := &fakeClock{now: nvdNow}
	cursor := nvdNow.Add(-51 * NVDMaxWindow).Add(time.Hour) // 51 windows
	if err := newTestNVD(t, newMemStore(), f, clock, "nvd-test-key", cursor).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f.received()
	if len(reqs) != 51 {
		t.Fatalf("%d requests, want 51", len(reqs))
	}
	if s := clock.slept(); len(s) != 1 || s[0] != 30*time.Second {
		t.Fatalf("waits %v, want one before the 51st request", s)
	}
	for _, r := range reqs {
		if r.apiKey != "nvd-test-key" {
			t.Fatalf("request without the key header: %+v", r)
		}
	}
}

// TestNVDApiKeyNotLogged covers AK2: the key appears in no log line, no
// error and no stored state, also when the NVD refuses it.
func TestNVDApiKeyNotLogged(t *testing.T) {
	const key = "secret-nvd-key-4711"
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	f := newFakeNVD(t)
	f.answer = func(nvdRequest, int) (int, []byte) {
		return http.StatusForbidden, []byte(`{"message":"Invalid apiKey ` + key + `"}`)
	}
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	n := newTestNVD(t, store, f, clock, key, nvdNow.Add(-24*time.Hour))
	err := n.RunWithKEV(context.Background())
	if err == nil {
		t.Fatal("a refused key is no error")
	}
	src, _ := store.GetSource(context.Background(), KindNVD)
	for name, text := range map[string]string{"log": logs.String(), "error": err.Error(), "last_error": src.LastError} {
		if strings.Contains(text, key) {
			t.Errorf("the API key is in the %s: %s", name, text)
		}
	}
	if !strings.Contains(src.LastError, "403") {
		t.Errorf("last_error %q does not name the refusal", src.LastError)
	}
	if r := f.received(); len(r) == 0 || r[0].apiKey != key {
		t.Fatal("the key was not sent in the header")
	}
}

// TestNVDCPEMatchRanges covers AK3: vulnerable cpeMatch entries with
// their ranges (including and excluding bounds), an exact version of the
// criteria, escaped characters; non-vulnerable platform entries are left
// out; CVSS v3.1 (primary metric) before v3.0.
func TestNVDCPEMatchRanges(t *testing.T) {
	f := newFakeNVD(t)
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	if err := newTestNVD(t, store, f, clock, "", nvdNow.Add(-time.Hour)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ros := store.ranges("nvd:CVE-2026-1001")
	if len(ros) != 2 {
		t.Fatalf("switchos ranges %+v", ros)
	}
	for _, a := range ros {
		if a.Vendor != "mikrotik" || a.Product != "switchos" || !strings.HasPrefix(a.CPE, "cpe:2.3:o:mikrotik:switchos") {
			t.Errorf("range %+v", a)
		}
	}
	if r := ros[0]; r.VersionStart != "6.0" || !r.VersionStartInclusive || r.VersionEnd != "6.49.7" || r.VersionEndInclusive {
		t.Errorf("first range %+v, want [6.0, 6.49.7)", r)
	}
	if r := ros[1]; r.VersionStart != "7.0" || r.VersionStartInclusive || r.VersionEnd != "7.10.1" || !r.VersionEndInclusive {
		t.Errorf("second range %+v, want (7.0, 7.10.1]", r)
	}
	adv := store.advisories()["nvd:CVE-2026-1001"]
	if adv.CVSSScore == nil || *adv.CVSSScore != 9.8 || adv.Severity != "critical" || !strings.HasPrefix(adv.CVSSVector, "CVSS:3.1/AV:N/AC:L") ||
		!strings.HasPrefix(adv.Title, "A buffer overflow") || adv.PublishedAt == nil || adv.ModifiedAt == nil {
		t.Errorf("advisory %+v", adv)
	}
	zy := store.ranges("nvd:CVE-2026-1002")
	if len(zy) != 1 || zy[0].Product != "usg_flex_100_firmware" || zy[0].VersionExact != "5.36" {
		t.Errorf("zyxel ranges %+v, want the exact firmware 5.36 without the hardware", zy)
	}
	if a := store.advisories()["nvd:CVE-2026-1002"]; a.CVSSScore == nil || *a.CVSSScore != 7.5 || a.Severity != "high" {
		t.Errorf("v3.0 metric: %+v", a)
	}
	avm := store.ranges("nvd:CVE-2026-1003")
	if len(avm) != 1 || avm[0].Product != "fritz!os" || avm[0].VersionEnd != "7.57" || !avm[0].VersionEndInclusive {
		t.Errorf("avm ranges %+v", avm)
	}
	if a := store.advisories()["nvd:CVE-2026-1003"]; a.CVSSScore != nil || a.Severity != "" {
		t.Errorf("CVE without metrics: %+v", a)
	}
	if adv, _ := MapNVD(&nvdCVE{ID: "CVE-2026-9", VulnStatus: "Rejected"}); adv == nil || !adv.Withdrawn {
		t.Errorf("rejected CVE: %+v", adv)
	}
	if adv, _ := MapNVD(&nvdCVE{ID: "GHSA-1"}); adv != nil {
		t.Error("an id without CVE- was mapped")
	}
}

// TestNVDSkipsRangesCoveredByCSAF covers AK4: for a (vendor, product)
// a CSAF advisory of the same CVE covers, no NVD range is written.
func TestNVDSkipsRangesCoveredByCSAF(t *testing.T) {
	f := newFakeNVD(t)
	store := newMemStore()
	seedSources(t, store, "csaf:https://mikrotik.example/pm.json")
	if err := store.ReplaceAdvisory(context.Background(), &Advisory{ID: "csaf:mikrotik.com:MT-1", SourceID: "csaf:https://mikrotik.example/pm.json",
		CVEIDs: []string{"CVE-2026-1001"}}, []Affected{{Vendor: "MikroTik", Product: "SwitchOS", VersionEnd: "6.49.8"}}); err != nil {
		t.Fatal(err)
	}
	if err := newTestNVD(t, store, f, &fakeClock{now: nvdNow}, "", nvdNow.Add(-time.Hour)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := store.ranges("nvd:CVE-2026-1001"); len(r) != 0 {
		t.Fatalf("NVD ranges of a CVE covered by CSAF: %+v", r)
	}
	if _, ok := store.advisories()["nvd:CVE-2026-1001"]; !ok {
		t.Fatal("the NVD advisory itself is missing")
	}
	if r := store.ranges("nvd:CVE-2026-1002"); len(r) != 1 {
		t.Fatalf("an uncovered CVE lost its ranges: %+v", r)
	}
}

// TestNVDRemovesRangesLaterCoveredByCSAF covers AK4: a CSAF advisory that
// arrives after the NVD import removes the covered NVD ranges in the next
// run, also when the NVD reports no change of the CVE.
func TestNVDRemovesRangesLaterCoveredByCSAF(t *testing.T) {
	f := newFakeNVD(t)
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	n := newTestNVD(t, store, f, clock, "", nvdNow.Add(-time.Hour))
	if err := n.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.ranges("nvd:CVE-2026-1001")) != 2 {
		t.Fatal("NVD ranges missing before CSAF")
	}
	seedSources(t, store, "csaf:https://mikrotik.example/pm.json")
	if err := store.ReplaceAdvisory(context.Background(), &Advisory{ID: "csaf:mikrotik.com:MT-1", SourceID: "csaf:https://mikrotik.example/pm.json",
		CVEIDs: []string{"CVE-2026-1001"}}, []Affected{{Vendor: "mikrotik", Product: "switchos", VersionEnd: "6.49.8"}}); err != nil {
		t.Fatal(err)
	}
	f.answer = emptyAnswer
	clock.now = clock.now.Add(2 * time.Hour)
	if err := n.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := store.ranges("nvd:CVE-2026-1001"); len(r) != 0 {
		t.Fatalf("NVD ranges left after CSAF: %+v", r)
	}
	if r := store.ranges("nvd:CVE-2026-1003"); len(r) != 1 {
		t.Fatalf("ranges of another CVE removed: %+v", r)
	}
}

// TestNVDBackoffOn503 covers AK1: 503 and 403 back off and are repeated;
// when the NVD keeps refusing, the run ends with an error, the cursor
// stays at the end of the last complete window and the next run resumes
// there.
func TestNVDBackoffOn503(t *testing.T) {
	f := newFakeNVD(t)
	f.answer = func(_ nvdRequest, n int) (int, []byte) {
		if n <= 2 {
			return http.StatusServiceUnavailable, nil
		}
		return http.StatusOK, []byte(`{"totalResults":0,"vulnerabilities":[]}`)
	}
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	if err := newTestNVD(t, store, f, clock, "", nvdNow.Add(-time.Hour)).Run(context.Background()); err != nil {
		t.Fatalf("two 503 then 200: %v", err)
	}
	if s := clock.slept(); len(s) != 2 || s[0] != nvdBackoff || s[1] != 2*nvdBackoff {
		t.Fatalf("waits %v, want %s and %s", s, nvdBackoff, 2*nvdBackoff)
	}

	// Two windows; the second keeps answering 503.
	g := newFakeNVD(t)
	cursor := nvdNow.Add(-NVDMaxWindow - 24*time.Hour)
	g.answer = func(r nvdRequest, _ int) (int, []byte) {
		if r.start.Equal(cursor) {
			return http.StatusOK, []byte(`{"totalResults":0,"vulnerabilities":[]}`)
		}
		return http.StatusServiceUnavailable, nil
	}
	store = newMemStore()
	n := newTestNVD(t, store, g, &fakeClock{now: nvdNow}, "", cursor)
	err := n.Run(context.Background())
	var status *StatusError
	if !errors.As(err, &status) && !strings.Contains(err.Error(), "503") {
		t.Fatalf("persistent 503: %v", err)
	}
	src, _ := store.GetSource(context.Background(), KindNVD)
	if want := cursor.Add(NVDMaxWindow).Format(time.RFC3339Nano); src.Cursor != want || src.LastError == "" || src.SyncedAt != nil {
		t.Fatalf("source %+v, want the cursor %s after the complete window", src, want)
	}
	g.mu.Lock()
	g.answer = emptyAnswer
	g.requests = nil
	g.mu.Unlock()
	if err := n.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := g.received(); len(r) != 1 || !r[0].start.Equal(cursor.Add(NVDMaxWindow)) {
		t.Fatalf("resumed windows %+v", r)
	}
}

// TestNVDDisabledWhenAirGapped covers AK6: no NVD import air-gapped; a
// non-https URL is refused; the job is vulnfeed.nvd every 2 h by default.
func TestNVDDisabledWhenAirGapped(t *testing.T) {
	if _, err := NewNVD(newMemStore(), &NVDConfig{AirGapped: true}); !errors.Is(err, ErrNVDDisabled) {
		t.Fatalf("air-gapped: %v", err)
	}
	if _, err := NewNVD(newMemStore(), &NVDConfig{KEVURL: "http://kev.example/kev.json"}); !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("http KEV URL: %v", err)
	}
	n, err := NewNVD(newMemStore(), &NVDConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if s := n.Schedule(); s.Kind != NVDKind || s.Spec != "@every 2h0m0s" {
		t.Fatalf("schedule %+v", s)
	}
	if n.limit.max != nvdRateWithoutKey {
		t.Fatalf("rate %d", n.limit.max)
	}
}

// seedSources stores the sources of advisories a test writes directly
// (vuln_advisory.source_id references vuln_source).
func seedSources(t *testing.T, store Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		kind, _, _ := strings.Cut(id, ":")
		if err := store.UpdateSource(context.Background(), &Source{ID: id, Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNVDCursorStoredAfterEachWindow covers AK1: the cursor is stored
// after every complete window, so a run cut by the timeout of the job (the
// context ends) goes on after the last complete window; the error state
// is stored also with the context ended.
func TestNVDCursorStoredAfterEachWindow(t *testing.T) {
	f := newFakeNVD(t)
	cursor := nvdNow.Add(-2*NVDMaxWindow - 24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.answer = func(r nvdRequest, _ int) (int, []byte) {
		if r.start.Equal(cursor) {
			return http.StatusOK, []byte(`{"totalResults":0,"vulnerabilities":[]}`)
		}
		cancel() // the job times out during the second window
		return http.StatusServiceUnavailable, nil
	}
	store := newMemStore()
	n := newTestNVD(t, store, f, &fakeClock{now: nvdNow}, "", cursor)
	if err := n.Run(ctx); err == nil {
		t.Fatal("cancelled run: no error")
	}
	src, _ := store.GetSource(context.Background(), KindNVD)
	if want := cursor.Add(NVDMaxWindow).Format(time.RFC3339Nano); src.Cursor != want || src.LastError == "" {
		t.Fatalf("source %+v, want the cursor %s after the first window and the error", src, want)
	}
}
