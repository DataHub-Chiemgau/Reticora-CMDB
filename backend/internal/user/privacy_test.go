package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/go-chi/chi/v5"
)

func privacyFixture(t *testing.T) (*MemoryRepository, *contact.MemoryRepository, chi.Router) {
	t.Helper()
	repo := NewMemoryRepository()
	contacts := contact.NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo, contacts).RegisterRoutes(mux)
	return repo, contacts, mux
}

func privacyRequest(t *testing.T, mux http.Handler, method, url, callerID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: callerID}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestExportUserDataIncludesContacts(t *testing.T) {
	repo, contacts, mux := privacyFixture(t)

	u := &User{OrganizationID: "org-1", Email: "jane@example.com", DisplayName: "Jane", Status: "active"}
	if err := repo.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if err := contacts.Create(context.Background(), &contact.Contact{
		OrganizationID: "org-1", DisplayName: "Jane", Email: "jane@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	// A contact of someone else must not leak into the export.
	if err := contacts.Create(context.Background(), &contact.Contact{
		OrganizationID: "org-1", DisplayName: "John", Email: "john@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	w := privacyRequest(t, mux, "GET", "/api/v1/users/"+u.ID+"/data-export", "admin-1")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		User     *User             `json:"user"`
		Contacts []contact.Contact `json:"contacts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.User == nil || payload.User.Email != "jane@example.com" {
		t.Fatalf("unexpected user: %+v", payload.User)
	}
	if len(payload.Contacts) != 1 || payload.Contacts[0].DisplayName != "Jane" {
		t.Fatalf("expected exactly Jane's contact, got %+v", payload.Contacts)
	}
}

func TestAnonymizeUserReplacesPersonalData(t *testing.T) {
	repo, _, mux := privacyFixture(t)
	u := &User{OrganizationID: "org-1", Email: "jane@example.com", DisplayName: "Jane", Status: "active", ExternalID: "oidc-123"}
	if err := repo.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}

	w := privacyRequest(t, mux, "POST", "/api/v1/users/"+u.ID+"/anonymize", "admin-1")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var anonymized User
	if err := json.Unmarshal(w.Body.Bytes(), &anonymized); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(anonymized.Email, "jane") || strings.Contains(anonymized.DisplayName, "Jane") {
		t.Fatalf("personal data survived anonymization: %+v", anonymized)
	}
	if !strings.HasSuffix(anonymized.Email, "@anonymized.invalid") {
		t.Fatalf("expected surrogate e-mail, got %q", anonymized.Email)
	}
	if anonymized.Status != "anonymized" {
		t.Fatalf("expected status anonymized, got %q", anonymized.Status)
	}

	// The row must still exist (referential integrity) but no longer identify.
	got, err := repo.GetUser(context.Background(), "org-1", u.ID)
	if err != nil {
		t.Fatalf("anonymized user row must remain: %v", err)
	}
	if strings.Contains(got.Email, "jane") {
		t.Fatalf("stored record still identifies the person: %+v", got)
	}
}

func TestAnonymizeSelfIsRejected(t *testing.T) {
	repo, _, mux := privacyFixture(t)
	u := &User{OrganizationID: "org-1", Email: "me@example.com", DisplayName: "Me", Status: "active"}
	if err := repo.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	w := privacyRequest(t, mux, "POST", "/api/v1/users/"+u.ID+"/anonymize", u.ID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestAnonymizeUnknownUserReturns404(t *testing.T) {
	_, _, mux := privacyFixture(t)
	w := privacyRequest(t, mux, "POST", "/api/v1/users/does-not-exist/anonymize", "admin-1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

func TestSurrogatesAreDeterministicAndDistinct(t *testing.T) {
	if SurrogateEmail("a") != SurrogateEmail("a") {
		t.Fatal("surrogate e-mail must be deterministic")
	}
	if SurrogateEmail("a") == SurrogateEmail("b") {
		t.Fatal("different users must get different surrogates")
	}
}

func TestExportUserDataIncludesTicketsAssignmentsAndRoles(t *testing.T) {
	repo := NewMemoryRepository()
	contacts := contact.NewMemoryRepository()
	tickets := ticket.NewMemoryRepository()
	assignments := assignment.NewMemoryRepository()

	mux := chi.NewRouter()
	NewHandler(repo, contacts).WithPrivacySources(tickets, assignments).RegisterRoutes(mux)

	ctx := context.Background()
	u := &User{OrganizationID: "org-1", Email: "jane@example.com", DisplayName: "Jane", Status: "active"}
	if err := repo.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	// A ticket reported by the user and one assigned to them.
	reporterTicket := &ticket.Ticket{OrganizationID: "org-1", Title: "Reported", Status: "open", Priority: "high", ReporterID: u.ID}
	if err := tickets.Create(ctx, reporterTicket); err != nil {
		t.Fatal(err)
	}
	assignedTicket := &ticket.Ticket{OrganizationID: "org-1", Title: "Assigned", Status: "open", Priority: "low", ReporterID: "someone-else", AssigneeID: u.ID}
	if err := tickets.Create(ctx, assignedTicket); err != nil {
		t.Fatal(err)
	}
	// Noise: a ticket of another person must not leak.
	otherTicket := &ticket.Ticket{OrganizationID: "org-1", Title: "Other", Status: "open", Priority: "low", ReporterID: "someone-else"}
	if err := tickets.Create(ctx, otherTicket); err != nil {
		t.Fatal(err)
	}

	// An assignment received by the user and one issued by them.
	received := &assignment.Assignment{OrganizationID: "org-1", AssignedTo: u.ID, AssignedBy: "admin-1", Status: "active"}
	if err := assignments.Create(ctx, received); err != nil {
		t.Fatal(err)
	}
	issued := &assignment.Assignment{OrganizationID: "org-1", AssignedTo: "someone-else", AssignedBy: u.ID, Status: "active"}
	if err := assignments.Create(ctx, issued); err != nil {
		t.Fatal(err)
	}
	noise := &assignment.Assignment{OrganizationID: "org-1", AssignedTo: "someone-else", AssignedBy: "admin-1", Status: "active"}
	if err := assignments.Create(ctx, noise); err != nil {
		t.Fatal(err)
	}

	role := &CustomRole{OrganizationID: "org-1", Name: "Ops", Permissions: []string{"ci:read"}}
	if err := repo.CreateRole(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := repo.AssignRole(ctx, "org-1", &UserRoleAssignment{UserID: u.ID, CustomRoleID: role.ID}); err != nil {
		t.Fatal(err)
	}

	w := privacyRequest(t, mux, "GET", "/api/v1/users/"+u.ID+"/data-export", "admin-1")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Tickets struct {
			Reported []ticket.Ticket `json:"reported"`
			Assigned []ticket.Ticket `json:"assigned"`
		} `json:"tickets"`
		Assignments struct {
			Received []assignment.Assignment `json:"received"`
			Issued   []assignment.Assignment `json:"issued"`
		} `json:"assignments"`
		Roles []UserRoleAssignment `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(payload.Tickets.Reported) != 1 || payload.Tickets.Reported[0].Title != "Reported" {
		t.Fatalf("expected exactly the reported ticket, got %+v", payload.Tickets.Reported)
	}
	if len(payload.Tickets.Assigned) != 1 || payload.Tickets.Assigned[0].Title != "Assigned" {
		t.Fatalf("expected exactly the assigned ticket, got %+v", payload.Tickets.Assigned)
	}
	if len(payload.Assignments.Received) != 1 || payload.Assignments.Received[0].ID != received.ID {
		t.Fatalf("expected exactly the received assignment, got %+v", payload.Assignments.Received)
	}
	if len(payload.Assignments.Issued) != 1 || payload.Assignments.Issued[0].ID != issued.ID {
		t.Fatalf("expected exactly the issued assignment, got %+v", payload.Assignments.Issued)
	}
	if len(payload.Roles) != 1 || payload.Roles[0].CustomRoleID != role.ID {
		t.Fatalf("expected the role assignment, got %+v", payload.Roles)
	}
}
