package savedview

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// presetEnv serves the saved-view routes on PostgreSQL with every
// permission and feature and calls them as a user of organization A: the
// context carries the tenant scope of that user (database.OrgWideScope),
// whose app.user_id owned_by_me reads; f.OrgCtx would always carry f.User.
type presetEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	mux *chi.Mux
}

func (e *presetEnv) call(user, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	org := e.f.OrgA
	scope := database.OrgWideScope(org, user)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: user})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser})
	ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(nil))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// run executes the system view of key through POST /api/v1/search/query as
// user, with its filter, entity kind, sort and columns, and returns the
// sorted names of the rows.
func (e *presetEnv) run(user, key string) []string {
	e.t.Helper()
	p, ok := PresetByKey(key)
	if !ok {
		e.t.Fatalf("no preset %s", key)
	}
	body := map[string]any{}
	for k, v := range p.FilterSpec {
		body[k] = v
	}
	body["entity_kind"], body["sort"], body["columns"] = p.EntityKind, p.Sort, p.Columns
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	code, out := e.call(user, http.MethodPost, "/api/v1/search/query?limit=100", string(raw))
	if code != http.StatusOK {
		e.t.Fatalf("run %s: %d %v", key, code, out)
	}
	data, _ := out["data"].([]any)
	names := []string{}
	for _, row := range data {
		name, _ := row.(map[string]any)["name"].(string)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestSystemViewsAgainstPostgres covers the system views of VIE-02
// (WP-307): each one returns the expected rows through POST
// /api/v1/search/query. my_devices_asset and my_devices_ci match the own
// owner, a team of the user (team_member) and, for assets, the current
// holder of the holder cache, user or team; another user matches none.
// warranty_expiring matches warranty_days_left 10 and 30, not 31 or none.
// servers_without_monitoring matches servers and subtypes of server without
// an outgoing monitored_by, an incoming monitors relationship and a sample
// within 24 hours; a deleted CI never. The presets taken over use the
// system types with subtypes (servers_without_backup,
// available_clients_berlin) and the holder cache (deployed_without_holder).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSystemViewsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	systemType := func(key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatalf("system type %s: %v", key, err)
		}
		return id
	}
	server, client, vm := systemType("server"), systemType("client"), systemType("vm")
	subtype := f.ID()
	exec(`INSERT INTO ci_type (id, organization_id, key, name, parent_type_id) VALUES ($1, $2, 'wp307_blade', 'WP-307 Blade', $3)`,
		subtype, f.OrgA, server)
	ci := func(name, typeID string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status) VALUES ($1, $2, $3, $4, $5, 'active')`,
			id, f.OrgA, f.Client1, typeID, name)
		return id
	}
	me := f.AppUser(t, f.OrgA, "wp307-me")
	stranger := f.AppUser(t, f.OrgA, "wp307-stranger")
	team := f.ID()
	exec(`INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp307-team')`, team, f.OrgA)
	exec(`INSERT INTO team_member (organization_id, team_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, team, me)

	// My devices: CIs of type vm, so that no server view counts them.
	exec(`UPDATE ci SET owner_user_id = $2 WHERE id = $1`, ci("my-ci-own", vm), me)
	exec(`UPDATE ci SET owner_team_id = $2 WHERE id = $1`, ci("my-ci-team", vm), team)
	exec(`UPDATE ci SET owner_user_id = $2 WHERE id = $1`, ci("my-ci-other", vm), stranger)
	exec(`UPDATE asset SET owner_user_id = $2, lifecycle_state = 'deployed' WHERE id = $1`, f.Asset(t, f.OrgA, f.Client1, "my-asset-own"), me)
	exec(`UPDATE asset SET owner_team_id = $2 WHERE id = $1`, f.Asset(t, f.OrgA, f.Client1, "my-asset-team"), team)
	exec(`UPDATE asset SET assigned_user_id = $2, lifecycle_state = 'deployed' WHERE id = $1`, f.Asset(t, f.OrgA, f.Client1, "my-asset-held"), me)
	exec(`UPDATE asset SET assigned_team_id = $2 WHERE id = $1`, f.Asset(t, f.OrgA, f.Client1, "my-asset-team-held"), team)
	exec(`UPDATE asset SET owner_user_id = $2, assigned_user_id = $2 WHERE id = $1`, f.Asset(t, f.OrgA, f.Client1, "my-asset-other"), stranger)

	// Warranty: the computed attribute of 000225 as the refresh writes it.
	for name, days := range map[string]any{"w-10": 10, "w-30": 30, "w-31": 31, "w-none": nil} {
		id := ci(name, vm)
		if days != nil {
			exec(`UPDATE ci SET attributes = jsonb_build_object('warranty_days_left', $2::int) WHERE id = $1`, id, days)
		}
	}

	// Servers: the monitoring system is a vm.
	tool := ci("tool", vm)
	plain, sub := ci("s-plain", server), ci("s-sub", subtype)
	monitoredIn, monitoredOut := ci("s-monitors-in", server), ci("s-monitored-by-out", server)
	recent, stale := ci("s-metric-1h", server), ci("s-metric-25h", server)
	exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, ci("s-deleted", server))
	rel := func(source, target, relType string) {
		t.Helper()
		exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, $4)`,
			f.OrgA, source, target, relType)
	}
	rel(tool, monitoredIn, "monitors")
	rel(monitoredOut, tool, "monitored_by")
	// The opposite directions do not count: the tool monitored by the
	// blade, a server that monitors the tool.
	rel(tool, sub, "monitored_by")
	rel(stale, tool, "monitors")
	rel(plain, tool, "backed_up_by")
	for id, hours := range map[string]int{recent: 1, stale: 25} {
		exec(`INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value)
			VALUES (now() - make_interval(hours => $3), $1, $2, 'cpu', 1)`, f.OrgA, id, hours)
	}

	// Clients in Berlin: an asset of the type client in stock below the
	// site Berlin; one elsewhere, one deployed and one of another type.
	var berlin, munich string
	for name, into := range map[string]*string{"WP307 Berlin": &berlin, "WP307 München": &munich} {
		if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, f.Client1, name).Scan(into); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []struct{ tag, typeID, location, state string }{
		{"client-berlin", client, berlin, "in_stock"},
		{"client-munich", client, munich, "in_stock"},
		{"client-berlin-deployed", client, berlin, "deployed"},
		{"server-berlin", server, berlin, "in_stock"},
	} {
		exec(`UPDATE asset SET asset_type_id = $2, location_id = $3, lifecycle_state = $4 WHERE id = $1`,
			f.Asset(t, f.OrgA, f.Client1, a.tag), a.typeID, a.location, a.state)
	}

	e := &presetEnv{t: t, f: f, mux: chi.NewRouter()}
	NewHandler(NewPGRepository(f.App)).WithQueryEngine(NewPGQueryEngine(f.App)).WithPermissions(srcAllowAll{}).
		WithEntitlements(srcAllowAll{}).WithInventory(true).RegisterRoutes(e.mux)

	for _, c := range []struct {
		key, user string
		want      []string
	}{
		{"my_devices_ci", me, []string{"my-ci-own", "my-ci-team"}},
		{"my_devices_ci", stranger, []string{"my-ci-other"}},
		{"my_devices_asset", me, []string{"my-asset-held", "my-asset-own", "my-asset-team", "my-asset-team-held"}},
		{"my_devices_asset", stranger, []string{"my-asset-other"}},
		{"warranty_expiring", me, []string{"w-10", "w-30"}},
		{"servers_without_monitoring", me, []string{"s-metric-25h", "s-plain", "s-sub"}},
		{"servers_without_backup", me, []string{"s-metric-1h", "s-metric-25h", "s-monitored-by-out", "s-monitors-in", "s-sub"}},
		{"available_clients_berlin", me, []string{"client-berlin"}},
		{"deployed_without_holder", me, []string{"client-berlin-deployed", "my-asset-own"}},
		{"discovery_conflicts", me, []string{}},
	} {
		if got := e.run(c.user, c.key); !slices.Equal(got, c.want) {
			t.Errorf("%s as %s: %v, want %v", c.key, c.user, got, c.want)
		}
	}
	// A user without any device matches nothing.
	nobody := f.AppUser(t, f.OrgA, "wp307-nobody")
	for _, key := range []string{"my_devices_ci", "my_devices_asset"} {
		if got := e.run(nobody, key); len(got) != 0 {
			t.Errorf("%s as a user without devices: %v", key, got)
		}
	}
}

