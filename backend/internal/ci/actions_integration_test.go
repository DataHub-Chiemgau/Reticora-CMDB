package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// Action grants on the CI routes (RBA-07, E-43, WP-728) against PostgreSQL
// through the production router: every route answers 403 with the action
// when an action grant none of the CI type (or an object grant none of the
// CI) refuses it, and 2xx without grant; delete, restore, merge (survivor
// and victim), unmerge and the revert of a ci.merge job with ci:delete
// (403, never 409, also with force and a changed survivor), create, PATCH,
// type change and bulk with ci:write, the instance attribute routes with
// ci_instance_attribute:manage, overrides with override:write and the
// lifecycle transition with lifecycle:transition. The action is checked
// before the field permissions. A bulk refuses the ids of the locked type
// with 403 and applies the others, synchronously and as a queue job
// (RunBulkJob with the policy of its creator, reloaded from the job row).
// An organization without grants behaves as before. It runs only against
// PostgreSQL (TEST_DATABASE_URL).

// phase1CreatorPolicy is the creator policy of a queue bulk job in an
// organization without grants (tests that run RunBulkJob themselves).
func phase1CreatorPolicy(context.Context, *jobs.Job) (*permission.FieldPolicy, error) {
	return permission.UnrestrictedPolicy(identity.AllPermissions()), nil
}

// wantAction checks a 403 of the action grants: the problem type of the
// field permissions, the action and no fields.
func wantAction(t *testing.T, what string, r *frResponse, action identity.Permission) {
	t.Helper()
	if r.code != http.StatusForbidden {
		t.Fatalf("%s: %d %s, want 403", what, r.code, r.body)
	}
	body := r.object(t)
	if body["type"] != api.ProblemTypeForbidden || body["action"] != string(action) {
		t.Fatalf("%s: %s, want type %s with action %s", what, r.body, api.ProblemTypeForbidden, action)
	}
	if fields, _ := body["fields"].([]any); len(fields) != 0 {
		t.Fatalf("%s: fields %v, want none", what, fields)
	}
}

// wantCode checks the status of an answer.
func wantCode(t *testing.T, what string, r *frResponse, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: %d %s, want %d", what, r.code, r.body, code)
	}
}

// bulkCodes returns the code of each id of a bulk report.
func bulkCodes(t *testing.T, raw []byte) map[string]int {
	t.Helper()
	var report ci.BulkResult
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode the bulk report %s: %v", raw, err)
	}
	out := map[string]int{}
	for _, item := range report.Items {
		out[item.ID] = item.Code
	}
	return out
}

