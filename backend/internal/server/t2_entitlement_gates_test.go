// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// entitlement gates through the production router and chain (chain helper
// of t2_auth_chain_test.go) instead of stub handlers, with enforcement on as
// in production (RETICORA_ENTITLEMENT_ENFORCEMENT defaults to true).
//
//   - WP-072 (ENT-05, ENT-03, ENT-07, CH21), gap (high) "Gates through the
//     production router instead of stub handlers": an organization without
//     entitlement rows (no DefaultPlan fallback) gets 403
//     feature-not-entitled for export, bulk ingest, the ingest alias,
//     GraphQL topology, racks and API keys (WP-471), while cmdb_core reads
//     stay 200. With an expired discovery license only ingest is refused
//     (license-expired); reading discovery jobs and exporting stay
//     available.
//   - WP-471 (ENT-05, API-04), finding F-070: racks, mounts and
//     rack-mounts need rack_view in REST and GraphQL (rackLayout); managing
//     API keys and every X-API-Key request need api_access, a session
//     without api_access keeps working (moved from
//     t2_entitlement_auditdefect_test.go).
//   - WP-472 (ENT-03, ENT-08), finding F-072: max_api_keys, max_users and
//     max_collectors are enforced on POST /api-keys, POST /users and POST
//     /collectors with 403 entitlement-limit and nothing created
//     (TestAuditT2EntitlementQuotasEnforced, moved from
//     t2_entitlement_auditdefect_test.go, the defect file is deleted); a
//     downgrade below the stock keeps keys, users and collectors usable and
//     readable and only refuses new ones; on PostgreSQL a first login with
//     an invitation and the acceptance of an invitation at max_users are
//     refused with entitlement-limit and leave the invitation open.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2EntitlementGates|TestAuditT2EntitlementRackViewGated|TestAuditT2EntitlementAPIAccessGatesAPIKeys|TestAuditT2EntitlementQuotasEnforced|TestT2EntitlementQuota' ./internal/server/
package server_test

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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	rackpkg "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

func t2GraphQLForbidden(body []byte) bool {
	var gql struct {
		Errors []struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &gql) != nil {
		return false
	}
	for _, e := range gql.Errors {
		if e.Extensions["code"] == "FORBIDDEN" {
			return true
		}
	}
	return false
}

