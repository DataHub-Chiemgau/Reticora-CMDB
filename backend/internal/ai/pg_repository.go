package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id',$1,true)", orgID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *PGRepository) CreateConversation(orgID, userID, title string) (Conversation, error) {
	var c Conversation
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO ai_conversation (organization_id,user_id,title) VALUES ($1,NULLIF($2,'')::uuid,$3) RETURNING id::text, organization_id::text, COALESCE(user_id::text,''), title, created_at, updated_at`, orgID, userID, title).Scan(&c.ID, &c.OrganizationID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	})
	return c, err
}
func (r *PGRepository) GetConversation(orgID, id string) (Conversation, error) {
	var c Conversation
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text, organization_id::text, COALESCE(user_id::text,''), title, created_at, updated_at FROM ai_conversation WHERE organization_id=$1 AND id=$2`, orgID, id).Scan(&c.ID, &c.OrganizationID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	})
	if err == pgx.ErrNoRows {
		return c, fmt.Errorf("conversation not found")
	}
	return c, err
}
func (r *PGRepository) ListConversations(orgID, userID string) ([]Conversation, error) {
	var out []Conversation
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
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
func (r *PGRepository) AddMessage(orgID, conversationID, role, content string, promptTokens, completionTokens int, citations []Citation) error {
	cites, _ := json.Marshal(citations)
	return r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ai_message (organization_id,conversation_id,role,content,prompt_tokens,completion_tokens,citations) VALUES ($1,$2,$3,$4,$5,$6,$7); UPDATE ai_conversation SET updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, conversationID, role, content, promptTokens, completionTokens, cites)
		return err
	})
}
func (r *PGRepository) UpsertChunk(ch Chunk) error {
	emb, _ := json.Marshal(ch.Embedding)
	return r.withTenant(context.Background(), ch.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ai_chunk (organization_id,entity_type,entity_id,title,content,url,embedding) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (organization_id,entity_type,entity_id) DO UPDATE SET title=EXCLUDED.title, content=EXCLUDED.content, url=EXCLUDED.url, embedding=EXCLUDED.embedding, updated_at=now()`, ch.OrganizationID, ch.EntityType, ch.EntityID, ch.Title, ch.Content, ch.URL, emb)
		return err
	})
}
func (r *PGRepository) CandidateChunks(orgID string, entityTypes []string, entityIDs []string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []Chunk
	err := r.withTenant(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, organization_id::text, entity_type, entity_id::text, title, content, url, embedding, updated_at FROM ai_chunk WHERE organization_id=$1 AND (cardinality($2::text[])=0 OR entity_type=ANY($2::text[])) AND (cardinality($3::text[])=0 OR entity_id::text=ANY($3::text[])) ORDER BY updated_at DESC LIMIT $4`, orgID, entityTypes, entityIDs, limit)
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
