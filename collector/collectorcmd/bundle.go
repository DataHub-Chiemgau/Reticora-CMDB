package collectorcmd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// Credential bundle (COL-02): the collector fetches the credentials of its
// scopes over its NATS channel, encrypted for its own key (counterpart:
// backend/internal/credential/bundle.go). The plaintext is kept only in
// memory, by credential ID; it is never written to disk or logged. The pass
// of a scope uses only the credentials its credential_ids name (DIS-09).
// Format 2 (WP-891, WP-892, CRD-04) adds the targets: the credential of a
// single device and namespace from a level above the scope (probe, ci,
// device_group, subnet), resolved by the server in a scope of the
// collector; the collector keeps them per scope by management address and
// CI id, applies them only in the pass of their scope and prefers them per
// namespace over the scope's list (credentialsForTarget).

// bundleRefresh is how often the collector fetches its bundle again.
const bundleRefresh = 15 * time.Minute

type sealedBundle struct {
	Version      int    `json:"version"`
	CollectorID  string `json:"collector_id"`
	EphemeralKey []byte `json:"ephemeral_key"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ciphertext"`
}

// bundleCredential is one credential of the bundle. Source is "stored"
// (or empty, before WP-894) for a secret, "vault_ref" for a reference into
// a vault connection without secret (VaultRef). The collector resolves a
// reference in memory (vault.go, WP-895) and keeps the values in Secret;
// until then, and after a failure, Secret is empty.
type bundleCredential struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Source   string          `json:"source,omitempty"`
	Secret   map[string]any  `json:"secret"`
	VaultRef *bundleVaultRef `json:"vault_ref,omitempty"`
}

// sourceVaultRef is the source of a vault reference in the bundle.
const sourceVaultRef = "vault_ref"

// bundleVaultConnection is what the collector needs to log in to a vault
// connection: the connection and the id of its stored login in the bundle
// (vault_approle or vault_token).
type bundleVaultConnection struct {
	ConnectionID string `json:"connection_id"`
	Kind         string `json:"kind"`
	Address      string `json:"address"`
	Namespace    string `json:"namespace,omitempty"`
	Mount        string `json:"mount"`
	CAPEM        string `json:"ca_pem,omitempty"`
	AuthMethod   string `json:"auth_method"`
	LoginID      string `json:"login_id"`
}

// bundleVaultRef is the reference of a vault_ref credential (WP-894): the
// connection, the entry (path, optional version) and the map of the kind's
// fields to fields of the entry.
type bundleVaultRef struct {
	bundleVaultConnection
	Path     string            `json:"path"`
	Version  *int              `json:"version,omitempty"`
	FieldMap map[string]string `json:"field_map"`
}

// bundleTarget is an entry of the targets of a bundle (format 2): the
// credential of a CI in a namespace and its origin (probe, ci,
// device_group, subnet) as the server resolved it in the scope ScopeID. The
// collector knows no device groups; it takes the origin as it is. An entry
// applies only in the pass of its scope: the same address may be another
// device in another scope (client, VRF).
type bundleTarget struct {
	CIID         string `json:"ci_id"`
	ScopeID      string `json:"scope_id"`
	ManagementIP string `json:"management_ip"`
	Namespace    string `json:"namespace"`
	CredentialID string `json:"credential_id"`
	Origin       string `json:"origin"`
}

// bundleContent is the plaintext of a bundle; a format 1 bundle has no
// targets. VaultConnections are the connections with resolve_at collector
// this collector is assigned to (WP-896), for the connection test; their
// logins are entries of Credentials.
type bundleContent struct {
	Credentials      []bundleCredential      `json:"credentials"`
	Targets          []bundleTarget          `json:"targets"`
	VaultConnections []bundleVaultConnection `json:"vault_connections,omitempty"`
}

