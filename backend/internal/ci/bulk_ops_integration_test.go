package ci_test

// The field operations of POST /cis/bulk against PostgreSQL (BLK-02,
// CI-08, OVR-03; WP-304, E-132):
//
//   - TestBulkOpsManualWritePath: under require_if_match a CI with a status
//     and an attribute override per PATCH takes bulk set_status and
//     set_fields without If-Match: the value changes, the override carries
//     the reason bulk and the caller as author, the version rises, every
//     CI gets exactly one audit entry and one event ci.changed; a CI
//     without a real change gets no override, audit entry or event; an
//     instance attribute in params.fields is 422 for the id with its field
//     and attributes._instance stays; 100 ids run as the job ci.bulk with
//     the reason bulk:<job_id>, whose report (GET /jobs/{id}/result) carries
//     code, type and fields; a CI of another client is 404 for the id.
//   - TestBulkOpsUniqueLocationOwner: through the production wiring
//     (ci.NewIndexingRepository) set_fields on a taken unique attribute is
//     409 with the attribute; set_location writes a location with its
//     override, an unknown location and a CI with an asset are 422 on
//     location_id; set_owner writes the owner without an override, a CI
//     with an asset and an unknown user are 422 on owner_user_id.
//   - TestBulkOpsTagsAndArchive: an unknown tag is 422 on params.tags for
//     the whole request and changes nothing; add_tags and remove_tags write
//     the tag list through the tag path (ci_change tags, audit, event,
//     version + 1, no override), a CI that already has the tag stays
//     untouched; archive sets inactive and the system tag archived, an
//     archived CI stays untouched; a failure of the tag step leaves the
//     status of the CI as it was (one transaction).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run 'TestBulkOps' ./internal/ci/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// failingTagWrites is the PostgreSQL repository whose tag write fails: the
// tag step of an archive breaks after its status write in the same
// transaction.
type failingTagWrites struct {
	*ci.PGRepository
}

func (failingTagWrites) ReplaceCITags(context.Context, string, string, []string, *int64) (*ci.Item, error) {
	return nil, errors.New("forced failure of the tag write")
}

// bulkOpsEnv wires the CI and job handlers on PostgreSQL as the server
// does: the PostgreSQL repository with audit and events behind the
// indexing decorator, the field resolver of the CI types, in-process bulk
// jobs.
type bulkOpsEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	jobSvc *jobs.Service
	user   string
	typeID string
	// ctx is a principal of client 1 with ci:write (the creator of jobs).
	ctx context.Context
}

// newBulkOpsEnv seeds the fixture and a CI type with the attributes note,
// inventory_no (unique in the organization) and its index; wrap (nil:
// none) replaces the repository the service writes through.
func newBulkOpsEnv(t *testing.T, tag string, wrap func(*ci.PGRepository) ci.Repository) *bulkOpsEnv {
	t.Helper()
	f := scopetest.Seed(t, tag)
	bg := context.Background()
	e := &bulkOpsEnv{t: t, f: f, user: f.AppUser(t, f.OrgA, "bulk-ops")}
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp304-dev", Name: "wp304-dev", IsObservable: true}
	if err := types.Create(orgCtx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	e.typeID = typ.ID
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	for _, d := range []*citype.AttributeDefinition{
		{CITypeID: typ.ID, Key: "note", DataType: "string"},
		{CITypeID: typ.ID, Key: "inventory_no", DataType: "string", Unique: true, UniqueScope: "org"},
	} {
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, d); err != nil {
			t.Fatalf("define %s: %v", d.Key, err)
		}
		if d.Unique {
			def := d
			t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(def.ID, true)) })
			if err := ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
				t.Fatalf("index %s: %v", d.Key, err)
			}
		}
	}

	scope := database.OrgWideScope(f.OrgA, e.user)
	scope.Clients = database.ScopeIDs(f.Client1)
	e.ctx = identity.WithPrincipal(tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user, ClientID: f.Client1}),
		identity.Principal{Subject: e.user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: []identity.Permission{identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}})

	e.jobSvc = jobs.NewService(jobs.NewPGRepository(f.App))
	e.jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	pg := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	var repo ci.Repository = pg
	if wrap != nil {
		repo = wrap(pg)
	}
	svc := ci.NewService(ci.NewIndexingRepository(repo, nopIndexer{})).WithFieldResolver(types).
		WithBulkJobs(e.jobSvc).WithCreatorPolicy(phase1CreatorPolicy)
	e.mux = chi.NewRouter()
	// The field policy source of the server router (WP-265).
	e.mux.Use(fieldPolicySource())
	ci.NewHandler(svc).RegisterRoutes(e.mux)
	jobs.NewAPIHandler(e.jobSvc).WithKind(ci.BulkJobKind, identity.PermCIWrite, nil).RegisterRoutes(e.mux)
	return e
}

