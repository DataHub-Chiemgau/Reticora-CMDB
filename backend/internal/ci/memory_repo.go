package ci

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// MemoryRepository is an in-memory CI repository for development and testing.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Item
	seq   int
	// deleted keeps what a restore brings back (CI-05).
	deleted map[string]deletedState
	// types holds the CI types known for a type change (CI-11).
	types map[string]memoryType
	// tags is the tag catalog by id (CI-08, WP-753); seeded orgs have the
	// system tag archived.
	tags     map[string]*Tag
	tagSeq   int
	tagSeeds map[string]bool
}

type memoryType struct {
	key      string
	required []string
}

type deletedState struct{ status, assetID string }

// NewMemoryRepository creates a new in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		items:    make(map[string]*Item),
		deleted:  make(map[string]deletedState),
		types:    make(map[string]memoryType),
		tags:     make(map[string]*Tag),
		tagSeeds: make(map[string]bool),
	}
}

func (r *MemoryRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Item, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Item
	for _, item := range r.items {
		if item.OrganizationID != orgID || item.DeletedAt != nil {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.TypeID != "" && item.CITypeID != filter.TypeID {
			continue
		}
		if filter.ClientID != "" && item.ClientID != filter.ClientID {
			continue
		}
		if filter.Search != "" && !containsIgnoreCase(item.Name, filter.Search) &&
			!containsIgnoreCase(item.SerialNumber, filter.Search) &&
			!containsIgnoreCase(item.ManagementIP, filter.Search) {
			continue
		}
		if !matchesFilters(item, filter.Filters) {
			continue
		}
		result = append(result, *item)
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(&result[i], column), sortValue(&result[j], column)
		if vi == vj {
			if direction == "asc" {
				return result[i].ID < result[j].ID
			}
			return result[i].ID > result[j].ID
		}
		if direction == "asc" {
			return vi < vj
		}
		return vi > vj
	})

	if page.Cursor != nil {
		if !page.Cursor.Matches(column, direction) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		remaining := make([]Item, 0, len(result))
		for i := range result {
			item := &result[i]
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, *item)
			}
		}
		if len(remaining) > page.Limit {
			remaining = remaining[:page.Limit]
		}
		return remaining, total, nil
	}

	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}

	return result[start:end], total, nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return item, nil
}

// GetByIDs implements BatchReader.
func (r *MemoryRepository) GetByIDs(_ context.Context, orgID string, ids []string) ([]Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := []Item{}
	for _, id := range ids {
		if item, ok := r.items[id]; ok && item.OrganizationID == orgID && item.DeletedAt == nil {
			items = append(items, *item)
		}
	}
	return items, nil
}

// ListNodes implements NodeReader.
func (r *MemoryRepository) ListNodes(_ context.Context, orgID string, ids []string) ([]Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := []Node{}
	for _, id := range ids {
		if item, ok := r.items[id]; ok && item.OrganizationID == orgID && item.DeletedAt == nil {
			nodes = append(nodes, NodeOf(item))
		}
	}
	return nodes, nil
}

func (r *MemoryRepository) Create(ctx context.Context, item *Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := takeLegacyTags(item); err != nil {
		return err
	}
	item.Tags = []string{}
	if item.LegacyTags != nil {
		var names []string
		item.Tags, names = tagKeys(*item.LegacyTags)
		r.ensureTags(item.OrganizationID, item.Tags, names)
		item.LegacyTags = nil
	}
	r.seq++
	item.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	item.CreatedAt = now
	item.UpdatedAt = now
	item.Version = 1
	if item.Health == "" {
		item.Health = "unknown"
	}
	// The default child mode like migration 000244 (WP-273).
	applyChildMode(item)
	r.items[item.ID] = item
	return nil
}

