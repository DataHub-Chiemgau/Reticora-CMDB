package collectorcmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func loadGoldenBundle(t *testing.T) (*sealedBundle, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var b sealedBundle
	if err = json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile("testdata/bundle-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	return &b, string(key)
}

// TestCredentialBundle covers WP-147 (COL-02) on the collector side: a
// bundle sealed by the server (testdata, produced by
// backend/internal/credential.SealBundle) opens with the collector's key
// only; its credentials become protocol-namespaced entries in memory, a
// local setting overrides them, and the bundle does not open for another
// collector id or key.
func TestCredentialBundle(t *testing.T) {
	b, key := loadGoldenBundle(t)
	if strings.Contains(string(b.Ciphertext), "golden") {
		t.Fatal("the bundle carries plaintext")
	}
	content, err := openBundle(b, key)
	if err != nil {
		t.Fatal(err)
	}
	creds := content.namespaced()
	if creds["snmp.community"] != "golden-community" || creds["ssh.username"] != "golden-user" ||
		creds["ssh.password"] != "golden-pass" {
		t.Fatalf("namespaced credentials = %v", creds)
	}

	collectorBundle.set(creds)
	t.Cleanup(func() { collectorBundle.set(nil) })
	cfg := collectorConfig{Credentials: map[string]string{"ssh.username": "local-user"}}
	merged := credentialsFor(&cfg)
	if merged["ssh.username"] != "local-user" || merged["snmp.community"] != "golden-community" {
		t.Fatalf("merged credentials = %v", merged)
	}

	// Bound to the collector and its key.
	other := *b
	other.CollectorID = "col-other"
	if _, err = openBundle(&other, key); err == nil {
		t.Fatal("the bundle opened for another collector id")
	}
	tampered := *b
	tampered.Ciphertext = append([]byte(nil), b.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err = openBundle(&tampered, key); err == nil {
		t.Fatal("a modified bundle opened")
	}
	if _, err = openBundle(b, "not a key"); err == nil {
		t.Fatal("the bundle opened without the collector key")
	}
}
