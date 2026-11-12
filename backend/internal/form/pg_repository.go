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

const definitionColumns = `id::text, organization_id::text, COALESCE(client_id::text, ''), name, description, schema, ui_hints, active,
	version, COALESCE(current_version_id::text, ''), COALESCE((SELECT v.version FROM form_def_version v WHERE v.id = form_def.current_version_id), 0),
	created_at, updated_at`
const submissionColumns = `id::text, organization_id::text, form_id::text, form_version_id::text,
	COALESCE((SELECT v.version FROM form_def_version v WHERE v.id = form_submission.form_version_id), 0),
	values, COALESCE(submitted_by::text, ''), COALESCE(ci_id::text, ''), COALESCE(ticket_id::text, ''), status, created_at, updated_at`
const versionColumns = `id::text, organization_id::text, form_id::text, COALESCE(client_id::text, ''), version, schema, ui_hints, COALESCE(created_by::text, ''), created_at`

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

// CreateDefinition stores the form and its schema version 1 in one
// transaction (WFL-01); current_version_id is checked at commit
// (DEFERRABLE), so the form can name the version before it exists.
func (r *PGRepository) CreateDefinition(ctx context.Context, def *Definition) error {
	return database.WithRequestTenant(ctx, r.pool, def.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if def.Schema == nil {
			def.Schema = JSONMap{}
		}
		if def.UIHints == nil {
			def.UIHints = JSONMap{}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO form_def (organization_id, client_id, name, description, schema, ui_hints, active, version, current_version_id)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, 1, gen_random_uuid())
			RETURNING id::text, version, current_version_id::text, created_at, updated_at`,
			def.OrganizationID, def.ClientID, def.Name, def.Description, def.Schema, def.UIHints, def.Active).
			Scan(&def.ID, &def.Version, &def.CurrentVersionID, &def.CreatedAt, &def.UpdatedAt); err != nil {
			return err
		}
		def.SchemaVersion = 1
		return insertVersion(ctx, tx, def, createdBy(ctx))
	})
}

// insertVersion stores schema and UI hints of def as its schema version
// def.SchemaVersion under the id def.CurrentVersionID; the trigger
// form_def_version_scope sets client and site from the form. created_by
// stays NULL for a user that is not one of the organization.
func insertVersion(ctx context.Context, tx pgx.Tx, def *Definition, user string) error {
	var by any
	if api.IsUUID(user) {
		by = user
	}
	_, err := tx.Exec(ctx, `INSERT INTO form_def_version (id, organization_id, form_id, version, schema, ui_hints, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, (SELECT u.id FROM app_user u WHERE u.id = $7::uuid AND u.organization_id = $2))`,
		def.CurrentVersionID, def.OrganizationID, def.ID, def.SchemaVersion, def.Schema, def.UIHints, by)
	return err
}

// UpdateDefinition changes a form under If-Match (API-07). A changed schema
// or UI hints stores the next schema version in the same transaction and
// points the form at it; every change raises the version counter (WFL-01).
// A request that changes nothing changes nothing.
func (r *PGRepository) UpdateDefinition(ctx context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	var item *Definition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanDefinition(tx.QueryRow(ctx, "SELECT "+definitionColumns+" FROM form_def WHERE id=$1 AND organization_id=$2 FOR UPDATE", id, orgID))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !ifMatches(req.IfMatch, current.Version) {
			return ErrPreconditionFailed
		}
		next := *current
		applyUpdate(&next, &req)
		changed, newSchema := changes(current, &next)
		if !changed {
			item = current
			return nil
		}
		if newSchema {
			err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, COALESCE(MAX(version), 0) + 1 FROM form_def_version WHERE form_id = $1 AND organization_id = $2`,
				id, orgID).Scan(&next.CurrentVersionID, &next.SchemaVersion)
			if err != nil {
				return err
			}
			err = insertVersion(ctx, tx, &next, req.CreatedBy)
			if err != nil {
				return err
			}
		}
		item, err = scanDefinition(tx.QueryRow(ctx, `UPDATE form_def SET client_id = NULLIF($3,'')::uuid, name = $4, description = $5, schema = $6, ui_hints = $7,
			active = $8, current_version_id = NULLIF($9,'')::uuid, version = version + 1, updated_at = now()
			WHERE id=$1 AND organization_id=$2 RETURNING `+definitionColumns,
			id, orgID, next.ClientID, next.Name, next.Description, next.Schema, next.UIHints, next.Active, next.CurrentVersionID))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return item, err
}

