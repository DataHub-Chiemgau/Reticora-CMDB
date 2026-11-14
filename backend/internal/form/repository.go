package form

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

var (
	// ErrNotFound: the form definition is unknown, of another organization
	// or outside the caller's scope; all answer 404 alike (TEN-04).
	ErrNotFound = errors.New("form definition not found")
	// ErrSubmissionNotFound: the submission is unknown, of another
	// organization or outside the caller's scope (404).
	ErrSubmissionNotFound = errors.New("form submission not found")
	// ErrVersionNotFound: the form has no such schema version, or the form
	// is unknown, of another organization or outside the caller's scope
	// (404, TEN-04).
	ErrVersionNotFound = errors.New("form definition version not found")
	// ErrPreconditionFailed: If-Match names another version than the
	// current one of the form definition (412, API-07).
	ErrPreconditionFailed = errors.New("the form definition has changed since the If-Match version")
	// ErrVersionChanged: the schema of the form changed between the
	// validation of a submission and its insert (409); the client submits
	// again against the new schema.
	// ErrRunNotWaiting: the run a submission names is unknown, of another
	// organization, outside the caller's scope or waits for no input of
	// this form, or the step was submitted already (409 run_not_waiting,
	// WP-1102).
	ErrRunNotWaiting = errors.New("run_not_waiting: the workflow run waits for no input of this form")
	// ErrNotFormRecipient: the caller is not the recipient the waiting step
	// requested the form from (403, WP-1102).
	ErrNotFormRecipient = errors.New("you are not the recipient of this form request")
	ErrVersionChanged   = errors.New("the form schema changed while the submission was validated; submit again")
)

// Repository defines persistence for form definitions and submissions.
type Repository interface {
	ListDefinitions(ctx context.Context, orgID, clientID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error)
	GetDefinition(ctx context.Context, orgID, id string) (*Definition, error)
	CreateDefinition(ctx context.Context, def *Definition) error
	UpdateDefinition(ctx context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error)
	DeleteDefinition(ctx context.Context, orgID, id string) error
	ListSubmissions(ctx context.Context, orgID string, filter SubmissionFilter, page api.PaginationParams) ([]Submission, int, error)
	GetSubmission(ctx context.Context, orgID, id string) (*Submission, error)
	// CreateSubmission binds the submission to the current schema version
	// of its form in the same statement (WFL-01).
	CreateSubmission(ctx context.Context, sub *Submission) error
	// ListVersions lists the schema versions of a form, newest first
	// (ErrNotFound for a form that is unknown or outside the scope).
	ListVersions(ctx context.Context, orgID, formID string, page api.PaginationParams) ([]DefinitionVersion, int, error)
	// GetVersion returns schema version number version of a form.
	GetVersion(ctx context.Context, orgID, formID string, version int) (*DefinitionVersion, error)
}

type MemoryRepository struct {
	mu          sync.RWMutex
	definitions map[string]*Definition
	versions    map[string]*DefinitionVersion
	submissions map[string]*Submission
	next        int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{definitions: map[string]*Definition{}, versions: map[string]*DefinitionVersion{}, submissions: map[string]*Submission{}}
}

// nextIDLocked returns ids of UUID form, as the handler accepts only UUID
// ids (API-03).
func (r *MemoryRepository) nextIDLocked() string {
	r.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.next)
}

// addVersionLocked stores the schema and UI hints of def as its next
// schema version and points def at it.
func (r *MemoryRepository) addVersionLocked(def *Definition, createdBy string, at time.Time) {
	v := &DefinitionVersion{
		ID: r.nextIDLocked(), OrganizationID: def.OrganizationID, FormID: def.ID, ClientID: def.ClientID,
		Version: def.SchemaVersion + 1, Schema: def.Schema, UIHints: def.UIHints, CreatedBy: createdBy, CreatedAt: at,
	}
	r.versions[v.ID] = v
	def.CurrentVersionID = v.ID
	def.SchemaVersion = v.Version
}

