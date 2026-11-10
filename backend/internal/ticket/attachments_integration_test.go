package ticket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

// The tests of this file run only against PostgreSQL (TEST_DATABASE_URL,
// see scopetest.Seed). They cover TKT-01 of WP-765: the attachments of a
// ticket are its documents linked over document_link (entity_type ticket),
// and the ticket DTOs follow the field permissions of the kind ticket.

// attachEnv serves the ticket and the document routes on PostgreSQL.
type attachEnv struct {
	f    *scopetest.Fixture
	mux  chi.Router
	user string
}

func newAttachEnv(t *testing.T) *attachEnv {
	t.Helper()
	f := scopetest.Seed(t, "38")
	docs := document.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ticket.NewHandler(ticket.NewService(ticket.NewPGRepository(f.App), nil)).WithDocuments(docs).RegisterRoutes(mux)
	document.NewHandler(docs, nil).RegisterRoutes(mux)
	return &attachEnv{f: f, mux: mux, user: f.AppUser(t, f.OrgA, "attach-reporter")}
}

// do sends a request of organization A with the tenant scope (and field
// policy) of ctx.
func (e *attachEnv) do(t *testing.T, ctx context.Context, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req.WithContext(ctx))
	return w
}

// created decodes the id of a 201 answer.
func created(t *testing.T, w *httptest.ResponseRecorder, what string) string {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("%s: %d %s, want 201", what, w.Code, w.Body)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.ID == "" {
		t.Fatalf("%s: %s err=%v", what, w.Body, err)
	}
	return out.ID
}

type attachmentPage struct {
	Data       []document.Document `json:"data"`
	Total      int                 `json:"total"`
	Limit      int                 `json:"limit"`
	HasMore    bool                `json:"has_more"`
	NextCursor string              `json:"next_cursor"`
}

// attachments reads every page of the attachments of a ticket with the
// limit and returns the document ids in answer order.
func (e *attachEnv) attachments(t *testing.T, ctx context.Context, ticketID string, limit int) (ids []string, total int) {
	t.Helper()
	url := "/api/v1/tickets/" + ticketID + "/attachments?limit=" + strconv.Itoa(limit)
	next := ""
	for range 10 {
		w := e.do(t, ctx, http.MethodGet, url+next, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET attachments: %d %s", w.Code, w.Body)
		}
		var page attachmentPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Limit != limit || len(page.Data) > limit || page.HasMore != (page.NextCursor != "") {
			t.Fatalf("page: %s", w.Body)
		}
		total = page.Total
		for i := range page.Data {
			ids = append(ids, page.Data[i].ID)
		}
		if page.NextCursor == "" {
			return ids, total
		}
		next = "&cursor=" + page.NextCursor
	}
	t.Fatal("attachments: more than 10 pages")
	return nil, 0
}

