package user

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrServiceAccountNotFound is returned for an unknown or foreign service
// account.
var ErrServiceAccountNotFound = errors.New("service account not found")

// ErrServiceAccountInUse is returned when a service account that API keys or
// webhook subscriptions are bound to is deleted.
var ErrServiceAccountInUse = errors.New("service account is bound to API keys or webhook subscriptions")

// RoleCeilingError is returned when a role assignment change exceeds the
// caller's rights (RBA-08, RBA-03): Missing are the permissions the caller
// does not hold in a scope covering the assignment's scope; Removed is set
// when an existing assignment the new list drops is not covered.
type RoleCeilingError struct {
	Missing []identity.Permission
	Removed *ServiceAccountRole
}

func (e *RoleCeilingError) Error() string {
	perms := make([]string, len(e.Missing))
	for i, p := range e.Missing {
		perms[i] = string(p)
	}
	if e.Removed != nil {
		return "removing the assignment of role " + e.Removed.RoleID + e.Removed.scopeText() +
			" requires " + strings.Join(perms, ", ") + " in its scope"
	}
	return "the role assignments would grant " + strings.Join(perms, ", ") + " beyond the caller's rights"
}

// ServiceAccount is a non-human principal of an organization (RBA-08). It
// holds role assignments like a user; API keys and webhook subscriptions
// bound to it act with its rights and scopes.
type ServiceAccount struct {
	ID             string               `json:"id"`
	OrganizationID string               `json:"organization_id"`
	Name           string               `json:"name"`
	Description    string               `json:"description"`
	IsActive       bool                 `json:"is_active"`
	CreatedBy      string               `json:"created_by,omitempty"`
	Roles          []ServiceAccountRole `json:"roles"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// ServiceAccountRole is a role assignment of a service account, optionally
// limited to a client or a site like a user's role assignment (RBA-03).
type ServiceAccountRole struct {
	RoleID        string  `json:"role_id"`
	ScopeClientID *string `json:"scope_client_id,omitempty"`
	ScopeSiteID   *string `json:"scope_site_id,omitempty"`
}

func (r *ServiceAccountRole) scopeText() string {
	switch {
	case r.ScopeSiteID != nil:
		return " in site " + *r.ScopeSiteID
	case r.ScopeClientID != nil:
		return " in client " + *r.ScopeClientID
	}
	return " organization-wide"
}

// sameAssignment reports whether a and b assign the same role in the same
// scope.
func sameAssignment(a, b ServiceAccountRole) bool {
	eq := func(x, y *string) bool {
		return (x == nil) == (y == nil) && (x == nil || strings.EqualFold(*x, *y))
	}
	return strings.EqualFold(a.RoleID, b.RoleID) && eq(a.ScopeClientID, b.ScopeClientID) && eq(a.ScopeSiteID, b.ScopeSiteID)
}

// ServiceAccountUpdate is a partial update.
type ServiceAccountUpdate struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IsActive    *bool   `json:"is_active"`
}

// ServiceAccountRepository persists service accounts and resolves their
// rights.
type ServiceAccountRepository interface {
	ListServiceAccounts(ctx context.Context, orgID string) ([]ServiceAccount, error)
	GetServiceAccount(ctx context.Context, orgID, id string) (*ServiceAccount, error)
	CreateServiceAccount(ctx context.Context, sa *ServiceAccount) error
	UpdateServiceAccount(ctx context.Context, orgID, id string, req ServiceAccountUpdate) (*ServiceAccount, error)
	DeleteServiceAccount(ctx context.Context, orgID, id string) error
	// SetServiceAccountRoles replaces the assignments on behalf of caller;
	// every new and every removed assignment must lie within the caller's
	// rights (RoleCeilingError).
	SetServiceAccountRoles(ctx context.Context, orgID, id string, caller *identity.Principal, roles []ServiceAccountRole) (*ServiceAccount, error)
	// ServiceAccountGrants returns the role grants of an active service
	// account; an inactive or unknown account has none.
	ServiceAccountGrants(ctx context.Context, orgID, id string) ([]identity.Grant, error)
}

// CanReadEvent reports whether the service account may read every object of
// an event with the given permission; it is the filter of webhook deliveries
// (RBA-08). objects holds the client and site of each object: "" is NULL,
// a shared row that client- and site-scoped readers read as well (RLS).
// Without objects the scope of the event is unknown (webhook/scope.go):
// only an account that holds the permission org-wide may read it.
func CanReadEvent(ctx context.Context, repo ServiceAccountRepository, orgID, serviceAccountID, permission string, objects []webhook.ObjectScope) (bool, error) {
	grants, err := repo.ServiceAccountGrants(ctx, orgID, serviceAccountID)
	if err != nil {
		return false, err
	}
	access := identity.ResolveAccess(grants)
	p := identity.Principal{OrganizationID: orgID, Permissions: access.Permissions, Scope: &access.Scope, PermissionScopes: access.PermissionScopes}
	scope, ok := p.ScopeFor(identity.Permission(permission))
	if !ok {
		return false, nil
	}
	if len(objects) == 0 {
		return scope.OrgWide(), nil
	}
	inScope := func(set identity.ScopeSet, id string) bool {
		return id == "" || set.All || slices.ContainsFunc(set.IDs, func(s string) bool { return strings.EqualFold(s, id) })
	}
	for _, o := range objects {
		if !inScope(scope.Clients, o.ClientID) || !inScope(scope.Sites, o.SiteID) {
			return false, nil
		}
	}
	return true, nil
}

// ServiceAccountAccess adapts a repository to the webhook dispatcher and the
// API key service.
type ServiceAccountAccess struct{ Repo ServiceAccountRepository }

// CanReadEvent implements webhook.SubscriberAccess.
func (a ServiceAccountAccess) CanReadEvent(ctx context.Context, orgID, serviceAccountID, permission string, objects []webhook.ObjectScope) (bool, error) {
	return CanReadEvent(ctx, a.Repo, orgID, serviceAccountID, permission, objects)
}

// ServiceAccountGrants implements identity.ServiceAccountAccessResolver.
func (a ServiceAccountAccess) ServiceAccountGrants(ctx context.Context, orgID, id string) ([]identity.Grant, error) {
	return a.Repo.ServiceAccountGrants(ctx, orgID, id)
}

// ServiceAccountState implements identity.ServiceAccountKeyResolver.
func (a ServiceAccountAccess) ServiceAccountState(ctx context.Context, orgID, id string) (exists, active bool, err error) {
	sa, err := a.Repo.GetServiceAccount(ctx, orgID, id)
	if errors.Is(err, ErrServiceAccountNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, sa.IsActive, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidPattern.MatchString(s) }

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func validServiceAccountName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	return nil
}

// PGServiceAccounts is the PostgreSQL service account repository.
type PGServiceAccounts struct {
	pool *pgxpool.Pool
}

// NewPGServiceAccounts creates the repository.
func NewPGServiceAccounts(pool *pgxpool.Pool) *PGServiceAccounts {
	return &PGServiceAccounts{pool: pool}
}

func recordServiceAccountAudit(ctx context.Context, tx pgx.Tx, orgID, id, action string, changes map[string]any) error {
	actor := tenant.FromContext(ctx).UserID
	actorType := "user"
	if actor == "" {
		actorType = "system"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID, ActorID: actor, ActorType: actorType, Action: action,
		ResourceType: "service_account", ResourceID: id, Changes: changes,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

const serviceAccountColumns = `id::text, organization_id::text, name, description, is_active,
	COALESCE(created_by::text, ''), created_at, updated_at`

func scanServiceAccount(row pgx.Row) (*ServiceAccount, error) {
	var sa ServiceAccount
	if err := row.Scan(&sa.ID, &sa.OrganizationID, &sa.Name, &sa.Description, &sa.IsActive, &sa.CreatedBy, &sa.CreatedAt, &sa.UpdatedAt); err != nil {
		return nil, err
	}
	sa.Roles = []ServiceAccountRole{}
	return &sa, nil
}

func loadServiceAccountRoles(ctx context.Context, tx pgx.Tx, sa *ServiceAccount) error {
	rows, err := tx.Query(ctx, `SELECT role_id::text, scope_client_id::text, scope_site_id::text
		FROM service_account_role WHERE service_account_id = $1 ORDER BY created_at, id`, sa.ID)
	if err != nil {
		return fmt.Errorf("list service account roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ServiceAccountRole
		if err := rows.Scan(&r.RoleID, &r.ScopeClientID, &r.ScopeSiteID); err != nil {
			return fmt.Errorf("scan service account role: %w", err)
		}
		sa.Roles = append(sa.Roles, r)
	}
	return rows.Err()
}

func getServiceAccountTx(ctx context.Context, tx pgx.Tx, orgID, id string, lock bool) (*ServiceAccount, error) {
	if !isUUID(id) {
		return nil, ErrServiceAccountNotFound
	}
	q := `SELECT ` + serviceAccountColumns + ` FROM service_account WHERE organization_id = $1 AND id = $2`
	if lock {
		q += ` FOR UPDATE`
	}
	sa, err := scanServiceAccount(tx.QueryRow(ctx, q, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrServiceAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read service account: %w", err)
	}
	return sa, loadServiceAccountRoles(ctx, tx, sa)
}

// ListServiceAccounts implements ServiceAccountRepository.
func (r *PGServiceAccounts) ListServiceAccounts(ctx context.Context, orgID string) ([]ServiceAccount, error) {
	out := []ServiceAccount{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+serviceAccountColumns+` FROM service_account WHERE organization_id = $1 ORDER BY name`, orgID)
		if err != nil {
			return fmt.Errorf("list service accounts: %w", err)
		}
		var accounts []*ServiceAccount
		for rows.Next() {
			sa, scanErr := scanServiceAccount(rows)
			if scanErr != nil {
				rows.Close()
				return fmt.Errorf("scan service account: %w", scanErr)
			}
			accounts = append(accounts, sa)
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		for _, sa := range accounts {
			if loadErr := loadServiceAccountRoles(ctx, tx, sa); loadErr != nil {
				return loadErr
			}
			out = append(out, *sa)
		}
		return nil
	})
	return out, err
}

// GetServiceAccount implements ServiceAccountRepository.
func (r *PGServiceAccounts) GetServiceAccount(ctx context.Context, orgID, id string) (*ServiceAccount, error) {
	var sa *ServiceAccount
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		sa, err = getServiceAccountTx(ctx, tx, orgID, id, false)
		return err
	})
	return sa, err
}

// CreateServiceAccount implements ServiceAccountRepository.
func (r *PGServiceAccounts) CreateServiceAccount(ctx context.Context, sa *ServiceAccount) error {
	if err := validServiceAccountName(sa.Name); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, sa.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := scanServiceAccount(tx.QueryRow(ctx, `
			INSERT INTO service_account (organization_id, name, description, created_by)
			VALUES ($1, $2, $3, NULLIF($4, '')::uuid)
			RETURNING `+serviceAccountColumns, sa.OrganizationID, strings.TrimSpace(sa.Name), sa.Description, sa.CreatedBy))
		if err != nil {
			return fmt.Errorf("insert service account: %w", err)
		}
		*sa = *created
		return recordServiceAccountAudit(ctx, tx, sa.OrganizationID, sa.ID, "service_account.created", map[string]any{"name": sa.Name})
	})
}

// UpdateServiceAccount implements ServiceAccountRepository.
func (r *PGServiceAccounts) UpdateServiceAccount(ctx context.Context, orgID, id string, req ServiceAccountUpdate) (*ServiceAccount, error) {
	var sa *ServiceAccount
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := getServiceAccountTx(ctx, tx, orgID, id, true)
		if err != nil {
			return err
		}
		changes := map[string]any{}
		if req.Name != nil {
			if nameErr := validServiceAccountName(*req.Name); nameErr != nil {
				return nameErr
			}
			current.Name, changes["name"] = strings.TrimSpace(*req.Name), strings.TrimSpace(*req.Name)
		}
		if req.Description != nil {
			current.Description, changes["description"] = *req.Description, *req.Description
		}
		if req.IsActive != nil {
			current.IsActive, changes["is_active"] = *req.IsActive, *req.IsActive
		}
		if sa, err = scanServiceAccount(tx.QueryRow(ctx, `UPDATE service_account SET name = $3, description = $4, is_active = $5
			WHERE organization_id = $1 AND id = $2 RETURNING `+serviceAccountColumns,
			orgID, id, current.Name, current.Description, current.IsActive)); err != nil {
			return fmt.Errorf("update service account: %w", err)
		}
		if loadErr := loadServiceAccountRoles(ctx, tx, sa); loadErr != nil {
			return loadErr
		}
		return recordServiceAccountAudit(ctx, tx, orgID, id, "service_account.updated", changes)
	})
	return sa, err
}

// DeleteServiceAccount implements ServiceAccountRepository. An account that
// keys or subscriptions are bound to is not deleted.
func (r *PGServiceAccounts) DeleteServiceAccount(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sa, err := getServiceAccountTx(ctx, tx, orgID, id, true)
		if err != nil {
			return err
		}
		var bound bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_key WHERE service_account_id = $1)
			OR EXISTS (SELECT 1 FROM webhook_subscription WHERE service_account_id = $1)`, id).Scan(&bound); err != nil {
			return fmt.Errorf("check service account bindings: %w", err)
		}
		if bound {
			return ErrServiceAccountInUse
		}
		if _, err = tx.Exec(ctx, `DELETE FROM service_account WHERE organization_id = $1 AND id = $2`, orgID, id); err != nil {
			return fmt.Errorf("delete service account: %w", err)
		}
		return recordServiceAccountAudit(ctx, tx, orgID, id, "service_account.deleted", map[string]any{"name": sa.Name})
	})
}

