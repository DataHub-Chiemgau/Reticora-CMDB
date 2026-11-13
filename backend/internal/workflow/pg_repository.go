package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defCols = `id::text, organization_id::text, name, description, trigger, conditions, actions, active, version, current_version_id::text, created_at, updated_at`
const versionCols = `id::text, organization_id::text, workflow_id::text, version, trigger, conditions, actions, COALESCE(created_by::text, ''), created_at`
const runCols = `id::text, organization_id::text, workflow_id::text, definition_version_id::text, status, trigger, context, started_at, finished_at, attempts, next_attempt_at, COALESCE(last_error, ''), COALESCE(initiated_by::text, ''), COALESCE(ci_id::text, ''), COALESCE(serial_key, ''), entity_seq, enqueues, created_at, updated_at`

// serialLock takes the transaction advisory lock of a serial key of the
// organization (WP-1089 AK2); claims and the pick of the next run of a key
// wait for each other.
const serialLock = `SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0))`

// serialOrder is the order in which the pending runs of a serial key are
// executed: the sequence of their events, then their creation.
const serialOrder = `ORDER BY entity_seq ASC NULLS LAST, created_at ASC, id ASC`
const stepCols = `id::text, organization_id::text, run_id::text, step_index, action_type, status, input, output, error, created_at, updated_at`

