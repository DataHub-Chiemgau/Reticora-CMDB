package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// keyImport is the CI import stack of WP-302 behind the auth, tenant and
// route audit middleware of a request with X-API-Key (WP-779): the API key
// service validates the request and reloads the key for the run like the
// router wires it, the CI writer resolves field rights of service
// accounts, and the runs go through the job queue (processed by the test).
type keyImport struct {
	*pgCI
	keys  *identity.PGAPIKeyStore
	api   chi.Router
	queue *jobs.Queue
}

func newKeyImport(t *testing.T) *keyImport {
	t.Helper()
	c := newPGCI(t)
	accounts := user.ServiceAccountAccess{Repo: user.NewPGServiceAccounts(c.f.App)}
	k := &keyImport{pgCI: c, keys: identity.NewPGAPIKeyStore(c.f.App)}
	apiKeys := identity.NewAPIKeyServiceWithStore(k.keys).WithOwnerAccess(permission.NewPGRepository(c.f.App)).
		WithServiceAccountAccess(accounts)
	c.h.WithAPIKeys(apiKeys)
	svc := ci.NewServiceWithLimits(c.cis, nil).WithFieldResolver(c.types)
	policies := permission.NewFieldResolver(permission.NewPGRepository(c.f.App), accounts, permission.NewPGFieldGrants(c.f.App), nil)
	c.h.WithWriter(NewCIWriter(svc, c.disc, c.ovr, policies).WithInstanceFields(c.types).WithFields(c.types).WithLocations(c.locs))
	k.queue = jobs.NewQueue(c.jobs, nil)
	k.queue.Handle(jobs.Kind{Name: JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true}, c.h.RunJob)
	c.h.WithQueue(k.queue)
	k.api = chi.NewRouter()
	k.api.Use(middleware.RequestID, middleware.AuthMiddlewareWithAPIKeys(nil, apiKeys), middleware.TenantMiddleware)
	k.api.With(middleware.AuditRoute(http.MethodPost, "/api/v1/imports", audit.NewPGRouteAuditor(c.f.App), middleware.AuditIntent, "")).
		Post("/api/v1/imports", c.h.Start)
	return k
}

// key stores an API key of owner (bound to account when set, with the
// scope its creator held) with perms and returns the key and its prefix.
func (k *keyImport) key(t *testing.T, owner, account string, creator map[identity.Permission]identity.Scope, perms ...identity.Permission) (raw, prefix string) {
	t.Helper()
	generated, err := identity.GenerateAPIKey(identity.APIKeyEnvironmentLive)
	if err != nil {
		t.Fatal(err)
	}
	scope := database.OrgWideScope(k.f.OrgA, owner)
	ctx := database.ContextWithTenantScope(tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: k.f.OrgA, UserID: owner}), &scope)
	stored := &identity.StoredAPIKey{OrganizationID: k.f.OrgA, Name: "wp779-" + generated.KeyPrefix, KeyHash: generated.KeyHash,
		KeyPrefix: generated.KeyPrefix, Environment: generated.Environment, Permissions: perms, CreatedBy: owner, CreatorScope: creator}
	if account != "" {
		stored.ServiceAccountID = &account
	}
	if _, err = k.keys.Create(ctx, stored); err != nil {
		t.Fatalf("api key: %v", err)
	}
	return generated.Plaintext, generated.KeyPrefix
}

