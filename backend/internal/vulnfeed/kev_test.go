package vulnfeed

import (
	"context"
	"testing"
	"time"
)

// TestKEVSetsAndClearsFlag covers AK5: kev is set for every advisory with
// a KEV CVE (NVD and CSAF) and cleared for the others; the state is
// vuln_source('kev').
func TestKEVSetsAndClearsFlag(t *testing.T) {
	f := newFakeNVD(t)
	store := newMemStore()
	ctx := context.Background()
	seedSources(t, store, KindNVD, "csaf:https://example.com/pm.json")
	for _, a := range []Advisory{
		{ID: "nvd:CVE-2026-1001", SourceID: KindNVD, CVEIDs: []string{"CVE-2026-1001"}},
		{ID: "csaf:example.com:EX-2026-0001", SourceID: "csaf:https://example.com/pm.json", CVEIDs: []string{"CVE-2026-12346", "CVE-2026-12345"}},
		{ID: "nvd:CVE-2026-9999", SourceID: KindNVD, CVEIDs: []string{"CVE-2026-9999"}, KEV: true},
	} {
		if err := store.ReplaceAdvisory(ctx, &a, nil); err != nil {
			t.Fatal(err)
		}
	}
	n := newTestNVD(t, store, f, &fakeClock{now: nvdNow}, "", time.Time{})
	if err := n.kev.RunIfDue(ctx); err != nil {
		t.Fatal(err)
	}
	advs := store.advisories()
	if !advs["nvd:CVE-2026-1001"].KEV || !advs["csaf:example.com:EX-2026-0001"].KEV || advs["nvd:CVE-2026-9999"].KEV {
		t.Fatalf("flags %v / %v / %v; want true, true, false", advs["nvd:CVE-2026-1001"].KEV,
			advs["csaf:example.com:EX-2026-0001"].KEV, advs["nvd:CVE-2026-9999"].KEV)
	}
	src, err := store.GetSource(ctx, KindKEV)
	if err != nil || src.Kind != KindKEV || src.Cursor != "2026.11.12" || src.SyncedAt == nil || src.DocumentsTotal != 1 {
		t.Fatalf("source %+v, %v", src, err)
	}
	// A broken catalog keeps the flags (the other server knows no ETag).
	kevSrc := store.sources[KindKEV]
	kevSrc.ETag = ""
	store.sources[KindKEV] = kevSrc
	n2 := newTestNVD(t, store, newFakeNVDWith(t, []byte(`{"vulnerabilities":[]}`)), &fakeClock{now: nvdNow.Add(48 * time.Hour)}, "", time.Time{})
	if err := n2.kev.RunIfDue(ctx); err == nil {
		t.Fatal("an empty catalog was applied")
	}
	if !store.advisories()["nvd:CVE-2026-1001"].KEV {
		t.Fatal("an empty catalog cleared the flags")
	}
}

// newFakeNVDWith is a fake NVD whose KEV catalog is kev.
func newFakeNVDWith(t *testing.T, kev []byte) *fakeNVD {
	t.Helper()
	f := newFakeNVD(t)
	f.kev = kev
	return f
}

// TestKEVRunsOncePerDay covers AK5 and AK6: the job fetches the catalog
// once per 24 h, a later run within the day does not; after 24 h it
// fetches it again (without If-None-Match) and applies it.
func TestKEVRunsOncePerDay(t *testing.T) {
	f := newFakeNVD(t)
	f.answer = emptyAnswer
	clock := &fakeClock{now: nvdNow}
	store := newMemStore()
	n := newTestNVD(t, store, f, clock, "", nvdNow.Add(-time.Hour))
	for range 3 {
		if err := n.RunWithKEV(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(2 * time.Hour)
	}
	f.mu.Lock()
	hits := f.kevHits
	f.mu.Unlock()
	if hits != 1 {
		t.Fatalf("KEV fetched %d times within 6 h, want 1", hits)
	}
	clock.now = nvdNow.Add(KEVInterval)
	if err := n.RunWithKEV(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	hits = f.kevHits
	f.mu.Unlock()
	if hits != 2 {
		t.Fatalf("KEV fetched %d times after 24 h, want 2", hits)
	}
	src, _ := store.GetSource(context.Background(), KindKEV)
	if src.ETag != `"kev-1"` || src.SyncedAt == nil || !src.SyncedAt.Equal(nvdNow.Add(KEVInterval)) {
		t.Fatalf("source %+v", src)
	}
}

// TestKEVFlagsAdvisoryImportedAfterTheLastSync covers AK5: an advisory of
// a KEV CVE imported after the last sync is flagged at the next due sync,
// also when the catalog has not changed since.
func TestKEVFlagsAdvisoryImportedAfterTheLastSync(t *testing.T) {
	f := newFakeNVD(t)
	store := newMemStore()
	ctx := context.Background()
	clock := &fakeClock{now: nvdNow}
	n := newTestNVD(t, store, f, clock, "", time.Time{})
	if err := n.kev.RunIfDue(ctx); err != nil {
		t.Fatal(err)
	}
	seedSources(t, store, "csaf:https://late.example/pm.json")
	late := Advisory{ID: "csaf:late.example:L-1", SourceID: "csaf:https://late.example/pm.json", CVEIDs: []string{"CVE-2026-12345"}}
	if err := store.ReplaceAdvisory(ctx, &late, nil); err != nil {
		t.Fatal(err)
	}
	if store.advisories()[late.ID].KEV {
		t.Fatal("precondition: the late advisory is flagged at once")
	}
	clock.now = nvdNow.Add(KEVInterval)
	if err := n.kev.RunIfDue(ctx); err != nil {
		t.Fatal(err)
	}
	if !store.advisories()[late.ID].KEV {
		t.Fatal("the advisory imported after the last sync is not flagged at the next sync")
	}
}
