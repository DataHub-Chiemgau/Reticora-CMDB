// Tags of assets (API-06; WP-755, E-38, migration 000249) against
// PostgreSQL:
//
//   - TestAssetTagScopeAgainstPostgres: asset_tag_link takes client and
//     site of its asset and follows a client and a site change of the
//     asset; a principal of another client cannot assign a tag to the
//     asset.
//   - TestAssetTagBackfillAgainstPostgres: the backfill section of
//     migration 000249, executed from the migration file: only string
//     elements of arrays, key rule and collisions, missing catalog tags,
//     custom_fields without tags, version and updated_at unchanged,
//     normalized saved views of assets with the same assets before and
//     after.
//   - TestReplaceAssetTagsAgainstPostgres: PUT /assets/{id}/tags with 422
//     for unknown keys, version + 1 and ETag, If-Match 412/409, trail row
//     and asset.changed with changed_fields ["tags"], no change for the same
//     list, tags in GET and list, custom_fields.tags of POST and PATCH 400,
//     a client-scoped principal (404 outside its client).
//   - TestAssetTagCatalogAgainstPostgres: DELETE /tags/{id} of a tag
//     assigned to an asset is 409; POST /tags/{id}/merge moves the asset
//     assignments (a duplicate drops) with version + 1, trail row and
//     asset.changed per asset.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -p 1 -run 'TestAssetTag|TestReplaceAssetTags' ./internal/asset/
package asset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// assetTagKeys returns the tag keys of an asset from the catalog, sorted.
func assetTagKeys(t *testing.T, f *scopetest.Fixture, assetID string) []string {
	t.Helper()
	var keys []string
	if err := f.Admin.QueryRow(context.Background(), `SELECT COALESCE(array_agg(t.key ORDER BY t.key), '{}')
		FROM asset_tag_link l JOIN tag t ON t.id = l.tag_id WHERE l.asset_id = $1`, assetID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

// catalogTag creates a tag of organization A through the maintenance pool
// and returns its id.
func catalogTag(t *testing.T, f *scopetest.Fixture, key string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `INSERT INTO tag (organization_id, key, display_name)
		VALUES ($1, $2, jsonb_build_object('de', $2::text)) RETURNING id::text`, f.OrgA, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// linkAssetTags assigns catalog tags to an asset through the maintenance
// pool.
func linkAssetTags(t *testing.T, f *scopetest.Fixture, assetID string, keys ...string) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO asset_tag_link (organization_id, asset_id, tag_id)
		SELECT t.organization_id, $1, t.id FROM tag t JOIN asset a ON a.id = $1 WHERE t.organization_id = a.organization_id AND t.key = ANY($2)`,
		assetID, keys); err != nil {
		t.Fatal(err)
	}
}

// adminCount runs a count query through the maintenance pool.
func adminCount(t *testing.T, f *scopetest.Fixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestAssetTagScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "2b")
	bg := context.Background()
	catalogTag(t, f, "scope")
	room1 := f.Room(t, f.Client1, "wp755-site-1")
	room2 := f.Room(t, f.Client1, "wp755-site-2")
	own := f.Asset(t, f.OrgA, f.Client1, "wp755-scope-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "wp755-scope-foreign")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, own, room1); err != nil {
		t.Fatal(err)
	}
	repo := asset.NewPGRepository(f.App)
	item, err := repo.ReplaceTags(f.ClientCtx(f.Client1), f.OrgA, own, []string{"scope"}, nil)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !slices.Equal(item.Tags, []string{"scope"}) {
		t.Errorf("tags of the answer: %v", item.Tags)
	}
	scopeOf := func() (client, site, assetSite string) {
		t.Helper()
		if err = f.Admin.QueryRow(bg, `SELECT COALESCE(l.client_id::text, ''), COALESCE(l.site_id::text, ''), COALESCE(a.site_id::text, '')
			FROM asset_tag_link l JOIN asset a ON a.id = l.asset_id WHERE l.asset_id = $1`, own).Scan(&client, &site, &assetSite); err != nil {
			t.Fatal(err)
		}
		return client, site, assetSite
	}
	client, site, site1 := scopeOf()
	if client != f.Client1 || site1 == "" || site != site1 {
		t.Errorf("scope of the assignment: client %q site %q, want %s and the asset's site %q", client, site, f.Client1, site1)
	}
	// A site change of the asset (another location) and a client change.
	if _, err = f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, own, room2); err != nil {
		t.Fatal(err)
	}
	if _, site, site2 := scopeOf(); site2 == site1 || site != site2 {
		t.Errorf("site of the assignment after the site change: %q, want %q", site, site2)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE asset SET client_id = $2 WHERE id = $1`, own, f.Client2); err != nil {
		t.Fatal(err)
	}
	if client, _, _ = scopeOf(); client != f.Client2 {
		t.Errorf("client of the assignment after the client change: %q, want %s", client, f.Client2)
	}

	// A principal of client 1 cannot assign a tag to an asset of client 2:
	// the asset is invisible, the derived scope is empty and WITH CHECK
	// fails.
	err = database.WithRequestTenant(f.ClientCtx(f.Client1), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `INSERT INTO asset_tag_link (organization_id, asset_id, tag_id)
			SELECT $1, $2, id FROM tag WHERE organization_id = $1 AND key = 'scope'`, f.OrgA, foreign)
		return execErr
	})
	if err == nil {
		t.Error("a principal of client 1 assigned a tag to an asset of client 2")
	}
	if _, err = repo.ReplaceTags(f.ClientCtx(f.Client1), f.OrgA, foreign, []string{"scope"}, nil); err == nil {
		t.Error("a principal of client 1 tagged an asset of client 2 through the repository")
	}
	if keys := assetTagKeys(t, f, foreign); len(keys) != 0 {
		t.Errorf("tags of the foreign asset: %v", keys)
	}
}

