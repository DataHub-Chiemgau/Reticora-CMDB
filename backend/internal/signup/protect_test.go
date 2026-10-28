package signup

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
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

// TestSignupIPLimitSlidingWindow covers WP-591 (TLC-01, F-304): the IP
// budget is a sliding window, not a clock hour. With an injected clock,
// six signups at 10:59:30 and six at 11:00:31 from one address admit five
// in total; every refusal carries a Retry-After of the time until the
// oldest signup leaves the hour (11:59:30), not a constant 3600. At
// 11:59:29 the address is still refused, at 11:59:30 it has a new budget.
// The window lives in the shared store: on Redis two replicas, each with
// its own connection, count the same window.
func TestSignupIPLimitSlidingWindow(t *testing.T) {
	t.Run("memory store", func(t *testing.T) {
		store := cache.NewMemoryStore()
		testSignupIPLimitSlidingWindow(t, store, store)
	})
	t.Run("redis store", func(t *testing.T) {
		testSignupIPLimitSlidingWindow(t, redisStore(t), redisStore(t))
	})
}

func testSignupIPLimitSlidingWindow(t *testing.T, storeA, storeB cache.Store) {
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	clock := day.Add(10*time.Hour + 59*time.Minute + 30*time.Second)
	replicas := []*Protection{newTestProtection(t, storeA), newTestProtection(t, storeB)}
	for _, p := range replicas {
		p.now = func() time.Time { return clock }
	}
	// An address of its own per run: the Redis window outlives the test.
	nonce := make([]byte, 4)
	_, _ = rand.Read(nonce)
	ip := "2001:db8::" + hex.EncodeToString(nonce[:2]) + ":" + hex.EncodeToString(nonce[2:])
	t.Cleanup(func() { _ = storeA.Delete(context.Background(), "signup:ipwindow:"+ip) })

	ctx := context.Background()
	accepted := 0
	// burst submits n signups alternately through both replicas; each
	// refusal must name the remaining time until the oldest accepted
	// signup (10:59:30) leaves the window.
	burst := func(n int, at time.Time) {
		t.Helper()
		clock = at
		remaining := day.Add(11*time.Hour + 59*time.Minute + 30*time.Second).Sub(at)
		for i := 0; i < n; i++ {
			p := replicas[i%len(replicas)]
			err := p.Check(ctx, ip, solve(t, p), "a@example.com")
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrSignupRateLimit):
				w := httptest.NewRecorder()
				writeProtectionError(w, httptest.NewRequest(http.MethodPost, "/api/v1/signup", nil), err)
				retry, perr := strconv.Atoi(w.Header().Get("Retry-After"))
				if perr != nil || time.Duration(retry)*time.Second > remaining || time.Duration(retry)*time.Second < remaining-time.Second {
					t.Errorf("%s: Retry-After = %q, want the remaining %v until the oldest signup leaves the window",
						at.Format("15:04:05"), w.Header().Get("Retry-After"), remaining)
				}
			default:
				t.Fatalf("%s: signup %d: %v", at.Format("15:04:05"), i+1, err)
			}
		}
	}

	burst(6, clock)
	if accepted != SignupsPerIPHour {
		t.Fatalf("10:59:30: %d signups accepted, want %d", accepted, SignupsPerIPHour)
	}
	burst(6, day.Add(11*time.Hour+31*time.Second))
	if accepted != SignupsPerIPHour {
		t.Errorf("10:59:30 and 11:00:31: %d signups accepted within one hour, want at most %d", accepted, SignupsPerIPHour)
	}
	burst(1, day.Add(11*time.Hour+59*time.Minute+29*time.Second))
	if accepted != SignupsPerIPHour {
		t.Errorf("11:59:29: %d signups accepted, the 10:59:30 signups are still in the window", accepted)
	}
	// 11:59:30: the five signups of 10:59:30 left the window.
	clock = day.Add(11*time.Hour + 59*time.Minute + 30*time.Second)
	for i := 0; i < SignupsPerIPHour; i++ {
		p := replicas[i%len(replicas)]
		if err := p.Check(ctx, ip, solve(t, p), "a@example.com"); err != nil {
			t.Errorf("11:59:30: signup %d: %v", i+1, err)
		}
	}
	if used, err := replicas[0].UsedByIP(ctx, ip); err != nil || used != SignupsPerIPHour {
		t.Errorf("11:59:30: used = %d (%v), want %d", used, err, SignupsPerIPHour)
	}
}

// redisStore connects to TEST_REDIS_URL with a connection of its own (a
// replica); without it the test is skipped.
func redisStore(t *testing.T) cache.Store {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; skipping Redis signup window test (e.g. redis://localhost:56379/0)")
	}
	client, err := redisx.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return cache.NewRedisStore(client.Unwrap())
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

func (failingStore) WindowAdd(context.Context, string, time.Time, time.Duration, int64) (cache.Window, error) {
	return cache.Window{}, errors.New("down")
}

func (failingStore) WindowCount(context.Context, string, time.Time, time.Duration) (cache.Window, error) {
	return cache.Window{}, errors.New("down")
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
	// The five signups just happened: the oldest leaves the window in
	// (almost) an hour.
	if retry, err := strconv.Atoi(limited.Header().Get("Retry-After")); err != nil || retry < 3500 || retry > 3600 {
		t.Errorf("429 Retry-After = %q, want the seconds until the oldest signup leaves the hour", limited.Header().Get("Retry-After"))
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
