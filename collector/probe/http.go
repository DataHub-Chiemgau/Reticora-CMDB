package probe

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeHTTP is the register key of the HTTP/HTTPS probe.
const TypeHTTP = "http"

// Values of the http probe (PRT-04, WP-971): the phase times in
// milliseconds, the status code and the remaining certificate lifetime in
// seconds. The metric bridge names them probe_http_phase_seconds{phase},
// probe_http_status_code and probe_http_cert_expiry_seconds
// (docs/betrieb/monitoring-probes.md).
const (
	ValueHTTPDNS        = "dns_ms"
	ValueHTTPConnect    = "connect_ms"
	ValueHTTPTLS        = "tls_ms"
	ValueHTTPTTFB       = "ttfb_ms"
	ValueHTTPStatusCode = "status_code"
	ValueHTTPCertExpiry = "cert_expiry_s"
)

// Defaults of the http parameters (api/probe-types.json).
const (
	httpDefaultMaxBody      = 64 << 10
	httpMaxBody             = 1 << 20
	httpDefaultCertWarnDays = 21
	httpMaxRedirects        = 5
	httpUserAgent           = "Reticora-Probe/1"
)

// registeredHTTP is the executor init registers; ConfigureHTTP sets its
// TLS scopes.
var registeredHTTP = &HTTPExecutor{shared: new(atomic.Pointer[[]plugins.TLSScope])}

func init() { Register(TypeHTTP, registeredHTTP) }

// ConfigureHTTP sets the TLS scopes (CA bundle or SPKI pins per network) of
// the registered http executor, which probe.Run uses; without it https is
// verified against the system roots only. The collector calls it once at
// start, before the first check; checks that run meanwhile see either the
// old or the new scopes (atomic swap).
func ConfigureHTTP(scopes []plugins.TLSScope) {
	s := slices.Clone(scopes)
	registeredHTTP.shared.Store(&s)
}

// Resolver resolves a host name; net.DefaultResolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// HTTPExecutor is the probe type http (PRT-04, WP-971): one GET or HEAD of
// params.url. It resolves the host itself (phase dns), verifies the
// certificate with the TLS scope of the address (CA bundle or SPKI pin,
// otherwise the system roots) and follows redirects itself, at most
// follow_redirects; Authorization (Basic from username/password or Bearer
// from token of the namespace http) goes only to the origin of url. An
// untrusted certificate ends the check in the handshake: no request and no
// credential reaches the target.
//
// Outcomes: ok (status expected, body_regex matched; with the warn code
// cert_expiring when the certificate expires within cert_warn_days, which
// the state machine turns into degraded); fail with refused, timeout,
// unreachable, tls, status_mismatch, body_mismatch or redirect_limit;
// error with tls_untrusted, auth_failed (401/403 not expected) or config.
type HTTPExecutor struct {
	// TLSScopes are the TLS exceptions of the collector.
	TLSScopes []plugins.TLSScope
	// Resolver replaces the system resolver in tests.
	Resolver Resolver
	// Now replaces the clock of the certificate lifetime in tests.
	Now func() time.Time
	// shared holds the scopes of ConfigureHTTP (registered executor only);
	// once set they replace TLSScopes.
	shared *atomic.Pointer[[]plugins.TLSScope]
}

// tlsScopes returns the TLS scopes of the executor.
func (e *HTTPExecutor) tlsScopes() []plugins.TLSScope {
	if e.shared != nil {
		if s := e.shared.Load(); s != nil {
			return *s
		}
	}
	return e.TLSScopes
}

type httpParams struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	ExpectedStatus  []string          `json:"expected_status"`
	BodyRegex       string            `json:"body_regex"`
	MaxBody         int64             `json:"max_body"`
	FollowRedirects int               `json:"follow_redirects"`
	Headers         map[string]string `json:"headers"`
	CertWarnDays    int               `json:"cert_warn_days"`
}

// statusRange is one entry of expected_status.
type statusRange struct{ lo, hi int }

// httpCheck is a decoded and checked http probe.
type httpCheck struct {
	url      *url.URL
	method   string
	expected []statusRange
	body     *regexp.Regexp
	maxBody  int64
	follow   int
	headers  map[string]string
	certWarn time.Duration
	auth     string
}

