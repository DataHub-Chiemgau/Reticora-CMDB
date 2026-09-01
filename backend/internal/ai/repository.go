package ai

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type MemoryRepository struct {
	mu            sync.RWMutex
	conversations map[string]Conversation
	chunks        map[string]Chunk
	next          int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{conversations: map[string]Conversation{}, chunks: map[string]Chunk{}}
}
func (r *MemoryRepository) CreateConversation(_ context.Context, orgID, userID, title string) (Conversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	now := time.Now().UTC()
	c := Conversation{ID: fmt.Sprintf("conv-%d", r.next), OrganizationID: orgID, UserID: userID, Title: title, CreatedAt: now, UpdatedAt: now}
	r.conversations[c.ID] = c
	return c, nil
}
func (r *MemoryRepository) GetConversation(_ context.Context, orgID, id string) (Conversation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conversations[id]
	if !ok || c.OrganizationID != orgID {
		return Conversation{}, fmt.Errorf("conversation not found")
	}
	return c, nil
}
func (r *MemoryRepository) ListConversations(_ context.Context, orgID, userID string) ([]Conversation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Conversation
	for _, c := range r.conversations {
		if c.OrganizationID == orgID && (userID == "" || c.UserID == userID) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
func (r *MemoryRepository) AddMessage(_ context.Context, orgID, conversationID, role, content string, promptTokens, completionTokens int, citations []Citation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.conversations[conversationID]
	c.UpdatedAt = time.Now().UTC()
	r.conversations[conversationID] = c
	return nil
}
func (r *MemoryRepository) UpsertChunk(_ context.Context, ch Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch.ID == "" {
		ch.ID = ch.EntityType + ":" + ch.EntityID
	}
	if ch.UpdatedAt.IsZero() {
		ch.UpdatedAt = time.Now().UTC()
	}
	r.chunks[ch.OrganizationID+":"+ch.ID] = ch
	return nil
}
func (r *MemoryRepository) DeleteChunk(_ context.Context, orgID, entityType, entityID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chunks, orgID+":"+entityType+":"+entityID)
	return nil
}
func (r *MemoryRepository) CandidateChunks(_ context.Context, orgID string, entityTypes []string, entityIDs []string, limit int) ([]Chunk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := set(entityTypes)
	ids := set(entityIDs)
	var out []Chunk
	for _, ch := range r.chunks {
		if ch.OrganizationID != orgID {
			continue
		}
		if len(types) > 0 && !types[ch.EntityType] {
			continue
		}
		if len(ids) > 0 && !ids[ch.EntityID] {
			continue
		}
		out = append(out, ch)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func set(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}
