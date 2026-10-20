package discovery

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// Certificate renewal over the collector's NATS channel (COL-04, F-012): a
// collector renews its client certificate from 60 % of its lifetime with a
// request on collector.<org>.<id>.certificate.renew. The internal NATS the
// server listens on authenticates nobody and the certificate is public, so
// the request proves possession of the current key: it carries the CSR of
// the new key, the current certificate, a timestamp and a signature with
// the current key over RenewalSigningInput. There is no renewal over the
// API. The predecessor is revoked when the collector first connects with
// the successor (callout), at the latest SupersededGrace after the
// successor was issued (RunEdge).
const (
	// CertificateRenewSuffix is appended to the collector's subject tree
	// (collector.<org>.<id>) for renewal requests.
	CertificateRenewSuffix = ".certificate.renew"
	// RenewalMaxSkew is the accepted difference between the timestamp of a
	// request and the server's clock.
	RenewalMaxSkew = 5 * time.Minute
	// SupersededGrace is how long a certificate stays valid at most after
	// its successor was issued.
	SupersededGrace = 7 * 24 * time.Hour
	// renewalReplayWindow covers every request a replay could still pass
	// the timestamp check with: a certificate renewed within it gets no
	// second successor.
	renewalReplayWindow = 2 * RenewalMaxSkew
	// renewalQueue spreads the requests over the server instances: each is
	// answered once, so one request issues one certificate.
	renewalQueue           = "reticora-certificate-renew"
	renewalSignaturePrefix = "reticora-collector-certificate-renewal"
)

// Refusals of a renewal (RenewalResponse.Error).
const (
	RenewalInvalidRequest = "invalid_request"
	// RenewalNotAuthorized: the certificate is not the collector's (subject),
	// not of the collector CA, revoked, of a disabled collector, or the
	// signature is not by its key.
	RenewalNotAuthorized = "not_authorized"
	// RenewalStaleTimestamp: the timestamp is more than RenewalMaxSkew off.
	RenewalStaleTimestamp = "timestamp_out_of_window"
	// RenewalInvalidCSR: the CSR is invalid, uses a weak key or names
	// another collector (CN) or organization (O).
	RenewalInvalidCSR = "invalid_csr"
	// RenewalNotDue: the certificate has not reached 60 % of its lifetime
	// or was renewed just now.
	RenewalNotDue = "renewal_not_due"
	// RenewalUnavailable: the server could not issue the certificate.
	RenewalUnavailable = "renewal_unavailable"
)

// RenewalRequest is what a collector sends on its renewal subject.
type RenewalRequest struct {
	// CSR is the PEM certificate signing request of the new key with
	// CN = collector id and O = organization id.
	CSR string `json:"csr"`
	// CertificatePEM is the collector's current certificate.
	CertificatePEM string `json:"certificate_pem"`
	// Timestamp is the time of the request (RFC 3339).
	Timestamp string `json:"timestamp"`
	// Signature is the signature of RenewalSigningInput with the key of the
	// current certificate (base64 in JSON).
	Signature []byte `json:"signature"`
}

// RenewalResponse carries the new certificate (certificate_pem,
// ca_bundle_pem with the active and the previous CA, serial, fingerprint,
// validity) or the refusal.
type RenewalResponse struct {
	*IssuedCert
	Error string `json:"error,omitempty"`
}

// RenewalSigningInput is the message a collector signs with its current
// key: a fixed prefix, the NATS subject of the request, its timestamp and
// the CSR, separated by line feeds. ECDSA keys sign its SHA-256 (ASN.1
// DER), RSA keys its SHA-256 (PKCS #1 v1.5), Ed25519 keys the message.
func RenewalSigningInput(subject, timestamp, csrPEM string) []byte {
	return []byte(renewalSignaturePrefix + "\n" + subject + "\n" + timestamp + "\n" + csrPEM)
}

func verifyRenewalSignature(pub any, msg, sig []byte) bool {
	digest := sha256.Sum256(msg)
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest[:], sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(k, msg, sig)
	}
	return false
}

// checkRenewalCSR refuses a CSR whose subject does not name the collector
// (CN) and its organization (O); the CA checks its signature and key.
func checkRenewalCSR(csrPEM, orgID, collectorID string) error {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return fmt.Errorf("%w: no PEM certificate request", ErrInvalidCSR)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCSR, err)
	}
	if csr.Subject.CommonName != collectorID || len(csr.Subject.Organization) != 1 || csr.Subject.Organization[0] != orgID {
		return fmt.Errorf("%w: the subject must name the collector (CN) and its organization (O)", ErrInvalidCSR)
	}
	return nil
}

// CertificateRenewal answers the renewal requests of the collectors.
type CertificateRenewal struct {
	authorizer *EdgeAuthorizer
	ca         *CA
	repo       *PGRepository
	now        func() time.Time
}