// do sends a request as the principal of e.ctx.
func (e *bulkOpsEnv) do(method, path, ifMatch string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(e.ctx)
	r.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// cis inserts n active CIs of the test type of client (attributes attrs)
// and returns their ids.
func (e *bulkOpsEnv) cis(prefix, client string, n int, attrs map[string]any) []string {
	e.t.Helper()
	raw, _ := json.Marshal(attrs)
	if attrs == nil {
		raw = []byte(`{}`)
	}
	rows, err := e.f.Admin.Query(context.Background(), `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
		SELECT $1, $2, $3, $4 || g, 'active', $6::jsonb FROM generate_series(1, $5::int) g RETURNING id::text`,
		e.f.OrgA, client, e.typeID, prefix, n, raw)
	if err != nil {
		e.t.Fatalf("seed cis: %v", err)
	}
	ids, err := collectIDs(rows.Next, rows.Scan, rows.Close)
	if err != nil || len(ids) != n {
		e.t.Fatalf("seeded %d cis, want %d: %v", len(ids), n, err)
	}
	return ids
}

func collectIDs(next func() bool, scan func(...any) error, closeRows func()) ([]string, error) {
	defer closeRows()
	var ids []string
	for next() {
		var id string
		if err := scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (e *bulkOpsEnv) count(query string, args ...any) int {
	e.t.Helper()
	return countRows(e.t, e.f, query, args...)
}

func (e *bulkOpsEnv) text(query string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return out
}

func (e *bulkOpsEnv) version(id string) int {
	e.t.Helper()
	return e.count(`SELECT version FROM ci WHERE id = $1`, id)
}

// trail counts the audit entries and the events ci.changed of a CI.
func (e *bulkOpsEnv) trail(id string) (audits, changed int) {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1`, id),
		e.count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'`, id)
}

// overrides counts the protected overrides of a CI by field, author and
// reason.
func (e *bulkOpsEnv) overrides(id, field, reason string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = $2 AND override_author = $3
		AND override_reason = $4 AND protected AND override_at IS NOT NULL`, id, field, e.user, reason)
}

// bulk posts a bulk request and returns the report of a synchronous run
// (200) or, with wantJob, of the job (202) read from GET /jobs/{id}/result,
// and the job id.
func (e *bulkOpsEnv) bulk(body map[string]any, wantJob bool) (ci.BulkResult, string) {
	e.t.Helper()
	w := e.do(http.MethodPost, "/api/v1/cis/bulk", "", body)
	var res ci.BulkResult
	if !wantJob {
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
			e.t.Fatalf("bulk %v: %d %s, want 200 with the report", body["action"], w.Code, w.Body.String())
		}
		return res, ""
	}
	var acc struct {
		JobID string `json:"job_id"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &acc) != nil || acc.JobID == "" {
		e.t.Fatalf("bulk %v: %d %s, want 202 with the job", body["action"], w.Code, w.Body.String())
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		job, err := e.jobSvc.Get(e.ctx, e.f.OrgA, acc.JobID)
		if err == nil && job.Status.Terminal() {
			if job.Status != jobs.StatusSucceeded {
				e.t.Fatalf("bulk job ended %s: %s", job.Status, job.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("bulk job %s not done: %+v %v", acc.JobID, job, err)
		}
	}
	w = e.do(http.MethodGet, "/api/v1/jobs/"+acc.JobID+"/result", "", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		e.t.Fatalf("GET /jobs/%s/result: %d %s", acc.JobID, w.Code, w.Body.String())
	}
	return res, acc.JobID
}

// expectItems checks the report: ok for every id outside fail, the error
// of fail[id] (code, type /problems/<code>, fields) for the others, in the
// order of ids.
func expectItems(t *testing.T, res ci.BulkResult, ids []string, fail map[string]ci.BulkItemResult) {
	t.Helper()
	if res.Total != len(ids) || len(res.Items) != len(ids) || res.Failed != len(fail) || res.Succeeded != len(ids)-len(fail) {
		t.Fatalf("report: total %d, %d items, ok %d, failed %d; want %d ids, %d errors: %+v",
			res.Total, len(res.Items), res.Succeeded, res.Failed, len(ids), len(fail), res.Items)
	}
	for i, item := range res.Items {
		want, isErr := fail[ids[i]]
		switch {
		case item.ID != ids[i]:
			t.Errorf("item %d: id %s, want %s", i, item.ID, ids[i])
		case !isErr && (item.Result != "ok" || item.Code != http.StatusOK || item.Type != "" || item.Fields != nil):
			t.Errorf("item %s: %+v, want ok", item.ID, item)
		case isErr && (item.Result != "error" || item.Code != want.Code || item.Type != fmt.Sprintf("https://reticora.io/problems/%d", want.Code) ||
			!slices.Equal(item.Fields, want.Fields)):
			t.Errorf("item %s: %+v, want error %d with fields %v", item.ID, item, want.Code, want.Fields)
		}
	}
}

func TestBulkOpsManualWritePath(t *testing.T) {
	e := newBulkOpsEnv(t, "d4", nil)
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	ids := e.cis("wp304-manual-", e.f.Client1, 2, map[string]any{"note": "seed"})
	a, b := ids[0], ids[1]
	foreign := e.cis("wp304-foreign-", e.f.Client2, 1, nil)[0]
	if w := e.do(http.MethodPatch, "/api/v1/cis/"+a, `"1"`, map[string]any{"status": "maintenance", "attributes": map[string]any{"note": "patched"}}); w.Code != http.StatusOK {
		t.Fatalf("PATCH with overrides: %d %s", w.Code, w.Body.String())
	}

	type before struct{ version, audits, changed int }
	snap := func(ids ...string) map[string]before {
		out := map[string]before{}
		for _, id := range ids {
			au, ev := e.trail(id)
			out[id] = before{e.version(id), au, ev}
		}
		return out
	}
	// oneWrite: version + 1, one audit entry and one ci.changed per CI.
	oneWrite := func(step string, was map[string]before) {
		t.Helper()
		for id, w := range was {
			au, ev := e.trail(id)
			if v := e.version(id); v != w.version+1 || au != w.audits+1 || ev != w.changed+1 {
				t.Errorf("%s %s: version %d -> %d, audit +%d, events +%d; want +1 each", step, id, w.version, v, au-w.audits, ev-w.changed)
			}
		}
	}

	t.Run("set_status over an override, without If-Match under require_if_match", func(t *testing.T) {
		was := snap(a, b)
		res, _ := e.bulk(map[string]any{"ids": []string{a, foreign, b}, "status": "inactive"}, false)
		expectItems(t, res, []string{a, foreign, b}, map[string]ci.BulkItemResult{foreign: {Code: http.StatusNotFound}})
		for _, id := range []string{a, b} {
			if s := e.text(`SELECT status FROM ci WHERE id = $1`, id); s != "inactive" || e.overrides(id, "status", "bulk:"+res.BulkJobID) != 1 {
				t.Errorf("%s: status %s, %d bulk status overrides of the caller, want inactive and 1", id, s, e.overrides(id, "status", "bulk:"+res.BulkJobID))
			}
		}
		oneWrite("set_status", was)
		if s := e.text(`SELECT status FROM ci WHERE id = $1`, foreign); s != "active" {
			t.Errorf("CI of another client: status %s", s)
		}
	})

	t.Run("set_fields over an attribute override", func(t *testing.T) {
		was := snap(a, b)
		res, _ := e.bulk(map[string]any{"action": ci.BulkSetFields, "ids": []string{a, b},
			"params": map[string]any{"fields": map[string]any{"model": "wp304-model", "attributes": map[string]any{"note": "bulk"}}}}, false)
		expectItems(t, res, []string{a, b}, nil)
		for _, id := range []string{a, b} {
			if n := e.text(`SELECT attributes->>'note' || '|' || model FROM ci WHERE id = $1`, id); n != "bulk|wp304-model" {
				t.Errorf("%s after set_fields: %s", id, n)
			}
			if e.overrides(id, "note", "bulk:"+res.BulkJobID) != 1 || e.overrides(id, "model", "bulk:"+res.BulkJobID) != 1 {
				t.Errorf("%s: overrides of note %d, model %d with reason bulk by the caller, want 1 each", id,
					e.overrides(id, "note", "bulk:"+res.BulkJobID), e.overrides(id, "model", "bulk:"+res.BulkJobID))
			}
		}
		oneWrite("set_fields", was)
	})

	t.Run("an unchanged CI gets no override, audit entry or event", func(t *testing.T) {
		was := snap(a)
		stamp := e.text(`SELECT COALESCE(max(override_at)::text, '') FROM ci_field_value WHERE ci_id = $1`, a)
		res, _ := e.bulk(map[string]any{"action": ci.BulkSetFields, "ids": []string{a},
			"params": map[string]any{"fields": map[string]any{"model": "wp304-model", "attributes": map[string]any{"note": "bulk"}}}}, false)
		expectItems(t, res, []string{a}, nil)
		au, ev := e.trail(a)
		if au != was[a].audits || ev != was[a].changed || e.version(a) != was[a].version+1 {
			t.Errorf("unchanged CI: audit +%d, events +%d, version %d -> %d; want 0, 0, +1", au-was[a].audits, ev-was[a].changed, was[a].version, e.version(a))
		}
		if now := e.text(`SELECT COALESCE(max(override_at)::text, '') FROM ci_field_value WHERE ci_id = $1`, a); now != stamp {
			t.Errorf("unchanged CI: an override was written (%s -> %s)", stamp, now)
		}
	})

	device := e.cis("wp304-instance-", e.f.Client1, 1, nil)[0]
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type, sort_order)
		VALUES ($1, $2, 'rack_label', 'text', 1)`, e.f.OrgA, device); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(bg, `UPDATE ci SET attributes = attributes || '{"_instance": {"rack_label": "R1"}}' WHERE id = $1`, device); err != nil {
		t.Fatal(err)
	}
	instanceFail := map[string]ci.BulkItemResult{device: {Code: http.StatusUnprocessableEntity, Fields: []string{"attributes.rack_label"}}}

	t.Run("an instance attribute is 422 for the id, _instance stays", func(t *testing.T) {
		was := snap(device)
		res, _ := e.bulk(map[string]any{"action": ci.BulkSetFields, "ids": []string{b, device},
			"params": map[string]any{"fields": map[string]any{"attributes": map[string]any{"rack_label": "R9"}}}}, false)
		expectItems(t, res, []string{b, device}, instanceFail)
		if v := e.text(`SELECT attributes->'_instance'->>'rack_label' FROM ci WHERE id = $1`, device); v != "R1" || e.version(device) != was[device].version {
			t.Errorf("instance attribute after the refused bulk: %s, version %d", v, e.version(device))
		}
	})

	t.Run("100 ids run as the job ci.bulk with the reason bulk:<job_id>", func(t *testing.T) {
		many := append(e.cis("wp304-job-", e.f.Client1, ci.BulkJobFromIDs-1, nil), device)
		res, jobID := e.bulk(map[string]any{"action": ci.BulkSetFields, "ids": many,
			"params": map[string]any{"fields": map[string]any{"attributes": map[string]any{"note": "job", "rack_label": "R9"}}}}, true)
		expectItems(t, res, many, instanceFail)
		if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = ANY($1::uuid[]) AND field_name = 'note'
			AND override_reason = $2 AND override_author = $3`, many, "bulk:"+jobID, e.user); n != ci.BulkJobFromIDs-1 {
			t.Errorf("%d overrides with the reason bulk:<job_id> by the creator, want %d", n, ci.BulkJobFromIDs-1)
		}
		if raw := e.text(`SELECT params::text FROM job WHERE id = $1`, jobID); bytes.Contains([]byte(raw), []byte("permission")) ||
			bytes.Contains([]byte(raw), []byte("ci:write")) {
			t.Errorf("the job parameters carry rights: %s", raw)
		}
	})
}