// assetBackfillSection is the backfill of migration 000249 between its
// markers.
func assetBackfillSection(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/000249_asset_tags.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "-- ─── 5. Backfill")
	end := strings.Index(text, "-- ─── end of backfill")
	if start < 0 || end < start {
		t.Fatal("backfill markers not found in 000249_asset_tags.up.sql")
	}
	return text[start:end]
}

func TestAssetTagBackfillAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "2b")
	bg := context.Background()
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
	// The state before the migration: tags in custom_fields; prod exists
	// in the catalog with another display name.
	exec(`INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'prod', '{"de": "Betrieb"}')`, f.OrgA)
	mixed, scalar, other, plain := f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO asset (id, organization_id, client_id, inventory_no, name, custom_fields, created_at)
		SELECT v.id::uuid, $1, $2, v.inv, v.inv, v.cf::jsonb, now() - v.age * interval '1 minute'
		FROM (VALUES
			($3, 'bf755-mixed', '{"tags": ["Prod", "DMZ zone", 5, "prod", "--", "x"], "rack": "R1"}', 9),
			($4, 'bf755-scalar', '{"tags": "Prod"}', 8),
			($5, 'bf755-other', '{"tags": ["New Tag", "DMZ zone"]}', 7),
			($6, 'bf755-plain', '{"rack": "R2"}', 6)) AS v(id, inv, cf, age)`,
		f.OrgA, f.Client1, mixed, scalar, other, plain)
	view := f.ID()
	exec(`INSERT INTO saved_view (id, organization_id, name, entity_kind, filter_spec) VALUES ($1, $2, 'bf755-view', 'asset', '{"tags": ["Prod", "DMZ zone"]}')`,
		view, f.OrgA)

	type rowState struct {
		version   int64
		updatedAt time.Time
	}
	states := func() map[string]rowState {
		t.Helper()
		rows, qerr := tx.Query(bg, `SELECT id::text, version, updated_at FROM asset WHERE organization_id = $1`, f.OrgA)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out := map[string]rowState{}
		for rows.Next() {
			var id string
			var s rowState
			if serr := rows.Scan(&id, &s.version, &s.updatedAt); serr != nil {
				t.Fatal(serr)
			}
			out[id] = s
		}
		return out
	}
	viewAssets := func(where string, args ...any) []string {
		t.Helper()
		var ids []string
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(array_agg(id::text ORDER BY id), '{}') FROM asset
			WHERE organization_id = $1 AND `+where, append([]any{f.OrgA}, args...)...).Scan(&ids); qerr != nil {
			t.Fatalf("%s: %v", where, qerr)
		}
		return ids
	}
	// The saved view before the migration: custom_fields->'tags' ? value.
	before := viewAssets(`asset.custom_fields->'tags' ? $2 AND asset.custom_fields->'tags' ? $3`, "Prod", "DMZ zone")
	stateBefore := states()

	exec(assetBackfillSection(t))

	keys := func(assetID string) []string {
		t.Helper()
		var out []string
		if qerr := tx.QueryRow(bg, `SELECT COALESCE(array_agg(t.key ORDER BY t.key), '{}') FROM asset_tag_link l JOIN tag t ON t.id = l.tag_id
			WHERE l.asset_id = $1`, assetID).Scan(&out); qerr != nil {
			t.Fatal(qerr)
		}
		return out
	}
	for id, want := range map[string][]string{
		mixed:  {"dmz_zone", "prod", "x"},
		scalar: {},
		other:  {"dmz_zone", "new_tag"},
		plain:  {},
	} {
		if got := keys(id); !slices.Equal(got, want) {
			t.Errorf("tags of %s: %v, want %v", id, got, want)
		}
	}
	names := map[string]string{}
	rows, err := tx.Query(bg, `SELECT key, display_name ->> 'de' FROM tag WHERE organization_id = $1 AND key = ANY($2)`,
		f.OrgA, []string{"prod", "dmz_zone", "new_tag", "x"})
	if err != nil {
		t.Fatal(err)
	}
	var key, name string
	if _, err = pgx.ForEachRow(rows, []any{&key, &name}, func() error { names[key] = name; return nil }); err != nil {
		t.Fatal(err)
	}
	// An existing tag keeps its name; a new one takes the first original.
	if names["prod"] != "Betrieb" || names["dmz_zone"] != "DMZ zone" || names["new_tag"] != "New Tag" || names["x"] != "x" {
		t.Errorf("catalog after the backfill: %v", names)
	}
	var withTags int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM asset WHERE organization_id = $1 AND custom_fields ? 'tags'`, f.OrgA).Scan(&withTags); err != nil {
		t.Fatal(err)
	}
	if withTags != 0 {
		t.Errorf("%d assets keep custom_fields.tags", withTags)
	}
	var rack string
	if err = tx.QueryRow(bg, `SELECT custom_fields ->> 'rack' FROM asset WHERE id = $1`, mixed).Scan(&rack); err != nil || rack != "R1" {
		t.Errorf("other custom fields: %q %v", rack, err)
	}
	if after := states(); len(after) != len(stateBefore) {
		t.Errorf("rows: %d before, %d after", len(stateBefore), len(after))
	} else {
		for id, s := range stateBefore {
			if a := after[id]; a.version != s.version || !a.updatedAt.Equal(s.updatedAt) {
				t.Errorf("%s: %+v before, %+v after", id, s, a)
			}
		}
	}
	var raw []byte
	if err = tx.QueryRow(bg, `SELECT filter_spec FROM saved_view WHERE id = $1`, view).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec savedview.FilterSpec
	if err = json.Unmarshal(raw, &spec); err != nil || !slices.Equal(spec.Tags, []string{"prod", "dmz_zone"}) {
		t.Fatalf("normalized filter_spec: %s %v", raw, err)
	}
	where, args, err := spec.CompileSQL("asset", []any{f.OrgA}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if after := viewAssets(where, args[1:]...); !slices.Equal(before, after) || !slices.Equal(after, []string{mixed}) {
		t.Errorf("saved view: %v before, %v after", before, after)
	}
}

// tagWorld mounts the asset routes with the event outbox and the tag
// routes of the CI domain over PostgreSQL.
type tagWorld struct {
	*assetWorld
}

func newTagWorld(t *testing.T) *tagWorld {
	t.Helper()
	f := scopetest.Seed(t, "2b")
	mux := chi.NewRouter()
	asset.NewHandler(asset.NewPGRepository(f.App).WithEvents(events.NewOutbox())).RegisterRoutes(mux)
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	ci.NewHandler(ci.NewService(cis)).RegisterRoutes(mux)
	return &tagWorld{assetWorld: &assetWorld{f: f, mux: mux}}
}

// putTags sends PUT /assets/{id}/tags with If-Match (none when empty).
func (w *tagWorld) putTags(t *testing.T, id, ifMatch, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, assetsPath+"/"+id+"/tags", bytes.NewBufferString(body))
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	req = req.WithContext(tenant.WithTenant(w.f.OrgCtx(w.f.OrgA), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User}))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("PUT tags of %s If-Match %q %s: got %d %s, want %d", id, ifMatch, body, rec.Code, rec.Body.String(), want)
	}
	return rec
}

// violations returns the fields of the violations of a 422.
func violations(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var p struct {
		Violations []api.Violation `json:"violations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	out := make([]string, len(p.Violations))
	for i, v := range p.Violations {
		out[i] = v.Field
	}
	return out
}

