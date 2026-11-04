package history_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// Field permissions of the CI history (RBA-07, E-43, WP-727) against
// PostgreSQL through the production router (server.NewRouter with the
// field grants of WP-264): GET /history/ci/{id} (ci_change entries with
// their field_changes, the audit entries ci.created, ci.updated and
// ci.deleted with before, after and fields), GET /cis/{id}/state and the
// result of POST /history/snapshots carry no value of an unreadable typed
// column, attribute, instance attribute or sensitive attribute, also for a
// role with ci:read without audit:read (E-78); the snapshot job of the
// queue (RunSnapshotJob) projects with the policy of its creator; a CI
// whose type is not known (deleted) keeps only its structural fields. It
// runs only against PostgreSQL (TEST_DATABASE_URL).

// Values the readers may not see.
const (
	hfrHostname = "hfr-host-secret.example"
	hfrCost     = "hfr-cc-secret"
	hfrSalary   = "hfr-salary-secret"
	hfrInstance = "hfr-instance-secret"
)

var hfrSecrets = []string{hfrHostname, hfrCost, hfrSalary, hfrInstance}

type hfrEnv struct {
	t      *testing.T
	router http.Handler
}

func (e *hfrEnv) do(p *identity.Principal, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))
	return w.Code, w.Body.Bytes()
}

