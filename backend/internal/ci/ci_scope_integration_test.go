package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestCIRepositoryClientScope runs the CI repository with the principal's
// tenant scope from the request context (TEN-06, WP-010): a principal
// restricted to client 1 neither sees nor changes a CI of client 2 or of
// another organization, and a call without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "10")
	own := f.CI(t, f.OrgA, f.Client1, "ci-client-1")
	foreign := f.CI(t, f.OrgA, f.Client2, "ci-client-2")
	otherOrg := f.CI(t, f.OrgB, "", "ci-org-b")

	repo := ci.NewPGRepository(f.App)
	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}

	items, _, err := repo.List(ctx, f.OrgA, ci.FilterParams{}, page)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if !seen[own] || seen[foreign] || seen[otherOrg] {
		t.Fatalf("client-1 list: own=%v foreign=%v other org=%v, want true/false/false", seen[own], seen[foreign], seen[otherOrg])
	}

	if _, err = repo.GetByID(ctx, f.OrgA, foreign); err == nil {
		t.Fatal("client-1 principal read a CI of client 2")
	}

	name := "renamed"
	if _, err = repo.Update(ctx, f.OrgA, foreign, ci.UpdateRequest{Name: &name}); err == nil {
		t.Fatal("client-1 principal updated a CI of client 2")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign); err == nil {
		t.Fatal("client-1 principal deleted a CI of client 2")
	}
	var gotName string
	var deleted bool
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT name, deleted_at IS NOT NULL FROM ci WHERE id = $1`, foreign).Scan(&gotName, &deleted); err != nil {
		t.Fatalf("read foreign ci: %v", err)
	}
	if gotName != "ci-client-2" || deleted {
		t.Fatalf("foreign CI changed: name=%q deleted=%v", gotName, deleted)
	}

	// Moving the own CI to client 2 and creating a CI for client 2 both
	// violate the policy's WITH CHECK.
	client2 := f.Client2
	if _, err = repo.Update(ctx, f.OrgA, own, ci.UpdateRequest{ClientID: &client2}); err == nil {
		t.Fatal("client-1 principal moved a CI to client 2")
	}
	typeID := ciTypeOf(t, f, own)
	if err = repo.Create(ctx, &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client2, CITypeID: typeID, Name: "new-c2", Status: "active"}); err == nil {
		t.Fatal("client-1 principal created a CI for client 2")
	}
	created := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typeID, Name: "new-c1", Status: "active"}
	if err = repo.Create(ctx, created); err != nil {
		t.Fatalf("client-1 principal could not create a CI for client 1: %v", err)
	}

	// An org-wide principal of organization A sees both clients.
	items, _, err = repo.List(f.OrgCtx(f.OrgA), f.OrgA, ci.FilterParams{}, page)
	if err != nil {
		t.Fatalf("org-wide list: %v", err)
	}
	seen = map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if !seen[own] || !seen[foreign] || seen[otherOrg] {
		t.Fatalf("org-wide list: own=%v foreign=%v other org=%v, want true/true/false", seen[own], seen[foreign], seen[otherOrg])
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, ci.FilterParams{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
	if _, err = repo.GetByID(ctx, f.OrgB, otherOrg); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read with another organization: got %v, want ErrTenantMismatch", err)
	}
}

// TestAuditT3ParentMustBeVisibleToScopedPrincipal covers WP-079 (DB-05,
// TEN-05, TEN-06; F-086, WP-370): a client-1 principal cannot attach its CI
// to a client-2 chassis through Service.Update with Manual (the PATCH
// /api/v1/cis/{id} path); a same-client and an org-wide parent are accepted.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditT3ParentMustBeVisibleToScopedPrincipal(t *testing.T) {
	f := scopetest.Seed(t, "6d")
	bg := context.Background()
	svc := ci.NewService(ci.NewPGRepository(f.App))
	manual := &ci.ManualChange{Author: f.User}
	parentOf := func(id string) string {
		t.Helper()
		var p string
		if err := f.Admin.QueryRow(bg, `SELECT coalesce(parent_ci_id::text, '') FROM ci WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	client1 := f.ClientCtx(f.Client1)

	// Controls: a same-client parent and an org-wide parent are accepted.
	child := f.CI(t, f.OrgA, f.Client1, "audit-t3-child")
	sameClient := f.CI(t, f.OrgA, f.Client1, "audit-t3-chassis-c1")
	if _, err := svc.Update(client1, f.OrgA, child, ci.UpdateRequest{ParentCIID: &sameClient, Manual: manual}); err != nil {
		t.Fatalf("control: same-client parent refused: %v", err)
	}
	orgWide := f.CI(t, f.OrgA, "", "audit-t3-chassis-org")
	if _, err := svc.Update(client1, f.OrgA, child, ci.UpdateRequest{ParentCIID: &orgWide, Manual: manual}); err != nil {
		t.Fatalf("control: org-wide parent refused: %v", err)
	}

	foreign := f.CI(t, f.OrgA, f.Client2, "audit-t3-chassis-c2")
	_, err := svc.Update(client1, f.OrgA, child, ci.UpdateRequest{ParentCIID: &foreign, Manual: manual})
	if err == nil {
		t.Errorf("client-1 principal set parent_ci_id to a client-2 CI without error (TEN-05: an invisible parent must be refused)")
	}
	if got := parentOf(child); got != orgWide {
		t.Errorf("stored parent_ci_id = %s, want unchanged %s (the client-2 chassis %s now has a client-1 child)", got, orgWide, foreign)
	}
}

