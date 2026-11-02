package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// fakeInvitations is an InvitationRepository without database: issue and
// renewal record the link the invitation mail would carry
// (invitation.created).
type fakeInvitations struct {
	links []string
}

func (f *fakeInvitations) IssueInvitation(_ context.Context, _ *identity.Principal, inv *Invitation, _, link string) error {
	f.links = append(f.links, link)
	inv.ID, inv.Status, inv.ScopeType, inv.CreatedAt = "inv-1", InvitationPending, "org", time.Now()
	return nil
}

func (f *fakeInvitations) ListInvitations(context.Context, string, api.PaginationParams) ([]Invitation, int, error) {
	return nil, 0, nil
}

func (f *fakeInvitations) RenewInvitation(_ context.Context, orgID, id string, _ *identity.Principal, _, link string, expiresAt time.Time) (*Invitation, error) {
	f.links = append(f.links, link)
	return &Invitation{ID: id, OrganizationID: orgID, Email: "victim@bigcorp.example", ScopeType: "org",
		Status: InvitationPending, ExpiresAt: expiresAt, CreatedAt: time.Now()}, nil
}

func (f *fakeInvitations) RevokeInvitation(context.Context, string, string) error { return nil }

func (f *fakeInvitations) AcceptInvitation(context.Context, string, string, string, QuotaGuard,
	func(context.Context, *Invitation, string) error) (*AcceptedInvitation, error) {
	return nil, ErrInvitationInvalid
}

// TestInvitationTokenReachesOnlyTheMailboxWithAccounts: with an accounts
// client the acceptance creates a verified, password-bearing IdP account of
// the invited address, so the token of issue and renewal goes only into
// the link of the invitation mail; the inviter, who never proved that
// address, does not get it in the response (otherwise any org_admin could
// invite victim@bigcorp.example and accept the invitation itself). Without
// an accounts client the acceptance creates no account and the response
// carries the token once, the one of the mailed link.
func TestInvitationTokenReachesOnlyTheMailboxWithAccounts(t *testing.T) {
	const orgID = "0b9a51a2-55a4-4c7c-9a4f-6f4f6f4f6f4f"
	all := identity.ScopeSet{All: true}
	ctx := withCaller(context.Background(), orgID, "inviter-1", identity.Scope{Clients: all, Sites: all, Teams: all})
	for _, tc := range []struct {
		name      string
		accounts  InvitationAccounts
		wantToken bool
	}{
		{name: "with accounts client", accounts: &stubInvitationAccounts{}, wantToken: false},
		{name: "without accounts client", wantToken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeInvitations{}
			h := NewInvitationHandler(repo).WithAcceptURL(testAcceptURL)
			if tc.accounts != nil {
				h.WithAccounts(tc.accounts)
			}
			mux := chi.NewRouter()
			h.RegisterRoutes(mux)
			for _, step := range []struct {
				name, path, body string
				code             int
			}{
				{name: "issue", path: "/api/v1/invitations", body: `{"email":"victim@bigcorp.example","role_id":"r-1"}`, code: http.StatusCreated},
				{name: "renew", path: "/api/v1/invitations/inv-1/renew", code: http.StatusOK},
			} {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, step.path, strings.NewReader(step.body)).WithContext(ctx))
				if w.Code != step.code {
					t.Fatalf("%s: %d %s, want %d", step.name, w.Code, w.Body.String(), step.code)
				}
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("%s: %v", step.name, err)
				}
				if len(repo.links) == 0 {
					t.Fatalf("%s: no link for the invitation mail", step.name)
				}
				link, err := url.Parse(repo.links[len(repo.links)-1])
				if err != nil {
					t.Fatalf("%s: link %q: %v", step.name, repo.links[len(repo.links)-1], err)
				}
				mailed := link.Query().Get("token")
				if org, ok := InvitationOrg(mailed); !ok || org != orgID {
					t.Fatalf("%s: the mailed link carries no token of the organization: %s", step.name, link)
				}
				token, present := body["token"]
				if present != tc.wantToken {
					t.Errorf("%s: token in the response = %v, want %v (%s)", step.name, present, tc.wantToken, w.Body.String())
				}
				if tc.wantToken && token != mailed {
					t.Errorf("%s: response token %v, mailed token %q", step.name, token, mailed)
				}
				if !tc.wantToken && strings.Contains(w.Body.String(), mailed) {
					t.Errorf("%s: the response repeats the mailed token: %s", step.name, w.Body.String())
				}
			}
		})
	}
}