func (r *MemoryRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) { //nolint:gocritic // signature of Repository; the update works on its own copy
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if err := reviveSighted(item, &req); err != nil {
		return nil, err
	}
	// The child contract like the PostgreSQL repository (WP-273).
	if err := checkChildContract(item, &req); err != nil {
		return nil, err
	}
	// The tag assignment like the PostgreSQL repository (WP-281).
	if req.LegacyTags == nil {
		rest, legacy, err := ExtractLegacyTags(req.Attributes)
		if err != nil {
			return nil, err
		}
		if legacy != nil {
			req.Attributes, req.LegacyTags = rest, legacy
		}
	}
	if req.LegacyTags != nil {
		var names []string
		item.Tags, names = tagKeys(*req.LegacyTags)
		r.ensureTags(orgID, item.Tags, names)
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}

	if req.Name != nil {
		item.Name = *req.Name
	}
	if req.Status != nil {
		item.Status = *req.Status
	}
	if req.Manufacturer != nil {
		item.Manufacturer = *req.Manufacturer
	}
	if req.Model != nil {
		item.Model = *req.Model
	}
	if req.SerialNumber != nil {
		item.SerialNumber = *req.SerialNumber
	}
	if req.ManagementIP != nil {
		item.ManagementIP = *req.ManagementIP
	}
	if req.FirmwareVersion != nil {
		item.FirmwareVersion = *req.FirmwareVersion
	}
	if req.AssetID != nil {
		item.AssetID = *req.AssetID
	}
	if req.ParentCIID != nil {
		item.ParentCIID = *req.ParentCIID
	}
	if req.ChildMode != nil {
		item.ChildMode = *req.ChildMode
	}
	applyChildMode(item)
	// The owner (WP-280, WP-752): "" clears it. Without assets the
	// mirroring of the asset's owner (DB-05) does not apply.
	if req.OwnerUserID != nil {
		item.OwnerUserID = *req.OwnerUserID
	}
	if req.OwnerTeamID != nil {
		item.OwnerTeamID = *req.OwnerTeamID
	}
	if req.LocationID != nil {
		// Without the location tree, site and room cannot be derived.
		item.LocationID, item.SiteID, item.RoomID = *req.LocationID, "", ""
	}
	if req.Attributes != nil {
		item.Attributes = MergePatch(item.Attributes, req.Attributes)
	}
	if req.DiscoverySource != nil {
		item.DiscoverySource = *req.DiscoverySource
	}
	if req.LastSeenAt != nil {
		t, err := time.Parse(time.RFC3339, *req.LastSeenAt)
		// A sighting keeps the later time, like the PostgreSQL repository.
		if err == nil && (req.Manual != nil || item.LastSeenAt == nil || t.After(*item.LastSeenAt)) {
			item.LastSeenAt = &t
		}
	}
	if req.Manual != nil || req.Authoritative {
		item.Version++
	}
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return item, nil
}

func (r *MemoryRepository) Delete(ctx context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return ErrNotFound
	}
	// A parent with configuration children is not deleted (E-123 (b)).
	for _, child := range r.items {
		if child.OrganizationID == orgID && child.ParentCIID == id && child.ChildMode == ChildModeConfiguration && child.DeletedAt == nil {
			return ErrConfigurationChildren
		}
	}
	// A deleted CI is kept for the retention like in PostgreSQL (CI-05):
	// decommissioned, without its asset link.
	now := time.Now().UTC()
	r.deleted[id] = deletedState{status: item.Status, assetID: item.AssetID}
	item.DeletedAt = &now
	item.Status = StatusDecommissioned
	item.AssetID = ""
	return nil
}

// RestoreDeleted implements Restorer.
func (r *MemoryRepository) RestoreDeleted(_ context.Context, orgID, id string, retention time.Duration) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if item.DeletedAt == nil {
		return nil, ErrNotDeleted
	}
	if time.Since(*item.DeletedAt) > retention {
		return nil, ErrRetentionExpired
	}
	if parent, ok := r.items[item.ParentCIID]; ok && item.ChildMode == ChildModeConfiguration && parent.DeletedAt != nil {
		return nil, ErrConfigurationParentDeleted
	}
	item.DeletedAt = nil
	if prev, ok := r.deleted[id]; ok {
		item.Status, item.AssetID = prev.status, prev.assetID
		if item.Status == "" || item.Status == StatusDecommissioned {
			item.Status = StatusActive
		}
		delete(r.deleted, id)
	}
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	item.Version++
	return item, nil
}

// ListChanges returns no persisted history for the in-memory repository.
func (r *MemoryRepository) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	return []Change{}, 0, nil
}