// NewCertificateRenewal creates the service on the collector CA.
func NewCertificateRenewal(pool *pgxpool.Pool, ca *CA) *CertificateRenewal {
	return &CertificateRenewal{authorizer: NewEdgeAuthorizer(pool, ca), ca: ca, repo: NewPGRepository(pool),
		now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock of the timestamp check and the 60 % rule
// (tests).
func (s *CertificateRenewal) WithClock(now func() time.Time) *CertificateRenewal {
	s.now = now
	return s
}

// Serve answers the renewal requests on nc (subject
// collector.<org>.<id>.certificate.renew; the endpoint lets a collector
// publish only below its own subject tree) until ctx ends.
func (s *CertificateRenewal) Serve(ctx context.Context, nc *nats.Conn) error {
	sub, err := nc.QueueSubscribe("collector.*.*"+CertificateRenewSuffix, renewalQueue, func(msg *nats.Msg) {
		// Without a reply subject nobody would receive the certificate.
		if msg.Reply == "" {
			return
		}
		raw, _ := json.Marshal(s.Renew(ctx, msg.Subject, msg.Data))
		if rerr := msg.Respond(raw); rerr != nil {
			slog.Error("certificate renewal reply", "error", rerr)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe certificate renewals: %w", err)
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// Renew answers one renewal request received on subject: the certificate
// must be the collector's of the subject (EdgeAuthorizer: collector CA, not
// revoked, collector not disabled), the signature by its key, the
// timestamp within RenewalMaxSkew, the CSR for the collector, and the
// certificate past 60 % of its lifetime. The successor is valid for 90
// days.
func (s *CertificateRenewal) Renew(ctx context.Context, subject string, data []byte) RenewalResponse {
	orgID, collectorID, ok := subjectCollector(subject, CertificateRenewSuffix)
	if !ok {
		return RenewalResponse{Error: RenewalInvalidRequest}
	}
	var req RenewalRequest
	if err := json.Unmarshal(data, &req); err != nil || req.CSR == "" || req.CertificatePEM == "" || req.Timestamp == "" || len(req.Signature) == 0 {
		return RenewalResponse{Error: RenewalInvalidRequest}
	}
	refuse := func(code string, err error) RenewalResponse {
		slog.Warn("certificate renewal refused", "organization_id", orgID, "collector_id", collectorID, "reason", code, "error", err)
		return RenewalResponse{Error: code}
	}
	id, err := s.authorizer.Authorize(ctx, []string{req.CertificatePEM})
	if err != nil {
		return refuse(RenewalNotAuthorized, err)
	}
	if id.OrganizationID != orgID || id.CollectorID != collectorID {
		return refuse(RenewalNotAuthorized, errors.New("the certificate belongs to another collector"))
	}
	if !verifyRenewalSignature(id.PublicKey, RenewalSigningInput(subject, req.Timestamp, req.CSR), req.Signature) {
		return refuse(RenewalNotAuthorized, errors.New("the signature is not by the key of the certificate"))
	}
	now := s.now()
	at, err := time.Parse(time.RFC3339, req.Timestamp)
	if err != nil || at.Sub(now).Abs() > RenewalMaxSkew {
		return refuse(RenewalStaleTimestamp, fmt.Errorf("timestamp %q, server time %s", req.Timestamp, now.Format(time.RFC3339)))
	}
	if err = checkRenewalCSR(req.CSR, orgID, collectorID); err != nil {
		return refuse(RenewalInvalidCSR, err)
	}
	if !NeedsRenewal(id.NotBefore, id.NotAfter, now) {
		return refuse(RenewalNotDue, ErrRenewalNotDue)
	}
	cert, err := s.repo.RenewEdgeCertificate(systemContext(ctx, orgID), orgID, collectorID, id.Serial,
		func(ctx context.Context, orgID, collectorID string) (*IssuedCert, error) {
			return s.ca.Sign(ctx, req.CSR, orgID, collectorID)
		})
	switch {
	case errors.Is(err, ErrRenewalNotDue):
		return refuse(RenewalNotDue, err)
	case errors.Is(err, ErrInvalidCSR):
		return refuse(RenewalInvalidCSR, err)
	case errors.Is(err, ErrCollectorDisabled), errors.Is(err, errCertificateNotCurrent):
		return refuse(RenewalNotAuthorized, err)
	case err != nil:
		slog.Error("certificate renewal", "organization_id", orgID, "collector_id", collectorID, "error", err)
		return RenewalResponse{Error: RenewalUnavailable}
	}
	slog.Info("collector certificate renewed", "organization_id", orgID, "collector_id", collectorID,
		"serial", cert.Serial, "predecessor_serial", id.Serial)
	return RenewalResponse{IssuedCert: cert}
}
