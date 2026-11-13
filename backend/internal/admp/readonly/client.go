// Package readonly is the strictly reading ADMP client (ADM-02, ADM-03,
// E-223, E-224, E-248, WP-926, WP-1073). It knows only the GET reads of the
// allowlist on the base URL of an account and the one login request
// (POST /V1/user/authentication of the ADMP documentation 9.14.0):
//
//   - readOnlyTransport passes only GET without a body on a path template
//     of the allowlist (allowGets in the file of each method) below the
//     base URL, authTransport only the login request to base URL +
//     /V1/user/authentication; everything else is refused before the
//     network (transport.go). Client and login use egress clients of their
//     own and never share a transport. There is no logout.
//   - The only raw read, getJSON, is unexported and builds its requests
//     with http.MethodGet only; the exported methods of Client begin with
//     Get, List or Lookup and read through getJSON only (client.go,
//     ports.go, service.go).
//   - A token bucket (rate_limit_rps, burst 1), a timeout per request
//     (timeout_ms), retries with backoff for GET on 429 and 5xx only, and
//     a circuit breaker per account guard ADMP; metrics per account and
//     error class.
//
// Secrets, tokens and answers never reach a log, an error text or a metric
// label: errors carry the class and the HTTP status only.
//
// Every method decodes only the members it names into Go types of its
// own; credentials of the network elements, LDAP data of the service
// account and every other member of an answer are dropped while decoding.
// readonly*_test.go prove the guarantee (WP-927).
package readonly

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// Bounds and defaults of a client (admp_account checks, ADM-03).
const (
	defaultRateLimitRPS = 2.0
	maxRateLimitRPS     = 50.0
	defaultTimeout      = 10 * time.Second
	minTimeout          = time.Second
	maxTimeout          = 60 * time.Second
	// maxRetries: retries of a GET after 429 or 5xx.
	maxRetries = 3
	// retryBase is the first backoff; it doubles per retry.
	retryBase = 250 * time.Millisecond
	// maxRetryWait caps a backoff and a Retry-After.
	maxRetryWait = 30 * time.Second
	// maxBody bounds an answer that is read; a longer one is the class
	// decode (the inventory of WP-1073 is the largest answer).
	maxBody = 16 << 20
)

// Metrics per account and error class (ADM-03). The account label is the
// account's id, never a name, URL or secret.
var (
	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_admp_requests_total",
		Help: "ADMP reads per account by result class (ok or an error class).",
	}, []string{"account", "class"})
	requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reticora_admp_request_duration_seconds",
		Help:    "Duration of ADMP reads per account, including retries.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"account"})
	circuitOpen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "reticora_admp_circuit_open",
		Help: "1 while the circuit breaker of an ADMP account is open.",
	}, []string{"account"})
	// undocumentedShape counts the answers without schema in the
	// documentation (A-86) whose form differs from the assumed one.
	undocumentedShape = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_admp_undocumented_shape_total",
		Help: "ADMP answers without documented schema that did not have the assumed form, by operation.",
	}, []string{"operation"})
)

// RegisterMetrics registers the ADMP client metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(requestsTotal, requestDuration, circuitOpen, undocumentedShape)
}

// Config holds the values of an account (admp_account, WP-925) the client
// needs. RateLimitRPS and TimeoutMS out of their bounds take the defaults.
type Config struct {
	AccountID      string
	OrganizationID string
	CredentialID   string
	// BaseURL is the root of the ADMP instance without /V1 (the
	// documentation has no servers entry).
	BaseURL string
	// AuthURL is the login URL: empty is BaseURL + /V1/user/authentication,
	// any other value must be exactly that URL, else every login is
	// refused (class auth) without a network access (E-250).
	AuthURL      string
	RateLimitRPS float64
	TimeoutMS    int
}

