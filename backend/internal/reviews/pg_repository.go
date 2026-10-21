package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository stores review items in review_item under RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates the PostgreSQL repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// visible: an item with candidate CIs is visible when all of them are
// visible under the transaction's tenant scope. An item without candidates
// is visible only when its client and site match a scope restricted in that
// dimension (fail-closed, E-08): one without a client (or site) is for
// org-wide principals only. The policies of migration 000140 enforce the
// same.
const visible = `(CASE WHEN cardinality(COALESCE(review_item.candidate_ci_ids, '{}'::uuid[])) = 0 THEN
	(NULLIF(current_setting('` + database.ClientScopeGUC + `', true), '') IS NULL
	 OR review_item.client_id = ANY (string_to_array(current_setting('` + database.ClientScopeGUC + `', true), ',')::uuid[]))
	AND (NULLIF(current_setting('` + database.SiteScopeGUC + `', true), '') IS NULL
	 OR review_item.site_id = ANY (string_to_array(current_setting('` + database.SiteScopeGUC + `', true), ',')::uuid[]))
ELSE NOT EXISTS (
	SELECT 1 FROM unnest(review_item.candidate_ci_ids) AS candidate(id)
	WHERE NOT EXISTS (SELECT 1 FROM ci WHERE ci.id = candidate.id)) END)`

const selectColumns = `
	id::text,
	organization_id::text,
	kind,
	status,
	payload,
	COALESCE(candidate_ci_ids::text[], ARRAY[]::text[]),
	COALESCE(object_key, ''),
	COALESCE(action, ''),
	COALESCE(resolved_by::text, ''),
	resolved_at,
	COALESCE(resolution, ''),
	status = 'open' AND created_at < now() - make_interval(secs => $1::double precision),
	created_at,
	updated_at,
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, '')
`

var escalationSeconds = EscalationAge.Seconds()

// ListReviewItems implements Repository.
func (r *PGRepository) ListReviewItems(ctx context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Item, int, error) {
	items := make([]Item, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $2 AND " + visible
		args := []any{escalationSeconds, orgID}
		if filter.Status != "" {
			args = append(args, filter.Status)
			where += fmt.Sprintf(" AND status = $%d", len(args))
		}
		if filter.Kind != "" {
			args = append(args, filter.Kind)
			where += fmt.Sprintf(" AND kind = $%d", len(args))
		}
		if filter.ObjectKey != "" {
			args = append(args, filter.ObjectKey)
			where += fmt.Sprintf(" AND object_key = $%d", len(args))
		}
		if filter.Escalated {
			where += " AND status = 'open' AND created_at < now() - make_interval(secs => $1::double precision)"
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM review_item WHERE $1::double precision IS NOT NULL AND "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count review items: %w", err)
		}
		query := fmt.Sprintf("SELECT %s FROM review_item WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d",
			selectColumns, where, len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list review items: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scan(rows)
			if err != nil {
				return fmt.Errorf("scan review item: %w", err)
			}
			items = append(items, *item)
		}
		return rows.Err()
	})
	return items, total, err
}

