package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

type OpenSearchConfig struct {
	URL, Username, Password, Index string
	Timeout                        time.Duration
	MaxResponseBytes               int64
}
type OpenSearchBackend struct {
	cfg    OpenSearchConfig
	client *http.Client
}

func NewOpenSearchBackend(cfg OpenSearchConfig, client *http.Client) *OpenSearchBackend {
	if cfg.Index == "" {
		cfg.Index = "reticora-search"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 4 << 20
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &OpenSearchBackend{cfg: cfg, client: client}
}
func (b *OpenSearchBackend) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("opensearch ping: build request: %w", err)
	}
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("opensearch ping failed: %s", res.Status)
	}
	return nil
}

// indexTemplateBody returns the idempotent index-template payload for the
// tenant search index. The explicit mapping keeps dynamic mapping guesses
// (e.g. entity_id indexed as text+keyword, or metadata values analyzed) from
// drifting between environments, and makes the schema reproducible.
func indexTemplateBody(index string) map[string]any {
	return map[string]any{
		"index_patterns": []string{index},
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":   1,
				"number_of_replicas": 0,
			},
			"mappings": map[string]any{
				"properties": map[string]any{
					"organization_id": map[string]any{"type": "keyword"},
					"client_id":       map[string]any{"type": "keyword"},
					"site_id":         map[string]any{"type": "keyword"},
					"entity_type":     map[string]any{"type": "keyword"},
					"entity_id":       map[string]any{"type": "keyword"},
					"title": map[string]any{
						"type":   "text",
						"fields": map[string]any{"keyword": map[string]any{"type": "keyword", "ignore_above": 512}},
					},
					"summary":    map[string]any{"type": "text"},
					"url":        map[string]any{"type": "keyword"},
					"metadata":   map[string]any{"type": "object", "dynamic": true},
					"updated_at": map[string]any{"type": "date"},
				},
			},
		},
	}
}

// EnsureIndexTemplate idempotently applies the search index template. It is
// safe to call on every startup: OpenSearch treats PUT _index_template as an
// upsert.
func (b *OpenSearchBackend) EnsureIndexTemplate(ctx context.Context) error {
	body, err := json.Marshal(indexTemplateBody(b.cfg.Index))
	if err != nil {
		return fmt.Errorf("opensearch index template: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/_index_template/%s", b.cfg.URL, b.cfg.Index), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opensearch index template: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("opensearch index template: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("opensearch index template failed: %s", res.Status)
	}
	return nil
}
func (b *OpenSearchBackend) IndexDocument(ctx context.Context, doc Document) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("opensearch index: marshal document: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s/_doc/%s-%s-%s", b.cfg.URL, b.cfg.Index, doc.OrganizationID, doc.EntityType, doc.EntityID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("opensearch index failed: %s", res.Status)
	}
	return nil
}
func (b *OpenSearchBackend) Delete(ctx context.Context, orgID, entityType, entityID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/%s/_doc/%s-%s-%s", b.cfg.URL, b.cfg.Index, orgID, entityType, entityID), nil)
	if err != nil {
		return err
	}
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode != 404 {
		return fmt.Errorf("opensearch delete failed: %s", res.Status)
	}
	return nil
}
func (b *OpenSearchBackend) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	return ReindexResult{}, fmt.Errorf("opensearch reindex requires the PostgreSQL coordinator")
}

