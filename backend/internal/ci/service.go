package ci

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// BatchReader reads several CIs in one query, so graph walks resolve their
// nodes without one query per node (NFR-02).
type BatchReader interface {
	GetByIDs(ctx context.Context, orgID string, ids []string) ([]Item, error)
}

// BatchCreator creates several CIs of one organization with a number of
// statements that does not grow with the batch (NFR-03, WP-333): the CI
// rows, their history, their audit entries chained in the order of items and
// their ci.created events. Search and AI index decorators add their
// documents with one statement each.
type BatchCreator interface {
	// CreateMany creates items in their order and sets on each what Create
	// sets (ID, SiteID, RoomID, LocationID, Health, Version, SchemaVersion,
	// CreatedAt, UpdatedAt). An item a unique index refuses (serial number,
	// hardware UUID, primary MAC, unique attribute; WP-207) is not created:
	// its position is in Refused, its ID stays empty and nothing of it is
	// written. Any other error fails the whole call.
	CreateMany(ctx context.Context, orgID string, items []*Item) (CreateManyResult, error)
}

// CreateManyResult lists the positions of the items a unique index refused
// and the values of their computed attributes (WP-717).
type CreateManyResult struct {
	Refused []int
	// Computed holds per item position the computed attributes the item
	// got (key → value, nil for an empty value; nil when its type computes
	// nothing), ComputedAt the time of the computation. The values are in
	// the items' attributes; the caller records their provenance (source
	// SourceComputed, rank RankComputed, observed at ComputedAt) with the
	// provenance of the batch (NFR-03), the discovery in its
	// RecordDiscoveredBatch call.
	Computed   []map[string]any
	ComputedAt time.Time
}

// BatchIndexer is the batch form of Indexer (NFR-03, WP-333): the documents
// of the CIs of one batch with one statement.
type BatchIndexer interface {
	IndexDocuments(ctx context.Context, docs []Document) error
}

// Node is the graph projection of a CI that topology and impact read
// (NFR-02): no attributes, no asset facts.
type Node struct {
	ID           string
	Name         string
	CITypeID     string
	Status       string
	ClientID     string
	SiteID       string
	ManagementIP string
}

// NodeOf projects an item onto its graph node.
func NodeOf(item *Item) Node {
	return Node{ID: item.ID, Name: item.Name, CITypeID: item.CITypeID, Status: item.Status,
		ClientID: item.ClientID, SiteID: item.SiteID, ManagementIP: item.ManagementIP}
}

// NodeReader reads the graph projection of several CIs in one statement
// (NFR-02): the visible, not deleted CIs among ids, in no particular order,
// with the effective name, status and management IP (OVR-01), the values
// GetByIDs returns.
type NodeReader interface {
	ListNodes(ctx context.Context, orgID string, ids []string) ([]Node, error)
}

// VisibleIDs adapts a NodeReader to the visibility check of a graph walk
// (IMP-07): which of ids are visible, not deleted CIs.
func VisibleIDs(reader NodeReader) func(ctx context.Context, orgID string, ids []string) (map[string]bool, error) {
	return func(ctx context.Context, orgID string, ids []string) (map[string]bool, error) {
		nodes, err := reader.ListNodes(ctx, orgID, ids)
		if err != nil {
			return nil, err
		}
		visible := make(map[string]bool, len(nodes))
		for i := range nodes {
			visible[nodes[i].ID] = true
		}
		return visible, nil
	}
}

// Repository defines persistence operations for CIs.
type Repository interface {
	List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Item, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Item, error)
	Create(ctx context.Context, item *Item) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error)
	Delete(ctx context.Context, orgID, id string) error
}

// RestoreRetention is the default of how long a deleted CI can be restored
// (CI-05); the server sets the configured soft-delete retention
// (WithRestoreRetention), after which the hard-delete job removes the CI.
const RestoreRetention = 90 * 24 * time.Hour

