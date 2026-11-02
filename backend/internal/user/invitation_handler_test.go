package user

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
)

// fakeInvitations is an InvitationRepository without database. Issue and
// renewal deliver like PGInvitations (sendInvitation, then
// appendInvitationCreated): they send the direct mail first and then record
// the data of invitation.created; a refused send changes nothing, like the
// rolled-back transaction.
type fakeInvitations struct {
	org    invitationOrganization
	stored map[string]Invitation
	// hashes are the stored token hashes by invitation id.
	hashes map[string]string
	// events are the data of the appended invitation.created events.
	events []map[string]any
}

func newFakeInvitations() *fakeInvitations {
	return &fakeInvitations{org: invitationOrganization{Name: "ACME", Locale: "de-DE"},
		stored: map[string]Invitation{}, hashes: map[string]string{}}
}

func (f *fakeInvitations) IssueInvitation(ctx context.Context, _ *identity.Principal, inv *Invitation, tokenHash string, delivery InvitationDelivery) error {
	if err := validateInvitation(inv); err != nil {
		return err
	}
	inv.ID, inv.Status, inv.CreatedAt = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.stored)+1), InvitationPending, time.Now()
	data := delivery.eventData(inv, f.org.Name)
	if err := delivery.send(ctx, inv, f.org); err != nil {
		return err
	}
	f.stored[inv.ID], f.hashes[inv.ID] = *inv, tokenHash
	f.events = append(f.events, data)
	return nil
}

func (f *fakeInvitations) ListInvitations(context.Context, string, api.PaginationParams) ([]Invitation, int, error) {
	return nil, 0, nil
}

func (f *fakeInvitations) RenewInvitation(ctx context.Context, _, id string, _ *identity.Principal, tokenHash string, delivery InvitationDelivery, expiresAt time.Time) (*Invitation, error) {
	current, ok := f.stored[id]
	if !ok {
		return nil, ErrInvitationNotFound
	}
	current.ExpiresAt = expiresAt
	data := delivery.eventData(&current, f.org.Name)
	if err := delivery.send(ctx, &current, f.org); err != nil {
		return nil, err
	}
	f.stored[id], f.hashes[id] = current, tokenHash
	f.events = append(f.events, data)
	return &current, nil
}

func (f *fakeInvitations) RevokeInvitation(context.Context, string, string) error { return nil }

func (f *fakeInvitations) AcceptInvitation(context.Context, string, string, string, QuotaGuard,
	func(context.Context, *Invitation, string) error) (*AcceptedInvitation, error) {
	return nil, ErrInvitationInvalid
}

// recordingMailer stands in for notify.InvitationMailer over the central
// SMTP server: it records every mail and the deadline of its send.
type recordingMailer struct {
	mu        sync.Mutex
	sent      []notify.InvitationMail
	deadlines []time.Duration
	err       error
}

func (m *recordingMailer) SendInvitation(ctx context.Context, mail *notify.InvitationMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("the send of the invitation mail is not bounded")
	}
	m.sent = append(m.sent, *mail)
	m.deadlines = append(m.deadlines, time.Until(deadline))
	return nil
}

// invitationMux serves the handler h with chi.
func invitationMux(h *InvitationHandler) *chi.Mux {
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

// postInvitation posts body to path as the principal of ctx.
func postInvitation(mux http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx))
	return w
}

// orgAdminCtx is an org-wide principal of orgID holding every permission.
func orgAdminCtx(orgID string) context.Context {
	all := identity.ScopeSet{All: true}
	return withCaller(context.Background(), orgID, "inviter-1", identity.Scope{Clients: all, Sites: all, Teams: all})
}

// tokenOf returns the token of an acceptance link.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link %q: %v", link, err)
	}
	return u.Query().Get("token")
}

