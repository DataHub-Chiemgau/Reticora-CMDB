package ticket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
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
	` + ticketTagsColumn + `,
	created_at,
	updated_at
`

// ticketTagsColumn are the sorted keys of the catalog tags of a ticket
// (ticket_tag, WP-764, E-127), {} without tags.
const ticketTagsColumn = `COALESCE((SELECT array_agg(tg.key ORDER BY tg.key)
		FROM ticket_tag tt JOIN tag tg ON tg.id = tt.tag_id WHERE tt.ticket_id = ticket.id), '{}'::text[])`

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
	pool   *pgxpool.Pool
	outbox *events.Outbox
}

// NewPGRepository creates a new PostgreSQL-backed ticket repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// WithEvents makes every ticket mutation (creation, change, comment,
// deletion) write one ticket.updated into the outbox of its transaction
// (EVT-03, WP-290).
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

// Ticket changes named in the change field of ticket.updated.
const (
	changeCreated = "created"
	changeUpdated = "updated"
	changeStatus  = "status"
	changeComment = "comment"
	changeDeleted = "deleted"
	changeLink    = "link"
	changeUnlink  = "unlink"
)

// ticketEvent is the data of ticket.updated: the frame of WP-246 (id,
// client_id and site_id of the ticket for the webhook scope) and the
// change. It names changed fields but carries no field values and no
// comment content.
type ticketEvent struct {
	ID            string   `json:"id"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	Change        string   `json:"change"`
	ChangedFields []string `json:"changed_fields,omitempty"`
	StatusFrom    string   `json:"status_from,omitempty"`
	StatusTo      string   `json:"status_to,omitempty"`
	CommentID     string   `json:"comment_id,omitempty"`
	IsInternal    *bool    `json:"is_internal,omitempty"`
	// LinkID, EntityType and EntityID name the link of a change link or
	// unlink (WP-764).
	LinkID     string `json:"link_id,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
	EntityID   string `json:"entity_id,omitempty"`
}

// recordEvent writes ticket.updated of a ticket mutation in tx as the last
// write of the transaction: in a context transaction right before its
// commit (database.BeforeCommit, K20, pattern lastInTx of the CI
// repository), else at once. Without an outbox nothing is written.
func (r *PGRepository) recordEvent(ctx context.Context, tx pgx.Tx, orgID string, data *ticketEvent) error {
	if r.outbox == nil {
		return nil
	}
	write := func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.TicketUpdated, EntityID: data.ID, Data: data})
		return err
	}
	if !database.InTx(ctx) {
		return write()
	}
	return database.BeforeCommit(ctx, write)
}

// Tickets carry the client and site of their related CI or asset
// (derive_ticket_scope, 000064) and a team; the policy
// ticket_tenant_isolation filters them by client, site and team scope
// (000065). ticketVisible additionally requires the related CI and asset to
// be visible under the transaction's tenant scope, whose policies filter the
// subqueries; comments follow their ticket.
const (
	ticketVisible = `(ticket.related_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = ticket.related_ci_id))
	AND (ticket.related_asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = ticket.related_asset_id))`
	commentVisible = "EXISTS (SELECT 1 FROM ticket WHERE ticket.id = ticket_comment.ticket_id AND " + ticketVisible + ")"
)

// requireVisible fails with ErrNotFound when the written row of table does
// not satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// requireRefs fails with a 422 field error when an object a ticket names
// (assignee, team, related CI or asset; nil: not written) is no UUID or not
// visible under the caller's scope. The foreign keys of ticket are checked
// without row level security: an object of another organization or client
// passed them, an unknown one failed with the constraint text, so the two
// could be told apart (TEN-04, API-03, F-047). The check runs before the
// write, in its transaction.
func requireRefs(ctx context.Context, tx pgx.Tx, orgID string, assigneeID, teamID, ciID, assetID *string) error {
	for _, ref := range []struct {
		table, field, missing string
		id                    *string
	}{
		{"app_user", "assignee_id", "user not found", assigneeID},
		{"team", "team_id", "team not found", teamID},
		{"ci", "related_ci_id", "CI not found", ciID},
		{"asset", "related_asset_id", "asset not found", assetID},
	} {
		if ref.id == nil || nilIfEmpty(*ref.id) == nil {
			continue
		}
		if err := api.RequireUUID(ref.field, *ref.id); err != nil {
			return err
		}
		var visible bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+ref.table+" WHERE id = $1 AND organization_id = $2)",
			*ref.id, orgID).Scan(&visible); err != nil {
			return fmt.Errorf("check %s of ticket: %w", ref.field, err)
		}
		if !visible {
			return &api.FieldError{Field: ref.field, Message: ref.missing, Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Ticket, int, error) {
	items := make([]Ticket, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
		addFilter("reporter_id", filter.ReporterID)
		if filter.TeamMemberOf != "" {
			// team_id=mine (E-143): the teams the principal is a member
			// of, read under the RLS of team_member.
			whereParts = append(whereParts, fmt.Sprintf(
				"EXISTS (SELECT 1 FROM team_member tm WHERE tm.team_id = ticket.team_id AND tm.user_id = $%d)", argPos))
			args = append(args, filter.TeamMemberOf)
			argPos++
		} else {
			addFilter("team_id", filter.TeamID)
		}
		// tags: every key (WP-764).
		for _, key := range filter.Tags {
			whereParts = append(whereParts, fmt.Sprintf(
				"EXISTS (SELECT 1 FROM ticket_tag tt JOIN tag tg ON tg.id = tt.tag_id WHERE tt.ticket_id = ticket.id AND tg.key = $%d)", argPos))
			args = append(args, key)
			argPos++
		}
		if filter.Search != "" {
			whereParts = append(whereParts, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d)", argPos, argPos))
			args = append(args, "%"+filter.Search+"%")
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ticket WHERE "+ticketVisible+" AND "+whereClause, args...).Scan(&total); err != nil {
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
				"SELECT %s FROM ticket WHERE "+ticketVisible+" AND %s ORDER BY %s %s, id %s LIMIT $%d",
				ticketSelectColumns, listWhere, sortColumn, strings.ToUpper(sortDirection),
				strings.ToUpper(sortDirection), argPos,
			)
		} else {
			listArgs = append(listArgs, page.Limit, page.Offset)
			query = fmt.Sprintf(
				"SELECT %s FROM ticket WHERE "+ticketVisible+" AND %s ORDER BY %s %s, id %s LIMIT $%d OFFSET $%d",
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

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM ticket WHERE "+ticketVisible+" AND id = $1 AND organization_id = $2", ticketSelectColumns)
		var err error
		item, err = scanTicket(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
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

// Create writes a new ticket. Its status is new (default, REST) or open
// (workflow); the database assigns the number of its organization
// (ticket_number_assign, 000214).
func (r *PGRepository) Create(ctx context.Context, t *Ticket) error {
	if t.Status == "" {
		t.Status = StatusNew
	}
	if err := CheckInitialStatus(t.Status); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		tags := NormalizeTags(t.Tags)
		if err := requireRefs(ctx, tx, t.OrganizationID, &t.AssigneeID, &t.TeamID, &t.RelatedCIID, &t.RelatedAssetID); err != nil {
			return err
		}
		if err := requireCatalogTags(ctx, tx, t.OrganizationID, tags); err != nil {
			return err
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
				due_date
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12
			)
			RETURNING id::text, ticket_number, created_at, updated_at,
				COALESCE(client_id::text, ''), COALESCE(site_id::text, '')
		`
		dueDate, err := parseOptionalTime(t.DueDate)
		if err != nil {
			return fmt.Errorf("parse due_date: %w", err)
		}
		var clientID, siteID string
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
		).Scan(&t.ID, &t.TicketNumber, &t.CreatedAt, &t.UpdatedAt, &clientID, &siteID); err != nil {
			return fmt.Errorf("create ticket: %w", err)
		}
		if err := requireVisible(ctx, tx, "ticket", ticketVisible, t.ID); err != nil {
			return err
		}
		if err := replaceTags(ctx, tx, t.OrganizationID, t.ID, tags); err != nil {
			return err
		}
		t.Tags = tags
		return r.recordEvent(ctx, tx, t.OrganizationID, &ticketEvent{ID: t.ID, ClientID: clientID, SiteID: siteID, Change: changeCreated})
	})
}

