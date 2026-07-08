package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	if strings.TrimSpace(p.config.IssuerURL) == "" {
		return nil, errors.New("identity: OIDC issuer URL is required")
	}
	if strings.TrimSpace(p.config.ClientID) == "" {
		return nil, errors.New("identity: OIDC client ID is required")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {strings.TrimSpace(code)},
		"client_id":     {strings.TrimSpace(p.config.ClientID)},
		"client_secret": {p.config.ClientSecret},
	}
	if redirectURL := strings.TrimSpace(p.config.RedirectURL); redirectURL != "" {
		form.Set("redirect_uri", redirectURL)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(p.config.IssuerURL, "/")+"/protocol/openid-connect/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("identity: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("identity: exchange authorization code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("identity: read token response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("identity: token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return nil, fmt.Errorf("identity: parse token response: %w", err)
	}
	if strings.TrimSpace(tokenResponse.IDToken) == "" {
		return nil, errors.New("identity: token response is missing ID token")
	}

	tokenSet := &TokenSet{
		AccessToken:  tokenResponse.AccessToken,
		IDToken:      tokenResponse.IDToken,
		RefreshToken: tokenResponse.RefreshToken,
	}
	if tokenResponse.ExpiresIn > 0 {
		tokenSet.ExpiresAt = time.Now().UTC().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
	}

	return tokenSet, nil
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

	parts := strings.Split(strings.TrimSpace(rawToken), ".")
	if len(parts) < 2 {
		return nil, errors.New("identity: invalid ID token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("identity: decode ID token payload: %w", err)
	}

	var tokenClaims struct {
		Issuer string          `json:"iss"`
		Sub    string          `json:"sub"`
		Email  string          `json:"email"`
		Name   string          `json:"name"`
		Groups json.RawMessage `json:"groups"`
		Exp    int64           `json:"exp,omitempty"`
	}
	if err := json.Unmarshal(payload, &tokenClaims); err != nil {
		return nil, fmt.Errorf("identity: parse ID token claims: %w", err)
	}

	expectedIssuer := strings.TrimSpace(p.config.IssuerURL)
	if expectedIssuer == "" {
		return nil, errors.New("identity: OIDC issuer URL is required")
	}
	if strings.TrimSpace(tokenClaims.Issuer) != expectedIssuer {
		return nil, fmt.Errorf("identity: unexpected ID token issuer %q", tokenClaims.Issuer)
	}
	if tokenClaims.Exp > 0 && time.Now().Unix() >= tokenClaims.Exp {
		return nil, errors.New("identity: ID token is expired")
	}

	groups, err := decodeGroups(tokenClaims.Groups)
	if err != nil {
		return nil, err
	}

	return &IDTokenClaims{
		Subject: tokenClaims.Sub,
		Email:   tokenClaims.Email,
		Name:    tokenClaims.Name,
		Groups:  groups,
	}, nil
}

func decodeGroups(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var groups []string
	if err := json.Unmarshal(raw, &groups); err == nil {
		return groups, nil
	}

	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, nil
		}
		return []string{single}, nil
	}

	return nil, errors.New("identity: groups claim must be a string array or string")
}
