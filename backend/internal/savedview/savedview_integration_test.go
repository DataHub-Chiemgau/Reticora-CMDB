package savedview_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSavedViewsVIE01AgainstPostgres covers VIE-01 (WP-188) against
// PostgreSQL: a view stores filter AST, sort, columns and the roles it is
// shared with; a private view of somebody else is 404 on GET, a role
// member reads a role-shared view but cannot change it; invalid sort keys
// and columns are 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSavedViewsVIE01AgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	bg := context.Background()
	owner := f.AppUser(t, f.OrgA, "view-owner")
	member := f.AppUser(t, f.OrgA, "view-member")
	stranger := f.AppUser(t, f.OrgA, "view-stranger")
	role := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name) VALUES ($1, $2, 'view-readers')`, role, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, member, role); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	savedview.NewHandler(savedview.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(user, method, path, body string) (int, map[string]any) {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, view := call(owner, http.MethodPost, "/api/v1/saved-views", `{"name": "Große Server", "entity_kind": "ci",
		"filter_spec": {"where": {"field": "attributes.ram_gb", "op": "gte", "value": 64}},
		"sort": [{"field": "name"}, {"field": "updated_at", "dir": "desc"}], "columns": ["name", "status", "attributes.ram_gb"],
		"shared_with_roles": ["`+role+`"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, view)
	}
	id, _ := view["id"].(string)
	private := func() string {
		_, v := call(owner, http.MethodPost, "/api/v1/saved-views", `{"name": "Privat", "entity_kind": "ci"}`)
		pid, _ := v["id"].(string)
		return pid
	}()

	code, got := call(owner, http.MethodGet, "/api/v1/saved-views/"+id, "")
	sortKeys, _ := got["sort"].([]any)
	columns, _ := got["columns"].([]any)
	roles, _ := got["shared_with_roles"].([]any)
	if code != http.StatusOK || len(sortKeys) != 2 || sortKeys[0].(map[string]any)["dir"] != "asc" || len(columns) != 3 || len(roles) != 1 {
		t.Errorf("stored view: %d %v", code, got)
	}
	if code, _ = call(member, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusOK {
		t.Errorf("role member reads the role-shared view: %d", code)
	}
	if code, _ = call(stranger, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusNotFound {
		t.Errorf("stranger reads the role-shared view: %d", code)
	}
	if code, _ = call(member, http.MethodGet, "/api/v1/saved-views/"+private, ""); code != http.StatusNotFound {
		t.Errorf("GET of somebody's private view: %d", code)
	}
	if code, _ = call(member, http.MethodPatch, "/api/v1/saved-views/"+id, `{"name": "gekapert"}`); code != http.StatusNotFound {
		t.Errorf("role member changes the view: %d", code)
	}
	_, list := call(member, http.MethodGet, "/api/v1/saved-views", "")
	if total, _ := list["total"].(float64); total != 1 {
		t.Errorf("member's list: %v", list)
	}
	for body, field := range map[string]string{
		`{"name": "x", "sort": [{"field": "password"}]}`:                                  "sort[0].field",
		`{"name": "x", "sort": [{"field": "name", "dir": "sideways"}]}`:                   "sort[0].dir",
		`{"name": "x", "columns": ["name", "secret"]}`:                                    "columns[1]",
		`{"name": "x", "shared_with_roles": ["admins"]}`:                                  "shared_with_roles[0]",
		`{"name": "x", "filter_spec": {"where": {"field": "x", "op": "eq", "value": 1}}}`: "filter_spec.where",
	} {
		if code, out := call(owner, http.MethodPost, "/api/v1/saved-views", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %v, want 422 on %s", body, code, out, field)
		}
	}
}

// viewCaller serves the saved-view routes on PostgreSQL and calls them as a
// user of org (an org-wide principal under the tenant GUCs).
func viewCaller(t *testing.T, f *scopetest.Fixture) func(org, user, method, path, body string) (int, map[string]any) {
	t.Helper()
	mux := chi.NewRouter()
	savedview.NewHandler(savedview.NewPGRepository(f.App)).RegisterRoutes(mux)
	return func(org, user, method, path, body string) (int, map[string]any) {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: user})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
}

