package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/vault"
)

// Resolution of vault references on the server (CRD-03, CRD-04, COL-03 P3
// part; WP-896, E-220, E-221): a reference of a connection with resolve_at
// server is read by the server, in memory, when a server connector asks
// for the secret (Decrypt; IGA today, ADMP later). The client is the
// read-only KV v2 client of edgecore/vault over the egress transport
// (SEC-08): a private destination without RETICORA_EGRESS_ALLOW_PRIVATE is
// refused before a connection opens. Values are cached per organization
// and (connection, path, version) with the TTL rule of the collector (300
// s, at most the lease); no value reaches the database, a log line or an
// error text. References of connections with resolve_at collector are
// never read here: the collectors resolve them (WP-895) and the bundle
// never asks the resolver (bundle.go).

// Error classes of a server-side resolution (AK2).
var (
	ErrVaultUnreachable = errors.New("credential: the vault is unreachable")
	ErrVaultDenied      = errors.New("credential: the vault denied the access")
	ErrVaultNotFound    = errors.New("credential: the vault entry was not found")
	ErrFieldMissing     = errors.New("credential: the vault entry lacks a field of the field map")
	ErrEgressBlocked    = errors.New("credential: the egress policy blocks the vault address")
)

// Result labels of the resolutions and tests.
const (
	VaultResultOK            = "ok"
	VaultResultCache         = "cache"
	VaultClassUnreachable    = vault.ClassUnreachable
	VaultClassDenied         = vault.ClassDenied
	VaultClassNotFound       = vault.ClassNotFound
	VaultClassFieldMissing   = vault.ClassFieldMissing
	VaultClassEgressBlocked  = "egress_blocked"
	vaultTestDefaultPath     = "reticora-test"
	vaultCollectorTestSuffix = ".vault.test"
)

// VaultResolves counts the server-side resolutions by result: ok, cache
// or an error class; it carries neither connection nor path (AK5).
var VaultResolves = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "reticora",
	Name:      "credential_vault_resolve_total",
	Help:      "Server-side resolutions of vault references (resolve_at server) by result: ok (read from the vault), cache, or the error class.",
}, []string{"result"})

// RegisterMetrics registers the credential metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(VaultResolves)
}

// vaultClassErr maps an error of edgecore/vault or the egress policy onto
// the error of its class.
func vaultClassErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, egress.ErrBlockedDestination):
		return ErrEgressBlocked
	case errors.Is(err, vault.ErrDenied):
		return ErrVaultDenied
	case errors.Is(err, vault.ErrNotFound):
		return ErrVaultNotFound
	case errors.Is(err, vault.ErrFieldMissing):
		return ErrFieldMissing
	}
	return ErrVaultUnreachable
}

// VaultErrorClass returns the class of an error of the resolution ("" for
// nil or another error).
func VaultErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEgressBlocked):
		return VaultClassEgressBlocked
	case errors.Is(err, ErrVaultDenied):
		return VaultClassDenied
	case errors.Is(err, ErrVaultNotFound):
		return VaultClassNotFound
	case errors.Is(err, ErrFieldMissing):
		return VaultClassFieldMissing
	case errors.Is(err, ErrVaultUnreachable):
		return VaultClassUnreachable
	}
	return ""
}

// VaultResolver reads vault references on the server.
type VaultResolver struct {
	egress    egress.Options
	transport *http.Transport
	cache     *vault.Cache
	nc        *nats.Conn
	// collectorTimeout bounds the answer of one collector to a test.
	collectorTimeout time.Duration

	// versionSeen learns the version of every fresh read of an entry
	// without a fixed version (the rotation of the service, WP-897).
	versionSeen func(ctx context.Context, orgID, connectionID, path string, version int)

	mu      sync.Mutex
	clients map[string]*serverVaultClient // by organization and connection
	// logins serializes the login per organization and connection, so a
	// slow vault of one organization delays only its own resolutions, not
	// those of the others (mu is never held across a network call).
	logins map[string]*sync.Mutex
}

type serverVaultClient struct {
	fingerprint string
	auth        vault.Auth
	client      *vault.Client
}

// NewVaultResolver returns the server's resolver: its HTTP transport is the
// one of the egress client of opts, nc (may be nil) carries the
// connection tests of collectors.
func NewVaultResolver(opts egress.Options, nc *nats.Conn) *VaultResolver {
	transport, _ := egress.NewClient(opts).Transport.(*http.Transport)
	return &VaultResolver{egress: opts, transport: transport, cache: vault.NewCache(vault.DefaultTTL, nil), nc: nc,
		collectorTimeout: 15 * time.Second, clients: map[string]*serverVaultClient{}, logins: map[string]*sync.Mutex{}}
}

