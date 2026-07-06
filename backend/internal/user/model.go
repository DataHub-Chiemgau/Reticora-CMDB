// Package user provides the User, Team, and Custom Role management domain.
package user

import "time"

// User represents an application user.
type User struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Email          string    `json:"email"`
	DisplayName    string    `json:"display_name"`
	AvatarURL      string    `json:"avatar_url,omitempty"`
	Status         string    `json:"status"`
	ExternalID     string    `json:"external_id,omitempty"`
	LastLoginAt    string    `json:"last_login_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Team represents a group of users.
type Team struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	LeadID         string    `json:"lead_id,omitempty"`
	MemberCount    int       `json:"member_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TeamMember represents a user's membership in a team.
type TeamMember struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"team_id"`
	UserID     string    `json:"user_id"`
	RoleInTeam string    `json:"role_in_team"`
	JoinedAt   time.Time `json:"joined_at"`
}

// CustomRole represents a custom permission role.
type CustomRole struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	IsSystem       bool      `json:"is_system"`
	Permissions    []string  `json:"permissions"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// UserRoleAssignment represents a user's custom role assignment.
type UserRoleAssignment struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	CustomRoleID string    `json:"custom_role_id"`
	ScopeType    string    `json:"scope_type"`
	ScopeID      string    `json:"scope_id,omitempty"`
	GrantedAt    time.Time `json:"granted_at"`
	GrantedBy    string    `json:"granted_by,omitempty"`
}

// CreateUserRequest is the payload for creating a user.
type CreateUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
}

// UpdateUserRequest is the payload for updating a user.
type UpdateUserRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Status      *string `json:"status,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// CreateTeamRequest is the payload for creating a team.
type CreateTeamRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	LeadID      string `json:"lead_id,omitempty"`
}

// UpdateTeamRequest is the payload for updating a team.
type UpdateTeamRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	LeadID      *string `json:"lead_id,omitempty"`
}

// AddMemberRequest is the payload for adding a team member.
type AddMemberRequest struct {
	UserID     string `json:"user_id"`
	RoleInTeam string `json:"role_in_team,omitempty"`
}

// CreateRoleRequest is the payload for creating a custom role.
type CreateRoleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
}

// UpdateRoleRequest is the payload for updating a custom role.
type UpdateRoleRequest struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// AssignRoleRequest is the payload for assigning a role to a user.
type AssignRoleRequest struct {
	UserID       string `json:"user_id"`
	CustomRoleID string `json:"custom_role_id"`
	ScopeType    string `json:"scope_type,omitempty"`
	ScopeID      string `json:"scope_id,omitempty"`
}
