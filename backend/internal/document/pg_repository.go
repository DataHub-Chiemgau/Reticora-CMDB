package document

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	updated_at,
	(SELECT dv.scan_status FROM document_version dv WHERE dv.document_id = document.id AND dv.version = document.version),
	(SELECT dv.scanned_at FROM document_version dv WHERE dv.document_id = document.id AND dv.version = document.version)
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

// A link is visible when its target is visible under the transaction's
// tenant scope (linkVisible, links.go); a document is visible when it is not
// soft-deleted (WP-732) and linked to at least one visible target or not
// linked at all. An unlinked document that had links (document.orphaned,
// migration 000281, SR-56) is visible to org-wide principals only, so
// neither an unlink nor the deletion of a link target makes it org-wide.
// The policies of document and document_link (migrations 000064, 000139,
// 000217, 000281) filter the same rows except the deleted ones, which only
// the repository and the search hide (migration 000221: the retention run
// sees them under its org-wide scope); the predicates keep the repository
// exact without them.
var (
	documentVisible = "(" + documentNotDeleted + " AND ((NOT EXISTS (SELECT 1 FROM document_link WHERE document_link.document_id = document.id)" +
		" AND (NOT document.orphaned OR " + orgWideDocumentSQL + "))" +
		" OR EXISTS (SELECT 1 FROM document_link WHERE document_link.document_id = document.id AND " + linkVisible + ")))"
	// documentWritable admits a document for UPDATE and DELETE (WP-341,
	// migration 000139): a principal restricted in none of the client, site
	// and team scope writes every document; any other only a linked one
	// whose links it sees completely and may write (client, site and team in
	// its scope, migrations 000146 and 000217), so neither a document shared
	// with another client or team or linked to an org-wide object nor an
	// unlinked (org-wide) one is writable (E-09, E-11).
	// A soft-deleted document is not writable either.
	documentWritable = "(" + documentNotDeleted + " AND (" + orgWideDocumentSQL +
		" OR (document.link_count > 0 AND document.link_count = (SELECT count(*) FROM document_link" +
		" WHERE document_link.document_id = document.id AND " + linkVisible + " AND " + database.ScopedWriteSQL +
		" AND " + teamWriteSQL + "))))"
)

// documentNotDeleted is the SQL condition "the document is not
// soft-deleted" (WP-732).
const documentNotDeleted = "document.deleted_at IS NULL"

// VisibleSQL is the SQL condition of GetByID on the table document (not
// soft-deleted, unlinked or linked to a visible target) under the
// transaction's tenant scope: the import runs of a document are read under
// it in the job API (importer.JobReadSQL, WP-828).
func VisibleSQL() string { return documentVisible }

// requireVisible fails with "not found" when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
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

// List returns paginated documents filtered by the given parameters. The
// entity filter (WP-811) first checks that the target is visible under the
// tenant scope (registry of links.go, api.ErrNotFound otherwise) and then
// keeps the documents with a link to it (idx_doc_link_entity); the policies
// of document_link hide the links outside the scope.
func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Document, int, error) {
	var docs []Document
	var total int

	filtered := filter.EntityType != "" || filter.EntityID != ""
	if filtered {
		if err := checkEntityFilter(filter.EntityType, filter.EntityID); err != nil {
			return nil, 0, err
		}
	}

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"true"}
		args := make([]any, 0, 6)
		argPos := 1

		if filtered {
			if err := targetVisible(ctx, tx, &DocumentLink{EntityType: filter.EntityType, EntityID: filter.EntityID}); err != nil {
				return err
			}
			whereParts = append(whereParts, fmt.Sprintf("EXISTS (SELECT 1 FROM document_link WHERE document_link.document_id = document.id"+
				" AND document_link.entity_type = $%d AND document_link.entity_id = $%d::uuid)", argPos, argPos+1))
			args = append(args, filter.EntityType, filter.EntityID)
			argPos += 2
		}
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