// SetServiceAccountRoles replaces the role assignments of an account. Like a
// user's role assignment it never grants more than the caller holds
// (RBA-08, RBA-03): every assignment of the new list and every existing
// assignment the list drops must be covered by the caller's rights
// (identity.GrantCeiling); otherwise nothing changes and nothing is audited.
func (r *PGServiceAccounts) SetServiceAccountRoles(ctx context.Context, orgID, id string, caller *identity.Principal, roles []ServiceAccountRole) (*ServiceAccount, error) {
	var sa *ServiceAccount
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := getServiceAccountTx(ctx, tx, orgID, id, true)
		if err != nil {
			return err
		}
		if ceilingErr := checkRoleCeiling(ctx, tx, orgID, caller, current.Roles, roles); ceilingErr != nil {
			return ceilingErr
		}
		if _, err = tx.Exec(ctx, `DELETE FROM service_account_role WHERE service_account_id = $1`, id); err != nil {
			return fmt.Errorf("clear service account roles: %w", err)
		}
		for _, role := range roles {
			if _, err = tx.Exec(ctx, `INSERT INTO service_account_role (organization_id, service_account_id, role_id, scope_client_id, scope_site_id)
				SELECT $1, $2, ro.id, $4, $5 FROM role ro WHERE ro.organization_id = $1 AND ro.id = $3
				ON CONFLICT DO NOTHING`, orgID, id, role.RoleID, role.ScopeClientID, role.ScopeSiteID); err != nil {
				return fmt.Errorf("assign service account role: %w", err)
			}
		}
		if sa, err = getServiceAccountTx(ctx, tx, orgID, id, false); err != nil {
			return err
		}
		if len(sa.Roles) != len(roles) {
			return errors.New("unknown role in assignments")
		}
		return recordServiceAccountAudit(ctx, tx, orgID, id, "service_account.roles_changed", map[string]any{"roles": sa.Roles})
	})
	return sa, err
}

