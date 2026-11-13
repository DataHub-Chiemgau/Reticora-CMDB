package vulnfeed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// The offline bundle (SEC-02, WP-1120): export and import round-trip with a
// test key; a wrong signature, a changed or missing file, an older bundle
// and an oversized one are refused, and nothing is written then.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestBundle' ./internal/vulnfeed/

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// sampleCatalog is a catalog with a CSAF and an NVD advisory.
func sampleCatalog(t *testing.T) *MemoryStore {
	t.Helper()
	m := NewMemoryStore()
	ctx := context.Background()
	synced := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	score := 9.8
	for _, s := range []*Source{{ID: "csaf:https://csaf.example/provider-metadata.json", Kind: KindCSAF, SyncedAt: &synced},
		{ID: KindNVD, Kind: KindNVD, SyncedAt: &synced}} {
		if err := m.UpdateSource(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.ReplaceAdvisory(ctx, &Advisory{ID: "csaf:cisco-sa-1", SourceID: "csaf:https://csaf.example/provider-metadata.json",
		CVEIDs: []string{"CVE-2026-0001"}, Title: "IOS XE", Severity: "critical", CVSSScore: &score},
		[]Affected{{Vendor: "Cisco", Product: "IOS XE", VersionScheme: "cisco_ios_xe", VersionEnd: "17.9.5", FixedVersion: "17.9.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.ReplaceAdvisory(ctx, &Advisory{ID: "nvd:CVE-2026-0002", SourceID: KindNVD, CVEIDs: []string{"CVE-2026-0002"}, Severity: "high"},
		[]Affected{{Vendor: "mikrotik", Product: "router_os", VersionScheme: "generic", VersionExact: "7.1"}}); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeSample(t *testing.T, priv ed25519.PrivateKey, at time.Time) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := WriteBundle(&buf, sampleCatalog(t), priv, at); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// importBytes opens and imports a bundle into store.
func importBytes(t *testing.T, store Store, pub ed25519.PublicKey, raw []byte) (*BundleImport, error) {
	t.Helper()
	b, err := OpenBundle(bytes.NewReader(raw), pub, t.TempDir())
	if err != nil {
		return nil, err
	}
	defer b.Close()
	return ImportBundle(context.Background(), store, b)
}

// TestBundleRoundTrip: the imported catalog equals the exported one, the
// source bundle carries created_at.
func TestBundleRoundTrip(t *testing.T) {
	pub, priv := testKeys(t)
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	target := NewMemoryStore()
	res, err := importBytes(t, target, pub, writeSample(t, priv, at))
	if err != nil {
		t.Fatal(err)
	}
	if res.Advisories != 2 || res.ManifestSHA256 == "" || !res.CreatedAt.Equal(at) {
		t.Errorf("import %+v, want 2 advisories of %v", res, at)
	}
	want := sampleCatalog(t)
	if !reflect.DeepEqual(target.advisories, want.advisories) {
		t.Errorf("imported %+v, want %+v", target.advisories, want.advisories)
	}
	src, err := target.GetSource(context.Background(), BundleSourceID)
	if err != nil || src.SyncedAt == nil || !src.SyncedAt.Equal(at) || src.Kind != KindBundle {
		t.Errorf("bundle source %+v %v, want synced_at %v", src, err, at)
	}
	if _, err = target.GetSource(context.Background(), KindNVD); err != nil {
		t.Errorf("source nvd of the bundle not created: %v", err)
	}
}

// rewrite repacks a bundle with change applied to the members (name →
// content; a nil content removes the member).
func rewrite(t *testing.T, raw []byte, change func(members map[string][]byte)) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	members := map[string][]byte{}
	var order []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		members[hdr.Name] = data
		order = append(order, hdr.Name)
	}
	change(members)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	for _, name := range order {
		data, ok := members[name]
		if !ok || data == nil {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gw.Close()
	return out.Bytes()
}

// TestBundleRejectsBadSignature: a bundle signed with another key and a
// changed manifest are invalid; nothing is written.
func TestBundleRejectsBadSignature(t *testing.T) {
	pub, _ := testKeys(t)
	_, other := testKeys(t)
	target := NewMemoryStore()
	if _, err := importBytes(t, target, pub, writeSample(t, other, time.Now())); !errors.Is(err, ErrBundleInvalid) {
		t.Errorf("foreign key: %v, want ErrBundleInvalid", err)
	}
	pub2, priv2 := testKeys(t)
	tampered := rewrite(t, writeSample(t, priv2, time.Now()), func(m map[string][]byte) {
		m[bundleManifestName] = bytes.Replace(m[bundleManifestName], []byte(BundleFormat), []byte(BundleFormat+" "), 1)
	})
	if _, err := importBytes(t, target, pub2, tampered); !errors.Is(err, ErrBundleInvalid) {
		t.Errorf("changed manifest: %v, want ErrBundleInvalid", err)
	}
	if target.Len() != 0 {
		t.Errorf("%d advisories written from refused bundles", target.Len())
	}
}

// TestBundleRejectsManipulatedFile: a changed data file fails its hash and
// nothing is written.
func TestBundleRejectsManipulatedFile(t *testing.T) {
	pub, priv := testKeys(t)
	tampered := rewrite(t, writeSample(t, priv, time.Now()), func(m map[string][]byte) {
		m["advisories-nvd.ndjson"] = bytes.Replace(m["advisories-nvd.ndjson"], []byte("7.1"), []byte("7.2"), 1)
	})
	target := NewMemoryStore()
	if _, err := importBytes(t, target, pub, tampered); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("changed file: %v, want ErrBundleInvalid", err)
	}
	if target.Len() != 0 {
		t.Errorf("%d advisories written", target.Len())
	}
}

// TestBundleRejectsMissingFile: a data file of the manifest that the
// archive lacks is invalid.
func TestBundleRejectsMissingFile(t *testing.T) {
	pub, priv := testKeys(t)
	missing := rewrite(t, writeSample(t, priv, time.Now()), func(m map[string][]byte) { delete(m, "advisories-csaf.ndjson") })
	target := NewMemoryStore()
	if _, err := importBytes(t, target, pub, missing); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("missing file: %v, want ErrBundleInvalid", err)
	}
	unknown := rewrite(t, writeSample(t, priv, time.Now()), func(m map[string][]byte) { delete(m, bundleSignatureName) })
	if _, err := importBytes(t, target, pub, unknown); !errors.Is(err, ErrBundleInvalid) {
		t.Errorf("missing signature: %v, want ErrBundleInvalid", err)
	}
	if _, err := importBytes(t, target, pub, []byte("no bundle")); !errors.Is(err, ErrBundleInvalid) {
		t.Errorf("no archive: %v, want ErrBundleInvalid", err)
	}
	if target.Len() != 0 {
		t.Errorf("%d advisories written", target.Len())
	}
}

// TestBundleRejectsOlderThanCurrent: a bundle older than the imported one
// is ErrBundleStale (409) and writes nothing; the same one again imports.
func TestBundleRejectsOlderThanCurrent(t *testing.T) {
	pub, priv := testKeys(t)
	newer, older := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	target := NewMemoryStore()
	if _, err := importBytes(t, target, pub, writeSample(t, priv, newer)); err != nil {
		t.Fatal(err)
	}
	delete(target.advisories, "nvd:CVE-2026-0002")
	if _, err := importBytes(t, target, pub, writeSample(t, priv, older)); !errors.Is(err, ErrBundleStale) {
		t.Fatalf("older bundle: %v, want ErrBundleStale", err)
	}
	if target.Len() != 1 {
		t.Errorf("the older bundle wrote advisories: %d", target.Len())
	}
	if _, err := importBytes(t, target, pub, writeSample(t, priv, newer)); err != nil {
		t.Errorf("same bundle again: %v", err)
	}
}

// TestBundleRejectsOversize: an archive above the limit is ErrBundleTooLarge.
func TestBundleRejectsOversize(t *testing.T) {
	pub, priv := testKeys(t)
	raw := writeSample(t, priv, time.Now())
	if _, err := openBundle(bytes.NewReader(raw), pub, t.TempDir(), int64(len(raw)-1)); !errors.Is(err, ErrBundleTooLarge) {
		t.Errorf("compressed above the limit: %v, want ErrBundleTooLarge", err)
	}
	if b, err := openBundle(bytes.NewReader(raw), pub, t.TempDir(), int64(len(raw))); err != nil {
		t.Errorf("at the limit: %v", err)
	} else {
		_ = b.Close()
	}
}

// refusingStore is a memory catalog that refuses one advisory like a CHECK
// of PostgreSQL (SQLSTATE 23514).
type refusingStore struct {
	*MemoryStore
	refuse string
}

func (s refusingStore) ReplaceAdvisory(ctx context.Context, adv *Advisory, affected []Affected) error {
	if adv.ID == s.refuse {
		return &pgconn.PgError{Code: "23514", ConstraintName: "vuln_advisory_cvss_score_check"}
	}
	return s.MemoryStore.ReplaceAdvisory(ctx, adv, affected)
}

// TestBundleSkipsRecordsTheCatalogRefuses: a record without vendor, one
// with a severity the catalog does not know and one PostgreSQL refuses
// with a CHECK are skipped and counted; the other advisories and the
// source bundle are written. The memory store of the export refuses the
// first two already.
func TestBundleSkipsRecordsTheCatalogRefuses(t *testing.T) {
	pub, priv := testKeys(t)
	export := sampleCatalog(t)
	ctx := context.Background()
	if err := export.ReplaceAdvisory(ctx, &Advisory{ID: "nvd:CVE-2026-0003", SourceID: KindNVD, Severity: "none"}, nil); !errors.Is(err, ErrInvalidAdvisory) {
		t.Errorf("export of severity none: %v, want ErrInvalidAdvisory", err)
	}
	if err := export.ReplaceAdvisory(ctx, &Advisory{ID: "nvd:CVE-2026-0004", SourceID: KindNVD},
		[]Affected{{Vendor: "  ", Product: "x"}}); !errors.Is(err, ErrInvalidAdvisory) {
		t.Errorf("export without vendor: %v, want ErrInvalidAdvisory", err)
	}
	// A bundle of an older exporter carries them anyway.
	export.advisories["nvd:CVE-2026-0003"] = bundleRecord{Advisory: Advisory{ID: "nvd:CVE-2026-0003", SourceID: KindNVD, Severity: "none"}}
	export.advisories["nvd:CVE-2026-0004"] = bundleRecord{Advisory: Advisory{ID: "nvd:CVE-2026-0004", SourceID: KindNVD},
		Affected: []Affected{{Vendor: "", Product: "x"}}}
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	if _, err := WriteBundle(&buf, export, priv, at); err != nil {
		t.Fatal(err)
	}
	target := refusingStore{MemoryStore: NewMemoryStore(), refuse: "csaf:cisco-sa-1"}
	res, err := importBytes(t, target, pub, buf.Bytes())
	if err != nil {
		t.Fatalf("import with refused records: %v, want them skipped", err)
	}
	if res.Advisories != 1 || res.Skipped != 3 || target.Len() != 1 {
		t.Errorf("import %+v, catalog %d; want 1 imported and 3 skipped", res, target.Len())
	}
	if src, serr := target.GetSource(ctx, BundleSourceID); serr != nil || src.SyncedAt == nil || !src.SyncedAt.Equal(at) {
		t.Errorf("bundle source %+v %v, want synced_at %v", src, serr, at)
	}
}

// TestBundleMalformedLineWritesNothing: every data file is parsed before
// the first write, a line that is no advisory is ErrBundleInvalid with
// nothing written, also from the files before it.
func TestBundleMalformedLineWritesNothing(t *testing.T) {
	dir := t.TempDir()
	good, _ := json.Marshal(bundleRecord{Advisory: Advisory{ID: "nvd:CVE-2026-0005", SourceID: KindNVD}})
	if err := os.WriteFile(filepath.Join(dir, "a.ndjson"), append(good, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.ndjson"), append(append(good, '\n'), []byte("{not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &VerifiedBundle{Manifest: &BundleManifest{CreatedAt: time.Now().UTC(), Files: []BundleFile{{Name: "a.ndjson"}, {Name: "b.ndjson"}}}, dir: dir}
	target := NewMemoryStore()
	if _, err := ImportBundle(context.Background(), target, b); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("malformed line: %v, want ErrBundleInvalid", err)
	}
	if target.Len() != 0 {
		t.Errorf("%d advisories written before the malformed line, want none", target.Len())
	}
}
