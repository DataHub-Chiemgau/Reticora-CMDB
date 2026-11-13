package operator

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The vulnerability catalog on the operator path (SEC-02, TEC-16, WP-1120,
// E-235 option 2): the import of a signed offline bundle and the state of
// the feeds. Both run behind Middleware (an operator session, issued only
// with MFA, or the break-glass token); the import writes its own entry to
// operator_audit with the hash of the manifest, the number of advisories
// and the result.

// VulnfeedCatalog is the catalog the operator path reads and writes
// (vulnfeed.PGRepository; writes in the system context).
type VulnfeedCatalog interface {
	vulnfeed.Store
	ListSources(ctx context.Context) ([]vulnfeed.Source, error)
}

// VulnfeedConfig configures the catalog routes.
type VulnfeedConfig struct {
	Catalog VulnfeedCatalog
	// PublicKey checks the bundle signature; nil (KeyError set) refuses
	// every bundle.
	PublicKey ed25519.PublicKey
	KeyError  error
	// OnlineSync reports whether the online feeds run (not in the
	// air-gapped profile without a CSAF provider list).
	OnlineSync bool
	// TempDir is where a bundle is unpacked (os.TempDir when empty).
	TempDir string
	// Now is the clock (tests).
	Now func() time.Time
	// ImportTimeout is the read and write deadline of one import (upload
	// of up to 512 MiB and the writes); DefaultBundleImportTimeout when
	// zero. The server's ReadTimeout and WriteTimeout (15 s) do not fit.
	ImportTimeout time.Duration

	// slot admits one import per instance at a time; a concurrent one is
	// 503 with Retry-After (WithVulnfeed creates it).
	slot chan struct{}
}

// DefaultBundleImportTimeout is the deadline of one bundle import.
const DefaultBundleImportTimeout = time.Hour

// bundleImportRetryAfter is the Retry-After of a refused concurrent
// import, in seconds.
const bundleImportRetryAfter = "60"

// WithVulnfeed serves the catalog routes; without it they answer 503.
func (h *Handler) WithVulnfeed(cfg *VulnfeedConfig) *Handler {
	if cfg != nil && cfg.slot == nil {
		cfg.slot = make(chan struct{}, 1)
	}
	h.vulnfeed = cfg
	return h
}

// beginBundleImport takes the single import slot and extends the
// deadlines of the connection for the import; ok false answered 503.
func (cfg *VulnfeedConfig) beginBundleImport(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case cfg.slot <- struct{}{}:
	default:
		w.Header().Set("Retry-After", bundleImportRetryAfter)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "a bundle import is in progress, retry later")
		return nil, false
	}
	timeout := cfg.ImportTimeout
	if timeout <= 0 {
		timeout = DefaultBundleImportTimeout
	}
	// A writer without deadlines (tests) keeps the server's; the
	// middleware writers pass the controller through (Unwrap).
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(timeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
	return func() { <-cfg.slot }, true
}

