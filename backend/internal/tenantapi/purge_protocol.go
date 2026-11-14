package tenantapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// The deletion protocol (TLC-06, WP-1156, E-166): the request of a
// deletion keeps the address and language of the requester in the
// operator table org_deletion_notice, encrypted with a data key of its own
// that the master key wraps (the purge deletes app_user and the vault of
// the organization). As the last step of org.purge before purged,
// operator_audit receives org.purged: the request, the due time, start and
// end of the purge, the counts and SHA-256 per table, the counts per
// object kind, a SHA-256 over the whole protocol and the note that the
// backups expire with their retention; no personal value. Once the
// organization is purged, the protocol goes to the decrypted address over
// the central SMTP server (notify.OrgPurgeProtocolMailer) and the row is
// deleted; without central SMTP or after three failed attempts the row is
// deleted as well and operator_audit records
// org.deletion.protocol_undelivered. A cancelled request deletes the row.

// Actions of the deletion protocol in operator_audit.
const (
	ActionOrgPurged           = "org.purged"
	ActionProtocolUndelivered = "org.deletion.protocol_undelivered"
)

// maxProtocolAttempts is how often the protocol is tried before it is
// given up.
const maxProtocolAttempts = 3

// BackupNotice is the note of the protocol on the backups.
const BackupNotice = "Backups still hold the data of the organization until their retention ends; they are not changed by the purge."

var noticeEncryptor atomic.Pointer[crypto.EnvelopeEncryptor]

// SetNoticeEncryptor sets the master key encryptor that wraps the data
// key of a stored address; nil stores no address (the protocol then stays
// in operator_audit only).
func SetNoticeEncryptor(e *crypto.EnvelopeEncryptor) { noticeEncryptor.Store(e) }

// ProtocolMailer sends the deletion protocol (notify.OrgPurgeProtocolMailer).
type ProtocolMailer interface {
	SendPurgeProtocol(ctx context.Context, p *notify.PurgeProtocolMail) error
}

// WithProtocolMailer sets the mailer of the deletion protocol; without it
// a stored address is deleted with org.deletion.protocol_undelivered.
func (p *Purger) WithProtocolMailer(m ProtocolMailer) *Purger {
	p.mailer = m
	return p
}

// PurgeProtocol is the protocol of a purge in org.purged.
type PurgeProtocol struct {
	RequestedAt  string             `json:"requested_at"`
	DueAt        string             `json:"due_at"`
	PurgeStarted string             `json:"purge_started_at"`
	PurgeEnded   string             `json:"purge_ended_at"`
	Tables       []PurgeTableResult `json:"tables"`
	Rows         int64              `json:"rows"`
	// Objects are the counts per object kind of the external purge
	// (vault, objects per bucket, search, stream, cache).
	Objects      map[string]map[string]int64 `json:"objects"`
	ObjectsTotal int64                       `json:"objects_total"`
	Backups      string                      `json:"backups"`
	// SHA256 is the hash over the protocol without it.
	SHA256 string `json:"sha256"`
}

// ProtocolHash is the SHA-256 over the canonical JSON of the protocol
// without its hash: anyone holding the protocol can recompute it.
func ProtocolHash(p *PurgeProtocol) (string, error) {
	c := *p
	c.SHA256 = ""
	tables := append([]PurgeTableResult(nil), c.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Table < tables[j].Table })
	c.Tables = tables
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	// Through a generic value: map keys sort, so the hash does not depend
	// on the order of the steps.
	var generic any
	if uerr := json.Unmarshal(raw, &generic); uerr != nil {
		return "", uerr
	}
	if raw, err = json.Marshal(generic); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// CountsText is the text attachment of the protocol: one line per table
