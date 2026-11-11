package collectorcmd

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/vault"
)

// Vault references of the bundle (CRD-04, WP-895, E-220): a credential
// with source vault_ref names an entry of a vault connection with
// resolve_at collector; the bundle carries the reference and the stored
// login of the connection, never a value of the vault. The collector logs
// in, reads the entry (edgecore/vault, read only), maps its fields onto
// the fields of the kind (field_map) and keeps the result in memory in the
// bundle cache, under the namespace of the kind, so credentialsFor and
// credentialsForTarget work unchanged. Values are cached per (connection,
// path, version and a fingerprint of address, namespace, CA, mount and
// login) for the TTL (300 s, at most the lease) and read again once it
// expired; nothing reaches the disk. Every pass overwrites the expired
// entries and those of references that left the bundle and closes the
// clients of connections no reference names any more. A failure disables
// only the affected credential and is retried once vaultFailureBackoff
// passed (or the reference, its connection or login changed); the log
// names its class, the connection and the path, never a value, token or
// secret id.

// vaultRefreshCheck is how often the collector looks for references whose
// values expired (tests shorten it).
var vaultRefreshCheck = 30 * time.Second

// vaultTestPath is the entry a connection test reads when the request
// names none (WP-896).
const vaultTestPath = "reticora-test"

// vaultFailureBackoff is how long a failed reference is not read again
// (the TTL of a value): a denied path or an unreachable vault is not asked
// every vaultRefreshCheck.
var vaultFailureBackoff = vault.DefaultTTL

// vaultTestSuffix is the subject of the connection test below the
// collector's subject tree (collector.<org>.<id>.vault.test, WP-896).
const vaultTestSuffix = ".vault.test"

// vaultReplyInfix: the server's reply subject of a connection test lies in
// the collector's own subject tree (collector.<org>.<id>.vault.reply.<n>),
// the only tree the endpoint lets the collector publish to; a reply
// subject outside it is not answered.
const vaultReplyInfix = ".vault.reply."

// vaultTestBudget bounds the collector's answer to a connection test; it
// stays below the server's wait for one collector (15 s, WP-896).
var vaultTestBudget = 12 * time.Second

// vaultResult is the outcome of a vault access: "ok" or an error class.
const vaultResultOK = "ok"

// vaultResolver resolves the vault references of the bundle.
type vaultResolver struct {
	cache *vault.Cache

	mu      sync.Mutex
	clients map[string]*vaultClient         // by connection id
	failed  map[vault.CacheKey]vaultFailure // failed reads until their retry
	now     func() time.Time
}

// vaultFailure is a failed read and when it is tried again.
type vaultFailure struct {
	err   error
	until time.Time
}

// vaultClient is the logged-in client of a connection and what it was
// built from; a change of either builds a new one.
type vaultClient struct {
	conn   bundleVaultConnection
	auth   vault.Auth
	client *vault.Client
}

var collectorVault = newVaultResolver()

func newVaultResolver() *vaultResolver {
	return &vaultResolver{cache: vault.NewCache(vault.DefaultTTL, nil), clients: map[string]*vaultClient{},
		failed: map[vault.CacheKey]vaultFailure{}, now: time.Now}
}

// close drops the clients and overwrites the cached values (the collector
// shuts down).
func (r *vaultResolver) close() {
	r.mu.Lock()
	for id, c := range r.clients {
		c.client.Close()
		delete(r.clients, id)
	}
	clear(r.failed)
	r.mu.Unlock()
	r.cache.Close()
}

// cacheKey is the key of a reference: connection, path, version and the
// fingerprint of what it is read with.
func (job *vaultRefJob) cacheKey() vault.CacheKey {
	conn := &job.ref.bundleVaultConnection
	auth := vaultAuth(conn, &job.login)
	return vault.CacheKey{Connection: conn.ConnectionID, Path: job.ref.Path, Version: refVersion(&job.ref),
		Source: vault.Fingerprint(conn.Kind, conn.Address, conn.Namespace, conn.CAPEM, conn.Mount, conn.AuthMethod,
			conn.LoginID, auth.RoleID, auth.SecretID, auth.Token)}
}

// prune overwrites the cached values and forgets the failures of entries
// no reference of the bundle names (any more) and the expired ones, and
// closes the clients of connections no reference names.
func (r *vaultResolver) prune(jobs []vaultRefJob) {
	live := make(map[vault.CacheKey]bool, len(jobs))
	conns := map[string]bool{}
	for i := range jobs {
		live[jobs[i].cacheKey()] = true
		conns[jobs[i].ref.ConnectionID] = true
	}
	r.cache.DeleteFunc(func(k vault.CacheKey) bool { return !live[k] })
	r.cache.Sweep()
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.failed {
		if !live[k] {
			delete(r.failed, k)
		}
	}
	for id, c := range r.clients {
		if !conns[id] {
			c.client.Close()
			delete(r.clients, id)
		}
	}
}