// account stores an active service account created by the import user
// with an org-wide role of perms and returns the account and the role.
func (k *keyImport) account(t *testing.T, name string, perms ...identity.Permission) (accountID, roleID string) {
	t.Helper()
	bg := context.Background()
	accountID, roleID = k.f.ID(), k.f.ID()
	raw, _ := json.Marshal(perms)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO service_account (id, organization_id, name, created_by) VALUES ($1, $2, $3, $4)`, []any{accountID, k.f.OrgA, name, k.user}},
		{`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, $4::jsonb)`, []any{roleID, k.f.OrgA, name, string(raw)}},
		{`INSERT INTO service_account_role (organization_id, service_account_id, role_id) VALUES ($1, $2, $3)`, []any{k.f.OrgA, accountID, roleID}},
	} {
		if _, err := k.f.Admin.Exec(bg, q.sql, q.args...); err != nil {
			t.Fatalf("seed service account %s: %v", name, err)
		}
	}
	return accountID, roleID
}

// ciProfile stores a CI profile that maps every column of header onto the
// target of its name.
func (k *keyImport) ciProfile(t *testing.T, header string) string {
	t.Helper()
	cols := map[string]ColumnMapping{}
	for _, target := range strings.Split(header, ";") {
		cols[target] = ColumnMapping{Target: target}
	}
	p := &Profile{OrganizationID: k.f.OrgA, Name: "wp779-" + k.f.ID(), Entity: "ci", Format: FormatCSV, Mode: "both",
		Mapping: Mapping{Columns: cols}}
	if err := k.profile.Create(k.f.OrgCtx(k.f.OrgA), p); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return p.ID
}

// post starts an import of the rows with the API key; it returns the
// answer and the request id of the request.
func (k *keyImport) post(t *testing.T, apiKey, header string, rows ...string) *httptest.ResponseRecorder {
	t.Helper()
	docID := k.document(t, header+"\n"+strings.Join(rows, "\n")+"\n")
	body := fmt.Sprintf(`{"document_id":%q,"profile_id":%q}`, docID, k.ciProfile(t, header))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/imports", strings.NewReader(body))
	r.Header.Set("X-API-Key", apiKey)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	k.api.ServeHTTP(w, r)
	return w
}

// launch starts the import and returns its job id and request id.
func (k *keyImport) launch(t *testing.T, apiKey, header string, rows ...string) (jobID, requestID string) {
	t.Helper()
	w := k.post(t, apiKey, header, rows...)
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted.JobID, w.Header().Get("X-Request-ID")
}

// process runs the import job like a queue worker and returns it ended.
func (k *keyImport) process(t *testing.T, jobID string) *jobs.Job {
	t.Helper()
	data, _ := json.Marshal(jobs.Message{OrganizationID: k.f.OrgA, JobID: jobID, Kind: JobKind})
	k.queue.Process(context.Background(), &memDelivery{data: data})
	return k.wait(t, jobID)
}

// serials counts the CIs of organization A with the serial number.
func (k *keyImport) serials(t *testing.T, serial string) int {
	t.Helper()
	return k.count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2`, k.f.OrgA, serial)
}