// SecretSource returns the plaintext secret of a credential;
// credential.Service.Decrypt satisfies it (stored, or vault_ref of a
// connection with resolve_at server).
type SecretSource interface {
	Decrypt(ctx context.Context, orgID, id string) (map[string]any, error)
}

// Client reads ADMP for one account. Its exported methods begin with Get,
// List or Lookup and read through getJSON only.
type Client struct {
	cfg     Config
	base    *url.URL
	http    *http.Client
	login   *login
	tokens  *tokenCache
	bucket  *bucket
	breaker *breaker
	clock   clock
	timeout time.Duration
	// jitter returns a random part of d (tests replace it).
	jitter func(d time.Duration) time.Duration
}

// parseURL checks an account URL: absolute http(s) with a host, without
// user info, query or fragment.
func parseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, false
	}
	return u, true
}

// New creates the client of an account. opts is the egress policy of the
// server (SEC-08); the client and the login get egress clients of their
// own, each wrapped in its guarding transport. A nil auth is the login of
// the documentation (POST /V1/user/authentication, cookie AuthToken). The
// configuration error names no value.
func New(cfg Config, secrets SecretSource, auth Authenticator, opts egress.Options) (*Client, error) { //nolint:gocritic // the signature of WP-926 (AK1): the account values by value
	if cfg.AccountID == "" || cfg.OrganizationID == "" {
		return nil, ErrInvalidConfig
	}
	base, ok := parseURL(cfg.BaseURL)
	if !ok {
		return nil, ErrInvalidConfig
	}
	if cfg.AuthURL != "" {
		if _, ok = parseURL(cfg.AuthURL); !ok {
			return nil, ErrInvalidConfig
		}
	}
	// A configured auth URL other than the documented one leaves the
	// login without a URL: it is refused before the network (E-250).
	authURL, _ := authURLFor(base, cfg.AuthURL)
	if auth == nil {
		auth = &sessionAuth{}
	}
	if cfg.RateLimitRPS <= 0 || cfg.RateLimitRPS > maxRateLimitRPS {
		cfg.RateLimitRPS = defaultRateLimitRPS
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout < minTimeout || timeout > maxTimeout {
		timeout = defaultTimeout
	}
	opts.Timeout = timeout
	c := &Client{cfg: cfg, base: base, bucket: newBucket(cfg.RateLimitRPS), breaker: &breaker{},
		clock: realClock(), timeout: timeout,
		jitter: func(d time.Duration) time.Duration {
			if d <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(d)))
		}}
	hc := egress.NewClient(opts)
	hc.Transport = newReadOnlyTransport(hc.Transport, base)
	c.http = hc
	c.login = newLogin(authURL, opts, func(ctx context.Context) (map[string]any, error) {
		return c.secret(ctx, secrets)
	}, func() time.Time { return c.clock.now() })
	if b, isBinder := auth.(loginBinder); isBinder {
		b.bindLogin(c.login)
	}
	c.tokens = &tokenCache{auth: auth, now: func() time.Time { return c.clock.now() }}
	circuitOpen.WithLabelValues(cfg.AccountID).Set(0)
	return c, nil
}

// secret reads the login secret of the account; every failure is the
// class credential without the source's text.
func (c *Client) secret(ctx context.Context, secrets SecretSource) (map[string]any, error) {
	if secrets == nil || c.cfg.CredentialID == "" {
		return nil, classErr(ClassCredential, 0, nil)
	}
	s, err := secrets.Decrypt(ctx, c.cfg.OrganizationID, c.cfg.CredentialID)
	if err != nil || len(s) == 0 {
		return nil, classErr(ClassCredential, 0, nil)
	}
	return s, nil
}

// TokenLifetime is the lifetime of the session token of a client's last
// login, without the token: when it was obtained, when it expires and
// where the expiry came from (ExpiryMaxAge, ExpiryExpires, ExpiryJWT,
// ExpiryDefault; empty for an authenticator that does not tell).
type TokenLifetime struct {
	IssuedAt time.Time
	Expires  time.Time
	Source   string
}