// checkRoleCeiling returns a RoleCeilingError when caller does not cover an
// assignment of next or an assignment of current missing from next. An
// assignment of a client- or site-scoped role without that scope grants
// nothing at use, but is checked as organization-wide (fail-closed).
func checkRoleCeiling(ctx context.Context, tx pgx.Tx, orgID string, caller *identity.Principal, current, next []ServiceAccountRole) error {
	var missing []identity.Permission
	for _, role := range next {
		grant, err := assignmentGrant(ctx, tx, orgID, role)
		if err != nil {
			return err
		}
		for _, perm := range identity.GrantCeiling(caller, []identity.Grant{grant}) {
			if !slices.Contains(missing, perm) {
				missing = append(missing, perm)
			}
		}
	}
	if len(missing) > 0 {
		return &RoleCeilingError{Missing: missing}
	}
	for _, old := range current {
		if slices.ContainsFunc(next, func(r ServiceAccountRole) bool { return sameAssignment(r, old) }) {
			continue
		}
		grant, err := assignmentGrant(ctx, tx, orgID, old)
		if err != nil {
			return err
		}
		if m := identity.GrantCeiling(caller, []identity.Grant{grant}); len(m) > 0 {
			return &RoleCeilingError{Missing: m, Removed: &old}
		}
	}
	return nil
}