// Update changes a ticket. A status change must be a transition of the
// state machine (states.go); the current status is read with FOR UPDATE in
// the write transaction, so concurrent changes see each other's result.
// The same status is no change. Changing to resolved or closed sets
// resolved_at or closed_at; a reopen clears both.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req *UpdateRequest) (*Ticket, error) {
	var item *Ticket

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		before, err := scanTicket(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM ticket WHERE "+ticketVisible+" AND id = $1 AND organization_id = $2 FOR UPDATE", ticketSelectColumns), id, orgID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("get ticket for update: %w", err)
		}
		statusChange := req.Status != nil && *req.Status != before.Status
		if statusChange {
			if transErr := CheckTransition(before.Status, *req.Status); transErr != nil {
				return transErr
			}
		}
		if refErr := requireRefs(ctx, tx, orgID, req.AssigneeID, req.TeamID, req.RelatedCIID, req.RelatedAssetID); refErr != nil {
			return refErr
		}
		// tags replace the catalog tags of the ticket before the UPDATE,
		// whose RETURNING reads them (WP-764).
		tagsChanged := false
		if req.Tags != nil {
			tags := NormalizeTags(req.Tags)
			if tagErr := requireCatalogTags(ctx, tx, orgID, tags); tagErr != nil {
				return tagErr
			}
			if !slices.Equal(tags, before.Tags) {
				if tagErr := replaceTags(ctx, tx, orgID, id, tags); tagErr != nil {
					return tagErr
				}
				tagsChanged = true
			}
		}
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
		if statusChange {
			setClauses = append(setClauses, fmt.Sprintf("status = $%d", argPos))
			args = append(args, *req.Status)
			argPos++
			switch {
			case *req.Status == StatusResolved:
				setClauses = append(setClauses, "resolved_at = NOW()")
			case *req.Status == StatusClosed:
				setClauses = append(setClauses, "closed_at = NOW()")
			case IsReopen(before.Status, *req.Status):
				setClauses = append(setClauses, "resolved_at = NULL", "closed_at = NULL")
			}
		}
		addStringField("priority", req.Priority)
		addStringField("category", req.Category)
		addStringField("assignee_id", req.AssigneeID)
		addStringField("team_id", req.TeamID)
		addStringField("related_ci_id", req.RelatedCIID)
		addStringField("related_asset_id", req.RelatedAssetID)
		if req.DueDate != nil {
			dueDate, parseErr := parseOptionalTime(*req.DueDate)
			if parseErr != nil {
				return fmt.Errorf("parse due_date: %w", parseErr)
			}
			setClauses = append(setClauses, fmt.Sprintf("due_date = $%d", argPos))
			args = append(args, dueDate)
		}

		if len(setClauses) == 0 && !tagsChanged {
			item = before
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf(
			"UPDATE ticket SET %s WHERE "+ticketVisible+" AND id = $1 AND organization_id = $2 RETURNING %s, COALESCE(client_id::text, ''), COALESCE(site_id::text, '')",
			strings.Join(setClauses, ", "),
			ticketSelectColumns,
		)
		var clientID, siteID string
		item, err = scanTicket(tx.QueryRow(ctx, query, args...), &clientID, &siteID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("update ticket: %w", err)
		}
		if visErr := requireVisible(ctx, tx, "ticket", ticketVisible, id); visErr != nil {
			return visErr
		}
		ev := &ticketEvent{ID: id, ClientID: clientID, SiteID: siteID, Change: changeUpdated, ChangedFields: changedFields(before, item, req)}
		if statusChange {
			ev.Change, ev.StatusFrom, ev.StatusTo = changeStatus, before.Status, item.Status
		}
		return r.recordEvent(ctx, tx, orgID, ev)
	})

	return item, err
}