// TestInvitationTokenReachesOnlyTheMailboxWithAccounts: with an accounts
// client the acceptance creates a verified, password-bearing IdP account of
// the invited address, so the token of issue and renewal reaches that
// mailbox only. The response leaves it out (otherwise any org_admin could
// invite victim@bigcorp.example and accept the invitation itself), so does
// the event invitation.created, and exactly one mail goes straight to the
// invited address over the central SMTP server (mailer), with the stored
// token, the expiry and the organization, in a send bounded by
// invitationMailTimeout. Without an accounts client, and with one but no
// mailer (not wired: nobody would get the link), no mail is sent directly:
// the response carries the token once, the one of the event's link, which
// the notifier mails over the organization's channels.
func TestInvitationTokenReachesOnlyTheMailboxWithAccounts(t *testing.T) {
	const orgID = "0b9a51a2-55a4-4c7c-9a4f-6f4f6f4f6f4f"
	const invitee = "victim@bigcorp.example"
	for _, tc := range []struct {
		name     string
		accounts InvitationAccounts
		mailer   *recordingMailer
		direct   bool
	}{
		{name: "with accounts client and mailer", accounts: &stubInvitationAccounts{}, mailer: &recordingMailer{}, direct: true},
		{name: "without accounts client", mailer: &recordingMailer{}},
		{name: "accounts client without mailer", accounts: &stubInvitationAccounts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeInvitations()
			h := NewInvitationHandler(repo).WithAcceptURL(testAcceptURL)
			if tc.accounts != nil {
				var mailer InvitationMailer
				if tc.mailer != nil {
					mailer = tc.mailer
				}
				h.WithAccounts(tc.accounts, mailer)
			}
			mux := invitationMux(h)
			ctx := orgAdminCtx(orgID)
			id := ""
			for i, step := range []struct {
				name, body string
				code       int
			}{
				{name: "issue", body: `{"email":"` + invitee + `","role_id":"` + orgID + `"}`, code: http.StatusCreated},
				{name: "renew", code: http.StatusOK},
			} {
				path := "/api/v1/invitations"
				if id != "" {
					path += "/" + id + "/renew"
				}
				w := postInvitation(mux, ctx, path, step.body)
				if w.Code != step.code {
					t.Fatalf("%s: %d %s, want %d", step.name, w.Code, w.Body.String(), step.code)
				}
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("%s: %v", step.name, err)
				}
				id, _ = body["id"].(string)
				if len(repo.events) != i+1 {
					t.Fatalf("%s: %d invitation.created events, want %d", step.name, len(repo.events), i+1)
				}
				event := repo.events[i]
				eventJSON, _ := json.Marshal(event)
				token, inResponse := body["token"].(string)

				if !tc.direct {
					if tc.mailer != nil && len(tc.mailer.sent) != 0 {
						t.Errorf("%s: %d mails sent directly without accounts client", step.name, len(tc.mailer.sent))
					}
					link, _ := event["link"].(string)
					if !inResponse || token == "" || token != tokenOf(t, link) || HashInvitationToken(token) != repo.hashes[id] {
						t.Errorf("%s: response token %q, event link %q: want the stored token in both", step.name, token, link)
					}
					continue
				}

				if inResponse || strings.Contains(w.Body.String(), `"token"`) {
					t.Errorf("%s: the response carries a token: %s", step.name, w.Body.String())
				}
				if len(tc.mailer.sent) != i+1 {
					t.Fatalf("%s: %d direct mails, want %d (one per issue and renewal)", step.name, len(tc.mailer.sent), i+1)
				}
				mail := tc.mailer.sent[i]
				mailed := tokenOf(t, mail.Link)
				if org, ok := InvitationOrg(mailed); !ok || org != orgID || HashInvitationToken(mailed) != repo.hashes[id] {
					t.Errorf("%s: the mailed link %q carries no stored token of the organization", step.name, mail.Link)
				}
				if !strings.HasPrefix(mail.Link, testAcceptURL+"?token=") {
					t.Errorf("%s: mailed link %q, want the accept page %s", step.name, mail.Link, testAcceptURL)
				}
				if mail.To != invitee || mail.OrganizationName != "ACME" || mail.Locale != "de-DE" ||
					!mail.ExpiresAt.Equal(repo.stored[id].ExpiresAt) {
					t.Errorf("%s: mail %+v, want to %s for ACME (de-DE) until %s", step.name, mail, invitee, repo.stored[id].ExpiresAt)
				}
				if d := tc.mailer.deadlines[i]; d <= 0 || d > invitationMailTimeout {
					t.Errorf("%s: the send has %v left, want at most %v", step.name, d, invitationMailTimeout)
				}
				if strings.Contains(w.Body.String(), mailed) {
					t.Errorf("%s: the response repeats the mailed token: %s", step.name, w.Body.String())
				}
				if _, ok := event["link"]; ok || strings.Contains(string(eventJSON), mailed) {
					t.Errorf("%s: invitation.created carries the link or token: %s", step.name, eventJSON)
				}
				if event["email"] != invitee || event["id"] != id || event["organization_name"] != "ACME" {
					t.Errorf("%s: invitation.created data %s, want id, address and organization", step.name, eventJSON)
				}
			}
		})
	}
}

