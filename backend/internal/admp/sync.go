package admp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The refresh of the ADMP cache (ADM-03, E-225, WP-929): on demand as the
// job admp.refresh, periodically only for services linked to a port and
// their connections (admp.refresh_linked), and the expiry of entries past
// their TTL with their personal surrogates (admp.expire). A failed fetch
// keeps the entry with stale and the error class at the object and at the
// account. The fetches go through a Source (source.go); until the
// endpoints of the documentation exist (A-55) it fetches nothing.

// Job kinds of the refresh.
const (
	RefreshJobKind    = "admp.refresh"
	RefreshLinkedKind = "admp.refresh_linked"
	ExpireKind        = "admp.expire"
)

// KindInterface refreshes the services linked to a network interface and
// their connections.
const KindInterface = "interface"

// Defaults of the refresh.
const (
	// DefaultCacheTTLDays is the TTL of a fetched entry
	// (RETICORA_ADMP_CACHE_TTL_DAYS, E-243).
	DefaultCacheTTLDays = 30
	// linkedHorizon: the periodic refresh renews linked entries expiring
	// within it.
	linkedHorizon = 7 * 24 * time.Hour
	// DefaultLinkedRunTime is the run time of one admp.refresh_linked: an
	// account gets at most rate_limit_rps × run time fetches per run.
	DefaultLinkedRunTime = 5 * time.Minute
	// resultOK is the result of a successful refresh.
	resultOK = "ok"
)

// ErrNoRefreshJobs: a refresh was submitted without a job service.
var ErrNoRefreshJobs = errors.New("admp: no job service for the refresh")

// Metrics of the refresh (ADM-03).
var (
	refreshTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_admp_refresh_total",
		Help: "ADMP refreshes by object kind and result (ok or an error class).",
	}, []string{"kind", "result"})
	expiredTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reticora_admp_expired_total",
		Help: "ADMP cache entries removed after their TTL by object kind.",
	}, []string{"kind"})
)

// RegisterMetrics registers the refresh metrics and those of the read-only
// client on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(refreshTotal, expiredTotal, portMatchTotal)
	readonly.RegisterMetrics(reg)
}

// RefreshParams are the parameters of admp.refresh: the account, the
// object kind (connection, service or interface) and the external id or,
// for interface, the network interface.
type RefreshParams struct {
	AccountID          string `json:"account_id"`
	Kind               string `json:"kind"`
	ExternalID         string `json:"external_id,omitempty"`
	NetworkInterfaceID string `json:"network_interface_id,omitempty"`
}

// target is the id the dedupe key names.
func (p *RefreshParams) target() string {
	switch p.Kind {
	case KindInterface:
		return p.NetworkInterfaceID
	case KindInventory:
		return ""
	}
	return p.ExternalID
}

func (p *RefreshParams) validate() error {
	p.ExternalID = strings.TrimSpace(p.ExternalID)
	switch {
	case !api.IsUUID(p.AccountID):
		return fieldError("account_id", "must be a uuid")
	case p.Kind == KindConnection || p.Kind == KindService || p.Kind == KindPort:
		if p.ExternalID == "" || len(p.ExternalID) > 200 {
			return fieldError("external_id", "is required")
		}
	case p.Kind == KindInventory:
		p.ExternalID, p.NetworkInterfaceID = "", ""
	case p.Kind == KindInterface:
		if !api.IsUUID(p.NetworkInterfaceID) {
			return fieldError("network_interface_id", "must be a uuid")
		}
	default:
		return fieldError("kind", "must be connection, service, interface, inventory or port")
	}
	return nil
}

// RefreshResult is the result of a refresh: ok or the error class, and
// how many objects were refreshed or marked stale. A refresh of the
// inventory adds matched, unmatched and the first 100 unmatched ports
// (WP-1073, readable through GET /api/v1/jobs/{id}).
type RefreshResult struct {
	Result    string `json:"result"`
	Refreshed int    `json:"refreshed"`
	Stale     int    `json:"stale"`
	*MatchResult
}

