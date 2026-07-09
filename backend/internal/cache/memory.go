package cache

import (
	"context"
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

	// Parse and increment
	var count int64
	for _, c := range entry.value {
		count = count*10 + int64(c-'0')
	}
	count++

	m.entries[key] = memEntry{
		value:     intToString(count),
		expiresAt: entry.expiresAt,
	}
	return count, nil
}

func (m *MemoryStore) Close() error {
	return nil
}

func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// Reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
