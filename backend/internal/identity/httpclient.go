package identity

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// NewHTTPClientWithCA returns an HTTP client for OIDC provider requests that
// trusts the PEM bundle at caCertFile in addition to the system trust store.
//
// Deployments that terminate TLS in front of Keycloak with a private CA — or
// with the self-signed bootstrap certificate the installer creates before
// Let's Encrypt has issued the real one — would otherwise fail the token
// exchange with an x509 verification error. Certificate verification stays
// enabled; only the set of accepted roots is extended.
//
// An empty caCertFile returns nil so callers keep the package default client.
func NewHTTPClientWithCA(caCertFile string) (*http.Client, error) {
	path := strings.TrimSpace(caCertFile)
	if path == "" {
		return nil, nil
	}

	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity: read OIDC CA bundle: %w", err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("identity: OIDC CA bundle contains no certificates")
	}

	transport := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}

	return &http.Client{Timeout: 15 * time.Second, Transport: transport}, nil
}