// ErrRetentionExpired: the CI was deleted longer ago than the retention and
// can no longer be restored (410).
var ErrRetentionExpired = errors.New("the CI was deleted longer ago than the restore retention")

// ErrNotDeleted: the CI to restore is not deleted (409).
var ErrNotDeleted = errors.New("the CI is not deleted")

// Restorer restores soft-deleted CIs within the retention (CI-05).
type Restorer interface {
	RestoreDeleted(ctx context.Context, orgID, id string, retention time.Duration) (*Item, error)
}

// LimitFeatureKey is the entitlement feature whose limit caps the number of CIs
// an organization may store.
const LimitFeatureKey = "max_cis"

// ChangeReader defines persistence operations for CI change history.
type ChangeReader interface {
	ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error)
}

// ActiveCounter counts the active (not deleted) CIs of the organization for
// max_cis, whatever the client and site scope of the caller (ENT-03,
// F-403): the limit caps the organization, not what one principal sees. The
// PG repository implements it with ci_active_count() (migration 000186).
type ActiveCounter interface {
	CountActive(ctx context.Context, orgID string) (int64, error)
}

// CountActive returns the number of active CIs max_cis is checked against:
// through the repository's ActiveCounter, or the total of List for a
// repository without one (it has no scope to narrow the count).
func CountActive(ctx context.Context, repo Repository, orgID string) (int64, error) {
	if counter, ok := repo.(ActiveCounter); ok {
		return counter.CountActive(ctx, orgID)
	}
	_, total, err := repo.List(ctx, orgID, &FilterParams{}, api.PaginationParams{Limit: 1})
	return int64(total), err
}

// LimitGuard enforces licensed resource limits before a new CI is created.
// It is implemented by the entitlement service.
type LimitGuard interface {
	AllowCreate(ctx context.Context, orgID, featureKey string, current int64) error
}

// Indexer keeps the tenant search index in sync with CI mutations. It is
// implemented by the search backends (Postgres FTS, OpenSearch, hybrid).
// Indexing is best-effort: failures are logged and never fail the underlying
// CI mutation, because the index can always be rebuilt with
// POST /api/v1/search/reindex.
type Indexer interface {
	IndexDocument(ctx context.Context, doc *Document) error
	DeleteDocument(ctx context.Context, orgID, entityType, entityID string) error
}

// Document is the search-index representation of a CI. It mirrors
// search.Document; the ci package declares its own copy so it does not
// depend on the search package (search already depends on shared api types,
// and keeping the dependency direction ci-free avoids an import cycle).
type Document struct {
	OrganizationID string
	EntityType     string
	EntityID       string
	Title          string
	Summary        string
	URL            string
	Metadata       map[string]string
	ClientID       string
	SiteID         string
}

// EntityTypeCI is the search entity type under which CIs are indexed.
const EntityTypeCI = "ci"

// IndexDocumentFor builds the search document for a CI, matching the field
// selection the reindex path uses in search.PGRepository.ReindexTenant.
func IndexDocumentFor(item *Item) Document {
	return Document{
		OrganizationID: item.OrganizationID,
		EntityType:     EntityTypeCI,
		EntityID:       item.ID,
		Title:          item.Name,
		Summary:        ciSummary(item),
		URL:            "/cmdb/" + item.ID,
		ClientID:       item.ClientID,
		SiteID:         item.SiteID,
	}
}

