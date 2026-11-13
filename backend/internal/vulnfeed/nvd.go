package vulnfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// NVD 2.0 import (SEC-02, WP-1119, E-234 option 3): the CVEs of the NVD
// API 2.0 for vendors without CSAF, incrementally by lastModified in
// windows of at most 120 days after the cursor of vuln_source('nvd'),
// paged with startIndex/resultsPerPage (2,000). The client keeps the rate
// limit of the NVD (5 requests per 30 s without API key, 50 with); 403 and
// 503 answers back off, and a run that gives up resumes in the next run
// from the last complete window. A range (vendor, product) that a CSAF
// advisory of the same CVE covers is not written (CSAF first); ranges
// covered later are removed after every run. The KEV catalog follows in
// the same job once per 24 h (kev.go).

// NVDKind is the system job of the NVD and KEV import.
const NVDKind = "vulnfeed.nvd"

// Defaults of the NVD import.
const (
	DefaultNVDInterval = 2 * time.Hour
	// DefaultNVDURL is the CVE API 2.0 of the NVD.
	DefaultNVDURL = "https://services.nvd.nist.gov/rest/json/cves/2.0"
	// NVDMaxWindow is the longest lastModified window the API accepts.
	NVDMaxWindow = 120 * 24 * time.Hour
	// NVDPageSize is resultsPerPage.
	NVDPageSize = 2000
	// nvdRateWindow is the window of the rate limit.
	nvdRateWindow = 30 * time.Second
	// nvdRateWithoutKey and nvdRateWithKey are the requests per window.
	nvdRateWithoutKey = 5
	nvdRateWithKey    = 50
	// nvdAttempts is how often a request is tried on 403 or 503 in a run.
	nvdAttempts = 3
	// nvdBackoff is the first wait after a 403 or 503 (doubled).
	nvdBackoff = 6 * time.Second
	nvdMaxPage = 128 << 20
)

// nvdEpoch is the start of the first sync: before it the NVD holds no
// change.
var nvdEpoch = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrNVDDisabled: the NVD import does not run in the air-gapped profile
// (only the offline bundle, WP-1120).
var ErrNVDDisabled = errors.New("vulnfeed: NVD import off in the air-gapped profile")

// NVDStore is the catalog of the NVD import.
type NVDStore interface {
	Store
	// CSAFCovered returns per CVE the (vendor, product) pairs a CSAF
	// advisory of that CVE has ranges for (normalized).
	CSAFCovered(ctx context.Context, cves []string) (map[string]map[[2]string]bool, error)
	// RemoveCoveredNVDRanges deletes the NVD ranges a CSAF advisory of
	// the same CVE covers and returns their number.
	RemoveCoveredNVDRanges(ctx context.Context) (int64, error)
	// SetKEV sets kev for the advisories with a CVE of cves and clears it
	// for the others; it returns the number of changed advisories.
	SetKEV(ctx context.Context, cves []string) (int64, error)
}

// NVDConfig configures the NVD and KEV import.
type NVDConfig struct {
	// APIKey is RETICORA_VULNFEED_NVD_API_KEY (deployment secret of the
	// installation); it goes only into the header apiKey.
	APIKey string
	// Interval is the schedule (DefaultNVDInterval when 0).
	Interval time.Duration
	// AirGapped is RETICORA_AIR_GAPPED.
	AirGapped bool
	// URL replaces DefaultNVDURL (tests).
	URL string
	// KEVURL is RETICORA_VULNFEED_KEV_URL (DefaultKEVURL when empty).
	KEVURL string
	// Egress are the options of the egress client.
	Egress egress.Options
	// Client replaces the egress client (tests); the host restriction
	// applies to it as well.
	Client *http.Client
	// Now and Sleep are the clock (tests).
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// NVD is the NVD import with the KEV sync.
type NVD struct {
	store    NVDStore
	apiKey   string
	interval time.Duration
	url      string
	client   *http.Client
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	limit    *rateLimiter
	kev      *KEV
}

// NewNVD returns the NVD import of cfg (ErrNVDDisabled when air-gapped).
func NewNVD(store NVDStore, cfg *NVDConfig) (*NVD, error) {
	if cfg.AirGapped {
		return nil, ErrNVDDisabled
	}
	n := &NVD{store: store, apiKey: strings.TrimSpace(cfg.APIKey), interval: cfg.Interval, url: cfg.URL, now: cfg.Now, sleep: cfg.Sleep}
	if n.interval <= 0 {
		n.interval = DefaultNVDInterval
	}
	if n.url == "" {
		n.url = DefaultNVDURL
	}
	if n.now == nil {
		n.now = time.Now
	}
	if n.sleep == nil {
		n.sleep = sleepCtx
	}
	kevURL := cfg.KEVURL
	if kevURL == "" {
		kevURL = DefaultKEVURL
	}
	for _, u := range []string{n.url, kevURL} {
		if err := CheckProviderURL(u); err != nil {
			return nil, err
		}
	}
	base := cfg.Client
	if base == nil {
		base = egress.NewClient(cfg.Egress)
	}
	n.client = restrictHosts(base, []string{hostOf(n.url), hostOf(kevURL)})
	rate := nvdRateWithoutKey
	if n.apiKey != "" {
		rate = nvdRateWithKey
	}
	n.limit = &rateLimiter{max: rate, window: nvdRateWindow, now: n.now, sleep: n.sleep}
	n.kev = &KEV{store: store, url: kevURL, client: n.client, now: n.now}
	return n, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Schedule is the system job vulnfeed.nvd (NVD, then KEV once per 24 h).
func (n *NVD) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: NVDKind, Spec: "@every " + n.interval.String(), Run: n.RunWithKEV, MaxCatchUp: 1, Timeout: 2 * time.Hour}
}

