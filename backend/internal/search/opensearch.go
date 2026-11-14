package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

func NewOpenSearchBackend(config *OpenSearchConfig, client *http.Client) *OpenSearchBackend {
	cfg := *config
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.URL, http.NoBody)
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

// Fields of the remote document that only the full documents of the
// PostgreSQL index fill (SRC-02, WP-787): the field rows by field key and
// the identifiers. No hit carries them (_source excludes).
const (
	remoteFieldRows    = "fields"
	remoteIdentifiers  = "identifiers"
	remoteHighlightPre = "<mark>"
	remoteHighlightEnd = "</mark>"
)

// searchFieldMappings returns the dynamic templates and the properties of
// the field rows and identifiers (WP-787): every field row a text field
// fields.<key>, the identifiers nested with field and norm as keyword and
// addr as ip.
func searchFieldMappings() (templates []any, properties map[string]any) {
	templates = []any{map[string]any{"search_field_rows": map[string]any{
		"path_match":         remoteFieldRows + ".*",
		"match_mapping_type": "string",
		"mapping":            map[string]any{"type": "text"},
	}}}
	properties = map[string]any{
		remoteFieldRows: map[string]any{"type": "object", "dynamic": true},
		remoteIdentifiers: map[string]any{
			"type": "nested",
			"properties": map[string]any{
				"field": map[string]any{"type": "keyword"},
				"norm":  map[string]any{"type": "keyword"},
				"addr":  map[string]any{"type": "ip"},
			},
		},
	}
	return templates, properties
}

// indexTemplateBody returns the idempotent index-template payload for the
// tenant search index. The explicit mapping keeps dynamic mapping guesses
// (e.g. entity_id indexed as text+keyword, or metadata values analyzed) from
// drifting between environments, and makes the schema reproducible.
func indexTemplateBody(index string) map[string]any {
	templates, fields := searchFieldMappings()
	properties := map[string]any{
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
	}
	for k, v := range fields {
		properties[k] = v
	}
	return map[string]any{
		"index_patterns": []string{index},
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":   1,
				"number_of_replicas": 0,
			},
			"mappings": map[string]any{
				"dynamic_templates": templates,
				"properties":        properties,
			},
		},
	}
}

// indexMappingBody returns the additive mapping of an existing index: the
// field rows and identifiers (WP-787), which the template gives only to a
// new index.
func indexMappingBody() map[string]any {
	templates, properties := searchFieldMappings()
	return map[string]any{"dynamic_templates": templates, "properties": properties}
}

// EnsureIndexTemplate idempotently applies the search index template and
// adds the mapping of the field rows and identifiers to the index when it
// exists already (PUT /<index>/_mapping, additive: no existing field
// changes; WP-787). It is safe to call on every startup: OpenSearch treats
// PUT _index_template as an upsert, and a missing index gets the mapping
// from the template when it is created. A reindex job then fills the new
// fields (docs/ops/sizing.md).
func (b *OpenSearchBackend) EnsureIndexTemplate(ctx context.Context) error {
	if err := b.putJSON(ctx, fmt.Sprintf("%s/_index_template/%s", b.cfg.URL, b.cfg.Index), indexTemplateBody(b.cfg.Index), false); err != nil {
		return fmt.Errorf("opensearch index template: %w", err)
	}
	if err := b.putJSON(ctx, fmt.Sprintf("%s/%s/_mapping", b.cfg.URL, b.cfg.Index), indexMappingBody(), true); err != nil {
		return fmt.Errorf("opensearch index mapping: %w", err)
	}
	return nil
}

// putJSON sends body with PUT; with missingOK a 404 (no such index) is no
// error.
func (b *OpenSearchBackend) putJSON(ctx context.Context, url string, body any, missingOK bool) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if missingOK && res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("failed: %s", res.Status)
	}
	return nil
}

// IndexDocument writes the document with the fields of Document alone: an
// OpenSearchBackend without PostgreSQL keeps the fields of phase 1, its
// queries find no field rows and identifiers (WP-787).
func (b *OpenSearchBackend) IndexDocument(ctx context.Context, doc *Document) error {
	return b.putDocument(ctx, doc, doc)
}

// remoteDocument is the document of the remote index of a full document
// (WP-787): the document with its field rows and identifiers.
type remoteDocument struct {
	*Document
	Fields      map[string]string `json:"fields,omitempty"`
	Identifiers []IdentifierRow   `json:"identifiers,omitempty"`
}