// TokenLifetimeOf reports the lifetime of the token c holds (the smoke
// test answers A-86 with it); ok is false before the first login and
// after a 401 dropped the token. It sends no request.
func TokenLifetimeOf(c *Client) (TokenLifetime, bool) {
	return c.tokens.lifetime()
}

// IsCircuitOpen reports whether the circuit breaker of the client's
// account refuses reads now (WP-929 skips the account).
func IsCircuitOpen(c *Client) bool {
	return c.breaker.isOpen(c.clock.now())
}

// resolve resolves a read path relative to the base URL. Absolute URLs,
// dot segments (also encoded) and paths outside the prefix of the base
// URL are refused before any network access.
func (c *Client) resolve(p string, query url.Values) (*url.URL, error) {
	if strings.Contains(p, "://") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "?#\\") {
		return nil, classErr(ClassMethodNotAllowed, 0, ErrInvalidPath)
	}
	rel, err := url.Parse(p)
	if err != nil || rel.IsAbs() || rel.Host != "" || rel.User != nil {
		return nil, classErr(ClassMethodNotAllowed, 0, ErrInvalidPath)
	}
	decoded, err := url.PathUnescape(rel.EscapedPath())
	if err != nil {
		return nil, classErr(ClassMethodNotAllowed, 0, ErrInvalidPath)
	}
	prefix := strings.TrimSuffix(c.base.Path, "/")
	joined := prefix + "/" + strings.TrimPrefix(rel.Path, "/")
	if !cleanBelow(rel.Path, "") || !cleanBelow(decoded, "") || !cleanBelow(joined, prefix) {
		return nil, classErr(ClassMethodNotAllowed, 0, ErrInvalidPath)
	}
	u := *c.base
	u.Path = joined
	u.RawPath = ""
	if rel.RawPath != "" {
		u.RawPath = strings.TrimSuffix(c.base.EscapedPath(), "/") + "/" + strings.TrimPrefix(rel.RawPath, "/")
	}
	u.RawQuery = query.Encode()
	return &u, nil
}

// breakerFailure reports whether a class counts against the breaker: the
// failures of ADMP or the way to it, not a refused or malformed read.
func breakerFailure(class string) bool {
	switch class {
	case ClassNetwork, ClassTimeout, ClassServer, ClassRateLimited, ClassAuth:
		return true
	}
	return false
}

// getJSON reads path (relative to the base URL) with query by GET and
// decodes the JSON answer into out. It is the only read of the package.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	start := c.clock.now()
	err := c.read(ctx, path, query, out)
	class := classOK
	if err != nil {
		class = ClassOf(err)
	}
	requestsTotal.WithLabelValues(c.cfg.AccountID, class).Inc()
	requestDuration.WithLabelValues(c.cfg.AccountID).Observe(c.clock.now().Sub(start).Seconds())
	return err
}

func (c *Client) read(ctx context.Context, path string, query url.Values, out any) error {
	u, err := c.resolve(path, query)
	if err != nil {
		return err
	}
	// A path outside the allowlist ends here, before the login: ADMP sees
	// no request for it, not even the login (WP-1077). The transport
	// refuses it again.
	if rt, ok := c.http.Transport.(*readOnlyTransport); ok && !rt.allowed(u) {
		return classErr(ClassPathNotAllowed, 0, ErrPathNotAllowed)
	}
	if !c.breaker.allow(c.clock.now()) {
		return classErr(ClassCircuitOpen, 0, ErrCircuitOpen)
	}
	err = c.attempts(ctx, u, out)
	if err != nil && ctx.Err() != nil {
		// The caller ended the read (cancel, its own deadline): no outcome
		// of ADMP, whatever class the attempt reported.
		err = classErr(ClassCanceled, 0, nil)
	}
	class := ClassOf(err)
	var open bool
	switch {
	case err == nil || breakerFailure(class):
		open = c.breaker.done(c.clock.now(), err != nil)
	case class == ClassClient || class == ClassDecode:
		// ADMP answered: the breaker closes, a half-open probe included.
		open = c.breaker.done(c.clock.now(), false)
	default:
		// A read that never reached ADMP, a local failure or one the
		// caller ended: no outcome; the breaker and its gauge stay.
		c.breaker.release()
		return err
	}
	c.setCircuitGauge(open)
	return err
}

