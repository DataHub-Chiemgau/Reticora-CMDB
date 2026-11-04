// Tag REST (CI-08, API-06; WP-753, E-38, E-105, E-125): the tag catalog,
// the merge of two tags and PUT /cis/{id}/tags.
//
//   - TestTagCatalogAgainstPostgres: GET /tags sorted by key with the
//     keyset cursor; POST with 409 on a taken key and 422 on invalid
//     values; GET/PATCH (a sent key is 422); DELETE refuses a system tag and
//     an assigned tag (409); audit tag.created, tag.updated, tag.deleted.
//   - TestTagMergeAgainstPostgres: the assignments move to the target tag,
//     a duplicate drops, the source tag goes; per live CI version + 1,
//     ci_change, audit ci.updated and the event ci.changed; a deleted CI
//     keeps the target tag; audit tag.merged; a system tag as source is
//     409, an unknown target 422.
//   - TestReplaceCITagsAgainstPostgres: PUT /cis/{id}/tags with ci_change,
//     audit, outbox event, version and ETag, If-Match 412/409, unknown key
//     422, no override in ci_field_value, a client-scoped principal (404
//     outside its client).
//   - TestTagStoreWiring (no database): the memory repository serves the
//     routes, the indexing decorator forwards the tag store and indexes the
//     CI after PUT /cis/{id}/tags and after a merge.
//   - TestTagSpecification (no database): the schema CI of api/openapi.yaml
//     and the generated schema.d.ts carry tags; attributes.tags of POST and
//     PATCH /cis is deprecated with the sunset ci/handler.go sends.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestTag|TestReplaceCITags' ./internal/ci/
package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// tagAPI serves the CI routes of repo for the principal of ctx.
type tagAPI struct {
	t   *testing.T
	mux *chi.Mux
	ctx context.Context
}

func newTagAPI(t *testing.T, ctx context.Context, repo ci.Repository) *tagAPI {
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	return &tagAPI{t: t, mux: mux, ctx: ctx}
}

func (a *tagAPI) call(method, path, body string, header ...string) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(a.ctx)
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, r)
	return w
}

// expect fails unless w has the status; it decodes the body into out.
func (a *tagAPI) expect(step string, w *httptest.ResponseRecorder, status int, out any) {
	a.t.Helper()
	if w.Code != status {
		a.t.Fatalf("%s: %d %s, want %d", step, w.Code, w.Body.String(), status)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			a.t.Fatalf("%s: decode %s: %v", step, w.Body.String(), err)
		}
	}
}

