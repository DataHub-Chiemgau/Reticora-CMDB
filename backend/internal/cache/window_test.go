package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
)

// forEachWindowStore runs a sliding-window test on the in-memory store and
// on Redis (two clients, as two replicas would hold); the Redis case needs
// TEST_REDIS_URL.
func forEachWindowStore(t *testing.T, run func(t *testing.T, a, b Store)) {
	t.Run("memory store", func(t *testing.T) {
		m := NewMemoryStore()
		run(t, m, m)
	})
	t.Run("redis store", func(t *testing.T) {
		url := os.Getenv("TEST_REDIS_URL")
		if url == "" {
			t.Skip("TEST_REDIS_URL not set; skipping Redis window test (e.g. redis://localhost:56379/0)")
		}
		var stores [2]Store
		for i := range stores {
			client, err := redisx.Connect(context.Background(), url)
			if err != nil {
				t.Fatalf("connect Redis: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			stores[i] = NewRedisStore(client.Unwrap())
		}
		run(t, stores[0], stores[1])
	})
}

func windowKey(t *testing.T, store Store) string {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	key := "test:window:" + hex.EncodeToString(b)
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })
	return key
}

// TestStoreWindow covers the sliding window of WP-591 (F-304): at most
// limit events in (now-window, now]; an event leaves the window exactly
// window after it; the oldest event names the retry time; a refused event
// is not recorded; WindowCount records nothing.
func TestStoreWindow(t *testing.T) {
	forEachWindowStore(t, func(t *testing.T, a, b Store) {
		ctx := context.Background()
		key := windowKey(t, a)
		t0 := time.Date(2026, 10, 28, 10, 59, 30, 0, time.UTC)
		add := func(s Store, at time.Time) Window {
			t.Helper()
			w, err := s.WindowAdd(ctx, key, at, time.Hour, 3)
			if err != nil {
				t.Fatalf("WindowAdd: %v", err)
			}
			return w
		}
		if w, err := a.WindowCount(ctx, key, t0, time.Hour); err != nil || w.Count != 0 || !w.Oldest.IsZero() {
			t.Fatalf("empty window = %+v (%v)", w, err)
		}
		for i, s := range []Store{a, b, a} {
			if w := add(s, t0.Add(time.Duration(i)*time.Second)); !w.Added || w.Count != int64(i+1) || !w.Oldest.Equal(t0) {
				t.Fatalf("event %d = %+v", i+1, w)
			}
		}
		at := t0.Add(61 * time.Second)
		w := add(b, at)
		if w.Added || w.Count != 3 || !w.Oldest.Equal(t0) {
			t.Fatalf("event over the limit = %+v", w)
		}
		if got := w.RetryAfter(at, time.Hour); got != time.Hour-61*time.Second {
			t.Errorf("RetryAfter = %v, want %v", got, time.Hour-61*time.Second)
		}
		// One microsecond before t0+1h the oldest event still counts; at
		// t0+1h it has left, and the refused event was never recorded.
		if w := add(a, t0.Add(time.Hour-time.Microsecond)); w.Added {
			t.Errorf("event before the oldest left = %+v", w)
		}
		if w, err := b.WindowCount(ctx, key, t0.Add(time.Hour), time.Hour); err != nil || w.Count != 2 ||
			!w.Oldest.Equal(t0.Add(time.Second)) {
			t.Errorf("window after the oldest left = %+v (%v)", w, err)
		}
		if w := add(b, t0.Add(time.Hour)); !w.Added || w.Count != 3 {
			t.Errorf("event after the oldest left = %+v", w)
		}
		if w, err := a.WindowCount(ctx, key, t0.Add(3*time.Hour), time.Hour); err != nil || w.Count != 0 {
			t.Errorf("window hours later = %+v (%v)", w, err)
		}
	})
}

// TestStoreWindowIsAtomic: concurrent events of two replicas record at
// most limit events.
func TestStoreWindowIsAtomic(t *testing.T) {
	forEachWindowStore(t, func(t *testing.T, a, b Store) {
		ctx := context.Background()
		key := windowKey(t, a)
		now := time.Now()
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			added int
		)
		for i := 0; i < 40; i++ {
			s := a
			if i%2 == 1 {
				s = b
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				w, err := s.WindowAdd(ctx, key, now, time.Hour, 5)
				if err != nil {
					t.Errorf("WindowAdd: %v", err)
					return
				}
				if w.Added {
					mu.Lock()
					added++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if added != 5 {
			t.Errorf("%d events recorded, want 5", added)
		}
		if w, err := a.WindowCount(ctx, key, now, time.Hour); err != nil || w.Count != 5 {
			t.Errorf("window = %+v (%v), want 5 events", w, err)
		}
	})
}