// setCircuitGauge mirrors the state of the breaker in the metric.
func (c *Client) setCircuitGauge(open bool) {
	gauge := 0.0
	if open {
		gauge = 1
	}
	circuitOpen.WithLabelValues(c.cfg.AccountID).Set(gauge)
}

// attempts sends the GET until it succeeds, fails for good or used its
// retries; a 401 drops the token and logs in once more.
func (c *Client) attempts(ctx context.Context, u *url.URL, out any) error {
	token, err := c.tokens.get(ctx)
	if err != nil {
		return err
	}
	renewed := false
	for retries := 0; ; {
		wait, slot := c.bucket.reserve(c.clock.now())
		if err = c.clock.sleep(ctx, wait); err != nil {
			// Only the caller's context ends a wait: the token goes back.
			c.bucket.unreserve(slot)
			return classErr(ClassCanceled, 0, nil)
		}
		status, header, body, serr := c.send(ctx, u, token)
		switch {
		case serr != nil:
			return serr
		case status == http.StatusUnauthorized && !renewed:
			renewed = true
			c.tokens.invalidate()
			if token, err = c.tokens.get(ctx); err != nil {
				return err
			}
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return classErr(ClassAuth, status, nil)
		case retryable(http.MethodGet, status):
			if retries >= maxRetries {
				if status == http.StatusTooManyRequests {
					return classErr(ClassRateLimited, status, nil)
				}
				return classErr(ClassServer, status, nil)
			}
			wait := c.backoff(retries, status, header)
			retries++
			if err = c.clock.sleep(ctx, wait); err != nil {
				return classErr(ClassCanceled, 0, nil)
			}
			continue
		case status < 200 || status > 299:
			e := classErr(ClassClient, status, nil)
			e.named = status == http.StatusNotFound && namedError(body)
			return e
		}
		if out != nil && len(body) > 0 {
			if derr := json.Unmarshal(body, out); derr != nil {
				return classErr(ClassDecode, status, nil)
			}
		}
		return nil
	}
}

// retryable reports whether a request is repeated: only a GET, only after
// 429 or a 5xx.
func retryable(method string, status int) bool {
	return method == http.MethodGet && (status == http.StatusTooManyRequests || status >= 500 && status <= 599)
}

// backoff returns the wait before retry n (0-based): exponential with
// jitter, at least a Retry-After of a 429, at most maxRetryWait.
func (c *Client) backoff(n, status int, header http.Header) time.Duration {
	d := retryBase << n
	d += c.jitter(d / 2)
	if status == http.StatusTooManyRequests {
		if after := retryAfter(header.Get("Retry-After"), c.clock.now()); after > d {
			d = after
		}
	}
	if d > maxRetryWait {
		d = maxRetryWait
	}
	return d
}

