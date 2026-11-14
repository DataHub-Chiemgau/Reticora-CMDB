// Package vault is a read-only client for the KV version 2 secrets engine
// of OpenBao and HashiCorp Vault (CRD-03, CRD-04, WP-895, E-221). It logs
// in with AppRole or a token, reads one entry (optionally a version) and
// renews its own token; it never writes. The transport refuses every method
// but GET and every POST but the two login paths before a connection is
// opened (ErrMethodNotAllowed). Values live only in memory: the Cache
// overwrites them when they expire or on Close, nothing is written to disk.
// Errors carry a class (Class) and never a value, a token or a secret id.
//
// The collector uses it for references of connections with resolve_at
// collector, the server (backend/internal/credential) for resolve_at
// server, over its egress dialer.
package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Login methods of a connection.
const (
	AuthAppRole = "approle"
	AuthToken   = "token"
)

// The two POST paths the transport lets through: the AppRole login and the
// renewal of the client's own token.
const (
	pathAppRoleLogin = "/v1/auth/approle/login"
	pathRenewSelf    = "/v1/auth/token/renew-self"
	pathLookupSelf   = "/v1/auth/token/lookup-self"
)

// DefaultTTL is how long a value read is kept when the answer names no
// shorter lease (AK2 of WP-895).
const DefaultTTL = 300 * time.Second

// DefaultTimeout bounds one request to the vault.
const DefaultTimeout = 15 * time.Second

// maxBody bounds an answer of the vault.
const maxBody = 1 << 20

// Error kinds. An error of the client matches one of them with errors.Is;
// Class names it for logs, metrics and reports.
var (
	// ErrUnreachable: the vault did not answer, answered with a server
	// error or with something that is not KV v2.
	ErrUnreachable = errors.New("vault unreachable")
	// ErrDenied: the login was refused or the token may not read the path.
	ErrDenied = errors.New("vault access denied")
	// ErrNotFound: the entry or its version does not exist.
	ErrNotFound = errors.New("vault entry not found")
	// ErrFieldMissing: the entry lacks a field the field map names.
	ErrFieldMissing = errors.New("vault field missing")
	// ErrMethodNotAllowed: the client tried a write; nothing was sent.
	ErrMethodNotAllowed = errors.New("vault: only reads and the login are allowed")
)

// Error classes as the collector and the server report them (AK4).
const (
	ClassUnreachable  = "vault_unreachable"
	ClassDenied       = "vault_denied"
	ClassNotFound     = "vault_not_found"
	ClassFieldMissing = "field_missing"
)

// Class returns the error class of err ("" for nil or an error of another
// kind). A refused write counts as unreachable: nothing reached the vault.
func Class(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDenied):
		return ClassDenied
	case errors.Is(err, ErrNotFound):
		return ClassNotFound
	case errors.Is(err, ErrFieldMissing):
		return ClassFieldMissing
	case errors.Is(err, ErrUnreachable), errors.Is(err, ErrMethodNotAllowed):
		return ClassUnreachable
	}
	return ""
}

// Error is an error of the client: its kind and, for an unreachable vault,
// the cause (a transport error; it names at most address and path).
type Error struct {
	Kind  error
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Kind.Error() + ": " + e.Cause.Error()
	}
	return e.Kind.Error()
}

// Is matches the kind.
func (e *Error) Is(target error) bool { return target == e.Kind }

// Unwrap returns the cause (an egress refusal, a dial error).
func (e *Error) Unwrap() error { return e.Cause }

func kindErr(kind, cause error) error { return &Error{Kind: kind, Cause: cause} }

// Config describes a connection.
type Config struct {
	// Address is the https URL of the vault: scheme, host and optionally
	// the path prefix of a reverse proxy (https://host/vault); the API
	// paths /v1/... follow the prefix.
	Address string
	// Namespace is sent as X-Vault-Namespace when set (Vault Enterprise,
	// OpenBao namespaces).
	Namespace string
	// CAPEM is the only trust anchor of the TLS connection when set;
	// otherwise the system roots apply.
	CAPEM string
	// Transport is the base transport (cloned; nil: a plain one). The
	// server passes the transport of its egress client (SEC-08).
	Transport *http.Transport
	// Timeout bounds one request (zero: DefaultTimeout).
	Timeout time.Duration
	// Now replaces the clock (tests).
	Now func() time.Time
}

// Auth is the login of a connection: AppRole (RoleID, SecretID) or a
// token.
type Auth struct {
	Method   string
	RoleID   string
	SecretID string
	Token    string
}

