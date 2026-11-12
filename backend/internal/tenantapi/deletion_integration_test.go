package tenantapi_test

// WP-1152 (TLC-06 part): the deletion request of an organization — an
// org-wide holder of org:delete with a session opened with a second factor
// requests it with the slug as confirmation, the state is requested for 30
// days of grace and can be cancelled; a trigger allows only the transitions
// of the deletion. PostgreSQL integration through the session middleware
// (verifier, tenant context) and the route authorization of the server.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestOrgDeletion' ./internal/tenantapi/

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// stubVerifier verifies the session tokens of the test: each token stands
// for its claims.
type stubVerifier map[string]*identity.SessionClaims

func (v stubVerifier) Validate(token string) (*identity.SessionClaims, error) {
	c, ok := v[token]
	if !ok {
		return nil, errors.New("unknown token")
	}
	return c, nil
}

// deletionEnv routes /api/v1/org/deletion like the server: session
// middleware, tenant context, route authorization.
type deletionEnv struct {
	f        *scopetest.Fixture
	tokens   stubVerifier
	handler  http.Handler
	slug     string
	admin    string
	engineer string
}

func newDeletionEnv(t *testing.T) *deletionEnv {
	t.Helper()
	f := scopetest.Seed(t, "21")
	e := &deletionEnv{f: f, tokens: stubVerifier{}, slug: "scopetest-21-a"}
	mux := chi.NewRouter()
	h := tenantapi.NewDeletionHandler(f.App)
	for _, route := range []struct {
		method  string
		handler http.HandlerFunc
	}{
		{http.MethodGet, h.Get}, {http.MethodPost, h.Request}, {http.MethodDelete, h.Cancel},
	} {
		mux.With(server.AuthorizeRoute(route.method, "/api/v1/org/deletion")).Method(route.method, "/api/v1/org/deletion", route.handler)
	}
	e.handler = middleware.AuthMiddlewareWithVerifier(e.tokens)(middleware.TenantMiddleware(mux))
	user := f.AppUser(t, f.OrgA, "wp1152-admin")
	e.admin = e.session(t, "admin-mfa", user, "org_admin", true)
	e.session(t, "admin-nomfa", user, "org_admin", false)
	e.engineer = e.session(t, "engineer-mfa", f.AppUser(t, f.OrgA, "wp1152-engineer"), "engineer", true)
	return e
}

// session registers a token of user with the permissions of a standard
// role, organization-wide.
func (e *deletionEnv) session(t *testing.T, token, user, role string, mfa bool) string {
	t.Helper()
	keys, ok := permission.StandardRolePermissions(role)
	if !ok {
		t.Fatalf("standard role %s unknown", role)
	}
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}
	all := identity.ScopeSet{All: true}
	e.tokens[token] = &identity.SessionClaims{Subject: user, OrganizationID: e.f.OrgA, Permissions: perms,
		Scope: &identity.Scope{Clients: all, Sites: all, Teams: all}, SessionID: "sid-" + token, MFA: mfa,
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	return user
}

type deletionResponse struct {
	code int
	body string
	out  tenantapi.OrganizationDeletion
}

func (e *deletionEnv) call(t *testing.T, token, method, body string) deletionResponse {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/org/deletion", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	r := deletionResponse{code: w.Code, body: w.Body.String()}
	if w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &r.out); err != nil {
			t.Fatalf("%s: %v (%s)", method, err, r.body)
		}
	}
	return r
}

func (e *deletionEnv) request(t *testing.T, token string) deletionResponse {
	t.Helper()
	return e.call(t, token, http.MethodPost, `{"confirm_slug":"`+e.slug+`"}`)
}

func (e *deletionEnv) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT deletion_state FROM organization WHERE id = $1`, e.f.OrgA).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// outbox returns the data of the events of typ of organization A.
func (e *deletionEnv) outbox(t *testing.T, typ string) []events.OrgDeletionData {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT entity_id, data FROM event_outbox WHERE organization_id = $1 AND type = $2 ORDER BY sequence`,
		e.f.OrgA, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.OrgDeletionData
	for rows.Next() {
		var entity string
		var raw []byte
		if err := rows.Scan(&entity, &raw); err != nil {
			t.Fatal(err)
		}
		var d events.OrgDeletionData
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if entity != e.f.OrgA {
			t.Errorf("%s: entity %s, want the organization", typ, entity)
		}
		out = append(out, d)
	}
	return out
}