type PGRepository struct {
	pool *pgxpool.Pool
	// afterAppend is a test hook: an error after the event of AppendEvent
	// rolls its transaction back (WP-1096).
	afterAppend func() error
}

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }
func (r *PGRepository) ListDefinitions(ctx context.Context, orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
	var out []Definition
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id=$1"
		if activeOnly {
			where += " AND active=true"
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM workflow_def WHERE "+where, orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+defCols+" FROM workflow_def WHERE "+where+" ORDER BY name ASC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDef(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) GetDefinition(ctx context.Context, orgID, id string) (*Definition, error) {
	var d *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = scanDef(tx.QueryRow(ctx, "SELECT "+defCols+" FROM workflow_def WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return d, err
}

// CreateDefinition stores the definition and its version 1 in one
// transaction (WFL-02); current_version_id is checked at commit
// (DEFERRABLE), so the definition can name the version before it exists.
func (r *PGRepository) CreateDefinition(ctx context.Context, d *Definition) error {
	normalizeDefinition(d)
	if err := requireScheduleScope(ctx, d.Trigger); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO workflow_def (organization_id,name,description,trigger,conditions,actions,active,version,current_version_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,1,gen_random_uuid())
			RETURNING id::text, version, current_version_id::text, created_at, updated_at`,
			d.OrganizationID, d.Name, d.Description, d.Trigger, d.Conditions, d.Actions, d.Active).
			Scan(&d.ID, &d.Version, &d.CurrentVersionID, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return err
		}
		return insertVersion(ctx, tx, d, createdBy(ctx))
	})
}

// insertVersion stores the trigger, conditions and actions of d as its
// version d.Version under the id d.CurrentVersionID. created_by stays NULL
// for a user that is not one of the organization.
func insertVersion(ctx context.Context, tx pgx.Tx, d *Definition, user string) error {
	var by any
	if api.IsUUID(user) {
		by = user
	}
	_, err := tx.Exec(ctx, `INSERT INTO workflow_def_version (id, organization_id, workflow_id, version, trigger, conditions, actions, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, (SELECT u.id FROM app_user u WHERE u.id = $8::uuid AND u.organization_id = $2))`,
		d.CurrentVersionID, d.OrganizationID, d.ID, d.Version, d.Trigger, d.Conditions, d.Actions, by)
	return err
}

// UpdateDefinition changes a definition under If-Match (API-07). A change
// of trigger, conditions or actions stores version n+1 in the same
// transaction and points the definition at it; name, description and
// active change no version (WFL-02, E-228).
func (r *PGRepository) UpdateDefinition(ctx context.Context, orgID, id string, req *UpdateDefinitionRequest) (*Definition, error) {
	var d *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanDef(tx.QueryRow(ctx, "SELECT "+defCols+" FROM workflow_def WHERE id=$1 AND organization_id=$2 FOR UPDATE", id, orgID))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !ifMatches(req.IfMatch, current.Version) {
			return ErrPreconditionFailed
		}
		next := *current
		applyUpdate(&next, req)
		if verr := requireScheduleScope(ctx, current.Trigger, next.Trigger); verr != nil {
			return verr
		}
		if verr := validateEffective(current, &next); verr != nil {
			return verr
		}
		newVersion := contentChanged(current, &next)
		if !newVersion && req.Name == nil && req.Description == nil && req.Active == nil {
			d = current
			return nil
		}
		if newVersion {
			next.Version = current.Version + 1
			err = tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&next.CurrentVersionID)
			if err != nil {
				return err
			}
			err = insertVersion(ctx, tx, &next, req.CreatedBy)
			if err != nil {
				return err
			}
		}
		d, err = scanDef(tx.QueryRow(ctx, `UPDATE workflow_def SET name=$3, description=$4, trigger=$5, conditions=$6, actions=$7, active=$8,
			version=$9, current_version_id=$10, updated_at=now() WHERE id=$1 AND organization_id=$2 RETURNING `+defCols,
			id, orgID, next.Name, next.Description, next.Trigger, next.Conditions, next.Actions, next.Active, next.Version, next.CurrentVersionID))
		return err
	})
	return d, err
}

// ListVersions lists the versions of a definition, newest first; an
// unknown definition or one of another organization is ErrNotFound.
func (r *PGRepository) ListVersions(ctx context.Context, orgID, workflowID string, page api.PaginationParams) ([]DefinitionVersion, int, error) {
	out := []DefinitionVersion{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM workflow_def WHERE organization_id=$1 AND id=$2)", orgID, workflowID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM workflow_def_version WHERE organization_id=$1 AND workflow_id=$2", orgID, workflowID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+versionCols+" FROM workflow_def_version WHERE organization_id=$1 AND workflow_id=$2 ORDER BY version DESC LIMIT $3 OFFSET $4",
			orgID, workflowID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetVersion returns version number version of a definition.
func (r *PGRepository) GetVersion(ctx context.Context, orgID, workflowID string, version int) (*DefinitionVersion, error) {
	return r.getVersion(ctx, orgID, "workflow_id=$2 AND version=$3", workflowID, version)
}

// GetVersionByID returns the version a run is bound to.
func (r *PGRepository) GetVersionByID(ctx context.Context, orgID, id string) (*DefinitionVersion, error) {
	return r.getVersion(ctx, orgID, "id=$2", id)
}

func (r *PGRepository) getVersion(ctx context.Context, orgID, where string, args ...any) (*DefinitionVersion, error) {
	var v *DefinitionVersion
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = scanVersion(tx.QueryRow(ctx, "SELECT "+versionCols+" FROM workflow_def_version WHERE organization_id=$1 AND "+where, append([]any{orgID}, args...)...))
		if err == pgx.ErrNoRows {
			return ErrVersionNotFound
		}
		return err
	})
	return v, err
}
func (r *PGRepository) DeleteDefinition(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM workflow_def WHERE organization_id=$1 AND id=$2", orgID, id)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListRuns implements Repository: the visible runs that match f, newest
// first on (created_at, id) with the keyset of api.NewestFirstSQL (WP-1092
// AK3), with their steps.
func (r *PGRepository) ListRuns(ctx context.Context, orgID string, f *RunFilter, page api.PaginationParams) ([]Run, int, error) {
	out := []Run{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The scope of a run is its client and site in row level security
		// (WP-1093): a run without CI is org-wide.
		where := []string{"organization_id=$1"}
		args := []any{orgID}
		add := func(cond string, v any) {
			args = append(args, v)
			where = append(where, fmt.Sprintf(cond, len(args)))
		}
		if f.WorkflowID != "" {
			add("workflow_id=$%d::uuid", f.WorkflowID)
		}
		if f.CIID != "" {
			add("ci_id=$%d::uuid", f.CIID)
		}
		if f.Status != "" {
			add("status=$%d", f.Status)
		}
		if f.Trigger != "" {
			add("trigger=$%d", f.Trigger)
		}
		if f.From != nil {
			add("created_at>=$%d", *f.From)
		}
		if f.To != nil {
			add("created_at<$%d", *f.To)
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM workflow_run WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		keyset, tail, pageArgs, err := api.NewestFirstSQL(page, len(args)+1)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+runCols+" FROM workflow_run WHERE "+clause+" AND "+keyset+" "+tail, append(args, pageArgs...)...)
		if err != nil {
			return err
		}
		// Collect run ids and close the rows BEFORE issuing the per-run step
		// queries: a nested query on the same connection while rows are open
		// fails with "conn busy".
		runIDs := make([]string, 0, page.Limit+1)
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, *run)
			runIDs = append(runIDs, run.ID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, id := range runIDs {
			steps, err := r.listStepsTx(ctx, tx, orgID, id)
			if err != nil {
				return fmt.Errorf("list steps for run %s: %w", id, err)
			}
			out[i].Steps = steps
		}
		return nil
	})
	return out, total, err
}
func (r *PGRepository) GetRun(ctx context.Context, orgID, id string) (*Run, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		if err != nil {
			return err
		}
		run.Steps, err = r.listStepsTx(ctx, tx, orgID, id)
		return err
	})
	return run, err
}

// GetRunForJob implements Repository: the run with its steps, read
// organization-wide whatever the scope of the caller (the pattern of
// RunJobOrigin).
func (r *PGRepository) GetRunForJob(ctx context.Context, orgID, id string) (*Run, error) {
	var run *Run
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(database.WithoutTx(ctx), r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		if err != nil {
			return err
		}
		run.Steps, err = r.listStepsTx(ctx, tx, orgID, id)
		return err
	})
	return run, err
}

// CreateRun stores a run. initiated_by is set only for a user of the
// organization; a principal without user row (a system or test identity)
// starts a run without initiator.
func (r *PGRepository) CreateRun(ctx context.Context, run *Run) error {
	return database.WithRequestTenant(ctx, r.pool, run.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if run.Context == nil {
			run.Context = JSONMap{}
		}
		if run.StartedAt.IsZero() {
			run.StartedAt = time.Now().UTC()
		}
		run.SerialKey = serialKeyOf(run.CIID)
		return tx.QueryRow(ctx, `INSERT INTO workflow_run (organization_id,workflow_id,definition_version_id,status,trigger,context,started_at,initiated_by,ci_id,serial_key,entity_seq)
			VALUES ($1,$2,$3,$4,$5,$6,$7,(SELECT u.id FROM app_user u WHERE u.organization_id=$1 AND u.id::text=NULLIF($8,'')),NULLIF($9,'')::uuid,NULLIF($10,''),$11)
			RETURNING id::text, COALESCE(initiated_by::text, ''), created_at, updated_at`,
			run.OrganizationID, run.WorkflowID, run.DefinitionVersionID, run.Status, run.Trigger, run.Context, run.StartedAt, run.InitiatedBy,
			run.CIID, run.SerialKey, run.EntitySeq).Scan(&run.ID, &run.InitiatedBy, &run.CreatedAt, &run.UpdatedAt)
	})
}

// UpdateRunStatus implements Repository: the change of the status is
// audited and, where a type exists, written to the outbox in the same
// transaction (recordRunTransition, WP-1092 AK4); a refused event rolls the
// change back.
func (r *PGRepository) UpdateRunStatus(ctx context.Context, orgID, id, status string, finished *time.Time, ctxm JSONMap) (*Run, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, "SELECT status FROM workflow_run WHERE organization_id=$1 AND id=$2 FOR UPDATE", orgID, id).Scan(&prev)
		if err == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		if err != nil {
			return err
		}
		if prev != status && endedStatus(prev) {
			// An ended run is changed only by RetryRun (WP-1092): a late
			// write of an executor that raced a cancel leaves it ended.
			return ErrRunEnded
		}
		run, err = scanRun(tx.QueryRow(ctx, "UPDATE workflow_run SET status=$3, finished_at=$4, context=COALESCE($5, context), updated_at=now() WHERE organization_id=$1 AND id=$2 RETURNING "+runCols, orgID, id, status, finished, ctxm))
		if err != nil {
			return err
		}
		run.Steps, err = r.listStepsTx(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		return recordRunTransition(ctx, tx, prev, run)
	})
	return run, err
}

// CancelRun implements Repository (WP-1092 AK1): the row lock orders the
// cancel with the claim of the run's job, which then finds it cancelled.
func (r *PGRepository) CancelRun(ctx context.Context, orgID, id string) (*Run, error) {
	return r.changeRun(ctx, orgID, id, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if !slices.Contains(cancellableStatuses, prev) {
			return nil, ErrRunNotCancellable
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_step SET status='cancelled', updated_at=now()
			WHERE organization_id=$1 AND run_id=$2 AND status IN ('pending', 'running', 'waiting_approval', 'waiting_input')`, orgID, id); err != nil {
			return nil, err
		}
		return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='cancelled', finished_at=now(), next_attempt_at=NULL, updated_at=now()
			WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, id))
	})
}

