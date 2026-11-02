package signup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
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
	spec             *OrganizationSpec
	audit            *operator.AuditEntry
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

func (r *stubRepo) CreateOrganization(ctx context.Context, spec *OrganizationSpec, setup *Setup,
	createAccount func(ctx context.Context) error, audit *operator.AuditEntry) (Result, error) {
	if err := createAccount(ctx); err != nil {
		return Result{}, err
	}
	if r.failAfterAccount != nil {
		return Result{}, r.failAfterAccount
	}
	r.setup, r.spec, r.audit = setup, spec, audit
	slug := spec.Slug
	if slug == "" {
		slug = Slugify(spec.Name)
	}
	return Result{OrganizationID: setup.OrganizationID, OrganizationSlug: slug, Plan: setup.Plan}, nil
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
	// Essential features stay after the trial, standard-only ones carry
	// the end of the trial (WP-244: the matrix of E-44, unchanged
	// TrialEntitlements).
	for _, f := range []string{entitlement.FeatureDiscovery, entitlement.FeatureAssetsBasic, entitlement.FeatureDocuments,
		entitlement.FeatureImport, entitlement.FeatureTicketingEssential} {
		if e, ok := byKey[f]; !ok || e.ValidUntil != nil {
			t.Errorf("essential feature %s: %+v, want a row without valid_until", f, e)
		}
	}
	for _, f := range []string{entitlement.FeatureTicketingStandard, entitlement.FeatureSignature, entitlement.FeatureStock,
		entitlement.FeatureStocktakeStandard, entitlement.FeatureBarcode, entitlement.FeatureGPSStandard,
		entitlement.FeatureWorkplace, entitlement.FeatureExportDATEV} {
		if v := byKey[f].ValidUntil; v == nil || !v.Equal(ends) {
			t.Errorf("%s valid_until = %v, want %v", f, v, ends)
		}
	}
	if _, ok := byKey[entitlement.FeatureInternalOrders]; ok {
		t.Error("the standard trial includes internal_orders")
	}

	cfg.TrialMaxCIs = 250
	ents, _ = TrialEntitlements(cfg, ends)
	for _, e := range ents {
		if e.FeatureKey == entitlement.FeatureCMDBCore && e.Limits[entitlement.LimitMaxCIs] != 250 {
			t.Errorf("configured max_cis not applied: %+v", e)
		}
	}
}

// storageOf returns the stored max_storage_mb per feature of the rows.
func storageOf(ents []entitlement.Entitlement) map[string]int64 {
	out := map[string]int64{}
	for _, e := range ents {
		if mb, ok := e.Limits[entitlement.LimitMaxStorageMB]; ok {
			out[e.FeatureKey] = mb
		}
	}
	return out
}