// WithCacheTTL replaces the TTL of the values read (default 300 s, at most
// the lease); the values cached so far are overwritten.
func (r *VaultResolver) WithCacheTTL(ttl time.Duration) *VaultResolver {
	r.cache.Close()
	r.cache = vault.NewCache(ttl, nil)
	return r
}

// WithVault lets the service resolve references of connections with
// resolve_at server (WP-896) and check their addresses against the egress
// policy; copies of the service made afterwards share it.
func (s *Service) WithVault(r *VaultResolver) *Service {
	s.vault = r
	r.versionSeen = s.vaultVersionSeen
	return s
}

// Close overwrites the cached values and drops the clients.
func (r *VaultResolver) Close() {
	r.mu.Lock()
	for k, c := range r.clients {
		c.client.Close()
		delete(r.clients, k)
	}
	r.mu.Unlock()
	r.cache.Close()
}

// checkAddress applies the egress policy to the address of a connection
// (without a DNS lookup; the dial checks the resolved address).
func (r *VaultResolver) checkAddress(address string) error {
	if err := r.egress.ValidateURL(address); err != nil {
		return ErrEgressBlocked
	}
	return nil
}

// loginAuth returns the login of conn from the secret of its credential.
func loginAuth(conn *VaultConnection, login map[string]any) vault.Auth {
	str := func(k string) string {
		s, _ := login[k].(string)
		return s
	}
	return vault.Auth{Method: conn.AuthMethod, RoleID: str("role_id"), SecretID: str("secret_id"), Token: str("token")}
}

func (r *VaultResolver) newClient(conn *VaultConnection) (*vault.Client, error) {
	client, err := vault.New(vault.Config{Address: conn.Address, Namespace: conn.Namespace, CAPEM: conn.CAPEM,
		Transport: r.transport})
	if err != nil {
		return nil, ErrVaultUnreachable
	}
	return client, nil
}

// cached returns the client of key while it was built from fingerprint
// and auth; a stale one is closed and dropped.
func (r *VaultResolver) cached(key, fingerprint string, auth vault.Auth) *vault.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[key]
	if !ok {
		return nil
	}
	if c.fingerprint == fingerprint && c.auth == auth {
		return c.client
	}
	c.client.Close()
	delete(r.clients, key)
	return nil
}

// clientFor returns the logged-in client of a connection, reusing it while
// address, namespace, CA and login are the same. The login runs under the
// lock of its organization and connection only: concurrent misses of the
// same connection log in once, other connections and organizations do not
// wait for it.
func (r *VaultResolver) clientFor(ctx context.Context, orgID string, conn *VaultConnection, auth vault.Auth) (*vault.Client, error) {
	key := orgID + "/" + conn.ID
	fingerprint := strings.Join([]string{conn.Address, conn.Namespace, conn.CAPEM}, "\x00")
	if client := r.cached(key, fingerprint, auth); client != nil {
		return client, nil
	}
	r.mu.Lock()
	login, ok := r.logins[key]
	if !ok {
		login = &sync.Mutex{}
		r.logins[key] = login
	}
	r.mu.Unlock()
	login.Lock()
	defer login.Unlock()
	if client := r.cached(key, fingerprint, auth); client != nil {
		return client, nil // logged in while this call waited
	}
	client, err := r.newClient(conn)
	if err != nil {
		return nil, err
	}
	if err = client.Login(ctx, auth); err != nil {
		client.Close()
		return nil, vaultClassErr(err)
	}
	r.mu.Lock()
	r.clients[key] = &serverVaultClient{fingerprint: fingerprint, auth: auth, client: client}
	r.mu.Unlock()
	return client, nil
}

// forget overwrites the cached values of a connection and closes its
// client (the connection was changed or deleted).
func (r *VaultResolver) forget(orgID, connectionID string) {
	r.cache.DeleteFunc(func(k vault.CacheKey) bool { return k.Tenant == orgID && k.Connection == connectionID })
	key := orgID + "/" + connectionID
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		c.client.Close()
		delete(r.clients, key)
	}
}

// forgetVault drops what the resolver holds of a connection.
func (s *Service) forgetVault(orgID, connectionID string) {
	if s.vault != nil {
		s.vault.forget(orgID, connectionID)
	}
}

