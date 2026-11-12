package admp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The source decouples the refresh (WP-929) from the endpoints of ADMP. The
// productive source is clientSource on the read-only client of the
// account (WP-1073, ADMP documentation 9.14.0): the port inventory, the
// services of a port and a service. A connection follows with WP-1075;
// until then FetchConnection ends with the class not_implemented. Tests
// use a fake source.

// Classes of a refresh beyond those of the read-only client
// (readonly.Classes).
const (
	// ClassNotImplemented: the source of the endpoint is not there yet
	// (FetchConnection until WP-1075).
	ClassNotImplemented = "not_implemented"
	// ClassNotSupported: the documentation has no endpoint for the read
	// (the ports of a service: ADMP knows only port → services, E-251).
	ClassNotSupported = "not_supported"
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

// InventoryPort is a port of the inventory as the source returns it
// (E-251): the key (AccessPort.id) and where it lies.
type InventoryPort struct {
	PortKey        string
	NodeExternalID string
	NodeName       string
	Protocol       string
	UnitIndex      int64
	PortIndex      int64
}

// Source reads the objects of one account from ADMP; it only reads.
type Source interface {
	FetchConnection(ctx context.Context, externalID string) (*ConnectionData, error)
	FetchService(ctx context.Context, externalID string) (*ServiceData, error)
	FetchServicePorts(ctx context.Context, serviceExternalID string) ([]PortData, error)
	// FetchInventory reads the port inventory of the account (WP-1073).
	FetchInventory(ctx context.Context) ([]InventoryPort, error)
	// FetchPortServices returns the external ids of the services of a
	// port (WP-1073).
	FetchPortServices(ctx context.Context, portKey string) ([]string, error)
	// CheckSession reads the login state of the account (WP-1073); a
	// source without one ends with ClassNotImplemented.
	CheckSession(ctx context.Context) error
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

// clientSource reads through the read-only client of an account (WP-1073).
type clientSource struct{ c *readonly.Client }

// externalNumber parses an external id of ADMP (a decimal id); anything
// else is the class client without a request.
func externalNumber(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 {
		return 0, &SourceError{Class: readonly.ClassClient}
	}
	return n, nil
}

// FetchConnection implements Source: the connection follows with
// WP-1075.
func (clientSource) FetchConnection(context.Context, string) (*ConnectionData, error) {
	return nil, &SourceError{Class: ClassNotImplemented}
}

// FetchService implements Source: GET /V1/dienst/{id}. kind is the
// template name, else template:<templateId>, else unknown; the template id
// and the life cycle (E-253) go into properties.
func (s clientSource) FetchService(ctx context.Context, externalID string) (*ServiceData, error) {
	id, err := externalNumber(externalID)
	if err != nil {
		return nil, err
	}
	d, err := s.c.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	return serviceData(d), nil
}

// serviceData maps a Dienst of the documentation (WP-1073, E-253).
func serviceData(d *readonly.Service) *ServiceData {
	out := &ServiceData{ExternalID: strconv.FormatInt(d.ID, 10), Kind: "unknown", Properties: map[string]any{}}
	if d.ConnectionID > 0 {
		out.ConnectionExternalID = strconv.FormatInt(d.ConnectionID, 10)
	}
	switch {
	case d.TemplateName != "":
		out.Kind = d.TemplateName
	case d.TemplateID != nil:
		out.Kind = "template:" + strconv.FormatInt(*d.TemplateID, 10)
	}
	if d.TemplateID != nil {
		out.Properties["template_id"] = *d.TemplateID
	}
	lifecycle := map[string]any{}
	for key, v := range map[string]*float64{
		"booked_at": d.Booked, "provisioning_planned_at": d.ProvisioningPlanned, "provisioned_at": d.Provisioned,
		"cancelled_at": d.Cancelled, "shutdown_planned_at": d.ShutdownPlanned, "shut_down_at": d.ShutDown,
	} {
		if t, ok := DocTime(v); ok {
			lifecycle[key] = t.Format(time.RFC3339)
		}
	}
	out.Properties["lifecycle"] = lifecycle
	return out
}

// docMillis is the magnitude from which a time of the documentation counts
// as milliseconds (E-253): unique for every date after 1973.
const docMillis = 1e11

// DocTime converts a time of the ADMP documentation (a number without
// unit) to UTC: from 10^11 on milliseconds, below seconds (E-253). nil, a
// value that is no finite positive number gives false.
func DocTime(v *float64) (time.Time, bool) {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 {
		return time.Time{}, false
	}
	if math.Abs(*v) >= docMillis {
		return time.UnixMilli(int64(*v)).UTC(), true
	}
	return time.Unix(int64(*v), 0).UTC(), true
}

// FetchServicePorts implements Source: the documentation knows no
// direction service → ports (E-251).
func (clientSource) FetchServicePorts(context.Context, string) ([]PortData, error) {
	return nil, &SourceError{Class: ClassNotSupported}
}

// FetchInventory implements Source: GET /V1/infrastructure/accessnodepicker.
func (s clientSource) FetchInventory(ctx context.Context) ([]InventoryPort, error) {
	ports, err := s.c.ListAccessPorts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InventoryPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, InventoryPort{PortKey: strconv.FormatInt(p.ID, 10), NodeExternalID: strconv.FormatInt(p.NodeID, 10),
			NodeName: p.NodeName, Protocol: p.Protocol, UnitIndex: p.UnitIndex, PortIndex: p.PortIndex})
	}
	return out, nil
}

// FetchPortServices implements Source: GET
// /V1/infrastructure/port/{portId}/dienstIds.
func (s clientSource) FetchPortServices(ctx context.Context, portKey string) ([]string, error) {
	id, err := externalNumber(portKey)
	if err != nil {
		return nil, err
	}
	ids, err := s.c.ListPortServiceIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, n := range ids {
		out = append(out, strconv.FormatInt(n, 10))
	}
	return out, nil
}

// CheckSession implements Source: GET /V1/user/authentication.
func (s clientSource) CheckSession(ctx context.Context) error {
	_, err := s.c.GetSession(ctx)
	return err
}

// ClientSources keeps one read-only client per account (E-223: on the
// server over the egress policy) and gives its source. A changed account
// (URL, credential, rate, timeout) gets a new client and with it a new
// session: the old token is dropped, there is no logout (E-250).
type ClientSources struct {
	secrets readonly.SecretSource
	egress  egress.Options
	// newSource builds the source of a client (clientSource; tests may
	// replace it).
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
		newSource: func(c *readonly.Client) Source { return clientSource{c: c} }}
}

// configKey identifies the values of an account the client depends on.
func configKey(a *Account) string {
	return a.BaseURL + "\x00" + a.AuthURL + "\x00" + a.CredentialID + "\x00" +
		strconv.FormatFloat(a.RateLimitRPS, 'f', -1, 64) + "\x00" + strconv.Itoa(a.TimeoutMS)
}

// client returns the cached client of the account, a new one when its
// values changed. The client logs in as the documentation says (nil
// authenticator, E-250): username and token of the account's credential.
func (s *ClientSources) client(a *Account) (*readonly.Client, error) {
	key := configKey(a)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[a.ID]; ok && c.key == key {
		return c.client, nil
	}
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
