package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed lifecycle repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// List returns the lifecycle definitions visible to the tenant (own + global
// system definitions).
func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Definition, int, error) {
	var out []Definition
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM lifecycle_definition").Scan(&total); err != nil {
			return fmt.Errorf("count lifecycle definitions: %w", err)
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text, COALESCE(organization_id::text, ''), key, name,
				applies_to, is_system, COALESCE(description, ''), version, created_at, updated_at
			FROM lifecycle_definition ORDER BY key ASC, id ASC LIMIT $1 OFFSET $2`,
			page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list lifecycle definitions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d Definition
			if err := rows.Scan(&d.ID, &d.OrganizationID, &d.Key, &d.Name,
				&d.AppliesTo, &d.IsSystem, &d.Description, &d.Version, &d.CreatedAt, &d.UpdatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByID returns one definition including states and transitions; an
// unknown, invisible or malformed id is api.ErrNotFound.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Definition, error) {
	if !api.IsUUID(id) {
		return nil, api.ErrNotFound
	}
	var out *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := r.getTx(ctx, tx, id)
		out = d
		return err
	})
	return out, err
}

// getTx reads a definition with states and transitions in tx.
func (r *PGRepository) getTx(ctx context.Context, tx pgx.Tx, id string) (*Definition, error) {
	var d Definition
	err := tx.QueryRow(ctx, `
		SELECT id::text, COALESCE(organization_id::text, ''), key, name,
			applies_to, is_system, COALESCE(description, ''), version, created_at, updated_at
		FROM lifecycle_definition WHERE id = $1`, id).
		Scan(&d.ID, &d.OrganizationID, &d.Key, &d.Name, &d.AppliesTo,
			&d.IsSystem, &d.Description, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get lifecycle definition: %w", err)
	}
	states, err := r.listStatesTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	d.States = states
	transitions, err := r.listTransitionsTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	d.Transitions = transitions
	return &d, nil
}

func (r *PGRepository) listStatesTx(ctx context.Context, tx pgx.Tx, definitionID string) ([]State, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, definition_id::text, key, label, is_initial, is_terminal,
			sort_order, required_fields, locked_fields
		FROM lifecycle_state WHERE definition_id = $1
		ORDER BY sort_order ASC, key ASC`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list lifecycle states: %w", err)
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var s State
		var required, locked []byte
		if err := rows.Scan(&s.ID, &s.DefinitionID, &s.Key, &s.Label,
			&s.IsInitial, &s.IsTerminal, &s.SortOrder, &required, &locked); err != nil {
			return nil, err
		}
		s.RequiredFields, s.LockedFields = []string{}, []string{}
		if len(required) > 0 {
			_ = json.Unmarshal(required, &s.RequiredFields)
		}
		if len(locked) > 0 {
			_ = json.Unmarshal(locked, &s.LockedFields)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PGRepository) listTransitionsTx(ctx context.Context, tx pgx.Tx, definitionID string) ([]Transition, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.definition_id::text,
			COALESCE(t.from_state_id::text, ''), COALESCE(sf.key, ''),
			t.to_state_id::text, COALESCE(st.key, ''),
			t.key, t.label, t.required_fields, t.validation
		FROM lifecycle_transition t
		LEFT JOIN lifecycle_state sf ON sf.id = t.from_state_id
		LEFT JOIN lifecycle_state st ON st.id = t.to_state_id
		WHERE t.definition_id = $1
		ORDER BY t.key ASC`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list lifecycle transitions: %w", err)
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		var tr Transition
		var required, validation []byte
		if err := rows.Scan(&tr.ID, &tr.DefinitionID, &tr.FromStateID, &tr.FromStateKey,
			&tr.ToStateID, &tr.ToStateKey, &tr.Key, &tr.Label, &required, &validation); err != nil {
			return nil, err
		}
		tr.RequiredFields = []string{}
		if len(required) > 0 {
			_ = json.Unmarshal(required, &tr.RequiredFields)
		}
		if len(validation) > 0 {
			_ = json.Unmarshal(validation, &tr.Validation)
		}
		tr.Guard = guardFromValidation(validation)
		out = append(out, tr)
	}
	return out, rows.Err()
}

// Create inserts a definition with its states and transitions.
func (r *PGRepository) Create(ctx context.Context, def *Definition) error {
	return database.WithRequestTenant(ctx, r.pool, def.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		appliesTo := def.AppliesTo
		if appliesTo == "" {
			appliesTo = "asset"
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO lifecycle_definition (organization_id, key, name, applies_to, is_system, description)
			VALUES ($1, $2, $3, $4, false, $5)
			RETURNING id::text, created_at, updated_at`,
			def.OrganizationID, def.Key, def.Name, appliesTo, nilIfEmpty(def.Description)).
			Scan(&def.ID, &def.CreatedAt, &def.UpdatedAt); err != nil {
			return fmt.Errorf("create lifecycle definition: %w", err)
		}
		def.Version = 1
		def.AppliesTo = appliesTo
		stateIDs := map[string]string{}
		for i := range def.States {
			st := &def.States[i]
			id, err := insertStateTx(ctx, tx, def.OrganizationID, def.ID, st)
			if err != nil {
				return err
			}
			stateIDs[st.Key] = id
		}
		return insertTransitionsTx(ctx, tx, def.OrganizationID, def.ID, stateIDs, def.Transitions)
	})
}

