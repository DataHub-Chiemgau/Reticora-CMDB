package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TestDeactivationEndsAccess covers WP-042 (TLC-04, AUT-02, AUT-01): a login
// never reactivates a deactivated user; deactivation revokes the user's API
// keys and records an audit entry in the same transaction and blacklists the
// sessions; refresh checks the status and reloads the grants; a user owning
// open objects cannot be deleted.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeactivationEndsAccess(t *testing.T) {
	f := scopetest.Seed(t, "53")
	bg := context.Background()
	users := user.NewPGRepository(f.App)

	// First login provisions the user (first user of the organization).
	subject := "deact-oidc-subject-53"
	userID, _, err := users.EnsureUser(bg, f.OrgA, subject, "deact@example.invalid", "Deact", "")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	admin := f.AppUser(t, f.OrgA, "deact-admin")

	// A role granting ci:read and an API key of the user.
	roleID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'deact-reader', '["ci:read"]')`, roleID, f.OrgA); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userID, roleID); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	keyID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO api_key (id, organization_id, name, key_hash, key_prefix, created_by) VALUES ($1, $2, 'deact', 'hash', 'deact53', $3)`,
		keyID, f.OrgA, userID); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	// Sessions: a blacklist on a cache store; the refresh handler reads the
	// user status and the grants from the database.
	revocations := identity.NewSessionRevocations(cache.NewMemoryStore())
	sessions := newIssuer(t).WithRevocations(revocations)
	plain := newIssuer(t) // without blacklist: isolates the refresh status check
	refreshStores := map[*identity.SessionIssuer]*identity.RefreshSessions{
		sessions: identity.NewRefreshSessions(cache.NewMemoryStore(), revocations),
		plain:    identity.NewRefreshSessions(cache.NewMemoryStore(), nil),
	}
	// session is an access token with the refresh cookie a login sets.
	type session struct{ token, cookie string }
	issue := func(issuer *identity.SessionIssuer) session {
		t.Helper()
		now := time.Now().UTC()
		claims := identity.SessionClaims{Subject: userID, OrganizationID: f.OrgA, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
		token, issueErr := issuer.Issue(claims)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		cookie, issueErr := refreshStores[issuer].Issue(bg, &identity.RefreshGrant{Claims: claims})
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return session{token, cookie}
	}
	refresh := func(issuer *identity.SessionIssuer, s session) (int, []identity.Permission) {
		t.Helper()
		h := identity.NewHandler(nil, issuer).WithProvisioning(users, "").WithAccessResolver(permission.NewPGRepository(f.App)).
			WithRefreshSessions(refreshStores[issuer])
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: s.cookie})
		w := httptest.NewRecorder()
		h.Refresh(w, req)
		if w.Code != http.StatusOK {
			return w.Code, nil
		}
		var out struct {
			Token string `json:"token"`
		}
		if decodeErr := json.Unmarshal(w.Body.Bytes(), &out); decodeErr != nil {
			t.Fatalf("decode refresh: %v", decodeErr)
		}
		claims, validateErr := issuer.Validate(out.Token)
		if validateErr != nil {
			t.Fatalf("refreshed token invalid: %v", validateErr)
		}
		return w.Code, claims.Permissions
	}
	hasCIRead := func(perms []identity.Permission) bool {
		for _, p := range perms {
			if p == identity.PermCIRead {
				return true
			}
		}
		return false
	}

	// Refresh reloads the grants: ci:read now, gone after the role is withdrawn.
	if code, perms := refresh(plain, issue(plain)); code != http.StatusOK || !hasCIRead(perms) {
		t.Fatalf("refresh with role: status %d permissions %v", code, perms)
	}
	if _, err = f.Admin.Exec(bg, `DELETE FROM role_assignment WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("withdraw role: %v", err)
	}
	if code, perms := refresh(plain, issue(plain)); code != http.StatusOK || hasCIRead(perms) {
		t.Errorf("refresh after the role was withdrawn: status %d permissions %v, want no ci:read", code, perms)
	}

	// Deactivation through the API.
	mux := chi.NewRouter()
	user.NewHandler(users).WithSessionRevoker(revocations).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, admin)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}
	before := issue(sessions)
	time.Sleep(time.Millisecond)
	if w := call(http.MethodPatch, "/api/v1/users/"+userID, `{"status":"inactive"}`); w.Code != http.StatusOK {
		t.Fatalf("deactivate: status %d: %s", w.Code, w.Body.String())
	}

	// Sessions and refresh tokens end at once.
	if _, err = sessions.Validate(before.token); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Errorf("session after deactivation: %v, want ErrSessionRevoked", err)
	}
	if code, _ := refresh(sessions, before); code != http.StatusUnauthorized {
		t.Errorf("refresh of a revoked session: status %d, want 401", code)
	}
	// Without the blacklist the refresh still fails on the user status.
	if code, _ := refresh(plain, issue(plain)); code != http.StatusUnauthorized {
		t.Errorf("refresh of a deactivated user: status %d, want 401", code)
	}

	// The API key is revoked and no longer found.
	var revoked bool
	if err = f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, keyID).Scan(&revoked); err != nil || !revoked {
		t.Errorf("api key after deactivation: revoked=%v, %v", revoked, err)
	}
	if key, lookupErr := identity.NewPGAPIKeyStore(f.Admin).LookupByPrefix(bg, "deact53"); lookupErr != nil || key != nil {
		t.Errorf("lookup of the revoked key: %+v, %v; want none", key, lookupErr)
	}

	// The deactivation is audited by the acting admin.
	var audited int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'user.deactivated' AND resource_id = $2 AND actor_id = $3`,
		f.OrgA, userID, admin).Scan(&audited); err != nil || audited != 1 {
		t.Errorf("audit entries of the deactivation: %d, %v; want 1", audited, err)
	}

	// A new login neither succeeds nor reactivates the user.
	if _, _, err = users.EnsureUser(bg, f.OrgA, subject, "deact@example.invalid", "Deact", ""); !errors.Is(err, identity.ErrUserInactive) {
		t.Errorf("login of a deactivated user: %v, want ErrUserInactive", err)
	}
	if active, statusErr := users.UserActive(bg, f.OrgA, userID); statusErr != nil || active {
		t.Errorf("user after login attempt: active=%v, %v; want inactive", active, statusErr)
	}

	// Reactivation allows a new login; the revoked key stays revoked.
	if w := call(http.MethodPatch, "/api/v1/users/"+userID, `{"status":"active"}`); w.Code != http.StatusOK {
		t.Fatalf("reactivate: status %d: %s", w.Code, w.Body.String())
	}
	if _, _, err = users.EnsureUser(bg, f.OrgA, subject, "deact@example.invalid", "Deact", ""); err != nil {
		t.Errorf("login after reactivation: %v", err)
	}
	// iat has the second resolution of a JWT NumericDate; a token from the
	// same second as the revocation counts as revoked (fail closed).
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	if _, err = sessions.Validate(issue(sessions).token); err != nil {
		t.Errorf("session issued after reactivation: %v", err)
	}
	if err = f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, keyID).Scan(&revoked); err != nil || !revoked {
		t.Errorf("api key after reactivation: revoked=%v, %v; want still revoked", revoked, err)
	}

	// Handover rule: an owner of an open ticket cannot be deleted; a user
	// still referenced by a closed ticket neither; an unreferenced one can.
	ticketID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO ticket (id, organization_id, title, reporter_id, assignee_id, status) VALUES ($1, $2, 'handover', $3, $4, 'open')`,
		ticketID, f.OrgA, admin, userID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if w := call(http.MethodDelete, "/api/v1/users/"+userID, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "hand them over") {
		t.Errorf("delete an owner of an open ticket: status %d body %s, want 409", w.Code, w.Body.String())
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ticket SET status = 'closed' WHERE id = $1`, ticketID); err != nil {
		t.Fatalf("close ticket: %v", err)
	}
	if w := call(http.MethodDelete, "/api/v1/users/"+userID, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "anonymize") {
		t.Errorf("delete a user referenced by a closed ticket: status %d body %s, want 409", w.Code, w.Body.String())
	}
	spare := f.AppUser(t, f.OrgA, "deact-spare")
	if w := call(http.MethodDelete, "/api/v1/users/"+spare, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete an unreferenced user: status %d body %s, want 204", w.Code, w.Body.String())
	}
}