// Submitter queues a job (jobs.Queue.Submit, or the in-process runner of
// WithJobs).
type Submitter interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// SyncOptions configures the refresh.
type SyncOptions struct {
	Accounts AccountRepository
	Cache    Repository
	Sources  Sources
	// Pool is the application pool: the upsert of a connection and its
	// event share one transaction. nil (--no-db): no events.
	Pool *pgxpool.Pool
	// Organizations lists the organizations the periodic jobs run
	// through (database.OrganizationIDs, or the memory accounts).
	Organizations func(ctx context.Context) ([]string, error)
	// TTL of a fetched entry; zero is DefaultCacheTTLDays.
	TTL time.Duration
	// LinkedRunTime of admp.refresh_linked; zero is DefaultLinkedRunTime.
	LinkedRunTime time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Sync refreshes the ADMP cache.
type Sync struct {
	o         SyncOptions
	submitter Submitter
	outbox    *events.Outbox
	// carry is the budget of admp.refresh_linked an account saved up from
	// earlier runs (keepCredit), by account id; mu guards it.
	mu    sync.Mutex
	carry map[string]float64
}

// NewSync creates the refresh.
func NewSync(opts *SyncOptions) *Sync {
	o := *opts
	if o.TTL <= 0 {
		o.TTL = DefaultCacheTTLDays * 24 * time.Hour
	}
	if o.LinkedRunTime <= 0 {
		o.LinkedRunTime = DefaultLinkedRunTime
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sources == nil {
		o.Sources = NewClientSources(nil, egress.Options{})
	}
	return &Sync{o: o, outbox: events.NewOutbox(), carry: map[string]float64{}}
}

// WithSubmitter sets how admp.refresh is queued.
func (s *Sync) WithSubmitter(sub Submitter) *Sync {
	s.submitter = sub
	return s
}

// WithQueue queues admp.refresh on the job queue (NATS), whose handler is
// RunRefreshJob.
func (s *Sync) WithQueue(q *jobs.Queue) *Sync { return s.WithSubmitter(q) }

// WithJobs runs admp.refresh in this process on svc (without NATS).
func (s *Sync) WithJobs(svc *jobs.Service) *Sync {
	return s.WithSubmitter(inProcess{svc: svc, sync: s})
}

// inProcess runs a queued refresh at once in the background.
type inProcess struct {
	svc  *jobs.Service
	sync *Sync
}

func (p inProcess) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	return p.svc.RunDetachedWith(ctx, orgID, kind, params, opts, func(ctx context.Context) (any, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		return p.sync.RunRefreshJob(ctx, &jobs.Job{OrganizationID: orgID, Kind: kind, Params: raw})
	})
}

// MinuteWindow is the dedupe window of a refresh asked for through the
// API (WP-930): the minute.
func MinuteWindow(t time.Time) string { return "m" + t.UTC().Format("200601021504") }

// RefreshKey is the idempotency key of a refresh in a window.
func RefreshKey(p *RefreshParams, window string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", RefreshJobKind, p.AccountID, p.Kind, p.target(), window)
}

// Submit queues admp.refresh for p, at most once per object and window
// (jobs.Job.IdempotencyKey): a second submit in the window returns the
// first job with created false, a later window queues a new one.
func (s *Sync) Submit(ctx context.Context, orgID string, p RefreshParams, window string) (*jobs.Job, bool, error) {
	if err := p.validate(); err != nil {
		return nil, false, err
	}
	if s.submitter == nil {
		return nil, false, ErrNoRefreshJobs
	}
	return s.submitter.Submit(ctx, orgID, RefreshJobKind, p, jobs.EnqueueOptions{IdempotencyKey: RefreshKey(&p, window)})
}

// RequestRefresh queues admp.refresh asked for through the read API
// (WP-930): at most one job per object and minute (MinuteWindow), a
// second request in the minute gets the id of the first job. An open
// circuit breaker of the account queues nothing (RefreshSkippedCircuitOpen).
// The account is read organization-wide: a principal restricted to a
// client refreshes its data with the organization's account, which it
// does not see.
func (s *Sync) RequestRefresh(ctx context.Context, orgID string, p RefreshParams) (jobID, skipped string, err error) {
	if verr := p.validate(); verr != nil {
		return "", "", verr
	}
	scope := database.OrgWideScope(orgID, tenant.FromContext(ctx).UserID)
	orgWide := database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
	a, err := s.o.Accounts.GetAccount(orgWide, orgID, p.AccountID)
	if err != nil {
		return "", "", err
	}
	if s.o.Sources.CircuitOpen(a) {
		return "", RefreshSkippedCircuitOpen, nil
	}
	window := MinuteWindow(s.o.Now())
	job, _, err := s.Submit(ctx, orgID, p, window)
	if errors.Is(err, jobs.ErrNotFound) {
		// The job of this minute was queued by a principal of another
		// scope, which the caller does not see: the dedupe still holds,
		// its id is read organization-wide (nothing is queued, the key is
		// taken).
		job, _, err = s.Submit(orgWide, orgID, p, window)
	}
	if err != nil {
		return "", "", err
	}
	return job.ID, "", nil
}

// orgContext is the organization-wide context of the system refresh.
func orgContext(ctx context.Context, orgID string) context.Context {
	if scope, ok := database.TenantScopeFromContext(ctx); ok && strings.EqualFold(scope.OrgID, orgID) {
		return ctx
	}
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(ctx, &scope)
}

// transient reports whether a class may pass on a retry of the job.
func transient(class string) bool {
	switch class {
	case readonly.ClassNetwork, readonly.ClassTimeout, readonly.ClassServer, readonly.ClassRateLimited,
		readonly.ClassCanceled:
		return true
	}
	return false
}

// RunRefreshJob is the handler of admp.refresh. A transient failure fails
// the attempt (the queue retries it, MaxAttempts 3); any other ends the
// job with its class as result.
func (s *Sync) RunRefreshJob(ctx context.Context, job *jobs.Job) (any, error) {
	var p RefreshParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, fmt.Errorf("%s: invalid params", RefreshJobKind)
	}
	res, err := s.Refresh(orgContext(ctx, job.OrganizationID), job.OrganizationID, p)
	if err != nil {
		return nil, err
	}
	if transient(res.Result) {
		return nil, fmt.Errorf("%s: class %s", RefreshJobKind, res.Result)
	}
	return res, nil
}

