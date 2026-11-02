package cache

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemoryStore_GetSet(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Get non-existent key
	val, found, err := store.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected not found")
	}
	if val != "" {
		t.Fatalf("expected empty value, got %q", val)
	}

	// Set and get
	if err := store.Set(ctx, "key1", "value1", time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, found, err = store.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found")
	}
	if val != "value1" {
		t.Fatalf("expected value1, got %q", val)
	}
}

func TestMemoryStore_Expiration(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.Set(ctx, "expire", "val", time.Millisecond); err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)

	_, found, err := store.Get(ctx, "expire")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected entry to be expired")
	}
}

func TestMemoryStore_Increment(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	count, err := store.Increment(ctx, "counter", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}

	count, err = store.Increment(ctx, "counter", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}

	count, err = store.Increment(ctx, "counter", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3, got %d", count)
	}
}

func TestMemoryStore_IncrementExpiration(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, err := store.Increment(ctx, "counter2", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)

	// After expiry, should start from 1 again
	count, err := store.Increment(ctx, "counter2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 after expiry, got %d", count)
	}
}

func TestMemoryStore_SetNoTTL(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.Set(ctx, "forever", "val", 0); err != nil {
		t.Fatal(err)
	}

	val, found, err := store.Get(ctx, "forever")
	if err != nil {
		t.Fatal(err)
	}
	if !found || val != "val" {
		t.Fatalf("expected to find 'val', got found=%v val=%q", found, val)
	}
}

// TestMemoryStore_Decrement covers the release of a pre-auth reservation
// (WP-462, AUT-10): Decrement lowers an existing counter atomically and
// keeps its expiry; a missing or expired key is not created.
func TestMemoryStore_Decrement(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	for range 3 {
		if _, err := store.Increment(ctx, "counter", 30*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := store.Decrement(ctx, "counter"); err != nil || count != 2 {
		t.Fatalf("decrement of 3: %d %v, want 2", count, err)
	}
	if count, err := store.Increment(ctx, "counter", time.Hour); err != nil || count != 3 {
		t.Fatalf("increment after decrement: %d %v, want 3", count, err)
	}

	// The expiry of the window stays: neither call extends it.
	time.Sleep(40 * time.Millisecond)
	if _, found, _ := store.Get(ctx, "counter"); found {
		t.Fatal("decremented counter outlived its TTL")
	}
	if count, err := store.Decrement(ctx, "counter"); err != nil || count != 0 {
		t.Fatalf("decrement of an expired key: %d %v, want 0", count, err)
	}
	if count, err := store.Decrement(ctx, "missing"); err != nil || count != 0 {
		t.Fatalf("decrement of a missing key: %d %v, want 0", count, err)
	}
	if _, found, _ := store.Get(ctx, "missing"); found {
		t.Fatal("decrement created a missing key")
	}

	// Concurrent increments and decrements stay exact.
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.Increment(ctx, "parallel", time.Minute)
			_, _ = store.Increment(ctx, "parallel", time.Minute)
			_, _ = store.Decrement(ctx, "parallel")
		}()
	}
	wg.Wait()
	if value, _, _ := store.Get(ctx, "parallel"); value != "50" {
		t.Fatalf("parallel counter %s, want 50", value)
	}
}