func TestT2EntitlementGatesWithoutRows(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	token, _ := s.login(t, identity.AllPermissions()...)

	for _, r := range []t2Request{
		{method: http.MethodGet, path: "/api/v1/export/cis?format=csv"},
		{method: http.MethodPost, path: "/api/v1/ingest/bulk", contentType: "application/json", body: `{"records":[]}`},
		{method: http.MethodPost, path: "/api/v1/discovery/ingest", contentType: "application/json", body: `{"records":[]}`},
		{method: http.MethodGet, path: "/api/v1/racks"},
		{method: http.MethodGet, path: "/api/v1/api-keys"},
	} {
		r.bearer = token
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("%s %s for an organization without entitlement rows: %d %s, want 403 feature-not-entitled", r.method, r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	w := s.do(t2Request{method: http.MethodPost, path: "/bff/graphql", bearer: token, contentType: "application/json",
		body: `{"query":"{ topology { nodes { id } } }"}`})
	if !t2GraphQLForbidden(w.Body.Bytes()) {
		t.Errorf("GraphQL topology without the topology entitlement: %d %s, want a FORBIDDEN error", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: token}); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/cis (cmdb_core, always active): %d, want 200", w.Code)
	}
}

func TestT2EntitlementGatesExpiredDiscovery(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	past := time.Now().UTC().Add(-time.Hour)
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureDiscovery, Enabled: true, ValidUntil: &past})
	token, _ := s.login(t, identity.AllPermissions()...)

	for _, r := range []t2Request{
		{method: http.MethodPost, path: "/api/v1/ingest/bulk", contentType: "application/json", body: `{"records":[]}`},
		{method: http.MethodPost, path: "/api/v1/discovery/ingest", contentType: "application/json", body: `{"records":[]}`},
	} {
		r.bearer = token
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "license-expired") {
			t.Errorf("%s %s after the discovery license expired: %d %s, want 403 license-expired", r.method, r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	for _, path := range []string{"/api/v1/discovery/jobs", "/api/v1/export/cis?format=csv"} {
		if w := s.do(t2Request{method: http.MethodGet, path: path, bearer: token}); w.Code != http.StatusOK {
			t.Errorf("GET %s after the discovery license expired: %d %s, want 200 (CH21: only ingest stops)", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}

// TestAuditT2EntitlementRackViewGated: an organization whose rack_view
// entitlement is disabled must not read or write racks and mounts through
// REST or GraphQL (ENT-05: enforcement in REST and GraphQL); cmdb_core reads
// stay available. Enabled again, the same requests pass.
func TestAuditT2EntitlementRackViewGated(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true}) // production default (RETICORA_ENTITLEMENT_ENFORCEMENT)
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureRackView, Enabled: false})
	token, _ := s.login(t, identity.PermRackRead, identity.PermRackWrite, identity.PermCIRead)
	if s.entitlements.IsEnabled(context.Background(), t2Org, entitlement.FeatureRackView) {
		t.Fatal("setup: rack_view still enabled")
	}

	rk := &rackpkg.Rack{OrganizationID: t2Org, RoomID: "7a2a0000-0000-4000-8000-000000000201", Name: "R1", HeightUnits: 42}
	if err := s.repos.Rack.CreateRack(context.Background(), rk); err != nil {
		t.Fatal(err)
	}
	rack := rk.ID
	gated := []t2Request{
		{method: http.MethodGet, path: "/api/v1/racks"},
		{method: http.MethodGet, path: "/api/v1/racks/" + rack},
		{method: http.MethodGet, path: "/api/v1/racks/" + rack + "/layout"},
		{method: http.MethodGet, path: "/api/v1/racks/" + rack + "/mounts"},
		{method: http.MethodGet, path: "/api/v1/mounts"},
		{method: http.MethodGet, path: "/api/v1/mounts/" + rack},
		{method: http.MethodGet, path: "/api/v1/rack-mounts/" + rack},
		{method: http.MethodPost, path: "/api/v1/racks", contentType: "application/json", body: `{"name":"R1","height_units":42}`},
		{method: http.MethodDelete, path: "/api/v1/rack-mounts/" + rack},
	}
	for _, r := range gated {
		r.bearer = token
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("%s %s without rack_view: %d %s, want 403 feature-not-entitled", r.method, r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}

	rackLayout := t2Request{method: http.MethodPost, path: "/bff/graphql", bearer: token, contentType: "application/json",
		body: `{"query":"{ rackLayout(rackId: \"` + rack + `\") { freeFront } }"}`}
	if w := s.do(rackLayout); w.Code != http.StatusForbidden && !t2GraphQLForbidden(w.Body.Bytes()) {
		t.Errorf("GraphQL rackLayout without rack_view: %d %s, want a FORBIDDEN error", w.Code, strings.TrimSpace(w.Body.String()))
	}

	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: token}); w.Code != http.StatusOK {
		t.Errorf("control: GET /api/v1/cis (cmdb_core) without rack_view: %d, want 200", w.Code)
	}

	// Enabled again: the gate opens (the grant invalidates the cache).
	t2ProvisionRows(t, s, entitlement.Entitlement{FeatureKey: entitlement.FeatureRackView, Enabled: true})
	for _, path := range []string{"/api/v1/racks", "/api/v1/racks/" + rack + "/layout", "/api/v1/mounts?rack_id=" + rack} {
		if w := s.do(t2Request{method: http.MethodGet, path: path, bearer: token}); w.Code != http.StatusOK {
			t.Errorf("GET %s with rack_view: %d %s, want 200", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	if w := s.do(rackLayout); t2GraphQLForbidden(w.Body.Bytes()) || !strings.Contains(w.Body.String(), `"freeFront":[`) {
		t.Errorf("GraphQL rackLayout with rack_view: %d %s, want the layout", w.Code, strings.TrimSpace(w.Body.String()))
	}
}

// TestAuditT2EntitlementAPIAccessGatesAPIKeys: API keys are the api_access
// feature (ENT-05, phase-1 feature). With api_access disabled an X-API-Key
// request is refused on every path and managing keys is refused, while the
// same organization's UI session keeps working on the other paths. Enabled
// again, the key works.
func TestAuditT2EntitlementAPIAccessGatesAPIKeys(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true}) // production default (RETICORA_ENTITLEMENT_ENFORCEMENT)
	t2ProvisionRows(t, s)
	admin, _ := s.login(t, identity.PermAPIKeyManage, identity.PermCIRead)
	w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
		body: `{"name":"t2-api-access","permissions":["ci:read"]}`})
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key}); w.Code != http.StatusOK {
		t.Fatalf("setup: key with api_access enabled: %d", w.Code)
	}

	grant := func(enabled bool) {
		t.Helper()
		if _, err := s.entitlements.Grant(context.Background(), entitlement.Entitlement{OrganizationID: t2Org, FeatureKey: entitlement.FeatureAPIAccess,
			Plan: entitlement.PlanEssential, Enabled: enabled, Source: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	grant(false)
	for _, r := range []t2Request{
		{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key},
		{method: http.MethodGet, path: "/api/v1/entitlements", apiKey: created.Key},
		{method: http.MethodGet, path: "/api/v1/api-keys", bearer: admin},
		{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
			body: `{"name":"t2-api-access-2","permissions":["ci:read"]}`},
	} {
		w := s.do(r)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("%s %s (api key %t) of an organization without api_access: %d %s, want 403 feature-not-entitled",
				r.method, r.path, r.apiKey != "", w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", bearer: admin}); w.Code != http.StatusOK {
		t.Errorf("control: session request without api_access: %d, want 200", w.Code)
	}

	grant(true)
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: created.Key}); w.Code != http.StatusOK {
		t.Errorf("X-API-Key request after api_access was enabled again: %d %s, want 200", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/api-keys", bearer: admin}); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/api-keys after api_access was enabled again: %d, want 200", w.Code)
	}
}

// t2ProvisionRows gives the organization the essential plan and stores the
// given rows on top.
func t2ProvisionRows(t *testing.T, s *t2Stack, rows ...entitlement.Entitlement) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.entitlements.Provision(ctx, t2Org, entitlement.PlanEssential); err != nil {
		t.Fatalf("provision: %v", err)
	}
	for _, row := range rows {
		row.OrganizationID, row.Plan, row.Source = t2Org, entitlement.PlanEssential, "manual"
		if _, err := s.entitlements.Grant(ctx, row); err != nil {
			t.Fatalf("grant %s: %v", row.FeatureKey, err)
		}
	}
}