// parseHTTP decodes the parameters and the credential; a violation is the
// error/config result with the field.
func parseHTTP(spec *probewire.ProbeSpec, creds map[string]string) (*httpCheck, *probewire.Result) {
	p := httpParams{Method: http.MethodGet, MaxBody: httpDefaultMaxBody, CertWarnDays: httpDefaultCertWarnDays}
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			r := configResult("params", "parameters cannot be decoded")
			return nil, &r
		}
	}
	bad := func(field, msg string) (*httpCheck, *probewire.Result) {
		r := configResult(field, msg)
		return nil, &r
	}
	u, err := probewire.ParseHTTPURL(p.URL)
	if err != nil {
		return bad("params.url", err.Error())
	}
	c := &httpCheck{url: u, method: p.Method, maxBody: p.MaxBody, follow: p.FollowRedirects, headers: p.Headers,
		certWarn: time.Duration(p.CertWarnDays) * 24 * time.Hour}
	if c.method != http.MethodGet && c.method != http.MethodHead {
		return bad("params.method", "only GET and HEAD are allowed")
	}
	if c.maxBody <= 0 || c.maxBody > httpMaxBody {
		return bad("params.max_body", "must be between 1 and 1048576")
	}
	if c.follow < 0 || c.follow > httpMaxRedirects {
		return bad("params.follow_redirects", "must be between 0 and 5")
	}
	for k := range c.headers {
		if k != "Accept" && k != "User-Agent" && k != "Host" {
			return bad("params.headers."+k, "unknown parameter")
		}
	}
	if len(p.ExpectedStatus) == 0 {
		p.ExpectedStatus = []string{"200-399"}
	}
	for i, s := range p.ExpectedStatus {
		lo, hi, err := probewire.ParseStatusRange(s)
		if err != nil {
			return bad("params.expected_status["+strconv.Itoa(i)+"]", err.Error())
		}
		c.expected = append(c.expected, statusRange{lo: lo, hi: hi})
	}
	if p.BodyRegex != "" {
		if c.method != http.MethodGet {
			return bad("params.body_regex", "is allowed with method GET only")
		}
		re, err := regexp.Compile(p.BodyRegex)
		if err != nil {
			return bad("params.body_regex", "must be a valid RE2 regular expression")
		}
		c.body = re
	}
	switch user, token := creds["username"], creds["token"]; {
	case token != "":
		c.auth = "Bearer " + token
	case user != "" && creds["password"] != "":
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+creds["password"]))
	case len(creds) > 0:
		// A credential is bound, but it names neither a token nor a user
		// with password.
		return bad("credential", "the http credential needs a token or username and password")
	}
	if c.auth != "" && u.Scheme != "https" {
		return bad("params.url", "a credential is sent over https only")
	}
	return c, nil
}

// expects reports whether the status lies in expected_status.
func (c *httpCheck) expects(status int) bool {
	for _, r := range c.expected {
		if status >= r.lo && status <= r.hi {
			return true
		}
	}
	return false
}

// httpPhases are the phase times of the last request.
type httpPhases struct {
	dns, connect, tls, ttfb time.Duration
	connectStart, tlsStart  time.Time
}

// Execute implements Executor.
func (e *HTTPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	c, refused := parseHTTP(&spec, creds)
	if refused != nil {
		return *refused
	}
	start := time.Now()
	origin := c.url
	target := c.url
	for hop := 0; ; hop++ {
		resp, phases, res := e.request(ctx, c, target, sameOrigin(origin, target))
		if res != nil {
			res.RTTMS = msSince(start)
			return *res
		}
		location := resp.Header.Get("Location")
		if c.follow > 0 && isRedirect(resp.StatusCode) && location != "" {
			_ = resp.Body.Close()
			next, err := target.Parse(location)
			switch {
			case err != nil || (next.Scheme != "http" && next.Scheme != "https"):
				return withRTT(failResult(probewire.StatusFail, probewire.ReasonRedirectLimit), start)
			case hop >= c.follow:
				return withRTT(failResult(probewire.StatusFail, probewire.ReasonRedirectLimit), start)
			case c.auth != "" && target.Scheme == "https" && next.Scheme == "http":
				// A credential never leaves https (COL-02).
				return withRTT(failResult(probewire.StatusFail, probewire.ReasonRedirectLimit), start)
			}
			target = next
			continue
		}
		return e.evaluate(c, resp, phases, start)
	}
}

func withRTT(r probewire.Result, start time.Time) probewire.Result {
	r.RTTMS = msSince(start)
	return r
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	}
	return false
}