// vaultAuth returns the login of conn from its stored login credential.
func vaultAuth(conn *bundleVaultConnection, login *bundleCredential) vault.Auth {
	str := func(k string) string {
		s, _ := login.Secret[k].(string)
		return s
	}
	return vault.Auth{Method: conn.AuthMethod, RoleID: str("role_id"), SecretID: str("secret_id"), Token: str("token")}
}

// clientFor returns the client of conn logged in with auth, reusing the
// last one while connection and login are the same.
func (r *vaultResolver) clientFor(ctx context.Context, conn *bundleVaultConnection, auth vault.Auth) (*vault.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[conn.ConnectionID]; ok {
		if c.conn == *conn && c.auth == auth {
			return c.client, nil
		}
		c.client.Close()
		delete(r.clients, conn.ConnectionID)
	}
	client, err := newVaultClient(conn)
	if err != nil {
		return nil, err
	}
	if err = client.Login(ctx, auth); err != nil {
		client.Close()
		return nil, err
	}
	r.clients[conn.ConnectionID] = &vaultClient{conn: *conn, auth: auth, client: client}
	return client, nil
}

func newVaultClient(conn *bundleVaultConnection) (*vault.Client, error) {
	client, err := vault.New(vault.Config{Address: conn.Address, Namespace: conn.Namespace, CAPEM: conn.CAPEM})
	if err != nil {
		// An address or CA the client refuses: the vault is not reachable
		// with it.
		return nil, &vault.Error{Kind: vault.ErrUnreachable, Cause: err}
	}
	return client, nil
}

// vaultRefJob is a reference of the bundle to resolve.
type vaultRefJob struct {
	id    string
	ref   bundleVaultRef
	login bundleCredential
	found bool // the login is in the bundle
}

// vaultRefs returns the vault references of the bundle with their logins.
func (c *bundleCache) vaultRefs() []vaultRefJob {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var jobs []vaultRefJob
	for id, cred := range c.creds {
		if cred.VaultRef == nil {
			continue
		}
		login, found := c.creds[cred.VaultRef.LoginID]
		jobs = append(jobs, vaultRefJob{id: id, ref: *cred.VaultRef, login: login, found: found})
	}
	return jobs
}

// setResolved stores the values of a reference (nil: it failed, the
// credential is disabled) while the bundle still carries the same
// reference.
func (c *bundleCache) setResolved(id string, ref *bundleVaultRef, values map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cred, ok := c.creds[id]
	if !ok || !sameVaultRef(cred.VaultRef, ref) {
		return
	}
	secret := make(map[string]any, len(values))
	for k, v := range values {
		secret[k] = v
	}
	cred.Secret = secret
	c.creds[id] = cred
}

// vaultConnection returns a connection of the bundle and its login: from
// the connections of the bundle, else from a reference to it.
func (c *bundleCache) vaultConnection(id string) (bundleVaultConnection, bundleCredential, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	conn, ok := c.vaults[id]
	if !ok {
		for _, cred := range c.creds {
			if cred.VaultRef != nil && cred.VaultRef.ConnectionID == id {
				conn, ok = cred.VaultRef.bundleVaultConnection, true
				break
			}
		}
	}
	if !ok {
		return conn, bundleCredential{}, false
	}
	login, found := c.creds[conn.LoginID]
	return conn, login, found
}

// sameVaultRef reports whether two references name the same entry the same
// way.
func sameVaultRef(a, b *bundleVaultRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.bundleVaultConnection != b.bundleVaultConnection || a.Path != b.Path || !maps.Equal(a.FieldMap, b.FieldMap) {
		return false
	}
	return refVersion(a) == refVersion(b)
}

func refVersion(ref *bundleVaultRef) int {
	if ref.Version == nil {
		return 0
	}
	return *ref.Version
}

