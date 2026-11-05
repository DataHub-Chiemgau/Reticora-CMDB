package asset_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// Retest (WP-745, AST-02):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssetHolder' ./internal/asset/

// wantSQLState fails unless err is a PostgreSQL error with code and
// constraint.
func wantSQLState(t *testing.T, what string, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Errorf("%s: %v, want %s on %s", what, err, code, constraint)
	}
}

// TestAssetHolderConstraints covers the schema of WP-745 (AST-02) against
// PostgreSQL: a holder user or team of another organization fails the
// composite foreign key (23503), a user and a team together and a due
// date without holder fail their CHECK (23514), and stock_movement.due_at
// is refused on every movement type but assignment (23514).
func TestAssetHolderConstraints(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	bg := context.Background()
	id := f.Asset(t, f.OrgA, "", "WP745-C")
	userA, userB := f.AppUser(t, f.OrgA, "wp745-ca"), f.AppUser(t, f.OrgB, "wp745-cb")
	teamA, teamB := f.ID(), f.ID()
	w.admin(t, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp745-ca'), ($3, $4, 'wp745-cb')`,
		teamA, f.OrgA, teamB, f.OrgB)
	update := func(set string, args ...any) error {
		_, err := f.Admin.Exec(bg, `UPDATE asset SET `+set+` WHERE id = $1`, append([]any{id}, args...)...)
		return err
	}

	wantSQLState(t, "user of organization B", update(`assigned_user_id = $2`, userB), "23503", "asset_assigned_user_id_fkey")
	wantSQLState(t, "team of organization B", update(`assigned_team_id = $2`, teamB), "23503", "asset_assigned_team_id_fkey")
	wantSQLState(t, "user and team", update(`assigned_user_id = $2, assigned_team_id = $3`, userA, teamA), "23514", "asset_single_holder")
	wantSQLState(t, "due date without holder", update(`assignment_due_at = now()`), "23514", "asset_holder_due")
	for _, ok := range []struct {
		set  string
		args []any
	}{
		{`assigned_user_id = $2, assignment_due_at = now()`, []any{userA}},
		{`assigned_user_id = NULL, assigned_team_id = $2, assignment_due_at = NULL`, []any{teamA}},
		{`assigned_team_id = NULL`, nil},
	} {
		if err := update(ok.set, ok.args...); err != nil {
			t.Errorf("%s: %v", ok.set, err)
		}
	}

	insert := func(movementType string) error {
		_, err := f.Admin.Exec(bg, `INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, due_at)
			VALUES ($1, 'asset', $2, $3, now() + interval '7 days')`, f.OrgA, id, movementType)
		return err
	}
	for _, other := range []string{"transfer", "return", "reservation"} {
		wantSQLState(t, "due_at on "+other, insert(other), "23514", "stock_movement_due_at_assignment")
	}
	if err := insert("assignment"); err != nil {
		t.Errorf("due_at on assignment: %v", err)
	}
}

