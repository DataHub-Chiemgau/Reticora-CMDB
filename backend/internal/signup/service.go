// Package signup implements self-signup (TLC-01, CH23, WP-111): a request
// with organization name and e-mail address, an e-mail verification link,
// and on verification the atomic creation of the organization with slug,
// default VRF, first org_admin and trial entitlements (ENT-07), recorded in
// operator_audit. The password is entered on verification and set directly
// in the IdP; Reticora never stores it.
package signup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// Errors of the signup flow.
var (
	// ErrUnavailable: no e-mail channel or no IdP administration is
	// configured, so no signup can be verified.
	ErrUnavailable = errors.New("self-signup is not available")
	// ErrInvalidToken: the verification token is unknown or already used.
	ErrInvalidToken = errors.New("the verification link is invalid or was already used")
	// ErrExpired: the verification token has expired.
	ErrExpired = errors.New("the verification link has expired")
	// ErrAccountExists: the IdP already has an account for the address.
	ErrAccountExists = errors.New("an account with this e-mail address already exists")
)

// Minimum password length checked before the IdP applies its own policy.
const minPasswordLength = 12

// Config holds the trial values and the verification settings (E-33, V).
type Config struct {
	// TrialPlan is the plan of the trial (ENT-07: standard).
	TrialPlan entitlement.Plan
	// FallbackPlan is the plan after the trial (ENT-07: essential); features
	// of the trial plan that it lacks expire at the trial's end.
	FallbackPlan entitlement.Plan
	// TrialDays is the trial length (ENT-07: 30).
	TrialDays int
	// TrialMaxCIs is the max_cis quota during the trial (ENT-07: 100).
	TrialMaxCIs int64
	// TokenTTL is the validity of a verification link.
	TokenTTL time.Duration
	// VerifyURL is the page the verification link opens; the token is
	// appended as query parameter "token".
	VerifyURL string
}

// DefaultConfig returns the values of ENT-07 and E-33.
func DefaultConfig() Config {
	return Config{
		TrialPlan:    entitlement.PlanStandard,
		FallbackPlan: entitlement.PlanEssential,
		TrialDays:    30,
		TrialMaxCIs:  100,
		TokenTTL:     24 * time.Hour,
	}
}

// Mailer sends the verification e-mail. Until the notification channels
// exist (WP-137) only tests provide one.
type Mailer interface {
	SendSignupVerification(ctx context.Context, to, organizationName, link string) error
}

// Account is the IdP account of the first org_admin.
type Account struct {
	Email          string
	DisplayName    string
	Password       string
	OrganizationID string
}

// Accounts administers IdP accounts (Keycloak admin API).
type Accounts interface {
	// CreateAccount creates an enabled account with verified e-mail address
	// and returns its id; ErrAccountExists when the address is taken.
	CreateAccount(ctx context.Context, a Account) (string, error)
	// DeleteAccount removes an account created by CreateAccount.
	DeleteAccount(ctx context.Context, id string) error
}

// Request is a signup request (POST /api/v1/signup).
type Request struct {
	OrganizationName string `json:"organization_name"`
	Email            string `json:"email"`
	DisplayName      string `json:"display_name,omitempty"`
	// Altcha is the ALTCHA solution (base64 JSON), checked by Protection.
	Altcha string `json:"altcha"`
}

// Verification completes a signup (POST /api/v1/signup/verify).
type Verification struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// Result describes the created organization.
type Result struct {
	OrganizationID   string           `json:"organization_id"`
	OrganizationSlug string           `json:"organization_slug"`
	Plan             entitlement.Plan `json:"plan"`
	TrialEndsAt      time.Time        `json:"trial_ends_at"`
}

// PendingRequest is a stored signup request.
type PendingRequest struct {
	ID               string
	Email            string
	OrganizationName string
	DisplayName      string
	TokenHash        string
	ExpiresAt        time.Time
}

// Setup is everything the atomic creation writes besides the request.
type Setup struct {
	OrganizationID string
	Plan           entitlement.Plan
	TrialEndsAt    time.Time
	FallbackPlan   entitlement.Plan
	Entitlements   []entitlement.Entitlement
}

// Repository persists signup requests and creates organizations.
type Repository interface {
	CreateRequest(ctx context.Context, req *PendingRequest) error
	// Complete atomically resolves the pending request of tokenHash and
	// creates the organization of setup. createAccount runs inside the
	// transaction after the database rows exist; its error rolls back
	// everything. The returned request is the one that was completed.
	Complete(ctx context.Context, tokenHash string, now time.Time, setup *Setup,
		createAccount func(ctx context.Context, req *PendingRequest) error) (Result, error)
}

// Service is the signup flow.
type Service struct {
	repo     Repository
	mailer   Mailer
	accounts Accounts
	cfg      Config
	now      func() time.Time
	newOrgID func() string
}

// NewService creates the service. A nil mailer or accounts makes every
// signup ErrUnavailable.
func NewService(repo Repository, mailer Mailer, accounts Accounts, cfg Config) *Service {
	return &Service{repo: repo, mailer: mailer, accounts: accounts, cfg: cfg,
		now: time.Now, newOrgID: newUUID}
}

// Available reports whether signups can be completed.
func (s *Service) Available() bool {
	return s != nil && s.repo != nil && s.mailer != nil && s.accounts != nil && s.cfg.VerifyURL != ""
}

