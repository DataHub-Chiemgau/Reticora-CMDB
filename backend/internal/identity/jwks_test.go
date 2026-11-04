package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// TestECJWKPublicKey covers the P-256 JWK path after the move from the
// deprecated IsOnCurve/X/Y API to ecdsa.ParseUncompressedPublicKey (F-123,
// staticcheck SA1019): a valid key decodes to the same key and verifies an
// ES256 signature; a point off the curve or a coordinate wider than 32 bytes
// is refused.
func TestECJWKPublicKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, err := priv.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	k := jwk{KeyType: "EC", Curve: "P-256", X: enc(point[1:33]), Y: enc(point[33:])}

	key, err := k.publicKey()
	if err != nil {
		t.Fatalf("valid P-256 JWK refused: %v", err)
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&priv.PublicKey) {
		t.Fatalf("decoded key %#v differs from the generated key", key)
	}

	const signingInput = "header.payload"
	hash := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	if verifyErr := verifyJWTSignature(signingInput, signature, "ES256", key); verifyErr != nil {
		t.Fatalf("ES256 signature of the decoded key refused: %v", verifyErr)
	}

	for name, bad := range map[string]jwk{
		"off curve":     {KeyType: "EC", Curve: "P-256", X: enc([]byte{1}), Y: enc([]byte{1})},
		"wide x":        {KeyType: "EC", Curve: "P-256", X: enc(append([]byte{1}, point[1:33]...)), Y: enc(point[33:])},
		"other curve":   {KeyType: "EC", Curve: "P-384", X: k.X, Y: k.Y},
		"undecodable x": {KeyType: "EC", Curve: "P-256", X: "%%", Y: k.Y},
		"undecodable y": {KeyType: "EC", Curve: "P-256", X: k.X, Y: "%%"},
	} {
		if got, badErr := bad.publicKey(); badErr == nil {
			t.Errorf("%s: publicKey() = %#v, want error", name, got)
		}
	}
}
