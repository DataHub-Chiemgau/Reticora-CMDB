package locationnode

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Repository defines persistence for location nodes.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams) ([]Node, error)
	Tree(ctx context.Context, orgID string) ([]Node, error)
	GetByID(ctx context.Context, orgID, id string) (*Node, error)
	Create(ctx context.Context, node *Node) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Node, error)
	Delete(ctx context.Context, orgID, id string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	nodes map[string]*Node
	seq   int
}

// NewMemoryRepository creates an empty in-memory location repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{nodes: map[string]*Node{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams) ([]Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Node
	for _, n := range r.nodes {
		if n.OrganizationID != orgID {
			continue
		}
		if filter.RootOnly && n.ParentID != "" {
			continue
		}
		if filter.ParentID != "" && n.ParentID != filter.ParentID {
			continue
		}
		if filter.NodeType != "" && n.NodeType != filter.NodeType {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(n.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Tree returns the location hierarchy rooted at the root nodes.
func (r *MemoryRepository) Tree(ctx context.Context, orgID string) ([]Node, error) {
	all, err := r.List(ctx, orgID, FilterParams{})
	if err != nil {
		return nil, err
	}
	return BuildTree(all), nil
}

// BuildTree assembles the flat node list into a hierarchy.
func BuildTree(all []Node) []Node {
	byID := map[string]*Node{}
	for i := range all {
		n := all[i]
		byID[n.ID] = &n
	}
	var roots []Node
	for i := range all {
		n := all[i]
		if n.ParentID == "" {
			roots = append(roots, n)
			continue
		}
		parent, ok := byID[n.ParentID]
		if !ok {
			roots = append(roots, n)
			continue
		}
		parent.Children = append(parent.Children, n)
	}
	return roots
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[id]
	if !ok || n.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	out := *n
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, node *Node) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !NodeTypes[node.NodeType] {
		return fmt.Errorf("invalid node_type %q", node.NodeType)
	}
	if node.ParentID != "" {
		parent, ok := r.nodes[node.ParentID]
		if !ok || parent.OrganizationID != node.OrganizationID {
			return fmt.Errorf("parent location not found")
		}
	}
	node.ID = r.nextID()
	now := time.Now().UTC()
	node.CreatedAt = now
	node.UpdatedAt = now
	if node.Attributes == nil {
		node.Attributes = map[string]any{}
	}
	stored := *node
	r.nodes[node.ID] = &stored
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok || n.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Name != nil {
		n.Name = *req.Name
	}
	if req.ParentID != nil {
		// Prevent cycles: the new parent must not be a descendant of the node.
		if *req.ParentID != "" {
			if r.isDescendantLocked(*req.ParentID, id) {
				return nil, fmt.Errorf("moving a location below its own descendant would create a cycle")
			}
		}
		n.ParentID = *req.ParentID
	}
	if req.Barcode != nil {
		n.Barcode = *req.Barcode
	}
	if req.Attributes != nil {
		n.Attributes = req.Attributes
	}
	if req.SortOrder != nil {
		n.SortOrder = *req.SortOrder
	}
	n.UpdatedAt = time.Now().UTC()
	out := *n
	return &out, nil
}

func (r *MemoryRepository) isDescendantLocked(candidateID, ancestorID string) bool {
	current := candidateID
	for current != "" {
		if current == ancestorID {
			return true
		}
		n, ok := r.nodes[current]
		if !ok {
			return false
		}
		current = n.ParentID
	}
	return false
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok || n.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	for _, other := range r.nodes {
		if other.ParentID == id {
			return fmt.Errorf("location has child nodes")
		}
	}
	delete(r.nodes, id)
	return nil
}