// vaultSource is the fingerprint of what a value of conn is read with:
// address, namespace, CA, mount and login (CacheKey.Source). A changed
// mount or login never serves a value read before the change.
func vaultSource(conn *VaultConnection, auth vault.Auth) string {
	return vault.Fingerprint(conn.Kind, conn.Address, conn.Namespace, conn.CAPEM, conn.Mount, conn.AuthMethod,
		conn.AuthCredentialID, auth.RoleID, auth.SecretID, auth.Token)
}

// resolve returns the values of ref mapped onto the fields of its kind.
func (r *VaultResolver) resolve(ctx context.Context, orgID string, conn *VaultConnection, login map[string]any,
	ref *VaultRef) (map[string]any, error) {
	values, result, err := r.read(ctx, orgID, conn, login, ref)
	if err != nil {
		result = VaultErrorClass(err)
	}
	VaultResolves.WithLabelValues(result).Inc()
	if err != nil {
		slog.WarnContext(ctx, "vault reference not resolved", "connection_id", conn.ID, "path", ref.Path,
			"error_class", VaultErrorClass(err))
		return nil, err
	}
	return values, nil
}

func (r *VaultResolver) read(ctx context.Context, orgID string, conn *VaultConnection, login map[string]any,
	ref *VaultRef) (values map[string]any, result string, err error) {
	if blocked := r.checkAddress(conn.Address); blocked != nil {
		return nil, "", blocked
	}
	version := 0
	if ref.Version != nil {
		version = *ref.Version
	}
	auth := loginAuth(conn, login)
	key := vault.CacheKey{Tenant: orgID, Connection: conn.ID, Path: ref.Path, Version: version, Source: vaultSource(conn, auth)}
	result = VaultResultCache
	secret, cached := r.cache.Get(key)
	if !cached {
		// Expired values nobody asks for again are overwritten as well.
		r.cache.Sweep()
		client, cerr := r.clientFor(ctx, orgID, conn, auth)
		if cerr != nil {
			return nil, "", cerr
		}
		if secret, err = client.Read(ctx, conn.Mount, ref.Path, version); err != nil {
			return nil, "", vaultClassErr(err)
		}
		r.cache.Put(key, secret)
		result = VaultResultOK
		// A value without a fixed version may have rotated (WP-897).
		if version == 0 && r.versionSeen != nil {
			r.versionSeen(ctx, orgID, conn.ID, ref.Path, secret.Version)
		}
	}
	mapped, err := vault.MapFields(secret.Data, ref.FieldMap)
	if err != nil {
		return nil, "", ErrFieldMissing
	}
	values = make(map[string]any, len(mapped))
	for k, v := range mapped {
		values[k] = v
	}
	return values, result, nil
}

// testServer logs in to conn with a fresh client and reads the test entry
// path; it returns ok or the error class.
func (r *VaultResolver) testServer(ctx context.Context, conn *VaultConnection, login map[string]any, path string) string {
	if err := r.checkAddress(conn.Address); err != nil {
		return VaultClassEgressBlocked
	}
	client, err := r.newClient(conn)
	if err == nil {
		defer client.Close()
		if err = client.Login(ctx, loginAuth(conn, login)); err == nil {
			_, err = client.Read(ctx, conn.Mount, path, 0)
		}
	}
	if err != nil {
		return VaultErrorClass(vaultClassErr(err))
	}
	return VaultResultOK
}

// testCollectors asks the collectors of conn in turn
// (collector.<org>.<id>.vault.test) and returns the answer of the first
// that replies; none replying is vault_unreachable. The reply subject lies
// in the collector's own subject tree (RequestCollector). Each collector
// gets at most collectorTimeout and never more than the time left before
// the deadline of ctx (the job's 60 s) minus a reserve, so the job always
// ends with ok or an error class, not with its timeout.
func (r *VaultResolver) testCollectors(ctx context.Context, orgID string, conn *VaultConnection, path string) (result, collectorID string) {
	if r.nc == nil {
		slog.WarnContext(ctx, "vault connection test: no NATS connection to reach the collectors", "connection_id", conn.ID)
		return VaultClassUnreachable, ""
	}
	req, _ := json.Marshal(map[string]string{"connection_id": conn.ID, "path": path})
	for _, id := range conn.CollectorIDs {
		wait := r.collectorTimeout
		if deadline, ok := ctx.Deadline(); ok {
			wait = min(wait, time.Until(deadline)-vaultTestReserve)
		}
		if wait <= 0 {
			slog.WarnContext(ctx, "vault connection test: no time left to ask further collectors", "connection_id", conn.ID)
			break
		}
		rctx, cancel := context.WithTimeout(ctx, wait)
		msg, err := RequestCollector(rctx, r.nc, orgID, id, vaultCollectorTestSuffix, req)
		cancel()
		if err != nil {
			continue
		}
		var reply struct {
			Result string `json:"result"`
		}
		if json.Unmarshal(msg.Data, &reply) != nil || !knownVaultResult(reply.Result) {
			continue
		}
		return reply.Result, id
	}
	return VaultClassUnreachable, ""
}

