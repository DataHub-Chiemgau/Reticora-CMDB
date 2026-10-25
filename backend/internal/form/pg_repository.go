package form

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const definitionColumns = `id::text, organization_id::text, COALESCE(client_id::text, ''), name, description, schema, ui_hints, active, created_at, updated_at`
const submissionColumns = `id::text, organization_id::text, form_id::text, values, COALESCE(submitted_by::text, ''), COALESCE(ci_id::text, ''), COALESCE(ticket_id::text, ''), status, created_at, updated_at`

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// submissionVisible restricts submissions to those whose form, CI and ticket
// are visible under the transaction's tenant scope: their policies filter the
// subqueries (tickets by their related CI and asset until WP-028), while
// form_submission carries no client column yet (WP-025).
const submissionVisible = `EXISTS (SELECT 1 FROM form_def WHERE form_def.id = form_submission.form_id)
	AND (form_submission.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = form_submission.ci_id))
	AND (form_submission.ticket_id IS NULL OR EXISTS (SELECT 1 FROM ticket WHERE ticket.id = form_submission.ticket_id
		AND (ticket.related_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = ticket.related_ci_id))
		AND (ticket.related_asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = ticket.related_asset_id))))`

// requireVisible fails with api.ErrNotFound when the written row of table
// does not satisfy visible, i.e. when a write pointed it at an object outside
// the tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return api.ErrNotFound
	}
	return nil
}

// requireSubmissionRefs fails when an object a submission names is no UUID
// or not visible under the caller's scope: the form definition with
// ErrNotFound, the CI or the ticket (by its related CI and asset, as in
// submissionVisible) with a 422 field error. The foreign keys of
// form_submission are checked without row level security: an object of
// another organization or client passed them, an unknown one failed with the
// constraint text, so the two could be told apart (TEN-04, API-03, F-047).
func requireSubmissionRefs(ctx context.Context, tx pgx.Tx, sub *Submission) error {
	for _, ref := range []struct {
		id, field, table, visible string
		missing                   error
	}{
		{sub.FormID, "", "form_def", "true", ErrNotFound},
		{sub.CIID, "ci_id", "ci", "true", &api.FieldError{Field: "ci_id", Message: "CI not found", Status: http.StatusUnprocessableEntity}},
		{sub.TicketID, "ticket_id", "ticket", `(ticket.related_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = ticket.related_ci_id))
			AND (ticket.related_asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = ticket.related_asset_id))`,
			&api.FieldError{Field: "ticket_id", Message: "ticket not found", Status: http.StatusUnprocessableEntity}},
	} {
		if ref.id == "" {
			continue
		}
		if !api.IsUUID(ref.id) {
			if ref.field == "" {
				return ref.missing
			}
			return api.RequireUUID(ref.field, ref.id)
		}
		var visible bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+ref.table+" WHERE id = $1 AND organization_id = $2 AND "+ref.visible+")",
			ref.id, sub.OrganizationID).Scan(&visible); err != nil {
			return fmt.Errorf("check %s of submission: %w", ref.table, err)
		}
		if !visible {
			return ref.missing
		}
	}
	return nil
}

