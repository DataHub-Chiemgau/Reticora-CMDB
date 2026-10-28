package server_test

// WP-394 (TLC-03, AUT-10): regression test of the invitation acceptance in
// the production request chain (t2_auth_chain_test.go). POST
// /api/v1/invitations/accept is public, its token is the credential, so it
// runs through the pre-auth limit of the client IP.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestInvitationAcceptanceRunsThroughPreAuthLimit' ./internal/server/

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// unknownInvitations knows no invitation: every token is invalid.
type unknownInvitations struct{}

func (unknownInvitations) IssueInvitation(context.Context, *identity.Principal, *user.Invitation, string, string) error {
	return user.ErrInvitationInvalid
}

func (unknownInvitations) ListInvitations(context.Context, string, api.PaginationParams) ([]user.Invitation, int, error) {
	return nil, 0, nil
}

func (unknownInvitations) RenewInvitation(context.Context, string, string, *identity.Principal, string, string, time.Time) (*user.Invitation, error) {
	return nil, user.ErrInvitationNotFound
}

func (unknownInvitations) RevokeInvitation(context.Context, string, string) error {
	return user.ErrInvitationNotFound
}

func (unknownInvitations) AcceptInvitation(context.Context, string, string, string, user.QuotaGuard,
	func(context.Context, *user.Invitation, string) error) (*user.AcceptedInvitation, error) {
	return nil, user.ErrInvitationInvalid
}

// TestInvitationAcceptanceRunsThroughPreAuthLimit: every acceptance counts
// against the pre-auth budget of its IP (20 per minute); invalid tokens are
// 404 (unknown token of a valid form) or 400 (malformed body) until the
// budget is spent, then the IP gets 429 with Retry-After. Another IP keeps
// its own budget.
func TestInvitationAcceptanceRunsThroughPreAuthLimit(t *testing.T) {
	repos := server.MemoryRepositories()
	repos.Invitations = unknownInvitations{}
	s := newT2StackFrom(t, entitlement.Options{}, repos)
	// The budget is per minute; start early enough in a window.
	if left := 60 - time.Now().UTC().Unix()%60; left < 10 {
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
	accept := func(ip, body string) int {
		w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/invitations/accept", body: body,
			contentType: "application/json", remoteAddr: ip + ":41000"})
		return w.Code
	}
	unknown := `{"token":"` + t2Org + `.not-a-valid-secret"}`
	for i := range middleware.DefaultPreAuthPerMinute {
		body, want := unknown, http.StatusNotFound
		if i%2 == 1 {
			body, want = `{"token":"`+t2Org+`.x","unexpected":true}`, http.StatusBadRequest
		}
		if code := accept("198.51.100.40", body); code != want {
			t.Fatalf("acceptance %d within the budget: %d, want %d", i+1, code, want)
		}
	}
	w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/invitations/accept", body: unknown,
		contentType: "application/json", remoteAddr: "198.51.100.40:41000"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("acceptance beyond the budget: %d %s, want 429", w.Code, w.Body.String())
	}
	retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Errorf("429 Retry-After = %q, want the seconds until the budget renews (1..60)", w.Header().Get("Retry-After"))
	}
	if code := accept("198.51.100.41", unknown); code != http.StatusNotFound {
		t.Errorf("acceptance from another IP: %d, want 404 (its own budget)", code)
	}
}
