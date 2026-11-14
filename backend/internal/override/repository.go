package override

import (
	"context"
	"errors"
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
	RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*FieldValue, error)
	SetOverride(ctx context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error)
	ClearOverride(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error)
	// Conflicts returns field values where discovered and effective diverge.
	Conflicts(ctx context.Context, orgID string, page api.PaginationParams) ([]FieldValue, int, error)
	// Policy returns the organization's default source-priority policy.
	Policy(ctx context.Context, orgID string) (*SourcePolicy, error)
	// UpsertPolicy stores the former source-priority list; PUT
	// /reconciliation/source-policy no longer calls it (410, REC-04).
	UpsertPolicy(ctx context.Context, policy *SourcePolicy) error
	// SourceRankEntries returns the stored source ranks per attribute of
	// the organization (REC-04).
	SourceRankEntries(ctx context.Context, orgID string) ([]RankEntry, error)
	// ReplaceSourceRanks replaces them with entries, writes the audit
	// entry reconciliation.source_ranks_updated and returns the former and
	// the new entries as read in the writing transaction; the effective
	// ranks of this instance follow at once.
	ReplaceSourceRanks(ctx context.Context, orgID, actor string, entries []RankEntry) (before, after []RankEntry, err error)
}

// AuditSourceRanksUpdated is the audit action of a change of the source
// ranks (REC-04).
const AuditSourceRanksUpdated = "reconciliation.source_ranks_updated"

// IsProtected reports whether the field carries a protected manual override.
// Shared helper so other modules (discovery ingest) honor the protection
// without depending on the concrete repository (spec §13).
func IsProtected(ctx context.Context, repo Repository, orgID, ciID, fieldName string) (bool, error) {
	fv, err := repo.Get(ctx, orgID, ciID, fieldName)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		// Fail closed: an unreadable override counts as protected (REC-03).
		return true, err
	}
	// override_at marks an override even when its value is null: a manually
	// removed field stays removed (OVR-01).
	return fv.Protected && fv.OverrideAt != nil, nil
}

// AuthorNames resolves the display name of an override author (a user or
// a service account) within orgID. ok is false for an author that is
// unknown, deleted or of another organization (UI-15).
type AuthorNames func(ctx context.Context, orgID, authorID string) (name string, ok bool)

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu       sync.RWMutex
	values   map[string]*FieldValue // orgID/ciID/field -> value
	policies map[string]*SourcePolicy
	// ranks are the source ranks per organization (REC-04); the memory
	// profile knows no CI types, so only organization-wide entries apply
	// to its decisions.
	ranks map[string][]RankEntry
	// audits are the source-rank audit entries (memory profile).
	audits []RankAudit
	seq    int
	// authorNames resolves OverrideAuthorName in ListForCI as the
	// PostgreSQL repository does from app_user and service_account.
	authorNames AuthorNames
}

// NewMemoryRepository creates an empty in-memory override repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{values: map[string]*FieldValue{}, policies: map[string]*SourcePolicy{}, ranks: map[string][]RankEntry{}}
}

// WithAuthorNames sets the resolver of override author names (UI-15).
// Without one, ListForCI answers no names.
func (r *MemoryRepository) WithAuthorNames(names AuthorNames) *MemoryRepository {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authorNames = names
	return r
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

// ListForCI returns the field values of a CI with the display names of
// their override authors (UI-15).
func (r *MemoryRepository) ListForCI(ctx context.Context, orgID, ciID string) ([]FieldValue, error) {
	r.mu.RLock()
	var out []FieldValue
	for _, fv := range r.values {
		if fv.OrganizationID == orgID && fv.CIID == ciID {
			out = append(out, *r.withEffective(fv))
		}
	}
	names := r.authorNames
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].FieldName < out[j].FieldName })
	// The names are resolved outside the lock: the resolver reads other
	// repositories.
	if names != nil {
		for i := range out {
			if out[i].OverrideAuthor == "" {
				continue
			}
			if name, ok := names(ctx, orgID, out[i].OverrideAuthor); ok {
				out[i].OverrideAuthorName = name
			}
		}
	}
	return out, nil
}

func (r *MemoryRepository) Get(_ context.Context, orgID, ciID, fieldName string) (*FieldValue, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fv, ok := r.values[key(orgID, ciID, fieldName)]
	if !ok {
		return nil, ErrNotFound
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
func (r *MemoryRepository) RecordDiscovered(_ context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*FieldValue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rank := NewRankTable(r.ranks[orgID]).Rank("", fieldName, source)
	now := time.Now().UTC()
	if observedAt.IsZero() {
		observedAt = now
	}
	observedAt = observedAt.UTC()
	fv := r.upsertLocked(orgID, ciID, fieldName, func(fv *FieldValue) {
		fv.DiscoveredValue = value
		fv.DiscoveredSource = source
		fv.DiscoveredAt = &now
		fv.DiscoveredRank = rank
		fv.ObservedAt = &observedAt
	})
	return r.withEffective(fv), nil
}

func (r *MemoryRepository) SetOverride(_ context.Context, orgID, ciID, fieldName string, value any, author, reason string, protected bool) (*FieldValue, error) {
	if IsReservedName(fieldName) {
		return nil, ErrReservedName
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
	if IsReservedName(fieldName) {
		return nil, ErrReservedName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fv, ok := r.values[key(orgID, ciID, fieldName)]
	if !ok {
		return nil, ErrNotFound
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

// RankAudit is an audit entry of a change of the source ranks in the
// memory profile.
type RankAudit struct {
	OrganizationID string
	Actor          string
	Before, After  []RankEntry
}

// RankAudits returns the source-rank audit entries of the memory profile.
func (r *MemoryRepository) RankAudits() []RankAudit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]RankAudit(nil), r.audits...)
}

// SourceRankEntries returns the stored source ranks of the organization.
func (r *MemoryRepository) SourceRankEntries(_ context.Context, orgID string) ([]RankEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]RankEntry{}, r.ranks[orgID]...), nil
}

// ReplaceSourceRanks replaces the source ranks of the organization. The
// memory profile keeps no CI type catalog and accepts every type id.
func (r *MemoryRepository) ReplaceSourceRanks(_ context.Context, orgID, actor string, entries []RankEntry) (before, after []RankEntry, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	before = append([]RankEntry{}, r.ranks[orgID]...)
	now := time.Now().UTC()
	after = make([]RankEntry, len(entries))
	for i := range entries {
		after[i] = entries[i]
		after[i].UpdatedAt = &now
		if api.IsUUID(actor) {
			after[i].UpdatedBy = actor
		}
	}
	sortEntries(after)
	r.ranks[orgID] = after
	r.audits = append(r.audits, RankAudit{OrganizationID: orgID, Actor: actor, Before: before, After: append([]RankEntry{}, after...)})
	return before, append([]RankEntry{}, after...), nil
}

// rankTable returns the organization's rank table (no cache needed).
func (r *MemoryRepository) rankTable(_ context.Context, orgID string) (*RankTable, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return NewRankTable(r.ranks[orgID]), nil
}