func ciSummary(item *Item) string {
	parts := []string{item.Hostname, item.Manufacturer, item.Model, item.SerialNumber, item.OSName, item.OSVersion}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// UniqueChecker finds the unique attributes (MET-15) whose value in attrs
// another active CI in the attribute's scope holds. The unique index of the
// attribute (DB-04) guards concurrent writes the check cannot see.
type UniqueChecker interface {
	UniqueConflicts(ctx context.Context, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) ([]string, error)
}

// UniqueError reports attributes whose value another CI holds (409).
type UniqueError struct {
	Fields []string
	// index is the violated attribute index, when the database reported it.
	index string
}

// validation: a fixed rule text with the names of the unique attributes.
func (e *UniqueError) Error() string {
	return "another CI holds the value of the unique attribute(s): " + strings.Join(e.Fields, ", ")
}

// ConflictViolations implements api.ViolationsConflict: 409 with a
// violation per attribute, never naming the other CI.
func (e *UniqueError) ConflictViolations() []api.Violation {
	violations := make([]api.Violation, len(e.Fields))
	for i, f := range e.Fields {
		violations[i] = api.Violation{Field: f, Detail: "another CI already holds this value of the unique attribute"}
	}
	return violations
}

// CheckUnique enforces the unique attributes server-side (MET-15) when the
// repository can check them (UniqueChecker): a value of attrs another
// active CI holds is a *UniqueError. ciID is the CI written ("" for a new
// one). Every path that sets or brings back the values of a CI runs it,
// not only ci.Service: the unique index may not exist yet (F-429).
func CheckUnique(ctx context.Context, repo Repository, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) error {
	checker, ok := repo.(UniqueChecker)
	if !ok {
		return nil
	}
	taken, err := checker.UniqueConflicts(ctx, orgID, ciTypeID, clientID, ciID, attrs)
	if err != nil {
		return err
	}
	if len(taken) > 0 {
		return &UniqueError{Fields: taken}
	}
	return nil
}

// Service coordinates CI persistence with mutation semantics. Audit-log and
// ci_change rows are written by the repository inside the same transaction as
// the mutation, so history can never diverge from the stored data.
type Service struct {
	repo   Repository
	limit  LimitGuard
	fields FieldResolver
	// catalog is fields as a RuleResolver (WP-257); nil: no catalog rules.
	catalog RuleResolver
	// references checks reference values against their targets (MET-12);
	// nil: form only.
	references fieldmeta.ReferenceLookup
	jobs       jobs.Tracker
	// bulkJobs runs bulk operations from 100 ids on (API-08); with
	// bulkQueue they run on the job queue (TEC-14).
	bulkJobs  BulkJobs
	bulkQueue BulkQueue
	// restoreRetention is the restore window (CI-05); 0 is RestoreRetention.
	restoreRetention time.Duration
}

// NewService creates a CI service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// NewServiceWithLimits creates a CI service that enforces the tenant's licensed
// CI limit on every create.
func NewServiceWithLimits(repo Repository, limit LimitGuard) *Service {
	return &Service{repo: repo, limit: limit}
}

// WithFieldResolver enables server-side validation of CI attributes against the
// global, CI-type and instance field metadata. Without a resolver the service
// keeps its previous pass-through behaviour, which keeps the constructor
// signatures stable for callers that do not manage field metadata.
// When the resolver is also a RuleResolver (citype's repositories), the
// active catalog rules of a CI are evaluated with its attribute rules
// (WP-257, MET-10).
func (s *Service) WithFieldResolver(fields FieldResolver) *Service {
	s.fields = fields
	s.catalog, _ = fields.(RuleResolver)
	return s
}

// WithReferences enables the existence, scope and client check of
// reference values (MET-12, WP-254) on the manual and API write path
// (Create, Update). Without a lookup (memory mode) reference values are
// checked for their form only.
func (s *Service) WithReferences(lookup fieldmeta.ReferenceLookup) *Service {
	s.references = lookup
	return s
}

// writeScope is the client a write leaves the CI in and whether the write
// changes it: then every reference value is checked against it.
type writeScope struct {
	clientID      string
	clientChanged bool
}

// validate checks the effective attributes of a CI against its field
// metadata and the reference values the write sets against their targets
// (checkReferences). templates, when set, is the template list the write
// leaves the CI with (WP-710): the attributes are checked against the
// schema composed with it. The active catalog rules of the CI (type chain,
// sets, template versions; WP-257) apply with the attribute rules; their
// conditions see the values after the write, typed columns included.
// Computed attributes are not written (WP-717): on a creation (existing
// nil) any computed key of the patch is a violation; on an update a
// computed key with the stored value is no change and returned in drop,
// any other value a violation (attributes.<key>, computed).
func (s *Service) validate(ctx context.Context, orgID, ciTypeID, ciID string, scope writeScope, existing, patch map[string]any,
	templates *[]citype.CITemplate) (drop []string, err error) {
	if s.fields == nil {
		if templates != nil {
			return nil, ErrTemplatesUnavailable
		}
		return nil, nil
	}
	defs, catalog, err := resolveWithRules(ctx, s.fields, s.catalog, orgID, ciTypeID, ciID, templates)
	if err != nil {
		return nil, err
	}
	drop, violations := computedWriteViolations(defs, patch, existing, existing == nil)
	if len(violations) > 0 {
		return nil, &ValidationError{Violations: violations}
	}
	if len(drop) > 0 {
		rest := make(map[string]any, len(patch))
		for k, v := range patch {
			if !slices.Contains(drop, k) {
				rest[k] = v
			}
		}
		patch = rest
	}
	values := MergePatch(existing, patch)
	if verr := validateWithRules(defs, catalog, values, patch, existing); verr != nil {
		return nil, verr
	}
	return drop, checkReferences(ctx, s.references, orgID, defs, scope, values, patch, existing)
}

// withoutKeys returns attrs without the keys drop; nil when nothing is
// left, so a patch of computed keys alone writes no attributes.
func withoutKeys(attrs map[string]any, drop []string) map[string]any {
	if len(drop) == 0 {
		return attrs
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if !slices.Contains(drop, k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LegacyTagsKey is the deprecated attribute that writes the tag assignment
// of a CI until the sunset (E-125, WP-281).
const LegacyTagsKey = "tags"

// LegacyTagsError reports a value of attributes.tags that is no array of
// strings (400 on the field attributes.tags).
type LegacyTagsError struct{}

// validation: a fixed rule text.
func (*LegacyTagsError) Error() string {
	return "attributes.tags must be an array of strings"
}

// ExtractLegacyTags takes the deprecated attributes.tags out of attrs (E-125,
// WP-281): it returns attrs without the key (nil when nothing else is left)
// and the tag list the write gives the CI, the original texts in their
// order. nil means attrs has no tags: the assignment stays; JSON null
// yields an empty list, which removes it (merge patch). A value that is no
// array of strings is a *LegacyTagsError. attrs itself is not changed. The
// repositories write the list to ci_tag in the transaction of the CI write
// with the key rule of TagKey; missing catalog tags are created there.
func ExtractLegacyTags(attrs map[string]any) (rest map[string]any, tags *[]string, err error) {
	raw, ok := attrs[LegacyTagsKey]
	if !ok {
		return attrs, nil, nil
	}
	list := []string{}
	switch v := raw.(type) {
	case nil:
	case []string:
		list = append(list, v...)
	case []any:
		for _, element := range v {
			text, isString := element.(string)
			if !isString {
				return nil, nil, &LegacyTagsError{}
			}
			list = append(list, text)
		}
	default:
		return nil, nil, &LegacyTagsError{}
	}
	rest = make(map[string]any, len(attrs)-1)
	for k, v := range attrs {
		if k != LegacyTagsKey {
			rest[k] = v
		}
	}
	if len(rest) == 0 {
		rest = nil
	}
	return rest, &list, nil
}

// tagKeyInvalid are the runs of characters a tag key does not have.
var tagKeyInvalid = regexp.MustCompile(`[^a-z0-9_-]+`)

// TagKey is the catalog key of a tag text, the key rule of migration 000218
// (tag_normalize_key): lower case, every run of other characters than a-z,
// 0-9, _ and - becomes _, leading _ and - are removed, at most 63
// characters. "" is no key; such a text is left out.
func TagKey(text string) string {
	key := strings.TrimLeft(tagKeyInvalid.ReplaceAllString(strings.ToLower(text), "_"), "_-")
	if len(key) > 63 {
		key = key[:63]
	}
	return key
}

// tagKeys returns the sorted keys of the tag texts without duplicates and
// for each key its first text (the display name of a new catalog tag).
func tagKeys(texts []string) (keys, names []string) {
	first := map[string]string{}
	keys = []string{}
	for _, text := range texts {
		key := TagKey(text)
		if _, seen := first[key]; key == "" || seen {
			continue
		}
		first[key] = text
		keys = append(keys, key)
	}
	sort.Strings(keys)
	names = make([]string, len(keys))
	for i, key := range keys {
		names[i] = first[key]
	}
	return keys, names
}

// List returns paginated CIs filtered by the given parameters.
func (s *Service) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Item, int, error) {
	return s.repo.List(ctx, orgID, filter, page)
}

// GetByID retrieves a single CI by ID.
func (s *Service) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	return s.repo.GetByID(ctx, orgID, id)
}

// Create inserts a new CI after verifying the tenant's licensed CI limit.
// The type is the writer's choice (ChosenType): an ID that is no UUID is
// refused here, the repository reads the type in the writing transaction.
func (s *Service) Create(ctx context.Context, item *Item) error {
	// The definitions the validation resolves are those the repository
	// computes with (WP-717).
	ctx = WithFieldsMemo(ctx)
	return s.activating(ctx, item.OrganizationID, func(ctx context.Context) error {
		return s.create(ctx, item)
	})
}

// create is Create in the transaction of activating.
func (s *Service) create(ctx context.Context, item *Item) error {
	if err := s.allowActive(ctx, item.OrganizationID); err != nil {
		return err
	}
	// attributes.tags is the deprecated write of the tag assignment
	// (E-125): it goes to ci_tag, not into the attributes.
	rest, legacy, tagsErr := ExtractLegacyTags(item.Attributes)
	if tagsErr != nil {
		return tagsErr
	}
	if legacy != nil {
		if rest == nil {
			rest = map[string]any{}
		}
		item.Attributes, item.LegacyTags = rest, legacy
	}
	if err := CheckReservedKeys("attributes", item.Attributes); err != nil {
		return err
	}
	typed := typedValues(item)
	if err := validateTypedColumns(typed); err != nil {
		return err
	}
	if err := checkCITypeID(item.CITypeID); err != nil {
		return err
	}
	item.ChosenType = true
	// A creation with templates is validated against the schema composed
	// with them, atomically with the assignment (WP-710).
	var templates *[]citype.CITemplate
	if len(item.Templates) > 0 {
		if !s.TemplatesAvailable() {
			return ErrTemplatesUnavailable
		}
		templates = &item.Templates
	}
	if _, err := s.validate(ctx, item.OrganizationID, item.CITypeID, "", writeScope{clientID: item.ClientID},
		nil, withTyped(item.Attributes, typed), templates); err != nil {
		return err
	}
	if item.AssetID != "" && item.LocationID != "" {
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: locationOwnedByAsset}}}
	}
	if err := CheckUnique(ctx, s.repo, item.OrganizationID, item.CITypeID, item.ClientID, "", item.Attributes); err != nil {
		return err
	}
	return s.repo.Create(ctx, item)
}

