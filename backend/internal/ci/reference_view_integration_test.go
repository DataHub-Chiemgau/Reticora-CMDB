package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// refView is a decoded value of include=references; Missing is nil when
// absent from the JSON, raw holds its members.
type refView struct {
	Target     string `json:"target"`
	ID         string `json:"id"`
	Missing    *bool  `json:"missing"`
	Kind       string `json:"kind"`
	MergedInto string `json:"merged_into"`
	raw        map[string]json.RawMessage
}

// TestCIReferencesInclude covers WP-713 (MET-12) against PostgreSQL:
// GET /cis/{id}?include=references names the target of every reference
// value of the CI (type, global and instance attributes; a list for
// multi_value) with its name when the caller has the read permission of the
// target kind, and marks a deleted, merged or invisible target as missing;
// a merged CI names its survivor. The names are resolved with one statement
// per target kind and one for all merged CI targets (no N+1); attributes
// keeps the ids; without the read permission a value is {target, id};
// without a lookup (memory mode) as well. An unknown include is still 422
// and lists references.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIReferencesInclude(t *testing.T) {
	f := scopetest.Seed(t, "0b")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}

	// Targets: present CIs of both clients, a deleted CI, two merged CIs
	// with their tombstones, a user, a team, a location, an asset and a
	// document of organization A.
	ciC1 := f.CI(t, f.OrgA, f.Client1, "wp713-c1")
	ciC2 := f.CI(t, f.OrgA, f.Client2, "wp713-c2")
	survivor := f.CI(t, f.OrgA, "", "wp713-survivor")
	ciDeleted := f.CI(t, f.OrgA, "", "wp713-deleted")
	exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, ciDeleted)
	merged1 := f.CI(t, f.OrgA, "", "wp713-merged-1")
	merged2 := f.CI(t, f.OrgA, "", "wp713-merged-2")
	for _, victim := range []string{merged1, merged2} {
		exec(`UPDATE ci SET deleted_at = now(), merged_into_id = $2 WHERE id = $1`, victim, survivor)
		exec(`INSERT INTO ci_merge (organization_id, survivor_id, victim_id, snapshot) VALUES ($1, $2, $3, '{}')`, f.OrgA, survivor, victim)
	}
	userA := f.AppUser(t, f.OrgA, "wp713-a")
	teamA := scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp713 team') RETURNING id::text`, f.OrgA)
	assetA := f.Asset(t, f.OrgA, "", "WP713-ASSET")
	docA := scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'wp713 manual', 'wp713.pdf', '', $2) RETURNING id::text`, f.OrgA, userA)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	site, err := locations.NewPGRepository(f.App).Create(orgCtx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp713-site"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}

	types := citype.NewPGRepository(f.App)
	for _, d := range []citype.AttributeDefinition{
		{Key: "wp713_host", DataType: fieldmeta.TypeCIRef},
		{Key: "wp713_hosts", DataType: fieldmeta.TypeReference, ReferenceTarget: "ci", MultiValue: true},
		{Key: "wp713_owner", DataType: fieldmeta.TypeUser},
		{Key: "wp713_team", DataType: fieldmeta.TypeTeam},
		{Key: "wp713_place", DataType: fieldmeta.TypeLocation},
		{Key: "wp713_asset", DataType: fieldmeta.TypeAssetRef},
		{Key: "wp713_manual", DataType: fieldmeta.TypeFile},
		{Key: "wp713_legacy", DataType: fieldmeta.TypeCIRef},
	} {
		if cerr := types.CreateAttributeDefinition(orgCtx, f.OrgA, &d); cerr != nil {
			t.Fatalf("definition %s: %v", d.Key, cerr)
		}
	}

	// The CI of client 1 holds every kind of reference; the values are
	// stored as they are (a G1 value without UUID form among them), an
	// instance attribute under _instance.
	holder := f.CI(t, f.OrgA, f.Client1, "wp713-holder")
	if _, err = types.UpsertInstanceField(orgCtx, f.OrgA, holder, &citype.UpsertInstanceFieldRequest{
		Name: "wp713_backup", DataType: fieldmeta.TypeCIRef}); err != nil {
		t.Fatalf("instance field: %v", err)
	}
	attrs, _ := json.Marshal(map[string]any{
		"wp713_host":   strings.ToUpper(ciC2),
		"wp713_hosts":  []any{ciC1, ciDeleted, merged1, merged2},
		"wp713_owner":  userA,
		"wp713_team":   teamA,
		"wp713_place":  site.ID,
		"wp713_asset":  assetA,
		"wp713_manual": docA,
		"wp713_legacy": "srv-legacy",
		"_instance":    map[string]any{"wp713_backup": ciC1},
	})
	exec(`UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, holder, string(attrs))

	// The lookup runs on its own traced pool: its statements are counted.
	cfg := f.App.Config()
	trace := &statementLog{}
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	tracedPool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracedPool.Close)
	repo := ci.NewPGRepository(f.App)
	svc := ci.NewService(repo).WithFieldResolver(types).WithReferences(ci.NewPGReferenceLookup(tracedPool))
	memory := ci.NewService(repo).WithFieldResolver(types)
	statements := func(mark int, fragment string) int {
		n := 0
		for _, s := range trace.since(mark) {
			if strings.Contains(s.sql, fragment) {
				n++
			}
		}
		return n
	}

	allRead := []identity.Permission{identity.PermCIRead, identity.PermAssetRead, identity.PermUserRead,
		identity.PermLocationRead, identity.PermDocumentRead}
	clientCtx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	get := func(s *ci.Service, base context.Context, perms []identity.Permission, include string) (int, map[string]json.RawMessage) {
		t.Helper()
		h := ci.NewHandler(s).WithIncludes(map[string]ci.IncludeFunc{
			ci.IncludeReferences: func(ctx context.Context, orgID, ciID string) (any, bool, error) {
				refs, rerr := s.References(ctx, orgID, ciID)
				return refs, false, rerr
			},
		})
		mux := chi.NewRouter()
		h.RegisterRoutes(mux)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+holder+"?include="+include, nil)
		mux.ServeHTTP(w, req.WithContext(identity.WithPermissions(base, perms)))
		var body map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	decode := func(t *testing.T, body map[string]json.RawMessage) (map[string]refView, map[string][]refView) {
		t.Helper()
		var raw map[string]json.RawMessage
		if uerr := json.Unmarshal(body["references"], &raw); uerr != nil {
			t.Fatalf("references %s: %v", body["references"], uerr)
		}
		single, lists := map[string]refView{}, map[string][]refView{}
		for name, value := range raw {
			if strings.HasPrefix(string(value), "[") {
				var items []json.RawMessage
				_ = json.Unmarshal(value, &items)
				for _, item := range items {
					lists[name] = append(lists[name], decodeView(t, item))
				}
				continue
			}
			single[name] = decodeView(t, value)
		}
		return single, lists
	}
	name := func(v refView) string {
		raw, ok := v.raw["display_name"]
		switch {
		case !ok:
			return "<absent>"
		case string(raw) == "null":
			return "<null>"
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	present := func(t *testing.T, label string, v refView, target, id, want string) {
		t.Helper()
		if v.Target != target || !strings.EqualFold(v.ID, id) || name(v) != want || v.Missing == nil || *v.Missing {
			t.Errorf("%s: %+v (name %s), want %s %s named %q, not missing", label, v, name(v), target, id, want)
		}
	}
	missing := func(t *testing.T, label string, v refView, target, id, mergedInto string) {
		t.Helper()
		if v.Target != target || v.ID != id || name(v) != "<null>" || v.Missing == nil || !*v.Missing || v.MergedInto != mergedInto {
			t.Errorf("%s: %+v (name %s), want %s %s missing, merged_into %q", label, v, name(v), target, id, mergedInto)
		}
	}
	bare := func(t *testing.T, label string, v refView, target, id string) {
		t.Helper()
		if v.Target != target || v.ID != id || len(v.raw) != 2 {
			t.Errorf("%s: %v, want only {target %s, id %s}", label, v.raw, target, id)
		}
	}

	t.Run("names with every read permission", func(t *testing.T) {
		mark := trace.mark()
		code, body := get(svc, orgCtx, allRead, "references")
		if code != http.StatusOK {
			t.Fatalf("GET include=references: %d %v", code, body)
		}
		single, lists := decode(t, body)
		present(t, "CI of client 2 (org-wide caller)", single["wp713_host"], "ci", ciC2, "wp713-c2")
		present(t, "user", single["wp713_owner"], "user", userA, scalar(`SELECT display_name FROM app_user WHERE id = $1`, userA))
		present(t, "team", single["wp713_team"], "team", teamA, "wp713 team")
		present(t, "location", single["wp713_place"], "location", site.ID, "wp713-site")
		if single["wp713_place"].Kind != "site" {
			t.Errorf("location kind: %q, want site", single["wp713_place"].Kind)
		}
		present(t, "asset", single["wp713_asset"], "asset", assetA, "WP713-ASSET")
		present(t, "document", single["wp713_manual"], "document", docA, "wp713 manual")
		present(t, "instance attribute", single["wp713_backup"], "ci", ciC1, "wp713-c1")
		missing(t, "value without UUID form", single["wp713_legacy"], "ci", "srv-legacy", "")
		hosts := lists["wp713_hosts"]
		if len(hosts) != 4 {
			t.Fatalf("multi_value list: %+v, want 4 entries", hosts)
		}
		present(t, "list: present CI", hosts[0], "ci", ciC1, "wp713-c1")
		missing(t, "list: deleted CI", hosts[1], "ci", ciDeleted, "")
		missing(t, "list: merged CI 1", hosts[2], "ci", merged1, survivor)
		missing(t, "list: merged CI 2", hosts[3], "ci", merged2, survivor)

		// attributes keeps the ids.
		var attributes map[string]any
		_ = json.Unmarshal(body["attributes"], &attributes)
		if attributes["wp713_owner"] != userA || attributes["wp713_backup"] != ciC1 {
			t.Errorf("attributes: %v, want the stored ids", attributes)
		}
		// One statement per target kind (ci, user, team, location, asset,
		// document) and one for both merged CIs.
		if got := statements(mark, "AND id = ANY($2::uuid[])"); got != 6 {
			t.Errorf("name statements: %d, want 6 (one per target kind)", got)
		}
		if got := statements(mark, "victim_id = ANY($2::uuid[])"); got != 1 {
			t.Errorf("merge statements: %d, want 1 for all merged targets", got)
		}
	})

	t.Run("invisible target of another client", func(t *testing.T) {
		code, body := get(svc, clientCtx, allRead, "references")
		if code != http.StatusOK {
			t.Fatalf("GET as client 1: %d %v", code, body)
		}
		single, _ := decode(t, body)
		missing(t, "CI of client 2 for client 1", single["wp713_host"], "ci", strings.ToUpper(ciC2), "")
		present(t, "CI of client 1 for client 1", single["wp713_backup"], "ci", ciC1, "wp713-c1")
	})

	t.Run("without read permissions", func(t *testing.T) {
		mark := trace.mark()
		code, body := get(svc, orgCtx, []identity.Permission{identity.PermCIRead}, "references")
		if code != http.StatusOK {
			t.Fatalf("GET with ci:read only: %d %v", code, body)
		}
		single, lists := decode(t, body)
		bare(t, "user without user:read", single["wp713_owner"], "user", userA)
		bare(t, "team without user:read", single["wp713_team"], "team", teamA)
		bare(t, "location without location:read", single["wp713_place"], "location", site.ID)
		bare(t, "asset without asset:read", single["wp713_asset"], "asset", assetA)
		bare(t, "document without document:read", single["wp713_manual"], "document", docA)
		present(t, "CI with ci:read", single["wp713_host"], "ci", ciC2, "wp713-c2")
		missing(t, "merged CI with ci:read", lists["wp713_hosts"][2], "ci", merged1, survivor)
		if got := statements(mark, "AND id = ANY($2::uuid[])"); got != 1 {
			t.Errorf("name statements with ci:read only: %d, want 1", got)
		}
	})

	t.Run("memory mode", func(t *testing.T) {
		code, body := get(memory, orgCtx, allRead, "references")
		if code != http.StatusOK {
			t.Fatalf("GET without a lookup: %d %v", code, body)
		}
		single, lists := decode(t, body)
		bare(t, "CI without a lookup", single["wp713_host"], "ci", strings.ToUpper(ciC2))
		bare(t, "list without a lookup", lists["wp713_hosts"][1], "ci", ciDeleted)
	})

	t.Run("unknown include", func(t *testing.T) {
		code, body := get(svc, orgCtx, allRead, "references,secrets")
		if code != http.StatusUnprocessableEntity || !strings.Contains(string(body["detail"]), "references") {
			t.Errorf("unknown include: %d %v, want 422 listing references", code, body)
		}
	})
}

// decodeView decodes one value of include=references.
func decodeView(t *testing.T, raw json.RawMessage) refView {
	t.Helper()
	var v refView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("reference %s: %v", raw, err)
	}
	_ = json.Unmarshal(raw, &v.raw)
	return v
}
