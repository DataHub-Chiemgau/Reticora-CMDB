package vulnfeed

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// CSAF 2.0 import (SEC-02, WP-1118, E-234 option 3, E-235): the documents
// of the configured providers (provider-metadata.json, or aggregator.json
// with the mirrors on the aggregator's host) are read from their
// distributions (ROLIE feeds or a directory with changes.csv),
// incrementally after the cursor of the source and with conditional
// requests (ETag). Every document is checked against its hash (.sha512,
// else .sha256) and its OpenPGP signature (.asc) with the keys of
// public_openpgp_keys whose fingerprint matches; a document that fails is
// counted and not written, the run goes on.

// CSAFKind is the system job of the CSAF import.
const CSAFKind = "vulnfeed.csaf"

// DefaultCSAFInterval is the default of RETICORA_VULNFEED_CSAF_INTERVAL.
const DefaultCSAFInterval = 6 * time.Hour

// DefaultCSAFProvider is the CSAF provider of the BSI (CERT-Bund WID), the
// default of RETICORA_VULNFEED_CSAF_PROVIDERS.
const DefaultCSAFProvider = "https://wid.cert-bund.de/.well-known/csaf/provider-metadata.json"

// Size limits of the fetched files.
const (
	csafMaxMetadata = 4 << 20
	csafMaxListing  = 64 << 20
	csafMaxDocument = 32 << 20
	csafMaxSidecar  = 64 << 10
)

// ErrCSAFDisabled: in the air-gapped profile the CSAF import runs only with
// an explicitly set provider list (an own mirror, E-235).
var ErrCSAFDisabled = errors.New("vulnfeed: CSAF import off in the air-gapped profile without an explicit provider list")

// ErrNotHTTPS refuses a provider URL without HTTPS.
var ErrNotHTTPS = errors.New("vulnfeed: provider URL must be an absolute https URL")

// CSAFConfig configures the CSAF import.
type CSAFConfig struct {
	// Providers are the URLs of provider-metadata.json or aggregator.json
	// (RETICORA_VULNFEED_CSAF_PROVIDERS).
	Providers []string
	// Explicit is true when the provider list was set explicitly.
	Explicit bool
	// AirGapped is RETICORA_AIR_GAPPED.
	AirGapped bool
	// Interval is the schedule (DefaultCSAFInterval when 0).
	Interval time.Duration
	// Egress are the options of the egress client (private targets).
	Egress egress.Options
	// Client replaces the egress client (tests); the host restriction
	// applies to it as well.
	Client *http.Client
	// Now is the clock (tests).
	Now func() time.Time
}

// CSAF is the CSAF import.
type CSAF struct {
	store     Store
	providers []string
	interval  time.Duration
	base      *http.Client
	now       func() time.Time
}

// NewCSAF returns the CSAF import of cfg. Every provider must be an https
// URL (ErrNotHTTPS); air-gapped without an explicit list it is
// ErrCSAFDisabled.
func NewCSAF(store Store, cfg *CSAFConfig) (*CSAF, error) {
	if cfg.AirGapped && !cfg.Explicit {
		return nil, ErrCSAFDisabled
	}
	providers := cfg.Providers
	if len(providers) == 0 && !cfg.Explicit {
		providers = []string{DefaultCSAFProvider}
	}
	for _, p := range providers {
		if err := CheckProviderURL(p); err != nil {
			return nil, err
		}
	}
	c := &CSAF{store: store, providers: providers, interval: cfg.Interval, base: cfg.Client, now: cfg.Now}
	if c.interval <= 0 {
		c.interval = DefaultCSAFInterval
	}
	if c.base == nil {
		c.base = egress.NewClient(cfg.Egress)
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// CheckProviderURL refuses a provider URL that is no absolute https URL
// with a host.
func CheckProviderURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("%w: %s", ErrNotHTTPS, redact(raw))
	}
	return nil
}

