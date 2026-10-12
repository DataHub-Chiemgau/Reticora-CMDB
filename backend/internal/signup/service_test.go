package signup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// stubMailer records the verification mails (the notify channel stub until
// WP-137).
type stubMailer struct {
	to, org, link string
	err           error
}

func (m *stubMailer) SendSignupVerification(_ context.Context, to, org, link string) error {
	m.to, m.org, m.link = to, org, link
	return m.err
}

type stubAccounts struct {
	created []Account
	deleted []string
	err     error
}

func (a *stubAccounts) CreateAccount(_ context.Context, acc Account) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	a.created = append(a.created, acc)
	return "kc-1", nil
}

func (a *stubAccounts) DeleteAccount(_ context.Context, id string) error {
	a.deleted = append(a.deleted, id)
	return nil
}

// stubRepo keeps requests in memory; failAfterAccount simulates a failing
// commit after the account was created.
type stubRepo struct {
	requests         map[string]*PendingRequest
	completed        map[string]bool
	failAfterAccount error
	setup            *Setup
}

func newStubRepo() *stubRepo {
	return &stubRepo{requests: map[string]*PendingRequest{}, completed: map[string]bool{}}
}

func (r *stubRepo) CreateRequest(_ context.Context, req *PendingRequest) error {
	req.ID = "req-1"
	r.requests[req.TokenHash] = req
	return nil
}

func (r *stubRepo) Complete(ctx context.Context, tokenHash string, now time.Time, setup *Setup,
	createAccount func(ctx context.Context, req *PendingRequest) error) (Result, error) {
	req, ok := r.requests[tokenHash]
	if !ok || r.completed[tokenHash] {
		return Result{}, ErrInvalidToken
	}
	if !now.Before(req.ExpiresAt) {
		return Result{}, ErrExpired
	}
	if err := createAccount(ctx, req); err != nil {
		return Result{}, err
	}
	if r.failAfterAccount != nil {
		return Result{}, r.failAfterAccount
	}
	r.completed[tokenHash] = true
	r.setup = setup
	return Result{OrganizationID: setup.OrganizationID, OrganizationSlug: Slugify(req.OrganizationName),
		Plan: setup.Plan, TrialEndsAt: setup.TrialEndsAt}, nil
}

func newTestService(repo Repository, mailer Mailer, accounts Accounts) *Service {
	cfg := DefaultConfig()
	cfg.VerifyURL = "https://app.example/signup/verify"
	svc := NewService(repo, mailer, accounts, cfg)
	svc.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	svc.newOrgID = func() string { return "11111111-2222-4333-8444-555555555555" }
	return svc
}

func tokenOf(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	return u.Query().Get("token")
}

func TestSignupUnavailableWithoutChannelOrIdP(t *testing.T) {
	repo := newStubRepo()
	for name, svc := range map[string]*Service{
		"nil service":  nil,
		"no mailer":    newTestService(repo, nil, &stubAccounts{}),
		"no accounts":  newTestService(repo, &stubMailer{}, nil),
		"no verifyURL": NewService(repo, &stubMailer{}, &stubAccounts{}, DefaultConfig()),
	} {
		if err := svc.Submit(context.Background(), Request{OrganizationName: "Acme", Email: "a@example.com"}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: Submit error = %v, want ErrUnavailable", name, err)
		}
		if _, err := svc.Verify(context.Background(), Verification{Token: "x", Password: "a-long-password"}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: Verify error = %v, want ErrUnavailable", name, err)
		}
	}
}