// ListVersions lists the schema versions of a form, newest first; a form
// that is unknown, of another organization or outside the caller's scope
// is ErrNotFound (row level security on form_def and form_def_version).
func (r *PGRepository) ListVersions(ctx context.Context, orgID, formID string, page api.PaginationParams) ([]DefinitionVersion, int, error) {
	out := []DefinitionVersion{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM form_def WHERE organization_id=$1 AND id=$2)", orgID, formID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM form_def_version WHERE organization_id=$1 AND form_id=$2", orgID, formID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+versionColumns+" FROM form_def_version WHERE organization_id=$1 AND form_id=$2 ORDER BY version DESC LIMIT $3 OFFSET $4",
			orgID, formID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetVersion returns schema version number version of a form visible to
// the caller.
func (r *PGRepository) GetVersion(ctx context.Context, orgID, formID string, version int) (*DefinitionVersion, error) {
	var v *DefinitionVersion
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		v, err = scanVersion(tx.QueryRow(ctx, "SELECT "+versionColumns+` FROM form_def_version
			WHERE organization_id=$1 AND form_id=$2 AND version=$3 AND EXISTS (SELECT 1 FROM form_def f WHERE f.id = form_def_version.form_id)`,
			orgID, formID, version))
		if err == pgx.ErrNoRows {
			return ErrVersionNotFound
		}
		return err
	})
	return v, err
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
		// The current schema version of the form is bound in the same
		// statement (WFL-01), for the API and the workflow action alike.
		if err := tx.QueryRow(ctx, `INSERT INTO form_submission (organization_id, form_id, form_version_id, values, submitted_by, ci_id, ticket_id, status)
			VALUES ($1, $2, (SELECT f.current_version_id FROM form_def f WHERE f.id = $2 AND f.organization_id = $1),
				$3, NULLIF($4,'')::uuid, NULLIF($5,'')::uuid, NULLIF($6,'')::uuid, $7)
			RETURNING id::text, form_version_id::text, (SELECT v.version FROM form_def_version v WHERE v.id = form_submission.form_version_id), created_at, updated_at`,
			sub.OrganizationID, sub.FormID, sub.Values, sub.SubmittedBy, sub.CIID, sub.TicketID, sub.Status).
			Scan(&sub.ID, &sub.FormVersionID, &sub.FormVersion, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return err
		}
		if sub.ExpectedVersionID != "" && sub.ExpectedVersionID != sub.FormVersionID {
			return ErrVersionChanged
		}
		// The values must satisfy the schema of the bound version, also
		// when a schema change committed after the caller validated (the
		// submit_form action checks against the definition it read before).
		// A version never changes, so this check holds for the stored row.
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT schema FROM form_def_version WHERE id = $1 AND organization_id = $2",
			sub.FormVersionID, sub.OrganizationID).Scan(&raw); err != nil {
			return fmt.Errorf("read bound form version: %w", err)
		}
		if err := Validate(scanMap(raw), map[string]any(sub.Values)); err != nil {
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
	if err := s.Scan(&d.ID, &d.OrganizationID, &client, &d.Name, &d.Description, &schema, &ui, &d.Active,
		&d.Version, &d.CurrentVersionID, &d.SchemaVersion, &d.CreatedAt, &d.UpdatedAt); err != nil {
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
	if err := s.Scan(&sub.ID, &sub.OrganizationID, &sub.FormID, &sub.FormVersionID, &sub.FormVersion, &vals, &sub.SubmittedBy, &sub.CIID, &sub.TicketID, &sub.Status, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return nil, err
	}
	sub.Values = scanMap(vals)
	return sub, nil
}
func scanVersion(s scanner) (*DefinitionVersion, error) {
	v := &DefinitionVersion{}
	var schema, ui []byte
	if err := s.Scan(&v.ID, &v.OrganizationID, &v.FormID, &v.ClientID, &v.Version, &schema, &ui, &v.CreatedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.Schema = scanMap(schema)
	v.UIHints = scanMap(ui)
	return v, nil
}
