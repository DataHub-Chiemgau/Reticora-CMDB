package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ruleCols = `id::text, organization_id::text, COALESCE(ci_type_id::text,''), name, description, severity, category, expression, remediation_hint, active, created_at, updated_at`
const resultCols = `id::text, organization_id::text, rule_id::text, ci_id::text, COALESCE(ci_type_id::text,''), status, details, evaluated_at, created_at, updated_at`

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
func (r *PGRepository) ListRules(orgID, ciTypeID, category string, activeOnly bool, page api.PaginationParams) ([]Rule, int, error) {
	var out []Rule
	var total int
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id=$1"}
		args := []any{orgID}
		pos := 2
		if ciTypeID != "" {
			where = append(where, fmt.Sprintf("ci_type_id=$%d", pos))
			args = append(args, ciTypeID)
			pos++
		}
		if category != "" {
			where = append(where, fmt.Sprintf("category=$%d", pos))
			args = append(args, category)
			pos++
		}
		if activeOnly {
			where = append(where, "active=true")
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM compliance_rule WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM compliance_rule WHERE %s ORDER BY name ASC LIMIT $%d OFFSET $%d", ruleCols, clause, pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rule, err := scanRule(rows)
			if err != nil {
				return err
			}
			out = append(out, *rule)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) GetRule(orgID, id string) (*Rule, error) {
	var rule *Rule
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rule, err = scanRule(tx.QueryRow(ctx, "SELECT "+ruleCols+" FROM compliance_rule WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("compliance rule not found")
		}
		return err
	})
	return rule, err
}
func (r *PGRepository) CreateRule(rule *Rule) error {
	return r.withTenant(context.Background(), rule.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if rule.Expression == nil {
			rule.Expression = JSONMap{}
		}
		return tx.QueryRow(ctx, `INSERT INTO compliance_rule (organization_id,ci_type_id,name,description,severity,category,expression,remediation_hint,active) VALUES ($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text, created_at, updated_at`, rule.OrganizationID, rule.CITypeID, rule.Name, rule.Description, rule.Severity, rule.Category, rule.Expression, rule.RemediationHint, rule.Active).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)
	})
}
func (r *PGRepository) UpdateRule(orgID, id string, req UpdateRuleRequest) (*Rule, error) {
	var rule *Rule
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		set := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.CITypeID != nil {
			set = append(set, fmt.Sprintf("ci_type_id=NULLIF($%d,'')::uuid", pos))
			args = append(args, *req.CITypeID)
			pos++
		}
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
		if req.Severity != nil {
			set = append(set, fmt.Sprintf("severity=$%d", pos))
			args = append(args, *req.Severity)
			pos++
		}
		if req.Category != nil {
			set = append(set, fmt.Sprintf("category=$%d", pos))
			args = append(args, *req.Category)
			pos++
		}
		if req.Expression != nil {
			set = append(set, fmt.Sprintf("expression=$%d", pos))
			args = append(args, req.Expression)
			pos++
		}
		if req.RemediationHint != nil {
			set = append(set, fmt.Sprintf("remediation_hint=$%d", pos))
			args = append(args, *req.RemediationHint)
			pos++
		}
		if req.Active != nil {
			set = append(set, fmt.Sprintf("active=$%d", pos))
			args = append(args, *req.Active)
			pos++
		}
		if len(set) == 0 {
			var err error
			rule, err = scanRule(tx.QueryRow(ctx, "SELECT "+ruleCols+" FROM compliance_rule WHERE id=$1 AND organization_id=$2", id, orgID))
			return err
		}
		set = append(set, "updated_at=now()")
		var err error
		rule, err = scanRule(tx.QueryRow(ctx, fmt.Sprintf("UPDATE compliance_rule SET %s WHERE id=$1 AND organization_id=$2 RETURNING %s", strings.Join(set, ","), ruleCols), args...))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("compliance rule not found")
		}
		return err
	})
	return rule, err
}
func (r *PGRepository) DeleteRule(orgID, id string) error {
	return r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM compliance_rule WHERE organization_id=$1 AND id=$2", orgID, id)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("compliance rule not found")
		}
		return nil
	})
}
func (r *PGRepository) ReplaceResults(orgID string, results []Result) error {
	return r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM compliance_result WHERE organization_id=$1", orgID); err != nil {
			return err
		}
		for i := range results {
			res := &results[i]
			if err := tx.QueryRow(ctx, `INSERT INTO compliance_result (organization_id,rule_id,ci_id,ci_type_id,status,details,evaluated_at) VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7) RETURNING id::text, created_at, updated_at`, res.OrganizationID, res.RuleID, res.CIID, res.CITypeID, res.Status, res.Details, res.EvaluatedAt).Scan(&res.ID, &res.CreatedAt, &res.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *PGRepository) ListResults(orgID, ciTypeID, status string, page api.PaginationParams) ([]Result, int, error) {
	var out []Result
	var total int
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id=$1"}
		args := []any{orgID}
		pos := 2
		if ciTypeID != "" {
			where = append(where, fmt.Sprintf("ci_type_id=$%d", pos))
			args = append(args, ciTypeID)
			pos++
		}
		if status != "" {
			where = append(where, fmt.Sprintf("status=$%d", pos))
			args = append(args, status)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM compliance_result WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM compliance_result WHERE %s ORDER BY evaluated_at DESC LIMIT $%d OFFSET $%d", resultCols, clause, pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			res, err := scanResult(rows)
			if err != nil {
				return err
			}
			out = append(out, *res)
		}
		return rows.Err()
	})
	return out, total, err
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
func scanRule(s scanner) (*Rule, error) {
	r := &Rule{}
	var expr []byte
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.CITypeID, &r.Name, &r.Description, &r.Severity, &r.Category, &expr, &r.RemediationHint, &r.Active, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Expression = asMap(expr)
	return r, nil
}
func scanResult(s scanner) (*Result, error) {
	r := &Result{}
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.RuleID, &r.CIID, &r.CITypeID, &r.Status, &r.Details, &r.EvaluatedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return r, nil
}
