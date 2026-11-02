package jobs

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Certificate notices of COL-04 on the scheduler (JOB-02, WP-425): every
// day each collector certificate whose renewal is overdue and the active
// collector CA once it reached its renewal point are reported as
// certificate.expiring, once per certificate and organization (notice_sent,
// dedup key the fingerprint), so discovery does not stop unannounced when
// a certificate ends. A collector renews its certificate itself from 60 %
// of the lifetime (discovery/ca.go); its notice names the client and site
// of the collector. The CA, which spans organizations, is reported to every
// organization with a collector that holds a certificate and is not
// disabled.

// CertificateNoticesKind is the scheduler kind of the run.
const CertificateNoticesKind = "certificate.notices"

// CertificateRenewAfter is the share of the lifetime from which a
// certificate is due for renewal: discovery.RenewAfter (COL-04), which this
// package cannot import.
const CertificateRenewAfter = 0.6

// CertificateRenewalGrace is how long a collector may take to renew a due
// certificate before its renewal counts as overdue: a collector checks its
// certificate every 24 hours, so a working one renews within a day of the
// renewal point and is not reported.
const CertificateRenewalGrace = 48 * time.Hour

// CertificateNotice is the data of certificate.expiring: name and end of
// the certificate (expires_at, RFC 3339) and its SHA-256 fingerprint; for a
// collector certificate the collector (also the entity of the event) and
// its client and site.
type CertificateNotice struct {
	Name        string `json:"name"`
	ExpiresAt   string `json:"expires_at"`
	Fingerprint string `json:"fingerprint"`
	CollectorID string `json:"collector_id,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
}

// renewalPoint is when a certificate valid from notBefore to notAfter is
// due for renewal.
func renewalPoint(notBefore, notAfter time.Time) time.Time {
	return notBefore.Add(time.Duration(float64(notAfter.Sub(notBefore)) * CertificateRenewAfter))
}

// CertificateNotices sends the certificate notices.
type CertificateNotices struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewCertificateNotices creates the run on pool.
func NewCertificateNotices(pool *pgxpool.Pool) *CertificateNotices {
	return &CertificateNotices{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (n *CertificateNotices) WithClock(now func() time.Time) *CertificateNotices {
	n.now = now
	return n
}

// Schedule runs the notices daily; a missed day is caught up once.
func (n *CertificateNotices) Schedule() Schedule {
	return Schedule{Kind: CertificateNoticesKind, Spec: "23 4 * * *", MaxCatchUp: 1, Timeout: 15 * time.Minute,
		Run: func(ctx context.Context) error {
			sent, err := n.Run(ctx)
			if sent > 0 {
				slog.Info("certificate notices sent", "notices", sent)
			}
			return err
		}}
}

// Run sends the due notices of every organization and returns how many it
// sent.
func (n *CertificateNotices) Run(ctx context.Context) (int, error) {
	now := n.now()
	// A CA that cannot be read does not hold up the notices of the
	// collector certificates; the run reports it.
	ca, caErr := n.dueCA(ctx, now)
	sent, err := eachNoticeOrganization(ctx, n.pool, CertificateNoticesKind, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		// The current certificate of each collector that is not disabled:
		// the one the collector row names, not revoked.
		rows, err := tx.Query(ctx, `SELECT c.id::text, c.name, COALESCE(c.client_id::text, ''), COALESCE(c.site_id::text, ''),
				c.cert_fingerprint, cc.not_before, c.cert_not_after
			FROM collector c
			JOIN collector_certificate cc ON cc.organization_id = c.organization_id AND cc.collector_id = c.id
				AND cc.fingerprint = c.cert_fingerprint
			WHERE c.organization_id = $1 AND c.status <> 'disabled' AND c.cert_not_after IS NOT NULL AND cc.revoked_at IS NULL
			ORDER BY c.name, c.id`, orgID)
		if err != nil {
			return 0, fmt.Errorf("list collector certificates: %w", err)
		}
		type certRow struct {
			collectorID, name, clientID, siteID, fingerprint string
			notBefore, notAfter                              time.Time
		}
		certs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (certRow, error) {
			var c certRow
			scanErr := row.Scan(&c.collectorID, &c.name, &c.clientID, &c.siteID, &c.fingerprint, &c.notBefore, &c.notAfter)
			return c, scanErr
		})
		if err != nil {
			return 0, fmt.Errorf("list collector certificates: %w", err)
		}
		sent := 0
		for i := range certs {
			c := &certs[i]
			if now.Before(renewalPoint(c.notBefore, c.notAfter).Add(CertificateRenewalGrace)) {
				continue
			}
			ok, serr := sendNotice(ctx, tx, now, c.fingerprint, &events.Event{OrgID: orgID, Type: events.CertificateExpiring,
				EntityID: c.collectorID, Data: &CertificateNotice{Name: c.name, ExpiresAt: c.notAfter.UTC().Format(time.RFC3339),
					Fingerprint: c.fingerprint, CollectorID: c.collectorID, ClientID: c.clientID, SiteID: c.siteID}})
			if serr != nil {
				return sent, serr
			}
			if ok {
				sent++
			}
		}
		if ca == nil || len(certs) == 0 {
			return sent, nil
		}
		ok, err := sendNotice(ctx, tx, now, ca.Fingerprint, &events.Event{OrgID: orgID, Type: events.CertificateExpiring, Data: ca})
		if ok {
			sent++
		}
		return sent, err
	})
	if err == nil {
		err = caErr
	}
	return sent, err
}

// dueCA returns the notice of the active collector CA when it reached its
// renewal point at now; nil without an active CA or before.
func (n *CertificateNotices) dueCA(ctx context.Context, now time.Time) (*CertificateNotice, error) {
	var certPEM string
	err := database.WithOperator(ctx, n.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT cert_pem FROM collector_ca WHERE state = 'active'`).Scan(&certPEM)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: read the collector CA: %w", CertificateNoticesKind, err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("%s: the collector CA is no PEM certificate", CertificateNoticesKind)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: parse the collector CA: %w", CertificateNoticesKind, err)
	}
	if now.Before(renewalPoint(cert.NotBefore, cert.NotAfter)) {
		return nil, nil
	}
	sum := sha256.Sum256(cert.Raw)
	return &CertificateNotice{Name: cert.Subject.CommonName, ExpiresAt: cert.NotAfter.UTC().Format(time.RFC3339),
		Fingerprint: hex.EncodeToString(sum[:])}, nil
}