// tagTrail returns the trail rows of tags of the asset as "version:old->new".
func tagTrail(t *testing.T, f *scopetest.Fixture, assetID string) []string {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT entity_version, old_value::text, new_value::text FROM entity_change
		WHERE entity_type = 'asset' AND entity_id = $1 AND field_name = 'tags' ORDER BY entity_version`, assetID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	var version int64
	var oldValue, newValue string
	if _, err = pgx.ForEachRow(rows, []any{&version, &oldValue, &newValue}, func() error {
		out = append(out, strconv.FormatInt(version, 10)+":"+oldValue+"->"+newValue)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReplaceAssetTagsAgainstPostgres(t *testing.T) {
	w := newTagWorld(t)
	f := w.f
	catalogTag(t, f, "prod")
	catalogTag(t, f, "dmz")
	a := w.create(t, `{"inventory_no":"WP755-1","name":"tagged","client_id":"`+f.Client1+`"}`)
	if a.Tags == nil || len(a.Tags) != 0 {
		t.Fatalf("tags of a new asset: %#v", a.Tags)
	}
	path := assetsPath + "/" + a.ID

	// Unknown or malformed keys and a missing list are 422 with the entries;
	// nothing changes.
	rec := w.putTags(t, a.ID, "", `{"tags":["prod","nope","BAD KEY"]}`, http.StatusUnprocessableEntity)
	if got := violations(t, rec); !slices.Equal(got, []string{"tags[2]", "tags[1]"}) && !slices.Equal(got, []string{"tags[1]", "tags[2]"}) {
		t.Errorf("violations: %v", got)
	}
	if got := violations(t, w.putTags(t, a.ID, "", `{}`, http.StatusUnprocessableEntity)); !slices.Equal(got, []string{"tags"}) {
		t.Errorf("violations without tags: %v", got)
	}
	if v := (&versionWorld{assetWorld: w.assetWorld}).version(t, a.ID); v != a.Version {
		t.Fatalf("version after refused PUTs: %d, want %d", v, a.Version)
	}

	// A change: version + 1, ETag, sorted without duplicates, trail row and
	// asset.changed with changed_fields ["tags"].
	rec = w.putTags(t, a.ID, api.ETag(a.Version), `{"tags":["prod","dmz","prod"]}`, http.StatusOK)
	tagged := decodeAsset(t, rec)
	if !slices.Equal(tagged.Tags, []string{"dmz", "prod"}) || tagged.Version != a.Version+1 ||
		rec.Header().Get("ETag") != api.ETag(a.Version+1) {
		t.Fatalf("PUT answer: tags %v version %d ETag %q", tagged.Tags, tagged.Version, rec.Header().Get("ETag"))
	}
	if got := assetTagKeys(t, f, a.ID); !slices.Equal(got, []string{"dmz", "prod"}) {
		t.Errorf("stored tags: %v", got)
	}
	wantTrail := []string{strconv.FormatInt(tagged.Version, 10) + `:[]->["dmz", "prod"]`}
	if got := tagTrail(t, f, a.ID); !slices.Equal(got, wantTrail) {
		t.Errorf("trail: %v, want %v", got, wantTrail)
	}
	if types, changed := outboxEvents(t, f, a.ID); !slices.Equal(types, []string{events.AssetCreated, events.AssetChanged}) ||
		len(changed) != 1 || !slices.Equal(changed[0], []string{"tags"}) {
		t.Errorf("events: %v %v", types, changed)
	}
	if n := adminCount(t, f, `SELECT count(*) FROM asset_tag_link WHERE asset_id = $1 AND created_by = $2`, a.ID, f.User); n != 2 {
		t.Errorf("assignments with the author: %d", n)
	}

	// The same list again changes nothing.
	same := decodeAsset(t, w.putTags(t, a.ID, "", `{"tags":["dmz","prod"]}`, http.StatusOK))
	if same.Version != tagged.Version {
		t.Errorf("version after an unchanged PUT: %d, want %d", same.Version, tagged.Version)
	}

	// If-Match: an unknown version is 412, the version before the tag
	// change is 409 on tags, but not for a PATCH of another field.
	w.putTags(t, a.ID, api.ETag(tagged.Version+5), `{"tags":[]}`, http.StatusPreconditionFailed)
	fields, current := conflict(t, w.putTags(t, a.ID, api.ETag(a.Version), `{"tags":["prod"]}`, http.StatusConflict))
	if !slices.Equal(fields, []string{"tags"}) || current != tagged.Version {
		t.Errorf("409: fields %v current %d", fields, current)
	}
	vw := &versionWorld{assetWorld: w.assetWorld}
	renamed := decodeAsset(t, vw.patchIf(t, a.ID, api.ETag(a.Version), `{"name":"renamed"}`, http.StatusOK))
	if !slices.Equal(renamed.Tags, []string{"dmz", "prod"}) {
		t.Errorf("tags in the PATCH answer: %v", renamed.Tags)
	}

	// GET and the list carry the tags.
	if got := decodeAsset(t, w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK})); !slices.Equal(got.Tags, []string{"dmz", "prod"}) {
		t.Errorf("tags of GET: %v", got.Tags)
	}
	var list api.ListResponse[asset.Asset]
	if err := json.Unmarshal(w.do(t, &call{method: http.MethodGet, path: assetsPath + "?search=WP755", want: http.StatusOK}).Body.Bytes(), &list); err != nil ||
		len(list.Data) != 1 || !slices.Equal(list.Data[0].Tags, []string{"dmz", "prod"}) {
		t.Errorf("list: %+v %v", list.Data, err)
	}

	// custom_fields.tags is refused with 400 on the field.
	w.do(t, &call{method: http.MethodPost, path: assetsPath, body: `{"inventory_no":"WP755-2","name":"x","custom_fields":{"tags":["prod"]}}`,
		want: http.StatusBadRequest, wantField: "custom_fields.tags"})
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"custom_fields":{"tags":["prod"]}}`,
		want: http.StatusBadRequest, wantField: "custom_fields.tags"})

	// An empty list removes every tag.
	cleared := decodeAsset(t, w.putTags(t, a.ID, "*", `{"tags":[]}`, http.StatusOK))
	if len(cleared.Tags) != 0 || len(assetTagKeys(t, f, a.ID)) != 0 {
		t.Errorf("after clearing: %v", cleared.Tags)
	}

	// A principal of client 2 does not see the asset of client 1.
	scoped := w.do(t, &call{ctx: f.ClientCtx(f.Client2), method: http.MethodPut, path: path + "/tags", body: `{"tags":["prod"]}`})
	if scoped.Code != http.StatusNotFound {
		t.Errorf("client-2 principal: %d %s", scoped.Code, scoped.Body.String())
	}
	if got := assetTagKeys(t, f, a.ID); len(got) != 0 {
		t.Errorf("tags after the refused PUT: %v", got)
	}
}

