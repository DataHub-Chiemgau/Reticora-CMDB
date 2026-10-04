package ai

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

type Retriever struct {
	repo        Repository
	search      search.Backend
	permissions permission.Repository
	provider    Provider
}

func NewRetriever(repo Repository, searchBackend search.Backend, permissions permission.Repository, provider Provider) *Retriever {
	return &Retriever{repo: repo, search: searchBackend, permissions: permissions, provider: provider}
}
func Cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
func (r *Retriever) Retrieve(ctx context.Context, orgID, userID, question string, limit int) ([]Chunk, []Citation, error) {
	if limit <= 0 {
		limit = 5
	}
	sr, err := r.search.Query(ctx, search.Query{OrganizationID: orgID, UserID: userID, Text: question, Limit: 25, Highlight: false})
	if err != nil {
		return nil, nil, err
	}
	var ids, types []string
	seen := map[string]bool{}
	for _, h := range sr.Data {
		// Defense in depth: never trust a search backend hit that belongs to a
		// different tenant, even if the backend is misconfigured.
		if h.OrganizationID != "" && h.OrganizationID != orgID {
			continue
		}
		if !r.allowed(ctx, orgID, userID, h.EntityType) {
			continue
		}
		ids = append(ids, h.EntityID)
		if !seen[h.EntityType] {
			types = append(types, h.EntityType)
			seen[h.EntityType] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	chunks, err := r.repo.CandidateChunks(ctx, orgID, types, ids, 50)
	if err != nil {
		return nil, nil, err
	}
	qvec := []float64(nil)
	if r.provider != nil && r.provider.EmbeddingsEnabled() {
		qvec, _ = r.provider.Embed(question)
	}
	type scored struct {
		ch    Chunk
		score float64
	}
	var scoredChunks []scored
	qlower := strings.ToLower(question)
	for _, ch := range chunks {
		// Defense in depth: only rank chunks owned by the requesting tenant.
		if ch.OrganizationID != "" && ch.OrganizationID != orgID {
			continue
		}
		s := float64(strings.Count(strings.ToLower(ch.Title+" "+ch.Content), qlower))
		if len(qvec) > 0 && len(ch.Embedding) > 0 {
			s = Cosine(qvec, ch.Embedding)
		}
		scoredChunks = append(scoredChunks, scored{ch, s})
	}
	sort.Slice(scoredChunks, func(i, j int) bool { return scoredChunks[i].score > scoredChunks[j].score })
	if len(scoredChunks) > limit {
		scoredChunks = scoredChunks[:limit]
	}
	out := make([]Chunk, 0, len(scoredChunks))
	cites := make([]Citation, 0, len(scoredChunks))
	for _, s := range scoredChunks {
		out = append(out, s.ch)
		cites = append(cites, Citation{EntityType: s.ch.EntityType, EntityID: s.ch.EntityID, Title: s.ch.Title, URL: s.ch.URL, Score: s.score})
	}
	return out, cites, nil
}
// allowed checks the read permission of a source object's entity type. It
// fails closed: without a permission repository or user nothing is allowed.
func (r *Retriever) allowed(ctx context.Context, orgID, userID, entity string) bool {
	if r.permissions == nil || userID == "" {
		return false
	}
	key := searchPermission(entity)
	if key == "" {
		return false
	}
	ok, err := r.permissions.HasPermission(ctx, orgID, userID, key)
	return err == nil && ok
}
// searchPermission is the read permission of an entity type, shared with the
// search handler (search.ReadPermission); "" for types never returned.
func searchPermission(entity string) string {
	return search.ReadPermission[entity]
}