func TestSubmitStoresOnlyTheTokenHashAndMailsTheLink(t *testing.T) {
	repo, mailer := newStubRepo(), &stubMailer{}
	svc := newTestService(repo, mailer, &stubAccounts{})
	if err := svc.Submit(context.Background(), Request{OrganizationName: " Acme GmbH ", Email: "admin@acme.example"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if mailer.to != "admin@acme.example" || mailer.org != "Acme GmbH" {
		t.Fatalf("mail = %q/%q", mailer.to, mailer.org)
	}
	if !strings.HasPrefix(mailer.link, "https://app.example/signup/verify?token=") {
		t.Fatalf("link = %q", mailer.link)
	}
	token := tokenOf(t, mailer.link)
	req, ok := repo.requests[hashToken(token)]
	if !ok {
		t.Fatal("stored request is not keyed by the token's hash")
	}
	if req.TokenHash == token || strings.Contains(req.TokenHash, token) {
		t.Fatal("the plain token is stored")
	}
	if req.DisplayName != "admin@acme.example" {
		t.Errorf("display name defaults to the address, got %q", req.DisplayName)
	}
	if want := svc.now().Add(24 * time.Hour); !req.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", req.ExpiresAt, want)
	}
}

func TestSubmitValidatesFields(t *testing.T) {
	svc := newTestService(newStubRepo(), &stubMailer{}, &stubAccounts{})
	for _, tc := range []struct {
		req   Request
		field string
	}{
		{Request{OrganizationName: "Acme", Email: "not-an-address"}, "email"},
		{Request{OrganizationName: "Acme", Email: "Admin <a@example.com>"}, "email"},
		{Request{OrganizationName: "  ", Email: "a@example.com"}, "organization_name"},
		{Request{OrganizationName: strings.Repeat("x", 201), Email: "a@example.com"}, "organization_name"},
	} {
		var fe *api.FieldError
		if err := svc.Submit(context.Background(), tc.req); !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("Submit(%+v) = %v, want field error on %s", tc.req, err, tc.field)
		}
	}
}