// (rows before, deleted, SHA-256) and per object kind.
func (p *PurgeProtocol) CountsText() string {
	var b strings.Builder
	b.WriteString("table\trows_before\tdeleted\tsha256\n")
	tables := append([]PurgeTableResult(nil), p.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Table < tables[j].Table })
	for _, t := range tables {
		fmt.Fprintf(&b, "%s\t%d\t%d\t%s\n", t.Table, t.RowsBefore, t.Deleted, t.SHA256)
	}
	b.WriteString("\nobject kind\tcount\n")
	steps := make([]string, 0, len(p.Objects))
	for name := range p.Objects {
		steps = append(steps, name)
	}
	sort.Strings(steps)
	for _, step := range steps {
		counts := p.Objects[step]
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s.%s\t%d\n", step, k, counts[k])
		}
	}
	fmt.Fprintf(&b, "\nsha256\t%s\n", p.SHA256)
	return b.String()
}

// storeNoticeTx keeps the address and language of the requester in the
// transaction of the request (operator flag set): the address encrypted
// with a new data key, which the master key wraps. Without an encryptor
// or an address of the requester nothing is stored.
func storeNoticeTx(ctx context.Context, tx pgx.Tx, orgID, userID string) error {
	enc := noticeEncryptor.Load()
	if enc == nil || uuidString(userID) == "" {
		slog.WarnContext(ctx, "org deletion: the address of the protocol is not kept", "organization_id", orgID,
			"encryptor", enc != nil)
		return nil
	}
	var email, locale string
	err := tx.QueryRow(ctx, `SELECT COALESCE(email, ''), COALESCE(locale, '') FROM app_user
		WHERE id = $1 AND organization_id = $2`, userID, orgID).Scan(&email, &locale)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && strings.TrimSpace(email) == "") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the requester: %w", err)
	}
	dek, err := crypto.GenerateDEK()
	if err != nil {
		return err
	}
	sealed, err := crypto.Encrypt(dek, []byte(email))
	if err != nil {
		return fmt.Errorf("encrypt the address: %w", err)
	}
	wrapped, err := enc.WrapDEK(dek)
	if err != nil {
		return fmt.Errorf("wrap the data key: %w", err)
	}
	if locale == "" {
		locale = "de"
	}
	_, err = tx.Exec(ctx, `INSERT INTO org_deletion_notice (target_org, recipient_enc, wrapped_dek, locale)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (target_org) DO UPDATE SET recipient_enc = EXCLUDED.recipient_enc, wrapped_dek = EXCLUDED.wrapped_dek,
			locale = EXCLUDED.locale, created_at = now(), attempts = 0`, orgID, sealed, wrapped, locale)
	if err != nil {
		return fmt.Errorf("store the address of the protocol: %w", err)
	}
	return nil
}

