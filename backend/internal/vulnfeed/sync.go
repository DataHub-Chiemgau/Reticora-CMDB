package vulnfeed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// The feed imports (SEC-02, E-234 option 3): CSAF 2.0 (WP-1118), NVD 2.0
// and CISA KEV (WP-1119) write the catalog in the system context through
// Store. Every request leaves over the egress client
// (platform/egress, DNS pinning, no private targets) and only to the hosts
// of its feed (restrictHosts): a URL or redirect to another host, or one
// without HTTPS, is refused. The state of a feed (ETag, cursor, counters,
// last error) is kept in vuln_source.

// Store is the catalog the importers write; PGRepository implements it,
// the tests a memory fake.
type Store interface {
	ReplaceAdvisory(ctx context.Context, adv *Advisory, affected []Affected) error
	UpdateSource(ctx context.Context, src *Source) error
	// GetSource returns the stored state of a source; an unknown source is
	// an error that wraps pgx.ErrNoRows.
	GetSource(ctx context.Context, id string) (*Source, error)
}

// Results of a document (reticora_vulnfeed_documents_total{result}).
const (
	ResultImported         = "imported"
	ResultUnchanged        = "unchanged"
	ResultInvalidSignature = "invalid_signature"
	ResultInvalidHash      = "invalid_hash"
	ResultParseError       = "parse_error"
)

// Metrics of the feed imports (WP-1118 AK5): documents per source and
// result, failed runs per source and the age of the last sync per source.
var (
	documentsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_vulnfeed_documents_total",
		Help: "Vulnerability feed documents by source (csaf, nvd, kev) and result.",
	}, []string{"source", "result"})
	syncErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_vulnfeed_sync_errors_total",
		Help: "Vulnerability feed syncs that ended with an error, by source.",
	}, []string{"source"})
	syncAge = newAgeCollector()
)

// RegisterMetrics registers the feed metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(documentsTotal, syncErrorsTotal, syncAge)
}

// ageCollector reports reticora_vulnfeed_age_seconds{source}: the seconds
// since the oldest synced_at of the source kind, computed at the scrape.
type ageCollector struct {
	desc *prometheus.Desc
	mu   sync.Mutex
	at   map[string]map[string]time.Time // kind → source id → synced_at
	now  func() time.Time
}

func newAgeCollector() *ageCollector {
	return &ageCollector{
		desc: prometheus.NewDesc("reticora_vulnfeed_age_seconds",
			"Seconds since the last successful sync of a vulnerability feed (the oldest of its sources).", []string{"source"}, nil),
		at:  map[string]map[string]time.Time{},
		now: time.Now,
	}
}