// TestTicketAttachments: a document linked with POST
// /api/v1/documents/{id}/links (entity_type ticket) is an attachment of the
// ticket, newest first and paged with the cursor; a forged cursor is 400. A
// client-scoped principal gets 404 for the ticket of another client and
// sees on its own ticket only the documents whose link it sees. The route
// needs document:read.
func TestTicketAttachments(t *testing.T) {
	e := newAttachEnv(t)
	f := e.f
	org := f.OrgCtx(f.OrgA)
	client1 := f.ClientCtx(f.Client1)
	ciC1, ciC2 := f.CI(t, f.OrgA, f.Client1, "attach-c1"), f.CI(t, f.OrgA, f.Client2, "attach-c2")

	own := created(t, e.do(t, org, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "Anhang", "related_ci_id": ciC1}), "own ticket")
	foreign := created(t, e.do(t, org, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "Fremd", "related_ci_id": ciC2}), "foreign ticket")

	link := func(docID, entityType, entityID string) {
		t.Helper()
		w := e.do(t, org, http.MethodPost, "/api/v1/documents/"+docID+"/links", map[string]any{"entity_type": entityType, "entity_id": entityID})
		if w.Code != http.StatusCreated {
			t.Fatalf("link %s to %s %s: %d %s", docID, entityType, entityID, w.Code, w.Body)
		}
	}
	var docs []string
	for _, title := range []string{"eins", "zwei", "drei"} {
		id := created(t, e.do(t, org, http.MethodPost, "/api/v1/documents", map[string]any{"title": title, "file_name": title + ".pdf"}), "document "+title)
		link(id, "ticket", own)
		docs = append(docs, id)
	}
	// A document of the foreign ticket that is also linked to the client-2
	// CI: it is no attachment of the own ticket.
	shared := created(t, e.do(t, org, http.MethodPost, "/api/v1/documents", map[string]any{"title": "fremd", "file_name": "fremd.pdf"}), "foreign document")
	link(shared, "ticket", foreign)
	link(shared, "ci", ciC2)
	// The document of the own ticket that is also linked to the client-2
	// CI stays an attachment for the client-1 principal: its link to the
	// ticket is visible.
	link(docs[0], "ci", ciC2)

	newestFirst := []string{docs[2], docs[1], docs[0]}
	for _, limit := range []int{2, 50} {
		ids, total := e.attachments(t, org, own, limit)
		if total != 3 || !slices.Equal(ids, newestFirst) {
			t.Fatalf("org-wide, limit %d: total %d %v, want %v", limit, total, ids, newestFirst)
		}
	}
	if ids, total := e.attachments(t, client1, own, 1); total != 3 || !slices.Equal(ids, newestFirst) {
		t.Fatalf("client-1 attachments: total %d %v, want %v", total, ids, newestFirst)
	}
	if ids, total := e.attachments(t, org, foreign, 50); total != 1 || !slices.Equal(ids, []string{shared}) {
		t.Fatalf("foreign ticket attachments: total %d %v, want [%s]", total, ids, shared)
	}

	url := "/api/v1/tickets/" + own + "/attachments"
	for _, c := range []struct {
		name string
		ctx  context.Context
		url  string
		want int
	}{
		{"forged cursor", org, url + "?cursor=forged", http.StatusBadRequest},
		{"cursor of another order", org, url + "?cursor=" + api.EncodeCursor(api.Cursor{Sort: "created_at", Dir: "asc", Value: "2026-01-01T00:00:00Z", ID: own}), http.StatusBadRequest},
		{"foreign ticket for client 1", client1, "/api/v1/tickets/" + foreign + "/attachments", http.StatusNotFound},
		{"unknown ticket", org, "/api/v1/tickets/" + f.ID() + "/attachments", http.StatusNotFound},
		{"no uuid", org, "/api/v1/tickets/nope/attachments", http.StatusNotFound},
	} {
		if w := e.do(t, c.ctx, http.MethodGet, c.url, nil); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body, c.want)
		}
	}

	// Without the document repository (release module documents off) the
	// route is not registered: 404 like an unknown path (SR-61, WP-874).
	bare := chi.NewRouter()
	ticket.NewHandler(ticket.NewService(ticket.NewPGRepository(f.App), nil)).RegisterRoutes(bare)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	bare.ServeHTTP(rec, req.WithContext(tenant.WithTenant(org, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user})))
	if rec.Code != http.StatusNotFound {
		t.Errorf("without documents: %d, want 404", rec.Code)
	}

	// The route needs document:read; ticket:read alone is 403.
	route := "/api/v1/tickets/{id}/attachments"
	for _, c := range []struct {
		perms []identity.Permission
		want  int
	}{
		{[]identity.Permission{identity.PermTicketRead}, http.StatusForbidden},
		{[]identity.Permission{identity.PermDocumentRead}, http.StatusOK},
	} {
		authorized := server.AuthorizeRoute(http.MethodGet, route)(e.mux)
		principal := identity.Principal{Subject: e.user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Permissions: c.perms}
		ctx := identity.WithPrincipal(tenant.WithTenant(org, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user}), principal)
		ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(c.perms))
		w := httptest.NewRecorder()
		authorized.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil).WithContext(ctx))
		if w.Code != c.want {
			t.Errorf("permissions %v: %d %s, want %d", c.perms, w.Code, w.Body, c.want)
		}
	}
}

