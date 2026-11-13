package vulnfeed

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The offline bundle of the vulnerability catalog (SEC-02, TEC-16, INS-08,
// WP-1120, E-235 option 2): a tar.gz in the format reticora-vulnfeed/1 with
//
//	manifest.json  format, created_at, the sources with synced_at and the
//	               files with their SHA-256
//	manifest.sig   Ed25519 signature over the bytes of manifest.json
//	*.ndjson       one advisory with its ranges per line (the model of
//	               WP-1117, the one of the online sync)
//
// cmd/vulnbundle writes it from the importers of this package
// (BundleImporters), the operator route POST /api/v1/admin/vulnfeed/bundle
// imports it: the archive is unpacked to a temporary directory, the
// signature is checked against the public key, every file against its hash,
// and only then the advisories are written in the system context.

// BundleFormat is the format of the bundle.
const BundleFormat = "reticora-vulnfeed/1"

// MaxBundleBytes is the size limit of a bundle (512 MiB, compressed and
// unpacked).
const MaxBundleBytes int64 = 512 << 20

// BundleSourceID is the vuln_source of the bundle import.
const BundleSourceID = KindBundle

// Names of the bundle members.
const (
	bundleManifestName  = "manifest.json"
	bundleSignatureName = "manifest.sig"
)

// DefaultBundlePublicKey is the public key of the release bundles
// (base64 of the 32-byte Ed25519 key); its private half is the secret
// VULNBUNDLE_SIGNING_KEY of the release workflow.
// RETICORA_VULNFEED_BUNDLE_PUBLIC_KEY replaces it (an own bundle, a key
// rotation).
const DefaultBundlePublicKey = "ru8Ae7QtB1vgFdPK8Jt5jbh7JsxMwy9Ut9lsHAtr3jU="

var (
	// ErrBundleInvalid: the signature, a hash, a file or the format of the
	// bundle is wrong; nothing was imported (422).
	ErrBundleInvalid = errors.New("vulnfeed: invalid bundle")
	// ErrBundleStale: the bundle is older than the imported one (409).
	ErrBundleStale = errors.New("vulnfeed: the bundle is older than the imported catalog")
	// ErrBundleTooLarge: the bundle exceeds MaxBundleBytes (413).
	ErrBundleTooLarge = errors.New("vulnfeed: the bundle exceeds 512 MiB")
)

// BundleManifest is manifest.json.
type BundleManifest struct {
	Format    string         `json:"format"`
	CreatedAt time.Time      `json:"created_at"`
	Sources   []BundleSource `json:"sources"`
	Files     []BundleFile   `json:"files"`
}

// BundleSource is a source of the bundle with its sync time.
type BundleSource struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	URL      string     `json:"url,omitempty"`
	SyncedAt *time.Time `json:"synced_at,omitempty"`
}

// BundleFile is a data file of the bundle.
type BundleFile struct {
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Advisories int    `json:"advisories"`
}

// bundleRecord is one line of a data file.
type bundleRecord struct {
	Advisory Advisory   `json:"advisory"`
	Affected []Affected `json:"affected"`
}

// ParsePublicKey reads a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("vulnfeed: the bundle public key is no base64 Ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivateKey reads a base64 Ed25519 private key: the 32-byte seed or
// the 64-byte key.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	switch {
	case err != nil:
		return nil, errors.New("vulnfeed: the bundle signing key is no base64")
	case len(raw) == ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case len(raw) == ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	}
	return nil, errors.New("vulnfeed: the bundle signing key is no Ed25519 key")
}

// MemoryStore is the catalog in memory (NVDStore) the exporter fills and
// writes as a bundle.
type MemoryStore struct {
	mu         sync.Mutex
	advisories map[string]bundleRecord
	sources    map[string]*Source
}

// NewMemoryStore returns an empty catalog.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{advisories: map[string]bundleRecord{}, sources: map[string]*Source{}}
}

