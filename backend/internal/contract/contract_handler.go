package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// ContractHandler serves /api/v1/contracts (MGT-12, API-06, WP-269).
// Reading needs contract:read, writing contract:write (server/authz.go);
// the routes belong to the module inventory and the feature assets_basic.
// Answers carry only the fields the field permissions let the principal
// read (object kind contract, WP-265); a write of a field it may not edit
// is 403 with the fields.
type ContractHandler struct {
	contracts ContractRepository
}

// NewContractHandler returns the handler; without a repository every
// route answers 503.
func NewContractHandler(contracts ContractRepository) *ContractHandler {
	return &ContractHandler{contracts: contracts}
}

// RegisterRoutes registers the contract routes.
func (h *ContractHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/contracts", h.ListContracts)
	r.Post("/api/v1/contracts", h.CreateContract)
	r.Get("/api/v1/contracts/{id}", h.GetContract)
	r.Patch("/api/v1/contracts/{id}", h.UpdateContract)
	r.Delete("/api/v1/contracts/{id}", h.DeleteContract)
}

// store returns the repository, the organization and the field policy of
// the request, or answers 401, 503 or 500 (policy not resolvable,
// fail-closed).
func (h *ContractHandler) store(w http.ResponseWriter, r *http.Request) (ContractRepository, string, *permission.FieldPolicy, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return nil, "", nil, false
	}
	if h.contracts == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "contracts are not available")
		return nil, "", nil, false
	}
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return nil, "", nil, false
	}
	return h.contracts, t.OrganizationID, policy, true
}

// writeContractError reports a failed contract request (RFC 7807).
func writeContractError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrContractNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrContractNotFound.Error())
	case errors.Is(err, ErrPreconditionRequired):
		api.WritePreconditionFailed(w, ErrPreconditionRequired.Error())
	case errors.Is(err, ErrContractChanged):
		api.WritePreconditionFailed(w, ErrContractChanged.Error())
	case errors.Is(err, errCursorOrder):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", errCursorOrder.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// contractRef is the object reference of a contract for its field
// permissions: the contract and its client (no site).
func contractRef(c *Contract) permission.ObjectRef {
	ref := permission.ObjectRef{ID: c.ID}
	if c.ClientID != nil {
		ref.ClientID = *c.ClientID
	}
	return ref
}

// project returns the contract as the policy lets the principal read it:
// the contract itself without a restriction.
func project(policy *permission.FieldPolicy, c *Contract) (any, error) {
	return permission.ProjectValue(policy, permission.KindContract, "", contractRef(c), c)
}

// writeContract answers the projected contract with its version as ETag;
// the ETag does not depend on the visible fields.
func writeContract(w http.ResponseWriter, status int, policy *permission.FieldPolicy, c *Contract) {
	body, err := project(policy, c)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(c.Version))
	api.WriteJSON(w, status, body)
}

// checkUpdate returns the check of a PATCH against the field permissions
// (WP-265): every sent field the principal may not read counts, whatever
// its value (no equality oracle), and every readable field whose value
// changes; each must be editable.
func checkUpdate(policy *permission.FieldPolicy, req *UpdateContractRequest) func(*Contract) error {
	return func(current *Contract) error {
		if !policy.Restricted(permission.KindContract) {
			return nil
		}
		ref := contractRef(current)
		next := *current
		req.Apply(&next)
		before, err := fieldValues(current)
		if err != nil {
			return err
		}
		after, err := fieldValues(&next)
		if err != nil {
			return err
		}
		var changed []string
		for _, path := range req.SentFields() {
			if !permission.Readable(policy, permission.KindContract, "", ref, path) || !bytes.Equal(before[path], after[path]) {
				changed = append(changed, path)
			}
		}
		return permission.CheckWrite(policy, permission.KindContract, "", ref, changed)
	}
}

// fieldValues encodes the contract as its DTO members.
func fieldValues(c *Contract) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if decodeErr := json.Unmarshal(raw, &out); decodeErr != nil {
		return nil, decodeErr
	}
	return out, nil
}

