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
	// windows holds the event times of the sliding windows, oldest first.
	windows map[string][]time.Time
}

type memEntry struct {
	value     string
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory cache store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string]memEntry),
		windows: make(map[string][]time.Time),
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

	count, err := strconv.ParseInt(entry.value, 10, 64)
	if err != nil {
		// Corrupted (non-numeric) entry: reset the counter instead of
		// silently treating it as 0 while keeping the bogus value.
		count = 1
		m.entries[key] = memEntry{value: "1", expiresAt: entry.expiresAt}
		return count, nil
	}
	count++

	m.entries[key] = memEntry{
		value:     strconv.FormatInt(count, 10),
		expiresAt: entry.expiresAt,
	}
	return count, nil
}

func (m *MemoryStore) Decrement(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.entries[key]
	if !exists || (!entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt)) {
		delete(m.entries, key)
		return 0, nil
	}
	count, err := strconv.ParseInt(entry.value, 10, 64)
	if err != nil {
		// Corrupted (non-numeric) entry: reset the counter, as Increment does.
		count = 1
	}
	count--
	m.entries[key] = memEntry{value: strconv.FormatInt(count, 10), expiresAt: entry.expiresAt}
	return count, nil
}

func (m *MemoryStore) IncrementBelow(_ context.Context, key string, limit int64, ttl time.Duration, other string) (BoundedCount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	value := func(k string) (memEntry, int64, bool) {
		entry, exists := m.entries[k]
		if !exists || (!entry.expiresAt.IsZero() && now.After(entry.expiresAt)) {
			return memEntry{}, 0, false
		}
		// A corrupted (non-numeric) entry counts as 0, as in Increment.
		n, _ := strconv.ParseInt(entry.value, 10, 64)
		return entry, n, true
	}
	entry, count, exists := value(key)
	res := BoundedCount{Count: count}
	if count < limit {
		if !exists && ttl > 0 {
			entry.expiresAt = now.Add(ttl)
		}
		res.Added, res.Count = true, count+1
		m.entries[key] = memEntry{value: strconv.FormatInt(res.Count, 10), expiresAt: entry.expiresAt}
	}
	_, res.Other, _ = value(other)
	return res, nil
}

func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.entries, key)
	delete(m.windows, key)
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) WindowAdd(_ context.Context, key string, now time.Time, window time.Duration, limit int64) (Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := m.pruneWindow(key, now, window)
	added := limit > 0 && int64(len(events)) < limit
	if added {
		// Kept in time order, like the scores of the Redis set.
		i := len(events)
		for i > 0 && events[i-1].After(now) {
			i--
		}
		events = append(events, time.Time{})
		copy(events[i+1:], events[i:])
		events[i] = now
		m.windows[key] = events
	}
	return windowOf(events, added), nil
}

func (m *MemoryStore) WindowCount(_ context.Context, key string, now time.Time, window time.Duration) (Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return windowOf(m.pruneWindow(key, now, window), false), nil
}

// pruneWindow drops the events of key at or before now-window.
func (m *MemoryStore) pruneWindow(key string, now time.Time, window time.Duration) []time.Time {
	events := m.windows[key]
	cut := now.Add(-window)
	i := 0
	for i < len(events) && !events[i].After(cut) {
		i++
	}
	events = events[i:]
	if len(events) == 0 {
		delete(m.windows, key)
		return nil
	}
	m.windows[key] = events
	return events
}

func windowOf(events []time.Time, added bool) Window {
	w := Window{Added: added, Count: int64(len(events))}
	if len(events) > 0 {
		w.Oldest = events[0]
	}
	return w
}

func (m *MemoryStore) Close() error {
	return nil
}