// Secret is a version of a KV v2 entry.
type Secret struct {
	// Data are the string fields of the entry; other values are left out.
	Data map[string]string
	// Version is the version the vault returned (metadata.version).
	Version int
	// LeaseDuration is lease_duration of the answer (0: none).
	LeaseDuration time.Duration
}

// Client reads one vault. It is safe for concurrent use.
type Client struct {
	base      *url.URL
	namespace string
	http      *http.Client
	now       func() time.Time

	mu        sync.Mutex
	auth      Auth
	token     string
	expires   time.Time // zero: no expiry known
	renewable bool
}

// New returns a client for cfg. The address must be https; an invalid CA
// is an error.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.Address), "/"))
	if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil {
		return nil, errors.New("vault: the address must be an https URL with a host")
	}
	var transport *http.Transport
	if cfg.Transport != nil {
		transport = cfg.Transport.Clone()
	} else {
		transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: DefaultTimeout}).DialContext,
			TLSHandshakeTimeout: DefaultTimeout, ResponseHeaderTimeout: DefaultTimeout}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	if strings.TrimSpace(cfg.CAPEM) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CAPEM)) {
			return nil, errors.New("vault: ca_pem holds no certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		base:      u,
		namespace: strings.TrimSpace(cfg.Namespace),
		http: &http.Client{Timeout: timeout, Transport: readOnlyTransport{base: transport, prefix: strings.TrimRight(u.Path, "/")},
			// A redirect could turn into another method or host: none
			// is followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now: now,
	}, nil
}

// readOnlyTransport lets only GET and the POSTs of the login and the token
// renewal through (AK1); everything else fails before a connection. prefix
// is the path of the address (a reverse proxy, "" without one): the two
// POST paths are allowed below it and nowhere else.
type readOnlyTransport struct {
	base   http.RoundTripper
	prefix string
}

// RoundTrip implements http.RoundTripper.
func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !allowedRequest(req.Method, strings.TrimPrefix(req.URL.Path, t.prefix)) || !strings.HasPrefix(req.URL.Path, t.prefix) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrMethodNotAllowed
	}
	return t.base.RoundTrip(req)
}

func allowedRequest(method, path string) bool {
	switch method {
	case http.MethodGet:
		return true
	case http.MethodPost:
		return path == pathAppRoleLogin || path == pathRenewSelf
	}
	return false
}

// Close drops the token and the idle connections.
func (c *Client) Close() {
	c.mu.Lock()
	c.token, c.auth = "", Auth{}
	c.mu.Unlock()
	c.http.CloseIdleConnections()
}

// Login logs in: AppRole over POST /v1/auth/approle/login, a token by
// looking itself up (GET /v1/auth/token/lookup-self), which also tells its
// lifetime. The client logs in again on its own when the token expired.
func (c *Client) Login(ctx context.Context, auth Auth) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.auth = auth
	return c.loginLocked(ctx)
}

