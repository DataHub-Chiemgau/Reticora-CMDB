package monitoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGAlertStore persists alert rules in PostgreSQL. The table is
// RLS-protected; every statement runs inside a tenant-scoped transaction
// (app.org_id). The evaluation state of a series is its alarm (WP-984).
type PGAlertStore struct {
	pool *pgxpool.Pool
}

// NewPGAlertStore creates a PostgreSQL-backed alert rule store.
func NewPGAlertStore(pool *pgxpool.Pool) *PGAlertStore {
	return &PGAlertStore{pool: pool}
}

// scanRule maps an alert_rule row onto the domain type.
func scanRule(row pgx.Row) (AlertRule, error) {
	var (
		r                            AlertRule
		duration, window             time.Duration
		scope                        []byte
		condition, severity          *string
		threshold                    *float64
		critical, warning, info, clr []byte
	)
	err := row.Scan(&r.ID, &r.OrgID, &r.Name, &r.MetricName, &condition,
		&threshold, &duration, &severity, &r.Enabled, &scope, &r.GroupBy,
		&critical, &warning, &info, &clr, &window)
	if err != nil {
		return r, err
	}
	r.Duration, r.EvalWindow = duration, window
	if condition != nil && threshold != nil && severity != nil {
		r.Condition, r.Threshold, r.Severity = *condition, *threshold, *severity
	}
	r.CriticalCondition, r.WarningCondition = rawOrNil(critical), rawOrNil(warning)
	r.InfoCondition, r.ClearCondition = rawOrNil(info), rawOrNil(clr)
	if r.GroupBy == nil {
		r.GroupBy = []string{}
	}
	if err := json.Unmarshal(scope, &r.Scope); err != nil {
		return r, fmt.Errorf("decode alert rule scope: %w", err)
	}
	return r, nil
}

// rawOrNil is a jsonb column as a condition (nil for NULL).
func rawOrNil(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return json.RawMessage(b)
	}
	return buf.Bytes()
}

// conditionArg is a condition as a jsonb parameter (NULL for none).
func conditionArg(raw json.RawMessage) any {
	if isNull(raw) {
		return nil
	}
	return string(raw)
}

// legacyArgs are condition, threshold and severity as parameters: NULL for
// a rule without the old form.
func legacyArgs(r *AlertRule) (condition, threshold, severity any) {
	if r.Condition == "" {
		return nil, nil, nil
	}
	return r.Condition, r.Threshold, r.Severity
}

const ruleColumns = `id::text, organization_id::text, name, metric_name, condition, threshold,
	EXTRACT(EPOCH FROM duration) * 1000000000, severity, enabled, scope, group_by,
	critical_condition, warning_condition, info_condition, clear_condition,
	EXTRACT(EPOCH FROM eval_window) * 1000000000`

// scopeJSON encodes a scope for the column scope.
func scopeJSON(scope *RuleScope) ([]byte, error) {
	return json.Marshal(scope)
}

// groupByOf is group_by for the column (never NULL).
func groupByOf(groupBy []string) []string {
	if groupBy == nil {
		return []string{}
	}
	return groupBy
}

// checkScopeVisible refuses ids of a scope that the organization does not
// see under the transaction's scope: 422 with the field of the first one
// (scope.ci_type_ids[1]), unknown and invisible alike.
func checkScopeVisible(ctx context.Context, tx pgx.Tx, scope *RuleScope) error {
	lists := []struct {
		field, query string
		ids          []string
	}{
		{"scope.ci_ids", `SELECT 1 FROM ci WHERE id = $1::uuid`, scope.CIIDs},
		{"scope.ci_type_ids", `SELECT 1 FROM ci_type WHERE id = $1::uuid`, scope.CITypeIDs},
	}
	for _, l := range lists {
		if len(l.ids) == 0 {
			continue
		}
		var missing *int
		if err := tx.QueryRow(ctx, `SELECT min(n)::int FROM unnest($1::uuid[]) WITH ORDINALITY AS t(id, n)
			WHERE NOT EXISTS (`+strings.Replace(l.query, "$1::uuid", "t.id", 1)+`)`, l.ids).Scan(&missing); err != nil {
			return fmt.Errorf("check alert rule scope: %w", err)
		}
		if missing != nil {
			return unprocessableField(fmt.Sprintf("%s[%d]", l.field, *missing-1), "not found")
		}
	}
	for _, one := range []struct{ field, table, id string }{
		{"scope.client_id", "client", scope.ClientID}, {"scope.site_id", "site", scope.SiteID},
	} {
		if one.id == "" {
			continue
		}
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+one.table+` WHERE id = $1::uuid)`, one.id).Scan(&visible); err != nil {
			return fmt.Errorf("check alert rule scope: %w", err)
		}
		if !visible {
			return unprocessableField(one.field, "not found")
		}
	}
	return nil
}

// ListRules returns all rules of the tenant ordered by name.
func (s *PGAlertStore) ListRules(ctx context.Context, orgID string) ([]AlertRule, error) {
	rules := make([]AlertRule, 0)
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+ruleColumns+" FROM alert_rule ORDER BY name ASC, id ASC")
		if err != nil {
			return fmt.Errorf("list alert rules: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			rule, err := scanRule(rows)
			if err != nil {
				return err
			}
			rules = append(rules, rule)
		}
		return rows.Err()
	})
	return rules, err
}

