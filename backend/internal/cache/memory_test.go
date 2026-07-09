package cache

import (
	"context"
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