// directResolver resolves an audience to its direct recipients (the
// invitee of invitation.created).
type directResolver struct{}

func (directResolver) Resolve(_ context.Context, _ string, a *notify.Audience) ([]notify.Recipient, error) {
	return append([]notify.Recipient(nil), a.Direct...), nil
}

// TestInvitationMailGoesOverNoChannelOfTheOrganization: with an accounts
// client an org_admin who configured an e-mail channel with an SMTP server
// of their own (a relay they read) does not get the invitation link through
// it. The issue mails the invitee straight over the central SMTP server,
// and the event the notifier receives queues no delivery on any channel:
// neither the own SMTP server nor a central_smtp channel (no duplicate) nor
// the in-app center. The default organization of an installation, which
// has no e-mail channel at all, gets the same single mail. Control: the
// event of a handler without accounts client (link in the event) is queued
// on both e-mail channels, so the notifier would have seen a copy.
func TestInvitationMailGoesOverNoChannelOfTheOrganization(t *testing.T) {
	const defaultOrg = "00000000-0000-0000-0000-000000000001"
	const attackerOrg = "0b9a51a2-55a4-4c7c-9a4f-6f4f6f4f6f4f"
	const invitee = "victim@bigcorp.example"
	channels := func(t *testing.T, orgID string, own bool) *notify.MemoryRepository {
		t.Helper()
		repo := notify.NewMemoryRepository()
		if !own {
			return repo
		}
		for _, ch := range []*notify.Channel{
			{OrganizationID: orgID, Name: "relay", Provider: notify.ProviderSMTP, Enabled: true,
				Config: map[string]any{"host": "relay.attacker.example", "port": float64(25), "tls": "none", "from": "x@attacker.example"}},
			{OrganizationID: orgID, Name: "central", Provider: notify.ProviderCentralSMTP, Enabled: true},
			{OrganizationID: orgID, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
		} {
			if err := repo.Create(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
		}
		return repo
	}
	// queued feeds the recorded invitation.created events to the notifier
	// of the channels and returns the deliveries it queued.
	queued := func(t *testing.T, orgID string, repo *notify.MemoryRepository, recorded []map[string]any) []notify.QueuedDelivery {
		t.Helper()
		store := notify.NewMemoryStore()
		consumer := notify.NewEventConsumer(directResolver{}, notify.NewNotifier(repo, store, store, notify.NewSender(nil)))
		for i, data := range recorded {
			raw, _ := json.Marshal(data)
			if err := consumer.Consume(context.Background(), &events.Envelope{ID: fmt.Sprintf("e-%d", i), OrgID: orgID,
				Type: events.InvitationCreated, Data: raw}); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := store.List(context.Background(), orgID, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	for _, tc := range []struct {
		name  string
		orgID string
		own   bool
	}{
		{name: "organization with its own SMTP server", orgID: attackerOrg, own: true},
		{name: "default organization without any channel", orgID: defaultOrg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mailer := newFakeInvitations(), &recordingMailer{}
			mux := invitationMux(NewInvitationHandler(repo).WithAcceptURL(testAcceptURL).WithAccounts(&stubInvitationAccounts{}, mailer))
			w := postInvitation(mux, orgAdminCtx(tc.orgID), "/api/v1/invitations", `{"email":"`+invitee+`","role_id":"`+attackerOrg+`"}`)
			if w.Code != http.StatusCreated {
				t.Fatalf("issue: %d %s", w.Code, w.Body.String())
			}
			if len(mailer.sent) != 1 || mailer.sent[0].To != invitee {
				t.Fatalf("direct mails %+v, want exactly one to %s", mailer.sent, invitee)
			}
			if rows := queued(t, tc.orgID, channels(t, tc.orgID, tc.own), repo.events); len(rows) != 0 {
				t.Errorf("the notifier queued %d deliveries of the invitation over the organization's channels: %+v", len(rows), rows)
			}
		})
	}

	t.Run("control: without accounts client the event is mailed over the e-mail channels", func(t *testing.T) {
		repo := newFakeInvitations()
		mux := invitationMux(NewInvitationHandler(repo).WithAcceptURL(testAcceptURL))
		if w := postInvitation(mux, orgAdminCtx(attackerOrg), "/api/v1/invitations", `{"email":"`+invitee+`","role_id":"`+attackerOrg+`"}`); w.Code != http.StatusCreated {
			t.Fatalf("issue: %d %s", w.Code, w.Body.String())
		}
		ch := channels(t, attackerOrg, true)
		rows := queued(t, attackerOrg, ch, repo.events)
		if len(rows) != 2 {
			t.Fatalf("deliveries %+v, want one per e-mail channel", rows)
		}
		for _, r := range rows {
			stored, err := ch.Get(context.Background(), attackerOrg, r.ChannelID)
			if err != nil || stored.Kind != notify.KindEmail || r.Recipient.Email != invitee {
				t.Errorf("delivery %+v over channel %+v (%v), want an e-mail channel to %s", r, stored, err, invitee)
			}
		}
	})
}

// TestInvitationMailFailureChangesNothing: when the central SMTP server
// does not take the invitation mail, the issue stores no invitation and no
// event, and a renewal keeps the old token and expiry; both answer 503 with
// the detail "the invitation e-mail could not be sent" (which the UI words)
// and neither the token nor the SMTP cause, which names the server and
// goes to the log only.
func TestInvitationMailFailureChangesNothing(t *testing.T) {
	const orgID = "0b9a51a2-55a4-4c7c-9a4f-6f4f6f4f6f4f"
	repo, mailer := newFakeInvitations(), &recordingMailer{}
	mux := invitationMux(NewInvitationHandler(repo).WithAcceptURL(testAcceptURL).WithAccounts(&stubInvitationAccounts{}, mailer))
	ctx := orgAdminCtx(orgID)
	refused := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusServiceUnavailable || problem.Detail != ErrInvitationMail.Error() ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("refused send: %d %q %s, want 503 problem+json %q", w.Code, w.Header().Get("Content-Type"), w.Body.String(), ErrInvitationMail)
		}
		if strings.Contains(w.Body.String(), "relay.internal") || strings.Contains(w.Body.String(), "token") {
			t.Errorf("the refusal names the SMTP cause or a token: %s", w.Body.String())
		}
	}

	mailer.err = errors.New("smtp connect relay.internal:587: connection refused")
	refused(t, postInvitation(mux, ctx, "/api/v1/invitations", `{"email":"lost@bigcorp.example","role_id":"`+orgID+`"}`))
	if len(repo.stored) != 0 || len(repo.events) != 0 {
		t.Fatalf("a refused send stored %d invitations and %d events", len(repo.stored), len(repo.events))
	}

	mailer.err = nil
	w := postInvitation(mux, ctx, "/api/v1/invitations", `{"email":"kept@bigcorp.example","role_id":"`+orgID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("issue: %d %s", w.Code, w.Body.String())
	}
	var issued Invitation
	_ = json.Unmarshal(w.Body.Bytes(), &issued)
	hash, expires := repo.hashes[issued.ID], repo.stored[issued.ID].ExpiresAt

	mailer.err = errors.New("smtp RCPT TO kept@bigcorp.example via relay.internal: 451 try again later")
	refused(t, postInvitation(mux, ctx, "/api/v1/invitations/"+issued.ID+"/renew", ``))
	if repo.hashes[issued.ID] != hash || !repo.stored[issued.ID].ExpiresAt.Equal(expires) || len(repo.events) != 1 {
		t.Errorf("a refused send renewed the invitation: hash changed %v, expiry %s (was %s), %d events",
			repo.hashes[issued.ID] != hash, repo.stored[issued.ID].ExpiresAt, expires, len(repo.events))
	}
	if len(mailer.sent) != 1 {
		t.Errorf("%d mails sent, want the one of the issue", len(mailer.sent))
	}
}
