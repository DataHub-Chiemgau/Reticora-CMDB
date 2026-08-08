package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }
func (r *PGRepository) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *PGRepository) IndexDocument(ctx context.Context, doc Document) error {
	if doc.OrganizationID == "" || doc.EntityType == "" || doc.EntityID == "" {
		return fmt.Errorf("organization_id, entity_type and entity_id are required")
	}
	meta, err := json.Marshal(doc.Metadata)
	if err != nil {
		return fmt.Errorf("marshal document metadata: %w", err)
	}
	return r.withTenant(ctx, doc.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE SET title=EXCLUDED.title, summary=EXCLUDED.summary, url=EXCLUDED.url, metadata=EXCLUDED.metadata, updated_at=now()`, doc.OrganizationID, doc.EntityType, doc.EntityID, doc.Title, doc.Summary, doc.URL, meta)
		return err
	})
}
func (r *PGRepository) Delete(ctx context.Context, orgID, entityType, entityID string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM search_document WHERE organization_id=$1 AND entity_type=$2 AND entity_id=$3`, orgID, entityType, entityID)
		return err
	})
}

func buildPostgresQuery(q Query) (string, []any, error) {
	if err := validateQuery(q.Text); err != nil {
		return "", nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	where := []string{"organization_id = $1"}
	args := []any{q.OrganizationID}
	pos := 2
	if strings.TrimSpace(q.Text) != "" {
		where = append(where, fmt.Sprintf("search_vector @@ websearch_to_tsquery('simple', $%d)", pos))
		args = append(args, q.Text)
		pos++
	}
	if len(q.EntityTypes) > 0 {
		where = append(where, fmt.Sprintf("entity_type = ANY($%d)", pos))
		args = append(args, q.EntityTypes)
		pos++
	}
	score := "0.0"
	if strings.TrimSpace(q.Text) != "" {
		score = fmt.Sprintf("ts_rank(search_vector, websearch_to_tsquery('simple', $%d))", 2)
	}
	snippet := "''"
	if q.Highlight && strings.TrimSpace(q.Text) != "" {
		snippet = fmt.Sprintf("ts_headline('simple', title || ' ' || summary, websearch_to_tsquery('simple', $%d), 'StartSel=<mark>, StopSel=</mark>, MaxFragments=2')", 2)
	}
	args = append(args, limit, q.Offset)
	query := fmt.Sprintf(`SELECT id::text, organization_id::text, entity_type, entity_id::text, title, summary, url, metadata, updated_at, %s AS score, %s AS snippet, COUNT(*) OVER() FROM search_document WHERE %s ORDER BY score DESC, updated_at DESC LIMIT $%d OFFSET $%d`, score, snippet, strings.Join(where, " AND "), pos, pos+1)
	return query, args, nil
}
func (r *PGRepository) Query(ctx context.Context, q Query) (Result, error) {
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	var res Result
	err := r.withTenant(ctx, q.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		sql, args, err := buildPostgresQuery(q)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var meta []byte
			var snippet string
			if err := rows.Scan(&h.ID, &h.OrganizationID, &h.EntityType, &h.EntityID, &h.Title, &h.Summary, &h.URL, &meta, &h.UpdatedAt, &h.Score, &snippet, &res.Total); err != nil {
				return err
			}
			if err := json.Unmarshal(meta, &h.Metadata); err != nil {
				return fmt.Errorf("unmarshal document metadata for %s: %w", h.ID, err)
			}
			if snippet != "" {
				h.Highlights = []string{snippet}
			}
			res.Data = append(res.Data, h)
		}
		return rows.Err()
	})
	res.Limit, res.Offset = limit, q.Offset
	res.HasMore = q.Offset+len(res.Data) < res.Total
	return res, err
}
func (r *PGRepository) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	var count int64
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM search_document WHERE organization_id=$1`, orgID); err != nil {
			return err
		}
		cmd, err := tx.Exec(ctx, `INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT organization_id,'ci',id,name,concat_ws(' ',hostname,manufacturer,model,serial_number,os_name,os_version),'/cmdb/'||id,'{}'::jsonb FROM ci WHERE organization_id=$1 AND deleted_at IS NULL
UNION ALL SELECT organization_id,'asset',id,name,concat_ws(' ',asset_tag,category,status,supplier,serial_number,location,notes),'/assets','{}'::jsonb FROM asset WHERE organization_id=$1
UNION ALL SELECT organization_id,'document',id,title,concat_ws(' ',description,file_name,category,array_to_string(tags,' ')),'/documents','{}'::jsonb FROM document WHERE organization_id=$1
UNION ALL SELECT organization_id,'ticket',id,title,concat_ws(' ',description,status,priority,category,array_to_string(tags,' ')),'/tickets','{}'::jsonb FROM ticket WHERE organization_id=$1
UNION ALL SELECT organization_id,'contact',id,display_name,concat_ws(' ',email,phone,role,department,notes),'/contacts','{}'::jsonb FROM contact WHERE organization_id=$1`, orgID)
		count = cmd.RowsAffected()
		return err
	})
	return ReindexResult{Indexed: int(count)}, err
}