// GetWritable retrieves a document the caller may change (documentWritable);
// a document it only reads, shared with another client or org-wide, is "not
// found" like a missing one.
func (r *PGRepository) GetWritable(ctx context.Context, orgID, id string) (*Document, error) {
	var doc *Document
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM document WHERE "+documentWritable+" AND id = $1 AND organization_id = $2", documentSelectColumns)
		var err error
		doc, err = scanDocument(tx.QueryRow(ctx, query, id, orgID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("get writable document: %w", err)
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
	return r.CreateWithLinks(ctx, d, nil)
}

// CreateWithLinks inserts the document and then its links in one
// transaction (E-61 option 1). Every link is checked like LinkDocument:
// an entity type outside the registry fails with a 422 field error before
// the transaction, an invisible or missing target with api.ErrNotFound and
// rolls the document back. For a principal restricted in the client, site
// or team scope the links must also be writable (WITH CHECK of
// document_link), and the deferred trigger document_scoped_insert_linked
// refuses a document left without a link (403). With links the
// transaction ends with the entries document.created and one
// document.linked per link (WP-851, recordCreateAudit); without links the
// route audit records the creation.
func (r *PGRepository) CreateWithLinks(ctx context.Context, d *Document, links []DocumentLink) error {
	for i := range links {
		if err := checkEntityType(links[i].EntityType); err != nil {
			return err
		}
		if !api.IsUUID(links[i].EntityID) {
			return errInvalidEntityID
		}
	}
	return database.WithRequestTenant(ctx, r.pool, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := insertDocument(ctx, tx, d); err != nil {
			return err
		}
		for i := range links {
			link := &links[i]
			link.OrganizationID, link.DocumentID = d.OrganizationID, d.ID
			if err := targetVisible(ctx, tx, link); err != nil {
				return err
			}
			if err := insertLink(ctx, tx, link); err != nil {
				return err
			}
			if err := requireVisible(ctx, tx, "document_link", linkVisible, link.ID); err != nil {
				return err
			}
		}
		if len(links) == 0 {
			return nil
		}
		return recordCreateAudit(ctx, tx, d, links)
	})
}

// insertDocument inserts d in tx and sets its id, version and timestamps.
func insertDocument(ctx context.Context, tx pgx.Tx, d *Document) error {
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
}

// insertLink inserts link in tx and sets its id and creation time; the
// trigger derive_document_link_scope sets client, site and team.
func insertLink(ctx context.Context, tx pgx.Tx, link *DocumentLink) error {
	if err := tx.QueryRow(ctx, `
		INSERT INTO document_link (organization_id, document_id, entity_type, entity_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, created_at`,
		link.OrganizationID, link.DocumentID, link.EntityType, link.EntityID,
	).Scan(&link.ID, &link.CreatedAt); err != nil {
		return fmt.Errorf("link document: %w", err)
	}
	link.CreatedAt = link.CreatedAt.UTC()
	return nil
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
			query := fmt.Sprintf("SELECT %s FROM document WHERE "+documentWritable+" AND id = $1 AND organization_id = $2", documentSelectColumns)
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
		query := fmt.Sprintf("UPDATE document SET %s WHERE "+documentWritable+" AND id = $1 AND organization_id = $%d RETURNING %s", strings.Join(setClauses, ", "), argPos, documentSelectColumns)
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
// updated document; like Update it changes only a writable document
// (documentWritable). The storage key is always server-generated
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
			WHERE `+documentWritable+` AND id = $1 AND organization_id = $5
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

// CreateVersion stores v as the next version of a writable document in one
// transaction (WP-731, K20: the blob is already stored, no lock is held
// during the transfer). It locks the document, adds v.Size to the usage of
// the organization only within limit (the UPDATE on organization_usage
// serializes parallel uploads of the organization, so exactly the uploads
// that fit succeed), numbers v after the highest version, inserts it,
// mirrors it in the document and records document.version_created last.
// The usage row is counted org-wide: its policy checks only app.org_id, so
// a scoped caller cannot hide stored bytes from the limit.
func (r *PGRepository) CreateVersion(ctx context.Context, v *Version, limit *int64) (*Document, error) {
	if err := blob.CheckOrgKey(v.OrganizationID, v.StorageKey); err != nil {
		return nil, err
	}
	if !api.IsUUID(v.DocumentID) {
		return nil, api.ErrNotFound
	}
	var doc *Document
	err := database.WithRequestTenant(ctx, r.pool, v.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var locked string
		lockErr := tx.QueryRow(ctx, "SELECT id::text FROM document WHERE "+documentWritable+" AND id = $1 AND organization_id = $2 FOR UPDATE",
			v.DocumentID, v.OrganizationID).Scan(&locked)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if lockErr != nil {
			return fmt.Errorf("lock document: %w", lockErr)
		}
		if err := AddVersionTx(ctx, tx, v, limit); err != nil {
			return err
		}
		var err error
		if doc, err = scanDocument(tx.QueryRow(ctx, `
			UPDATE document
			SET version = $2, storage_key = $3, file_size = $4, mime_type = $5, updated_at = NOW()
			WHERE id = $1 AND organization_id = $6
			RETURNING `+documentSelectColumns, v.DocumentID, v.Version, v.StorageKey, v.Size, v.MimeType, v.OrganizationID)); err != nil {
			return fmt.Errorf("mirror document version: %w", err)
		}
		return recordVersionAudit(ctx, tx, v)
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// addStorageUsage adds size bytes to the usage of the organization when the
// sum stays within limit (nil: unlimited); otherwise it fails with
// *entitlement.LimitExceededError naming the usage.
func addStorageUsage(ctx context.Context, tx pgx.Tx, orgID string, size int64, limit *int64) error {
	if _, err := tx.Exec(ctx, `INSERT INTO organization_usage (organization_id) VALUES ($1) ON CONFLICT DO NOTHING`, orgID); err != nil {
		return fmt.Errorf("create storage usage: %w", err)
	}
	var used int64
	err := tx.QueryRow(ctx, `
		UPDATE organization_usage SET storage_bytes = storage_bytes + $2, updated_at = NOW()
		WHERE organization_id = $1 AND ($3::bigint IS NULL OR storage_bytes + $2 <= $3)
		RETURNING storage_bytes`, orgID, size, limit).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.QueryRow(ctx, `SELECT storage_bytes FROM organization_usage WHERE organization_id = $1`, orgID).Scan(&used); err != nil {
			return fmt.Errorf("read storage usage: %w", err)
		}
		return storageLimitError(*limit, used)
	}
	if err != nil {
		return fmt.Errorf("count storage usage: %w", err)
	}
	return nil
}

const documentVersionSelectColumns = `
	id::text,
	organization_id::text,
	document_id::text,
	version,
	storage_key,
	size,
	mime,
	checksum_sha256,
	scan_status,
	scanned_at,
	COALESCE(uploaded_by_surrogate::text, ''),
	created_at
`

// ListVersions returns a page of the versions of a visible document,
// highest first.
func (r *PGRepository) ListVersions(ctx context.Context, orgID, docID string, page api.PaginationParams) ([]Version, int, error) {
	if !api.IsUUID(docID) {
		return nil, 0, api.ErrNotFound
	}
	after := 0
	if page.Cursor != nil {
		var err error
		if after, err = versionCursorAfter(page.Cursor); err != nil {
			return nil, 0, err
		}
	}
	var out []Version
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisible(ctx, tx, "document", documentVisible, docID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM document_version WHERE document_id = $1`, docID).Scan(&total); err != nil {
			return fmt.Errorf("count document versions: %w", err)
		}
		query := "SELECT " + documentVersionSelectColumns + " FROM document_version WHERE document_id = $1"
		args := []any{docID, page.Limit}
		if page.Cursor != nil {
			query += " AND version < $3 ORDER BY version DESC LIMIT $2"
			args = append(args, after)
		} else {
			query += " ORDER BY version DESC LIMIT $2 OFFSET $3"
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list document versions: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) {
			v, scanErr := scanVersion(row)
			if scanErr != nil {
				return Version{}, scanErr
			}
			return *v, nil
		})
		if err != nil {
			return fmt.Errorf("scan document versions: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetVersion returns version n of a visible document.
func (r *PGRepository) GetVersion(ctx context.Context, orgID, docID string, n int) (*Version, error) {
	if !api.IsUUID(docID) {
		return nil, api.ErrNotFound
	}
	var v *Version
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireVisible(ctx, tx, "document", documentVisible, docID); err != nil {
			return err
		}
		var err error
		v, err = scanVersion(tx.QueryRow(ctx, "SELECT "+documentVersionSelectColumns+
			" FROM document_version WHERE document_id = $1 AND version = $2", docID, n))
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get document version: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// StorageUsage returns the stored bytes of the organization from
// organization_usage (no count over the versions); its policy checks only
// the organization, so every caller gets the org-wide value.
func (r *PGRepository) StorageUsage(ctx context.Context, orgID string) (int64, error) {
	var used int64
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT storage_bytes FROM organization_usage WHERE organization_id = $1), 0)`,
			orgID).Scan(&used); err != nil {
			return fmt.Errorf("read storage usage: %w", err)
		}
		return nil
	})
	return used, err
}

// SetScanResult stores the scan result of version n of the document
// (WP-733): status, scanned_at (now, NULL for pending_scan) and the
// signature name of a finding in scan_signature. It joins a transaction of
// the context (database.WithRequestTenant), so the scan job (WP-734) writes
// status, audit and event in one transaction. The document is not checked
// for visibility beyond the policies of document_version: the job runs
// org-wide. A missing version fails with api.ErrNotFound.
func (r *PGRepository) SetScanResult(ctx context.Context, orgID, documentID string, n int, status, signature string) error {
	if !validScanStatus(status) {
		return errInvalidScanStatus
	}
	if !api.IsUUID(documentID) {
		return api.ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE document_version
			   SET scan_status = $4,
			       scanned_at = CASE WHEN $4 = 'pending_scan' THEN NULL ELSE now() END,
			       scan_signature = NULLIF($5, '')
			 WHERE organization_id = $1 AND document_id = $2 AND version = $3`,
			orgID, documentID, n, status, signature)
		if err != nil {
			return fmt.Errorf("set document scan result: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return api.ErrNotFound
		}
		return nil
	})
}

// PendingScans returns up to limit open versions (pending_scan,
// scan_failed) of the organization, oldest first. The predicate
// scan_status <> 'clean' lets the planner use the partial index
// idx_document_version_scan_open (migration 000222); versions of
// soft-deleted documents are left out.
func (r *PGRepository) PendingScans(ctx context.Context, orgID string, limit int) ([]Version, error) {
	var out []Version
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+documentVersionSelectColumns+` FROM document_version
			WHERE organization_id = $1 AND scan_status <> 'clean' AND scan_status IN ('pending_scan', 'scan_failed')
			  AND NOT EXISTS (SELECT 1 FROM document WHERE document.id = document_version.document_id AND document.deleted_at IS NOT NULL)
			ORDER BY created_at, id LIMIT $2`, orgID, max(limit, 0))
		if err != nil {
			return fmt.Errorf("list pending document scans: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) {
			v, scanErr := scanVersion(row)
			if scanErr != nil {
				return Version{}, scanErr
			}
			return *v, nil
		})
		if err != nil {
			return fmt.Errorf("scan pending document scans: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanVersion(scanner documentScanner) (*Version, error) {
	v := &Version{}
	if err := scanner.Scan(&v.ID, &v.OrganizationID, &v.DocumentID, &v.Version, &v.StorageKey, &v.Size, &v.MimeType,
		&v.ChecksumSHA256, &v.ScanStatus, &v.ScannedAt, &v.UploadedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.CreatedAt = v.CreatedAt.UTC()
	if v.ScannedAt != nil {
		t := v.ScannedAt.UTC()
		v.ScannedAt = &t
	}
	return v, nil
}

// uuidOrEmpty returns s when it is a UUID, else "" (stored as NULL): the
// surrogate of a version names a user only by a valid id.
func uuidOrEmpty(s string) string {
	if api.IsUUID(s) {
		return s
	}
	return ""
}

// Delete soft-deletes a writable document (WP-732): it sets deleted_at, so
// every read and write of the repository and the search treat it as
// missing, and a second call fails with "not found". Versions, links, blobs
// and the storage usage stay until the retention run retention.soft_deleted
// purges the document after the soft-delete period. The audit entry
// document.deleted is the last step of the transaction.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return fmt.Errorf("not found")
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var title string
		var deletedAt time.Time
		err := tx.QueryRow(ctx, "UPDATE document SET deleted_at = NOW() WHERE "+documentWritable+
			" AND id = $1 AND organization_id = $2 RETURNING title, deleted_at", id, orgID).Scan(&title, &deletedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("delete document: %w", err)
		}
		return recordDeleteAudit(ctx, tx, orgID, id, title, deletedAt)
	})
}

// LinkDocument creates a link between a document and an entity. The entity
// type must stand in the registry (422) and the target must be visible under
// the tenant scope (api.ErrNotFound), both checked before the INSERT; the
// audit entry document.linked is the last step of the transaction.
func (r *PGRepository) LinkDocument(ctx context.Context, link *DocumentLink) error {
	if err := checkEntityType(link.EntityType); err != nil {
		return err
	}
	if !api.IsUUID(link.EntityID) {
		return errInvalidEntityID
	}
	return database.WithRequestTenant(ctx, r.pool, link.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := targetVisible(ctx, tx, link); err != nil {
			return err
		}
		if err := insertLink(ctx, tx, link); err != nil {
			return err
		}
		// Document and linked object must both be visible under the scope.
		if err := requireVisible(ctx, tx, "document", documentVisible, link.DocumentID); err != nil {
			return err
		}
		if err := refuseQuarantined(ctx, tx, link.DocumentID); err != nil {
			return err
		}
		if err := requireVisible(ctx, tx, "document_link", linkVisible, link.ID); err != nil {
			return err
		}
		return recordLinkAudit(ctx, tx, ActionLinked, link)
	})
}

// refuseQuarantined fails with ErrQuarantined when the current version of
// the document is infected (WP-733, E-42): a quarantined document gets no
// new link; its existing links stay, since removing them would widen its
// visibility to the whole organization (000064, 000139).
func refuseQuarantined(ctx context.Context, tx pgx.Tx, docID string) error {
	var infected bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document d JOIN document_version dv
		  ON dv.document_id = d.id AND dv.version = d.version
		 WHERE d.id = $1 AND dv.scan_status = 'infected')`, docID).Scan(&infected); err != nil {
		return fmt.Errorf("check document quarantine: %w", err)
	}
	if infected {
		return ErrQuarantined
	}
	return nil
}

// UnlinkDocument removes link linkID of document docID when the link is
// visible under the tenant scope and the caller may delete it (the
// restrictive DELETE policy of document_link: client, site and team in its
// scope); otherwise it fails with api.ErrNotFound. The audit entry
// document.unlinked is the last step of the transaction.
func (r *PGRepository) UnlinkDocument(ctx context.Context, orgID, docID, linkID string) error {
	if !api.IsUUID(docID) || !api.IsUUID(linkID) {
		return api.ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		link := &DocumentLink{ID: linkID, OrganizationID: orgID, DocumentID: docID}
		err := tx.QueryRow(ctx, `
			DELETE FROM document_link
			WHERE id = $1 AND document_id = $2 AND organization_id = $3 AND `+linkVisible+`
			  AND EXISTS (SELECT 1 FROM document WHERE document.id = $2 AND `+documentNotDeleted+`)
			RETURNING entity_type, entity_id::text`, linkID, docID, orgID).Scan(&link.EntityType, &link.EntityID)
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("unlink document: %w", err)
		}
		return recordLinkAudit(ctx, tx, ActionUnlinked, link)
	})
}

