package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ruleCols = `id::text, organization_id::text, COALESCE(ci_type_id::text,''), name, description, severity, category, expression, applies_when, remediation_hint, active, created_at, updated_at`
const resultCols = `id::text, organization_id::text, rule_id::text, ci_id::text, COALESCE(ci_type_id::text,''), status, details, evaluated_at, status_changed_at, created_at, updated_at`

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// resultVisible restricts compliance results to CIs visible under the
// transaction's tenant scope: the ci policy filters the subquery, while
// compliance_result carries no client column yet (WP-025).
const resultVisible = "EXISTS (SELECT 1 FROM ci WHERE ci.id = compliance_result.ci_id)"

// requireCIType fails with a 422 field error on ci_type_id when the type of
// a rule is no UUID or neither a system type nor a type of the
// organization. The foreign key on ci_type is checked without row level
// security: a private type of another organization was accepted (201) and
// an unknown one refused with the constraint text, so the two could be told
// apart (TEN-04, API-03, F-047; like WP-396 for CIs).
func requireCIType(ctx context.Context, tx pgx.Tx, orgID, ciTypeID string) error {
	if ciTypeID == "" {
		return nil
	}
	if err := api.RequireUUID("ci_type_id", ciTypeID); err != nil {
		return err
	}
	var visible bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci_type WHERE id = $1 AND (organization_id IS NULL OR organization_id = $2))",
		ciTypeID, orgID).Scan(&visible); err != nil {
		return fmt.Errorf("check ci type of rule: %w", err)
	}
	if !visible {
		return &api.FieldError{Field: "ci_type_id", Message: "CI type not found", Status: http.StatusUnprocessableEntity}
	}
	return nil
}

func (r *PGRepository) ListRules(ctx context.Context, orgID, ciTypeID, category string, activeOnly bool, page api.PaginationParams) ([]Rule, int, error) {
	var out []Rule
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
func (r *PGRepository) GetRule(ctx context.Context, orgID, id string) (*Rule, error) {
	var rule *Rule
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rule, err = scanRule(tx.QueryRow(ctx, "SELECT "+ruleCols+" FROM compliance_rule WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return rule, err
}
func (r *PGRepository) CreateRule(ctx context.Context, rule *Rule) error {
	return database.WithRequestTenant(ctx, r.pool, rule.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if rule.Expression == nil {
			rule.Expression = JSONMap{}
		}
		if err := requireCIType(ctx, tx, rule.OrganizationID, rule.CITypeID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO compliance_rule (organization_id,ci_type_id,name,description,severity,category,expression,applies_when,remediation_hint,active) VALUES ($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text, created_at, updated_at`, rule.OrganizationID, rule.CITypeID, rule.Name, rule.Description, rule.Severity, rule.Category, rule.Expression, nullableMap(rule.AppliesWhen), rule.RemediationHint, rule.Active).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)
	})
}
func (r *PGRepository) UpdateRule(ctx context.Context, orgID, id string, req *UpdateRuleRequest) (*Rule, error) {
	var rule *Rule
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		set := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.CITypeID != nil {
			if err := requireCIType(ctx, tx, orgID, *req.CITypeID); err != nil {
				return err
			}
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
		if req.setAppliesWhen {
			set = append(set, fmt.Sprintf("applies_when=$%d", pos))
			args = append(args, nullableMap(req.appliesWhen))
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
		}
		if len(set) == 0 {
			var err error
			rule, err = scanRule(tx.QueryRow(ctx, "SELECT "+ruleCols+" FROM compliance_rule WHERE id=$1 AND organization_id=$2", id, orgID))
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		set = append(set, "updated_at=now()")
		var err error
		rule, err = scanRule(tx.QueryRow(ctx, fmt.Sprintf("UPDATE compliance_rule SET %s WHERE id=$1 AND organization_id=$2 RETURNING %s", strings.Join(set, ","), ruleCols), args...))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return rule, err
}
func (r *PGRepository) DeleteRule(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM compliance_rule WHERE organization_id=$1 AND id=$2", orgID, id)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// UpsertResults implements Repository: one INSERT … ON CONFLICT (organization_id,
// rule_id, ci_id) DO UPDATE for all results (WP-1126 AK3).
func (r *PGRepository) UpsertResults(ctx context.Context, orgID string, results []Result) error {
	if len(results) == 0 {
		return nil
	}
	rules, cis, types, statuses, details := make([]string, len(results)), make([]string, len(results)), make([]string, len(results)),
		make([]string, len(results)), make([]string, len(results))
	for i := range results {
		rules[i], cis[i], types[i], statuses[i], details[i] = results[i].RuleID, results[i].CIID, results[i].CITypeID, results[i].Status, results[i].Details
	}
	// evaluated_at is the database clock (Now), whatever instance writes:
	// the removal after a run (DeleteStaleResults) compares with it.
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO compliance_result (organization_id, rule_id, ci_id, ci_type_id, status, details, evaluated_at, status_changed_at)
			SELECT $1, u.rule_id, u.ci_id, NULLIF(u.ci_type_id, '')::uuid, u.status, u.details, now(), now()
			FROM unnest($2::uuid[], $3::uuid[], $4::text[], $5::text[], $6::text[]) AS u(rule_id, ci_id, ci_type_id, status, details)
			ON CONFLICT (organization_id, rule_id, ci_id) DO UPDATE SET ci_type_id = EXCLUDED.ci_type_id, status = EXCLUDED.status,
				details = EXCLUDED.details, evaluated_at = EXCLUDED.evaluated_at,
				status_changed_at = CASE WHEN compliance_result.status IS DISTINCT FROM EXCLUDED.status THEN EXCLUDED.evaluated_at
					ELSE compliance_result.status_changed_at END`,
			orgID, rules, cis, types, statuses, details)
		return err
	})
}

// Now implements Repository: the clock of the database.
func (r *PGRepository) Now(ctx context.Context, orgID string) (time.Time, error) {
	var now time.Time
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT now()").Scan(&now)
	})
	return now.UTC(), err
}

// DeleteStaleResults implements Repository over
// idx_compliance_result_evaluated.
func (r *PGRepository) DeleteStaleResults(ctx context.Context, orgID string, before time.Time) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM compliance_result WHERE organization_id=$1 AND evaluated_at < $2", orgID, before)
		return err
	})
}