func (r *MemoryRepository) ListDefinitions(_ context.Context, orgID, clientID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Definition{}
	for _, def := range r.definitions {
		if def.OrganizationID != orgID || (clientID != "" && def.ClientID != clientID) || (activeOnly && !def.Active) {
			continue
		}
		out = append(out, *def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return pageDefinitions(out, page)
}
func (r *MemoryRepository) GetDefinition(_ context.Context, orgID, id string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def := r.definitions[id]
	if def == nil || def.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *def
	return &cp, nil
}
func (r *MemoryRepository) CreateDefinition(ctx context.Context, def *Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def.ID = r.nextIDLocked()
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	if def.Schema == nil {
		def.Schema = JSONMap{}
	}
	if def.UIHints == nil {
		def.UIHints = JSONMap{}
	}
	def.Version = 1
	def.SchemaVersion = 0
	r.addVersionLocked(def, createdBy(ctx), now)
	cp := *def
	r.definitions[def.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateDefinition(_ context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := r.definitions[id]
	if def == nil || def.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if !ifMatches(req.IfMatch, def.Version) {
		return nil, ErrPreconditionFailed
	}
	next := *def
	applyUpdate(&next, &req)
	changed, newSchema := changes(def, &next)
	if !changed {
		cp := *def
		return &cp, nil
	}
	now := time.Now().UTC()
	if newSchema {
		r.addVersionLocked(&next, req.CreatedBy, now)
	}
	if next.ClientID != def.ClientID {
		// The versions follow the client of their form.
		for _, v := range r.versions {
			if v.FormID == id {
				v.ClientID = next.ClientID
			}
		}
	}
	next.Version = def.Version + 1
	next.UpdatedAt = now
	*def = next
	cp := *def
	return &cp, nil
}
func (r *MemoryRepository) DeleteDefinition(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := r.definitions[id]
	if def == nil || def.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.definitions, id)
	// Versions and submissions go with their form (ON DELETE CASCADE).
	for vid, v := range r.versions {
		if v.FormID == id {
			delete(r.versions, vid)
		}
	}
	for sid, sub := range r.submissions {
		if sub.FormID == id {
			delete(r.submissions, sid)
		}
	}
	return nil
}
func (r *MemoryRepository) ListVersions(_ context.Context, orgID, formID string, page api.PaginationParams) ([]DefinitionVersion, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if def := r.definitions[formID]; def == nil || def.OrganizationID != orgID {
		return nil, 0, ErrNotFound
	}
	out := []DefinitionVersion{}
	for _, v := range r.versions {
		if v.OrganizationID == orgID && v.FormID == formID {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	total := len(out)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return out[start:end], total, nil
}
func (r *MemoryRepository) GetVersion(_ context.Context, orgID, formID string, version int) (*DefinitionVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.versions {
		if v.OrganizationID == orgID && v.FormID == formID && v.Version == version {
			cp := *v
			return &cp, nil
		}
	}
	return nil, ErrVersionNotFound
}
func (r *MemoryRepository) ListSubmissions(_ context.Context, orgID string, filter SubmissionFilter, page api.PaginationParams) ([]Submission, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Submission{}
	for _, sub := range r.submissions {
		if sub.OrganizationID != orgID || (filter.FormID != "" && sub.FormID != filter.FormID) || (filter.Status != "" && sub.Status != filter.Status) || (filter.TicketID != "" && sub.TicketID != filter.TicketID) || (filter.CIID != "" && sub.CIID != filter.CIID) {
			continue
		}
		out = append(out, *sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return pageSubmissions(out, page)
}
func (r *MemoryRepository) GetSubmission(_ context.Context, orgID, id string) (*Submission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub := r.submissions[id]
	if sub == nil || sub.OrganizationID != orgID {
		return nil, ErrSubmissionNotFound
	}
	cp := *sub
	return &cp, nil
}
func (r *MemoryRepository) CreateSubmission(_ context.Context, sub *Submission) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := r.definitions[sub.FormID]
	if def == nil || def.OrganizationID != sub.OrganizationID {
		return ErrNotFound
	}
	if sub.ExpectedVersionID != "" && sub.ExpectedVersionID != def.CurrentVersionID {
		return ErrVersionChanged
	}
	if err := Validate(def.Schema, map[string]any(sub.Values)); err != nil {
		return err
	}
	sub.FormVersionID = def.CurrentVersionID
	sub.FormVersion = def.SchemaVersion
	sub.ID = r.nextIDLocked()
	now := time.Now().UTC()
	sub.CreatedAt = now
	sub.UpdatedAt = now
	if sub.Values == nil {
		sub.Values = JSONMap{}
	}
	if sub.Status == "" {
		sub.Status = "submitted"
	}
	cp := *sub
	r.submissions[sub.ID] = &cp
	return nil
}

func pageDefinitions(items []Definition, page api.PaginationParams) ([]Definition, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
func pageSubmissions(items []Submission, page api.PaginationParams) ([]Submission, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}

// applyUpdate copies the fields the request carries onto def.
func applyUpdate(def *Definition, req *UpdateDefinitionRequest) {
	if req.ClientID != nil {
		def.ClientID = *req.ClientID
	}
	if req.Name != nil {
		def.Name = *req.Name
	}
	if req.Description != nil {
		def.Description = *req.Description
	}
	if req.Schema != nil {
		def.Schema = req.Schema
	}
	if req.UIHints != nil {
		def.UIHints = req.UIHints
	}
	if req.Active != nil {
		def.Active = *req.Active
	}
}

// changes reports whether next differs from current at all, and whether
// its schema or UI hints differ (compared as JSON): only those store a new
// schema version; name, description, client and active raise the version
// counter only (WFL-01, AK3 of WP-1100).
func changes(current, next *Definition) (changed, newSchema bool) {
	newSchema = !sameJSON(current.Schema, next.Schema) || !sameJSON(current.UIHints, next.UIHints)
	changed = newSchema || current.ClientID != next.ClientID || current.Name != next.Name ||
		current.Description != next.Description || current.Active != next.Active
	return changed, newSchema
}

func sameJSON(a, b any) bool {
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ra, rb)
}

// ifMatches reports whether an If-Match version admits a change of a form
// at version current: no header and "*" always do (API-07).
func ifMatches(ifMatch *int64, current int64) bool {
	return ifMatch == nil || *ifMatch == api.IfMatchAny || *ifMatch == current
}

// createdBy is the user of the request that stores a version; empty for a
// caller without a user id (API key, system).
func createdBy(ctx context.Context) string {
	if id := tenant.FromContext(ctx).UserID; api.IsUUID(id) {
		return id
	}
	return ""
}
