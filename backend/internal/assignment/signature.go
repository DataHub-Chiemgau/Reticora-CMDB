package assignment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// storedSignature is a signature as the assignment table keeps it
// (checkout_signature, return_signature). The signer name lives in the PII
// vault (SEC-05, WP-241 AK1, WP-656): signer_name is a surrogate reference
// {"$pii": "<id>"} to a value subject. A plain string is a name stored
// without a vault (development without a master key) or before the
// pii.backfill run moved it.
type storedSignature struct {
	ImageBase64 string `json:"image_base64,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	SignerName  any    `json:"signer_name,omitempty"`
	SignedAt    string `json:"signed_at,omitempty"`
	Method      string `json:"method,omitempty"`
}

// sealSignature returns the stored form of sig (nil for none) with the
// signer name as a new vault subject, or in plaintext when no vault is
// configured.
func sealSignature(ctx context.Context, tx pgx.Tx, orgID string, sig *Signature) (any, error) {
	if sig == nil {
		return nil, nil
	}
	stored := storedSignature{ImageBase64: sig.ImageBase64, ObjectKey: sig.ObjectKey, SignedAt: sig.SignedAt, Method: sig.Method}
	if sig.SignerName != "" {
		stored.SignerName = sig.SignerName
		if pii.Default() != nil {
			ref, err := pii.SealValueTx(ctx, tx, orgID, sig.SignerName)
			if err != nil {
				return nil, fmt.Errorf("store signer: %w", err)
			}
			stored.SignerName = ref
		}
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("encode signature: %w", err)
	}
	return string(b), nil
}

// decodeSignature reads a stored signature: the signature with a plaintext
// signer name filled in, and the surrogate id of a signer in the vault. A
// value that is not a signature reads as none.
func decodeSignature(raw []byte) (sig *Signature, signer string) {
	if len(raw) == 0 {
		return nil, ""
	}
	var stored storedSignature
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, ""
	}
	sig = &Signature{ImageBase64: stored.ImageBase64, ObjectKey: stored.ObjectKey, SignedAt: stored.SignedAt, Method: stored.Method}
	switch name := stored.SignerName.(type) {
	case string:
		sig.SignerName = name
	default:
		signer, _ = pii.RefID(name)
	}
	return sig, signer
}

// reveal fills the signer names of the assignments' signatures from their
// vault subjects; a signer whose subject was deleted shows none.
func reveal(ctx context.Context, tx pgx.Tx, orgID string, items ...*Assignment) error {
	vault := pii.Default()
	var ids []string
	for _, a := range items {
		for _, id := range []string{a.checkoutSigner, a.returnSigner} {
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	if vault == nil || len(ids) == 0 {
		return nil
	}
	subjects, err := vault.ResolveTx(ctx, tx, orgID, ids)
	if err != nil {
		return fmt.Errorf("resolve signers: %w", err)
	}
	for _, a := range items {
		if s := subjects[a.checkoutSigner]; s != nil && a.CheckoutSignature != nil {
			a.CheckoutSignature.SignerName = s.Data["v"]
		}
		if s := subjects[a.returnSigner]; s != nil && a.ReturnSignature != nil {
			a.ReturnSignature.SignerName = s.Data["v"]
		}
	}
	return nil
}

// shred deletes the vault subjects of signers no assignment references
// anymore (crypto-shredding).
func shred(ctx context.Context, tx pgx.Tx, orgID string, ids ...string) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := pii.DeleteTx(ctx, tx, orgID, id); err != nil && !errors.Is(err, pii.ErrNotFound) {
			return fmt.Errorf("delete signer: %w", err)
		}
	}
	return nil
}
