package ai

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	repo      Repository
	provider  Provider
	retriever *Retriever
	consent   Consent
}

// ProblemAIOptInRequired: the organization has not opted in to external AI
// processing or does not hold the ai add-on (AI-02).
const ProblemAIOptInRequired = "https://reticora.io/problems/ai-opt-in-required"

func NewHandler(repo Repository, provider Provider, retriever *Retriever) *Handler {
	return &Handler{repo: repo, provider: provider, retriever: retriever}
}

// WithConsent attaches the per-organization consent of Ask (AI-02). Without
// one, Ask refuses every organization (fail-closed).
func (h *Handler) WithConsent(consent Consent) *Handler {
	h.consent = consent
	return h
}
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/ai/conversations", h.ListConversations)
	r.Post("/api/v1/ai/conversations", h.CreateConversation)
	r.Post("/api/v1/ai/ask", h.Ask)
}
func (h *Handler) ListConversations(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// {data, next_cursor} with limit and the signed cursor (API-02, F-075).
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListConversations(r.Context(), t.OrganizationID, t.UserID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(items, page))
}
func (h *Handler) CreateConversation(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	_ = api.ReadJSON(r, &req)
	if req.Title == "" {
		req.Title = "Neue Unterhaltung"
	}
	c, err := h.repo.CreateConversation(r.Context(), t.OrganizationID, t.UserID, req.Title)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, c)
}
func (h *Handler) Ask(w http.ResponseWriter, r *http.Request) {
	if h.provider == nil || !h.provider.Enabled() {
		api.WriteError(w, http.StatusServiceUnavailable, "AI assistant disabled", "RETICORA_LLM_BASE_URL and RETICORA_LLM_CHAT_MODEL must be configured")
		return
	}
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// The question and the CI context leave the installation only with the
	// organization's opt-in and the ai add-on (AI-02): without them no
	// conversation is created, nothing is retrieved or sent to the provider.
	if h.consent == nil || !h.consent.AllowsExternalAI(r.Context(), t.OrganizationID) {
		api.WriteProblem(w, http.StatusForbidden, ProblemAIOptInRequired, "Forbidden",
			"the organization has not opted in to external AI processing or does not hold the ai add-on")
		return
	}
	var req AskRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || len([]rune(req.Question)) > 2000 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "question is required and must be at most 2000 characters")
		return
	}
	convID := req.ConversationID
	if convID == "" {
		c, err := h.repo.CreateConversation(r.Context(), t.OrganizationID, t.UserID, shortTitle(req.Question))
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
		convID = c.ID
	} else if _, err := h.repo.GetConversation(r.Context(), t.OrganizationID, convID); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "conversation not found")
		return
	}
	chunks, cites, err := h.retriever.Retrieve(r.Context(), t.OrganizationID, t.UserID, req.Question, 6)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// Citation obligation (AI-01): without permitted source objects the
	// provider is not called at all; the answer says so and cites nothing.
	// Otherwise the answer is grounded in exactly the cited chunks.
	var answer string
	var pt, ct int
	if len(chunks) == 0 {
		answer = noGroundingAnswer
		cites = nil
	} else {
		prompt := buildPrompt(req.Question, chunks)
		answer, pt, ct, err = h.provider.Chat([]Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: prompt}})
		if err != nil {
			// The provider's error can name its endpoint and echo its
			// response; it goes to the log only (API-03).
			slog.WarnContext(r.Context(), "ai: provider request failed", "error", err)
			api.WriteError(w, http.StatusBadGateway, "AI provider error", "the AI provider did not answer")
			return
		}
	}
	_ = h.repo.AddMessage(r.Context(), t.OrganizationID, convID, "user", req.Question, 0, 0, nil)
	_ = h.repo.AddMessage(r.Context(), t.OrganizationID, convID, "assistant", answer, pt, ct, cites)
	api.WriteJSON(w, http.StatusOK, AskResponse{ConversationID: convID, Answer: answer, Citations: cites, PromptTokens: pt, CompletionTokens: ct})
}

// noGroundingAnswer is returned without a provider call when no source object
// visible to the caller matches the question.
const noGroundingAnswer = "Es wurden keine passenden Daten gefunden, die du sehen darfst. Ohne belegte Quellen erzeugt der Assistent keine Antwort."

// systemPrompt enforces the RAG governance rules: answer only from the
// supplied tenant-owned context, name uncertainty, and cite the sources.
const systemPrompt = "Du bist der Reticora CMDB Assistent. Antworte ausschließlich anhand des bereitgestellten tenant-eigenen Kontextes. " +
	"Wenn der Kontext die Frage nicht beantwortet, sage das ausdrücklich und erfinde keine Fakten. " +
	"Nenne Unsicherheit und verweise bei jeder Aussage auf die Quelle (die Nummer des Kontext-Eintrags, z. B. [1])."

func buildPrompt(q string, chunks []Chunk) string {
	var b strings.Builder
	b.WriteString("Kontext:\n")
	for i, ch := range chunks {
		fmt.Fprintf(&b, "[%d] %s %s/%s: %s\n", i+1, ch.Title, ch.EntityType, ch.EntityID, ch.Content)
	}
	b.WriteString("\nFrage: " + q)
	return b.String()
}
func shortTitle(q string) string {
	r := []rune(q)
	if len(r) > 60 {
		return string(r[:60])
	}
	return q
}