func TestVerifyCreatesAccountForTheNewOrganization(t *testing.T) {
	repo, mailer, accounts := newStubRepo(), &stubMailer{}, &stubAccounts{}
	svc := newTestService(repo, mailer, accounts)
	if err := svc.Submit(context.Background(), Request{OrganizationName: "Acme", Email: "a@example.com", DisplayName: "Ada"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	token := tokenOf(t, mailer.link)

	var fe *api.FieldError
	if _, err := svc.Verify(context.Background(), Verification{Token: token, Password: "short"}); !errors.As(err, &fe) || fe.Field != "password" {
		t.Fatalf("short password: %v", err)
	}

	res, err := svc.Verify(context.Background(), Verification{Token: token, Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.OrganizationID != "11111111-2222-4333-8444-555555555555" || res.Plan != entitlement.PlanStandard {
		t.Fatalf("result = %+v", res)
	}
	if want := svc.now().AddDate(0, 0, 30); !res.TrialEndsAt.Equal(want) {
		t.Errorf("trial ends %v, want %v", res.TrialEndsAt, want)
	}
	if len(accounts.created) != 1 {
		t.Fatalf("accounts created = %d", len(accounts.created))
	}
	acc := accounts.created[0]
	if acc.Email != "a@example.com" || acc.DisplayName != "Ada" || acc.Password != "correct horse battery" ||
		acc.OrganizationID != res.OrganizationID {
		t.Errorf("account = %+v", acc)
	}
	if _, err := svc.Verify(context.Background(), Verification{Token: token, Password: "correct horse battery"}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("second use: %v, want ErrInvalidToken", err)
	}
}

func TestVerifyDeletesTheAccountWhenTheTransactionFails(t *testing.T) {
	repo, mailer, accounts := newStubRepo(), &stubMailer{}, &stubAccounts{}
	repo.failAfterAccount = errors.New("commit failed")
	svc := newTestService(repo, mailer, accounts)
	if err := svc.Submit(context.Background(), Request{OrganizationName: "Acme", Email: "a@example.com"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := svc.Verify(context.Background(), Verification{Token: tokenOf(t, mailer.link), Password: "correct horse battery"}); err == nil {
		t.Fatal("Verify succeeded")
	}
	if len(accounts.deleted) != 1 || accounts.deleted[0] != "kc-1" {
		t.Errorf("deleted = %v, want the created account", accounts.deleted)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	repo, mailer := newStubRepo(), &stubMailer{}
	svc := newTestService(repo, mailer, &stubAccounts{})
	if err := svc.Submit(context.Background(), Request{OrganizationName: "Acme", Email: "a@example.com"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	svc.now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	if _, err := svc.Verify(context.Background(), Verification{Token: tokenOf(t, mailer.link), Password: "correct horse battery"}); !errors.Is(err, ErrExpired) {
		t.Errorf("Verify = %v, want ErrExpired", err)
	}
}

func TestTrialEntitlementsFollowENT07(t *testing.T) {
	cfg := DefaultConfig()
	ends := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	ents, err := TrialEntitlements(cfg, ends)
	if err != nil {
		t.Fatalf("TrialEntitlements: %v", err)
	}
	byKey := map[string]entitlement.Entitlement{}
	for _, e := range ents {
		if e.Source != "selfsignup" || e.Plan != entitlement.PlanStandard || !e.Enabled {
			t.Errorf("%s: %+v", e.FeatureKey, e)
		}
		byKey[e.FeatureKey] = e
	}
	if len(byKey) != len(entitlement.PlanFeatures(entitlement.PlanStandard)) {
		t.Fatalf("features = %d, want the standard plan's", len(byKey))
	}
	core := byKey[entitlement.FeatureCMDBCore]
	if core.ValidUntil != nil || core.Limits[entitlement.LimitMaxCIs] != 100 {
		t.Errorf("cmdb_core = %+v, want max_cis 100 without expiry", core)
	}
	// Essential features stay after the trial, standard-only ones expire.
	if byKey[entitlement.FeatureDiscovery].ValidUntil != nil || byKey[entitlement.FeatureInventory].ValidUntil != nil {
		t.Error("an essential feature expires with the trial")
	}
	for _, f := range []string{entitlement.FeatureDocuments, entitlement.FeatureStocktake, entitlement.FeatureTicketing} {
		if v := byKey[f].ValidUntil; v == nil || !v.Equal(ends) {
			t.Errorf("%s valid_until = %v, want %v", f, v, ends)
		}
	}

	cfg.TrialMaxCIs = 250
	ents, _ = TrialEntitlements(cfg, ends)
	for _, e := range ents {
		if e.FeatureKey == entitlement.FeatureCMDBCore && e.Limits[entitlement.LimitMaxCIs] != 250 {
			t.Errorf("configured max_cis not applied: %+v", e)
		}
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Acme GmbH":                      "acme-gmbh",
		"  Müller & Söhne – Straße 1 ":   "muller-sohne-strasse-1",
		"!!!":                            "org",
		"a--b__c":                        "a-b-c",
		strings.Repeat("abcdefghij", 10): "abcdefghijabcdefghijabcdefghijabcdefghijabcdefgh",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandlerStatusCodes(t *testing.T) {
	repo, mailer, accounts := newStubRepo(), &stubMailer{}, &stubAccounts{}
	h := NewHandler(newTestService(repo, mailer, accounts))
	call := func(handler http.HandlerFunc, body string) int {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		return w.Code
	}

	if code := call(NewHandler(nil).Submit, `{"organization_name":"Acme","email":"a@example.com"}`); code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured submit = %d, want 503", code)
	}
	if code := call(h.Submit, `{"organization_name":"Acme","email":"a@example.com"}`); code != http.StatusAccepted {
		t.Errorf("submit = %d, want 202", code)
	}
	if code := call(h.Submit, `{`); code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d, want 400", code)
	}
	if code := call(h.Verify, `{"token":"unknown","password":"correct horse battery"}`); code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", code)
	}

	accounts.err = ErrAccountExists
	token := tokenOf(t, mailer.link)
	if code := call(h.Verify, `{"token":"`+token+`","password":"correct horse battery"}`); code != http.StatusConflict {
		t.Errorf("existing account = %d, want 409", code)
	}
	accounts.err = nil
	if code := call(h.Verify, `{"token":"`+token+`","password":"correct horse battery"}`); code != http.StatusCreated {
		t.Errorf("verify = %d, want 201", code)
	}
}
