// User deletion through the production router and authentication chain on
// PostgreSQL and Redis (WP-664, TLC-04, AUT-02, AUT-04; finding F-393).
//
// DELETE /api/v1/users/{id} answered 204 and removed the row, but ended no
// access: the bearer token of the deleted user kept reading and writing
// until it expired (GET /cis 200, POST /cis 201), its API key stayed
// unrevoked (the role assignments cascade, so it answered 403 instead of
// 401) and no user.deleted entry was written. The deletion now ends every
// access like the deactivation (TestT2DeactivationFullChain), in its
// transaction: own API keys revoked, service-account keys kept and listed
// (E-64, option 2), sessions and refresh families blacklisted, user.deleted
// recorded by the domain recorder (no route entry besides it). A refused
// deletion (own account, F-377; open objects, 409) ends nothing.
//
// Retest (needs a migrated database and Redis):
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… TEST_REDIS_URL=redis://localhost:56379/0 go test -count=1 -v -run '^TestUserDeletionEndsAccess$' ./internal/identity/
package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

func TestUserDeletionEndsAccess(t *testing.T) {
	f := scopetest.Seed(t, "ee")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("TEST_REDIS_URL is required next to TEST_DATABASE_URL for this integration test")
	}
	bg := context.Background()
	client, err := redisx.Connect(bg, redisURL)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisStore(client.Unwrap())
	revocations := identity.NewSessionRevocations(store)
	issuer := newIssuer(t).WithRevocations(revocations)
	refresh := identity.NewRefreshSessions(store, revocations)

	// The router of the PostgreSQL profile with the route audit of
	// cmd/server (AuditPool), behind its authentication chain.
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mkey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(mkey)
	if err != nil {
		t.Fatal(err)
	}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "user-deletion", Entitlements: entitlements, Sessions: issuer, RefreshSessions: refresh,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		AuditPool:   f.App,
	})
	if err != nil {
		t.Fatal(err)
	}
	apiKeys := identity.NewAPIKeyServiceWithStore(repos.APIKeys).WithOwnerAccess(repos.Permission)
	h := middleware.Chain(middleware.RequestID, middleware.AuthMiddlewareWithAPIKeys(issuer, apiKeys), middleware.TenantMiddleware)(mux)
	authHandler := identity.NewHandler(nil, issuer).WithProvisioning(user.NewPGRepository(f.App), "").
		WithAccessResolver(permission.NewPGRepository(f.App)).WithRefreshSessions(refresh)

	admin := f.AppUser(t, f.OrgA, "deletion-admin")
	victim := f.AppUser(t, f.OrgA, "deletion-victim")
	other := f.AppUser(t, f.OrgA, "deletion-other")
	owner := f.AppUser(t, f.OrgA, "deletion-owner")
	f.CI(t, f.OrgA, "", "deletion-visible-ci")
	roleID := f.ID()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := f.Admin.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", sql, execErr)
		}
	}
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'deletion-writer', '["ci:read","ci:write"]')`, roleID, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $5), ($1, $3, $5), ($1, $4, $5)`, f.OrgA, victim, other, owner, roleID)
	// An open ticket of owner: owner cannot be deleted (409).
	exec(`INSERT INTO ticket (id, organization_id, title, reporter_id, assignee_id, status) VALUES ($1, $2, 'deletion-handover', $3, $4, 'open')`,
		f.ID(), f.OrgA, admin, owner)
	serviceAccount := f.ID()
	exec(`INSERT INTO service_account (id, organization_id, name, created_by) VALUES ($1, $2, 'deletion-sa', $3)`, serviceAccount, f.OrgA, victim)
	type seededKey struct{ id, plaintext string }
	seedKey := func(creator string, account any) seededKey {
		t.Helper()
		k, genErr := identity.GenerateAPIKey("live")
		if genErr != nil {
			t.Fatal(genErr)
		}
		id := f.ID()
		exec(`INSERT INTO api_key (id, organization_id, name, key_hash, key_prefix, permissions, created_by, service_account_id) VALUES ($1, $2, $3, $4, $5, '{ci:read}', $6, $7)`,
			id, f.OrgA, "deletion-"+k.KeyPrefix, k.KeyHash, k.KeyPrefix, creator, account)
		return seededKey{id: id, plaintext: k.Plaintext}
	}
	victimKey := seedKey(victim, nil)
	saKey := seedKey(victim, serviceAccount)
	ownerKey := seedKey(owner, nil)

	orgWide := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	login := func(subject string, perms []identity.Permission) (token, cookie string) {
		t.Helper()
		now := time.Now().UTC()
		claims := identity.SessionClaims{Subject: subject, OrganizationID: f.OrgA, Permissions: perms, Scope: orgWide,
			ID: "deletion-" + subject + "-" + now.Format("150405.000000000"), IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
		if token, err = issuer.Issue(claims); err != nil {
			t.Fatal(err)
		}
		if cookie, err = refresh.Issue(bg, &identity.RefreshGrant{Claims: claims}); err != nil {
			t.Fatal(err)
		}
		return token, cookie
	}
	do := func(method, path, header, value, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(header, value)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	bearer := func(token string) (string, string) { return "Authorization", "Bearer " + token }
	refreshCode := func(cookie string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
		w := httptest.NewRecorder()
		authHandler.Refresh(w, req)
		return w.Code
	}
	revokedAt := func(keyID string) *time.Time {
		t.Helper()
		var at *time.Time
		if qErr := f.Admin.QueryRow(bg, `SELECT revoked_at FROM api_key WHERE id = $1`, keyID).Scan(&at); qErr != nil {
			t.Fatalf("read api key: %v", qErr)
		}
		return at
	}
	var typeID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	writeCI := func(token, name string) int {
		hd, v := bearer(token)
		return do(http.MethodPost, "/api/v1/cis", hd, v, `{"name":"`+name+`","ci_type_id":"`+typeID+`","status":"active"}`).Code
	}
	readCIs := func(header, value string) int { return do(http.MethodGet, "/api/v1/cis", header, value, "").Code }

	rw := []identity.Permission{identity.PermCIRead, identity.PermCIWrite}
	adminToken, _ := login(admin, identity.AllPermissions())
	victimToken, victimCookie := login(victim, rw)
	otherToken, _ := login(other, rw)
	ownerToken, ownerCookie := login(owner, rw)
	for name, code := range map[string]int{
		"bearer of the user":  readCIs(bearer(victimToken)),
		"API key of the user": readCIs("X-API-Key", victimKey.plaintext),
		"bearer of the owner": readCIs(bearer(ownerToken)),
	} {
		if code != http.StatusOK {
			t.Fatalf("before: %s GET /api/v1/cis -> %d, want 200", name, code)
		}
	}
	// The blacklist compares the token's iat in whole seconds.
	time.Sleep(1100 * time.Millisecond)

	t.Run("refused deletions end nothing", func(t *testing.T) {
		hd, v := bearer(adminToken)
		if w := do(http.MethodDelete, "/api/v1/users/"+admin, hd, v, ""); w.Code != http.StatusBadRequest ||
			!strings.Contains(w.Body.String(), "you cannot delete your own account") {
			t.Errorf("DELETE own account: %d %s, want 400 (F-377)", w.Code, w.Body.String())
		}
		if w := do(http.MethodDelete, "/api/v1/users/"+owner, hd, v, ""); w.Code != http.StatusConflict {
			t.Errorf("DELETE the assignee of an open ticket: %d %s, want 409", w.Code, w.Body.String())
		}
		if code := readCIs(bearer(ownerToken)); code != http.StatusOK {
			t.Errorf("after the refused deletion: bearer of the owner -> %d, want 200", code)
		}
		if code := readCIs("X-API-Key", ownerKey.plaintext); code != http.StatusOK {
			t.Errorf("after the refused deletion: API key of the owner -> %d, want 200", code)
		}
		if code := refreshCode(ownerCookie); code != http.StatusOK {
			t.Errorf("after the refused deletion: refresh of the owner -> %d, want 200", code)
		}
		if at := revokedAt(ownerKey.id); at != nil {
			t.Errorf("after the refused deletion: API key of the owner revoked at %v, want active", at)
		}
		if at, rErr := revocations.RevokedAt(bg, owner); rErr != nil || !at.IsZero() {
			t.Errorf("after the refused deletion: blacklist entry of the owner at %v (err %v), want none", at, rErr)
		}
		var audits int
		if qErr := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id IN ($2, $3)`, f.OrgA, admin, owner).Scan(&audits); qErr != nil {
			t.Fatal(qErr)
		}
		if audits != 0 {
			t.Errorf("audit entries of the refused deletions: %d, want 0", audits)
		}
	})

	hd, v := bearer(adminToken)
	w := do(http.MethodDelete, "/api/v1/users/"+victim, hd, v, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/users/{id}: %d %s, want 204", w.Code, w.Body.String())
	}
	requestID := w.Header().Get("X-Request-ID")
	var exists bool
	if err = f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1)`, victim).Scan(&exists); err != nil || exists {
		t.Fatalf("app_user of the deleted user still exists: %v (err %v)", exists, err)
	}

	for name, code := range map[string]int{
		"bearer GET /api/v1/cis":    readCIs(bearer(victimToken)),
		"bearer POST /api/v1/cis":   writeCI(victimToken, "deletion-written-after-delete"),
		"X-API-Key GET /api/v1/cis": readCIs("X-API-Key", victimKey.plaintext),
		"refresh cookie":            refreshCode(victimCookie),
	} {
		if code != http.StatusUnauthorized {
			t.Errorf("after the deletion: %s of the deleted user -> %d, want 401", name, code)
		}
	}
	var written int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND name = 'deletion-written-after-delete'`, f.OrgA).Scan(&written); err != nil {
		t.Fatal(err)
	}
	if written != 0 {
		t.Errorf("CIs the deleted user created after the deletion: %d, want 0", written)
	}
	if at := revokedAt(victimKey.id); at == nil {
		t.Error("after the deletion: API key of the deleted user has revoked_at NULL, want set")
	}
	// E-64, option 2: the service-account key belongs to the account.
	if at := revokedAt(saKey.id); at != nil {
		t.Errorf("after the deletion: service-account key of the deleted user revoked at %v, want kept (E-64)", at)
	}
	if code := readCIs(bearer(otherToken)); code != http.StatusOK {
		t.Errorf("after the deletion: another user of the organization -> %d, want 200", code)
	}
	if code := writeCI(otherToken, "deletion-control-write"); code != http.StatusCreated {
		t.Errorf("after the deletion: another user of the organization POST /api/v1/cis -> %d, want 201", code)
	}

	// Exactly one entry for the request: user.deleted of the domain
	// recorder, attributed to the admin, with the keys; no route entry.
	rows, err := f.Admin.Query(bg, `SELECT action, coalesce(resource_id::text, ''), coalesce(actor_id::text, ''), changes FROM audit_log
		WHERE organization_id = $1 AND request_id = $2 ORDER BY timestamp`, f.OrgA, requestID)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		action, resource, actor string
		changes                 []byte
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.action, &e.resource, &e.actor, &e.changes); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if len(entries) != 1 || entries[0].action != "user.deleted" {
		actions := make([]string, 0, len(entries))
		for _, e := range entries {
			actions = append(actions, e.action)
		}
		t.Fatalf("audit entries of the deletion request: %v, want exactly [user.deleted]", actions)
	}
	e := entries[0]
	if e.resource != victim || e.actor != admin {
		t.Errorf("user.deleted: resource %s actor %s, want resource %s actor %s", e.resource, e.actor, victim, admin)
	}
	var changes struct {
		Revoked  int64 `json:"api_keys_revoked"`
		Sessions bool  `json:"sessions_revoked"`
		Kept     []struct {
			ID string `json:"id"`
		} `json:"service_account_keys_kept"`
	}
	if err = json.Unmarshal(e.changes, &changes); err != nil {
		t.Fatalf("changes of user.deleted: %v", err)
	}
	if changes.Revoked != 1 || !changes.Sessions || len(changes.Kept) != 1 || changes.Kept[0].ID != saKey.id {
		t.Errorf("changes of user.deleted: %s; want api_keys_revoked 1, sessions_revoked true, the service-account key kept", e.changes)
	}
}