// ReplaceAdvisory implements Store.
func (m *MemoryStore) ReplaceAdvisory(_ context.Context, adv *Advisory, affected []Affected) error {
	if err := checkAdvisoryData(adv, affected); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.advisories[adv.ID]; ok && prev.Advisory.SourceID != adv.SourceID {
		return ErrAdvisoryOwnedByOtherSource
	}
	ranges := make([]Affected, len(affected))
	for i := range affected {
		ranges[i] = affected[i]
		ranges[i].AdvisoryID = adv.ID
		ranges[i].Vendor, ranges[i].Product = Normalize(ranges[i].Vendor), Normalize(ranges[i].Product)
	}
	m.advisories[adv.ID] = bundleRecord{Advisory: *adv, Affected: ranges}
	return nil
}

// UpdateSource implements Store.
func (m *MemoryStore) UpdateSource(_ context.Context, src *Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *src
	cp.UpdatedAt = time.Now().UTC()
	m.sources[src.ID] = &cp
	return nil
}

// GetSource implements Store.
func (m *MemoryStore) GetSource(_ context.Context, id string) (*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sources[id]
	if !ok {
		return nil, fmt.Errorf("source %s: %w", id, pgx.ErrNoRows)
	}
	cp := *s
	return &cp, nil
}

// CSAFCovered implements NVDStore.
func (m *MemoryStore) CSAFCovered(_ context.Context, cves []string) (map[string]map[[2]string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[[2]string]bool{}
	for id := range m.advisories {
		rec := m.advisories[id]
		if !strings.HasPrefix(rec.Advisory.SourceID, KindCSAF) {
			continue
		}
		for _, cve := range rec.Advisory.CVEIDs {
			if !slices.Contains(cves, cve) {
				continue
			}
			if out[cve] == nil {
				out[cve] = map[[2]string]bool{}
			}
			for i := range rec.Affected {
				out[cve][[2]string{rec.Affected[i].Vendor, rec.Affected[i].Product}] = true
			}
		}
	}
	return out, nil
}

// RemoveCoveredNVDRanges implements NVDStore.
func (m *MemoryStore) RemoveCoveredNVDRanges(ctx context.Context) (int64, error) {
	var cves []string
	m.mu.Lock()
	for id := range m.advisories {
		cves = append(cves, m.advisories[id].Advisory.CVEIDs...)
	}
	m.mu.Unlock()
	covered, err := m.CSAFCovered(ctx, cves)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var removed int64
	for id := range m.advisories {
		rec := m.advisories[id]
		if rec.Advisory.SourceID != KindNVD {
			continue
		}
		kept := rec.Affected[:0:0]
		for i := range rec.Affected {
			f := rec.Affected[i]
			hit := false
			for _, cve := range rec.Advisory.CVEIDs {
				if covered[cve][[2]string{f.Vendor, f.Product}] {
					hit = true
				}
			}
			if hit {
				removed++
				continue
			}
			kept = append(kept, f)
		}
		rec.Affected = kept
		m.advisories[id] = rec
	}
	return removed, nil
}

// SetKEV implements NVDStore.
func (m *MemoryStore) SetKEV(_ context.Context, cves []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var changed int64
	for id := range m.advisories {
		rec := m.advisories[id]
		kev := slices.ContainsFunc(rec.Advisory.CVEIDs, func(c string) bool { return slices.Contains(cves, c) })
		if rec.Advisory.KEV != kev {
			rec.Advisory.KEV = kev
			m.advisories[id] = rec
			changed++
		}
	}
	return changed, nil
}

// Len is the number of advisories.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.advisories)
}