// violationFields returns the fields of a problem with violations.
func violationFields(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var problem struct {
		Violations []struct {
			Field string `json:"field"`
		} `json:"violations"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem %s: %v", w.Body.String(), err)
	}
	var fields []string
	for _, v := range problem.Violations {
		fields = append(fields, v.Field)
	}
	if problem.Field != "" {
		fields = append(fields, problem.Field)
	}
	return fields
}

// assignTags gives a CI catalog tags through the maintenance pool.
func assignTags(t *testing.T, f *scopetest.Fixture, ciID string, keys ...string) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci_tag (organization_id, ci_id, tag_id)
		SELECT t.organization_id, $1, t.id FROM tag t JOIN ci ON ci.id = $1 WHERE t.organization_id = ci.organization_id AND t.key = ANY($2)`,
		ciID, keys); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, f *scopetest.Fixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestTagCatalogAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ea")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	a := newTagAPI(t, ctx, ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))

	var created ci.Tag
	a.expect("POST", a.call(http.MethodPost, "/api/v1/tags", `{"key":"prod","display_name":{"de":"Produktiv","en":"Production"},"color":"#00aa00"}`),
		http.StatusCreated, &created)
	if created.ID == "" || created.Key != "prod" || created.DisplayName["de"] != "Produktiv" || created.Color == nil || created.IsSystem {
		t.Errorf("created: %+v", created)
	}
	var plain ci.Tag
	a.expect("POST without display_name", a.call(http.MethodPost, "/api/v1/tags", `{"key":"dmz"}`), http.StatusCreated, &plain)
	if plain.DisplayName["en"] != "dmz" || plain.Color != nil {
		t.Errorf("defaults: %+v", plain)
	}

	// A taken key is 409 on key; invalid values are 422 with the fields.
	w := a.call(http.MethodPost, "/api/v1/tags", `{"key":"prod"}`)
	if w.Code != http.StatusConflict || !slices.Equal(violationFields(t, w), []string{"key"}) {
		t.Errorf("taken key: %d %s", w.Code, w.Body.String())
	}
	w = a.call(http.MethodPost, "/api/v1/tags", `{"key":"Bad Key","display_name":{"fr":"x","de":""},"color":"red"}`)
	if got := violationFields(t, w); w.Code != http.StatusUnprocessableEntity ||
		!slices.Equal(got, []string{"key", "display_name.de", "display_name.fr", "color"}) {
		t.Errorf("invalid values: %d %v", w.Code, got)
	}

	// The list: the system tag archived and the two tags, sorted by key,
	// two pages of two over the cursor.
	var page api2Page
	a.expect("list", a.call(http.MethodGet, "/api/v1/tags?limit=2", ""), http.StatusOK, &page)
	if page.Total != 3 || len(page.Data) != 2 || page.Data[0].Key != "archived" || page.Data[1].Key != "dmz" || page.NextCursor == "" {
		t.Fatalf("first page: %+v", page)
	}
	var next api2Page
	a.expect("list 2", a.call(http.MethodGet, "/api/v1/tags?limit=2&cursor="+page.NextCursor, ""), http.StatusOK, &next)
	if len(next.Data) != 1 || next.Data[0].Key != "prod" || next.HasMore {
		t.Errorf("second page: %+v", next)
	}
	if w = a.call(http.MethodGet, "/api/v1/tags?cursor=forged", ""); w.Code != http.StatusBadRequest {
		t.Errorf("forged cursor: %d", w.Code)
	}

	var got ci.Tag
	a.expect("GET", a.call(http.MethodGet, "/api/v1/tags/"+created.ID, ""), http.StatusOK, &got)
	if got.Key != "prod" {
		t.Errorf("GET: %+v", got)
	}
	if w = a.call(http.MethodGet, "/api/v1/tags/"+f.ID(), ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown tag: %d", w.Code)
	}

	// PATCH changes names and color, null removes the color; the key is
	// unchangeable.
	var patched ci.Tag
	a.expect("PATCH", a.call(http.MethodPatch, "/api/v1/tags/"+created.ID, `{"display_name":{"en":"Prod"},"color":null}`), http.StatusOK, &patched)
	if patched.Color != nil || patched.DisplayName["en"] != "Prod" || patched.DisplayName["de"] != "" || patched.Key != "prod" {
		t.Errorf("PATCH: %+v", patched)
	}
	w = a.call(http.MethodPatch, "/api/v1/tags/"+created.ID, `{"key":"other"}`)
	if w.Code != http.StatusUnprocessableEntity || !slices.Equal(violationFields(t, w), []string{"key"}) {
		t.Errorf("PATCH key: %d %s", w.Code, w.Body.String())
	}

	// DELETE: a system tag and an assigned tag are 409; an assigned tag of
	// a deleted CI counts.
	var archived string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM tag WHERE organization_id = $1 AND key = 'archived'`, f.OrgA).
		Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if w = a.call(http.MethodDelete, "/api/v1/tags/"+archived, ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE archived: %d %s", w.Code, w.Body.String())
	}
	gone := f.CI(t, f.OrgA, f.Client1, "tag-catalog-deleted")
	assignTags(t, f, gone, "prod")
	if _, err := f.Admin.Exec(context.Background(), `UPDATE ci SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if w = a.call(http.MethodDelete, "/api/v1/tags/"+created.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE assigned: %d %s", w.Code, w.Body.String())
	}
	if w = a.call(http.MethodDelete, "/api/v1/tags/"+plain.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE: %d %s", w.Code, w.Body.String())
	}
	if w = a.call(http.MethodDelete, "/api/v1/tags/"+plain.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("DELETE again: %d", w.Code)
	}

	for action, id := range map[string]string{"tag.created": created.ID, "tag.updated": created.ID, "tag.deleted": plain.ID} {
		if n := countRows(t, f, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = $2 AND resource_type = 'tag'
			AND resource_id = $3`, f.OrgA, action, id); n == 0 {
			t.Errorf("no audit entry %s", action)
		}
	}
}

// api2Page is a page of GET /tags.
type api2Page struct {
	Data       []ci.Tag `json:"data"`
	Total      int      `json:"total"`
	HasMore    bool     `json:"has_more"`
	NextCursor string   `json:"next_cursor"`
}

func TestTagMergeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ea")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	a := newTagAPI(t, ctx, repo)

	var source, into ci.Tag
	a.expect("POST source", a.call(http.MethodPost, "/api/v1/tags", `{"key":"prod_old"}`), http.StatusCreated, &source)
	a.expect("POST into", a.call(http.MethodPost, "/api/v1/tags", `{"key":"prod"}`), http.StatusCreated, &into)
	both := f.CI(t, f.OrgA, f.Client1, "tag-merge-both")
	only := f.CI(t, f.OrgA, f.Client2, "tag-merge-only")
	gone := f.CI(t, f.OrgA, f.Client1, "tag-merge-deleted")
	other := f.CI(t, f.OrgA, f.Client1, "tag-merge-other")
	assignTags(t, f, both, "prod_old", "prod", "archived")
	assignTags(t, f, only, "prod_old")
	assignTags(t, f, gone, "prod_old")
	assignTags(t, f, other, "archived")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	version := func(id string) (v int64) {
		if err := f.Admin.QueryRow(bg, `SELECT version FROM ci WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := map[string]int64{both: version(both), only: version(only), other: version(other)}

	// The source and the target must differ; a system tag is no source.
	if w := a.call(http.MethodPost, "/api/v1/tags/"+source.ID+"/merge", `{"into_id":"`+f.ID()+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown target: %d %s", w.Code, w.Body.String())
	}
	if w := a.call(http.MethodPost, "/api/v1/tags/"+source.ID+"/merge", `{"into_id":"`+source.ID+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("merge into itself: %d %s", w.Code, w.Body.String())
	}
	var archived string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM tag WHERE organization_id = $1 AND key = 'archived'`, f.OrgA).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if w := a.call(http.MethodPost, "/api/v1/tags/"+archived+"/merge", `{"into_id":"`+into.ID+`"}`); w.Code != http.StatusConflict {
		t.Errorf("merge a system tag: %d %s", w.Code, w.Body.String())
	}

	var result struct {
		Tag   ci.Tag `json:"tag"`
		Moved int    `json:"moved"`
	}
	a.expect("merge", a.call(http.MethodPost, "/api/v1/tags/"+source.ID+"/merge", `{"into_id":"`+into.ID+`"}`), http.StatusOK, &result)
	if result.Tag.ID != into.ID || result.Moved != 2 {
		t.Errorf("merge result: %+v", result)
	}
	for id, want := range map[string][]string{both: {"archived", "prod"}, only: {"prod"}, gone: {"prod"}, other: {"archived"}} {
		if got := ciTagKeys(t, f, id); !slices.Equal(got, want) {
			t.Errorf("tags of %s: %v, want %v", id, got, want)
		}
	}
	if n := countRows(t, f, `SELECT count(*) FROM tag WHERE id = $1`, source.ID); n != 0 {
		t.Error("the source tag remains")
	}
	// Per live CI: version + 1, ci_change, audit ci.updated, event
	// ci.changed with the field tags; nothing for an untouched CI.
	for _, id := range []string{both, only} {
		if got := version(id); got != before[id]+1 {
			t.Errorf("version of %s: %d, want %d", id, got, before[id]+1)
		}
		var oldValue, newValue string
		if err := f.Admin.QueryRow(bg, `SELECT old_value::text, new_value::text FROM ci_change WHERE ci_id = $1 AND field_name = 'tags'`, id).
			Scan(&oldValue, &newValue); err != nil {
			t.Errorf("ci_change of %s: %v", id, err)
		} else if !strings.Contains(oldValue, "prod_old") || strings.Contains(newValue, "prod_old") || !strings.Contains(newValue, "prod") {
			t.Errorf("ci_change of %s: %s -> %s", id, oldValue, newValue)
		}
		if n := countRows(t, f, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.updated'`, id); n != 1 {
			t.Errorf("audit ci.updated of %s: %d", id, n)
		}
		if n := countRows(t, f, `SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'
			AND data -> 'changed_fields' = '["tags"]'::jsonb`, id); n != 1 {
			t.Errorf("event ci.changed of %s: %d", id, n)
		}
	}
	if got := version(other); got != before[other] {
		t.Errorf("an untouched CI changed its version: %d", got)
	}
	if n := countRows(t, f, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'tag.merged' AND resource_id = $2
		AND changes -> 'into' ->> 'key' = 'prod'`, f.OrgA, source.ID); n != 1 {
		t.Errorf("audit tag.merged: %d", n)
	}
}

func TestReplaceCITagsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ea")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	a := newTagAPI(t, ctx, repo)
	for _, key := range []string{"prod", "dmz", "web"} {
		a.expect("POST "+key, a.call(http.MethodPost, "/api/v1/tags", `{"key":"`+key+`"}`), http.StatusCreated, nil)
	}
	id := f.CI(t, f.OrgA, f.Client1, "tag-put")

	w := a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":["web","prod","web"]}`, "If-Match", `"1"`)
	var item ci.Item
	a.expect("PUT", w, http.StatusOK, &item)
	if !slices.Equal(item.Tags, []string{"prod", "web"}) || item.Version != 2 || w.Header().Get("ETag") != `"2"` {
		t.Errorf("PUT: tags %v version %d etag %s", item.Tags, item.Version, w.Header().Get("ETag"))
	}
	if got := ciTagKeys(t, f, id); !slices.Equal(got, []string{"prod", "web"}) {
		t.Errorf("ci_tag: %v", got)
	}
	var oldValue, newValue string
	if err := f.Admin.QueryRow(bg, `SELECT old_value::text, new_value::text FROM ci_change WHERE ci_id = $1 AND field_name = 'tags'`, id).
		Scan(&oldValue, &newValue); err != nil {
		t.Fatalf("ci_change: %v", err)
	}
	if strings.ReplaceAll(oldValue, " ", "") != `[]` || strings.ReplaceAll(newValue, " ", "") != `["prod","web"]` {
		t.Errorf("ci_change: %s -> %s", oldValue, newValue)
	}
	if n := countRows(t, f, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.updated'
		AND changes -> 'fields' @> '[{"field":"tags"}]'::jsonb`, id); n != 1 {
		t.Errorf("audit ci.updated: %d", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'
		AND data -> 'changed_fields' = '["tags"]'::jsonb`, id); n != 1 {
		t.Errorf("event ci.changed: %d", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1`, id); n != 0 {
		t.Errorf("%d field values (overrides) after PUT", n)
	}

	// An unchanged list keeps the version.
	a.expect("PUT unchanged", a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":["prod","web"]}`), http.StatusOK, &item)
	if item.Version != 2 {
		t.Errorf("unchanged PUT version %d", item.Version)
	}

	// If-Match: an unknown version is 412; a stale version whose tags
	// changed since is 409 with the field; a required If-Match is 412.
	if w = a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":[]}`, "If-Match", `"99"`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("unknown If-Match: %d %s", w.Code, w.Body.String())
	}
	w = a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":["dmz"]}`, "If-Match", `"1"`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"fields":["tags"]`) {
		t.Errorf("stale If-Match: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`,
		f.OrgA); err != nil {
		t.Fatal(err)
	}
	if w = a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":["dmz"]}`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("missing required If-Match: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET settings = settings - 'require_if_match' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}

	// Unknown keys are 422 per entry; nothing is created.
	w = a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":["prod","nope","Not A Key"]}`)
	if got := violationFields(t, w); w.Code != http.StatusUnprocessableEntity || !slices.Equal(got, []string{"tags[2]", "tags[1]"}) &&
		!slices.Equal(got, []string{"tags[1]", "tags[2]"}) {
		t.Errorf("unknown keys: %d %v", w.Code, got)
	}
	if n := countRows(t, f, `SELECT count(*) FROM tag WHERE organization_id = $1 AND key = 'nope'`, f.OrgA); n != 0 {
		t.Error("an unknown key created a tag")
	}
	if w = a.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("without tags: %d", w.Code)
	}
	if w = a.call(http.MethodPut, "/api/v1/cis/"+f.ID()+"/tags", `{"tags":[]}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown CI: %d", w.Code)
	}

	// A client-scoped principal tags the CIs of its client; another
	// client's CI is 404.
	scoped := newTagAPI(t, tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}), repo)
	a.expect("scoped PUT", scoped.call(http.MethodPut, "/api/v1/cis/"+id+"/tags", `{"tags":["dmz"]}`), http.StatusOK, &item)
	if !slices.Equal(item.Tags, []string{"dmz"}) {
		t.Errorf("scoped PUT: %v", item.Tags)
	}
	foreign := f.CI(t, f.OrgA, f.Client2, "tag-put-foreign")
	if w = scoped.call(http.MethodPut, "/api/v1/cis/"+foreign+"/tags", `{"tags":["dmz"]}`); w.Code != http.StatusNotFound {
		t.Errorf("foreign client: %d %s", w.Code, w.Body.String())
	}
	if got := ciTagKeys(t, f, foreign); len(got) != 0 {
		t.Errorf("foreign CI got tags %v", got)
	}
}

// recordingIndexer records the CIs it indexes.
type recordingIndexer struct {
	mu      sync.Mutex
	indexed []string
}

func (x *recordingIndexer) IndexDocument(_ context.Context, doc *ci.Document) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.indexed = append(x.indexed, doc.EntityID)
	return nil
}

func (x *recordingIndexer) DeleteDocument(context.Context, string, string, string) error { return nil }

func (x *recordingIndexer) take() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := x.indexed
	x.indexed = nil
	return out
}

func TestTagStoreWiring(t *testing.T) {
	// The production wiring: the PostgreSQL repository behind the indexing
	// decorator keeps the tag store (SR-11).
	if _, ok := ci.NewIndexingRepository(ci.NewPGRepository(nil), &recordingIndexer{}).(ci.TagStore); !ok {
		t.Fatal("the indexing decorator hides the tag store of the PostgreSQL repository")
	}

	const org = "00000000-0000-4000-8000-0000000000aa"
	indexer := &recordingIndexer{}
	repo := ci.NewIndexingRepository(ci.NewMemoryRepository(), indexer)
	if _, ok := repo.(ci.TagStore); !ok {
		t.Fatal("the indexing decorator hides the tag store of the memory repository")
	}
	a := newTagAPI(t, tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: "u"}), repo)
	var page api2Page
	a.expect("list", a.call(http.MethodGet, "/api/v1/tags", ""), http.StatusOK, &page)
	if len(page.Data) != 1 || page.Data[0].Key != "archived" || !page.Data[0].IsSystem {
		t.Errorf("memory catalog: %+v", page.Data)
	}
	var source, into ci.Tag
	a.expect("POST", a.call(http.MethodPost, "/api/v1/tags", `{"key":"old"}`), http.StatusCreated, &source)
	a.expect("POST", a.call(http.MethodPost, "/api/v1/tags", `{"key":"new"}`), http.StatusCreated, &into)
	if w := a.call(http.MethodPost, "/api/v1/tags", `{"key":"old"}`); w.Code != http.StatusConflict {
		t.Errorf("taken key: %d", w.Code)
	}
	var created ci.Item
	a.expect("POST /cis", a.call(http.MethodPost, "/api/v1/cis", `{"name":"wired","ci_type_id":"00000000-0000-4000-8000-0000000000b1"}`), http.StatusCreated, &created)
	indexer.take()

	var item ci.Item
	w := a.call(http.MethodPut, "/api/v1/cis/"+created.ID+"/tags", `{"tags":["old"]}`)
	a.expect("PUT", w, http.StatusOK, &item)
	if !slices.Equal(item.Tags, []string{"old"}) || item.Version != 2 || w.Header().Get("ETag") != `"2"` {
		t.Errorf("PUT: %v version %d", item.Tags, item.Version)
	}
	if got := indexer.take(); !slices.Equal(got, []string{created.ID}) {
		t.Errorf("indexed after PUT: %v", got)
	}
	if w = a.call(http.MethodPut, "/api/v1/cis/"+created.ID+"/tags", `{"tags":["nope"]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown key: %d", w.Code)
	}
	if w = a.call(http.MethodPut, "/api/v1/cis/"+created.ID+"/tags", `{"tags":[]}`, "If-Match", `"1"`); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d", w.Code)
	}
	if w = a.call(http.MethodDelete, "/api/v1/tags/"+source.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE assigned: %d", w.Code)
	}
	a.expect("merge", a.call(http.MethodPost, "/api/v1/tags/"+source.ID+"/merge", `{"into_id":"`+into.ID+`"}`), http.StatusOK, nil)
	if got := indexer.take(); !slices.Equal(got, []string{created.ID}) {
		t.Errorf("indexed after merge: %v", got)
	}
	a.expect("GET", a.call(http.MethodGet, "/api/v1/cis/"+created.ID, ""), http.StatusOK, &item)
	if !slices.Equal(item.Tags, []string{"new"}) || item.Version != 3 {
		t.Errorf("after merge: %v version %d", item.Tags, item.Version)
	}
	if w = a.call(http.MethodGet, "/api/v1/tags/"+source.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("merged source: %d", w.Code)
	}
}