func hfrExec(t *testing.T, f *scopetest.Fixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func hfrPrincipal(org, userID string, keys ...string) identity.Principal {
	perms := make([]identity.Permission, len(keys))
	for i, k := range keys {
		perms[i] = identity.Permission(k)
	}
	scope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	return identity.Principal{Subject: userID, OrganizationID: org, Type: identity.PrincipalTypeUser, Scope: scope,
		Permissions: perms, Credential: "wp-727"}
}

// hfrHidden fails when body carries one of the values.
func hfrHidden(t *testing.T, what string, body []byte, values ...string) {
	t.Helper()
	for _, v := range values {
		if bytes.Contains(body, []byte(v)) {
			t.Fatalf("%s leaks %q: %s", what, v, body)
		}
	}
}

// hfrShown fails unless body carries every value.
func hfrShown(t *testing.T, what string, body []byte, values ...string) {
	t.Helper()
	for _, v := range values {
		if !bytes.Contains(body, []byte(v)) {
			t.Fatalf("%s lacks %q: %s", what, v, body)
		}
	}
}

func TestCIHistoryFieldRightsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "a7")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}

	// Two readers with the same field grants: hostname, cost_center and the
	// instance attribute secret_inst are none, salary is sensitive without
	// a grant. One reads the audit entries (audit:read), one does not.
	grantRole := func(name string, perms ...string) (role, userID string) {
		role = f.ID()
		raw, _ := json.Marshal(perms)
		hfrExec(t, f, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, $4::jsonb)`, role, f.OrgA, name, string(raw))
		userID = f.AppUser(t, f.OrgA, name)
		hfrExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userID, role)
		for _, key := range []string{"core.hostname", "attributes.cost_center", "_instance.secret_inst"} {
			hfrExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', $3, 'none')`,
				f.OrgA, role, key)
		}
		return role, userID
	}
	auditPerms := []string{"ci:read", "audit:read", "job:read"}
	_, userAudit := grantRole("wp727-audit-reader", auditPerms...)
	_, userPlain := grantRole("wp727-plain-reader", "ci:read")
	hfrExec(t, f, `INSERT INTO field_sensitivity (organization_id, object_kind, field_key) VALUES ($1, 'ci', 'attributes.salary')`, f.OrgA)
	// The administrator writes the sensitive field through its grant.
	userAdmin := f.AppUser(t, f.OrgA, "wp727-admin")
	hfrExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userAdmin)
	hfrExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level)
		SELECT $1, id, 'ci', 'attributes.salary', 'write' FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA)

	readerAudit := hfrPrincipal(f.OrgA, userAudit, auditPerms...)
	readerPlain := hfrPrincipal(f.OrgA, userPlain, "ci:read")
	admin := hfrPrincipal(f.OrgA, userAdmin)
	admin.Permissions = identity.AllPermissions()

	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-727", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &hfrEnv{t: t, router: middleware.RequestID(middleware.TenantMiddleware(mux))}

	// The writes of the administrator: create, an instance attribute, an
	// update of hostname and attributes in one change set.
	create := func(name, model, owner string) string {
		t.Helper()
		code, body := e.do(&admin, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": serverType, "client_id": f.Client1, "name": name, "hostname": hfrHostname, "model": model,
			"attributes": map[string]any{"cost_center": hfrCost, "owner": owner, "salary": hfrSalary}})
		if code != http.StatusCreated {
			t.Fatalf("POST %s: %d %s", name, code, body)
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	patch := func(id string, body map[string]any) {
		t.Helper()
		if code, resp := e.do(&admin, http.MethodPatch, "/api/v1/cis/"+id, body); code != http.StatusOK {
			t.Fatalf("PATCH %v: %d %s", body, code, resp)
		}
	}
	ci1 := create("wp727-ci-1", "M-OPEN", "ops-open")
	hfrExec(t, f, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type) VALUES ($1, $2, 'secret_inst', 'string')`,
		f.OrgA, ci1)
	patch(ci1, map[string]any{"attributes": map[string]any{"secret_inst": hfrInstance}})
	patch(ci1, map[string]any{"hostname": hfrHostname + ".2", "attributes": map[string]any{"cost_center": hfrCost + "-2", "owner": "ops-open-2"}})

	historyOf := func(p *identity.Principal, id string) (history.ListResponse, []byte) {
		t.Helper()
		code, body := e.do(p, http.MethodGet, "/api/v1/history/ci/"+id+"?limit=100", nil)
		if code != http.StatusOK {
			t.Fatalf("GET history of %s: %d %s", id, code, body)
		}
		var out history.ListResponse
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out, body
	}

	t.Run("history", func(t *testing.T) {
		all, body := historyOf(&admin, ci1)
		hfrShown(t, "admin history", body, append(hfrSecrets, `"field":"hostname"`)...)
		withAudit, body := historyOf(&readerAudit, ci1)
		hfrHidden(t, "history with audit:read", body, append(hfrSecrets, `"field":"hostname"`, `"field_name":"hostname"`)...)
		hfrShown(t, "history with audit:read", body, "M-OPEN", "ops-open-2", `"source":"audit"`, `"ci.created"`, `"ci.updated"`)
		if !withAudit.AuditIncluded || withAudit.Total != all.Total {
			t.Fatalf("history with audit:read: audit %v, total %d of %d", withAudit.AuditIncluded, withAudit.Total, all.Total)
		}
		for _, c := range withAudit.Data {
			if c.FieldName == "hostname" {
				t.Fatalf("entry of the unreadable hostname: %+v", c)
			}
			for _, fc := range c.FieldChanges {
				if fc.Field == "hostname" {
					t.Fatalf("field change of the unreadable hostname: %+v", c)
				}
			}
			if c.Source == history.SourceAudit {
				changes, _ := c.NewValue.(map[string]any)
				for _, member := range []string{"before", "after"} {
					if dto, ok := changes[member].(map[string]any); ok {
						if _, has := dto["hostname"]; has {
							t.Fatalf("audit %s with hostname: %+v", member, dto)
						}
						if dto["name"] != "wp727-ci-1" {
							t.Fatalf("audit %s without the structural name: %+v", member, dto)
						}
					}
				}
			}
		}
		plain, body := historyOf(&readerPlain, ci1)
		hfrHidden(t, "history without audit:read", body, hfrSecrets...)
		hfrShown(t, "history without audit:read", body, "M-OPEN", "ops-open-2")
		if plain.AuditIncluded || bytes.Contains(body, []byte(`"source":"audit"`)) {
			t.Fatalf("history without audit:read carries audit entries: %s", body)
		}
	})

	t.Run("state", func(t *testing.T) {
		code, body := e.do(&readerPlain, http.MethodGet, "/api/v1/cis/"+ci1+"/state", nil)
		if code != http.StatusOK {
			t.Fatalf("state: %d %s", code, body)
		}
		hfrHidden(t, "state", body, hfrSecrets...)
		var state history.StateSnapshot
		if err := json.Unmarshal(body, &state); err != nil {
			t.Fatal(err)
		}
		attrs, _ := state.Fields["attributes"].(map[string]any)
		if _, has := state.Fields["hostname"]; has || state.Fields["model"] != "M-OPEN" || attrs["owner"] != "ops-open-2" ||
			state.Fields["name"] != "wp727-ci-1" {
			t.Fatalf("state fields: %v", state.Fields)
		}
		if _, body = e.do(&admin, http.MethodGet, "/api/v1/cis/"+ci1+"/state", nil); !bytes.Contains(body, []byte(hfrHostname+".2")) {
			t.Fatalf("admin state lacks the hostname: %s", body)
		}
	})

	t.Run("snapshot job of the route", func(t *testing.T) {
		code, body := e.do(&readerAudit, http.MethodPost, "/api/v1/history/snapshots", map[string]any{"entity_type": "ci", "entity_id": ci1})
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if code != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil {
			t.Fatalf("POST snapshot: %d %s", code, body)
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			code, body = e.do(&readerAudit, http.MethodGet, "/api/v1/jobs/"+accepted.JobID+"/result", nil)
			if code == http.StatusOK || time.Now().After(deadline) {
				break
			}
		}
		if code != http.StatusOK {
			t.Fatalf("snapshot result: %d %s", code, body)
		}
		hfrHidden(t, "snapshot result", body, hfrSecrets...)
		hfrShown(t, "snapshot result", body, "M-OPEN", "ops-open-2")
	})

	t.Run("snapshot job of the queue", func(t *testing.T) {
		resolver := permission.NewFieldResolver(repos.Permission, nil, repos.FieldGrants, nil)
		params, _ := json.Marshal(history.SnapshotRequest{EntityType: "ci", EntityID: ci1, At: ptrTime(time.Now().UTC())})
		run := func(h *history.Handler, creator string) []byte {
			t.Helper()
			result, rerr := h.RunSnapshotJob(bg, &jobs.Job{OrganizationID: f.OrgA, CreatedBy: creator, Kind: history.SnapshotJobKind, Params: params})
			if rerr != nil {
				t.Fatalf("run snapshot job of %s: %v", creator, rerr)
			}
			raw, _ := json.Marshal(result)
			return raw
		}
		h := history.NewHandler(repos.History).WithCIs(server.HistoryCIs(&repos)).WithCreatorPolicy(server.SnapshotCreatorPolicy(&repos, resolver))
		body := run(h, userPlain)
		hfrHidden(t, "queued snapshot of the reader", body, hfrSecrets...)
		hfrShown(t, "queued snapshot of the reader", body, "M-OPEN", "ops-open-2")
		hfrShown(t, "queued snapshot of the administrator", run(h, userAdmin), hfrSecrets[0]+".2", hfrSalary, hfrInstance)
		// Without the creator's policy only the structural fields stay.
		body = run(history.NewHandler(repos.History).WithCIs(server.HistoryCIs(&repos)), userAdmin)
		hfrHidden(t, "queued snapshot without creator policy", body, append(hfrSecrets, "M-OPEN", "ops-open-2")...)
		hfrShown(t, "queued snapshot without creator policy", body, "wp727-ci-1")
	})

	t.Run("deleted CI", func(t *testing.T) {
		ci2 := create("wp727-ci-2", "M-DELETED", "ops-deleted")
		if code, body := e.do(&admin, http.MethodDelete, "/api/v1/cis/"+ci2, nil); code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", code, body)
		}
		var stored int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND old_value::text LIKE '%M-DELETED%'`, ci2).
			Scan(&stored); err != nil || stored == 0 {
			t.Fatalf("delete entry with the values: %d %v", stored, err)
		}
		// The type of the deleted CI is not known: under a restricted
		// policy only the structural fields stay (fail-closed), for the
		// administrator as well, whose policy knows the sensitive salary.
		for _, p := range []*identity.Principal{&readerAudit, &admin} {
			_, body := historyOf(p, ci2)
			hfrHidden(t, "history of the deleted CI", body, append(hfrSecrets, "M-DELETED", "ops-deleted")...)
			hfrShown(t, "history of the deleted CI", body, "wp727-ci-2", `"ci.deleted"`)
			if !strings.Contains(string(body), `"change_type":"delete"`) {
				t.Fatalf("history of the deleted CI lacks the delete: %s", body)
			}
		}
	})
}

func ptrTime(t time.Time) *time.Time { return &t }