// DecideApproval implements Repository: under the row lock the run must
// still wait for its approval, so a cancel committed just before wins and
// the decision is ErrNotWaiting (WP-1092 AK1).
func (r *PGRepository) DecideApproval(ctx context.Context, orgID, id string, approved bool, output JSONMap) (*Run, error) {
	if output == nil {
		output = JSONMap{}
	}
	return r.changeRun(ctx, orgID, id, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if prev != StatusWaitingApproval {
			return nil, ErrNotWaiting
		}
		stepStatus, errText := StatusSucceeded, ""
		if !approved {
			stepStatus, errText = StatusCancelled, approvalRejectedText
		}
		tag, err := tx.Exec(ctx, `UPDATE workflow_step SET status=$3, output=$4, error=$5, updated_at=now()
			WHERE organization_id=$1 AND run_id=$2 AND status='waiting_approval'`, orgID, id, stepStatus, output, errText)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, fmt.Errorf("approval step not found: %w", ErrNotWaiting)
		}
		if !approved {
			return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='cancelled', finished_at=now(), next_attempt_at=NULL, updated_at=now()
				WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, id))
		}
		return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='pending', enqueues=enqueues+1, updated_at=now()
			WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, id))
	})
}

// RetryRun implements Repository (WP-1092 AK2).
func (r *PGRepository) RetryRun(ctx context.Context, orgID, id string) (*Run, error) {
	return r.changeRun(ctx, orgID, id, func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error) {
		if prev != StatusFailed {
			return nil, ErrRunNotRetryable
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_step SET status='pending', output='{}'::jsonb, error='', updated_at=now()
			WHERE organization_id=$1 AND run_id=$2 AND status='failed'`, orgID, id); err != nil {
			return nil, err
		}
		return scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET status='pending', finished_at=NULL, attempts=0, next_attempt_at=NULL,
			last_error=NULL, enqueues=enqueues+1, updated_at=now() WHERE organization_id=$1 AND id=$2 RETURNING `+runCols, orgID, id))
	})
}