// Schedule is the system job vulnfeed.csaf.
func (c *CSAF) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: CSAFKind, Spec: "@every " + c.interval.String(), Run: c.Run, MaxCatchUp: 1, Timeout: time.Hour}
}

// Run imports every configured provider; it returns the errors of the
// providers that failed (the others are imported).
func (c *CSAF) Run(ctx context.Context) error {
	var errs []error
	for _, entry := range c.providers {
		if err := c.runEntry(ctx, entry); err != nil {
			countSyncError(KindCSAF)
			slog.Warn("csaf import failed", "provider", redact(entry), "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runEntry imports a provider-metadata.json, or the providers of an
// aggregator.json that are mirrored on its host.
func (c *CSAF) runEntry(ctx context.Context, entry string) error {
	client := restrictHosts(c.base, []string{hostOf(entry)})
	f, err := fetch(ctx, client, entry, "", csafMaxMetadata, nil)
	if err != nil {
		return err
	}
	var agg struct {
		Aggregator    json.RawMessage `json:"aggregator"`
		CSAFProviders []struct {
			Metadata struct {
				URL string `json:"url"`
			} `json:"metadata"`
			Mirrors []string `json:"mirrors"`
		} `json:"csaf_providers"`
	}
	if json.Unmarshal(f.body, &agg) == nil && len(agg.Aggregator) > 0 {
		var errs []error
		for _, p := range agg.CSAFProviders {
			target := ""
			for _, m := range append([]string{p.Metadata.URL}, p.Mirrors...) {
				if hostOf(m) == hostOf(entry) {
					target = m
				}
			}
			if target == "" {
				slog.Info("csaf provider of the aggregator skipped: not on the host of the aggregator",
					"aggregator", redact(entry), "provider", redact(p.Metadata.URL))
				continue
			}
			pf, perr := fetch(ctx, client, target, "", csafMaxMetadata, nil)
			if perr == nil {
				perr = c.runProvider(ctx, client, target, pf.body)
			}
			if perr != nil {
				errs = append(errs, perr)
			}
		}
		return errors.Join(errs...)
	}
	return c.runProvider(ctx, client, entry, f.body)
}

// providerMetadata is the part of provider-metadata.json the import reads.
type providerMetadata struct {
	Distributions []struct {
		DirectoryURL string `json:"directory_url"`
		Rolie        *struct {
			Feeds []struct {
				URL string `json:"url"`
			} `json:"feeds"`
		} `json:"rolie"`
	} `json:"distributions"`
	PublicOpenPGPKeys []struct {
		Fingerprint string `json:"fingerprint"`
		URL         string `json:"url"`
	} `json:"public_openpgp_keys"`
	Publisher struct {
		Namespace string `json:"namespace"`
	} `json:"publisher"`
}

// csafEntry is one document of a distribution with its time.
type csafEntry struct {
	url string
	at  time.Time
}

// runProvider imports the documents of one provider after its cursor.
func (c *CSAF) runProvider(ctx context.Context, client *http.Client, metadataURL string, body []byte) error {
	var meta providerMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return fmt.Errorf("vulnfeed: provider metadata %s: %w", redact(metadataURL), err)
	}
	src, err := loadSource(ctx, c.store, KindCSAF+":"+metadataURL, KindCSAF, metadataURL)
	if err != nil {
		return err
	}
	keys, err := c.keyring(ctx, client, &meta)
	if err != nil {
		return c.fail(ctx, src, err)
	}
	etags := map[string]string{}
	if src.ETag != "" {
		_ = json.Unmarshal([]byte(src.ETag), &etags)
	}
	var entries []csafEntry
	for _, d := range meta.Distributions {
		switch {
		case d.Rolie != nil:
			for _, feed := range d.Rolie.Feeds {
				list, ferr := c.rolie(ctx, client, feed.URL, etags)
				if skipListing(feed.URL, ferr) {
					continue
				}
				if ferr != nil {
					return c.fail(ctx, src, ferr)
				}
				entries = append(entries, list...)
			}
		case d.DirectoryURL != "":
			list, ferr := c.changes(ctx, client, d.DirectoryURL, etags)
			if skipListing(d.DirectoryURL, ferr) {
				continue
			}
			if ferr != nil {
				return c.fail(ctx, src, ferr)
			}
			entries = append(entries, list...)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	var start time.Time
	if src.Cursor != "" {
		start, _ = time.Parse(time.RFC3339Nano, src.Cursor)
	}
	var runErr error
	for i, e := range entries {
		if !e.at.After(start) {
			countDocument(KindCSAF, ResultUnchanged)
			continue
		}
		result, derr := c.document(ctx, client, src.ID, e.url, keys, meta.Publisher.Namespace)
		if derr != nil {
			// A transport failure: the entry is repeated in the next run.
			runErr = derr
			break
		}
		countDocument(KindCSAF, result)
		if result == ResultImported {
			src.DocumentsTotal++
		} else {
			src.ErrorsTotal++
		}
		// The cursor passes a time only when every entry of that time is
		// done: changes.csv often gives several documents the same second,
		// and the next run skips every entry up to the cursor.
		if i+1 == len(entries) || entries[i+1].at.After(e.at) {
			src.Cursor = e.at.UTC().Format(time.RFC3339Nano)
		}
	}
	// After a break the ETags of the lists stay as they were: with the new
	// ones the next run would get 304 and never see the entries behind the
	// break.
	if b, merr := json.Marshal(etags); merr == nil && runErr == nil {
		src.ETag = string(b)
	}
	if runErr != nil {
		return c.fail(ctx, src, runErr)
	}
	now := c.now().UTC()
	src.SyncedAt, src.LastError = &now, ""
	if err := c.store.UpdateSource(ctx, src); err != nil {
		return err
	}
	syncAge.synced(KindCSAF, src.ID, now)
	return nil
}

// fail stores the error of a run in the source (without the cursor of a
// document that was not processed) and returns it.
func (c *CSAF) fail(ctx context.Context, src *Source, err error) error {
	src.LastError = truncate(err.Error(), 500)
	src.ErrorsTotal++
	if uerr := c.store.UpdateSource(ctx, src); uerr != nil {
		return errors.Join(err, uerr)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// keyring fetches the keys of public_openpgp_keys and keeps those whose
// fingerprint matches the metadata.
func (c *CSAF) keyring(ctx context.Context, client *http.Client, meta *providerMetadata) (openpgp.EntityList, error) {
	var ring openpgp.EntityList
	for _, k := range meta.PublicOpenPGPKeys {
		want := strings.ToUpper(strings.ReplaceAll(k.Fingerprint, " ", ""))
		f, err := fetch(ctx, client, k.URL, "", csafMaxMetadata, nil)
		if err != nil {
			return nil, err
		}
		keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(f.body))
		if err != nil {
			slog.Warn("csaf provider key unreadable", "url", redact(k.URL), "error", err)
			continue
		}
		for _, e := range keys {
			if strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint)) == want {
				ring = append(ring, e)
			} else {
				slog.Warn("csaf provider key does not match its fingerprint", "url", redact(k.URL))
			}
		}
	}
	if len(ring) == 0 {
		return nil, errors.New("vulnfeed: no OpenPGP key of the provider matches its fingerprint")
	}
	return ring, nil
}

// rolie lists the documents of a ROLIE feed (conditional on its ETag).
func (c *CSAF) rolie(ctx context.Context, client *http.Client, feedURL string, etags map[string]string) ([]csafEntry, error) {
	f, err := fetch(ctx, client, feedURL, etags[feedURL], csafMaxListing, nil)
	if err != nil || f.notModified {
		return nil, err
	}
	var feed struct {
		Feed struct {
			Entry []struct {
				Updated string `json:"updated"`
				Link    []struct {
					Rel  string `json:"rel"`
					Href string `json:"href"`
				} `json:"link"`
				Content struct {
					Src string `json:"src"`
				} `json:"content"`
			} `json:"entry"`
		} `json:"feed"`
	}
	if err := json.Unmarshal(f.body, &feed); err != nil {
		return nil, fmt.Errorf("vulnfeed: ROLIE feed %s: %w", redact(feedURL), err)
	}
	out := make([]csafEntry, 0, len(feed.Feed.Entry))
	for _, e := range feed.Feed.Entry {
		doc := e.Content.Src
		for _, l := range e.Link {
			if l.Rel == "self" {
				doc = l.Href
			}
		}
		at, perr := time.Parse(time.RFC3339, e.Updated)
		if doc == "" || perr != nil {
			continue
		}
		out = append(out, csafEntry{url: resolveRef(feedURL, doc), at: at})
	}
	etags[feedURL] = f.etag
	return out, nil
}

// changes lists the documents of a directory distribution from its
// changes.csv ("path","time"), conditional on its ETag.
func (c *CSAF) changes(ctx context.Context, client *http.Client, dirURL string, etags map[string]string) ([]csafEntry, error) {
	if !strings.HasSuffix(dirURL, "/") {
		dirURL += "/"
	}
	listURL := dirURL + "changes.csv"
	f, err := fetch(ctx, client, listURL, etags[listURL], csafMaxListing, nil)
	if err != nil || f.notModified {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(f.body))
	r.FieldsPerRecord = -1
	var out []csafEntry
	for {
		rec, rerr := r.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("vulnfeed: changes.csv of %s: %w", redact(dirURL), rerr)
		}
		if len(rec) < 2 {
			continue
		}
		at, perr := time.Parse(time.RFC3339, strings.TrimSpace(rec[1]))
		if perr != nil {
			continue
		}
		out = append(out, csafEntry{url: resolveRef(dirURL, strings.TrimSpace(rec[0])), at: at})
	}
	etags[listURL] = f.etag
	return out, nil
}

// resolveRef resolves ref against base.
func resolveRef(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// document fetches, checks and imports one document. It returns the
// result; an error is a transport failure (the document is tried again).
func (c *CSAF) document(ctx context.Context, client *http.Client, sourceID, docURL string, keys openpgp.EntityList,
	namespace string) (string, error) {
	f, err := fetch(ctx, client, docURL, "", csafMaxDocument, nil)
	var status *StatusError
	switch {
	case errors.As(err, &status) && status.Status < 500:
		slog.Warn("csaf document not available", "url", redact(docURL), "status", status.Status)
		return ResultParseError, nil
	case refusedForGood(err):
		// Another host, an invalid URL or a document above the limit fails
		// the same way in every run: refused, so the run goes on.
		slog.Warn("csaf document refused", "url", redact(docURL), "error", err)
		return ResultParseError, nil
	case err != nil:
		return "", err
	}
	if ok, herr := c.checkHash(ctx, client, docURL, f.body); refusedForGood(herr) {
		slog.Warn("csaf document refused: hash file not readable", "url", redact(docURL), "error", herr)
		return ResultInvalidHash, nil
	} else if herr != nil {
		return "", herr
	} else if !ok {
		slog.Warn("csaf document refused: hash mismatch or missing", "url", redact(docURL))
		return ResultInvalidHash, nil
	}
	sig, err := fetch(ctx, client, docURL+".asc", "", csafMaxSidecar, nil)
	switch {
	case errors.As(err, &status) && status.Status < 500, refusedForGood(err):
		slog.Warn("csaf document refused: signature missing", "url", redact(docURL))
		return ResultInvalidSignature, nil
	case err != nil:
		return "", err
	}
	if _, verr := openpgp.CheckArmoredDetachedSignature(keys, bytes.NewReader(f.body), bytes.NewReader(sig.body), nil); verr != nil {
		slog.Warn("csaf document refused: invalid signature", "url", redact(docURL))
		return ResultInvalidSignature, nil
	}
	adv, affected, err := MapCSAF(f.body, sourceID, docURL, namespace)
	if err != nil {
		slog.Warn("csaf document refused: not a CSAF 2.0 advisory", "url", redact(docURL), "error", err)
		return ResultParseError, nil
	}
	if err := c.store.ReplaceAdvisory(ctx, adv, affected); err != nil {
		if errors.Is(err, ErrAdvisoryOwnedByOtherSource) || errors.Is(err, ErrInvalidAdvisory) || isDataError(err) {
			slog.Warn("csaf document refused", "url", redact(docURL), "error", err)
			return ResultParseError, nil
		}
		return "", err
	}
	return ResultImported, nil
}

// skipListing reports a distribution the import leaves out: one that
// answers 4xx. Providers list feeds of every TLP level, and the TLP:GREEN
// and TLP:AMBER feeds need an account (the BSI answers 404); the public
// feeds of the provider are imported all the same.
func skipListing(listURL string, err error) bool {
	var status *StatusError
	if errors.As(err, &status) && status.Status < 500 {
		slog.Info("csaf distribution not available; skipped", "url", redact(listURL), "status", status.Status)
		return true
	}
	return false
}

// refusedForGood reports an error of a request that repeats in every run:
// a URL outside the hosts of the feed (also by redirect), an invalid URL or
// an answer above the size limit.
func refusedForGood(err error) bool {
	return errors.Is(err, ErrForeignHost) || errors.Is(err, ErrInvalidURL) || errors.Is(err, ErrTooLarge)
}

// isDataError reports a refusal of the database for the values of an
// advisory (SQLSTATE class 22 data exception or 23 integrity constraint,
// e.g. a CHECK): the same document fails in every run.
func isDataError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23"))
}

// checkHash compares the document with its .sha512 (else .sha256) file;
// a document without either is refused.
func (c *CSAF) checkHash(ctx context.Context, client *http.Client, docURL string, body []byte) (bool, error) {
	for _, alg := range []struct {
		ext string
		new func() hash.Hash
	}{{".sha512", sha512.New}, {".sha256", sha256.New}} {
		f, err := fetch(ctx, client, docURL+alg.ext, "", csafMaxSidecar, nil)
		var status *StatusError
		if errors.As(err, &status) && status.Status < 500 {
			continue
		}
		if err != nil {
			return false, err
		}
		fields := strings.Fields(string(f.body))
		if len(fields) == 0 {
			return false, nil
		}
		h := alg.new()
		_, _ = h.Write(body)
		return strings.EqualFold(fields[0], hex.EncodeToString(h.Sum(nil))), nil
	}
	return false, nil
}

// csafDocument is the part of a CSAF 2.0 document the import maps.
type csafDocument struct {
	Document struct {
		Category          string `json:"category"`
		CSAFVersion       string `json:"csaf_version"`
		Title             string `json:"title"`
		AggregateSeverity *struct {
			Text string `json:"text"`
		} `json:"aggregate_severity"`
		Publisher struct {
			Namespace string `json:"namespace"`
		} `json:"publisher"`
		Tracking struct {
			ID                 string `json:"id"`
			Status             string `json:"status"`
			InitialReleaseDate string `json:"initial_release_date"`
			CurrentReleaseDate string `json:"current_release_date"`
			RevisionHistory    []struct {
				Summary string `json:"summary"`
			} `json:"revision_history"`
		} `json:"tracking"`
	} `json:"document"`
	ProductTree *struct {
		Branches         []csafBranch  `json:"branches"`
		FullProductNames []csafProduct `json:"full_product_names"`
	} `json:"product_tree"`
	Vulnerabilities []struct {
		CVE    string `json:"cve"`
		Scores []struct {
			CVSSV3 *struct {
				BaseScore    float64 `json:"baseScore"`
				VectorString string  `json:"vectorString"`
			} `json:"cvss_v3"`
		} `json:"scores"`
		ProductStatus struct {
			KnownAffected []string `json:"known_affected"`
			FirstAffected []string `json:"first_affected"`
			LastAffected  []string `json:"last_affected"`
			Fixed         []string `json:"fixed"`
			FirstFixed    []string `json:"first_fixed"`
		} `json:"product_status"`
		Remediations []struct {
			Category   string   `json:"category"`
			ProductIDs []string `json:"product_ids"`
		} `json:"remediations"`
	} `json:"vulnerabilities"`
}

type csafBranch struct {
	Category string       `json:"category"`
	Name     string       `json:"name"`
	Branches []csafBranch `json:"branches"`
	Product  *csafProduct `json:"product"`
}

type csafProduct struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Helper    *struct {
		CPE string `json:"cpe"`
	} `json:"product_identification_helper"`
}

// csafProductInfo is a product of the product tree with its path.
type csafProductInfo struct {
	vendor, product, version, versRange, cpe string
}

// germanSeverity maps the aggregate severity texts of the BSI.
var aggregateSeverity = map[string]string{
	"low": "low", "medium": "medium", "moderate": "medium", "high": "high", "important": "high", "critical": "critical",
	"niedrig": "low", "mittel": "medium", "hoch": "high", "kritisch": "critical",
}

// MapCSAF maps a CSAF 2.0 document to an advisory of sourceID with its
// affected ranges (WP-1118 AK3): product_tree (vendor → product_name →
// product_version or product_version_range with a vers: expression) and
// the affected product status → ranges; vulnerabilities[].cve → CVE ids;
// the highest CVSS v3 base score with its vector → score, vector and
// severity, else aggregate_severity; remediations of category vendor_fix
// with a fixed product of the same vendor and product → fixed_version. A
// document whose revision history names withdrawn (or of category
// csaf_withdrawn) is withdrawn; interim documents are imported.
func MapCSAF(data []byte, sourceID, docURL, providerNamespace string) (*Advisory, []Affected, error) {
	var doc csafDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("vulnfeed: decode CSAF: %w", err)
	}
	d := &doc.Document
	if d.Tracking.ID == "" || (d.CSAFVersion != "" && !strings.HasPrefix(d.CSAFVersion, "2.")) {
		return nil, nil, errors.New("vulnfeed: document without tracking id or not CSAF 2.x")
	}
	namespace := d.Publisher.Namespace
	if namespace == "" {
		namespace = providerNamespace
	}
	publisher := hostOf(namespace)
	if publisher == "" {
		publisher = strings.ToLower(strings.TrimSpace(namespace))
	}
	adv := &Advisory{ID: "csaf:" + publisher + ":" + d.Tracking.ID, SourceID: sourceID, Title: d.Title, URL: docURL}
	if t, err := time.Parse(time.RFC3339, d.Tracking.InitialReleaseDate); err == nil {
		adv.PublishedAt = &t
	}
	if t, err := time.Parse(time.RFC3339, d.Tracking.CurrentReleaseDate); err == nil {
		adv.ModifiedAt = &t
	}
	adv.Withdrawn = d.Category == "csaf_withdrawn" || strings.EqualFold(d.Tracking.Status, "withdrawn")
	for _, rev := range d.Tracking.RevisionHistory {
		if strings.Contains(strings.ToLower(rev.Summary), "withdrawn") {
			adv.Withdrawn = true
		}
	}

	products := map[string]csafProductInfo{}
	if doc.ProductTree != nil {
		walkBranches(doc.ProductTree.Branches, &csafProductInfo{}, products)
		for _, p := range doc.ProductTree.FullProductNames {
			if _, ok := products[p.ProductID]; !ok {
				info := csafProductInfo{product: p.Name}
				if p.Helper != nil {
					info.cpe = p.Helper.CPE
				}
				products[p.ProductID] = info
			}
		}
	}

	seenCVE := map[string]bool{}
	affectedIDs := map[string]bool{}
	fixedIDs := map[string]bool{}
	best := -1.0
	for i := range doc.Vulnerabilities {
		v := &doc.Vulnerabilities[i]
		if cve := strings.ToUpper(strings.TrimSpace(v.CVE)); cve != "" && !seenCVE[cve] {
			seenCVE[cve] = true
			adv.CVEIDs = append(adv.CVEIDs, cve)
		}
		for _, s := range v.Scores {
			if s.CVSSV3 != nil && s.CVSSV3.BaseScore >= 0 && s.CVSSV3.BaseScore <= 10 && s.CVSSV3.BaseScore > best {
				best = s.CVSSV3.BaseScore
				score := s.CVSSV3.BaseScore
				adv.CVSSScore, adv.CVSSVector = &score, s.CVSSV3.VectorString
			}
		}
		for _, ids := range [][]string{v.ProductStatus.KnownAffected, v.ProductStatus.FirstAffected, v.ProductStatus.LastAffected} {
			for _, id := range ids {
				affectedIDs[id] = true
			}
		}
		fixed := map[string]bool{}
		for _, id := range append(append([]string{}, v.ProductStatus.Fixed...), v.ProductStatus.FirstFixed...) {
			fixed[id] = true
		}
		for _, r := range v.Remediations {
			if r.Category != "vendor_fix" {
				continue
			}
			for _, id := range r.ProductIDs {
				if fixed[id] {
					fixedIDs[id] = true
				}
			}
		}
	}
	switch {
	case adv.CVSSScore != nil:
		adv.Severity = SeverityFromScore(*adv.CVSSScore)
	case d.AggregateSeverity != nil:
		adv.Severity = aggregateSeverity[strings.ToLower(strings.TrimSpace(d.AggregateSeverity.Text))]
	}

	// The fixed version per vendor and product: the lowest exact version
	// of a fixed product with a vendor fix.
	fixedVersion := map[[2]string]string{}
	fixedKeys := make([]string, 0, len(fixedIDs))
	for id := range fixedIDs {
		fixedKeys = append(fixedKeys, id)
	}
	sort.Strings(fixedKeys)
	for _, id := range fixedKeys {
		p, ok := products[id]
		if !ok || p.version == "" {
			continue
		}
		key := [2]string{Normalize(p.vendor), Normalize(p.product)}
		if cur, has := fixedVersion[key]; !has || CompareVersions(p.version, cur) < 0 {
			fixedVersion[key] = p.version
		}
	}

	ids := make([]string, 0, len(affectedIDs))
	for id := range affectedIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var affected []Affected
	for _, id := range ids {
		p, ok := products[id]
		if !ok || Normalize(p.vendor) == "" || Normalize(p.product) == "" {
			continue
		}
		base := Affected{Vendor: p.vendor, Product: p.product, CPE: p.cpe,
			FixedVersion: fixedVersion[[2]string{Normalize(p.vendor), Normalize(p.product)}]}
		switch {
		case p.versRange != "":
			ranges, err := ParseVers(p.versRange)
			if err != nil {
				continue // a range the import cannot read: no statement
			}
			for _, r := range ranges {
				a := base
				a.VersionScheme, a.VersionExact = r.Scheme, r.Exact
				a.VersionStart, a.VersionStartInclusive = r.Start, r.StartInclusive
				a.VersionEnd, a.VersionEndInclusive = r.End, r.EndInclusive
				affected = append(affected, a)
			}
		case p.version != "":
			a := base
			a.VersionExact = p.version
			affected = append(affected, a)
		default:
			affected = append(affected, base) // every version
		}
	}
	return adv, affected, nil
}

// CompareVersions compares two version strings segment by segment: the
// strings are split at '.', '-', '_' and '+' and between digits and
// letters; numeric segments compare as numbers (9.4 before 10.1), other
// segments as text, a text segment before a number (1.rc1 before 1.0). A
// version that is a prefix of the other is lower (1.0 before 1.0.1), unless
// the rest starts with text (1.0-rc1 before 1.0).
func CompareVersions(a, b string) int {
	as, bs := versionSegments(a), versionSegments(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareSegment(as[i], bs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(as) > len(bs) && !isDigits(as[len(bs)]):
		return -1 // 1.0-rc1 before 1.0
	case len(bs) > len(as) && !isDigits(bs[len(as)]):
		return 1
	}
	return cmp.Compare(len(as), len(bs))
}

func versionSegments(v string) []string {
	var out []string
	cur := []rune{}
	digit := false
	for _, r := range strings.ToLower(strings.TrimSpace(v)) {
		switch {
		case r == '.' || r == '-' || r == '_' || r == '+':
			if len(cur) > 0 {
				out, cur = append(out, string(cur)), cur[:0]
			}
			continue
		case len(cur) > 0 && unicode.IsDigit(r) != digit:
			out, cur = append(out, string(cur)), cur[:0]
		}
		digit = unicode.IsDigit(r)
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func compareSegment(a, b string) int {
	an, bn := isDigits(a), isDigits(b)
	switch {
	case an && bn:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if c := cmp.Compare(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return 1 // a number after a pre-release text (1.0 after 1.rc)
	case bn:
		return -1
	}
	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// walkBranches collects the products of the branches with their vendor,
// product name and version.
func walkBranches(branches []csafBranch, path *csafProductInfo, out map[string]csafProductInfo) {
	for i := range branches {
		b := &branches[i]
		p := *path
		switch b.Category {
		case "vendor":
			p.vendor = b.Name
		case "product_name":
			p.product = b.Name
		case "product_version":
			p.version = b.Name
		case "product_version_range":
			p.versRange = b.Name
		}
		if b.Product != nil && b.Product.ProductID != "" {
			info := p
			if info.product == "" {
				info.product = b.Product.Name
			}
			if b.Product.Helper != nil {
				info.cpe = b.Product.Helper.CPE
			}
			out[b.Product.ProductID] = info
		}
		walkBranches(b.Branches, &p, out)
	}
}

// VersRange is one range of a vers expression.
type VersRange struct {
	Scheme                       string
	Exact                        string
	Start, End                   string
	StartInclusive, EndInclusive bool
}

// ParseVers parses a vers expression (vers:<scheme>/<constraints>, the
// constraints separated by |: =v or v, >=v, >v, <=v, <v, != ignored, *
// every version) into ranges: a lower bound opens a range, an upper bound
// closes it, an exact version is a range of its own.
func ParseVers(expr string) ([]VersRange, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(expr), "vers:")
	if !ok {
		return nil, fmt.Errorf("vulnfeed: %q is no vers expression", expr)
	}
	scheme, constraints, ok := strings.Cut(rest, "/")
	if !ok || scheme == "" || constraints == "" {
		return nil, fmt.Errorf("vulnfeed: %q is no vers expression", expr)
	}
	scheme = strings.ToLower(scheme)
	if strings.TrimSpace(constraints) == "*" {
		return []VersRange{{Scheme: scheme}}, nil
	}
	var out []VersRange
	var cur *VersRange
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, c := range strings.Split(constraints, "|") {
		c = strings.TrimSpace(c)
		var op string
		for _, o := range []string{">=", "<=", "!=", ">", "<", "="} {
			if strings.HasPrefix(c, o) {
				op, c = o, strings.TrimSpace(c[len(o):])
				break
			}
		}
		if c == "" {
			return nil, fmt.Errorf("vulnfeed: %q has an empty constraint", expr)
		}
		v, err := url.PathUnescape(c)
		if err != nil {
			return nil, fmt.Errorf("vulnfeed: %q: %w", expr, err)
		}
		switch op {
		case ">=", ">":
			flush()
			cur = &VersRange{Scheme: scheme, Start: v, StartInclusive: op == ">="}
		case "<=", "<":
			if cur == nil {
				cur = &VersRange{Scheme: scheme}
			}
			cur.End, cur.EndInclusive = v, op == "<="
			flush()
		case "!=":
		default:
			flush()
			out = append(out, VersRange{Scheme: scheme, Exact: v})
		}
	}
	flush()
	if len(out) == 0 {
		return nil, fmt.Errorf("vulnfeed: %q has no range", expr)
	}
	return out, nil
}
