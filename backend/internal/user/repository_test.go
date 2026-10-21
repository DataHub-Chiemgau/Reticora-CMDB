package user

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestMemoryRepositoryUserCRUD(t *testing.T) {
	repo := NewMemoryRepository()
	u := &User{OrganizationID: "org-1", Email: "a@example.com", DisplayName: "Alice", Status: "active"}
	if err := repo.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if u.ID == "" {
		t.Fatal("expected generated ID")
	}

	got, err := repo.GetUser(context.Background(), "org-1", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "a@example.com" {
		t.Fatalf("unexpected user: %+v", got)
	}
	if _, err := repo.GetUser(context.Background(), "org-2", u.ID); err == nil {
		t.Fatal("expected not-found for wrong tenant")
	}

	name := "Alicia"
	updated, err := repo.UpdateUser(context.Background(), "org-1", u.ID, UpdateUserRequest{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Alicia" {
		t.Fatalf("expected updated name, got %+v", updated)
	}

	list, total, err := repo.ListUsers(context.Background(), "org-1", "alic", api.PaginationParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 user, got total=%d list=%d", total, len(list))
	}

	if err := repo.DeleteUser(context.Background(), "org-1", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetUser(context.Background(), "org-1", u.ID); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

func TestMemoryRepositoryTeamsAndRoles(t *testing.T) {
	repo := NewMemoryRepository()
	team := &Team{OrganizationID: "org-1", Name: "Ops"}
	if err := repo.CreateTeam(context.Background(), team); err != nil {
		t.Fatal(err)
	}
	m := &TeamMember{TeamID: team.ID, UserID: "user-1", RoleInTeam: "lead"}
	if err := repo.AddTeamMember(context.Background(), "org-1", m); err != nil {
		t.Fatal(err)
	}
	members, err := repo.ListTeamMembers(context.Background(), "org-1", team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != "user-1" {
		t.Fatalf("unexpected members: %+v", members)
	}
	if err := repo.RemoveTeamMember(context.Background(), "org-1", team.ID, "user-1"); err != nil {
		t.Fatal(err)
	}

	role := &CustomRole{OrganizationID: "org-1", Name: "auditor", Permissions: []string{"ci:read"}}
	if err := repo.CreateRole(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	a := &UserRoleAssignment{UserID: "user-1", CustomRoleID: role.ID, ScopeType: "organization"}
	// The grant ceiling holds in memory as well: a caller without the role's
	// rights org-wide assigns nothing.
	var ceiling *GrantCeilingError
	client1 := &identity.Principal{Permissions: []identity.Permission{identity.PermRoleManage, identity.PermCIRead}, ClientScope: "client-1"}
	for _, caller := range []*identity.Principal{nil, client1} {
		if assignErr := repo.AssignRole(context.Background(), "org-1", caller, a); !errors.As(assignErr, &ceiling) {
			t.Fatalf("org-wide assignment by %+v: %v, want GrantCeilingError", caller, assignErr)
		}
	}
	admin := &identity.Principal{Permissions: []identity.Permission{identity.PermRoleManage, identity.PermCIRead}}
	if assignErr := repo.AssignRole(context.Background(), "org-1", admin, a); assignErr != nil {
		t.Fatal(assignErr)
	}
	assignments, err := repo.ListUserRoles(context.Background(), "org-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || assignments[0].CustomRoleID != role.ID {
		t.Fatalf("unexpected assignments: %+v", assignments)
	}

	// Revoking needs the same as assigning (WP-395): the client-1 caller
	// gets GrantCeilingError, another organization and an unknown id
	// errAssignmentNotFound; the org-wide caller revokes it once.
	for _, caller := range []*identity.Principal{nil, client1} {
		if revokeErr := repo.RevokeRoleAssignment(context.Background(), "org-1", caller, a.ID); !errors.As(revokeErr, &ceiling) {
			t.Fatalf("revocation by %+v: %v, want GrantCeilingError", caller, revokeErr)
		}
	}
	for org, id := range map[string]string{"org-2": a.ID, "org-1": "assign-unknown"} {
		if revokeErr := repo.RevokeRoleAssignment(context.Background(), org, admin, id); !errors.Is(revokeErr, errAssignmentNotFound) {
			t.Fatalf("revocation of %s in %s: %v, want errAssignmentNotFound", id, org, revokeErr)
		}
	}
	if revokeErr := repo.RevokeRoleAssignment(context.Background(), "org-1", admin, a.ID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if assignments, err = repo.ListUserRoles(context.Background(), "org-1", "user-1"); err != nil || len(assignments) != 0 {
		t.Fatalf("assignments after the revocation: %+v, %v", assignments, err)
	}
	if revokeErr := repo.RevokeRoleAssignment(context.Background(), "org-1", admin, a.ID); !errors.Is(revokeErr, errAssignmentNotFound) {
		t.Fatalf("second revocation: %v, want errAssignmentNotFound", revokeErr)
	}
}