// changeRun locks the visible run id, lets change move it from its status
// prev and records the transition (WP-1092): a run that is unknown, of
// another organization or outside the scope is ErrRunNotFound (TEN-04).
func (r *PGRepository) changeRun(ctx context.Context, orgID, id string, change func(ctx context.Context, tx pgx.Tx, prev string) (*Run, error)) (*Run, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, "SELECT status FROM workflow_run WHERE organization_id=$1 AND id=$2 FOR UPDATE", orgID, id).Scan(&prev)
		if err == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		if err != nil {
			return err
		}
		if run, err = change(ctx, tx, prev); err != nil {
			return err
		}
		if run.Steps, err = r.listStepsTx(ctx, tx, orgID, id); err != nil {
			return err
		}
		return recordRunTransition(ctx, tx, prev, run)
	})
	return run, err
}

// recordRunTransition writes the trail of a status change of run from prev
// in its transaction (WP-1092 AK4): the audit entry workflow_run.<status>
// with origin workflow_run, as the user of the request or, in the worker
// and the system jobs (no request), without a user; then, last before the
// commit (database.BeforeCommit, convention 20), the event of the new
// status. running raises workflow.run_started only on the first start of a
// run (from pending, no audit entry workflow_run.running yet): a retry,
// the continuation after an approval and a run retried through the API
// are no new start. pending and
// waiting_approval have no event type. An unchanged status writes nothing.
func recordRunTransition(ctx context.Context, tx pgx.Tx, prev string, run *Run) error {
	if prev == run.Status {
		return nil
	}
	// The first start of the run is the one without an earlier audit
	// entry workflow_run.running: a run that failed before its first step
	// and is retried has no step either, but started before (WP-1092 AK4).
	first := false
	if prev == StatusPending && run.Status == StatusRunning {
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM audit_log WHERE organization_id=$1 AND resource_type='workflow_run'
			AND resource_id=$2::uuid AND action='workflow_run.running')`, run.OrganizationID, run.ID).Scan(&first); err != nil {
			return fmt.Errorf("read the starts of the workflow run: %w", err)
		}
	}
	actor, actorType := "", "system"
	if _, inRequest := audit.RequestInfoFrom(ctx); inRequest {
		if id := tenant.FromContext(ctx).UserID; api.IsUUID(id) {
			actor, actorType = id, "user"
		}
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: run.OrganizationID, ActorID: actor, ActorType: actorType,
		Action: "workflow_run." + run.Status, ResourceType: "workflow_run", ResourceID: run.ID,
		Changes: map[string]any{
			"status":                map[string]any{"old": prev, "new": run.Status},
			"workflow_id":           run.WorkflowID,
			"definition_version_id": run.DefinitionVersionID,
			"trigger":               run.Trigger,
		},
	}); err != nil {
		return fmt.Errorf("audit the workflow run: %w", err)
	}
	typ := runEventType(prev, run.Status, first)
	if typ == "" {
		return nil
	}
	data := map[string]any{"id": run.ID, "run_id": run.ID, "workflow_id": run.WorkflowID,
		"definition_version_id": run.DefinitionVersionID, "trigger": run.Trigger, "status": run.Status}
	if run.CIID != "" {
		data["ci_id"] = run.CIID
	}
	// Client and site of the event are those of the run row in row level
	// security (WP-1093): a run without CI started by a principal of one
	// client, and a run whose CI was deleted, keep them as well.
	var client, site string
	err := tx.QueryRow(ctx, "SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM workflow_run WHERE organization_id=$1 AND id=$2",
		run.OrganizationID, run.ID).Scan(&client, &site)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if client != "" {
		data["client_id"] = client
	}
	if site != "" {
		data["site_id"] = site
	}
	return database.BeforeCommit(ctx, func() error {
		if _, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: run.OrganizationID, Type: typ, EntityID: run.ID, Data: data}); err != nil {
			return fmt.Errorf("event of the workflow run: %w", err)
		}
		return nil
	})
}

// runEventType is the event of a change of a run from prev to status
// (WP-1092 AK4); first reports that the run never ran before.
func runEventType(prev, status string, first bool) string {
	switch status {
	case StatusRunning:
		if prev == StatusPending && first {
			return events.WorkflowRunStarted
		}
	case StatusSucceeded:
		return events.WorkflowRunSucceeded
	case StatusFailed:
		return events.WorkflowRunFailed
	case StatusCancelled:
		return events.WorkflowRunCancelled
	}
	return ""
}

func (r *PGRepository) RecordAttempt(ctx context.Context, orgID, id string, a RunAttempt) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE workflow_run SET attempts=$3, next_attempt_at=$4, last_error=NULLIF($5,''), updated_at=now() WHERE organization_id=$1 AND id=$2",
			orgID, id, a.Attempts, a.NextAttemptAt, a.LastError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrRunNotFound
		}
		return nil
	})
}

// ClaimRun implements Repository (WP-1089 AK2). The serial key of a run
// never changes, so it is read before the advisory lock is taken; the run
// row is locked after it, in the order every claim and pick takes them.
func (r *PGRepository) ClaimRun(ctx context.Context, orgID, id string, enqueue int) (RunClaim, error) {
	var c RunClaim
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = claimRunTx(ctx, tx, orgID, id, enqueue)
		return err
	})
	return c, err
}

func claimRunTx(ctx context.Context, tx pgx.Tx, orgID, id string, enqueue int) (RunClaim, error) {
	var key string
	err := tx.QueryRow(ctx, "SELECT COALESCE(serial_key, '') FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, id).Scan(&key)
	if err == pgx.ErrNoRows {
		return RunClaim{}, ErrRunNotFound
	}
	if err != nil {
		return RunClaim{}, err
	}
	if key != "" {
		if _, lerr := tx.Exec(ctx, serialLock, orgID, key); lerr != nil {
			return RunClaim{}, lerr
		}
	}
	var status string
	var enqueues int
	if serr := tx.QueryRow(ctx, "SELECT status, enqueues FROM workflow_run WHERE organization_id=$1 AND id=$2 FOR UPDATE", orgID, id).Scan(&status, &enqueues); serr != nil {
		return RunClaim{}, serr
	}
	switch {
	case enqueues != enqueue:
		return RunClaim{}, nil
	case status == StatusRunning:
		return RunClaim{Claimed: true}, nil
	case status != StatusPending:
		return RunClaim{}, nil
	}
	if key != "" {
		turn, terr := serialTurn(ctx, tx, orgID, key, id)
		if terr != nil || !turn {
			return RunClaim{}, terr
		}
	}
	var waited float64
	if uerr := tx.QueryRow(ctx, `UPDATE workflow_run SET status='running', updated_at=now() WHERE organization_id=$1 AND id=$2
		RETURNING GREATEST(EXTRACT(EPOCH FROM now() - created_at), 0)::float8`, orgID, id).Scan(&waited); uerr != nil {
		return RunClaim{}, uerr
	}
	// The claim starts the run: its first start writes workflow.run_started
	// (WP-1092 AK4).
	run, rerr := scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, id))
	if rerr != nil {
		return RunClaim{}, rerr
	}
	if run.Steps, rerr = listSteps(ctx, tx, orgID, id); rerr != nil {
		return RunClaim{}, rerr
	}
	if terr := recordRunTransition(ctx, tx, StatusPending, run); terr != nil {
		return RunClaim{}, terr
	}
	return RunClaim{Claimed: true, Started: true, Waited: time.Duration(waited * float64(time.Second))}, nil
}

// serialTurn reports whether the pending run id is the oldest pending run
// of key while no other run of key is running.
func serialTurn(ctx context.Context, tx pgx.Tx, orgID, key, id string) (bool, error) {
	var first string
	err := tx.QueryRow(ctx, `SELECT id::text FROM workflow_run WHERE organization_id=$1 AND serial_key=$2 AND status='pending'
		AND NOT EXISTS (SELECT 1 FROM workflow_run o WHERE o.organization_id=$1 AND o.serial_key=$2 AND o.status='running' AND o.id<>$3::uuid)
		`+serialOrder+` LIMIT 1`, orgID, key, id).Scan(&first)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil && first == id, err
}

// NextSerialRun implements Repository (WP-1089 AK3).
func (r *PGRepository) NextSerialRun(ctx context.Context, orgID, key string, then func(ctx context.Context, run *Run)) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, serialLock, orgID, key); err != nil {
			return err
		}
		run, err := scanRun(tx.QueryRow(ctx, `UPDATE workflow_run SET enqueues = enqueues + 1 WHERE organization_id=$1 AND id = (
			SELECT id FROM workflow_run WHERE organization_id=$1 AND serial_key=$2 AND status='pending'
				AND NOT EXISTS (SELECT 1 FROM workflow_run o WHERE o.organization_id=$1 AND o.serial_key=$2 AND o.status='running')
				`+serialOrder+` LIMIT 1) RETURNING `+runCols, orgID, key))
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		database.AfterCommit(ctx, func(ctx context.Context) { then(ctx, run) })
		return nil
	})
}

// RequeueRun implements Repository.
func (r *PGRepository) RequeueRun(ctx context.Context, orgID, id string) (*Run, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, "UPDATE workflow_run SET enqueues = enqueues + 1 WHERE organization_id=$1 AND id=$2 AND status='pending' RETURNING "+runCols, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		return err
	})
	return run, err
}

// StrandedSerialKeys implements Repository; the age is measured on the
// clock of the database, on the head of the key (its first pending run in
// serialOrder, the run NextSerialRun picks): a head queued just now keeps
// its job even when a later run of the key waits longer.
func (r *PGRepository) StrandedSerialKeys(ctx context.Context, orgID string, age time.Duration) ([]string, error) {
	out := []string{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT h.serial_key FROM (
				SELECT DISTINCT ON (p.serial_key) p.serial_key, p.updated_at FROM workflow_run p
				WHERE p.organization_id=$1 AND p.serial_key IS NOT NULL AND p.status='pending'
				ORDER BY p.serial_key, p.entity_seq ASC NULLS LAST, p.created_at ASC, p.id ASC) h
			WHERE h.updated_at < now() - make_interval(secs => $2)
			AND NOT EXISTS (SELECT 1 FROM workflow_run o WHERE o.organization_id=$1 AND o.serial_key=h.serial_key AND o.status='running')
			ORDER BY h.serial_key`, orgID, age.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

// RunJobOrigin implements Repository: it reads the job the run was first
// queued with organization-wide, whatever the scope of the caller, since a
// job of one run queues the job of another run of its CI.
func (r *PGRepository) RunJobOrigin(ctx context.Context, orgID, runID string) (*jobs.Job, error) {
	var job *jobs.Job
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(database.WithoutTx(ctx), r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		j := &jobs.Job{OrganizationID: orgID, Kind: RunJobKind}
		var recorded bool
		var s jobs.Scope
		err := tx.QueryRow(ctx, `SELECT id::text, COALESCE(created_by::text, ''), scope_recorded, scope_clients::text[], scope_sites::text[], scope_teams::text[]
			FROM job WHERE organization_id=$1 AND kind=$2 AND idempotency_key IN ($3, $4) ORDER BY created_at LIMIT 1`,
			orgID, RunJobKind, RunJobKey(runID, 0), legacyRunJobKey(runID)).Scan(&j.ID, &j.CreatedBy, &recorded, &s.Clients, &s.Sites, &s.Teams)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if recorded {
			j.Scope = &s
		}
		job = j
		return nil
	})
	return job, err
}

