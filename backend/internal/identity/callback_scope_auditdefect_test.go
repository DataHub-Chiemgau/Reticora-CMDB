//go:build auditdefect

package identity_test

// Defect test found by WP-536 (t1-ev-core-isolation; F-364): a user of the
// organization without any role assignment logs in through the OIDC callback
// with the production wiring (user.PGRepository with the default provisioning
// role, configuration default viewer; permission.NewPGRepository). The plan
// criterion of WP-536 is "ohne Zuweisung keine Session". Observed: the login
// assigns the default role org-wide (EnsureRole runs on every login, not only
// the first) and issues a session with cls=null and the viewer permissions,
// so a user whose client-1 assignment an administrator removed reads every
// client of the organization after the next login (TEN-04, RBA-03).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -tags auditdefect -count=1 -v \
//	  -run '^TestAuditCallbackWithoutAssignmentIssuesNoSession$' ./internal/identity/

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

func TestAuditCallbackWithoutAssignmentIssuesNoSession(t *testing.T) {
	f := scopetest.Seed(t, "61")
	bg := context.Background()
	idp := newScopeIdP(t, f.OrgA)
	f.AppUser(t, f.OrgA, "cb-admin")
	member := f.AppUser(t, f.OrgA, "cb-revoked")
	// The user held engineer in client 1; an administrator removed it.
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA, member, f.Client1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM role_assignment WHERE user_id = $1`, member); err != nil {
		t.Fatal(err)
	}
	issuer := newIssuer(t)
	handler := identity.NewHandler(idp.provider(), issuer).
		WithProvisioning(user.NewPGRepository(f.App), "viewer").
		WithAccessResolver(permission.NewPGRepository(f.App))

	rec := idp.login(handler, "scopetest-61-cb-revoked", "cb-revoked@scopetest.invalid")
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), `"token"`) || hasRefreshCookie(rec) {
		body := rec.Body.String()
		if i := strings.Index(body, `"token":"`); i >= 0 {
			wire := tokenPayload(t, strings.SplitN(body[i+9:], `"`, 2)[0])
			t.Errorf("token cls=%s sts=%s scopes=%s", wire["cls"], wire["sts"], wire["scopes"])
		}
		t.Errorf("callback of a user without role assignment: %d, want no session", rec.Code)
	}
	var granted int
	if err := f.Admin.QueryRow(bg, `SELECT COUNT(*) FROM role_assignment WHERE user_id = $1`, member).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted != 0 {
		t.Errorf("the login assigned %d role(s) to a user without assignment, want 0", granted)
	}
}
