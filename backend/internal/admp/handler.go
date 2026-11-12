package admp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// The read API of the ADMP cache (ADM-07, ADM-08; WP-930): a connection
// with its services and their ports, the services and connections at a
// network interface, and the connections over all ports of a CI. Every
// route only reads the cache; refresh=true queues admp.refresh at most
// once per object and minute and answers 202 with the cached state. The
// holder and the address of a private connection appear only as their
// surrogates (E-243). CIs are named by their id only, so no field of a CI
// passes the field filter of WP-265.

// ErrTargetNotFound: the network interface or CI of a route does not
// exist or is outside the caller's scope (404).
var ErrTargetNotFound = errors.New("network interface or ci not found")

// Target is the CI behind a network interface or a CI itself, with its
// client: the client picks the account of a refresh (AccountFor).
type Target struct {
	CIID     string
	ClientID string
}

// Targets resolves the network interface and the CI of a route in the
// caller's scope (ErrTargetNotFound). PGRepository implements it; --no-db
// adapts the IPAM and CI stores (TargetFuncs).
type Targets interface {
	InterfaceTarget(ctx context.Context, orgID, id string) (*Target, error)
	CITarget(ctx context.Context, orgID, id string) (*Target, error)
}

// TargetFuncs adapts two functions to Targets.
type TargetFuncs struct {
	Interface func(ctx context.Context, orgID, id string) (*Target, error)
	CI        func(ctx context.Context, orgID, id string) (*Target, error)
}

// InterfaceTarget implements Targets.
func (f TargetFuncs) InterfaceTarget(ctx context.Context, orgID, id string) (*Target, error) {
	return f.Interface(ctx, orgID, id)
}

// CITarget implements Targets.
func (f TargetFuncs) CITarget(ctx context.Context, orgID, id string) (*Target, error) {
	return f.CI(ctx, orgID, id)
}

// Reasons a refresh asked for through the API was not queued.
const (
	// RefreshSkippedCircuitOpen: the circuit breaker of the account is
	// open; the route answers 200 with the cache.
	RefreshSkippedCircuitOpen = "circuit_open"
	// RefreshSkippedNoAccount: neither the client of the interface nor
	// the organization has an enabled account.
	RefreshSkippedNoAccount = "no_account"
)

// connectionFields are the members of a connection in every answer.
type connectionFields struct {
	ID               string         `json:"id"`
	AccountID        string         `json:"account_id"`
	ExternalID       string         `json:"external_id"`
	Carrier          string         `json:"carrier,omitempty"`
	ContractID       string         `json:"contract_id,omitempty"`
	AddressText      string         `json:"address_text,omitempty"`
	AddressSubjectID string         `json:"address_subject_id,omitempty"`
	HolderSubjectID  string         `json:"holder_subject_id,omitempty"`
	HolderPresent    bool           `json:"holder_present"`
	Properties       map[string]any `json:"properties"`
	FetchedAt        time.Time      `json:"fetched_at"`
	ExpiresAt        time.Time      `json:"expires_at"`
	Stale            bool           `json:"stale"`
	LastErrorClass   string         `json:"last_error_class,omitempty"`
	ClientID         string         `json:"client_id,omitempty"`
}

func connectionFieldsOf(c *Connection) connectionFields {
	props := c.Properties
	if props == nil {
		props = map[string]any{}
	}
	return connectionFields{ID: c.ID, AccountID: c.AccountID, ExternalID: c.ExternalID, Carrier: c.Carrier,
		ContractID: c.ContractID, AddressText: c.AddressText, AddressSubjectID: c.AddressSubjectID,
		HolderSubjectID: c.HolderSubjectID, HolderPresent: c.HolderSubjectID != "", Properties: props,
		FetchedAt: c.FetchedAt, ExpiresAt: c.ExpiresAt, Stale: c.Stale, LastErrorClass: c.LastErrorClass, ClientID: c.ClientID}
}

// portView is a port of a service: the CI only by its id.
type portView struct {
	PortKey            string `json:"port_key"`
	NetworkInterfaceID string `json:"network_interface_id,omitempty"`
	CIID               string `json:"ci_id,omitempty"`
	MatchedBy          string `json:"matched_by"`
}

