package credential

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Rotation (CRD-05, WP-897): after a change of what a collector's bundle
// holds – the secret replaced (PUT /credentials/{id}/secret), a reference
// changed (PATCH of vault_path, vault_version, field_map) or a binding
// created, changed or deleted – the collectors whose bundle names the
// credential before or after the change get exactly one message
// collector.<org>.<id>.bundle.changed each, sent after the commit
// (database.AfterCommit); a rollback sends nothing. They fetch their bundle
// again within 20 s (collector/collectorcmd/bundle.go). Which collectors
// are affected decides the resolution of WP-891: the BundleTarget of each
// collector (BundleCollectorSource, discovery.BundleGate), plus the
// collectors of a vault connection whose login changed. A vault value
// without a fixed version that the server reads in a new version emits
// credential.rotated with source vault once per version. The daily system
// job credential.age_check reports every stored secret beyond its
// max_age_days once per secret_version as credential.rotation_due.

// BundleChangedSuffix is appended to the collector's subject tree
// (collector.<org>.<id>) for the reload notice.
const BundleChangedSuffix = ".bundle.changed"

// BundleCollectorSource names the collectors whose bundle
// (BundleTarget.CredentialIDs) holds one of the credentials;
// discovery.BundleGate implements it.
type BundleCollectorSource interface {
	CollectorsWithCredentials(ctx context.Context, orgID string, credentialIDs []string) ([]string, error)
}

// Rotation sends the reload notices and the rotation events.
type Rotation struct {
	pool   *pgxpool.Pool
	nc     *nats.Conn
	source BundleCollectorSource
	now    func() time.Time
}

// NewRotation returns the rotation on pool (the events), nc (the notices;
// nil sends none) and source.
func NewRotation(pool *pgxpool.Pool, nc *nats.Conn, source BundleCollectorSource) *Rotation {
	return &Rotation{pool: pool, nc: nc, source: source, now: func() time.Time { return time.Now().UTC() }}
}

// WithRotation lets the service notify the collectors of changes and emit
// vault rotations (WP-897); copies made afterwards share it.
func (s *Service) WithRotation(r *Rotation) *Service {
	s.rotation = r
	return s
}