// Refresh fetches the object of p and stores it in the cache; ctx carries
// the organization's tenant scope. A failure of the fetch is the result
// (its class), not an error; an error is a failure to store (the job
// fails, the transaction with the event is rolled back).
func (s *Sync) Refresh(ctx context.Context, orgID string, p RefreshParams) (RefreshResult, error) {
	if err := p.validate(); err != nil {
		return RefreshResult{}, err
	}
	a, err := s.o.Accounts.GetAccount(ctx, orgID, p.AccountID)
	if err != nil {
		return RefreshResult{}, err
	}
	var res RefreshResult
	src, class := s.sourceOf(ctx, a)
	switch p.Kind {
	case KindConnection:
		res, err = s.refreshConnection(ctx, a, src, class, p.ExternalID)
	case KindService:
		res, err = s.refreshService(ctx, a, src, class, p.ExternalID)
	case KindInterface:
		res, err = s.refreshInterface(ctx, a, src, class, p.NetworkInterfaceID)
	case KindInventory:
		res, err = s.refreshInventory(ctx, a, src, class)
	case KindPort:
		res, err = s.refreshPort(ctx, a, src, class, p.ExternalID)
	}
	if err != nil {
		return RefreshResult{}, err
	}
	// The account records an outcome only when a fetch ran or a class
	// forbade one; an interface without services of the account fetches
	// nothing and leaves last_ok_at and last_error_class as they are.
	if res.Result != resultOK || res.Refreshed+res.Stale > 0 {
		s.recordAccount(ctx, a, res.Result)
	}
	return res, nil
}

// sourceOf returns the source of an account, or the class that forbids a
// fetch: a disabled account, no credential, an open circuit breaker.
func (s *Sync) sourceOf(ctx context.Context, a *Account) (src Source, class string) {
	switch {
	case !a.Enabled:
		return nil, readonly.ClassClient
	case a.CredentialID == "":
		return nil, ClassCredentialMissing
	case s.o.Sources.CircuitOpen(a):
		return nil, readonly.ClassCircuitOpen
	}
	src, err := s.o.Sources.For(ctx, a)
	if err != nil {
		return nil, classOf(err)
	}
	return src, ""
}

// recordAccount stores the outcome at the account: last_ok_at after a
// success, else the error class.
func (s *Sync) recordAccount(ctx context.Context, a *Account, result string) {
	class := result
	if class == resultOK {
		class = ""
	}
	if err := s.o.Accounts.RecordAccountStatus(ctx, a.OrganizationID, a.ID, s.o.Now().UTC(), class); err != nil {
		slog.WarnContext(ctx, "admp account status", "account_id", a.ID, "error", err)
	}
}

func (s *Sync) count(kind, result string) { refreshTotal.WithLabelValues(kind, result).Inc() }

// inTx runs fn in one transaction of the organization (joined by the
// repository calls); without a pool it runs fn as it is.
func (s *Sync) inTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if s.o.Pool == nil {
		return fn(ctx)
	}
	return database.WithRequestTenantTx(ctx, s.o.Pool, orgID, fn)
}

// emit appends admp.connection.refreshed last before the commit of the
// transaction in ctx (Konvention 20); without a pool nothing.
func (s *Sync) emit(ctx context.Context, c *Connection) error {
	if s.o.Pool == nil {
		return nil
	}
	return database.WithRequestTenant(ctx, s.o.Pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return database.BeforeCommit(ctx, func() error {
			_, err := s.outbox.Append(ctx, tx, &events.Event{OrgID: c.OrganizationID, Type: events.ADMPConnectionRefreshed,
				EntityID: c.ID, Data: events.ADMPConnectionRefreshedData{ID: c.ID, AccountID: c.AccountID, ClientID: c.ClientID,
					Stale: c.Stale}})
			return err
		})
	})
}

// normalized returns properties as stored (through JSON), to compare a
// fetched value with the cached one.
func normalized(props map[string]any) string {
	if props == nil {
		props = map[string]any{}
	}
	raw, _ := json.Marshal(props)
	return string(raw)
}

// connectionChanged reports whether an upsert changes the connection.
func connectionChanged(prev, next *Connection) bool {
	return prev == nil || prev.Carrier != next.Carrier || prev.ContractID != next.ContractID ||
		prev.AddressText != next.AddressText || prev.Stale != next.Stale || prev.LastErrorClass != next.LastErrorClass ||
		normalized(prev.Properties) != normalized(next.Properties)
}