// listFieldPaths are the DTO paths the list query reads: its filters and a
// sort other than number and created_at.
func listFieldPaths(q *api.ListQuery) []string {
	var paths []string
	for _, f := range q.Filters {
		paths = append(paths, f.Field)
	}
	if q.SortBy == "start_date" || q.SortBy == "end_date" {
		paths = append(paths, q.SortBy)
	}
	return paths
}

// ListContracts handles GET /api/v1/contracts: filter[status],
// filter[kind], filter[supplier_id], filter[client_id],
// filter[end_date][gte|lte], sort number, start_date, end_date or
// created_at (a leading - descends), limit and the signed keyset cursor
// (API-02). A filter or sort on a field the principal cannot read in every
// scope is 403 with the fields.
func (h *ContractHandler) ListContracts(w http.ResponseWriter, r *http.Request) {
	repo, orgID, policy, ok := h.store(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	q, err := api.ParseListQuery(r, ContractFields)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if filterErr := permission.CheckListFilter(policy, permission.KindContract, nil, listFieldPaths(&q)); filterErr != nil {
		api.WriteFieldError(w, filterErr)
		return
	}
	field, dir := contractOrder(&q)
	if page.Cursor != nil {
		if posErr := contractCursorPosition(page.Cursor, field, dir); posErr != nil {
			writeContractError(w, posErr)
			return
		}
	}
	rows, total, err := repo.ListContracts(r.Context(), orgID, &q, page)
	if err != nil {
		writeContractError(w, err)
		return
	}
	list := contractPage(rows, total, page, field, dir)
	if !permission.NeedsProjection(policy, permission.KindContract) {
		api.WriteJSON(w, http.StatusOK, list)
		return
	}
	data := make([]any, len(list.Data))
	for i := range list.Data {
		if data[i], err = project(policy, &list.Data[i]); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[any]{Data: data, Total: list.Total, Limit: list.Limit,
		Offset: list.Offset, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

// GetContract handles GET /api/v1/contracts/{id}.
func (h *ContractHandler) GetContract(w http.ResponseWriter, r *http.Request) {
	repo, orgID, policy, ok := h.store(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrContractNotFound.Error())
	if !ok {
		return
	}
	c, err := repo.GetContract(r.Context(), orgID, id)
	if err != nil {
		writeContractError(w, err)
		return
	}
	writeContract(w, http.StatusOK, policy, c)
}

// CreateContract handles POST /api/v1/contracts: every sent field must be
// editable for the principal (403 with the fields).
func (h *ContractHandler) CreateContract(w http.ResponseWriter, r *http.Request) {
	repo, orgID, policy, ok := h.store(w, r)
	if !ok {
		return
	}
	var req CreateContractRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	c := req.contract(orgID)
	if err := permission.CheckWrite(policy, permission.KindContract, "", contractRef(c), req.SentFields()); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if err := repo.CreateContract(r.Context(), c); err != nil {
		writeContractError(w, err)
		return
	}
	writeContract(w, http.StatusCreated, policy, c)
}

// UpdateContract handles PATCH /api/v1/contracts/{id} with If-Match
// (API-07): 412 when it is required but missing or names an older version;
// 403 with the fields for a field the principal may not edit.
func (h *ContractHandler) UpdateContract(w http.ResponseWriter, r *http.Request) {
	repo, orgID, policy, ok := h.store(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrContractNotFound.Error())
	if !ok {
		return
	}
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return
	}
	var req UpdateContractRequest
	if err = api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.ClientID.Set {
		api.WriteFieldError(w, &api.FieldError{Field: "client_id", Message: "cannot be changed; create a contract for the other client"})
		return
	}
	if present {
		req.IfMatch = &version
	}
	req.Check = checkUpdate(policy, &req)
	c, err := repo.UpdateContract(r.Context(), orgID, id, &req)
	if err != nil {
		writeContractError(w, err)
		return
	}
	writeContract(w, http.StatusOK, policy, c)
}

// DeleteContract handles DELETE /api/v1/contracts/{id}; its object grants
// go with it (trigger contract_object_grants).
func (h *ContractHandler) DeleteContract(w http.ResponseWriter, r *http.Request) {
	repo, orgID, _, ok := h.store(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrContractNotFound.Error())
	if !ok {
		return
	}
	if err := repo.DeleteContract(r.Context(), orgID, id); err != nil {
		writeContractError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
