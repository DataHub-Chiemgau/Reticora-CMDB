package export

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// Documents of the full export (TLC-05, WP-1150): after the tables the job
// org.export streams the content of every document version of the
// organization into documents-<n>.zip next to the manifest, an entry
// documents/<document_id>/<version> per version. Only versions with
// scan_status clean carry content; the others are listed in the manifest
// with their status. The objects are read one by one (blob.Store.Get),
// written into a zip.Writer whose output runs through an io.Pipe into
// blob.PutStream, so at most one upload part lies in memory. SHA-256 per
// object is computed while streaming and compared with checksum_sha256.

// OrgExportDocumentPartLimit is the size at which a ZIP part is closed and
// the next one begun.
const OrgExportDocumentPartLimit int64 = 2 << 30

// orgExportDocumentsStep names the documents step for the step hook.
const orgExportDocumentsStep = "documents"

const orgExportZipType = "application/zip"

// OrgExportDocuments is the documents section of the manifest.
type OrgExportDocuments struct {
	// Count is the number of objects exported with content, Bytes their sum.
	Count      int                 `json:"count"`
	TotalBytes int64               `json:"total_bytes"`
	Parts      []OrgExportZipPart  `json:"parts"`
	Objects    []OrgExportDocument `json:"objects"`
	SnapshotAt time.Time           `json:"snapshot_at"`
	Snapshot   string              `json:"snapshot"`
}

// OrgExportZipPart is one ZIP part with its size and SHA-256.
type OrgExportZipPart struct {
	Key     string `json:"key"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	Entries int    `json:"entries"`
}

// OrgExportDocument is one document version of the manifest. A version
// without content (not clean, or its object missing in the storage) has no
// entry, part and hash.
type OrgExportDocument struct {
	DocumentID string `json:"document_id"`
	Version    int    `json:"version"`
	ScanStatus string `json:"scan_status"`
	Entry      string `json:"entry,omitempty"`
	Part       string `json:"part,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	// ChecksumMismatch: the content's SHA-256 differs from the stored
	// checksum_sha256 (versions taken over without one are not checked).
	ChecksumMismatch bool `json:"checksum_mismatch,omitempty"`
	// Missing: the version is clean but its object is not in the storage.
	Missing bool `json:"missing,omitempty"`
}

// documentVersion is a row of document_version the step reads.
type documentVersion struct {
	DocumentID string
	Version    int
	StorageKey string
	Size       int64
	Checksum   string
	ScanStatus string
	CreatedAt  time.Time
}

// WithDocumentPartLimit sets the size of a ZIP part (tests).
func (e *OrgExporter) WithDocumentPartLimit(limit int64) *OrgExporter {
	if limit > 0 {
		e.docPartLimit = limit
	}
	return e
}

// documentVersions reads the versions of the organization in the snapshot.
func documentVersions(ctx context.Context, tx pgx.Tx, orgID string) ([]documentVersion, error) {
	rows, err := tx.Query(ctx, `SELECT document_id::text, version, storage_key, size, COALESCE(checksum_sha256, ''), scan_status, created_at
		FROM document_version WHERE organization_id = $1 ORDER BY document_id, version`, orgID)
	if err != nil {
		return nil, fmt.Errorf("read document versions: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (documentVersion, error) {
		var v documentVersion
		err := r.Scan(&v.DocumentID, &v.Version, &v.StorageKey, &v.Size, &v.Checksum, &v.ScanStatus, &v.CreatedAt)
		return v, err
	})
}

// exportDocuments is the documents step of an attempt: the versions of the
// snapshot of tx into the ZIP parts.
func (e *OrgExporter) exportDocuments(ctx context.Context, tx pgx.Tx, m *OrgExportManifest) (*OrgExportDocuments, error) {
	versions, err := documentVersions(ctx, tx, m.OrganizationID)
	if err != nil {
		return nil, err
	}
	return e.writeDocuments(ctx, m, versions)
}

