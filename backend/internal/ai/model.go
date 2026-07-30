// Package ai implements the governed RAG assistant.
package ai

import "time"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Citation struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Score      float64 `json:"score"`
}
type AskRequest struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Question       string `json:"question"`
}
type AskResponse struct {
	ConversationID   string     `json:"conversation_id"`
	Answer           string     `json:"answer"`
	Citations        []Citation `json:"citations"`
	PromptTokens     int        `json:"prompt_tokens"`
	CompletionTokens int        `json:"completion_tokens"`
}
type Conversation struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	UserID         string    `json:"user_id,omitempty"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Chunk struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	EntityType     string    `json:"entity_type"`
	EntityID       string    `json:"entity_id"`
	Title          string    `json:"title"`
	Content        string    `json:"content"`
	URL            string    `json:"url"`
	Embedding      []float64 `json:"embedding,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Provider interface {
	Enabled() bool
	Chat(messages []Message) (string, int, int, error)
	Embed(text string) ([]float64, error)
	EmbeddingsEnabled() bool
}
type Repository interface {
	CreateConversation(orgID, userID, title string) (Conversation, error)
	GetConversation(orgID, id string) (Conversation, error)
	ListConversations(orgID, userID string) ([]Conversation, error)
	AddMessage(orgID, conversationID, role, content string, promptTokens, completionTokens int, citations []Citation) error
	UpsertChunk(chunk Chunk) error
	CandidateChunks(orgID string, entityTypes []string, entityIDs []string, limit int) ([]Chunk, error)
}