// TestAssetHolderAPI covers the API of WP-745 (AST-02) against
// PostgreSQL: GET /api/v1/assets/{id} returns the holder cache read-only
// (user or team and due date), the field projection leaves it out for a
// restricted policy, a PATCH sending one of its members is 422 naming the
// field, a holder change is a row of the value history, the ledger writes
// and reads due_at, and a team that holds an asset is not deleted
// (ErrTeamReferenced, 409 on DELETE /api/v1/teams/{id}).
func TestAssetHolderAPI(t *testing.T) {
	w := newAssetWorld(t)
	f := w.f
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	a := w.create(t, `{"inventory_no":"WP745-A","name":"holder"}`)
	if a.AssignedUserID != "" || a.AssignedTeamID != "" || a.AssignmentDueAt != nil {
		t.Fatalf("new asset with a holder: %+v", a)
	}
	path := assetsPath + "/" + a.ID
	userA := f.AppUser(t, f.OrgA, "wp745-a")
	team := f.ID()
	w.admin(t, `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp745-a')`, team, f.OrgA)
	due := time.Date(2026, 12, 31, 16, 30, 0, 0, time.UTC)

	// The cache is filled by the assignment service (WP-275); here the
	// maintenance pool sets it.
	w.admin(t, `UPDATE asset SET assigned_user_id = $2, assignment_due_at = $3 WHERE id = $1`, a.ID, userA, due)
	got := decodeAsset(t, w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK}))
	if got.AssignedUserID != userA || got.AssignedTeamID != "" || got.AssignmentDueAt == nil || !got.AssignmentDueAt.Equal(due) {
		t.Fatalf("holder user: %q %q %v", got.AssignedUserID, got.AssignedTeamID, got.AssignmentDueAt)
	}
	if got.OwnerUserID != "" {
		t.Fatalf("the holder is no owner: %q", got.OwnerUserID)
	}
	n := fieldRows(assetTrail(t, f, a.ID))
	if n["assigned_user_id"] != 1 || n["assignment_due_at"] != 1 {
		t.Errorf("value history of the holder: %v, want one row each of assigned_user_id and assignment_due_at", n)
	}

	w.admin(t, `UPDATE asset SET assigned_user_id = NULL, assigned_team_id = $2, assignment_due_at = NULL WHERE id = $1`, a.ID, team)
	rec := w.do(t, &call{method: http.MethodGet, path: path, want: http.StatusOK})
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if body, ok := raw["asset"]; ok { // the detail with the linked CI
		raw = nil
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decode asset %s: %v", body, err)
		}
	}
	var gotTeam string
	if err := json.Unmarshal(raw["assigned_team_id"], &gotTeam); err != nil || gotTeam != team {
		t.Fatalf("holder team %s (%v), want %s", raw["assigned_team_id"], err, team)
	}
	for _, absent := range []string{"assigned_user_id", "assignment_due_at"} {
		if _, ok := raw[absent]; ok {
			t.Errorf("team holder answers %s: %s", absent, rec.Body.String())
		}
	}

	// A restricted asset policy leaves the holder out: its members have no
	// grantable field key (fail-closed, WP-265).
	role := f.ID()
	perms, _ := json.Marshal([]string{"asset:read", "asset:write"})
	w.admin(t, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp745-reader', $3::jsonb)`, role, f.OrgA, string(perms))
	w.admin(t, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'asset', 'core.notes', 'none')`,
		f.OrgA, role)
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(bg, f.OrgA, []identity.Grant{{Permissions: []identity.Permission{"asset:read", "asset:write"}, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	restricted := w.do(t, &call{ctx: permission.WithPolicy(ctx, policy), method: http.MethodGet, path: path, want: http.StatusOK})
	var projected map[string]json.RawMessage
	if err = json.Unmarshal(restricted.Body.Bytes(), &projected); err != nil {
		t.Fatalf("decode %s: %v", restricted.Body.String(), err)
	}
	if _, ok := projected["assigned_team_id"]; ok {
		t.Errorf("restricted policy reads the holder: %s", restricted.Body.String())
	}

	// The holder cache is read-only.
	for _, field := range []string{"assigned_user_id", "assigned_team_id", "assignment_due_at"} {
		value := `"` + userA + `"`
		if field == "assignment_due_at" {
			value = `"2027-01-31T00:00:00Z"`
		}
		w.do(t, &call{method: http.MethodPatch, path: path, body: `{"` + field + `":` + value + `}`,
			want: http.StatusUnprocessableEntity, wantField: field})
	}
	w.do(t, &call{method: http.MethodPatch, path: path, body: `{"assigned_user_id":null}`,
		want: http.StatusUnprocessableEntity, wantField: "assigned_user_id"})

	// The ledger writes and reads the due date of an assignment.
	movements := movement.NewPGRepository(f.App)
	booked := &movement.Movement{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: a.ID, MovementType: "assignment",
		AssigneeUserID: userA, DueAt: &due}
	if err = movements.Record(ctx, booked); err != nil {
		t.Fatalf("record assignment: %v", err)
	}
	listed, _, err := movements.ListMovements(ctx, f.OrgA, &movement.MovementFilter{AssetID: a.ID}, api.PaginationParams{Limit: 10})
	if err != nil || len(listed) != 1 {
		t.Fatalf("movements of the asset: %d (%v)", len(listed), err)
	}
	if listed[0].ID != booked.ID || listed[0].DueAt == nil || !listed[0].DueAt.Equal(due) {
		t.Fatalf("listed movement %s due %v, want %s due %s", listed[0].ID, listed[0].DueAt, booked.ID, due)
	}
	encoded, _ := json.Marshal(listed[0])
	var asJSON map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &asJSON); err != nil {
		t.Fatal(err)
	}
	var dueJSON time.Time
	if err = json.Unmarshal(asJSON["due_at"], &dueJSON); err != nil || !dueJSON.Equal(due) {
		t.Fatalf("due_at member %s (%v)", asJSON["due_at"], err)
	}

	// A team that holds an asset is not deleted: ErrTeamReferenced, 409.
	users := user.NewPGRepository(f.App)
	if err = users.DeleteTeam(ctx, f.OrgA, team); !errors.Is(err, user.ErrTeamReferenced) {
		t.Fatalf("delete holder team: %v, want ErrTeamReferenced", err)
	}
	mux := chi.NewRouter()
	user.NewHandler(users).RegisterRoutes(mux)
	teamRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/teams/"+team, http.NoBody)
	mux.ServeHTTP(teamRec, req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})))
	if teamRec.Code != http.StatusConflict {
		t.Fatalf("DELETE /api/v1/teams/{id} of a holder team: %d %s, want 409", teamRec.Code, teamRec.Body.String())
	}
	var problem api.ProblemDetail
	if err = json.Unmarshal(teamRec.Body.Bytes(), &problem); err != nil || problem.Status != http.StatusConflict {
		t.Fatalf("problem %s (%v)", teamRec.Body.String(), err)
	}

	// Without the holder the team goes.
	w.admin(t, `UPDATE asset SET assigned_team_id = NULL WHERE id = $1`, a.ID)
	if err = users.DeleteTeam(ctx, f.OrgA, team); err != nil {
		t.Fatalf("delete team without assets: %v", err)
	}
}
