package signup

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// Signup protection (TLC-01, E-33): a self-hosted ALTCHA proof of work, the
// disposable-email-domains blocklist with allowlist and operator entries,
// and at most SignupsPerIPHour signups per client IP and hour in addition to
// the pre-auth limit (AUT-10, middleware.PreAuthRateLimiter).

// SignupsPerIPHour is the signup budget of a client IP (E-33).
const SignupsPerIPHour = 5

// Proof-of-work parameters of the ALTCHA challenge. maxNumber bounds the
// search space (about a second in a browser); challenges expire after
// challengeTTL and are accepted once.
const (
	altchaAlgorithm = "SHA-256"
	maxNumber       = 100_000
	challengeTTL    = 10 * time.Minute
)

// Errors of the protection; each answers RFC 7807 (writeProtectionError).
var (
	ErrCaptchaInvalid  = errors.New("the captcha solution is missing, invalid, expired or was already used")
	ErrDomainBlocked   = errors.New("e-mail addresses of disposable domains are not accepted")
	ErrSignupRateLimit = errors.New("too many signups from this address, try again later")
)

//go:embed disposable/blocklist.conf disposable/allowlist.conf
var disposableLists embed.FS

// Challenge is an ALTCHA challenge (GET /api/v1/signup/challenge).
type Challenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int64  `json:"maxnumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

// altchaPayload is the solution the widget submits, base64-encoded JSON.
type altchaPayload struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int64  `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

// Protection checks a signup request before it is stored.
type Protection struct {
	hmacKey []byte
	store   cache.Store
	domains *DomainPolicy
	now     func() time.Time
}

// NewProtection creates the protection. hmacKey signs the challenges (all
// instances need the same key); store holds the used challenges and the
// per-IP counters (shared, e.g. Redis).
func NewProtection(hmacKey []byte, store cache.Store, domains *DomainPolicy) (*Protection, error) {
	if len(hmacKey) < 32 {
		return nil, errors.New("signup protection: the captcha HMAC key needs at least 32 bytes")
	}
	if store == nil || domains == nil {
		return nil, errors.New("signup protection: store and domain policy are required")
	}
	return &Protection{hmacKey: hmacKey, store: store, domains: domains, now: time.Now}, nil
}

// NewChallenge issues a signed challenge.
func (p *Protection) NewChallenge() (Challenge, error) {
	saltBytes := make([]byte, 12)
	if _, err := rand.Read(saltBytes); err != nil {
		return Challenge{}, fmt.Errorf("altcha salt: %w", err)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(maxNumber+1))
	if err != nil {
		return Challenge{}, fmt.Errorf("altcha number: %w", err)
	}
	salt := hex.EncodeToString(saltBytes) + "?expires=" + strconv.FormatInt(p.now().Add(challengeTTL).Unix(), 10)
	challenge := altchaHash(salt, n.Int64())
	return Challenge{Algorithm: altchaAlgorithm, Challenge: challenge, MaxNumber: maxNumber,
		Salt: salt, Signature: p.sign(challenge)}, nil
}

// VerifySolution checks an ALTCHA payload: the signature is ours, the
// number solves the challenge, the challenge has not expired and was not
// used before.
func (p *Protection) VerifySolution(ctx context.Context, payload string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil || len(raw) == 0 {
		return ErrCaptchaInvalid
	}
	var sol altchaPayload
	if err = json.Unmarshal(raw, &sol); err != nil {
		return ErrCaptchaInvalid
	}
	if sol.Algorithm != altchaAlgorithm || sol.Number < 0 || sol.Number > maxNumber ||
		!constantEqual(sol.Signature, p.sign(sol.Challenge)) ||
		!constantEqual(sol.Challenge, altchaHash(sol.Salt, sol.Number)) {
		return ErrCaptchaInvalid
	}
	expires, ok := saltExpiry(sol.Salt)
	if !ok || !p.now().Before(expires) {
		return ErrCaptchaInvalid
	}
	// Single use: the first verification creates the key.
	uses, err := p.store.Increment(ctx, "signup:altcha:"+sol.Challenge, challengeTTL+time.Minute)
	if err != nil {
		return fmt.Errorf("signup captcha store: %w", err)
	}
	if uses > 1 {
		return ErrCaptchaInvalid
	}
	return nil
}