// TestOrgDeletionRequiresMFA: a session without second factor is refused
// with 403 mfa-required, request and cancel alike; nothing changes.
func TestOrgDeletionRequiresMFA(t *testing.T) {
	e := newDeletionEnv(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		r := e.call(t, "admin-nomfa", method, `{"confirm_slug":"`+e.slug+`"}`)
		if r.code != http.StatusForbidden || !strings.Contains(r.body, tenantapi.ProblemTypeMFARequired) {
			t.Errorf("%s without MFA = %d %s, want 403 mfa-required", method, r.code, r.body)
		}
	}
	if s := e.state(t); s != "none" || len(e.outbox(t, events.OrgDeletionRequested)) != 0 {
		t.Errorf("state %s after the refused request", s)
	}
	// Reading the state needs no second factor.
	if r := e.call(t, "admin-nomfa", http.MethodGet, ""); r.code != http.StatusOK || r.out.State != "none" ||
		r.out.DueAt != nil || r.out.RemainingSeconds != nil {
		t.Errorf("GET = %d %s", r.code, r.body)
	}
}

// TestOrgDeletionRequiresPermission: an engineer (no org:delete) is
// refused with 403 even with a second factor; the engineer still reads
// the state (location:read, the banner of WP-1157).
func TestOrgDeletionRequiresPermission(t *testing.T) {
	e := newDeletionEnv(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if r := e.call(t, "engineer-mfa", method, `{"confirm_slug":"`+e.slug+`"}`); r.code != http.StatusForbidden {
			t.Errorf("%s as engineer = %d %s, want 403", method, r.code, r.body)
		}
	}
	if s := e.state(t); s != "none" {
		t.Errorf("state %s", s)
	}
	if r := e.call(t, "engineer-mfa", http.MethodGet, ""); r.code != http.StatusOK || r.out.State != "none" {
		t.Errorf("GET as engineer = %d %s", r.code, r.body)
	}
}

// TestOrgDeletionWrongSlug: a missing or wrong slug is 422 with the field.
func TestOrgDeletionWrongSlug(t *testing.T) {
	e := newDeletionEnv(t)
	for _, body := range []string{`{"confirm_slug":"scopetest-21-b"}`, `{"confirm_slug":""}`, `{}`} {
		r := e.call(t, "admin-mfa", http.MethodPost, body)
		if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, `"confirm_slug"`) {
			t.Errorf("POST %s = %d %s, want 422 confirm_slug", body, r.code, r.body)
		}
	}
	if s := e.state(t); s != "none" {
		t.Errorf("state %s", s)
	}
}

// TestOrgDeletionTwice: a second request is 409; the first stays.
func TestOrgDeletionTwice(t *testing.T) {
	e := newDeletionEnv(t)
	first := e.request(t, "admin-mfa")
	if first.code != http.StatusAccepted {
		t.Fatalf("first request = %d %s", first.code, first.body)
	}
	if r := e.request(t, "admin-mfa"); r.code != http.StatusConflict {
		t.Errorf("second request = %d %s, want 409", r.code, r.body)
	}
	if n := len(e.outbox(t, events.OrgDeletionRequested)); n != 1 {
		t.Errorf("%d org.deletion_requested events, want 1", n)
	}
}