func (r *PGRepository) ListDefinitions(ctx context.Context, orgID, clientID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
	var out []Definition
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if clientID != "" {
			where = append(where, fmt.Sprintf("client_id = $%d", pos))
			args = append(args, clientID)
			pos++
		}
		if activeOnly {
			where = append(where, "active = true")
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM form_def WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM form_def WHERE %s ORDER BY name ASC LIMIT $%d OFFSET $%d", definitionColumns, clause, pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanDefinition(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) GetDefinition(ctx context.Context, orgID, id string) (*Definition, error) {
	var item *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scanDefinition(tx.QueryRow(ctx, "SELECT "+definitionColumns+" FROM form_def WHERE organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return item, err
}
func (r *PGRepository) CreateDefinition(ctx context.Context, def *Definition) error {
	return database.WithRequestTenant(ctx, r.pool, def.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if def.Schema == nil {
			def.Schema = JSONMap{}
		}
		if def.UIHints == nil {
			def.UIHints = JSONMap{}
		}
		return tx.QueryRow(ctx, `INSERT INTO form_def (organization_id, client_id, name, description, schema, ui_hints, active) VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7) RETURNING id::text, created_at, updated_at`, def.OrganizationID, def.ClientID, def.Name, def.Description, def.Schema, def.UIHints, def.Active).Scan(&def.ID, &def.CreatedAt, &def.UpdatedAt)
	})
}
func (r *PGRepository) UpdateDefinition(ctx context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	var item *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		set := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.ClientID != nil {
			set = append(set, fmt.Sprintf("client_id = NULLIF($%d,'')::uuid", pos))
			args = append(args, *req.ClientID)
			pos++
		}
		if req.Name != nil {
			set = append(set, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Description != nil {
			set = append(set, fmt.Sprintf("description = $%d", pos))
			args = append(args, *req.Description)
			pos++
		}
		if req.Schema != nil {
			set = append(set, fmt.Sprintf("schema = $%d", pos))
			args = append(args, req.Schema)
			pos++
		}
		if req.UIHints != nil {
			set = append(set, fmt.Sprintf("ui_hints = $%d", pos))
			args = append(args, req.UIHints)
			pos++
		}
		if req.Active != nil {
			set = append(set, fmt.Sprintf("active = $%d", pos))
			args = append(args, *req.Active)
			pos++
		}
		if len(set) == 0 {
			var err error
			item, err = scanDefinition(tx.QueryRow(ctx, "SELECT "+definitionColumns+" FROM form_def WHERE id=$1 AND organization_id=$2", id, orgID))
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		set = append(set, "updated_at = now()")
		var err error
		item, err = scanDefinition(tx.QueryRow(ctx, fmt.Sprintf("UPDATE form_def SET %s WHERE id=$1 AND organization_id=$2 RETURNING %s", strings.Join(set, ", "), definitionColumns), args...))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return item, err
}
func (r *PGRepository) DeleteDefinition(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM form_def WHERE organization_id=$1 AND id=$2", orgID, id)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
func (r *PGRepository) ListSubmissions(ctx context.Context, orgID string, filter SubmissionFilter, page api.PaginationParams) ([]Submission, int, error) {
	var out []Submission
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id=$1"}
		args := []any{orgID}
		pos := 2
		add := func(col, val string) {
			if val != "" {
				where = append(where, fmt.Sprintf("%s = $%d", col, pos))
				args = append(args, val)
				pos++
			}
		}
		add("form_id", filter.FormID)
		add("status", filter.Status)
		add("ticket_id", filter.TicketID)
		add("ci_id", filter.CIID)
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM form_submission WHERE "+submissionVisible+" AND "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM form_submission WHERE "+submissionVisible+" AND %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", submissionColumns, clause, pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanSubmission(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) GetSubmission(ctx context.Context, orgID, id string) (*Submission, error) {
	var item *Submission
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scanSubmission(tx.QueryRow(ctx, "SELECT "+submissionColumns+" FROM form_submission WHERE "+submissionVisible+" AND organization_id=$1 AND id=$2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrSubmissionNotFound
		}
		return err
	})
	return item, err
}
func (r *PGRepository) CreateSubmission(ctx context.Context, sub *Submission) error {
	return database.WithRequestTenant(ctx, r.pool, sub.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if sub.Values == nil {
			sub.Values = JSONMap{}
		}
		if sub.Status == "" {
			sub.Status = "submitted"
		}
		if err := requireSubmissionRefs(ctx, tx, sub); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO form_submission (organization_id, form_id, values, submitted_by, ci_id, ticket_id, status) VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7) RETURNING id::text, created_at, updated_at`, sub.OrganizationID, sub.FormID, sub.Values, sub.SubmittedBy, sub.CIID, sub.TicketID, sub.Status).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return err
		}
		return requireVisible(ctx, tx, "form_submission", submissionVisible, sub.ID)
	})
}

type scanner interface{ Scan(dest ...any) error }

func scanMap(raw []byte) JSONMap {
	if len(raw) == 0 {
		return JSONMap{}
	}
	var m JSONMap
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = JSONMap{}
	}
	return m
}
func scanDefinition(s scanner) (*Definition, error) {
	d := &Definition{}
	var client sql.NullString
	var schema, ui []byte
	if err := s.Scan(&d.ID, &d.OrganizationID, &client, &d.Name, &d.Description, &schema, &ui, &d.Active, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if client.Valid {
		d.ClientID = client.String
	}
	d.Schema = scanMap(schema)
	d.UIHints = scanMap(ui)
	return d, nil
}
func scanSubmission(s scanner) (*Submission, error) {
	sub := &Submission{}
	var vals []byte
	if err := s.Scan(&sub.ID, &sub.OrganizationID, &sub.FormID, &vals, &sub.SubmittedBy, &sub.CIID, &sub.TicketID, &sub.Status, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return nil, err
	}
	sub.Values = scanMap(vals)
	return sub, nil
}
