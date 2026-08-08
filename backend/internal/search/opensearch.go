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

func buildOpenSearchQuery(q Query) (map[string]any, error) {
	if err := validateQuery(q.Text); err != nil {
		return nil, err
	}
	filters := []any{map[string]any{"term": map[string]any{"organization_id": q.OrganizationID}}}
	if len(q.EntityTypes) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"entity_type": q.EntityTypes}})
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
	body, err := buildOpenSearchQuery(q)
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
		h := Hit{Document: item.Source, Score: item.Score}
		for _, vals := range item.Highlight {
			h.Highlights = append(h.Highlights, vals...)
		}
		out.Data = append(out.Data, h)
	}
	out.HasMore = out.Offset+len(out.Data) < out.Total
	return out, nil
}
func (b *OpenSearchBackend) auth(req *http.Request) {
	if b.cfg.Username != "" {
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	}
}