// activating runs fn, which checks max_cis (allowActive) and makes a CI
// active, in one transaction of the context's tenant scope when the
// repository opens one (TxRunner, the PostgreSQL repository) and a limit
// guard is set: the admission and the write commit together, so the
// max_cis lock of the admission holds until the CI is written (WP-835,
// SR-09). The memory repository keeps the count without a transaction.
func (s *Service) activating(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	runner, ok := s.repo.(TxRunner)
	if !ok || s.limit == nil {
		return fn(ctx)
	}
	return runner.RunInTx(ctx, orgID, fn)
}

// allowActive checks max_cis before a CI becomes active (ENT-03): Create
// adds one, Restore and Unmerge bring a deleted one back. Over the limit it
// returns the entitlement error (403 entitlement-limit) and nothing is
// written. It counts every active CI of the organization, also those
// outside the caller's scope (CountActive, F-403). In a transaction
// (activating) it admits through CIAdmitter: the organization's max_cis
// lock and the count under it, so that concurrent creations of every
// channel at the limit admit exactly (WP-835, SR-09); without one (memory
// repository) it counts as before.
func (s *Service) allowActive(ctx context.Context, orgID string) error {
	if s.limit == nil {
		return nil
	}
	if database.InTx(ctx) {
		return NewCIAdmitter(s.limit).AdmitNew(ctx, orgID, 1)
	}
	total, err := CountActive(ctx, s.repo, orgID)
	if err != nil {
		return err
	}
	return s.limit.AllowCreate(ctx, orgID, LimitFeatureKey, total)
}

