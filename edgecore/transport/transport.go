// Package transport provides mTLS-backed connectivity primitives for edge components.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Transport describes the network capabilities edge clients need to reach the server.
type Transport interface {
	Do(req *http.Request) (*http.Response, error)
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	HTTPClient() *http.Client
}

// Config configures the mTLS transport used by collectors and agents.
type Config struct {
	ServerName    string
	RootCAsPEM    []byte
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	Timeout       time.Duration
}

// MTLS is a concrete mTLS transport implementation.
type MTLS struct {
	client *http.Client
	dialer tls.Dialer
}

// NewMTLS constructs an mTLS-enabled transport from PEM encoded credentials.
func NewMTLS(cfg Config) (*MTLS, error) {
	if len(cfg.ClientCertPEM) == 0 || len(cfg.ClientKeyPEM) == 0 {
		return nil, fmt.Errorf("transport: client certificate and key are required")
	}

	cert, err := tls.X509KeyPair(cfg.ClientCertPEM, cfg.ClientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("transport: load client key pair: %w", err)
	}

	var rootCAs *x509.CertPool
	if len(cfg.RootCAsPEM) > 0 {
		rootCAs = x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM(cfg.RootCAsPEM) {
			return nil, fmt.Errorf("transport: parse root CAs")
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		RootCAs:      rootCAs,
		ServerName:   cfg.ServerName,
	}

	netDialer := &net.Dialer{Timeout: timeout}
	httpTransport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: timeout,
	}

	return &MTLS{
		client: &http.Client{
			Timeout:   timeout,
			Transport: httpTransport,
		},
		dialer: tls.Dialer{
			NetDialer: netDialer,
			Config:    tlsConfig,
		},
	}, nil
}

// Do executes an HTTP request over the configured mTLS client.
func (m *MTLS) Do(req *http.Request) (*http.Response, error) {
	return m.client.Do(req)
}

// DialContext opens a raw TLS connection suitable for non-HTTP protocols.
func (m *MTLS) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return m.dialer.DialContext(ctx, network, addr)
}

// HTTPClient exposes the underlying HTTP client for higher level APIs.
func (m *MTLS) HTTPClient() *http.Client {
	return m.client
}
