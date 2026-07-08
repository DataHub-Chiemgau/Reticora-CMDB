package identity

import (
	"context"
	"errors"
	"strings"
	"time"
)

// OIDCConfig stores the provider connection details.
type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// OIDCProvider wraps OIDC configuration and future provider clients.
type OIDCProvider struct {
	config OIDCConfig
}

// TokenSet holds the tokens returned from the OIDC provider.
type TokenSet struct {
	AccessToken  string
	IDToken      string
	RefreshToken string
	ExpiresAt    time.Time
}

// IDTokenClaims contains the claims extracted from an ID token.
type IDTokenClaims struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
}

// NewOIDCProvider constructs a new OIDC provider wrapper.
func NewOIDCProvider(cfg OIDCConfig) *OIDCProvider {
	return &OIDCProvider{config: cfg}
}

// ExchangeCode exchanges an authorization code for tokens.
func (p *OIDCProvider) ExchangeCode(ctx context.Context, code string) (*TokenSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("identity: OIDC provider is nil")
	}
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("identity: authorization code is required")
	}
	return nil, errors.New("identity: OIDC code exchange not implemented")
}

// ValidateIDToken validates a raw ID token and returns its claims.
func (p *OIDCProvider) ValidateIDToken(ctx context.Context, rawToken string) (*IDTokenClaims, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("identity: OIDC provider is nil")
	}
	if strings.TrimSpace(rawToken) == "" {
		return nil, errors.New("identity: ID token is required")
	}
	return nil, errors.New("identity: ID token validation not implemented")
}