// changedFields names the requested fields whose value differs between
// before and after, in the order of UpdateRequest (ticket.updated carries
// names only, no values).
func changedFields(before, after *Ticket, req *UpdateRequest) []string {
	out := []string{}
	add := func(name string, requested, differs bool) {
		if requested && differs {
			out = append(out, name)
		}
	}
	add("title", req.Title != nil, before.Title != after.Title)
	add("description", req.Description != nil, before.Description != after.Description)
	add("status", req.Status != nil, before.Status != after.Status)
	add("priority", req.Priority != nil, before.Priority != after.Priority)
	add("category", req.Category != nil, before.Category != after.Category)
	add("assignee_id", req.AssigneeID != nil, before.AssigneeID != after.AssigneeID)
	add("team_id", req.TeamID != nil, before.TeamID != after.TeamID)
	add("related_ci_id", req.RelatedCIID != nil, before.RelatedCIID != after.RelatedCIID)
	add("related_asset_id", req.RelatedAssetID != nil, before.RelatedAssetID != after.RelatedAssetID)
	add("due_date", req.DueDate != nil, before.DueDate != after.DueDate)
	add("tags", req.Tags != nil, strings.Join(before.Tags, "\x00") != strings.Join(after.Tags, "\x00"))
	return out
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var clientID, siteID string
		err := tx.QueryRow(ctx, "DELETE FROM ticket WHERE "+ticketVisible+" AND id = $1 AND organization_id = $2 RETURNING COALESCE(client_id::text, ''), COALESCE(site_id::text, '')",
			id, orgID).Scan(&clientID, &siteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete ticket: %w", err)
		}
		return r.recordEvent(ctx, tx, orgID, &ticketEvent{ID: id, ClientID: clientID, SiteID: siteID, Change: changeDeleted})
	})
}

