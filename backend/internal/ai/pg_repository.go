package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ pool *pgxpool.Pool }

// chunkVisible restricts RAG chunks to source objects that are visible under
// the transaction's tenant scope and still exist. The policy of ai_chunk
// already filters by the client and site derived from the source (migration
// 000069); the subqueries add the source's own policy, which covers
// documents and tickets (links, teams) and chunks that are out of date.
const chunkVisible = `CASE ai_chunk.entity_type
	WHEN 'ci' THEN EXISTS (SELECT 1 FROM ci WHERE ci.id = ai_chunk.entity_id)
	WHEN 'asset' THEN EXISTS (SELECT 1 FROM asset WHERE asset.id = ai_chunk.entity_id)
	WHEN 'contact' THEN EXISTS (SELECT 1 FROM contact WHERE contact.id = ai_chunk.entity_id)
	WHEN 'document' THEN EXISTS (SELECT 1 FROM document WHERE document.id = ai_chunk.entity_id)
	WHEN 'ticket' THEN EXISTS (SELECT 1 FROM ticket WHERE ticket.id = ai_chunk.entity_id)
	WHEN 'location' THEN EXISTS (SELECT 1 FROM location WHERE location.id = ai_chunk.entity_id)
	ELSE true END`

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }
func (r *PGRepository) CreateConversation(ctx context.Context, orgID, userID, title string) (Conversation, error) {
	var c Conversation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO ai_conversation (organization_id,user_id,title) VALUES ($1,NULLIF($2,'')::uuid,$3) RETURNING id::text, organization_id::text, COALESCE(user_id::text,''), title, created_at, updated_at`, orgID, userID, title).Scan(&c.ID, &c.OrganizationID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	})
	return c, err
}
func (r *PGRepository) GetConversation(ctx context.Context, orgID, id string) (Conversation, error) {
	var c Conversation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text, organization_id::text, COALESCE(user_id::text,''), title, created_at, updated_at FROM ai_conversation WHERE organization_id=$1 AND id=$2`, orgID, id).Scan(&c.ID, &c.OrganizationID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	})
	if err == pgx.ErrNoRows {
		return c, fmt.Errorf("conversation not found")
	}
	return c, err
}
func (r *PGRepository) ListConversations(ctx context.Context, orgID, userID string) ([]Conversation, error) {
	var out []Conversation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, COALESCE(user_id::text,''), title, created_at, updated_at FROM ai_conversation WHERE organization_id=$1 AND (NULLIF($2,'')::uuid IS NULL OR user_id=NULLIF($2,'')::uuid) ORDER BY updated_at DESC LIMIT 100`, orgID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Conversation
			if err := rows.Scan(&c.ID, &c.OrganizationID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
func (r *PGRepository) AddMessage(ctx context.Context, orgID, conversationID, role, content string, promptTokens, completionTokens int, citations []Citation) error {
	cites, err := json.Marshal(citations)
	if err != nil {
		return fmt.Errorf("marshal citations: %w", err)
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ai_message (organization_id,conversation_id,role,content,prompt_tokens,completion_tokens,citations) VALUES ($1,$2,$3,$4,$5,$6,$7); UPDATE ai_conversation SET updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, conversationID, role, content, promptTokens, completionTokens, cites)
		return err
	})
}
func (r *PGRepository) UpsertChunk(ctx context.Context, ch Chunk) error {
	emb, err := json.Marshal(ch.Embedding)
	if err != nil {
		return fmt.Errorf("marshal embedding: %w", err)
	}
	return database.WithRequestTenant(ctx, r.pool, ch.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ai_chunk (organization_id,entity_type,entity_id,title,content,url,embedding) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (organization_id,entity_type,entity_id) DO UPDATE SET title=EXCLUDED.title, content=EXCLUDED.content, url=EXCLUDED.url, embedding=EXCLUDED.embedding, updated_at=now()`, ch.OrganizationID, ch.EntityType, ch.EntityID, ch.Title, ch.Content, ch.URL, emb)
		return err
	})
}

// UpsertChunks upserts the chunks of one organization with one statement
// (NFR-03, WP-333), each like UpsertChunk. A chunk repeated in chunks is
// written once, the last one wins: one statement cannot update a row twice.
func (r *PGRepository) UpsertChunks(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	orgID := chunks[0].OrganizationID
	index := map[[2]string]int{}
	var types, ids, titles, contents, urls, embeddings []string
	for i := range chunks {
		ch := &chunks[i]
		if ch.OrganizationID != orgID {
			return fmt.Errorf("chunks of several organizations in one batch")
		}
		emb, err := json.Marshal(ch.Embedding)
		if err != nil {
			return fmt.Errorf("marshal embedding: %w", err)
		}
		k := [2]string{ch.EntityType, ch.EntityID}
		if j, seen := index[k]; seen {
			titles[j], contents[j], urls[j], embeddings[j] = ch.Title, ch.Content, ch.URL, string(emb)
			continue
		}
		index[k] = len(types)
		types, ids = append(types, ch.EntityType), append(ids, ch.EntityID)
		titles, contents, urls, embeddings = append(titles, ch.Title), append(contents, ch.Content), append(urls, ch.URL), append(embeddings, string(emb))
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ai_chunk (organization_id,entity_type,entity_id,title,content,url,embedding)
			SELECT $1, c.entity_type, c.entity_id::uuid, c.title, c.content, c.url, c.embedding::jsonb
			FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[])
			     WITH ORDINALITY AS c(entity_type, entity_id, title, content, url, embedding, ord)
			ORDER BY c.ord
			ON CONFLICT (organization_id,entity_type,entity_id) DO UPDATE SET title=EXCLUDED.title, content=EXCLUDED.content,
				url=EXCLUDED.url, embedding=EXCLUDED.embedding, updated_at=now()`,
			orgID, types, ids, titles, contents, urls, embeddings)
		return err
	})
}

func (r *PGRepository) DeleteChunk(ctx context.Context, orgID, entityType, entityID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM ai_chunk WHERE `+chunkVisible+` AND organization_id=$1 AND entity_type=$2 AND entity_id=$3`, orgID, entityType, entityID)
		return err
	})
}
func (r *PGRepository) CandidateChunks(ctx context.Context, orgID string, entityTypes []string, entityIDs []string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []Chunk
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, entity_type, entity_id::text, title, content, url, embedding, updated_at FROM ai_chunk WHERE `+chunkVisible+` AND organization_id=$1 AND (cardinality($2::text[])=0 OR entity_type=ANY($2::text[])) AND (cardinality($3::text[])=0 OR entity_id::text=ANY($3::text[])) ORDER BY updated_at DESC LIMIT $4`, orgID, entityTypes, entityIDs, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ch Chunk
			var emb []byte
			if err := rows.Scan(&ch.ID, &ch.OrganizationID, &ch.EntityType, &ch.EntityID, &ch.Title, &ch.Content, &ch.URL, &emb, &ch.UpdatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(emb, &ch.Embedding)
			out = append(out, ch)
		}
		return rows.Err()
	})
	return out, err
}

// AIOptIn reports the organization's AI opt-in (AI-02) in the request's
// tenant transaction.
func (r *PGRepository) AIOptIn(ctx context.Context, orgID string) (bool, error) {
	var optIn bool
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ai_opt_in FROM organization WHERE id = $1`, orgID).Scan(&optIn)
	})
	return optIn, err
}