// RunWithKEV runs the NVD import and the KEV sync (when due).
func (n *NVD) RunWithKEV(ctx context.Context) error {
	return errors.Join(n.Run(ctx), n.kev.RunIfDue(ctx))
}

// rateLimiter allows at most max requests per window (sliding).
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	sent   []time.Time
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
}

// wait blocks until a request is allowed and records it.
func (l *rateLimiter) wait(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		now := l.now()
		for len(l.sent) > 0 && now.Sub(l.sent[0]) >= l.window {
			l.sent = l.sent[1:]
		}
		if len(l.sent) < l.max {
			l.sent = append(l.sent, now)
			return nil
		}
		if err := l.sleep(ctx, l.window-now.Sub(l.sent[0])); err != nil {
			return err
		}
	}
}

// nvdTimeFormat is the date format of lastModStartDate/lastModEndDate.
const nvdTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// Run imports the CVEs changed since the cursor in windows of at most
// 120 days; the cursor moves to the end of every complete window.
func (n *NVD) Run(ctx context.Context) error {
	src, err := loadSource(ctx, n.store, KindNVD, KindNVD, n.url)
	if err != nil {
		return err
	}
	start := nvdEpoch
	if src.Cursor != "" {
		if t, perr := time.Parse(time.RFC3339Nano, src.Cursor); perr == nil {
			start = t
		}
	}
	end := n.now().UTC()
	var runErr error
	for start.Before(end) {
		windowEnd := start.Add(NVDMaxWindow)
		if windowEnd.After(end) {
			windowEnd = end
		}
		imported, failed, werr := n.window(ctx, start, windowEnd)
		src.DocumentsTotal += imported
		src.ErrorsTotal += failed
		if werr != nil {
			runErr = werr
			break
		}
		start = windowEnd
		src.Cursor = start.Format(time.RFC3339Nano)
		// Stored at once: a run cut by its timeout, a shutdown or a crash
		// goes on after the last complete window, not from the start (the
		// first import covers some 80 windows since 1999).
		if uerr := n.store.UpdateSource(ctx, src); uerr != nil {
			runErr = uerr
			break
		}
	}
	if runErr == nil {
		if removed, rerr := n.store.RemoveCoveredNVDRanges(ctx); rerr != nil {
			runErr = rerr
		} else if removed > 0 {
			slog.Info("nvd ranges covered by csaf removed", "ranges", removed)
		}
	}
	if runErr != nil {
		countSyncError(KindNVD)
		src.ErrorsTotal++
		src.LastError = truncate(n.scrub(runErr.Error()), 500)
		slog.Warn("nvd import failed; resumed from the last complete window", "error", n.scrub(runErr.Error()))
		// Also after the timeout of the job or a cancellation: the error
		// state is stored with a short deadline of its own.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if uerr := n.store.UpdateSource(sctx, src); uerr != nil {
			return errors.Join(errors.New(n.scrub(runErr.Error())), uerr)
		}
		return errors.New(n.scrub(runErr.Error()))
	}
	now := n.now().UTC()
	src.SyncedAt, src.LastError = &now, ""
	if err := n.store.UpdateSource(ctx, src); err != nil {
		return err
	}
	syncAge.synced(KindNVD, src.ID, now)
	return nil
}

// scrub removes the API key from a text (it never reaches a log or an
// error).
func (n *NVD) scrub(s string) string {
	if n.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, n.apiKey, "[redacted]")
}

