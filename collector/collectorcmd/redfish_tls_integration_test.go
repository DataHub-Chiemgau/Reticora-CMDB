// Integration run of the Redfish discovery against a TLS endpoint (WP-539;
// COL-02, open evidence "Integrationslauf Redfish-TLS-Scope" of WP-048): the
// credential bundle, the discovery scope with its credential_ids, the TLS
// scopes of RETICORA_REDFISH_TLS_SCOPES and the Redfish plugin run together
// against a Redfish emulator with a self-signed certificate on loopback.
//
// The fixtures in testdata/redfish-tls are self-signed P-256 certificates for
// IP 127.0.0.1, valid for 100 years:
//
//	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
//	  -keyout bmc-key.pem -out bmc.pem -days 36500 -subj "/CN=Reticora test BMC" \
//	  -addext "subjectAltName=IP:127.0.0.1" -addext "keyUsage=critical,digitalSignature,keyCertSign" \
//	  -addext "extendedKeyUsage=serverAuth" -addext "basicConstraints=critical,CA:TRUE"
//
// other.pem is a second certificate of another key (its SPKI hash is the
// wrong pin); its key was not kept.
package collectorcmd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/redfish"
)

const (
	redfishTLSDir = "testdata/redfish-tls"
	// The credentials of the bundle: the Redfish account of the BMC and an
	// SSH account of the same scope that must never reach the BMC.
	redfishTLSUser     = "bmc-operator"
	redfishTLSPassword = "redfish-secret-539"
	sshTLSUser         = "root-ssh-539"
	sshTLSPassword     = "ssh-secret-539"
)

// redfishEmulatorRequest is a request as the emulator received it.
type redfishEmulatorRequest struct {
	Path          string
	Authorization string
	User          string
	Password      string
	BasicOK       bool
	// Raw is the request line, all headers, the body and the decoded Basic
	// credentials, to search for credentials anywhere in the request.
	Raw string
}

// redfishEmulator is a Redfish service on loopback with the fixture
// certificate; it records the TLS handshakes it saw and every request that
// passed the handshake.
type redfishEmulator struct {
	srv    *httptest.Server
	port   int
	hellos atomic.Int64

	mu       sync.Mutex
	requests []redfishEmulatorRequest
}

func (e *redfishEmulator) reset() {
	e.hellos.Store(0)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = nil
}

func (e *redfishEmulator) received() []redfishEmulatorRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]redfishEmulatorRequest(nil), e.requests...)
}

// redfishEmulatorResources are the Redfish resources the emulator serves.
var redfishEmulatorResources = map[string]any{
	"/redfish/v1/": map[string]any{
		"RedfishVersion": "1.15.0", "UUID": "8a0e8b4f-5390-4f39-a539-000000000539", "Product": "Reticora Test BMC",
		"Vendor": "Reticora", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"},
	},
	"/redfish/v1/Systems": map[string]any{
		"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/1"}},
	},
	"/redfish/v1/Systems/1": map[string]any{
		"Id": "1", "Name": "bmc-host-539", "Manufacturer": "Contoso", "Model": "PowerEdge R750",
		"SerialNumber": "SN-WP539", "UUID": "4C4C4544-0039-3510-8052-B4C04F4E3539", "HostName": "bmc-host-539.example.test",
		"BiosVersion": "2.19.1", "PowerState": "On",
		"EthernetInterfaces": map[string]string{"@odata.id": "/redfish/v1/Systems/1/EthernetInterfaces"},
	},
	"/redfish/v1/Systems/1/EthernetInterfaces": map[string]any{
		"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/1/EthernetInterfaces/NIC.1"}},
	},
	"/redfish/v1/Systems/1/EthernetInterfaces/NIC.1": map[string]any{
		"Id": "NIC.1", "MACAddress": "AA:BB:CC:00:05:39", "SpeedMbps": 10000, "MTUSize": 1500, "LinkStatus": "LinkUp",
	},
}

// startRedfishEmulator starts the emulator with the certificate of
// testdata/redfish-tls/bmc.pem. It answers only the Redfish account (401
// otherwise), like a BMC.
func startRedfishEmulator(t *testing.T) *redfishEmulator {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(filepath.Join(redfishTLSDir, "bmc.pem"), filepath.Join(redfishTLSDir, "bmc-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	e := &redfishEmulator{}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var raw strings.Builder
		raw.WriteString(r.Method + " " + r.URL.String() + " " + r.Proto + "\n")
		for name, values := range r.Header {
			raw.WriteString(name + ": " + strings.Join(values, ", ") + "\n")
		}
		raw.Write(body)
		user, password, ok := r.BasicAuth()
		if ok {
			// Decoded, so a credential in Basic auth is found as text too.
			raw.WriteString("\nbasic-auth: " + user + ":" + password)
		}
		e.mu.Lock()
		e.requests = append(e.requests, redfishEmulatorRequest{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"),
			User: user, Password: password, BasicOK: ok, Raw: raw.String()})
		e.mu.Unlock()
		if !ok || user != redfishTLSUser || password != redfishTLSPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		resource, found := redfishEmulatorResources[r.URL.Path]
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resource)
	}))
	e.srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		// Every ClientHello counts: the collector contacted the target even
		// when it then refused the certificate.
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			e.hellos.Add(1)
			return nil, nil
		},
	}
	// The refused handshakes are expected; keep them out of the test log.
	e.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	e.srv.StartTLS()
	t.Cleanup(e.srv.Close)
	_, portText, err := net.SplitHostPort(e.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if e.port, err = strconv.Atoi(portText); err != nil {
		t.Fatal(err)
	}
	return e
}