// TestNewOrganizationsGetThePlanStorage covers WP-706 (MGT-01, E-44, E-34):
// a new organization gets the max_storage_mb of its plan stored on
// documents (1,000 / 10,000 / 50,000), enterprise gets no entry, an
// operator's own limit wins, and the trial stores its plan's value.
func TestNewOrganizationsGetThePlanStorage(t *testing.T) {
	for plan, want := range map[entitlement.Plan]int64{entitlement.PlanEssential: 1000,
		entitlement.PlanStandard: 10000, entitlement.PlanPro: 50000, entitlement.PlanEnterprise: 0} {
		ents, err := PlanEntitlements(plan, nil)
		if err != nil {
			t.Fatalf("PlanEntitlements(%s): %v", plan, err)
		}
		got := storageOf(ents)
		switch {
		case want == 0 && len(got) != 0:
			t.Errorf("%s stores max_storage_mb %v, want none (unlimited)", plan, got)
		case want != 0 && (len(got) != 1 || got[entitlement.FeatureDocuments] != want):
			t.Errorf("%s stores max_storage_mb %v, want documents %d", plan, got, want)
		}
	}

	// An own limit of the operator wins, also on enterprise; other quotas
	// leave the plan's storage in place.
	ents, err := PlanEntitlements(entitlement.PlanStandard, map[string]int64{entitlement.LimitMaxStorageMB: 2500})
	if err != nil {
		t.Fatal(err)
	}
	if got := storageOf(ents); len(got) != 1 || got[entitlement.FeatureDocuments] != 2500 {
		t.Errorf("own limit: %v, want documents 2500", got)
	}
	ents, err = PlanEntitlements(entitlement.PlanEnterprise, map[string]int64{entitlement.LimitMaxStorageMB: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := storageOf(ents); got[entitlement.FeatureDocuments] != 0 || len(got) != 1 {
		t.Errorf("own limit on enterprise: %v, want documents 0", got)
	}
	ents, err = PlanEntitlements(entitlement.PlanPro, map[string]int64{entitlement.LimitMaxCIs: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := storageOf(ents); got[entitlement.FeatureDocuments] != 50000 {
		t.Errorf("pro with max_cis only: %v, want documents 50000", got)
	}

	// The trial stores the value of the trial plan.
	cfg := DefaultConfig()
	ends := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	trial, err := TrialEntitlements(cfg, ends)
	if err != nil {
		t.Fatal(err)
	}
	if got := storageOf(trial); len(got) != 1 || got[entitlement.FeatureDocuments] != 10000 {
		t.Errorf("standard trial: %v, want documents 10000", got)
	}
	cfg.TrialPlan = entitlement.PlanEnterprise
	if trial, err = TrialEntitlements(cfg, ends); err != nil || len(storageOf(trial)) != 0 {
		t.Errorf("enterprise trial: %v %v, want no max_storage_mb", storageOf(trial), err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Acme GmbH":                      "acme-gmbh",
		"  Müller & Söhne – Straße 1 ":   "muller-sohne-strasse-1",
		"!!!":                            "org",
		"a--b__c":                        "a-b-c",
		strings.Repeat("abcdefghij", 10): "abcdefghijabcdefghijabcdefghijabcdefghijabcdefgh",
		// F-303: diacritics, ligatures, Greek and Cyrillic transliterate;
		// other scripts leave nothing.
		"Café Ñandú":        "cafe-nandu",
		"Øresund Æble Łódź": "oresund-aeble-lodz",
		"ООО Ромашка-Юг":    "ooo-romashka-iug",
		"Ελληνική Εταιρεία": "elliniki-etaireia",
		"東京商事":              "org",
		"東京 Tokyo":          "tokyo",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandlerStatusCodes(t *testing.T) {
	repo, mailer, accounts := newStubRepo(), &stubMailer{}, &stubAccounts{}
	prot := newTestProtection(t, cache.NewMemoryStore())
	h := NewHandler(newTestService(repo, mailer, accounts)).WithProtection(prot)
	call := func(handler http.HandlerFunc, body string) int {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		return w.Code
	}

	if code := call(NewHandler(nil).Submit, `{"organization_name":"Acme","email":"a@example.com"}`); code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured submit = %d, want 503", code)
	}
	unprotected := NewHandler(newTestService(repo, mailer, accounts))
	if code := call(unprotected.Submit, `{"organization_name":"Acme","email":"a@example.com"}`); code != http.StatusServiceUnavailable {
		t.Errorf("submit without protection = %d, want 503", code)
	}
	if code := call(h.Submit, `{"organization_name":"Acme","email":"a@example.com","altcha":"`+solve(t, prot)+`"}`); code != http.StatusAccepted {
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

// stubInviter is an IdP client that also invites (operator path).
type stubInviter struct {
	stubAccounts
	invited []Account
}

func (a *stubInviter) InviteAccount(_ context.Context, acc Account) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	a.invited = append(a.invited, acc)
	return "kc-invite", nil
}

func TestOperatorCreationRunsTheSignupChain(t *testing.T) {
	repo, accounts := newStubRepo(), &stubInviter{}
	// No mailer: operator creation does not depend on the signup channel;
	// the invitation goes out through the IdP's mail.
	svc := newTestService(repo, nil, accounts).WithInvitationMail(true)
	op := operator.Identity{ID: "op-1", Kind: "oidc"}
	req := &operator.NewOrganization{Name: " Acme ", Plan: "pro", AdminEmail: "admin@acme.example",
		Limits: map[string]int64{entitlement.LimitMaxCIs: 5000, entitlement.LimitMaxCollectors: 4,
			entitlement.LimitMaxStorageMB: 10000}}
	created, err := svc.CreateOrganization(context.Background(), op, req)
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if created.Slug != "acme" || created.Plan != "pro" || !created.AdminInvited || created.ID != repo.setup.OrganizationID {
		t.Errorf("created = %+v", created)
	}
	if len(accounts.invited) != 1 || accounts.invited[0].OrganizationID != created.ID || accounts.invited[0].Password != "" {
		t.Errorf("invited = %+v", accounts.invited)
	}
	if repo.spec.AdminDisplayName != "admin@acme.example" || repo.setup.TrialEndsAt != (time.Time{}) {
		t.Errorf("spec %+v setup %+v", repo.spec, repo.setup)
	}
	if repo.audit.OperatorID != "op-1" || repo.audit.Action != "org.create" || repo.audit.Details["plan"] != "pro" {
		t.Errorf("audit = %+v", repo.audit)
	}
	limits := map[string]map[string]int64{}
	for _, e := range repo.setup.Entitlements {
		if e.Source != "manual" || e.Plan != entitlement.PlanPro || e.ValidUntil != nil {
			t.Errorf("%s: %+v", e.FeatureKey, e)
		}
		limits[e.FeatureKey] = e.Limits
	}
	if limits[entitlement.FeatureCMDBCore][entitlement.LimitMaxCIs] != 5000 || limits[entitlement.FeatureDiscovery][entitlement.LimitMaxCollectors] != 4 {
		t.Errorf("limits = %v", limits)
	}
	// WP-244 (MGT-01, E-44): max_storage_mb is stored on documents only.
	for feature, l := range limits {
		if _, ok := l[entitlement.LimitMaxStorageMB]; ok != (feature == entitlement.FeatureDocuments) {
			t.Errorf("max_storage_mb on %s: %v", feature, l)
		}
	}
	if limits[entitlement.FeatureDocuments][entitlement.LimitMaxStorageMB] != 10000 {
		t.Errorf("documents limits = %v, want max_storage_mb 10000", limits[entitlement.FeatureDocuments])
	}
	if _, err := PlanEntitlements(entitlement.PlanEssential, map[string]int64{entitlement.LimitMaxStorageMB: 1000}); err != nil {
		t.Errorf("max_storage_mb on the essential plan: %v", err)
	}

	// Without an inviting IdP client the organization is still created.
	plain := newTestService(newStubRepo(), nil, &stubAccounts{}).WithInvitationMail(true)
	if created, err := plain.CreateOrganization(context.Background(), op, req); err != nil || created.AdminInvited {
		t.Errorf("without inviter: %+v %v", created, err)
	}

	// A failing transaction deletes the invited account again.
	repo.failAfterAccount = errors.New("commit failed")
	if _, err := svc.CreateOrganization(context.Background(), op, req); err == nil {
		t.Fatal("creation succeeded")
	}
	if len(accounts.deleted) != 1 || accounts.deleted[0] != "kc-invite" {
		t.Errorf("deleted = %v", accounts.deleted)
	}

	// An existing IdP account is a conflict on admin_email.
	repo.failAfterAccount, accounts.err = nil, ErrAccountExists
	var ce *api.ConflictError
	if _, err := svc.CreateOrganization(context.Background(), op, req); !errors.As(err, &ce) || ce.Field != "admin_email" {
		t.Errorf("existing account: %v", err)
	}
}

// TestChallengeAnswersUnavailableUntilConfigured: GET /signup/challenge is
// the first request of the signup form, so an instance without one of the
// prerequisites (central SMTP server → mailer, Keycloak admin client →
// accounts, verify URL) answers it with 503 and the fixed detail of
// ErrUnavailable, which the onboarding page maps to "not set up on this
// server" (OnboardingPage.tsx, SIGNUP_UNAVAILABLE_DETAIL). The 503 of a
// failing protection store has another detail: the page shows it as a
// passing outage.
func TestChallengeAnswersUnavailableUntilConfigured(t *testing.T) {
	repo := newStubRepo()
	p := newTestProtection(t, cache.NewMemoryStore())
	detailOf := func(t *testing.T, w *httptest.ResponseRecorder) string {
		t.Helper()
		var pd struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &pd); err != nil {
			t.Fatalf("problem body %q: %v", w.Body.String(), err)
		}
		return pd.Detail
	}
	for name, svc := range map[string]*Service{
		"no mailer (no SMTP)":           newTestService(repo, nil, &stubAccounts{}),
		"no accounts (no admin client)": newTestService(repo, &stubMailer{}, nil),
		"no verify URL":                 NewService(repo, &stubMailer{}, &stubAccounts{}, DefaultConfig()),
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			NewHandler(svc).WithProtection(p).Challenge(w, httptest.NewRequest(http.MethodGet, "/api/v1/signup/challenge", nil))
			if w.Code != http.StatusServiceUnavailable || detailOf(t, w) != "self-signup is not available" {
				t.Errorf("challenge = %d %s, want 503 with the detail of ErrUnavailable", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	NewHandler(newTestService(repo, &stubMailer{}, &stubAccounts{})).WithProtection(p).
		Challenge(w, httptest.NewRequest(http.MethodGet, "/api/v1/signup/challenge", nil))
	if w.Code != http.StatusOK {
		t.Errorf("configured challenge = %d %s, want 200", w.Code, w.Body.String())
	}

	down := NewHandler(newTestService(repo, &stubMailer{}, &stubAccounts{})).WithProtection(newTestProtection(t, failingStore{}))
	w = httptest.NewRecorder()
	down.Submit(w, httptest.NewRequest(http.MethodPost, "/api/v1/signup",
		strings.NewReader(`{"organization_name":"Acme","email":"a@example.com","altcha":"x"}`)))
	if w.Code != http.StatusServiceUnavailable || detailOf(t, w) == "self-signup is not available" {
		t.Errorf("store down = %d %s, want 503 with another detail than ErrUnavailable", w.Code, w.Body.String())
	}
}

// TestOperatorCreationWithoutIdPMailDoesNotInvite: with the Keycloak admin
// client but without the IdP's mail (no central SMTP server, so the realm
// has no SMTP settings either) the operator path must not call the
// inviting client. Its execute-actions-email would fail and roll back the
// whole creation; instead the organization is created as without an
// inviting client, and the response and the audit say that nobody was
// invited.
func TestOperatorCreationWithoutIdPMailDoesNotInvite(t *testing.T) {
	op := operator.Identity{ID: "op-1", Kind: "oidc"}
	req := &operator.NewOrganization{Name: "Acme", Plan: "standard", AdminEmail: "admin@acme.example"}
	for name, svc := range map[string]func(Repository, Accounts) *Service{
		"default": func(r Repository, a Accounts) *Service { return newTestService(r, nil, a) },
		"disabled": func(r Repository, a Accounts) *Service {
			return newTestService(r, nil, a).WithInvitationMail(false)
		},
		"disabled after enabled": func(r Repository, a Accounts) *Service {
			return newTestService(r, nil, a).WithInvitationMail(true).WithInvitationMail(false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newStubRepo()
			// The IdP would refuse the mail: no SMTP server in the realm.
			accounts := &stubInviter{stubAccounts: stubAccounts{err: errors.New("keycloak: invite: status 500")}}
			created, err := svc(repo, accounts).CreateOrganization(context.Background(), op, req)
			if err != nil {
				t.Fatalf("CreateOrganization: %v", err)
			}
			if created.AdminInvited || created.ID == "" || created.Slug != "acme" {
				t.Errorf("created = %+v, want an organization without invitation", created)
			}
			if len(accounts.invited) != 0 || len(accounts.created) != 0 || len(accounts.deleted) != 0 {
				t.Errorf("IdP calls: invited %v created %v deleted %v, want none", accounts.invited, accounts.created, accounts.deleted)
			}
			if repo.audit == nil || repo.audit.Details["admin_invited"] != false {
				t.Errorf("audit = %+v, want admin_invited false", repo.audit)
			}
		})
	}

	// With the IdP's mail the same refusal fails the creation, which then
	// leaves nothing behind (the repository rolls back).
	repo := newStubRepo()
	accounts := &stubInviter{stubAccounts: stubAccounts{err: errors.New("keycloak: invite: status 500")}}
	if _, err := newTestService(repo, nil, accounts).WithInvitationMail(true).CreateOrganization(context.Background(), op, req); err == nil {
		t.Fatal("creation succeeded although the invitation failed")
	}
	if repo.setup != nil {
		t.Errorf("organization stored after a failed invitation: %+v", repo.setup)
	}

	// A nil service stays nil.
	var none *Service
	if none.WithInvitationMail(true) != nil {
		t.Error("WithInvitationMail on a nil service returned a service")
	}
}

func TestOperatorCreationValidates(t *testing.T) {
	svc := newTestService(newStubRepo(), nil, nil)
	valid := func() *operator.NewOrganization {
		return &operator.NewOrganization{Name: "Acme", Plan: "standard", AdminEmail: "a@example.com"}
	}
	for field, mutate := range map[string]func(*operator.NewOrganization){
		"name":        func(r *operator.NewOrganization) { r.Name = " " },
		"slug":        func(r *operator.NewOrganization) { r.Slug = "Not A Slug" },
		"plan":        func(r *operator.NewOrganization) { r.Plan = "gold" },
		"admin_email": func(r *operator.NewOrganization) { r.AdminEmail = "nope" },
		"limits":      func(r *operator.NewOrganization) { r.Limits = map[string]int64{"max_cpus": 1} },
	} {
		req := valid()
		mutate(req)
		var fe *api.FieldError
		if _, err := svc.CreateOrganization(context.Background(), operator.Identity{}, req); !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	req := valid()
	req.Limits = map[string]int64{entitlement.LimitMaxUsers: -1}
	var fe *api.FieldError
	if _, err := svc.CreateOrganization(context.Background(), operator.Identity{}, req); !errors.As(err, &fe) || fe.Field != "limits" {
		t.Errorf("negative limit: %v", err)
	}
	if _, err := NewService(nil, nil, nil, DefaultConfig()).CreateOrganization(context.Background(), operator.Identity{}, valid()); !errors.Is(err, operator.ErrCreationUnavailable) {
		t.Errorf("without repository: %v", err)
	}
}
