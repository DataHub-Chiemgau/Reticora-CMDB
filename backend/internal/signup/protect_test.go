package signup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

var testCaptchaKey = []byte("0123456789abcdef0123456789abcdef")

func newTestProtection(t *testing.T, store cache.Store, extra ...string) *Protection {
	t.Helper()
	domains, err := NewDomainPolicy(extra, []string{"allowed.mailinator.com"})
	if err != nil {
		t.Fatalf("NewDomainPolicy: %v", err)
	}
	p, err := NewProtection(testCaptchaKey, store, domains)
	if err != nil {
		t.Fatalf("NewProtection: %v", err)
	}
	return p
}

// solveChallenge brute-forces a challenge like the ALTCHA widget does.
func solveChallenge(t *testing.T, c Challenge) string {
	t.Helper()
	for n := int64(0); n <= c.MaxNumber; n++ {
		if altchaHash(c.Salt, n) == c.Challenge {
			raw, _ := json.Marshal(altchaPayload{Algorithm: c.Algorithm, Challenge: c.Challenge, Number: n,
				Salt: c.Salt, Signature: c.Signature})
			return base64.StdEncoding.EncodeToString(raw)
		}
	}
	t.Fatal("challenge has no solution")
	return ""
}

func solve(t *testing.T, p *Protection) string {
	t.Helper()
	c, err := p.NewChallenge()
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	return solveChallenge(t, c)
}

func TestAltchaSolutionIsVerifiedOnce(t *testing.T) {
	p := newTestProtection(t, cache.NewMemoryStore())
	c, err := p.NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if c.Algorithm != "SHA-256" || c.MaxNumber != maxNumber || !strings.Contains(c.Salt, "?expires=") {
		t.Fatalf("challenge = %+v", c)
	}
	payload := solveChallenge(t, c)
	ctx := context.Background()
	if err := p.VerifySolution(ctx, payload); err != nil {
		t.Fatalf("valid solution rejected: %v", err)
	}
	if err := p.VerifySolution(ctx, payload); !errors.Is(err, ErrCaptchaInvalid) {
		t.Errorf("replayed solution: %v, want ErrCaptchaInvalid", err)
	}
}

func TestAltchaRejectsForgedSolutions(t *testing.T) {
	p := newTestProtection(t, cache.NewMemoryStore())
	ctx := context.Background()
	c, _ := p.NewChallenge()
	var sol altchaPayload
	raw, _ := base64.StdEncoding.DecodeString(solveChallenge(t, c))
	_ = json.Unmarshal(raw, &sol)
	encode := func(s altchaPayload) string {
		b, _ := json.Marshal(s)
		return base64.StdEncoding.EncodeToString(b)
	}

	wrongNumber := sol
	wrongNumber.Number = (sol.Number + 1) % (maxNumber + 1)
	selfSigned := sol
	selfSigned.Salt = "deadbeef?expires=9999999999"
	selfSigned.Challenge = altchaHash(selfSigned.Salt, 1)
	selfSigned.Number = 1
	otherKey, _ := NewProtection([]byte("ffffffffffffffffffffffffffffffff"), cache.NewMemoryStore(), p.domains)
	otherSig := sol
	otherSig.Signature = otherKey.sign(sol.Challenge)
	wrongAlgo := sol
	wrongAlgo.Algorithm = "SHA-1"

	for name, payload := range map[string]string{
		"empty":         "",
		"not base64":    "%%%",
		"not JSON":      base64.StdEncoding.EncodeToString([]byte("nope")),
		"wrong number":  encode(wrongNumber),
		"own challenge": encode(selfSigned),
		"foreign key":   encode(otherSig),
		"algorithm":     encode(wrongAlgo),
	} {
		if err := p.VerifySolution(ctx, payload); !errors.Is(err, ErrCaptchaInvalid) {
			t.Errorf("%s: %v, want ErrCaptchaInvalid", name, err)
		}
	}

	// Expired: the challenge is valid but its time is over.
	p.now = func() time.Time { return time.Now().Add(challengeTTL + time.Minute) }
	if err := p.VerifySolution(ctx, encode(sol)); !errors.Is(err, ErrCaptchaInvalid) {
		t.Errorf("expired: %v, want ErrCaptchaInvalid", err)
	}
}

func TestDomainPolicy(t *testing.T) {
	p, err := NewDomainPolicy([]string{"Spam.Example"}, []string{"allowed.mailinator.com", " "})
	if err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]bool{
		"mailinator.com":           true, // upstream blocklist
		"MAILINATOR.COM":           true,
		"eu.mailinator.com":        true, // subdomain of a listed domain
		"allowed.mailinator.com":   false,
		"x.allowed.mailinator.com": false, // the allowlist wins for subdomains
		"spam.example":             true,  // operator entry
		"a.spam.example":           true,
		"example.com":              false,
		"gmail.com":                false,
		"":                         false,
	} {
		if got := p.Blocked(domain); got != want {
			t.Errorf("Blocked(%q) = %v, want %v", domain, got, want)
		}
	}
}