func (r *PGRepository) ListResults(ctx context.Context, orgID, ciTypeID, status string, page api.PaginationParams) ([]Result, int, error) {
	var out []Result
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM compliance_result WHERE "+resultVisible+" AND "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM compliance_result WHERE "+resultVisible+" AND %s ORDER BY evaluated_at DESC LIMIT $%d OFFSET $%d", resultCols, clause, pos, pos+1), append(args, page.Limit, page.Offset)...)
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
	var expr, applies []byte
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.CITypeID, &r.Name, &r.Description, &r.Severity, &r.Category, &expr, &applies, &r.RemediationHint, &r.Active, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Expression = asMap(expr)
	if len(applies) > 0 && string(applies) != "null" {
		r.AppliesWhen = asMap(applies)
	}
	return r, nil
}

// nullableMap is the JSONB value of an optional condition: NULL for nil.
func nullableMap(m JSONMap) any {
	if m == nil {
		return nil
	}
	return m
}

// FindingFacts reads the open findings of the CIs grouped by CI and kind
// in one query (CMP-02, AK4 of WP-1125).
func (r *PGRepository) FindingFacts(ctx context.Context, orgID string, ciIDs []string) (map[string]FindingFacts, error) {
	out := map[string]FindingFacts{}
	if len(ciIDs) == 0 {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT ci_id::text, kind, count(*)::int,
		        max(CASE severity WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'critical' THEN 4 ELSE 0 END)::int
		   FROM security_finding
		  WHERE organization_id = $1 AND ci_id = ANY($2::uuid[]) AND status IN ('open', 'acknowledged') AND kind <> $3
		  GROUP BY ci_id, kind`, orgID, ciIDs, kindComplianceViolation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, kind string
			var n, rank int
			if err := rows.Scan(&id, &kind, &n, &rank); err != nil {
				return err
			}
			facts := out[id]
			if facts.OpenCount == nil {
				facts.OpenCount = map[string]int{}
			}
			facts.OpenCount[kind] = n
			facts.MaxSeverityRank = max(facts.MaxSeverityRank, rank)
			out[id] = facts
		}
		return rows.Err()
	})
	return out, err
}
func scanResult(s scanner) (*Result, error) {
	r := &Result{}
	if err := s.Scan(&r.ID, &r.OrganizationID, &r.RuleID, &r.CIID, &r.CITypeID, &r.Status, &r.Details, &r.EvaluatedAt, &r.StatusChangedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return r, nil
}