// fail marks the cached object stale with the class, fetched_at kept; an
// object that is not cached stays absent. A connection turning stale
// emits its event in the same transaction.
func (s *Sync) fail(ctx context.Context, a *Account, kind, externalID, class string) (RefreshResult, error) {
	s.count(kind, class)
	res := RefreshResult{Result: class}
	txErr := s.inTx(ctx, a.OrganizationID, func(ctx context.Context) error {
		if kind == KindConnection {
			c, err := s.o.Cache.ConnectionByExternal(ctx, a.OrganizationID, a.ID, externalID)
			if errors.Is(err, ErrCacheNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if merr := s.o.Cache.MarkStale(ctx, a.OrganizationID, kind, c.ID, class); merr != nil {
				return merr
			}
			res.Stale++
			if c.Stale {
				return nil
			}
			c.Stale, c.LastErrorClass = true, class
			return s.emit(ctx, c)
		}
		sv, err := s.o.Cache.ServiceByExternal(ctx, a.OrganizationID, a.ID, externalID)
		if errors.Is(err, ErrCacheNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if merr := s.o.Cache.MarkStale(ctx, a.OrganizationID, kind, sv.ID, class); merr != nil {
			return merr
		}
		res.Stale++
		return nil
	})
	if txErr != nil {
		return RefreshResult{}, fmt.Errorf("admp: mark %s stale: %w", kind, txErr)
	}
	return res, nil
}

// refreshConnection fetches a connection and stores it; class forbids
// the fetch.
func (s *Sync) refreshConnection(ctx context.Context, a *Account, src Source, class, externalID string) (RefreshResult, error) {
	if class != "" {
		return s.fail(ctx, a, KindConnection, externalID, class)
	}
	data, err := src.FetchConnection(ctx, externalID)
	if err != nil {
		return s.fail(ctx, a, KindConnection, externalID, classOf(err))
	}
	now := s.o.Now().UTC()
	c := &Connection{OrganizationID: a.OrganizationID, AccountID: a.ID, ExternalID: externalID, Carrier: data.Carrier,
		ContractID: data.ContractID, AddressText: data.AddressText, Properties: maps.Clone(data.Properties),
		FetchedAt: now, ExpiresAt: now.Add(s.o.TTL)}
	err = s.inTx(ctx, a.OrganizationID, func(ctx context.Context) error {
		prev, perr := s.o.Cache.ConnectionByExternal(ctx, a.OrganizationID, a.ID, externalID)
		switch {
		case errors.Is(perr, ErrCacheNotFound):
			prev = nil
		case perr != nil:
			return perr
		default:
			// The holder stays a surrogate of the vault until the source
			// names it (WP-1076); a business address replaces a private one.
			c.HolderSubjectID = prev.HolderSubjectID
			if c.AddressText == "" {
				c.AddressSubjectID = prev.AddressSubjectID
			}
		}
		if uerr := s.o.Cache.UpsertConnection(ctx, c); uerr != nil {
			return uerr
		}
		if connectionChanged(prev, c) {
			return s.emit(ctx, c)
		}
		return nil
	})
	if err != nil {
		return RefreshResult{}, fmt.Errorf("admp: store connection: %w", err)
	}
	s.count(KindConnection, resultOK)
	return RefreshResult{Result: resultOK, Refreshed: 1}, nil
}

// refreshService fetches a service and its ports and stores them; the
// links of a port to an interface and a CI are kept by its key. A source
// without the direction service → ports (ClassNotSupported, the ADMP
// documentation, E-251) keeps the ports as they are: they come from the
// ports (refreshPort). A connection of the service that is not cached yet
// is fetched first; a source that cannot fetch connections yet
// (ClassNotImplemented, until WP-1075) leaves a stub of it.
func (s *Sync) refreshService(ctx context.Context, a *Account, src Source, class, externalID string) (RefreshResult, error) {
	if class != "" {
		return s.fail(ctx, a, KindService, externalID, class)
	}
	data, err := src.FetchService(ctx, externalID)
	if err != nil {
		return s.fail(ctx, a, KindService, externalID, classOf(err))
	}
	ports, err := src.FetchServicePorts(ctx, externalID)
	keepPorts := false
	switch {
	case err != nil && classOf(err) == ClassNotSupported:
		keepPorts = true
	case err != nil:
		return s.fail(ctx, a, KindService, externalID, classOf(err))
	}
	res, sv, err := s.storeService(ctx, a, src, externalID, data)
	if err != nil {
		return RefreshResult{}, err
	}
	if !keepPorts {
		if err = s.inTx(ctx, a.OrganizationID, func(ctx context.Context) error {
			kept := map[string]ServicePort{}
			if prev, perr := s.o.Cache.ServiceByExternal(ctx, a.OrganizationID, a.ID, externalID); perr == nil {
				for i := range prev.Ports {
					kept[prev.Ports[i].PortKey] = prev.Ports[i]
				}
			} else if !errors.Is(perr, ErrCacheNotFound) {
				return perr
			}
			next := make([]ServicePort, 0, len(ports))
			for _, p := range ports {
				port := ServicePort{PortKey: p.PortKey, MatchedBy: MatchedByNone}
				if old, ok := kept[strings.TrimSpace(p.PortKey)]; ok {
					port.NetworkInterfaceID, port.CIID, port.MatchedBy = old.NetworkInterfaceID, old.CIID, old.MatchedBy
					port.ClientID, port.SiteID = old.ClientID, old.SiteID
				}
				next = append(next, port)
			}
			_, rerr := s.o.Cache.ReplaceServicePorts(ctx, a.OrganizationID, sv.ID, next)
			return rerr
		}); err != nil {
			return RefreshResult{}, fmt.Errorf("admp: store service ports: %w", err)
		}
	}
	return res, nil
}

// storeService stores a fetched service with its connection (fetched
// first when not cached) and returns the stored row.
func (s *Sync) storeService(ctx context.Context, a *Account, src Source, externalID string, data *ServiceData) (RefreshResult, *Service, error) {
	res := RefreshResult{Result: resultOK}
	connectionID := ""
	if data.ConnectionExternalID != "" {
		c, cerr := s.o.Cache.ConnectionByExternal(ctx, a.OrganizationID, a.ID, data.ConnectionExternalID)
		if errors.Is(cerr, ErrCacheNotFound) {
			sub, serr := s.refreshConnection(ctx, a, src, "", data.ConnectionExternalID)
			if serr != nil {
				return RefreshResult{}, nil, serr
			}
			res.Refreshed += sub.Refreshed
			switch sub.Result {
			case resultOK:
			case ClassNotImplemented:
				// The source fetches no connection yet (until WP-1075):
				// a stub without fields keeps the link of the service.
				if stubErr := s.stubConnection(ctx, a, data.ConnectionExternalID, sub.Result); stubErr != nil {
					return RefreshResult{}, nil, stubErr
				}
			default:
				// A failed fetch of the connection is the result of the
				// refresh: a transient class retries the job, the account
				// records it; the service is stored without its connection.
				res.Result = sub.Result
			}
			c, cerr = s.o.Cache.ConnectionByExternal(ctx, a.OrganizationID, a.ID, data.ConnectionExternalID)
		}
		if cerr == nil {
			connectionID = c.ID
		}
	}
	now := s.o.Now().UTC()
	sv := &Service{OrganizationID: a.OrganizationID, AccountID: a.ID, ExternalID: externalID,
		ConnectionID: connectionID, Kind: data.Kind, Properties: maps.Clone(data.Properties), FetchedAt: now, ExpiresAt: now.Add(s.o.TTL)}
	if err := s.inTx(ctx, a.OrganizationID, func(ctx context.Context) error {
		return s.o.Cache.UpsertService(ctx, sv)
	}); err != nil {
		return RefreshResult{}, nil, fmt.Errorf("admp: store service: %w", err)
	}
	s.count(KindService, resultOK)
	res.Refreshed++
	return res, sv, nil
}

// stubConnection stores a connection the source cannot fetch yet: its
// external id only, stale with the class (WP-1073).
func (s *Sync) stubConnection(ctx context.Context, a *Account, externalID, class string) error {
	now := s.o.Now().UTC()
	c := &Connection{OrganizationID: a.OrganizationID, AccountID: a.ID, ExternalID: externalID, FetchedAt: now,
		ExpiresAt: now.Add(s.o.TTL), Stale: true, LastErrorClass: class}
	return s.inTx(ctx, a.OrganizationID, func(ctx context.Context) error {
		return s.o.Cache.UpsertConnection(ctx, c)
	})
}

// merger folds the results of the parts of a refresh; ClassNotImplemented
// of a part (a connection until WP-1075) is no failure of the whole.
type merger struct{ res RefreshResult }

func (m *merger) add(r RefreshResult) {
	m.res.Refreshed += r.Refreshed
	m.res.Stale += r.Stale
	if r.Result != resultOK && r.Result != ClassNotImplemented {
		m.res.Result = r.Result
	}
}

// inventoryStale is how old an inventory may be before a refresh of an
// interface without matched port loads it again (WP-1073).
const inventoryStale = time.Hour

// refreshInterface refreshes what is linked to the interface: the ports
// of the account's inventory matched to it (refreshPort; without one the
// inventory is loaded first when it is older than an hour), the services
// linked to it otherwise and their connections.
func (s *Sync) refreshInterface(ctx context.Context, a *Account, src Source, class, interfaceID string) (RefreshResult, error) {
	m := merger{res: RefreshResult{Result: resultOK}}
	if class != "" {
		// A class that forbids the fetch is the result even when the
		// interface has no service of the account to mark stale.
		m.res.Result = class
	}
	done := map[string]bool{}
	if class == "" {
		ports, err := s.o.Cache.AccessPortsByInterface(ctx, a.OrganizationID, a.ID, interfaceID)
		if err != nil {
			return RefreshResult{}, err
		}
		if len(ports) == 0 {
			fetched, ferr := s.o.Cache.InventoryFetchedAt(ctx, a.OrganizationID, a.ID)
			if ferr != nil {
				return RefreshResult{}, ferr
			}
			if fetched.IsZero() || s.o.Now().Sub(fetched) > inventoryStale {
				r, rerr := s.refreshInventory(ctx, a, src, "")
				if rerr != nil {
					return RefreshResult{}, rerr
				}
				r.MatchResult = nil
				m.add(r)
				if ports, err = s.o.Cache.AccessPortsByInterface(ctx, a.OrganizationID, a.ID, interfaceID); err != nil {
					return RefreshResult{}, err
				}
			}
		}
		for i := range ports {
			r, services, perr := s.portRefresh(ctx, a, src, "", ports[i].ExternalID)
			if perr != nil {
				return RefreshResult{}, perr
			}
			m.add(r)
			for _, ext := range services {
				done[ext] = true
			}
		}
	}
	services, err := s.o.Cache.ListByInterface(ctx, a.OrganizationID, interfaceID)
	if err != nil {
		return RefreshResult{}, err
	}
	connections := map[string]bool{}
	for i := range services {
		sv := &services[i]
		if sv.AccountID != a.ID {
			continue
		}
		if sv.ConnectionID != "" {
			connections[sv.ConnectionID] = true
		}
		if done[sv.ExternalID] {
			continue
		}
		r, err := s.refreshService(ctx, a, src, class, sv.ExternalID)
		if err != nil {
			return RefreshResult{}, err
		}
		m.add(r)
	}
	for id := range connections {
		c, cerr := s.o.Cache.GetConnection(ctx, a.OrganizationID, id)
		if cerr != nil {
			continue
		}
		if class == "" && c.Stale && c.LastErrorClass == ClassNotImplemented {
			// A stub the source cannot fetch yet: nothing to refresh.
			continue
		}
		r, err := s.refreshConnection(ctx, a, src, class, c.ExternalID)
		if err != nil {
			return RefreshResult{}, err
		}
		m.add(r)
	}
	return m.res, nil
}

// refreshInventory loads the port inventory of the account, replaces the
// stored one and matches its ports to network interfaces (WP-1073, E-251).
func (s *Sync) refreshInventory(ctx context.Context, a *Account, src Source, class string) (RefreshResult, error) {
	if class != "" {
		s.count(KindInventory, class)
		return RefreshResult{Result: class}, nil
	}
	inv, err := src.FetchInventory(ctx)
	if err != nil {
		class = classOf(err)
		s.count(KindInventory, class)
		return RefreshResult{Result: class}, nil
	}
	now := s.o.Now().UTC()
	ports := make([]AccessPort, 0, len(inv))
	for _, p := range inv {
		ports = append(ports, AccessPort{ExternalID: p.PortKey, NodeExternalID: p.NodeExternalID, NodeName: p.NodeName,
			Protocol: p.Protocol, UnitIndex: p.UnitIndex, PortIndex: p.PortIndex})
	}
	if err = s.o.Cache.ReplaceAccessPorts(ctx, a.OrganizationID, a.ID, ports, now); err != nil {
		return RefreshResult{}, fmt.Errorf("admp: store inventory: %w", err)
	}
	match, err := s.o.Cache.MatchAccessPorts(ctx, a.OrganizationID, a.ID)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("admp: match inventory: %w", err)
	}
	countMatch(&match)
	s.count(KindInventory, resultOK)
	return RefreshResult{Result: resultOK, Refreshed: 1, MatchResult: &match}, nil
}

// refreshPort fetches the services of a port and each service, stores
// them and replaces the services of the port (WP-1073).
func (s *Sync) refreshPort(ctx context.Context, a *Account, src Source, class, portKey string) (RefreshResult, error) {
	res, _, err := s.portRefresh(ctx, a, src, class, portKey)
	return res, err
}

// portRefresh is refreshPort; it also returns the external ids of the
// services it refreshed.
func (s *Sync) portRefresh(ctx context.Context, a *Account, src Source, class, portKey string) (RefreshResult, []string, error) {
	if class != "" {
		s.count(KindPort, class)
		return RefreshResult{Result: class}, nil, nil
	}
	ids, err := src.FetchPortServices(ctx, portKey)
	if err != nil {
		class = classOf(err)
		s.count(KindPort, class)
		if !transient(class) {
			// A lasting failure (a port deleted in ADMP since the last
			// inventory, an undecodable answer) moves the port to its next
			// turn, so it does not lead every run of admp.refresh_linked and
			// spend the account's budget first; its services stay.
			if terr := s.o.Cache.TouchAccessPort(ctx, a.OrganizationID, a.ID, portKey, s.o.Now().UTC().Add(s.o.TTL)); terr != nil {
				return RefreshResult{}, nil, fmt.Errorf("admp: postpone port: %w", terr)
			}
		}
		return RefreshResult{Result: class}, nil, nil
	}
	m := merger{res: RefreshResult{Result: resultOK}}
	stored := make([]string, 0, len(ids))
	for _, ext := range ids {
		data, ferr := src.FetchService(ctx, ext)
		if ferr != nil {
			r, merr := s.fail(ctx, a, KindService, ext, classOf(ferr))
			if merr != nil {
				return RefreshResult{}, nil, merr
			}
			m.add(r)
		} else {
			r, _, serr := s.storeService(ctx, a, src, ext, data)
			if serr != nil {
				return RefreshResult{}, nil, serr
			}
			m.add(r)
		}
		// A service cached from before keeps its link to the port.
		if sv, cerr := s.o.Cache.ServiceByExternal(ctx, a.OrganizationID, a.ID, ext); cerr == nil {
			stored = append(stored, sv.ID)
		}
	}
	if err = s.o.Cache.ReplacePortServices(ctx, a.OrganizationID, a.ID, portKey, stored, s.o.Now().UTC().Add(s.o.TTL)); err != nil {
		return RefreshResult{}, nil, fmt.Errorf("admp: store the services of port: %w", err)
	}
	s.count(KindPort, m.res.Result)
	return m.res, ids, nil
}

// maxFetchCost is the most fetches one due object takes (a service).
const maxFetchCost = 2

// credit returns the budget an account carried over from earlier runs.
func (s *Sync) credit(accountID string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.carry[accountID]
}

// keepCredit carries the unused budget of a run over, but only as much as
// a budget per run below the cost of a service lacks for one: an account
// whose rate_limit_rps × run time is below two fetches saves up over runs
// until a service fits; any other gets at most rate_limit_rps × run time
// per run.
func (s *Sync) keepCredit(accountID string, perRun, unused float64) {
	keep := min(max(unused, 0), max(maxFetchCost-perRun, 0))
	s.mu.Lock()
	defer s.mu.Unlock()
	if keep <= 0 {
		delete(s.carry, accountID)
		return
	}
	s.carry[accountID] = keep
}

// fetchCost is the number of fetches a refresh of a due object takes.
func fetchCost(kind string) int {
	switch kind {
	case KindService:
		return maxFetchCost // the service and its ports
	case KindPort:
		return maxFetchCost // the services of the port and at least one service
	}
	return 1
}

// LinkedRun is the outcome of one admp.refresh_linked.
type LinkedRun struct {
	// Submitted counts the queued refreshes per account, Skipped the
	// accounts left out (no credential, open circuit breaker).
	Submitted map[string]int
	Skipped   map[string]string
}

// RefreshLinked queues admp.refresh for the services linked to a port and
// their connections whose expires_at lies within the next seven days, per
// enabled account at most as many fetches as its token bucket allows in
// the run time (no minimum; below the cost of a service the budget is
// saved up over runs, keepCredit); a service that does not fit leaves the
// budget to cheaper connections; the rest follows in the next run. An account without
// credential or with an open circuit breaker is skipped. The dedupe window
// is the run.
func (s *Sync) RefreshLinked(ctx context.Context) (LinkedRun, error) {
	out := LinkedRun{Submitted: map[string]int{}, Skipped: map[string]string{}}
	orgs, err := s.organizations(ctx)
	if err != nil {
		return out, err
	}
	start := s.o.Now().UTC()
	window := "run" + start.Format("20060102T150405.000000000")
	var errs []error
	for _, orgID := range orgs {
		octx := orgContext(ctx, orgID)
		accounts, err := s.o.Accounts.ListAccounts(octx, orgID)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: accounts of %s: %w", RefreshLinkedKind, orgID, err))
			continue
		}
		for i := range accounts {
			a := &accounts[i]
			if !a.Enabled {
				continue
			}
			src, class := s.sourceOf(octx, a)
			if class != "" {
				out.Skipped[a.ID] = class
				s.count("linked", class)
				continue
			}
			ports, inventory, err := s.linkedPorts(octx, a, src, start)
			if err != nil {
				var skip *skipError
				if errors.As(err, &skip) {
					out.Skipped[a.ID] = skip.class
					s.count("linked", skip.class)
					continue
				}
				errs = append(errs, fmt.Errorf("%s: ports of %s: %w", RefreshLinkedKind, a.ID, err))
				continue
			}
			due, err := s.o.Cache.ListDueLinked(octx, orgID, a.ID, start.Add(linkedHorizon))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: due entries of %s: %w", RefreshLinkedKind, a.ID, err))
				continue
			}
			if inventory {
				// With an inventory the services come with their ports.
				due = slices.DeleteFunc(due, func(d DueObject) bool { return d.Kind == KindService })
			}
			due = append(ports, due...)
			perRun := a.RateLimitRPS * s.o.LinkedRunTime.Seconds()
			budget := perRun + s.credit(a.ID)
			used := 0
			for _, d := range due {
				cost := fetchCost(d.Kind)
				if float64(used+cost) > budget {
					// A service (two fetches) that does not fit leaves the
					// budget to a cheaper connection behind it.
					continue
				}
				if _, _, err := s.Submit(octx, orgID, RefreshParams{AccountID: a.ID, Kind: d.Kind, ExternalID: d.ExternalID}, window); err != nil {
					errs = append(errs, fmt.Errorf("%s: submit: %w", RefreshLinkedKind, err))
					break
				}
				used += cost
				out.Submitted[a.ID]++
			}
			s.keepCredit(a.ID, perRun, budget-float64(used))
		}
	}
	return out, errors.Join(errs...)
}

