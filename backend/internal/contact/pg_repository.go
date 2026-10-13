package contact

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed contact repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

const contactCols = `id::text, organization_id::text, COALESCE(client_id::text,''), COALESCE(display_name,''),
	COALESCE(email,''), COALESCE(phone,''), COALESCE(role,''), COALESCE(department,''), COALESCE(notes,''),
	is_24_7, COALESCE(pii_subject_id::text,''), created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanContact(s scanner) (*Contact, error) {
	c := &Contact{}
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.DisplayName,
		&c.Email, &c.Phone, &c.Role, &c.Department, &c.Notes, &c.Is247, &c.subject, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

func scanContacts(rows pgx.Rows) ([]*Contact, error) {
	defer rows.Close()
	var out []*Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// personal is the data of a contact that lives in the PII vault (AUD-01,
// SEC-05).
func personal(c *Contact) pii.Data {
	d := pii.Data{"name": c.DisplayName}
	if c.Email != "" {
		d["email"] = c.Email
	}
	if c.Phone != "" {
		d["phone"] = c.Phone
	}
	return d
}

// reveal fills name, phone and e-mail of contacts from their vault
// entries; a contact whose entry was deleted shows none.
func reveal(ctx context.Context, tx pgx.Tx, orgID string, contacts ...*Contact) error {
	vault := pii.Default()
	var ids []string
	for _, c := range contacts {
		if c.subject != "" {
			ids = append(ids, c.subject)
		}
	}
	if vault == nil || len(ids) == 0 {
		return nil
	}
	subjects, err := vault.ResolveTx(ctx, tx, orgID, ids)
	if err != nil {
		return fmt.Errorf("resolve contacts: %w", err)
	}
	for _, c := range contacts {
		if s := subjects[c.subject]; s != nil {
			c.DisplayName, c.Email, c.Phone = s.Data["name"], s.Data["email"], s.Data["phone"]
		}
	}
	return nil
}

// store writes the personal data of c: into its vault entry (created when
// missing) with the plaintext columns cleared, or into the columns when no
// vault is configured (development without a master key).
func store(ctx context.Context, tx pgx.Tx, c *Contact) (name, email, phone, subject any, err error) {
	vault := pii.Default()
	if vault == nil {
		return c.DisplayName, nilIfEmpty(c.Email), nilIfEmpty(c.Phone), nilIfEmpty(c.subject), nil
	}
	if c.subject == "" {
		c.subject, err = vault.CreateTx(ctx, tx, c.OrganizationID, pii.KindContact, personal(c))
	} else {
		err = vault.UpdateTx(ctx, tx, c.OrganizationID, c.subject, personal(c))
	}
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("store contact data: %w", err)
	}
	return nil, nil, nil, c.subject, nil
}

func (r *PGRepository) List(ctx context.Context, orgID, clientID string, page api.PaginationParams) ([]Contact, int, error) {
	var out []Contact
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if clientID != "" {
			where += " AND client_id = $2"
			args = append(args, clientID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM contact WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + contactCols + " FROM contact WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		found, err := scanContacts(rows)
		if err != nil {
			return err
		}
		if rerr := reveal(ctx, tx, orgID, found...); rerr != nil {
			return rerr
		}
		for _, c := range found {
			out = append(out, *c)
		}
		return nil
	})
	return out, total, err
}

func getTx(ctx context.Context, tx pgx.Tx, orgID, id string, lock bool) (*Contact, error) {
	q := "SELECT " + contactCols + " FROM contact WHERE organization_id = $1 AND id = $2"
	if lock {
		q += " FOR UPDATE"
	}
	c, err := scanContact(tx.QueryRow(ctx, q, orgID, id))
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		return nil, err
	}
	return c, reveal(ctx, tx, orgID, c)
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Contact, error) {
	var c *Contact
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = getTx(ctx, tx, orgID, id, false)
		return err
	})
	return c, err
}

func (r *PGRepository) Create(ctx context.Context, c *Contact) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		name, email, phone, subject, err := store(ctx, tx, c)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO contact (organization_id, client_id, display_name, email, phone, role, department, notes, is_24_7, pii_subject_id)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text, created_at, updated_at`,
			c.OrganizationID, nilIfEmpty(c.ClientID), name, email, phone,
			nilIfEmpty(c.Role), nilIfEmpty(c.Department), nilIfEmpty(c.Notes), c.Is247, subject,
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateContactRequest) (*Contact, error) {
	var c *Contact
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if c, err = getTx(ctx, tx, orgID, id, true); err != nil {
			return err
		}
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&c.ClientID, req.ClientID)
		set(&c.DisplayName, req.DisplayName)
		set(&c.Email, req.Email)
		set(&c.Phone, req.Phone)
		set(&c.Role, req.Role)
		set(&c.Department, req.Department)
		set(&c.Notes, req.Notes)
		if req.Is247 != nil {
			c.Is247 = *req.Is247
		}
		name, email, phone, subject, err := store(ctx, tx, c)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE contact SET client_id = $3, display_name = $4, email = $5, phone = $6, role = $7,
				department = $8, notes = $9, is_24_7 = $10, pii_subject_id = $11, updated_at = NOW()
			WHERE organization_id = $1 AND id = $2 RETURNING updated_at`,
			orgID, id, nilIfEmpty(c.ClientID), name, email, phone, nilIfEmpty(c.Role), nilIfEmpty(c.Department),
			nilIfEmpty(c.Notes), c.Is247, subject).Scan(&c.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

// ListByEmail returns all contacts with the given e-mail address
// (case-insensitive, surrounding whitespace ignored). Contacts in the
// vault are found by the blind token of the address.
func (r *PGRepository) ListByEmail(ctx context.Context, orgID, email string) ([]Contact, error) {
	var out []Contact
	want := strings.ToLower(strings.TrimSpace(email))
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		token := ""
		if vault := pii.Default(); vault != nil && want != "" {
			var err error
			if token, err = vault.ExactTokenTx(ctx, tx, orgID, want); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "SELECT "+contactCols+` FROM contact WHERE organization_id = $1 AND (
				(lower(trim(email)) = $2 AND email <> '')
				OR pii_subject_id IN (SELECT surrogate_id FROM pii_subject WHERE organization_id = $1 AND search_tokens @> ARRAY[$3]::text[]))
			ORDER BY created_at`, orgID, want, token)
		if err != nil {
			return err
		}
		found, err := scanContacts(rows)
		if err != nil {
			return err
		}
		if rerr := reveal(ctx, tx, orgID, found...); rerr != nil {
			return rerr
		}
		for _, c := range found {
			if want != "" && strings.ToLower(strings.TrimSpace(c.Email)) == want {
				out = append(out, *c)
			}
		}
		return nil
	})
	return out, err
}

// Delete removes the contact and its vault entry: its name, phone and
// e-mail are gone from the history too (crypto-shredding, SEC-05).
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var subject string
		err := tx.QueryRow(ctx, "DELETE FROM contact WHERE organization_id = $1 AND id = $2 RETURNING COALESCE(pii_subject_id::text, '')",
			orgID, id).Scan(&subject)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		if err != nil || subject == "" {
			return err
		}
		if derr := pii.DeleteTx(ctx, tx, orgID, subject); derr != nil && !errors.Is(derr, pii.ErrNotFound) {
			return derr
		}
		return nil
	})
}

// visibleLink restricts CI-contact links to those whose CI and contact are
// both visible under the transaction's tenant scope: the ci and contact
// policies filter the subqueries by client scope, while ci_contact carries no
// client column yet (WP-025).
const visibleLink = `EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_contact.ci_id)
	AND EXISTS (SELECT 1 FROM contact WHERE contact.id = ci_contact.contact_id)`

const ciContactCols = `id::text, organization_id::text, ci_id::text, contact_id::text, relationship_type, created_at`

func scanCIContact(s scanner) (*CIContact, error) {
	l := &CIContact{}
	if err := s.Scan(&l.ID, &l.OrganizationID, &l.CIID, &l.ContactID, &l.RelationshipType, &l.CreatedAt); err != nil {
		return nil, err
	}
	l.CreatedAt = l.CreatedAt.UTC()
	return l, nil
}

func (r *PGRepository) ListForCI(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]CIContact, int, error) {
	var out []CIContact
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ci_contact WHERE organization_id = $1 AND ci_id = $2 AND "+visibleLink, orgID, ciID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+ciContactCols+" FROM ci_contact WHERE organization_id = $1 AND ci_id = $2 AND "+visibleLink+" ORDER BY created_at DESC LIMIT $3 OFFSET $4", orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanCIContact(rows)
			if err != nil {
				return err
			}
			out = append(out, *l)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) Link(ctx context.Context, link *CIContact) error {
	return database.WithRequestTenant(ctx, r.pool, link.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1) AND EXISTS (SELECT 1 FROM contact WHERE id = $2)`,
			link.CIID, link.ContactID,
		).Scan(&visible); err != nil {
			return fmt.Errorf("check link visibility: %w", err)
		}
		if !visible {
			return fmt.Errorf("not found")
		}
		return tx.QueryRow(ctx,
			`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type)
			 VALUES ($1,$2,$3,$4) RETURNING id::text, created_at`,
			link.OrganizationID, link.CIID, link.ContactID, link.RelationshipType,
		).Scan(&link.ID, &link.CreatedAt)
	})
}

func (r *PGRepository) Unlink(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM ci_contact WHERE organization_id = $1 AND id = $2 AND "+visibleLink, orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}
