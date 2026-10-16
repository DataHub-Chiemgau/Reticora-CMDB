//go:build auditdefect

package iga_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (it fails until
// the product is fixed).
//
// WP-021 (IGA-01, TEN-06; data integrity) P2 "IGA handler ignores repository
// write errors (approval without provisioning task)": iga/handler.go
// decideAccess discards the error of h.repo.CreateTask ('_ = ...') after
// DecideAccessRequest has committed in its own WithTenant transaction. When
// the task cannot be written (concurrent connector deletion, DB outage) the
// API answers 200, the request is 'approved' and no provisioning task
// exists, so the approved access is never provisioned and nobody learns of
// it. Requirement: the approval and the task are atomic, or the failure is
// reported (non-2xx) and the request stays pending.
//
// The repository is the PostgreSQL repository; only CreateTask is replaced by
// a failing one to model the write error.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1ApprovalNeverLosesTheProvisioningTask' ./internal/iga/

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// t1FailingTasks is the PostgreSQL repository whose task writes fail.
type t1FailingTasks struct{ iga.Repository }

func (t1FailingTasks) CreateTask(context.Context, *iga.ProvisioningTask) error {
	return errors.New("t1 audit: injected CreateTask failure")
}

func TestAuditT1ApprovalNeverLosesTheProvisioningTask(t *testing.T) {
	f := scopetest.Seed(t, "d6")
	bg := context.Background()
	subject := f.AppUser(t, f.OrgA, "t1-iga-subject")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: subject})
	pg := iga.NewPGRepository(f.App)
	conn := &iga.ConnectorConfig{OrganizationID: f.OrgA, Name: "t1-iga", Type: "scim", Status: "active"}
	if err := pg.CreateConnector(ctx, conn); err != nil {
		t.Fatalf("connector: %v", err)
	}
	request := func(entitlement string) *iga.AccessRequest {
		t.Helper()
		ar := &iga.AccessRequest{OrganizationID: f.OrgA, RequesterID: subject, SubjectUserID: subject, ConnectorID: conn.ID, Entitlement: entitlement, Status: "pending"}
		if err := pg.CreateAccessRequest(ctx, ar); err != nil {
			t.Fatalf("access request: %v", err)
		}
		return ar
	}
	approve := func(repo iga.Repository, id string) int {
		r := chi.NewRouter()
		iga.NewHandler(repo, nil, nil, nil, nil).RegisterRoutes(r)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/iga/access-requests/"+id+"/approve", strings.NewReader(`{}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	state := func(id, entitlement string) (string, int) {
		t.Helper()
		var status string
		var tasks int
		if err := f.Admin.QueryRow(bg, `SELECT status FROM iga_access_request WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM iga_provisioning_task WHERE organization_id = $1 AND payload->>'entitlement' = $2`,
			f.OrgA, entitlement).Scan(&tasks); err != nil {
			t.Fatal(err)
		}
		return status, tasks
	}

	// Positive control: a normal approval creates exactly one task.
	ok := request("t1-grp-ok")
	if code := approve(pg, ok.ID); code != http.StatusOK {
		t.Fatalf("normal approve: %d, want 200", code)
	}
	if status, tasks := state(ok.ID, "t1-grp-ok"); status != "approved" || tasks != 1 {
		t.Fatalf("normal approve: status %q, tasks %d; want approved and 1", status, tasks)
	}

	failing := request("t1-grp-fail")
	code := approve(t1FailingTasks{pg}, failing.ID)
	status, tasks := state(failing.ID, "t1-grp-fail")
	if code >= 200 && code < 300 && tasks == 0 {
		t.Errorf("IGA-01/data integrity: approve answered %d while the provisioning task could not be written; DB status %q, tasks %d (want non-2xx, or the task)", code, status, tasks)
	}
	if status == "approved" && tasks == 0 {
		t.Errorf("IGA-01/data integrity: the access request is %q without a provisioning task (want it still pending, or the task)", status)
	}
}
