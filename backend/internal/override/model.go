// Package override implements per-field provenance and controlled manual
// overrides (spec §13). Every discovered field retains its source metadata;
// manual overrides are protected from silent discovery overwrites, and the
// effective value resolves through the tenant's source-priority policy.
package override

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

func jsonMarshal(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// FieldValue carries the provenance of one CI field (spec §13).
type FieldValue struct {
	ID               string     `json:"id"`
	OrganizationID   string     `json:"organization_id"`
	CIID             string     `json:"ci_id"`
	FieldName        string     `json:"field_name"`
	DiscoveredValue  any        `json:"discovered_value,omitempty"`
	DiscoveredSource string     `json:"discovered_source,omitempty"`
	DiscoveredAt     *time.Time `json:"discovered_at,omitempty"`
	// DiscoveredRank is the REC-03 rank of DiscoveredSource when the value
	// was recorded; ObservedAt is the source time of the observation
	// (GLO-12, REC-05), DiscoveredAt the time it was recorded.
	DiscoveredRank int        `json:"discovered_rank,omitempty"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
	OverrideValue  any        `json:"override_value,omitempty"`
	OverrideAuthor string     `json:"override_author,omitempty"`
	// OverrideAuthorName is the display name of OverrideAuthor (UI-15,
	// REC-10): app_user.display_name of a user, service_account.name of a
	// service account. ListForCI resolves it on read under the reader's
	// tenant scope; it is never stored (SEC-05) and empty when the author is
	// unknown, deleted or not visible. Only the provenance of GET
	// /cis/{id}/provenance answers it (CIFieldProvenance); the field value
	// DTO of /cis/{id}/fields keeps its contract.
	OverrideAuthorName string     `json:"-"`
	OverrideReason     string     `json:"override_reason,omitempty"`
	OverrideAt         *time.Time `json:"override_at,omitempty"`
	Protected          bool       `json:"protected"`
	// EffectiveValue resolves through the source-priority policy: a protected
	// override always wins; otherwise the highest-trust discovered value.
	EffectiveValue any `json:"effective_value,omitempty"`
	// Diverged is true when discovered and effective values differ — the UI
	// surfaces this as a conflict badge.
	Diverged  bool      `json:"diverged"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// CIVersion is the version of the CI after SetOverride or ClearOverride
	// (API-07); the handler returns it as ETag. Zero when the repository
	// keeps no CI version (memory profile).
	CIVersion int64 `json:"-"`
	// InstanceAttribute reports that the field is an instance attribute of
	// the CI (ci_instance_field_definition, MET-14): Get sets it, also for a
	// field without provenance row, so the central write decision refuses
	// it (ReasonInstanceAttribute, WP-714). It is not part of the API.
	InstanceAttribute bool `json:"-"`
}

// SetOverrideRequest is the payload for setting a manual override.
type SetOverrideRequest struct {
	Value     any    `json:"value"`
	Reason    string `json:"reason"`
	Protected *bool  `json:"protected,omitempty"`
}

// SourcePolicy is the per-organization source-priority policy.
type SourcePolicy struct {
	ID             string   `json:"id"`
	OrganizationID string   `json:"organization_id"`
	Name           string   `json:"name"`
	Priorities     []string `json:"priorities"`
	IsDefault      bool     `json:"is_default"`
}

// DefaultPriorities is the built-in source-priority ordering (highest
// first): the sources of the REC-03 rank table SourceRanks by descending
// rank, sources of the same rank by name (F-230). GET
// /reconciliation/source-policy shows it while the organization keeps no
// policy of its own.
var DefaultPriorities = prioritiesByRank(SourceRanks)

// prioritiesByRank orders the sources of ranks by descending rank, sources
// of the same rank by name.
func prioritiesByRank(ranks map[string]int) []string {
	out := make([]string, 0, len(ranks))
	for source := range ranks {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool {
		if ranks[out[i]] != ranks[out[j]] {
			return ranks[out[i]] > ranks[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// ResolveEffective computes the effective value of a field: a manual
// override, protected or not, before the observed value. Manual ranks 100
// in REC-03 and stays 100 under a per-attribute policy (REC-04), so the
// policy cannot rank a source above it; the central write decision and the
// CI view use the same rule (OVR-01, CI-10).
func ResolveEffective(fv *FieldValue, _ []string) any {
	if fv.OverrideAt != nil || fv.OverrideValue != nil {
		return fv.OverrideValue
	}
	return fv.DiscoveredValue
}

// IsDiverged reports whether discovered and effective values differ.
func IsDiverged(fv *FieldValue, priorities []string) bool {
	if fv.DiscoveredValue == nil {
		return false
	}
	effective := ResolveEffective(fv, priorities)
	return !valuesEqual(fv.DiscoveredValue, effective)
}

func valuesEqual(a, b any) bool {
	return fmtSprint(a) == fmtSprint(b)
}

func fmtSprint(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		raw, err := jsonMarshal(v)
		if err != nil {
			return ""
		}
		return raw
	}
}

// ConfigurableSources are the automation sources whose rank an
// organization may set per attribute (REC-04, CHECK of
// source_rank_policy). manual, override and manual_override stay 100 and
// are not configurable: no automation source can rank at or above a
// manual override.
var ConfigurableSources = []string{"import", "workflow", "redfish", "ipmi", "agent", "snmp", "api", "integration", "wmi", "ssh", "nas", "sweep"}

// IsConfigurableSource reports whether source is one of ConfigurableSources.
func IsConfigurableSource(source string) bool {
	for _, s := range ConfigurableSources {
		if s == source {
			return true
		}
	}
	return false
}

// MaxRankEntries is the number of entries PUT
// /reconciliation/source-ranks accepts at most.
const MaxRankEntries = 1000

// Origins of an effective rank.
const (
	// OriginType: the entry of the CI type of the CI.
	OriginType = "type"
	// OriginOrg: the organization-wide entry (ci_type_id NULL).
	OriginOrg = "org"
	// OriginDefault: the REC-03 rank table SourceRanks.
	OriginDefault = "default"
)

// RankEntry is one stored rank of a source for an attribute, for a CI
// type or organization-wide (CITypeID empty).
type RankEntry struct {
	CITypeID     string     `json:"ci_type_id,omitempty"`
	AttributeKey string     `json:"attribute_key"`
	Source       string     `json:"source"`
	Rank         int        `json:"rank"`
	UpdatedBy    string     `json:"updated_by,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

// key identifies the entry: CI type, attribute and source.
func (e *RankEntry) key() rankKey {
	return rankKey{ciType: e.CITypeID, attribute: e.AttributeKey, source: e.Source}
}

type rankKey struct{ ciType, attribute, source string }

// RankTable resolves the effective rank of a source for a field of a CI
// (REC-04): the entry of the CI's type, else the organization-wide entry,
// else SourceRanks. A nil table is SourceRanks alone.
type RankTable struct {
	ranks map[rankKey]int
}

// NewRankTable builds the table of the stored entries.
func NewRankTable(entries []RankEntry) *RankTable {
	t := &RankTable{ranks: make(map[rankKey]int, len(entries))}
	for i := range entries {
		t.ranks[entries[i].key()] = entries[i].Rank
	}
	return t
}

// Effective returns the rank of source for attribute of a CI of type
// ciTypeID and where it comes from. Sources that are not configurable
// (manual: 100, unknown: 0) always take SourceRanks.
func (t *RankTable) Effective(ciTypeID, attribute, source string) (rank int, origin string) {
	if t != nil && IsConfigurableSource(source) {
		if ciTypeID != "" {
			if r, ok := t.ranks[rankKey{ciType: ciTypeID, attribute: attribute, source: source}]; ok {
				return r, OriginType
			}
		}
		if r, ok := t.ranks[rankKey{attribute: attribute, source: source}]; ok {
			return r, OriginOrg
		}
	}
	return SourceRank(source), OriginDefault
}

// empty reports whether the table holds no entry: every rank is the one
// of SourceRanks.
func (t *RankTable) empty() bool { return t == nil || len(t.ranks) == 0 }

// Rank is Effective without the origin.
func (t *RankTable) Rank(ciTypeID, attribute, source string) int {
	r, _ := t.Effective(ciTypeID, attribute, source)
	return r
}

// EffectiveRank is the rank of one automation source with its origin.
type EffectiveRank struct {
	Source string `json:"source"`
	Rank   int    `json:"rank"`
	Origin string `json:"origin"`
}

// EffectiveRanks is the effective table of all automation sources for one
// attribute (and CI type, if asked).
type EffectiveRanks struct {
	CITypeID     string          `json:"ci_type_id,omitempty"`
	AttributeKey string          `json:"attribute_key"`
	Ranks        []EffectiveRank `json:"ranks"`
}

// EffectiveFor returns the effective ranks of every configurable source for
// attribute of a CI of type ciTypeID (empty: organization-wide), highest
// first, the same rank by name.
func (t *RankTable) EffectiveFor(ciTypeID, attribute string) EffectiveRanks {
	out := EffectiveRanks{CITypeID: ciTypeID, AttributeKey: attribute, Ranks: make([]EffectiveRank, 0, len(ConfigurableSources))}
	for _, source := range ConfigurableSources {
		rank, origin := t.Effective(ciTypeID, attribute, source)
		out.Ranks = append(out.Ranks, EffectiveRank{Source: source, Rank: rank, Origin: origin})
	}
	sort.SliceStable(out.Ranks, func(i, j int) bool {
		if out.Ranks[i].Rank != out.Ranks[j].Rank {
			return out.Ranks[i].Rank > out.Ranks[j].Rank
		}
		return out.Ranks[i].Source < out.Ranks[j].Source
	})
	return out
}

// SourceRanksResponse answers GET and PUT /reconciliation/source-ranks:
// the stored entries and, per requested attribute, the effective table.
type SourceRanksResponse struct {
	Entries   []RankEntry      `json:"entries"`
	Effective []EffectiveRanks `json:"effective"`
}

// ReplaceSourceRanksRequest is the body of PUT
// /reconciliation/source-ranks: the complete set of entries of the
// organization.
type ReplaceSourceRanksRequest struct {
	Entries []RankEntry `json:"entries"`
}

// rankTabler is implemented by the repositories that keep source ranks
// per attribute (PGRepository, MemoryRepository): the write decision
// resolves both ranks through the table (REC-04). Without it the decision
// uses SourceRanks.
type rankTabler interface {
	// rankTable returns the organization's rank table (cached).
	rankTable(ctx context.Context, orgID string) (*RankTable, error)
}

// typedDecisionReader reads the provenance of the field (nil without a
// row; ErrNotFound never) and the CI type of the CI in one statement
// (PGRepository). A reader without it is read through Get, and only the
// organization-wide entries apply (the memory profile keeps no CI types).
type typedDecisionReader interface {
	decisionState(ctx context.Context, orgID, ciID, fieldName string) (fv *FieldValue, ciTypeID string, err error)
}

// rankCacheTTL is how long a rank table stays cached on an instance;
// another instance takes a change over after at most this time.
const rankCacheTTL = 30 * time.Second

// rankCache holds the rank tables per organization for the instance; a
// write on this instance invalidates its organization at once (REC-04).
// generations counts the invalidations per organization: a table read
// before an invalidation is not cached after it (putIfGeneration).
type rankCache struct {
	mu          sync.Mutex
	entries     map[string]rankCacheEntry
	generations map[string]uint64
	now         func() time.Time
}

type rankCacheEntry struct {
	table   *RankTable
	expires time.Time
}

// ranksOfInstance is the cache of the process: every repository of an
// instance shares it, so a PUT invalidates the decisions of discovery,
// import and workflow alike.
var ranksOfInstance = newRankCache(time.Now)

func newRankCache(now func() time.Time) *rankCache {
	return &rankCache{entries: map[string]rankCacheEntry{}, generations: map[string]uint64{}, now: now}
}

// get returns the cached table of the organization, or on a miss the
// generation to pass to putIfGeneration after reading the table.
func (c *rankCache) get(orgID string) (*RankTable, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[orgID]
	if !ok || !c.now().Before(e.expires) {
		return nil, c.generations[orgID], false
	}
	return e.table, 0, true
}

// putIfGeneration caches table only when no invalidation happened since
// get returned gen: otherwise the table may predate the latest write.
func (c *rankCache) putIfGeneration(orgID string, gen uint64, table *RankTable) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generations[orgID] != gen {
		return false
	}
	c.entries[orgID] = rankCacheEntry{table: table, expires: c.now().Add(rankCacheTTL)}
	return true
}

func (c *rankCache) invalidate(orgID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, orgID)
	c.generations[orgID]++
}

// changedAttributes returns the attribute keys whose entries differ
// between before and after, sorted.
func changedAttributes(before, after []RankEntry) []string {
	old := map[rankKey]int{}
	for i := range before {
		old[before[i].key()] = before[i].Rank
	}
	changed := map[string]bool{}
	for i := range after {
		k := after[i].key()
		if r, ok := old[k]; !ok || r != after[i].Rank {
			changed[k.attribute] = true
		}
		delete(old, k)
	}
	for k := range old {
		changed[k.attribute] = true
	}
	out := make([]string, 0, len(changed))
	for k := range changed {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortEntries orders entries by attribute, type (organization-wide first)
// and source.
func sortEntries(entries []RankEntry) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.AttributeKey != b.AttributeKey {
			return a.AttributeKey < b.AttributeKey
		}
		if a.CITypeID != b.CITypeID {
			return a.CITypeID < b.CITypeID
		}
		return a.Source < b.Source
	})
}
