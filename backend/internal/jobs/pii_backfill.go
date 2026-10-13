package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// PIIBackfillKind is the run that seals the personal data of existing
// history entries (SEC-05, WP-241).
const PIIBackfillKind = "pii.backfill"

// PIIBackfill replaces plaintext personal values in existing audit
// entries, CI changes and events with vault surrogates, per organization
// in the organization's transaction. Rewritten audit entries are
// re-chained (audit_rechain, migration 000125), so audit-verify stays
// intact; entries written since migration 000125 are sealed already, so
// later runs find nothing.
type PIIBackfill struct {
	pool *pgxpool.Pool
}

// NewPIIBackfill creates the run on pool.
func NewPIIBackfill(pool *pgxpool.Pool) *PIIBackfill { return &PIIBackfill{pool: pool} }

// Schedule runs the backfill daily.
func (b *PIIBackfill) Schedule() Schedule {
	return Schedule{Kind: PIIBackfillKind, Spec: "@every 24h", MaxCatchUp: 1, Timeout: time.Hour,
		Run: func(ctx context.Context) error {
			n, err := b.Run(ctx)
			if n > 0 {
				slog.Info("sealed personal data of the history", "rows", n)
			}
			return err
		}}
}

// BackfillResult counts the rows a backfill sealed.
type BackfillResult struct {
	Audit, CIChanges, Events, Rechained int
}

// Total is the number of rows sealed.
func (r BackfillResult) Total() int { return r.Audit + r.CIChanges + r.Events }

// Run seals every organization and returns how many rows it changed.
func (b *PIIBackfill) Run(ctx context.Context) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, b.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	total := 0
	for _, org := range orgs {
		res, rerr := b.RunOrg(ctx, org)
		if rerr != nil {
			return total, fmt.Errorf("organization %s: %w", org, rerr)
		}
		total += res.Total()
	}
	return total, nil
}

// RunOrg seals the history of one organization in one transaction.
func (b *PIIBackfill) RunOrg(ctx context.Context, orgID string) (BackfillResult, error) {
	var res BackfillResult
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, b.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if res.Audit, res.Rechained, err = sealAudit(ctx, tx, orgID); err != nil {
			return err
		}
		if res.CIChanges, err = sealCIChanges(ctx, tx, orgID); err != nil {
			return err
		}
		res.Events, err = sealEvents(ctx, tx, orgID)
		if err == nil && res.Total() > 0 {
			_, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorType: "system",
				Action: PIIBackfillKind, ResourceType: "organization", ResourceID: orgID,
				Changes: map[string]interface{}{"audit_entries": res.Audit, "ci_changes": res.CIChanges, "events": res.Events}})
		}
		return err
	})
	return res, err
}

type jsonRow struct {
	id    string
	seq   int64
	field string
	a, b  []byte
}

func sealAudit(ctx context.Context, tx pgx.Tx, orgID string) (sealed, rechained int, err error) {
	// The chain lock of the insert trigger: no entry is appended meanwhile.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1382380649, audit_org_lock_key($1))`, orgID); err != nil {
		return 0, 0, fmt.Errorf("lock audit chain: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text, seq, changes::text FROM audit_log
		WHERE organization_id = $1 AND changes::text ~ $2 ORDER BY seq`, orgID, pii.KeyPattern())
	if err != nil {
		return 0, 0, fmt.Errorf("find audit entries: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (jsonRow, error) {
		var j jsonRow
		scanErr := r.Scan(&j.id, &j.seq, &j.a)
		return j, scanErr
	})
	if err != nil || len(found) == 0 {
		return 0, 0, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.audit_pii_migration', 'on', true)`); err != nil {
		return 0, 0, err
	}
	from := int64(-1)
	for _, j := range found {
		out, changed, serr := sealJSON(ctx, tx, orgID, "", j.a)
		if serr != nil || !changed {
			if serr != nil {
				return 0, 0, fmt.Errorf("audit entry %s: %w", j.id, serr)
			}
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE audit_log SET changes = $2::jsonb WHERE id = $1`, j.id, out); err != nil {
			return 0, 0, fmt.Errorf("audit entry %s: %w", j.id, err)
		}
		if from < 0 {
			from = j.seq
		}
		sealed++
	}
	if from >= 0 {
		if err = tx.QueryRow(ctx, `SELECT audit_rechain($1, $2)`, orgID, from).Scan(&rechained); err != nil {
			return 0, 0, fmt.Errorf("re-chain audit log: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `SELECT set_config('app.audit_pii_migration', 'off', true)`)
	return sealed, rechained, err
}

func sealCIChanges(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(field_name, ''), COALESCE(old_value, 'null'::jsonb)::text,
			COALESCE(new_value, 'null'::jsonb)::text
		FROM ci_change WHERE organization_id = $1
		  AND (old_value::text ~ $2 OR new_value::text ~ $2 OR
		       (jsonb_typeof(old_value) = 'string' OR jsonb_typeof(new_value) = 'string')
		       AND lower(regexp_replace(COALESCE(field_name, ''), '^.*\.', '')) = ANY($3))`,
		orgID, pii.KeyPattern(), pii.PersonalKeys())
	if err != nil {
		return 0, fmt.Errorf("find ci changes: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (jsonRow, error) {
		var j jsonRow
		scanErr := r.Scan(&j.id, &j.field, &j.a, &j.b)
		return j, scanErr
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range found {
		oldOut, oldChanged, oerr := sealJSON(ctx, tx, orgID, j.field, j.a)
		if oerr != nil {
			return 0, fmt.Errorf("ci change %s: %w", j.id, oerr)
		}
		newOut, newChanged, nerr := sealJSON(ctx, tx, orgID, j.field, j.b)
		if nerr != nil {
			return 0, fmt.Errorf("ci change %s: %w", j.id, nerr)
		}
		if !oldChanged && !newChanged {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE ci_change SET old_value = $2::jsonb, new_value = $3::jsonb WHERE id = $1`,
			j.id, oldOut, newOut); err != nil {
			return 0, fmt.Errorf("ci change %s: %w", j.id, err)
		}
		n++
	}
	return n, nil
}

func sealEvents(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, data::text FROM event_outbox
		WHERE organization_id = $1 AND data::text ~ $2`, orgID, pii.KeyPattern())
	if err != nil {
		return 0, fmt.Errorf("find events: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (jsonRow, error) {
		var j jsonRow
		scanErr := r.Scan(&j.id, &j.a)
		return j, scanErr
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range found {
		out, changed, serr := sealJSON(ctx, tx, orgID, "", j.a)
		if serr != nil {
			return 0, fmt.Errorf("event %s: %w", j.id, serr)
		}
		if !changed {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE event_outbox SET data = $2::jsonb WHERE id = $1`, j.id, out); err != nil {
			return 0, fmt.Errorf("event %s: %w", j.id, err)
		}
		n++
	}
	return n, nil
}

// sealJSON seals a stored JSON value (of a field, when field is set).
func sealJSON(ctx context.Context, tx pgx.Tx, orgID, field string, raw []byte) (sealed string, changed bool, err error) {
	var v any
	if uerr := json.Unmarshal(raw, &v); uerr != nil {
		return "", false, uerr
	}
	out, changed, err := pii.SealFieldTx(ctx, tx, orgID, field, v)
	if err != nil || !changed {
		return string(raw), false, err
	}
	b, err := json.Marshal(out)
	return string(b), true, err
}