func TestAssetTagCatalogAgainstPostgres(t *testing.T) {
	w := newTagWorld(t)
	f := w.f
	vw := &versionWorld{assetWorld: w.assetWorld}
	source := catalogTag(t, f, "prod_old")
	into := catalogTag(t, f, "prod")
	both := f.Asset(t, f.OrgA, f.Client1, "wp755-merge-both")
	only := f.Asset(t, f.OrgA, f.Client2, "wp755-merge-only")
	untouched := f.Asset(t, f.OrgA, f.Client1, "wp755-merge-untouched")
	linkAssetTags(t, f, both, "prod_old", "prod")
	linkAssetTags(t, f, only, "prod_old")
	linkAssetTags(t, f, untouched, "prod")
	before := map[string]int64{both: vw.version(t, both), only: vw.version(t, only), untouched: vw.version(t, untouched)}

	// A tag assigned to an asset cannot be deleted.
	rec := w.do(t, &call{method: http.MethodDelete, path: "/api/v1/tags/" + source, want: http.StatusConflict})
	var problem api.ProblemDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil || problem.Detail != ci.ErrTagInUse.Error() {
		t.Errorf("409 of the delete: %s %v", rec.Body.String(), err)
	}
	if n := adminCount(t, f, `SELECT count(*) FROM tag WHERE id = $1`, source); n != 1 {
		t.Fatal("the assigned tag was deleted")
	}

	// The merge moves the asset assignments; the duplicate drops.
	w.do(t, &call{method: http.MethodPost, path: "/api/v1/tags/" + source + "/merge", body: `{"into_id":"` + into + `"}`, want: http.StatusOK})
	for id, want := range map[string][]string{both: {"prod"}, only: {"prod"}, untouched: {"prod"}} {
		if got := assetTagKeys(t, f, id); !slices.Equal(got, want) {
			t.Errorf("tags of %s: %v, want %v", id, got, want)
		}
	}
	if n := adminCount(t, f, `SELECT count(*) FROM tag WHERE id = $1`, source); n != 0 {
		t.Error("the source tag remains")
	}
	// Per asset concerned: version + 1, the trail row of tags and one
	// asset.changed with changed_fields ["tags"]; nothing for the others.
	for id, oldTags := range map[string]string{both: `["prod", "prod_old"]`, only: `["prod_old"]`} {
		v := vw.version(t, id)
		if v != before[id]+1 {
			t.Errorf("version of %s: %d, want %d", id, v, before[id]+1)
		}
		if got, want := tagTrail(t, f, id), []string{strconv.FormatInt(v, 10) + ":" + oldTags + `->["prod"]`}; !slices.Equal(got, want) {
			t.Errorf("trail of %s: %v, want %v", id, got, want)
		}
		if types, changed := outboxEvents(t, f, id); !slices.Equal(types, []string{events.AssetChanged}) ||
			!slices.Equal(changed[0], []string{"tags"}) {
			t.Errorf("events of %s: %v %v", id, types, changed)
		}
	}
	if v := vw.version(t, untouched); v != before[untouched] {
		t.Errorf("an untouched asset changed its version: %d", v)
	}
	if n := adminCount(t, f, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'tag.merged' AND resource_id = $2
		AND (changes ->> 'assets')::int = 2`, f.OrgA, source); n != 1 {
		t.Errorf("audit tag.merged with the assets: %d", n)
	}

	// The trail rows keep If-Match field-granular: the version before the
	// merge conflicts on tags only.
	fields, _ := conflict(t, w.putTags(t, both, api.ETag(before[both]), `{"tags":[]}`, http.StatusConflict))
	if !slices.Equal(fields, []string{"tags"}) {
		t.Errorf("409 fields after the merge: %v", fields)
	}
	vw.patchIf(t, both, api.ETag(before[both]), `{"name":"renamed"}`, http.StatusOK)

	// Without assignments the tag can be deleted.
	w.putTags(t, both, "", `{"tags":[]}`, http.StatusOK)
	w.putTags(t, only, "", `{"tags":[]}`, http.StatusOK)
	w.putTags(t, untouched, "", `{"tags":[]}`, http.StatusOK)
	w.do(t, &call{method: http.MethodDelete, path: "/api/v1/tags/" + into, want: http.StatusNoContent})
}