// nvdPage is one page of the CVE API 2.0.
type nvdPage struct {
	ResultsPerPage  int `json:"resultsPerPage"`
	StartIndex      int `json:"startIndex"`
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE nvdCVE `json:"cve"`
	} `json:"vulnerabilities"`
}

type nvdCVE struct {
	ID           string `json:"id"`
	Published    string `json:"published"`
	LastModified string `json:"lastModified"`
	VulnStatus   string `json:"vulnStatus"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Metrics struct {
		V31 []nvdMetric `json:"cvssMetricV31"`
		V30 []nvdMetric `json:"cvssMetricV30"`
	} `json:"metrics"`
	Configurations []struct {
		Nodes []struct {
			Negate   bool `json:"negate"`
			CPEMatch []struct {
				Vulnerable            bool   `json:"vulnerable"`
				Criteria              string `json:"criteria"`
				VersionStartIncluding string `json:"versionStartIncluding"`
				VersionStartExcluding string `json:"versionStartExcluding"`
				VersionEndIncluding   string `json:"versionEndIncluding"`
				VersionEndExcluding   string `json:"versionEndExcluding"`
			} `json:"cpeMatch"`
		} `json:"nodes"`
	} `json:"configurations"`
}

type nvdMetric struct {
	Type     string `json:"type"`
	CVSSData struct {
		BaseScore    float64 `json:"baseScore"`
		VectorString string  `json:"vectorString"`
	} `json:"cvssData"`
}

// window imports one lastModified window page by page.
func (n *NVD) window(ctx context.Context, start, end time.Time) (imported, failed int64, err error) {
	for index := 0; ; {
		page, perr := n.page(ctx, start, end, index)
		if perr != nil {
			return imported, failed, perr
		}
		cves := make([]string, 0, len(page.Vulnerabilities))
		for i := range page.Vulnerabilities {
			cves = append(cves, strings.ToUpper(page.Vulnerabilities[i].CVE.ID))
		}
		covered, cerr := n.store.CSAFCovered(ctx, cves)
		if cerr != nil {
			return imported, failed, cerr
		}
		for i := range page.Vulnerabilities {
			adv, affected := MapNVD(&page.Vulnerabilities[i].CVE)
			if adv == nil {
				countDocument(KindNVD, ResultParseError)
				failed++
				continue
			}
			kept := affected[:0]
			for j := range affected {
				if !covered[adv.CVEIDs[0]][[2]string{Normalize(affected[j].Vendor), Normalize(affected[j].Product)}] {
					kept = append(kept, affected[j])
				}
			}
			if werr := n.store.ReplaceAdvisory(ctx, adv, kept); werr != nil {
				// Like CSAF: a record the catalog refuses as data (the
				// memory store of the bundle export checks the same) is
				// skipped, not the end of the sync (WP-1120).
				if errors.Is(werr, ErrAdvisoryOwnedByOtherSource) || errors.Is(werr, ErrInvalidAdvisory) || isDataError(werr) {
					countDocument(KindNVD, ResultParseError)
					failed++
					continue
				}
				return imported, failed, werr
			}
			countDocument(KindNVD, ResultImported)
			imported++
		}
		index += len(page.Vulnerabilities)
		if len(page.Vulnerabilities) == 0 || index >= page.TotalResults {
			return imported, failed, nil
		}
	}
}

// page fetches one page within the rate limit; 403 and 503 back off and
// are tried again up to nvdAttempts times.
func (n *NVD) page(ctx context.Context, start, end time.Time, index int) (*nvdPage, error) {
	q := url.Values{}
	q.Set("lastModStartDate", start.UTC().Format(nvdTimeFormat))
	q.Set("lastModEndDate", end.UTC().Format(nvdTimeFormat))
	q.Set("startIndex", strconv.Itoa(index))
	q.Set("resultsPerPage", strconv.Itoa(NVDPageSize))
	target := n.url + "?" + q.Encode()
	var headers http.Header
	if n.apiKey != "" {
		headers = http.Header{"apiKey": []string{n.apiKey}}
	}
	backoff := nvdBackoff
	for attempt := 1; ; attempt++ {
		if err := n.limit.wait(ctx); err != nil {
			return nil, err
		}
		f, err := fetch(ctx, n.client, target, "", nvdMaxPage, headers)
		var status *StatusError
		if errors.As(err, &status) && (status.Status == http.StatusForbidden || status.Status == http.StatusServiceUnavailable) &&
			attempt < nvdAttempts {
			slog.Warn("nvd api refused the request; backing off", "status", status.Status, "wait", backoff.String())
			if serr := n.sleep(ctx, backoff); serr != nil {
				return nil, serr
			}
			backoff *= 2
			continue
		}
		if err != nil {
			return nil, err
		}
		var page nvdPage
		if err := json.Unmarshal(f.body, &page); err != nil {
			return nil, fmt.Errorf("vulnfeed: nvd page: %w", err)
		}
		return &page, nil
	}
}

// MapNVD maps a CVE of the NVD to the advisory nvd:<CVE-ID> with its
// ranges (WP-1119 AK3): cpeMatch with vulnerable = true → vendor and
// product from the criteria, ranges from versionStart/End
// Including/Excluding (an exact version of the criteria without range);
// CVSS v3.1, else v3.0 (the primary metric first) → score, vector and
// severity. A rejected CVE is withdrawn. nil for a CVE without id.
func MapNVD(c *nvdCVE) (*Advisory, []Affected) {
	id := strings.ToUpper(strings.TrimSpace(c.ID))
	if !strings.HasPrefix(id, "CVE-") {
		return nil, nil
	}
	adv := &Advisory{ID: "nvd:" + id, SourceID: KindNVD, CVEIDs: []string{id}, URL: "https://nvd.nist.gov/vuln/detail/" + id,
		Withdrawn: strings.EqualFold(c.VulnStatus, "Rejected")}
	for _, d := range c.Descriptions {
		if d.Lang == "en" {
			adv.Title = truncate(d.Value, 300)
			break
		}
	}
	for _, s := range []string{c.Published, c.LastModified} {
		if t, err := parseNVDTime(s); err == nil {
			t := t
			if adv.PublishedAt == nil {
				adv.PublishedAt = &t
			} else {
				adv.ModifiedAt = &t
			}
		}
	}
	for _, metrics := range [][]nvdMetric{c.Metrics.V31, c.Metrics.V30} {
		if m := primaryMetric(metrics); m != nil {
			score := m.CVSSData.BaseScore
			adv.CVSSScore, adv.CVSSVector, adv.Severity = &score, m.CVSSData.VectorString, SeverityFromScore(score)
			break
		}
	}
	var affected []Affected
	seen := map[Affected]bool{}
	for _, cfg := range c.Configurations {
		for _, node := range cfg.Nodes {
			if node.Negate {
				continue
			}
			for _, m := range node.CPEMatch {
				if !m.Vulnerable {
					continue
				}
				vendor, product, version, ok := parseCPE(m.Criteria)
				if !ok {
					continue
				}
				a := Affected{Vendor: vendor, Product: product, CPE: m.Criteria}
				switch {
				case m.VersionStartIncluding != "" || m.VersionStartExcluding != "" || m.VersionEndIncluding != "" || m.VersionEndExcluding != "":
					a.VersionStart, a.VersionStartInclusive = m.VersionStartIncluding, m.VersionStartIncluding != ""
					if m.VersionStartExcluding != "" {
						a.VersionStart = m.VersionStartExcluding
					}
					a.VersionEnd, a.VersionEndInclusive = m.VersionEndIncluding, m.VersionEndIncluding != ""
					if m.VersionEndExcluding != "" {
						a.VersionEnd = m.VersionEndExcluding
					}
				case version != "":
					a.VersionExact = version
				}
				if !seen[a] && Normalize(a.Vendor) != "" && Normalize(a.Product) != "" {
					seen[a] = true
					affected = append(affected, a)
				}
			}
		}
	}
	return adv, affected
}

// primaryMetric returns the primary metric, else the first.
func primaryMetric(ms []nvdMetric) *nvdMetric {
	for i := range ms {
		if ms[i].Type == "Primary" {
			return &ms[i]
		}
	}
	if len(ms) > 0 {
		return &ms[0]
	}
	return nil
}

// parseNVDTime parses the timestamps of the NVD (UTC without zone).
func parseNVDTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("vulnfeed: nvd time %q", s)
}

// parseCPE returns vendor, product and version ("" for * and -) of a CPE
// 2.3 formatted string; escaped characters (\:, \.) are unescaped.
func parseCPE(cpe string) (vendor, product, version string, ok bool) {
	var parts []string
	var cur strings.Builder
	escaped := false
	for _, r := range cpe {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == ':':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	if len(parts) < 6 || parts[0] != "cpe" || parts[1] != "2.3" {
		return "", "", "", false
	}
	vendor, product, version = parts[3], parts[4], parts[5]
	if vendor == "*" || product == "*" || vendor == "" || product == "" {
		return "", "", "", false
	}
	if version == "*" || version == "-" {
		version = ""
	}
	return vendor, product, version, true
}