// openBundle decrypts a bundle with the collector's private key.
func openBundle(b *sealedBundle, keyPEM string) (*bundleContent, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("bundle: no collector key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("bundle: collector key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("bundle: the collector key is not ECDSA")
	}
	priv, err := key.ECDH()
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.P256().NewPublicKey(b.EphemeralKey)
	if err != nil {
		return nil, fmt.Errorf("bundle: ephemeral key: %w", err)
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return nil, err
	}
	k, err := hkdf.Key(sha256.New, shared, b.EphemeralKey, "reticora/credential-bundle/v1|"+b.CollectorID, 32)
	if err != nil {
		return nil, err
	}
	aead, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(aead)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, b.Nonce, b.Ciphertext, []byte("reticora-credential-bundle|"+b.CollectorID))
	if err != nil {
		return nil, fmt.Errorf("bundle: decrypt: %w", err)
	}
	var content bundleContent
	if err = json.Unmarshal(plain, &content); err != nil {
		return nil, err
	}
	return &content, nil
}

// credentialNamespace maps a credential kind to the protocol namespace the
// plugins read (plugins.CredentialsFor). testdata/credential-kinds.json is
// the contract with the kinds of the backend (WP-378).
var credentialNamespace = map[string]string{
	"snmp_v2c": "snmp", "snmp_v3": "snmp", "ssh_password": "ssh", "ssh_key": "ssh",
	"redfish": "redfish", "ipmi": "ipmi", "wmi": "wmi", "api_token": "api", "nut": "nut",
	"nas": "nas",
	// The logins of vault connections (WP-893): no plugin reads them, the
	// resolver of the vault references (WP-895) does.
	"vault_approle": "vault", "vault_token": "vault",
	// The kinds of the probes (PRT-22, WP-967), each under its own
	// namespace without alias: no discovery plugin reads them, the probe
	// executors of their probe types (testdata/credential-kinds.json
	// probe_types) do.
	"http": "http", "gnmi": "gnmi", "acs": "acs",
}

// bundleCache holds the credentials of the last bundle in memory, by ID,
// and the targets of the bundle per scope.
type bundleCache struct {
	mu     sync.RWMutex
	creds  map[string]bundleCredential
	scopes map[string]*scopeBundleTargets
	vaults map[string]bundleVaultConnection
}

// scopeBundleTargets are the targets of one scope by management address and by
// CI id.
type scopeBundleTargets struct {
	byIP map[string][]bundleTarget
	byCI map[string][]bundleTarget
}

var collectorBundle = &bundleCache{}