func (c *Client) loginLocked(ctx context.Context) error {
	switch c.auth.Method {
	case AuthAppRole:
		if c.auth.RoleID == "" || c.auth.SecretID == "" {
			return kindErr(ErrDenied, nil)
		}
		body, _ := json.Marshal(map[string]string{"role_id": c.auth.RoleID, "secret_id": c.auth.SecretID})
		var out authAnswer
		if err := c.do(ctx, http.MethodPost, pathAppRoleLogin, nil, "", body, &out, true); err != nil {
			return err
		}
		if out.Auth.ClientToken == "" {
			return kindErr(ErrUnreachable, errors.New("the login answer carries no token"))
		}
		c.setToken(out.Auth.ClientToken, out.Auth.LeaseDuration, out.Auth.Renewable)
		return nil
	case AuthToken:
		if c.auth.Token == "" {
			return kindErr(ErrDenied, nil)
		}
		var out struct {
			Data struct {
				TTL       int  `json:"ttl"`
				Renewable bool `json:"renewable"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodGet, pathLookupSelf, nil, c.auth.Token, nil, &out, true); err != nil {
			return err
		}
		c.setToken(c.auth.Token, out.Data.TTL, out.Data.Renewable)
		return nil
	}
	return kindErr(ErrDenied, fmt.Errorf("unknown login method %q", c.auth.Method))
}

type authAnswer struct {
	Auth struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int    `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
}

func (c *Client) setToken(token string, ttlSeconds int, renewable bool) {
	c.token, c.renewable = token, renewable
	c.expires = time.Time{}
	if ttlSeconds > 0 {
		c.expires = c.now().Add(time.Duration(ttlSeconds) * time.Second)
	}
}

// Renew extends the client's token (POST /v1/auth/token/renew-self).
func (c *Client) Renew(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.renewLocked(ctx)
}

func (c *Client) renewLocked(ctx context.Context) error {
	if c.token == "" {
		return kindErr(ErrDenied, errors.New("not logged in"))
	}
	var out authAnswer
	if err := c.do(ctx, http.MethodPost, pathRenewSelf, nil, c.token, []byte("{}"), &out, true); err != nil {
		return err
	}
	if out.Auth.ClientToken != "" {
		c.token = out.Auth.ClientToken
	}
	c.setToken(c.token, out.Auth.LeaseDuration, out.Auth.Renewable)
	return nil
}

// tokenFor returns a usable token: it renews a renewable token in the last
// third of its lifetime and logs in again when it expired.
func (c *Client) tokenFor(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		if err := c.loginLocked(ctx); err != nil {
			return "", err
		}
		return c.token, nil
	}
	if c.expires.IsZero() {
		return c.token, nil
	}
	left := c.expires.Sub(c.now())
	switch {
	case left <= 0:
		if err := c.loginLocked(ctx); err != nil {
			return "", err
		}
	case c.renewable && left < time.Minute:
		if err := c.renewLocked(ctx); err != nil && c.auth.Method == AuthAppRole {
			// A renewal that failed: a new login.
			if lerr := c.loginLocked(ctx); lerr != nil {
				return "", lerr
			}
		}
	}
	return c.token, nil
}

// Read reads the entry path below the KV v2 mount (GET
// /v1/<mount>/data/<path>?version=); version 0 reads the latest.
func (c *Client) Read(ctx context.Context, mount, path string, version int) (*Secret, error) {
	mount, path = strings.Trim(mount, "/"), strings.Trim(path, "/")
	if mount == "" || path == "" {
		return nil, kindErr(ErrNotFound, nil)
	}
	query := url.Values{}
	if version > 0 {
		query.Set("version", strconv.Itoa(version))
	}
	var secret *Secret
	read := func(token string) error {
		var out struct {
			Data struct {
				Data     map[string]any `json:"data"`
				Metadata struct {
					Version int `json:"version"`
				} `json:"metadata"`
			} `json:"data"`
			LeaseDuration int `json:"lease_duration"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/"+mount+"/data/"+path, query, token, nil, &out, false); err != nil {
			return err
		}
		if out.Data.Data == nil {
			// A deleted or destroyed version answers without data.
			return kindErr(ErrNotFound, nil)
		}
		data := make(map[string]string, len(out.Data.Data))
		for k, v := range out.Data.Data {
			if s, ok := v.(string); ok {
				data[k] = s
			}
		}
		secret = &Secret{Data: data, Version: out.Data.Metadata.Version,
			LeaseDuration: time.Duration(out.LeaseDuration) * time.Second}
		return nil
	}
	token, err := c.tokenFor(ctx)
	if err != nil {
		return nil, err
	}
	err = read(token)
	if errors.Is(err, ErrDenied) && c.tokenRejected(ctx, token) && c.relogin(ctx, token) {
		// The token was revoked or expired early: one new login. A path
		// the valid token may not read stays denied without a login (a
		// new AppRole login per read would issue tokens and use up a
		// secret id limited in its uses).
		if token, err = c.tokenFor(ctx); err == nil {
			err = read(token)
		}
	}
	if err != nil {
		return nil, err
	}
	return secret, nil
}

// tokenRejected reports whether the vault refuses token itself (GET
// /v1/auth/token/lookup-self answers 401 or 403), not only the path read
// with it.
func (c *Client) tokenRejected(ctx context.Context, token string) bool {
	var out json.RawMessage
	return errors.Is(c.do(ctx, http.MethodGet, pathLookupSelf, nil, token, nil, &out, false), ErrDenied)
}

// relogin drops token for an AppRole login so the next tokenFor logs in;
// a fixed token cannot be replaced.
func (c *Client) relogin(_ context.Context, token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.auth.Method != AuthAppRole || c.token != token {
		return false
	}
	c.token = ""
	return true
}

// do sends one request and decodes a 2xx answer into out. A 400 of a login
// (invalid role or secret id) counts as denied.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, token string, body []byte, out any, login bool) error {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return kindErr(ErrUnreachable, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.namespace)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, ErrMethodNotAllowed) {
			return ErrMethodNotAllowed
		}
		return kindErr(ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return kindErr(ErrUnreachable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden,
		login && resp.StatusCode == http.StatusBadRequest:
		return kindErr(ErrDenied, nil)
	case resp.StatusCode == http.StatusNotFound:
		return kindErr(ErrNotFound, nil)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return kindErr(ErrUnreachable, fmt.Errorf("status %d", resp.StatusCode))
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return kindErr(ErrUnreachable, errors.New("the answer is not KV v2 JSON"))
	}
	return nil
}

// MapFields maps the fields of an entry onto the fields of a credential
// kind: fieldMap names, per field of the kind, the field of the entry. A
// field the entry lacks (or holds empty) is ErrFieldMissing; the error
// names the field of the kind, never a value.
func MapFields(data, fieldMap map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(fieldMap))
	for field, source := range fieldMap {
		v, ok := data[source]
		if !ok || v == "" {
			return nil, kindErr(ErrFieldMissing, fmt.Errorf("field %s", field))
		}
		out[field] = v
	}
	return out, nil
}

// --- cache (AK2) -----------------------------------------------------------

// CacheKey identifies a value read: the tenant (the server keys by
// organization, the collector leaves it empty), the connection, the path,
// the version asked for (0: the latest) and Source, the Fingerprint of what
// the value was read with (address, namespace, CA, mount and login). A
// changed mount or login is another entry: a value read before the change
// is never served for it.
type CacheKey struct {
	Tenant     string
	Connection string
	Path       string
	Version    int
	Source     string
}

// Fingerprint returns a digest of parts for CacheKey.Source; it keeps no
// login value readable in the key.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type cacheEntry struct {
	fields  map[string][]byte
	version int
	expires time.Time
}

// Cache keeps values read in memory for a TTL: DefaultTTL, at most the
// lease of the answer. An expired entry, a replaced one and every entry on
// Close are overwritten before they are dropped. It is safe for concurrent
// use.
type Cache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[CacheKey]*cacheEntry
}

// NewCache returns a cache with ttl (zero: DefaultTTL) and the clock now
// (nil: time.Now).
func NewCache(ttl time.Duration, now func() time.Time) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Cache{ttl: ttl, now: now, entries: map[CacheKey]*cacheEntry{}}
}

// Get returns a copy of the cached entry of k while it is fresh.
func (c *Cache) Get(k CacheKey) (*Secret, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return nil, false
	}
	if !c.now().Before(e.expires) {
		wipe(e)
		delete(c.entries, k)
		return nil, false
	}
	data := make(map[string]string, len(e.fields))
	for name, v := range e.fields {
		data[name] = string(v)
	}
	return &Secret{Data: data, Version: e.version}, true
}

// Put caches s under k for the TTL, at most its lease.
func (c *Cache) Put(k CacheKey, s *Secret) {
	ttl := c.ttl
	if s.LeaseDuration > 0 && s.LeaseDuration < ttl {
		ttl = s.LeaseDuration
	}
	fields := make(map[string][]byte, len(s.Data))
	for name, v := range s.Data {
		fields[name] = []byte(v)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[k]; ok {
		wipe(old)
	}
	c.entries[k] = &cacheEntry{fields: fields, version: s.Version, expires: c.now().Add(ttl)}
}

// Expires returns when the entry of k expires (false: none cached).
func (c *Cache) Expires(k CacheKey) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return time.Time{}, false
	}
	return e.expires, true
}

// Delete overwrites and drops the entry of k.
func (c *Cache) Delete(k CacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[k]; ok {
		wipe(e)
		delete(c.entries, k)
	}
}

// DeleteFunc overwrites and drops every entry whose key del reports (a
// reference or connection that left the bundle or changed).
func (c *Cache) DeleteFunc(del func(CacheKey) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if del(k) {
			wipe(e)
			delete(c.entries, k)
		}
	}
}

// Sweep overwrites and drops every expired entry, also those nobody asks
// for again.
func (c *Cache) Sweep() {
	now := c.now()
	c.DeleteFunc(func(k CacheKey) bool { return !now.Before(c.entries[k].expires) })
}

// Close overwrites and drops every entry.
func (c *Cache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		wipe(e)
		delete(c.entries, k)
	}
}

// Len returns the number of entries (fresh or not).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func wipe(e *cacheEntry) {
	for _, v := range e.fields {
		clear(v)
	}
	clear(e.fields)
}
