package user_test

// User deactivation and anonymization against PostgreSQL and Redis (WP-359,
// TLC-04, AUT-02; finding F-060): the sessions of a user are revoked only
// inside the transaction of a deactivation of a user of the caller's
// organization, after the org-bound lock, and once more after the commit. An
// organization A admin who sends PATCH /users/{id} status=inactive or POST
// /users/{id}/anonymize for a user of organization B, an unknown user or an
// invalid id gets 404 and writes nothing to the session blacklist. A failing
// blacklist rolls the deactivation back (503).
//
// Retest (needs a migrated database and Redis):
//
//	cd backend && TEST_DATABASE_URL=… TEST_REDIS_URL=redis://localhost:56379/0 go test -count=1 -v -run '^TestAuditT2ForeignDeactivationRevokesNothing$|^TestDeactivationRevocationFailureRollsBack$' ./internal/user/

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

// countingRevoker records every session revocation per user before passing
// it on; with fail set it fails like an unreachable Redis.
type countingRevoker struct {
	next  user.SessionRevoker
	calls map[string]int
	fail  bool
}

func (c *countingRevoker) RevokeUser(ctx context.Context, userID string, at time.Time) error {
	c.calls[userID]++
	if c.fail {
		return errors.New("blacklist unavailable")
	}
	return c.next.RevokeUser(ctx, userID, at)
}

// runStore prefixes the keys of one test run: the fixture ids repeat across
// runs, and a blacklist entry lives for a day in the shared Redis.
type runStore struct {
	cache.Store
	prefix string
}

func (s runStore) Get(ctx context.Context, key string) (string, bool, error) {
	return s.Store.Get(ctx, s.prefix+key)
}

func (s runStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.Store.Set(ctx, s.prefix+key, value, ttl)
}

// deactivationFixture is a handler on PostgreSQL whose session blacklist
// lives in Redis, with an issuer that validates against it.
type deactivationFixture struct {
	f           *scopetest.Fixture
	admin       string
	revocations *identity.SessionRevocations
	issuer      *identity.SessionIssuer
	revoker     *countingRevoker
	mux         *chi.Mux
}

func newDeactivationFixture(t *testing.T) *deactivationFixture {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	f := scopetest.Seed(t, "eb")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("TEST_REDIS_URL is required next to TEST_DATABASE_URL for this integration test")
	}
	client, err := redisx.Connect(context.Background(), redisURL)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	d := &deactivationFixture{f: f, admin: f.AppUser(t, f.OrgA, "t2-deact-admin-a")}
	d.revocations = identity.NewSessionRevocations(runStore{Store: cache.NewRedisStore(client.Unwrap()), prefix: "t2-deact-" + time.Now().UTC().Format("20060102T150405.000000000") + ":"})
	d.issuer = issuer.WithRevocations(d.revocations)
	d.revoker = &countingRevoker{next: d.revocations, calls: map[string]int{}}
	d.mux = chi.NewRouter()
	user.NewHandler(user.NewPGRepository(f.App)).WithSessionRevoker(d.revoker).RegisterRoutes(d.mux)
	return d
}