// retryAfter parses a Retry-After header: seconds or an HTTP date.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// send makes one GET of u with the token. A failure of the transport
// carries its class only; the answer is read bounded and never logged.
func (c *Client) send(ctx context.Context, u *url.URL, token string) (status int, header http.Header, body []byte, err error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return 0, nil, nil, classErr(ClassClient, 0, nil)
	}
	req.Header.Set("Accept", "application/json")
	// The session cookie of ADMP, set by hand (no cookie jar): only the
	// readOnlyTransport, which passes nothing but the base URL, carries it.
	req.Header.Set("Cookie", authCookie+"="+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, transportErr(ctx, actx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, nil, transportErr(ctx, actx, err)
	}
	if len(body) > maxBody {
		return 0, nil, nil, classErr(ClassDecode, resp.StatusCode, nil)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// transportErr classifies a failure of the transport: a refusal of the
// guarding transport, an end of the caller's context parent (canceled, no
// outcome of ADMP), the timeout of the attempt (actx), else network. The
// text of err (it may name the URL) is dropped.
func transportErr(parent, actx context.Context, err error) error {
	var netErr net.Error
	switch {
	case errors.Is(err, ErrPathNotAllowed):
		return classErr(ClassPathNotAllowed, 0, ErrPathNotAllowed)
	case errors.Is(err, ErrMethodNotAllowed):
		return classErr(ClassMethodNotAllowed, 0, ErrMethodNotAllowed)
	case errors.Is(err, ErrDestinationNotAllowed):
		return classErr(ClassMethodNotAllowed, 0, ErrDestinationNotAllowed)
	case parent.Err() != nil:
		return classErr(ClassCanceled, 0, nil)
	case errors.Is(err, context.DeadlineExceeded), actx.Err() != nil, errors.As(err, &netErr) && netErr.Timeout():
		return classErr(ClassTimeout, 0, nil)
	default:
		return classErr(ClassNetwork, 0, nil)
	}
}

// sessionPath is getV1UserAuthentication, the state of the login.
const sessionPath = "/V1/user/authentication"

var _ = allowGets(getRoute{path: sessionPath})

// Session is the service account ADMP logged in (getV1UserAuthentication):
// its id and organization only; the ldap* members are dropped.
type Session struct {
	UserID         int64
	OrganizationID int64
}

// GetSession reads the state of the login: GET /V1/user/authentication.
// It logs in first when no token is cached (E-250).
func (c *Client) GetSession(ctx context.Context) (*Session, error) {
	var raw struct {
		ID             *docInt `json:"id"`
		OrganizationID *docInt `json:"organizationId"`
	}
	if err := c.getJSON(ctx, sessionPath, nil, &raw); err != nil {
		return nil, err
	}
	if raw.ID == nil || raw.OrganizationID == nil {
		return nil, classErr(ClassDecode, 0, nil)
	}
	return &Session{UserID: int64(*raw.ID), OrganizationID: int64(*raw.OrganizationID)}, nil
}

// maxDocInt is the largest whole number a JSON number of ADMP carries
// exactly (2^53).
const maxDocInt = 1 << 53

// docInt is a whole JSON number of the documentation (an id, an index):
// a fraction, a string or a value beyond 2^53 fails decoding.
type docInt int64

// UnmarshalJSON implements json.Unmarshaler.
func (n *docInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		if v > maxDocInt || v < -maxDocInt {
			return errDocInt
		}
		*n = docInt(v)
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != float64(int64(f)) || f > maxDocInt || f < -maxDocInt {
		return errDocInt
	}
	*n = docInt(int64(f))
	return nil
}

// errDocInt names no value of the answer.
var errDocInt = errors.New("admp: not a whole number")

// docPath builds a read path from a template of the allowlist and its
// ids; an id below 1 is refused before the network (class client).
func docPath(template string, ids ...int64) (string, error) {
	out := template
	for _, id := range ids {
		if id < 1 || id > maxDocInt {
			return "", classErr(ClassClient, 0, ErrInvalidPath)
		}
		start := strings.Index(out, "{")
		end := strings.Index(out, "}")
		if start < 0 || end < start {
			return "", classErr(ClassClient, 0, ErrInvalidPath)
		}
		out = out[:start] + strconv.FormatInt(id, 10) + out[end+1:]
	}
	return out, nil
}

// undocumented counts a decode failure of an answer without documented
// schema (A-86) and returns err unchanged.
func undocumented(operation string, err error) error {
	if ClassOf(err) == ClassDecode {
		undocumentedShape.WithLabelValues(operation).Inc()
	}
	return err
}
