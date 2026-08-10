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
	// HTTPClient overrides the HTTP client used for token, discovery and JWKS
	// requests. When nil a timeout-bounded default client is used.
	HTTPClient *http.Client
}

// OIDCProvider wraps OIDC configuration and the provider's signing keys.
type OIDCProvider struct {
	config OIDCConfig
	jwks   jwksCache
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
	Nonce   string
}

// oidcHTTPClient is the default HTTP client for provider requests. Unlike
// http.DefaultClient it enforces a total request timeout so a stalled
// provider cannot hang login requests indefinitely.
var oidcHTTPClient = &http.Client{Timeout: 15 * time.Second}

// httpClient returns the configured HTTP client or the bounded default.
func (p *OIDCProvider) httpClient() *http.Client {
	if p != nil && p.config.HTTPClient != nil {
		return p.config.HTTPClient
	}
	return oidcHTTPClient
}

// NewOIDCProvider constructs a new OIDC provider wrapper.
func NewOIDCProvider(cfg OIDCConfig) *OIDCProvider {
	return &OIDCProvider{config: cfg}
}

// PublicConfig describes the OIDC parameters a browser client needs to start
// an authorization code flow. It never contains the client secret.
type PublicConfig struct {
	Issuer                string   `json:"issuer"`
	ClientID              string   `json:"client_id"`
	RedirectURI           string   `json:"redirect_uri,omitempty"`
	Scopes                []string `json:"scopes"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	EndSessionEndpoint    string   `json:"end_session_endpoint"`
	PKCERequired          bool     `json:"pkce_required"`
}

// PublicConfig returns the browser-facing OIDC configuration.
func (p *OIDCProvider) PublicConfig() PublicConfig {
	if p == nil {
		return PublicConfig{}
	}
	issuer := strings.TrimRight(strings.TrimSpace(p.config.IssuerURL), "/")
	return PublicConfig{
		Issuer:                issuer,
		ClientID:              strings.TrimSpace(p.config.ClientID),
		RedirectURI:           strings.TrimSpace(p.config.RedirectURL),
		// The realm emits the organization group via a groups claim mapper, so
		// no dedicated "groups" scope is requested (Keycloak rejects unknown
		// scopes with invalid_scope).
		Scopes:                []string{"openid", "profile", "email"},
		AuthorizationEndpoint: issuer + "/protocol/openid-connect/auth",
		TokenEndpoint:         issuer + "/protocol/openid-connect/token",
		EndSessionEndpoint:    issuer + "/protocol/openid-connect/logout",
		PKCERequired:          true,
	}
}

// ExchangeCodeWithVerifier exchanges an authorization code for tokens. The
// PKCE code verifier is mandatory: without it an injected or stolen
// authorization code could be redeemed by a party that never started the flow.
func (p *OIDCProvider) ExchangeCodeWithVerifier(ctx context.Context, code, codeVerifier string) (*TokenSet, error) {
	if strings.TrimSpace(codeVerifier) == "" {
		return nil, errors.New("identity: PKCE code verifier is required")
	}
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
	form.Set("code_verifier", strings.TrimSpace(codeVerifier))

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

	resp, err := p.httpClient().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("identity: exchange authorization code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
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

// ValidateIDToken validates a raw ID token and returns its claims. The token
// signature is verified against the issuer's JWKS (fetched from the discovery
// document and cached), and the issuer, audience, expiry and issued-at claims
// are enforced.
func (p *OIDCProvider) ValidateIDToken(ctx context.Context, rawToken string) (*IDTokenClaims, error) {
	return p.ValidateIDTokenWithNonce(ctx, rawToken, "")
}

// ValidateIDTokenWithNonce additionally enforces the nonce claim when
// expectedNonce is non-empty.
func (p *OIDCProvider) ValidateIDTokenWithNonce(ctx context.Context, rawToken, expectedNonce string) (*IDTokenClaims, error) {
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
	if len(parts) != 3 {
		return nil, errors.New("identity: invalid ID token format")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("identity: decode ID token header: %w", err)
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("identity: parse ID token header: %w", err)
	}
	if header.Algorithm != "RS256" && header.Algorithm != "ES256" {
		return nil, fmt.Errorf("identity: unexpected ID token algorithm %q", header.Algorithm)
	}

	key, err := p.resolveKey(ctx, header.KeyID, header.Algorithm)
	if err != nil {
		return nil, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("identity: decode ID token signature: %w", err)
	}
	if err := verifyJWTSignature(parts[0]+"."+parts[1], signature, header.Algorithm, key); err != nil {
		return nil, fmt.Errorf("identity: verify ID token signature: %w", err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("identity: decode ID token payload: %w", err)
	}

	var tokenClaims struct {
		Issuer          string          `json:"iss"`
		Sub             string          `json:"sub"`
		Email           string          `json:"email"`
		Name            string          `json:"name"`
		Groups          json.RawMessage `json:"groups"`
		Audience        json.RawMessage `json:"aud,omitempty"`
		AuthorizedParty string          `json:"azp,omitempty"`
		Nonce           string          `json:"nonce,omitempty"`
		Expiry          int64           `json:"exp,omitempty"`
		IssuedAt        int64           `json:"iat,omitempty"`
		NotBefore       int64           `json:"nbf,omitempty"`
	}
	if err := json.Unmarshal(payload, &tokenClaims); err != nil {
		return nil, fmt.Errorf("identity: parse ID token claims: %w", err)
	}

	expectedIssuer := strings.TrimSpace(p.config.IssuerURL)
	if expectedIssuer == "" {
		return nil, errors.New("identity: OIDC issuer URL is required")
	}
	if !sameIssuer(tokenClaims.Issuer, expectedIssuer) {
		return nil, fmt.Errorf("identity: unexpected ID token issuer %q", tokenClaims.Issuer)
	}

	if err := p.verifyAudience(tokenClaims.Audience, tokenClaims.AuthorizedParty); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	if tokenClaims.Expiry == 0 {
		return nil, errors.New("identity: ID token expiry is required")
	}
	if now >= tokenClaims.Expiry {
		return nil, errors.New("identity: ID token is expired")
	}
	if tokenClaims.NotBefore > 0 && now < tokenClaims.NotBefore {
		return nil, errors.New("identity: ID token is not yet valid")
	}
	if tokenClaims.IssuedAt > 0 && tokenClaims.IssuedAt > now+int64(maxIDTokenClockSkew/time.Second) {
		return nil, errors.New("identity: ID token was issued in the future")
	}

	if expectedNonce = strings.TrimSpace(expectedNonce); expectedNonce != "" && tokenClaims.Nonce != expectedNonce {
		return nil, errors.New("identity: unexpected ID token nonce")
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
		Nonce:   tokenClaims.Nonce,
	}, nil
}

const maxIDTokenClockSkew = 2 * time.Minute

// verifyAudience enforces the aud and azp rules from the OIDC core spec: the
// client ID must be listed in aud, and when multiple audiences (or an azp) are
// present the authorized party must be the client ID.
func (p *OIDCProvider) verifyAudience(rawAudience json.RawMessage, azp string) error {
	clientID := strings.TrimSpace(p.config.ClientID)
	if clientID == "" {
		return errors.New("identity: OIDC client ID is required")
	}

	audiences, err := decodeAudience(rawAudience)
	if err != nil {
		return err
	}
	if len(audiences) == 0 {
		return errors.New("identity: ID token audience is required")
	}

	found := false
	for _, aud := range audiences {
		if aud == clientID {
			found = true
			break
		}
	}
	if !found {
		return errors.New("identity: ID token audience does not include the client ID")
	}
	if len(audiences) > 1 || strings.TrimSpace(azp) != "" {
		if strings.TrimSpace(azp) != clientID {
			return errors.New("identity: ID token authorized party does not match the client ID")
		}
	}
	return nil
}

func decodeAudience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	return nil, errors.New("identity: aud claim must be a string array or string")
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
