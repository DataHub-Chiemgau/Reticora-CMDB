package user

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for users, teams, and roles.
type Repository interface {
	// Users
	ListUsers(ctx context.Context, orgID, search string, page api.PaginationParams) ([]User, int, error)
	GetUser(ctx context.Context, orgID, id string) (*User, error)
	CreateUser(ctx context.Context, u *User) error
	UpdateUser(ctx context.Context, orgID, id string, req UpdateUserRequest) (*User, error)
	DeleteUser(ctx context.Context, orgID, id string) error
	// AnonymizeUser replaces all personal data with surrogate values and
	// deactivates the account, keeping the row for referential integrity and
	// the audit chain (GDPR right to erasure).
	AnonymizeUser(ctx context.Context, orgID, id string) (*User, error)

	// Teams
	ListTeams(ctx context.Context, orgID, search string, page api.PaginationParams) ([]Team, int, error)
	GetTeam(ctx context.Context, orgID, id string) (*Team, error)
	CreateTeam(ctx context.Context, t *Team) error
	UpdateTeam(ctx context.Context, orgID, id string, req UpdateTeamRequest) (*Team, error)
	DeleteTeam(ctx context.Context, orgID, id string) error
	AddTeamMember(ctx context.Context, orgID string, m *TeamMember) error
	RemoveTeamMember(ctx context.Context, orgID, teamID, userID string) error
	ListTeamMembers(ctx context.Context, orgID, teamID string) ([]TeamMember, error)

	// Custom Roles
	ListRoles(ctx context.Context, orgID string, page api.PaginationParams) ([]CustomRole, int, error)
	GetRole(ctx context.Context, orgID, id string) (*CustomRole, error)
	CreateRole(ctx context.Context, r *CustomRole) error
	UpdateRole(ctx context.Context, orgID, id string, req UpdateRoleRequest) (*CustomRole, error)
	DeleteRole(ctx context.Context, orgID, id string) error
	AssignRole(ctx context.Context, orgID string, a *UserRoleAssignment) error
	ListUserRoles(ctx context.Context, orgID, userID string) ([]UserRoleAssignment, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu          sync.RWMutex
	users       map[string]*User
	teams       map[string]*Team
	members     map[string]*TeamMember
	roles       map[string]*CustomRole
	assignments map[string]*UserRoleAssignment
	nextID      int
}

// NewMemoryRepository creates a new in-memory user/team/role repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		users:       make(map[string]*User),
		teams:       make(map[string]*Team),
		members:     make(map[string]*TeamMember),
		roles:       make(map[string]*CustomRole),
		assignments: make(map[string]*UserRoleAssignment),
	}
}

// AgeForTesting backdates a stored user's updated_at timestamp so
// retention/erasure tests can create "expired" accounts without waiting.
func (r *MemoryRepository) AgeForTesting(id string, age time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.users[id]; ok {
		u.UpdatedAt = time.Now().UTC().Add(-age)
	}
}

func (r *MemoryRepository) nextIDStr(prefix string) string {
	r.nextID++
	return fmt.Sprintf("%s-%d", prefix, r.nextID)
}

// --- Users ---