func TestProtectionLimitsSignupsPerIP(t *testing.T) {
	p := newTestProtection(t, cache.NewMemoryStore())
	ctx := context.Background()
	for i := 0; i < SignupsPerIPHour; i++ {
		if err := p.Check(ctx, "203.0.113.7", solve(t, p), "a@example.com"); err != nil {
			t.Fatalf("signup %d: %v", i+1, err)
		}
	}
	if err := p.Check(ctx, "203.0.113.7", solve(t, p), "a@example.com"); !errors.Is(err, ErrSignupRateLimit) {
		t.Errorf("signup %d: %v, want ErrSignupRateLimit", SignupsPerIPHour+1, err)
	}
	if err := p.Check(ctx, "203.0.113.8", solve(t, p), "a@example.com"); err != nil {
		t.Errorf("other IP: %v", err)
	}
	// The next hour has a fresh budget.
	p.now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := p.Check(ctx, "203.0.113.7", solve(t, p), "a@example.com"); err != nil {
		t.Errorf("next hour: %v", err)
	}
}

func TestProtectionRejectsWithoutCountingFailedChecks(t *testing.T) {
	p := newTestProtection(t, cache.NewMemoryStore())
	ctx := context.Background()
	if err := p.Check(ctx, "198.51.100.1", "", "a@example.com"); !errors.Is(err, ErrCaptchaInvalid) {
		t.Errorf("missing captcha: %v", err)
	}
	if err := p.Check(ctx, "198.51.100.1", solve(t, p), "a@mailinator.com"); !errors.Is(err, ErrDomainBlocked) {
		t.Errorf("disposable domain: %v", err)
	}
	if used, _ := p.UsedByIP(ctx, "198.51.100.1"); used != 0 {
		t.Errorf("rejected attempts counted: %d", used)
	}
}

// failingStore fails every operation (Redis down).
type failingStore struct{ cache.Store }

func (failingStore) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("down")
}

func (failingStore) Increment(context.Context, string, time.Duration) (int64, error) {
	return 0, errors.New("down")
}

func TestSignupHandlerProtectionAnswersRFC7807(t *testing.T) {
	repo, mailer, accounts := newStubRepo(), &stubMailer{}, &stubAccounts{}
	p := newTestProtection(t, cache.NewMemoryStore())
	h := NewHandler(newTestService(repo, mailer, accounts)).WithProtection(p)
	submit := func(h *Handler, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
		r.RemoteAddr = "192.0.2.10:4711"
		h.Submit(w, r)
		return w
	}
	body := func(email, altcha string) string {
		return `{"organization_name":"Acme","email":"` + email + `","altcha":"` + altcha + `"}`
	}
	problem := func(name string, w *httptest.ResponseRecorder, status int, field string) {
		t.Helper()
		if w.Code != status || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("%s: %d %s, want %d problem+json", name, w.Code, w.Header().Get("Content-Type"), status)
			return
		}
		var pd struct {
			Status int    `json:"status"`
			Field  string `json:"field"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &pd); err != nil || pd.Status != status || pd.Field != field {
			t.Errorf("%s: problem %s", name, w.Body.String())
		}
	}

	problem("missing captcha", submit(h, body("a@example.com", "")), http.StatusBadRequest, "altcha")
	problem("disposable domain", submit(h, body("a@mailinator.com", solve(t, p))), http.StatusUnprocessableEntity, "email")
	if mailer.to != "" {
		t.Fatal("a rejected signup was mailed")
	}

	// Challenge endpoint.
	w := httptest.NewRecorder()
	h.Challenge(w, httptest.NewRequest(http.MethodGet, "/api/v1/signup/challenge", nil))
	var c Challenge
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatalf("challenge: %d %s", w.Code, w.Body.String())
	}
	if got := submit(h, body("a@example.com", solveChallenge(t, c))); got.Code != http.StatusAccepted {
		t.Fatalf("protected submit: %d %s", got.Code, got.Body.String())
	}
	for i := 1; i < SignupsPerIPHour; i++ {
		if got := submit(h, body("a@example.com", solve(t, p))); got.Code != http.StatusAccepted {
			t.Fatalf("submit %d: %d", i+1, got.Code)
		}
	}
	limited := submit(h, body("a@example.com", solve(t, p)))
	problem("rate limit", limited, http.StatusTooManyRequests, "")
	if limited.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}

	// A failing store fails closed.
	down := NewHandler(newTestService(repo, mailer, accounts)).WithProtection(newTestProtection(t, failingStore{}))
	if got := submit(down, body("a@example.com", "x")); got.Code != http.StatusServiceUnavailable {
		t.Errorf("store down: %d, want 503", got.Code)
	}
	w = httptest.NewRecorder()
	NewHandler(nil).Challenge(w, httptest.NewRequest(http.MethodGet, "/api/v1/signup/challenge", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured challenge: %d, want 503", w.Code)
	}
}

func TestNewProtectionRequiresKeyAndStore(t *testing.T) {
	domains, _ := NewDomainPolicy(nil, nil)
	if _, err := NewProtection([]byte("short"), cache.NewMemoryStore(), domains); err == nil {
		t.Error("short key accepted")
	}
	if _, err := NewProtection(testCaptchaKey, nil, domains); err == nil {
		t.Error("nil store accepted")
	}
}
