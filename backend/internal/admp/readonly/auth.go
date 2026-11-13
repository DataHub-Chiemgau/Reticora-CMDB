package readonly

import (
	"bytes"
	"context"
	"encoding/json"
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
// E-224, E-248). This file is the only one that may name another method
// than GET.
//
// The login of the ADMP documentation 9.14.0 (E-249, E-250, WP-1073):
// POST <base_url>/V1/user/authentication with the JSON body {username,
// password}; the answer 200 sets the cookie AuthToken (a JWT), which every
// GET to the base URL carries. Username and password are the fields
// username and token of the account's api_token credential. There is no
// logout: the session ends when the token is dropped from memory (expiry,
// a changed account, a restart); DELETE /V1/user/authentication is never
// sent.

// authMethod and authPath are the login request of the documentation; no
// configuration changes them.
const (
	authMethod = http.MethodPost
	authPath   = "/V1/user/authentication"
)

// defaultAuthMethod names the login method for the syntax proof of
// WP-927 (readonly_ast_test.go), which tracks every name of a write
// method; it is authMethod.
const defaultAuthMethod = authMethod //nolint:unused // the name the syntax proof of WP-927 checks

// authCookie is the session cookie of ADMP (components.securitySchemes).
const authCookie = "AuthToken"

// defaultTokenTTL is the lifetime of a token whose cookie names no expiry
// and whose JWT has no exp claim (E-250).
const defaultTokenTTL = 10 * time.Minute

// tokenMargin: a cached token is renewed this long before it expires.
const tokenMargin = 60 * time.Second

// maxLoginBody bounds the answer of a login that is read.
const maxLoginBody = 1 << 20

// Authenticator logs in to ADMP and returns a token with its expiry. New
// uses the login of the documentation (sessionAuth) when none is given;
// tests pass fakes. An authenticator of this package that implements
// loginBinder receives the login request of its client: POST on the auth
// URL over authTransport, and the login secret from the SecretSource.
type Authenticator interface {
	Token(ctx context.Context) (token string, expires time.Time, err error)
}

// loginBinder is implemented by the authenticators of this package that
// send the login request themselves.
type loginBinder interface {
	bindLogin(l *login)
}

// authURLFor returns the login URL of an account: base URL + authPath. A
// configured auth URL must be exactly that URL (scheme, host, port, path
// prefix, no further segment, no query or fragment; a trailing slash of
// the base URL is dropped first); otherwise ok is false and every login
// ends without a network access (E-250): username and password never go
// to another origin or path.
func authURLFor(base *url.URL, configured string) (u *url.URL, ok bool) {
	want := *base
	want.Path = strings.TrimSuffix(base.Path, "/") + authPath
	want.RawPath = ""
	want.RawQuery, want.Fragment, want.ForceQuery = "", "", false
	if strings.TrimSpace(configured) == "" {
		return &want, true
	}
	got, err := url.Parse(strings.TrimSpace(configured))
	if err != nil || got.User != nil || got.RawQuery != "" || got.ForceQuery || got.Fragment != "" ||
		originOf(got) != originOf(&want) || got.EscapedPath() != want.EscapedPath() {
		return nil, false
	}
	return &want, true
}

// login sends the one login request of an account over its own transport.
type login struct {
	// client is nil when the configured auth URL is not the login URL of
	// the base URL: every login is then refused with the class auth.
	client *http.Client
	method string
	url    *url.URL
	secret func(ctx context.Context) (map[string]any, error)
	now    func() time.Time
}

// newLogin builds the login of an account: an egress client of its own
// whose transport is wrapped in authTransport, which passes only POST on
// exactly the login URL.
func newLogin(authURL *url.URL, opts egress.Options, secret func(ctx context.Context) (map[string]any, error),
	now func() time.Time) *login {
	l := &login{method: authMethod, url: authURL, secret: secret, now: now}
	if authURL == nil {
		return l
	}
	hc := egress.NewClient(opts)
	hc.Transport = &authTransport{base: hc.Transport, method: authMethod, url: authURL}
	// A login does not follow redirects: its answer is the token.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	l.client = hc
	return l
}

// do sends the login request with header and body and returns the status,
// the header and the answer (bounded). Errors carry the class only.
func (l *login) do(ctx context.Context, header http.Header, body []byte) (status int, answerHeader http.Header, answer []byte, err error) {
	if l.client == nil {
		return 0, nil, nil, classErr(ClassAuth, 0, ErrDestinationNotAllowed)
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, l.method, l.url.String(), reader)
	if err != nil {
		return 0, nil, nil, classErr(ClassClient, 0, nil)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, nil, nil, transportErr(ctx, ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLoginBody))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, classErr(ClassNetwork, resp.StatusCode, nil)
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// sessionAuth is the login of the documentation (E-250): POST {username,
// password}, success is 200 with the cookie AuthToken. The body {message}
// is not read.
type sessionAuth struct {
	l *login
	// source is where the expiry of the last token came from
	// (sessionCookie); tokenCache calls Token one at a time.
	source string
}

func (a *sessionAuth) bindLogin(l *login) { a.l = l }

// loginBody is the request body of postV1UserAuthentication: exactly the
// two documented members (additionalProperties: false).
type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Token implements Authenticator. A login URL other than the documented
// one ends before the secret is read; a credential without username or
// token is the class credential; any other status than 200 or a missing
// cookie is auth. No error names the token, the password or the answer.
func (a *sessionAuth) Token(ctx context.Context) (string, time.Time, error) {
	if a.l == nil || a.l.client == nil {
		return "", time.Time{}, classErr(ClassAuth, 0, ErrDestinationNotAllowed)
	}
	secret, err := a.l.secret(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	user, _ := secret["username"].(string)
	password, _ := secret["token"].(string)
	if strings.TrimSpace(user) == "" || password == "" {
		return "", time.Time{}, classErr(ClassCredential, 0, nil)
	}
	body, err := json.Marshal(loginBody{Username: user, Password: password})
	if err != nil {
		return "", time.Time{}, classErr(ClassCredential, 0, nil)
	}
	status, header, _, err := a.l.do(ctx, http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}, body)
	if err != nil {
		return "", time.Time{}, err
	}
	if status != http.StatusOK {
		return "", time.Time{}, classErr(ClassAuth, status, nil)
	}
	now := time.Now()
	if a.l.now != nil {
		now = a.l.now()
	}
	token, expires, source, ok := sessionCookie(header, now)
	if !ok {
		return "", time.Time{}, classErr(ClassAuth, status, nil)
	}
	a.source = source
	return token, expires, nil
}

// expirySource implements expirySourcer.
func (a *sessionAuth) expirySource() string { return a.source }

// expirySourcer is implemented by the authenticators that tell where the
// expiry of their last token came from (TokenLifetimeOf).
type expirySourcer interface {
	expirySource() string
}

// Sources of the expiry of a token (TokenLifetime.Source).
const (
	ExpiryMaxAge  = "max-age"
	ExpiryExpires = "expires"
	ExpiryJWT     = "exp"
	ExpiryDefault = "default"
)

// sessionCookie returns the AuthToken of a login answer and its expiry:
// Max-Age, else Expires, else the exp claim of the JWT (read, not
// verified), else now + defaultTokenTTL; source names which one. A cookie
// that deletes itself (Max-Age 0 or less, an Expires in the past) is no
// token.
func sessionCookie(header http.Header, now time.Time) (token string, expires time.Time, source string, ok bool) {
	for _, line := range header.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil || c.Name != authCookie || c.Value == "" {
			continue
		}
		switch {
		case c.MaxAge < 0:
			return "", time.Time{}, "", false
		case c.MaxAge > 0:
			return c.Value, now.Add(time.Duration(c.MaxAge) * time.Second), ExpiryMaxAge, true
		case !c.Expires.IsZero():
			if !c.Expires.After(now) {
				return "", time.Time{}, "", false
			}
			return c.Value, c.Expires, ExpiryExpires, true
		}
		if exp, found := jwtExpiry(c.Value); found {
			return c.Value, exp, ExpiryJWT, true
		}
		return c.Value, now.Add(defaultTokenTTL), ExpiryDefault, true
	}
	return "", time.Time{}, "", false
}

// jwtExpiry reads the claim exp (seconds since the epoch) of a JWT without
// verifying it.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, ok := decodeBase64URL(parts[1])
	if !ok {
		return time.Time{}, false
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == nil || *claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(*claims.Exp), 0), true
}