// WriteBundle writes the catalog of m as a signed bundle to w (one data
// file per source kind) and returns its manifest.
func WriteBundle(w io.Writer, m *MemoryStore, key ed25519.PrivateKey, createdAt time.Time) (*BundleManifest, error) {
	m.mu.Lock()
	byKind := map[string][]bundleRecord{}
	for id := range m.advisories {
		kind := sourceKind(m.advisories[id].Advisory.SourceID)
		byKind[kind] = append(byKind[kind], m.advisories[id])
	}
	manifest := &BundleManifest{Format: BundleFormat, CreatedAt: createdAt.UTC().Truncate(time.Second), Sources: []BundleSource{}, Files: []BundleFile{}}
	for _, s := range m.sources {
		manifest.Sources = append(manifest.Sources, BundleSource{ID: s.ID, Kind: s.Kind, URL: s.URL, SyncedAt: s.SyncedAt})
	}
	m.mu.Unlock()
	sort.Slice(manifest.Sources, func(i, j int) bool { return manifest.Sources[i].ID < manifest.Sources[j].ID })

	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	files := map[string][]byte{}
	for _, kind := range kinds {
		recs := byKind[kind]
		sort.Slice(recs, func(i, j int) bool { return recs[i].Advisory.ID < recs[j].Advisory.ID })
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for i := range recs {
			if err := enc.Encode(&recs[i]); err != nil {
				return nil, fmt.Errorf("encode advisory %s: %w", recs[i].Advisory.ID, err)
			}
		}
		name := "advisories-" + kind + ".ndjson"
		sum := sha256.Sum256(buf.Bytes())
		files[name] = buf.Bytes()
		manifest.Files = append(manifest.Files, BundleFile{Name: name, SHA256: hex.EncodeToString(sum[:]), Advisories: len(recs)})
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	put := func(name string, data []byte) error {
		if herr := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg}); herr != nil {
			return herr
		}
		_, werr := tw.Write(data)
		return werr
	}
	members := []string{bundleManifestName, bundleSignatureName}
	files[bundleManifestName] = raw
	files[bundleSignatureName] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw)))
	for _, f := range manifest.Files {
		members = append(members, f.Name)
	}
	for _, name := range members {
		if perr := put(name, files[name]); perr != nil {
			return nil, perr
		}
	}
	if cerr := tw.Close(); cerr != nil {
		return nil, cerr
	}
	return manifest, gz.Close()
}

// sourceKind is the kind of a source id (csaf:<url> is csaf).
func sourceKind(id string) string {
	kind, _, _ := strings.Cut(id, ":")
	return kind
}

// VerifiedBundle is a bundle unpacked to a directory whose signature and
// file hashes were checked (OpenBundle).
type VerifiedBundle struct {
	Manifest *BundleManifest
	// ManifestSHA256 is the hash of manifest.json (hex).
	ManifestSHA256 string
	dir            string
}

// Close removes the unpacked files.
func (b *VerifiedBundle) Close() error { return os.RemoveAll(b.dir) }

// invalid wraps ErrBundleInvalid with its reason.
// validation: fixed texts naming the failed check and a member of the
// archive.
func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBundleInvalid, fmt.Sprintf(format, args...))
}

// OpenBundle unpacks the bundle of r (at most MaxBundleBytes compressed,
// and as much unpacked) into a temporary directory below tmpDir and checks it: the
// signature of manifest.json against pub, the format, every listed file
// present with its SHA-256. A refused bundle is ErrBundleInvalid or
// ErrBundleTooLarge, and its files are removed.
func OpenBundle(r io.Reader, pub ed25519.PublicKey, tmpDir string) (*VerifiedBundle, error) {
	return openBundle(r, pub, tmpDir, MaxBundleBytes)
}

