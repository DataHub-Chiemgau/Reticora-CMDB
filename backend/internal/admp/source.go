package admp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The source decouples the refresh (WP-929) from the endpoints of ADMP,
// which the documentation defines (A-55, WP-1073 to WP-1076). Until then
// the productive source is unavailableSource: every fetch ends with the
// class not_implemented and the cache stays as it is. Tests use a fake
// source.

// Classes of a refresh beyond those of the read-only client
// (readonly.Classes).
const (
	// ClassNotImplemented: the source of the endpoint is not there yet
	// (A-55).
	ClassNotImplemented = "not_implemented"
	// ClassCredentialMissing: the account has no credential
	// (ErrorCredentialMissing of WP-925).
	ClassCredentialMissing = ErrorCredentialMissing
)

// ConnectionData is a connection as the source returns it: no personal
// value in plain text (the holder follows as a surrogate with WP-1076).
type ConnectionData struct {
	ExternalID  string
	Carrier     string
	ContractID  string
	AddressText string
	Properties  map[string]any
}

// ServiceData is a service as the source returns it.
type ServiceData struct {
	ExternalID string
	// ConnectionExternalID names the connection of the service; empty:
	// none.
	ConnectionExternalID string
	Kind                 string
	Properties           map[string]any
}

// PortData is a port of a service as the source returns it; the match to
// a network interface is the rule of WP-1073.
type PortData struct {
	PortKey string
}

// Source reads the objects of one account from ADMP; it only reads.
type Source interface {
	FetchConnection(ctx context.Context, externalID string) (*ConnectionData, error)
	FetchService(ctx context.Context, externalID string) (*ServiceData, error)
	FetchServicePorts(ctx context.Context, serviceExternalID string) ([]PortData, error)
}

// Sources gives the source of an account and tells whether the circuit
// breaker of its client is open.
type Sources interface {
	For(ctx context.Context, a *Account) (Source, error)
	CircuitOpen(a *Account) bool
}

// SourceError is a failed fetch: its class only, never a text of ADMP.
type SourceError struct{ Class string }

func (e *SourceError) Error() string { return "admp: fetch failed (class " + e.Class + ")" }

// classOf returns the refresh class of a fetch error: the class of a
// SourceError or of the read-only client.
func classOf(err error) string {
	var se *SourceError
	if errors.As(err, &se) {
		return se.Class
	}
	return readonly.ClassOf(err)
}

// unavailableSource is the productive source until WP-1073: it fetches
// nothing.
type unavailableSource struct{}

func (unavailableSource) FetchConnection(context.Context, string) (*ConnectionData, error) {
	return nil, &SourceError{Class: ClassNotImplemented}
}

func (unavailableSource) FetchService(context.Context, string) (*ServiceData, error) {
	return nil, &SourceError{Class: ClassNotImplemented}
}

func (unavailableSource) FetchServicePorts(context.Context, string) ([]PortData, error) {
	return nil, &SourceError{Class: ClassNotImplemented}
}

// ClientSources keeps one read-only client per account (E-223: on the
// server over the egress policy) and gives its source. A changed account
// (URL, credential, rate, timeout) gets a new client.
type ClientSources struct {
	secrets readonly.SecretSource
	egress  egress.Options
	// newSource builds the source of a client: unavailableSource until
	// the endpoints of the documentation exist (WP-1073).
	newSource func(c *readonly.Client) Source

	mu      sync.Mutex
	clients map[string]cachedClient
}

type cachedClient struct {
	key    string
	client *readonly.Client
}

// NewClientSources creates the sources on the credential service
// (credential.Service.Decrypt: stored, or vault_ref resolved on the server)
// and the egress policy of the router.
func NewClientSources(secrets readonly.SecretSource, policy egress.Options) *ClientSources {
	return &ClientSources{secrets: secrets, egress: policy, clients: map[string]cachedClient{},
		newSource: func(*readonly.Client) Source { return unavailableSource{} }}
}

// configKey identifies the values of an account the client depends on.
func configKey(a *Account) string {
	return a.BaseURL + "\x00" + a.AuthURL + "\x00" + a.CredentialID + "\x00" +
		strconv.FormatFloat(a.RateLimitRPS, 'f', -1, 64) + "\x00" + strconv.Itoa(a.TimeoutMS)
}

// client returns the cached client of the account, a new one when its
// values changed.
func (s *ClientSources) client(a *Account) (*readonly.Client, error) {
	key := configKey(a)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[a.ID]; ok && c.key == key {
		return c.client, nil
	}
	// The authenticator of the login procedure follows the documentation
	// (A-55, WP-1073); until then a client has none.
	c, err := readonly.New(readonly.Config{AccountID: a.ID, OrganizationID: a.OrganizationID, CredentialID: a.CredentialID,
		BaseURL: a.BaseURL, AuthURL: a.AuthURL, RateLimitRPS: a.RateLimitRPS, TimeoutMS: a.TimeoutMS}, s.secrets, nil, s.egress)
	if err != nil {
		return nil, fmt.Errorf("admp client of account %s: %w", a.ID, err)
	}
	s.clients[a.ID] = cachedClient{key: key, client: c}
	return c, nil
}

// For implements Sources.
func (s *ClientSources) For(_ context.Context, a *Account) (Source, error) {
	c, err := s.client(a)
	if err != nil {
		return nil, &SourceError{Class: readonly.ClassClient}
	}
	return s.newSource(c), nil
}

// CircuitOpen implements Sources: false for an account without a client.
func (s *ClientSources) CircuitOpen(a *Account) bool {
	s.mu.Lock()
	c, ok := s.clients[a.ID]
	s.mu.Unlock()
	return ok && readonly.IsCircuitOpen(c.client)
}

var _ Sources = (*ClientSources)(nil)
