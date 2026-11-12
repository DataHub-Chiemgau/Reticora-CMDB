package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defCols = `id::text, organization_id::text, name, description, trigger, conditions, actions, active, version, current_version_id::text, created_at, updated_at`
const versionCols = `id::text, organization_id::text, workflow_id::text, version, trigger, conditions, actions, COALESCE(created_by::text, ''), created_at`
const runCols = `id::text, organization_id::text, workflow_id::text, definition_version_id::text, status, trigger, context, started_at, finished_at, attempts, next_attempt_at, COALESCE(last_error, ''), COALESCE(initiated_by::text, ''), created_at, updated_at`
const stepCols = `id::text, organization_id::text, run_id::text, step_index, action_type, status, input, output, error, created_at, updated_at`

type PGRepository struct{ pool *pgxpool.Pool }

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
func (r *PGRepository) ListRuns(ctx context.Context, orgID, wid, status string, page api.PaginationParams) ([]Run, int, error) {
	var out []Run
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id=$1"}
		args := []any{orgID}
		pos := 2
		if wid != "" {
			where = append(where, fmt.Sprintf("workflow_id=$%d", pos))
			args = append(args, wid)
			pos++
		}
		if status != "" {
			where = append(where, fmt.Sprintf("status=$%d", pos))
			args = append(args, status)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM workflow_run WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM workflow_run WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", runCols, clause, pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		// Collect run ids and close the rows BEFORE issuing the per-run step
		// queries: a nested query on the same connection while rows are open
		// fails with "conn busy".
		runIDs := make([]string, 0, page.Limit)
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
		return tx.QueryRow(ctx, `INSERT INTO workflow_run (organization_id,workflow_id,definition_version_id,status,trigger,context,started_at,initiated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,(SELECT u.id FROM app_user u WHERE u.organization_id=$1 AND u.id::text=NULLIF($8,''))) RETURNING id::text, COALESCE(initiated_by::text, ''), created_at, updated_at`, run.OrganizationID, run.WorkflowID, run.DefinitionVersionID, run.Status, run.Trigger, run.Context, run.StartedAt, run.InitiatedBy).Scan(&run.ID, &run.InitiatedBy, &run.CreatedAt, &run.UpdatedAt)
	})
}
func (r *PGRepository) UpdateRunStatus(ctx context.Context, orgID, id, status string, finished *time.Time, ctxm JSONMap) (*Run, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, "UPDATE workflow_run SET status=$3, finished_at=$4, context=COALESCE($5, context), updated_at=now() WHERE organization_id=$1 AND id=$2 RETURNING "+runCols, orgID, id, status, finished, ctxm))
		if err != nil {
			return err
		}
		run.Steps, err = r.listStepsTx(ctx, tx, orgID, id)
		return err
	})
	return run, err
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
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.WorkflowID, &r.DefinitionVersionID, &r.Status, &r.Trigger, &ctxb, &r.StartedAt, &fin,
		&r.Attempts, &next, &r.LastError, &r.InitiatedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
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