// TestSavedViewPartialUpdateKeepsSharing is the binding retest of F-166
// (WP-518, VIE-01): a PATCH that sends only the name keeps the org-wide
// sharing, so another member still reads the view; only a PATCH with
// shared=false withdraws it.
func TestSavedViewPartialUpdateKeepsSharing(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	call := viewCaller(t, f)
	owner := f.AppUser(t, f.OrgA, "partial-owner")
	other := f.AppUser(t, f.OrgA, "partial-other")

	code, view := call(f.OrgA, owner, http.MethodPost, "/api/v1/saved-views",
		`{"name": "Geteilt", "entity_kind": "ci", "shared": true, "columns": ["name"]}`)
	id, _ := view["id"].(string)
	if code != http.StatusCreated || id == "" || view["shared"] != true {
		t.Fatalf("create shared view: %d %v", code, view)
	}
	if code, got := call(f.OrgA, owner, http.MethodPatch, "/api/v1/saved-views/"+id, `{"name": "Umbenannt"}`); code != http.StatusOK || got["shared"] != true || got["name"] != "Umbenannt" {
		t.Fatalf("PATCH with only name: %d %v, want 200 with shared=true", code, got)
	}
	var shared bool
	var columns []string
	if err := f.Admin.QueryRow(context.Background(), `SELECT shared, columns FROM saved_view WHERE id = $1`, id).Scan(&shared, &columns); err != nil || !shared || len(columns) != 1 {
		t.Errorf("stored after PATCH with only name: shared=%v columns=%v err=%v", shared, columns, err)
	}
	if code, got := call(f.OrgA, other, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusOK || got["shared"] != true || got["name"] != "Umbenannt" {
		t.Errorf("other member GET after PATCH with only name: %d %v, want 200 with shared=true", code, got)
	}
	if code, got := call(f.OrgA, owner, http.MethodPatch, "/api/v1/saved-views/"+id, `{}`); code != http.StatusOK || got["shared"] != true {
		t.Errorf("empty PATCH: %d %v, want 200 with shared=true", code, got)
	}

	if code, got := call(f.OrgA, owner, http.MethodPatch, "/api/v1/saved-views/"+id, `{"shared": false}`); code != http.StatusOK || got["shared"] != false || got["name"] != "Umbenannt" {
		t.Fatalf("PATCH shared=false: %d %v, want 200 with shared=false", code, got)
	}
	if code, got := call(f.OrgA, other, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusNotFound {
		t.Errorf("other member GET after unsharing: %d %v, want 404", code, got)
	}
	if code, _ := call(f.OrgA, owner, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusOK {
		t.Errorf("owner GET after unsharing: %d, want 200", code)
	}
}

// TestSavedViewRolesMustExist is the retest of F-337 (WP-518, VIE-01):
// shared_with_roles takes only roles and custom roles of the caller's
// organization; an unknown id or a role of another organization is 422 on
// shared_with_roles[i] for create and update, and nothing is stored.
func TestSavedViewRolesMustExist(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	bg := context.Background()
	call := viewCaller(t, f)
	owner := f.AppUser(t, f.OrgA, "roles-owner")
	role, customRole, foreignRole, foreignCustom := f.ID(), f.ID(), f.ID(), f.ID()
	for _, seed := range []struct{ sql, id, org, name string }{
		{`INSERT INTO role (id, organization_id, name) VALUES ($1, $2, $3)`, role, f.OrgA, "view-role"},
		{`INSERT INTO custom_role (id, organization_id, name) VALUES ($1, $2, $3)`, customRole, f.OrgA, "view-custom"},
		{`INSERT INTO role (id, organization_id, name) VALUES ($1, $2, $3)`, foreignRole, f.OrgB, "view-role-b"},
		{`INSERT INTO custom_role (id, organization_id, name) VALUES ($1, $2, $3)`, foreignCustom, f.OrgB, "view-custom-b"},
	} {
		if _, err := f.Admin.Exec(bg, seed.sql, seed.id, seed.org, seed.name); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT COUNT(*) FROM saved_view WHERE organization_id = $1`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	code, view := call(f.OrgA, owner, http.MethodPost, "/api/v1/saved-views",
		`{"name": "Rollen", "entity_kind": "ci", "shared_with_roles": ["`+role+`", "`+customRole+`"]}`)
	id, _ := view["id"].(string)
	if roles, _ := view["shared_with_roles"].([]any); code != http.StatusCreated || id == "" || len(roles) != 2 {
		t.Fatalf("create with a role and a custom role of the organization: %d %v", code, view)
	}
	before := count()

	for _, tc := range []struct{ name, roles, field string }{
		{"unknown id", `["` + f.ID() + `"]`, "shared_with_roles[0]"},
		{"role of another organization", `["` + foreignRole + `"]`, "shared_with_roles[0]"},
		{"custom role of another organization", `["` + role + `", "` + foreignCustom + `"]`, "shared_with_roles[1]"},
	} {
		code, out := call(f.OrgA, owner, http.MethodPost, "/api/v1/saved-views",
			`{"name": "Neu `+tc.name+`", "entity_kind": "ci", "shared_with_roles": `+tc.roles+`}`)
		if code != http.StatusUnprocessableEntity || out["field"] != tc.field {
			t.Errorf("create, %s: %d %v, want 422 on %s", tc.name, code, out, tc.field)
		}
		code, out = call(f.OrgA, owner, http.MethodPatch, "/api/v1/saved-views/"+id,
			`{"name": "Geändert", "shared_with_roles": `+tc.roles+`}`)
		if code != http.StatusUnprocessableEntity || out["field"] != tc.field {
			t.Errorf("update, %s: %d %v, want 422 on %s", tc.name, code, out, tc.field)
		}
	}
	if n := count(); n != before {
		t.Errorf("refused creates stored views: %d, want %d", n, before)
	}
	var name string
	var roles []string
	if err := f.Admin.QueryRow(bg, `SELECT name, shared_with_roles::text[] FROM saved_view WHERE id = $1`, id).Scan(&name, &roles); err != nil ||
		name != "Rollen" || len(roles) != 2 {
		t.Errorf("refused updates changed the view: name=%q roles=%v err=%v", name, roles, err)
	}
	if code, out := call(f.OrgA, owner, http.MethodPatch, "/api/v1/saved-views/"+id, `{"shared_with_roles": ["`+customRole+`"]}`); code != http.StatusOK {
		t.Errorf("update to a custom role of the organization: %d %v", code, out)
	}
}

// TestSavedViewSortAndColumnsAgainstPostgres is the retest of F-336
// (WP-555, VIE-01): the query engine runs a saved view with its sort and
// columns as the UI sends them (filter_spec, entity_kind, sort, columns).
// Sorting by name desc reverses the order of name asc, a numeric attribute
// sorts by number with missing values last, paging follows the sort, each
// row carries exactly the saved columns, and an asset view sorts and shows
// its custom fields. Sort keys and columns outside the whitelist of the
// entity kind are 422 when saved and when run; the filter of a view holds
// neither.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSavedViewSortAndColumnsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	bg := context.Background()
	owner := f.AppUser(t, f.OrgA, "sort-owner")
	for name, attrs := range map[string]string{
		"sv-sort-alpha":   `{"rank": 2}`,
		"sv-sort-bravo":   `{"rank": 10}`,
		"sv-sort-charlie": `{"rank": 1}`,
		"sv-sort-delta":   `{}`,
	} {
		id := f.CI(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = $2::jsonb, status = CASE WHEN $3 THEN 'maintenance' ELSE status END WHERE id = $1`,
			id, attrs, name == "sv-sort-bravo"); err != nil {
			t.Fatal(err)
		}
	}
	for tag, room := range map[string]string{"SV-SORT-1": "A", "SV-SORT-2": "C", "SV-SORT-3": "B"} {
		id := f.Asset(t, f.OrgA, f.Client1, tag)
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET custom_fields = jsonb_build_object('room', $2::text) WHERE id = $1`, id, room); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	savedview.NewHandler(savedview.NewPGRepository(f.App)).WithQueryEngine(savedview.NewPGQueryEngine(f.App)).
		WithPermissions(grants{"ci:read": true, "asset:read": true}).RegisterRoutes(mux)
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: owner})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: owner, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	// run runs a stored view the way SavedViewsPage does: its filter with
	// entity_kind, sort and columns folded in. It returns the names in the
	// order of the answer, the columns of each row and the total.
	run := func(id, query string, change func(map[string]any)) (names []string, cols []map[string]any, total int) {
		t.Helper()
		code, view := call(http.MethodGet, "/api/v1/saved-views/"+id, "")
		if code != http.StatusOK {
			t.Fatalf("GET view: %d %v", code, view)
		}
		body := map[string]any{"entity_kind": view["entity_kind"]}
		spec, _ := view["filter_spec"].(map[string]any)
		maps.Copy(body, spec)
		if s, _ := view["sort"].([]any); len(s) > 0 {
			body["sort"] = s
		}
		if c, _ := view["columns"].([]any); len(c) > 0 {
			body["columns"] = c
		}
		if change != nil {
			change(body)
		}
		raw, _ := json.Marshal(body)
		code, out := call(http.MethodPost, "/api/v1/search/query"+query, string(raw))
		if code != http.StatusOK {
			t.Fatalf("run %s: %d %v", raw, code, out)
		}
		data, _ := out["data"].([]any)
		for _, item := range data {
			row, _ := item.(map[string]any)
			name, _ := row["name"].(string)
			names = append(names, name)
			c, _ := row["columns"].(map[string]any)
			cols = append(cols, c)
		}
		n, _ := out["total"].(float64)
		return names, cols, int(n)
	}
	create := func(body string) string {
		t.Helper()
		code, view := call(http.MethodPost, "/api/v1/saved-views", body)
		id, _ := view["id"].(string)
		if code != http.StatusCreated || id == "" {
			t.Fatalf("create %s: %d %v", body, code, view)
		}
		return id
	}

	desc := create(`{"name": "Sortiert absteigend", "entity_kind": "ci", "filter_spec": {"search": "sv-sort-"},
		"sort": [{"field": "name", "dir": "desc"}], "columns": ["name", "status"]}`)
	names, cols, total := run(desc, "", nil)
	if want := []string{"sv-sort-delta", "sv-sort-charlie", "sv-sort-bravo", "sv-sort-alpha"}; !slices.Equal(names, want) || total != 4 {
		t.Errorf("sort name desc: %v (total %d), want %v", names, total, want)
	}
	for i, c := range cols {
		wantStatus := "active"
		if names[i] == "sv-sort-bravo" {
			wantStatus = "maintenance"
		}
		if len(c) != 2 || c["name"] != names[i] || c["status"] != wantStatus {
			t.Errorf("columns of row %d: %v, want exactly name and status", i, c)
		}
	}
	names, _, _ = run(desc, "", func(b map[string]any) { b["sort"] = []any{map[string]any{"field": "name", "dir": "asc"}} })
	if want := []string{"sv-sort-alpha", "sv-sort-bravo", "sv-sort-charlie", "sv-sort-delta"}; !slices.Equal(names, want) {
		t.Errorf("sort name asc: %v, want %v", names, want)
	}
	// Paging follows the sort: the second page of two.
	names, _, total = run(desc, "?limit=2&offset=2", nil)
	if want := []string{"sv-sort-bravo", "sv-sort-alpha"}; !slices.Equal(names, want) || total != 4 {
		t.Errorf("second page of name desc: %v (total %d), want %v", names, total, want)
	}

	// A numeric attribute sorts by number (10 before 2), missing values last.
	byRank := create(`{"name": "Nach Rang", "entity_kind": "ci", "filter_spec": {"search": "sv-sort-"},
		"sort": [{"field": "attributes.rank", "dir": "desc"}], "columns": ["attributes.rank"]}`)
	names, cols, _ = run(byRank, "", nil)
	if want := []string{"sv-sort-bravo", "sv-sort-alpha", "sv-sort-charlie", "sv-sort-delta"}; !slices.Equal(names, want) {
		t.Errorf("sort attributes.rank desc: %v, want %v", names, want)
	}
	if len(cols) != 4 || cols[0]["attributes.rank"] != float64(10) || cols[3]["attributes.rank"] != nil || len(cols[3]) != 1 {
		t.Errorf("rank columns: %v, want 10 first and an empty rank last", cols)
	}

	// Without sort and columns: by name, and no columns in the rows.
	plain := create(`{"name": "Ohne Darstellung", "entity_kind": "ci", "filter_spec": {"search": "sv-sort-"}}`)
	names, cols, _ = run(plain, "", nil)
	if want := []string{"sv-sort-alpha", "sv-sort-bravo", "sv-sort-charlie", "sv-sort-delta"}; !slices.Equal(names, want) ||
		slices.ContainsFunc(cols, func(c map[string]any) bool { return c != nil }) {
		t.Errorf("view without sort and columns: %v %v, want by name without columns", names, cols)
	}

	// An asset view sorts by and shows its custom fields.
	rooms := create(`{"name": "Räume", "entity_kind": "asset", "filter_spec": {"search": "SV-SORT-"},
		"sort": [{"field": "attributes.room", "dir": "desc"}], "columns": ["asset_tag", "attributes.room"]}`)
	names, cols, _ = run(rooms, "", nil)
	if want := []string{"SV-SORT-2", "SV-SORT-3", "SV-SORT-1"}; !slices.Equal(names, want) {
		t.Errorf("asset sort attributes.room desc: %v, want %v", names, want)
	}
	if len(cols) != 3 || cols[0]["asset_tag"] != "SV-SORT-2" || cols[0]["attributes.room"] != "C" || len(cols[0]) != 2 {
		t.Errorf("asset columns: %v, want asset_tag and attributes.room", cols)
	}

	// Outside the whitelist: 422 when saved and when run, naming the field.
	// 51 columns: one more than jsonb_build_object holds (MaxViewFields).
	many := make([]string, 51)
	for i := range many {
		many[i] = `"name"`
	}
	for _, tc := range []struct{ path, body, field string }{
		{"/api/v1/saved-views", `{"name": "x", "sort": [{"field": "password_hash", "dir": "desc"}]}`, "sort[0].field"},
		{"/api/v1/saved-views", `{"name": "x", "filter_spec": {"sort": [{"field": "name"}]}}`, "filter_spec.sort"},
		{"/api/v1/saved-views", `{"name": "x", "filter_spec": {"columns": ["name"]}}`, "filter_spec.columns"},
		{"/api/v1/saved-views", `{"name": "x", "columns": [` + strings.Join(many, ",") + `]}`, "columns"},
		{"/api/v1/search/query", `{"entity_kind": "ci", "sort": [{"field": "password_hash"}]}`, "sort[0].field"},
		{"/api/v1/search/query", `{"entity_kind": "ci", "sort": [{"field": "name", "dir": "up"}]}`, "sort[0].dir"},
		{"/api/v1/search/query", `{"entity_kind": "ci", "columns": ["name", "name; DROP TABLE ci"]}`, "columns[1]"},
		{"/api/v1/search/query", `{"entity_kind": "asset", "sort": [{"field": "hostname"}]}`, "sort[0].field"},
		{"/api/v1/search/query", `{"entity_kind": "ci", "columns": ["attributes.Bad-Key"]}`, "columns[0]"},
	} {
		if code, out := call(http.MethodPost, tc.path, tc.body); code != http.StatusUnprocessableEntity || out["field"] != tc.field {
			t.Errorf("%s %s: %d %v, want 422 on %s", tc.path, tc.body, code, out, tc.field)
		}
	}
}

// TestSavedViewRelationshipFilterSeesProjection is the binding retest of
// F-244 (WP-573, DB-05): contains, mounted_in and located_in are not
// stored but projected by ci_structure_edge (parent_ci_id, rack_mount,
// location_id). has_relationship finds a CI at either end of a projected
// edge and lacks_relationship leaves it out, under the RLS of the caller:
// a CI of another client never appears, and an edge whose other CI the
// caller cannot see does not count, as in the relationship API. A stored
// type still reads ci_relationship.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSavedViewRelationshipFilterSeesProjection(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	bg := context.Background()
	seed := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Client 1: chassis contains blade; plain has no structure. Client 2:
	// chassis-c2 contains blade-c2. mixed (client 1) contains blade-x of
	// client 2: each end is invisible to the other client.
	chassis := f.CI(t, f.OrgA, f.Client1, "rel-chassis")
	blade := f.CI(t, f.OrgA, f.Client1, "rel-blade")
	plain := f.CI(t, f.OrgA, f.Client1, "rel-plain")
	chassis2 := f.CI(t, f.OrgA, f.Client2, "rel-chassis-c2")
	blade2 := f.CI(t, f.OrgA, f.Client2, "rel-blade-c2")
	mixed := f.CI(t, f.OrgA, f.Client1, "rel-mixed")
	bladeX := f.CI(t, f.OrgA, f.Client2, "rel-blade-x")
	for child, parent := range map[string]string{blade: chassis, blade2: chassis2, bladeX: mixed} {
		seed(`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, child, parent)
	}
	// mounted is mounted in a rack of client 1, located stands in its room.
	tree := locations.NewPGRepository(f.App)
	node := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(f.OrgCtx(f.OrgA), f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	site := node(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "rel-s1"})
	building := node(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "rel-b1"})
	room := node(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "rel-r1"})
	rack := node(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "rel-k1"})
	mounted := f.CI(t, f.OrgA, f.Client1, "rel-mounted")
	located := f.CI(t, f.OrgA, f.Client1, "rel-located")
	seed(`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u) VALUES ($1, $2, $3, 1, 1)`, f.OrgA, rack, mounted)
	seed(`UPDATE ci SET location_id = $2 WHERE id = $1`, located, room)
	// A stored edge plain → chassis-c2 (depends_on) crosses the clients.
	seed(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
		f.OrgA, plain, chassis2)
	var projected int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_structure_edge WHERE organization_id = $1`, f.OrgA).Scan(&projected); err != nil || projected != 5 {
		t.Fatalf("projected edges: %d (%v), want 5 (3 contains, mounted_in, located_in)", projected, err)
	}

	engine := savedview.NewPGQueryEngine(f.App)
	run := func(ctx context.Context, spec savedview.FilterSpec) []string {
		t.Helper()
		spec.EntityKind = "ci"
		spec.Search = "rel-"
		results, total, err := engine.Query(ctx, f.OrgA, spec, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatalf("%+v: %v", spec, err)
		}
		names := make([]string, 0, len(results))
		for _, r := range results {
			names = append(names, r.Name)
		}
		slices.Sort(names)
		if total != len(names) {
			t.Errorf("%+v: total %d for %v", spec, total, names)
		}
		return names
	}
	client1, client2, orgWide := f.ClientCtx(f.Client1), f.ClientCtx(f.Client2), f.OrgCtx(f.OrgA)
	for _, c := range []struct {
		name string
		ctx  context.Context
		spec savedview.FilterSpec
		want []string
	}{
		{"client 1 has contains", client1, savedview.FilterSpec{HasRelationship: "contains"},
			[]string{"rel-blade", "rel-chassis"}},
		{"client 1 lacks contains", client1, savedview.FilterSpec{LacksRelationship: "contains"},
			[]string{"rel-located", "rel-mixed", "rel-mounted", "rel-plain"}},
		{"client 2 has contains", client2, savedview.FilterSpec{HasRelationship: "contains"},
			[]string{"rel-blade-c2", "rel-chassis-c2"}},
		{"client 2 lacks contains", client2, savedview.FilterSpec{LacksRelationship: "contains"},
			[]string{"rel-blade-x"}},
		{"org-wide has contains", orgWide, savedview.FilterSpec{HasRelationship: "contains"},
			[]string{"rel-blade", "rel-blade-c2", "rel-blade-x", "rel-chassis", "rel-chassis-c2", "rel-mixed"}},
		{"org-wide lacks contains", orgWide, savedview.FilterSpec{LacksRelationship: "contains"},
			[]string{"rel-located", "rel-mounted", "rel-plain"}},
		{"client 1 has mounted_in", client1, savedview.FilterSpec{HasRelationship: "mounted_in"},
			[]string{"rel-mounted"}},
		{"client 1 lacks mounted_in", client1, savedview.FilterSpec{LacksRelationship: "mounted_in"},
			[]string{"rel-blade", "rel-chassis", "rel-located", "rel-mixed", "rel-plain"}},
		{"client 2 has mounted_in", client2, savedview.FilterSpec{HasRelationship: "mounted_in"}, []string{}},
		{"client 1 has located_in", client1, savedview.FilterSpec{HasRelationship: "located_in"},
			[]string{"rel-located"}},
		{"client 1 lacks located_in", client1, savedview.FilterSpec{LacksRelationship: "located_in"},
			[]string{"rel-blade", "rel-chassis", "rel-mixed", "rel-mounted", "rel-plain"}},
		{"client 2 has located_in", client2, savedview.FilterSpec{HasRelationship: "located_in"}, []string{}},
		{"org-wide has depends_on", orgWide, savedview.FilterSpec{HasRelationship: "depends_on"},
			[]string{"rel-chassis-c2", "rel-plain"}},
		{"client 1 has depends_on", client1, savedview.FilterSpec{HasRelationship: "depends_on"}, []string{}},
		{"has contains and lacks mounted_in", orgWide, savedview.FilterSpec{HasRelationship: "contains", LacksRelationship: "mounted_in"},
			[]string{"rel-blade", "rel-blade-c2", "rel-blade-x", "rel-chassis", "rel-chassis-c2", "rel-mixed"}},
	} {
		if got := run(c.ctx, c.spec); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
