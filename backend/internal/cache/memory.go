package cache

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation of Store for development/testing.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]memEntry
}

type memEntry struct {
	value     string
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory cache store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string]memEntry),
	}
}

func (m *MemoryStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	entry, exists := m.entries[key]
	m.mu.RUnlock()

	if !exists {
		return "", false, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		m.mu.Lock()
		delete(m.entries, key)
		m.mu.Unlock()
		return "", false, nil
	}
	return entry.value, true, nil
}

func (m *MemoryStore) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	m.entries[key] = memEntry{value: value, expiresAt: expiresAt}
	return nil
}

func (m *MemoryStore) Increment(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.entries[key]
	if !exists || (!entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt)) {
		var expiresAt time.Time
		if ttl > 0 {
			expiresAt = time.Now().Add(ttl)
		}
		m.entries[key] = memEntry{value: "1", expiresAt: expiresAt}
		return 1, nil
	}

	count, _ := strconv.ParseInt(entry.value, 10, 64)
	count++

	m.entries[key] = memEntry{
		value:     strconv.FormatInt(count, 10),
		expiresAt: entry.expiresAt,
	}
	return count, nil
}

func (m *MemoryStore) Close() error {
	return nil
}
