package credential

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func newCollectorKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestSealBundle covers WP-147 (COL-02): a bundle opens only with the
// collector's key and only for its collector id; the ciphertext carries no
// plaintext; the collector's implementation reads the same format (golden
// bundle shared with collector/collectorcmd).
func TestSealBundle(t *testing.T) {
	key := newCollectorKey(t)
	content := &BundleContent{Credentials: []BundleCredential{{ID: "c1", Kind: KindSSHPassword,
		Secret: map[string]any{"username": "admin", "password": "s3cret-value"}}}}
	b, err := SealBundle(content, "col-1", &key.PublicKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(b); strings.Contains(string(raw), "s3cret-value") {
		t.Fatal("the sealed bundle carries the secret in plaintext")
	}
	opened, err := OpenBundle(b, key)
	if err != nil || opened.Credentials[0].Secret["password"] != "s3cret-value" {
		t.Fatalf("open = %+v, %v", opened, err)
	}
	if _, err = OpenBundle(b, newCollectorKey(t)); err == nil {
		t.Fatal("the bundle opened with another key")
	}
	other := *b
	other.CollectorID = "col-2"
	if _, err = OpenBundle(&other, key); err == nil {
		t.Fatal("the bundle opened for another collector")
	}

	// The golden bundle of the collector tests.
	raw, err := os.ReadFile("../../../collector/collectorcmd/testdata/bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden SealedBundle
	if err = json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := os.ReadFile("../../../collector/collectorcmd/testdata/bundle-key.pem")
	block, _ := pem.Decode(keyPEM)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if g, gerr := OpenBundle(&golden, parsed.(*ecdsa.PrivateKey)); gerr != nil || len(g.Credentials) != 3 {
		t.Fatalf("golden bundle: %+v, %v", g, gerr)
	}
}

type fakeVerifier struct {
	target *BundleTarget
	err    error
}

func (f *fakeVerifier) VerifyBundleTarget(ctx context.Context, orgID, collectorID, _ string) (context.Context, *BundleTarget, error) {
	if f.err != nil || orgID != f.target.OrganizationID || collectorID != f.target.CollectorID {
		return nil, nil, errors.New("refused")
	}
	return ctx, f.target, nil
}

type recordedDelivery struct{ ids []string }

func (r *recordedDelivery) BundleDelivered(_ context.Context, _ *BundleTarget, ids []string) error {
	r.ids = ids
	return nil
}

// TestBundleSelectsScopeCredentials covers WP-147 (COL-02) and WP-376
// (E-67): a bundle holds exactly the listed credentials (the effective
// credentials of the collector's scopes, resolved by the BundleGate); an
// empty list, like none, seals an empty bundle and reports no ids – there is
// no fallback to the collector's client. The server answers requests on the
// collector's subject over NATS with the sealed bundle (TEST_NATS_URL) —
// refused requests get no bundle.
func TestBundleSelectsScopeCredentials(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryRepository(), testEncryptor(t))
	mk := func(name, client string) string {
		t.Helper()
		c, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", ClientID: client, Name: name, Kind: KindSSHPassword,
			Secret: map[string]any{"username": name, "password": "pw-" + name}})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	own, shared, foreign := mk("own", "client-1"), mk("shared", ""), mk("foreign", "client-2")
	key := newCollectorKey(t)
	target := &BundleTarget{OrganizationID: "org-1", CollectorID: "col-1", ClientID: "client-1", PublicKey: &key.PublicKey}
	held := func(list []string) ([]string, []string) {
		t.Helper()
		target.CredentialIDs = list
		b, ids, err := svc.Bundle(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		opened, err := OpenBundle(b, key)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range opened.Credentials {
			got = append(got, c.ID)
		}
		return got, ids
	}
	// No list and an empty list: an empty bundle, not the client's
	// credentials; the delivery names an empty list.
	for name, list := range map[string][]string{"nil": nil, "empty": {}} {
		if got, ids := held(list); len(got) != 0 || ids == nil || len(ids) != 0 {
			t.Errorf("%s list: bundle holds %v (ids %#v), want an empty bundle with ids []", name, got, ids)
		}
	}
	// Explicit ids: exactly those, whatever their client (the gate filters
	// by the scope's client before).
	if got, ids := held([]string{own, shared}); len(got) != 2 || !slices.Contains(got, own) || !slices.Contains(got, shared) ||
		!slices.Equal(ids, got) {
		t.Fatalf("listed own and shared: bundle holds %v (ids %v)", got, ids)
	}
	if got, ids := held([]string{foreign}); !slices.Equal(got, []string{foreign}) || !slices.Equal(ids, []string{foreign}) {
		t.Fatalf("listed credentials: %v (ids %v)", got, ids)
	}
	if got, _ := held([]string{"00000000-0000-4000-8000-0000000000aa"}); len(got) != 0 {
		t.Fatalf("an unknown id selected %v", got)
	}
	target.CredentialIDs = []string{own, shared}

	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Log("TEST_NATS_URL not set; skipping the NATS round trip")
		return
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	verifier, delivered := &fakeVerifier{target: target}, &recordedDelivery{}
	go func() { _ = svc.ServeBundles(serveCtx, nc, verifier, delivered) }()
	time.Sleep(200 * time.Millisecond)

	ask := func(subject string) BundleResponse {
		t.Helper()
		msg, rerr := nc.Request(subject, []byte(`{"certificate_pem":"x"}`), 5*time.Second)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if strings.Contains(string(msg.Data), "pw-own") {
			t.Fatal("the NATS reply carries a secret in plaintext")
		}
		var resp BundleResponse
		if uerr := json.Unmarshal(msg.Data, &resp); uerr != nil {
			t.Fatal(uerr)
		}
		return resp
	}
	resp := ask("collector.org-1.col-1" + BundleRequestSuffix)
	if resp.Bundle == nil || len(delivered.ids) != 2 {
		t.Fatalf("bundle reply %+v, delivered %v", resp, delivered.ids)
	}
	if opened, oerr := OpenBundle(resp.Bundle, key); oerr != nil || len(opened.Credentials) != 2 {
		t.Fatalf("reply bundle: %v", oerr)
	}
	if resp = ask("collector.org-1.col-2" + BundleRequestSuffix); resp.Bundle != nil || resp.Error == "" {
		t.Fatalf("request of an unverified collector: %+v", resp)
	}
}