// Submit validates and stores a request and sends the verification link.
func (s *Service) Submit(ctx context.Context, req Request) error {
	if !s.Available() {
		return ErrUnavailable
	}
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(req.OrganizationName)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return &api.FieldError{Field: "organization_name", Message: "organization_name is required (at most 200 characters)"}
	}
	display := strings.TrimSpace(req.DisplayName)
	if utf8.RuneCountInString(display) > 200 {
		return &api.FieldError{Field: "display_name", Message: "display_name has at most 200 characters"}
	}
	if display == "" {
		display = email
	}
	token, hash, err := newToken()
	if err != nil {
		return err
	}
	pending := &PendingRequest{Email: email, OrganizationName: name, DisplayName: display,
		TokenHash: hash, ExpiresAt: s.now().Add(s.cfg.TokenTTL)}
	if cerr := s.repo.CreateRequest(ctx, pending); cerr != nil {
		return cerr
	}
	link, err := verifyLink(s.cfg.VerifyURL, token)
	if err != nil {
		return err
	}
	if err := s.mailer.SendSignupVerification(ctx, email, name, link); err != nil {
		return fmt.Errorf("send signup verification: %w", err)
	}
	return nil
}

// Verify completes a request: the organization, its first org_admin and
// the trial are created in one transaction together with the IdP account.
// When the transaction fails after the account was created, the account is
// deleted again.
func (s *Service) Verify(ctx context.Context, v Verification) (Result, error) {
	if !s.Available() {
		return Result{}, ErrUnavailable
	}
	token := strings.TrimSpace(v.Token)
	if token == "" {
		return Result{}, &api.FieldError{Field: "token", Message: "token is required"}
	}
	if utf8.RuneCountInString(v.Password) < minPasswordLength {
		return Result{}, &api.FieldError{Field: "password",
			Message: fmt.Sprintf("password must have at least %d characters", minPasswordLength)}
	}
	// Microseconds: the precision PostgreSQL keeps.
	now := s.now().UTC().Truncate(time.Microsecond)
	setup, err := s.setup(now)
	if err != nil {
		return Result{}, err
	}

	var accountID string
	result, err := s.repo.Complete(ctx, hashToken(token), now, setup,
		func(ctx context.Context, req *PendingRequest) error {
			id, cerr := s.accounts.CreateAccount(ctx, Account{Email: req.Email, DisplayName: req.DisplayName,
				Password: v.Password, OrganizationID: setup.OrganizationID})
			if cerr != nil {
				return cerr
			}
			accountID = id
			return nil
		})
	if err != nil && accountID != "" {
		// Compensation: the organization does not exist, so neither may
		// its account. Detached from ctx so a cancelled request still
		// cleans up.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if derr := s.accounts.DeleteAccount(cleanup, accountID); derr != nil {
			err = errors.Join(err, fmt.Errorf("delete account %s after failed signup: %w", accountID, derr))
		}
	}
	return result, err
}

// setup derives the organization id and trial entitlements of a new
// organization.
func (s *Service) setup(now time.Time) (*Setup, error) {
	ends := now.AddDate(0, 0, s.cfg.TrialDays)
	ents, err := TrialEntitlements(s.cfg, ends)
	if err != nil {
		return nil, err
	}
	return &Setup{OrganizationID: s.newOrgID(), Plan: s.cfg.TrialPlan, TrialEndsAt: ends,
		FallbackPlan: s.cfg.FallbackPlan, Entitlements: ents}, nil
}

// TrialEntitlements returns the entitlement rows of a trial (ENT-07): every
// feature of the trial plan with source selfsignup; features the fallback
// plan lacks are valid until the trial ends; cmdb_core carries the trial's
// max_cis.
func TrialEntitlements(cfg Config, endsAt time.Time) ([]entitlement.Entitlement, error) {
	fallback := map[string]bool{}
	for _, f := range entitlement.PlanFeatures(cfg.FallbackPlan) {
		fallback[f] = true
	}
	var out []entitlement.Entitlement
	for _, f := range entitlement.PlanFeatures(cfg.TrialPlan) {
		ent := entitlement.Entitlement{FeatureKey: f, Plan: cfg.TrialPlan, Enabled: true,
			Limits: map[string]int64{}, Source: "selfsignup"}
		if f == entitlement.FeatureCMDBCore {
			ent.Limits[entitlement.LimitMaxCIs] = cfg.TrialMaxCIs
		} else if !fallback[f] {
			until := endsAt
			ent.ValidUntil = &until
		}
		if err := ent.Validate(); err != nil {
			return nil, err
		}
		out = append(out, ent)
	}
	return out, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || utf8.RuneCountInString(email) > 254 {
		return "", &api.FieldError{Field: "email", Message: "email must be a plain e-mail address"}
	}
	return email, nil
}

// newToken returns a random verification token and its stored hash.
func newToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("signup token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func verifyLink(base, token string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("signup verify URL: %w", err)
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Slugify derives an organization slug from its name: lower-case ASCII
// letters and digits separated by single hyphens, at most 48 characters;
// "org" when nothing remains.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch r {
		case 'ä':
			r = 'a'
		case 'ö':
			r = 'o'
		case 'ü':
			r = 'u'
		case 'ß':
			b.WriteString("ss")
			dash = false
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		return "org"
	}
	return slug
}