// UsedByIP returns the signups of the client IP in the current hour.
func (p *Protection) UsedByIP(ctx context.Context, ip string) (int64, error) {
	value, found, err := p.store.Get(ctx, p.ipKey(ip))
	if err != nil || !found {
		return 0, err
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// CountIP counts a signup of the client IP; ErrSignupRateLimit when it
// exceeds the budget.
func (p *Protection) CountIP(ctx context.Context, ip string) error {
	n, err := p.store.Increment(ctx, p.ipKey(ip), 2*time.Hour)
	if err != nil {
		return fmt.Errorf("signup rate limit store: %w", err)
	}
	if n > SignupsPerIPHour {
		return ErrSignupRateLimit
	}
	return nil
}

// Check runs the protection of one signup request in order: IP budget,
// captcha, disposable domain; then it counts the signup against the IP.
func (p *Protection) Check(ctx context.Context, ip, captcha, email string) error {
	used, err := p.UsedByIP(ctx, ip)
	if err != nil {
		return fmt.Errorf("signup rate limit store: %w", err)
	}
	if used >= SignupsPerIPHour {
		return ErrSignupRateLimit
	}
	if verr := p.VerifySolution(ctx, captcha); verr != nil {
		return verr
	}
	if _, domain, ok := strings.Cut(strings.TrimSpace(email), "@"); ok && p.domains.Blocked(domain) {
		return ErrDomainBlocked
	}
	return p.CountIP(ctx, ip)
}

func (p *Protection) ipKey(ip string) string {
	return fmt.Sprintf("signup:ip:%s:%d", ip, p.now().UTC().Unix()/3600)
}

func (p *Protection) sign(challenge string) string {
	mac := hmac.New(sha256.New, p.hmacKey)
	mac.Write([]byte(challenge))
	return hex.EncodeToString(mac.Sum(nil))
}

func altchaHash(salt string, number int64) string {
	sum := sha256.Sum256([]byte(salt + strconv.FormatInt(number, 10)))
	return hex.EncodeToString(sum[:])
}

func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// saltExpiry reads the expires parameter ALTCHA appends to the salt.
func saltExpiry(salt string) (time.Time, bool) {
	_, query, ok := strings.Cut(salt, "?")
	if !ok {
		return time.Time{}, false
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return time.Time{}, false
	}
	unix, err := strconv.ParseInt(values.Get("expires"), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(unix, 0), true
}

// DomainPolicy decides whether an e-mail domain is disposable: listed in
// the blocklist (or a parent domain is) and neither it nor a parent domain
// is allowed. The allowlist wins.
type DomainPolicy struct {
	blocked map[string]bool
	allowed map[string]bool
}

// NewDomainPolicy combines the embedded lists (E-33) with the operator
// entries.
func NewDomainPolicy(extraBlocked, extraAllowed []string) (*DomainPolicy, error) {
	p := &DomainPolicy{blocked: map[string]bool{}, allowed: map[string]bool{}}
	for file, set := range map[string]map[string]bool{
		"disposable/blocklist.conf": p.blocked,
		"disposable/allowlist.conf": p.allowed,
	} {
		f, err := disposableLists.Open(file)
		if err != nil {
			return nil, fmt.Errorf("signup domain list %s: %w", file, err)
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			addDomain(set, scanner.Text())
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("signup domain list %s: %w", file, err)
		}
	}
	for _, d := range extraBlocked {
		addDomain(p.blocked, d)
	}
	for _, d := range extraAllowed {
		addDomain(p.allowed, d)
	}
	return p, nil
}

func addDomain(set map[string]bool, line string) {
	d := strings.ToLower(strings.TrimSpace(line))
	if d == "" || strings.HasPrefix(d, "#") {
		return
	}
	set[strings.Trim(d, ".")] = true
}

// Blocked reports whether domain is disposable.
func (p *DomainPolicy) Blocked(domain string) bool {
	d := strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	blocked := false
	for d != "" {
		if p.allowed[d] {
			return false
		}
		if p.blocked[d] {
			blocked = true
		}
		_, parent, ok := strings.Cut(d, ".")
		if !ok {
			break
		}
		d = parent
	}
	return blocked
}

// writeProtectionError answers a protection error with RFC 7807 and
// reports whether err was one.
func writeProtectionError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, ErrSignupRateLimit):
		w.Header().Set("Retry-After", "3600")
		httpx.RateLimited(w, r, err.Error())
	case errors.Is(err, ErrCaptchaInvalid):
		api.WriteFieldError(w, &api.FieldError{Field: "altcha", Message: err.Error()})
	case errors.Is(err, ErrDomainBlocked):
		api.WriteFieldError(w, &api.FieldError{Field: "email", Message: err.Error(), Status: http.StatusUnprocessableEntity})
	default:
		return false
	}
	return true
}