// CreateReviewItem implements Repository. With an ObjectKey an open item of
// the same object and type is returned instead of a second one.
func (r *PGRepository) CreateReviewItem(ctx context.Context, item *Item) error {
	return database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Payload == nil {
			item.Payload = map[string]any{}
		}
		status := item.Status
		if status == "" {
			status = StatusOpen
		}
		candidates := item.CandidateCIIDs
		if candidates == nil {
			candidates = []string{}
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids, object_key, client_id, site_id)
			VALUES ($1, $2, $3, $4::jsonb, $5::uuid[], NULLIF($6, ''), NULLIF($7, '')::uuid, NULLIF($8, '')::uuid)
			ON CONFLICT (organization_id, kind, object_key) WHERE status = 'open' AND object_key IS NOT NULL DO NOTHING
			RETURNING id::text`,
			item.OrganizationID, item.Kind, status, item.Payload, candidates, item.ObjectKey, scopeID(item.ClientID), scopeID(item.SiteID)).Scan(&id)
		deduplicated := false
		if errors.Is(err, pgx.ErrNoRows) {
			deduplicated = true
			err = tx.QueryRow(ctx, `SELECT id::text FROM review_item
				WHERE organization_id = $1 AND kind = $2 AND object_key = $3 AND status = 'open'`,
				item.OrganizationID, item.Kind, item.ObjectKey).Scan(&id)
		}
		if err != nil {
			return fmt.Errorf("create review item: %w", err)
		}
		stored, err := scan(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM review_item WHERE id = $2 AND %s", selectColumns, visible),
			escalationSeconds, id))
		if errors.Is(err, pgx.ErrNoRows) {
			// The item references an object outside the tenant scope.
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("read review item: %w", err)
		}
		*item = *stored
		item.Deduplicated = deduplicated
		return nil
	})
}

// GetReviewItem implements Repository.
func (r *PGRepository) GetReviewItem(ctx context.Context, orgID, id string) (*Item, error) {
	var item *Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scan(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM review_item WHERE id = $2 AND organization_id = $3 AND %s",
			selectColumns, visible), escalationSeconds, id, orgID))
		return err
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ResolveReviewItem implements Repository. Resolving an unlicensed_ci item
// is audited (ENT-03); resolved_by and the audit actor are both
// resolution.ResolvedBy, the principal of the request (the system when
// empty). Every resolution writes review.resolved in its transaction
// (EVT-02).
func (r *PGRepository) ResolveReviewItem(ctx context.Context, orgID, id string, resolution *Resolution) (*Item, error) {
	var item *Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scan(tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE review_item
			SET status = $4, action = NULLIF($5, ''), resolution = $6, resolved_by = NULLIF($7, '')::uuid,
			    resolved_at = now(), updated_at = now()
			WHERE id = $2 AND organization_id = $3 AND %s
			RETURNING %s`, visible, selectColumns),
			escalationSeconds, id, orgID, resolution.Status, resolution.Action, resolution.Note, resolution.ResolvedBy))
		if err != nil {
			return err
		}
		if item.Kind == KindUnlicensedCI {
			actorType := "user"
			if resolution.ResolvedBy == "" {
				actorType = "system"
			}
			if _, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
				OrganizationID: orgID, ActorID: resolution.ResolvedBy, ActorType: actorType,
				Action: "unlicensed_ci." + item.Status, ResourceType: "review_item", ResourceID: item.ID,
				Changes: map[string]any{"resolution": item.Resolution, "device_key": item.Payload["device_key"]},
			}); err != nil {
				return err
			}
		}
		return recordResolved(ctx, tx, item)
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// recordResolved writes review.resolved of item in tx: in a context
// transaction (an effect that resolves the item with its own writes, e.g.
// the CI it creates) right before its commit, else at once
// (database.BeforeCommit, WP-334). The data names the item and its
// resolution, not the reported payload.
func recordResolved(ctx context.Context, tx pgx.Tx, item *Item) error {
	data := map[string]any{"id": item.ID, "kind": item.Kind, "status": item.Status, "action": item.Action,
		"resolution": item.Resolution, "resolved_by": item.ResolvedBy, "resolved_at": item.ResolvedAt,
		"object_key": item.ObjectKey, "candidate_ci_ids": item.CandidateCIIDs, "client_id": item.ClientID, "site_id": item.SiteID}
	return database.BeforeCommit(ctx, func() error {
		_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: item.OrganizationID, Type: events.ReviewResolved,
			EntityID: item.ID, Data: data})
		return err
	})
}

// scopeID passes a client or site id to the derivation trigger; anything but
// a UUID is no scope (the trigger keeps only ids of the organization).
func scopeID(id string) string {
	if audit.IsUUID(id) {
		return id
	}
	return ""
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Item, error) {
	item := &Item{}
	var candidates []string
	var resolvedAt sql.NullTime
	var createdAt, updatedAt time.Time
	if err := s.Scan(&item.ID, &item.OrganizationID, &item.Kind, &item.Status, &item.Payload, &candidates,
		&item.ObjectKey, &item.Action, &item.ResolvedBy, &resolvedAt, &item.Resolution, &item.Escalated,
		&createdAt, &updatedAt, &item.ClientID, &item.SiteID); err != nil {
		return nil, err
	}
	if item.Payload == nil {
		item.Payload = map[string]any{}
	}
	if len(candidates) > 0 {
		item.CandidateCIIDs = candidates
	}
	if resolvedAt.Valid {
		item.ResolvedAt = resolvedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return item, nil
}
