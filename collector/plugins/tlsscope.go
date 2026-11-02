package plugins

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"time"
)

// ErrTLSUntrusted marks a collection that was refused because the target's
// certificate is not trusted: neither the system roots nor a TLS scope of its
// network (CA bundle or SPKI pin) vouches for it. No request, in particular
// no credential, reached the target (COL-02).
var ErrTLSUntrusted = errors.New("tls_untrusted")

// errPinMismatch is the verification failure of a pinned TLS scope.
var errPinMismatch = errors.New("certificate does not match a configured pin")

// TLSScope is a TLS exception for the targets of one network: certificates
// are verified against RootCAs instead of the system roots, and/or the
// SHA-256 hash of the leaf certificate's SubjectPublicKeyInfo must match one
// of PinsSHA256. With pins only, the chain is not verified (self-signed
// device certificates); the pin is the trust anchor.
type TLSScope struct {
	Network    *net.IPNet
	RootCAs    *x509.CertPool
	PinsSHA256 [][]byte
}

// SystemTLSConfig is the verification outside every TLS scope: the system
// roots, including the target address.
func SystemTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// TLSConfig builds the verification of the scope.
func (s TLSScope) TLSConfig() *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.RootCAs}
	if len(s.PinsSHA256) == 0 {
		return cfg
	}
	pins := s.PinsSHA256
	if s.RootCAs == nil {
		// Pinned self-signed certificate: the chain cannot be verified, the
		// pin check below replaces it.
		cfg.InsecureSkipVerify = true //nolint:gosec // verified by VerifyConnection (SPKI pin)
	}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errPinMismatch
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
		for _, pin := range pins {
			if bytes.Equal(pin, sum[:]) {
				return nil
			}
		}
		return errPinMismatch
	}
	return cfg
}

// UntrustedCertificate reports whether err is a failed certificate
// verification: chain or address against the system roots or the CA bundle
// of a TLS scope, or the pin of a TLS scope. The handshake failed before any
// request was sent.
func UntrustedCertificate(err error) bool {
	var verifyErr *tls.CertificateVerificationError
	return errors.As(err, &verifyErr) || errors.Is(err, errPinMismatch)
}

// NewTLSClient returns an HTTP client that verifies server certificates with
// cfg (SystemTLSConfig or TLSScope.TLSConfig). It follows no redirects and
// returns the redirect response instead: net/http would repeat Authorization
// (and a 307/308 also the body) to plain HTTP on the same host (COL-02).
func NewTLSClient(timeout time.Duration, cfg *tls.Config) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			TLSClientConfig:     cfg,
			MaxIdleConnsPerHost: 4,
		},
	}
}

// TLSClients holds the HTTP clients of a plugin's TLS scopes.
type TLSClients struct {
	scoped []scopedClient
}

type scopedClient struct {
	network *net.IPNet
	client  *http.Client
}

// Add builds a client with newClient for every scope that names a network
// and a trust anchor (CA bundle or pin); other scopes are ignored.
func (c *TLSClients) Add(newClient func(*tls.Config) *http.Client, scopes ...TLSScope) {
	for _, s := range scopes {
		if s.Network == nil || (s.RootCAs == nil && len(s.PinsSHA256) == 0) {
			continue
		}
		c.scoped = append(c.scoped, scopedClient{network: s.Network, client: newClient(s.TLSConfig())})
	}
}

// For returns the client whose verification applies to target: that of the
// first scope containing it, otherwise fallback (the system roots).
func (c *TLSClients) For(target string, fallback *http.Client) *http.Client {
	if ip := net.ParseIP(target); ip != nil {
		for _, s := range c.scoped {
			if s.network.Contains(ip) {
				return s.client
			}
		}
	}
	return fallback
}
