package agent

import (
	"context"
	"errors"
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

	// CreateEnrollmentToken stores a token under the hash of its secret.
	CreateEnrollmentToken(ctx context.Context, orgID string, tok *EnrollmentToken, tokenHash string) error
	// Enroll consumes the token with tokenHash and registers the agent bound
	// to the token's client and site, in one transaction (AGT-06). An
	// unknown, used or expired token yields ErrInvalidEnrollmentToken.
	Enroll(ctx context.Context, a *Agent, tokenHash, ipAddress string) error
	// SuggestSite records the agent's network fingerprint and suggests the
	// site whose subnet contains it when it differs from the confirmed one.
	SuggestSite(ctx context.Context, orgID, agentID, ipAddress string) error
	// ConfirmSite sets the agent's site manually; the site must belong to
	// the agent's client.
	ConfirmSite(ctx context.Context, orgID, id, siteID string) (*Agent, error)
}

// ErrInvalidEnrollmentToken reports an unknown, used or expired token.
var ErrInvalidEnrollmentToken = errors.New("invalid, used or expired enrollment token")

// ErrSiteNotOfClient reports a site outside the agent's client.
var ErrSiteNotOfClient = errors.New("agent not found or site does not belong to its client")

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	tokens map[string]*memoryToken
	seq    int
}

type memoryToken struct {
	orgID string
	tok   EnrollmentToken
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{agents: map[string]*Agent{}, tokens: map[string]*memoryToken{}}
}

func (r *MemoryRepository) CreateEnrollmentToken(_ context.Context, orgID string, tok *EnrollmentToken, tokenHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	tok.ID = fmt.Sprintf("token-%08d", r.seq)
	tok.CreatedAt = time.Now().UTC()
	stored := *tok
	stored.Token = ""
	r.tokens[tokenHash] = &memoryToken{orgID: orgID, tok: stored}
	return nil
}

func (r *MemoryRepository) Enroll(ctx context.Context, a *Agent, tokenHash, ipAddress string) error {
	r.mu.Lock()
	mt, ok := r.tokens[tokenHash]
	now := time.Now().UTC()
	if !ok || mt.orgID != a.OrganizationID || mt.tok.UsedAt != nil || !mt.tok.ExpiresAt.After(now) {
		r.mu.Unlock()
		return ErrInvalidEnrollmentToken
	}
	mt.tok.UsedAt = &now
	r.mu.Unlock()
	a.ClientID, a.SiteID, a.NetworkFingerprint = mt.tok.ClientID, mt.tok.SiteID, ipAddress
	if a.SiteID != "" {
		a.SiteConfirmedAt = &now
	}
	return r.Register(ctx, a)
}

func (r *MemoryRepository) SuggestSite(_ context.Context, orgID, agentID, ipAddress string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.OrganizationID == orgID && a.AgentID == agentID {
			a.NetworkFingerprint = ipAddress
			return nil
		}
	}
	return fmt.Errorf("agent not found")
}

func (r *MemoryRepository) ConfirmSite(_ context.Context, orgID, id, siteID string) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok || a.OrganizationID != orgID || a.ClientID == "" {
		return nil, ErrSiteNotOfClient
	}
	now := time.Now().UTC()
	a.SiteID, a.SuggestedSiteID, a.SiteConfirmedAt = siteID, "", &now
	cp := *a
	return &cp, nil
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
			existing.ClientID, existing.SiteID = a.ClientID, a.SiteID
			existing.NetworkFingerprint, existing.SiteConfirmedAt = a.NetworkFingerprint, a.SiteConfirmedAt
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
