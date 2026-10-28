package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	return r.WithContext(orgWideCtx(r.Context(), "org-1", ""))
}

// orgWideCtx attaches the tenant context of an org-wide principal of orgID,
// as the tenant middleware does: the tenant info and the tenant scope.
func orgWideCtx(ctx context.Context, orgID, userID string) context.Context {
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID, UserID: userID})
	scope := database.OrgWideScope(orgID, userID)
	return database.ContextWithTenantScope(ctx, &scope)
}

func TestHandler_CreateAndList(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"name":"My Hook","url":"https://example.com/hook","secret":"s3cr3t","events":["ci.created","ci.updated"]}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Subscription
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Name != "My Hook" {
		t.Errorf("expected My Hook, got %s", created.Name)
	}
	if !created.IsActive {
		t.Error("expected is_active to be true")
	}

	// List
	req = httptest.NewRequest("GET", "/api/v1/webhooks", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Subscription `json:"data"`
		Total int            `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestHandler_ListDeadLetters(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()

	if err := repo.Create(context.Background(), &Subscription{
		OrganizationID: "org-1", Name: "hook", URL: "https://example.com/hook",
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(repo, nil, DispatcherOptions{
		Deliveries:   store,
		MaxAttempts:  1,
		BaseBackoff:  time.Millisecond,
		PollInterval: -1,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}()

	h := NewHandler(repo, dispatcher)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Empty queue initially.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data  []DeadLetter `json:"data"`
		Total int          `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Fatalf("expected empty dead-letter queue, got %d", resp.Total)
	}

	// Dispatch to an unreachable endpoint and let the attempt exhaust.
	dispatcher.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil)))
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Total == 1 {
			if resp.Data[0].Event != "ci.created" || resp.Data[0].Attempts != 1 {
				t.Fatalf("unexpected dead letter: %+v", resp.Data[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("dead letter never surfaced on the API")
}

func TestHandler_ListDeadLetters_Unauthorized(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// No tenant context: the handler checks the tenant before touching the
	// (here deliberately absent) dead-letter store, so this exercises the 401
	// path rather than the 503 path.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandler_ListDeadLetters_NoStore(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo) // no dispatcher: dead-letter listing unavailable
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil)))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandler_InvalidEvent(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"name":"Bad","url":"https://example.com","secret":"x","events":["bad.event"]}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	sub := &Subscription{
		OrganizationID: "org-1",
		Name:           "to-delete",
		URL:            "https://example.com",
		Secret:         "secret",
		Events:         []string{"ci.created"},
		IsActive:       true,
	}
	repo.Create(context.Background(), sub)

	req := httptest.NewRequest("DELETE", "/api/v1/webhooks/"+sub.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestHandler_Test_DeliversSignedPing(t *testing.T) {
	var (
		gotEvent     string
		gotSignature string
		gotBody      []byte
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvent = r.Header.Get("X-Webhook-Event")
		gotSignature = r.Header.Get("X-Webhook-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	repo := NewMemoryRepository()
	sub := &Subscription{
		OrganizationID: "org-1",
		Name:           "Ping",
		URL:            target.URL,
		Secret:         "s3cr3t",
		Events:         []string{"ci.created"},
		IsActive:       true,
	}
	if err := repo.Create(context.Background(), sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	dispatcher := NewDispatcher(repo, target.Client())
	defer dispatcher.Shutdown(context.Background())

	mux := chi.NewRouter()
	NewHandler(repo, dispatcher).RegisterRoutes(mux)

	req := tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks/"+sub.ID+"/test", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var delivery Delivery
	if err := json.Unmarshal(w.Body.Bytes(), &delivery); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	if !delivery.Success || delivery.StatusCode != http.StatusOK {
		t.Errorf("expected a successful delivery, got %+v", delivery)
	}
	if gotEvent != TestEvent {
		t.Errorf("expected event %q, got %q", TestEvent, gotEvent)
	}
	if want := signPayload(sub.Secret, gotBody); gotSignature != want {
		t.Errorf("expected signature %q, got %q", want, gotSignature)
	}
}

func TestHandler_Test_UnknownSubscription(t *testing.T) {
	repo := NewMemoryRepository()
	dispatcher := NewDispatcher(repo, nil)
	defer dispatcher.Shutdown(context.Background())

	mux := chi.NewRouter()
	NewHandler(repo, dispatcher).RegisterRoutes(mux)

	req := tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks/does-not-exist/test", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandler_HeadersAreWriteOnly covers WP-047 (SEC-01): custom header
// values (authentication) are masked in create, get and list responses, while
// the stored subscription keeps them for delivery.
func TestHandler_HeadersAreWriteOnly(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	body := `{"name":"Hook","url":"https://example.com/hook","secret":"s","events":["ci.created"],"headers":{"Authorization":"Bearer hdr-s3cret"}}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created Subscription
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	responses := []string{w.Body.String()}
	for _, path := range []string{"/api/v1/webhooks", "/api/v1/webhooks/" + created.ID} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", path, nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
		responses = append(responses, w.Body.String())
	}
	for _, out := range responses {
		if bytes.Contains([]byte(out), []byte("hdr-s3cret")) || !bytes.Contains([]byte(out), []byte(`"Authorization":"***"`)) {
			t.Errorf("response does not mask the header: %s", out)
		}
	}
	stored, err := repo.GetByID(context.Background(), "org-1", created.ID)
	if err != nil || stored.Headers["Authorization"] != "Bearer hdr-s3cret" {
		t.Errorf("stored header %v, %v; want the real value for delivery", stored, err)
	}
}

// TestHandler_RejectsInternalDestinations covers WP-049 (SEC-08): subscriber
// URLs are validated when stored; the on-premises policy admits private
// targets but never metadata endpoints.
func TestHandler_RejectsInternalDestinations(t *testing.T) {
	create := func(h *Handler, url string) int {
		t.Helper()
		mux := chi.NewRouter()
		h.RegisterRoutes(mux)
		body := `{"name":"Hook","url":"` + url + `","secret":"s","events":["ci.created"]}`
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))))
		return w.Code
	}
	strict := NewHandler(NewMemoryRepository())
	for _, url := range []string{"http://127.0.0.1:8080/hook", "http://169.254.169.254/latest/", "http://localhost/hook", "ftp://example.com/x", "http://[::1]/"} {
		if code := create(strict, url); code != http.StatusBadRequest {
			t.Errorf("create %s: status %d, want 400", url, code)
		}
	}
	if code := create(strict, "https://hooks.example.com/x"); code != http.StatusCreated {
		t.Errorf("public destination: status %d, want 201", code)
	}
	onPrem := NewHandler(NewMemoryRepository()).WithEgress(egress.Options{AllowPrivate: true})
	if code := create(onPrem, "http://10.0.0.5/hook"); code != http.StatusCreated {
		t.Errorf("on-prem private destination: status %d, want 201", code)
	}
	if code := create(onPrem, "http://169.254.169.254/latest/"); code != http.StatusBadRequest {
		t.Errorf("on-prem metadata destination: status %d, want 400", code)
	}
}

// TestHandlerHidesSubscriptionsOutsideTheScope covers E-98 (WP-601,
// TEN-09) in the handler, independent of row level security (memory
// profile): a subscription created by a client-1 principal carries its
// snapshot; a client-2 principal does not list it, every route with its id
// answers 404 as for an unknown id, its dead letter is not listed and
// nothing changes; a request without tenant scope is 401. The creator
// manages it.
func TestHandlerHidesSubscriptionsOutsideTheScope(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()
	d := NewDispatcher(repo, nil, DispatcherOptions{Deliveries: store, PollInterval: -1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}()
	mux := chi.NewRouter()
	NewHandler(repo, d).RegisterRoutes(mux)
	scoped := func(clients ...string) context.Context {
		ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u-1"})
		scope := database.OrgWideScope("org-1", "u-1")
		scope.Clients = database.ScopeIDs(clients...)
		return database.ContextWithTenantScope(ctx, &scope)
	}
	call := func(ctx context.Context, method, path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w.Code, w.Body.String()
	}
	client1, client2 := scoped("c1"), scoped("c2")
	code, body := call(client1, http.MethodPost, "/api/v1/webhooks", `{"name":"c1","url":"https://hooks.example.com/c1","secret":"s","events":["ci.created"]}`)
	var created Subscription
	if err := json.Unmarshal([]byte(body), &created); err != nil || code != http.StatusCreated {
		t.Fatalf("create as client 1: %d %s", code, body)
	}
	stored, err := repo.GetByID(context.Background(), "org-1", created.ID)
	if err != nil || stored.Scope == nil || len(stored.Scope.Clients) != 1 || stored.Scope.Clients[0] != "c1" || stored.Scope.Sites != nil {
		t.Fatalf("stored snapshot: %+v (%v)", stored, err)
	}
	if strings.Contains(body, "c1\"]") || strings.Contains(body, "scope") {
		t.Errorf("the response shows the snapshot: %s", body)
	}
	rec, err := store.Enqueue(context.Background(), DeliveryRecord{OrganizationID: "org-1", SubscriptionID: created.ID, Event: "ci.created", Status: StatusFailed})
	if err != nil {
		t.Fatal(err)
	}
	rec.Attempt = 6
	if _, err = store.MoveToDeadLetter(context.Background(), &rec, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if code, body = call(client2, http.MethodGet, "/api/v1/webhooks", ""); code != http.StatusOK || strings.Contains(body, created.ID) {
		t.Errorf("list as client 2: %d %s", code, body)
	}
	if code, body = call(client2, http.MethodGet, "/api/v1/webhooks/dead-letters", ""); code != http.StatusOK || strings.Contains(body, created.ID) {
		t.Errorf("dead letters as client 2: %d %s", code, body)
	}
	const unknown = "99999999-0000-0000-0000-000000000099"
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/webhooks/%s", ""},
		{http.MethodDelete, "/api/v1/webhooks/%s", ""},
		{http.MethodPost, "/api/v1/webhooks/%s/test", ""},
		{http.MethodPost, "/api/v1/webhooks/%s/rotate-secret", `{"secret":"rotated-secret-123456"}`},
		{http.MethodGet, "/api/v1/webhooks/%s/deliveries", ""},
		{http.MethodPost, "/api/v1/webhooks/%s/deliveries/" + rec.ID + "/retry", ""},
	} {
		foreignCode, foreignBody := call(client2, route.method, fmt.Sprintf(route.path, created.ID), route.body)
		unknownCode, unknownBody := call(client2, route.method, fmt.Sprintf(route.path, unknown), route.body)
		if foreignCode != http.StatusNotFound || unknownCode != http.StatusNotFound || !strings.Contains(foreignBody, "webhook not found") ||
			!strings.Contains(unknownBody, "webhook not found") {
			t.Errorf("%s %s as client 2: %d %s; unknown id: %d %s", route.method, route.path, foreignCode, foreignBody, unknownCode, unknownBody)
		}
		if code, body = call(context.Background(), route.method, fmt.Sprintf(route.path, created.ID), route.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s without tenant scope: %d %s", route.method, route.path, code, body)
		}
	}
	if stored, err = repo.GetByID(context.Background(), "org-1", created.ID); err != nil || stored.Secret != "s" {
		t.Errorf("after the refused requests: %+v (%v)", stored, err)
	}

	if code, body = call(client1, http.MethodGet, "/api/v1/webhooks/dead-letters", ""); code != http.StatusOK || !strings.Contains(body, created.ID) {
		t.Errorf("dead letters as client 1: %d %s", code, body)
	}
	if code, body = call(client1, http.MethodPost, "/api/v1/webhooks/"+created.ID+"/deliveries/"+rec.ID+"/retry", ""); code != http.StatusAccepted {
		t.Errorf("retry as client 1: %d %s", code, body)
	}
	if code, body = call(client1, http.MethodDelete, "/api/v1/webhooks/"+created.ID, ""); code != http.StatusNoContent {
		t.Errorf("delete as client 1: %d %s", code, body)
	}
}

// TestSubscriptionVisibleTo pins the rule of webhook_sub_isolation in Go
// (migration 000155): per dimension the principal is org-wide or the
// snapshot lies completely in its scope; without snapshot only org-wide
// principals; never another organization.
func TestSubscriptionVisibleTo(t *testing.T) {
	scope := func(org string, clients, sites, teams []string) *database.TenantScope {
		s := database.OrgWideScope(org, "")
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		if teams != nil {
			s.Teams = database.ScopeIDs(teams...)
		}
		return &s
	}
	ids := func(v ...string) []string { return append([]string{}, v...) }
	for _, c := range []struct {
		name     string
		snapshot *Scope
		scope    *database.TenantScope
		want     bool
	}{
		{"no snapshot, org-wide", nil, scope("o1", nil, nil, nil), true},
		{"no snapshot, client 1", nil, scope("o1", ids("c1"), nil, nil), false},
		{"no snapshot, team", nil, scope("o1", nil, nil, ids("t1")), false},
		{"org-wide snapshot, client 1", &Scope{}, scope("o1", ids("c1"), nil, nil), false},
		{"client 1, client 1", &Scope{Clients: ids("C1")}, scope("o1", ids("c1"), nil, nil), true},
		{"client 1, clients 1 and 2", &Scope{Clients: ids("c1")}, scope("o1", ids("c1", "c2"), nil, nil), true},
		{"clients 1 and 2, client 1", &Scope{Clients: ids("c1", "c2")}, scope("o1", ids("c1"), nil, nil), false},
		{"client 1, client 2", &Scope{Clients: ids("c1")}, scope("o1", ids("c2"), nil, nil), false},
		{"client 1, site 1 of client 1", &Scope{Clients: ids("c1")}, scope("o1", ids("c1"), ids("s1"), nil), false},
		{"site 1, client 1", &Scope{Clients: ids("c1"), Sites: ids("s1")}, scope("o1", ids("c1"), nil, nil), true},
		{"no client, no client", &Scope{Clients: ids()}, scope("o1", ids(), nil, nil), true},
		{"team 1, team 1", &Scope{Teams: ids("t1")}, scope("o1", nil, nil, ids("t1")), true},
		{"client 1, org-wide", &Scope{Clients: ids("c1")}, scope("o1", nil, nil, nil), true},
		{"other organization", nil, scope("o2", nil, nil, nil), false},
		{"no scope", nil, nil, false},
	} {
		sub := &Subscription{OrganizationID: "o1", Scope: c.snapshot}
		if got := sub.VisibleTo(c.scope); got != c.want {
			t.Errorf("%s: visible %v, want %v", c.name, got, c.want)
		}
	}
}
