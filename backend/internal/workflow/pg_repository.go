package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defCols = `id::text, organization_id::text, name, description, trigger, conditions, actions, active, created_at, updated_at`
const runCols = `id::text, organization_id::text, workflow_id::text, status, trigger, context, started_at, finished_at, created_at, updated_at`
const stepCols = `id::text, organization_id::text, run_id::text, step_index, action_type, status, input, output, error, created_at, updated_at`

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id',$1,true)", orgID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *PGRepository) ListDefinitions(ctx context.Context, orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
	var out []Definition
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = scanDef(tx.QueryRow(ctx, "SELECT "+defCols+" FROM workflow_def WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("workflow definition not found")
		}
		return err
	})
	return d, err
}
func (r *PGRepository) CreateDefinition(ctx context.Context, d *Definition) error {
	return r.withTenant(ctx, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if d.Trigger == nil {
			d.Trigger = JSONMap{}
		}
		if d.Conditions == nil {
			d.Conditions = []JSONMap{}
		}
		if d.Actions == nil {
			d.Actions = []JSONMap{}
		}
		return tx.QueryRow(ctx, `INSERT INTO workflow_def (organization_id,name,description,trigger,conditions,actions,active) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text, created_at, updated_at`, d.OrganizationID, d.Name, d.Description, d.Trigger, d.Conditions, d.Actions, d.Active).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
	})
}
func (r *PGRepository) UpdateDefinition(ctx context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	var d *Definition
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		set := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.Name != nil {
			set = append(set, fmt.Sprintf("name=$%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Description != nil {
			set = append(set, fmt.Sprintf("description=$%d", pos))
			args = append(args, *req.Description)
			pos++
		}
		if req.Trigger != nil {
			set = append(set, fmt.Sprintf("trigger=$%d", pos))
			args = append(args, req.Trigger)
			pos++
		}
		if req.Conditions != nil {
			set = append(set, fmt.Sprintf("conditions=$%d", pos))
			args = append(args, req.Conditions)
			pos++
		}
		if req.Actions != nil {
			set = append(set, fmt.Sprintf("actions=$%d", pos))
			args = append(args, req.Actions)
			pos++
		}
		if req.Active != nil {
			set = append(set, fmt.Sprintf("active=$%d", pos))
			args = append(args, *req.Active)
			pos++
		}
		if len(set) == 0 {
			var err error
			d, err = scanDef(tx.QueryRow(ctx, "SELECT "+defCols+" FROM workflow_def WHERE id=$1 AND organization_id=$2", id, orgID))
			return err
		}
		set = append(set, "updated_at=now()")
		var err error
		d, err = scanDef(tx.QueryRow(ctx, fmt.Sprintf("UPDATE workflow_def SET %s WHERE id=$1 AND organization_id=$2 RETURNING %s", strings.Join(set, ","), defCols), args...))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("workflow definition not found")
		}
		return err
	})
	return d, err
}
func (r *PGRepository) DeleteDefinition(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM workflow_def WHERE organization_id=$1 AND id=$2", orgID, id)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("workflow definition not found")
		}
		return nil
	})
}
func (r *PGRepository) ListRuns(ctx context.Context, orgID, wid, status string, page api.PaginationParams) ([]Run, int, error) {
	var out []Run
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("workflow run not found")
		}
		if err != nil {
			return err
		}
		run.Steps, err = r.listStepsTx(ctx, tx, orgID, id)
		return err
	})
	return run, err
}
func (r *PGRepository) CreateRun(ctx context.Context, run *Run) error {
	return r.withTenant(ctx, run.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if run.Context == nil {
			run.Context = JSONMap{}
		}
		if run.StartedAt.IsZero() {
			run.StartedAt = time.Now().UTC()
		}
		return tx.QueryRow(ctx, `INSERT INTO workflow_run (organization_id,workflow_id,status,trigger,context,started_at) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text, created_at, updated_at`, run.OrganizationID, run.WorkflowID, run.Status, run.Trigger, run.Context, run.StartedAt).Scan(&run.ID, &run.CreatedAt, &run.UpdatedAt)
	})
}
func (r *PGRepository) UpdateRunStatus(ctx context.Context, orgID, id, status string, finished *time.Time, ctxm JSONMap) (*Run, error) {
	var run *Run
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
func (r *PGRepository) AppendStep(ctx context.Context, s *Step) error {
	return r.withTenant(ctx, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		s, err = scanStep(tx.QueryRow(ctx, "UPDATE workflow_step SET status=$3, output=$4, error=$5, updated_at=now() WHERE organization_id=$1 AND id=$2 RETURNING "+stepCols, orgID, id, status, output, errText))
		return err
	})
	return s, err
}
func (r *PGRepository) ListSteps(ctx context.Context, orgID, runID string) ([]Step, error) {
	var out []Step
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	if err := s.Scan(&d.ID, &d.OrganizationID, &d.Name, &d.Description, &trig, &conds, &acts, &d.Active, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Trigger = asMap(trig)
	d.Conditions = asSlice(conds)
	d.Actions = asSlice(acts)
	return d, nil
}
func scanRun(s scanner) (*Run, error) {
	r := &Run{}
	var ctxb []byte
	var fin sql.NullTime
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.WorkflowID, &r.Status, &r.Trigger, &ctxb, &r.StartedAt, &fin, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
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