// assignmentGrant is the grant of one assignment: the catalog permissions of
// the role (as ServiceAccountGrants resolves them) in the assignment's scope.
// An unknown role grants nothing; the assignment itself is then refused.
func assignmentGrant(ctx context.Context, tx pgx.Tx, orgID string, role ServiceAccountRole) (identity.Grant, error) {
	grant := identity.Grant{}
	if role.ScopeClientID != nil {
		grant.Clients = []string{*role.ScopeClientID}
	}
	if role.ScopeSiteID != nil {
		grant.Sites = []string{*role.ScopeSiteID}
	}
	if !isUUID(role.RoleID) {
		return grant, nil
	}
	var keys []string
	err := tx.QueryRow(ctx, `
		SELECT ARRAY(SELECT DISTINCT k FROM (
		           SELECT rp.permission_key AS k FROM role_permission rp
		            WHERE rp.role_id = ro.id AND rp.organization_id = ro.organization_id
		           UNION SELECT jsonb_array_elements_text(COALESCE(ro.permissions, '[]'::jsonb))) keys
		         WHERE k IN (SELECT key FROM permission) ORDER BY k)
		  FROM role ro WHERE ro.organization_id = $1 AND ro.id = $2`, orgID, role.RoleID).Scan(&keys)
	if errors.Is(err, pgx.ErrNoRows) {
		return grant, nil
	}
	if err != nil {
		return grant, fmt.Errorf("read role permissions: %w", err)
	}
	for _, k := range keys {
		grant.Permissions = append(grant.Permissions, identity.Permission(k))
	}
	return grant, nil
}

