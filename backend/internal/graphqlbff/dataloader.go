package graphqlbff

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// The CI dataloader (GQL-02): nested CI fields (relationship ends, topology
// nodes, rack mounts, review candidates) read their CIs per request in
// batches instead of one query per object. List resolvers prime it with all
// ids they hand out; a field resolver that misses the cache joins a batch
// collected for batchWait.

const batchWait = 2 * time.Millisecond

type loaderKey struct{}

// ciLoader caches the CIs of one request; nil marks a CI that is not
// visible.
type ciLoader struct {
	repo  ci.Repository
	orgID string

	mu    sync.Mutex
	cache map[string]*ci.Item
	queue []string
	batch *loaderBatch

	// The CI lists of the asset detail (GQL-03): the CIs of an asset and
	// the child CIs of a set of parents, each read once per request;
	// childrenOf holds the complete children per parent CI.
	assetCIs   map[string]*ciList
	children   map[string]*ciList
	childrenOf map[string][]ci.Item

	// The P3 fields at the CI (WP-1136): siblings maps a CI to the CIs a
	// list handed out with it (one level of the answer); batches holds one
	// read per field, arguments and level.
	siblings map[string][]string
	batches  map[string]*levelBatch
}

// levelBatch is one batch read of a field for the CIs of a level: the value
// per CI id.
type levelBatch struct {
	once   sync.Once
	values map[string]any
	err    error
}

// ciList is one CI list read once per request.
type ciList struct {
	once  sync.Once
	items []ci.Item
	err   error
}

type loaderBatch struct {
	done chan struct{}
	err  error
}

func withLoader(ctx context.Context, repo ci.Repository, orgID string) context.Context {
	return context.WithValue(ctx, loaderKey{}, &ciLoader{repo: repo, orgID: orgID, cache: map[string]*ci.Item{},
		assetCIs: map[string]*ciList{}, children: map[string]*ciList{}, childrenOf: map[string][]ci.Item{},
		siblings: map[string][]string{}, batches: map[string]*levelBatch{}})
}

func loaderFrom(ctx context.Context) *ciLoader {
	l, _ := ctx.Value(loaderKey{}).(*ciLoader)
	return l
}

// Prime reads the CIs of ids not cached yet in one batch.
func (l *ciLoader) Prime(ctx context.Context, ids []string) error {
	l.mu.Lock()
	missing := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if _, cached := l.cache[id]; !cached && id != "" && !seen[id] {
			seen[id] = true
			missing = append(missing, id)
		}
	}
	l.mu.Unlock()
	if len(missing) == 0 {
		return nil
	}
	items, err := l.read(ctx, missing)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range missing {
		if _, cached := l.cache[id]; !cached {
			l.cache[id] = nil
		}
	}
	for i := range items {
		item := items[i]
		l.cache[item.ID] = &item
	}
	return nil
}

// read loads ids in one query when the repository reads in batches.
func (l *ciLoader) read(ctx context.Context, ids []string) ([]ci.Item, error) {
	if reader, ok := l.repo.(ci.BatchReader); ok {
		return reader.GetByIDs(ctx, l.orgID, ids)
	}
	items := make([]ci.Item, 0, len(ids))
	for _, id := range ids {
		if item, err := l.repo.GetByID(ctx, l.orgID, id); err == nil {
			items = append(items, *item)
		}
	}
	return items, nil
}

// Load returns the CI of id (nil when not visible), from the cache or a
// batch shared with the concurrent field resolvers.
func (l *ciLoader) Load(ctx context.Context, id string) (*ci.Item, error) {
	l.mu.Lock()
	if item, cached := l.cache[id]; cached {
		l.mu.Unlock()
		return item, nil
	}
	l.queue = append(l.queue, id)
	b := l.batch
	if b == nil {
		b = &loaderBatch{done: make(chan struct{})}
		l.batch = b
		time.AfterFunc(batchWait, func() { l.flush(ctx, b) })
	}
	l.mu.Unlock()
	<-b.done
	if b.err != nil {
		return nil, b.err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cache[id], nil
}

func (l *ciLoader) flush(ctx context.Context, b *loaderBatch) {
	l.mu.Lock()
	ids := l.queue
	l.queue = nil
	l.batch = nil
	l.mu.Unlock()
	b.err = l.Prime(ctx, ids)
	close(b.done)
}

// store puts CIs a list read into the cache (prime without a read).
func (l *ciLoader) store(items []ci.Item) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range items {
		item := items[i]
		l.cache[item.ID] = &item
	}
}