// IndexFullDocument implements FullDocumentIndexer: it writes the document
// with its field rows (fields.<key>) and identifiers (WP-787).
func (b *OpenSearchBackend) IndexFullDocument(ctx context.Context, doc *FullDocument) error {
	return b.putDocument(ctx, &doc.Document, remoteDocument{Document: &doc.Document, Fields: doc.Fields, Identifiers: doc.Identifiers})
}

func (b *OpenSearchBackend) putDocument(ctx context.Context, doc *Document, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("opensearch index: marshal document: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s/_doc/%s-%s-%s", b.cfg.URL, b.cfg.Index, doc.OrganizationID, doc.EntityType, doc.EntityID), bytes.NewReader(raw))
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
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/%s/_doc/%s-%s-%s", b.cfg.URL, b.cfg.Index, orgID, entityType, entityID), http.NoBody)
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

// DeleteTenant removes every document of the organization from the index
// (TLC-06, WP-1155): _delete_by_query on organization_id, refreshed and
// proceeding over version conflicts; it returns the number deleted. A
// missing index deletes nothing. Repeated it deletes nothing more.
func (b *OpenSearchBackend) DeleteTenant(ctx context.Context, orgID string) (int64, error) {
	if strings.TrimSpace(orgID) == "" {
		return 0, fmt.Errorf("opensearch delete tenant: organization is required")
	}
	body, err := json.Marshal(map[string]any{"query": map[string]any{"term": map[string]any{"organization_id": orgID}}})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s/_delete_by_query?conflicts=proceed&refresh=true", b.cfg.URL, b.cfg.Index), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	b.auth(req)
	res, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	if res.StatusCode >= 300 {
		return 0, fmt.Errorf("opensearch delete tenant failed: %s", res.Status)
	}
	var out struct {
		Deleted  int64 `json:"deleted"`
		Failures []any `json:"failures"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, b.cfg.MaxResponseBytes)).Decode(&out); err != nil {
		return 0, fmt.Errorf("opensearch delete tenant: %w", err)
	}
	if len(out.Failures) > 0 {
		return out.Deleted, fmt.Errorf("opensearch delete tenant: %d failures", len(out.Failures))
	}
	return out.Deleted, nil
}

func (b *OpenSearchBackend) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	return ReindexResult{}, fmt.Errorf("opensearch reindex requires the PostgreSQL coordinator")
}

// buildOpenSearchQuery builds the search request. The scope filters come
// from the principal's tenant scope, never from the request: client and site
// must match or be unset (E-09), and entity types whose visibility depends on
// more than the indexed fields are left out for restricted principals
// (documents: their links; tickets: the team).
//
// The text matches the title, the summary, the metadata and the field rows
// (fields.*), and through search.ParseIdentifiers the identifiers that
// count for a hit like in the PostgreSQL index (openIdentifierFields:
// network.* only with Query.NetworkReadable; SRC-02, WP-787). The filters
// apply to every hit, however it matched. With highlighting the title,
// the summary and the field rows are highlighted and the matched
// identifiers come back as inner hits (matched_fields).
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
				map[string]any{"terms": map[string]any{field: api.NonNilSlice(set.IDs())}},
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
	text := strings.TrimSpace(q.Text) != ""
	if text {
		should := []any{map[string]any{"multi_match": map[string]any{"query": q.Text,
			"fields": []string{"title^3", "summary", "metadata.*", remoteFieldRows + ".*"}, "type": "best_fields"}}}
		if ids, ok := ParseIdentifiers(q.Text); ok {
			if fields := openIdentifierFields(q, nil); len(fields) > 0 {
				should = append(should, identifierQuery(&ids, fields, q.Highlight))
			}
		}
		// The users the text resolved to (HybridBackend, WP-788).
		if fields := openUserFields(q, nil); len(q.userIDs) > 0 && len(fields) > 0 {
			should = append(should, userQuery(fields, q.userIDs, q.Highlight))
		}
		must = append(must, map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}})
	}
	if len(must) == 0 {
		must = append(must, map[string]any{"match_all": map[string]any{}})
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	body := map[string]any{
		"from": q.Offset, "size": limit,
		"_source": map[string]any{"excludes": []string{remoteFieldRows, remoteIdentifiers}},
		"query":   map[string]any{"bool": map[string]any{"filter": filters, "must": must}},
	}
	if q.Highlight {
		body["highlight"] = map[string]any{"fields": map[string]any{
			"title":   map[string]any{},
			"summary": map[string]any{},
			remoteFieldRows + ".*": map[string]any{
				"pre_tags": []string{remoteHighlightPre}, "post_tags": []string{remoteHighlightEnd}, "number_of_fragments": 2,
			},
		}}
	}
	return body, nil
}

// identifierInnerHits is the name of the inner hits of the identifier
// query and the most identifiers one hit names.
const (
	identifierInnerHits    = "identifiers"
	identifierInnerHitsMax = 20
)

// identifierQuery is the nested query on the identifiers of the fields that
// meets the identifier conditions like identifierCond of the PostgreSQL
// index: equality on norm or addr, a range of addr, a prefix of norm (for
// the MAC fields the prefix of the hex digits). With inner the matched
// identifiers come back as inner hits.
func identifierQuery(ids *Identifiers, fields []string, inner bool) map[string]any {
	const norm, addr, field = remoteIdentifiers + ".norm", remoteIdentifiers + ".addr", remoteIdentifiers + ".field"
	var cond any
	switch {
	case ids.MAC != "":
		cond = map[string]any{"term": map[string]any{norm: ids.MAC}}
	case ids.Addr != "":
		cond = map[string]any{"term": map[string]any{addr: ids.Addr}}
	case ids.Low != "":
		cond = map[string]any{"range": map[string]any{addr: map[string]any{"gte": ids.Low, "lte": ids.High}}}
	default:
		cond = map[string]any{"bool": map[string]any{
			"filter":   []any{map[string]any{"prefix": map[string]any{norm: ids.Prefix}}},
			"must_not": []any{map[string]any{"terms": map[string]any{field: macFields}}},
		}}
		if ids.MACPrefix != "" {
			cond = map[string]any{"bool": map[string]any{"should": []any{cond, map[string]any{"bool": map[string]any{
				"filter": []any{
					map[string]any{"prefix": map[string]any{norm: ids.MACPrefix}},
					map[string]any{"terms": map[string]any{field: macFields}},
				},
			}}}, "minimum_should_match": 1}}
		}
	}
	nested := map[string]any{
		"path": remoteIdentifiers,
		"query": map[string]any{"constant_score": map[string]any{"filter": map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"terms": map[string]any{field: fields}}, cond},
		}}}},
		"score_mode": "max",
	}
	if inner {
		nested["inner_hits"] = map[string]any{
			"name": identifierInnerHits, "size": identifierInnerHitsMax, "_source": false,
			"docvalue_fields": []string{field, norm},
		}
	}
	return map[string]any{"nested": nested}
}

// userInnerHits is the name of the inner hits of the user query.
const userInnerHits = "users"

// userQuery is the nested query on the user fields of the identifiers that
// hold one of the users (userIn of the PostgreSQL index, WP-788). With
// inner the matched fields come back as inner hits.
func userQuery(fields, users []string, inner bool) map[string]any {
	const norm, field = remoteIdentifiers + ".norm", remoteIdentifiers + ".field"
	nested := map[string]any{
		"path": remoteIdentifiers,
		"query": map[string]any{"constant_score": map[string]any{"filter": map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"terms": map[string]any{field: fields}},
				map[string]any{"terms": map[string]any{norm: users}},
			},
		}}}},
		"score_mode": "max",
	}
	if inner {
		nested["inner_hits"] = map[string]any{
			"name": userInnerHits, "size": identifierInnerHitsMax, "_source": false,
			"docvalue_fields": []string{field},
		}
	}
	return map[string]any{"nested": nested}
}

// openSearchHit is a hit of the answer of _search.
type openSearchHit struct {
	Score     float64             `json:"_score"`
	Source    Document            `json:"_source"`
	Highlight map[string][]string `json:"highlight"`
	InnerHits map[string]struct {
		Hits struct {
			Hits []struct {
				Fields map[string][]any `json:"fields"`
				Source *IdentifierRow   `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	} `json:"inner_hits"`
}