func openBundle(r io.Reader, pub ed25519.PublicKey, tmpDir string, limit int64) (vb *VerifiedBundle, err error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, invalid("no public key to check the signature")
	}
	dir, err := os.MkdirTemp(tmpDir, "vulnbundle-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	counted := &limitedReader{r: r, left: limit}
	gz, err := gzip.NewReader(counted)
	if err != nil {
		if counted.over {
			return nil, ErrBundleTooLarge
		}
		return nil, invalid("no gzip archive")
	}
	tr := tar.NewReader(gz)
	hashes := map[string]string{}
	var unpacked int64
	for {
		hdr, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			if counted.over {
				return nil, ErrBundleTooLarge
			}
			return nil, invalid("broken tar archive")
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := hdr.Name
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") || name == "" {
			return nil, invalid("member %q outside the bundle root", name)
		}
		if _, dup := hashes[name]; dup {
			return nil, invalid("member %q twice", name)
		}
		f, ferr := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if ferr != nil {
			return nil, ferr
		}
		h := sha256.New()
		n, cerr := io.Copy(io.MultiWriter(f, h), io.LimitReader(tr, MaxBundleBytes-unpacked+1))
		_ = f.Close()
		unpacked += n
		if counted.over || unpacked > MaxBundleBytes {
			return nil, ErrBundleTooLarge
		}
		if cerr != nil {
			return nil, invalid("broken member %q", name)
		}
		hashes[name] = hex.EncodeToString(h.Sum(nil))
	}
	// The rest of the stream (gzip trailer, padding) counts against the
	// limit as well.
	if _, derr := io.Copy(io.Discard, gz); derr != nil && !counted.over {
		return nil, invalid("broken gzip stream")
	}
	if _, derr := io.Copy(io.Discard, counted); derr != nil && !counted.over {
		return nil, invalid("broken stream")
	}
	if counted.over {
		return nil, ErrBundleTooLarge
	}
	raw, rerr := os.ReadFile(filepath.Join(dir, bundleManifestName))
	if rerr != nil {
		return nil, invalid("manifest.json missing")
	}
	sigText, serr := os.ReadFile(filepath.Join(dir, bundleSignatureName))
	if serr != nil {
		return nil, invalid("manifest.sig missing")
	}
	sig, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if derr != nil || !ed25519.Verify(pub, raw, sig) {
		return nil, invalid("the signature of manifest.json does not verify")
	}
	var manifest BundleManifest
	if jerr := json.Unmarshal(raw, &manifest); jerr != nil {
		return nil, invalid("manifest.json is no manifest")
	}
	if manifest.Format != BundleFormat {
		return nil, invalid("unknown format %q", manifest.Format)
	}
	if manifest.CreatedAt.IsZero() {
		return nil, invalid("manifest.json names no created_at")
	}
	for _, f := range manifest.Files {
		got, ok := hashes[f.Name]
		switch {
		case f.Name == bundleManifestName || f.Name == bundleSignatureName || filepath.Base(f.Name) != f.Name:
			return nil, invalid("file %q is no data file", f.Name)
		case !ok:
			return nil, invalid("file %q missing", f.Name)
		case !strings.EqualFold(got, f.SHA256):
			return nil, invalid("the hash of %q does not match", f.Name)
		}
	}
	sum := sha256.Sum256(raw)
	return &VerifiedBundle{Manifest: &manifest, ManifestSHA256: hex.EncodeToString(sum[:]), dir: dir}, nil
}