// sealRedfishTLSBundle seals content for the collector key of
// testdata/bundle-key.pem the way the server does
// (backend/internal/credential.SealBundle: ECDH P-256 with an ephemeral
// key, HKDF-SHA256, AES-256-GCM bound to the collector id).
func sealRedfishTLSBundle(t *testing.T, keyPEM, collectorID string, content *bundleContent) *sealedBundle {
	t.Helper()
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		t.Fatal("no collector key in testdata/bundle-key.pem")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	collectorPub, err := parsed.(*ecdsa.PrivateKey).PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.P256().GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := eph.ECDH(collectorPub)
	if err != nil {
		t.Fatal(err)
	}
	ephPub := eph.PublicKey().Bytes()
	key, err := hkdf.Key(sha256.New, shared, ephPub, "reticora/credential-bundle/v1|"+collectorID, 32)
	if err != nil {
		t.Fatal(err)
	}
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(blockCipher)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = crand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return &sealedBundle{Version: 1, CollectorID: collectorID, EphemeralKey: ephPub, Nonce: nonce,
		Ciphertext: gcm.Seal(nil, nonce, plain, []byte("reticora-credential-bundle|"+collectorID))}
}

// installRedfishTLSBundle seals a bundle with the Redfish and the SSH
// credential, opens it with the collector key and keeps it in memory like
// syncBundle after fetchBundle.
func installRedfishTLSBundle(t *testing.T) {
	t.Helper()
	keyPEM, err := os.ReadFile("testdata/bundle-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	sealed := sealRedfishTLSBundle(t, string(keyPEM), "col-wp539", &bundleContent{Credentials: []bundleCredential{
		{ID: "cred-ssh", Name: "linux root", Kind: "ssh_password", Secret: map[string]any{"username": sshTLSUser, "password": sshTLSPassword}},
		{ID: "cred-redfish", Name: "bmc", Kind: "redfish", Secret: map[string]any{"username": redfishTLSUser, "password": redfishTLSPassword}},
	}})
	for _, secret := range []string{redfishTLSPassword, sshTLSPassword} {
		if strings.Contains(string(sealed.Ciphertext), secret) {
			t.Fatalf("the sealed bundle carries %q in plaintext", secret)
		}
	}
	content, err := openBundle(sealed, string(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	collectorBundle.set(content)
}

// spkiPin returns the hex SHA-256 of the SubjectPublicKeyInfo of a PEM
// certificate, the pin format of RETICORA_REDFISH_TLS_SCOPES.
func spkiPin(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s: no PEM certificate", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// TestRedfishTLSScopeIntegration (WP-539; COL-02): a discovery pass of a
// server scope with a Redfish and an SSH credential runs the Redfish plugin,
// configured from RETICORA_REDFISH_TLS_SCOPES like Main, against a Redfish
// emulator with a self-signed certificate.
//
//   - (a) without a TLS scope for the target (none, or one of another
//     network) the handshake is refused: no request, in particular none with
//     Authorization, reaches the target and no record is produced; the
//     plugin's collection names the certificate error.
//   - (b) with the certificate as CA bundle of the TLS scope (and, as a
//     control of the pin path, with its matching pin) the pass produces the
//     DeviceRecord of the BMC, tagged with the scope; every request carries
//     Basic auth with the Redfish credential only.
//   - (c) with a wrong pin the handshake is refused like (a).
//
// In no case does the SSH credential appear in any request.
func TestRedfishTLSScopeIntegration(t *testing.T) {
	bmc := startRedfishEmulator(t)
	caBundle := filepath.Join(redfishTLSDir, "bmc.pem")
	scope := collectorScope{ID: "6f0b6f0e-0000-4000-8000-000000000539", Kind: "host", Target: "127.0.0.1",
		ClientID: "0b9e5d8a-0000-4000-8000-0000000005c1", SiteID: "0b9e5d8a-0000-4000-8000-0000000005e1", Schedule: "24h",
		// The SSH credential comes first: the namespaces, not the order,
		// keep it from the Redfish plugin.
		CredentialIDs: []string{"cred-ssh", "cred-redfish"}, Plugins: []string{"redfish"}}

	cases := []struct {
		name      string
		tlsScopes string // RETICORA_REDFISH_TLS_SCOPES
		trusted   bool
	}{
		{"a/no TLS scope", "", false},
		{"a/TLS scope of another network", "10.0.0.0/8=ca:" + caBundle, false},
		{"b/TLS scope with CA", "127.0.0.1/32=ca:" + caBundle, true},
		{"b/TLS scope with matching pin", "127.0.0.0/8=pin:" + spkiPin(t, caBundle), true},
		{"c/TLS scope with wrong pin", "127.0.0.1/32=pin:" + spkiPin(t, filepath.Join(redfishTLSDir, "other.pem")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bmc.reset()
			t.Setenv("RETICORA_REDFISH_TLS_SCOPES", tc.tlsScopes)
			tlsScopes := tlsScopesFromEnv("RETICORA_REDFISH_TLS_SCOPES")
			if tc.tlsScopes != "" && len(tlsScopes) != 1 {
				t.Fatalf("RETICORA_REDFISH_TLS_SCOPES=%q parsed into %d scopes, want 1", tc.tlsScopes, len(tlsScopes))
			}
			// The plugin as Main builds it, on the emulator's port.
			plug := redfish.New(redfish.WithTLSScopes(tlsScopes...), redfish.WithPort(bmc.port))
			auditUsePlugins(t, plug)
			installRedfishTLSBundle(t)
			collectorScopes.set([]collectorScope{scope}, nil)

			cfg := &collectorConfig{}
			passes := discoveryPasses(cfg)
			if len(passes) != 1 || passes[0].scope == nil || passes[0].scope.ID != scope.ID {
				t.Fatalf("discovery passes = %+v, want the one server scope", passes)
			}
			records, errs := runPass(context.Background(), cfg, nil, &passes[0], []string{"redfish"}, &hostCache{})
			requests := bmc.received()

			if bmc.hellos.Load() == 0 {
				t.Fatal("the collector never contacted the emulator: the pass did not run against the TLS target")
			}
			for _, req := range requests {
				for _, secret := range []string{sshTLSUser, sshTLSPassword} {
					if strings.Contains(req.Raw, secret) {
						t.Errorf("COL-02 violated: the SSH credential (%q) reached the Redfish target in %s", secret, req.Path)
					}
				}
			}

			if !tc.trusted {
				if len(requests) != 0 {
					for _, req := range requests {
						t.Errorf("request %s (Authorization %q) reached a target whose certificate is not trusted", req.Path, req.Authorization)
					}
				}
				if len(records) != 0 {
					t.Errorf("records = %+v, want none from an untrusted target", records)
				}
				// The plugin's collection fails with the certificate error
				// before it sends anything.
				_, err := plug.Collect(context.Background(), "127.0.0.1",
					plugins.CredentialsFor("redfish", collectorBundle.resolve(scope.CredentialIDs)))
				if err == nil || !strings.Contains(err.Error(), "certificate") {
					t.Errorf("Collect error = %v, want the certificate error", err)
				}
				if got := bmc.received(); len(got) != 0 {
					t.Errorf("Collect sent %d requests to an untrusted target", len(got))
				}
				return
			}

			if len(errs) != 0 {
				t.Errorf("run errors = %+v, want none", errs)
			}
			if len(requests) == 0 {
				t.Fatal("no request reached the trusted Redfish target")
			}
			paths := map[string]bool{}
			for _, req := range requests {
				paths[req.Path] = true
				if !req.BasicOK || req.User != redfishTLSUser || req.Password != redfishTLSPassword {
					t.Errorf("%s: Authorization %q, want Basic auth with the Redfish credential only", req.Path, req.Authorization)
				}
			}
			for path := range redfishEmulatorResources {
				if !paths[path] {
					t.Errorf("the pass did not read %s (read: %v)", path, paths)
				}
			}
			if len(records) != 1 {
				t.Fatalf("records = %+v, want the DeviceRecord of the BMC", records)
			}
			rec := records[0]
			if rec.Source != "redfish" || rec.TargetIP != "127.0.0.1" {
				t.Errorf("record source/target = %q/%q, want redfish/127.0.0.1", rec.Source, rec.TargetIP)
			}
			if rec.ScopeID != scope.ID || rec.ClientID != scope.ClientID || rec.SiteID != scope.SiteID {
				t.Errorf("record scope/client/site = %q/%q/%q, want those of the scope", rec.ScopeID, rec.ClientID, rec.SiteID)
			}
			id := rec.Identity
			if id.SerialNumber != "SN-WP539" || id.HardwareUUID != "4c4c4544-0039-3510-8052-b4c04f4e3539" ||
				id.Hostname != "bmc-host-539" || id.PrimaryMAC != "aa:bb:cc:00:05:39" {
				t.Errorf("record identity = %+v, want serial, hardware UUID, host name and MAC of the emulated system", id)
			}
			if v := rec.Validate(); len(v) != 0 {
				t.Errorf("the record breaks the DeviceRecord contract: %v", v)
			}
		})
	}
}
