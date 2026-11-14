package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// The field locks of lifecycle states on PostgreSQL (LCY-04, WP-1103,
// E-232 option 1): an organization model for a CI type with the state
// retired, which locks serial_number (a typed column) and asset_tag (an
// attribute). The lock is a readonly catalog rule derived from the model
// when the rules of the CI are resolved; manual writes, bulk, import and
// set_ci_field are refused with read_only, discovery keeps its value in
// the provenance and opens the review override_conflict.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestLockedField|TestTransitionThenLock|TestLockedFieldsValidation' ./internal/ci/

type lockEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	ctx    context.Context
	types  *citype.PGRepository
	repo   *ci.PGRepository
	svc    *ci.Service
	overs  *override.PGRepository
	models *chi.Mux
	cis    *chi.Mux
	// transitions moves the lifecycle state of a CI (lifecycle.Service).
	transitions *lifecycle.Service
	typeID      string
	typeKey     string
	modelID     string
	user        string
}

// lockModel is the organization model of the tests: in_stock (initial) →
// deployed ⇄ retired; retired locks serial_number and asset_tag.
const lockModel = `{"key":"%s","name":"Locks","applies_to":"ci","states":[
	{"key":"in_stock","label":"In stock","is_initial":true,"sort_order":1},
	{"key":"deployed","label":"Deployed","sort_order":2},
	{"key":"retired","label":"Retired","sort_order":3,"locked_fields":["serial_number","asset_tag"]}],
	"transitions":[{"from_state_key":"in_stock","to_state_key":"deployed","key":"deploy"},
	{"from_state_key":"deployed","to_state_key":"retired","key":"retire"},
	{"from_state_key":"retired","to_state_key":"deployed","key":"redeploy"}]}`