// list returns the entry of key in lists, created on first use.
func (l *ciLoader) list(lists map[string]*ciList, key string) *ciList {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := lists[key]
	if e == nil {
		e = &ciList{}
		lists[key] = e
	}
	return e
}

// listCIs reads at most maxPage CIs matching filter and primes the cache
// with them.
func (l *ciLoader) listCIs(ctx context.Context, filter api.Filter) ([]ci.Item, int, error) {
	items, total, err := l.repo.List(ctx, l.orgID, &ci.FilterParams{Filters: []api.Filter{filter}},
		api.PaginationParams{Limit: maxPage})
	if err != nil {
		return nil, 0, err
	}
	l.store(items)
	return items, total, nil
}

// AssetCIs returns the CIs linked to an asset (ci.asset_id, at most
// maxPage), read once per request whichever field asks first.
func (l *ciLoader) AssetCIs(ctx context.Context, assetID string) ([]ci.Item, error) {
	e := l.list(l.assetCIs, assetID)
	e.once.Do(func() {
		e.items, _, e.err = l.listCIs(ctx, api.Filter{Field: "asset_id", Op: api.OpExact, Kind: api.KindUUID,
			Values: []string{assetID}})
	})
	return e.items, e.err
}

// Children returns the child CIs (ci.parent_ci_id, AST-05) of the parent
// CIs, at most maxPage: the parents not cached yet are read in one query
// with the operator in, and a complete answer is cached per parent CI, so
// the same parents are never read twice in a request.
func (l *ciLoader) Children(ctx context.Context, parents []string) ([]ci.Item, error) {
	ids := make([]string, 0, len(parents))
	for _, id := range parents {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []ci.Item{}, nil
	}
	if out, ok := l.cachedChildren(ids); ok {
		return out, nil
	}
	sorted := slices.Sorted(slices.Values(ids))
	e := l.list(l.children, strings.Join(sorted, ","))
	e.once.Do(func() {
		var total int
		e.items, total, e.err = l.listCIs(ctx, api.Filter{Field: "parent_ci_id", Op: api.OpIn, Kind: api.KindUUID, Values: sorted})
		if e.err != nil || total > len(e.items) {
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, id := range sorted {
			l.childrenOf[id] = []ci.Item{}
		}
		for i := range e.items {
			l.childrenOf[e.items[i].ParentCIID] = append(l.childrenOf[e.items[i].ParentCIID], e.items[i])
		}
	})
	return e.items, e.err
}

// cachedChildren returns the children of ids when every parent is cached.
func (l *ciLoader) cachedChildren(ids []string) ([]ci.Item, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ci.Item
	for _, id := range ids {
		kids, ok := l.childrenOf[id]
		if !ok {
			return nil, false
		}
		out = append(out, kids...)
	}
	return append([]ci.Item{}, out...), true
}

// registerSiblings records the CIs of items as one level of the answer: a
// P3 field of one of them reads the field for all of them in one batch
// (WP-1136 AK4/AK6). A CI already in a level keeps it.
func (l *ciLoader) registerSiblings(items []*ci.Item) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ID)
		}
	}
	l.registerLevel(ids)
}

// registerLevel records the CI ids a list hands out (the ends of the
// relationships, the nodes of a topology, the mounts of a rack, the
// candidates of the review items) as one level, like registerSiblings.
func (l *ciLoader) registerLevel(raw []string) {
	ids := make([]string, 0, len(raw))
	for _, id := range raw {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range ids {
		if _, ok := l.siblings[id]; !ok {
			l.siblings[id] = ids
		}
	}
}

// fieldBatch returns the value of ciID of the batch key over the level of ciID
// (the CI alone without one): the first field resolver of the level reads
// with fetch, the others wait for it.
func (l *ciLoader) fieldBatch(key, ciID string, fetch func(ids []string) (map[string]any, error)) (any, error) {
	l.mu.Lock()
	level, ok := l.siblings[ciID]
	if !ok {
		level = []string{ciID}
	}
	sorted := slices.Sorted(slices.Values(level))
	full := key + "|" + strings.Join(sorted, ",")
	b := l.batches[full]
	if b == nil {
		b = &levelBatch{}
		l.batches[full] = b
	}
	l.mu.Unlock()
	b.once.Do(func() { b.values, b.err = fetch(sorted) })
	if b.err != nil {
		return nil, b.err
	}
	return b.values[ciID], nil
}
