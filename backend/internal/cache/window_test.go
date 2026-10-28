package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"sync/atomic"
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

// TestStoreIncrementBelow covers the bounded counter of WP-658 (F-387, the
// pre-auth limit): IncrementBelow increments only below the limit and
// reads the other counter in the same step (0 when missing); a counter it
// refuses stays unchanged, and concurrent calls of two replicas never
// pass the limit.
func TestStoreIncrementBelow(t *testing.T) {
	forEachWindowStore(t, func(t *testing.T, a, b Store) {
		ctx := context.Background()
		key, other := windowKey(t, a), windowKey(t, a)
		for i := int64(1); i <= 3; i++ {
			res, err := a.IncrementBelow(ctx, key, 3, time.Minute, other)
			if want := (BoundedCount{Added: true, Count: i}); err != nil || res != want {
				t.Fatalf("increment %d below 3: %+v (%v), want %+v", i, res, err, want)
			}
		}
		for range 2 {
			if _, err := b.Increment(ctx, other, time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		res, err := b.IncrementBelow(ctx, key, 3, time.Minute, other)
		if want := (BoundedCount{Count: 3, Other: 2}); err != nil || res != want {
			t.Errorf("increment at the limit: %+v (%v), want %+v", res, err, want)
		}
		if v, _, err := a.Get(ctx, key); err != nil || v != "3" {
			t.Errorf("counter after a refused increment: %q (%v), want 3", v, err)
		}
		if n, err := a.Decrement(ctx, key); err != nil || n != 2 {
			t.Fatalf("decrement: %d (%v)", n, err)
		}
		if res, err := b.IncrementBelow(ctx, key, 3, time.Minute, other); err != nil || !res.Added || res.Count != 3 {
			t.Errorf("increment after a unit was given back: %+v (%v), want added, 3", res, err)
		}

		bounded := windowKey(t, a)
		var (
			wg    sync.WaitGroup
			added atomic.Int64
		)
		for i := range 50 {
			s := a
			if i%2 == 1 {
				s = b
			}
			wg.Go(func() {
				res, err := s.IncrementBelow(ctx, bounded, 20, time.Minute, other)
				if err != nil {
					t.Errorf("IncrementBelow: %v", err)
				}
				if res.Added {
					added.Add(1)
				}
			})
		}
		wg.Wait()
		if n := added.Load(); n != 20 {
			t.Errorf("50 concurrent increments below 20: %d incremented, want 20", n)
		}
	})
}

// TestMemoryStoreIncrementBelowExpires: the counter IncrementBelow
// creates expires after its TTL like one of Increment.
func TestMemoryStoreIncrementBelowExpires(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()
	if res, _ := m.IncrementBelow(ctx, "k", 1, 50*time.Millisecond, "o"); !res.Added {
		t.Fatal("first increment refused")
	}
	if res, _ := m.IncrementBelow(ctx, "k", 1, 50*time.Millisecond, "o"); res.Added {
		t.Fatal("increment at the limit added")
	}
	time.Sleep(80 * time.Millisecond)
	if res, _ := m.IncrementBelow(ctx, "k", 1, 50*time.Millisecond, "o"); !res.Added || res.Count != 1 {
		t.Errorf("increment after the TTL: %+v, want a new counter of 1", res)
	}
}
