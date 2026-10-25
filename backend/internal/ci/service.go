package ci

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// CreateManyResult lists the positions of the items a unique index refused.
type CreateManyResult struct {
	Refused []int
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
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Item, error)
	Create(ctx context.Context, item *Item) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error)
	Delete(ctx context.Context, orgID, id string) error
}

// RestoreRetention is how long a deleted CI can be restored (CI-05); after
// it the hard-delete job removes it.
const RestoreRetention = 90 * 24 * time.Hour

// ErrRetentionExpired: the CI was deleted longer ago than the retention and
// can no longer be restored (410).
var ErrRetentionExpired = errors.New("the CI was deleted longer ago than the restore retention")

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
	IndexDocument(ctx context.Context, doc Document) error
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

// checkUnique enforces the unique attributes server-side (MET-15) when the
// repository can check them.
func (s *Service) checkUnique(ctx context.Context, orgID, ciTypeID, clientID, ciID string, attrs map[string]any) error {
	checker, ok := s.repo.(UniqueChecker)
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
	jobs   jobs.Tracker
	// bulkJobs runs bulk operations from 100 ids on (API-08).
	bulkJobs BulkJobs
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
func (s *Service) WithFieldResolver(fields FieldResolver) *Service {
	s.fields = fields
	return s
}

// validate checks the effective attributes of a CI against its field metadata.
func (s *Service) validate(ctx context.Context, orgID, ciTypeID, ciID string, existing, patch map[string]any) error {
	if s.fields == nil {
		return nil
	}
	defs, err := resolveFields(ctx, s.fields, orgID, ciTypeID, ciID)
	if err != nil {
		return err
	}
	return validateAttributes(defs, MergePatch(existing, patch), patch, existing)
}

// List returns paginated CIs filtered by the given parameters.
func (s *Service) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
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
	if s.limit != nil {
		_, total, err := s.repo.List(ctx, item.OrganizationID, FilterParams{}, api.PaginationParams{Limit: 1})
		if err != nil {
			return err
		}
		if err := s.limit.AllowCreate(ctx, item.OrganizationID, LimitFeatureKey, int64(total)); err != nil {
			return err
		}
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
	if err := s.validate(ctx, item.OrganizationID, item.CITypeID, "", nil, withTyped(item.Attributes, typed)); err != nil {
		return err
	}
	if item.AssetID != "" && item.LocationID != "" {
		return &ValidationError{Violations: []Violation{{Field: "location_id", Detail: locationOwnedByAsset}}}
	}
	if err := s.checkUnique(ctx, item.OrganizationID, item.CITypeID, item.ClientID, "", item.Attributes); err != nil {
		return err
	}
	return s.repo.Create(ctx, item)
}

// Update modifies an existing CI. The repository loads the prior state inside
// the mutation transaction so the recorded diff cannot race with concurrent
// writers.
func (s *Service) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	if err := CheckReservedKeys("attributes", req.Attributes); err != nil {
		return nil, err
	}
	typed := typedPatch(&req)
	if err := validateTypedColumns(typed); err != nil {
		return nil, err
	}
	_, unique := s.repo.(UniqueChecker)
	unique = unique && (req.Attributes != nil || req.ClientID != nil)
	needsBefore := unique || s.fields != nil || (req.Manual != nil && (req.LocationID != nil || req.SerialNumber != nil))
	if needsBefore {
		before, err := s.repo.GetByID(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		if err := s.validate(ctx, orgID, before.CITypeID, id,
			withTyped(before.Attributes, typedValues(before)), withTyped(req.Attributes, typed)); err != nil {
			return nil, err
		}
		if err := checkAssetOwnedFields(before, &req); err != nil {
			return nil, err
		}
		if unique {
			clientID := before.ClientID
			if req.ClientID != nil {
				clientID = *req.ClientID
			}
			if err := s.checkUnique(ctx, orgID, before.CITypeID, clientID, id, MergePatch(before.Attributes, req.Attributes)); err != nil {
				return nil, err
			}
		}
	}
	return s.repo.Update(ctx, orgID, id, req)
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

// Delete removes a CI after loading its prior state.
// Restore brings a deleted CI back within the retention (CI-05, API-05).
func (s *Service) Restore(ctx context.Context, orgID, id string) (*Item, error) {
	restorer, ok := s.repo.(Restorer)
	if !ok {
		return nil, errors.New("restore is not supported by this repository")
	}
	return restorer.RestoreDeleted(ctx, orgID, id, RestoreRetention)
}

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