// TestOrgDeletionRequestSetsDueDate: the request sets requested, the due
// date 30 days after the request and org.deletion_requested in the outbox;
// GET shows the rest of the grace period.
func TestOrgDeletionRequestSetsDueDate(t *testing.T) {
	e := newDeletionEnv(t)
	r := e.request(t, "admin-mfa")
	if r.code != http.StatusAccepted || r.out.State != "requested" || r.out.RequestedAt == nil || r.out.DueAt == nil {
		t.Fatalf("POST = %d %s", r.code, r.body)
	}
	if got := r.out.DueAt.Sub(*r.out.RequestedAt); got != tenantapi.DeletionGracePeriod {
		t.Errorf("due %v after the request, want %v", got, tenantapi.DeletionGracePeriod)
	}
	if r.out.RequestedBy == nil || *r.out.RequestedBy != e.admin {
		t.Errorf("requested_by %v, want %s", r.out.RequestedBy, e.admin)
	}
	var requested, due time.Time
	var by string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT deletion_requested_at, deletion_due_at, deletion_requested_by::text
		FROM organization WHERE id = $1`, e.f.OrgA).Scan(&requested, &due, &by); err != nil {
		t.Fatal(err)
	}
	if due.Sub(requested) != 30*24*time.Hour || by != e.admin || e.state(t) != "requested" {
		t.Errorf("stored request %v → %v by %s", requested, due, by)
	}
	evs := e.outbox(t, events.OrgDeletionRequested)
	if len(evs) != 1 || evs[0].ID != e.f.OrgA || evs[0].RequestedBy != e.admin || evs[0].DueAt != due.UTC().Format(time.RFC3339) {
		t.Errorf("org.deletion_requested events %+v", evs)
	}
	g := e.call(t, "engineer-mfa", http.MethodGet, "")
	if g.code != http.StatusOK || g.out.State != "requested" || g.out.RemainingSeconds == nil ||
		*g.out.RemainingSeconds <= int64((29*24*time.Hour).Seconds()) || *g.out.RemainingSeconds > int64(tenantapi.DeletionGracePeriod.Seconds()) {
		t.Errorf("GET = %d %s", g.code, g.body)
	}
	// PATCH /org cannot touch the deletion columns: they are no field of
	// its request; a direct change of the request keeps failing.
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE organization SET deletion_due_at = now() WHERE id = $1`, e.f.OrgA); err == nil {
		t.Error("the due date of a request was changed without a transition")
	}
}

// TestOrgDeletionCancel: the cancel empties the request and writes
// org.deletion_cancelled; without a request it is 404, once the purge
// started 409.
func TestOrgDeletionCancel(t *testing.T) {
	e := newDeletionEnv(t)
	bg := context.Background()
	if r := e.call(t, "admin-mfa", http.MethodDelete, ""); r.code != http.StatusNotFound {
		t.Errorf("cancel without request = %d %s, want 404", r.code, r.body)
	}
	if r := e.request(t, "admin-mfa"); r.code != http.StatusAccepted {
		t.Fatalf("request = %d %s", r.code, r.body)
	}
	due := e.outbox(t, events.OrgDeletionRequested)[0].DueAt
	r := e.call(t, "admin-mfa", http.MethodDelete, "")
	if r.code != http.StatusOK || r.out.State != "none" || r.out.RequestedAt != nil || r.out.DueAt != nil || r.out.RequestedBy != nil {
		t.Fatalf("cancel = %d %s", r.code, r.body)
	}
	var cleared bool
	if err := e.f.Admin.QueryRow(bg, `SELECT deletion_state = 'none' AND deletion_requested_at IS NULL AND deletion_due_at IS NULL
		AND deletion_requested_by IS NULL FROM organization WHERE id = $1`, e.f.OrgA).Scan(&cleared); err != nil || !cleared {
		t.Errorf("columns after the cancel cleared = %v %v", cleared, err)
	}
	evs := e.outbox(t, events.OrgDeletionCancelled)
	if len(evs) != 1 || evs[0].CancelledBy != e.admin || evs[0].RequestedBy != e.admin || evs[0].DueAt != due {
		t.Errorf("org.deletion_cancelled events %+v", evs)
	}

	// A new request falls due at once (grace period passed) and the purge
	// starts: the cancel is refused.
	if r = e.request(t, "admin-mfa"); r.code != http.StatusAccepted {
		t.Fatalf("second request = %d %s", r.code, r.body)
	}
	passGracePeriod(t, e)
	if _, err := e.f.Admin.Exec(bg, `UPDATE organization SET deletion_state = 'purging' WHERE id = $1`, e.f.OrgA); err != nil {
		t.Fatalf("requested -> purging after the grace period: %v", err)
	}
	if r = e.call(t, "admin-mfa", http.MethodDelete, ""); r.code != http.StatusConflict {
		t.Errorf("cancel while purging = %d %s, want 409", r.code, r.body)
	}
	if r = e.request(t, "admin-mfa"); r.code != http.StatusConflict {
		t.Errorf("request while purging = %d %s, want 409", r.code, r.body)
	}
	if s := e.state(t); s != "purging" {
		t.Errorf("state %s, want purging", s)
	}
	resetDeletion(t, e)
}