// affected returns the collectors whose bundle holds one of ids, read in
// its own organization-wide transaction, plus the collectors of the vault
// connections that log in with one of them. A failure is logged: the
// collectors then reload on their 15-minute cycle.
func (s *Service) affected(ctx context.Context, orgID string, ids []string) []string {
	ctx = database.WithoutTx(ctx)
	var out []string
	if s.rotation.source != nil {
		collectors, err := s.rotation.source.CollectorsWithCredentials(ctx, orgID, ids)
		if err != nil {
			slog.WarnContext(ctx, "bundle reload: affected collectors not determined", "organization_id", orgID, "error", err)
		}
		out = append(out, collectors...)
	}
	conns, err := s.vaults.List(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "bundle reload: vault connections not read", "organization_id", orgID, "error", err)
	}
	for i := range conns {
		if conns[i].ResolveAt == ResolveAtCollector && slices.Contains(ids, conns[i].AuthCredentialID) {
			out = append(out, conns[i].CollectorIDs...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// notifyChange runs change and, once its transaction committed, notifies
// the collectors whose bundle held one of ids before or holds one after
// it, each once. Without a rotation it only runs change.
func (s *Service) notifyChange(ctx context.Context, orgID string, ids func() []string, change func() error) error {
	if s.rotation == nil {
		return change()
	}
	before := s.affected(ctx, orgID, ids())
	if err := change(); err != nil {
		return err
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		after := s.affected(ctx, orgID, ids())
		all := append(slices.Clone(before), after...)
		slices.Sort(all)
		s.rotation.publish(ctx, orgID, slices.Compact(all))
	})
	return nil
}

// publish sends bundle.changed to each collector.
func (r *Rotation) publish(ctx context.Context, orgID string, collectors []string) {
	if r.nc == nil {
		return
	}
	for _, id := range collectors {
		if err := r.nc.Publish("collector."+orgID+"."+id+BundleChangedSuffix, []byte(`{}`)); err != nil {
			slog.WarnContext(ctx, "bundle reload notice not sent", "collector_id", id, "error", err)
		}
	}
	if err := r.nc.Flush(); err != nil {
		slog.WarnContext(ctx, "bundle reload notices not flushed", "error", err)
	}
}

// --- vault versions (AK3) --------------------------------------------------

// vaultVersionSeen records the version a server-side read of an entry
// without a fixed version returned. For each credential of the
// organization that references the entry without a fixed version (read
// organization-wide, whatever scope the read ran in) the first version
// recorded is its baseline (notice_sent, no event); every other version
// emits credential.rotated with source vault, once per version
// (notice_sent credential/<id>/vault/<version>). Baseline and versions
// live in the database, not in the process: a restart, another replica or
// a credential created later loses no change, and a failure is tried
// again with the next read. A fresh read happens at most once per TTL (300
// s) and entry, so does this check.
func (s *Service) vaultVersionSeen(ctx context.Context, orgID, connectionID, path string, version int) {
	r := s.rotation
	if r == nil || version < 1 || r.pool == nil {
		return
	}
	ctx = database.WithoutTx(ctx)
	refs, err := r.vaultReferences(ctx, orgID, connectionID, path)
	if err != nil {
		slog.WarnContext(ctx, "vault rotation: credentials not read", "connection_id", connectionID, "error", err)
		return
	}
	for _, c := range refs {
		if err = r.recordVaultVersion(ctx, orgID, &c, version); err != nil {
			slog.WarnContext(ctx, "vault rotation event not appended", "credential_id", c.ID, "error", err)
		}
	}
}

// vaultRefCredential is a credential that references a vault entry
// without a fixed version.
type vaultRefCredential struct{ ID, ClientID, Kind string }

// vaultReferences returns the credentials of the organization that
// reference path of the connection without a fixed version, in an
// organization-wide transaction.
func (r *Rotation) vaultReferences(ctx context.Context, orgID, connectionID, path string) ([]vaultRefCredential, error) {
	var out []vaultRefCredential
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(client_id::text, ''), kind FROM credential
			WHERE organization_id = $1 AND source = 'vault_ref' AND vault_connection_id = $2 AND vault_path = $3
			  AND vault_version IS NULL
			ORDER BY id`, orgID, connectionID, path)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c vaultRefCredential
			if serr := rows.Scan(&c.ID, &c.ClientID, &c.Kind); serr != nil {
				return serr
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// recordVaultVersion records version for c in its own transaction: the
// baseline when no version of c was recorded before, else
// credential.rotated once per version.
func (r *Rotation) recordVaultVersion(ctx context.Context, orgID string, c *vaultRefCredential, version int) error {
	prefix := "credential/" + c.ID + "/vault/"
	key := prefix + strconv.Itoa(version)
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var known, recorded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notice_sent
				WHERE organization_id = $1 AND kind = $2 AND starts_with(dedup_key, $3)),
			EXISTS (SELECT 1 FROM notice_sent WHERE organization_id = $1 AND kind = $2 AND dedup_key = $4)`,
			orgID, events.CredentialRotated, prefix, key).Scan(&known, &recorded); err != nil {
			return fmt.Errorf("recorded vault versions: %w", err)
		}
		if recorded {
			return nil // the version was recorded before: nothing to write
		}
		if !known {
			// The first version of the credential: its baseline, no
			// rotation.
			_, err := tx.Exec(ctx, `INSERT INTO notice_sent (organization_id, kind, dedup_key, sent_at) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, orgID, events.CredentialRotated, key, r.now())
			return err
		}
		data := events.CredentialEventData{ID: c.ID, ClientID: c.ClientID, Kind: c.Kind, SecretVersion: version, Source: "vault"}
		ev := &events.Event{OrgID: orgID, Type: events.CredentialRotated, EntityID: c.ID, Data: data}
		_, err := recordNotice(ctx, tx, r.now(), key, ev)
		return err
	})
}

// recordNotice records the notice of ev in notice_sent (kind = event type,
// dedup_key = key) and appends ev to the outbox in tx as the last write
// (the pattern of sendNotice in internal/jobs). A notice sent before
// appends nothing and reports false.
func recordNotice(ctx context.Context, tx pgx.Tx, now time.Time, key string, ev *events.Event) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO notice_sent (organization_id, kind, dedup_key, sent_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, ev.OrgID, ev.Type, key, now)
	if err != nil {
		return false, fmt.Errorf("record notice %s: %w", ev.Type, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err = events.NewOutbox().Append(ctx, tx, ev); err != nil {
		return false, err
	}
	return true, nil
}

// --- age check (AK4, AK5) --------------------------------------------------

// AgeCheckKind is the scheduler kind of the daily age check.
const AgeCheckKind = "credential.age_check"

// AgeCheck is the system job credential.age_check.
type AgeCheck struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewAgeCheck returns the age check on pool.
func NewAgeCheck(pool *pgxpool.Pool) *AgeCheck {
	return &AgeCheck{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (a *AgeCheck) WithClock(now func() time.Time) *AgeCheck {
	a.now = now
	return a
}

// Schedule runs the check daily at 05:17 UTC (leader only, like every
// system job); a missed day is caught up once.
func (a *AgeCheck) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: AgeCheckKind, Spec: "17 5 * * *", MaxCatchUp: 1, Timeout: 15 * time.Minute,
		Run: func(ctx context.Context) error {
			sent, err := a.Run(ctx)
			if sent > 0 {
				slog.InfoContext(ctx, "credential age check", "notices", sent)
			}
			return err
		}}
}

// Run reports every stored credential whose secret is older than its
// max_age_days once per secret_version (credential.rotation_due, dedup
// credential/<id>/<secret_version> in notice_sent) and returns the number
// of notices sent. Each organization runs in its own transaction; a
// failing one does not stop the others.
func (a *AgeCheck) Run(ctx context.Context) (int, error) {
	orgs, err := database.OrganizationIDs(ctx, a.pool)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", AgeCheckKind, err)
	}
	now := a.now()
	total := 0
	var errs []error
	for _, orgID := range orgs {
		sent := 0
		scope := database.OrgWideScope(orgID, "")
		err = database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var ferr error
			sent, ferr = a.runOrg(ctx, tx, orgID, now)
			return ferr
		})
		if err != nil {
			slog.ErrorContext(ctx, "credential age check", "organization_id", orgID, "error", err)
			errs = append(errs, fmt.Errorf("%s for %s: %w", AgeCheckKind, orgID, err))
			continue
		}
		total += sent
	}
	return total, errors.Join(errs...)
}

func (a *AgeCheck) runOrg(ctx context.Context, tx pgx.Tx, orgID string, now time.Time) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(client_id::text, ''), kind, secret_version, max_age_days,
			COALESCE(rotated_at, created_at) + make_interval(days => max_age_days)
		FROM credential
		WHERE organization_id = $1 AND source = 'stored' AND max_age_days IS NOT NULL
		  AND COALESCE(rotated_at, created_at) + make_interval(days => max_age_days) < $2
		ORDER BY id`, orgID, now)
	if err != nil {
		return 0, fmt.Errorf("due credentials: %w", err)
	}
	type due struct {
		id, client, kind string
		version, maxAge  int
		dueAt            time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.id, &d.client, &d.kind, &d.version, &d.maxAge, &d.dueAt); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return 0, rerr
	}
	sent := 0
	for _, d := range list {
		ev := &events.Event{OrgID: orgID, Type: events.CredentialRotationDue, EntityID: d.id,
			Data: events.CredentialRotationDueData{ID: d.id, CredentialID: d.id, ClientID: d.client, Kind: d.kind, SecretVersion: d.version,
				Source: SourceStored, MaxAgeDays: d.maxAge, DueAt: d.dueAt.UTC().Format(time.RFC3339)}}
		ok, nerr := recordNotice(ctx, tx, now, "credential/"+d.id+"/"+strconv.Itoa(d.version), ev)
		if nerr != nil {
			return sent, nerr
		}
		if ok {
			sent++
		}
	}
	return sent, nil
}