func TestActionGrantsOnCIRoutes(t *testing.T) {
	f := scopetest.Seed(t, "b9")
	bg := context.Background()
	orgType := func(org, key string) string {
		var id string
		// The software lifecycle model (applies to ci, WP-263).
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, $2, $2, '00000000-0000-0000-0000-000000000102') RETURNING id::text`, org, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	lockedType, openType := orgType(f.OrgA, "wp728-locked"), orgType(f.OrgA, "wp728-open")
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	newCI := func(org, client, typeID, name string) string {
		id := f.CI(t, org, client, name)
		frExec(t, f, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, id, typeID)
		return id
	}

	// The role of the locked user holds every permission of the routes;
	// its action grants close them on the locked type. hostname is hidden
	// on every type.
	lockedPerms := []string{"ci:read", "ci:write", "ci:delete", "override:write", "lifecycle:transition",
		"ci_instance_attribute:manage", "job:read"}
	roleL := frRole(t, f, f.OrgA, "wp728-locked", lockedPerms...)
	userL := f.AppUser(t, f.OrgA, "wp728-locked")
	frExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userL, roleL)
	for _, action := range []string{"ci:write", "ci:delete", "override:write", "lifecycle:transition", "ci_instance_attribute:manage"} {
		frExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, ci_type_id, action, level)
			VALUES ($1, $2, 'ci', $3, $4, 'none')`, f.OrgA, roleL, lockedType, action)
	}
	frExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'core.hostname', 'none')`,
		f.OrgA, roleL)
	userA := f.AppUser(t, f.OrgA, "wp728-admin")
	frExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userA)
	locked := userPrincipal(f.OrgA, userL, frPerms(lockedPerms...))
	admin := userPrincipal(f.OrgA, userA, identity.AllPermissions())
	e := newFREnv(t, f)
	lockedCI := func(name string) string { return newCI(f.OrgA, f.Client1, lockedType, name) }
	openCI := func(name string) string { return newCI(f.OrgA, f.Client1, openType, name) }

	t.Run("delete and restore", func(t *testing.T) {
		l, o := lockedCI("wp728-del-l"), openCI("wp728-del-o")
		wantAction(t, "DELETE of the locked type", e.do(&locked, http.MethodDelete, "/api/v1/cis/"+l, nil), identity.PermCIDelete)
		wantCode(t, "the CI stays", e.do(&admin, http.MethodGet, "/api/v1/cis/"+l, nil), http.StatusOK)
		wantCode(t, "DELETE of the open type", e.do(&locked, http.MethodDelete, "/api/v1/cis/"+o, nil), http.StatusNoContent)
		wantCode(t, "DELETE by the administrator", e.do(&admin, http.MethodDelete, "/api/v1/cis/"+l, nil), http.StatusNoContent)
		wantAction(t, "restore of the locked type", e.do(&locked, http.MethodPost, "/api/v1/cis/"+l+"/restore", nil), identity.PermCIDelete)
		wantCode(t, "restore of the open type", e.do(&locked, http.MethodPost, "/api/v1/cis/"+o+"/restore", nil), http.StatusOK)
		wantCode(t, "unknown CI", e.do(&locked, http.MethodDelete, "/api/v1/cis/"+f.ID(), nil), http.StatusNotFound)
	})

	t.Run("create, PATCH and type change", func(t *testing.T) {
		wantAction(t, "POST of the locked type", e.do(&locked, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": lockedType, "client_id": f.Client1, "name": "wp728-new-l"}), identity.PermCIWrite)
		wantCode(t, "POST of the open type", e.do(&locked, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": openType, "client_id": f.Client1, "name": "wp728-new-o"}), http.StatusCreated)
		l, o := lockedCI("wp728-patch-l"), openCI("wp728-patch-o")
		wantAction(t, "PATCH of the locked type", e.do(&locked, http.MethodPatch, "/api/v1/cis/"+l, map[string]any{"model": "M"}),
			identity.PermCIWrite)
		wantCode(t, "PATCH of the open type", e.do(&locked, http.MethodPatch, "/api/v1/cis/"+o, map[string]any{"model": "M"}), http.StatusOK)
		// The action grant before the field permissions, both before If-Match.
		wantAction(t, "PATCH of a hidden field of the locked type",
			e.do(&locked, http.MethodPatch, "/api/v1/cis/"+l, map[string]any{"hostname": "h"}, "If-Match", `"999"`), identity.PermCIWrite)
		wantForbidden(t, "PATCH of a hidden field of the open type",
			e.do(&locked, http.MethodPatch, "/api/v1/cis/"+o, map[string]any{"hostname": "h"}), "hostname")
		wantAction(t, "type change of the locked type",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+l+"/type", map[string]any{"ci_type_id": serverType}), identity.PermCIWrite)
		wantCode(t, "type change of the open type",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+o+"/type", map[string]any{"ci_type_id": serverType}), http.StatusOK)
	})

	t.Run("instance attributes and overrides", func(t *testing.T) {
		l, o := lockedCI("wp728-inst-l"), openCI("wp728-inst-o")
		for _, id := range []string{l, o} {
			frExec(t, f, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type) VALUES ($1, $2, 'note', 'string')`,
				f.OrgA, id)
		}
		const manage = identity.PermCIInstanceAttributeManage
		wantAction(t, "instance PUT of the locked type",
			e.do(&locked, http.MethodPut, "/api/v1/cis/"+l+"/instance-attributes/note", map[string]any{"value": "x"}), manage)
		wantCode(t, "instance PUT of the open type",
			e.do(&locked, http.MethodPut, "/api/v1/cis/"+o+"/instance-attributes/note", map[string]any{"value": "x"}), http.StatusOK)
		wantAction(t, "instance DELETE of the locked type",
			e.do(&locked, http.MethodDelete, "/api/v1/cis/"+l+"/instance-attributes/note", nil), manage)
		wantCode(t, "instance DELETE of the open type",
			e.do(&locked, http.MethodDelete, "/api/v1/cis/"+o+"/instance-attributes/note", nil), http.StatusNoContent)
		// The action precedes the validation of the key (422).
		wantAction(t, "instance PUT of an undefined key",
			e.do(&locked, http.MethodPut, "/api/v1/cis/"+l+"/instance-attributes/undefined", map[string]any{"value": "x"}), manage)

		override := map[string]any{"value": "M-ovr", "reason": "wp728"}
		wantAction(t, "override PUT of the locked type",
			e.do(&locked, http.MethodPut, "/api/v1/cis/"+l+"/fields/model/override", override), identity.PermOverrideWrite)
		wantCode(t, "override PUT of the open type",
			e.do(&locked, http.MethodPut, "/api/v1/cis/"+o+"/fields/model/override", override), http.StatusOK)
		wantAction(t, "override DELETE of the locked type",
			e.do(&locked, http.MethodDelete, "/api/v1/cis/"+l+"/fields/model/override", nil), identity.PermOverrideWrite)
		wantCode(t, "override DELETE of the open type",
			e.do(&locked, http.MethodDelete, "/api/v1/cis/"+o+"/fields/model/override", nil), http.StatusOK)
	})

	t.Run("lifecycle transition", func(t *testing.T) {
		l, o := lockedCI("wp728-lc-l"), openCI("wp728-lc-o")
		body := map[string]any{"to_state": "evaluation"}
		wantAction(t, "transition of the locked type",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+l+"/lifecycle-transitions", body), identity.PermLifecycleTransition)
		wantCode(t, "transition of the open type",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+o+"/lifecycle-transitions", body), http.StatusOK)
		wantCode(t, "transition of an unknown CI",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+f.ID()+"/lifecycle-transitions", body), http.StatusNotFound)
	})

	t.Run("merge, unmerge and job revert", func(t *testing.T) {
		oS, oV, l := openCI("wp728-m-os"), openCI("wp728-m-ov"), lockedCI("wp728-m-l")
		wantAction(t, "merge of a locked victim",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+oS+"/merge", map[string]any{"victim_id": l}), identity.PermCIDelete)
		wantAction(t, "merge into a locked survivor",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+l+"/merge", map[string]any{"victim_id": oV}), identity.PermCIDelete)
		wantCode(t, "merge of open CIs",
			e.do(&locked, http.MethodPost, "/api/v1/cis/"+oS+"/merge", map[string]any{"victim_id": oV}), http.StatusOK)
		wantCode(t, "unmerge of open CIs", e.do(&locked, http.MethodPost, "/api/v1/cis/"+oV+"/unmerge", nil), http.StatusOK)

		// A merge of locked CIs whose survivor changed afterwards: the
		// locked user gets 403, never the 409 of E-80, also with force and
		// through the job revert.
		lS, lV := lockedCI("wp728-m-ls"), lockedCI("wp728-m-lv")
		merged := e.do(&admin, http.MethodPost, "/api/v1/cis/"+lS+"/merge", map[string]any{"victim_id": lV})
		wantCode(t, "merge by the administrator", merged, http.StatusOK)
		mergeID, _ := merged.object(t)["merge_id"].(string)
		wantCode(t, "change of the survivor", e.do(&admin, http.MethodPatch, "/api/v1/cis/"+lS, map[string]any{"model": "later"}), http.StatusOK)
		wantAction(t, "unmerge", e.do(&locked, http.MethodPost, "/api/v1/cis/"+lV+"/unmerge", nil), identity.PermCIDelete)
		wantAction(t, "unmerge with force", e.do(&locked, http.MethodPost, "/api/v1/cis/"+lV+"/unmerge?force=true", nil), identity.PermCIDelete)
		var jobID string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM job WHERE organization_id = $1 AND ref_table = 'ci_merge' AND ref_id = $2`,
			f.OrgA, mergeID).Scan(&jobID); err != nil {
			t.Fatalf("merge job: %v", err)
		}
		wantAction(t, "job revert", e.do(&locked, http.MethodPost, "/api/v1/jobs/"+jobID+"/revert", nil), identity.PermCIDelete)
		wantCode(t, "unmerge by the administrator", e.do(&admin, http.MethodPost, "/api/v1/cis/"+lV+"/unmerge", nil), http.StatusConflict)
		wantCode(t, "job revert by the administrator", e.do(&admin, http.MethodPost, "/api/v1/jobs/"+jobID+"/revert", nil), http.StatusConflict)
		wantCode(t, "forced unmerge by the administrator",
			e.do(&admin, http.MethodPost, "/api/v1/cis/"+lV+"/unmerge?force=true", nil), http.StatusOK)
	})

	t.Run("object grant none closes every action", func(t *testing.T) {
		o := openCI("wp728-og")
		frExec(t, f, `INSERT INTO object_grant (organization_id, grantee_role_id, object_kind, object_id, level) VALUES ($1, $2, 'ci', $3, 'none')`,
			f.OrgA, roleL, o)
		og := newFREnv(t, f)
		wantAction(t, "DELETE", og.do(&locked, http.MethodDelete, "/api/v1/cis/"+o, nil), identity.PermCIDelete)
		wantAction(t, "PATCH of a structural field", og.do(&locked, http.MethodPatch, "/api/v1/cis/"+o, map[string]any{"status": "maintenance"}),
			identity.PermCIWrite)
		wantCode(t, "the administrator", og.do(&admin, http.MethodPatch, "/api/v1/cis/"+o, map[string]any{"status": "maintenance"}), http.StatusOK)
	})

	t.Run("bulk", func(t *testing.T) {
		l, o := lockedCI("wp728-bulk-l"), openCI("wp728-bulk-o")
		r := e.do(&locked, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": "set_status", "ids": []string{l, o}, "status": "maintenance"})
		wantCode(t, "bulk", r, http.StatusOK)
		if codes := bulkCodes(t, r.body); codes[l] != http.StatusForbidden || codes[o] != http.StatusOK {
			t.Fatalf("bulk report %s: want 403 for the locked type, 200 for the open one", r.body)
		}

		// The queue job: the policy of its creator, reloaded at run time.
		repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
		users, ok := repos.User.(permission.CreatorStatus)
		if !ok {
			t.Fatal("the user repository reports no user status")
		}
		resolver := permission.NewFieldResolver(repos.Permission, nil, repos.FieldGrants, nil)
		svc := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithCreatorPolicy(ci.CreatorPolicyOf(resolver, users))
		jl, jo := lockedCI("wp728-job-l"), openCI("wp728-job-o")
		params, _ := json.Marshal(ci.BulkRequest{Action: ci.BulkSetStatus, IDs: []string{jl, jo}, Status: ci.StatusInactive})
		job := &jobs.Job{ID: f.ID(), OrganizationID: f.OrgA, Kind: ci.BulkJobKind, CreatedBy: userL, Params: params, Scope: &jobs.Scope{}}
		result, err := svc.RunBulkJob(bg, job)
		if err != nil {
			t.Fatalf("bulk job: %v", err)
		}
		raw, _ := json.Marshal(result)
		if codes := bulkCodes(t, raw); codes[jl] != http.StatusForbidden || codes[jo] != http.StatusOK {
			t.Fatalf("bulk job report %s: want 403 for the locked type, 200 for the open one", raw)
		}
		var status string
		if err = f.Admin.QueryRow(bg, `SELECT status FROM ci WHERE id = $1`, jl).Scan(&status); err != nil || status != ci.StatusActive {
			t.Fatalf("CI of the locked type after the job: %s %v", status, err)
		}
		// A creator that is no longer active, or a service without the
		// creator policy, fails the job (fail-closed).
		frExec(t, f, `UPDATE app_user SET is_active = false WHERE id = $1`, userL)
		if _, err = svc.RunBulkJob(bg, job); !errors.Is(err, ci.ErrNoCreatorPolicy) {
			t.Fatalf("inactive creator: %v, want ErrNoCreatorPolicy", err)
		}
		frExec(t, f, `UPDATE app_user SET is_active = true WHERE id = $1`, userL)
		bare := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))
		if _, err = bare.RunBulkJob(bg, job); !errors.Is(err, ci.ErrNoCreatorPolicy) {
			t.Fatalf("without creator policy: %v, want ErrNoCreatorPolicy", err)
		}
	})

	t.Run("organization without grants as before", func(t *testing.T) {
		typeB := orgType(f.OrgB, "wp728-b")
		userB := f.AppUser(t, f.OrgB, "wp728-b")
		adminB := userPrincipal(f.OrgB, userB, identity.AllPermissions())
		b1, b2, b3 := newCI(f.OrgB, "", typeB, "wp728-b1"), newCI(f.OrgB, "", typeB, "wp728-b2"), newCI(f.OrgB, "", typeB, "wp728-b3")
		wantCode(t, "POST", e.do(&adminB, http.MethodPost, "/api/v1/cis", map[string]any{"ci_type_id": typeB, "name": "wp728-b-new"}),
			http.StatusCreated)
		wantCode(t, "PATCH", e.do(&adminB, http.MethodPatch, "/api/v1/cis/"+b1, map[string]any{"model": "B"}), http.StatusOK)
		wantCode(t, "override", e.do(&adminB, http.MethodPut, "/api/v1/cis/"+b1+"/fields/model/override",
			map[string]any{"value": "B-ovr", "reason": "wp728"}), http.StatusOK)
		wantCode(t, "transition", e.do(&adminB, http.MethodPost, "/api/v1/cis/"+b1+"/lifecycle-transitions",
			map[string]any{"to_state": "evaluation"}), http.StatusOK)
		wantCode(t, "merge", e.do(&adminB, http.MethodPost, "/api/v1/cis/"+b1+"/merge", map[string]any{"victim_id": b2}), http.StatusOK)
		wantCode(t, "unmerge", e.do(&adminB, http.MethodPost, "/api/v1/cis/"+b2+"/unmerge", nil), http.StatusOK)
		wantCode(t, "DELETE", e.do(&adminB, http.MethodDelete, "/api/v1/cis/"+b3, nil), http.StatusNoContent)
		wantCode(t, "restore", e.do(&adminB, http.MethodPost, "/api/v1/cis/"+b3+"/restore", nil), http.StatusOK)
		r := e.do(&adminB, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": "set_status", "ids": []string{b1, b3}, "status": "maintenance"})
		wantCode(t, "bulk", r, http.StatusOK)
		if codes := bulkCodes(t, r.body); codes[b1] != http.StatusOK || codes[b3] != http.StatusOK {
			t.Fatalf("bulk report %s", r.body)
		}
		wantCode(t, "type change", e.do(&adminB, http.MethodPost, "/api/v1/cis/"+b3+"/type", map[string]any{"ci_type_id": serverType}), http.StatusOK)

		// The instance attribute routes act with ci_instance_attribute:manage,
		// not ci:write (WP-256).
		frExec(t, f, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type) VALUES ($1, $2, 'note', 'string')`,
			f.OrgB, b1)
		manager := userPrincipal(f.OrgB, userB, frPerms("ci:read", "ci_instance_attribute:manage"))
		wantCode(t, "instance PUT without ci:write",
			e.do(&manager, http.MethodPut, "/api/v1/cis/"+b1+"/instance-attributes/note", map[string]any{"value": "x"}), http.StatusOK)
		wantCode(t, "instance DELETE without ci:write",
			e.do(&manager, http.MethodDelete, "/api/v1/cis/"+b1+"/instance-attributes/note", nil), http.StatusNoContent)
	})
}