// passGracePeriod moves the due date of the request into the past with the
// triggers off (session_replication_role replica), as 30 days would.
func passGracePeriod(t *testing.T, e *deletionEnv) {
	t.Helper()
	withoutTriggers(t, e, `UPDATE organization SET deletion_requested_at = now() - interval '31 days',
		deletion_due_at = now() - interval '1 day' WHERE id = $1`)
}

// resetDeletion sets the fixture organization back to none (cleanup of a
// purging state the trigger keeps).
func resetDeletion(t *testing.T, e *deletionEnv) {
	t.Helper()
	withoutTriggers(t, e, `UPDATE organization SET deletion_state = 'none', deletion_requested_at = NULL,
		deletion_due_at = NULL, deletion_requested_by = NULL WHERE id = $1`)
}

func withoutTriggers(t *testing.T, e *deletionEnv, sql string) {
	t.Helper()
	if err := execWithoutTriggers(e, sql); err != nil {
		t.Fatal(err)
	}
}

// updateWithoutTriggers sets the columns of the fixture organization past
// the transition trigger; the checks still apply.
func updateWithoutTriggers(e *deletionEnv, set string) error {
	return execWithoutTriggers(e, `UPDATE organization SET `+set+` WHERE id = $1`)
}

func execWithoutTriggers(e *deletionEnv, sql string) (err error) {
	bg := context.Background()
	conn, err := e.f.Admin.Acquire(bg)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(bg, `SET session_replication_role = replica`); err != nil {
		return err
	}
	defer func() {
		if _, rerr := conn.Exec(bg, `SET session_replication_role = origin`); rerr != nil && err == nil {
			err = rerr
		}
	}()
	_, err = conn.Exec(bg, sql, e.f.OrgA)
	return err
}

// TestOrgDeletionTransitionTrigger: the trigger allows only none ->
// requested, requested -> none, requested -> purging after the due date
// and purging -> purged.
func TestOrgDeletionTransitionTrigger(t *testing.T) {
	e := newDeletionEnv(t)
	bg := context.Background()
	update := func(set string) error {
		_, err := e.f.Admin.Exec(bg, `UPDATE organization SET `+set+` WHERE id = $1`, e.f.OrgA)
		return err
	}
	for _, set := range []string{
		`deletion_state = 'purging', deletion_requested_at = now(), deletion_due_at = now()`,
		`deletion_state = 'purged', deletion_requested_at = now(), deletion_due_at = now()`,
		// A request without a due date, without a request date, with a due
		// date in the past or before the end of the 30 days of grace.
		`deletion_state = 'requested', deletion_requested_at = now()`,
		`deletion_state = 'requested', deletion_due_at = now() + interval '30 days'`,
		`deletion_state = 'requested', deletion_requested_at = now(), deletion_due_at = now() - interval '1 day'`,
		`deletion_state = 'requested', deletion_requested_at = now(), deletion_due_at = now() + interval '29 days'`,
		// Without a request no requester.
		`deletion_requested_by = gen_random_uuid()`,
	} {
		if err := update(set); err == nil {
			t.Errorf("none -> %s was allowed", set)
		}
	}
	// The check holds without the trigger as well (e.g. a restore).
	for _, set := range []string{
		`deletion_state = 'requested', deletion_requested_at = now()`,
		`deletion_state = 'purging', deletion_requested_at = now(), deletion_due_at = now() - interval '1 day'`,
		`deletion_state = 'none', deletion_due_at = now()`,
	} {
		if err := updateWithoutTriggers(e, set); err == nil {
			t.Errorf("check allowed %s", set)
		}
	}
	if r := e.request(t, "admin-mfa"); r.code != http.StatusAccepted {
		t.Fatalf("request = %d %s", r.code, r.body)
	}
	for _, set := range []string{`deletion_state = 'purging'`, `deletion_state = 'purged'`,
		`deletion_state = 'purging', deletion_due_at = now() - interval '1 day'`} {
		if err := update(set); err == nil {
			t.Errorf("requested -> %s before the due date was allowed", set)
		}
	}
	if s := e.state(t); s != "requested" {
		t.Fatalf("state %s", s)
	}
	passGracePeriod(t, e)
	if err := update(`deletion_state = 'purging'`); err != nil {
		t.Fatalf("requested -> purging after the due date: %v", err)
	}
	if err := update(`deletion_state = 'requested'`); err == nil {
		t.Error("purging -> requested was allowed")
	}
	if err := update(`deletion_state = 'purged'`); err != nil {
		t.Fatalf("purging -> purged: %v", err)
	}
	for _, set := range []string{`deletion_state = 'none', deletion_requested_at = NULL, deletion_due_at = NULL, deletion_requested_by = NULL`,
		`deletion_state = 'purging'`} {
		if err := update(set); err == nil {
			t.Errorf("purged -> %s was allowed", set)
		}
	}
	// An unknown state violates the check.
	resetDeletion(t, e)
	if err := update(`deletion_state = 'gone'`); err == nil {
		t.Error("unknown state accepted")
	}
}

