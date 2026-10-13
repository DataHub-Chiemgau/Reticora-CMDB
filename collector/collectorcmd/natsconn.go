package collectorcmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// natsEndpointPort is the collector NATS endpoint of the platform (COL-08):
// the collector connects outbound to it with mutual TLS (PRI-07).
const natsEndpointPort = "4222"

// natsEndpoint is RETICORA_NATS_URL or, by default, the host of the server
// URL on port 4222 with TLS.
func natsEndpoint(serverURL string) string {
	if v := os.Getenv("RETICORA_NATS_URL"); v != "" {
		return v
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return "tls://" + net.JoinHostPort(u.Hostname(), natsEndpointPort)
}

// collectorSubject is the subject tree the endpoint grants the collector.
func collectorSubject(creds *keystore.Credentials) (string, error) {
	block, err := leafCertificate(creds.ClientCertificatePEM)
	if err != nil {
		return "", err
	}
	if len(block.Subject.Organization) != 1 {
		return "", errors.New("collector certificate names no organization")
	}
	return "collector." + block.Subject.Organization[0] + "." + block.Subject.CommonName, nil
}

func leafCertificate(certPEM string) (*x509.Certificate, error) {
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			return nil, errors.New("no collector certificate")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// natsOptions are the connection options of the collector: its client
// certificate and key from the keystore, the collector CA of the
// enrollment as trust anchor, the TLS handshake first and its own reply
// inbox (other collectors cannot read its replies).
func natsOptions(creds *keystore.Credentials) ([]nats.Option, error) {
	pair, err := tls.X509KeyPair([]byte(creds.ClientCertificatePEM), []byte(creds.ClientPrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("collector certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(creds.CertificateAuthority)) {
		return nil, errors.New("collector credentials carry no CA bundle")
	}
	return []nats.Option{
		nats.Name("reticora-collector-" + creds.DeviceID),
		nats.Secure(&tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, MinVersion: tls.VersionTLS12}),
		nats.TLSHandshakeFirst(),
		nats.CustomInboxPrefix("_INBOX." + creds.DeviceID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(5 * time.Second),
		nats.RetryOnFailedConnect(true),
	}, nil
}

// runNATS keeps the collector's mTLS connection to the NATS endpoint open
// while ctx lasts; without enrolled credentials it stays off.
func runNATS(ctx context.Context, cfg collectorConfig) {
	endpoint := natsEndpoint(cfg.ServerURL)
	creds, err := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(ctx)
	if err != nil || endpoint == "" {
		slog.Info("collector NATS endpoint not used", "reason", "not enrolled or no endpoint", "error", err)
		return
	}
	opts, err := natsOptions(creds)
	if err != nil {
		slog.Error("collector NATS endpoint", "error", err)
		return
	}
	nc, err := nats.Connect(endpoint, opts...)
	if err != nil {
		slog.Error("collector NATS endpoint", "url", endpoint, "error", err)
		return
	}
	defer nc.Close()
	slog.Info("collector NATS endpoint connected over mTLS", "url", endpoint)
	<-ctx.Done()
}
