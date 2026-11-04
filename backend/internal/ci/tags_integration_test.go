// Tag catalog and tag assignments of CIs (CI-07, CI-08, CI-12; WP-281,
// E-125, migration 000218) against PostgreSQL:
//
//   - TestCITagScopeAgainstPostgres: ci_tag takes client and site of its CI
//     and follows a client change of the CI; a principal of another client
//     cannot assign a tag to the CI.
//   - TestCITagBackfillAgainstPostgres: the backfill section of migration
//     000218, executed from the migration file: override before
//     attributes.tags, only string elements, key rule and collisions,
//     deleted CIs, open override_conflict reviews, open merge snapshots (the
//     unmerge afterwards works), version, schema_version and updated_at
//     unchanged, normalized saved views with the same CIs before and after.
//   - TestCITagMergeAndUnmergeAgainstPostgres: the unmerge brings back the
//     tag assignments of survivor and victim.
//   - TestCITagsThroughTheAPIAgainstPostgres: the system tag archived of a
//     new organization; tags in GET, list, fields=tags and the PATCH answer;
//     attributes.tags of POST and PATCH writes ci_tag, creates unknown tags,
//     leaves attributes without tags and carries Deprecation and Sunset; a
//     value that is no array of strings is 400; PUT
//     /cis/{id}/fields/tags/override is 422; an ingest record with
//     attributes.tags sets the assignment through the reconciliation.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestCITag' ./internal/ci/
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
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// ciTagKeys returns the tag keys of a CI from the catalog, sorted.
func ciTagKeys(t *testing.T, f *scopetest.Fixture, ciID string) []string {
	t.Helper()
	var keys []string
	if err := f.Admin.QueryRow(context.Background(), `SELECT COALESCE(array_agg(t.key ORDER BY t.key), '{}')
		FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id WHERE ct.ci_id = $1`, ciID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestCITagScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e7")
	bg := context.Background()
	repo := ci.NewPGRepository(f.App)
	own := f.CI(t, f.OrgA, f.Client1, "tag-scope-own")
	foreign := f.CI(t, f.OrgA, f.Client2, "tag-scope-foreign")

	tags := []string{"Scope"}
	item, err := repo.Update(f.ClientCtx(f.Client1), f.OrgA, own, ci.UpdateRequest{LegacyTags: &tags})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !slices.Equal(item.Tags, []string{"scope"}) {
		t.Errorf("tags of the answer: %v", item.Tags)
	}
	scopeOf := func() (client string) {
		t.Helper()
		if err = f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM ci_tag WHERE ci_id = $1`, own).Scan(&client); err != nil {
			t.Fatal(err)
		}
		return client
	}
	if got := scopeOf(); got != f.Client1 {
		t.Errorf("client of the assignment: %q, want %s", got, f.Client1)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, own, f.Client2); err != nil {
		t.Fatal(err)
	}
	if got := scopeOf(); got != f.Client2 {
		t.Errorf("client of the assignment after the client change: %q, want %s", got, f.Client2)
	}

	// A principal of client 1 cannot assign a tag to a CI of client 2:
	// the CI is invisible, the derived scope is empty and WITH CHECK fails.
	err = database.WithRequestTenant(f.ClientCtx(f.Client1), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `INSERT INTO ci_tag (organization_id, ci_id, tag_id)
			SELECT $1, $2, id FROM tag WHERE organization_id = $1 AND key = 'archived'`, f.OrgA, foreign)
		return execErr
	})
	if err == nil {
		t.Error("a principal of client 1 assigned a tag to a CI of client 2")
	}
	if _, err = repo.Update(f.ClientCtx(f.Client1), f.OrgA, foreign, ci.UpdateRequest{LegacyTags: &tags}); err == nil {
		t.Error("a principal of client 1 tagged a CI of client 2 through the repository")
	}
	if keys := ciTagKeys(t, f, foreign); len(keys) != 0 {
		t.Errorf("tags of the foreign CI: %v", keys)
	}
}

// backfillSection is the backfill of migration 000218 between its markers.
func backfillSection(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/000218_tags.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "-- ─── 5. Backfill")
	end := strings.Index(text, "-- ─── end of backfill")
	if start < 0 || end < start {
		t.Fatal("backfill markers not found in 000218_tags.up.sql")
	}
	return text[start:end]
}

func TestCITagBackfillAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e8")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, query, args...); execErr != nil {
			t.Fatalf("%s: %v", query, execErr)
		}
	}
	// The state before the migration: tags in attributes and as field
	// values.
	exec(`ALTER TABLE ci_field_value DROP CONSTRAINT ci_field_value_no_tags`)
	mixed, overridden, deleted, scalar := f.ID(), f.ID(), f.ID(), f.ID()
	survivor, victim := f.ID(), f.ID()
	exec(`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, attributes, created_at)
		SELECT v.id::uuid, $1, $2, t.id, v.name, 'active', v.attrs::jsonb, now() - v.age * interval '1 minute'
		FROM ci_type t, (VALUES
			($3, 'bf-mixed', '{"tags": ["Prod", "DMZ zone", 5, "prod", "--", "x"], "rack": "R1"}', 9),
			($4, 'bf-overridden', '{"tags": ["old"]}', 8),
			($5, 'bf-deleted', '{"tags": ["Gone"]}', 7),
			($6, 'bf-scalar', '{"tags": "Prod"}', 6),
			($7, 'bf-survivor', '{"tags": ["S"], "_overrides": {"tags": "x", "rack": "y"}}', 5),
			($8, 'bf-victim', '{"tags": ["V"]}', 4)) AS v(id, name, attrs, age)
		WHERE t.key = 'server' AND t.organization_id IS NULL`,
		f.OrgA, f.Client1, mixed, overridden, deleted, scalar, survivor, victim)
	exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted)
	exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		VALUES ($1, $2, 'tags', '["Override-Tag", {"no": "string"}]', 'manual', now(), true),
			($1, $3, 'tags', '["V"]', 'manual', now(), true)`, f.OrgA, overridden, victim)
	exec(`INSERT INTO review_item (organization_id, kind, status, payload, object_key) VALUES
		($1, 'override_conflict', 'open', jsonb_build_object('field', 'tags', 'ci_id', $2::text), 'ci:' || $2 || '/tags'),
		($1, 'override_conflict', 'open', jsonb_build_object('field', 'model', 'ci_id', $2::text), 'ci:' || $2 || '/model')`,
		f.OrgA, overridden)
	// An open merge of bf-victim into bf-survivor: the snapshot holds both
	// rows and the field value of the victim that the merge moved.
	exec(`INSERT INTO ci_merge (organization_id, client_id, survivor_id, victim_id, snapshot) SELECT $1, $2, $3, $4,
		jsonb_build_object('survivor', (SELECT to_jsonb(c) FROM ci c WHERE id = $3), 'victim', (SELECT to_jsonb(c) FROM ci c WHERE id = $4),
			'changed', jsonb_build_array(jsonb_build_object('table', 'ci_field_value', 'row', (SELECT to_jsonb(v) FROM ci_field_value v WHERE ci_id = $4))),
			'removed', '[]'::jsonb)`, f.OrgA, f.Client1, survivor, victim)
	exec(`UPDATE ci_field_value SET ci_id = $1 WHERE ci_id = $2`, survivor, victim)
	exec(`UPDATE ci SET version = version + 1 WHERE id = $1`, survivor)
	exec(`UPDATE ci SET deleted_at = now(), merged_into_id = $1 WHERE id = $2`, survivor, victim)
	view := f.ID()
	exec(`INSERT INTO saved_view (id, organization_id, name, entity_kind, filter_spec) VALUES ($1, $2, 'bf-view', 'ci', '{"tags": ["Prod", "DMZ zone"]}')`,
		view, f.OrgA)

	type rowState struct {
		version, schemaVersion int64
		updatedAt              time.Time
	}
	states := func() map[string]rowState {
		t.Helper()
		rows, qerr := tx.Query(bg, `SELECT id::text, version, schema_version, updated_at FROM ci WHERE organization_id = $1`, f.OrgA)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out := map[string]rowState{}
		for rows.Next() {
			var id string
			var s rowState
			if serr := rows.Scan(&id, &s.version, &s.schemaVersion, &s.updatedAt); serr != nil {
				t.Fatal(serr)
			}
			out[id] = s
		}
		return out
	}
	viewCIs := func(where string, args ...any) []string {
		t.Helper()
		var ids []string
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(array_agg(id::text ORDER BY id), '{}') FROM ci
			WHERE organization_id = $1 AND deleted_at IS NULL AND `+where, append([]any{f.OrgA}, args...)...).Scan(&ids); qerr != nil {
			t.Fatalf("%s: %v", where, qerr)
		}
		return ids
	}
	// The saved view before the migration: attributes->'tags' ? value.
	before := viewCIs(`attributes->'tags' ? $2 AND attributes->'tags' ? $3`, "Prod", "DMZ zone")
	stateBefore := states()

	exec(backfillSection(t))
	exec(`ALTER TABLE ci_field_value ADD CONSTRAINT ci_field_value_no_tags CHECK (field_name <> 'tags')`)

	keys := func(ciID string) []string {
		t.Helper()
		var out []string
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(array_agg(t.key ORDER BY t.key), '{}') FROM ci_tag ct JOIN tag t ON t.id = ct.tag_id
			WHERE ct.ci_id = $1`, ciID).Scan(&out); qerr != nil {
			t.Fatal(qerr)
		}
		return out
	}
	for id, want := range map[string][]string{
		mixed:      {"dmz_zone", "prod", "x"},
		overridden: {"override-tag"},
		deleted:    {"gone"},
		scalar:     {},
		// The override the merge moved to the survivor is its effective
		// value; the retired victim keeps its own.
		survivor: {"v"},
		victim:   {"v"},
	} {
		if got := keys(id); !slices.Equal(got, want) {
			t.Errorf("tags of %s: %v, want %v", id, got, want)
		}
	}
	var prodName string
	if err = tx.QueryRow(bg, `SELECT display_name ->> 'de' || '/' || (display_name ->> 'en') FROM tag WHERE organization_id = $1 AND key = 'prod'`,
		f.OrgA).Scan(&prodName); err != nil || prodName != "Prod/Prod" {
		t.Errorf("display name of prod: %q %v", prodName, err)
	}
	var withTags, tagValues, openReviews, otherReviews int
	if err = tx.QueryRow(bg, `SELECT
		(SELECT count(*) FROM ci WHERE organization_id = $1 AND (attributes ? 'tags' OR attributes -> '_overrides' ? 'tags')),
		(SELECT count(*) FROM ci_field_value WHERE organization_id = $1 AND field_name = 'tags'),
		(SELECT count(*) FROM review_item WHERE organization_id = $1 AND payload ->> 'field' = 'tags' AND status = 'open'),
		(SELECT count(*) FROM review_item WHERE organization_id = $1 AND payload ->> 'field' = 'model' AND status = 'open')`,
		f.OrgA).Scan(&withTags, &tagValues, &openReviews, &otherReviews); err != nil {
		t.Fatal(err)
	}
	if withTags != 0 || tagValues != 0 || openReviews != 0 || otherReviews != 1 {
		t.Errorf("after the backfill: %d CIs with tags, %d field values, %d open tag reviews, %d other reviews",
			withTags, tagValues, openReviews, otherReviews)
	}
	var action string
	if err = tx.QueryRow(bg, `SELECT status || '/' || action FROM review_item WHERE organization_id = $1 AND payload ->> 'field' = 'tags'`,
		f.OrgA).Scan(&action); err != nil || action != "dismissed/dismiss" {
		t.Errorf("tag review: %q %v", action, err)
	}
	var rack string
	if err = tx.QueryRow(bg, `SELECT attributes ->> 'rack' FROM ci WHERE id = $1`, mixed).Scan(&rack); err != nil || rack != "R1" {
		t.Errorf("other attributes: %q %v", rack, err)
	}
	if after := states(); len(after) != len(stateBefore) {
		t.Errorf("rows: %d before, %d after", len(stateBefore), len(after))
	} else {
		for id, s := range stateBefore {
			if a := after[id]; a.version != s.version || a.schemaVersion != s.schemaVersion || !a.updatedAt.Equal(s.updatedAt) {
				t.Errorf("%s: %+v before, %+v after", id, s, a)
			}
		}
	}
	var snapshot string
	if err = tx.QueryRow(bg, `SELECT snapshot::text FROM ci_merge WHERE victim_id = $1`, victim).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot, `"tags"`) || !strings.Contains(snapshot, `"rack": "y"`) {
		t.Errorf("merge snapshot: %s", snapshot)
	}
	var raw []byte
	if err = tx.QueryRow(bg, `SELECT filter_spec FROM saved_view WHERE id = $1`, view).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec savedview.FilterSpec
	if err = json.Unmarshal(raw, &spec); err != nil || !slices.Equal(spec.Tags, []string{"prod", "dmz_zone"}) {
		t.Fatalf("normalized filter_spec: %s %v", raw, err)
	}
	where, args, err := spec.CompileSQL("ci", []any{f.OrgA}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if after := viewCIs(where, args[1:]...); !slices.Equal(before, after) || !slices.Equal(after, []string{mixed}) {
		t.Errorf("saved view: %v before, %v after", before, after)
	}
	if err = tx.Commit(bg); err != nil {
		t.Fatal(err)
	}

	// The unmerge of the open merge works on the stripped snapshot; the
	// survivor keeps the united tags (known limitation).
	if _, err = ci.NewPGRepository(f.App).Unmerge(ctx, f.OrgA, victim, false); err != nil {
		t.Fatalf("unmerge after the backfill: %v", err)
	}
	var victimAttrs string
	if err = f.Admin.QueryRow(bg, `SELECT attributes::text FROM ci WHERE id = $1 AND deleted_at IS NULL`, victim).Scan(&victimAttrs); err != nil ||
		strings.Contains(victimAttrs, "tags") {
		t.Errorf("victim after the unmerge: %s %v", victimAttrs, err)
	}
}

func TestCITagMergeAndUnmergeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e9")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepository(f.App)
	survivor := f.CI(t, f.OrgA, f.Client1, "tag-merge-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "tag-merge-victim")
	for id, list := range map[string][]string{survivor: {"a", "Shared"}, victim: {"b", "shared"}} {
		if _, err := repo.Update(ctx, f.OrgA, id, ci.UpdateRequest{LegacyTags: &list}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := repo.Merge(ctx, f.OrgA, survivor, victim, "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !slices.Equal(res.Survivor.Tags, []string{"a", "b", "shared"}) || res.Moved["tags"] != 1 {
		t.Errorf("merge: tags %v, moved %d", res.Survivor.Tags, res.Moved["tags"])
	}
	if _, err = repo.Unmerge(ctx, f.OrgA, victim, false); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	if got := ciTagKeys(t, f, survivor); !slices.Equal(got, []string{"a", "shared"}) {
		t.Errorf("survivor after the unmerge: %v", got)
	}
	if got := ciTagKeys(t, f, victim); !slices.Equal(got, []string{"b", "shared"}) {
		t.Errorf("victim after the unmerge: %v", got)
	}
}

// tagsProvenance is the field provenance of the ingest as wired by the
// server (override repository, batch and many).
type tagsProvenance struct{ repo *override.PGRepository }

func (p tagsProvenance) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*discovery.FieldProvenance, error) {
	fv, err := p.repo.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source, observedAt)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (p tagsProvenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, p.repo, w)
}