// Query searches the remote index under the tenant scope of ctx.
//
// The index has no field permissions (TEC-12 [P5]): with restricted field
// permissions it would match fields the principal may not read, so it
// answers no hits (fail-closed, RBA-07, WP-266); HybridBackend answers such
// a query from PostgreSQL.
func (b *OpenSearchBackend) Query(ctx context.Context, q *Query) (Result, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return Result{}, database.ErrNoTenantScope
	}
	if len(q.restrictedTypes()) > 0 {
		if err := validateQuery(q.Text); err != nil {
			return Result{}, err
		}
		return Result{Data: []Hit{}, Limit: q.Limit, Offset: q.Offset}, nil
	}
	body, err := buildOpenSearchQuery(q, &scope)
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
			Total any             `json:"total"`
			Hits  []openSearchHit `json:"hits"`
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
	withFields := q.Highlight && strings.TrimSpace(q.Text) != ""
	open := openIdentifierFields(q, nil)
	openUsers := openUserFields(q, nil)
	for i := range parsed.Hits.Hits {
		item := &parsed.Hits.Hits[i]
		// The filters run in OpenSearch; the check repeats them on the
		// returned documents so a mapping error cannot leak hits.
		if !visibleTo(&scope, q.OrganizationID, &item.Source) {
			continue
		}
		item.Source.Fields = nil
		h := Hit{Document: item.Source, Score: item.Score}
		for _, key := range []string{"title", "summary"} {
			h.Highlights = append(h.Highlights, item.Highlight[key]...)
		}
		if withFields {
			h.MatchedFields = openSearchMatchedFields(item, open, openUsers)
		}
		out.Data = append(out.Data, h)
	}
	out.HasMore = out.Offset+len(out.Data) < out.Total
	return out, nil
}