// TestOrgDeletionSessionMFAClaim: identity.MFASatisfied decides the claim
// mfa of the login; it survives the JSON form of the refresh grant and
// reaches the handler as Principal.MFA through the session middleware. An
// API key never has it.
func TestOrgDeletionSessionMFAClaim(t *testing.T) {
	for _, c := range []struct {
		amr  []string
		acr  string
		want bool
	}{
		{[]string{"pwd", "otp"}, "", true},
		{[]string{"pwd"}, "1", false},
		{[]string{"pwd"}, "2", true},
		{nil, "", false},
		{[]string{"WebAuthn"}, "0", true},
	} {
		if got := identity.MFASatisfied(c.amr, c.acr); got != c.want {
			t.Errorf("MFASatisfied(%v, %q) = %v, want %v", c.amr, c.acr, got, c.want)
		}
	}
	if !identity.MFASatisfied([]string{"pwd"}, "gold", "gold") || identity.MFASatisfied([]string{"pwd"}, "2", "gold") {
		t.Error("configured acr values are not honoured")
	}

	all := identity.ScopeSet{All: true}
	claims := identity.SessionClaims{Subject: "c0de2100-0000-4000-8000-0000000000f1", OrganizationID: "c0de21a0-0000-4000-8000-000000000001",
		Permissions: []identity.Permission{identity.PermLocationRead}, Scope: &identity.Scope{Clients: all, Sites: all, Teams: all},
		SessionID: "sid", MFA: identity.MFASatisfied([]string{"pwd", "otp"}, "")}
	raw, err := json.Marshal(identity.RefreshGrant{Family: "sid", Claims: claims})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mfa":true`) {
		t.Errorf("refresh grant %s without the claim mfa", raw)
	}
	var grant identity.RefreshGrant
	if err = json.Unmarshal(raw, &grant); err != nil || !grant.Claims.MFA {
		t.Fatalf("refresh grant after the round trip: MFA %v, %v", grant.Claims.MFA, err)
	}
	without := claims
	without.MFA = false
	if raw, _ = json.Marshal(without); strings.Contains(string(raw), `"mfa"`) {
		t.Errorf("claims without MFA carry the claim: %s", raw)
	}

	refreshed := grant.Claims
	refreshed.ExpiresAt = time.Now().Add(time.Hour)
	plain := without
	plain.ExpiresAt = refreshed.ExpiresAt
	seen := map[string]bool{}
	probe := middleware.AuthMiddlewareWithVerifier(stubVerifier{"refreshed": &refreshed, "plain": &plain})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := identity.PrincipalFromContext(r.Context())
			seen[r.Header.Get("Authorization")] = p.MFA
			w.WriteHeader(http.StatusNoContent)
		}))
	for _, token := range []string{"refreshed", "plain"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/org/deletion", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		probe.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", token, w.Code, w.Body.String())
		}
	}
	if !seen["Bearer refreshed"] || seen["Bearer plain"] {
		t.Errorf("Principal.MFA through the middleware = %v", seen)
	}
}
