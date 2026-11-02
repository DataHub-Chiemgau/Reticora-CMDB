package history_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestHistoryRepositoryClientScope runs the history repository with the
// principal's tenant scope (TEN-06, WP-011): a principal restricted to client
// 1 neither reads nor extends the change trail of a client-2 CI; its
// history is not found (WP-475).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHistoryRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "16")
	own := f.CI(t, f.OrgA, f.Client1, "history-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "history-c2")

	repo := history.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	for _, id := range []string{own, foreign} {
		if err := repo.Record(orgCtx, &history.Change{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, ChangeType: "update", FieldName: "name"}); err != nil {
			t.Fatalf("org-wide record for %s: %v", id, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	changes, total, err := repo.List(ctx, f.OrgA, "ci", own, page, history.ListOptions{IncludeAudit: true})
	if err != nil || total != 1 || len(changes) != 1 {
		t.Fatalf("client-1 trail of own CI: total=%d len=%d err=%v, want 1/1/nil", total, len(changes), err)
	}
	changes, total, err = repo.List(ctx, f.OrgA, "ci", foreign, page, history.ListOptions{IncludeAudit: true})
	if !errors.Is(err, api.ErrNotFound) || total != 0 || len(changes) != 0 {
		t.Fatalf("client-1 trail of client-2 CI: total=%d len=%d err=%v, want 0/0/api.ErrNotFound", total, len(changes), err)
	}
	trail, err := repo.TrailUpTo(ctx, f.OrgA, "ci", foreign, time.Now().Add(time.Minute))
	if err != nil || len(trail) != 0 {
		t.Fatalf("client-1 replay of client-2 CI: len=%d err=%v, want 0/nil", len(trail), err)
	}
	if err = repo.Record(ctx, &history.Change{OrganizationID: f.OrgA, EntityType: "ci", EntityID: foreign, ChangeType: "comment"}); err == nil {
		t.Fatal("client-1 principal extended the trail of a client-2 CI")
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM entity_change WHERE entity_id = $1`, foreign).Scan(&count); err != nil {
		t.Fatalf("count foreign trail: %v", err)
	}
	if count != 1 {
		t.Fatalf("client-2 CI trail has %d rows, want 1", count)
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, "ci", own, page, history.ListOptions{IncludeAudit: true}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestLocationHistoryClientScope covers LOC-11 and TEN-06 (WP-473): a
// principal restricted to client 1 reads the history of a moved client-1
// location, while GET /api/v1/history/location/{id} of a moved client-2
// location is 404, before and after that location's deletion; an org-wide
// principal still reads the deleted location's move.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationHistoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "16")
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	locs := locations.NewPGRepository(f.App)
	moved := map[string]string{}
	for _, client := range []string{f.Client1, f.Client2} {
		tree := locationTree{t: t, ctx: orgCtx, org: f.OrgA, clientID: client, repo: locs}
		site := tree.mk(locations.KindSite, "", "hist-scope-site-"+client[len(client)-2:])
		b1 := tree.mk(locations.KindBuilding, site, "hist-scope-b1")
		b2 := tree.mk(locations.KindBuilding, site, "hist-scope-b2")
		room := tree.mk(locations.KindRoom, b1, "hist-scope-room")
		tree.move(room, b2)
		moved[client] = room
	}
	h := history.NewHandler(history.NewPGRepository(f.App))
	ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	if rec, own := getHistory(ctx, t, h, "/api/v1/history/location/"+moved[f.Client1]); rec.Code != http.StatusOK || own.Total != 1 {
		t.Fatalf("client-1 history of own moved room: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := getHistory(ctx, t, h, "/api/v1/history/location/"+moved[f.Client2]); rec.Code != http.StatusNotFound {
		t.Fatalf("client-1 history of a client-2 room: %d %s, want 404", rec.Code, rec.Body)
	}
	if _, _, err := history.NewPGRepository(f.App).List(ctx, f.OrgA, "location", moved[f.Client2], api.PaginationParams{Limit: 100}, history.ListOptions{IncludeAudit: true}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("client-1 repository list of a client-2 room: %v, want api.ErrNotFound", err)
	}
	if err := locs.Delete(orgCtx, f.OrgA, moved[f.Client2]); err != nil {
		t.Fatalf("delete client-2 room: %v", err)
	}
	if rec, _ := getHistory(ctx, t, h, "/api/v1/history/location/"+moved[f.Client2]); rec.Code != http.StatusNotFound {
		t.Fatalf("client-1 history of a deleted client-2 room: %d %s, want 404", rec.Code, rec.Body)
	}
	if rec, other := getHistory(orgCtx, t, h, "/api/v1/history/location/"+moved[f.Client2]); rec.Code != http.StatusOK || other.Total != 1 {
		t.Fatalf("org-wide history of the deleted client-2 room: %d %s", rec.Code, rec.Body)
	}
}

// TestHistoryAccessByRole covers CI-06 and E-78 (WP-475, F-094): GET
// /api/v1/history/{entityType}/{id} needs ci:read on the route, the read
// permission of the entity type in the handler, and lists the audit
// entries of a CI only for a principal with audit:read. Engineer and
// viewer read the ci_change, override and location_change entries of a
// client-1 CI without its audit entries, the org_admin with them; a
// principal without ci:read is 403, without relationship:read, location:read
// or asset:read for those types 403; the CI of another client is 404.
//
// The request runs through the route authorization (server.AuthorizeRoute)
// with the permissions of the standard roles (RBA-02).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHistoryAccessByRole(t *testing.T) {
	f := scopetest.Seed(t, "75")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	tree := locationTree{t: t, ctx: orgCtx, org: f.OrgA, clientID: f.Client1, repo: locations.NewPGRepository(f.App)}
	site := tree.mk(locations.KindSite, "", "hist-role-site")
	b1 := tree.mk(locations.KindBuilding, site, "hist-role-b1")
	b2 := tree.mk(locations.KindBuilding, site, "hist-role-b2")
	room := tree.mk(locations.KindRoom, b1, "hist-role-room")
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	item := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, LocationID: room, CITypeID: typeID, Name: "hist-role-ci", Status: "active"}
	if err := cis.Create(orgCtx, item); err != nil {
		t.Fatal(err)
	}
	renamed := "hist-role-ci-2"
	if _, err := cis.Update(orgCtx, f.OrgA, item.ID, ci.UpdateRequest{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	if _, err := override.NewPGRepository(f.App).SetOverride(orgCtx, f.OrgA, item.ID, "model", "manual-model", f.User, "history roles", true); err != nil {
		t.Fatalf("set override: %v", err)
	}
	tree.move(room, b2)
	foreign := f.CI(t, f.OrgA, f.Client2, "hist-role-c2")

	const route = "/api/v1/history/{entityType}/{id}"
	r := chi.NewRouter()
	h := history.NewHandler(history.NewPGRepository(f.App))
	r.With(server.AuthorizeRoute(http.MethodGet, route)).Get(route, h.List)
	clientCtx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	get := func(keys []string, path string) (*httptest.ResponseRecorder, history.ListResponse) {
		t.Helper()
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		ctx := identity.WithPrincipal(clientCtx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Permissions: perms, Type: identity.PrincipalTypeUser})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		var body history.ListResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
		}
		return rec, body
	}
	role := func(name string) []string {
		t.Helper()
		keys, ok := permission.StandardRolePermissions(name)
		if !ok {
			t.Fatalf("standard role %s missing", name)
		}
		return keys
	}
	without := func(keys []string, drop string) []string {
		out := []string{}
		for _, k := range keys {
			if k != drop {
				out = append(out, k)
			}
		}
		return out
	}
	ciPath := "/api/v1/history/ci/" + item.ID

	for _, name := range []string{"engineer", "viewer", "org_admin"} {
		t.Run(name, func(t *testing.T) {
			keys := role(name)
			admin := name == "org_admin"
			if slices.Contains(keys, string(identity.PermAuditRead)) != admin {
				t.Fatalf("precondition: %s holds audit:read = %v (RBA-02)", name, !admin)
			}
			rec, body := get(keys, ciPath)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s GET %s: %d %s, want 200", name, ciPath, rec.Code, rec.Body)
			}
			sources := map[string]int{}
			overrides := 0
			for _, c := range body.Data {
				sources[c.Source]++
				if c.Source == history.SourceCIChange && c.ChangeType == "override" {
					overrides++
				}
			}
			if sources[history.SourceCIChange] < 2 || overrides != 1 || sources[history.SourceLocationChange] != 1 {
				t.Errorf("%s: sources %v, %d override entries; want ci_change incl. one override and one location_change", name, sources, overrides)
			}
			if admin && sources[history.SourceAudit] < 3 || !admin && sources[history.SourceAudit] != 0 {
				t.Errorf("%s: %d audit entries, audit:read %v; want create, update and override_set only with audit:read", name, sources[history.SourceAudit], admin)
			}
			if body.AuditIncluded != admin || body.Total != len(body.Data) {
				t.Errorf("%s: audit_included %v, total %d of %d entries", name, body.AuditIncluded, body.Total, len(body.Data))
			}
			if rec, _ := get(keys, "/api/v1/history/ci/"+foreign); rec.Code != http.StatusNotFound {
				t.Errorf("%s history of a client-2 CI: %d %s, want 404", name, rec.Code, rec.Body)
			}
			if rec, _ := get(keys, "/api/v1/history/location/"+room); rec.Code != http.StatusOK {
				t.Errorf("%s history of the CI's room: %d %s, want 200", name, rec.Code, rec.Body)
			}
		})
	}

	engineer := role("engineer")
	if rec, _ := get(without(engineer, "ci:read"), ciPath); rec.Code != http.StatusForbidden {
		t.Errorf("without ci:read: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec, _ := get([]string{"audit:read"}, ciPath); rec.Code != http.StatusForbidden {
		t.Errorf("audit:read without ci:read: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec, _ := get(without(engineer, "relationship:read"), "/api/v1/history/relationship/"+f.ID()); rec.Code != http.StatusForbidden {
		t.Errorf("relationship history without relationship:read: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec, _ := get(engineer, "/api/v1/history/relationship/"+f.ID()); rec.Code != http.StatusOK {
		t.Errorf("relationship history with relationship:read: %d %s, want 200", rec.Code, rec.Body)
	}
	if rec, _ := get(without(engineer, "location:read"), "/api/v1/history/location/"+room); rec.Code != http.StatusForbidden {
		t.Errorf("location history without location:read: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec, _ := get([]string{"ci:read"}, "/api/v1/history/asset/"+f.ID()); rec.Code != http.StatusForbidden {
		t.Errorf("asset history without asset:read: %d %s, want 403", rec.Code, rec.Body)
	}
}
