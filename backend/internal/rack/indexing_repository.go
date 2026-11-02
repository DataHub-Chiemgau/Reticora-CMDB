package rack

import (
	"context"
	"log/slog"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/prometheus/client_golang/prometheus"
)

// Indexer refreshes the search documents a rack placement changed (TEC-12,
// WP-346). A mount, move or unmount sets the location of the CI or, for a
// CI bound to an asset, of the asset and all its CIs; the database triggers
// derive client and site again, a remote index learns of it only here.
type Indexer interface {
	// ReindexPlacement indexes the CI again and, when it is bound to an
	// asset, the asset and every CI of the asset.
	ReindexPlacement(ctx context.Context, orgID, ciID string) error
}

// SearchReindexFailures counts placements whose search documents could not
// be refreshed; the placement itself stays committed.
var SearchReindexFailures = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "reticora",
	Name:      "rack_search_reindex_failures_total",
	Help:      "Rack placements (mount, move, unmount) whose search documents could not be indexed again; rebuild via POST /api/v1/search/reindex.",
})

// RegisterMetrics registers the rack metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(SearchReindexFailures)
}

// indexingRepository decorates a Repository so that every committed mount,
// move and unmount indexes the placed CI again (like ci.NewIndexingRepository
// for CI writes). Index failures are logged and counted and never fail the
// placement; the index can be rebuilt with POST /api/v1/search/reindex.
type indexingRepository struct {
	Repository
	indexer Indexer
}

// NewIndexingRepository wraps repo so rack placements are mirrored into the
// search index. A nil indexer returns the repository unchanged.
func NewIndexingRepository(repo Repository, indexer Indexer) Repository {
	if indexer == nil {
		return repo
	}
	return &indexingRepository{Repository: repo, indexer: indexer}
}

func (r *indexingRepository) CreateMount(ctx context.Context, m *RackMount) error {
	if err := r.Repository.CreateMount(ctx, m); err != nil {
		return err
	}
	r.reindex(ctx, m.OrganizationID, m.CIID)
	return nil
}

func (r *indexingRepository) UpdateMount(ctx context.Context, orgID, id string, req UpdateMountRequest) (*RackMount, error) {
	m, err := r.Repository.UpdateMount(ctx, orgID, id, req)
	if err != nil {
		return nil, err
	}
	r.reindex(ctx, orgID, m.CIID)
	return m, nil
}

// DeleteMount reads the mount first: after the delete its CI is unknown.
func (r *indexingRepository) DeleteMount(ctx context.Context, orgID, id string) error {
	current, getErr := r.GetMount(ctx, orgID, id)
	if err := r.Repository.DeleteMount(ctx, orgID, id); err != nil {
		return err
	}
	if getErr != nil {
		slog.WarnContext(ctx, "search index update after unmount skipped; rebuild via POST /api/v1/search/reindex",
			"mount_id", id, "error", getErr)
		SearchReindexFailures.Inc()
		return nil
	}
	r.reindex(ctx, orgID, current.CIID)
	return nil
}

// reindex runs after the commit of a transaction in ctx, at once otherwise.
func (r *indexingRepository) reindex(ctx context.Context, orgID, ciID string) {
	database.AfterCommit(ctx, func(ctx context.Context) {
		if err := r.indexer.ReindexPlacement(ctx, orgID, ciID); err != nil {
			slog.WarnContext(ctx, "search index update after rack placement failed; rebuild via POST /api/v1/search/reindex",
				"ci_id", ciID, "error", err)
			SearchReindexFailures.Inc()
		}
	})
}
