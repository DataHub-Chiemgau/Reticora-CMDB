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

// bundleRefresh is how often the collector fetches its bundle again.
const bundleRefresh = 15 * time.Minute

type sealedBundle struct {
	Version      int    `json:"version"`
	CollectorID  string `json:"collector_id"`
	EphemeralKey []byte `json:"ephemeral_key"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ciphertext"`
}

type bundleCredential struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Secret map[string]any `json:"secret"`
}

type bundleContent struct {
	Credentials []bundleCredential `json:"credentials"`
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
}

// bundleCache holds the credentials of the last bundle in memory, by ID.
type bundleCache struct {
	mu    sync.RWMutex
	creds map[string]bundleCredential
}

var collectorBundle = &bundleCache{}

// set replaces the cached credentials by those of content (nil clears them).
func (c *bundleCache) set(content *bundleContent) {
	byID := map[string]bundleCredential{}
	if content != nil {
		for _, cred := range content.Credentials {
			byID[cred.ID] = cred
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creds = byID
}

// resolve returns the cached credentials with the given IDs as
// protocol-namespaced entries: each credential under the namespace of its
// kind, the first of a namespace in ids wins. IDs missing from the bundle
// and kinds without a namespace are skipped.
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
		if !known || taken[ns] {
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
			slog.Info("credential bundle updated", "credentials", len(fresh.Credentials))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
