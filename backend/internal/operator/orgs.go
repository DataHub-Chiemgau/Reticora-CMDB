package operator

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// NewOrganization is an operator's request to create an organization with
// plan and limits (TLC-02). Reseller creation is phase 4.
type NewOrganization struct {
	Name string `json:"name"`
	// Slug is optional; empty derives it from the name.
	Slug string `json:"slug,omitempty"`
	Plan string `json:"plan"`
	// Limits are the quotas (max_cis, max_collectors, max_users,
	// max_api_keys) stored in the limits of their feature's entitlement.
	Limits           map[string]int64 `json:"limits,omitempty"`
	AdminEmail       string           `json:"admin_email"`
	AdminDisplayName string           `json:"admin_display_name,omitempty"`
}

// CreatedOrganization is the result of an operator creation.
type CreatedOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Plan string `json:"plan"`
	// AdminInvited reports whether the first org_admin received an IdP
	// account with an invitation to set the password; otherwise the
	// account is provisioned in the IdP by hand and linked on first login.
	AdminInvited bool      `json:"admin_invited"`
	CreatedAt    time.Time `json:"created_at"`
}

// OrgCreator creates organizations with the creation chain of the
// self-signup (internal/signup) and records the operator in the same
// transaction.
type OrgCreator interface {
	CreateOrganization(ctx context.Context, op Identity, req *NewOrganization) (CreatedOrganization, error)
}

// ErrCreationUnavailable is returned by an OrgCreator without a database.
var ErrCreationUnavailable = errors.New("organization creation is not available")

// ErrSlugTaken is returned by an OrgCreator when a requested slug is used.
var ErrSlugTaken = errors.New("the organization slug is already taken")

// WithOrgCreator enables POST /api/v1/admin/orgs.
func (h *Handler) WithOrgCreator(c OrgCreator) *Handler {
	h.orgs = c
	return h
}

// CreateOrg creates an organization with plan and limits, its default VRF
// and its first org_admin (TLC-02). The creation and its operator_audit
// entry are atomic.
func (h *Handler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgs == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "organization creation is not configured")
		return
	}
	var req NewOrganization
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	op, _ := FromContext(r.Context())
	created, err := h.orgs.CreateOrganization(r.Context(), op, &req)
	switch {
	case errors.Is(err, ErrCreationUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
	case errors.Is(err, ErrSlugTaken):
		api.WriteFieldError(w, &api.ConflictError{Field: "slug", Message: err.Error()})
	case err != nil && api.WriteFieldError(w, err):
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusCreated, created)
	}
}