// ServiceAccountGrants implements ServiceAccountRepository. It runs on behalf
// of the account's organization, without a principal scope (E-08), like the
// grant resolution of a user at login.
func (r *PGServiceAccounts) ServiceAccountGrants(ctx context.Context, orgID, id string) ([]identity.Grant, error) {
	grants := []identity.Grant{}
	if !isUUID(id) {
		return grants, nil
	}
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT sar.scope_client_id::text, sar.scope_site_id::text,
			       ARRAY(SELECT DISTINCT k FROM (
			           SELECT rp.permission_key AS k FROM role_permission rp
			            WHERE rp.role_id = sar.role_id AND rp.organization_id = sar.organization_id
			           UNION SELECT jsonb_array_elements_text(COALESCE(ro.permissions, '[]'::jsonb))) keys
			         WHERE k IN (SELECT key FROM permission) ORDER BY k)
			  FROM service_account_role sar
			  JOIN service_account sa ON sa.id = sar.service_account_id AND sa.is_active
			  JOIN role ro ON ro.id = sar.role_id AND ro.organization_id = sar.organization_id
			 WHERE sar.organization_id = $1 AND sar.service_account_id = $2
			   AND (ro.scope <> 'client' OR sar.scope_client_id IS NOT NULL)
			   AND (ro.scope <> 'site' OR sar.scope_site_id IS NOT NULL)`, orgID, id)
		if err != nil {
			return fmt.Errorf("list service account grants: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var clientID, siteID *string
			var keys []string
			if err := rows.Scan(&clientID, &siteID, &keys); err != nil {
				return fmt.Errorf("scan service account grant: %w", err)
			}
			grant := identity.Grant{}
			for _, k := range keys {
				grant.Permissions = append(grant.Permissions, identity.Permission(k))
			}
			if clientID != nil {
				grant.Clients = []string{*clientID}
			}
			if siteID != nil {
				grant.Sites = []string{*siteID}
			}
			grants = append(grants, grant)
		}
		return rows.Err()
	})
	return grants, err
}

// MemoryServiceAccounts keeps service accounts in memory (tests, --no-db).
// Role permissions are not known here, so accounts hold no grants.
type MemoryServiceAccounts struct {
	mu       sync.Mutex
	accounts []*ServiceAccount
}

// NewMemoryServiceAccounts creates an empty repository.
func NewMemoryServiceAccounts() *MemoryServiceAccounts { return &MemoryServiceAccounts{} }

func (m *MemoryServiceAccounts) find(orgID, id string) *ServiceAccount {
	for _, sa := range m.accounts {
		if sa.OrganizationID == orgID && sa.ID == id {
			return sa
		}
	}
	return nil
}

// ListServiceAccounts implements ServiceAccountRepository.
func (m *MemoryServiceAccounts) ListServiceAccounts(_ context.Context, orgID string) ([]ServiceAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ServiceAccount{}
	for _, sa := range m.accounts {
		if sa.OrganizationID == orgID {
			out = append(out, *sa)
		}
	}
	return out, nil
}

// GetServiceAccount implements ServiceAccountRepository.
func (m *MemoryServiceAccounts) GetServiceAccount(_ context.Context, orgID, id string) (*ServiceAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sa := m.find(orgID, id); sa != nil {
		c := *sa
		return &c, nil
	}
	return nil, ErrServiceAccountNotFound
}

// CreateServiceAccount implements ServiceAccountRepository.
func (m *MemoryServiceAccounts) CreateServiceAccount(_ context.Context, sa *ServiceAccount) error {
	if err := validServiceAccountName(sa.Name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	sa.ID, sa.IsActive, sa.CreatedAt, sa.UpdatedAt, sa.Roles = newUUID(), true, now, now, []ServiceAccountRole{}
	c := *sa
	m.accounts = append(m.accounts, &c)
	return nil
}

// UpdateServiceAccount implements ServiceAccountRepository.
func (m *MemoryServiceAccounts) UpdateServiceAccount(_ context.Context, orgID, id string, req ServiceAccountUpdate) (*ServiceAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sa := m.find(orgID, id)
	if sa == nil {
		return nil, ErrServiceAccountNotFound
	}
	if req.Name != nil {
		if err := validServiceAccountName(*req.Name); err != nil {
			return nil, err
		}
		sa.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		sa.Description = *req.Description
	}
	if req.IsActive != nil {
		sa.IsActive = *req.IsActive
	}
	sa.UpdatedAt = time.Now().UTC()
	c := *sa
	return &c, nil
}

// DeleteServiceAccount implements ServiceAccountRepository.
func (m *MemoryServiceAccounts) DeleteServiceAccount(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, sa := range m.accounts {
		if sa.OrganizationID == orgID && sa.ID == id {
			m.accounts = append(m.accounts[:i], m.accounts[i+1:]...)
			return nil
		}
	}
	return ErrServiceAccountNotFound
}

// SetServiceAccountRoles implements ServiceAccountRepository. Role
// permissions are not known here and the assignments grant nothing, so there
// is no ceiling to check.
func (m *MemoryServiceAccounts) SetServiceAccountRoles(_ context.Context, orgID, id string, _ *identity.Principal, roles []ServiceAccountRole) (*ServiceAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sa := m.find(orgID, id)
	if sa == nil {
		return nil, ErrServiceAccountNotFound
	}
	sa.Roles = append([]ServiceAccountRole{}, roles...)
	c := *sa
	return &c, nil
}

// ServiceAccountGrants implements ServiceAccountRepository.
func (m *MemoryServiceAccounts) ServiceAccountGrants(context.Context, string, string) ([]identity.Grant, error) {
	return []identity.Grant{}, nil
}