func TestTagSpecification(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					ReadOnly   bool `yaml:"readOnly"`
					Properties map[string]struct {
						Deprecated bool   `yaml:"deprecated"`
						Sunset     string `yaml:"x-sunset"`
					} `yaml:"properties"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if tags, ok := doc.Components.Schemas["CI"].Properties["tags"]; !ok || !tags.ReadOnly {
		t.Error("the schema CI has no read-only tags")
	}

	// The sunset of the deprecated attributes.tags is the one the handler
	// sends.
	a := newTagAPI(t, tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "o", UserID: "u"}), ci.NewMemoryRepository())
	w := a.call(http.MethodPost, "/api/v1/cis", `{"name":"legacy","ci_type_id":"00000000-0000-4000-8000-0000000000b1","attributes":{"tags":["x"]}}`)
	sunset, err := http.ParseTime(w.Header().Get("Sunset"))
	if err != nil {
		t.Fatalf("Sunset header %q: %v", w.Header().Get("Sunset"), err)
	}
	for _, schema := range []string{"CreateCIRequest", "UpdateCIRequest"} {
		legacy := doc.Components.Schemas[schema].Properties["attributes"].Properties["tags"]
		if !legacy.Deprecated || legacy.Sunset != sunset.UTC().Format(time.DateOnly) {
			t.Errorf("%s attributes.tags: deprecated %v, x-sunset %q, handler %s", schema, legacy.Deprecated, legacy.Sunset, sunset)
		}
	}

	// The generated types carry the field.
	generated, err := os.ReadFile("../../../frontend/src/api/generated/schema.d.ts")
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	start := strings.Index(text, "\n    CI: {\n")
	end := strings.Index(text[start+1:], "\n    };\n")
	if start < 0 || end < 0 || !strings.Contains(text[start:start+1+end], "readonly tags?: string[];") {
		t.Error("schema.d.ts: the type CI has no tags")
	}
}