// sameOrigin reports whether b has the scheme, host and port of a.
func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// request sends one request to target over the address the executor
// resolved; with auth it carries Authorization. A failure before an answer
// is the returned result.
func (e *HTTPExecutor) request(ctx context.Context, c *httpCheck, target *url.URL, auth bool) (*http.Response, *httpPhases, *probewire.Result) {
	ph := &httpPhases{}
	host := target.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		resolver := e.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		t0 := time.Now()
		addrs, err := resolver.LookupIPAddr(ctx, host)
		ph.dns = time.Since(t0)
		if err != nil || len(addrs) == 0 {
			code := probewire.ReasonUnreachable
			if ctx.Err() != nil {
				code = probewire.ReasonTimeout
			}
			r := failResult(probewire.StatusFail, code)
			return nil, nil, &r
		}
		ip = addrs[0].IP
	}
	addr := net.JoinHostPort(ip.String(), effectivePort(target))
	var tlsCfg *tls.Config
	if target.Scheme == "https" {
		tlsCfg = plugins.TLSConfigFor(ip.String(), e.tlsScopes()).Clone()
		tlsCfg.ServerName = host
	}
	var dialer net.Dialer
	transport := &http.Transport{
		DialContext:       func(ctx context.Context, network, _ string) (net.Conn, error) { return dialer.DialContext(ctx, network, addr) },
		TLSClientConfig:   tlsCfg,
		DisableKeepAlives: true,
		Proxy:             nil,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	trace := &httptrace.ClientTrace{
		ConnectStart:      func(string, string) { ph.connectStart = time.Now() },
		ConnectDone:       func(string, string, error) { ph.connect = time.Since(ph.connectStart) },
		TLSHandshakeStart: func() { ph.tlsStart = time.Now() },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { ph.tls = time.Since(ph.tlsStart) },
	}
	reqStart := time.Now()
	trace.GotFirstResponseByte = func() { ph.ttfb = time.Since(reqStart) }
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), c.method, target.String(), http.NoBody)
	if err != nil {
		r := configResult("params.url", "the URL cannot be requested")
		return nil, nil, &r
	}
	req.Header.Set("User-Agent", httpUserAgent)
	for k, v := range c.headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	if auth && c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	resp, err := client.Do(req)
	if err != nil {
		r := httpErrorResult(ctx, err)
		return nil, nil, &r
	}
	return resp, ph, nil
}

// httpErrorResult maps a failed request: an untrusted certificate is
// error/tls_untrusted, another TLS failure fail/tls, the rest like a tcp
// connect.
func httpErrorResult(ctx context.Context, err error) probewire.Result {
	var recordErr tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case plugins.UntrustedCertificate(err):
		return failResult(probewire.StatusError, probewire.ReasonTLSUntrusted)
	case errors.As(err, &recordErr), errors.As(err, &alert):
		return failResult(probewire.StatusFail, probewire.ReasonTLS)
	}
	return failResult(probewire.StatusFail, tcpReason(ctx, err))
}

// evaluate judges the final answer and reads the body for body_regex (at
// most max_body; the rest is cut off without error).
func (e *HTTPExecutor) evaluate(c *httpCheck, resp *http.Response, ph *httpPhases, start time.Time) probewire.Result {
	defer func() { _ = resp.Body.Close() }()
	var body []byte
	var readErr error
	if c.body != nil {
		body, readErr = io.ReadAll(io.LimitReader(resp.Body, c.maxBody))
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: map[string]float64{
		ValueHTTPDNS: ms(ph.dns), ValueHTTPConnect: ms(ph.connect), ValueHTTPTLS: ms(ph.tls), ValueHTTPTTFB: ms(ph.ttfb),
		ValueHTTPStatusCode: float64(resp.StatusCode),
	}}
	var expiry time.Duration
	hasCert := resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0
	if hasCert {
		now := time.Now
		if e.Now != nil {
			now = e.Now
		}
		expiry = resp.TLS.PeerCertificates[0].NotAfter.Sub(now())
		r.Values[ValueHTTPCertExpiry] = expiry.Truncate(time.Second).Seconds()
	}
	r.RTTMS = msSince(start)
	switch {
	case !c.expects(resp.StatusCode) && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden):
		r.Status, r.Code = probewire.StatusError, probewire.ReasonAuthFailed
	case !c.expects(resp.StatusCode):
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonStatusMismatch
	case readErr != nil:
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonTimeout
	case c.body != nil && !c.body.Match(body):
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonBodyMismatch
	case hasCert && expiry < c.certWarn:
		r.Code = probewire.WarnCertExpiring
	}
	return r
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