func (r *PGRepository) AddComment(ctx context.Context, c *Comment) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var clientID, siteID string
		err := tx.QueryRow(ctx, "SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM ticket WHERE "+ticketVisible+" AND id = $1 AND organization_id = $2",
			c.TicketID, c.OrganizationID).Scan(&clientID, &siteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("check ticket for comment: %w", err)
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
		if err = tx.QueryRow(ctx, query,
			c.OrganizationID,
			c.TicketID,
			c.AuthorID,
			c.Content,
			c.IsInternal,
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return fmt.Errorf("add ticket comment: %w", err)
		}
		internal := c.IsInternal
		return r.recordEvent(ctx, tx, c.OrganizationID, &ticketEvent{
			ID: c.TicketID, ClientID: clientID, SiteID: siteID, Change: changeComment, CommentID: c.ID, IsInternal: &internal,
		})
	})
}

func (r *PGRepository) ListComments(ctx context.Context, orgID, ticketID string, page api.PaginationParams) ([]Comment, int, error) {
	items := make([]Comment, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{orgID, ticketID}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ticket_comment WHERE "+commentVisible+" AND organization_id = $1 AND ticket_id = $2", args...).Scan(&total); err != nil {
			return fmt.Errorf("count ticket comments: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM ticket_comment WHERE "+commentVisible+" AND organization_id = $1 AND ticket_id = $2 ORDER BY created_at ASC LIMIT $3 OFFSET $4",
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

// ─── Tags (WP-764, E-127) ──────────────────────────────────────────────────────

// requireCatalogTags fails with a 422 field error naming the keys the tag
// catalog of the organization lacks; the tags it finds stay locked (FOR
// SHARE) until the commit, so a merge or delete waits for the write.
func requireCatalogTags(ctx context.Context, tx pgx.Tx, orgID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT key FROM tag WHERE organization_id = $1 AND key = ANY($2) FOR SHARE`, orgID, keys)
	if err != nil {
		return fmt.Errorf("read catalog tags: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read catalog tags: %w", err)
	}
	var unknown []string
	for _, key := range keys {
		if !slices.Contains(found, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		return &api.FieldError{Field: "tags", Status: http.StatusUnprocessableEntity,
			Message: "unknown tags: " + strings.Join(unknown, ", ")}
	}
	return nil
}

// replaceTags makes keys (catalog tags, checked by requireCatalogTags) the
// tags of ticket ticketID: the others are removed, the missing ones added.
// The scope of the rows follows the ticket (trigger ticket_tag_scope).
func replaceTags(ctx context.Context, tx pgx.Tx, orgID, ticketID string, keys []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM ticket_tag tt WHERE tt.ticket_id = $1 AND NOT EXISTS (
			SELECT 1 FROM tag tg WHERE tg.id = tt.tag_id AND tg.organization_id = $2 AND tg.key = ANY($3))`,
		ticketID, orgID, keys); err != nil {
		return fmt.Errorf("replace ticket tags: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_tag (organization_id, ticket_id, tag_id)
		SELECT $1, $2, tg.id FROM tag tg WHERE tg.organization_id = $1 AND tg.key = ANY($3)
		ON CONFLICT (ticket_id, tag_id) DO NOTHING`, orgID, ticketID, keys); err != nil {
		return fmt.Errorf("assign ticket tags: %w", err)
	}
	return nil
}

// ─── Links (WP-764) ────────────────────────────────────────────────────────────

// reviewItemVisible is the predicate visible of reviews/pg_repository.go
// (WP-342): an item with candidate CIs is visible when all of them are, one
// without candidates when its client and site lie in a restricted scope
// (fail-closed, E-08).
const reviewItemVisible = `(CASE WHEN cardinality(COALESCE(review_item.candidate_ci_ids, '{}'::uuid[])) = 0 THEN
	(NULLIF(current_setting('` + database.ClientScopeGUC + `', true), '') IS NULL
	 OR review_item.client_id = ANY (string_to_array(current_setting('` + database.ClientScopeGUC + `', true), ',')::uuid[]))
	AND (NULLIF(current_setting('` + database.SiteScopeGUC + `', true), '') IS NULL
	 OR review_item.site_id = ANY (string_to_array(current_setting('` + database.SiteScopeGUC + `', true), ',')::uuid[]))
ELSE NOT EXISTS (
	SELECT 1 FROM unnest(review_item.candidate_ci_ids) AS candidate(id)
	WHERE NOT EXISTS (SELECT 1 FROM ci WHERE ci.id = candidate.id)) END)`

// linkTarget is an entity type of ticket_link: its target column and the
// predicate that the target with the id expression id is visible under the
// transaction's tenant scope (the policies of the target table filter the
// subquery).
type linkTarget struct {
	column  string
	visible func(id string) string
}

// linkTargets is the registry of the entity types; every key stands in
// ticket_link_entity_type_check (migration 000230) and LinkEntityTypes.
var linkTargets = map[string]linkTarget{
	LinkCI: {"ci_id", func(id string) string {
		return "EXISTS (SELECT 1 FROM ci WHERE ci.id = " + id + ")"
	}},
	LinkAsset: {"asset_id", func(id string) string {
		return "EXISTS (SELECT 1 FROM asset WHERE asset.id = " + id + ")"
	}},
	LinkLocation: {"location_id", func(id string) string {
		return "EXISTS (SELECT 1 FROM location WHERE location.id = " + id + ")"
	}},
	LinkContract: {"contract_id", func(id string) string {
		return "EXISTS (SELECT 1 FROM contract WHERE contract.id = " + id + ")"
	}},
	LinkReviewItem: {"review_item_id", func(id string) string {
		return "EXISTS (SELECT 1 FROM review_item WHERE review_item.id = " + id + " AND " + reviewItemVisible + ")"
	}},
}

// linkVisible is the SQL condition "the row of ticket_link belongs to a
// visible ticket and names a visible target"; an entity type outside the
// registry is never visible.
var linkVisible = func() string {
	var b strings.Builder
	b.WriteString("EXISTS (SELECT 1 FROM ticket WHERE ticket.id = ticket_link.ticket_id AND " + ticketVisible + ")")
	b.WriteString(" AND CASE ticket_link.entity_type")
	for _, entityType := range LinkEntityTypes {
		target := linkTargets[entityType]
		b.WriteString(" WHEN '" + entityType + "' THEN " + target.visible("ticket_link."+target.column))
	}
	b.WriteString(" ELSE false END")
	return b.String()
}()

// linkSelectColumns are the columns scanLink reads.
const linkSelectColumns = `id::text, organization_id::text, ticket_id::text, entity_type,
	COALESCE(ci_id, asset_id, location_id, contract_id, review_item_id)::text, created_at`

func scanLink(row scanner) (*Link, error) {
	l := &Link{}
	if err := row.Scan(&l.ID, &l.OrganizationID, &l.TicketID, &l.EntityType, &l.EntityID, &l.CreatedAt); err != nil {
		return nil, err
	}
	return l, nil
}

// visibleTicketScope reads client and site of a ticket visible under the
// tenant scope, or fails with ErrNotFound.
func visibleTicketScope(ctx context.Context, tx pgx.Tx, orgID, ticketID string) (clientID, siteID string, err error) {
	err = tx.QueryRow(ctx, "SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM ticket WHERE "+ticketVisible+
		" AND id = $1 AND organization_id = $2", ticketID, orgID).Scan(&clientID, &siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("read ticket: %w", err)
	}
	return clientID, siteID, nil
}

// ListLinks implements Repository: the links of a visible ticket whose
// target is visible, in the order created_at, id.
func (r *PGRepository) ListLinks(ctx context.Context, orgID, ticketID string, page api.PaginationParams) ([]Link, int, error) {
	items := make([]Link, 0)
	var total int
	if !api.IsUUID(ticketID) {
		return nil, 0, ErrNotFound
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := visibleTicketScope(ctx, tx, orgID, ticketID); err != nil {
			return err
		}
		where := "ticket_link.organization_id = $1 AND ticket_link.ticket_id = $2 AND " + linkVisible
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ticket_link WHERE "+where, orgID, ticketID).Scan(&total); err != nil {
			return fmt.Errorf("count ticket links: %w", err)
		}
		args := []any{orgID, ticketID}
		if page.Cursor != nil {
			if !page.Cursor.Matches(linkSortColumn, linkSortDir) {
				return fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
			}
			where += " AND " + api.KeysetClause(linkSortColumn, "timestamptz", linkSortDir, 3)
			args = append(args, page.Cursor.Value, page.Cursor.ID)
		}
		args = append(args, page.Limit)
		query := fmt.Sprintf("SELECT %s FROM ticket_link WHERE %s ORDER BY created_at, id LIMIT $%d", linkSelectColumns, where, len(args))
		if page.Cursor == nil {
			args = append(args, page.Offset)
			query += fmt.Sprintf(" OFFSET $%d", len(args))
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list ticket links: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			l, scanErr := scanLink(rows)
			if scanErr != nil {
				return fmt.Errorf("scan ticket link: %w", scanErr)
			}
			items = append(items, *l)
		}
		if err = rows.Err(); err != nil {
			return fmt.Errorf("iterate ticket links: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// CreateLink implements Repository: ticket and target must be visible under
// the tenant scope (else ErrNotFound, no existence leak); the link takes
// client, site and team of the ticket (trigger ticket_link_scope). The
// event ticket.updated with change link is written in the transaction.
func (r *PGRepository) CreateLink(ctx context.Context, link *Link) error {
	target, ok := linkTargets[link.EntityType]
	if !ok {
		return &api.FieldError{Field: "entity_type", Status: http.StatusUnprocessableEntity,
			Message: "must be one of " + strings.Join(LinkEntityTypes, ", ")}
	}
	if !api.IsUUID(link.TicketID) || !api.IsUUID(link.EntityID) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, link.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		clientID, siteID, err := visibleTicketScope(ctx, tx, link.OrganizationID, link.TicketID)
		if err != nil {
			return err
		}
		var visible bool
		if err = tx.QueryRow(ctx, "SELECT "+target.visible("$1::uuid"), link.EntityID).Scan(&visible); err != nil {
			return fmt.Errorf("check link target: %w", err)
		}
		if !visible {
			return ErrNotFound
		}
		err = tx.QueryRow(ctx, `INSERT INTO ticket_link (organization_id, ticket_id, entity_type, `+target.column+`)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (ticket_id, `+target.column+`) DO NOTHING
			RETURNING id::text, created_at`,
			link.OrganizationID, link.TicketID, link.EntityType, link.EntityID).Scan(&link.ID, &link.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLinkExists
		}
		if err != nil {
			return fmt.Errorf("create ticket link: %w", err)
		}
		return r.recordEvent(ctx, tx, link.OrganizationID, &ticketEvent{ID: link.TicketID, ClientID: clientID, SiteID: siteID,
			Change: changeLink, LinkID: link.ID, EntityType: link.EntityType, EntityID: link.EntityID})
	})
}

// DeleteLink implements Repository: it removes a link of a visible ticket
// whose target is visible and that the caller may delete (restrictive
// DELETE policy of ticket_link), with the event ticket.updated with change
// unlink; any other link is ErrNotFound.
func (r *PGRepository) DeleteLink(ctx context.Context, orgID, ticketID, linkID string) error {
	if !api.IsUUID(ticketID) || !api.IsUUID(linkID) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		clientID, siteID, err := visibleTicketScope(ctx, tx, orgID, ticketID)
		if err != nil {
			return err
		}
		ev := &ticketEvent{ID: ticketID, ClientID: clientID, SiteID: siteID, Change: changeUnlink, LinkID: linkID}
		err = tx.QueryRow(ctx, `DELETE FROM ticket_link WHERE id = $1 AND ticket_id = $2 AND organization_id = $3 AND `+linkVisible+`
			RETURNING entity_type, COALESCE(ci_id, asset_id, location_id, contract_id, review_item_id)::text`,
			linkID, ticketID, orgID).Scan(&ev.EntityType, &ev.EntityID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete ticket link: %w", err)
		}
		return r.recordEvent(ctx, tx, orgID, ev)
	})
}

type scanner interface {
	Scan(dest ...any) error
}

// scanTicket reads the columns of ticketSelectColumns, followed by extra
// columns into extra.
func scanTicket(scanner scanner, extra ...any) (*Ticket, error) {
	item := &Ticket{}
	var dueDate sql.NullTime
	var resolvedAt sql.NullTime
	var closedAt sql.NullTime
	dest := []any{
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
	}
	if err := scanner.Scan(append(dest, extra...)...); err != nil {
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

// parseOptionalTime parses due_date; a value that is no RFC 3339 timestamp
// is a 400 field error with a fixed text.
func parseOptionalTime(value string) (any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, &api.FieldError{Field: "due_date", Message: "must be an RFC 3339 timestamp", Status: http.StatusBadRequest}
	}
	return parsed, nil
}