// CreateRule inserts a rule; the id is generated by the database when empty.
// The rule is normalized first (normalizeRule): the old form becomes its
// stage, the old columns follow the stages.
func (s *PGAlertStore) CreateRule(ctx context.Context, in *AlertRule) (AlertRule, error) {
	var stored AlertRule
	rule := *in
	if err := normalizeRule(&rule); err != nil {
		return AlertRule{}, err
	}
	scope, err := scopeJSON(&rule.Scope)
	if err != nil {
		return AlertRule{}, err
	}
	condition, threshold, severity := legacyArgs(&rule)
	err = database.WithRequestTenant(ctx, s.pool, rule.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if serr := checkScopeVisible(ctx, tx, &rule.Scope); serr != nil {
			return serr
		}
		row := tx.QueryRow(ctx,
			`INSERT INTO alert_rule (organization_id, name, metric_name, condition, threshold, duration, severity, enabled,
			                         scope, group_by, critical_condition, warning_condition, info_condition,
			                         clear_condition, eval_window)
			 VALUES ($1, $2, $3, $4, $5, $6::interval, $7, $8, $9, $10, $11::jsonb, $12::jsonb, $13::jsonb, $14::jsonb,
			         $15::interval)
			 RETURNING `+ruleColumns,
			rule.OrgID, rule.Name, rule.MetricName, condition, threshold, intervalLiteral(rule.Duration), severity,
			rule.Enabled, scope, groupByOf(rule.GroupBy), conditionArg(rule.CriticalCondition),
			conditionArg(rule.WarningCondition), conditionArg(rule.InfoCondition), conditionArg(rule.ClearCondition),
			intervalLiteral(rule.EvalWindow))
		created, serr := scanRule(row)
		if serr != nil {
			return serr
		}
		stored = created
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return AlertRule{}, ErrAlertRuleExists
		}
		var fieldErr *api.FieldError
		if errors.As(err, &fieldErr) {
			return AlertRule{}, err
		}
		return AlertRule{}, fmt.Errorf("create alert rule: %w", err)
	}
	return stored, nil
}

// UpdateRule applies a partial update to the tenant's rule (applyUpdate):
// the row is read and locked, merged with the request, checked as a whole
// and written back; the returned rule reflects the stored state.
func (s *PGAlertStore) UpdateRule(ctx context.Context, orgID, id string, req *UpdateAlertRuleRequest) (AlertRule, error) {
	var rule AlertRule
	if !api.IsUUID(id) {
		return AlertRule{}, errRuleNotFound
	}
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanRule(tx.QueryRow(ctx, "SELECT "+ruleColumns+" FROM alert_rule WHERE id = $1::uuid FOR UPDATE", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errRuleNotFound
		}
		if err != nil {
			return fmt.Errorf("read alert rule: %w", err)
		}
		if uerr := applyUpdate(&current, req); uerr != nil {
			return uerr
		}
		if req.ParsedScope != nil {
			if serr := checkScopeVisible(ctx, tx, req.ParsedScope); serr != nil {
				return serr
			}
		}
		scope, err := scopeJSON(&current.Scope)
		if err != nil {
			return err
		}
		condition, threshold, severity := legacyArgs(&current)
		// orgID scopes via RLS (set by withTenant); the WHERE clause filters
		// the row by id only.
		rule, err = scanRule(tx.QueryRow(ctx,
			`UPDATE alert_rule SET
				name = $2, condition = $3, threshold = $4, duration = $5::interval, severity = $6, enabled = $7,
				scope = $8::jsonb, group_by = $9::text[], critical_condition = $10::jsonb,
				warning_condition = $11::jsonb, info_condition = $12::jsonb, clear_condition = $13::jsonb,
				eval_window = $14::interval, updated_at = now()
			 WHERE id = $1::uuid
			 RETURNING `+ruleColumns,
			id, current.Name, condition, threshold, intervalLiteral(current.Duration), severity, current.Enabled,
			scope, groupByOf(current.GroupBy), conditionArg(current.CriticalCondition),
			conditionArg(current.WarningCondition), conditionArg(current.InfoCondition),
			conditionArg(current.ClearCondition), intervalLiteral(current.EvalWindow)))
		return err
	})
	if err != nil {
		return AlertRule{}, err
	}
	return rule, nil
}

// errRuleNotFound signals an update against a missing or foreign rule id.
var errRuleNotFound = errors.New("alert rule not found")

// DeleteRule removes the tenant's rule and reports whether it existed.
func (s *PGAlertStore) DeleteRule(ctx context.Context, orgID, id string) (bool, error) {
	deleted := false
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM alert_rule WHERE id = $1::uuid", id)
		if err != nil {
			return fmt.Errorf("delete alert rule: %w", err)
		}
		deleted = tag.RowsAffected() > 0
		return nil
	})
	return deleted, err
}

// ListEnabled returns every enabled rule across tenants; the evaluator needs
// the system-wide view. It reads with the system flag, which cannot write
// (database.WithSystem); the evaluator changes a rule's state in the rule's
// organization.
func (s *PGAlertStore) ListEnabled(ctx context.Context) ([]AlertRule, error) {
	rules := make([]AlertRule, 0)
	err := database.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+ruleColumns+" FROM alert_rule WHERE enabled ORDER BY organization_id, name")
		if err != nil {
			return fmt.Errorf("list enabled alert rules: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			rule, err := scanRule(rows)
			if err != nil {
				return err
			}
			rules = append(rules, rule)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return rules, nil
}

func intervalLiteral(d time.Duration) string {
	return fmt.Sprintf("%f seconds", d.Seconds())
}