// Update modifies an existing CI. The repository loads the prior state inside
// the mutation transaction so the recorded diff cannot race with concurrent
// writers.
func (s *Service) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) { //nolint:gocritic // signature of Repository; the update works on its own copy
	// null clears a nullable field; on a required one it is refused (CI-04).
	if err := req.nullViolations(); err != nil {
		return nil, err
	}
	// The definitions the validation resolves are those the repository
	// computes with (WP-717).
	ctx = WithFieldsMemo(ctx)
	// attributes.tags replaces the tag assignment (E-125, merge patch: null
	// removes it).
	rest, legacy, tagsErr := ExtractLegacyTags(req.Attributes)
	if tagsErr != nil {
		return nil, tagsErr
	}
	if legacy != nil {
		req.Attributes, req.LegacyTags = rest, legacy
	}
	if err := CheckReservedKeys("attributes", req.Attributes); err != nil {
		return nil, err
	}
	typed := typedPatch(&req)
	if err := validateTypedColumns(typed); err != nil {
		return nil, err
	}
	if req.Templates != nil && !s.TemplatesAvailable() {
		return nil, ErrTemplatesUnavailable
	}
	_, unique := s.repo.(UniqueChecker)
	unique = unique && (req.Attributes != nil || req.ClientID != nil)
	needsBefore := unique || s.fields != nil || (req.Manual != nil && (req.LocationID != nil || req.SerialNumber != nil))
	if needsBefore {
		before, err := s.repo.GetByID(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		// A manual change of an instance attribute needs its own right
		// (MET-14, WP-256).
		if err := s.checkInstanceWrite(ctx, orgID, before, &req); err != nil {
			return nil, err
		}
		// The client the CI has after the write: references are checked
		// against it, all of them when the write changes it (WP-254).
		clientID := before.ClientID
		if req.ClientID != nil {
			clientID = *req.ClientID
		}
		scope := writeScope{clientID: clientID, clientChanged: !strings.EqualFold(clientID, before.ClientID)}
		drop, validateErr := s.validate(ctx, orgID, before.CITypeID, id, scope,
			withTyped(before.Attributes, typedValues(before)), withTyped(req.Attributes, typed), req.Templates)
		if validateErr != nil {
			return nil, validateErr
		}
		// A computed key with its stored value is no change (WP-717).
		req.Attributes = withoutKeys(req.Attributes, drop)
		if err := checkAssetOwnedFields(before, &req); err != nil {
			return nil, err
		}
		if unique {
			if err := CheckUnique(ctx, s.repo, orgID, before.CITypeID, clientID, id, MergePatch(before.Attributes, req.Attributes)); err != nil {
				return nil, err
			}
		}
	}
	return s.repo.Update(ctx, orgID, id, req)
}

// TemplatesAvailable reports whether CIs keep template assignments
// (WP-710): only with the field resolver of the database
// (TemplateFieldResolver). Without it GET and PUT /cis/{id}/templates and a
// POST /cis with templates answer 503.
func (s *Service) TemplatesAvailable() bool {
	_, ok := s.fields.(TemplateFieldResolver)
	return ok
}

// ListTemplates returns the templates assigned to a visible CI in position
// order (ci_template, WP-710).
func (s *Service) ListTemplates(ctx context.Context, orgID, id string) ([]citype.CITemplate, error) {
	if !s.TemplatesAvailable() {
		return nil, ErrTemplatesUnavailable
	}
	item, err := s.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if item.Templates == nil {
		return []citype.CITemplate{}, nil
	}
	return item.Templates, nil
}

// SetTemplates replaces the ordered template list of a CI (PUT
// /cis/{id}/templates, MET-16, WP-710): a manual write through Update
// (If-Match, version + 1 when the list changes); the attributes of the CI
// are validated against the schema composed with the new list. Without a
// version an entry takes the template's current version, the position is
// the index. It returns the CI with the new list.
func (s *Service) SetTemplates(ctx context.Context, orgID, id string, templates []citype.CITemplate, ifMatch *int64, author string) (*Item, error) {
	if templates == nil {
		templates = []citype.CITemplate{}
	}
	return s.Update(ctx, orgID, id, UpdateRequest{Templates: &templates, IfMatch: ifMatch, Manual: &ManualChange{Author: author}})
}

// checkAssetOwnedFields rejects a manual write of the fields the asset owns
// on a linked CI (DB-05, CH12): the location mirrors the asset; the serial
// number is the asset's, the CI keeps the observed one from discovery.
func checkAssetOwnedFields(before *Item, req *UpdateRequest) error {
	if req.Manual == nil {
		return nil
	}
	linked := before.AssetID != ""
	if req.AssetID != nil {
		linked = *req.AssetID != ""
	}
	if !linked {
		return nil
	}
	var violations []Violation
	if req.LocationID != nil && *req.LocationID != before.LocationID {
		violations = append(violations, Violation{Field: "location_id", Detail: locationOwnedByAsset})
	}
	if req.SerialNumber != nil && *req.SerialNumber != before.SerialNumber {
		violations = append(violations, Violation{Field: "serial_number",
			Detail: "the serial number of a CI linked to an asset is the asset's; the CI keeps the observed value"})
	}
	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	return nil
}

// WithRestoreRetention sets the restore window to the configured
// soft-delete retention (RETICORA_RETENTION_SOFT_DELETE_DAYS, CI-05); a
// value that is not positive keeps the default RestoreRetention.
func (s *Service) WithRestoreRetention(retention time.Duration) *Service {
	if retention > 0 {
		s.restoreRetention = retention
	}
	return s
}

// Restore brings a deleted CI back within the retention (CI-05, API-05).
// The restored CI counts against max_cis like a new one: at the limit the
// CI stays deleted (ENT-03, F-399).
func (s *Service) Restore(ctx context.Context, orgID, id string) (*Item, error) {
	restorer, ok := s.repo.(Restorer)
	if !ok {
		return nil, errors.New("restore is not supported by this repository")
	}
	retention := s.restoreRetention
	if retention <= 0 {
		retention = RestoreRetention
	}
	var restored *Item
	err := s.activating(ctx, orgID, func(ctx context.Context) error {
		if err := s.allowActive(ctx, orgID); err != nil {
			return err
		}
		var err error
		restored, err = restorer.RestoreDeleted(ctx, orgID, id, retention)
		return err
	})
	if err != nil {
		return nil, err
	}
	return restored, nil
}

// Delete removes a CI after loading its prior state.
func (s *Service) Delete(ctx context.Context, orgID, id string) (*Item, error) {
	item, err := s.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Delete(ctx, orgID, id); err != nil {
		return nil, err
	}
	return item, nil
}

// ListChanges returns paginated CI change history when supported by the repository.
func (s *Service) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	reader, ok := s.repo.(ChangeReader)
	if !ok {
		return []Change{}, 0, nil
	}
	return reader.ListChanges(ctx, orgID, ciID, page)
}