// stringValues returns the string members of a secret.
func stringValues(secret map[string]any) map[string]string {
	out := make(map[string]string, len(secret))
	for k, v := range secret {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// resolveBundle resolves every vault reference of the bundle whose values
// are not cached (any longer) and returns the outcome by credential id:
// "ok" or the error class.
func (r *vaultResolver) resolveBundle(ctx context.Context, bundle *bundleCache) map[string]string {
	out := map[string]string{}
	jobs := bundle.vaultRefs()
	r.prune(jobs)
	// A connection whose login failed in this pass is not asked again for
	// its other references (an unreachable vault: one timeout, not one per
	// reference).
	logins := map[string]error{}
	for _, job := range jobs {
		values, err := r.resolve(ctx, &job, logins)
		if err != nil {
			class := vault.Class(err)
			if class == "" {
				class = vault.ClassUnreachable
			}
			out[job.id] = class
			bundle.setResolved(job.id, &job.ref, nil)
			slog.Warn("vault reference not resolved: the credential is not used", "credential_id", job.id,
				"connection_id", job.ref.ConnectionID, "path", job.ref.Path, "error_class", class)
			continue
		}
		out[job.id] = vaultResultOK
		bundle.setResolved(job.id, &job.ref, values)
	}
	return out
}

// resolve returns the values of one reference mapped onto the fields of
// its kind, from the cache or read from the vault.
func (r *vaultResolver) resolve(ctx context.Context, job *vaultRefJob, logins map[string]error) (map[string]string, error) {
	if !job.found {
		return nil, &vault.Error{Kind: vault.ErrDenied}
	}
	key := job.cacheKey()
	secret, cached := r.cache.Get(key)
	if !cached {
		if err := r.failedBefore(key); err != nil {
			return nil, err
		}
		err, loginFailed := logins[key.Source]
		var client *vault.Client
		if !loginFailed {
			if client, err = r.clientFor(ctx, &job.ref.bundleVaultConnection, vaultAuth(&job.ref.bundleVaultConnection, &job.login)); err != nil {
				logins[key.Source] = err
			}
		}
		if err == nil {
			secret, err = client.Read(ctx, job.ref.Mount, job.ref.Path, key.Version)
		}
		if err != nil {
			r.fail(key, err)
			return nil, err
		}
		r.cache.Put(key, secret)
	}
	return vault.MapFields(secret.Data, job.ref.FieldMap)
}

// failedBefore returns the error of a failed read of key while its retry
// is not due.
func (r *vaultResolver) failedBefore(key vault.CacheKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.failed[key]
	if !ok {
		return nil
	}
	if !r.now().Before(f.until) {
		delete(r.failed, key)
		return nil
	}
	return f.err
}

// fail records a failed read of key; it is tried again after
// vaultFailureBackoff.
func (r *vaultResolver) fail(key vault.CacheKey, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed[key] = vaultFailure{err: err, until: r.now().Add(vaultFailureBackoff)}
}

// vaultTestRequest is the connection test the server sends (WP-896).
type vaultTestRequest struct {
	ConnectionID string `json:"connection_id"`
	Path         string `json:"path,omitempty"`
}

// vaultTestReply is the answer: "ok" or the error class, nothing else.
type vaultTestReply struct {
	Result string `json:"result"`
}

// testVaultConnection logs in to a connection of the bundle and reads the
// test entry; it returns "ok" or the error class. A connection the bundle
// does not carry is unreachable for this collector.
func testVaultConnection(ctx context.Context, bundle *bundleCache, req vaultTestRequest) string {
	conn, login, found := bundle.vaultConnection(req.ConnectionID)
	if !found {
		slog.Warn("vault connection test: the connection is not in the credential bundle", "connection_id", req.ConnectionID)
		return vault.ClassUnreachable
	}
	path := req.Path
	if path == "" {
		path = vaultTestPath
	}
	// A fresh client: the test checks the login, not a cached token.
	client, err := newVaultClient(&conn)
	if err == nil {
		defer client.Close()
		if err = client.Login(ctx, vaultAuth(&conn, &login)); err == nil {
			_, err = client.Read(ctx, conn.Mount, path, 0)
		}
	}
	if err != nil {
		class := vault.Class(err)
		if class == "" {
			class = vault.ClassUnreachable
		}
		slog.Warn("vault connection test failed", "connection_id", conn.ConnectionID, "path", path, "error_class", class)
		return class
	}
	return vaultResultOK
}

// serveVaultTests answers the connection tests of the server on the
// collector's subject tree (AK5 of WP-895). The endpoint lets a collector
// publish only below its own tree (backend/internal/discovery/natsedge.go,
// UserClaims), so the server's reply subject lies there as well
// (collector.<org>.<id>.vault.reply.<n>); a request with another reply
// subject is not answered.
func serveVaultTests(ctx context.Context, nc *nats.Conn, subject string) (*nats.Subscription, error) {
	return nc.Subscribe(subject+vaultTestSuffix, func(msg *nats.Msg) {
		if !strings.HasPrefix(msg.Reply, subject+vaultReplyInfix) {
			slog.Warn("vault connection test: the reply subject is outside the collector's subject tree; not answered")
			return
		}
		var req vaultTestRequest
		result := vault.ClassUnreachable
		if err := json.Unmarshal(msg.Data, &req); err == nil && req.ConnectionID != "" {
			tctx, cancel := context.WithTimeout(ctx, vaultTestBudget)
			result = testVaultConnection(tctx, collectorBundle, req)
			cancel()
		}
		raw, _ := json.Marshal(vaultTestReply{Result: result})
		if err := msg.Respond(raw); err != nil {
			slog.Warn("vault connection test: reply failed", "error", err)
		}
	})
}
