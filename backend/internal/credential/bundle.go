package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// Credential bundles (COL-02): the server hands a collector the credentials
// it needs over its mTLS NATS channel, encrypted for the collector's key
// (the public key of its client certificate): ECDH P-256 with an ephemeral
// key, HKDF-SHA256 and AES-256-GCM, bound to the collector id. The
// plaintext exists only in the server's memory while sealing and in the
// collector's memory; it is never stored, logged or returned by the API.

// BundleVersion is the format of SealedBundle.
const BundleVersion = 1

// BundleRequestSuffix is appended to the collector's subject tree
// (collector.<org>.<id>) for bundle requests.
const BundleRequestSuffix = ".credentials.request"

// SealedBundle is an encrypted credential bundle.
type SealedBundle struct {
	Version     int       `json:"version"`
	CollectorID string    `json:"collector_id"`
	IssuedAt    time.Time `json:"issued_at"`
	// EphemeralKey is the sender's ephemeral P-256 public key (uncompressed).
	EphemeralKey []byte `json:"ephemeral_key"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ciphertext"`
}

// BundleCredential is one credential in a bundle.
type BundleCredential struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Secret map[string]any `json:"secret"`
}

// BundleContent is the plaintext of a bundle.
type BundleContent struct {
	Credentials []BundleCredential `json:"credentials"`
}

func bundleKey(shared, ephemeral []byte, collectorID string) ([]byte, error) {
	return hkdf.Key(sha256.New, shared, ephemeral, "reticora/credential-bundle/v1|"+collectorID, 32)
}

func bundleAAD(collectorID string) []byte { return []byte("reticora-credential-bundle|" + collectorID) }

// SealBundle encrypts content for the collector's public key.
func SealBundle(content *BundleContent, collectorID string, recipient *ecdsa.PublicKey, now time.Time) (*SealedBundle, error) {
	pub, err := recipient.ECDH()
	if err != nil {
		return nil, fmt.Errorf("bundle: collector key: %w", err)
	}
	if pub.Curve() != ecdh.P256() {
		return nil, errors.New("bundle: the collector key must be P-256")
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	key, err := bundleKey(shared, eph.PublicKey().Bytes(), collectorID)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return &SealedBundle{Version: BundleVersion, CollectorID: collectorID, IssuedAt: now.UTC(),
		EphemeralKey: eph.PublicKey().Bytes(), Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plain, bundleAAD(collectorID))}, nil
}

// OpenBundle decrypts a bundle with the collector's private key (the
// counterpart lives in the collector; this one serves the tests).
func OpenBundle(b *SealedBundle, key *ecdsa.PrivateKey) (*BundleContent, error) {
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
	k, err := bundleKey(shared, b.EphemeralKey, b.CollectorID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, b.Nonce, b.Ciphertext, bundleAAD(b.CollectorID))
	if err != nil {
		return nil, fmt.Errorf("bundle: decrypt: %w", err)
	}
	var content BundleContent
	if uerr := json.Unmarshal(plain, &content); uerr != nil {
		return nil, uerr
	}
	return &content, nil
}

// BundleTarget is the collector a bundle is meant for, as verified from its
// certificate.
type BundleTarget struct {
	OrganizationID string
	CollectorID    string
	ClientID       string
	PublicKey      *ecdsa.PublicKey
	// CredentialIDs are the credentials of the bundle: the effective
	// credentials of the collector's enabled discovery scopes (E-67). An
	// empty list, like nil, selects none.
	CredentialIDs []string
}

// relevant reports whether a credential belongs into the target's bundle:
// only the listed ones.
func (t *BundleTarget) relevant(c *Credential) bool {
	return slices.Contains(t.CredentialIDs, c.ID)
}

// Bundle seals the credentials relevant for the target and returns their ids
// (an empty list for an empty bundle). ctx carries the organization's tenant
// scope.
func (s *Service) Bundle(ctx context.Context, t *BundleTarget) (*SealedBundle, []string, error) {
	all, err := s.repo.List(ctx, t.OrganizationID)
	if err != nil {
		return nil, nil, err
	}
	content := &BundleContent{Credentials: []BundleCredential{}}
	ids := []string{}
	for i := range all {
		c := &all[i]
		if !t.relevant(c) {
			continue
		}
		secret, derr := s.Decrypt(ctx, t.OrganizationID, c.ID)
		if derr != nil {
			return nil, nil, fmt.Errorf("credential %s: %w", c.ID, derr)
		}
		content.Credentials = append(content.Credentials, BundleCredential{ID: c.ID, Name: c.Name, Kind: c.Kind, Secret: secret})
		ids = append(ids, c.ID)
	}
	sealed, err := SealBundle(content, t.CollectorID, t.PublicKey, time.Now())
	return sealed, ids, err
}

// BundleRequest is what a collector sends: its current certificate.
type BundleRequest struct {
	CertificatePEM string `json:"certificate_pem"`
}

// BundleResponse carries the sealed bundle or the refusal.
type BundleResponse struct {
	Bundle *SealedBundle `json:"bundle,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// CollectorVerifier checks the certificate a collector presents for a
// bundle: issued to that collector by the collector CA, not revoked, the
// collector not disabled. It returns the target and the tenant context to
// read its credentials in.
type CollectorVerifier interface {
	VerifyBundleTarget(ctx context.Context, orgID, collectorID, certPEM string) (context.Context, *BundleTarget, error)
}

// BundleAuditor records that a collector received credentials.
type BundleAuditor interface {
	BundleDelivered(ctx context.Context, t *BundleTarget, credentialIDs []string) error
}

// ServeBundles answers the bundle requests of the collectors on nc
// (subject collector.<org>.<id>.credentials.request; the endpoint lets a
// collector publish only below its own subject tree) until ctx ends.
func (s *Service) ServeBundles(ctx context.Context, nc *nats.Conn, verify CollectorVerifier, audit BundleAuditor) error {
	sub, err := nc.Subscribe("collector.*.*"+BundleRequestSuffix, func(msg *nats.Msg) {
		resp := s.answerBundle(ctx, msg, verify, audit)
		raw, _ := json.Marshal(resp)
		if rerr := msg.Respond(raw); rerr != nil {
			slog.Error("credential bundle reply", "error", rerr)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe credential bundle requests: %w", err)
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

func (s *Service) answerBundle(ctx context.Context, msg *nats.Msg, verify CollectorVerifier, audit BundleAuditor) BundleResponse {
	parts := strings.Split(strings.TrimSuffix(msg.Subject, BundleRequestSuffix), ".")
	if len(parts) != 3 {
		return BundleResponse{Error: "invalid subject"}
	}
	var req BundleRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		return BundleResponse{Error: "invalid request"}
	}
	tctx, target, err := verify.VerifyBundleTarget(ctx, parts[1], parts[2], req.CertificatePEM)
	if err != nil {
		slog.Warn("credential bundle refused", "organization_id", parts[1], "collector_id", parts[2], "error", err)
		return BundleResponse{Error: "not authorized"}
	}
	sealed, ids, err := s.Bundle(tctx, target)
	if err != nil {
		slog.Error("credential bundle", "collector_id", target.CollectorID, "error", err)
		return BundleResponse{Error: "bundle unavailable"}
	}
	if audit != nil {
		if aerr := audit.BundleDelivered(tctx, target, ids); aerr != nil {
			slog.Error("audit credential bundle", "collector_id", target.CollectorID, "error", aerr)
			return BundleResponse{Error: "bundle unavailable"}
		}
	}
	return BundleResponse{Bundle: sealed}
}
