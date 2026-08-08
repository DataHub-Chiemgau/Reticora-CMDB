package ticket

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ticketSelectColumns = `
	id::text,
	organization_id::text,
	ticket_number,
	title,
	COALESCE(description, ''),
	status,
	priority,
	category,
	reporter_id::text,
	COALESCE(assignee_id::text, ''),
	COALESCE(team_id::text, ''),
	COALESCE(related_ci_id::text, ''),
	COALESCE(related_asset_id::text, ''),
	due_date,
	resolved_at,
	closed_at,
	COALESCE(tags, '{}'::text[]),
	created_at,
	updated_at
`

const ticketCommentSelectColumns = `
	id::text,
	organization_id::text,
	ticket_id::text,
	author_id::text,
	content,
	is_internal,
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed ticket repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// withTenant executes fn within a transaction that has app.org_id set for RLS.
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Ticket, int, error) {
	var items []Ticket
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"organization_id = $1"}
		args := []any{orgID}
		argPos := 2

		addFilter := func(column, value string) {
			if value == "" {
				return
			}
			whereParts = append(whereParts, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, value)
			argPos++
		}

		addFilter("status", filter.Status)
		addFilter("priority", filter.Priority)
		addFilter("category", filter.Category)
		addFilter("assignee_id", filter.AssigneeID)
		addFilter("team_id", filter.TeamID)
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d)", argPos, argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ticket WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count tickets: %w", err)
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
				"SELECT %s FROM ticket WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
				ticketSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM ticket WHERE %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
				ticketSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos, argPos+1,
			)
		}
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list tickets: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanTicket(rows)
			if err != nil {
				return fmt.Errorf("scan ticket: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate tickets: %w", err)
		}
		return nil
	})

	return items, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Ticket, error) {
	var item *Ticket

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ticket WHERE id = $1 AND organization_id = $2", ticketSelectColumns)
		var err error
		item, err = scanTicket(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("ticket not found")
			}
			return fmt.Errorf("get ticket by id: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *PGRepository) Create(ctx context.Context, t *Ticket) error {
	return r.withTenant(ctx, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if t.Tags == nil {
			t.Tags = []string{}
		}
		query := `
			INSERT INTO ticket (
				organization_id,
				title,
				description,
				status,
				priority,
				category,
				reporter_id,
				assignee_id,
				team_id,
				related_ci_id,
				related_asset_id,
				due_date,
				tags
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13
			)
			RETURNING id::text, ticket_number, created_at, updated_at
		`
		dueDate, err := parseOptionalTime(t.DueDate)
		if err != nil {
			return fmt.Errorf("parse due_date: %w", err)
		}
		if err := tx.QueryRow(ctx, query,
			t.OrganizationID,
			t.Title,
			nilIfEmpty(t.Description),
			t.Status,
			t.Priority,
			t.Category,
			t.ReporterID,
			nilIfEmpty(t.AssigneeID),
			nilIfEmpty(t.TeamID),
			nilIfEmpty(t.RelatedCIID),
			nilIfEmpty(t.RelatedAssetID),
			dueDate,
			t.Tags,
		).Scan(&t.ID, &t.TicketNumber, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return fmt.Errorf("create ticket: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Ticket, error) {
	var item *Ticket

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 12)
		args := []any{id, orgID}
		argPos := 3

		addStringField := func(column string, value *string) {
			if value == nil {
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, nilIfEmpty(*value))
			argPos++
		}

		addStringField("title", req.Title)
		addStringField("description", req.Description)
		if req.Status != nil {
			setClauses = append(setClauses, fmt.Sprintf("status = $%d", argPos))
			args = append(args, *req.Status)
			argPos++
			if *req.Status == "resolved" {
				setClauses = append(setClauses, "resolved_at = COALESCE(resolved_at, NOW())")
			}
			if *req.Status == "closed" {
				setClauses = append(setClauses, "closed_at = COALESCE(closed_at, NOW())")
			}
		}
		addStringField("priority", req.Priority)
		addStringField("category", req.Category)
		addStringField("assignee_id", req.AssigneeID)
		addStringField("team_id", req.TeamID)
		addStringField("related_ci_id", req.RelatedCIID)
		addStringField("related_asset_id", req.RelatedAssetID)
		if req.DueDate != nil {
			dueDate, err := parseOptionalTime(*req.DueDate)
			if err != nil {
				return fmt.Errorf("parse due_date: %w", err)
			}
			setClauses = append(setClauses, fmt.Sprintf("due_date = $%d", argPos))
			args = append(args, dueDate)
			argPos++
		}
		if req.Tags != nil {
			setClauses = append(setClauses, fmt.Sprintf("tags = $%d", argPos))
			args = append(args, req.Tags)
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM ticket WHERE id = $1 AND organization_id = $2", ticketSelectColumns)
			var err error
			item, err = scanTicket(tx.QueryRow(ctx, query, id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("ticket not found")
				}
				return fmt.Errorf("get ticket for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf(
			"UPDATE ticket SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(setClauses, ", "),
			ticketSelectColumns,
		)
		var err error
		item, err = scanTicket(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("ticket not found")
			}
			return fmt.Errorf("update ticket: %w", err)
		}
		return nil
	})

	return item, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM ticket WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete ticket: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("ticket not found")
		}
		return nil
	})
}

func (r *PGRepository) AddComment(ctx context.Context, c *Comment) error {
	return r.withTenant(ctx, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ticket WHERE id = $1 AND organization_id = $2)", c.TicketID, c.OrganizationID).Scan(&exists); err != nil {
			return fmt.Errorf("check ticket for comment: %w", err)
		}
		if !exists {
			return fmt.Errorf("ticket not found")
		}

		query := `
			INSERT INTO ticket_comment (
				organization_id,
				ticket_id,
				author_id,
				content,
				is_internal
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			c.OrganizationID,
			c.TicketID,
			c.AuthorID,
			c.Content,
			c.IsInternal,
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return fmt.Errorf("add ticket comment: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) ListComments(ctx context.Context, orgID, ticketID string, page api.PaginationParams) ([]Comment, int, error) {
	var items []Comment
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{orgID, ticketID}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ticket_comment WHERE organization_id = $1 AND ticket_id = $2", args...).Scan(&total); err != nil {
			return fmt.Errorf("count ticket comments: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM ticket_comment WHERE organization_id = $1 AND ticket_id = $2 ORDER BY created_at ASC LIMIT $3 OFFSET $4",
			ticketCommentSelectColumns,
		)
		rows, err := tx.Query(ctx, query, orgID, ticketID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list ticket comments: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanComment(rows)
			if err != nil {
				return fmt.Errorf("scan ticket comment: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate ticket comments: %w", err)
		}
		return nil
	})

	return items, total, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTicket(scanner scanner) (*Ticket, error) {
	item := &Ticket{}
	var dueDate sql.NullTime
	var resolvedAt sql.NullTime
	var closedAt sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.TicketNumber,
		&item.Title,
		&item.Description,
		&item.Status,
		&item.Priority,
		&item.Category,
		&item.ReporterID,
		&item.AssigneeID,
		&item.TeamID,
		&item.RelatedCIID,
		&item.RelatedAssetID,
		&dueDate,
		&resolvedAt,
		&closedAt,
		&item.Tags,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if dueDate.Valid {
		item.DueDate = dueDate.Time.UTC().Format(time.RFC3339)
	}
	if resolvedAt.Valid {
		item.ResolvedAt = resolvedAt.Time.UTC().Format(time.RFC3339)
	}
	if closedAt.Valid {
		item.ClosedAt = closedAt.Time.UTC().Format(time.RFC3339)
	}
	return item, nil
}

func scanComment(scanner scanner) (*Comment, error) {
	item := &Comment{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.TicketID,
		&item.AuthorID,
		&item.Content,
		&item.IsInternal,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func parseOptionalTime(value string) (any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}