// deleteNoticeTx deletes the address of a cancelled request.
func deleteNoticeTx(ctx context.Context, tx pgx.Tx, orgID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM org_deletion_notice WHERE target_org = $1`, orgID); err != nil {
		return fmt.Errorf("delete the address of the protocol: %w", err)
	}
	return nil
}

// protocolEntry returns the id and the protocol of the org.purged entry of
// the organization; found false without one.
func (p *Purger) protocolEntry(ctx context.Context, orgID string) (id int64, protocol *PurgeProtocol, found bool, err error) {
	err = database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		// Only the protocol of the latest purge of the organization: an
		// entry older than its org.deletion.purge_started belongs to an
		// earlier one.
		qerr := tx.QueryRow(ctx, `SELECT id, details FROM operator_audit WHERE target_org = $1 AND action = $2
			  AND id > COALESCE((SELECT max(id) FROM operator_audit WHERE target_org = $1 AND action = 'org.deletion.purge_started'), 0)
			ORDER BY id DESC LIMIT 1`, orgID, ActionOrgPurged).Scan(&id, &raw)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return nil
		}
		if qerr != nil {
			return qerr
		}
		protocol = &PurgeProtocol{}
		found = true
		return json.Unmarshal(raw, protocol)
	})
	return id, protocol, found, err
}

// buildProtocol collects the protocol of a purge whose rows and external
// stores are gone: request and due time of the organization, the start of
// org.deletion.purge_started, the results per table and the counts of the
// external steps.
func (p *Purger) buildProtocol(ctx context.Context, orgID string, results []PurgeTableResult) (*PurgeProtocol, error) {
	out := &PurgeProtocol{Tables: results, Objects: map[string]map[string]int64{}, Backups: BackupNotice}
	var requestedAt, dueAt *time.Time
	err := database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT deletion_requested_at, deletion_due_at FROM organization WHERE id = $1`, orgID).
			Scan(&requestedAt, &dueAt)
	})
	if err != nil {
		return nil, fmt.Errorf("read the request: %w", err)
	}
	stamp := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	out.RequestedAt, out.DueAt = stamp(requestedAt), stamp(dueAt)
	var started time.Time
	err = database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `SELECT timestamp FROM operator_audit WHERE target_org = $1 AND action = 'org.deletion.purge_started'
			ORDER BY id DESC LIMIT 1`, orgID).Scan(&started)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return nil
		}
		return qerr
	})
	if err != nil {
		return nil, fmt.Errorf("read the start of the purge: %w", err)
	}
	if !started.IsZero() {
		out.PurgeStarted = stamp(&started)
	}
	ended := p.now().UTC()
	out.PurgeEnded = stamp(&ended)
	for _, r := range results {
		out.Rows += r.RowsBefore
	}
	if p.external != nil {
		progress, rerr := p.readExternal(ctx, orgID)
		if rerr != nil {
			return nil, fmt.Errorf("read the external purge: %w", rerr)
		}
		for name, counts := range progress.Steps {
			out.Objects[name] = counts
			if strings.HasPrefix(name, "objects:") {
				out.ObjectsTotal += counts["objects"]
			}
		}
	}
	if out.SHA256, err = ProtocolHash(out); err != nil {
		return nil, err
	}
	return out, nil
}

// recordProtocol writes org.purged once (a run after a crash between it
// and purged finds it).
func (p *Purger) recordProtocol(ctx context.Context, orgID string, results []PurgeTableResult) error {
	if _, _, found, err := p.protocolEntry(ctx, orgID); err != nil || found {
		return err
	}
	protocol, err := p.buildProtocol(ctx, orgID, results)
	if err != nil {
		return fmt.Errorf("org purge: protocol: %w", err)
	}
	raw, err := json.Marshal(protocol)
	if err != nil {
		return err
	}
	var details map[string]any
	if uerr := json.Unmarshal(raw, &details); uerr != nil {
		return uerr
	}
	if err = p.auditor.Record(ctx, &operator.AuditEntry{OperatorID: purgeOperator, OperatorKind: operator.KindSystem,
		Action: ActionOrgPurged, TargetOrg: orgID, Status: 200, Details: details}); err != nil {
		return fmt.Errorf("org purge: record the protocol: %w", err)
	}
	return nil
}

// notice is a row of org_deletion_notice.
type notice struct {
	target   string
	sealed   []byte
	wrapped  []byte
	locale   string
	attempts int
}