func (p tagsProvenance) RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	return p.repo.RecordDiscoveredMany(ctx, orgID, ciID, values, source, observedAt)
}

func (p tagsProvenance) RecordDiscoveredBatch(ctx context.Context, orgID string, batch []override.DiscoveredValues) error {
	return p.repo.RecordDiscoveredBatch(ctx, orgID, batch)
}

func TestCITagsThroughTheAPIAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ea")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	override.NewHandler(override.NewPGRepository(f.App)).RegisterRoutes(mux)
	discovery.NewHandler(discovery.NewPGRepository(f.App), repo).
		WithProvenance(tagsProvenance{repo: override.NewPGRepository(f.App)}).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) ci.Item {
		t.Helper()
		var item ci.Item
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
		return item
	}
	deprecated := func(step string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Header().Get("Deprecation") == "" || w.Header().Get("Sunset") == "" {
			t.Errorf("%s: no Deprecation and Sunset: %v", step, w.Header())
		}
	}

	// A new organization has the system tag archived.
	var archived string
	if err := f.Admin.QueryRow(bg, `SELECT display_name ->> 'de' FROM tag WHERE organization_id = $1 AND key = 'archived' AND is_system`,
		f.OrgA).Scan(&archived); err != nil || archived != "Archiviert" {
		t.Errorf("system tag archived: %q %v", archived, err)
	}

	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	w := call(http.MethodPost, "/api/v1/cis", `{"name": "tag-api", "ci_type_id": "`+typeID+`", "client_id": "`+f.Client1+`",
		"attributes": {"tags": ["Prod", "New Tag", "prod"], "rack": "R2"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	deprecated("POST", w)
	created := decode(w)
	if !slices.Equal(created.Tags, []string{"new_tag", "prod"}) || created.Attributes["tags"] != nil || created.Attributes["rack"] != "R2" {
		t.Errorf("POST answer: tags %v, attributes %v", created.Tags, created.Attributes)
	}
	var newName string
	if err := f.Admin.QueryRow(bg, `SELECT display_name ->> 'en' FROM tag WHERE organization_id = $1 AND key = 'new_tag'`, f.OrgA).
		Scan(&newName); err != nil || newName != "New Tag" {
		t.Errorf("created catalog tag: %q %v", newName, err)
	}
	var stored string
	if err := f.Admin.QueryRow(bg, `SELECT attributes::text FROM ci WHERE id = $1`, created.ID).Scan(&stored); err != nil || strings.Contains(stored, "tags") {
		t.Errorf("stored attributes: %s %v", stored, err)
	}
	if got := ciTagKeys(t, f, created.ID); !slices.Equal(got, []string{"new_tag", "prod"}) {
		t.Errorf("ci_tag after POST: %v", got)
	}

	// Reads carry the keys; a CI without tags has [].
	if got := decode(call(http.MethodGet, "/api/v1/cis/"+created.ID, "")); !slices.Equal(got.Tags, []string{"new_tag", "prod"}) {
		t.Errorf("GET: %v", got.Tags)
	}
	plain := f.CI(t, f.OrgA, f.Client1, "tag-api-plain")
	if w = call(http.MethodGet, "/api/v1/cis/"+plain, ""); !strings.Contains(w.Body.String(), `"tags":[]`) {
		t.Errorf("GET of a CI without tags: %s", w.Body.String())
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if w = call(http.MethodGet, "/api/v1/cis?limit=100", ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	listed := map[string]string{}
	for _, row := range list.Data {
		b, _ := json.Marshal(row["tags"])
		listed[row["id"].(string)] = string(b)
	}
	if listed[created.ID] != `["new_tag","prod"]` || listed[plain] != `[]` {
		t.Errorf("list tags: %v", listed)
	}
	if w = call(http.MethodGet, "/api/v1/cis?limit=100&fields=id,tags", ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"tags":["new_tag","prod"]`) || strings.Contains(w.Body.String(), `"name"`) {
		t.Errorf("fields=id,tags: %d %s", w.Code, w.Body.String())
	}

	// PATCH replaces the assignment, null removes it (merge patch).
	w = call(http.MethodPatch, "/api/v1/cis/"+created.ID, `{"attributes": {"tags": ["DMZ"]}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	deprecated("PATCH", w)
	if patched := decode(w); !slices.Equal(patched.Tags, []string{"dmz"}) || patched.Version != created.Version+1 ||
		patched.Attributes["rack"] != "R2" {
		t.Errorf("PATCH answer: tags %v, version %d (before %d), attributes %v", patched.Tags, patched.Version, created.Version, patched.Attributes)
	}
	var history int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'tags'`, created.ID).Scan(&history); err != nil || history != 1 {
		t.Errorf("history of the tags: %d %v", history, err)
	}
	if w = call(http.MethodPatch, "/api/v1/cis/"+created.ID, `{"attributes": {"tags": null}}`); w.Code != http.StatusOK || len(decode(w).Tags) != 0 {
		t.Errorf("PATCH null: %d %s", w.Code, w.Body.String())
	}
	if got := ciTagKeys(t, f, created.ID); len(got) != 0 {
		t.Errorf("ci_tag after null: %v", got)
	}

	// A value that is no array of strings is 400 on attributes.tags.
	for _, body := range []string{`{"attributes": {"tags": "Prod"}}`, `{"attributes": {"tags": [1]}}`} {
		w = call(http.MethodPatch, "/api/v1/cis/"+created.ID, body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"field":"attributes.tags"`) {
			t.Errorf("PATCH %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w = call(http.MethodPost, "/api/v1/cis", `{"name": "tag-api-bad", "ci_type_id": "`+typeID+`", "attributes": {"tags": {"a": 1}}}`); w.Code != http.StatusBadRequest {
		t.Errorf("POST with an object: %d %s", w.Code, w.Body.String())
	}

	// The override path is closed (E-125).
	if w = call(http.MethodPut, "/api/v1/cis/"+created.ID+"/fields/tags/override", `{"value": ["x"], "reason": "manual"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("override of tags: %d %s", w.Code, w.Body.String())
	}

	// An ingest record with attributes.tags assigns them through the
	// reconciliation; a later record replaces them.
	ingest := func(tags ...any) discovery.BulkIngestResponse {
		t.Helper()
		rec := plugins.RecordFromResult("ssh", &plugins.Result{CIType: "server", Name: "tag-ingest", IP: "192.0.2.234",
			Serial: "TAG-SN-281", Attributes: map[string]any{"tags": tags}}, time.Now().UTC())
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		w := call(http.MethodPost, "/api/v1/discovery/ingest", string(body))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var resp discovery.BulkIngestResponse
		if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := ingest("Ingested", "Linux"); resp.Created != 1 {
		t.Fatalf("first record: %+v", resp)
	}
	var ingested string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'TAG-SN-281'`, f.OrgA).Scan(&ingested); err != nil {
		t.Fatal(err)
	}
	if got := ciTagKeys(t, f, ingested); !slices.Equal(got, []string{"ingested", "linux"}) {
		t.Errorf("tags of the ingested CI: %v", got)
	}
	if resp := ingest("Linux"); resp.Created != 0 {
		t.Fatalf("second record: %+v", resp)
	}
	if got := ciTagKeys(t, f, ingested); !slices.Equal(got, []string{"linux"}) {
		t.Errorf("tags after the second record: %v", got)
	}
	// A value that is no array of strings is dropped, the record stays.
	if resp := ingest("Linux", 7); resp.InvalidValues != 1 {
		t.Errorf("record with a number among the tags: %+v", resp)
	}
	if got := ciTagKeys(t, f, ingested); !slices.Equal(got, []string{"linux"}) {
		t.Errorf("tags after the invalid record: %v", got)
	}
	var tagFields int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'tags'`, ingested).Scan(&tagFields); err != nil || tagFields != 0 {
		t.Errorf("field values of tags: %d %v", tagFields, err)
	}
}