// buildOpenSearchQuery builds the search request. The scope filters come
// from the principal's tenant scope, never from the request: client and site
// must match or be unset (E-09), and entity types whose visibility depends on
// more than the indexed fields are left out for restricted principals
// (documents: their links; tickets: the team).
func buildOpenSearchQuery(q *Query, scope *database.TenantScope) (map[string]any, error) {
	if err := validateQuery(q.Text); err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, database.ErrNoTenantScope
	}
	if !strings.EqualFold(scope.OrgID, q.OrganizationID) {
		return nil, database.ErrTenantMismatch
	}
	filters := []any{map[string]any{"term": map[string]any{"organization_id": q.OrganizationID}}}
	if len(q.EntityTypes) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"entity_type": q.EntityTypes}})
	}
	for _, dim := range []struct {
		field string
		set   database.ScopeSet
	}{{"client_id", scope.Clients}, {"site_id", scope.Sites}} {
		field, set := dim.field, dim.set
		if set.IsAll() {
			continue
		}
		filters = append(filters, map[string]any{"bool": map[string]any{
			"should": []any{
				map[string]any{"terms": map[string]any{field: nonNil(set.IDs())}},
				map[string]any{"bool": map[string]any{"must_not": map[string]any{"exists": map[string]any{"field": field}}}},
			},
			"minimum_should_match": 1,
		}})
	}
	if excluded := excludedTypes(scope); len(excluded) > 0 {
		filters = append(filters, map[string]any{"bool": map[string]any{
			"must_not": map[string]any{"terms": map[string]any{"entity_type": excluded}},
		}})
	}
	must := []any{}
	if strings.TrimSpace(q.Text) != "" {
		must = append(must, map[string]any{"multi_match": map[string]any{"query": q.Text, "fields": []string{"title^3", "summary", "metadata.*"}, "type": "best_fields"}})
	}
	if len(must) == 0 {
		must = append(must, map[string]any{"match_all": map[string]any{}})
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	body := map[string]any{"from": q.Offset, "size": limit, "query": map[string]any{"bool": map[string]any{"filter": filters, "must": must}}}
	if q.Highlight {
		body["highlight"] = map[string]any{"fields": map[string]any{"title": map[string]any{}, "summary": map[string]any{}}}
	}
	return body, nil
}
func (b *OpenSearchBackend) Query(ctx context.Context, q Query) (Result, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return Result{}, database.ErrNoTenantScope
	}
	body, err := buildOpenSearchQuery(&q, &scope)
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Result{}, fmt.Errorf("opensearch query: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%s/_search", b.cfg.URL, b.cfg.Index), bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("opensearch query failed: %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, b.cfg.MaxResponseBytes))
	if err != nil {
		return Result{}, err
	}
	var parsed struct {
		Hits struct {
			Total any `json:"total"`
			Hits  []struct {
				Score     float64             `json:"_score"`
				Source    Document            `json:"_source"`
				Highlight map[string][]string `json:"highlight"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Result{}, err
	}
	out := Result{Limit: q.Limit, Offset: q.Offset}
	switch v := parsed.Hits.Total.(type) {
	case map[string]any:
		if n, ok := v["value"].(float64); ok {
			out.Total = int(n)
		}
	case float64:
		out.Total = int(v)
	}
	for _, item := range parsed.Hits.Hits {
		// The filters run in OpenSearch; the check repeats them on the
		// returned documents so a mapping error cannot leak hits.
		if !visibleTo(&scope, q.OrganizationID, &item.Source) {
			continue
		}
		h := Hit{Document: item.Source, Score: item.Score}
		for _, vals := range item.Highlight {
			h.Highlights = append(h.Highlights, vals...)
		}
		out.Data = append(out.Data, h)
	}
	out.HasMore = out.Offset+len(out.Data) < out.Total
	return out, nil
}
// excludedTypes lists the entity types a restricted principal never gets from
// a remote index.
func excludedTypes(scope *database.TenantScope) []string {
	var out []string
	if !scope.Clients.IsAll() || !scope.Sites.IsAll() {
		out = append(out, "document")
	}
	if !scope.Teams.IsAll() {
		out = append(out, "ticket")
	}
	return out
}

// visibleTo applies the scope filters of buildOpenSearchQuery to one document.
func visibleTo(scope *database.TenantScope, orgID string, doc *Document) bool {
	if !strings.EqualFold(doc.OrganizationID, orgID) {
		return false
	}
	inSet := func(set database.ScopeSet, id string) bool {
		if set.IsAll() || id == "" {
			return true
		}
		for _, allowed := range set.IDs() {
			if strings.EqualFold(allowed, id) {
				return true
			}
		}
		return false
	}
	if !inSet(scope.Clients, doc.ClientID) || !inSet(scope.Sites, doc.SiteID) {
		return false
	}
	for _, excluded := range excludedTypes(scope) {
		if doc.EntityType == excluded {
			return false
		}
	}
	return true
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func (b *OpenSearchBackend) auth(req *http.Request) {
	if b.cfg.Username != "" {
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	}
}