// vaultTestReserve is the time a connection test keeps before the deadline
// of its job to report its result.
const vaultTestReserve = 2 * time.Second

// collectorReplyInfix: the reply subject of a request to a collector lies
// in the collector's subject tree, collector.<org>.<id>.vault.reply.<n>.
// The collector NATS endpoint lets a collector publish only below its own
// tree (discovery.EdgeIdentity.UserClaims); a reply to the server's _INBOX
// would be refused. The collector answers only reply subjects of this form
// (collector/collectorcmd/vault.go, serveVaultTests).
const collectorReplyInfix = ".vault.reply."

// errNoResponders: no collector subscription received the request.
var errNoResponders = errors.New("no collector answered")

// RequestCollector sends data to collector.<org>.<id><suffix> with a reply
// subject in the collector's own subject tree and waits for the answer
// until ctx ends. A request nobody subscribes to fails at once.
func RequestCollector(ctx context.Context, nc *nats.Conn, orgID, collectorID, suffix string, data []byte) (*nats.Msg, error) {
	tree := "collector." + orgID + "." + collectorID
	reply := tree + collectorReplyInfix + strings.TrimPrefix(nats.NewInbox(), "_INBOX.")
	sub, err := nc.SubscribeSync(reply)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if perr := nc.PublishRequest(tree+suffix, reply, data); perr != nil {
		return nil, perr
	}
	msg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	// The server's "no responders" status (503, no payload).
	if len(msg.Data) == 0 && msg.Header.Get("Status") == "503" {
		return nil, errNoResponders
	}
	return msg, nil
}

func knownVaultResult(s string) bool {
	switch s {
	case VaultResultOK, VaultClassUnreachable, VaultClassDenied, VaultClassNotFound, VaultClassFieldMissing:
		return true
	}
	return false
}

// --- connection test (AK4) -------------------------------------------------

// VaultTestJobKind is the job kind of a connection test.
const VaultTestJobKind = "vault.test"

// VaultTestParams are the parameters of a job vault.test: no secret.
type VaultTestParams struct {
	ConnectionID string `json:"connection_id"`
	Path         string `json:"path"`
}

// VaultTestResult is the result of a job vault.test: ok or the error
// class, and the collector that answered.
type VaultTestResult struct {
	Result      string `json:"result"`
	ResolveAt   string `json:"resolve_at"`
	CollectorID string `json:"collector_id,omitempty"`
}

// errVaultTestUnavailable: the server has no resolver.
var errVaultTestUnavailable = errors.New("vault connection tests are not available")

// TestVaultConnection tests a connection: the server logs in and reads the
// test entry itself (resolve_at server) or asks a collector of the
// connection (resolve_at collector). ctx carries the tenant scope.
func (s *Service) TestVaultConnection(ctx context.Context, orgID string, p VaultTestParams) (*VaultTestResult, error) {
	if s.vault == nil {
		return nil, errVaultTestUnavailable
	}
	conn, err := s.vaults.Get(ctx, orgID, p.ConnectionID)
	if err != nil {
		return nil, err
	}
	path := strings.Trim(strings.TrimSpace(p.Path), "/")
	if path == "" {
		path = vaultTestDefaultPath
	}
	out := &VaultTestResult{ResolveAt: conn.ResolveAt}
	if conn.ResolveAt == ResolveAtCollector {
		out.Result, out.CollectorID = s.vault.testCollectors(ctx, orgID, conn, path)
		return out, nil
	}
	login, err := s.Decrypt(ctx, orgID, conn.AuthCredentialID)
	if err != nil {
		return nil, fmt.Errorf("vault connection %s: login: %w", conn.ID, err)
	}
	out.Result = s.vault.testServer(ctx, conn, login, path)
	return out, nil
}
