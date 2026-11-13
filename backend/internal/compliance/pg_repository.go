package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// ListControls implements ControlStore.
func (r *PGRepository) ListControls(ctx context.Context, orgID, standard string, withoutActiveRule bool) ([]Control, error) {
	out := []Control{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id::text, c.standard, c.control_id, c.title_de, c.title_en, c.sort_order,
				count(rc.rule_id)::int, count(rc.rule_id) FILTER (WHERE cr.active)::int
			FROM compliance_control c
			LEFT JOIN compliance_rule_control rc ON rc.control_id = c.id AND rc.organization_id = $1
			LEFT JOIN compliance_rule cr ON cr.id = rc.rule_id AND cr.organization_id = $1
			WHERE c.standard = $2
			GROUP BY c.id
			HAVING NOT $3 OR count(rc.rule_id) FILTER (WHERE cr.active) = 0
			ORDER BY c.sort_order, c.control_id`, orgID, standard, withoutActiveRule)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Control
			if err := rows.Scan(&c.ID, &c.Standard, &c.ControlID, &c.TitleDE, &c.TitleEN, &c.SortOrder, &c.RuleCount, &c.ActiveRuleCount); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// RuleControlIDs implements ControlStore.
func (r *PGRepository) RuleControlIDs(ctx context.Context, orgID, ruleID string) ([]string, error) {
	out := []string{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT rc.control_id::text FROM compliance_rule_control rc JOIN compliance_control c ON c.id = rc.control_id
			WHERE rc.organization_id = $1 AND rc.rule_id = $2 ORDER BY c.standard, c.sort_order`, orgID, ruleID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err == nil {
			out = append(out, ids...)
		}
		return err
	})
	return out, err
}

// ReplaceRuleControls implements ControlStore in one transaction.
func (r *PGRepository) ReplaceRuleControls(ctx context.Context, orgID, ruleID string, controlIDs []string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM compliance_rule WHERE organization_id = $1 AND id = $2 FOR UPDATE)`,
			orgID, ruleID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		unique := []string{}
		for i, c := range controlIDs {
			var known bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM compliance_control WHERE id = $1::uuid)`, c).Scan(&known); err != nil {
				return err
			}
			if !known {
				return controlFieldError(i)
			}
			if !slices.Contains(unique, c) {
				unique = append(unique, c)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM compliance_rule_control WHERE organization_id = $1 AND rule_id = $2`, orgID, ruleID); err != nil {
			return err
		}
		if len(unique) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO compliance_rule_control (organization_id, rule_id, control_id)
			SELECT $1, $2, c FROM unnest($3::uuid[]) AS c`, orgID, ruleID, unique)
		return err
	})
}

// ─── Score (WP-1127) ─────────────────────────────────────────────────────────

// scoreWeights is the weights of the organization as a CTE w: the row of
// compliance_settings, the defaults without one.
const scoreWeights = `w AS (SELECT COALESCE((SELECT weights FROM compliance_settings WHERE organization_id = $1), $2::jsonb) AS weights)`

// scoreSums are the aggregates of the joined results r and rules ru.
const scoreSums = `count(*) FILTER (WHERE r.status = 'pass') AS passed,
	count(*) FILTER (WHERE r.status = 'fail') AS failed,
	count(*) FILTER (WHERE r.status NOT IN ('pass', 'fail')) AS na,
	COALESCE(sum((w.weights ->> ru.severity)::bigint) FILTER (WHERE r.status = 'pass'), 0) AS pw,
	COALESCE(sum((w.weights ->> ru.severity)::bigint) FILTER (WHERE r.status = 'fail'), 0) AS fw`

// scoreGroupKeys are the key of a group in the results and the label of a
// key k of the aggregated rows.
var scoreGroupKeys = map[string]struct{ key, label string }{
	GroupCI: {`r.ci_id::text`, `COALESCE((SELECT ci.name FROM ci WHERE ci.id = g.key::uuid), '')`},
	GroupCIType: {`COALESCE(r.ci_type_id::text, '')`,
		`CASE WHEN g.key = '' THEN '' ELSE COALESCE((SELECT ci_type.name FROM ci_type WHERE ci_type.id = g.key::uuid), '') END`},
	GroupClient: {`COALESCE(r.client_id::text, '')`,
		`CASE WHEN g.key = '' THEN '' ELSE COALESCE((SELECT client.name FROM client WHERE client.id = g.key::uuid), '') END`},
	GroupSite: {`COALESCE(r.site_id::text, '')`,
		`CASE WHEN g.key = '' THEN '' ELSE COALESCE((SELECT location.name FROM location WHERE location.id = g.key::uuid), '') END`},
	GroupCategory: {`ru.category`, `g.key`},
	GroupSeverity: {`ru.severity`, `g.key`},
}