func (r *PGRepository) AppendStep(ctx context.Context, s *Step) error {
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if s.Input == nil {
			s.Input = JSONMap{}
		}
		if s.Output == nil {
			s.Output = JSONMap{}
		}
		return tx.QueryRow(ctx, `INSERT INTO workflow_step (organization_id,run_id,step_index,action_type,status,input,output,error) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text, created_at, updated_at`, s.OrganizationID, s.RunID, s.StepIndex, s.ActionType, s.Status, s.Input, s.Output, s.Error).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	})
}
func (r *PGRepository) UpdateStep(ctx context.Context, orgID, id, status string, output JSONMap, errText string) (*Step, error) {
	var s *Step
	// The output column is NOT NULL; a nil map (e.g. from a failed action that
	// produced no output) would violate the constraint and leave the step
	// stuck in "running". Persist an empty object instead.
	if output == nil {
		output = JSONMap{}
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		s, err = scanStep(tx.QueryRow(ctx, "UPDATE workflow_step SET status=$3, output=$4, error=$5, updated_at=now() WHERE organization_id=$1 AND id=$2 RETURNING "+stepCols, orgID, id, status, output, errText))
		return err
	})
	return s, err
}

// AppendEvent implements Repository (WP-1096 AK3): the step becomes
// succeeded with output and, last before the commit (database.BeforeCommit,
// convention 20), the event is written to the outbox; its id is added to
// the step output. A failed write rolls back both.
func (r *PGRepository) AppendEvent(ctx context.Context, orgID, id string, output JSONMap, event *events.Event) (string, error) {
	if output == nil {
		output = JSONMap{}
	}
	var eventID string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// Only a step that has not succeeded yet: of two attempts that
		// execute the step at once, the second waits for the row lock of
		// the first, finds the step succeeded and writes no second event.
		tag, err := tx.Exec(ctx, "UPDATE workflow_step SET status=$3, output=$4, error='', updated_at=now() WHERE organization_id=$1 AND id=$2 AND status<>$3",
			orgID, id, StatusSucceeded, output)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			var exists bool
			if qerr := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM workflow_step WHERE organization_id=$1 AND id=$2)", orgID, id).Scan(&exists); qerr != nil {
				return qerr
			}
			if exists {
				return errStepSucceeded
			}
			return fmt.Errorf("workflow step not found")
		}
		return database.BeforeCommit(ctx, func() error {
			env, aerr := events.NewOutbox().Append(ctx, tx, event)
			if aerr != nil {
				return fmt.Errorf("event of the workflow step: %w", aerr)
			}
			eventID = env.ID
			// The event id belongs to the output of the step; the row was
			// written above in this transaction.
			if _, uerr := tx.Exec(ctx, `UPDATE workflow_step SET output = output || jsonb_build_object('event_id', $3::text)
				WHERE organization_id=$1 AND id=$2`, orgID, id, env.ID); uerr != nil {
				return uerr
			}
			if r.afterAppend != nil {
				return r.afterAppend()
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	return eventID, nil
}
func (r *PGRepository) ListSteps(ctx context.Context, orgID, runID string) ([]Step, error) {
	var out []Step
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = r.listStepsTx(ctx, tx, orgID, runID)
		return err
	})
	return out, err
}
func (r *PGRepository) listStepsTx(ctx context.Context, tx pgx.Tx, orgID, runID string) ([]Step, error) {
	return listSteps(ctx, tx, orgID, runID)
}

