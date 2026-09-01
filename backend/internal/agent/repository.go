package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores registered endpoint agents.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Agent, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Agent, error)
	GetByAgentID(ctx context.Context, orgID, agentID string) (*Agent, error)
	Register(ctx context.Context, a *Agent) error
	// UpdatePolicy replaces the agent's policy; SetStatus drives the kill-switch
	// (disabled) and online/offline.
	UpdatePolicy(ctx context.Context, orgID, id string, req UpdatePolicyRequest) (*Agent, error)
	SetStatus(ctx context.Context, orgID, id, status string) (*Agent, error)
	// Heartbeat updates last_heartbeat and marks the agent online.
	Heartbeat(ctx context.Context, orgID, agentID string) error
	// SetCI links the agent to its CI (reconciled endpoint).
	SetCI(ctx context.Context, orgID, agentID, ciID string) error
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	seq    int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{agents: map[string]*Agent{}}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, page api.PaginationParams) ([]Agent, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Agent
	for _, a := range r.agents {
		if a.OrganizationID == orgID {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	total := len(out)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("agent not found")
	}
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) GetByAgentID(_ context.Context, orgID, agentID string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.agents {
		if a.OrganizationID == orgID && a.AgentID == agentID {
			cp := *a
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("agent not found")
}

func (r *MemoryRepository) Register(_ context.Context, a *Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.agents {
		if existing.OrganizationID == a.OrganizationID && existing.AgentID == a.AgentID {
			// re-enrollment refreshes the record
			existing.Hostname = a.Hostname
			existing.Version = a.Version
			existing.OS = a.OS
			existing.Arch = a.Arch
			existing.Status = "online"
			existing.UpdatedAt = time.Now().UTC()
			now := existing.UpdatedAt
			existing.LastHeartbeat = &now
			*a = *existing
			return nil
		}
	}
	r.seq++
	a.ID = fmt.Sprintf("agent-%08d", r.seq)
	if a.Status == "" {
		a.Status = "online"
	}
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	a.LastHeartbeat = &now
	if a.Policy.IntervalSeconds == 0 {
		a.Policy = DefaultPolicy()
	}
	r.agents[a.ID] = a
	return nil
}

func (r *MemoryRepository) UpdatePolicy(_ context.Context, orgID, id string, req UpdatePolicyRequest) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("agent not found")
	}
	if req.IntervalSeconds != nil {
		a.Policy.IntervalSeconds = *req.IntervalSeconds
	}
	if req.MetricsEnabled != nil {
		a.Policy.MetricsEnabled = *req.MetricsEnabled
	}
	if req.InventoryEnabled != nil {
		a.Policy.InventoryEnabled = *req.InventoryEnabled
	}
	a.UpdatedAt = time.Now().UTC()
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) SetStatus(_ context.Context, orgID, id, status string) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("agent not found")
	}
	a.Status = status
	a.UpdatedAt = time.Now().UTC()
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) Heartbeat(_ context.Context, orgID, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.OrganizationID == orgID && a.AgentID == agentID {
			now := time.Now().UTC()
			a.LastHeartbeat = &now
			if a.Status == "offline" {
				a.Status = "online"
			}
			a.UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("agent not found")
}

func (r *MemoryRepository) SetCI(_ context.Context, orgID, agentID, ciID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.OrganizationID == orgID && a.AgentID == agentID {
			a.CIID = ciID
			a.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return fmt.Errorf("agent not found")
}
