package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// allModules enables every module of the release.
func allModules(string) bool { return true }

// featureSet enables the listed features.
type featureSet map[string]bool

func (s featureSet) IsEnabled(_ context.Context, _, key string) bool { return s[key] }

// utPerms are the permissions of the principals of the tests.
var utPerms = []identity.Permission{identity.PermCIRead, identity.PermSearchRead, "asset:read", "reservation:read",
	"ticket:read", "document:read", identity.PermUserRead}

// utWorld is the fixture of the search over tags and users (SRC-02,
// WP-788).
type utWorld struct {
	f       *scopetest.Fixture
	repo    *search.PGRepository
	owner   string // user Erika Wp788owner
	holder  string // user Hans Wp788holder
	other   string // user Otto Wp788other
	retired string // inactive user Ida Wp788retired
	prod    string // tag wp788-prod (de Produktiv788, en Live788)
	legacy  string // tag wp788-legacy (de Altlast788)
	alpha   string // CI of client 1 with tag prod, owner
	beta    string // CI of client 1 linked to the asset
	gamma   string // CI of client 1 without tag and asset
	asset   string // asset of client 1 with tag prod, owner and holder
}

func newUTWorld(t *testing.T) *utWorld {
	t.Helper()
	f := scopetest.Seed(t, "38")
	bg := context.Background()
	w := &utWorld{f: f, repo: search.NewPGRepository(f.App)}
	user := func(name, email string, active bool) string {
		id := f.ID()
		w.exec(t, `INSERT INTO app_user (id, organization_id, oidc_subject, email, display_name, is_active) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, f.OrgA, "wp788-"+email, email, name, active)
		return id
	}
	w.owner = user("Erika Wp788owner", "erika.wp788@example.test", true)
	w.holder = user("Hans Wp788holder", "hans.wp788@example.test", true)
	w.other = user("Otto Wp788other", "otto.wp788@example.test", true)
	w.retired = user("Ida Wp788retired", "ida.wp788@example.test", false)
	tag := func(key, display string) string {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO tag (organization_id, key, display_name) VALUES ($1, $2, $3::jsonb) RETURNING id::text`,
			f.OrgA, key, display).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	w.prod = tag("wp788-prod", `{"de": "Produktiv788", "en": "Live788"}`)
	w.legacy = tag("wp788-legacy", `{"de": "Altlast788"}`)
	w.alpha = f.CI(t, f.OrgA, f.Client1, "wp788 alpha")
	w.beta = f.CI(t, f.OrgA, f.Client1, "wp788 beta")
	w.gamma = f.CI(t, f.OrgA, f.Client1, "wp788 gamma")
	w.exec(t, `UPDATE ci SET owner_user_id = $2 WHERE id = $1`, w.alpha, w.owner)
	w.exec(t, `INSERT INTO ci_tag (organization_id, ci_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, w.alpha, w.prod)
	w.asset = f.Asset(t, f.OrgA, f.Client1, "wp788-asset")
	w.exec(t, `UPDATE asset SET owner_user_id = $2, assigned_user_id = $3 WHERE id = $1`, w.asset, w.owner, w.holder)
	w.exec(t, `INSERT INTO asset_tag_link (organization_id, asset_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, w.asset, w.prod)
	w.exec(t, `UPDATE ci SET asset_id = $2 WHERE id = $1`, w.beta, w.asset)
	if _, err := w.repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	return w
}

func (w *utWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (w *utWorld) name(id string) string {
	return map[string]string{w.alpha: "alpha", w.beta: "beta", w.gamma: "gamma", w.asset: "asset"}[id] + " " + id
}

// utRequest describes a search of the tests.
type utRequest struct {
	granted  map[string]bool // permissions of the caller (nil: utPerms)
	features search.FeatureChecker
	modules  func(string) bool
	policy   *permission.FieldPolicy
	backend  search.Backend
}

// rest runs GET /api/v1/search?q= and returns the hits by entity id.
func (w *utWorld) rest(t *testing.T, text string, opts *utRequest) map[string]search.Hit {
	t.Helper()
	granted := opts.granted
	if granted == nil {
		granted = map[string]bool{}
		for _, p := range utPerms {
			granted[string(p)] = true
		}
	}
	backend := opts.backend
	if backend == nil {
		backend = w.repo
	}
	policy := opts.policy
	if policy == nil {
		policy = permission.UnrestrictedPolicy(utPerms)
	}
	mux := chi.NewRouter()
	h := search.NewHandler(backend, permissionStub{granted: granted})
	if opts.features != nil {
		h.WithGates(opts.features, opts.modules)
	}
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+url.Values{"q": {text}, "limit": {"100"}}.Encode(), nil)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User})
	scope := database.OrgWideScope(w.f.OrgA, w.f.User)
	ctx = permission.WithPolicy(database.ContextWithTenantScope(ctx, &scope), policy)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	var body struct {
		Data []search.Hit `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("GET /api/v1/search?q=%s: %d %s", text, rec.Code, rec.Body.String())
	}
	out := map[string]search.Hit{}
	for i := range body.Data {
		out[body.Data[i].EntityID] = body.Data[i]
	}
	return out
}

// open searches with every permission, feature and module.
func (w *utWorld) open(t *testing.T, text string) map[string]search.Hit {
	t.Helper()
	return w.rest(t, text, &utRequest{features: allFeatures{}, modules: allModules})
}

// expect checks that the hits contain want and none of unwanted.
func (w *utWorld) expect(t *testing.T, label string, hits map[string]search.Hit, want, unwanted []string) {
	t.Helper()
	for _, id := range want {
		if _, ok := hits[id]; !ok {
			t.Errorf("%s: %s is no hit (%d hits)", label, w.name(id), len(hits))
		}
	}
	for _, id := range unwanted {
		if _, ok := hits[id]; ok {
			t.Errorf("%s: %s is a hit", label, w.name(id))
		}
	}
}

// matchedKeys returns the matched field keys of a hit.
func matchedKeys(h *search.Hit) []string {
	var out []string
	for _, m := range h.MatchedFields {
		out = append(out, m.Field)
	}
	return out
}

// TestSearchUsersAndTags covers WP-788 (SRC-02): tags of CIs and assets
// are searchable by key and by every language value of their display name,
// kept current on assigning, removing, renaming and merging; users are
// indexed as surrogate only and found by name or e-mail address through
// the owner and holder fields, with user:read, the holder only with
// assets_basic and the owner only with a readable field; the hybrid backend
// searches the remote index with the resolved ids.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchUsersAndTags(t *testing.T) {
	w := newUTWorld(t)
	f := w.f
	ctx := f.OrgCtx(f.OrgA)

	// Tags: key and display names, at CI and asset.
	for _, text := range []string{"wp788-prod", "Produktiv788", "live788"} {
		hits := w.open(t, text)
		w.expect(t, "tag "+text, hits, []string{w.alpha, w.asset}, []string{w.beta, w.gamma})
		if h, ok := hits[w.alpha]; ok && !slices.Contains(matchedKeys(&h), "core.tags") {
			t.Errorf("tag %s: matched fields of alpha %v", text, matchedKeys(&h))
		}
	}

	// Assign, remove, rename and merge.
	w.exec(t, `INSERT INTO ci_tag (organization_id, ci_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, w.gamma, w.legacy)
	w.expect(t, "assigned", w.open(t, "Altlast788"), []string{w.gamma}, []string{w.alpha})
	w.exec(t, `DELETE FROM ci_tag WHERE ci_id = $1 AND tag_id = $2`, w.gamma, w.legacy)
	w.expect(t, "removed", w.open(t, "Altlast788"), nil, []string{w.gamma})
	w.exec(t, `INSERT INTO ci_tag (organization_id, ci_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, w.gamma, w.legacy)
	w.exec(t, `INSERT INTO asset_tag_link (organization_id, asset_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, w.asset, w.legacy)
	// A second asset with the legacy tag only: the merge moves its
	// assignment (UPDATE of tag_id).
	moved := f.Asset(t, f.OrgA, f.Client1, "wp788-moved")
	w.exec(t, `INSERT INTO asset_tag_link (organization_id, asset_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, moved, w.legacy)
	tags := ci.NewPGRepository(f.App)
	if _, err := tags.UpdateTag(ctx, f.OrgA, w.legacy, &ci.UpdateTagRequest{DisplayName: map[string]string{"de": "Erbstueck788", "en": "Heirloom788"}}); err != nil {
		t.Fatalf("rename tag: %v", err)
	}
	w.expect(t, "renamed, new name", w.open(t, "Heirloom788"), []string{w.gamma, w.asset}, []string{w.alpha})
	w.expect(t, "renamed, old name", w.open(t, "Altlast788"), nil, []string{w.gamma, w.asset})
	if _, err := tags.MergeTag(ctx, f.OrgA, w.legacy, w.prod); err != nil {
		t.Fatalf("merge tag: %v", err)
	}
	w.expect(t, "merged, source", w.open(t, "Erbstueck788"), nil, []string{w.gamma, w.asset})
	w.expect(t, "merged, target", w.open(t, "Produktiv788"), []string{w.alpha, w.gamma, w.asset, moved}, []string{w.beta})

	// The indexer writes the tags of a CI whose document did not exist when
	// its tags were written (a CI created with tags).
	delta := f.CI(t, f.OrgA, f.Client1, "wp788 delta")
	w.exec(t, `INSERT INTO ci_tag (organization_id, ci_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, delta, w.prod)
	w.expect(t, "new CI before indexing", w.open(t, "Live788"), nil, []string{delta})
	doc := ci.IndexDocumentFor(&ci.Item{OrganizationID: f.OrgA, ID: delta, Name: "wp788 delta", ClientID: f.Client1})
	if err := search.NewCIIndexer(w.repo).IndexDocument(ctx, &doc); err != nil {
		t.Fatalf("index delta: %v", err)
	}
	w.expect(t, "new CI after indexing", w.open(t, "Live788"), []string{delta}, nil)

	// Users: the owner of CI and asset, the holder of the asset at the asset
	// and its CIs, by name and by e-mail address.
	hits := w.open(t, "Erika")
	w.expect(t, "owner by name", hits, []string{w.alpha, w.asset, w.beta}, []string{w.gamma})
	if h, ok := hits[w.alpha]; ok && !slices.Equal(matchedKeys(&h), []string{search.UserFieldOwner}) {
		t.Errorf("owner by name: matched fields of alpha %v", matchedKeys(&h))
	}
	for _, m := range hits[w.alpha].MatchedFields {
		if m.Highlight != "" {
			t.Errorf("owner by name: highlight %q of %s", m.Highlight, m.Field)
		}
	}
	hits = w.open(t, "hans.wp788@example.test")
	w.expect(t, "holder by e-mail", hits, []string{w.asset, w.beta}, []string{w.alpha, w.gamma})
	if h, ok := hits[w.beta]; ok && !slices.Contains(matchedKeys(&h), search.UserFieldAssetHolder) {
		t.Errorf("holder by e-mail: matched fields of beta %v", matchedKeys(&h))
	}
	if h, ok := hits[w.asset]; ok && !slices.Contains(matchedKeys(&h), search.UserFieldHolder) {
		t.Errorf("holder by e-mail: matched fields of asset %v", matchedKeys(&h))
	}
	w.expect(t, "inactive user", w.open(t, "Ida"), nil, []string{w.alpha, w.asset, w.beta, w.gamma})

	// Linking a CI to the asset through the CI service (PATCH
	// /api/v1/cis/{id}) gives it the holder.
	cis := ci.NewService(ci.NewIndexingRepository(ci.NewPGRepository(f.App), search.NewCIIndexer(w.repo)))
	if _, err := cis.Update(ctx, f.OrgA, w.gamma, ci.UpdateRequest{AssetID: &w.asset}); err != nil {
		t.Fatalf("link gamma: %v", err)
	}
	w.expect(t, "holder after linking", w.open(t, "Hans"), []string{w.asset, w.beta, w.gamma}, []string{w.alpha})
	w.expect(t, "mirrored owner after linking", w.open(t, "Erika"), []string{w.gamma}, nil)

	// A new owner of the asset reaches its CIs (asset_propagate_owner), a
	// new holder the asset and its CIs.
	w.exec(t, `UPDATE asset SET owner_user_id = $2, assigned_user_id = $3 WHERE id = $1`, w.asset, w.other, w.other)
	w.expect(t, "new owner of the asset", w.open(t, "Otto"), []string{w.asset, w.beta, w.gamma}, []string{w.alpha})
	w.expect(t, "former owner", w.open(t, "Erika"), []string{w.alpha}, []string{w.asset, w.beta, w.gamma})
	w.expect(t, "former holder", w.open(t, "Hans"), nil, []string{w.asset, w.beta, w.gamma})

	// No name or e-mail address in the index.
	var leaks int
	if err := f.Admin.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM search_document WHERE organization_id = $1 AND (title || ' ' || summary) ~* $2)
		+ (SELECT count(*) FROM search_document_field WHERE organization_id = $1 AND content ~* $2)
		+ (SELECT count(*) FROM search_identifier WHERE organization_id = $1 AND norm ~* $2)`,
		f.OrgA, `wp788(owner|holder|other|retired)|example\.test`).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Errorf("the index holds %d rows with a name or e-mail address", leaks)
	}
	var surrogates int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM search_identifier
		WHERE organization_id = $1 AND field = ANY($2::text[]) AND norm = ANY($3::text[])`,
		f.OrgA, []string{search.UserFieldOwner, search.UserFieldHolder, search.UserFieldAssetHolder}, []string{w.owner, w.other}).Scan(&surrogates); err != nil {
		t.Fatal(err)
	}
	if surrogates == 0 {
		t.Error("no user surrogate in search_identifier")
	}

	// Without user:read no user hits; without assets_basic no holder hits.
	noUsers := map[string]bool{"ci:read": true, "asset:read": true}
	w.expect(t, "without user:read", w.rest(t, "Otto", &utRequest{granted: noUsers, features: allFeatures{}, modules: allModules}),
		nil, []string{w.alpha, w.asset, w.beta, w.gamma})
	w.exec(t, `UPDATE asset SET owner_user_id = $2 WHERE id = $1`, w.asset, w.owner)
	w.expect(t, "without assets_basic", w.rest(t, "Otto", &utRequest{features: featureSet{}, modules: allModules}),
		nil, []string{w.beta, w.gamma})
	w.expect(t, "holder with assets_basic", w.rest(t, "Otto", &utRequest{features: featureSet{search.FeatureAssetsBasic: true}, modules: allModules}),
		[]string{w.beta, w.gamma, w.asset}, nil)

	// Field permission none on core.owner_user_id of CIs: no hit through
	// the owner; the asset keeps its own policy.
	role := f.ID()
	raw, _ := json.Marshal(utPerms)
	w.exec(t, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp788 owner hidden', $3::jsonb)`, role, f.OrgA, string(raw))
	w.exec(t, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'core.owner_user_id', 'none')`,
		f.OrgA, role)
	hidden, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(context.Background(), f.OrgA, []identity.Grant{{Permissions: utPerms, RoleID: role}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hits = w.rest(t, "Erika", &utRequest{features: allFeatures{}, modules: allModules, policy: hidden})
	w.expect(t, "owner field none", hits, []string{w.asset}, []string{w.alpha, w.beta, w.gamma})
	w.expect(t, "tags with owner field none", w.rest(t, "Produktiv788", &utRequest{features: allFeatures{}, modules: allModules, policy: hidden}),
		[]string{w.alpha, w.asset}, nil)

	// Hybrid: the remote index gets the tags and the user surrogates of the
	// full documents and is searched with the resolved ids.
	remote := newFullRemote(t)
	hybrid := &search.HybridBackend{
		Remote: search.NewOpenSearchBackend(&search.OpenSearchConfig{URL: remote.URL, Index: "wp788"}, remote.Client()),
		Source: w.repo,
	}
	if _, err = hybrid.ReindexTenant(ctx, f.OrgA); err != nil {
		t.Fatalf("hybrid reindex: %v", err)
	}
	var full struct {
		Fields      map[string]string      `json:"fields"`
		Identifiers []search.IdentifierRow `json:"identifiers"`
	}
	if err = json.Unmarshal([]byte(remote.doc("ci", w.alpha)), &full); err != nil {
		t.Fatalf("remote document of alpha: %v", err)
	}
	if !strings.Contains(full.Fields["core.tags"], "Produktiv788") {
		t.Errorf("remote document of alpha: core.tags %q", full.Fields["core.tags"])
	}
	if !slices.Contains(full.Identifiers, search.IdentifierRow{Field: search.UserFieldOwner, Norm: w.owner}) {
		t.Errorf("remote document of alpha: identifiers %v", full.Identifiers)
	}
	w.rest(t, "Erika", &utRequest{features: allFeatures{}, modules: allModules, backend: hybrid})
	if users := remoteUserTerms(t, remote); !slices.Equal(users, []string{w.owner}) {
		t.Errorf("remote query with user:read: user ids %v", users)
	}
	w.rest(t, "Erika", &utRequest{granted: noUsers, features: allFeatures{}, modules: allModules, backend: hybrid})
	if users := remoteUserTerms(t, remote); len(users) != 0 {
		t.Errorf("remote query without user:read: user ids %v", users)
	}
}

// remoteUserTerms returns the user ids of the user query of the last
// _search request of the remote: the terms on identifiers.norm of a nested
// query whose inner hits are named users.
func remoteUserTerms(t *testing.T, remote *fullRemote) []string {
	t.Helper()
	remote.mu.Lock()
	raw := remote.searches[len(remote.searches)-1]
	remote.mu.Unlock()
	var body any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("remote request: %v", err)
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if nested, ok := x["nested"].(map[string]any); ok {
				if inner, ok := nested["inner_hits"].(map[string]any); ok && inner["name"] == "users" {
					var terms func(v any)
					terms = func(v any) {
						switch y := v.(type) {
						case map[string]any:
							if t, ok := y["terms"].(map[string]any); ok {
								if ids, ok := t["identifiers.norm"].([]any); ok {
									for _, id := range ids {
										if s, ok := id.(string); ok {
											out = append(out, s)
										}
									}
								}
							}
							for _, e := range y {
								terms(e)
							}
						case []any:
							for _, e := range y {
								terms(e)
							}
						}
					}
					terms(nested["query"])
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(body)
	return out
}

// TestSearchHitsNeedFeatureAndModule covers WP-788 (SRC-02, ENT-06,
// WP-214): hits of asset and reservation need assets_basic, of document
// documents and of ticket ticketing_essential, in REST also the modules
// inventory, documents and ticketing; GraphQL searchEverything checks the
// features. Without the checks these hits are dropped (fail-closed).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchHitsNeedFeatureAndModule(t *testing.T) {
	w := newUTWorld(t)
	f := w.f
	bg := context.Background()
	w.exec(t, `UPDATE ci SET name = 'wp788gate alpha' WHERE id = $1`, w.alpha)
	w.exec(t, `UPDATE asset SET name = 'wp788gate asset' WHERE id = $1`, w.asset)
	ids := map[string]string{"ci": w.alpha, "asset": w.asset}
	var reservation, document, ticket string
	if err := f.Admin.QueryRow(bg, `INSERT INTO reservation (organization_id, item_kind, asset_id, quantity, state, reason)
		VALUES ($1, 'asset', $2, 1, 'active', 'wp788gate') RETURNING id::text`, f.OrgA, w.asset).Scan(&reservation); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'wp788gate document', 'gate788.pdf', '', $2) RETURNING id::text`, f.OrgA, w.owner).Scan(&document); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO ticket (organization_id, client_id, title, reporter_id)
		VALUES ($1, $2, 'wp788gate ticket', $3) RETURNING id::text`, f.OrgA, f.Client1, w.owner).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	ids["reservation"], ids["document"], ids["ticket"] = reservation, document, ticket
	if _, err := w.repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	all := featureSet{search.FeatureAssetsBasic: true, search.FeatureDocuments: true, search.FeatureTicketingEssential: true,
		graphqlbff.FeatureCMDBCore: true}
	without := func(key string) featureSet {
		out := featureSet{}
		for k, v := range all {
			out[k] = v && k != key
		}
		return out
	}
	moduleOff := func(key string) func(string) bool { return func(k string) bool { return k != key } }
	types := func(hits map[string]search.Hit) map[string]bool {
		out := map[string]bool{}
		for id := range hits {
			for typ, want := range ids {
				if id == want {
					out[typ] = true
				}
			}
		}
		return out
	}
	check := func(label string, got map[string]bool, missing ...string) {
		t.Helper()
		for typ := range ids {
			if want := !slices.Contains(missing, typ); got[typ] != want {
				t.Errorf("%s: hit of %s = %v, want %v", label, typ, got[typ], want)
			}
		}
	}
	rest := func(features search.FeatureChecker, modules func(string) bool) map[string]bool {
		return types(w.rest(t, "wp788gate", &utRequest{features: features, modules: modules}))
	}
	check("REST with features and modules", rest(all, allModules))
	check("REST without assets_basic", rest(without(search.FeatureAssetsBasic), allModules), "asset", "reservation")
	check("REST without documents", rest(without(search.FeatureDocuments), allModules), "document")
	check("REST without ticketing_essential", rest(without(search.FeatureTicketingEssential), allModules), "ticket")
	check("REST without module inventory", rest(all, moduleOff(search.ModuleInventory)), "asset", "reservation")
	check("REST without module documents", rest(all, moduleOff(search.ModuleDocuments)), "document")
	check("REST without module ticketing", rest(all, moduleOff(search.ModuleTicketing)), "ticket")
	check("REST without checks", types(w.rest(t, "wp788gate", &utRequest{})), "asset", "reservation", "document", "ticket")

	gql := func(features featureSet) map[string]bool {
		t.Helper()
		mux := chi.NewRouter()
		graphqlbff.NewHandler(ci.NewMemoryRepository(), relationship.NewMemoryRepository()).
			WithSources(&graphqlbff.Sources{Search: w.repo}).WithEntitlements(features).RegisterRoutes(mux)
		body, _ := json.Marshal(map[string]string{"query": `{ searchEverything(q: "wp788gate", first: 100) { hits { entityId } } }`})
		req := httptest.NewRequest(http.MethodPost, graphqlbff.Path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		rctx = identity.WithPrincipal(rctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Permissions: utPerms, Type: identity.PrincipalTypeUser})
		scope := database.OrgWideScope(f.OrgA, f.User)
		rctx = permission.WithPolicy(database.ContextWithTenantScope(rctx, &scope), permission.UnrestrictedPolicy(utPerms))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(rctx))
		var out struct {
			Data struct {
				SearchEverything struct {
					Hits []struct {
						EntityID string `json:"entityId"`
					} `json:"hits"`
				} `json:"searchEverything"`
			} `json:"data"`
			Errors []any `json:"errors"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Errors) > 0 {
			t.Fatalf("searchEverything: %d %s", rec.Code, rec.Body.String())
		}
		hits := map[string]search.Hit{}
		for _, h := range out.Data.SearchEverything.Hits {
			hits[h.EntityID] = search.Hit{}
		}
		return types(hits)
	}
	check("GraphQL with features", gql(all))
	check("GraphQL without assets_basic", gql(without(search.FeatureAssetsBasic)), "asset", "reservation")
	check("GraphQL without documents", gql(without(search.FeatureDocuments)), "document")
	check("GraphQL without ticketing_essential", gql(without(search.FeatureTicketingEssential)), "ticket")
}