// scoreStatement is the aggregate of the score query q of the
// organization with its arguments.
func scoreStatement(orgID string, q *ScoreQuery) (sql string, args []any, err error) {
	args = []any{orgID, weightsJSON(DefaultWeights())}
	where := "r.organization_id = $1 AND " + strings.ReplaceAll(resultVisible, "compliance_result.", "r.")
	if q.CITypeID != "" {
		args = append(args, q.CITypeID)
		where += fmt.Sprintf(" AND r.ci_type_id = $%d::uuid", len(args))
	}
	from := ` FROM compliance_result r JOIN compliance_rule ru ON ru.id = r.rule_id CROSS JOIN w WHERE ` + where
	if q.GroupBy == "" {
		return `WITH ` + scoreWeights + `, g AS (SELECT ` + scoreSums + from + `)
			SELECT '', '', passed, failed, na, pw, fw,
				CASE WHEN pw + fw > 0 THEN pw::float8 * 100 / (pw + fw) ELSE 0 END FROM g`, args, nil
	}
	k, ok := scoreGroupKeys[q.GroupBy]
	if !ok {
		return "", nil, badRequest("unknown group_by")
	}
	sql = `WITH ` + scoreWeights + `, g AS (SELECT ` + k.key + ` AS key, ` + scoreSums + from + ` GROUP BY 1),
		s AS (SELECT g.key, ` + k.label + ` AS label, g.passed, g.failed, g.na, g.pw, g.fw,
			CASE WHEN g.pw + g.fw > 0 THEN g.pw::float8 * 100 / (g.pw + g.fw) ELSE 0 END AS score FROM g)
		SELECT key, label, passed, failed, na, pw, fw, score FROM s`
	if q.After != nil {
		args = append(args, q.After.Score, q.After.Key)
		sql += fmt.Sprintf(" WHERE (s.score, s.key) > ($%d::float8, $%d::text)", len(args)-1, len(args))
	}
	sql += " ORDER BY s.score, s.key"
	if q.GroupBy == GroupCI && q.Limit > 0 {
		args = append(args, q.Limit+1)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return sql, args, nil
}

// Score implements ScoreStore: one aggregate in SQL over compliance_result
// and compliance_rule under the caller's row level security (and the CIs
// it sees, resultVisible); without group_by the whole organization in one
// row, group_by=ci by keyset pages (score ascending, then key).
func (r *PGRepository) Score(ctx context.Context, orgID string, q *ScoreQuery) (*ScorePage, error) {
	page := &ScorePage{Groups: []ScoreGroup{}}
	sql, args, err := scoreStatement(orgID, q)
	if err != nil {
		return nil, err
	}
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, sql, args...)
		if qerr != nil {
			return fmt.Errorf("aggregate compliance score: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var key, label string
			var s Sums
			var score float64
			if serr := rows.Scan(&key, &label, &s.Passed, &s.Failed, &s.NotApplicable, &s.PassedWeight, &s.FailedWeight, &score); serr != nil {
				return fmt.Errorf("scan compliance score: %w", serr)
			}
			// The score as SQL computed it: the cursor compares it there.
			g := groupOf(key, label, &s)
			g.Score = score
			page.Groups = append(page.Groups, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if q.GroupBy == GroupCI && q.Limit > 0 && len(page.Groups) > q.Limit {
		page.Groups = page.Groups[:q.Limit]
		last := page.Groups[q.Limit-1]
		page.Next = &ScoreCursor{Score: last.Score, Key: last.Key}
	}
	return page, nil
}

// GetSettings implements ScoreStore: the defaults without a row.
func (r *PGRepository) GetSettings(ctx context.Context, orgID string) (*Settings, error) {
	out := &Settings{Weights: DefaultWeights()}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT weights, updated_at FROM compliance_settings WHERE organization_id = $1`, orgID).Scan(&raw, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read compliance settings: %w", err)
		}
		weights := Weights{}
		if err := json.Unmarshal(raw, &weights); err != nil {
			return fmt.Errorf("decode compliance weights: %w", err)
		}
		out.Weights, out.UpdatedAt = weights, &at
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PutSettings implements ScoreStore: the weights as a whole.
func (r *PGRepository) PutSettings(ctx context.Context, orgID string, weights Weights) (*Settings, error) {
	out := &Settings{Weights: weights}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var at time.Time
		if err := tx.QueryRow(ctx, `INSERT INTO compliance_settings (organization_id, weights) VALUES ($1, $2::jsonb)
			ON CONFLICT (organization_id) DO UPDATE SET weights = EXCLUDED.weights
			RETURNING updated_at`, orgID, weightsJSON(weights)).Scan(&at); err != nil {
			return fmt.Errorf("write compliance settings: %w", err)
		}
		out.UpdatedAt = &at
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// snapshotInsert writes the sums of the day $3 per client, site and
// dimension (pass and fail only) and replaces an earlier run of the day.
const snapshotInsert = `WITH ` + scoreWeights + `,
	base AS (
		SELECT r.client_id, r.site_id, COALESCE(r.ci_type_id::text, '') AS ci_type, ru.category, ru.severity, r.status,
			COALESCE((w.weights ->> ru.severity)::bigint, 0) AS weight
		FROM compliance_result r JOIN compliance_rule ru ON ru.id = r.rule_id CROSS JOIN w
		WHERE r.organization_id = $1 AND r.status IN ('pass', 'fail')
		  AND EXISTS (SELECT 1 FROM ci WHERE ci.id = r.ci_id)),
	agg AS (
		SELECT client_id, site_id, 'overall' AS dimension, '' AS group_key, status, weight FROM base
		UNION ALL SELECT client_id, site_id, 'ci_type', ci_type, status, weight FROM base
		UNION ALL SELECT client_id, site_id, 'category', category, status, weight FROM base
		UNION ALL SELECT client_id, site_id, 'severity', severity, status, weight FROM base)
	INSERT INTO compliance_score_snapshot (organization_id, day, client_id, site_id, dimension, group_key,
		passed, failed, passed_weight, failed_weight)
	SELECT $1, $3::date, client_id, site_id, dimension, group_key,
		count(*) FILTER (WHERE status = 'pass'), count(*) FILTER (WHERE status = 'fail'),
		COALESCE(sum(weight) FILTER (WHERE status = 'pass'), 0), COALESCE(sum(weight) FILTER (WHERE status = 'fail'), 0)
	FROM agg GROUP BY client_id, site_id, dimension, group_key
	ON CONFLICT ON CONSTRAINT compliance_score_snapshot_key DO UPDATE SET
		passed = EXCLUDED.passed, failed = EXCLUDED.failed,
		passed_weight = EXCLUDED.passed_weight, failed_weight = EXCLUDED.failed_weight, created_at = now()`

// SnapshotScores implements ScoreStore in one transaction of the context's
// scope (the system job runs org-wide): the groups of the day that are gone
// since an earlier run are removed, the others upserted, the snapshots
// older than SnapshotRetention days removed.
func (r *PGRepository) SnapshotScores(ctx context.Context, orgID string, day time.Time) (int, error) {
	written := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d := day.UTC().Format(time.DateOnly)
		if _, err := tx.Exec(ctx, `DELETE FROM compliance_score_snapshot WHERE organization_id = $1 AND day = $2::date`, orgID, d); err != nil {
			return fmt.Errorf("replace compliance score snapshot: %w", err)
		}
		tag, err := tx.Exec(ctx, snapshotInsert, orgID, weightsJSON(DefaultWeights()), d)
		if err != nil {
			return fmt.Errorf("write compliance score snapshot: %w", err)
		}
		written = int(tag.RowsAffected())
		if _, err := tx.Exec(ctx, `DELETE FROM compliance_score_snapshot WHERE organization_id = $1 AND day < $2::date - $3::int`,
			orgID, d, SnapshotRetention); err != nil {
			return fmt.Errorf("remove old compliance score snapshots: %w", err)
		}
		return nil
	})
	return written, err
}

// historyKeys are per group of the history the dimension of the snapshot
// rows, their key and the label of a key k.
var historyKeys = map[string]struct{ dimension, key, label string }{
	GroupOverall:  {GroupOverall, `''`, `''`},
	GroupCIType:   {GroupCIType, `group_key`, scoreGroupKeys[GroupCIType].label},
	GroupCategory: {GroupCategory, `group_key`, `g.key`},
	GroupSeverity: {GroupSeverity, `group_key`, `g.key`},
	GroupClient:   {GroupOverall, `COALESCE(client_id::text, '')`, scoreGroupKeys[GroupClient].label},
	GroupSite:     {GroupOverall, `COALESCE(site_id::text, '')`, scoreGroupKeys[GroupSite].label},
}

// ScoreHistory implements ScoreStore: the snapshot rows the caller sees
// (their client and site under row level security) summed per day and
// key; the score follows from the sums.
func (r *PGRepository) ScoreHistory(ctx context.Context, orgID string, q *HistoryQuery) ([]HistoryPoint, error) {
	h, ok := historyKeys[q.GroupBy]
	if !ok {
		return nil, badRequest("unknown group_by")
	}
	out := []HistoryPoint{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH g AS (
				SELECT day, `+h.key+` AS key, sum(passed)::int AS passed, sum(failed)::int AS failed,
					sum(passed_weight)::bigint AS pw, sum(failed_weight)::bigint AS fw
				FROM compliance_score_snapshot
				WHERE organization_id = $1 AND dimension = $2 AND day BETWEEN $3::date AND $4::date
				GROUP BY day, 2)
			SELECT to_char(g.day, 'YYYY-MM-DD'), g.key, `+h.label+`, g.passed, g.failed, g.pw, g.fw
			FROM g ORDER BY g.day, g.key`,
			orgID, h.dimension, q.From.Format(time.DateOnly), q.To.Format(time.DateOnly))
		if err != nil {
			return fmt.Errorf("read compliance score history: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p HistoryPoint
			if err := rows.Scan(&p.Day, &p.Key, &p.Label, &p.Passed, &p.Failed, &p.PassedWeight, &p.FailedWeight); err != nil {
				return fmt.Errorf("scan compliance score history: %w", err)
			}
			p.Score = ScoreFromSums(p.PassedWeight, p.FailedWeight)
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