// ImportVulnfeedBundle handles POST /api/v1/admin/vulnfeed/bundle: the body
// (application/octet-stream, at most 512 MiB) is unpacked to a temporary
// directory, its signature and file hashes are checked, then the advisories
// are written. An invalid bundle is 422, an older one than the imported 409,
// an oversized one 413; nothing is imported then. One import runs at a time
// (503 with Retry-After otherwise), with its own deadlines instead of the
// server's (ImportTimeout).
func (h *Handler) ImportVulnfeedBundle(w http.ResponseWriter, r *http.Request) {
	op, _ := FromContext(r.Context())
	cfg := h.vulnfeed
	if cfg == nil || cfg.Catalog == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the vulnerability catalog is not configured")
		return
	}
	entry := &AuditEntry{OperatorID: op.ID, OperatorKind: op.Kind, Action: "vulnfeed.bundle.import",
		Details: map[string]any{"remote": r.RemoteAddr}}
	refuse := func(status int, title, detail, result string) {
		entry.Status = status
		entry.Details["result"] = result
		if h.record(w, r, entry) {
			markAudited(r.Context())
			api.WriteError(w, status, title, detail)
		}
	}
	release, admitted := cfg.beginBundleImport(w)
	if !admitted {
		return
	}
	defer release()
	if cfg.PublicKey == nil {
		slog.Error("vulnfeed bundle import without public key", "error", cfg.KeyError)
		refuse(http.StatusUnprocessableEntity, "Unprocessable Entity", "no valid bundle public key is configured", "no_public_key")
		return
	}
	body := http.MaxBytesReader(w, r.Body, vulnfeed.MaxBundleBytes+1)
	tmp := cfg.TempDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	bundle, err := vulnfeed.OpenBundle(body, cfg.PublicKey, tmp)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, vulnfeed.ErrBundleTooLarge) || errors.As(err, &tooLarge):
		refuse(http.StatusRequestEntityTooLarge, "Payload Too Large", vulnfeed.ErrBundleTooLarge.Error(), "too_large")
		return
	case errors.Is(err, vulnfeed.ErrBundleInvalid):
		// The reason names the failed check and a member name of the
		// archive, no secret.
		refuse(http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error(), "invalid")
		return
	case err != nil:
		slog.Error("vulnfeed bundle could not be unpacked", "error", err)
		refuse(http.StatusInternalServerError, "Internal Server Error", "the bundle could not be unpacked", "error")
		return
	}
	defer func() { _ = bundle.Close() }()
	entry.Details["manifest_sha256"] = bundle.ManifestSHA256
	entry.Details["created_at"] = bundle.Manifest.CreatedAt.Format(time.RFC3339)
	res, err := vulnfeed.ImportBundle(context.WithoutCancel(r.Context()), cfg.Catalog, bundle)
	switch {
	case errors.Is(err, vulnfeed.ErrBundleStale):
		refuse(http.StatusConflict, "Conflict", vulnfeed.ErrBundleStale.Error(), "stale")
		return
	case errors.Is(err, vulnfeed.ErrBundleInvalid):
		refuse(http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error(), "invalid")
		return
	case err != nil:
		slog.Error("vulnfeed bundle import failed", "error", err)
		refuse(http.StatusInternalServerError, "Internal Server Error", "the bundle could not be imported", "error")
		return
	}
	entry.Status = http.StatusOK
	entry.Details["result"] = "imported"
	entry.Details["advisories"] = res.Advisories
	entry.Details["skipped"] = res.Skipped
	if !h.record(w, r, entry) {
		return
	}
	markAudited(r.Context())
	api.WriteJSON(w, http.StatusOK, res)
}

// vulnfeedSourceStatus is the state of one feed source.
type vulnfeedSourceStatus struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	SyncedAt       *time.Time `json:"synced_at,omitempty"`
	AgeSeconds     *int64     `json:"age_seconds,omitempty"`
	DocumentsTotal int64      `json:"documents_total"`
	ErrorsTotal    int64      `json:"errors_total"`
	LastError      string     `json:"last_error,omitempty"`
}

// vulnfeedStatus is the answer of GET /api/v1/admin/vulnfeed/status.
type vulnfeedStatus struct {
	OnlineSync bool                   `json:"online_sync"`
	Sources    []vulnfeedSourceStatus `json:"sources"`
	// Bundle is the state of the imported bundle while the online sync is
	// off (the only source of the catalog then).
	Bundle *vulnfeedSourceStatus `json:"bundle,omitempty"`
}

// VulnfeedStatus handles GET /api/v1/admin/vulnfeed/status: per source
// synced_at, age, counters and last error, and whether the online sync
// runs; without it the state and age of the bundle.
func (h *Handler) VulnfeedStatus(w http.ResponseWriter, r *http.Request) {
	cfg := h.vulnfeed
	if cfg == nil || cfg.Catalog == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the vulnerability catalog is not configured")
		return
	}
	sources, err := cfg.Catalog.ListSources(r.Context())
	if err != nil {
		slog.Error("vulnfeed status", "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "the state of the feeds could not be read")
		return
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	out := vulnfeedStatus{OnlineSync: cfg.OnlineSync, Sources: []vulnfeedSourceStatus{}}
	for i := range sources {
		s := &sources[i]
		st := vulnfeedSourceStatus{ID: s.ID, Kind: s.Kind, SyncedAt: s.SyncedAt, DocumentsTotal: s.DocumentsTotal,
			ErrorsTotal: s.ErrorsTotal, LastError: s.LastError}
		if s.SyncedAt != nil {
			age := int64(now().Sub(*s.SyncedAt).Seconds())
			st.AgeSeconds = &age
		}
		out.Sources = append(out.Sources, st)
		if s.ID == vulnfeed.BundleSourceID && !cfg.OnlineSync {
			b := st
			out.Bundle = &b
		}
	}
	if !cfg.OnlineSync && out.Bundle == nil {
		out.Bundle = &vulnfeedSourceStatus{ID: vulnfeed.BundleSourceID, Kind: vulnfeed.KindBundle}
	}
	api.WriteJSON(w, http.StatusOK, out)
}
