package graphqlbff

import (
	"context"
	"sync"
	"time"

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
}

type loaderBatch struct {
	done chan struct{}
	err  error
}

func withLoader(ctx context.Context, repo ci.Repository, orgID string) context.Context {
	return context.WithValue(ctx, loaderKey{}, &ciLoader{repo: repo, orgID: orgID, cache: map[string]*ci.Item{}})
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
