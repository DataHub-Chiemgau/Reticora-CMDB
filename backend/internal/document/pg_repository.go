package document

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const documentSelectColumns = `
	id::text,
	organization_id::text,
	title,
	COALESCE(description, ''),
	file_name,
	file_size,
	mime_type,
	storage_key,
	version,
	category,
	COALESCE(tags, '{}'),
	uploaded_by::text,
	created_at,
	updated_at
`

const documentSelectColumnsWithAlias = `
	d.id::text,
	d.organization_id::text,
	d.title,
	COALESCE(d.description, ''),
	d.file_name,
	d.file_size,
	d.mime_type,
	d.storage_key,
	d.version,
	d.category,
	COALESCE(d.tags, '{}'),
	d.uploaded_by::text,
	d.created_at,
	d.updated_at
`

const documentLinkSelectColumns = `
	id::text,
	organization_id::text,
	document_id::text,
	entity_type,
	entity_id::text,
	created_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed document repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// Documents and their links carry no client or site column yet (WP-028). A
// link is visible when its object is visible under the transaction's tenant
// scope, whose policies filter the subqueries; a document is visible when it
// is not linked at all or linked to at least one visible object.
const (
	linkVisible = `CASE document_link.entity_type
	WHEN 'ci' THEN EXISTS (SELECT 1 FROM ci WHERE ci.id = document_link.entity_id)
	WHEN 'asset' THEN EXISTS (SELECT 1 FROM asset WHERE asset.id = document_link.entity_id)
	WHEN 'contact' THEN EXISTS (SELECT 1 FROM contact WHERE contact.id = document_link.entity_id)
	WHEN 'ticket' THEN EXISTS (SELECT 1 FROM ticket WHERE ticket.id = document_link.entity_id)
	WHEN 'location' THEN EXISTS (SELECT 1 FROM location_node WHERE location_node.id = document_link.entity_id)
	ELSE true END`
	documentVisible = "(NOT EXISTS (SELECT 1 FROM document_link WHERE document_link.document_id = document.id)" +
		" OR EXISTS (SELECT 1 FROM document_link WHERE document_link.document_id = document.id AND " + linkVisible + "))"
)

// requireVisible fails with "not found" when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return fmt.Errorf("not found")
	}
	return nil
}

// List returns paginated documents filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Document, int, error) {
	var docs []Document
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"true"}
		args := make([]any, 0, 4)
		argPos := 1

		if filter.Category != "" {
			whereParts = append(whereParts, fmt.Sprintf("category = $%d", argPos))
			args = append(args, filter.Category)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("(title ILIKE $%d OR file_name ILIKE $%d)", argPos, argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM document WHERE "+documentVisible+" AND "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count documents: %w", err)
		}

		sortColumn, sortDirection := NormalizeSort(filter)

		// Keyset pagination: when a cursor is supplied it replaces the OFFSET
		// so page boundaries stay stable while rows are inserted or removed.
		listWhere := whereClause
		listArgs := append([]any{}, args...)
		if page.Cursor != nil {
			if !page.Cursor.Matches(sortColumn, sortDirection) {
				return fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
			}
			listWhere += " AND " + api.KeysetClause(sortColumn, sortColumnCasts[sortColumn], sortDirection, argPos)
			listArgs = append(listArgs, page.Cursor.Value, page.Cursor.ID)
			argPos += 2
		}

		var query string
		if page.Cursor != nil {
			listArgs = append(listArgs, page.Limit)
			query = fmt.Sprintf(
				"SELECT %s FROM document WHERE "+documentVisible+" AND %s ORDER BY %s %s, id %s LIMIT $%d",
				documentSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM document WHERE "+documentVisible+" AND %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				documentSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list documents: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanDocument(rows)
			if err != nil {
				return fmt.Errorf("scan document: %w", err)
			}
			docs = append(docs, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate documents: %w", err)
		}
		return nil
	})

	return docs, total, err
}

// GetByID retrieves a single document by ID within the tenant scope.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Document, error) {
	var doc *Document

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM document WHERE "+documentVisible+" AND id = $1 AND organization_id = $2", documentSelectColumns)
		var err error
		doc, err = scanDocument(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get document by id: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// Create inserts a new document. It has no stored content yet: the storage
// key stays empty until SetStorage, whatever d.StorageKey holds.
func (r *PGRepository) Create(ctx context.Context, d *Document) error {
	return database.WithRequestTenant(ctx, r.pool, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if d.Tags == nil {
			d.Tags = []string{}
		}
		d.StorageKey = ""
		query := `
			INSERT INTO document (
				organization_id, title, description, file_name, file_size, mime_type,
				storage_key, version, category, tags, uploaded_by
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, COALESCE(NULLIF($8, 0), 1), $9, $10, $11
			)
			RETURNING id::text, version, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			d.OrganizationID,
			d.Title,
			nilIfEmpty(d.Description),
			d.FileName,
			d.FileSize,
			d.MimeType,
			d.StorageKey,
			d.Version,
			d.Category,
			d.Tags,
			d.UploadedBy,
		).Scan(&d.ID, &d.Version, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return fmt.Errorf("create document: %w", err)
		}
		d.CreatedAt = d.CreatedAt.UTC()
		d.UpdatedAt = d.UpdatedAt.UTC()
		return nil
	})
}

