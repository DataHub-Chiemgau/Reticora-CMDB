package lifecycle

import (
	"context"
	"encoding/json"
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
				applies_to, is_system, COALESCE(description, ''), created_at, updated_at
			FROM lifecycle_definition ORDER BY key ASC LIMIT $1 OFFSET $2`,
			page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list lifecycle definitions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d Definition
			if err := rows.Scan(&d.ID, &d.OrganizationID, &d.Key, &d.Name,
				&d.AppliesTo, &d.IsSystem, &d.Description, &d.CreatedAt, &d.UpdatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetByID returns one definition including states and transitions.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Definition, error) {
	var out *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var d Definition
		err := tx.QueryRow(ctx, `
			SELECT id::text, COALESCE(organization_id::text, ''), key, name,
				applies_to, is_system, COALESCE(description, ''), created_at, updated_at
			FROM lifecycle_definition WHERE id = $1`, id).
			Scan(&d.ID, &d.OrganizationID, &d.Key, &d.Name, &d.AppliesTo,
				&d.IsSystem, &d.Description, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get lifecycle definition: %w", err)
		}
		states, err := r.listStatesTx(ctx, tx, id)
		if err != nil {
			return err
		}
		d.States = states
		transitions, err := r.listTransitionsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		d.Transitions = transitions
		out = &d
		return nil
	})
	return out, err
}

func (r *PGRepository) listStatesTx(ctx context.Context, tx pgx.Tx, definitionID string) ([]State, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, definition_id::text, key, label, is_initial, is_terminal,
			sort_order, required_fields
		FROM lifecycle_state WHERE definition_id = $1
		ORDER BY sort_order ASC, key ASC`, definitionID)
	if err != nil {
		return nil, fmt.Errorf("list lifecycle states: %w", err)
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var s State
		var required []byte
		if err := rows.Scan(&s.ID, &s.DefinitionID, &s.Key, &s.Label,
			&s.IsInitial, &s.IsTerminal, &s.SortOrder, &required); err != nil {
			return nil, err
		}
		s.RequiredFields = []string{}
		if len(required) > 0 {
			_ = json.Unmarshal(required, &s.RequiredFields)
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
		stateIDs := map[string]string{}
		for _, st := range def.States {
			required, _ := json.Marshal(st.RequiredFields)
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO lifecycle_state (organization_id, definition_id, key, label, is_initial, is_terminal, sort_order, required_fields)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				RETURNING id::text`,
				def.OrganizationID, def.ID, st.Key, st.Label, st.IsInitial, st.IsTerminal, st.SortOrder, string(required)).
				Scan(&id); err != nil {
				return fmt.Errorf("create lifecycle state: %w", err)
			}
			stateIDs[st.Key] = id
		}
		for _, tr := range def.Transitions {
			required, _ := json.Marshal(tr.RequiredFields)
			label := tr.Label
			if label == "" {
				label = tr.Key
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO lifecycle_transition (organization_id, definition_id, from_state_id, to_state_id, key, label, required_fields)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				def.OrganizationID, def.ID, nilIfEmpty(stateIDs[tr.FromStateKey]),
				stateIDs[tr.ToStateKey], tr.Key, label, string(required)); err != nil {
				return fmt.Errorf("create lifecycle transition: %w", err)
			}
		}
		return nil
	})
}

// Delete removes a tenant-owned definition; system definitions are protected.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx,
			"DELETE FROM lifecycle_definition WHERE id = $1 AND organization_id IS NOT NULL AND NOT is_system", id)
		if err != nil {
			return fmt.Errorf("delete lifecycle definition: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("not found")
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
