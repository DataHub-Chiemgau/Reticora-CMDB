package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// CodeAPIKeyInvalid: the API key that started the import is revoked,
// expired or deleted (AUT-04, WP-779); the run writes no row.
const CodeAPIKeyInvalid = "import-api-key-invalid"

// apiKeyCredential is the prefix of the credential of an API key principal
// (middleware.authenticateRequest: "apikey:<public prefix>").
const apiKeyCredential = "apikey:"

// StarterRef is the identity reference of the starter of an import in the
// job parameters (E-145): the principal type, the subject (the user, or
// the owner or service account of an API key; the creator of the job row),
// for an API key its public prefix as credential reference, and the
// correlation id of the request (its request id is in jobs.ScopeParams).
// It holds no rights, scopes or groups: the run reloads them from the
// reference when it begins (jobContext), so a revoked key or a withdrawn
// right takes effect at once and the job parameters, which every reader of
// the job sees, show none of them.
type StarterRef struct {
	Type          identity.PrincipalType `json:"type"`
	Subject       string                 `json:"subject"`
	KeyPrefix     string                 `json:"key_prefix,omitempty"`
	CorrelationID string                 `json:"correlation_id,omitempty"`
}

// captureStarter takes the identity reference of the principal of ctx;
// nil without principal. A credential reference is kept for API keys only:
// a session id identifies nothing a run could check again.
func captureStarter(ctx context.Context) *StarterRef {
	p, ok := identity.PrincipalFromContext(ctx)
	if !ok || p.Subject == "" {
		return nil
	}
	ref := &StarterRef{Type: p.Type, Subject: p.Subject}
	if p.Type == identity.PrincipalTypeAPIKey {
		if prefix, found := strings.CutPrefix(p.Credential, apiKeyCredential); found {
			ref.KeyPrefix = prefix
		}
	}
	if info, ok := audit.RequestInfoFrom(ctx); ok {
		ref.CorrelationID = info.CorrelationID
	}
	return ref
}

// KeyReloader identifies an API key by its public prefix again and returns
// the rights it grants now (identity.APIKeyService.Reload over
// identity.APIKeyStore.LookupByPrefix): identity.ErrAPIKeyInvalid for an
// unknown, revoked or expired key.
type KeyReloader interface {
	Reload(ctx context.Context, prefix string) (*identity.APIKeyInfo, error)
}

// WithAPIKeys lets runs started with an API key reload the key
// (jobContext); without it such runs fail with import-principal-missing.
func (h *ImportHandler) WithAPIKeys(keys KeyReloader) *ImportHandler {
	h.keys = keys
	return h
}

// starter is the reloaded principal of a run before the narrowing to the
// scope snapshot of the job row.
type starter struct {
	access           identity.Access
	credential       string
	serviceAccountID string
}

// reloadStarter reloads the rights of the starter a job references:
// a user from their status and current role assignments (WP-775), an API
// key from the key store with the rights it grants now (AUT-04: the
// intersection of the key's and its owner's current rights, a key bound to
// a service account within its creator's scope, WP-668). A reference that
// does not match the job row, a type without reload and missing sources
// are import-principal-missing; a revoked or expired key is
// import-api-key-invalid.
func (h *ImportHandler) reloadStarter(ctx context.Context, job *jobs.Job, ref *StarterRef) (*starter, error) {
	missing := &ParseError{Code: CodePrincipalMissing, Message: "the principal who started the import cannot be restored", Fatal: true}
	if ref == nil || job.CreatedBy == "" || !strings.EqualFold(ref.Subject, job.CreatedBy) {
		return nil, missing
	}
	switch ref.Type {
	case identity.PrincipalTypeUser:
		if h.users == nil || h.grants == nil {
			return nil, missing
		}
		active, err := h.users.UserActive(ctx, job.OrganizationID, job.CreatedBy)
		if err != nil {
			return nil, fmt.Errorf("read the status of the user who started the import: %w", err)
		}
		if !active {
			return nil, missing
		}
		grants, err := h.grants.AccessGrants(ctx, job.OrganizationID, job.CreatedBy)
		if err != nil {
			return nil, fmt.Errorf("read the role assignments of the user who started the import: %w", err)
		}
		return &starter{access: identity.ResolveAccess(grants)}, nil
	case identity.PrincipalTypeAPIKey:
		if h.keys == nil || ref.KeyPrefix == "" {
			return nil, missing
		}
		info, err := h.keys.Reload(ctx, ref.KeyPrefix)
		if errors.Is(err, identity.ErrAPIKeyInvalid) {
			return nil, &ParseError{Code: CodeAPIKeyInvalid, Message: "the API key that started the import is revoked or expired", Fatal: true}
		}
		if err != nil {
			return nil, fmt.Errorf("reload the API key that started the import: %w", err)
		}
		// The key must still act as the starter of the job, in its
		// organization, with resolved owner rights (fail-closed).
		if info == nil || info.OrganizationID != job.OrganizationID || !strings.EqualFold(info.OwnerID, job.CreatedBy) || info.Scope == nil {
			return nil, missing
		}
		return &starter{access: identity.Access{Permissions: info.Scopes, Scope: *info.Scope, PermissionScopes: info.PermissionScopes},
			credential: apiKeyCredential + info.ID, serviceAccountID: info.ServiceAccountID}, nil
	}
	return nil, missing
}

// checkAPIAccess checks the feature api_access of a run started with an
// API key, as every request with a key needs it (WP-471); a failed lookup
// is not entitled (fail-closed). Without org gate it passes like the
// feature checks of checkGates.
func (h *ImportHandler) checkAPIAccess(ctx context.Context, orgID string) error {
	if h.features == nil {
		return nil
	}
	_, status, err := h.features.Status(ctx, orgID, entitlement.FeatureAPIAccess)
	if err != nil || status != entitlement.StatusActive {
		return fatalCode(CodeFeatureNotEntitled, "the organization is not entitled to the feature %s", entitlement.FeatureAPIAccess)
	}
	return nil
}
