package reviews_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Tickets from review items (TKT-01, WP-291) through the production router
// (server.NewRouter) against PostgreSQL: creation with links, scope,
// priority and description; one open ticket per item; the permissions,
// the module and the entitlement gate; the tickets on GET; the retention
// of items; the audit entry. It runs only against PostgreSQL
// (TEST_DATABASE_URL, see scopetest.Seed).

// rtEnv is the router of the test and its fixture.
type rtEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	router http.Handler
	user   string
}

// newRTEnv builds the production router with enforced entitlements and the
// late modules of spec ("all" or "" for none).
func newRTEnv(t *testing.T, f *scopetest.Fixture, user, modules string) *rtEnv {
	t.Helper()
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	set, err := server.ParseModules(modules)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-291", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		Modules: set, AuditPool: f.App,
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The stages of cmd/server: tenant, then the org gate of the
	// entitlements (feature-not-entitled), then the router.
	return &rtEnv{t: t, f: f, router: middleware.RequestID(middleware.TenantMiddleware(entitlements.Middleware(mux))), user: user}
}

var (
	allScope    = identity.ScopeSet{All: true}
	allRTPerms  = []identity.Permission{identity.PermDiscoveryRead, identity.PermReviewResolve, identity.PermTicketRead, identity.PermTicketWrite}
	orgWideRTSc = identity.Scope{Clients: allScope, Sites: allScope, Teams: allScope}
)

func (e *rtEnv) principal(scope identity.Scope, perms ...identity.Permission) identity.Principal {
	return identity.Principal{Subject: e.user, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Scope: &scope,
		Permissions: perms, Credential: "wp-291"}
}

func (e *rtEnv) do(p *identity.Principal, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			e.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))
	return w
}

// ticketOf decodes a created ticket.
type rtTicket struct {
	ID             string `json:"id"`
	TicketNumber   int    `json:"ticket_number"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Priority       string `json:"priority"`
	Category       string `json:"category"`
	TeamID         string `json:"team_id"`
	RelatedCIID    string `json:"related_ci_id"`
	RelatedAssetID string `json:"related_asset_id"`
}

func decodeTicket(t *testing.T, w *httptest.ResponseRecorder) rtTicket {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("create ticket: %d %s", w.Code, w.Body)
	}
	var tk rtTicket
	if err := json.Unmarshal(w.Body.Bytes(), &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func rtScalar[T any](t *testing.T, f *scopetest.Fixture, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := f.Admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func rtExec(t *testing.T, f *scopetest.Fixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// links returns "entity_type:entity_id" of the links of a ticket.
func rtLinks(t *testing.T, f *scopetest.Fixture, ticketID string) map[string]bool {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT entity_type || ':' ||
		COALESCE(ci_id::text, asset_id::text, review_item_id::text, '') FROM ticket_link WHERE ticket_id = $1`, ticketID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out[s] = true
	}
	return out
}