// deliverProtocols sends the protocol of every purged organization with a
// stored address; it returns the first error after trying all of them.
func (p *Purger) deliverProtocols(ctx context.Context) error {
	var targets []string
	err := database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `SELECT target_org::text FROM org_deletion_notice ORDER BY created_at, target_org`)
		if qerr != nil {
			return qerr
		}
		targets, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
		return qerr
	})
	if err != nil {
		return fmt.Errorf("org purge: read the stored addresses: %w", err)
	}
	var first error
	for _, target := range targets {
		var state string
		serr := database.WithSystem(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT COALESCE(max(deletion_state), '') FROM organization WHERE id = $1`, target).Scan(&state)
		})
		if serr == nil && state == DeletionPurged {
			serr = p.DeliverProtocol(ctx, target)
		}
		if serr != nil && first == nil {
			first = serr
		}
	}
	return first
}

// DeliverProtocol sends the protocol of a purged organization to the
// stored address and deletes the address; without central SMTP, without a
// key or after the third failed attempt the address is deleted as well and
// org.deletion.protocol_undelivered recorded. Without a stored address it
// does nothing.
func (p *Purger) DeliverProtocol(ctx context.Context, orgID string) error {
	var n *notice
	err := database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		var row notice
		qerr := tx.QueryRow(ctx, `SELECT target_org::text, recipient_enc, wrapped_dek, locale, attempts
			FROM org_deletion_notice WHERE target_org = $1`, orgID).Scan(&row.target, &row.sealed, &row.wrapped, &row.locale, &row.attempts)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return nil
		}
		n = &row
		return qerr
	})
	if err != nil || n == nil {
		return err
	}
	auditID, protocol, found, err := p.protocolEntry(ctx, orgID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("org purge: no protocol of %s to send", orgID)
	}
	if p.mailer == nil {
		return p.giveUp(ctx, orgID, auditID, "no_smtp", n.attempts)
	}
	enc := noticeEncryptor.Load()
	if enc == nil {
		return p.giveUp(ctx, orgID, auditID, "no_key", n.attempts)
	}
	dek, err := enc.UnwrapDEK(n.wrapped)
	if err != nil {
		return p.giveUp(ctx, orgID, auditID, "no_key", n.attempts)
	}
	address, err := crypto.Decrypt(dek, n.sealed)
	if err != nil {
		return p.giveUp(ctx, orgID, auditID, "no_key", n.attempts)
	}
	parse := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	mail := &notify.PurgeProtocolMail{To: string(address), Locale: n.locale, AuditID: auditID, Hash: protocol.SHA256,
		RequestedAt: parse(protocol.RequestedAt), DueAt: parse(protocol.DueAt), PurgeStarted: parse(protocol.PurgeStarted),
		PurgeEnded: parse(protocol.PurgeEnded), Rows: protocol.Rows, Objects: protocol.ObjectsTotal, Counts: protocol.CountsText()}
	sendErr := p.mailer.SendPurgeProtocol(ctx, mail)
	switch {
	case sendErr == nil:
		return database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
			_, derr := tx.Exec(ctx, `DELETE FROM org_deletion_notice WHERE target_org = $1`, orgID)
			return derr
		})
	case errors.Is(sendErr, notify.ErrNoCentralSMTP):
		return p.giveUp(ctx, orgID, auditID, "no_smtp", n.attempts)
	case n.attempts+1 >= maxProtocolAttempts:
		slog.WarnContext(ctx, "org purge: the protocol could not be sent", "organization_id", orgID, "error", sendFailure(sendErr))
		return p.giveUp(ctx, orgID, auditID, "attempts", n.attempts+1)
	default:
		slog.WarnContext(ctx, "org purge: the protocol could not be sent; retried", "organization_id", orgID, "error", sendFailure(sendErr))
		return database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
			_, uerr := tx.Exec(ctx, `UPDATE org_deletion_notice SET attempts = attempts + 1 WHERE target_org = $1`, orgID)
			return uerr
		})
	}
}

// sendFailure is the loggable form of a failed send: the SMTP reply code
// only. The errors of notify.SMTPSender name the recipient ("smtp RCPT TO
// <address>: …", "recipient \"<address>\": …"), and the address of the
// requester must not outlive the purge in the server logs.
func sendFailure(err error) string {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		return fmt.Sprintf("smtp reply %d", reply.Code)
	}
	return "the e-mail could not be sent"
}

// giveUp deletes the stored address and records
// org.deletion.protocol_undelivered with the reason in one transaction.
func (p *Purger) giveUp(ctx context.Context, orgID string, auditID int64, reason string, attempts int) error {
	err := database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, derr := tx.Exec(ctx, `DELETE FROM org_deletion_notice WHERE target_org = $1`, orgID); derr != nil {
			return derr
		}
		return operator.RecordTx(ctx, tx, &operator.AuditEntry{OperatorID: purgeOperator, OperatorKind: operator.KindSystem,
			Action: ActionProtocolUndelivered, TargetOrg: orgID, Status: 200,
			Details: map[string]any{"reason": reason, "attempts": attempts, "protocol_entry": auditID}})
	})
	if err != nil {
		return fmt.Errorf("org purge: give up the protocol: %w", err)
	}
	return nil
}
