package alarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGStore keeps the alarms in PostgreSQL (tables alarm and
// alarm_transition, migration 000313). Every method joins the transaction
// of the context (database.WithRequestTenant), so the row level security
// of the caller's scope applies.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore returns the PostgreSQL store on pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

var _ Store = (*PGStore)(nil)

// alarmColumns are the columns scanAlarm reads, in its order.
const alarmColumns = `id::text, organization_id::text, client_id::text, site_id::text, ci_id::text,
	source_kind, source_id::text, series_key, labels, dedup_key,
	severity, state, suppressed_reason, incident_id::text,
	occurrence_count, first_seen_at, last_seen_at, opened_at, acknowledged_at, acknowledged_by::text, resolved_at,
	summary, details, updated_at`

// transitionColumns are the columns scanTransition reads, in its order.
const transitionColumns = `id::text, organization_id::text, client_id::text, site_id::text, alarm_id::text,
	from_state, to_state, from_severity, to_severity, at, actor_id::text, reason, comment`

func scanAlarm(row pgx.Row, extra ...any) (*Alarm, error) {
	var a Alarm
	var labels, details []byte
	dest := []any{&a.ID, &a.OrganizationID, &a.ClientID, &a.SiteID, &a.CIID,
		&a.SourceKind, &a.SourceID, &a.SeriesKey, &labels, &a.DedupKey,
		&a.Severity, &a.State, &a.SuppressedReason, &a.IncidentID,
		&a.OccurrenceCount, &a.FirstSeenAt, &a.LastSeenAt, &a.OpenedAt, &a.AcknowledgedAt, &a.AcknowledgedBy, &a.ResolvedAt,
		&a.Summary, &details, &a.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.Labels = map[string]string{}
	if err := json.Unmarshal(labels, &a.Labels); err != nil {
		return nil, fmt.Errorf("alarm: decode labels: %w", err)
	}
	a.Details = map[string]any{}
	if err := json.Unmarshal(details, &a.Details); err != nil {
		return nil, fmt.Errorf("alarm: decode details: %w", err)
	}
	return &a, nil
}

func scanTransition(row pgx.Row) (*Transition, error) {
	var t Transition
	if err := row.Scan(&t.ID, &t.OrganizationID, &t.ClientID, &t.SiteID, &t.AlarmID,
		&t.FromState, &t.ToState, &t.FromSeverity, &t.ToSeverity, &t.At, &t.ActorID, &t.Reason, &t.Comment); err != nil {
		return nil, err
	}
	return &t, nil
}

// jsonObject encodes a map as a JSON object; nil is {}.
func jsonObject[V any](m map[string]V) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, orgID, id string) (*Alarm, error) {
	var out *Alarm
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanAlarm(tx.QueryRow(ctx, `SELECT `+alarmColumns+` FROM alarm WHERE organization_id = $1 AND id = $2`, orgID, id))
		return err
	})
	return out, err
}