// base64URLAlphabet is the alphabet of base64url (RFC 4648 §5).
const base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// decodeBase64URL decodes unpadded (or padded) base64url, the encoding of
// the JWT parts.
func decodeBase64URL(s string) ([]byte, bool) {
	s = strings.TrimRight(s, "=")
	out := make([]byte, 0, len(s)*3/4)
	var buf uint32
	var bits uint
	for i := range len(s) {
		v := strings.IndexByte(base64URLAlphabet, s[i])
		if v < 0 {
			return nil, false
		}
		buf = buf<<6 | uint32(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(buf>>bits))
			buf &= 1<<bits - 1
		}
	}
	return out, true
}

// tokenCache holds the token of an account until its expiry minus
// tokenMargin; invalidate drops it (after a 401).
type tokenCache struct {
	auth    Authenticator
	now     func() time.Time
	mu      sync.Mutex
	token   string
	expires time.Time
	// issued is when the cached token was obtained, source where its
	// expiry came from ("" for an authenticator that does not tell).
	issued time.Time
	source string
}

// get returns the cached token or logs in. A failure of the login is the
// class auth unless it already has a class (credential).
func (c *tokenCache) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expires.Add(-tokenMargin)) {
		return c.token, nil
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
	c.token, c.expires, c.issued, c.source = token, expires, c.now(), ""
	if s, ok := c.auth.(expirySourcer); ok {
		c.source = s.expirySource()
	}
	return token, nil
}

// lifetime returns the cached token's times; ok is false without one.
func (c *tokenCache) lifetime() (TokenLifetime, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return TokenLifetime{}, false
	}
	return TokenLifetime{IssuedAt: c.issued, Expires: c.expires, Source: c.source}, true
}

// invalidate drops the cached token.
func (c *tokenCache) invalidate() {
	c.mu.Lock()
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
}
