package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// Key rotation runs of SEC-06 on the scheduler:
//   - rotation.master_key rewraps every data key that a previous master key
//     wrapped (org DEKs, notification channel keys); once none is left the
//     previous key can be removed from the configuration (retired);
//   - rotation.org_dek replaces each organization's DEK after a year and
//     re-encrypts its credentials, atomically, audited;
//   - rotation.webhook_secrets ends the 24-hour overlap of rotated webhook
//     secrets.
// JWT signing keys rotate with their kid in the identity layer, API keys
// through their rotate endpoint.

// Rotation kinds.
const (
	RotationMasterKey      = "rotation.master_key"
	RotationOrgDEK         = "rotation.org_dek"
	RotationWebhookSecrets = "rotation.webhook_secrets"
)

// DefaultOrgDEKAge is how long an organization's DEK is used (yearly).
const DefaultOrgDEKAge = 365 * 24 * time.Hour

// DEKRotator rotates and rewraps organization DEKs (credential store).
type DEKRotator interface {
	RotateOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (keyVersion, rotated int, err error)
	RewrapOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (bool, error)
}

// SecretCleaner ends expired webhook secret overlaps.
type SecretCleaner interface {
	ClearExpiredSecrets(ctx context.Context, now time.Time) (int, error)
}

// Rotation runs the key rotations.
type Rotation struct {
	pool     *pgxpool.Pool
	enc      *crypto.EnvelopeEncryptor
	deks     DEKRotator
	webhooks SecretCleaner
	// OrgDEKAge is how long a DEK is used before it is replaced.
	OrgDEKAge time.Duration
	now       func() time.Time
}

// NewRotation creates the rotation runs.
func NewRotation(pool *pgxpool.Pool, enc *crypto.EnvelopeEncryptor, deks DEKRotator, webhooks SecretCleaner) *Rotation {
	return &Rotation{pool: pool, enc: enc, deks: deks, webhooks: webhooks, OrgDEKAge: DefaultOrgDEKAge,
		now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (r *Rotation) WithClock(now func() time.Time) *Rotation {
	r.now = now
	return r
}

// Schedules are the rotation runs for the scheduler.
func (r *Rotation) Schedules() []Schedule {
	return []Schedule{
		{Kind: RotationMasterKey, Spec: "23 2 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: func(ctx context.Context) error {
			_, err := r.RewrapMasterKey(ctx)
			return err
		}},
		{Kind: RotationOrgDEK, Spec: "33 2 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: func(ctx context.Context) error {
			_, err := r.RotateOrgDEKs(ctx)
			return err
		}},
		{Kind: RotationWebhookSecrets, Spec: "*/15 * * * *", MaxCatchUp: 1, Timeout: 5 * time.Minute, Run: func(ctx context.Context) error {
			_, err := r.webhooks.ClearExpiredSecrets(ctx, r.now())
			return err
		}},
	}
}

// organizations lists every organization; the key tables are read in the
// organization's own transaction.
func (r *Rotation) organizations(ctx context.Context) ([]string, error) {
	var orgs []string
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return orgs, err
}

// RewrapMasterKey moves every data key to the current master key and
// returns how many it rewrapped.
func (r *Rotation) RewrapMasterKey(ctx context.Context) (int, error) {
	orgs, err := r.organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	rewrapped := 0
	for _, orgID := range orgs {
		changed, err := r.deks.RewrapOrgDEK(ctx, orgID, r.enc)
		if err != nil {
			return rewrapped, fmt.Errorf("organization %s: %w", orgID, err)
		}
		if changed {
			rewrapped++
		}
		n, err := r.rewrapChannels(ctx, orgID)
		if err != nil {
			return rewrapped, fmt.Errorf("organization %s: %w", orgID, err)
		}
		rewrapped += n
	}
	slog.Info("master key rewrap", "key_id", r.enc.KeyID(), "rewrapped", rewrapped,
		"note", "all data keys use the current master key; previous master keys can be retired")
	return rewrapped, nil
}

func (r *Rotation) rewrapChannels(ctx context.Context, orgID string) (int, error) {
	n := 0
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, config_dek FROM notification_channel
			WHERE organization_id = $1 AND config_dek IS NOT NULL FOR UPDATE`, orgID)
		if err != nil {
			return err
		}
		type key struct {
			id  string
			dek []byte
		}
		keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (key, error) {
			var k key
			scanErr := row.Scan(&k.id, &k.dek)
			return k, scanErr
		})
		if err != nil {
			return err
		}
		for _, k := range keys {
			if r.enc.IsCurrent(k.dek) {
				continue
			}
			rewrapped, err := r.enc.Rewrap(k.dek)
			if err != nil {
				return fmt.Errorf("channel %s: %w", k.id, err)
			}
			if _, err = tx.Exec(ctx, `UPDATE notification_channel SET config_dek = $2 WHERE id = $1`, k.id, rewrapped); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RotateOrgDEKs replaces the DEKs older than OrgDEKAge and returns how many
// it replaced; each rotation is audited.
func (r *Rotation) RotateOrgDEKs(ctx context.Context) (int, error) {
	orgs, err := r.organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	rotated := 0
	for _, orgID := range orgs {
		due := false
		scope := database.OrgWideScope(orgID, "")
		if err = database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM org_dek WHERE organization_id = $1 AND created_at < $2)`,
				orgID, r.now().Add(-r.OrgDEKAge)).Scan(&due)
		}); err != nil {
			return rotated, fmt.Errorf("organization %s: %w", orgID, err)
		}
		if !due {
			continue
		}
		version, credentials, err := r.deks.RotateOrgDEK(ctx, orgID, r.enc)
		if err != nil {
			return rotated, fmt.Errorf("organization %s: %w", orgID, err)
		}
		rotated++
		if auditErr := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, recErr := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorType: "system",
				Action: "key.org_dek_rotated", ResourceType: "org_dek", Source: audit.SourceSystem,
				Changes: map[string]interface{}{"key_version": version, "credentials_reencrypted": credentials}})
			return recErr
		}); auditErr != nil {
			return rotated, auditErr
		}
	}
	return rotated, nil
}
