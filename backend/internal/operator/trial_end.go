package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// KindSystem is the operator kind of a change the system makes on its own
// (the trial end, WP-884; migration 000290).
const KindSystem = "system"

// TrialEndKind is the scheduler kind of the trial end and the operator id
// of its audit entries.
const TrialEndKind = "entitlement.trial_end"

// trialEndReason is the reason of the plan change at the trial end.
const trialEndReason = "trial_end"

// errTrialEndDone rolls back a trial end recorded before (notice_sent).
var errTrialEndDone = errors.New("trial end already applied")

// TrialEnd switches an organization whose trial ended to the fallback plan
// of the trial (ENT-07, E-157, WP-884): every day it finds the
// organizations whose settings.trial.ends_at has passed, whose plan is still
// the trial plan and whose plan no operator changed (plan_changed_at is
// NULL), and changes each to settings.trial.fallback_plan with the default
// limits of that plan (TransitionOptions.ResetLimits; max_cis 100 → 500).
// Rows, data and CIs stay (ENT-08). Each change is one transaction with its
// plan.change entry (operator kind system, operator id
// entitlement.trial_end, reason trial_end), entitlement.changed and the
// notice_sent row (kind entitlement.trial_end, dedup_key ends_at), so a
// second run changes nothing.
type TrialEnd struct {
	pool  *pgxpool.Pool
	audit auditLog
	svc   *entitlement.Service
	now   func() time.Time
}

// NewTrialEnd creates the run on pool with the entitlement service.
func NewTrialEnd(pool *pgxpool.Pool, svc *entitlement.Service) *TrialEnd {
	return &TrialEnd{pool: pool, audit: NewAuditor(pool), svc: svc, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (e *TrialEnd) WithClock(now func() time.Time) *TrialEnd {
	e.now = now
	return e
}

// Schedule runs the trial end daily; a missed day is caught up once.
func (e *TrialEnd) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: TrialEndKind, Spec: "23 4 * * *", MaxCatchUp: 1, Timeout: 15 * time.Minute,
		Run: func(ctx context.Context) error {
			n, err := e.Run(ctx)
			if n > 0 {
				slog.Info("trials ended", "organizations", n)
			}
			return err
		}}
}

// endedTrial is an organization due for the trial end.
type endedTrial struct {
	orgID    string
	endsAt   string
	fallback entitlement.Plan
}

// due lists the organizations whose trial ended before now (system read,
// E-08).
func (e *TrialEnd) due(ctx context.Context) ([]endedTrial, error) {
	type row struct {
		id, plan string
		trial    []byte
	}
	var rows []row
	err := database.WithSystem(ctx, e.pool, func(ctx context.Context, tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT id::text, plan, settings->'trial' FROM organization
			WHERE settings ? 'trial' AND plan_changed_at IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(cr pgx.CollectableRow) (row, error) {
			var x row
			scanErr := cr.Scan(&x.id, &x.plan, &x.trial)
			return x, scanErr
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: list trials: %w", TrialEndKind, err)
	}
	now := e.now()
	var out []endedTrial
	for _, x := range rows {
		var trial struct {
			Plan         string `json:"plan"`
			EndsAt       string `json:"ends_at"`
			FallbackPlan string `json:"fallback_plan"`
		}
		if json.Unmarshal(x.trial, &trial) != nil {
			slog.Warn("trial end: unreadable settings.trial", "organization_id", x.id)
			continue
		}
		ends, parseErr := time.Parse(time.RFC3339, trial.EndsAt)
		if parseErr != nil || trial.FallbackPlan == "" {
			slog.Warn("trial end: settings.trial without ends_at or fallback_plan", "organization_id", x.id)
			continue
		}
		if !ends.Before(now) || x.plan != trial.Plan {
			continue
		}
		out = append(out, endedTrial{orgID: x.id, endsAt: trial.EndsAt, fallback: entitlement.Plan(trial.FallbackPlan)})
	}
	return out, nil
}

// Run ends the due trials and returns how many organizations changed. A
// failing organization does not stop the others; the next run retries it.
func (e *TrialEnd) Run(ctx context.Context) (int, error) {
	trials, err := e.due(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	var firstErr error
	for _, trial := range trials {
		err = e.end(ctx, trial)
		switch {
		case errors.Is(err, errTrialEndDone):
		case err != nil:
			slog.Error("trial end", "organization_id", trial.orgID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s for %s: %w", TrialEndKind, trial.orgID, err)
			}
		default:
			changed++
		}
	}
	return changed, firstErr
}

// end switches one organization to its fallback plan.
func (e *TrialEnd) end(ctx context.Context, trial endedTrial) error {
	now := e.now()
	record := func(ctx context.Context) error {
		return database.WithRequestTenant(ctx, e.pool, trial.orgID, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO notice_sent (organization_id, kind, dedup_key, sent_at)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, trial.orgID, TrialEndKind, trial.endsAt, now)
			if err != nil {
				return fmt.Errorf("record trial end: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return errTrialEndDone
			}
			return nil
		})
	}
	_, _, err := changePlanAudited(orgContext(ctx, trial.orgID), e.audit, e.svc, e.pool, trial.orgID, trial.fallback,
		entitlement.TransitionOptions{ResetLimits: true, Source: "selfsignup"},
		planChangeActor{ID: TrialEndKind, Kind: KindSystem, Reason: trialEndReason}, record)
	return err
}
