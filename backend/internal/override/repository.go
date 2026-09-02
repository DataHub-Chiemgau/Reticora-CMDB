package override

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for field provenance and overrides.
type Repository interface {
	ListForCI(ctx context.Context, orgID, ciID string) ([]FieldValue, error)
	Get(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error)
	// RecordDiscovered upserts the discovered side of a field. It never
	// touches the override side: a protected manual override survives every
	// discovery run (spec §13).
	RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string) (*FieldValue, error)
	SetOverride(ctx context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error)
	ClearOverride(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error)
	// Conflicts returns field values where discovered and effective diverge.
	Conflicts(ctx context.Context, orgID string, page api.PaginationParams) ([]FieldValue, int, error)
	// Policy returns the organization's default source-priority policy.
	Policy(ctx context.Context, orgID string) (*SourcePolicy, error)
	UpsertPolicy(ctx context.Context, policy *SourcePolicy) error
}

// IsProtected reports whether the field carries a protected manual override.
// Shared helper so other modules (discovery ingest) honor the protection
// without depending on the concrete repository (spec §13).
func IsProtected(ctx context.Context, repo Repository, orgID, ciID, fieldName string) (bool, error) {
	fv, err := repo.Get(ctx, orgID, ciID, fieldName)
	if err != nil {
		// No provenance row yet: nothing is protected.
		return false, nil
	}
	return fv.Protected && fv.OverrideValue != nil, nil
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu       sync.RWMutex
	values   map[string]*FieldValue // orgID/ciID/field -> value
	policies map[string]*SourcePolicy
	seq      int
}

// NewMemoryRepository creates an empty in-memory override repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{values: map[string]*FieldValue{}, policies: map[string]*SourcePolicy{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

func key(orgID, ciID, field string) string { return orgID + "/" + ciID + "/" + field }

func (r *MemoryRepository) withEffective(fv *FieldValue) *FieldValue {
	priorities := DefaultPriorities
	if p, ok := r.policies[fv.OrganizationID]; ok && len(p.Priorities) > 0 {
		priorities = p.Priorities
	}
	out := *fv
	out.EffectiveValue = ResolveEffective(fv, priorities)
	out.Diverged = IsDiverged(fv, priorities)
	return &out
}

func (r *MemoryRepository) ListForCI(_ context.Context, orgID, ciID string) ([]FieldValue, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []FieldValue
	for _, fv := range r.values {
		if fv.OrganizationID == orgID && fv.CIID == ciID {
			out = append(out, *r.withEffective(fv))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FieldName < out[j].FieldName })
	return out, nil
}

func (r *MemoryRepository) Get(_ context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fv, ok := r.values[key(orgID, ciID, fieldName)]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return r.withEffective(fv), nil
}

func (r *MemoryRepository) upsertLocked(orgID, ciID, fieldName string, mutate func(fv *FieldValue)) *FieldValue {
	k := key(orgID, ciID, fieldName)
	fv, ok := r.values[k]
	if !ok {
		fv = &FieldValue{
			ID:             r.nextID(),
			OrganizationID: orgID,
			CIID:           ciID,
			FieldName:      fieldName,
			CreatedAt:      time.Now().UTC(),
		}
		r.values[k] = fv
	}
	mutate(fv)
	fv.UpdatedAt = time.Now().UTC()
	return fv
}

// RecordDiscovered updates only the discovered side; the override columns
// stay untouched so protected overrides survive (spec §13).
func (r *MemoryRepository) RecordDiscovered(_ context.Context, orgID, ciID, fieldName string, value any, source string) (*FieldValue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	fv := r.upsertLocked(orgID, ciID, fieldName, func(fv *FieldValue) {
		fv.DiscoveredValue = value
		fv.DiscoveredSource = source
		fv.DiscoveredAt = &now
	})
	return r.withEffective(fv), nil
}

func (r *MemoryRepository) SetOverride(_ context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error) {
	if reason == "" {
		return nil, fmt.Errorf("override reason is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	fv := r.upsertLocked(orgID, ciID, fieldName, func(fv *FieldValue) {
		fv.OverrideValue = value
		fv.OverrideAuthor = author
		fv.OverrideReason = reason
		fv.OverrideAt = &now
		fv.Protected = protected
	})
	return r.withEffective(fv), nil
}

func (r *MemoryRepository) ClearOverride(_ context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fv, ok := r.values[key(orgID, ciID, fieldName)]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	fv.OverrideValue = nil
	fv.OverrideAuthor = ""
	fv.OverrideReason = ""
	fv.OverrideAt = nil
	fv.Protected = false
	fv.UpdatedAt = time.Now().UTC()
	return r.withEffective(fv), nil
}

func (r *MemoryRepository) Conflicts(_ context.Context, orgID string, page api.PaginationParams) ([]FieldValue, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []FieldValue
	for _, fv := range r.values {
		if fv.OrganizationID != orgID {
			continue
		}
		resolved := r.withEffective(fv)
		if resolved.Diverged {
			out = append(out, *resolved)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FieldName < out[j].FieldName })
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

func (r *MemoryRepository) Policy(_ context.Context, orgID string) (*SourcePolicy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.policies[orgID]; ok {
		out := *p
		return &out, nil
	}
	return &SourcePolicy{
		OrganizationID: orgID,
		Name:           "default",
		Priorities:     append([]string{}, DefaultPriorities...),
		IsDefault:      true,
	}, nil
}

func (r *MemoryRepository) UpsertPolicy(_ context.Context, policy *SourcePolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if policy.Name == "" {
		policy.Name = "default"
	}
	stored := *policy
	r.policies[policy.OrganizationID] = &stored
	return nil
}
