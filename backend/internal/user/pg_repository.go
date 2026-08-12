package user

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const userSelectColumns = `
	id::text,
	organization_id::text,
	email,
	display_name,
	COALESCE(avatar_url, ''),
	CASE WHEN is_active THEN 'active' ELSE 'inactive' END,
	COALESCE(oidc_subject, ''),
	last_login,
	created_at,
	updated_at
`

const teamSelectColumns = `
	id::text,
	organization_id::text,
	name,
	COALESCE(description, ''),
	COALESCE(lead_id::text, ''),
	(SELECT COUNT(*)::int FROM team_member tm WHERE tm.team_id = team.id),
	created_at,
	updated_at
`

const teamMemberSelectColumns = `
	id::text,
	team_id::text,
	user_id::text,
	role_in_team,
	joined_at
`

const customRoleSelectColumns = `
	id::text,
	organization_id::text,
	name,
	COALESCE(description, ''),
	is_system,
	permissions,
	created_at,
	updated_at
`

const userRoleAssignmentSelectColumns = `
	user_custom_role.id::text,
	user_id::text,
	custom_role_id::text,
	scope_type,
	COALESCE(scope_id::text, ''),
	granted_at,
	COALESCE(granted_by::text, '')
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed user repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// withTenant executes fn within a transaction that has app.org_id set for RLS.
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *PGRepository) ListUsers(ctx context.Context, orgID, search string, page api.PaginationParams) ([]User, int, error) {
	items := make([]User, 0)
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"organization_id = $1"}
		args := []any{orgID}
		argPos := 2
		if search != "" {
			whereParts = append(whereParts, fmt.Sprintf("(display_name ILIKE $%d OR email ILIKE $%d)", argPos, argPos))
			args = append(args, "%"+search+"%")
			argPos++
		}
		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM app_user WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count users: %w", err)
		}

		listArgs := append(append([]any{}, args...), page.Limit, page.Offset)
		query := fmt.Sprintf(
			"SELECT %s FROM app_user WHERE %s ORDER BY display_name ASC LIMIT $%d OFFSET $%d",
			userSelectColumns,
			whereClause,
			argPos,
			argPos+1,
		)
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanUser(rows)
			if err != nil {
				return fmt.Errorf("scan user: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate users: %w", err)
		}
		return nil
	})
	return items, total, err
}

func (r *PGRepository) GetUser(ctx context.Context, orgID, id string) (*User, error) {
	var item *User
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM app_user WHERE id = $1 AND organization_id = $2", userSelectColumns)
		var err error
		item, err = scanUser(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("user not found")
			}
			return fmt.Errorf("get user: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *PGRepository) CreateUser(ctx context.Context, u *User) error {
	return r.withTenant(ctx, u.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO app_user (
				organization_id,
				oidc_subject,
				email,
				display_name,
				avatar_url,
				is_active
			) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			u.OrganizationID,
			nilIfEmpty(u.ExternalID),
			u.Email,
			u.DisplayName,
			nilIfEmpty(u.AvatarURL),
			statusIsActive(u.Status),
		).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if u.Status == "" {
			u.Status = "active"
		}
		return nil
	})
}

func (r *PGRepository) UpdateUser(ctx context.Context, orgID, id string, req UpdateUserRequest) (*User, error) {
	var item *User
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 4)
		args := []any{id, orgID}
		argPos := 3
		addStringField := func(column string, value *string) {
			if value == nil {
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, nilIfEmpty(*value))
			argPos++
		}
		addStringField("display_name", req.DisplayName)
		addStringField("avatar_url", req.AvatarURL)
		if req.Status != nil {
			setClauses = append(setClauses, fmt.Sprintf("is_active = $%d", argPos))
			args = append(args, statusIsActive(*req.Status))
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM app_user WHERE id = $1 AND organization_id = $2", userSelectColumns)
			var err error
			item, err = scanUser(tx.QueryRow(ctx, query, id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("user not found")
				}
				return fmt.Errorf("get user for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf(
			"UPDATE app_user SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(setClauses, ", "),
			userSelectColumns,
		)
		var err error
		item, err = scanUser(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("user not found")
			}
			return fmt.Errorf("update user: %w", err)
		}
		return nil
	})
	return item, err
}

func (r *PGRepository) DeleteUser(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM app_user WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("user not found")
		}
		return nil
	})
}

// AnonymizeUser replaces every personal column with a deterministic,
// non-identifying surrogate and deactivates the account. The row is kept so
// foreign keys (tickets, assignments, audit) stay intact — the record simply
// no longer identifies a person.
func (r *PGRepository) AnonymizeUser(ctx context.Context, orgID, id string) (*User, error) {
	var item *User
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE app_user
			SET email = $3,
				display_name = $4,
				avatar_url = NULL,
				oidc_subject = NULL,
				is_active = false,
				updated_at = NOW()
			WHERE id = $1 AND organization_id = $2
			RETURNING `+userSelectColumns,
			id, orgID, SurrogateEmail(id), SurrogateDisplayName(id))
		var err error
		item, err = scanUser(row)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("user not found")
		}
		return err
	})
	return item, err
}