// skipError makes admp.refresh_linked skip an account with a class.
type skipError struct{ class string }

func (e *skipError) Error() string { return "admp: account skipped (class " + e.class + ")" }

// inventoryMaxAge: admp.refresh_linked loads the inventory of an account
// once a day (E-251).
const inventoryMaxAge = 24 * time.Hour

// linkedPorts runs the steps of WP-1073 for one account of
// admp.refresh_linked: the login state (GET /V1/user/authentication; a
// success sets last_ok_at, a failure skips the account with its class),
// the inventory when it is older than a day (queued first) and the matched
// ports due within the horizon. A source without these reads
// (ClassNotImplemented) skips the steps. inventory reports whether the
// account has an inventory.
func (s *Sync) linkedPorts(ctx context.Context, a *Account, src Source, start time.Time) (due []DueObject, inventory bool, err error) {
	if cerr := src.CheckSession(ctx); cerr != nil {
		class := classOf(cerr)
		if class == ClassNotImplemented {
			return nil, false, nil
		}
		s.recordAccount(ctx, a, class)
		return nil, false, &skipError{class: class}
	}
	s.recordAccount(ctx, a, resultOK)
	fetched, err := s.o.Cache.InventoryFetchedAt(ctx, a.OrganizationID, a.ID)
	if err != nil {
		return nil, false, err
	}
	if fetched.IsZero() || start.Sub(fetched) > inventoryMaxAge {
		due = append(due, DueObject{Kind: KindInventory, ExpiresAt: fetched})
	}
	ports, err := s.o.Cache.ListDuePorts(ctx, a.OrganizationID, a.ID, start.Add(linkedHorizon))
	if err != nil {
		return nil, false, err
	}
	for i := range ports {
		due = append(due, DueObject{Kind: KindPort, ExternalID: ports[i].ExternalID, ExpiresAt: ports[i].ExpiresAt})
	}
	return due, !fetched.IsZero(), nil
}