// TestImportWithAPIKey covers IMP-IO-01 and AUT-04 (WP-779, E-145): an
// import started with an API key runs with the rights the key grants when
// the run begins (the intersection of the key's and its owner's current
// rights), reloaded from the identity reference in the job parameters: a
// key without ci:write fails the run, a key revoked after the start too,
// a job without reference fails; the rows are audited with source import,
// the request id of the start and the owner as actor, and carry the
// provenance import with rank 95, while the request itself is audited
// with source api.
func TestImportWithAPIKey(t *testing.T) {
	k := newKeyImport(t)
	header := "ci_type;name;serial_number;manufacturer"
	full, prefix := k.key(t, k.user, "", nil, identity.PermImportRun, identity.PermCIWrite)

	// A valid key: the rows are written as import of the owner.
	jobID, requestID := k.launch(t, full, header, "server;k779-ok;K779-OK;Key-Vendor")
	if requestID == "" {
		t.Fatal("no request id")
	}
	job, err := k.jobs.Get(k.f.OrgCtx(k.f.OrgA), k.f.OrgA, jobID)
	if err != nil {
		t.Fatal(err)
	}
	var params JobParams
	if err = json.Unmarshal(job.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Starter == nil || params.Starter.Type != identity.PrincipalTypeAPIKey || params.Starter.KeyPrefix != prefix ||
		params.Starter.Subject != k.user || params.RequestID != requestID || params.Source != audit.SourceAPI || job.CreatedBy != k.user {
		t.Fatalf("job by %s with params %s", job.CreatedBy, job.Params)
	}
	for _, leak := range []string{"permission", "scope", string(identity.PermCIWrite)} {
		if strings.Contains(string(job.Params), leak) {
			t.Fatalf("the job parameters hold %q: %s", leak, job.Params)
		}
	}
	if done := k.process(t, jobID); done.Status != jobs.StatusSucceeded || resultOf(t, done).Created != 1 {
		t.Fatalf("run with the key %s %q %s", done.Status, done.Error, done.Result)
	}
	var created string
	if err = k.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'K779-OK'`,
		k.f.OrgA).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if n := k.count(t, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND source = 'import' AND actor_id = $2::uuid AND request_id = $3`,
		created, k.user, requestID); n != 1 {
		t.Errorf("%d import audit entries of the CI with the request and the owner", n)
	}
	if n := k.count(t, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND source <> 'import'`, created); n != 0 {
		t.Errorf("%d audit entries of the CI with another source", n)
	}
	if n := k.count(t, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND discovered_source = 'import' AND discovered_rank = 95`,
		created); n < 2 {
		t.Errorf("%d fields with provenance import rank 95", n)
	}
	if n := k.count(t, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND discovered_source IS DISTINCT FROM 'import'`, created); n != 0 {
		t.Errorf("%d fields with another provenance", n)
	}
	if n := k.count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id = $2 AND source = 'api' AND actor_id = $3::uuid`,
		k.f.OrgA, requestID, k.user); n != 1 {
		t.Errorf("%d api audit entries of the request", n)
	}
	if n := k.count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id = $2 AND source NOT IN ('api', 'import')`,
		k.f.OrgA, requestID); n != 0 {
		t.Errorf("%d audit entries of the request with another source", n)
	}

	// A key without ci:write, whose owner holds it, cannot start an import
	// (403) and fails a run it started before losing the right.
	readOnly, _ := k.key(t, k.user, "", nil, identity.PermImportRun)
	if w := k.post(t, readOnly, header, "server;k779-ro;K779-RO;V"); w.Code != http.StatusForbidden {
		t.Fatalf("start without ci:write: %d %s", w.Code, w.Body.String())
	}
	dropped, droppedPrefix := k.key(t, k.user, "", nil, identity.PermImportRun, identity.PermCIWrite)
	jobID, _ = k.launch(t, dropped, header, "server;k779-drop;K779-DROP;V")
	if _, err = k.f.Admin.Exec(context.Background(), `UPDATE api_key SET permissions = '{import:run}' WHERE key_prefix = $1`, droppedPrefix); err != nil {
		t.Fatal(err)
	}
	if done := k.process(t, jobID); done.Status != jobs.StatusFailed || errorCode(done.Error) != CodeForbidden {
		t.Fatalf("run of a key without ci:write %s %q", done.Status, done.Error)
	}
	if n := k.serials(t, "K779-DROP"); n != 0 {
		t.Fatalf("%d CIs written without ci:write of the key", n)
	}

	// A key revoked after the start fails the run before its first row.
	revoked, revokedPrefix := k.key(t, k.user, "", nil, identity.PermImportRun, identity.PermCIWrite)
	jobID, _ = k.launch(t, revoked, header, "server;k779-rev;K779-REV;V")
	if _, err = k.f.Admin.Exec(context.Background(), `UPDATE api_key SET revoked_at = now() WHERE key_prefix = $1`, revokedPrefix); err != nil {
		t.Fatal(err)
	}
	if done := k.process(t, jobID); done.Status != jobs.StatusFailed || errorCode(done.Error) != CodeAPIKeyInvalid {
		t.Fatalf("run of a revoked key %s %q", done.Status, done.Error)
	}
	if n := k.serials(t, "K779-REV"); n != 0 {
		t.Fatalf("%d CIs written with a revoked key", n)
	}
	if !strings.Contains(k.report(t, jobID), ";"+CodeAPIKeyInvalid+";") {
		t.Fatalf("report %q", k.report(t, jobID))
	}

	// A job without identity reference fails (fail-closed).
	docID := k.document(t, header+"\nserver;k779-none;K779-NONE;V\n")
	p := JobParams{DocumentID: docID, DocumentVersion: 1, ProfileID: k.ciProfile(t, header), Mode: "both",
		ScopeParams: jobs.ScopeParams{RequestID: "req-779", Source: audit.SourceAPI}}
	bare, _, err := k.jobs.Enqueue(k.ctx(), k.f.OrgA, JobKind, p, jobs.EnqueueOptions{CreatedBy: k.user})
	if err != nil {
		t.Fatal(err)
	}
	if done := k.process(t, bare.ID); done.Status != jobs.StatusFailed || errorCode(done.Error) != CodePrincipalMissing {
		t.Fatalf("run without reference %s %q", done.Status, done.Error)
	}
	if n := k.serials(t, "K779-NONE"); n != 0 {
		t.Fatalf("%d CIs written without reference", n)
	}
}

// TestImportWithAPIKeyScopesAndFieldRights: a run started with an API key
// keeps the key's limits (WP-779, AUT-04): a service account key whose
// role may only read a field gets a row error for that field; the key of
// a client-bound owner refuses rows of another client; a service account
// key bound with the creator scope of WP-668 writes only rows in its
// creator's scope.
func TestImportWithAPIKeyScopesAndFieldRights(t *testing.T) {
	k := newKeyImport(t)
	bg := context.Background()
	perms := []identity.Permission{identity.PermImportRun, identity.PermCIWrite}
	all := identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}

	// Field rights of the service account's role.
	account, role := k.account(t, "wp779-fields", perms...)
	if _, err := k.f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'core.model', 'read')`,
		k.f.OrgA, role); err != nil {
		t.Fatal(err)
	}
	accountKey, _ := k.key(t, k.user, account, map[identity.Permission]identity.Scope{identity.PermImportRun: all, identity.PermCIWrite: all}, perms...)
	jobID, _ := k.launch(t, accountKey, "ci_type;name;serial_number;model", "server;k779-sa;K779-SA;M-1")
	if done := k.process(t, jobID); done.Status != jobs.StatusSucceeded {
		t.Fatalf("run of the service account %s %q", done.Status, done.Error)
	}
	res, report := k.finish(t, jobID)
	if res.Failed != 1 || !hasLine(report, 2, "model", CodeFieldForbidden) || k.serials(t, "K779-SA") != 0 {
		t.Fatalf("field without write right of the service account: %+v %v", res, report)
	}

	// The key of an owner bound to client 1.
	owner := k.f.AppUser(t, k.f.OrgA, "wp779-client")
	clientRole := k.f.ID()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp779-client', 'client', '["import:run","ci:write"]')`,
			[]any{clientRole, k.f.OrgA}},
		{`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
			[]any{k.f.OrgA, owner, clientRole, k.f.Client1}},
	} {
		if _, err := k.f.Admin.Exec(bg, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	clientKey, _ := k.key(t, owner, "", nil, perms...)
	header := "ci_type;name;serial_number;client_id"
	jobID, _ = k.launch(t, clientKey, header, "server;k779-c1;K779-C1;"+k.f.Client1, "server;k779-c2;K779-C2;"+k.f.Client2)
	k.process(t, jobID)
	res, report = k.finish(t, jobID)
	if res.Created != 1 || res.Failed != 1 || !hasLine(report, 3, CIClientTarget, CodeOutOfScope) ||
		k.serials(t, "K779-C1") != 1 || k.serials(t, "K779-C2") != 0 {
		t.Fatalf("client-bound owner: %+v %v", res, report)
	}

	// A service account key limited to its creator's scope (WP-668): the
	// account holds the rights org-wide, its creator held them in client 1.
	wide, _ := k.account(t, "wp779-creator", perms...)
	creator := identity.ResolveAccess([]identity.Grant{{Permissions: perms, Clients: []string{k.f.Client1}}})
	ceiling := map[identity.Permission]identity.Scope{identity.PermImportRun: creator.Scope, identity.PermCIWrite: creator.Scope}
	boundKey, _ := k.key(t, k.user, wide, ceiling, perms...)
	jobID, _ = k.launch(t, boundKey, header, "server;k779-b1;K779-B1;"+k.f.Client1, "server;k779-b2;K779-B2;"+k.f.Client2)
	k.process(t, jobID)
	res, report = k.finish(t, jobID)
	if res.Created != 1 || res.Failed != 1 || !hasLine(report, 3, CIClientTarget, CodeOutOfScope) ||
		k.serials(t, "K779-B1") != 1 || k.serials(t, "K779-B2") != 0 {
		t.Fatalf("service account within its creator's scope: %+v %v", res, report)
	}
}
