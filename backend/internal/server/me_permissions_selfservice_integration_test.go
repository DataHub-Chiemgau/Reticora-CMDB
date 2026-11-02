// GET /me/permissions is self-service (OpenAPI getMyPermissions: "effective
// permissions for the authenticated caller", no 403): every signed-in user
// reads its own user_id and effective permissions, also without
// permission:read. The frontend reads them for every user (WP-518 saved views,
// WP-521 job center cancel rule in useJobs.ts); a 403 left a user with a
// custom role or a client_technician without its own id, so the job center
// never offered the creator the cancel of its own job.
//
//   - TestOwnPermissionsAreReadableWithoutPermissionRead – a user holding only
//     a custom role with job:read and ci:read (no permission:read) gets 200
//     with its own user_id and exactly these permissions; a client_technician
//     (RBA-02, no permission:read) gets 200 with its user_id. Before the fix
//     both got 403 "missing required permission: permission:read".
//
// The test runs the complete router of audit_t6_harness_test.go on
// PostgreSQL.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run TestOwnPermissionsAreReadableWithoutPermissionRead ./internal/server/
package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestOwnPermissionsAreReadableWithoutPermissionRead(t *testing.T) {
	e := t6NewEnv(t, "5d", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "mp-admin"), t6RolePerms(t, "org_admin")}

	type effective struct {
		UserID      string   `json:"user_id"`
		Permissions []string `json:"permissions"`
	}
	read := func(t *testing.T, as t6As) effective {
		t.Helper()
		r := e.Call(t, as, http.MethodGet, "/api/v1/me/permissions", ``)
		if r.Code != http.StatusOK {
			t.Fatalf("GET /me/permissions = %d %s, want 200 (self-service)", r.Code, t6Short(r.Body))
		}
		var got effective
		if err := json.Unmarshal(r.Body, &got); err != nil {
			t.Fatalf("decode %s: %v", t6Short(r.Body), err)
		}
		if got.UserID != as.User {
			t.Errorf("user_id = %q, want the caller %q", got.UserID, as.User)
		}
		return got
	}

	t.Run("custom role with job:read and ci:read only", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodPost, "/api/v1/roles",
			`{"name":"mp-job-reader","permissions":["job:read","ci:read"]}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /roles = %d %s", r.Code, t6Short(r.Body))
		}
		role := t6ID(r.Body)
		reader := f.AppUser(t, f.OrgA, "mp-reader")
		r = e.Call(t, admin, http.MethodPost, "/api/v1/role-assignments",
			`{"user_id":"`+reader+`","custom_role_id":"`+role+`"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /role-assignments = %d %s", r.Code, t6Short(r.Body))
		}

		got := read(t, t6As{f.OrgA, reader, []identity.Permission{identity.PermJobRead, identity.PermCIRead}})
		if want := []string{"ci:read", "job:read"}; !slices.Equal(got.Permissions, want) {
			t.Errorf("permissions = %v, want %v", got.Permissions, want)
		}
	})

	t.Run("client_technician", func(t *testing.T) {
		perms := t6RolePerms(t, "client_technician")
		if slices.Contains(perms, identity.PermPermissionRead) {
			t.Fatal("client_technician holds permission:read; the case proves nothing")
		}
		read(t, t6As{f.OrgA, f.AppUser(t, f.OrgA, "mp-technician"), perms})
	})
}