// openSearchMatchedFields returns the matched fields of a hit like the
// PostgreSQL index (matchedFields, WP-786): the title, the summary of a type
// without field rows and the field rows with their highlight (marked with
// <mark>, fragments joined like ts_headline), and the identifiers of the
// inner hits with their value in canonical form, only of the fields that
// count for a hit (open), and the user fields of the user inner hits
// without highlight (openUsers, WP-788). A contact hit names no field.
func openSearchMatchedFields(item *openSearchHit, open, openUsers []string) []MatchedField {
	entityType := item.Source.EntityType
	if entityType == "contact" {
		return nil
	}
	var out []MatchedField
	for key, fragments := range item.Highlight {
		field := key
		switch {
		case key == "title":
		case key == "summary":
			if slices.Contains(fieldTypes, entityType) {
				continue
			}
		case strings.HasPrefix(key, remoteFieldRows+"."):
			field = strings.TrimPrefix(key, remoteFieldRows+".")
		default:
			continue
		}
		highlight := strings.Join(fragments, " ... ")
		if field == key {
			// Title and summary keep the default tags of Hit.Highlights.
			highlight = defaultHighlightTags.Replace(highlight)
		}
		out = append(out, MatchedField{Field: field, Highlight: highlight})
	}
	seen := map[MatchedField]bool{}
	for _, inner := range item.InnerHits[identifierInnerHits].Hits.Hits {
		var ident IdentifierRow
		if inner.Source != nil {
			ident = *inner.Source
		}
		if v := firstString(inner.Fields[remoteIdentifiers+".field"]); v != "" {
			ident.Field = v
		}
		if v := firstString(inner.Fields[remoteIdentifiers+".norm"]); v != "" {
			ident.Norm = v
		}
		if ident.Norm == "" || !slices.Contains(open, ident.Field) || !slices.Contains(identifierFields[entityType], ident.Field) {
			continue
		}
		m := MatchedField{Field: ident.Field, Highlight: ident.Norm}
		if slices.Contains(macFields, m.Field) {
			m.Highlight = canonicalMAC(m.Highlight)
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, inner := range item.InnerHits[userInnerHits].Hits.Hits {
		f := firstString(inner.Fields[remoteIdentifiers+".field"])
		if f == "" && inner.Source != nil {
			f = inner.Source.Field
		}
		m := MatchedField{Field: f}
		if f == "" || seen[m] || !slices.Contains(openUsers, f) || !slices.Contains(userFields[entityType], f) {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sortMatchedFields(entityType, out)
	return out
}

// defaultHighlightTags rewrites the default highlight tags of OpenSearch,
// which Hit.Highlights keeps, to the ones of matched_fields.
var defaultHighlightTags = strings.NewReplacer("<em>", remoteHighlightPre, "</em>", remoteHighlightEnd)

// firstString returns the first value of a docvalue field as text.
func firstString(values []any) string {
	if len(values) == 0 {
		return ""
	}
	s, _ := values[0].(string)
	return s
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

func (b *OpenSearchBackend) auth(req *http.Request) {
	if b.cfg.Username != "" {
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	}
}
