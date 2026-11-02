package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

var apiKeyFormat = regexp.MustCompile(`^rk_(live|test)_[0-9A-Za-z]{12}[0-9A-Za-z]{40}$`)

// TestAPIKeyLifecycle covers WP-067 (AUT-04, API-05): the api-keys resource
// creates keys in the AUT-04 format (rk_live_/rk_test_, 12 + 40 Base62) with
// the plaintext shown once and only the SHA-256 stored; a key never grants
// more than its creator holds and, at use, only the intersection with its
// owner's current rights in the owner's scope; identification works without
// tenant context for the application role; rotation keeps the old key valid
// for the overlap; revocation ends a key at once; every change is audited.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAPIKeyLifecycle(t *testing.T) {
	f := scopetest.Seed(t, "61")
	bg := context.Background()
	owner := f.AppUser(t, f.OrgA, "key-owner")

	// The owner holds ci:read and ci:write within client 1 only.
	roleID := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 'key-role', '["ci:read","ci:write"]', 'client')`, roleID, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, owner, roleID, f.Client1); err != nil {
		t.Fatal(err)
	}

	store := identity.NewPGAPIKeyStore(f.App)
	service := identity.NewAPIKeyServiceWithStore(store).WithOwnerAccess(permission.NewPGRepository(f.App))
	mux := chi.NewRouter()
	identity.NewAPIKeyHandler(store).RegisterRoutes(mux)
	call := func(principal identity.Principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, principal.Subject)
		ctx := identity.WithPrincipal(req.Context(), principal)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: principal.Subject})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}
	user := identity.Principal{Subject: owner, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermAPIKeyManage, identity.PermCIRead, identity.PermCIWrite}}

	type created struct {
		ID          string `json:"id"`
		Key         string `json:"key"`
		KeyPrefix   string `json:"key_prefix"`
		Environment string `json:"environment"`
	}
	create := func(body string) created {
		t.Helper()
		w := call(user, http.MethodPost, "/api/v1/api-keys", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var c created
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	live := create(`{"name":"ci-sync","permissions":["ci:read","ci:write"]}`)
	if !apiKeyFormat.MatchString(live.Key) || !strings.HasPrefix(live.Key, "rk_live_"+live.KeyPrefix) || live.Environment != "live" {
		t.Fatalf("live key %+v does not follow AUT-04", live)
	}
	var storedHash string
	if err := f.Admin.QueryRow(bg, `SELECT key_hash FROM api_key WHERE id = $1`, live.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != identity.HashKey(live.Key) || strings.Contains(storedHash, live.Key) {
		t.Errorf("stored hash %q is not the SHA-256 of the key", storedHash)
	}
	testKey := create(`{"name":"sandbox","permissions":["ci:read"],"environment":"test"}`)
	if !strings.HasPrefix(testKey.Key, "rk_test_") || !apiKeyFormat.MatchString(testKey.Key) {
		t.Fatalf("test key %q", testKey.Key)
	}

	// The list never shows a secret or hash.
	w := call(user, http.MethodGet, "/api/v1/api-keys", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), live.Key) || strings.Contains(w.Body.String(), storedHash) ||
		!strings.Contains(w.Body.String(), live.KeyPrefix) {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}

	// A key cannot grant more than its creator holds, and keys cannot mint keys.
	if w = call(user, http.MethodPost, "/api/v1/api-keys", `{"name":"x","permissions":["credential:manage"]}`); w.Code != http.StatusForbidden {
		t.Errorf("create with a permission the caller lacks: %d, want 403", w.Code)
	}
	keyPrincipal := user
	keyPrincipal.Type = identity.PrincipalTypeAPIKey
	if w = call(keyPrincipal, http.MethodPost, "/api/v1/api-keys", `{"name":"x","permissions":["ci:read"]}`); w.Code != http.StatusForbidden {
		t.Errorf("create by an API key: %d, want 403", w.Code)
	}

	// Identification runs for the application role without tenant context.
	info, err := service.Validate(bg, live.Key)
	if err != nil {
		t.Fatalf("validate live key: %v", err)
	}
	if info.OrganizationID != f.OrgA || len(info.Scopes) != 2 || info.Scope == nil || info.Scope.Clients.All ||
		len(info.Scope.Clients.IDs) != 1 || info.Scope.Clients.IDs[0] != f.Client1 {
		t.Errorf("live key info %+v (scope %+v), want ci:read+ci:write within client 1", info, info.Scope)
	}
	if _, err = service.Validate(bg, testKey.Key); err != nil {
		t.Errorf("validate test key: %v", err)
	}
	if _, err = service.Validate(bg, "rk_live_"+strings.TrimPrefix(testKey.Key, "rk_test_")); err == nil {
		t.Error("a test key presented as live key was accepted")
	}

	// The owner loses ci:write: the key keeps only the intersection.
	if _, err = f.Admin.Exec(bg, `UPDATE role SET permissions = '["ci:read"]' WHERE id = $1`, roleID); err != nil {
		t.Fatal(err)
	}
	if info, err = service.Validate(bg, live.Key); err != nil || len(info.Scopes) != 1 || info.Scopes[0] != identity.PermCIRead {
		t.Errorf("after the owner lost ci:write: %+v, %v; want ci:read only", info, err)
	}

	// Rotation: the successor works, the old key stays valid for the overlap.
	w = call(user, http.MethodPost, "/api/v1/api-keys/"+live.ID+"/rotate", `{"overlap_seconds":3600}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	var rotated struct {
		Key      created `json:"key"`
		Previous struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"previous"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Key.Key == live.Key || !apiKeyFormat.MatchString(rotated.Key.Key) || rotated.Previous.ExpiresAt == "" {
		t.Fatalf("rotation result %s", w.Body.String())
	}
	for name, key := range map[string]string{"successor": rotated.Key.Key, "old key during the overlap": live.Key} {
		if _, err = service.Validate(bg, key); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A rotation without overlap ends the old key at once.
	w = call(user, http.MethodPost, "/api/v1/api-keys/"+rotated.Key.ID+"/rotate", `{"overlap_seconds":0}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate without overlap: %d %s", w.Code, w.Body.String())
	}
	if _, err = service.Validate(bg, rotated.Key.Key); err == nil {
		t.Error("key rotated without overlap is still valid")
	}

	// Revocation ends a key at once.
	if w = call(user, http.MethodDelete, "/api/v1/api-keys/"+testKey.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if _, err = service.Validate(bg, testKey.Key); err == nil {
		t.Error("revoked key is still valid")
	}
	if w = call(user, http.MethodDelete, "/api/v1/api-keys/"+f.ID(), ""); w.Code != http.StatusNotFound {
		t.Errorf("revoke unknown key: %d, want 404", w.Code)
	}

	// Keys issued before WP-067 (underscore between prefix and secret) stay
	// valid until they are rotated.
	legacy := "rk_live_LEGACY000061_" + strings.Repeat("a", 40)
	if _, err = f.Admin.Exec(bg, `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, permissions, created_by)
		VALUES ($1, 'legacy', $2, 'LEGACY000061', '{ci:read}', $3)`, f.OrgA, identity.HashKey(legacy), owner); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Validate(bg, legacy); err != nil {
		t.Errorf("legacy key: %v", err)
	}

	var actions []string
	rows, err := f.Admin.Query(bg, `SELECT action FROM audit_log WHERE organization_id = $1 AND resource_type = 'api_key' AND actor_id = $2 ORDER BY timestamp`, f.OrgA, owner)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	rows.Close()
	want := "api_key.created,api_key.created,api_key.rotated,api_key.rotated,api_key.revoked"
	if strings.Join(actions, ",") != want {
		t.Errorf("audit %v, want %s", actions, want)
	}
}

// TestAPIKeyListPagesAtMost200 is the retest of F-215 (WP-565, API-02):
// with 250 keys GET /api-keys answers at most 200 entries and a signed
// next_cursor; the second page returns the remaining 50, none twice, newest
// first on (created_at, id) also across keys created at the same instant.
// Without limit the page holds 50 entries, a larger limit is capped to 200;
// a forged cursor or a keyset cursor of another order is 400; the keys of
// another organization never show.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAPIKeyListPagesAtMost200(t *testing.T) {
	f := scopetest.Seed(t, "61")
	bg := context.Background()
	owner := f.AppUser(t, f.OrgA, "page-owner")
	// 250 keys of organization A in 125 instants (two keys share each
	// created_at, so the order needs the id), 3 keys of organization B.
	if _, err := f.Admin.Exec(bg, `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, permissions, created_by, created_at)
		SELECT $1, 'page-' || n, md5('page-' || n), 'PAGE' || lpad(n::text, 8, '0'), '{ci:read}', $2,
			timestamptz '2026-01-01 00:00:00.123456Z' + (n / 2) * interval '1 second'
		FROM generate_series(1, 250) AS n`, f.OrgA, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, permissions, created_by)
		SELECT $1, 'foreign-' || n, md5('foreign-' || n), 'FOREIGN' || lpad(n::text, 5, '0'), '{ci:read}', $2
		FROM generate_series(1, 3) AS n`, f.OrgB, owner); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	identity.NewAPIKeyHandler(identity.NewPGAPIKeyStore(f.App)).RegisterRoutes(mux)
	user := identity.Principal{Subject: owner, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermAPIKeyManage, identity.PermCIRead}}
	type page struct {
		Data []struct {
			ID        string    `json:"id"`
			Name      string    `json:"name"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"data"`
		Total      int    `json:"total"`
		Limit      int    `json:"limit"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	list := func(query string) (int, page, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys"+query, nil)
		scope := database.OrgWideScope(f.OrgA, owner)
		ctx := identity.WithPrincipal(req.Context(), user)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: owner})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		var p page
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		return w.Code, p, w.Body.String()
	}

	code, first, body := list("?limit=200")
	if code != http.StatusOK {
		t.Fatalf("first page: %d %.300s", code, body)
	}
	if len(first.Data) > 200 || first.NextCursor == "" || !first.HasMore {
		t.Fatalf("first page of 250 keys: %d entries, next_cursor %q, has_more %v; want at most 200 and a next_cursor (API-02, F-215)",
			len(first.Data), first.NextCursor, first.HasMore)
	}
	if len(first.Data) != 200 || first.Total != 250 || first.Limit != 200 {
		t.Errorf("first page: %d entries, total %d, limit %d; want 200 of 250", len(first.Data), first.Total, first.Limit)
	}
	code, second, body := list("?limit=200&cursor=" + url.QueryEscape(first.NextCursor))
	if code != http.StatusOK {
		t.Fatalf("second page: %d %.300s", code, body)
	}
	if len(second.Data) != 50 || second.NextCursor != "" || second.HasMore {
		t.Errorf("second page: %d entries, next_cursor %q, has_more %v; want the remaining 50 and no cursor",
			len(second.Data), second.NextCursor, second.HasMore)
	}
	seen := map[string]bool{}
	all := slices.Concat(first.Data, second.Data)
	for i, k := range all {
		if seen[k.ID] {
			t.Errorf("key %s (%s) returned twice", k.ID, k.Name)
		}
		seen[k.ID] = true
		if !strings.HasPrefix(k.Name, "page-") {
			t.Errorf("foreign key %s listed", k.Name)
		}
		if i > 0 {
			prev := all[i-1]
			if k.CreatedAt.After(prev.CreatedAt) || (k.CreatedAt.Equal(prev.CreatedAt) && k.ID > prev.ID) {
				t.Errorf("entry %d (%s %s) does not follow %s %s in the order created_at desc, id desc", i, k.CreatedAt, k.ID, prev.CreatedAt, prev.ID)
			}
		}
	}
	if len(seen) != 250 {
		t.Errorf("two pages returned %d distinct keys, want 250", len(seen))
	}

	if code, p, body := list(""); code != http.StatusOK || len(p.Data) != 50 || p.Limit != 50 || p.NextCursor == "" {
		t.Errorf("default page: %d, %d entries, limit %d, next_cursor %q; want 50 and a cursor (%.200s)", code, len(p.Data), p.Limit, p.NextCursor, body)
	}
	if code, p, _ := list("?limit=1000"); code != http.StatusOK || len(p.Data) != 200 || p.Limit != 200 {
		t.Errorf("limit=1000: %d, %d entries, limit %d; want capped to 200", code, len(p.Data), p.Limit)
	}
	if code, _, body := list("?cursor=eyJpIjoiY2ktOSJ9.AAAAAAAAAAAAAAAAAAAAAA"); code != http.StatusBadRequest {
		t.Errorf("forged cursor: %d %.200s, want 400", code, body)
	}
	other := api.EncodeCursor(api.Cursor{Sort: "name", Dir: "asc", Value: "page-1", ID: all[0].ID})
	if code, _, body := list("?cursor=" + url.QueryEscape(other)); code != http.StatusBadRequest {
		t.Errorf("cursor of another order: %d %.200s, want 400", code, body)
	}
}
