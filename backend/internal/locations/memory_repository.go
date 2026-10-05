package locations

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryRepository is an in-memory location tree for --no-db mode and
// tests. It applies the parent matrix, the cycle guard and the derivation of
// path, site and client like the database; it knows no tenant scope below
// the organization and no references from other tables.
type MemoryRepository struct {
	mu    sync.RWMutex
	nodes map[string]*Location
}

// NewMemoryRepository returns an empty in-memory location tree.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{nodes: map[string]*Location{}}
}

var _ Repository = (*MemoryRepository)(nil)

// List returns the nodes of the organization, parents first.
func (m *MemoryRepository) List(_ context.Context, orgID string, filter Filter) ([]Location, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Location{}
	search := strings.ToLower(filter.Search)
	for _, n := range m.nodes {
		switch {
		case n.OrganizationID != orgID,
			filter.RootOnly && n.ParentID != "",
			filter.ParentID != "" && n.ParentID != filter.ParentID,
			filter.Kind != "" && n.Kind != filter.Kind,
			search != "" && !strings.Contains(strings.ToLower(n.Name), search):
			continue
		}
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := strings.Count(out[i].Path, "."), strings.Count(out[j].Path, ".")
		if di != dj {
			return di < dj
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Create inserts a node below its parent.
func (m *MemoryRepository) Create(_ context.Context, orgID string, req CreateRequest) (*Location, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	n := &Location{ID: newID(), OrganizationID: orgID, Kind: req.Kind, Name: req.Name, CreatedAt: now, UpdatedAt: now}
	if req.Kind == KindSite {
		n.ClientID, n.SiteID, n.Path = req.ClientID, n.ID, label(n.ID)
	} else {
		parent, err := m.parentFor(orgID, n, req.ParentID)
		if err != nil {
			return nil, err
		}
		n.ParentID = parent.ID
		derive(n, parent)
	}
	m.nodes[n.ID] = n
	out := *n
	return &out, nil
}

// Get returns one node.
func (m *MemoryRepository) Get(_ context.Context, orgID, id string) (*Location, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	if !ok || n.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	out := *n
	return &out, nil
}

// Update renames and/or moves a node; a move re-derives the subtree.
func (m *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Location, error) {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return nil, fieldError("name", ErrInvalidInput, "name must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	var parent *Location
	if req.ParentID != nil {
		if n.Kind == KindSite {
			return nil, fieldError("parent_id", ErrInvalidParent, "a site is a root and cannot be moved")
		}
		var err error
		if parent, err = m.parentFor(orgID, n, *req.ParentID); err != nil {
			return nil, err
		}
		if parent.ID == n.ID || strings.HasPrefix(parent.Path+".", n.Path+".") {
			return nil, fieldError("parent_id", ErrCycle, "a node cannot be moved below itself")
		}
	}
	if req.Name != nil {
		n.Name = *req.Name
	}
	if parent != nil {
		n.ParentID = parent.ID
		m.rederive(n, parent)
	}
	n.UpdatedAt = time.Now().UTC()
	out := *n
	return &out, nil
}

// Delete removes a leaf node.
func (m *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.OrganizationID != orgID {
		return ErrNotFound
	}
	for _, c := range m.nodes {
		if c.ParentID == id {
			return &DependencyError{Kinds: []string{DependencyLocation}}
		}
	}
	delete(m.nodes, id)
	return nil
}

func (m *MemoryRepository) parentFor(orgID string, n *Location, parentID string) (*Location, error) {
	want, _ := n.Kind.ParentKind()
	parent, ok := m.nodes[parentID]
	if !ok || parent.OrganizationID != orgID {
		return nil, fieldError("parent_id", ErrInvalidParent, "parent %s not found", parentID)
	}
	if parent.Kind != want {
		return nil, fieldError("parent_id", ErrInvalidParent, "a %s needs a parent of kind %s, not %s", n.Kind, want, parent.Kind)
	}
	return parent, nil
}

// rederive updates n from parent and propagates to its subtree.
func (m *MemoryRepository) rederive(n, parent *Location) {
	derive(n, parent)
	for _, c := range m.nodes {
		if c.ParentID == n.ID {
			m.rederive(c, n)
		}
	}
}

func derive(n, parent *Location) {
	n.ClientID, n.SiteID, n.Path = parent.ClientID, parent.SiteID, parent.Path+"."+label(n.ID)
}

func label(id string) string { return strings.ReplaceAll(id, "-", "_") }

// newID returns a random version 4 UUID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