func (r *PGRepository) ListTeams(ctx context.Context, orgID, search string, page api.PaginationParams) ([]Team, int, error) {
	items := make([]Team, 0)
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"organization_id = $1"}
		args := []any{orgID}
		argPos := 2
		if search != "" {
			whereParts = append(whereParts, fmt.Sprintf("name ILIKE $%d", argPos))
			args = append(args, "%"+search+"%")
			argPos++
		}
		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM team WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count teams: %w", err)
		}
		listArgs := append(append([]any{}, args...), page.Limit, page.Offset)
		query := fmt.Sprintf(
			"SELECT %s FROM team WHERE %s ORDER BY name ASC LIMIT $%d OFFSET $%d",
			teamSelectColumns,
			whereClause,
			argPos,
			argPos+1,
		)
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list teams: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanTeam(rows)
			if err != nil {
				return fmt.Errorf("scan team: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate teams: %w", err)
		}
		return nil
	})
	return items, total, err
}

func (r *PGRepository) GetTeam(ctx context.Context, orgID, id string) (*Team, error) {
	var item *Team
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM team WHERE id = $1 AND organization_id = $2", teamSelectColumns)
		var err error
		item, err = scanTeam(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("team not found")
			}
			return fmt.Errorf("get team: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *PGRepository) CreateTeam(ctx context.Context, t *Team) error {
	return r.withTenant(ctx, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO team (
				organization_id,
				name,
				description,
				lead_id
			) VALUES ($1, $2, $3, $4)
			RETURNING id::text, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			t.OrganizationID,
			t.Name,
			nilIfEmpty(t.Description),
			nilIfEmpty(t.LeadID),
		).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return fmt.Errorf("create team: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) UpdateTeam(ctx context.Context, orgID, id string, req UpdateTeamRequest) (*Team, error) {
	var item *Team
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 4)
		args := []any{id, orgID}
		argPos := 3
		addStringField := func(column string, value *string) {
			if value == nil {
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, nilIfEmpty(*value))
			argPos++
		}
		addStringField("name", req.Name)
		addStringField("description", req.Description)
		addStringField("lead_id", req.LeadID)

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM team WHERE id = $1 AND organization_id = $2", teamSelectColumns)
			var err error
			item, err = scanTeam(tx.QueryRow(ctx, query, id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("team not found")
				}
				return fmt.Errorf("get team for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf(
			"UPDATE team SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(setClauses, ", "),
			teamSelectColumns,
		)
		var err error
		item, err = scanTeam(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("team not found")
			}
			return fmt.Errorf("update team: %w", err)
		}
		return nil
	})
	return item, err
}

func (r *PGRepository) DeleteTeam(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM team WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete team: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("team not found")
		}
		return nil
	})
}

func (r *PGRepository) AddTeamMember(ctx context.Context, orgID string, m *TeamMember) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM team WHERE id = $1 AND organization_id = $2)", m.TeamID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("check team for member: %w", err)
		}
		if !exists {
			return fmt.Errorf("team not found")
		}
		role := m.RoleInTeam
		if role == "" {
			role = "member"
		}
		query := `
			INSERT INTO team_member (
				team_id,
				user_id,
				role_in_team
			) VALUES ($1, $2, $3)
			RETURNING id::text, role_in_team, joined_at
		`
		if err := tx.QueryRow(ctx, query, m.TeamID, m.UserID, role).Scan(&m.ID, &m.RoleInTeam, &m.JoinedAt); err != nil {
			return fmt.Errorf("add team member: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) RemoveTeamMember(ctx context.Context, orgID, teamID, userID string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM team WHERE id = $1 AND organization_id = $2)", teamID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("check team for member removal: %w", err)
		}
		if !exists {
			return fmt.Errorf("team not found")
		}
		cmdTag, err := tx.Exec(ctx, "DELETE FROM team_member WHERE team_id = $1 AND user_id = $2", teamID, userID)
		if err != nil {
			return fmt.Errorf("remove team member: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("member not found")
		}
		return nil
	})
}