// LockForUpdate implements Store.
func (s *PGStore) LockForUpdate(ctx context.Context, orgID, id string) (*Alarm, error) {
	var out *Alarm
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanAlarm(tx.QueryRow(ctx, `SELECT `+alarmColumns+` FROM alarm WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
		return err
	})
	return out, err
}

// LockOpenByKey implements Store.
func (s *PGStore) LockOpenByKey(ctx context.Context, orgID, dedupKey string) (*Alarm, error) {
	var out *Alarm
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanAlarm(tx.QueryRow(ctx, `SELECT `+alarmColumns+` FROM alarm
			WHERE organization_id = $1 AND dedup_key = $2 AND state <> 'resolved' FOR UPDATE`, orgID, dedupKey))
		return err
	})
	return out, err
}

// sqlArgs collects the arguments of a statement.
type sqlArgs struct{ values []any }

// add appends v and returns its placeholder.
func (a *sqlArgs) add(v any) string {
	a.values = append(a.values, v)
	return "$" + strconv.Itoa(len(a.values))
}

// listWhere renders the conditions of a filter (without cursor and limit)
// with their arguments; $1 is the organization.
func listWhere(orgID string, f *ListFilter) (where []string, args *sqlArgs) {
	args = &sqlArgs{values: []any{orgID}}
	where = []string{"organization_id = $1"}
	arg := args.add
	if len(f.States) > 0 {
		where = append(where, "state = ANY("+arg(f.States)+"::text[])")
	}
	if len(f.Severities) > 0 {
		where = append(where, "severity = ANY("+arg(f.Severities)+"::text[])")
	}
	if len(f.SourceKinds) > 0 {
		where = append(where, "source_kind = ANY("+arg(f.SourceKinds)+"::text[])")
	}
	if f.SourceID != "" {
		where = append(where, "source_id = "+arg(f.SourceID)+"::uuid")
	}
	if f.CIID != "" {
		where = append(where, "ci_id = "+arg(f.CIID)+"::uuid")
	}
	if f.ClientID != "" {
		where = append(where, "client_id = "+arg(f.ClientID)+"::uuid")
	}
	if f.SiteID != "" {
		where = append(where, "site_id = "+arg(f.SiteID)+"::uuid")
	}
	if f.IncidentID != "" {
		where = append(where, "incident_id = "+arg(f.IncidentID)+"::uuid")
	}
	if f.Suppressed != nil {
		if *f.Suppressed {
			where = append(where, "suppressed_reason IS NOT NULL")
		} else {
			where = append(where, "suppressed_reason IS NULL")
		}
	}
	if f.To != nil {
		where = append(where, "first_seen_at < "+arg(*f.To))
	}
	if f.From != nil {
		where = append(where, "(resolved_at IS NULL OR resolved_at >= "+arg(*f.From)+")")
	}
	if f.OpenedFrom != nil {
		where = append(where, "opened_at >= "+arg(*f.OpenedFrom))
	}
	if f.OpenedTo != nil {
		where = append(where, "opened_at < "+arg(*f.OpenedTo))
	}
	return where, args
}

// Count implements Store.
func (s *PGStore) Count(ctx context.Context, orgID string, f *ListFilter, limit int) (int, error) {
	where, args := listWhere(orgID, f)
	query := `SELECT 1 FROM alarm WHERE ` + strings.Join(where, " AND ")
	if limit > 0 {
		query += ` LIMIT ` + args.add(limit)
	}
	var n int
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM (`+query+`) AS matching`, args.values...).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("alarm: count: %w", err)
	}
	return n, nil
}

// listSortKey is the sort key of the list (Alarm.SortAt).
const listSortKey = "COALESCE(opened_at, first_seen_at)"

