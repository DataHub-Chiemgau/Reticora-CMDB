package readonly

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The login is the only request of the package that is not a GET (ADM-02,
// E-224). This file is the only one that may name another method than GET.

// defaultAuthMethod is the login method when Config.AuthMethod is empty.
const defaultAuthMethod = http.MethodPost

// tokenMargin: a cached token is renewed this long before it expires.
const tokenMargin = 60 * time.Second

// maxLoginBody bounds the answer of a login that is read.
const maxLoginBody = 1 << 20

// Authenticator logs in to ADMP and returns a token with its expiry. The
// implementation for the login procedure of the documentation follows
// after A-55 (WP-1073); until then only test fakes exist. An
// authenticator of this package that implements loginBinder receives the
// login request of its client: the configured method on the auth URL over
// authTransport, and the login secret from the SecretSource.
type Authenticator interface {
	Token(ctx context.Context) (token string, expires time.Time, err error)
}

// loginBinder is implemented by the authenticators of this package that
// send the login request themselves.
type loginBinder interface {
	bindLogin(l *login)
}

// authMethod returns the login method of cfg: POST by default, else GET or
// POST; another method is refused.
func authMethod(cfg *Config) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(cfg.AuthMethod)) {
	case "":
		return defaultAuthMethod, true
	case http.MethodPost:
		return http.MethodPost, true
	case http.MethodGet:
		return http.MethodGet, true
	default:
		return "", false
	}
}

// login sends the one login request of an account over its own transport.
type login struct {
	// client is nil without an auth URL: every login is then refused.
	client *http.Client
	method string
	url    *url.URL
	secret func(ctx context.Context) (map[string]any, error)
}

// newLogin builds the login of an account: an egress client of its own
// whose transport is wrapped in authTransport.
func newLogin(authURL *url.URL, method string, opts egress.Options, secret func(ctx context.Context) (map[string]any, error)) *login {
	l := &login{method: method, url: authURL, secret: secret}
	if authURL == nil {
		return l
	}
	hc := egress.NewClient(opts)
	hc.Transport = &authTransport{base: hc.Transport, method: method, url: authURL}
	// A login does not follow redirects: its answer is the token.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	l.client = hc
	return l
}

// do sends the login request with header and body and returns the status
// and the answer (bounded). Errors carry the class only.
func (l *login) do(ctx context.Context, header http.Header, body []byte) (status int, answer []byte, err error) {
	if l.client == nil {
		return 0, nil, classErr(ClassMethodNotAllowed, 0, ErrDestinationNotAllowed)
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, l.method, l.url.String(), reader)
	if err != nil {
		return 0, nil, classErr(ClassClient, 0, nil)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, nil, transportErr(ctx, ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLoginBody))
	if err != nil {
		return resp.StatusCode, nil, classErr(ClassNetwork, resp.StatusCode, nil)
	}
	return resp.StatusCode, raw, nil
}

// tokenCache holds the token of an account until its expiry minus
// tokenMargin; invalidate drops it (after a 401).
type tokenCache struct {
	auth    Authenticator
	now     func() time.Time
	mu      sync.Mutex
	token   string
	expires time.Time
}

// get returns the cached token or logs in. A failure of the login is the
// class auth unless it already has a class (credential).
func (c *tokenCache) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expires.Add(-tokenMargin)) {
		return c.token, nil
	}
	if c.auth == nil {
		return "", classErr(ClassAuth, 0, ErrNoAuthenticator)
	}
	token, expires, err := c.auth.Token(ctx)
	if err != nil {
		// A classified failure (credential, network, timeout, a refusal of
		// the transport) keeps its class; any other is auth. The text of
		// the authenticator's error is dropped: it may name the answer.
		var e *Error
		if errors.As(err, &e) {
			return "", classErr(e.Class, e.Status, e.sentinel)
		}
		return "", classErr(ClassAuth, 0, nil)
	}
	if token == "" {
		return "", classErr(ClassAuth, 0, nil)
	}
	c.token, c.expires = token, expires
	return token, nil
}

// invalidate drops the cached token.
func (c *tokenCache) invalidate() {
	c.mu.Lock()
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
}
