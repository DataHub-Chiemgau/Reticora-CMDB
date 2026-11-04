package discovery_test

// The ticket comment on the resolution of a review item (TKT-01, WP-767)
// through the production router (server.NewRouter) against PostgreSQL: a
// resolution (merge) or dismissal of an item with a linked ticket that is
// not closed writes exactly one internal comment by the resolving
// principal; the self-persisting effect of unlicensed_ci with new writes
// one; the adoption after max_cis was raised writes a system comment
// without author, also in the context of an operator's request. Without a
// ticket, with a closed ticket and with a ticket the resolver does not see
// there is no comment, and the resolution still succeeds.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestReviewResolutionCommentsTicket$' ./internal/discovery/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// wp767Comment is a stored ticket comment.
type wp767Comment struct {
	authorType, authorID, content string
	internal                      bool
}

func TestReviewResolutionCommentsTicket(t *testing.T) {
	f := scopetest.Seed(t, "c7")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp767")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	exec(`INSERT INTO entitlement (organization_id, feature_key, enabled) VALUES ($1, 'ticketing_essential', true)`, f.OrgA)

	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	modules, err := server.ParseModules("all")
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-767", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		Modules: modules, AuditPool: f.App,
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.RequestID(middleware.TenantMiddleware(entitlements.Middleware(mux)))

	all := identity.ScopeSet{All: true}
	perms := []identity.Permission{identity.PermDiscoveryRead, identity.PermReviewResolve, identity.PermTicketRead, identity.PermTicketWrite}
	principal := func(scope identity.Scope) identity.Principal {
		return identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Scope: &scope,
			Permissions: perms, Credential: "wp-767"}
	}
	full := principal(identity.Scope{Clients: all, Sites: all, Teams: all})
	do := func(p *identity.Principal, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if encErr := json.NewEncoder(&buf).Encode(body); encErr != nil {
				t.Fatal(encErr)
			}
		}
		req := httptest.NewRequest(http.MethodPost, path, &buf)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	openTicket := func(itemID string, body any) string {
		t.Helper()
		w := do(&full, "/api/v1/review-items/"+itemID+"/ticket", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("ticket for %s: %d %s", itemID, w.Code, w.Body)
		}
		var tk struct {
			ID string `json:"id"`
		}
		if decErr := json.Unmarshal(w.Body.Bytes(), &tk); decErr != nil {
			t.Fatal(decErr)
		}
		return tk.ID
	}
	resolve := func(p *identity.Principal, itemID string, body any) {
		t.Helper()
		if w := do(p, "/api/v1/review-items/"+itemID+"/resolve", body); w.Code != http.StatusOK {
			t.Fatalf("resolve %s: %d %s", itemID, w.Code, w.Body)
		}
	}
	comments := func(ticketID string) []wp767Comment {
		t.Helper()
		rows, qErr := f.Admin.Query(bg, `SELECT author_type, COALESCE(author_id::text, ''), content, is_internal
			FROM ticket_comment WHERE ticket_id = $1 ORDER BY created_at`, ticketID)
		if qErr != nil {
			t.Fatal(qErr)
		}
		defer rows.Close()
		var out []wp767Comment
		for rows.Next() {
			var c wp767Comment
			if qErr = rows.Scan(&c.authorType, &c.authorID, &c.content, &c.internal); qErr != nil {
				t.Fatal(qErr)
			}
			out = append(out, c)
		}
		return out
	}
	// one checks the single internal comment of a ticket.
	one := func(t *testing.T, ticketID, authorType, authorID string, parts ...string) {
		t.Helper()
		got := comments(ticketID)
		if len(got) != 1 {
			t.Fatalf("comments of %s: %+v, want exactly one", ticketID, got)
		}
		c := got[0]
		if c.authorType != authorType || c.authorID != authorID || !c.internal {
			t.Errorf("comment %+v, want internal by %s %q", c, authorType, authorID)
		}
		for _, part := range parts {
			if !strings.Contains(c.content, part) {
				t.Errorf("comment %q, want it to name %q", c.content, part)
			}
		}
	}

	repo := reviews.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	n := 0
	open := func(kind string, candidates ...string) *reviews.Item {
		t.Helper()
		n++
		item := &reviews.Item{OrganizationID: f.OrgA, Kind: kind, Status: reviews.StatusOpen, CandidateCIIDs: candidates,
			ObjectKey: "wp767:" + strings.Repeat("x", n), Payload: map[string]any{"name": "wp767-host"}}
		if cErr := repo.CreateReviewItem(orgCtx, item); cErr != nil {
			t.Fatalf("open %s: %v", kind, cErr)
		}
		return item
	}
	serverType := str(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
	held := func(name string) string {
		return str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'unlicensed_ci', 'open',
			jsonb_build_object('ci_type_id', $2::text, 'ci_type_name', 'server', 'source', 'sweep', 'name', $3::text, 'device',
				jsonb_build_object('name', $3::text, 'serial_number', upper($3::text))))
			RETURNING id::text`, f.OrgA, serverType, name)
	}
	operatorCtx := tenant.WithTenant(orgCtx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	grantMaxCIs := func(limit int64) {
		t.Helper()
		if _, gErr := entitlements.Grant(operatorCtx, &entitlement.Entitlement{OrganizationID: f.OrgA,
			FeatureKey: entitlement.FeatureCMDBCore, Plan: entitlement.PlanEssential, Enabled: true,
			Limits: map[string]int64{entitlement.LimitMaxCIs: limit}}); gErr != nil {
			t.Fatal(gErr)
		}
	}
	grantMaxCIs(100000)

	t.Run("merge: one comment by the resolver", func(t *testing.T) {
		candidate := f.CI(t, f.OrgA, f.Client1, "wp767-merge")
		item := open(reviews.KindAmbiguousIdentity, candidate)
		ticketID := openTicket(item.ID, nil)
		resolve(&full, item.ID, map[string]any{"action": "merge", "ci_id": candidate})
		one(t, ticketID, "user", user, "Type: ambiguous_identity", "Action: merge", "Status: resolved", "Note: merged into "+candidate)
	})

	t.Run("dismiss: one comment by the resolver", func(t *testing.T) {
		item := open(reviews.KindAmbiguousIdentity, f.CI(t, f.OrgA, f.Client1, "wp767-dismiss"))
		ticketID := openTicket(item.ID, nil)
		resolve(&full, item.ID, map[string]any{"action": "dismiss", "resolution": "not a device of ours"})
		one(t, ticketID, "user", user, "Action: dismiss", "Status: dismissed", "Note: not a device of ours")
	})

	t.Run("a service account resolver writes a system comment", func(t *testing.T) {
		account := str(`INSERT INTO service_account (organization_id, name) VALUES ($1, 'wp767-account') RETURNING id::text`, f.OrgA)
		if _, gErr := entitlements.Grant(operatorCtx, &entitlement.Entitlement{OrganizationID: f.OrgA,
			FeatureKey: entitlement.FeatureAPIAccess, Plan: entitlement.PlanEssential, Enabled: true}); gErr != nil {
			t.Fatal(gErr)
		}
		sa := full
		sa.Subject, sa.ServiceAccountID, sa.Type, sa.Credential = account, account, identity.PrincipalTypeAPIKey, "apikey:wp767"
		item := open(reviews.KindAmbiguousIdentity, f.CI(t, f.OrgA, f.Client1, "wp767-account"))
		ticketID := openTicket(item.ID, nil)
		resolve(&sa, item.ID, map[string]any{"action": "dismiss", "resolution": "by automation"})
		one(t, ticketID, "system", "", "Action: dismiss", "Note: by automation")
	})

	t.Run("unlicensed_ci new: the self-persisting effect writes one comment", func(t *testing.T) {
		itemID := held("wp767-new")
		ticketID := openTicket(itemID, nil)
		resolve(&full, itemID, map[string]any{"action": "new"})
		if s := str(`SELECT status FROM review_item WHERE id = $1`, itemID); s != reviews.StatusResolved {
			t.Fatalf("item after new: %s, want resolved", s)
		}
		one(t, ticketID, "user", user, "Type: unlicensed_ci", "Action: new", "Status: resolved", "within max_cis")
	})

	t.Run("no comment without a ticket or with a closed one", func(t *testing.T) {
		without := open(reviews.KindAmbiguousIdentity, f.CI(t, f.OrgA, f.Client1, "wp767-none"))
		before := str(`SELECT count(*)::text FROM ticket_comment WHERE organization_id = $1`, f.OrgA)
		resolve(&full, without.ID, map[string]any{"action": "dismiss"})
		if after := str(`SELECT count(*)::text FROM ticket_comment WHERE organization_id = $1`, f.OrgA); after != before {
			t.Errorf("comments %s → %s, want none for an item without a ticket", before, after)
		}
		closed := open(reviews.KindAmbiguousIdentity, f.CI(t, f.OrgA, f.Client1, "wp767-closed"))
		ticketID := openTicket(closed.ID, nil)
		exec(`UPDATE ticket SET status = 'closed' WHERE id = $1`, ticketID)
		resolve(&full, closed.ID, map[string]any{"action": "dismiss"})
		if got := comments(ticketID); len(got) != 0 {
			t.Errorf("comments of a closed ticket: %+v, want none", got)
		}
	})

	t.Run("a ticket the resolver does not see gets no comment, the resolution holds", func(t *testing.T) {
		teamA := str(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp767 team a') RETURNING id::text`, f.OrgA)
		teamB := str(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp767 team b') RETURNING id::text`, f.OrgA)
		item := open(reviews.KindAmbiguousIdentity, f.CI(t, f.OrgA, f.Client1, "wp767-hidden"))
		ticketID := openTicket(item.ID, map[string]any{"team_id": teamA})
		other := principal(identity.Scope{Clients: all, Sites: all, Teams: identity.ScopeSet{IDs: []string{teamB}}})
		resolve(&other, item.ID, map[string]any{"action": "dismiss"})
		if s := str(`SELECT status FROM review_item WHERE id = $1`, item.ID); s != reviews.StatusDismissed {
			t.Errorf("item: %s, want dismissed", s)
		}
		if got := comments(ticketID); len(got) != 0 {
			t.Errorf("comments of a ticket the resolver does not see: %+v, want none", got)
		}
	})

	t.Run("adoption after max_cis was raised: a system comment without author", func(t *testing.T) {
		current, cErr := ci.CountActive(orgCtx, repos.CI, f.OrgA)
		if cErr != nil {
			t.Fatal(cErr)
		}
		grantMaxCIs(current)
		itemID := held("wp767-adopt")
		ticketID := openTicket(itemID, nil)
		// The operator's request carries a user; the adoption has no actor.
		grantMaxCIs(current + 10)
		if s := str(`SELECT status FROM review_item WHERE id = $1`, itemID); s != reviews.StatusResolved {
			t.Fatalf("held item after the grant: %s, want resolved", s)
		}
		one(t, ticketID, "system", "", "Type: unlicensed_ci", "Action: new", "Status: resolved", "within max_cis")
	})
}