// List implements Store.
func (s *PGStore) List(ctx context.Context, orgID string, f *ListFilter) (Page, error) {
	where, args := listWhere(orgID, f)
	arg := args.add
	if f.After != nil {
		at := arg(f.After.At)
		where = append(where, "("+listSortKey+", id) < ("+at+"::timestamptz, "+arg(f.After.ID)+"::uuid)")
	}
	limit := limitOf(f)
	query := `SELECT ` + alarmColumns + ` FROM alarm WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + listSortKey + ` DESC, id DESC LIMIT ` + arg(limit+1)
	var page Page
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args.values...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAlarm(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, *a)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, fmt.Errorf("alarm: list: %w", err)
	}
	return paged(page.Items, limit), nil
}

// paged cuts items (at most limit+1) to the page and sets the cursor of
// the next one.
func paged(items []Alarm, limit int) Page {
	if len(items) <= limit {
		return Page{Items: items}
	}
	last := items[limit-1]
	return Page{Items: items[:limit], Next: &Cursor{At: last.SortAt(), ID: last.ID}}
}

// UpsertOpen implements Store.
func (s *PGStore) UpsertOpen(ctx context.Context, orgID string, in *UpsertInput) (*Alarm, bool, error) {
	if err := ValidateUpsert(in); err != nil {
		return nil, false, err
	}
	labels, err := jsonObject(in.Labels)
	if err != nil {
		return nil, false, fmt.Errorf("alarm: encode labels: %w", err)
	}
	details, err := jsonObject(in.Details)
	if err != nil {
		return nil, false, fmt.Errorf("alarm: encode details: %w", err)
	}
	var observed any
	if !in.ObservedAt.IsZero() {
		observed = in.ObservedAt
	}
	var out *Alarm
	var created bool
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var qerr error
		out, qerr = scanAlarm(tx.QueryRow(ctx, `
			INSERT INTO alarm (organization_id, client_id, ci_id, source_kind, source_id, series_key, labels,
			                   severity, state, summary, details, first_seen_at, last_seen_at, opened_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
			        COALESCE($12::timestamptz, now()), COALESCE($12::timestamptz, now()),
			        CASE WHEN $9 = 'open' THEN COALESCE($12::timestamptz, now()) END)
			ON CONFLICT (organization_id, dedup_key) WHERE state <> 'resolved'
			DO UPDATE SET occurrence_count = alarm.occurrence_count + 1,
			              last_seen_at = GREATEST(alarm.last_seen_at, EXCLUDED.last_seen_at)
			RETURNING `+alarmColumns+`, (xmax = 0)`,
			orgID, in.ClientID, in.CIID, in.SourceKind, in.SourceID, in.SeriesKey, labels,
			in.Severity, in.State, in.Summary, details, observed), &created)
		return qerr
	})
	if err != nil {
		return nil, false, fmt.Errorf("alarm: upsert: %w", err)
	}
	return out, created, nil
}

// UpdateState implements Store.
func (s *PGStore) UpdateState(ctx context.Context, orgID, id string, u *StateUpdate) (*Alarm, error) {
	if err := validateState(u); err != nil {
		return nil, err
	}
	var out *Alarm
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanAlarm(tx.QueryRow(ctx, `
			UPDATE alarm SET state = $3, severity = $4, suppressed_reason = $5, incident_id = $6,
			       opened_at = $7, acknowledged_at = $8, acknowledged_by = $9, resolved_at = $10
			 WHERE organization_id = $1 AND id = $2
			RETURNING `+alarmColumns,
			orgID, id, u.State, u.Severity, u.SuppressedReason, u.IncidentID,
			u.OpenedAt, u.AcknowledgedAt, u.AcknowledgedBy, u.ResolvedAt))
		return err
	})
	return out, err
}

// AppendTransition implements Store.
func (s *PGStore) AppendTransition(ctx context.Context, orgID string, tr *Transition) (*Transition, error) {
	if err := ValidateTransition(tr); err != nil {
		return nil, err
	}
	var at any
	if !tr.At.IsZero() {
		at = tr.At
	}
	var out *Transition
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanTransition(tx.QueryRow(ctx, `
			INSERT INTO alarm_transition (organization_id, alarm_id, from_state, to_state, from_severity, to_severity,
			                              at, actor_id, reason, comment)
			VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7::timestamptz, now()), $8, $9, $10)
			RETURNING `+transitionColumns,
			orgID, tr.AlarmID, tr.FromState, tr.ToState, tr.FromSeverity, tr.ToSeverity, at, tr.ActorID, tr.Reason, tr.Comment))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("alarm: append transition: %w", err)
	}
	return out, nil
}

// Transitions implements Store.
func (s *PGStore) Transitions(ctx context.Context, orgID, alarmID string) ([]Transition, error) {
	return s.TransitionsUpTo(ctx, orgID, alarmID, 0)
}

// TransitionsUpTo implements Store.
func (s *PGStore) TransitionsUpTo(ctx context.Context, orgID, alarmID string, limit int) ([]Transition, error) {
	var out []Transition
	var lim any // NULL: LIMIT ALL
	if limit > 0 {
		lim = limit
	}
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+transitionColumns+` FROM alarm_transition
			WHERE organization_id = $1 AND alarm_id = $2 ORDER BY at, id LIMIT $3::int`, orgID, alarmID, lim)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTransition(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("alarm: transitions: %w", err)
	}
	return out, nil
}

// now is the clock of the memory store and the retention (tests replace it
// through their own constructors).
func now() time.Time { return time.Now().UTC() }
