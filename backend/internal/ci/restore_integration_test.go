package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRestoreAgainstPostgres covers POST /cis/{id}/restore (CI-05, API-05,
// WP-169) against PostgreSQL: a deleted CI comes back with a new version,
// a change row and the ci.restored audit entry; a CI that is not deleted is
// 409 (WP-567, F-222); after the default retention of 90 days the answer
// is 410.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRestoreAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "af")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))).RegisterRoutes(mux)
	call := func(method, path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(nil)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	id := f.CI(t, f.OrgA, f.Client1, "restore-1")
	if code, _ := call(http.MethodDelete, "/api/v1/cis/"+id); code >= 300 {
		t.Fatalf("delete: %d", code)
	}
	code, out := call(http.MethodPost, "/api/v1/cis/"+id+"/restore")
	if code != http.StatusOK || out["id"] != id {
		t.Fatalf("restore: %d %v", code, out)
	}
	var deleted bool
	var audits, changes int
	if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL,
		(SELECT count(*) FROM audit_log WHERE organization_id = $2 AND action = 'ci.restored' AND resource_id = $1),
		(SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'deleted_at')
		FROM ci WHERE id = $1`, id, f.OrgA).Scan(&deleted, &audits, &changes); err != nil {
		t.Fatal(err)
	}
	if deleted || audits != 1 || changes != 1 {
		t.Errorf("deleted %v, audit entries %d, change rows %d", deleted, audits, changes)
	}
	if code, _ = call(http.MethodPost, "/api/v1/cis/"+id+"/restore"); code != http.StatusConflict {
		t.Errorf("restore of a live CI: %d, want 409", code)
	}
	call(http.MethodDelete, "/api/v1/cis/"+id)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() - interval '91 days' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if code, _ = call(http.MethodPost, "/api/v1/cis/"+id+"/restore"); code != http.StatusGone {
		t.Errorf("restore after the retention: %d", code)
	}
}

// restoreRouter is the production router (server.NewRouter with the
// PostgreSQL repositories and the OpenAPI validation) whose soft-delete
// retention is retentionDays (server.Options.RetentionSoftDeleteDays, 0:
// not configured). call sends a request of an organization-A principal with
// all permissions and the given client scope (nil: org-wide).
func restoreRouter(t *testing.T, f *scopetest.Fixture, userID string, retentionDays int) func(clients []string, method, path string) (int, string) {
	t.Helper()
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-567", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		RouteMiddleware:         []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
		RetentionSoftDeleteDays: retentionDays,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.RequestID(middleware.TenantMiddleware(mux))
	return func(clients []string, method, path string) (int, string) {
		t.Helper()
		scope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
		if clients != nil {
			scope.Clients = identity.ScopeSet{IDs: clients}
		}
		req := httptest.NewRequest(method, path, nil)
		p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: scope, Permissions: identity.AllPermissions(), Credential: "wp-567"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w.Code, w.Body.String()
	}
}

// restoreAdmin creates the app user of the principal of restoreRouter.
func restoreAdmin(t *testing.T, f *scopetest.Fixture) string {
	t.Helper()
	userID := f.AppUser(t, f.OrgA, "wp567-restore-admin")
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userID); err != nil {
		t.Fatal(err)
	}
	return userID
}

// TestRestoreWithinConfiguredRetention: the restore window is the
// configured soft-delete retention (RETICORA_RETENTION_SOFT_DELETE_DAYS,
// handed to the CI service through server.Options), not a fixed 90 days
// (CI-05, WP-567, F-221). With 180 days a CI deleted 100 days ago comes back
// (200) and one deleted 181 days ago is 410; without a configured retention
// the default of 90 days applies (89 days 200, 91 days 410).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRestoreWithinConfiguredRetention(t *testing.T) {
	f := scopetest.Seed(t, "af")
	userID := restoreAdmin(t, f)
	deletedAgo := func(name string, days int) string {
		t.Helper()
		id := f.CI(t, f.OrgA, f.Client1, name)
		if _, err := f.Admin.Exec(context.Background(), `UPDATE ci SET deleted_at = now() - make_interval(days => $2), status = 'decommissioned' WHERE id = $1`,
			id, days); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tc := range []struct {
		retention, days, want int
	}{
		{180, 100, http.StatusOK},
		{180, 181, http.StatusGone},
		{0, 89, http.StatusOK},
		{0, 91, http.StatusGone},
		{90, 91, http.StatusGone},
	} {
		call := restoreRouter(t, f, userID, tc.retention)
		id := deletedAgo("wp567-retention-"+strconv.Itoa(tc.retention)+"-"+strconv.Itoa(tc.days), tc.days)
		if code, body := call(nil, http.MethodPost, "/api/v1/cis/"+id+"/restore"); code != tc.want {
			t.Errorf("retention %d days, deleted %d days ago: %d %s, want %d", tc.retention, tc.days, code, body, tc.want)
		}
	}
}

// TestRestoreEdgeCasesAndContract: the answers of POST /cis/{id}/restore
// through the production router (CI-05, API-05, WP-567, F-222): a CI that is
// not deleted is 409, a deleted CI of another organization, one of a client
// outside the caller's scope and an unknown id are 404 without a change, a
// CI whose serial number another active CI took meanwhile is 409 and stays
// deleted, a merged CI is 409 (unmerge); every answer is documented for
// restoreCI in api/openapi.yaml.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRestoreEdgeCasesAndContract(t *testing.T) {
	f := scopetest.Seed(t, "af")
	bg := context.Background()
	userID := restoreAdmin(t, f)
	call := restoreRouter(t, f, userID, 0)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	deleted := func(id string) bool {
		t.Helper()
		var d bool
		if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1`, id).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	softDelete := func(id string) {
		t.Helper()
		exec(`UPDATE ci SET deleted_at = now() - interval '1 day', status = 'decommissioned' WHERE id = $1`, id)
	}

	live := f.CI(t, f.OrgA, f.Client1, "wp567-live")
	otherOrg := f.CI(t, f.OrgB, "", "wp567-org-b")
	softDelete(otherOrg)
	otherClient := f.CI(t, f.OrgA, f.Client2, "wp567-client-2")
	softDelete(otherClient)
	serialHolder := f.CI(t, f.OrgA, f.Client1, "wp567-serial-old")
	exec(`UPDATE ci SET serial_number = 'WP567-SN-1' WHERE id = $1`, serialHolder)
	softDelete(serialHolder)
	taker := f.CI(t, f.OrgA, f.Client1, "wp567-serial-new")
	exec(`UPDATE ci SET serial_number = 'WP567-SN-1' WHERE id = $1`, taker)
	survivor := f.CI(t, f.OrgA, f.Client1, "wp567-survivor")
	merged := f.CI(t, f.OrgA, f.Client1, "wp567-merged")
	exec(`UPDATE ci SET merged_into_id = $2, deleted_at = now(), status = 'decommissioned' WHERE id = $1`, merged, survivor)

	observed := map[string]bool{}
	for _, tc := range []struct {
		what    string
		clients []string
		id      string
		want    int
		// stays: the CI stays deleted (false: it stays live).
		stays bool
	}{
		{"a CI that is not deleted", nil, live, http.StatusConflict, false},
		{"a deleted CI of another organization", nil, otherOrg, http.StatusNotFound, true},
		{"a deleted CI of a client outside the scope", []string{f.Client1}, otherClient, http.StatusNotFound, true},
		{"an unknown id", nil, "00000000-0000-4000-8000-0000000005a7", http.StatusNotFound, false},
		{"a CI whose serial number another CI took", nil, serialHolder, http.StatusConflict, true},
		{"a merged CI", nil, merged, http.StatusConflict, true},
	} {
		code, body := call(tc.clients, http.MethodPost, "/api/v1/cis/"+tc.id+"/restore")
		observed[strconv.Itoa(code)] = true
		if code != tc.want {
			t.Errorf("restore of %s: %d %s, want %d", tc.what, code, body, tc.want)
		}
		if tc.id != "00000000-0000-4000-8000-0000000005a7" && deleted(tc.id) != tc.stays {
			t.Errorf("restore of %s: deleted %v afterwards, want %v", tc.what, !tc.stays, tc.stays)
		}
	}
	// The CI of the other client comes back for an org-wide caller.
	code, body := call(nil, http.MethodPost, "/api/v1/cis/"+otherClient+"/restore")
	observed[strconv.Itoa(code)] = true
	if code != http.StatusOK || deleted(otherClient) {
		t.Errorf("org-wide restore of the client-2 CI: %d %s, want 200", code, body)
	}

	documented := restoreResponses(t)
	var missing []string
	for status := range observed {
		if !documented[status] {
			missing = append(missing, status)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || !documented["409"] {
		t.Errorf("restoreCI in api/openapi.yaml documents %v; answered but not documented: %v (409 required)", documented, missing)
	}
}

// restoreResponses returns the status codes api/openapi.yaml documents for
// POST /api/v1/cis/{id}/restore.
func restoreResponses(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var op struct {
		OperationID string               `yaml:"operationId"`
		Responses   map[string]yaml.Node `yaml:"responses"`
	}
	node, ok := doc.Paths["/api/v1/cis/{id}/restore"]["post"]
	if !ok {
		t.Fatal("api/openapi.yaml has no POST /api/v1/cis/{id}/restore")
	}
	if err = node.Decode(&op); err != nil || op.OperationID != "restoreCI" {
		t.Fatalf("restoreCI in api/openapi.yaml: %v (operationId %q)", err, op.OperationID)
	}
	out := map[string]bool{}
	for status := range op.Responses {
		out[status] = true
	}
	return out
}