func (r *PGRepository) ListTeamMembers(ctx context.Context, orgID, teamID string) ([]TeamMember, error) {
	items := make([]TeamMember, 0)
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM team WHERE id = $1 AND organization_id = $2)", teamID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("check team for members: %w", err)
		}
		if !exists {
			return fmt.Errorf("team not found")
		}
		query := fmt.Sprintf("SELECT %s FROM team_member WHERE team_id = $1 ORDER BY joined_at ASC", teamMemberSelectColumns)
		rows, err := tx.Query(ctx, query, teamID)
		if err != nil {
			return fmt.Errorf("list team members: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanTeamMember(rows)
			if err != nil {
				return fmt.Errorf("scan team member: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate team members: %w", err)
		}
		return nil
	})
	return items, err
}

func (r *PGRepository) ListRoles(ctx context.Context, orgID string, page api.PaginationParams) ([]CustomRole, int, error) {
	items := make([]CustomRole, 0)
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM custom_role WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return fmt.Errorf("count roles: %w", err)
		}
		query := fmt.Sprintf("SELECT %s FROM custom_role WHERE organization_id = $1 ORDER BY name ASC LIMIT $2 OFFSET $3", customRoleSelectColumns)
		rows, err := tx.Query(ctx, query, orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list roles: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanCustomRole(rows)
			if err != nil {
				return fmt.Errorf("scan role: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate roles: %w", err)
		}
		return nil
	})
	return items, total, err
}