func listSteps(ctx context.Context, tx pgx.Tx, orgID, runID string) ([]Step, error) {
	rows, err := tx.Query(ctx, "SELECT "+stepCols+" FROM workflow_step WHERE organization_id=$1 AND run_id=$2 ORDER BY step_index ASC", orgID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func asMap(b []byte) JSONMap {
	var m JSONMap
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = JSONMap{}
	}
	return m
}
func asSlice(b []byte) []JSONMap {
	var s []JSONMap
	_ = json.Unmarshal(b, &s)
	if s == nil {
		s = []JSONMap{}
	}
	return s
}
func scanDef(s scanner) (*Definition, error) {
	d := &Definition{}
	var trig, conds, acts []byte
	if err := s.Scan(&d.ID, &d.OrganizationID, &d.Name, &d.Description, &trig, &conds, &acts, &d.Active, &d.Version, &d.CurrentVersionID, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Trigger = asMap(trig)
	d.Conditions = asSlice(conds)
	d.Actions = asSlice(acts)
	return d, nil
}
func scanVersion(s scanner) (*DefinitionVersion, error) {
	v := &DefinitionVersion{}
	var trig, conds, acts []byte
	if err := s.Scan(&v.ID, &v.OrganizationID, &v.WorkflowID, &v.Version, &trig, &conds, &acts, &v.CreatedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.Trigger = asMap(trig)
	v.Conditions = asSlice(conds)
	v.Actions = asSlice(acts)
	return v, nil
}
func scanRun(s scanner) (*Run, error) {
	r := &Run{}
	var ctxb []byte
	var fin, next sql.NullTime
	var seq sql.NullInt64
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.WorkflowID, &r.DefinitionVersionID, &r.Status, &r.Trigger, &ctxb, &r.StartedAt, &fin,
		&r.Attempts, &next, &r.LastError, &r.InitiatedBy, &r.CIID, &r.SerialKey, &seq, &r.Enqueues, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if seq.Valid {
		v := seq.Int64
		r.EntitySeq = &v
	}
	if next.Valid {
		v := next.Time.UTC()
		r.NextAttemptAt = &v
	}
	r.Context = asMap(ctxb)
	if fin.Valid {
		v := fin.Time.UTC()
		r.FinishedAt = &v
	}
	return r, nil
}
func scanStep(s scanner) (*Step, error) {
	st := &Step{}
	var in, out []byte
	if err := s.Scan(&st.ID, &st.OrganizationID, &st.RunID, &st.StepIndex, &st.ActionType, &st.Status, &in, &out, &st.Error, &st.CreatedAt, &st.UpdatedAt); err != nil {
		return nil, err
	}
	st.Input = asMap(in)
	st.Output = asMap(out)
	return st, nil
}
