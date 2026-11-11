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
// a vault connection without secret, which this collector does not read yet
// (WP-895).
type bundleCredential struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Source string         `json:"source,omitempty"`
	Secret map[string]any `json:"secret"`
}

// sourceVaultRef is the source of a vault reference in the bundle.
const sourceVaultRef = "vault_ref"

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
// targets.
type bundleContent struct {
	Credentials []bundleCredential `json:"credentials"`
	Targets     []bundleTarget     `json:"targets"`
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
}

// bundleCache holds the credentials of the last bundle in memory, by ID,
// and the targets of the bundle per scope.
type bundleCache struct {
	mu     sync.RWMutex
	creds  map[string]bundleCredential
	scopes map[string]*scopeBundleTargets
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
// A target without a scope applies nowhere. An address the targets of a
// scope name for two CIs is ambiguous and left out of that scope's address
// index; its devices resolve per scope (or by CI id).
func (c *bundleCache) set(content *bundleContent) {
	byID := map[string]bundleCredential{}
	scopes := map[string]*scopeBundleTargets{}
	if content != nil {
		for _, cred := range content.Credentials {
			byID[cred.ID] = cred
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
	c.creds, c.scopes = byID, scopes
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
		if !ok || credentialNamespace[cred.Kind] != tgt.Namespace {
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

// overrideTarget replaces in creds (namespaced, owned by the caller) every
// namespace the device has a target credential for by that credential and
// returns the namespaces replaced; the other namespaces keep the scope's
// credentials.
func (c *bundleCache) overrideTarget(creds map[string]string, scope *collectorScope, ip, ciID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	usable := c.usableTargets(scope, ip, ciID)
	replaced := make([]string, 0, len(usable))
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
		replaced = append(replaced, ns)
	}
	slices.Sort(replaced)
	return replaced
}

// resolve returns the cached credentials with the given IDs as
// protocol-namespaced entries: each credential under the namespace of its
// kind, the first of a namespace in ids wins. IDs missing from the bundle,
// kinds without a namespace and entries without secret values (a vault
// reference, WP-894) are skipped without taking their namespace, so a
// stored credential later in ids still applies.
func (c *bundleCache) resolve(ids []string) map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]string{}
	taken := map[string]bool{}
	for _, id := range ids {
		cred, ok := c.creds[id]
		if !ok {
			continue
		}
		ns, known := credentialNamespace[cred.Kind]
		if !known || taken[ns] || cred.Source == sourceVaultRef || len(cred.Secret) == 0 {
			continue
		}
		taken[ns] = true
		for k, v := range cred.Secret {
			if s, isString := v.(string); isString {
				out[ns+"."+k] = s
			}
		}
	}
	return out
}

// credentialsFor returns the credentials of a discovery pass (COL-02,
// DIS-09). A server scope gets only the bundle credentials its
// credential_ids name (the scope's effective list from the server, E-67),
// never those of another scope; the locally configured credentials apply
// only to the fallback pass without server scopes (scope nil).
func credentialsFor(cfg *collectorConfig, scope *collectorScope) map[string]string {
	if scope == nil {
		return maps.Clone(cfg.Credentials)
	}
	return collectorBundle.resolve(scope.CredentialIDs)
}

// credentialsForTarget returns the credentials of one device of a pass
// (CRD-04, WP-892): per namespace the target credential the bundle names
// for the device in the pass of this scope (by CI id, else by management
// address; never an entry of another scope), otherwise the
// scope's (credentialsFor). A target credential for snmp does not displace
// the scope's ssh credential. The pass without server scopes (scope nil)
// uses only the local settings. It is the one resolution for every
// consumer, the probe engine (WP-948) passes the CI id. The log names at
// most namespace and origin, never a value.
func credentialsForTarget(cfg *collectorConfig, scope *collectorScope, ip, ciID string) map[string]string {
	creds := credentialsFor(cfg, scope)
	if scope == nil {
		return creds
	}
	if replaced := collectorBundle.overrideTarget(creds, scope, ip, ciID); len(replaced) > 0 {
		slog.Debug("discovery credentials of a target", "namespaces", replaced, "origin", "target")
	}
	return creds
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
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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

// syncBundle keeps the in-memory credentials current while ctx lasts.
func syncBundle(ctx context.Context, nc *nats.Conn, creds *keystore.Credentials) {
	ticker := time.NewTicker(bundleRefresh)
	defer ticker.Stop()
	for {
		if fresh, err := fetchBundle(ctx, nc, creds); err != nil {
			slog.Warn("credential bundle not updated", "error", err)
		} else {
			collectorBundle.set(fresh)
			slog.Info("credential bundle updated", "credentials", len(fresh.Credentials), "targets", len(fresh.Targets))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