func (r *PGRepository) GetRole(ctx context.Context, orgID, id string) (*CustomRole, error) {
	var item *CustomRole
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM custom_role WHERE id = $1 AND organization_id = $2", customRoleSelectColumns)
		var err error
		item, err = scanCustomRole(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("role not found")
			}
			return fmt.Errorf("get role: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *PGRepository) CreateRole(ctx context.Context, role *CustomRole) error {
	return r.withTenant(ctx, role.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if role.Permissions == nil {
			role.Permissions = []string{}
		}
		query := `
			INSERT INTO custom_role (
				organization_id,
				name,
				description,
				is_system,
				permissions
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			role.OrganizationID,
			role.Name,
			nilIfEmpty(role.Description),
			role.IsSystem,
			role.Permissions,
		).Scan(&role.ID, &role.CreatedAt, &role.UpdatedAt); err != nil {
			return fmt.Errorf("create role: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) UpdateRole(ctx context.Context, orgID, id string, req UpdateRoleRequest) (*CustomRole, error) {
	var item *CustomRole
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var isSystem bool
		if err := tx.QueryRow(ctx, "SELECT is_system FROM custom_role WHERE id = $1 AND organization_id = $2", id, orgID).Scan(&isSystem); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("role not found")
			}
			return fmt.Errorf("check role: %w", err)
		}
		if isSystem {
			return fmt.Errorf("cannot modify system role")
		}

		setClauses := make([]string, 0, 4)
		args := []any{id, orgID}
		argPos := 3
		addStringField := func(column string, value *string) {
			if value == nil {
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, nilIfEmpty(*value))
			argPos++
		}
		addStringField("name", req.Name)
		addStringField("description", req.Description)
		if req.Permissions != nil {
			setClauses = append(setClauses, fmt.Sprintf("permissions = $%d", argPos))
			args = append(args, req.Permissions)
			argPos++
		}

		if len(setClauses) == 0 {
			query := fmt.Sprintf("SELECT %s FROM custom_role WHERE id = $1 AND organization_id = $2", customRoleSelectColumns)
			var err error
			item, err = scanCustomRole(tx.QueryRow(ctx, query, id, orgID))
			if err != nil {
				return fmt.Errorf("get role for update: %w", err)
			}
			return nil
		}

		setClauses = append(setClauses, "updated_at = NOW()")
		query := fmt.Sprintf(
			"UPDATE custom_role SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(setClauses, ", "),
			customRoleSelectColumns,
		)
		var err error
		item, err = scanCustomRole(tx.QueryRow(ctx, query, args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("role not found")
			}
			return fmt.Errorf("update role: %w", err)
		}
		return nil
	})
	return item, err
}

func (r *PGRepository) DeleteRole(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var isSystem bool
		if err := tx.QueryRow(ctx, "SELECT is_system FROM custom_role WHERE id = $1 AND organization_id = $2", id, orgID).Scan(&isSystem); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("role not found")
			}
			return fmt.Errorf("check role for delete: %w", err)
		}
		if isSystem {
			return fmt.Errorf("cannot delete system role")
		}
		cmdTag, err := tx.Exec(ctx, "DELETE FROM custom_role WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete role: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("role not found")
		}
		return nil
	})
}

func (r *PGRepository) AssignRole(ctx context.Context, orgID string, a *UserRoleAssignment) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM custom_role WHERE id = $1 AND organization_id = $2)", a.CustomRoleID, orgID).Scan(&exists); err != nil {
			return fmt.Errorf("check role for assignment: %w", err)
		}
		if !exists {
			return fmt.Errorf("role not found")
		}
		if a.ScopeType == "" {
			a.ScopeType = "organization"
		}
		query := `
			INSERT INTO user_custom_role (
				user_id,
				custom_role_id,
				scope_type,
				scope_id,
				granted_by
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text, granted_at
		`
		if err := tx.QueryRow(ctx, query,
			a.UserID,
			a.CustomRoleID,
			a.ScopeType,
			nilIfEmpty(a.ScopeID),
			nilIfEmpty(a.GrantedBy),
		).Scan(&a.ID, &a.GrantedAt); err != nil {
			return fmt.Errorf("assign role: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) ListUserRoles(ctx context.Context, orgID, userID string) ([]UserRoleAssignment, error) {
	items := make([]UserRoleAssignment, 0)
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf(`
			SELECT %s
			FROM user_custom_role
			JOIN custom_role ON custom_role.id = user_custom_role.custom_role_id
			WHERE custom_role.organization_id = $1 AND user_custom_role.user_id = $2
			ORDER BY granted_at DESC
		`, userRoleAssignmentSelectColumns)
		rows, err := tx.Query(ctx, query, orgID, userID)
		if err != nil {
			return fmt.Errorf("list user roles: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanUserRoleAssignment(rows)
			if err != nil {
				return fmt.Errorf("scan user role assignment: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate user role assignments: %w", err)
		}
		return nil
	})
	return items, err
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(scanner userScanner) (*User, error) {
	item := &User{}
	var lastLogin sql.NullTime
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.Email,
		&item.DisplayName,
		&item.AvatarURL,
		&item.Status,
		&item.ExternalID,
		&lastLogin,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		item.LastLoginAt = lastLogin.Time.UTC().Format(time.RFC3339Nano)
	}
	return item, nil
}

func scanTeam(scanner userScanner) (*Team, error) {
	item := &Team{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.Name,
		&item.Description,
		&item.LeadID,
		&item.MemberCount,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func scanTeamMember(scanner userScanner) (*TeamMember, error) {
	item := &TeamMember{}
	if err := scanner.Scan(
		&item.ID,
		&item.TeamID,
		&item.UserID,
		&item.RoleInTeam,
		&item.JoinedAt,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func scanCustomRole(scanner userScanner) (*CustomRole, error) {
	item := &CustomRole{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.Name,
		&item.Description,
		&item.IsSystem,
		&item.Permissions,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if item.Permissions == nil {
		item.Permissions = []string{}
	}
	return item, nil
}

func scanUserRoleAssignment(scanner userScanner) (*UserRoleAssignment, error) {
	item := &UserRoleAssignment{}
	if err := scanner.Scan(
		&item.ID,
		&item.UserID,
		&item.CustomRoleID,
		&item.ScopeType,
		&item.ScopeID,
		&item.GrantedAt,
		&item.GrantedBy,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func statusIsActive(status string) bool {
	return status == "" || strings.EqualFold(status, "active")
}
