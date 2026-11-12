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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// The incidents in PostgreSQL (tables incident and incident_transition,
// migration 000326). Every method runs in one transaction of the caller's
// scope (database.WithRequestTenant joins the context's transaction) and
// appends its transitions there.

const incidentColumns = `id::text, organization_id::text, client_id::text, site_id::text, root_alarm_id::text, root_ci_id::text,
	cause_class, confidence, reasons, state, opened_at, acknowledged_at, acknowledged_by::text, resolved_at,
	manual_root, affected_counts, updated_at`

const incidentTransitionColumns = `id::text, organization_id::text, client_id::text, site_id::text, incident_id::text,
	from_state, to_state, at, actor_id::text, reason, details`

func scanIncident(row pgx.Row) (*Incident, error) {
	var inc Incident
	var reasons, counts []byte
	if err := row.Scan(&inc.ID, &inc.OrganizationID, &inc.ClientID, &inc.SiteID, &inc.RootAlarmID, &inc.RootCIID,
		&inc.CauseClass, &inc.Confidence, &reasons, &inc.State, &inc.OpenedAt, &inc.AcknowledgedAt, &inc.AcknowledgedBy,
		&inc.ResolvedAt, &inc.ManualRoot, &counts, &inc.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, err
	}
	inc.Reasons = []string{}
	if err := json.Unmarshal(reasons, &inc.Reasons); err != nil {
		return nil, fmt.Errorf("alarm: decode incident reasons: %w", err)
	}
	inc.AffectedCounts = map[string]int{}
	if err := json.Unmarshal(counts, &inc.AffectedCounts); err != nil {
		return nil, fmt.Errorf("alarm: decode affected counts: %w", err)
	}
	return &inc, nil
}

func scanIncidentTransition(row pgx.Row) (*IncidentTransition, error) {
	var t IncidentTransition
	var details []byte
	if err := row.Scan(&t.ID, &t.OrganizationID, &t.ClientID, &t.SiteID, &t.IncidentID,
		&t.FromState, &t.ToState, &t.At, &t.ActorID, &t.Reason, &details); err != nil {
		return nil, err
	}
	t.Details = map[string]any{}
	if err := json.Unmarshal(details, &t.Details); err != nil {
		return nil, fmt.Errorf("alarm: decode incident transition details: %w", err)
	}
	return &t, nil
}

func jsonArray(lines []string) ([]byte, error) {
	if lines == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(lines)
}

