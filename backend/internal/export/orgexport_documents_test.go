package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// WP-1150 (TLC-05): the documents of the full export as ZIP parts in a
// FileStore. Entries and SHA-256 of the manifest match the objects; only
// clean versions carry content, the others are listed with their status; a
// checksum mismatch is noted and does not stop the export; the parts are
// split at the limit (1 MiB in the test).
//
// Retest:
//   cd backend && go test ./internal/export/ -run TestOrgExportDocuments

const (
	docOrg = "0f000000-0000-4000-8000-000000001150"
	docJob = "1f000000-0000-4000-8000-000000001150"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type docFixture struct {
	store    *blob.FileStore
	exporter *OrgExporter
	manifest *OrgExportManifest
	versions []documentVersion
	content  map[string][]byte
}

func newDocFixture(t *testing.T) *docFixture {
	t.Helper()
	store := blob.NewFileStore(t.TempDir())
	return &docFixture{store: store, exporter: NewOrgExporter(nil, store, nil),
		manifest: &OrgExportManifest{OrganizationID: docOrg, JobID: docJob}, content: map[string][]byte{}}
}

// add stores a version with content of size bytes (no object for missing).
func (f *docFixture) add(t *testing.T, doc string, version int, size int, status string, checksum func([]byte) string) {
	t.Helper()
	data := bytes.Repeat([]byte{byte('a' + version)}, size)
	key := blob.OrgKey(docOrg, "documents", doc, fmt.Sprint(version))
	if err := f.store.Put(context.Background(), document.Bucket, key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	f.content[fmt.Sprintf("documents/%s/%d", doc, version)] = data
	f.versions = append(f.versions, documentVersion{DocumentID: doc, Version: version, StorageKey: key, Size: int64(size),
		Checksum: checksum(data), ScanStatus: status, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
}

func (f *docFixture) run(t *testing.T) *OrgExportDocuments {
	t.Helper()
	docs, err := f.exporter.writeDocuments(context.Background(), f.manifest, f.versions)
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// read opens a ZIP part from the store and returns its entries.
func (f *docFixture) read(t *testing.T, key string) (map[string][]byte, []byte) {
	t.Helper()
	rc, err := f.store.Get(context.Background(), ExportBucket, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		_ = r.Close()
		out[file.Name] = b
	}
	return out, raw
}

func TestOrgExportDocumentsZipHashes(t *testing.T) {
	f := newDocFixture(t)
	f.add(t, "d1", 1, 1000, "clean", sum)
	f.add(t, "d1", 2, 2000, "clean", sum)
	f.add(t, "d2", 1, 10, "clean", func([]byte) string { return "" }) // taken over without checksum
	docs := f.run(t)
	if docs.Count != 3 || docs.TotalBytes != 3010 || len(docs.Parts) != 1 {
		t.Fatalf("documents %+v", docs)
	}
	part := docs.Parts[0]
	if part.Key != OrgExportPrefix(docOrg, docJob)+"/documents-1.zip" || part.Entries != 3 {
		t.Fatalf("part %+v", part)
	}
	entries, raw := f.read(t, part.Key)
	if part.SHA256 != sum(raw) || part.Bytes != int64(len(raw)) {
		t.Fatalf("hash of the part %s/%d, object %s/%d", part.SHA256, part.Bytes, sum(raw), len(raw))
	}
	for _, obj := range docs.Objects {
		data, ok := entries[obj.Entry]
		if !ok || !bytes.Equal(data, f.content[obj.Entry]) || obj.SHA256 != sum(data) || obj.Bytes != int64(len(data)) || obj.Part != part.Key {
			t.Fatalf("object %+v does not match its entry", obj)
		}
		if obj.ChecksumMismatch {
			t.Fatalf("mismatch reported for %s", obj.Entry)
		}
	}
	if len(f.manifest.PendingDocuments) != 1 || f.manifest.PendingDocuments[0] != part.Key {
		t.Fatalf("pending parts %v", f.manifest.PendingDocuments)
	}
}

func TestOrgExportDocumentsSkipsQuarantine(t *testing.T) {
	f := newDocFixture(t)
	f.add(t, "d1", 1, 100, "clean", sum)
	for i, status := range []string{"infected", "pending_scan", "scan_failed"} {
		f.add(t, "q", i+1, 100, status, sum)
	}
	docs := f.run(t)
	if docs.Count != 1 || len(docs.Objects) != 4 {
		t.Fatalf("documents %+v", docs)
	}
	entries, _ := f.read(t, docs.Parts[0].Key)
	if len(entries) != 1 {
		t.Fatalf("entries %v", entries)
	}
	for _, obj := range docs.Objects[1:] {
		if obj.Entry != "" || obj.Part != "" || obj.SHA256 != "" || obj.Bytes != 0 || obj.ScanStatus == "clean" {
			t.Fatalf("quarantined version with content: %+v", obj)
		}
	}
	if got := []string{docs.Objects[1].ScanStatus, docs.Objects[2].ScanStatus, docs.Objects[3].ScanStatus}; strings.Join(got, ",") != "infected,pending_scan,scan_failed" {
		t.Fatalf("statuses %v", got)
	}
	// Nothing clean: no part at all.
	g := newDocFixture(t)
	g.add(t, "q", 1, 10, "infected", sum)
	if docs := g.run(t); len(docs.Parts) != 0 || len(g.manifest.PendingDocuments) != 0 {
		t.Fatalf("part without content: %+v", docs)
	}
}

func TestOrgExportDocumentsChecksumMismatch(t *testing.T) {
	f := newDocFixture(t)
	f.add(t, "d1", 1, 100, "clean", func([]byte) string { return strings.Repeat("0", 64) })
	f.add(t, "d2", 1, 100, "clean", sum)
	// A clean version whose object is gone is listed as missing.
	f.versions = append(f.versions, documentVersion{DocumentID: "d3", Version: 1, StorageKey: blob.OrgKey(docOrg, "documents", "gone"),
		Size: 5, ScanStatus: "clean"})
	docs := f.run(t)
	if !docs.Objects[0].ChecksumMismatch || docs.Objects[1].ChecksumMismatch || !docs.Objects[2].Missing || docs.Count != 2 {
		t.Fatalf("objects %+v", docs.Objects)
	}
}

func TestOrgExportDocumentsSplitsParts(t *testing.T) {
	f := newDocFixture(t)
	f.exporter.WithDocumentPartLimit(1 << 20)
	f.add(t, "d1", 1, 600<<10, "clean", sum)
	f.add(t, "d1", 2, 600<<10, "clean", sum) // does not fit beside the first
	f.add(t, "d2", 1, 3<<20, "clean", sum)   // larger than a part: alone
	f.add(t, "d3", 1, 100<<10, "clean", sum)
	docs := f.run(t)
	if len(docs.Parts) != 4 {
		t.Fatalf("%d parts: %+v", len(docs.Parts), docs.Parts)
	}
	seen := 0
	for i, p := range docs.Parts {
		if !strings.HasSuffix(p.Key, fmt.Sprintf("/documents-%d.zip", i+1)) {
			t.Fatalf("key %s", p.Key)
		}
		entries, raw := f.read(t, p.Key)
		seen += len(entries)
		if p.SHA256 != sum(raw) || p.Entries != len(entries) {
			t.Fatalf("part %+v", p)
		}
		if len(entries) > 1 && p.Bytes > 1<<20+4096 {
			t.Fatalf("part %s exceeds the limit with %d bytes", p.Key, p.Bytes)
		}
	}
	if seen != 4 {
		t.Fatalf("%d entries", seen)
	}
}

// failingStore fails the stream of every upload after a few bytes.
type failingStore struct{ *blob.FileStore }

func (s failingStore) PutStream(_ context.Context, _, _ string, r io.Reader, _ string) error {
	_, _ = io.CopyN(io.Discard, r, 10)
	return errors.New("upload refused")
}

func TestOrgExportDocumentsFailedUploadLeavesNoObject(t *testing.T) {
	f := newDocFixture(t)
	f.add(t, "d1", 1, 100<<10, "clean", sum)
	f.exporter.blobs = failingStore{f.store}
	if _, err := f.exporter.writeDocuments(context.Background(), f.manifest, f.versions); err == nil {
		t.Fatal("no error")
	}
	if ok, _ := blob.Exists(context.Background(), f.store, ExportBucket, f.manifest.PendingDocuments[0]); ok {
		t.Fatal("a part of the failed upload exists")
	}
}