// TestSystemViewPresetsAndCloneAgainstPostgres covers the delivery and the
// clone of the system views (VIE-02, WP-307): GET /api/v1/saved-views/presets
// lists every preset with key, display_name (de, en) and system true and
// no cursor; POST /api/v1/saved-views with from_preset answers 201 with the
// filter, sort and columns of the preset and the name and sharing of the
// request; an unknown key, from_preset with filter_spec and from_preset in
// a PATCH are 422 on from_preset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSystemViewPresetsAndCloneAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	e := &presetEnv{t: t, f: f, mux: chi.NewRouter()}
	NewHandler(NewPGRepository(f.App)).WithQueryEngine(NewPGQueryEngine(f.App)).WithPermissions(srcAllowAll{}).
		WithEntitlements(srcAllowAll{}).WithInventory(true).RegisterRoutes(e.mux)
	user := f.AppUser(t, f.OrgA, "wp307-clone")

	code, out := e.call(user, http.MethodGet, "/api/v1/saved-views/presets", "")
	data, _ := out["data"].([]any)
	if _, cursor := out["next_cursor"]; code != http.StatusOK || cursor || len(data) != len(Presets()) {
		t.Fatalf("presets: %d %v", code, out)
	}
	keys := map[string]map[string]any{}
	for _, item := range data {
		p, _ := item.(map[string]any)
		key, _ := p["key"].(string)
		names, _ := p["display_name"].(map[string]any)
		if key == "" || p["system"] != true || names["de"] == "" || names["en"] == "" || names["de"] == nil || names["en"] == nil {
			t.Errorf("preset without key, display_name or system: %v", p)
		}
		keys[key] = p
	}
	for key, de := range map[string]string{
		"my_devices_asset": "Meine Geräte", "my_devices_ci": "Meine Geräte",
		"warranty_expiring": "Garantie läuft bald ab", "servers_without_monitoring": "Server ohne Monitoring",
	} {
		names, _ := keys[key]["display_name"].(map[string]any)
		if names["de"] != de {
			t.Errorf("preset %s: display_name %v, want de %q", key, keys[key]["display_name"], de)
		}
	}

	code, view := e.call(user, http.MethodPost, "/api/v1/saved-views",
		`{"name": "Meine Kopie", "from_preset": "servers_without_monitoring", "shared": true}`)
	preset, _ := PresetByKey("servers_without_monitoring")
	want, _ := json.Marshal(preset.FilterSpec)
	got, _ := json.Marshal(view["filter_spec"])
	columns, _ := view["columns"].([]any)
	if code != http.StatusCreated || string(got) != string(want) || view["entity_kind"] != "ci" || view["shared"] != true ||
		view["name"] != "Meine Kopie" || len(columns) != len(preset.Columns) {
		t.Fatalf("clone: %d %v, want the filter %s", code, view, want)
	}
	id, _ := view["id"].(string)
	code, stored := e.call(user, http.MethodGet, "/api/v1/saved-views/"+id, "")
	if got, _ = json.Marshal(stored["filter_spec"]); code != http.StatusOK || string(got) != string(want) {
		t.Errorf("stored clone: %d %v", code, stored)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/saved-views", `{"name": "x", "from_preset": "no_such_view"}`},
		{http.MethodPost, "/api/v1/saved-views", `{"name": "x", "from_preset": "warranty_expiring", "filter_spec": {"status": "active"}}`},
		{http.MethodPatch, "/api/v1/saved-views/" + id, `{"from_preset": "warranty_expiring"}`},
	} {
		if code, out := e.call(user, c.method, c.path, c.body); code != http.StatusUnprocessableEntity || out["field"] != "from_preset" {
			t.Errorf("%s %s %s: %d %v, want 422 on from_preset", c.method, c.path, c.body, code, out)
		}
	}
	if _, after := e.call(user, http.MethodGet, "/api/v1/saved-views/"+id, ""); after["name"] != "Meine Kopie" {
		t.Errorf("view after the refused PATCH: %v", after)
	}
}