// GetLinks returns the visible entity links of a document; a soft-deleted
// document fails with api.ErrNotFound (WP-732).
func (r *PGRepository) GetLinks(ctx context.Context, orgID, docID string) ([]DocumentLink, error) {
	var links []DocumentLink

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document WHERE id = $1 AND deleted_at IS NOT NULL)`, docID).Scan(&deleted); err != nil {
			return fmt.Errorf("get document links: %w", err)
		}
		if deleted {
			return api.ErrNotFound
		}
		query := fmt.Sprintf("SELECT %s FROM document_link WHERE "+linkVisible+" AND document_id = $1 ORDER BY created_at DESC, id DESC", documentLinkSelectColumns)
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

// GetLinksForEntity returns a page of the documents linked to an entity,
// newest first on (created_at, id) (api.NewestFirstSQL: at most one more
// than the limit), and their number. A document counts when it is not
// soft-deleted and has a link to the entity that is visible under the
// transaction's tenant scope (linkVisible, the target check of WP-267);
// the link is checked with EXISTS, so the keyset predicate names the
// columns of document unqualified. A cursor of another order is
// api.ErrInvalidCursor.
func (r *PGRepository) GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]Document, int, error) {
	var docs []Document
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		linked := `document.organization_id = $1 AND ` + documentNotDeleted + `
			AND EXISTS (SELECT 1 FROM document_link WHERE document_link.document_id = document.id
				AND document_link.entity_type = $2 AND document_link.entity_id = $3 AND ` + linkVisible + `)`
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM document WHERE "+linked, orgID, entityType, entityID).Scan(&total); err != nil {
			return fmt.Errorf("count documents for entity: %w", err)
		}
		where, tail, args, err := api.NewestFirstSQL(page, 4)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+documentSelectColumns+" FROM document WHERE "+linked+" AND "+where+" "+tail,
			append([]any{orgID, entityType, entityID}, args...)...)
		if err != nil {
			return fmt.Errorf("get documents for entity: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			doc, scanErr := scanDocument(rows)
			if scanErr != nil {
				return fmt.Errorf("scan linked document: %w", scanErr)
			}
			docs = append(docs, *doc)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate linked documents: %w", err)
		}
		return nil
	})

	return docs, total, err
}

type documentScanner interface {
	Scan(dest ...any) error
}

// scanDocument scans the columns of documentSelectColumns; scan status and
// scanned_at are those of the current version (WP-733), NULL without one.
func scanDocument(scanner documentScanner) (*Document, error) {
	item := &Document{}
	var scanStatus *string
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
		&scanStatus,
		&item.ScannedAt,
	); err != nil {
		return nil, err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if scanStatus != nil {
		item.ScanStatus = *scanStatus
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if item.ScannedAt != nil {
		at := item.ScannedAt.UTC()
		item.ScannedAt = &at
	}
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