// serviceView is a service with its ports.
type serviceView struct {
	ID             string         `json:"id"`
	AccountID      string         `json:"account_id"`
	ExternalID     string         `json:"external_id"`
	ConnectionID   string         `json:"connection_id,omitempty"`
	Kind           string         `json:"kind"`
	Properties     map[string]any `json:"properties"`
	FetchedAt      time.Time      `json:"fetched_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	Stale          bool           `json:"stale"`
	LastErrorClass string         `json:"last_error_class,omitempty"`
	Ports          []portView     `json:"ports"`
}

func serviceViewOf(s *Service) serviceView {
	props := s.Properties
	if props == nil {
		props = map[string]any{}
	}
	v := serviceView{ID: s.ID, AccountID: s.AccountID, ExternalID: s.ExternalID, ConnectionID: s.ConnectionID, Kind: s.Kind,
		Properties: props, FetchedAt: s.FetchedAt, ExpiresAt: s.ExpiresAt, Stale: s.Stale,
		LastErrorClass: s.LastErrorClass, Ports: make([]portView, 0, len(s.Ports))}
	for i := range s.Ports {
		p := &s.Ports[i]
		v.Ports = append(v.Ports, portView{PortKey: p.PortKey, NetworkInterfaceID: p.NetworkInterfaceID, CIID: p.CIID,
			MatchedBy: p.MatchedBy})
	}
	return v
}

// connectionView is GET /admp/connections/{id}.
type connectionView struct {
	connectionFields
	Services       []serviceView `json:"services"`
	RefreshJobID   string        `json:"refresh_job_id,omitempty"`
	RefreshSkipped string        `json:"refresh_skipped,omitempty"`
}

// interfaceServiceView is an entry of GET /network-interfaces/{id}/admp:
// a service with its ports at the interface and its connection.
type interfaceServiceView struct {
	serviceView
	Connection *connectionFields `json:"connection,omitempty"`
}

// ciConnectionView is an entry of GET /cis/{id}/admp-connections.
type ciConnectionView struct {
	connectionFields
}

// refreshList is a list page with the outcome of a refresh: the members of
// api.ListResponse (which must not be embedded, its MarshalJSON would be
// promoted) and refresh_job_id or refresh_skipped.
type refreshList[T any] struct {
	Data           []T    `json:"data"`
	Total          int    `json:"total"`
	Limit          int    `json:"limit"`
	Offset         int    `json:"offset"`
	HasMore        bool   `json:"has_more"`
	NextCursor     string `json:"next_cursor,omitempty"`
	RefreshJobID   string `json:"refresh_job_id,omitempty"`
	RefreshSkipped string `json:"refresh_skipped,omitempty"`
}

func refreshListOf[T any](items []T, page api.PaginationParams) refreshList[T] {
	p := api.Page(items, page)
	out := refreshList[T]{Data: api.NonNilSlice(p.Data), Total: p.Total, Limit: p.Limit, Offset: p.Offset, HasMore: p.HasMore}
	if p.HasMore && len(p.Data) > 0 {
		out.NextCursor = api.EncodeOffsetCursor(p.Offset + len(p.Data))
	}
	return out
}

// DataHandler serves the read API of the ADMP cache (admp:read; feature
// and module admp).
type DataHandler struct {
	cache    Repository
	targets  Targets
	accounts *AccountService
	sync     *Sync
}

// NewDataHandler creates the read API: accounts picks the account of an
// interface refresh, sync queues the refresh (nil: refresh=true answers
// 503).
func NewDataHandler(cache Repository, targets Targets, accounts *AccountService, sync *Sync) *DataHandler {
	return &DataHandler{cache: cache, targets: targets, accounts: accounts, sync: sync}
}

// RegisterRoutes mounts the read routes; none of them reaches ADMP, a
// refresh is a job.
func (h *DataHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/admp/connections/{id}", h.getConnection)
	r.Get("/api/v1/network-interfaces/{id}/admp", h.listInterface)
	r.Get("/api/v1/cis/{id}/admp-connections", h.listCI)
}

// writeDataError maps the errors of the read API without their texts.
func writeDataError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCacheNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "admp connection not found")
	case errors.Is(err, ErrTargetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "network interface or ci not found")
	case errors.Is(err, ErrNoRefreshJobs):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the admp refresh is not available")
	default:
		api.WriteRepoError(w, err)
	}
}

// refreshRequested reads refresh=true; any other value than a boolean is
// 400.
func refreshRequested(w http.ResponseWriter, r *http.Request) (refresh, ok bool) {
	raw := r.URL.Query().Get("refresh")
	if raw == "" {
		return false, true
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "refresh must be true or false")
		return false, false
	}
	return v, true
}

// refresh queues admp.refresh for p and returns the job id or the reason
// it was skipped.
func (h *DataHandler) refresh(ctx context.Context, orgID string, p RefreshParams) (jobID, skipped string, err error) {
	if h.sync == nil {
		return "", "", ErrNoRefreshJobs
	}
	return h.sync.RequestRefresh(ctx, orgID, p)
}

// status is 202 when a refresh job was queued or found, else 200.
func status(jobID string) int {
	if jobID != "" {
		return http.StatusAccepted
	}
	return http.StatusOK
}

func (h *DataHandler) getConnection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	refresh, ok := refreshRequested(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := h.cache.GetConnection(ctx, orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeDataError(w, err)
		return
	}
	services, err := h.cache.ListByConnection(ctx, orgID, c.ID)
	if err != nil {
		writeDataError(w, err)
		return
	}
	view := connectionView{connectionFields: connectionFieldsOf(c), Services: make([]serviceView, 0, len(services))}
	for i := range services {
		view.Services = append(view.Services, serviceViewOf(&services[i]))
	}
	if refresh {
		view.RefreshJobID, view.RefreshSkipped, err = h.refresh(ctx, orgID,
			RefreshParams{AccountID: c.AccountID, Kind: KindConnection, ExternalID: c.ExternalID})
		if err != nil {
			writeDataError(w, err)
			return
		}
	}
	api.WriteJSON(w, status(view.RefreshJobID), view)
}

func (h *DataHandler) listInterface(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	refresh, ok := refreshRequested(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	target, err := h.targets.InterfaceTarget(ctx, orgID, id)
	if err != nil {
		writeDataError(w, err)
		return
	}
	services, err := h.cache.ListByInterface(ctx, orgID, id)
	if err != nil {
		writeDataError(w, err)
		return
	}
	var connectionIDs []string
	for i := range services {
		if services[i].ConnectionID != "" {
			connectionIDs = append(connectionIDs, services[i].ConnectionID)
		}
	}
	connections, err := h.cache.ConnectionsByID(ctx, orgID, connectionIDs)
	if err != nil {
		writeDataError(w, err)
		return
	}
	byID := make(map[string]*connectionFields, len(connections))
	for i := range connections {
		f := connectionFieldsOf(&connections[i])
		byID[f.ID] = &f
	}
	items := make([]interfaceServiceView, 0, len(services))
	for i := range services {
		items = append(items, interfaceServiceView{serviceView: serviceViewOf(&services[i]), Connection: byID[services[i].ConnectionID]})
	}
	out := refreshListOf(items, page)
	if refresh {
		out.RefreshJobID, out.RefreshSkipped, err = h.refreshInterface(ctx, orgID, id, target)
		if err != nil {
			writeDataError(w, err)
			return
		}
	}
	api.WriteJSON(w, status(out.RefreshJobID), out)
}

// refreshInterface queues the refresh of an interface with the account of
// its CI's client, else the organization's (AccountFor).
func (h *DataHandler) refreshInterface(ctx context.Context, orgID, interfaceID string, target *Target) (jobID, skipped string, err error) {
	if h.accounts == nil {
		return "", RefreshSkippedNoAccount, nil
	}
	a, err := h.accounts.AccountFor(ctx, orgID, target.ClientID)
	if errors.Is(err, ErrNoAccount) {
		return "", RefreshSkippedNoAccount, nil
	}
	if err != nil {
		return "", "", err
	}
	return h.refresh(ctx, orgID, RefreshParams{AccountID: a.ID, Kind: KindInterface, NetworkInterfaceID: interfaceID})
}

func (h *DataHandler) listCI(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if _, err := h.targets.CITarget(ctx, orgID, id); err != nil {
		writeDataError(w, err)
		return
	}
	connections, err := h.cache.ListByCI(ctx, orgID, id)
	if err != nil {
		writeDataError(w, err)
		return
	}
	items := make([]ciConnectionView, 0, len(connections))
	for i := range connections {
		items = append(items, ciConnectionView{connectionFields: connectionFieldsOf(&connections[i])})
	}
	api.WriteJSON(w, http.StatusOK, api.Page(items, page))
}