func TestBulkOpsUniqueLocationOwner(t *testing.T) {
	e := newBulkOpsEnv(t, "d5", nil)
	bg := context.Background()

	t.Run("set_fields on a taken unique attribute is 409 with the attribute", func(t *testing.T) {
		holder := e.cis("wp304-holder-", e.f.Client2, 1, map[string]any{"inventory_no": "INV-304"})[0]
		ids := e.cis("wp304-unique-", e.f.Client1, 2, nil)
		res, _ := e.bulk(map[string]any{"action": ci.BulkSetFields, "ids": []string{ids[0]},
			"params": map[string]any{"fields": map[string]any{"attributes": map[string]any{"inventory_no": "INV-304"}}}}, false)
		expectItems(t, res, []string{ids[0]}, map[string]ci.BulkItemResult{ids[0]: {Code: http.StatusConflict, Fields: []string{"inventory_no"}}})
		if n := e.count(`SELECT count(*) FROM ci WHERE attributes->>'inventory_no' = 'INV-304'`); n != 1 {
			t.Errorf("%d CIs hold INV-304, want only %s", n, holder)
		}
		res, _ = e.bulk(map[string]any{"action": ci.BulkSetFields, "ids": []string{ids[1]},
			"params": map[string]any{"fields": map[string]any{"attributes": map[string]any{"inventory_no": "INV-305"}}}}, false)
		expectItems(t, res, []string{ids[1]}, nil)
	})

	tree := locations.NewPGRepository(e.f.App)
	site, err := tree.Create(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: e.f.Client1, Name: "wp304-site"})
	if err != nil {
		t.Fatal(err)
	}
	loose := e.cis("wp304-loose-", e.f.Client1, 1, nil)[0]
	linked := e.cis("wp304-linked-", e.f.Client1, 1, nil)[0]
	asset := e.f.Asset(t, e.f.OrgA, e.f.Client1, "wp304-asset")
	if _, err = e.f.Admin.Exec(bg, `UPDATE ci SET asset_id = $2 WHERE id = $1`, linked, asset); err != nil {
		t.Fatal(err)
	}

	t.Run("set_location", func(t *testing.T) {
		res, _ := e.bulk(map[string]any{"action": ci.BulkSetLocation, "ids": []string{loose, linked}, "params": map[string]any{"location_id": site.ID}}, false)
		expectItems(t, res, []string{loose, linked}, map[string]ci.BulkItemResult{linked: {Code: http.StatusUnprocessableEntity, Fields: []string{"location_id"}}})
		if got := e.text(`SELECT COALESCE(location_id::text, '') FROM ci WHERE id = $1`, loose); got != site.ID || e.overrides(loose, "location_id", "bulk:"+res.BulkJobID) != 1 {
			t.Errorf("CI without asset: location %q, %d overrides, want %s and 1", got, e.overrides(loose, "location_id", "bulk:"+res.BulkJobID), site.ID)
		}
		if got := e.text(`SELECT COALESCE(location_id::text, '') FROM ci WHERE id = $1`, linked); got != "" {
			t.Errorf("CI with asset took the location %s", got)
		}
		res, _ = e.bulk(map[string]any{"action": ci.BulkSetLocation, "ids": []string{loose}, "params": map[string]any{"location_id": e.f.ID()}}, false)
		expectItems(t, res, []string{loose}, map[string]ci.BulkItemResult{loose: {Code: http.StatusUnprocessableEntity, Fields: []string{"location_id"}}})
	})

	t.Run("set_owner", func(t *testing.T) {
		res, _ := e.bulk(map[string]any{"action": ci.BulkSetOwner, "ids": []string{loose, linked}, "params": map[string]any{"owner_user_id": e.user}}, false)
		expectItems(t, res, []string{loose, linked}, map[string]ci.BulkItemResult{linked: {Code: http.StatusUnprocessableEntity, Fields: []string{"owner_user_id"}}})
		if got := e.text(`SELECT COALESCE(owner_user_id::text, '') FROM ci WHERE id = $1`, loose); got != e.user {
			t.Errorf("owner %q, want %s", got, e.user)
		}
		if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name LIKE 'owner%'`, loose); n != 0 {
			t.Errorf("%d owner overrides, want none (derived field)", n)
		}
		res, _ = e.bulk(map[string]any{"action": ci.BulkSetOwner, "ids": []string{loose}, "params": map[string]any{"owner_user_id": e.f.ID()}}, false)
		expectItems(t, res, []string{loose}, map[string]ci.BulkItemResult{loose: {Code: http.StatusUnprocessableEntity, Fields: []string{"owner_user_id"}}})
		res, _ = e.bulk(map[string]any{"action": ci.BulkSetOwner, "ids": []string{loose}, "params": map[string]any{"owner_user_id": ""}}, false)
		expectItems(t, res, []string{loose}, nil)
		if got := e.text(`SELECT COALESCE(owner_user_id::text, '') FROM ci WHERE id = $1`, loose); got != "" {
			t.Errorf("owner after clearing: %q", got)
		}
	})
}

func TestBulkOpsTagsAndArchive(t *testing.T) {
	e := newBulkOpsEnv(t, "d6", nil)
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'dmz', '{"en": "DMZ"}')`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	ids := e.cis("wp304-tags-", e.f.Client1, 2, nil)
	a, b := ids[0], ids[1]
	foreign := e.cis("wp304-tags-foreign-", e.f.Client2, 1, nil)[0]
	tags := func(id string) string {
		return e.text(`SELECT COALESCE(string_agg(t.key, ',' ORDER BY t.key), '') FROM ci_tag x JOIN tag t ON t.id = x.tag_id WHERE x.ci_id = $1`, id)
	}

	t.Run("an unknown tag is 422 for the whole request", func(t *testing.T) {
		va := e.version(a)
		w := e.do(http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": ci.BulkAddTags, "ids": ids, "params": map[string]any{"tags": []string{"dmz", "nope"}}})
		if !violationOn(t, w.Code, w.Body.String(), "params.tags") {
			t.Errorf("unknown tag: %d %s, want 422 on params.tags", w.Code, w.Body.String())
		}
		if tags(a) != "" || e.version(a) != va {
			t.Errorf("the refused request changed the CI: tags %q version %d", tags(a), e.version(a))
		}
	})

	t.Run("add_tags and remove_tags through the tag path", func(t *testing.T) {
		if _, err := e.f.Admin.Exec(bg, `INSERT INTO ci_tag (organization_id, ci_id, tag_id) SELECT $1, $2, id FROM tag WHERE organization_id = $1 AND key = 'dmz'`,
			e.f.OrgA, b); err != nil {
			t.Fatal(err)
		}
		va, vb := e.version(a), e.version(b)
		auA, evA := e.trail(a)
		auB, evB := e.trail(b)
		res, _ := e.bulk(map[string]any{"action": ci.BulkAddTags, "ids": []string{a, foreign, b}, "params": map[string]any{"tags": []string{"dmz"}}}, false)
		expectItems(t, res, []string{a, foreign, b}, map[string]ci.BulkItemResult{foreign: {Code: http.StatusNotFound}})
		if tags(a) != "dmz" || tags(b) != "dmz" || tags(foreign) != "" {
			t.Errorf("tags after add_tags: %q %q, foreign %q", tags(a), tags(b), tags(foreign))
		}
		au, ev := e.trail(a)
		if e.version(a) != va+1 || au != auA+1 || ev != evA+1 ||
			e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'tags'`, a) != 1 {
			t.Errorf("tagged CI: version %d -> %d, audit +%d, events +%d", va, e.version(a), au-auA, ev-evA)
		}
		if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1`, a); n != 0 {
			t.Errorf("%d field values (overrides) for a tag change, want none", n)
		}
		au, ev = e.trail(b)
		if e.version(b) != vb || au != auB || ev != evB {
			t.Errorf("CI that had the tag: version %d -> %d, audit +%d, events +%d; want unchanged", vb, e.version(b), au-auB, ev-evB)
		}
		res, _ = e.bulk(map[string]any{"action": ci.BulkRemoveTags, "ids": ids, "params": map[string]any{"tags": []string{"dmz"}}}, false)
		expectItems(t, res, ids, nil)
		if tags(a) != "" || tags(b) != "" {
			t.Errorf("tags after remove_tags: %q %q", tags(a), tags(b))
		}
	})

	t.Run("archive sets inactive and the tag archived", func(t *testing.T) {
		res, _ := e.bulk(map[string]any{"action": ci.BulkArchive, "ids": []string{a, foreign}}, false)
		expectItems(t, res, []string{a, foreign}, map[string]ci.BulkItemResult{foreign: {Code: http.StatusNotFound}})
		if s := e.text(`SELECT status FROM ci WHERE id = $1`, a); s != "inactive" || tags(a) != "archived" || e.overrides(a, "status", "bulk:"+res.BulkJobID) != 1 {
			t.Errorf("archived CI: status %s, tags %q, %d status overrides", s, tags(a), e.overrides(a, "status", "bulk:"+res.BulkJobID))
		}
		va := e.version(a)
		au, ev := e.trail(a)
		res, _ = e.bulk(map[string]any{"action": ci.BulkArchive, "ids": []string{a}}, false)
		expectItems(t, res, []string{a}, nil)
		au2, ev2 := e.trail(a)
		if e.version(a) != va || au2 != au || ev2 != ev {
			t.Errorf("archiving an archived CI wrote: version %d -> %d, audit +%d, events +%d", va, e.version(a), au2-au, ev2-ev)
		}
	})

	t.Run("a failed tag step leaves the status", func(t *testing.T) {
		broken := newBulkOpsEnv(t, "d3", func(pg *ci.PGRepository) ci.Repository { return failingTagWrites{pg} })
		id := broken.cis("wp304-atomic-", broken.f.Client1, 1, nil)[0]
		v := broken.version(id)
		res, _ := broken.bulk(map[string]any{"action": ci.BulkArchive, "ids": []string{id}}, false)
		if res.Failed != 1 || res.Items[0].Result != "error" {
			t.Fatalf("archive with a failing tag step: %+v, want the id failed", res.Items)
		}
		if s := broken.text(`SELECT status FROM ci WHERE id = $1`, id); s != "active" || broken.version(id) != v ||
			broken.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1`, id) != 0 {
			t.Errorf("after the failed archive: status %s, version %d -> %d; want active and unchanged", s, v, broken.version(id))
		}
	})
}