// Expire removes the entries past their TTL in every organization and
// with them their personal surrogates (the trigger of admp_connection).
func (s *Sync) Expire(ctx context.Context) (Expired, error) {
	var total Expired
	orgs, err := s.organizations(ctx)
	if err != nil {
		return total, err
	}
	now := s.o.Now().UTC()
	var errs []error
	for _, orgID := range orgs {
		gone, err := s.o.Cache.DeleteExpired(orgContext(ctx, orgID), orgID, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", ExpireKind, orgID, err))
			continue
		}
		total.Connections += gone.Connections
		total.Services += gone.Services
	}
	expiredTotal.WithLabelValues(KindConnection).Add(float64(total.Connections))
	expiredTotal.WithLabelValues(KindService).Add(float64(total.Services))
	return total, errors.Join(errs...)
}

func (s *Sync) organizations(ctx context.Context) ([]string, error) {
	if s.o.Organizations == nil {
		return nil, nil
	}
	return s.o.Organizations(ctx)
}

// RefreshLinkedSchedule runs admp.refresh_linked every 15 minutes (leader
// only, like every system job).
func (s *Sync) RefreshLinkedSchedule() jobs.Schedule {
	return jobs.Schedule{Kind: RefreshLinkedKind, Spec: "@every 15m", Timeout: 10 * time.Minute,
		Run: func(ctx context.Context) error {
			run, err := s.RefreshLinked(ctx)
			if n := len(run.Submitted); n > 0 {
				slog.InfoContext(ctx, "admp refresh of linked entries", "accounts", n)
			}
			return err
		}}
}

// ExpireSchedule runs admp.expire hourly.
func (s *Sync) ExpireSchedule() jobs.Schedule {
	return jobs.Schedule{Kind: ExpireKind, Spec: "@every 1h", Timeout: 15 * time.Minute,
		Run: func(ctx context.Context) error {
			gone, err := s.Expire(ctx)
			if gone.Connections+gone.Services > 0 {
				slog.InfoContext(ctx, "admp expiry", "connections", gone.Connections, "services", gone.Services)
			}
			return err
		}}
}