// set replaces the cached credentials and the target indexes by those of
// content in one step (nil clears them; format 1 leaves the indexes empty).
// A vault reference that is unchanged, with an unchanged login, keeps the
// values resolved for it (WP-895) until the resolver reads it again. A
// target without a scope applies nowhere. An address the targets of a
// scope name for two CIs is ambiguous and left out of that scope's address
// index; its devices resolve per scope (or by CI id).
func (c *bundleCache) set(content *bundleContent) {
	byID := map[string]bundleCredential{}
	scopes := map[string]*scopeBundleTargets{}
	vaults := map[string]bundleVaultConnection{}
	if content != nil {
		for _, cred := range content.Credentials {
			byID[cred.ID] = cred
		}
		for _, conn := range content.VaultConnections {
			vaults[conn.ConnectionID] = conn
		}
		type scopeIP struct{ scope, ip string }
		ipOwner := map[scopeIP]string{}
		ambiguous := map[scopeIP]bool{}
		for _, tgt := range content.Targets {
			if tgt.ScopeID == "" {
				continue
			}
			st := scopes[tgt.ScopeID]
			if st == nil {
				st = &scopeBundleTargets{byIP: map[string][]bundleTarget{}, byCI: map[string][]bundleTarget{}}
				scopes[tgt.ScopeID] = st
			}
			if tgt.CIID != "" {
				st.byCI[tgt.CIID] = append(st.byCI[tgt.CIID], tgt)
			}
			ip := canonicalIP(tgt.ManagementIP)
			if ip == "" {
				continue
			}
			k := scopeIP{tgt.ScopeID, ip}
			if owner, seen := ipOwner[k]; seen && owner != tgt.CIID {
				ambiguous[k] = true
			}
			ipOwner[k] = tgt.CIID
			st.byIP[ip] = append(st.byIP[ip], tgt)
		}
		for k := range ambiguous {
			delete(scopes[k.scope].byIP, k.ip)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cred := range byID {
		old, ok := c.creds[id]
		if !ok || cred.VaultRef == nil || len(cred.Secret) > 0 || len(old.Secret) == 0 || old.VaultRef == nil {
			continue
		}
		if sameVaultRef(old.VaultRef, cred.VaultRef) && maps.Equal(stringValues(c.creds[old.VaultRef.LoginID].Secret),
			stringValues(byID[cred.VaultRef.LoginID].Secret)) {
			cred.Secret = old.Secret
			byID[id] = cred
		}
	}
	c.creds, c.scopes, c.vaults = byID, scopes, vaults
}

// canonicalIP returns the canonical form of an address ("" for none or a
// name).
func canonicalIP(s string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

// targetEntries returns the bundle's entries of a device in the pass of
// scope (none without a server scope): by CI id, else by management
// address. A host scope covers one device, which the server matched by
// address or by name: when its entries name one CI, they are the device's
// for every target of the pass (the probe of a host name and the collection
// of its address alike); with several CIs (an ambiguous name) none. The
// caller holds the read lock.
func (c *bundleCache) targetEntries(scope *collectorScope, ip, ciID string) []bundleTarget {
	if scope == nil {
		return nil
	}
	st := c.scopes[scope.ID]
	if st == nil {
		return nil
	}
	if ciID != "" {
		if list, ok := st.byCI[ciID]; ok {
			return list
		}
	}
	if scope.Kind == "host" {
		if len(st.byCI) != 1 {
			return nil
		}
		for _, list := range st.byCI {
			return list
		}
	}
	if key := canonicalIP(ip); key != "" {
		return st.byIP[key]
	}
	return nil
}

// usableTargets returns the entries of a device in the pass of scope whose
// credential is in the bundle under the entry's namespace, by namespace.
func (c *bundleCache) usableTargets(scope *collectorScope, ip, ciID string) map[string]bundleTarget {
	out := map[string]bundleTarget{}
	for _, tgt := range c.targetEntries(scope, ip, ciID) {
		cred, ok := c.creds[tgt.CredentialID]
		// An unresolved vault reference has no values: the scope's
		// credential of the namespace stays (WP-895).
		if !ok || credentialNamespace[cred.Kind] != tgt.Namespace || len(cred.Secret) == 0 {
			continue
		}
		if _, taken := out[tgt.Namespace]; !taken {
			out[tgt.Namespace] = tgt
		}
	}
	return out
}

// targetKey identifies the resolution of a device in the pass of scope: its
// namespaces with a target credential ("" when it has none, i.e. it
// resolves per scope). Devices with the same key resolve to the same
// credentials.
func (c *bundleCache) targetKey(scope *collectorScope, ip, ciID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	usable := c.usableTargets(scope, ip, ciID)
	if len(usable) == 0 {
		return ""
	}
	parts := make([]string, 0, len(usable))
	for ns, tgt := range usable {
		parts = append(parts, ns+"="+tgt.CredentialID)
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

// credentialRef names the bundle credential a device uses in a namespace
// and, when it came from a bundle target, the CI the server resolved it for
// (CRD-06, WP-898). It carries no value of the secret.
type credentialRef struct {
	ID   string
	CIID string
}

// overrideTarget replaces in creds (namespaced, owned by the caller) every
// namespace the device has a target credential for by that credential and
// returns the targets used, by namespace; the other namespaces keep the
// scope's credentials.
func (c *bundleCache) overrideTarget(creds map[string]string, scope *collectorScope, ip, ciID string) map[string]bundleTarget {
	c.mu.RLock()
	defer c.mu.RUnlock()
	usable := c.usableTargets(scope, ip, ciID)
	for ns, tgt := range usable {
		prefix := ns + "."
		for k := range creds {
			if strings.HasPrefix(k, prefix) {
				delete(creds, k)
			}
		}
		for k, v := range c.creds[tgt.CredentialID].Secret {
			if str, isString := v.(string); isString {
				creds[prefix+k] = str
			}
		}
	}
	return usable
}

// resolve returns the cached credentials with the given IDs as
// protocol-namespaced entries: each credential under the namespace of its
// kind, the first of a namespace in ids wins. IDs missing from the bundle,
// kinds without a namespace and entries without secret values (a vault
// reference not resolved, WP-894, WP-895) are skipped without taking their
// namespace, so a stored credential later in ids still applies. A resolved
// reference applies like a stored credential. The second result names per
// namespace the ID of the credential that applies (WP-898).
func (c *bundleCache) resolve(ids []string) (creds, used map[string]string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	creds, used = map[string]string{}, map[string]string{}
	for _, id := range ids {
		cred, ok := c.creds[id]
		if !ok {
			continue
		}
		ns, known := credentialNamespace[cred.Kind]
		if !known || used[ns] != "" || len(cred.Secret) == 0 {
			continue
		}
		used[ns] = id
		for k, v := range cred.Secret {
			if s, isString := v.(string); isString {
				creds[ns+"."+k] = s
			}
		}
	}
	return creds, used
}

// credentialsFor returns the credentials of a discovery pass (COL-02,
// DIS-09). A server scope gets only the bundle credentials its
// credential_ids name (the scope's effective list from the server, E-67),
// never those of another scope; the locally configured credentials apply
// only to the fallback pass without server scopes (scope nil).
func credentialsFor(cfg *collectorConfig, scope *collectorScope) map[string]string {
	creds, _ := credentialsForPass(cfg, scope)
	return creds
}

// credentialsForPass is credentialsFor with the credential that applies per
// namespace (none for the local settings, which have no ID).
func credentialsForPass(cfg *collectorConfig, scope *collectorScope) (map[string]string, map[string]credentialRef) {
	refs := map[string]credentialRef{}
	if scope == nil {
		return maps.Clone(cfg.Credentials), refs
	}
	creds, used := collectorBundle.resolve(scope.CredentialIDs)
	for ns, id := range used {
		refs[ns] = credentialRef{ID: id}
	}
	return creds, refs
}

// credentialsForTarget returns the credentials of one device of a pass
// (CRD-04, WP-892): per namespace the target credential the bundle names
// for the device in the pass of this scope (by CI id, else by management
// address; never an entry of another scope), otherwise the
// scope's (credentialsFor). A target credential for snmp does not displace
// the scope's ssh credential. The pass without server scopes (scope nil)
// uses only the local settings. It is the one resolution for every
// consumer, the probe engine (WP-948) passes the CI id. The second result
// names per namespace the credential that applies and, for a target
// credential, its CI (WP-898). The log names at most namespace and origin,
// never a value.
func credentialsForTarget(cfg *collectorConfig, scope *collectorScope, ip, ciID string) (map[string]string, map[string]credentialRef) {
	creds, refs := credentialsForPass(cfg, scope)
	if scope == nil {
		return creds, refs
	}
	if replaced := collectorBundle.overrideTarget(creds, scope, ip, ciID); len(replaced) > 0 {
		namespaces := make([]string, 0, len(replaced))
		for ns, tgt := range replaced {
			refs[ns] = credentialRef{ID: tgt.CredentialID, CIID: tgt.CIID}
			namespaces = append(namespaces, ns)
		}
		slices.Sort(namespaces)
		slog.Debug("discovery credentials of a target", "namespaces", namespaces, "origin", "target")
	}
	return creds, refs
}

// fetchBundle requests and opens the collector's bundle over nc.
func fetchBundle(ctx context.Context, nc *nats.Conn, creds *keystore.Credentials) (*bundleContent, error) {
	subject, err := collectorSubject(creds)
	if err != nil {
		return nil, err
	}
	req, err := json.Marshal(map[string]string{"certificate_pem": creds.ClientCertificatePEM})
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, bundleFetchTimeout)
	defer cancel()
	msg, err := nc.RequestWithContext(reqCtx, subject+".credentials.request", req)
	if err != nil {
		return nil, fmt.Errorf("request credential bundle: %w", err)
	}
	var resp struct {
		Bundle *sealedBundle `json:"bundle"`
		Error  string        `json:"error"`
	}
	if err = json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("credential bundle: %w", err)
	}
	if resp.Error != "" || resp.Bundle == nil {
		return nil, fmt.Errorf("credential bundle refused: %s", resp.Error)
	}
	if resp.Bundle.CollectorID != creds.DeviceID {
		return nil, errors.New("credential bundle for another collector")
	}
	return openBundle(resp.Bundle, creds.ClientPrivateKeyPEM)
}

// syncBundle keeps the in-memory credentials current while ctx lasts: it
// fetches the bundle at the start of the session, every bundleRefresh and
// after a reload notice of the server (bundle.changed, WP-897).
func syncBundle(ctx context.Context, nc *nats.Conn, creds *keystore.Credentials) {
	ticker := time.NewTicker(bundleRefresh)
	defer ticker.Stop()
	reload := newBundleReloader()
	defer reload.stop()
	if subject, err := collectorSubject(creds); err == nil {
		if sub, serr := subscribeBundleChanged(nc, subject, reload); serr != nil {
			slog.Warn("bundle reload notices not received; the bundle is fetched every "+bundleRefresh.String(), "error", serr)
		} else {
			defer func() { _ = sub.Unsubscribe() }()
		}
	}
	for {
		if fresh, err := fetchBundle(ctx, nc, creds); err != nil {
			slog.Warn("credential bundle not updated", "error", err)
		} else {
			collectorBundle.set(fresh)
			slog.Info("credential bundle updated", "credentials", len(fresh.Credentials), "targets", len(fresh.Targets))
			// The vault references of the bundle, in memory (WP-895).
			collectorVault.resolveBundle(ctx, collectorBundle)
		}
		if !waitBundleRefresh(ctx, ticker.C, reload.ch) {
			return
		}
	}
}

// waitBundleRefresh waits for the next fetch of the bundle (the refresh
// cycle or a reload notice) and meanwhile resolves the vault references
// whose values expired (checked every vaultRefreshCheck); it reports false
// when ctx ended.
func waitBundleRefresh(ctx context.Context, refresh <-chan time.Time, reload <-chan struct{}) bool {
	check := time.NewTicker(vaultRefreshCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-refresh:
			return true
		case <-reload:
			return true
		case <-check.C:
			collectorVault.resolveBundle(ctx, collectorBundle)
		}
	}
}

// Reload notices (CRD-05, WP-897): after a change of a secret, a reference
// or a binding the server sends collector.<org>.<id>.bundle.changed to the
// collectors whose bundle holds the credential. The collector fetches its
// bundle after a random delay of up to bundleReloadJitter, so the
// collectors of a large change do not ask at once; further notices within
// the delay lead to the same single fetch. Commit to new bundle in memory
// stays below 60 s: at most the delay plus bundleFetchTimeout. Without the
// NATS connection the 15-minute cycle applies.

// bundleChangedSuffix is the subject of the reload notice below the
// collector's subject tree.
const bundleChangedSuffix = ".bundle.changed"

// bundleFetchTimeout bounds one bundle request.
const bundleFetchTimeout = 30 * time.Second

// bundleReloadJitter is the longest delay of a reload after a notice
// (tests shorten it).
var bundleReloadJitter = 20 * time.Second

// bundleReloadDelay draws the delay of a reload, 0 to bundleReloadJitter
// (tests replace it).
var bundleReloadDelay = func() time.Duration {
	if bundleReloadJitter <= 0 {
		return 0
	}
	return rand.N(bundleReloadJitter + 1)
}

// bundleReloader coalesces the reload notices into one fetch per delay.
type bundleReloader struct {
	ch      chan struct{}
	mu      sync.Mutex
	pending *time.Timer
}

func newBundleReloader() *bundleReloader { return &bundleReloader{ch: make(chan struct{}, 1)} }

// notify schedules a fetch after the random delay unless one is pending.
func (r *bundleReloader) notify() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil {
		return
	}
	r.pending = time.AfterFunc(bundleReloadDelay(), func() {
		r.mu.Lock()
		r.pending = nil
		r.mu.Unlock()
		select {
		case r.ch <- struct{}{}:
		default: // a fetch is already due
		}
	})
}

// stop drops a pending fetch.
func (r *bundleReloader) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil {
		r.pending.Stop()
		r.pending = nil
	}
}

// subscribeBundleChanged subscribes the reload notices of the collector's
// subject tree.
func subscribeBundleChanged(nc *nats.Conn, subject string, reload *bundleReloader) (*nats.Subscription, error) {
	return nc.Subscribe(subject+bundleChangedSuffix, func(*nats.Msg) { reload.notify() })
}