// Describe implements prometheus.Collector.
func (c *ageCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

// Collect implements prometheus.Collector.
func (c *ageCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for kind, sources := range c.at {
		var oldest time.Time
		for _, t := range sources {
			if oldest.IsZero() || t.Before(oldest) {
				oldest = t
			}
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, now.Sub(oldest).Seconds(), kind)
	}
}

// synced records the synced_at of a source.
func (c *ageCollector) synced(kind, id string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at[kind] == nil {
		c.at[kind] = map[string]time.Time{}
	}
	c.at[kind][id] = at
}

// age returns the age of the source kind at now (tests).
func (c *ageCollector) age(kind string, now time.Time) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var oldest time.Time
	for _, t := range c.at[kind] {
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	return now.Sub(oldest), !oldest.IsZero()
}

// countDocument counts one document of a source kind.
func countDocument(kind, result string) { documentsTotal.WithLabelValues(kind, result).Inc() }

// countSyncError counts a failed sync of a source kind.
func countSyncError(kind string) { syncErrorsTotal.WithLabelValues(kind).Inc() }

// loadSource returns the stored state of a source. An unknown source is
// stored at once: the advisories of the run refer to it (foreign key
// vuln_advisory.source_id), so it must exist before the first document.
func loadSource(ctx context.Context, store Store, id, kind, rawURL string) (*Source, error) {
	src, err := store.GetSource(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		src = &Source{ID: id, Kind: kind, URL: rawURL}
		if uerr := store.UpdateSource(ctx, src); uerr != nil {
			return nil, uerr
		}
		return src, nil
	case err != nil:
		return nil, err
	}
	src.Kind, src.URL = kind, rawURL
	return src, nil
}

// ErrForeignHost refuses a request or redirect to a host outside the feed,
// or one without HTTPS.
var ErrForeignHost = errors.New("vulnfeed: URL outside the hosts of the feed")

// ErrInvalidURL refuses a URL that cannot be requested.
var ErrInvalidURL = errors.New("vulnfeed: invalid URL")

// ErrTooLarge refuses an answer above the size limit of its file.
var ErrTooLarge = errors.New("vulnfeed: answer too large")

// hostTransport refuses every request outside its hosts.
type hostTransport struct {
	next  http.RoundTripper
	hosts map[string]bool
}

func (t *hostTransport) allowed(u *url.URL) bool {
	return u != nil && strings.EqualFold(u.Scheme, "https") && t.hosts[strings.ToLower(u.Hostname())]
}

// RoundTrip implements http.RoundTripper.
func (t *hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.allowed(req.URL) {
		return nil, fmt.Errorf("%w: %s", ErrForeignHost, redact(req.URL.String()))
	}
	return t.next.RoundTrip(req)
}

// restrictHosts returns a copy of base that sends requests and follows
// redirects only over HTTPS to hosts (WP-1118 AK4, WP-1119 AK6).
func restrictHosts(base *http.Client, hosts []string) *http.Client {
	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	t := &hostTransport{next: next, hosts: map[string]bool{}}
	for _, h := range hosts {
		t.hosts[strings.ToLower(h)] = true
	}
	c := *base
	c.Transport = t
	check := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !t.allowed(req.URL) {
			return fmt.Errorf("%w: redirect to %s", ErrForeignHost, redact(req.URL.String()))
		}
		if check != nil {
			return check(req, via)
		}
		if len(via) >= 10 {
			return errors.New("vulnfeed: too many redirects")
		}
		return nil
	}
	return &c
}

// hostOf returns the lower-case host of a URL ("" when it has none).
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// StatusError is an HTTP answer other than 200 (and 304 for a conditional
// request).
type StatusError struct {
	URL    string
	Status int
}

func (e *StatusError) Error() string { return fmt.Sprintf("vulnfeed: %s answered %d", e.URL, e.Status) }

// fetched is the answer of fetch.
type fetched struct {
	body        []byte
	etag        string
	notModified bool
}

// fetch GETs rawURL with the ETag of the last answer (If-None-Match) and
// at most maxBytes of body; headers are added to the request (e.g. the NVD
// API key, never logged).
func fetch(ctx context.Context, client *http.Client, rawURL, etag string, maxBytes int64, headers http.Header) (fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return fetched{}, fmt.Errorf("%w: %s", ErrInvalidURL, redact(rawURL))
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	req.Header.Set("User-Agent", "Reticora-CMDB vulnerability feed")
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error carries the full URL (with the query, which may hold a
		// token); the error goes to logs and vuln_source.last_error.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fetched{}, fmt.Errorf("vulnfeed: get %s: %w", redact(rawURL), err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return fetched{etag: etag, notModified: true}, nil
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fetched{}, &StatusError{URL: redact(rawURL), Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return fetched{}, fmt.Errorf("vulnfeed: read %s: %w", redact(rawURL), err)
	}
	if int64(len(body)) > maxBytes {
		return fetched{}, fmt.Errorf("%w: %s is larger than %d bytes", ErrTooLarge, redact(rawURL), maxBytes)
	}
	return fetched{body: body, etag: resp.Header.Get("ETag")}, nil
}

// redact drops the query of a URL for logs and errors.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid URL"
	}
	u.RawQuery, u.User = "", nil
	return u.String()
}

// SeverityFromScore maps a CVSS base score to the severity of the catalog
// (CVSS v3: 0.1-3.9 low, 4.0-6.9 medium, 7.0-8.9 high, 9.0-10 critical;
// 0 or less none).
func SeverityFromScore(score float64) string {
	switch {
	case score >= 9:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 4:
		return "medium"
	case score > 0:
		return "low"
	}
	return ""
}
