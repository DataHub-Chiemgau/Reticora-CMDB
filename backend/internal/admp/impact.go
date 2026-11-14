package admp

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Affected connections for the recommendations of an outage (ADM-08,
// ALR-09; WP-931).
//
// Contract for the incident domain (WP-1037 p3-inc-affected, WP-1038,
// WP-1043):
//
//   - The entry point is the method Impact.AffectedConnections. A consumer
//     creates its instance at the server wiring with
//     admp.NewImpact(repos.ADMP, admpSync): the cache of the repositories
//     (PostgreSQL or the memory store of --no-db) and the Sync of the read
//     API, which queues the refresh of WithRefreshStale (nil: none). The
//     instance holds no state of its own and is safe for concurrent use.
//   - The caller runs the impact analysis first (WP-1025, WP-1028) and
//     passes the complete set of affected CIs, and the affected network
//     interfaces where it knows them, to Impact.AffectedConnections.
//   - The result has one entry per affected connection and port: the
//     connection (connection_id, external_id, account_id, carrier,
//     contract_id), the service (service_id, service_kind), the port
//     (port_key, ci_id, network_interface_id) and the cache state (stale,
//     fetched_at). A port is affected when its CI is in ciIDs or its
//     interface in interfaceIDs.
//   - redundant reports that another port of the same service or of
//     another service of the same connection is not affected: its CI lies
//     outside ciIDs and its interface outside interfaceIDs. redundant_via
//     names the CIs of those ports, sorted. The redundancy is therefore only
//     as complete as the set of CIs the caller passes.
//   - Only the cache is read (one set-based statement over both lists);
//     ADMP is never called in the alarm path. WithRefreshStale queues
//     admp.refresh for the stale entries (dedupe of WP-929 per minute): the
//     object kind connection for a stale connection, service for a stale
//     service. The entries returned are those of the cache. The refresh is
//     queued independently of a transaction of the caller (outside it): a
//     failure is only logged and never aborts the caller's transaction.
//   - The call runs in the tenant scope of ctx: row level security and the
//     client separation (E-09) apply, a principal restricted to clients
//     gets only its clients' and shared rows. A worker without request
//     scope runs organization-wide (database.WithTenant through
//     WithRequestTenant on an organization-wide scope).
//   - No personal data: neither holder nor address of a connection is part
//     of an entry.
//
// The data follow the documentation of ADMP from WP-1073 on: carrier is
// the name of the network operator (WP-1075), service_kind the template
// name of the service, the port link comes from the port inventory.

// AffectedConnection is an affected connection at one port.
type AffectedConnection struct {
	ConnectionID       string    `json:"connection_id"`
	ExternalID         string    `json:"external_id"`
	AccountID          string    `json:"account_id"`
	Carrier            string    `json:"carrier,omitempty"`
	ContractID         string    `json:"contract_id,omitempty"`
	ServiceID          string    `json:"service_id"`
	ServiceKind        string    `json:"service_kind"`
	PortKey            string    `json:"port_key"`
	CIID               string    `json:"ci_id,omitempty"`
	NetworkInterfaceID string    `json:"network_interface_id,omitempty"`
	Stale              bool      `json:"stale"`
	FetchedAt          time.Time `json:"fetched_at"`
	Redundant          bool      `json:"redundant"`
	RedundantVia       []string  `json:"redundant_via"`

	// The parts of Stale with the service's external id: WithRefreshStale
	// refreshes the stale object (both stores set them).
	serviceExternalID string
	connectionStale   bool
	serviceStale      bool
}

// Impact gives the affected connections of an outage from the cache.
type Impact struct {
	cache Repository
	sync  *Sync
}

// NewImpact creates the feed on the cache; sync queues the refresh of
// WithRefreshStale (nil: none).
func NewImpact(cache Repository, sync *Sync) *Impact { return &Impact{cache: cache, sync: sync} }

// AffectedOption changes a call of AffectedConnections.
type AffectedOption func(*affectedOptions)

type affectedOptions struct{ refreshStale bool }

// WithRefreshStale queues admp.refresh for the stale connections of the
// result; the call does not wait for it.
func WithRefreshStale() AffectedOption { return func(o *affectedOptions) { o.refreshStale = true } }

// AffectedConnections returns the connections with a port on one of
// ciIDs or interfaceIDs (see the contract above). Ids that are no UUIDs
// are ignored; empty lists give an empty result without a statement.
func (i *Impact) AffectedConnections(ctx context.Context, orgID string, ciIDs, interfaceIDs []string,
	opts ...AffectedOption) ([]AffectedConnection, error) {
	var o affectedOptions
	for _, opt := range opts {
		opt(&o)
	}
	if _, ok := database.TenantScopeFromContext(ctx); !ok {
		ctx = orgContext(ctx, orgID)
	}
	out, err := i.cache.AffectedConnections(ctx, orgID, uuids(ciIDs), uuids(interfaceIDs))
	if err != nil {
		return nil, err
	}
	if o.refreshStale && i.sync != nil {
		i.refreshStale(ctx, orgID, out)
	}
	return out, nil
}

// refreshStale queues one refresh per stale connection and per stale
// service, outside a transaction of the caller; a failure is logged, the
// alarm path goes on.
func (i *Impact) refreshStale(ctx context.Context, orgID string, rows []AffectedConnection) {
	ctx = database.WithoutTx(ctx)
	window := MinuteWindow(i.sync.o.Now())
	seen := map[string]bool{}
	submit := func(key string, p RefreshParams) {
		if seen[key] {
			return
		}
		seen[key] = true
		if _, _, err := i.sync.Submit(ctx, orgID, p, window); err != nil {
			slog.WarnContext(ctx, "admp refresh of a stale affected entry", "kind", p.Kind, "error", err)
		}
	}
	for k := range rows {
		r := &rows[k]
		if r.connectionStale {
			submit("c"+r.ConnectionID, RefreshParams{AccountID: r.AccountID, Kind: KindConnection, ExternalID: r.ExternalID})
		}
		if r.serviceStale && r.serviceExternalID != "" {
			submit("s"+r.ServiceID, RefreshParams{AccountID: r.AccountID, Kind: KindService, ExternalID: r.serviceExternalID})
		}
	}
}

// uuids keeps the distinct UUIDs of ids.
func uuids(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if api.IsUUID(id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// redundancyByConnection folds the entries of affected connections into
// one redundancy per connection: redundant when any entry is, the union of
// redundant_via.
func redundancyByConnection(rows []AffectedConnection) map[string][]string {
	out := map[string][]string{}
	for k := range rows {
		r := &rows[k]
		via := out[r.ConnectionID]
		if via == nil {
			via = []string{}
		}
		for _, ci := range r.RedundantVia {
			if !slices.Contains(via, ci) {
				via = append(via, ci)
			}
		}
		slices.Sort(via)
		out[r.ConnectionID] = via
	}
	return out
}

// sortAffected orders the entries by carrier, connection, service and
// port (both stores).
func sortAffected(rows []AffectedConnection) {
	slices.SortFunc(rows, func(a, b AffectedConnection) int {
		for _, c := range [][2]string{{a.Carrier, b.Carrier}, {a.ExternalID, b.ExternalID}, {a.ConnectionID, b.ConnectionID},
			{a.ServiceID, b.ServiceID}, {a.PortKey, b.PortKey}} {
			if n := strings.Compare(c[0], c[1]); n != 0 {
				return n
			}
		}
		return 0
	})
}