// TestAuditT2EntitlementQuotasEnforced: a quota stored in the limits of a
// row is enforced on the REST write path (ENT-03): with max_api_keys 1 the
// second key, with max_users 1 the second user and with max_collectors 1
// the second collector are refused with 403 entitlement-limit and nothing
// is created.
func TestAuditT2EntitlementQuotasEnforced(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	t2ProvisionRows(t, s,
		entitlement.Entitlement{FeatureKey: entitlement.FeatureAPIAccess, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxAPIKeys: 1}},
		entitlement.Entitlement{FeatureKey: entitlement.FeatureCMDBCore, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxUsers: 1}},
		entitlement.Entitlement{FeatureKey: entitlement.FeatureDiscovery, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCollectors: 1}},
	)
	if q, err := s.entitlements.Quota(context.Background(), t2Org, entitlement.LimitMaxAPIKeys); err != nil || q == nil || *q != 1 {
		t.Fatalf("setup: max_api_keys quota %v (err %v), want 1", q, err)
	}
	admin, _ := s.login(t, identity.PermAPIKeyManage, identity.PermCIRead, identity.PermUserManage, identity.PermUserRead,
		identity.PermCollectorManage, identity.PermDiscoveryRead)

	t.Run("max_api_keys", func(t *testing.T) {
		var codes []int
		for i := range 2 {
			w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/api-keys", bearer: admin, contentType: "application/json",
				body: `{"name":"t2-quota-` + string(rune('a'+i)) + `","permissions":["ci:read"]}`})
			codes = append(codes, w.Code)
			if i == 1 && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "entitlement-limit")) {
				t.Errorf("second API key with max_api_keys 1: %d %s, want 403 entitlement-limit", w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
		if codes[0] != http.StatusCreated {
			t.Errorf("first API key within the quota: %d, want 201", codes[0])
		}
		if keys, _ := s.repos.APIKeys.List(context.Background(), t2Org); len(keys) != 1 {
			t.Errorf("stored API keys: %d, want 1 (nothing created over the quota)", len(keys))
		}
	})
	t.Run("max_users", func(t *testing.T) {
		var codes []int
		for i := range 2 {
			w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/users", bearer: admin, contentType: "application/json",
				body: `{"email":"t2-quota-` + string(rune('a'+i)) + `@example.com","display_name":"T2 Quota"}`})
			codes = append(codes, w.Code)
			if i == 1 && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "entitlement-limit")) {
				t.Errorf("second user with max_users 1: %d %s, want 403 entitlement-limit", w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
		if codes[0] != http.StatusCreated {
			t.Errorf("first user within the quota: %d, want 201", codes[0])
		}
		if n, _ := s.repos.User.CountActiveUsers(context.Background(), t2Org); n != 1 {
			t.Errorf("stored users: %d, want 1 (nothing created over the quota)", n)
		}
	})
	t.Run("max_collectors", func(t *testing.T) {
		var codes []int
		for i := range 2 {
			w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/collectors", bearer: admin, contentType: "application/json",
				body: `{"name":"t2-quota-` + string(rune('a'+i)) + `"}`})
			codes = append(codes, w.Code)
			if i == 1 && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "entitlement-limit")) {
				t.Errorf("second collector with max_collectors 1: %d %s, want 403 entitlement-limit", w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
		if codes[0] != http.StatusCreated {
			t.Errorf("first collector within the quota: %d, want 201", codes[0])
		}
		if n, _ := s.repos.Discovery.CountCollectors(context.Background(), t2Org); n != 1 {
			t.Errorf("stored collectors: %d, want 1 (nothing created over the quota)", n)
		}
	})
}

