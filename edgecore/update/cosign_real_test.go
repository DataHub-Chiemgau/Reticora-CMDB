//go:build cosignreal

// Integration test of CosignVerifier with the real cosign CLI against a real
// keyless Sigstore bundle (WP-545, TEC-15, COL-06; tools per E-31). It needs
// an artifact signed by `cosign sign-blob --bundle`, so it runs where such a
// signature is made: release.yml after 'Sign binaries and SBOMs' for every
// architecture, and the CI job cosign-verify (WP-546).
//
//	COSIGN_BIN              cosign executable (required)
//	RELEASE_ARTIFACT        the signed artifact (required)
//	RELEASE_BUNDLE          its Sigstore bundle (required); its directory must
//	                        also hold the bundle of another artifact with the
//	                        same suffix (e.g. <file>.sigstore.json), the
//	                        foreign bundle of the negative case
//	COSIGN_EXPECT_IDENTITY  expected signer as CosignVerifier.IdentityRegexp;
//	                        default the pinned ReleaseIdentityRegexp
//
// A missing variable fails the test; it is never skipped.
//
// Retest: cd edgecore && COSIGN_BIN=… RELEASE_ARTIFACT=… RELEASE_BUNDLE=… \
// go test -tags cosignreal -count=1 -run '^TestCosignVerifierWithRealCosign$' ./update/
package update

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// otherIdentityRegexp is a signer no bundle of this repository carries.
const otherIdentityRegexp = `^https://github\.com/reticora-invalid/other-repository/\.github/workflows/release\.yml@refs/tags/v.+$`

// TestCosignVerifierWithRealCosign: the real cosign accepts the signed
// artifact for the expected identity and refuses an artifact changed by one
// byte, a foreign bundle and another identity.
func TestCosignVerifierWithRealCosign(t *testing.T) {
	cosignBin := requireEnv(t, "COSIGN_BIN")
	artifact := requireEnv(t, "RELEASE_ARTIFACT")
	bundle := requireEnv(t, "RELEASE_BUNDLE")
	identity := os.Getenv("COSIGN_EXPECT_IDENTITY")
	if identity == "" {
		identity = ReleaseIdentityRegexp
	}
	t.Logf("cosign %s, artifact %s, bundle %s, identity %s", cosignBin, artifact, bundle, identity)

	original, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("RELEASE_ARTIFACT: %v", err)
	}
	if len(original) == 0 {
		t.Fatalf("RELEASE_ARTIFACT %s is empty", artifact)
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("RELEASE_BUNDLE: %v", err)
	}
	foreign := foreignBundle(t, artifact, bundle)
	verifier := CosignVerifier{Binary: cosignBin, IdentityRegexp: identity}

	t.Run("signed artifact with the expected identity is accepted", func(t *testing.T) {
		if err := verifier.Verify(t.Context(), artifact, bundle); err != nil {
			t.Fatalf("the signed artifact was refused: %v", err)
		}
	})

	t.Run("artifact changed by one byte is refused", func(t *testing.T) {
		changed := append([]byte(nil), original...)
		changed[len(changed)/2] ^= 0x01
		path := filepath.Join(t.TempDir(), filepath.Base(artifact))
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifier.Verify(t.Context(), path, bundle); err == nil {
			t.Fatal("an artifact changed by one byte was accepted")
		} else {
			t.Logf("refused: %v", err)
		}
	})

	t.Run("foreign bundle is refused", func(t *testing.T) {
		if err := verifier.Verify(t.Context(), artifact, foreign); err == nil {
			t.Fatalf("the artifact was accepted with the bundle %s of another artifact", foreign)
		} else {
			t.Logf("refused: %v", err)
		}
	})

	t.Run("another identity is refused", func(t *testing.T) {
		other := CosignVerifier{Binary: cosignBin, IdentityRegexp: otherIdentityRegexp}
		err := other.Verify(t.Context(), artifact, bundle)
		if err == nil {
			t.Fatalf("the signature was accepted for the identity %s", otherIdentityRegexp)
		}
		// The refusal names the identity, not a load or network error.
		if !strings.Contains(err.Error(), "none of the expected identities matched") {
			t.Fatalf("refused, but not for the identity: %v", err)
		}
		t.Logf("refused: %v", err)
	})
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is not set; the real cosign test needs COSIGN_BIN, RELEASE_ARTIFACT and RELEASE_BUNDLE", name)
	}
	return value
}

// foreignBundle returns the first bundle beside bundle (same suffix) that
// belongs to another artifact: its content differs from bundle and the file
// it is named after, if present, differs from artifact.
func foreignBundle(t *testing.T, artifact, bundle string) string {
	t.Helper()
	suffix := ".sigstore.json"
	if !strings.HasSuffix(bundle, suffix) {
		suffix = filepath.Ext(bundle)
	}
	own, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatalf("RELEASE_BUNDLE: %v", err)
	}
	signed, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("RELEASE_ARTIFACT: %v", err)
	}
	dir := filepath.Dir(bundle)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the directory of RELEASE_BUNDLE: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) || e.Name() == filepath.Base(bundle) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(path)
		if err != nil || bytes.Equal(content, own) {
			continue
		}
		if subject, err := os.ReadFile(strings.TrimSuffix(path, suffix)); err == nil && bytes.Equal(subject, signed) {
			continue
		}
		return path
	}
	t.Fatalf("no foreign bundle (*%s of another artifact) beside RELEASE_BUNDLE in %s", suffix, dir)
	return ""
}