func (r *MemoryRepository) ListUsers(_ context.Context, orgID, search string, page api.PaginationParams) ([]User, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []User
	for _, u := range r.users {
		if u.OrganizationID != orgID {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(u.DisplayName), strings.ToLower(search)) &&
			!strings.Contains(strings.ToLower(u.Email), strings.ToLower(search)) {
			continue
		}
		result = append(result, *u)
	}

	total := len(result)
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

func (r *MemoryRepository) GetUser(_ context.Context, orgID, id string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.users[id]
	if !ok || u.OrganizationID != orgID {
		return nil, fmt.Errorf("user not found")
	}
	return u, nil
}

func (r *MemoryRepository) CreateUser(_ context.Context, u *User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	u.ID = r.nextIDStr("user")
	now := time.Now().UTC()
	u.CreatedAt = now
	u.UpdatedAt = now
	r.users[u.ID] = u
	return nil
}

func (r *MemoryRepository) UpdateUser(_ context.Context, orgID, id string, req UpdateUserRequest) (*User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.users[id]
	if !ok || u.OrganizationID != orgID {
		return nil, fmt.Errorf("user not found")
	}
	if req.DisplayName != nil {
		u.DisplayName = *req.DisplayName
	}
	if req.Status != nil {
		u.Status = *req.Status
	}
	if req.AvatarURL != nil {
		u.AvatarURL = *req.AvatarURL
	}
	u.UpdatedAt = time.Now().UTC()
	return u, nil
}

func (r *MemoryRepository) DeleteUser(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.users[id]
	if !ok || u.OrganizationID != orgID {
		return fmt.Errorf("user not found")
	}
	delete(r.users, id)
	return nil
}

func (r *MemoryRepository) AnonymizeUser(_ context.Context, orgID, id string) (*User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.users[id]
	if !ok || u.OrganizationID != orgID {
		return nil, fmt.Errorf("user not found")
	}
	u.Email = SurrogateEmail(id)
	u.DisplayName = SurrogateDisplayName(id)
	u.AvatarURL = ""
	u.ExternalID = ""
	u.Status = "anonymized"
	u.UpdatedAt = time.Now().UTC()
	cp := *u
	return &cp, nil
}

// --- Teams ---

func (r *MemoryRepository) ListTeams(_ context.Context, orgID, search string, page api.PaginationParams) ([]Team, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Team
	for _, t := range r.teams {
		if t.OrganizationID != orgID {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(t.Name), strings.ToLower(search)) {
			continue
		}
		result = append(result, *t)
	}

	total := len(result)
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

func (r *MemoryRepository) GetTeam(_ context.Context, orgID, id string) (*Team, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.teams[id]
	if !ok || t.OrganizationID != orgID {
		return nil, fmt.Errorf("team not found")
	}
	return t, nil
}

func (r *MemoryRepository) CreateTeam(_ context.Context, t *Team) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t.ID = r.nextIDStr("team")
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	r.teams[t.ID] = t
	return nil
}

func (r *MemoryRepository) UpdateTeam(_ context.Context, orgID, id string, req UpdateTeamRequest) (*Team, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.teams[id]
	if !ok || t.OrganizationID != orgID {
		return nil, fmt.Errorf("team not found")
	}
	if req.Name != nil {
		t.Name = *req.Name
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	if req.LeadID != nil {
		t.LeadID = *req.LeadID
	}
	t.UpdatedAt = time.Now().UTC()
	return t, nil
}

func (r *MemoryRepository) DeleteTeam(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.teams[id]
	if !ok || t.OrganizationID != orgID {
		return fmt.Errorf("team not found")
	}
	delete(r.teams, id)
	// Remove team members
	for mid, m := range r.members {
		if m.TeamID == id {
			delete(r.members, mid)
		}
	}
	return nil
}

func (r *MemoryRepository) AddTeamMember(_ context.Context, orgID string, m *TeamMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.teams[m.TeamID]
	if !ok || t.OrganizationID != orgID {
		return fmt.Errorf("team not found")
	}

	m.ID = r.nextIDStr("member")
	m.JoinedAt = time.Now().UTC()
	r.members[m.ID] = m
	t.MemberCount++
	return nil
}

func (r *MemoryRepository) RemoveTeamMember(_ context.Context, orgID, teamID, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.teams[teamID]
	if !ok || t.OrganizationID != orgID {
		return fmt.Errorf("team not found")
	}

	for mid, m := range r.members {
		if m.TeamID == teamID && m.UserID == userID {
			delete(r.members, mid)
			t.MemberCount--
			return nil
		}
	}
	return fmt.Errorf("member not found")
}

func (r *MemoryRepository) ListTeamMembers(_ context.Context, orgID, teamID string) ([]TeamMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.teams[teamID]
	if !ok || t.OrganizationID != orgID {
		return nil, fmt.Errorf("team not found")
	}

	var result []TeamMember
	for _, m := range r.members {
		if m.TeamID == teamID {
			result = append(result, *m)
		}
	}
	return result, nil
}

// --- Custom Roles ---

func (r *MemoryRepository) ListRoles(_ context.Context, orgID string, page api.PaginationParams) ([]CustomRole, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []CustomRole
	for _, role := range r.roles {
		if role.OrganizationID != orgID {
			continue
		}
		result = append(result, *role)
	}

	total := len(result)
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

func (r *MemoryRepository) GetRole(_ context.Context, orgID, id string) (*CustomRole, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	role, ok := r.roles[id]
	if !ok || role.OrganizationID != orgID {
		return nil, fmt.Errorf("role not found")
	}
	return role, nil
}

func (r *MemoryRepository) CreateRole(_ context.Context, role *CustomRole) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	role.ID = r.nextIDStr("role")
	now := time.Now().UTC()
	role.CreatedAt = now
	role.UpdatedAt = now
	r.roles[role.ID] = role
	return nil
}

func (r *MemoryRepository) UpdateRole(_ context.Context, orgID, id string, req UpdateRoleRequest) (*CustomRole, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	role, ok := r.roles[id]
	if !ok || role.OrganizationID != orgID {
		return nil, fmt.Errorf("role not found")
	}
	if role.IsSystem {
		return nil, fmt.Errorf("cannot modify system role")
	}
	if req.Name != nil {
		role.Name = *req.Name
	}
	if req.Description != nil {
		role.Description = *req.Description
	}
	if req.Permissions != nil {
		role.Permissions = req.Permissions
	}
	role.UpdatedAt = time.Now().UTC()
	return role, nil
}

func (r *MemoryRepository) DeleteRole(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	role, ok := r.roles[id]
	if !ok || role.OrganizationID != orgID {
		return fmt.Errorf("role not found")
	}
	if role.IsSystem {
		return fmt.Errorf("cannot delete system role")
	}
	delete(r.roles, id)
	return nil
}

func (r *MemoryRepository) AssignRole(_ context.Context, orgID string, a *UserRoleAssignment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.RoleID != "" {
		role, ok := r.roles[a.RoleID]
		if !ok || role.OrganizationID != orgID {
			return fmt.Errorf("role not found")
		}
		a.ID = r.nextIDStr("assign")
		a.GrantedAt = time.Now().UTC()
		r.assignments[a.ID] = a
		return nil
	}

	// Verify role exists
	role, ok := r.roles[a.CustomRoleID]
	if !ok || role.OrganizationID != orgID {
		return fmt.Errorf("role not found")
	}

	a.ID = r.nextIDStr("assign")
	a.GrantedAt = time.Now().UTC()
	r.assignments[a.ID] = a
	return nil
}

func (r *MemoryRepository) ListUserRoles(_ context.Context, orgID, userID string) ([]UserRoleAssignment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []UserRoleAssignment
	for _, a := range r.assignments {
		if a.UserID == userID {
			result = append(result, *a)
		}
	}
	return result, nil
}