func containsIgnoreCase(s, sub string) bool {
	if s == "" || sub == "" {
		return false
	}
	ls := []rune(s)
	lsub := []rune(sub)
	if len(lsub) > len(ls) {
		return false
	}
	for i := 0; i <= len(ls)-len(lsub); i++ {
		match := true
		for j := range lsub {
			if toLower(ls[i+j]) != toLower(lsub[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// matchesFilters applies the filter[field][op] conditions in memory. The
// memory repository has no attribute definitions: an attribute compares as
// a number when the filter value is one, otherwise as text.
func matchesFilters(item *Item, filters []api.Filter) bool {
	for _, f := range filters {
		if f.Kind == api.KindSet {
			if !f.MatchSet(item.Tags) {
				return false
			}
			continue
		}
		if f.Kind == "" {
			f.Kind = api.KindText
			if _, err := strconv.ParseFloat(f.Values[0], 64); err == nil {
				f.Kind = api.KindNumber
			}
		}
		if !f.Match(fieldValue(item, f.Field)) {
			return false
		}
	}
	return true
}

// SetType registers a CI type for type changes (CI-11).
func (r *MemoryRepository) SetType(id, key string, required ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types[id] = memoryType{key: key, required: required}
}

// ChangeType implements TypeChanger like the PostgreSQL repository.
func (r *MemoryRepository) ChangeType(_ context.Context, orgID, ciID, typeID, mode string) (*TypeChangeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[ciID]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return nil, ErrNotFound
	}
	to, ok := r.types[typeID]
	if !ok {
		return nil, ErrUnknownType
	}
	if mode == TypeChangeDiscovery && !DiscoveryMayReclassify(r.types[item.CITypeID].key, to.key) {
		return nil, ErrReclassificationRefused
	}
	result := &TypeChangeResult{Findings: MissingRequired(to.required, item.Attributes)}
	if item.CITypeID != typeID {
		item.CITypeID = typeID
		item.Version++
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		result.Changed = true
	}
	cp := *item
	result.CI = &cp
	return result, nil
}

// ─── Tag catalog and tags of a CI (CI-08, WP-753) ─────────────────────────────

// seedTags gives an organization its system tag archived once, like the
// trigger organization_default_tags (migration 000218). The caller holds
// the write lock.
func (r *MemoryRepository) seedTags(orgID string) {
	if r.tagSeeds[orgID] {
		return
	}
	r.tagSeeds[orgID] = true
	r.addTag(&Tag{OrganizationID: orgID, Key: "archived", DisplayName: map[string]string{"de": "Archiviert", "en": "Archived"},
		IsSystem: true})
}

// addTag stores a new catalog tag with an id and times.
func (r *MemoryRepository) addTag(tag *Tag) {
	r.tagSeq++
	tag.ID = fmt.Sprintf("7a900000-0000-4000-8000-%012d", r.tagSeq)
	tag.CreatedAt = time.Now().UTC()
	tag.UpdatedAt = tag.CreatedAt
	cp := *tag
	r.tags[tag.ID] = &cp
}

// tagByKey returns the catalog tag of key in the organization.
func (r *MemoryRepository) tagByKey(orgID, key string) *Tag {
	for _, tag := range r.tags {
		if tag.OrganizationID == orgID && tag.Key == key {
			return tag
		}
	}
	return nil
}

// ensureTags creates the catalog tags of a write through attributes.tags
// that are missing (names: the texts of the keys), like writeCITags. The
// caller holds the write lock.
func (r *MemoryRepository) ensureTags(orgID string, keys, names []string) {
	r.seedTags(orgID)
	for i, key := range keys {
		if r.tagByKey(orgID, key) == nil {
			r.addTag(&Tag{OrganizationID: orgID, Key: key, DisplayName: map[string]string{"de": names[i], "en": names[i]}})
		}
	}
}

// tagOf returns the tag id of the organization.
func (r *MemoryRepository) tagOf(orgID, id string) (*Tag, error) {
	r.seedTags(orgID)
	tag, ok := r.tags[id]
	if !ok || tag.OrganizationID != orgID {
		return nil, ErrTagNotFound
	}
	return tag, nil
}

// ListTags implements TagStore like the PostgreSQL repository.
func (r *MemoryRepository) ListTags(_ context.Context, orgID string, page api.PaginationParams) ([]Tag, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seedTags(orgID)
	all := []Tag{}
	for _, tag := range r.tags {
		if tag.OrganizationID == orgID {
			all = append(all, *tag)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	total := len(all)
	rows := all
	if page.Cursor != nil {
		if _, _, err := tagPosition(page.Cursor); err != nil {
			return nil, 0, err
		}
		rows = []Tag{}
		for _, tag := range all {
			if api.KeysetCompare(tag.Key, tag.ID, *page.Cursor, tagDir) {
				rows = append(rows, tag)
			}
		}
	} else {
		rows = rows[min(page.Offset, len(rows)):]
	}
	return rows[:min(len(rows), page.Limit+1)], total, nil
}

// GetTag implements TagStore.
func (r *MemoryRepository) GetTag(_ context.Context, orgID, id string) (*Tag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tag, err := r.tagOf(orgID, id)
	if err != nil {
		return nil, err
	}
	cp := *tag
	return &cp, nil
}

// CreateTag implements TagStore; a taken key is a conflict on key.
func (r *MemoryRepository) CreateTag(_ context.Context, tag *Tag) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seedTags(tag.OrganizationID)
	if r.tagByKey(tag.OrganizationID, tag.Key) != nil {
		return &api.ConflictError{Field: "key", Message: "a tag with this key exists"}
	}
	r.addTag(tag)
	return nil
}

// UpdateTag implements TagStore.
func (r *MemoryRepository) UpdateTag(_ context.Context, orgID, id string, req *UpdateTagRequest) (*Tag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tag, err := r.tagOf(orgID, id)
	if err != nil {
		return nil, err
	}
	req.apply(tag)
	tag.UpdatedAt = time.Now().UTC()
	cp := *tag
	return &cp, nil
}

// tagged reports whether a CI of the organization (a deleted one too)
// carries key.
func (r *MemoryRepository) tagged(orgID, key string) bool {
	for _, item := range r.items {
		if item.OrganizationID == orgID && slices.Contains(item.Tags, key) {
			return true
		}
	}
	return false
}

// DeleteTag implements TagStore: no system tag, no assigned tag (409).
func (r *MemoryRepository) DeleteTag(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tag, err := r.tagOf(orgID, id)
	if err != nil {
		return err
	}
	if tag.IsSystem {
		return ErrSystemTag
	}
	if r.tagged(orgID, tag.Key) {
		return ErrTagInUse
	}
	delete(r.tags, id)
	return nil
}

// MergeTag implements TagStore like the PostgreSQL repository: live CIs
// with the source tag get the target tag (version + 1), deleted CIs keep
// it under the target, the source tag goes.
func (r *MemoryRepository) MergeTag(_ context.Context, orgID, sourceID, intoID string) (*TagMergeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	source, err := r.tagOf(orgID, sourceID)
	if err != nil {
		return nil, err
	}
	into, err := r.tagOf(orgID, intoID)
	if err != nil {
		return nil, &ValidationError{Violations: []Violation{{Field: "into_id", Detail: "unknown tag"}}}
	}
	if source.IsSystem {
		return nil, ErrSystemTag
	}
	result := &TagMergeResult{}
	ids := make([]string, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := r.items[id]
		if item.OrganizationID != orgID || !slices.Contains(item.Tags, source.Key) {
			continue
		}
		keys := slices.DeleteFunc(slices.Clone(item.Tags), func(k string) bool { return k == source.Key })
		if !slices.Contains(keys, into.Key) {
			keys = append(keys, into.Key)
		}
		slices.Sort(keys)
		item.Tags = keys
		if item.DeletedAt != nil {
			continue
		}
		item.Version++
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		cp := *item
		result.CIs = append(result.CIs, &cp)
	}
	result.Moved = len(result.CIs)
	delete(r.tags, sourceID)
	cp := *into
	result.Tag = &cp
	return result, nil
}

// ReplaceCITags implements TagStore: catalog keys only (422), If-Match on
// the version (412 for another one); an unchanged list keeps the version.
func (r *MemoryRepository) ReplaceCITags(_ context.Context, orgID, ciID string, keys []string, ifMatch *int64) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seedTags(orgID)
	sorted, violations := catalogKeys(keys)
	known := map[string]bool{}
	for _, key := range sorted {
		known[key] = r.tagByKey(orgID, key) != nil
	}
	violations = append(violations, unknownTags(keys, known)...)
	if len(violations) > 0 {
		return nil, &ValidationError{Violations: violations}
	}
	item, ok := r.items[ciID]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if ifMatch != nil && *ifMatch != api.IfMatchAny && *ifMatch != item.Version {
		return nil, ErrPreconditionFailed
	}
	if !slices.Equal(item.Tags, sorted) {
		item.Tags = sorted
		item.Version++
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	cp := *item
	return &cp, nil
}