// TestParentCIStaysWithinTheClient covers WP-370 (DB-05, AST-05, TEN-05;
// F-086) against PostgreSQL through the production handler and CreateMany:
//   - Create, Update and CreateMany read the parent under the principal's
//     row level security: a parent the principal cannot see (a CI of
//     another client, a deleted or unknown CI) is 422 on parent_ci_id with
//     "parent CI not found" and nothing is stored;
//   - a visible parent of another client than the CI is 422 for an
//     org-wide principal too, also for an org-wide CI with a client's
//     parent and for a CI that moves to another client;
//   - a CI that moves to a client cannot contain CIs of another client or
//     org-wide CIs (422 on client_id); an org-wide CI can;
//   - a parent of the same client and an org-wide parent are accepted
//     (E-09, E-62).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestParentCIStaysWithinTheClient(t *testing.T) {
	f := scopetest.Seed(t, "23")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "parent-scope")
	repo := ci.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	as := func(scoped context.Context) context.Context {
		return tenant.WithTenant(scoped, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	}
	client1, orgWide := as(f.ClientCtx(f.Client1)), as(f.OrgCtx(f.OrgA))
	type violation struct{ Field, Detail string }
	call := func(ctx context.Context, method, path string, body map[string]any) (int, []violation) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		var problem struct{ Violations []violation }
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		return w.Code, problem.Violations
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	parentOf := func(id string) string {
		return str(`SELECT coalesce(parent_ci_id::text, '') FROM ci WHERE id = $1`, id)
	}
	clientOf := func(id string) string { return str(`SELECT coalesce(client_id::text, '') FROM ci WHERE id = $1`, id) }
	versionOf := func(id string) string { return str(`SELECT version::text FROM ci WHERE id = $1`, id) }
	stored := func(name string) string {
		return str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND name = $2`, f.OrgA, name)
	}
	const (
		notFound      = "parent CI not found"
		otherClient   = "the parent CI belongs to another client"
		containsOther = "the CI contains CIs of another client or org-wide CIs (their parent_ci_id)"
	)
	refused := func(t *testing.T, what string, code int, got []violation, field, detail string) {
		t.Helper()
		if code != http.StatusUnprocessableEntity || len(got) != 1 || got[0].Field != field || got[0].Detail != detail {
			t.Errorf("%s: %d %+v, want 422 with %s %q", what, code, got, field, detail)
		}
	}

	own := f.CI(t, f.OrgA, f.Client1, "parent-chassis-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "parent-chassis-c2")
	shared := f.CI(t, f.OrgA, "", "parent-chassis-org")
	deleted := f.CI(t, f.OrgA, f.Client1, "parent-chassis-deleted")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	typeID := ciTypeOf(t, f, own)
	cases := []struct {
		name           string
		ctx            context.Context
		client, parent string
		detail         string // empty: accepted
	}{
		{"foreign parent", client1, f.Client1, foreign, notFound},
		{"deleted parent", client1, f.Client1, deleted, notFound},
		{"unknown parent", client1, f.Client1, f.ID(), notFound},
		{"org-wide principal, parent of another client", orgWide, f.Client1, foreign, otherClient},
		{"org-wide CI with a client's parent", orgWide, "", own, otherClient},
		{"same-client parent", client1, f.Client1, own, ""},
		{"org-wide parent", client1, f.Client1, shared, ""},
		{"org-wide CI with an org-wide parent", orgWide, "", shared, ""},
	}

	t.Run("create", func(t *testing.T) {
		for i, tc := range cases {
			name := "parent-create-" + string(rune('a'+i))
			body := map[string]any{"name": name, "ci_type_id": typeID, "parent_ci_id": tc.parent}
			if tc.client != "" {
				body["client_id"] = tc.client
			}
			code, got := call(tc.ctx, http.MethodPost, "/api/v1/cis", body)
			if tc.detail != "" {
				refused(t, tc.name, code, got, "parent_ci_id", tc.detail)
				if n := stored(name); n != "0" {
					t.Errorf("%s: %s CIs stored, want none", tc.name, n)
				}
				continue
			}
			if code != http.StatusCreated {
				t.Errorf("%s: %d %+v, want 201", tc.name, code, got)
				continue
			}
			if p := str(`SELECT coalesce(parent_ci_id::text, '') FROM ci WHERE organization_id = $1 AND name = $2`, f.OrgA, name); p != tc.parent {
				t.Errorf("%s: stored parent %q, want %q", tc.name, p, tc.parent)
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		children := map[string]string{
			f.Client1: f.CI(t, f.OrgA, f.Client1, "parent-update-child-c1"),
			"":        f.CI(t, f.OrgA, "", "parent-update-child-org"),
		}
		for _, tc := range cases {
			child := children[tc.client]
			before, version := parentOf(child), versionOf(child)
			code, got := call(tc.ctx, http.MethodPatch, "/api/v1/cis/"+child, map[string]any{"parent_ci_id": tc.parent})
			if tc.detail != "" {
				refused(t, tc.name, code, got, "parent_ci_id", tc.detail)
				if p, v := parentOf(child), versionOf(child); p != before || v != version {
					t.Errorf("%s: parent %q version %s, want unchanged %q version %s", tc.name, p, v, before, version)
				}
				continue
			}
			if code != http.StatusOK {
				t.Errorf("%s: %d %+v, want 200", tc.name, code, got)
				continue
			}
			if p := parentOf(child); p != tc.parent {
				t.Errorf("%s: stored parent %q, want %q", tc.name, p, tc.parent)
			}
		}
	})

	t.Run("create many", func(t *testing.T) {
		item := func(name, client, parent string) *ci.Item {
			return &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typeID, Name: name, Status: "active", ParentCIID: parent}
		}
		isRefused := func(err error, detail string) bool {
			v, ok := ci.AsValidationError(err)
			return ok && len(v.Violations) == 1 && v.Violations[0].Field == "parent_ci_id" && v.Violations[0].Detail == detail
		}
		for _, tc := range cases {
			if tc.detail == "" {
				continue
			}
			plain, linked := "parent-batch-plain", "parent-batch-linked"
			_, err := repo.CreateMany(tc.ctx, f.OrgA, []*ci.Item{item(plain, tc.client, ""), item(linked, tc.client, tc.parent)})
			if !isRefused(err, tc.detail) {
				t.Errorf("%s: %v, want a validation error on parent_ci_id %q", tc.name, err, tc.detail)
			}
			if n, m := stored(plain), stored(linked); n != "0" || m != "0" {
				t.Errorf("%s: %s and %s CIs of the batch stored, want none", tc.name, n, m)
			}
		}
		var accepted []*ci.Item
		for i, tc := range cases {
			if tc.detail == "" && tc.client == f.Client1 {
				accepted = append(accepted, item("parent-batch-"+string(rune('a'+i)), tc.client, tc.parent))
			}
		}
		accepted = append(accepted, item("parent-batch-none", f.Client1, ""))
		res, err := repo.CreateMany(client1, f.OrgA, accepted)
		if err != nil || len(res.Refused) != 0 {
			t.Fatalf("batch with a same-client and an org-wide parent: %+v, %v", res, err)
		}
		for _, it := range accepted {
			if p := parentOf(it.ID); p != it.ParentCIID {
				t.Errorf("batch item %s: stored parent %q, want %q", it.Name, p, it.ParentCIID)
			}
		}
	})

	t.Run("move to another client", func(t *testing.T) {
		chassis := f.CI(t, f.OrgA, f.Client1, "parent-move-chassis")
		blade := f.CI(t, f.OrgA, f.Client1, "parent-move-blade")
		patch := func(id string, body map[string]any) (int, []violation) {
			return call(orgWide, http.MethodPatch, "/api/v1/cis/"+id, body)
		}
		if code, got := patch(blade, map[string]any{"parent_ci_id": chassis}); code != http.StatusOK {
			t.Fatalf("blade into the chassis: %d %+v", code, got)
		}
		code, got := patch(blade, map[string]any{"client_id": f.Client2})
		refused(t, "blade of a client-1 chassis to client 2", code, got, "parent_ci_id", otherClient)
		code, got = patch(chassis, map[string]any{"client_id": f.Client2})
		refused(t, "chassis with a client-1 blade to client 2", code, got, "client_id", containsOther)
		if c, b, p := clientOf(chassis), clientOf(blade), parentOf(blade); c != f.Client1 || b != f.Client1 || p != chassis {
			t.Fatalf("after the refused moves: chassis client %q, blade client %q, parent %q", c, b, p)
		}

		// An org-wide chassis contains CIs of any client.
		if code, got = patch(chassis, map[string]any{"client_id": ""}); code != http.StatusOK {
			t.Fatalf("chassis to org-wide: %d %+v", code, got)
		}
		if code, got = patch(blade, map[string]any{"client_id": f.Client2}); code != http.StatusOK {
			t.Fatalf("blade of an org-wide chassis to client 2: %d %+v", code, got)
		}
		code, got = patch(chassis, map[string]any{"client_id": f.Client1})
		refused(t, "org-wide chassis with a client-2 blade to client 1", code, got, "client_id", containsOther)
		if c, b, p := clientOf(chassis), clientOf(blade), parentOf(blade); c != "" || b != f.Client2 || p != chassis {
			t.Errorf("after the moves: chassis client %q, blade client %q, parent %q, want org-wide, client 2, the chassis", c, b, p)
		}
	})
}

func ciTypeOf(t *testing.T, f *scopetest.Fixture, ciID string) string {
	t.Helper()
	var typeID string
	if err := f.Admin.QueryRow(context.Background(), `SELECT ci_type_id::text FROM ci WHERE id = $1`, ciID).Scan(&typeID); err != nil {
		t.Fatalf("read ci type: %v", err)
	}
	return typeID
}
