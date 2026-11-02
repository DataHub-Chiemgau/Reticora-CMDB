package collectorcmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// checkRenewalRequest checks a renewal request received on subject like
// the server (backend/internal/discovery/renew_edge.go, WP-375,
// docs/betrieb/collector-ca.md): the presented certificate is of the CA
// and names the collector of the subject, the signature over
// "reticora-collector-certificate-renewal", subject, timestamp and CSR is by
// its key, the timestamp is within 5 min of now, the CSR names the
// collector (CN) and its organization (O), and the certificate has reached
// 60 % of its lifetime. It returns the CSR and the presented certificate,
// or the error code the server answers.
func checkRenewalRequest(ca *auditPKI, subject string, data []byte, now time.Time) (*x509.CertificateRequest, *x509.Certificate, string) {
	var req struct {
		CSR            string `json:"csr"`
		CertificatePEM string `json:"certificate_pem"`
		Timestamp      string `json:"timestamp"`
		Signature      []byte `json:"signature"`
	}
	parts := strings.Split(subject, ".")
	if len(parts) != 5 || parts[0] != "collector" || parts[3]+"."+parts[4] != "certificate.renew" ||
		json.Unmarshal(data, &req) != nil || req.CSR == "" || req.CertificatePEM == "" || req.Timestamp == "" || len(req.Signature) == 0 {
		return nil, nil, "invalid_request"
	}
	orgID, collectorID := parts[1], parts[2]
	cert, err := leafCertificate(req.CertificatePEM)
	if err != nil {
		return nil, nil, "not_authorized"
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil ||
		cert.Subject.CommonName != collectorID || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != orgID {
		return nil, nil, "not_authorized"
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	digest := sha256.Sum256([]byte("reticora-collector-certificate-renewal\n" + subject + "\n" + req.Timestamp + "\n" + req.CSR))
	if !ok || !ecdsa.VerifyASN1(pub, digest[:], req.Signature) {
		return nil, nil, "not_authorized"
	}
	at, err := time.Parse(time.RFC3339, req.Timestamp)
	if err != nil || at.Sub(now).Abs() > 5*time.Minute {
		return nil, nil, "timestamp_out_of_window"
	}
	block, _ := pem.Decode([]byte(req.CSR))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, nil, "invalid_csr"
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != collectorID ||
		len(csr.Subject.Organization) != 1 || csr.Subject.Organization[0] != orgID {
		return nil, nil, "invalid_csr"
	}
	life := cert.NotAfter.Sub(cert.NotBefore)
	if now.Sub(cert.NotBefore) < time.Duration(float64(life)*0.6) {
		return nil, nil, "renewal_not_due"
	}
	return csr, cert, ""
}

// TestRenewCertificateWithFakeClock covers WP-414 (COL-04, F-026) with a
// fake clock: a certificate valid for 90 days is not renewed on day 53 (no
// request); on day 55 the collector sends the renewal over its channel,
// the keystore then holds the new certificate, the new key and the CA
// bundle of the answer (0600, identity and metadata kept, no temporary
// file left). A refused, unanswered or unusable renewal leaves the
// keystore as it was.
func TestRenewCertificateWithFakeClock(t *testing.T) {
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000e1", "6f0b6f0e-0000-4000-8000-0000000000e2"
	ctx := context.Background()
	ca := newAuditPKI(t)
	issuedAt := time.Now().UTC().Add(-60 * 24 * time.Hour).Truncate(time.Second)
	day := func(n int) func() time.Time {
		return func() time.Time { return issuedAt.Add(time.Duration(n) * 24 * time.Hour) }
	}
	colKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	subject := pkix.Name{CommonName: colID, Organization: []string{orgID}}
	enrolled := keystore.Credentials{
		DeviceID:             colID,
		ClientCertificatePEM: ca.issue(t, &colKey.PublicKey, subject, issuedAt, issuedAt.Add(90*24*time.Hour), false),
		ClientPrivateKeyPEM:  auditKeyPEM(t, colKey),
		CertificateAuthority: ca.pem,
		Metadata:             map[string]string{"server_url": "https://cmdb.example.com", "name": "col-1"},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := (&keystore.FileStore{Path: path}).Save(ctx, enrolled); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func(label string) {
		t.Helper()
		if now, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(now, stored) {
			t.Errorf("%s: the keystore changed (%v)", label, rerr)
		}
	}

	// The server: it checks the request as WP-375 does and answers with
	// reply.
	var requests []string
	var issued string
	accept := func(subj string, data []byte, now time.Time) any {
		csr, _, code := checkRenewalRequest(ca, subj, data, now)
		if code != "" {
			return map[string]string{"error": code}
		}
		issued = ca.issue(t, csr.PublicKey, subject, now.Add(-time.Minute), now.Add(90*24*time.Hour), false)
		return map[string]any{"certificate_pem": issued, "ca_bundle_pem": ca.pem + ca.pem}
	}
	var clock func() time.Time
	reply := accept
	request := func(_ context.Context, subj string, data []byte) ([]byte, error) {
		requests = append(requests, subj)
		if bytes.Contains(data, []byte("PRIVATE KEY")) {
			t.Errorf("the renewal request carries a private key")
		}
		if reply == nil {
			return nil, errors.New("nats: timeout")
		}
		return json.Marshal(reply(subj, data, clock()))
	}

	// Day 53 of 90: not due, no request.
	clock = day(53)
	if renewed, rerr := renewCertificate(ctx, path, request, clock); renewed || rerr != nil || len(requests) != 0 {
		t.Fatalf("day 53: renewed %v, %v, %d requests", renewed, rerr, len(requests))
	}
	unchanged("day 53")

	// Day 55, but the server refuses, does not answer or issues a
	// certificate the collector cannot use: the current certificate stays.
	clock = day(55)
	for name, r := range map[string]func(string, []byte, time.Time) any{
		"refused":    func(string, []byte, time.Time) any { return map[string]string{"error": "not_authorized"} },
		"unanswered": nil,
		"certificate for another key": func(_ string, _ []byte, now time.Time) any {
			other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			return map[string]any{"certificate_pem": ca.issue(t, &other.PublicKey, subject, now.Add(-time.Minute), now.Add(90*24*time.Hour), false)}
		},
	} {
		reply = r
		before := len(requests)
		renewed, rerr := renewCertificate(ctx, path, request, clock)
		if renewed || rerr == nil || len(requests) != before+1 {
			t.Errorf("%s: renewed %v, %v, %d requests", name, renewed, rerr, len(requests)-before)
		}
		if name == "refused" && (rerr == nil || !strings.Contains(rerr.Error(), "not_authorized")) {
			t.Errorf("the refusal does not name its cause: %v", rerr)
		}
		unchanged(name)
	}

	// Day 55: renewed over the channel, stored atomically with mode 0600.
	reply = accept
	before := len(requests)
	if renewed, rerr := renewCertificate(ctx, path, request, clock); !renewed || rerr != nil || len(requests) != before+1 {
		t.Fatalf("day 55: renewed %v, %v, %d requests", renewed, rerr, len(requests)-before)
	}
	if want := "collector." + orgID + "." + colID + ".certificate.renew"; requests[len(requests)-1] != want {
		t.Errorf("renewal subject %q, want %q", requests[len(requests)-1], want)
	}
	got, err := (&keystore.FileStore{Path: path}).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientCertificatePEM != issued || got.ClientPrivateKeyPEM == enrolled.ClientPrivateKeyPEM || got.CertificateAuthority != ca.pem+ca.pem {
		t.Fatal("the keystore does not hold the renewed certificate, a new key and the CA bundle of the answer")
	}
	if _, err = tls.X509KeyPair([]byte(got.ClientCertificatePEM), []byte(got.ClientPrivateKeyPEM)); err != nil {
		t.Fatalf("the stored key does not belong to the renewed certificate: %v", err)
	}
	if got.DeviceID != colID || got.Metadata["server_url"] != "https://cmdb.example.com" || got.Metadata["name"] != "col-1" {
		t.Errorf("identity or metadata lost: %+v", got.Metadata)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("keystore mode %v, %v; want 0600", st.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the keystore directory holds %d entries, want only the keystore (no temporary file)", len(entries))
	}

	// The renewed certificate is not due on day 55 again.
	before = len(requests)
	if renewed, rerr := renewCertificate(ctx, path, request, clock); renewed || rerr != nil || len(requests) != before {
		t.Errorf("after the renewal: renewed %v, %v, %d requests", renewed, rerr, len(requests)-before)
	}
}

// TestRenewalSchedule: the collector checks at once and then every 24 h;
// after a failure it logs a warning with the cause and tries again after
// 1 h; a stored renewal ends the session's check (runNATS reconnects).
func TestRenewalSchedule(t *testing.T) {
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	type result struct {
		renewed bool
		err     error
	}
	results := []result{{err: errors.New("enrollment: renewal refused: renewal_unavailable")}, {}, {}, {renewed: true}}
	var waits []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	calls := 0
	renew := func(context.Context) (bool, error) {
		r := results[calls]
		calls++
		return r.renewed, r.err
	}
	if !runRenewal(context.Background(), renew, after) {
		t.Fatal("the stored renewal did not end the check")
	}
	want := []time.Duration{time.Hour, 24 * time.Hour, 24 * time.Hour}
	if calls != 4 || len(waits) != len(want) || waits[0] != want[0] || waits[1] != want[1] || waits[2] != want[2] {
		t.Fatalf("checks %d, waits %v; want 4 checks and waits %v", calls, waits, want)
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"collector certificate not renewed; the current certificate stays"`) ||
		!strings.Contains(out, "renewal_unavailable") || !strings.Contains(out, `"retry_in":"1h0m0s"`) {
		t.Fatalf("no warning with the cause and the retry:\n%s", out)
	}

	// The session ends: no further check.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls, results = 0, []result{{err: context.Canceled}}
	if runRenewal(ctx, renew, after) || calls != 1 {
		t.Fatalf("after the end of the session: checks %d", calls)
	}
}