func newLockEnv(t *testing.T) *lockEnv {
	t.Helper()
	f := scopetest.Seed(t, "5c")
	bg := context.Background()
	e := &lockEnv{t: t, f: f, user: f.AppUser(t, f.OrgA, "lock-editor")}
	scope := database.OrgWideScope(f.OrgA, e.user)
	e.ctx = audit.WithRequestInfo(identity.WithPrincipal(tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user}),
		identity.Principal{Subject: e.user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()}),
		audit.RequestInfo{RequestID: "wp1103", Source: audit.SourceUI})
	e.types = citype.NewPGRepository(f.App)
	e.repo = ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Cancellable: true})
	e.svc = ci.NewService(e.repo).WithFieldResolver(e.types).WithBulkJobs(jobSvc).WithCreatorPolicy(phase1CreatorPolicy)
	e.overs = override.NewPGRepository(f.App)

	lc := lifecycle.NewPGRepository(f.App)
	e.transitions = lifecycle.NewService(lc, lifecycle.NewPGStateStore(f.App), nil)
	e.models = chi.NewRouter()
	lifecycle.NewHandler(lc, e.transitions, lifecycle.NewResolver(f.App)).RegisterRoutes(e.models)
	e.cis = chi.NewRouter()
	e.cis.Use(fieldPolicySource())
	ci.NewHandler(e.svc).RegisterRoutes(e.cis)

	rec := e.call(e.models, http.MethodPost, "/api/v1/lifecycle-models", fmt.Sprintf(lockModel, "wp1103_locks"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}
	var model lifecycle.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	e.modelID = model.ID
	e.typeKey = "wp1103_device"
	typ := &citype.Type{OrganizationID: f.OrgA, Key: e.typeKey, Name: "wp1103 device", IsObservable: true, LifecycleModelID: model.ID}
	if err := e.types.Create(e.ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	e.typeID = typ.ID
	if err := e.types.CreateAttributeDefinition(e.ctx, f.OrgA, &citype.AttributeDefinition{CITypeID: typ.ID, Key: "asset_tag", DataType: "string"}); err != nil {
		t.Fatalf("define asset_tag: %v", err)
	}
	return e
}

func (e *lockEnv) call(mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// device inserts a CI of the type in state with serial number SN-1 (or
// serial, for a second CI of a test), hardware UUID uuid and asset_tag
// AT-1, observed by SNMP.
func (e *lockEnv) device(name, state, uuid string, serial ...string) string {
	e.t.Helper()
	id := e.f.ID()
	sn := "SN-1"
	if len(serial) > 0 {
		sn = serial[0]
	}
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, ci_type_id, name, status, serial_number, hardware_uuid,
		model, attributes, lifecycle_state, discovery_source) VALUES ($1, $2, $3, $4, 'active', $7, NULLIF($5, '')::uuid, 'M-1', '{"asset_tag":"AT-1"}', $6, 'snmp')`,
		id, e.f.OrgA, e.typeID, name, uuid, state, sn); err != nil {
		e.t.Fatalf("insert %s: %v", name, err)
	}
	return id
}

func (e *lockEnv) column(id, col string) string {
	e.t.Helper()
	var v string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(`+col+`::text, '') FROM ci WHERE id = $1`, id).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// readOnly reports whether a 422 answer names field with code read_only.
func readOnly(t *testing.T, rec *httptest.ResponseRecorder, field string) bool {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		return false
	}
	var body struct {
		Violations []ci.Violation `json:"violations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	for _, v := range body.Violations {
		if v.Field == field && v.Code == ci.CodeReadOnly {
			return true
		}
	}
	return false
}

// TestLockedFieldRefusedOnPatch: PATCH serial_number in the state retired
// is 422 read_only, in deployed 200; the field catalog shows the field
// read-only in retired.
func TestLockedFieldRefusedOnPatch(t *testing.T) {
	e := newLockEnv(t)
	id := e.device("lock-patch", "retired", "")
	rec := e.call(e.cis, http.MethodPatch, "/api/v1/cis/"+id, `{"serial_number":"SN-2"}`)
	if !readOnly(t, rec, "serial_number") {
		t.Fatalf("PATCH in retired: %d %s, want 422 read_only on serial_number", rec.Code, rec.Body.String())
	}
	if rec = e.call(e.cis, http.MethodPatch, "/api/v1/cis/"+id, `{"attributes":{"asset_tag":"AT-2"}}`); !readOnly(t, rec, "asset_tag") {
		t.Errorf("PATCH asset_tag in retired: %d %s, want 422 read_only", rec.Code, rec.Body.String())
	}
	if rec = e.call(e.cis, http.MethodPatch, "/api/v1/cis/"+id, `{"model":"M-2"}`); rec.Code != http.StatusOK {
		t.Errorf("PATCH of an unlocked field in retired: %d %s, want 200", rec.Code, rec.Body.String())
	}
	form, err := citype.NewFormResolver(e.types, nil).ResolveForm(e.ctx, e.f.OrgA, citype.FormRequest{CIID: id})
	if err != nil {
		t.Fatal(err)
	}
	locked := false
	for _, s := range form.Sections {
		for _, fld := range s.Fields {
			if fld.Name == "asset_tag" {
				locked = fld.State.ReadOnly
			}
		}
	}
	if !locked {
		t.Errorf("field catalog in retired: asset_tag not read-only")
	}
	if !slices.ContainsFunc(form.Rules, func(r citype.FormRule) bool {
		return r.Key == citype.LifecycleLockPrefix+"wp1103_locks:retired:asset_tag" && r.Kind == "readonly"
	}) {
		t.Errorf("rules of the form %+v, want the lock rule of asset_tag", form.Rules)
	}
	if _, err = e.f.Admin.Exec(context.Background(), `UPDATE ci SET lifecycle_state = 'deployed' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if rec = e.call(e.cis, http.MethodPatch, "/api/v1/cis/"+id, `{"serial_number":"SN-2"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH in deployed: %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := e.column(id, "serial_number"); got != "SN-2" {
		t.Errorf("serial number %q, want SN-2", got)
	}
}

// TestLockedFieldRefusedOnImport: the import row fails with read_only, the
// other fields of the row stay unchanged.
func TestLockedFieldRefusedOnImport(t *testing.T) {
	e := newLockEnv(t)
	id := e.device("lock-import", "retired", "")
	disc := discovery.NewHandler(discovery.NewPGRepository(e.f.App), e.repo).WithProvenance(provenance{repo: e.overs}).
		WithInstanceFields(e.types).WithIngestLanes(2)
	policies := permission.NewFieldResolver(permission.NewPGRepository(e.f.App), nil, permission.NewPGFieldGrants(e.f.App), nil)
	writer := importer.NewCIWriter(e.svc, disc, e.overs, policies).WithInstanceFields(e.types).WithFields(e.types)
	run := &importer.RunInfo{OrganizationID: e.f.OrgA, Entity: "ci", Mode: "both", StartedAt: time.Now().UTC(), InitiatedBy: e.user}
	var res importer.RowResult
	// The row runs in its transaction, as in the import run.
	err := e.repo.RunInTx(e.ctx, e.f.OrgA, func(ctx context.Context) error {
		var werr error
		res, werr = writer.Write(ctx, &importer.WriteRow{Line: 2, Run: run, Values: &importer.MappedRow{Line: 2, Values: map[string]importer.Value{
			importer.CITypeTarget: {Text: e.typeKey}, "name": {Text: "lock-import"}, "serial_number": {Text: "SN-1"},
			"model": {Text: "M-9"}, "attributes.asset_tag": {Text: "AT-9"},
		}}})
		return werr
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != importer.OutcomeFailed || !slices.ContainsFunc(res.Issues, func(i importer.Issue) bool {
		return i.Code == ci.CodeReadOnly && strings.Contains(i.Field, "asset_tag")
	}) {
		t.Fatalf("import row %+v, want failed with read_only on asset_tag", res)
	}
	if got := e.column(id, "model"); got != "M-1" {
		t.Errorf("model after the refused row %q, want M-1 (unchanged)", got)
	}
	if got := e.column(id, "attributes->>'asset_tag'"); got != "AT-1" {
		t.Errorf("asset_tag after the refused row %q, want AT-1", got)
	}
}

// TestLockedFieldRefusedOnBulk: the bulk field change reports the CI with
// read_only (422 and the field).
func TestLockedFieldRefusedOnBulk(t *testing.T) {
	e := newLockEnv(t)
	retired := e.device("lock-bulk-r", "retired", "")
	deployed := e.device("lock-bulk-d", "deployed", "", "SN-D")
	// The field policy the route puts in front of the bulk (WP-265).
	ctx := permission.WithPolicy(e.ctx, permission.UnrestrictedPolicy(identity.AllPermissions()))
	result, _, err := e.svc.Bulk(ctx, e.f.OrgA, &ci.BulkRequest{Action: ci.BulkSetFields, IDs: []string{retired, deployed},
		Params: json.RawMessage(`{"fields":{"serial_number":"SN-7"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ci.BulkItemResult{}
	for _, item := range result.Items {
		byID[item.ID] = item
	}
	if r := byID[retired]; r.Code != http.StatusUnprocessableEntity || !slices.Contains(r.Fields, "serial_number") || !strings.Contains(r.Error, "read-only") {
		t.Errorf("bulk result of the retired CI %+v, want 422 read-only on serial_number", r)
	}
	if r := byID[deployed]; r.Code != http.StatusOK {
		t.Errorf("bulk result of the deployed CI %+v, want 200", r)
	}
	if got := e.column(retired, "serial_number"); got != "SN-1" {
		t.Errorf("serial number of the retired CI %q, want SN-1", got)
	}
}

// TestLockedFieldRefusedOnWorkflow: set_ci_field fails its step with the
// violations of the lock.
func TestLockedFieldRefusedOnWorkflow(t *testing.T) {
	e := newLockEnv(t)
	id := e.device("lock-workflow", "retired", "")
	runs := workflow.NewPGRepository(e.f.App)
	exec := workflow.NewExecutor(runs, nil, e.repo, nil, nil).WithCIService(e.svc).WithFieldGuard(override.NewGuard(e.overs))
	sys := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA})
	def := &workflow.Definition{OrganizationID: e.f.OrgA, Name: "wf-1103-lock", Trigger: workflow.JSONMap{"type": "manual"},
		Actions: []workflow.JSONMap{{"type": "set_ci_field", "ci_id": id, "fields": map[string]any{"serial_number": "SN-8"}}}, Active: true}
	if err := runs.CreateDefinition(sys, def); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Trigger(sys, e.f.OrgA, def, "manual", workflow.JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflow.StatusFailed || len(run.Steps) != 1 || run.Steps[0].Status != workflow.StatusFailed {
		t.Fatalf("run %s steps %+v, want the step failed", run.Status, run.Steps)
	}
	raw, err := json.Marshal(run.Steps[0].Output["violations"])
	if err != nil || !strings.Contains(string(raw), ci.CodeReadOnly) || !strings.Contains(string(raw), "serial_number") {
		t.Errorf("violations of the step %s, want read_only on serial_number", raw)
	}
	if got := e.column(id, "serial_number"); got != "SN-1" {
		t.Errorf("serial number %q, want SN-1", got)
	}
}

// TestLockedFieldDiscoveryObserved: an SNMP record with a new serial number
// changes the field not, the provenance carries the value and the review
// override_conflict is open.
func TestLockedFieldDiscoveryObserved(t *testing.T) {
	e := newLockEnv(t)
	const hw = "6a1f1103-0000-4000-8000-000000001103"
	id := e.device("lock-discovery", "retired", hw)
	ingest := discovery.NewHandler(discovery.NewPGRepository(e.f.App), e.repo).WithProvenance(provenance{repo: e.overs})
	mux := chi.NewRouter()
	ingest.RegisterRoutes(mux)
	rec := e.call(mux, http.MethodPost, "/api/v1/ingest/bulk", `{"collector_id":"col-1103","items":[{"ci_type_name":"`+e.typeKey+`",
		"name":"lock-discovery","hardware_uuid":"`+hw+`","serial_number":"SN-NEW","model":"M-NEW"}]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", rec.Code, rec.Body.String())
	}
	if got := e.column(id, "serial_number"); got != "SN-1" {
		t.Errorf("serial number after the ingest %q, want SN-1 (locked)", got)
	}
	var observed string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(discovered_value #>> '{}', '') FROM ci_field_value
		WHERE ci_id = $1 AND field_name = 'serial_number'`, id).Scan(&observed); err != nil || observed != "SN-NEW" {
		t.Errorf("provenance of serial_number %q (%v), want SN-NEW", observed, err)
	}
	var open int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict'
		AND status = 'open' AND payload->>'ci_id' = $2 AND payload->>'field' = 'serial_number'`, e.f.OrgA, id).Scan(&open); err != nil || open != 1 {
		t.Errorf("open override_conflict reviews of serial_number: %d (%v), want 1", open, err)
	}
}

// TestTransitionThenLock: a transition to retired locks the field for the
// next PATCH; the transition back unlocks it.
func TestTransitionThenLock(t *testing.T) {
	e := newLockEnv(t)
	id := e.device("lock-transition", "deployed", "")
	move := func(to string) {
		t.Helper()
		if _, err := e.transitions.Transition(e.ctx, e.f.OrgA, "ci", id, e.modelID, lifecycle.TransitionRequest{ToState: to}); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	move("retired")
	if rec := e.call(e.cis, http.MethodPatch, "/api/v1/cis/"+id, `{"serial_number":"SN-3"}`); !readOnly(t, rec, "serial_number") {
		t.Fatalf("PATCH after the transition to retired: %d %s, want 422 read_only", rec.Code, rec.Body.String())
	}
	move("deployed")
	if rec := e.call(e.cis, http.MethodPatch, "/api/v1/cis/"+id, `{"serial_number":"SN-3"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH after the transition back: %d %s, want 200", rec.Code, rec.Body.String())
	}
	// A write validated against deployed while a transition to retired
	// committed in between is a conflict on the locked row, not a write
	// of the field the new state locks.
	move("retired")
	validated, serial := "deployed", "SN-4"
	_, err := e.repo.Update(e.ctx, e.f.OrgA, id, ci.UpdateRequest{SerialNumber: &serial, ExpectedLifecycleState: &validated,
		Manual: &ci.ManualChange{Author: e.user}})
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "lifecycle_state" || fe.Status != http.StatusConflict {
		t.Fatalf("write validated against the previous state: %v, want 409 on lifecycle_state", err)
	}
	if item, gerr := e.repo.GetByID(e.ctx, e.f.OrgA, id); gerr != nil || item.SerialNumber == serial {
		t.Fatalf("serial number after the refused write: %+v %v", item, gerr)
	}
}

// TestLockedFieldsValidation: lifecycle_state, a reserved namespace, a
// duplicate and an empty name in locked_fields are 422 with the path.
func TestLockedFieldsValidation(t *testing.T) {
	e := newLockEnv(t)
	for i, tc := range []struct{ fields, path string }{
		{`["lifecycle_state"]`, "states[0].locked_fields[0]"},
		{`["serial_number","_observed"]`, "states[0].locked_fields[1]"},
		{`["asset_tag","asset_tag"]`, "states[0].locked_fields[1]"},
		{`[""]`, "states[0].locked_fields[0]"},
	} {
		body := fmt.Sprintf(`{"key":"wp1103_bad_%d","name":"Bad","applies_to":"ci","states":[{"key":"a","is_initial":true,"locked_fields":%s}]}`, i, tc.fields)
		rec := e.call(e.models, http.MethodPost, "/api/v1/lifecycle-models", body)
		var p struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusUnprocessableEntity || p.Field != tc.path {
			t.Errorf("locked_fields %s: %d %s, want 422 on %s", tc.fields, rec.Code, rec.Body.String(), tc.path)
		}
	}
	// An asset model never locks fields (only CI states are evaluated).
	asset := `{"key":"wp1103_asset_locks","name":"Asset","applies_to":"asset","states":[` +
		`{"key":"in_stock","is_initial":true,"locked_fields":["serial_number"]},{"key":"reserved"},{"key":"deployed"},` +
		`{"key":"repair"},{"key":"retired"},{"key":"disposed","is_terminal":true}]}`
	if rec := e.call(e.models, http.MethodPost, "/api/v1/lifecycle-models", asset); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `"states[0].locked_fields"`) {
		t.Errorf("asset model with locked fields: %d %s, want 422 on states[0].locked_fields", rec.Code, rec.Body.String())
	}
	rec := e.call(e.models, http.MethodGet, "/api/v1/lifecycle-models/"+e.modelID, "")
	var model lifecycle.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	for _, s := range model.States {
		if s.Key == "retired" && !slices.Equal(s.LockedFields, []string{"serial_number", "asset_tag"}) {
			t.Errorf("locked fields of retired %v", s.LockedFields)
		}
	}
}