// session issues a valid session of subject in org, two seconds in the past
// so the blacklist (second resolution) separates it from later revocations.
func (d *deactivationFixture) session(t *testing.T, subject, org string) string {
	t.Helper()
	issued := time.Now().UTC().Add(-2 * time.Second)
	token, err := d.issuer.Issue(identity.SessionClaims{Subject: subject, OrganizationID: org, ID: "t2-" + subject, IssuedAt: issued, ExpiresAt: issued.Add(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.issuer.Validate(token); err != nil {
		t.Fatalf("token of %s before the request: %v", subject, err)
	}
	return token
}

// asAdminA sends the request as the org-wide admin of organization A.
func (d *deactivationFixture) asAdminA(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	scope := database.OrgWideScope(d.f.OrgA, d.admin)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: d.f.OrgA, UserID: d.admin})
	w := httptest.NewRecorder()
	d.mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
	return w
}

// apiKey seeds an active API key created by owner and returns its id.
func (d *deactivationFixture) apiKey(t *testing.T, org, owner, prefix string) string {
	t.Helper()
	id := d.f.ID()
	if _, err := d.f.Admin.Exec(context.Background(), `INSERT INTO api_key (id, organization_id, name, key_hash, key_prefix, created_by) VALUES ($1, $2, $3, 'hash', $3, $4)`,
		id, org, prefix, owner); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	return id
}

// state reads is_active, e-mail, the revocation of the key and the number
// of deactivation audit entries of the user.
func (d *deactivationFixture) state(t *testing.T, userID, keyID string) (active bool, email string, keyRevoked bool, audits int) {
	t.Helper()
	bg := context.Background()
	if err := d.f.Admin.QueryRow(bg, `SELECT is_active, email FROM app_user WHERE id = $1`, userID).Scan(&active, &email); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := d.f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, keyID).Scan(&keyRevoked); err != nil {
		t.Fatalf("read api key: %v", err)
	}
	if err := d.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action IN ('user.deactivated', 'user.anonymized')`, userID).Scan(&audits); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return active, email, keyRevoked, audits
}

var deactivationRequests = []struct {
	name, method, suffix, body string
}{
	{"PATCH status inactive", http.MethodPatch, "", `{"status":"inactive"}`},
	{"POST anonymize", http.MethodPost, "/anonymize", ""},
}

func slug(name string) string { return strings.ReplaceAll(strings.ToLower(name), " ", "-") }

// TestAuditT2ForeignDeactivationRevokesNothing: an organization A admin who
// sends PATCH /users/{id} status=inactive or POST /users/{id}/anonymize for a
// user of organization B gets 404, and the organization B user's sessions,
// account and API keys stay unchanged (tenant isolation). Unknown and invalid
// ids neither reach the blacklist. Same-organization deactivation and
// anonymization end the session at once, revoking in the transaction and
// again after the commit (TLC-04).
func TestAuditT2ForeignDeactivationRevokesNothing(t *testing.T) {
	d := newDeactivationFixture(t)
	bg := context.Background()

	for _, c := range deactivationRequests {
		t.Run(c.name+" on an organization B user", func(t *testing.T) {
			victim := d.f.AppUser(t, d.f.OrgB, "t2-deact-victim-"+slug(c.name))
			keyID := d.apiKey(t, d.f.OrgB, victim, "t2v"+slug(c.name)[:4])
			_, emailBefore, _, _ := d.state(t, victim, keyID)
			token := d.session(t, victim, d.f.OrgB)
			w := d.asAdminA(c.method, "/api/v1/users/"+victim+c.suffix, c.body)
			if w.Code != http.StatusNotFound {
				t.Errorf("%s by an org A admin on an org B user: %d, want 404", c.name, w.Code)
			}
			if n := d.revoker.calls[victim]; n != 0 {
				t.Errorf("%s by org A called the session revocation %d times for the org B user; want 0", c.name, n)
			}
			if at, err := d.revocations.RevokedAt(bg, victim); err != nil || !at.IsZero() {
				t.Errorf("%s by org A wrote a session blacklist entry for the org B user (revoked at %v, err %v); want none", c.name, at, err)
			}
			if _, err := d.issuer.Validate(token); err != nil {
				t.Errorf("org B user's session after the foreign %s: %v; want still valid", c.name, err)
			}
			active, email, keyRevoked, audits := d.state(t, victim, keyID)
			if !active || email != emailBefore || keyRevoked || audits != 0 {
				t.Errorf("org B user after the foreign %s: active=%v email=%q key revoked=%v audits=%d; want unchanged", c.name, active, email, keyRevoked, audits)
			}
		})
	}

	for _, id := range []string{d.f.ID(), "not-a-uuid"} {
		for _, c := range deactivationRequests {
			t.Run(c.name+" on unknown id "+id, func(t *testing.T) {
				if w := d.asAdminA(c.method, "/api/v1/users/"+id+c.suffix, c.body); w.Code != http.StatusNotFound {
					t.Errorf("%s on an unknown user: %d, want 404", c.name, w.Code)
				}
				if n := d.revoker.calls[id]; n != 0 {
					t.Errorf("%s on an unknown user called the session revocation %d times; want 0", c.name, n)
				}
			})
		}
	}

	for _, c := range deactivationRequests {
		t.Run("control: same organization "+c.name+" ends the session", func(t *testing.T) {
			member := d.f.AppUser(t, d.f.OrgA, "t2-deact-member-"+slug(c.name))
			keyID := d.apiKey(t, d.f.OrgA, member, "t2m"+slug(c.name)[:4])
			token := d.session(t, member, d.f.OrgA)
			if w := d.asAdminA(c.method, "/api/v1/users/"+member+c.suffix, c.body); w.Code != http.StatusOK {
				t.Fatalf("%s on an org A user: %d %s", c.name, w.Code, w.Body.String())
			}
			if _, err := d.issuer.Validate(token); !errors.Is(err, identity.ErrSessionRevoked) {
				t.Errorf("session of the org A user after %s: %v, want ErrSessionRevoked", c.name, err)
			}
			if n := d.revoker.calls[member]; n != 2 {
				t.Errorf("session revocations for the org A user: %d, want 2 (in the transaction and after the commit)", n)
			}
			active, _, keyRevoked, audits := d.state(t, member, keyID)
			if active || !keyRevoked || audits != 1 {
				t.Errorf("org A user after %s: active=%v key revoked=%v audits=%d; want inactive, key revoked, 1 audit entry", c.name, active, keyRevoked, audits)
			}
		})
	}
}

// TestDeactivationRevocationFailureRollsBack: when the session blacklist
// cannot be written, deactivation and anonymization answer 503 and the
// transaction is rolled back: the user stays active with its personal data,
// its API key stays valid and no audit entry is written (TLC-04).
func TestDeactivationRevocationFailureRollsBack(t *testing.T) {
	d := newDeactivationFixture(t)
	d.revoker.fail = true

	for _, c := range deactivationRequests {
		t.Run(c.name, func(t *testing.T) {
			member := d.f.AppUser(t, d.f.OrgA, "t2-deact-fail-"+slug(c.name))
			keyID := d.apiKey(t, d.f.OrgA, member, "t2f"+slug(c.name)[:4])
			_, emailBefore, _, _ := d.state(t, member, keyID)
			w := d.asAdminA(c.method, "/api/v1/users/"+member+c.suffix, c.body)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s with an unavailable blacklist: %d %s, want 503", c.name, w.Code, w.Body.String())
			}
			if n := d.revoker.calls[member]; n != 1 {
				t.Errorf("session revocation attempts: %d, want 1 (inside the transaction)", n)
			}
			active, email, keyRevoked, audits := d.state(t, member, keyID)
			if !active || email != emailBefore || keyRevoked || audits != 0 {
				t.Errorf("user after the failed %s: active=%v email=%q key revoked=%v audits=%d; want unchanged", c.name, active, email, keyRevoked, audits)
			}
		})
	}
}