func insertStateTx(ctx context.Context, tx pgx.Tx, orgID, definitionID string, st *State) (string, error) {
	required, _ := json.Marshal(api.NonNilSlice(st.RequiredFields))
	locked, _ := json.Marshal(api.NonNilSlice(st.LockedFields))
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO lifecycle_state (organization_id, definition_id, key, label, is_initial, is_terminal, sort_order, required_fields, locked_fields)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id::text`,
		orgID, definitionID, st.Key, st.Label, st.IsInitial, st.IsTerminal, st.SortOrder, string(required), string(locked)).
		Scan(&id); err != nil {
		return "", fmt.Errorf("create lifecycle state: %w", err)
	}
	return id, nil
}

// insertTransitionsTx writes the transitions with their guard in
// lifecycle_transition.validation ('{}' without one).
func insertTransitionsTx(ctx context.Context, tx pgx.Tx, orgID, definitionID string, stateIDs map[string]string, transitions []Transition) error {
	for i := range transitions {
		tr := &transitions[i]
		required, _ := json.Marshal(api.NonNilSlice(tr.RequiredFields))
		validation := []byte("{}")
		if tr.Guard != nil {
			raw, err := json.Marshal(tr.Guard)
			if err != nil {
				return fmt.Errorf("encode lifecycle guard: %w", err)
			}
			validation = raw
		}
		label := tr.Label
		if label == "" {
			label = tr.Key
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO lifecycle_transition (organization_id, definition_id, from_state_id, to_state_id, key, label, required_fields, validation)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			orgID, definitionID, nilIfEmpty(stateIDs[tr.FromStateKey]),
			stateIDs[tr.ToStateKey], tr.Key, label, string(required), string(validation)); err != nil {
			return fmt.Errorf("create lifecycle transition: %w", err)
		}
	}
	return nil
}

// Update implements Repository in one transaction: the definition row is
// locked and its version checked; removed states in which CIs or assets of
// the model stand refuse the change (*StatesInUseConflict; soft-deleted
// CIs count as well). Kept states are updated in place and keep their id,
// new ones are inserted, the transitions replaced; the version rises by
// one.
func (r *PGRepository) Update(ctx context.Context, def *Definition, expectedVersion int64) error {
	if !api.IsUUID(def.ID) {
		return api.ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, def.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := r.getTx(ctx, tx, def.ID)
		if err != nil {
			return err
		}
		if current.IsSystem || current.OrganizationID == "" {
			return ErrSystemModel
		}
		// Lock the organization row (system rows are not updatable under
		// RLS, so FOR UPDATE would hide them) and read it again.
		if _, err = tx.Exec(ctx, `SELECT 1 FROM lifecycle_definition WHERE id = $1 FOR UPDATE`, def.ID); err != nil {
			return fmt.Errorf("lock lifecycle definition: %w", err)
		}
		if current, err = r.getTx(ctx, tx, def.ID); err != nil {
			return err
		}
		if expectedVersion != api.IfMatchAny && expectedVersion != current.Version {
			return ErrVersionMismatch
		}
		wanted := map[string]bool{}
		for i := range def.States {
			wanted[def.States[i].Key] = true
		}
		existing := map[string]string{}
		var removed []string
		for i := range current.States {
			st := &current.States[i]
			existing[st.Key] = st.ID
			if !wanted[st.Key] {
				removed = append(removed, st.Key)
			}
		}
		if len(removed) > 0 {
			counts, countErr := stateCountsTx(ctx, tx, def.ID, removed)
			if countErr != nil {
				return countErr
			}
			if len(counts) > 0 {
				return &StatesInUseConflict{Counts: counts}
			}
		}
		if _, err = tx.Exec(ctx, `DELETE FROM lifecycle_transition WHERE definition_id = $1`, def.ID); err != nil {
			return fmt.Errorf("replace lifecycle transitions: %w", err)
		}
		if len(removed) > 0 {
			if _, err = tx.Exec(ctx, `DELETE FROM lifecycle_state WHERE definition_id = $1 AND key = ANY($2)`,
				def.ID, removed); err != nil {
				return fmt.Errorf("remove lifecycle states: %w", err)
			}
		}
		stateIDs := map[string]string{}
		for i := range def.States {
			st := &def.States[i]
			if id, ok := existing[st.Key]; ok {
				required, _ := json.Marshal(api.NonNilSlice(st.RequiredFields))
				locked, _ := json.Marshal(api.NonNilSlice(st.LockedFields))
				if _, err = tx.Exec(ctx, `
					UPDATE lifecycle_state SET label = $2, is_initial = $3, is_terminal = $4,
						sort_order = $5, required_fields = $6, locked_fields = $7
					WHERE id = $1`,
					id, st.Label, st.IsInitial, st.IsTerminal, st.SortOrder, string(required), string(locked)); err != nil {
					return fmt.Errorf("update lifecycle state: %w", err)
				}
				stateIDs[st.Key] = id
				continue
			}
			id, insertErr := insertStateTx(ctx, tx, current.OrganizationID, def.ID, st)
			if insertErr != nil {
				return insertErr
			}
			stateIDs[st.Key] = id
		}
		if insertErr := insertTransitionsTx(ctx, tx, current.OrganizationID, def.ID, stateIDs, def.Transitions); insertErr != nil {
			return insertErr
		}
		if _, err = tx.Exec(ctx, `
			UPDATE lifecycle_definition SET name = $2, description = $3, version = version + 1, updated_at = now()
			WHERE id = $1`, def.ID, def.Name, nilIfEmpty(def.Description)); err != nil {
			return fmt.Errorf("update lifecycle definition: %w", err)
		}
		stored, err := r.getTx(ctx, tx, def.ID)
		if err != nil {
			return err
		}
		*def = *stored
		return nil
	})
}

// modelTypesCTE selects the CI types whose effective model of each kind
// is $1 (lifecycle_model_effective): the types that bind it and their
// subtypes without a nearer model of the kind.
const modelTypesCTE = `
	WITH ci_types AS (
		SELECT id FROM ci_type WHERE lifecycle_model_effective(id, 'ci') = $1::uuid
	), asset_types AS (
		SELECT id FROM ci_type WHERE lifecycle_model_effective(id, 'asset') = $1::uuid
	)`

// stateCountsTx counts the CIs and assets of model id per state of states
// (the states a PATCH removes); soft-deleted CIs count, since a restore
// does not check the state again.
func stateCountsTx(ctx context.Context, tx pgx.Tx, id string, states []string) ([]StateCount, error) {
	rows, err := tx.Query(ctx, modelTypesCTE+`
		SELECT state, sum(n)::int FROM (
			SELECT c.lifecycle_state AS state, count(*) AS n FROM ci c
			WHERE c.ci_type_id IN (SELECT id FROM ci_types) AND c.lifecycle_state = ANY($2)
			GROUP BY c.lifecycle_state
			UNION ALL
			SELECT a.lifecycle_state, count(*) FROM asset a
			WHERE a.asset_type_id IN (SELECT id FROM asset_types) AND a.lifecycle_state = ANY($2)
			GROUP BY a.lifecycle_state
		) counted
		GROUP BY state ORDER BY state`, id, states)
	if err != nil {
		return nil, fmt.Errorf("count lifecycle states: %w", err)
	}
	counts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StateCount, error) {
		var c StateCount
		return c, row.Scan(&c.State, &c.Count)
	})
	if err != nil {
		return nil, fmt.Errorf("count lifecycle states: %w", err)
	}
	return counts, nil
}

// Delete removes an organization model in one transaction: the row is
// locked; a model that a CI type binds (lifecycle_model_id) or that is the
// effective model of CIs or assets with a lifecycle state is refused with
// the counts (*ModelInUseConflict); a system model is ErrSystemModel.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return api.ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var system bool
		err := tx.QueryRow(ctx, `
			SELECT organization_id IS NULL OR is_system FROM lifecycle_definition WHERE id = $1`, id).Scan(&system)
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete lifecycle definition: %w", err)
		}
		if system {
			return ErrSystemModel
		}
		if _, err = tx.Exec(ctx, `SELECT 1 FROM lifecycle_definition WHERE id = $1 FOR UPDATE`, id); err != nil {
			return fmt.Errorf("lock lifecycle definition: %w", err)
		}
		var used ModelInUseConflict
		if err = tx.QueryRow(ctx, modelTypesCTE+`
			SELECT
				(SELECT count(*) FROM ci_type WHERE lifecycle_model_id = $1::uuid)::int,
				(SELECT count(*) FROM ci WHERE ci_type_id IN (SELECT id FROM ci_types) AND lifecycle_state IS NOT NULL)::int,
				(SELECT count(*) FROM asset WHERE asset_type_id IN (SELECT id FROM asset_types) AND lifecycle_state IS NOT NULL)::int`,
			id).Scan(&used.CITypes, &used.CIs, &used.Assets); err != nil {
			return fmt.Errorf("check lifecycle model use: %w", err)
		}
		if used.CITypes > 0 || used.CIs > 0 || used.Assets > 0 {
			return &used
		}
		if _, err = tx.Exec(ctx, "DELETE FROM lifecycle_definition WHERE id = $1", id); err != nil {
			return fmt.Errorf("delete lifecycle definition: %w", err)
		}
		return nil
	})
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