func TestTicketsFromReviewItems(t *testing.T) {
	f := scopetest.Seed(t, "c0")
	user := f.AppUser(t, f.OrgA, "wp291")
	rtExec(t, f, `INSERT INTO entitlement (organization_id, feature_key, enabled) VALUES ($1, 'ticketing_essential', true)`, f.OrgA)
	e := newRTEnv(t, f, user, "all")
	repo := reviews.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	n := 0
	open := func(t *testing.T, item *reviews.Item) *reviews.Item {
		t.Helper()
		n++
		item.OrganizationID = f.OrgA
		if item.ObjectKey == "" {
			item.ObjectKey = "wp291:" + strings.Repeat("x", n)
		}
		if item.Payload == nil {
			item.Payload = map[string]any{}
		}
		if err := repo.CreateReviewItem(orgCtx, item); err != nil {
			t.Fatalf("open %s: %v", item.Kind, err)
		}
		return item
	}
	full := e.principal(orgWideRTSc, allRTPerms...)
	ticketURL := func(id string) string { return "/api/v1/review-items/" + id + "/ticket" }

	t.Run("links, CI scope, priority, title and description", func(t *testing.T) {
		first := f.CI(t, f.OrgA, f.Client1, "wp291-first")
		second := f.CI(t, f.OrgA, f.Client1, "wp291-second")
		item := open(t, &reviews.Item{Kind: reviews.KindAmbiguousIdentity, CandidateCIIDs: []string{first, second},
			Payload: map[string]any{"name": "host-17", "raw_data": map[string]any{"secret": "x"}, "fingerprint": "fp-1",
				"device": map[string]any{"ip": "10.0.0.7", "raw_data": "nested"}}})
		before := rtScalar[int](t, f, `SELECT count(*)::int FROM audit_log WHERE organization_id = $1`, f.OrgA)
		tk := decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(item.ID), nil))
		if tk.Title != "ambiguous_identity: host-17" || tk.Priority != "high" || tk.Category != "task" || tk.Status != "new" {
			t.Errorf("ticket %+v", tk)
		}
		if tk.RelatedCIID != first {
			t.Errorf("related_ci_id %s, want the first candidate %s", tk.RelatedCIID, first)
		}
		if !strings.Contains(tk.Description, "object_key: "+item.ObjectKey) || !strings.Contains(tk.Description, "name: host-17") ||
			!strings.Contains(tk.Description, "10.0.0.7") {
			t.Errorf("description %q", tk.Description)
		}
		if strings.Contains(tk.Description, "raw_data") || strings.Contains(tk.Description, "fingerprint") || strings.Contains(tk.Description, "fp-1") {
			t.Errorf("description carries raw_data or fingerprint: %q", tk.Description)
		}
		if client := rtScalar[string](t, f, `SELECT COALESCE(client_id::text, '') FROM ticket WHERE id = $1`, tk.ID); client != f.Client1 {
			t.Errorf("ticket client %q, want the client of the first candidate %s", client, f.Client1)
		}
		links := rtLinks(t, f, tk.ID)
		for _, want := range []string{"ci:" + first, "ci:" + second, "review_item:" + item.ID} {
			if !links[want] {
				t.Errorf("link %s missing: %v", want, links)
			}
		}
		if len(links) != 3 {
			t.Errorf("links %v", links)
		}
		// The item stays open.
		if status := rtScalar[string](t, f, `SELECT status FROM review_item WHERE id = $1`, item.ID); status != reviews.StatusOpen {
			t.Errorf("item %s after the ticket", status)
		}
		// Exactly one audit entry: ticket.create with ticket and item; the
		// route writes none of its own.
		after := rtScalar[int](t, f, `SELECT count(*)::int FROM audit_log WHERE organization_id = $1`, f.OrgA)
		if after-before != 1 {
			t.Errorf("%d audit entries, want 1", after-before)
		}
		origin := rtScalar[string](t, f, `SELECT changes->>'origin_id' FROM audit_log
			WHERE organization_id = $1 AND action = 'ticket.create' AND resource_id = $2`, f.OrgA, tk.ID)
		if origin != item.ID {
			t.Errorf("audit origin %q, want %s", origin, item.ID)
		}

		// The tickets on GET and the list.
		var got reviews.Item
		w := e.do(&full, http.MethodGet, "/api/v1/review-items/"+item.ID, nil)
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
			t.Fatalf("get: %d %s", w.Code, w.Body)
		}
		if len(got.Tickets) != 1 || got.Tickets[0].ID != tk.ID || got.Tickets[0].TicketNumber != tk.TicketNumber || got.Tickets[0].Status != "new" {
			t.Errorf("tickets on GET %+v", got.Tickets)
		}
		var page api.ListResponse[reviews.Item]
		w = e.do(&full, http.MethodGet, "/api/v1/review-items?kind=ambiguous_identity&limit=100", nil)
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		found := false
		for i := range page.Data {
			if page.Data[i].ID == item.ID {
				found = len(page.Data[i].Tickets) == 1 && page.Data[i].Tickets[0].ID == tk.ID
			}
		}
		if !found {
			t.Errorf("list does not name the ticket of the item")
		}
		// Without ticket:read the field is absent.
		noRead := e.principal(orgWideRTSc, identity.PermDiscoveryRead)
		w = e.do(&noRead, http.MethodGet, "/api/v1/review-items/"+item.ID, nil)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"tickets"`) {
			t.Errorf("without ticket:read: %d %s", w.Code, w.Body)
		}

		// A second ticket is 409 naming the open one.
		w = e.do(&full, http.MethodPost, ticketURL(item.ID), nil)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), tk.ID) {
			t.Errorf("second ticket: %d %s", w.Code, w.Body)
		}
	})

	t.Run("serial_mismatch links the asset", func(t *testing.T) {
		ciID := f.CI(t, f.OrgA, f.Client1, "wp291-serial")
		assetID := f.Asset(t, f.OrgA, f.Client1, "wp291-serial-asset")
		item := open(t, &reviews.Item{Kind: reviews.KindSerialMismatch, CandidateCIIDs: []string{ciID},
			Payload: map[string]any{"ci_id": ciID, "asset_id": assetID, "ci_serial_number": "A", "asset_serial_number": "B"}})
		tk := decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(item.ID), nil))
		if tk.RelatedAssetID != assetID || tk.RelatedCIID != ciID || tk.Priority != "high" {
			t.Errorf("ticket %+v", tk)
		}
		if tk.Title != "serial_mismatch: wp291-serial" {
			t.Errorf("title %q, want the CI name", tk.Title)
		}
		if links := rtLinks(t, f, tk.ID); !links["asset:"+assetID] || !links["ci:"+ciID] || len(links) != 3 {
			t.Errorf("links %v", links)
		}
	})

	t.Run("priority per type and from the body", func(t *testing.T) {
		scope := open(t, &reviews.Item{Kind: reviews.KindScopeSuggested, Payload: map[string]any{"cidr": "10.9.0.0/24"}})
		tk := decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(scope.ID), nil))
		if tk.Priority != "low" || tk.Title != "scope_suggested: 10.9.0.0/24" || tk.RelatedCIID != "" {
			t.Errorf("scope_suggested ticket %+v", tk)
		}
		if links := rtLinks(t, f, tk.ID); len(links) != 1 || !links["review_item:"+scope.ID] {
			t.Errorf("links of an item without candidates %v", links)
		}
		conflict := open(t, &reviews.Item{Kind: reviews.KindConflictingValues,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-conflict")}})
		tk = decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(conflict.ID), nil))
		if tk.Priority != "medium" {
			t.Errorf("conflicting_values priority %s", tk.Priority)
		}
		dup := open(t, &reviews.Item{Kind: reviews.KindProbableDuplicate,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-dup")}})
		tk = decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(dup.ID), map[string]any{"priority": "critical"}))
		if tk.Priority != "critical" {
			t.Errorf("priority of the body: %s", tk.Priority)
		}
		bad := open(t, &reviews.Item{Kind: reviews.KindProbableDuplicate,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-bad")}})
		if w := e.do(&full, http.MethodPost, ticketURL(bad.ID), map[string]any{"team_id": f.ID()}); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("unknown team: %d %s", w.Code, w.Body)
		}
	})

	t.Run("parallel requests create one ticket", func(t *testing.T) {
		item := open(t, &reviews.Item{Kind: reviews.KindDuplicateIP,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-parallel")}})
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = e.do(&full, http.MethodPost, ticketURL(item.ID), nil).Code
			}(i)
		}
		wg.Wait()
		if codes[0]+codes[1] != http.StatusCreated+http.StatusConflict {
			t.Errorf("codes %v, want one 201 and one 409", codes)
		}
		if n := rtScalar[int](t, f, `SELECT count(*)::int FROM ticket_link WHERE review_item_id = $1`, item.ID); n != 1 {
			t.Errorf("%d tickets for the item", n)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		item := open(t, &reviews.Item{Kind: reviews.KindResurrectedDevice,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client2, "wp291-refused")}})
		noResolve := e.principal(orgWideRTSc, identity.PermDiscoveryRead, identity.PermTicketWrite)
		if w := e.do(&noResolve, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusForbidden {
			t.Errorf("without review:resolve: %d %s", w.Code, w.Body)
		}
		noWrite := e.principal(orgWideRTSc, identity.PermDiscoveryRead, identity.PermReviewResolve)
		if w := e.do(&noWrite, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusForbidden {
			t.Errorf("without ticket:write: %d %s", w.Code, w.Body)
		}
		client1 := e.principal(identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: allScope, Teams: allScope}, allRTPerms...)
		if w := e.do(&client1, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusNotFound {
			t.Errorf("item of another client: %d %s", w.Code, w.Body)
		}
		if w := e.do(&full, http.MethodPost, ticketURL(f.ID()), nil); w.Code != http.StatusNotFound {
			t.Errorf("unknown item: %d %s", w.Code, w.Body)
		}
		// An item that is no longer open.
		rtExec(t, f, `UPDATE review_item SET status = 'dismissed', action = 'dismiss', resolution = 'dismiss', resolved_at = now() WHERE id = $1`, item.ID)
		if w := e.do(&full, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusConflict {
			t.Errorf("dismissed item: %d %s", w.Code, w.Body)
		}
	})

	t.Run("restricted principals and items without candidates", func(t *testing.T) {
		team := rtScalar[string](t, f, `INSERT INTO team (organization_id, name) VALUES ($1, 'wp291 team') RETURNING id::text`, f.OrgA)
		item := open(t, &reviews.Item{Kind: reviews.KindUnclassifiedDevice, ClientID: f.Client1,
			Payload: map[string]any{"name": "printer-3"}})
		client1 := e.principal(identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: allScope, Teams: allScope}, allRTPerms...)
		if w := e.do(&client1, http.MethodGet, "/api/v1/review-items/"+item.ID, nil); w.Code != http.StatusOK {
			t.Fatalf("the client-restricted principal sees the item: %d %s", w.Code, w.Body)
		}
		if w := e.do(&client1, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusForbidden {
			t.Errorf("client-restricted, item without candidates: %d %s", w.Code, w.Body)
		}
		teamed := e.principal(identity.Scope{Clients: allScope, Sites: allScope, Teams: identity.ScopeSet{IDs: []string{team}}}, allRTPerms...)
		if w := e.do(&teamed, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusForbidden {
			t.Errorf("team-restricted without team_id: %d %s", w.Code, w.Body)
		}
		tk := decodeTicket(t, e.do(&teamed, http.MethodPost, ticketURL(item.ID), map[string]any{"team_id": team}))
		if tk.TeamID != team || tk.Priority != "medium" {
			t.Errorf("team ticket %+v", tk)
		}
	})

	t.Run("retention keeps the ticket", func(t *testing.T) {
		item := open(t, &reviews.Item{Kind: reviews.KindUnknownRelationship,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-retained")}})
		tk := decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(item.ID), nil))
		rtExec(t, f, `UPDATE review_item SET status = 'dismissed', action = 'dismiss', resolution = 'dismiss',
			resolved_at = '2001-01-01T00:00:00Z' WHERE id = $1`, item.ID)
		clock := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
		retention := jobs.NewRetention(f.App, jobs.RetentionPolicy{ReviewItems: 24 * time.Hour}).WithClock(func() time.Time { return clock })
		if err := retention.RunReviewItems(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := rtScalar[int](t, f, `SELECT count(*)::int FROM review_item WHERE id = $1`, item.ID); n != 0 {
			t.Fatalf("the item survived the retention")
		}
		if n := rtScalar[int](t, f, `SELECT count(*)::int FROM ticket WHERE id = $1`, tk.ID); n != 1 {
			t.Errorf("the ticket went with the item")
		}
		if links := rtLinks(t, f, tk.ID); links["review_item:"+item.ID] || len(links) != 1 {
			t.Errorf("links after the retention %v", links)
		}
	})

	t.Run("module off: 404", func(t *testing.T) {
		item := open(t, &reviews.Item{Kind: reviews.KindProbableDuplicate,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-module")}})
		off := newRTEnv(t, f, user, "")
		if w := off.do(&full, http.MethodPost, ticketURL(item.ID), nil); w.Code != http.StatusNotFound {
			t.Errorf("module off: %d %s", w.Code, w.Body)
		}
		// The queue answers without tickets.
		if w := off.do(&full, http.MethodGet, "/api/v1/review-items/"+item.ID, nil); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"tickets"`) {
			t.Errorf("GET with the module off: %d %s", w.Code, w.Body)
		}
	})

	t.Run("without ticketing_essential: 403 feature-not-entitled", func(t *testing.T) {
		item := open(t, &reviews.Item{Kind: reviews.KindProbableDuplicate,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-entitled")}})
		decodeTicket(t, e.do(&full, http.MethodPost, ticketURL(item.ID), nil))
		other := open(t, &reviews.Item{Kind: reviews.KindProbableDuplicate,
			CandidateCIIDs: []string{f.CI(t, f.OrgA, f.Client1, "wp291-unentitled")}})
		rtExec(t, f, `DELETE FROM entitlement WHERE organization_id = $1 AND feature_key = 'ticketing_essential'`, f.OrgA)
		plain := newRTEnv(t, f, user, "all")
		w := plain.do(&full, http.MethodPost, ticketURL(other.ID), nil)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") {
			t.Errorf("without the entitlement: %d %s", w.Code, w.Body)
		}
		// GET stays ungated and names no tickets.
		w = plain.do(&full, http.MethodGet, "/api/v1/review-items/"+item.ID, nil)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"tickets"`) {
			t.Errorf("GET without the entitlement: %d %s", w.Code, w.Body)
		}
	})
}

// countingTickets counts the queries for the tickets of items.
type countingTickets struct {
	calls int
}

func (c *countingTickets) CreateTicket(context.Context, *reviews.NewTicket) (any, error) {
	return nil, nil
}

func (c *countingTickets) LinkedTickets(_ context.Context, _ string, ids []string) (map[string][]reviews.TicketRef, error) {
	c.calls++
	out := map[string][]reviews.TicketRef{}
	for _, id := range ids {
		out[id] = []reviews.TicketRef{{ID: "t-" + id, TicketNumber: 1, Status: "new"}}
	}
	return out, nil
}

func (c *countingTickets) CIName(context.Context, string, string) (string, error) { return "", nil }

// TestReviewItemListQueriesTicketsOncePerPage: the list asks for the
// tickets of its page with one query, not one per item.
func TestReviewItemListQueriesTicketsOncePerPage(t *testing.T) {
	repo := reviews.NewMemoryRepository()
	counter := &countingTickets{}
	svc := reviews.NewService(repo).WithTickets(counter)
	ctx := context.Background()
	for i := range 5 {
		item := &reviews.Item{OrganizationID: "org-1", Kind: reviews.KindDuplicateIP, Status: reviews.StatusOpen,
			ObjectKey: "k" + strings.Repeat("x", i), Payload: map[string]any{}}
		if err := repo.CreateReviewItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	reviews.NewHandler(svc).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/review-items", nil)
	req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var page api.ListResponse[reviews.Item]
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if len(page.Data) != 5 || counter.calls != 1 {
		t.Fatalf("%d items, %d ticket queries; want 5 items and 1 query", len(page.Data), counter.calls)
	}
	for i := range page.Data {
		if len(page.Data[i].Tickets) != 1 {
			t.Errorf("item %s without its ticket", page.Data[i].ID)
		}
	}
}