// Update modifies an existing document.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Document, error) {
	var doc *Document

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 5)
		args := []any{id}
		argPos := 2

		if req.Title != nil {
			setClauses = append(setClauses, fmt.Sprintf("title = $%d", argPos))
			args = append(args, *req.Title)
			argPos++
		}
		if req.Description != nil {
			setClauses = append(setClauses, fmt.Sprintf("description = $%d", argPos))
			args = append(args, nilIfEmpty(*req.Description))
			argPos++
		}
		if req.Category != nil {
			setClauses = append(setClauses, fmt.Sprintf("category = $%d", argPos))
			args = append(args, *req.Category)
			argPos++
		}
		if req.Tags != nil {
			setClauses = append(setClauses, fmt.Sprintf("tags = $%d", argPos))
			args = append(args, req.Tags)
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM document WHERE "+documentVisible+" AND id = $1 AND organization_id = $2", documentSelectColumns)
			var err error
			doc, err = scanDocument(tx.QueryRow(ctx, query, id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return fmt.Errorf("get document for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		// Redundant with row-level security, kept as defense in depth.
		query := fmt.Sprintf("UPDATE document SET %s WHERE "+documentVisible+" AND id = $1 AND organization_id = $%d RETURNING %s", strings.Join(setClauses, ", "), argPos, documentSelectColumns)
		args = append(args, orgID)
		var err error
		doc, err = scanDocument(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update document: %w", err)
		}
		return nil
	})

	return doc, err
}

// SetStorage stores the blob location after an upload and returns the
// updated document. The storage key is always server-generated
// (org/<org>/documents/<id>/<version>) so a client can never point a document
// at another tenant's object; keys outside the organization's prefix are
// rejected here and by the CHECK constraint of migration 000066.
func (r *PGRepository) SetStorage(ctx context.Context, orgID, id, storageKey, mimeType string, size int64) (*Document, error) {
	if err := blob.CheckOrgKey(orgID, storageKey); err != nil {
		return nil, err
	}
	var doc *Document
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE document
			SET storage_key = $2, mime_type = $3, file_size = $4, updated_at = NOW()
			WHERE id = $1 AND organization_id = $5
			RETURNING `+documentSelectColumns, id, storageKey, mimeType, size, orgID)
		var err error
		doc, err = scanDocument(row)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return doc, err
}

// Delete deletes a document. The document table has no deleted_at column, so this is a hard delete.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM document WHERE "+documentVisible+" AND id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete document: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// LinkDocument creates a link between a document and an entity.
func (r *PGRepository) LinkDocument(ctx context.Context, link *DocumentLink) error {
	return database.WithRequestTenant(ctx, r.pool, link.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO document_link (organization_id, document_id, entity_type, entity_id)
			VALUES ($1, $2, $3, $4)
			RETURNING id::text, created_at
		`
		if err := tx.QueryRow(ctx, query,
			link.OrganizationID,
			link.DocumentID,
			link.EntityType,
			link.EntityID,
		).Scan(&link.ID, &link.CreatedAt); err != nil {
			return fmt.Errorf("link document: %w", err)
		}
		link.CreatedAt = link.CreatedAt.UTC()
		// Document and linked object must both be visible under the scope.
		if err := requireVisible(ctx, tx, "document", documentVisible, link.DocumentID); err != nil {
			return err
		}
		return requireVisible(ctx, tx, "document_link", linkVisible, link.ID)
	})
}

// GetLinks returns all entity links for a document.
func (r *PGRepository) GetLinks(ctx context.Context, orgID, docID string) ([]DocumentLink, error) {
	var links []DocumentLink

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM document_link WHERE "+linkVisible+" AND document_id = $1 ORDER BY created_at DESC", documentLinkSelectColumns)
		rows, err := tx.Query(ctx, query, docID)
		if err != nil {
			return fmt.Errorf("get document links: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			link, err := scanDocumentLink(rows)
			if err != nil {
				return fmt.Errorf("scan document link: %w", err)
			}
			links = append(links, *link)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate document links: %w", err)
		}
		return nil
	})

	return links, err
}

// GetLinksForEntity returns documents linked to an entity.
func (r *PGRepository) GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string) ([]Document, error) {
	var docs []Document

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf(`
			SELECT %s
			FROM document d
			JOIN document_link l ON l.document_id = d.id
			WHERE l.entity_type = $1 AND l.entity_id = $2
			  AND EXISTS (SELECT 1 FROM document_link WHERE document_link.id = l.id AND `+linkVisible+`)
			ORDER BY d.created_at DESC
		`, documentSelectColumnsWithAlias)
		rows, err := tx.Query(ctx, query, entityType, entityID)
		if err != nil {
			return fmt.Errorf("get documents for entity: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			doc, err := scanDocument(rows)
			if err != nil {
				return fmt.Errorf("scan linked document: %w", err)
			}
			docs = append(docs, *doc)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate linked documents: %w", err)
		}
		return nil
	})

	return docs, err
}

type documentScanner interface {
	Scan(dest ...any) error
}

func scanDocument(scanner documentScanner) (*Document, error) {
	item := &Document{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.Title,
		&item.Description,
		&item.FileName,
		&item.FileSize,
		&item.MimeType,
		&item.StorageKey,
		&item.Version,
		&item.Category,
		&item.Tags,
		&item.UploadedBy,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

func scanDocumentLink(scanner documentScanner) (*DocumentLink, error) {
	link := &DocumentLink{}
	if err := scanner.Scan(
		&link.ID,
		&link.OrganizationID,
		&link.DocumentID,
		&link.EntityType,
		&link.EntityID,
		&link.CreatedAt,
	); err != nil {
		return nil, err
	}
	link.CreatedAt = link.CreatedAt.UTC()
	return link, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
