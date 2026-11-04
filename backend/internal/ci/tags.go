package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Tag REST (CI-08, API-06; WP-753, E-38, E-105): the tag catalog of an
// organization (GET/POST /tags, GET/PATCH/DELETE /tags/{id}), the merge of
// two tags (POST /tags/{id}/merge) and the replacement of the tags of a CI
// (PUT /cis/{id}/tags). Tags are core: no module gate, no feature key;
// reading needs ci:read, the catalog tag:manage and the assignment of a CI
// ci:write (server/authz.go). The catalog and the assignments live in tag
// and ci_tag (migration 000218, WP-281).

// Tag is a catalog tag of an organization.
type Tag struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	// Key is the unique, unchangeable key of the tag in the organization:
	// lower case, digits, _ and -, at most 63 characters.
	Key string `json:"key"`
	// DisplayName holds the name in de and/or en.
	DisplayName map[string]string `json:"display_name"`
	// Color is #rrggbb in lower case or nil.
	Color *string `json:"color"`
	// IsSystem marks a tag the system relies on (archived): it cannot be
	// deleted or merged into another tag.
	IsSystem  bool      `json:"is_system"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateTagRequest is the body of POST /tags.
type CreateTagRequest struct {
	Key         string            `json:"key"`
	DisplayName map[string]string `json:"display_name"`
	Color       *string           `json:"color"`
}

// OptionalColor is the color of a PATCH: Set reports that the member was
// sent, a nil Value with Set removes the color.
type OptionalColor struct {
	Set   bool
	Value *string
}

// UnmarshalJSON records that the member was sent (null too).
func (o *OptionalColor) UnmarshalJSON(data []byte) error {
	o.Set = true
	return json.Unmarshal(data, &o.Value)
}

// UpdateTagRequest is the body of PATCH /tags/{id}: display_name and color
// change; the key is unchangeable (a sent key is 422).
type UpdateTagRequest struct {
	Key         *string           `json:"key"`
	DisplayName map[string]string `json:"display_name"`
	Color       OptionalColor     `json:"color"`
}

// MergeTagRequest is the body of POST /tags/{id}/merge.
type MergeTagRequest struct {
	IntoID string `json:"into_id"`
}

// TagMergeResult is the outcome of a merge: the target tag and the number
// of CIs whose assignment moved to it. CIs are those CIs after the merge
// (their history, audit entry and event are written); the search index
// takes them again.
type TagMergeResult struct {
	Tag   *Tag    `json:"tag"`
	Moved int     `json:"moved"`
	CIs   []*Item `json:"-"`
}

// ReplaceTagsRequest is the body of PUT /cis/{id}/tags.
type ReplaceTagsRequest struct {
	Tags []string `json:"tags"`
}

// TagStore is the tag catalog and the tag assignment of the CI
// repositories (PGRepository, MemoryRepository); the indexing decorator
// forwards it (SR-11).
type TagStore interface {
	// ListTags reads the tags of the organization sorted by key, at most
	// one row more than the page limit (keyset cursor).
	ListTags(ctx context.Context, orgID string, page api.PaginationParams) ([]Tag, int, error)
	GetTag(ctx context.Context, orgID, id string) (*Tag, error)
	CreateTag(ctx context.Context, tag *Tag) error
	UpdateTag(ctx context.Context, orgID, id string, req *UpdateTagRequest) (*Tag, error)
	DeleteTag(ctx context.Context, orgID, id string) error
	// MergeTag moves the assignments of tag sourceID to tag intoID and
	// deletes sourceID, in one transaction.
	MergeTag(ctx context.Context, orgID, sourceID, intoID string) (*TagMergeResult, error)
	// ReplaceCITags replaces the tags of CI ciID with the catalog tags
	// keys (a manual change with If-Match); unknown keys are a
	// *ValidationError.
	ReplaceCITags(ctx context.Context, orgID, ciID string, keys []string, ifMatch *int64) (*Item, error)
}

// Errors of the tag catalog.
var (
	ErrTagNotFound = errors.New("tag not found")
	// ErrTagInUse: the tag is assigned to CIs or tickets (409).
	ErrTagInUse = errors.New("the tag is assigned to CIs or tickets")
	// ErrSystemTag: a system tag is neither deleted nor merged away (409).
	ErrSystemTag = errors.New("a system tag cannot be deleted or merged into another tag")
	// ErrTagsUnsupported: the repository keeps no tag catalog.
	ErrTagsUnsupported = errors.New("tags are not supported by this repository")
)

// Sort order of GET /tags (API-02).
const (
	tagSort = "key"
	tagDir  = "asc"
)

var (
	tagKeyPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	tagColorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// validateTagKey checks a catalog key (CHECK tag_key_check).
func validateTagKey(key string) []Violation {
	if !tagKeyPattern.MatchString(key) {
		return []Violation{{Field: "key", Detail: "must match ^[a-z0-9][a-z0-9_-]{0,62}$"}}
	}
	return nil
}

// validateTagDisplayName checks display_name (CHECK tag_display_name_i18n):
// de and/or en, no other language, no empty name.
func validateTagDisplayName(names map[string]string) []Violation {
	if len(names) == 0 {
		return []Violation{{Field: "display_name", Detail: "needs a name in de or en"}}
	}
	var out []Violation
	for _, lang := range slices.Sorted(maps.Keys(names)) {
		switch {
		case lang != "de" && lang != "en":
			out = append(out, Violation{Field: "display_name." + lang, Detail: "only de and en are allowed"})
		case names[lang] == "":
			out = append(out, Violation{Field: "display_name." + lang, Detail: "must not be empty"})
		}
	}
	return out
}

// validateTagColor checks a color (CHECK tag_color_check).
func validateTagColor(color *string) []Violation {
	if color != nil && !tagColorPattern.MatchString(*color) {
		return []Violation{{Field: "color", Detail: "must be #rrggbb in lower case"}}
	}
	return nil
}

// validationOf returns the violations as a *ValidationError, nil without.
func validationOf(violations ...[]Violation) error {
	var all []Violation
	for _, v := range violations {
		all = append(all, v...)
	}
	if len(all) == 0 {
		return nil
	}
	return &ValidationError{Violations: all}
}

// newTag builds and checks the tag of POST /tags; without display_name the
// key names the tag in de and en.
func newTag(orgID string, req *CreateTagRequest) (*Tag, error) {
	names := req.DisplayName
	if names == nil {
		names = map[string]string{"de": req.Key, "en": req.Key}
	}
	if err := validationOf(validateTagKey(req.Key), validateTagDisplayName(names), validateTagColor(req.Color)); err != nil {
		return nil, err
	}
	return &Tag{OrganizationID: orgID, Key: req.Key, DisplayName: names, Color: req.Color}, nil
}

// check validates a PATCH: no key, display_name and color like a create.
func (req *UpdateTagRequest) check() error {
	var violations []Violation
	if req.Key != nil {
		violations = append(violations, Violation{Field: "key", Detail: "the key of a tag cannot be changed"})
	}
	if req.DisplayName != nil {
		violations = append(violations, validateTagDisplayName(req.DisplayName)...)
	}
	if req.Color.Set {
		violations = append(violations, validateTagColor(req.Color.Value)...)
	}
	return validationOf(violations)
}

// apply merges the PATCH into tag.
func (req *UpdateTagRequest) apply(tag *Tag) {
	if req.DisplayName != nil {
		tag.DisplayName = req.DisplayName
	}
	if req.Color.Set {
		tag.Color = req.Color.Value
	}
}

// tagPosition is the (key, id) of a keyset cursor of GET /tags.
func tagPosition(c *api.Cursor) (key, id string, err error) {
	if !c.Matches(tagSort, tagDir) {
		return "", "", fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
	}
	if !api.IsUUID(c.ID) {
		return "", "", fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
	}
	return c.Value, c.ID, nil
}

// tagPage is the list response of the rows a repository read (at most one
// more than the limit): when more follow, the page ends at the limit and
// next_cursor is the keyset cursor after its last row.
func tagPage(rows []Tag, total int, page api.PaginationParams) api.ListResponse[Tag] {
	list := api.ListResponse[Tag]{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: tagSort, Dir: tagDir, Value: last.Key, ID: last.ID})
	}
	return list
}

// catalogKeys returns the sorted keys of a PUT /cis/{id}/tags without
// duplicates and the violations of keys that are no catalog key in form.
func catalogKeys(keys []string) ([]string, []Violation) {
	var violations []Violation
	out := make([]string, 0, len(keys))
	for i, key := range keys {
		if !tagKeyPattern.MatchString(key) {
			violations = append(violations, Violation{Field: fmt.Sprintf("tags[%d]", i), Detail: "unknown tag"})
			continue
		}
		if !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, violations
}

// unknownTags returns the violations of the keys of the request that the
// catalog lacks (known holds the catalog keys found).
func unknownTags(keys []string, known map[string]bool) []Violation {
	var violations []Violation
	for i, key := range keys {
		if tagKeyPattern.MatchString(key) && !known[key] {
			violations = append(violations, Violation{Field: fmt.Sprintf("tags[%d]", i), Detail: "unknown tag"})
		}
	}
	return violations
}

// tagStore returns the tag store of the repository.
func (s *Service) tagStore() (TagStore, error) {
	store, ok := s.repo.(TagStore)
	if !ok {
		return nil, ErrTagsUnsupported
	}
	return store, nil
}

// ListTags returns a page of the tag catalog sorted by key.
func (s *Service) ListTags(ctx context.Context, orgID string, page api.PaginationParams) ([]Tag, int, error) {
	store, err := s.tagStore()
	if err != nil {
		return nil, 0, err
	}
	return store.ListTags(ctx, orgID, page)
}

// GetTag returns a catalog tag.
func (s *Service) GetTag(ctx context.Context, orgID, id string) (*Tag, error) {
	store, err := s.tagStore()
	if err != nil {
		return nil, err
	}
	return store.GetTag(ctx, orgID, id)
}

// CreateTag checks and stores a catalog tag; a taken key is a conflict on
// key (409).
func (s *Service) CreateTag(ctx context.Context, orgID string, req *CreateTagRequest) (*Tag, error) {
	store, err := s.tagStore()
	if err != nil {
		return nil, err
	}
	tag, err := newTag(orgID, req)
	if err != nil {
		return nil, err
	}
	if createErr := store.CreateTag(ctx, tag); createErr != nil {
		return nil, createErr
	}
	return tag, nil
}

// UpdateTag changes display_name and color of a catalog tag.
func (s *Service) UpdateTag(ctx context.Context, orgID, id string, req *UpdateTagRequest) (*Tag, error) {
	store, err := s.tagStore()
	if err != nil {
		return nil, err
	}
	if checkErr := req.check(); checkErr != nil {
		return nil, checkErr
	}
	return store.UpdateTag(ctx, orgID, id, req)
}

// DeleteTag deletes a catalog tag that no CI carries and that is no system
// tag.
func (s *Service) DeleteTag(ctx context.Context, orgID, id string) error {
	store, err := s.tagStore()
	if err != nil {
		return err
	}
	return store.DeleteTag(ctx, orgID, id)
}

// MergeTag merges tag sourceID into tag intoID.
func (s *Service) MergeTag(ctx context.Context, orgID, sourceID, intoID string) (*TagMergeResult, error) {
	store, err := s.tagStore()
	if err != nil {
		return nil, err
	}
	switch {
	case !api.IsUUID(intoID):
		return nil, &ValidationError{Violations: []Violation{{Field: "into_id", Detail: "must be the id of a tag"}}}
	case sourceID == intoID:
		return nil, &ValidationError{Violations: []Violation{{Field: "into_id", Detail: "must name another tag"}}}
	}
	return store.MergeTag(ctx, orgID, sourceID, intoID)
}

// ReplaceCITags replaces the tags of a CI with catalog tags (PUT
// /cis/{id}/tags): a manual change with If-Match (API-07); unknown keys are
// 422, tags are not created implicitly.
func (s *Service) ReplaceCITags(ctx context.Context, orgID, ciID string, keys []string, ifMatch *int64) (*Item, error) {
	store, err := s.tagStore()
	if err != nil {
		return nil, err
	}
	if keys == nil {
		return nil, &ValidationError{Violations: []Violation{{Field: "tags", Detail: "is required (an empty list removes every tag)"}}}
	}
	return store.ReplaceCITags(ctx, orgID, ciID, keys, ifMatch)
}

// writeTagError answers a failed tag request: 404 for an unknown tag or
// CI, 409 for a tag in use or a system tag and a taken key, 412/409 for
// If-Match, 422 for invalid values, 400 for a forged cursor. Other errors
// go through api.WriteRepoError, which keeps database texts out of the
// answer.
func writeTagError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTagNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTagNotFound.Error())
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
	case errors.Is(err, ErrTagInUse):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrTagInUse.Error())
	case errors.Is(err, ErrSystemTag):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrSystemTag.Error())
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
	case errors.Is(err, ErrTagsUnsupported):
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", ErrTagsUnsupported.Error())
	case writeConcurrencyError(w, err), writeValidationError(w, err), api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// registerTagRoutes registers the tag routes (RegisterRoutes).
func (h *Handler) registerTagRoutes(r chi.Router) {
	r.Get("/api/v1/tags", h.ListTags)
	r.Post("/api/v1/tags", h.CreateTag)
	r.Get("/api/v1/tags/{id}", h.GetTag)
	r.Patch("/api/v1/tags/{id}", h.UpdateTag)
	r.Delete("/api/v1/tags/{id}", h.DeleteTag)
	r.Post("/api/v1/tags/{id}/merge", h.MergeTag)
	r.Put("/api/v1/cis/{id}/tags", h.ReplaceTags)
}

func tagOrg(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return t, false
	}
	return t, true
}

// ListTags handles GET /api/v1/tags: the catalog sorted by key, with limit
// (at most 200) and the signed keyset cursor (API-02).
func (h *Handler) ListTags(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	cursorErr := page.CursorError
	if cursorErr == nil && page.Cursor != nil {
		_, _, cursorErr = tagPosition(page.Cursor)
	}
	if cursorErr != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
		return
	}
	rows, total, err := h.svc.ListTags(r.Context(), t.OrganizationID, page)
	if err != nil {
		writeTagError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, tagPage(rows, total, page))
}

// GetTag handles GET /api/v1/tags/{id}.
func (h *Handler) GetTag(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	tag, err := h.svc.GetTag(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeTagError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, tag)
}

// CreateTag handles POST /api/v1/tags (201).
func (h *Handler) CreateTag(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	var req CreateTagRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	tag, err := h.svc.CreateTag(r.Context(), t.OrganizationID, &req)
	if err != nil {
		writeTagError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, tag)
}

// UpdateTag handles PATCH /api/v1/tags/{id}.
func (h *Handler) UpdateTag(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	var req UpdateTagRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	tag, err := h.svc.UpdateTag(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeTagError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, tag)
}

// DeleteTag handles DELETE /api/v1/tags/{id} (204).
func (h *Handler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteTag(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeTagError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MergeTag handles POST /api/v1/tags/{id}/merge: the CIs of the tag get
// the target tag, the tag is deleted.
func (h *Handler) MergeTag(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	var req MergeTagRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	result, err := h.svc.MergeTag(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req.IntoID)
	if err != nil {
		writeTagError(w, err)
		return
	}
	if h.dispatcher != nil {
		for _, item := range result.CIs {
			h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
		}
	}
	api.WriteJSON(w, http.StatusOK, result)
}

// ReplaceTags handles PUT /api/v1/cis/{id}/tags: replaces the tags of the
// CI (If-Match, API-07) and answers the CI with its new ETag.
func (h *Handler) ReplaceTags(w http.ResponseWriter, r *http.Request) {
	t, ok := tagOrg(w, r)
	if !ok {
		return
	}
	var req ReplaceTagsRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return
	}
	var ifMatch *int64
	if present {
		ifMatch = &version
	}
	item, err := h.svc.ReplaceCITags(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req.Tags, ifMatch)
	if err != nil {
		writeTagError(w, err)
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, http.StatusOK, item)
}