// hashCounter counts and hashes what passes into w.
type hashCounter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func (c *hashCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

// zipPart is an open ZIP part: the archive writes into the pipe whose
// reader blob.PutStream uploads.
type zipPart struct {
	key     string
	pw      *io.PipeWriter
	out     *hashCounter
	zw      *zip.Writer
	done    chan error
	entries int
}

// openPart begins ZIP part n. Its key is recorded in the manifest as
// pending first, so a crash leaves no object a later attempt does not know.
func (e *OrgExporter) openPart(ctx context.Context, m *OrgExportManifest, n int) (*zipPart, error) {
	key := fmt.Sprintf("%s/documents-%d.zip", OrgExportPrefix(m.OrganizationID, m.JobID), n)
	m.PendingDocuments = append(m.PendingDocuments, key)
	if err := e.saveManifest(ctx, m); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	p := &zipPart{key: key, pw: pw, out: &hashCounter{w: pw, h: sha256.New()}, done: make(chan error, 1)}
	go func() {
		err := blob.PutStream(ctx, e.blobs, e.bucket, key, pr, orgExportZipType)
		// A failed upload ends the writer side, so the archive stops.
		_ = pr.CloseWithError(errors.Join(err, io.ErrClosedPipe))
		p.done <- err
	}()
	p.zw = zip.NewWriter(p.out)
	return p, nil
}

// close finishes the archive and waits for the upload.
func (p *zipPart) close() (OrgExportZipPart, error) {
	err := p.zw.Close()
	if err != nil {
		_ = p.pw.CloseWithError(err)
		<-p.done
		return OrgExportZipPart{}, err
	}
	_ = p.pw.Close()
	if err := <-p.done; err != nil {
		return OrgExportZipPart{}, fmt.Errorf("store %s: %w", p.key, err)
	}
	return OrgExportZipPart{Key: p.key, Bytes: p.out.n, SHA256: hex.EncodeToString(p.out.h.Sum(nil)), Entries: p.entries}, nil
}

// abort ends the upload of the part: PutStream aborts it (no object).
func (p *zipPart) abort(cause error) {
	_ = p.pw.CloseWithError(cause)
	<-p.done
}

// writeDocuments streams the clean versions into ZIP parts of at most
// docPartLimit bytes (a larger object is a part of its own) and lists every
// version in the manifest section it returns.
func (e *OrgExporter) writeDocuments(ctx context.Context, m *OrgExportManifest, versions []documentVersion) (docs *OrgExportDocuments, err error) {
	limit := e.docPartLimit
	if limit <= 0 {
		limit = OrgExportDocumentPartLimit
	}
	docs = &OrgExportDocuments{Parts: []OrgExportZipPart{}, Objects: []OrgExportDocument{}}
	var part *zipPart
	defer func() {
		if err != nil && part != nil {
			part.abort(err)
		}
	}()
	finish := func() error {
		done, cerr := part.close()
		part = nil
		if cerr != nil {
			return cerr
		}
		docs.Parts = append(docs.Parts, done)
		return nil
	}
	for i := range versions {
		v := &versions[i]
		if cancelled(ctx) {
			return nil, jobs.ErrCancelled
		}
		obj := OrgExportDocument{DocumentID: v.DocumentID, Version: v.Version, ScanStatus: v.ScanStatus}
		if v.ScanStatus != "clean" {
			docs.Objects = append(docs.Objects, obj)
			continue
		}
		body, gerr := e.blobs.Get(ctx, document.Bucket, v.StorageKey)
		if errors.Is(gerr, blob.ErrNotFound) {
			obj.Missing = true
			docs.Objects = append(docs.Objects, obj)
			continue
		}
		if gerr != nil {
			return nil, fmt.Errorf("read document %s version %d: %w", v.DocumentID, v.Version, gerr)
		}
		if part != nil && part.entries > 0 && part.out.n+v.Size > limit {
			if ferr := finish(); ferr != nil {
				_ = body.Close()
				return nil, ferr
			}
		}
		if part == nil {
			if part, err = e.openPart(ctx, m, len(docs.Parts)+1); err != nil {
				_ = body.Close()
				return nil, err
			}
		}
		obj.Entry = fmt.Sprintf("documents/%s/%d", v.DocumentID, v.Version)
		obj.Part = part.key
		w, cerr := part.zw.CreateHeader(&zip.FileHeader{Name: obj.Entry, Method: zip.Store, Modified: v.CreatedAt.UTC()})
		if cerr != nil {
			_ = body.Close()
			return nil, cerr
		}
		sum := sha256.New()
		n, cerr := io.Copy(io.MultiWriter(w, sum), body)
		_ = body.Close()
		if cerr != nil {
			return nil, fmt.Errorf("stream document %s version %d: %w", v.DocumentID, v.Version, cerr)
		}
		part.entries++
		obj.Bytes, obj.SHA256 = n, hex.EncodeToString(sum.Sum(nil))
		obj.ChecksumMismatch = v.Checksum != "" && v.Checksum != obj.SHA256
		docs.Count++
		docs.TotalBytes += n
		docs.Objects = append(docs.Objects, obj)
		if part.out.n >= limit {
			if ferr := finish(); ferr != nil {
				return nil, ferr
			}
		}
	}
	if part != nil {
		if ferr := finish(); ferr != nil {
			return nil, ferr
		}
	}
	return docs, nil
}