// TestTicketFieldRights: a ticket field the role may not read (grant none)
// is missing in the list, the detail and the answers of POST and PATCH; a
// PATCH or POST of it, or of a field the role may only read with another
// value, is 403 with the fields; a filter on it is 403 as well.
func TestTicketFieldRights(t *testing.T) {
	e := newAttachEnv(t)
	f := e.f
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	id := created(t, e.do(t, org, http.MethodPost, "/api/v1/tickets",
		map[string]any{"title": "Feldrechte", "description": "vertraulich", "priority": "high"}), "ticket")

	role := f.ID()
	perms := []string{"ticket:read", "ticket:write"}
	raw, _ := json.Marshal(perms)
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp765-reader', $3::jsonb)`, []any{role, f.OrgA, string(raw)}},
		{`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ticket', 'core.description', 'none')`, []any{f.OrgA, role}},
		{`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ticket', 'core.priority', 'read')`, []any{f.OrgA, role}},
	} {
		if _, err := f.Admin.Exec(bg, s.sql, s.args...); err != nil {
			t.Fatalf("%s: %v", s.sql, err)
		}
	}
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(bg, f.OrgA, []identity.Grant{{Permissions: []identity.Permission{"ticket:read", "ticket:write"}, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	reader := permission.WithPolicy(org, policy)
	asReader := func(method, url string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := e.do(t, reader, method, url, body)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, url, w.Code, w.Body, want)
		}
		return w
	}
	wantFields := func(w *httptest.ResponseRecorder, fields ...string) {
		t.Helper()
		var p api.FieldForbiddenProblem
		if decodeErr := json.Unmarshal(w.Body.Bytes(), &p); decodeErr != nil || !slices.Equal(p.Fields, fields) {
			t.Fatalf("403 %s, want fields %v", w.Body, fields)
		}
	}
	projected := func(w *httptest.ResponseRecorder) {
		t.Helper()
		var body map[string]any
		if decodeErr := json.Unmarshal(w.Body.Bytes(), &body); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if _, ok := body["description"]; ok || body["title"] == nil || body["status"] == nil || body["priority"] == nil {
			t.Fatalf("projected ticket: %s", w.Body)
		}
	}

	url := "/api/v1/tickets/" + id
	projected(asReader(http.MethodGet, url, nil, http.StatusOK))
	if full := e.do(t, org, http.MethodGet, url, nil); !strings.Contains(full.Body.String(), "vertraulich") {
		t.Fatalf("unrestricted detail: %s", full.Body)
	}
	list := asReader(http.MethodGet, "/api/v1/tickets?limit=200", nil, http.StatusOK)
	if strings.Contains(list.Body.String(), "vertraulich") || !strings.Contains(list.Body.String(), "Feldrechte") {
		t.Fatalf("projected list: %s", list.Body)
	}

	wantFields(asReader(http.MethodPatch, url, map[string]any{"description": "vertraulich"}, http.StatusForbidden), "description")
	wantFields(asReader(http.MethodPatch, url, map[string]any{"priority": "low", "title": "neu"}, http.StatusForbidden), "priority")
	wantFields(asReader(http.MethodPost, "/api/v1/tickets", map[string]any{"title": "x", "description": "y", "priority": "low"}, http.StatusForbidden),
		"description", "priority")
	wantFields(asReader(http.MethodGet, "/api/v1/tickets?search=vertr", nil, http.StatusForbidden), "description")
	asReader(http.MethodGet, "/api/v1/tickets?priority=high", nil, http.StatusOK)

	// The unchanged value of a readable, read-only field passes; the
	// answer is projected.
	w := asReader(http.MethodPatch, url, map[string]any{"priority": "high", "status": "open"}, http.StatusOK)
	projected(w)
	var tk ticket.Ticket
	if err = json.Unmarshal(w.Body.Bytes(), &tk); err != nil || tk.Status != ticket.StatusOpen {
		t.Fatalf("patched as reader: %s err=%v", w.Body, err)
	}
	projected(asReader(http.MethodPost, "/api/v1/tickets", map[string]any{"title": "Leser"}, http.StatusCreated))
	var description string
	if err = f.Admin.QueryRow(bg, `SELECT description FROM ticket WHERE id = $1`, id).Scan(&description); err != nil || description != "vertraulich" {
		t.Fatalf("description after refused PATCH: %q err=%v", description, err)
	}
}