// limitedReader reads at most left bytes and reports a larger input.
type limitedReader struct {
	r    io.Reader
	left int64
	over bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// One byte more decides between the end and too large.
		var b [1]byte
		n, err := l.r.Read(b[:])
		if n > 0 {
			l.over = true
			return 0, ErrBundleTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

// BundleImport is the result of an import.
type BundleImport struct {
	CreatedAt      time.Time `json:"created_at"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Advisories     int       `json:"advisories"`
	Skipped        int       `json:"skipped"`
}

// bundleSourceFinisher is a store that sets the state of the source
// bundle only when it is not newer than the import (PGRepository): a
// concurrent import of a newer bundle that finished first keeps its state,
// the older one ends with ErrBundleStale.
type bundleSourceFinisher interface {
	finishBundleSource(ctx context.Context, src *Source) (bool, error)
}

// ImportBundle writes the advisories of a verified bundle to store: a
// bundle older than the imported one (vuln_source('bundle').synced_at) is
// ErrBundleStale and writes nothing. Every data file is parsed before the
// first write: a malformed line is ErrBundleInvalid with nothing written.
// The sources of the bundle that the catalog does not know are created
// (the advisories keep their source); the source bundle gets synced_at =
// created_at. An advisory another source of the catalog owns, and one the
// catalog refuses as data (checkAdvisoryData, a CHECK of PostgreSQL), is
// skipped and counted. The caller serializes the imports (the operator
// route has a single import slot).
func ImportBundle(ctx context.Context, store Store, b *VerifiedBundle) (*BundleImport, error) {
	for _, f := range b.Manifest.Files {
		if perr := parseBundleFile(filepath.Join(b.dir, f.Name)); perr != nil {
			return nil, perr
		}
	}
	current, err := store.GetSource(ctx, BundleSourceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		current = &Source{ID: BundleSourceID, Kind: KindBundle}
	case err != nil:
		return nil, err
	case current.SyncedAt != nil && b.Manifest.CreatedAt.Before(*current.SyncedAt):
		return nil, ErrBundleStale
	}
	for _, s := range b.Manifest.Sources {
		if s.ID == "" || s.ID == BundleSourceID {
			continue
		}
		if _, gerr := store.GetSource(ctx, s.ID); errors.Is(gerr, pgx.ErrNoRows) {
			if uerr := store.UpdateSource(ctx, &Source{ID: s.ID, Kind: s.Kind, URL: s.URL, SyncedAt: s.SyncedAt}); uerr != nil {
				return nil, uerr
			}
		} else if gerr != nil {
			return nil, gerr
		}
	}
	res := &BundleImport{CreatedAt: b.Manifest.CreatedAt, ManifestSHA256: b.ManifestSHA256}
	for _, f := range b.Manifest.Files {
		if ferr := importBundleFile(ctx, store, filepath.Join(b.dir, f.Name), res); ferr != nil {
			return nil, ferr
		}
	}
	at := b.Manifest.CreatedAt
	current.Kind, current.SyncedAt, current.LastError = KindBundle, &at, ""
	current.DocumentsTotal += int64(res.Advisories)
	if fin, ok := store.(bundleSourceFinisher); ok {
		done, ferr := fin.finishBundleSource(ctx, current)
		if ferr != nil {
			return nil, ferr
		}
		if !done {
			return nil, ErrBundleStale
		}
	} else if uerr := store.UpdateSource(ctx, current); uerr != nil {
		return nil, uerr
	}
	syncAge.synced(KindBundle, BundleSourceID, at)
	countDocuments(KindBundle, ResultImported, res.Advisories)
	return res, nil
}

func countDocuments(kind, result string, n int) {
	if n > 0 {
		documentsTotal.WithLabelValues(kind, result).Add(float64(n))
	}
}

// eachBundleRecord calls fn for every advisory of one data file; a line
// that is no advisory is ErrBundleInvalid.
func eachBundleRecord(path string, fn func(rec *bundleRecord) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec bundleRecord
		if err = json.Unmarshal(line, &rec); err != nil {
			return invalid("a line of %s is no advisory", filepath.Base(path))
		}
		if ferr := fn(&rec); ferr != nil {
			return ferr
		}
	}
	if err = sc.Err(); err != nil {
		return invalid("%s cannot be read", filepath.Base(path))
	}
	return nil
}

// parseBundleFile parses one data file without writing (the pass before
// the import).
func parseBundleFile(path string) error {
	return eachBundleRecord(path, func(*bundleRecord) error { return nil })
}

// importBundleFile writes the advisories of one data file.
func importBundleFile(ctx context.Context, store Store, path string, res *BundleImport) error {
	return eachBundleRecord(path, func(rec *bundleRecord) error {
		werr := checkAdvisoryData(&rec.Advisory, rec.Affected)
		if werr == nil {
			werr = store.ReplaceAdvisory(ctx, &rec.Advisory, rec.Affected)
		}
		switch {
		case errors.Is(werr, ErrAdvisoryOwnedByOtherSource) || errors.Is(werr, ErrInvalidAdvisory) || isDataError(werr):
			slog.WarnContext(ctx, "vulnfeed bundle: advisory skipped", "advisory_id", rec.Advisory.ID, "error", werr)
			res.Skipped++
		case werr != nil:
			return werr
		default:
			res.Advisories++
		}
		return nil
	})
}

// advisorySeverities are the severities the catalog stores
// (vuln_advisory_severity_check); empty is unknown.
var advisorySeverities = map[string]bool{"": true, "low": true, "medium": true, "high": true, "critical": true}

// checkAdvisoryData checks an advisory against the CHECKs of the catalog
// (vuln_advisory, vuln_affected): id and source, a known severity, a CVSS
// score from 0 to 10 and per range a vendor and a product that are not
// empty after Normalize. A refusal wraps ErrInvalidAdvisory; the memory
// store of the export refuses alike, so a bundle never carries a record
// PostgreSQL refuses.
func checkAdvisoryData(adv *Advisory, affected []Affected) error {
	if adv == nil || adv.ID == "" || adv.SourceID == "" {
		return ErrInvalidAdvisory
	}
	if !advisorySeverities[adv.Severity] {
		return fmt.Errorf("%w: severity %q", ErrInvalidAdvisory, adv.Severity)
	}
	if adv.CVSSScore != nil && (*adv.CVSSScore < 0 || *adv.CVSSScore > 10) {
		return fmt.Errorf("%w: cvss score out of range", ErrInvalidAdvisory)
	}
	for i := range affected {
		if Normalize(affected[i].Vendor) == "" || Normalize(affected[i].Product) == "" {
			return fmt.Errorf("%w: a range without vendor or product", ErrInvalidAdvisory)
		}
	}
	return nil
}

// ListSources returns every source of the catalog with its sync state
// (the status of the operator path, WP-1120 AK5), ordered by id.
func (r *PGRepository) ListSources(ctx context.Context) ([]Source, error) {
	var out []Source
	err := r.read(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, kind, url, etag, last_modified, cursor, synced_at, documents_total, errors_total,
				last_error, updated_at FROM vuln_source ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s Source
			if serr := rows.Scan(&s.ID, &s.Kind, &s.URL, &s.ETag, &s.LastModified, &s.Cursor, &s.SyncedAt, &s.DocumentsTotal,
				&s.ErrorsTotal, &s.LastError, &s.UpdatedAt); serr != nil {
				return serr
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	return out, nil
}

// ListSources lists the sources of the memory catalog, ordered by id.
func (m *MemoryStore) ListSources(context.Context) ([]Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Source, 0, len(m.sources))
	for _, s := range m.sources {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// BundleSources configures the importers of the bundle export.
type BundleSources struct {
	// CSAFProviders are the CSAF providers (DefaultCSAFProvider when
	// empty).
	CSAFProviders []string
	// NVDAPIKey is the key of the NVD API (optional).
	NVDAPIKey string
	// KEVURL replaces DefaultKEVURL.
	KEVURL string
	// Egress are the options of the egress client.
	Egress egress.Options
}

// BundleImporters returns the importers of this package against store, in
// the order the bundle export runs them: CSAF (WP-1118), then NVD with the
// KEV sync (WP-1119). A new importer of the package is added here and the
// export tool (cmd/vulnbundle) runs it without a change of its own.
func BundleImporters(store NVDStore, cfg *BundleSources) ([]func(ctx context.Context) error, error) {
	csaf, err := NewCSAF(store, &CSAFConfig{Providers: cfg.CSAFProviders, Explicit: len(cfg.CSAFProviders) > 0, Egress: cfg.Egress})
	if err != nil {
		return nil, err
	}
	nvd, err := NewNVD(store, &NVDConfig{APIKey: cfg.NVDAPIKey, KEVURL: cfg.KEVURL, Egress: cfg.Egress})
	if err != nil {
		return nil, err
	}
	return []func(ctx context.Context) error{csaf.Run, nvd.RunWithKEV}, nil
}