// TestT2EntitlementQuotaDowngrade covers ENT-08 through the production
// chain: keys, users and collectors created without quota stay usable and
// readable when the limits are lowered below the stock; only a new key,
// user or collector gets 403 entitlement-limit.
func TestT2EntitlementQuotaDowngrade(t *testing.T) {
	s := newT2StackWith(t, entitlement.Options{Enforce: true})
	t2ProvisionRows(t, s)
	admin, _ := s.login(t, identity.PermAPIKeyManage, identity.PermCIRead, identity.PermUserManage, identity.PermUserRead,
		identity.PermCollectorManage, identity.PermDiscoveryRead, identity.PermDiscoveryIngest)
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return s.do(t2Request{method: http.MethodPost, path: path, bearer: admin, contentType: "application/json", body: body})
	}
	var keys, users, collectors []string
	for i := range 2 {
		n := string(rune('a' + i))
		w := post("/api/v1/api-keys", `{"name":"t2-down-`+n+`","permissions":["ci:read"]}`)
		var key struct {
			Key string `json:"key"`
		}
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &key) != nil {
			t.Fatalf("setup: API key without quota: %d %s", w.Code, w.Body.String())
		}
		keys = append(keys, key.Key)
		var created struct {
			ID string `json:"id"`
		}
		if w = post("/api/v1/users", `{"email":"t2-down-`+n+`@example.com","display_name":"T2 Down"}`); w.Code != http.StatusCreated ||
			json.Unmarshal(w.Body.Bytes(), &created) != nil {
			t.Fatalf("setup: user without quota: %d %s", w.Code, w.Body.String())
		}
		users = append(users, created.ID)
		if w = post("/api/v1/collectors", `{"name":"t2-down-`+n+`"}`); w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
			t.Fatalf("setup: collector without quota: %d %s", w.Code, w.Body.String())
		}
		collectors = append(collectors, created.ID)
	}

	// The downgrade: every limit below the stock of two.
	ctx := context.Background()
	for feature, quota := range map[string]string{
		entitlement.FeatureAPIAccess: entitlement.LimitMaxAPIKeys,
		entitlement.FeatureCMDBCore:  entitlement.LimitMaxUsers,
		entitlement.FeatureDiscovery: entitlement.LimitMaxCollectors,
	} {
		if _, err := s.entitlements.Grant(ctx, entitlement.Entitlement{OrganizationID: t2Org, FeatureKey: feature,
			Plan: entitlement.PlanEssential, Enabled: true, Source: "manual", Limits: map[string]int64{quota: 1}}); err != nil {
			t.Fatal(err)
		}
	}

	// The stock stays usable and readable.
	for i, key := range keys {
		if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/cis", apiKey: key}); w.Code != http.StatusOK {
			t.Errorf("API key %d after the downgrade: %d %s, want 200", i, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	for _, c := range []struct {
		path string
		want int
	}{{"/api/v1/api-keys", 2}, {"/api/v1/users", 2}, {"/api/v1/collectors", 2}} {
		w := s.do(t2Request{method: http.MethodGet, path: c.path, bearer: admin})
		var list struct {
			Data []json.RawMessage `json:"data"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Data) != c.want {
			t.Errorf("GET %s after the downgrade: %d, %d entries, want 200 with %d", c.path, w.Code, len(list.Data), c.want)
		}
	}
	for _, id := range users {
		w := s.do(t2Request{method: http.MethodPatch, path: "/api/v1/users/" + id, bearer: admin, contentType: "application/json",
			body: `{"display_name":"T2 Downgraded"}`})
		if w.Code != http.StatusOK {
			t.Errorf("PATCH user %s after the downgrade: %d %s, want 200", id, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	for _, id := range collectors {
		if w := post("/api/v1/collectors/"+id+"/heartbeat", ""); w.Code != http.StatusNoContent {
			t.Errorf("heartbeat of collector %s after the downgrade: %d %s, want 204", id, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}

	// Only new ones are refused.
	for _, r := range []struct{ path, body string }{
		{"/api/v1/api-keys", `{"name":"t2-down-new","permissions":["ci:read"]}`},
		{"/api/v1/users", `{"email":"t2-down-new@example.com","display_name":"T2 Down"}`},
		{"/api/v1/collectors", `{"name":"t2-down-new"}`},
	} {
		if w := post(r.path, r.body); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), api.ProblemEntitlementLimit) {
			t.Errorf("POST %s after the downgrade: %d %s, want 403 entitlement-limit", r.path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}

// TestT2EntitlementQuotaFirstLoginWithInvitation covers ENT-03 and ENT-08
// for the users created without a principal, against PostgreSQL: at
// max_users (active users) the first login with a pending invitation and
// the acceptance of an invitation fail with entitlement-limit (POST
// /invitations/accept: 403); no user is created and the invitation stays
// open. A returning user still logs in. With the limit raised the same
// login is admitted.
func TestT2EntitlementQuotaFirstLoginWithInvitation(t *testing.T) {
	f := scopetest.Seed(t, "ed")
	bg := context.Background()
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	users := user.NewPGRepository(f.App).WithQuota(svc)
	inviter := f.AppUser(t, f.OrgA, "quota-inviter")
	f.AppUser(t, f.OrgA, "quota-member")
	var active int64
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM app_user WHERE organization_id = $1 AND is_active`, f.OrgA).Scan(&active); err != nil {
		t.Fatal(err)
	}
	setLimit := func(limit int64) {
		t.Helper()
		if _, err := svc.Grant(f.OrgCtx(f.OrgA), entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureCMDBCore,
			Plan: entitlement.PlanEssential, Enabled: true, Source: "manual", Limits: map[string]int64{entitlement.LimitMaxUsers: limit}}); err != nil {
			t.Fatal(err)
		}
	}
	setLimit(active)
	invite := func(email, tokenHash string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `
			INSERT INTO user_invitation (organization_id, email, role_id, scope_type, invited_by, token_hash, expires_at)
			SELECT $1, $2, id, 'org', $3, $4, now() + interval '7 days' FROM role WHERE organization_id = $1 AND name = 'viewer'
			RETURNING id::text`, f.OrgA, email, inviter, tokenHash).Scan(&id); err != nil {
			t.Fatalf("seed invitation: %v", err)
		}
		return id
	}
	open := func(invitation string) bool {
		t.Helper()
		var pending bool
		if err := f.Admin.QueryRow(bg, `SELECT accepted_at IS NULL FROM user_invitation WHERE id = $1`, invitation).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	}
	usersOf := func(email string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM app_user WHERE organization_id = $1 AND lower(email) = lower($2)`, f.OrgA, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	limitRefusal := func(err error) bool {
		var exceeded *entitlement.LimitExceededError
		return errors.As(err, &exceeded) && exceeded.ProblemType() == api.ProblemEntitlementLimit
	}

	// First login with an invitation at the limit.
	loginInvitation := invite("quota-login@a.invalid", "quota-login-hash")
	if _, err := users.EnsureUser(bg, f.OrgA, "scopetest-ed-quota-login", "quota-login@a.invalid", "Quota Login"); !limitRefusal(err) {
		t.Errorf("first login with an invitation at max_users: %v, want entitlement-limit", err)
	}
	if !open(loginInvitation) || usersOf("quota-login@a.invalid") != 0 {
		t.Errorf("refused first login: invitation open %v, users %d; want open and none", open(loginInvitation), usersOf("quota-login@a.invalid"))
	}

	// Acceptance through POST /invitations/accept at the limit.
	token, hash, err := user.NewInvitationToken(f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	acceptInvitation := invite("quota-accept@a.invalid", hash)
	mux := chi.NewRouter()
	user.NewInvitationHandler(user.NewPGInvitations(f.App)).WithQuota(svc).RegisterRoutes(mux)
	accept := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept",
			strings.NewReader(`{"token":"`+token+`","display_name":"Quota Accept"}`)))
		return w
	}
	if w := accept(); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), api.ProblemEntitlementLimit) {
		t.Errorf("acceptance at max_users: %d %s, want 403 entitlement-limit", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if !open(acceptInvitation) || usersOf("quota-accept@a.invalid") != 0 {
		t.Errorf("refused acceptance: invitation open %v, users %d; want open and none", open(acceptInvitation), usersOf("quota-accept@a.invalid"))
	}

	// The stock stays usable: a returning user logs in at the limit.
	if _, err = users.EnsureUser(bg, f.OrgA, "scopetest-ed-quota-member", "", ""); err != nil {
		t.Errorf("returning user at max_users: %v, want admitted", err)
	}

	// Raised by one: the login is admitted, the next acceptance refused.
	setLimit(active + 1)
	if _, err = users.EnsureUser(bg, f.OrgA, "scopetest-ed-quota-login", "quota-login@a.invalid", "Quota Login"); err != nil {
		t.Fatalf("first login with an invitation below max_users: %v", err)
	}
	if open(loginInvitation) || usersOf("quota-login@a.invalid") != 1 {
		t.Errorf("admitted first login: invitation open %v, users %d; want accepted and one", open(loginInvitation), usersOf("quota-login@a.invalid"))
	}
	if w := accept(); w.Code != http.StatusForbidden || !open(acceptInvitation) {
		t.Errorf("acceptance at the raised limit: %d, invitation open %v; want 403 and open", w.Code, open(acceptInvitation))
	}
	setLimit(active + 2)
	if w := accept(); w.Code != http.StatusCreated || open(acceptInvitation) || usersOf("quota-accept@a.invalid") != 1 {
		t.Errorf("acceptance below max_users: %d %s, invitation open %v; want 201 and accepted", w.Code, strings.TrimSpace(w.Body.String()), open(acceptInvitation))
	}
}