// lockIncident reads the incident and locks its row.
func lockIncident(ctx context.Context, tx pgx.Tx, orgID, id string) (*Incident, error) {
	return scanIncident(tx.QueryRow(ctx, `SELECT `+incidentColumns+` FROM incident
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
}

// appendIncidentTransition writes one entry of the incident's log.
func appendIncidentTransition(ctx context.Context, tx pgx.Tx, orgID, incidentID string, from *string, to string,
	at time.Time, actor *string, reason string, details map[string]any) error {
	raw, err := jsonObject(details)
	if err != nil {
		return fmt.Errorf("alarm: encode transition details: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO incident_transition (organization_id, incident_id, from_state, to_state, at, actor_id, reason, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, orgID, incidentID, from, to, at, actor, reason, raw); err != nil {
		return fmt.Errorf("alarm: append incident transition: %w", err)
	}
	return nil
}

// UpsertIncident implements Store.
func (s *PGStore) UpsertIncident(ctx context.Context, orgID string, in *IncidentInput) (*Incident, bool, error) {
	if err := ValidateIncident(in); err != nil {
		return nil, false, err
	}
	reasons, err := jsonArray(in.Reasons)
	if err != nil {
		return nil, false, err
	}
	counts, err := jsonObject(in.AffectedCounts)
	if err != nil {
		return nil, false, err
	}
	at := in.At
	if at.IsZero() {
		at = now()
	}
	var out *Incident
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if in.ID == "" {
			inc, ierr := scanIncident(tx.QueryRow(ctx, `
				INSERT INTO incident (organization_id, client_id, site_id, root_ci_id, cause_class, confidence, reasons,
				                      affected_counts, opened_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				RETURNING `+incidentColumns,
				orgID, in.ClientID, in.SiteID, in.RootCIID, causeOrUnknown(in.CauseClass), in.Confidence, reasons, counts, at))
			if ierr != nil {
				return ierr
			}
			out = inc
			return appendIncidentTransition(ctx, tx, orgID, inc.ID, nil, IncidentOpen, at, nil, IncidentReasonOpened,
				map[string]any{"cause_class": inc.CauseClass, "confidence": inc.Confidence})
		}
		cur, lerr := lockIncident(ctx, tx, orgID, in.ID)
		if lerr != nil {
			return lerr
		}
		if cur.State == IncidentResolved {
			return ErrIncidentResolved
		}
		inc, uerr := scanIncident(tx.QueryRow(ctx, `
			UPDATE incident SET cause_class = $3, confidence = $4, reasons = $5, affected_counts = $6
			 WHERE organization_id = $1 AND id = $2 RETURNING `+incidentColumns,
			orgID, in.ID, causeOrUnknown(in.CauseClass), in.Confidence, reasons, counts))
		if uerr != nil {
			return uerr
		}
		out = inc
		return appendIncidentTransition(ctx, tx, orgID, inc.ID, &cur.State, inc.State, at, nil, IncidentReasonUpdated,
			map[string]any{"cause_class": inc.CauseClass, "confidence": inc.Confidence})
	})
	if err != nil {
		return nil, false, err
	}
	return out, in.ID == "", nil
}

// GetIncident implements Store.
func (s *PGStore) GetIncident(ctx context.Context, orgID, id string) (*Incident, error) {
	var out *Incident
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanIncident(tx.QueryRow(ctx, `SELECT `+incidentColumns+` FROM incident WHERE organization_id = $1 AND id = $2`,
			orgID, id))
		return err
	})
	return out, err
}

// ListIncidents implements Store.
func (s *PGStore) ListIncidents(ctx context.Context, orgID string, f *IncidentFilter) (IncidentPage, error) {
	args := []any{orgID}
	where := []string{"organization_id = $1"}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if len(f.States) > 0 {
		where = append(where, "state = ANY("+arg(f.States)+"::text[])")
	}
	if len(f.CauseClasses) > 0 {
		where = append(where, "cause_class = ANY("+arg(f.CauseClasses)+"::text[])")
	}
	if f.ClientID != "" {
		where = append(where, "client_id = "+arg(f.ClientID)+"::uuid")
	}
	if f.To != nil {
		where = append(where, "opened_at < "+arg(*f.To))
	}
	if f.From != nil {
		where = append(where, "(resolved_at IS NULL OR resolved_at >= "+arg(*f.From)+")")
	}
	if f.After != nil {
		at := arg(f.After.OpenedAt)
		where = append(where, "(opened_at, id) < ("+at+"::timestamptz, "+arg(f.After.ID)+"::uuid)")
	}
	limit := incidentLimitOf(f)
	query := `SELECT ` + incidentColumns + ` FROM incident WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY opened_at DESC, id DESC LIMIT ` + arg(limit+1)
	var items []Incident
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			inc, err := scanIncident(rows)
			if err != nil {
				return err
			}
			items = append(items, *inc)
		}
		return rows.Err()
	})
	if err != nil {
		return IncidentPage{}, fmt.Errorf("alarm: list incidents: %w", err)
	}
	return pagedIncidents(items, limit), nil
}

// attachSQL assigns alarms to an incident: the root loses a symptom
// suppression, a symptom gets one unless it is suppressed already. An alarm
// of another incident is never taken (lockAttachable refuses it before).
const attachSQL = `UPDATE alarm SET incident_id = $2,
	suppressed_reason = CASE WHEN $4 THEN NULLIF(suppressed_reason, 'symptom') ELSE COALESCE(suppressed_reason, 'symptom') END
	WHERE organization_id = $1 AND id = ANY($3::uuid[]) AND (incident_id IS NULL OR incident_id = $2)
	RETURNING id::text, ci_id::text`

// lockAttachable locks the alarms ids for an attach to incidentID: every
// one must exist in the caller's scope (ErrNotFound) and belong to no other
// incident (ErrAlarmInOtherIncident).
func lockAttachable(ctx context.Context, tx pgx.Tx, orgID, incidentID string, ids []string) error {
	rows, err := tx.Query(ctx, `SELECT incident_id::text FROM alarm WHERE organization_id = $1 AND id = ANY($2::uuid[])
		ORDER BY id FOR UPDATE`, orgID, ids)
	if err != nil {
		return err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[*string])
	if err != nil {
		return err
	}
	if len(owners) != len(ids) {
		return fmt.Errorf("alarm: attach: %w", ErrNotFound)
	}
	for _, owner := range owners {
		if owner != nil && *owner != incidentID {
			return fmt.Errorf("alarm: attach: %w", ErrAlarmInOtherIncident)
		}
	}
	return nil
}

// setRootTx makes alarmID (already attached) the root of inc.
func setRootTx(ctx context.Context, tx pgx.Tx, inc *Incident, alarmID string, ciID *string, manual bool, at time.Time) (*Incident, error) {
	if inc.RootAlarmID != nil && *inc.RootAlarmID != alarmID {
		if _, err := tx.Exec(ctx, `UPDATE alarm SET suppressed_reason = COALESCE(suppressed_reason, 'symptom')
			WHERE organization_id = $1 AND id = $2 AND incident_id = $3`, inc.OrganizationID, *inc.RootAlarmID, inc.ID); err != nil {
			return nil, err
		}
	}
	out, err := scanIncident(tx.QueryRow(ctx, `UPDATE incident SET root_alarm_id = $3, root_ci_id = $4, manual_root = manual_root OR $5
		WHERE organization_id = $1 AND id = $2 RETURNING `+incidentColumns, inc.OrganizationID, inc.ID, alarmID, ciID, manual))
	if err != nil {
		return nil, err
	}
	return out, appendIncidentTransition(ctx, tx, inc.OrganizationID, inc.ID, &inc.State, out.State, at, nil,
		IncidentReasonRootChanged, map[string]any{"from_alarm_id": deref(inc.RootAlarmID), "to_alarm_id": alarmID, "manual": manual})
}

// AttachAlarms implements Store.
func (s *PGStore) AttachAlarms(ctx context.Context, orgID, incidentID string, alarmIDs []string, role string) (*Incident, error) {
	ids := uniqueIDs(alarmIDs)
	if err := validateAttach(ids, role); err != nil {
		return nil, err
	}
	at := now()
	var out *Incident
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		inc, err := lockIncident(ctx, tx, orgID, incidentID)
		if err != nil {
			return err
		}
		if cerr := checkAttach(inc, role); cerr != nil {
			return cerr
		}
		if lerr := lockAttachable(ctx, tx, orgID, incidentID, ids); lerr != nil {
			return lerr
		}
		rows, err := tx.Query(ctx, attachSQL, orgID, incidentID, ids, role == RoleRoot)
		if err != nil {
			return err
		}
		type attached struct {
			id string
			ci *string
		}
		got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (attached, error) {
			var a attached
			return a, row.Scan(&a.id, &a.ci)
		})
		if err != nil {
			return err
		}
		if len(got) != len(ids) {
			return fmt.Errorf("alarm: attach: %w", ErrNotFound)
		}
		if role == RoleRoot {
			out, err = setRootTx(ctx, tx, inc, got[0].id, got[0].ci, false, at)
			return err
		}
		out = inc
		return appendIncidentTransition(ctx, tx, orgID, incidentID, &inc.State, inc.State, at, nil,
			IncidentReasonAlarmsAttached, map[string]any{"alarm_ids": ids, "role": role})
	})
	return out, err
}

// SetRoot implements Store.
func (s *PGStore) SetRoot(ctx context.Context, orgID, incidentID, alarmID string, manual bool) (*Incident, error) {
	at := now()
	var out *Incident
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		inc, err := lockIncident(ctx, tx, orgID, incidentID)
		if err != nil {
			return err
		}
		if cerr := checkRoot(inc, manual); cerr != nil {
			return cerr
		}
		if lerr := lockAttachable(ctx, tx, orgID, incidentID, []string{alarmID}); lerr != nil {
			return lerr
		}
		var ciID *string
		if err = tx.QueryRow(ctx, attachSQL, orgID, incidentID, []string{alarmID}, true).Scan(new(string), &ciID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("alarm: set root: %w", ErrNotFound)
			}
			return err
		}
		out, err = setRootTx(ctx, tx, inc, alarmID, ciID, manual, at)
		return err
	})
	return out, err
}

// MergeIncidents implements Store.
func (s *PGStore) MergeIncidents(ctx context.Context, orgID, keepID, absorbID, rootAlarmID string) (*Incident, error) {
	if keepID == absorbID {
		return nil, fmt.Errorf("alarm: merge an incident into itself")
	}
	at := now()
	var out *Incident
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// In a client or site scope the restrictive policies of alarm would
		// skip alarms of absorb outside the scope and leave them at a
		// resolved incident: the merge needs the organization-wide scope.
		var orgWide bool
		if err := tx.QueryRow(ctx, `SELECT NULLIF(current_setting('app.client_scope', true), '') IS NULL
			AND NULLIF(current_setting('app.site_scope', true), '') IS NULL`).Scan(&orgWide); err != nil {
			return err
		}
		if !orgWide {
			return ErrMergeScopeRestricted
		}
		// Lock in id order: two merges of the same pair never deadlock.
		first, second := keepID, absorbID
		if second < first {
			first, second = second, first
		}
		locked := map[string]*Incident{}
		for _, id := range []string{first, second} {
			inc, err := lockIncident(ctx, tx, orgID, id)
			if err != nil {
				return err
			}
			if inc.State == IncidentResolved {
				return ErrIncidentResolved
			}
			locked[id] = inc
		}
		keep, absorb := locked[keepID], locked[absorbID]
		// The root of keep stays when it was set by hand.
		if keep.ManualRoot || (keep.RootAlarmID != nil && *keep.RootAlarmID == rootAlarmID) {
			rootAlarmID = ""
		}
		tag, err := tx.Exec(ctx, `UPDATE alarm SET incident_id = $2,
			suppressed_reason = CASE WHEN id = $4::uuid THEN NULLIF(suppressed_reason, 'symptom')
			                         ELSE COALESCE(suppressed_reason, 'symptom') END
			WHERE organization_id = $1 AND incident_id = $3`, orgID, keepID, absorbID, nilIfEmpty(rootAlarmID))
		if err != nil {
			return err
		}
		moved := int(tag.RowsAffected())
		// absorb keeps no root alarm: it belongs to keep now; the former
		// root stays in the details of merged_into, the root CI stays.
		if _, err = tx.Exec(ctx, `UPDATE incident SET state = 'resolved', resolved_at = $3, root_alarm_id = NULL
			WHERE organization_id = $1 AND id = $2`, orgID, absorbID, at); err != nil {
			return err
		}
		if aerr := appendIncidentTransition(ctx, tx, orgID, absorbID, &absorb.State, IncidentResolved, at, nil,
			IncidentReasonMergedInto, map[string]any{"into": keepID, "alarms": moved,
				"root_alarm_id": deref(absorb.RootAlarmID)}); aerr != nil {
			return aerr
		}
		if aerr := appendIncidentTransition(ctx, tx, orgID, keepID, &keep.State, keep.State, at, nil,
			IncidentReasonAbsorbed, map[string]any{"absorbed": absorbID, "alarms": moved}); aerr != nil {
			return aerr
		}
		out = keep
		if rootAlarmID == "" {
			out, err = scanIncident(tx.QueryRow(ctx, `SELECT `+incidentColumns+` FROM incident WHERE organization_id = $1 AND id = $2`,
				orgID, keepID))
			return err
		}
		if lerr := lockAttachable(ctx, tx, orgID, keepID, []string{rootAlarmID}); lerr != nil {
			return lerr
		}
		var ciID *string
		if err = tx.QueryRow(ctx, attachSQL, orgID, keepID, []string{rootAlarmID}, true).Scan(new(string), &ciID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("alarm: merge root: %w", ErrNotFound)
			}
			return err
		}
		out, err = setRootTx(ctx, tx, keep, rootAlarmID, ciID, false, at)
		return err
	})
	return out, err
}

// TransitionIncident implements Store.
func (s *PGStore) TransitionIncident(ctx context.Context, orgID, id, to string, actorID *string) (*Incident, error) {
	at := now()
	var out *Incident
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		inc, err := lockIncident(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if !incidentTransitionAllowed(inc.State, to) {
			return fmt.Errorf("%w: incident %s to %s", ErrInvalidTransition, inc.State, to)
		}
		var ackAt *time.Time
		var ackBy *string
		var resolvedAt *time.Time
		switch to {
		case IncidentAcknowledged:
			ackAt, ackBy = &at, actorID
		case IncidentResolved:
			ackAt, ackBy, resolvedAt = inc.AcknowledgedAt, inc.AcknowledgedBy, &at
		}
		out, err = scanIncident(tx.QueryRow(ctx, `UPDATE incident SET state = $3, acknowledged_at = $4, acknowledged_by = $5,
			resolved_at = $6 WHERE organization_id = $1 AND id = $2 RETURNING `+incidentColumns,
			orgID, id, to, ackAt, ackBy, resolvedAt))
		if err != nil {
			return err
		}
		return appendIncidentTransition(ctx, tx, orgID, id, &inc.State, to, at, actorID, incidentReason(inc.State, to), nil)
	})
	return out, err
}

// IncidentTransitions implements Store.
func (s *PGStore) IncidentTransitions(ctx context.Context, orgID, incidentID string) ([]IncidentTransition, error) {
	var out []IncidentTransition
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+incidentTransitionColumns+` FROM incident_transition
			WHERE organization_id = $1 AND incident_id = $2 ORDER BY at, id`, orgID, incidentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanIncidentTransition(rows)
			if err != nil {
				return err
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("alarm: incident transitions: %w", err)
	}
	return out, nil
}

// validateAttach checks the alarms and role of AttachAlarms.
func validateAttach(ids []string, role string) error {
	switch {
	case role != RoleRoot && role != RoleSymptom:
		return fmt.Errorf("alarm: unknown role %q", role)
	case len(ids) == 0:
		return fmt.Errorf("alarm: no alarms to attach")
	case role == RoleRoot && len(ids) != 1:
		return fmt.Errorf("alarm: an incident has one root alarm, not %d", len(ids))
	}
	return nil
}

// checkAttach checks the incident of AttachAlarms.
func checkAttach(inc *Incident, role string) error {
	if inc.State == IncidentResolved {
		return ErrIncidentResolved
	}
	if role == RoleRoot && inc.ManualRoot {
		return ErrManualRoot
	}
	return nil
}

// checkRoot checks the incident of SetRoot.
func checkRoot(inc *Incident, manual bool) error {
	if inc.State == IncidentResolved {
		return ErrIncidentResolved
	}
	if inc.ManualRoot && !manual {
		return ErrManualRoot
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