func newIssuer(t *testing.T) *identity.SessionIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

// TestDeactivationRevokesServiceAccountKeysOfCreator covers decision E-64,
// option 2 (TLC-04, AUT-04, RBA-08; finding F-195): user A creates an API key
// for themself and one bound to a service account. Deactivating A revokes
// only A's own key; the service-account key belongs to the account and stays
// valid. The audit entry counts the revoked key and lists the kept one
// (id, prefix, name, service account, no secret), and the org_admins of A's
// organization receive exactly one notification naming it through the
// outbox and the notification center (NTF-05). Keys another user created stay
// valid and are not listed; other users' notifications are unaffected; the
// org_admins of another organization are not notified, and an administrator
// of that organization deactivates nothing (404). The deactivation of a
// creator in the other organization notifies only its own org_admins.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeactivationRevokesServiceAccountKeysOfCreator(t *testing.T) {
	f := scopetest.Seed(t, "f6")
	bg := context.Background()
	users := user.NewPGRepository(f.App)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// orgAdmin creates a user holding the seeded standard role org_admin.
	orgAdmin := func(orgID, name string) string {
		t.Helper()
		id := f.AppUser(t, orgID, name)
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
			SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, orgID, id)
		return id
	}
	creator := f.AppUser(t, f.OrgA, "e64-creator")
	admin := orgAdmin(f.OrgA, "e64-admin")
	secondAdmin := orgAdmin(f.OrgA, "e64-admin2")
	engineer := f.AppUser(t, f.OrgA, "e64-engineer")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA, engineer)
	foreignAdmin := orgAdmin(f.OrgB, "e64-foreign-admin")
	foreignCreator := f.AppUser(t, f.OrgB, "e64-foreign-creator")
	serviceAccount, foreignAccount := f.ID(), f.ID()
	exec(`INSERT INTO service_account (id, organization_id, name, created_by) VALUES ($1, $2, 'e64-integration', $3)`,
		serviceAccount, f.OrgA, creator)
	exec(`INSERT INTO service_account (id, organization_id, name, created_by) VALUES ($1, $2, 'e64-foreign-integration', $3)`,
		foreignAccount, f.OrgB, foreignCreator)

	asUser := func(ctx context.Context, orgID, userID string) context.Context {
		scope := database.OrgWideScope(orgID, userID)
		return database.ContextWithTenantScope(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID, UserID: userID}), &scope)
	}
	keys := identity.NewPGAPIKeyStore(f.App)
	createKey := func(orgID, owner string, account *string) (id, prefix, name string) {
		t.Helper()
		generated, err := identity.GenerateAPIKey("live")
		if err != nil {
			t.Fatal(err)
		}
		created, err := keys.Create(asUser(bg, orgID, owner), &identity.StoredAPIKey{OrganizationID: orgID, Name: "e64-" + generated.KeyPrefix,
			KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix, Environment: "live", Permissions: []identity.Permission{identity.PermCIRead},
			CreatedBy: owner, ServiceAccountID: account})
		if err != nil {
			t.Fatalf("create API key: %v", err)
		}
		return created.ID, generated.KeyPrefix, created.Name
	}
	userKey, userPrefix, _ := createKey(f.OrgA, creator, nil)
	saKey, saPrefix, saName := createKey(f.OrgA, creator, &serviceAccount)
	otherKey, otherPrefix, _ := createKey(f.OrgA, admin, nil)
	otherSAKey, otherSAPrefix, _ := createKey(f.OrgA, admin, &serviceAccount)
	foreignSAKey, foreignSAPrefix, _ := createKey(f.OrgB, foreignCreator, &foreignAccount)

	// The notification center of both organizations: an in-app channel each
	// (NTF-05); the events of the outbox reach it through the consumer.
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	for _, org := range []string{f.OrgA, f.OrgB} {
		if err := center.Channels.Create(bg, &notify.Channel{OrganizationID: org, Name: "center", Provider: notify.ProviderInApp, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences,
		notify.NewSender(map[string]notify.Provider{notify.ProviderInApp: notify.NewInAppProvider(center.Inbox)}))
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	// deliverEvents hands the pending events of the two fixture
	// organizations to the consumer as the publisher does, without touching
	// the outbox of other packages' fixtures, and works off the deliveries.
	deliverEvents := func() {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text, type, organization_id::text, occurred_at, sequence, COALESCE(entity_id, ''),
			COALESCE(entity_seq, 0), data FROM event_outbox WHERE organization_id IN ($1, $2) AND published_at IS NULL ORDER BY organization_id, sequence`,
			f.OrgA, f.OrgB)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (events.Envelope, error) {
			var e events.Envelope
			scanErr := row.Scan(&e.ID, &e.Type, &e.OrgID, &e.OccurredAt, &e.Sequence, &e.EntityID, &e.EntitySeq, &e.Data)
			return e, scanErr
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := range pending {
			if err = consumer.Consume(bg, &pending[i]); err != nil {
				t.Fatalf("consume %s: %v", pending[i].Type, err)
			}
			exec(`UPDATE event_outbox SET published_at = now() WHERE id = $1`, pending[i].ID)
		}
		if _, err = notifier.ProcessDue(bg); err != nil {
			t.Fatal(err)
		}
	}
	inbox := func(orgID, userID string) []notify.InAppNotification {
		t.Helper()
		items, _, err := center.Inbox.List(bg, orgID, userID, false, 50, 0)
		if err != nil {
			t.Fatalf("inbox of %s: %v", userID, err)
		}
		return items
	}
	// rotationNotices are the notifications of userID naming a kept key.
	rotationNotices := func(orgID, userID string) []notify.InAppNotification {
		t.Helper()
		var out []notify.InAppNotification
		for _, n := range inbox(orgID, userID) {
			if n.Data["event_type"] == "apikey.creator_deactivated" {
				out = append(out, n)
			}
		}
		return out
	}
	// An earlier notification of another user must stay as it is.
	if err := center.Inbox.Add(bg, []notify.InAppNotification{{OrganizationID: f.OrgA, UserID: engineer, Category: "export",
		Subject: "e64 earlier notice", Body: "earlier", CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	user.NewHandler(users).WithSessionRevoker(identity.NewSessionRevocations(cache.NewMemoryStore())).RegisterRoutes(mux)
	patch := func(orgID, actor, target string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+target, strings.NewReader(`{"status":"inactive"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(asUser(req.Context(), orgID, actor)))
		return w.Code
	}
	outboxEvents := func(orgID, entityID string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'apikey.creator_deactivated' AND entity_id = $2`,
			orgID, entityID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// An administrator of the other organization deactivates nothing.
	if code := patch(f.OrgB, foreignAdmin, creator); code != http.StatusNotFound {
		t.Fatalf("deactivation of the creator by an administrator of another organization: status %d, want 404", code)
	}
	if n := outboxEvents(f.OrgA, creator) + outboxEvents(f.OrgB, creator); n != 0 {
		t.Errorf("rotation events after the foreign request: %d, want 0", n)
	}

	if code := patch(f.OrgA, admin, creator); code != http.StatusOK {
		t.Fatalf("deactivate the creator: status %d", code)
	}
	for _, c := range []struct {
		name, id, prefix string
		revoked          bool
	}{
		{"user key of the creator", userKey, userPrefix, true},
		{"service-account key of the creator (E-64 option 2)", saKey, saPrefix, false},
		{"user key of another user", otherKey, otherPrefix, false},
		{"service-account key of another user", otherSAKey, otherSAPrefix, false},
		{"service-account key in another organization", foreignSAKey, foreignSAPrefix, false},
	} {
		var revoked bool
		if err := f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, c.id).Scan(&revoked); err != nil || revoked != c.revoked {
			t.Errorf("%s after the deactivation: revoked=%v, %v; want revoked=%v", c.name, revoked, err, c.revoked)
		}
		found, err := keys.LookupByPrefix(bg, c.prefix)
		if err != nil || (found != nil) == c.revoked {
			t.Errorf("lookup of the %s: found=%v, %v; want found=%v", c.name, found != nil, err, !c.revoked)
		}
	}

	// The audit entry counts the revoked key and lists the kept one.
	var counted int64
	var keptRaw []byte
	if err := f.Admin.QueryRow(bg, `SELECT (changes->>'api_keys_revoked')::bigint, COALESCE(changes->'service_account_keys_kept', 'null')
		FROM audit_log WHERE organization_id = $1 AND action = 'user.deactivated' AND resource_id = $2`,
		f.OrgA, creator).Scan(&counted, &keptRaw); err != nil || counted != 1 {
		t.Errorf("api_keys_revoked in the deactivation audit entry: %d, %v; want 1", counted, err)
	}
	var kept []map[string]any
	want := map[string]any{"id": saKey, "key_prefix": saPrefix, "name": saName, "service_account_id": serviceAccount,
		"service_account_name": "e64-integration"}
	if err := json.Unmarshal(keptRaw, &kept); err != nil || len(kept) != 1 {
		t.Errorf("service_account_keys_kept in the deactivation audit entry: %s, %v; want the one kept key", keptRaw, err)
	} else if !reflect.DeepEqual(kept[0], want) {
		t.Errorf("kept key in the audit entry: %v, want %v (no secret, no hash)", kept[0], want)
	}

	// Exactly one rotation notice for each org_admin of the organization,
	// naming the kept key; nobody else is notified.
	deliverEvents()
	for _, a := range []string{admin, secondAdmin} {
		notices := rotationNotices(f.OrgA, a)
		if len(notices) != 1 {
			t.Errorf("rotation notices of org_admin %s: %d, want 1", a, len(notices))
			continue
		}
		n := notices[0]
		if !strings.Contains(n.Body, saPrefix) || !strings.Contains(n.Body, "e64-integration") || strings.Contains(n.Body, otherSAPrefix) ||
			strings.Contains(n.Body, userPrefix) {
			t.Errorf("rotation notice of %s does not name exactly the kept key %s: %q", a, saPrefix, n.Body)
		}
		if n.Category != "apikey" || n.Data["user_id"] != creator {
			t.Errorf("rotation notice of %s: category %q, data %v", a, n.Category, n.Data)
		}
	}
	if items := inbox(f.OrgA, engineer); len(items) != 1 || items[0].Subject != "e64 earlier notice" {
		t.Errorf("notifications of a user without org_admin: %+v, want only the earlier one", items)
	}
	if items := inbox(f.OrgA, creator); len(items) != 0 {
		t.Errorf("notifications of the deactivated creator: %+v, want none", items)
	}
	// Cross-organization: the org_admin of the other organization gets
	// nothing, and no delivery is queued there.
	if items := inbox(f.OrgB, foreignAdmin); len(items) != 0 {
		t.Errorf("notifications of the org_admin of another organization: %+v, want none", items)
	}
	var foreignDeliveries int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM notification_delivery WHERE organization_id = $1`, f.OrgB).Scan(&foreignDeliveries); err != nil ||
		foreignDeliveries != 0 {
		t.Errorf("deliveries in the other organization: %d, %v; want 0", foreignDeliveries, err)
	}
	// A repeated delivery of the event notifies nobody twice.
	exec(`UPDATE event_outbox SET published_at = NULL WHERE organization_id = $1 AND type = 'apikey.creator_deactivated'`, f.OrgA)
	deliverEvents()
	if n := len(rotationNotices(f.OrgA, admin)); n != 1 {
		t.Errorf("rotation notices of the org_admin after a repeated event: %d, want 1", n)
	}

	// The deactivation of a creator in the other organization notifies only
	// its own org_admin.
	if code := patch(f.OrgB, foreignAdmin, foreignCreator); code != http.StatusOK {
		t.Fatalf("deactivate the creator of the other organization: status %d", code)
	}
	deliverEvents()
	if notices := rotationNotices(f.OrgB, foreignAdmin); len(notices) != 1 || !strings.Contains(notices[0].Body, foreignSAPrefix) {
		t.Errorf("rotation notices of the org_admin of the other organization: %+v, want one naming %s", notices, foreignSAPrefix)
	}
	for _, a := range []string{admin, secondAdmin} {
		for _, n := range rotationNotices(f.OrgA, a) {
			if strings.Contains(n.Body, foreignSAPrefix) {
				t.Errorf("org_admin %s was notified of a key of another organization: %q", a, n.Body)
			}
		}
		if n := len(rotationNotices(f.OrgA, a)); n != 1 {
			t.Errorf("rotation notices of org_admin %s after the deactivation in another organization: %d, want 1", a, n)
		}
	}
}
