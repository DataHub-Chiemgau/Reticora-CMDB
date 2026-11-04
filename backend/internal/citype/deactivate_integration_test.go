package citype_test

// WP-249 (MET-05): a deactivated CI type gets no new CIs, its CIs stay
// readable and editable, and an active type has no deactivated parent.
//
//   - ci.PGRepository.Create and CreateMany without ChosenType (discovery,
//     agent) refuse a deactivated type with ci.ErrTypeInactive: nothing is
//     written and nothing is reported as refused. POST /api/v1/cis and the
//     manual type change answer 422 (WP-396, regression); the discovery
//     reclassification leaves the type as it is.
//   - A CI of the deactivated type stays readable (GET) and editable
//     (PATCH); GET /api/v1/ci-types hides deactivated types unless
//     include_inactive=true.
//   - deactivate/activate of a system type is 403 (PostgreSQL and memory),
//     of an unknown id 404; deactivating a type with an active subtype,
//     activating one under a deactivated parent and cloning one under a
//     deactivated parent are 409; POST and PATCH with a deactivated
//     parent_type_id 422. A refused change writes no audit entry.
//   - The clone copies the attribute definitions, compliance_rules,
//     ui_schema, lifecycle_definition_id and parent_type_id under a new key
//     with cloned_from_id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestDeactivatedType' ./internal/citype/

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

func TestDeactivatedTypeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "27")
	bg := context.Background()
	a := newInheritAPI(t, f)
	ciRepo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	ci.NewHandler(ci.NewService(ciRepo)).RegisterRoutes(a.mux)
	ctxA := a.ctx(f.OrgA)
	generic := systemTypeID(t, f, "generic_device")
	server := systemTypeID(t, f, "server")
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	audits := func(id string) int {
		return count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2
			AND action IN ('ci_type.deactivated', 'ci_type.activated')`, f.OrgA, id)
	}
	expect := func(w *httptest.ResponseRecorder, code int, field string) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), code)
		}
		if p := problemOf(w); field != "" && p.Field != field {
			t.Errorf("problem %+v, want field %s", p, field)
		}
	}
	setActive := func(id string, active bool) *httptest.ResponseRecorder {
		action := "/deactivate"
		if active {
			action = "/activate"
		}
		return a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types/"+id+action, "")
	}

	parent := a.create("wp249-parent", "")
	child := a.create("wp249-child", parent.ID)

	t.Run("invariant: an active type has no deactivated parent", func(t *testing.T) {
		expect(setActive(parent.ID, false), http.StatusConflict, "is_active")
		if n := audits(parent.ID); n != 0 {
			t.Errorf("%d audit entries after the refused deactivation", n)
		}
		expect(setActive(child.ID, false), http.StatusOK, "")
		expect(setActive(parent.ID, false), http.StatusOK, "")
		expect(setActive(child.ID, true), http.StatusConflict, "parent_type_id")
		expect(a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types/"+child.ID+"/clone", `{"key":"wp249-child-clone"}`),
			http.StatusConflict, "parent_type_id")
		w := a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types",
			`{"key":"wp249-orphan","name":"wp249-orphan","parent_type_id":"`+parent.ID+`"}`)
		expect(w, http.StatusUnprocessableEntity, "parent_type_id")
		if p := problemOf(w); p.Detail != "parent_type_id: the CI type is deactivated" {
			t.Errorf("detail %q", p.Detail)
		}
		other := a.create("wp249-other", "")
		expect(a.reparent(other.ID, parent.ID), http.StatusUnprocessableEntity, "parent_type_id")
		if n := audits(child.ID); n != 1 {
			t.Errorf("%d audit entries of the child, want 1 (its deactivation)", n)
		}
		if n := audits(parent.ID); n != 1 {
			t.Errorf("%d audit entries of the parent, want 1 (its deactivation)", n)
		}
	})

	t.Run("system types are read-only, unknown ids not found", func(t *testing.T) {
		for _, active := range []bool{false, true} {
			expect(setActive(generic, active), http.StatusForbidden, "")
			expect(setActive("c0de2700-ffff-4000-8000-00000000dead", active), http.StatusNotFound, "")
		}
		if n := audits(generic); n != 0 {
			t.Errorf("%d audit entries of the system type", n)
		}
		mem := citype.NewMemoryRepository()
		mem.SeedType(&citype.Type{ID: "sys", Name: "sys", IsActive: true})
		if _, err := mem.SetActive(bg, f.OrgA, "sys", false); !errors.Is(err, citype.ErrTypeReadOnly) {
			t.Errorf("memory deactivate of a system type: %v", err)
		}
		if _, err := mem.SetActive(bg, f.OrgA, "missing", false); err == nil || err.Error() != "not found" {
			t.Errorf("memory deactivate of an unknown type: %v", err)
		}
		mux := chi.NewRouter()
		citype.NewHandler(mem).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ci-types/sys/activate", http.NoBody).WithContext(ctxA)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("memory activate of a system type: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("list hides deactivated types unless include_inactive", func(t *testing.T) {
		listed := func(query string) bool {
			w := a.call(f.OrgA, http.MethodGet, "/api/v1/ci-types?limit=200&search=wp249-parent"+query, "")
			if w.Code != http.StatusOK {
				t.Fatalf("list: %d %s", w.Code, w.Body.String())
			}
			return strings.Contains(w.Body.String(), parent.ID)
		}
		if listed("") || !listed("&include_inactive=true") {
			t.Errorf("deactivated type listed without include_inactive: %v, with: %v", listed(""), listed("&include_inactive=true"))
		}
	})

	t.Run("no new CIs of a deactivated type", func(t *testing.T) {
		item := &ci.Item{OrganizationID: f.OrgA, CITypeID: child.ID, Name: "wp249-new", Status: "active"}
		if err := ciRepo.Create(ctxA, item); !errors.Is(err, ci.ErrTypeInactive) {
			t.Errorf("Create without ChosenType: %v, want ErrTypeInactive", err)
		}
		res, err := ciRepo.CreateMany(ctxA, f.OrgA, []*ci.Item{
			{OrganizationID: f.OrgA, CITypeID: server, Name: "wp249-batch-ok", Status: "active"},
			{OrganizationID: f.OrgA, CITypeID: child.ID, Name: "wp249-batch-off", Status: "active"},
		})
		if !errors.Is(err, ci.ErrTypeInactive) || len(res.Refused) != 0 {
			t.Errorf("CreateMany: %+v, %v, want ErrTypeInactive and nothing refused", res, err)
		}
		if n := count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND name LIKE 'wp249-%'`, f.OrgA); n != 0 {
			t.Errorf("%d CIs written", n)
		}
		w := a.call(f.OrgA, http.MethodPost, "/api/v1/cis", `{"name":"wp249-api","ci_type_id":"`+child.ID+`","status":"active"}`)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "the CI type is deactivated") {
			t.Errorf("POST /cis: %d %s, want 422 the CI type is deactivated", w.Code, w.Body.String())
		}
	})

	t.Run("CIs of a deactivated type stay readable and editable", func(t *testing.T) {
		old := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, ci_type_id, name, status) VALUES ($1, $2, $3, 'wp249-old', 'active')`,
			old, f.OrgA, child.ID); err != nil {
			t.Fatal(err)
		}
		if w := a.call(f.OrgA, http.MethodGet, "/api/v1/cis/"+old, ""); w.Code != http.StatusOK {
			t.Errorf("GET: %d %s", w.Code, w.Body.String())
		}
		w := a.call(f.OrgA, http.MethodPatch, "/api/v1/cis/"+old, `{"name":"wp249-old-renamed"}`)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "wp249-old-renamed") {
			t.Errorf("PATCH: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("type changes to a deactivated type", func(t *testing.T) {
		gen := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, ci_type_id, name, status) VALUES ($1, $2, $3, 'wp249-generic', 'active')`,
			gen, f.OrgA, generic); err != nil {
			t.Fatal(err)
		}
		w := a.call(f.OrgA, http.MethodPost, "/api/v1/cis/"+gen+"/type", `{"ci_type_id":"`+child.ID+`"}`)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "the CI type is deactivated") {
			t.Errorf("manual type change: %d %s, want 422", w.Code, w.Body.String())
		}
		res, err := ciRepo.UpdateRecord(ctxA, f.OrgA, gen, &ci.RecordUpdate{TypeID: child.ID})
		if err != nil || res.TypeChanged || res.CI.CITypeID != generic {
			t.Errorf("discovery reclassification: %+v, %v, want the generic_device unchanged", res, err)
		}
		if _, err = ciRepo.ChangeType(ctxA, f.OrgA, gen, child.ID, ci.TypeChangeDiscovery); !errors.Is(err, ci.ErrTypeInactive) {
			t.Errorf("ChangeType in discovery mode: %v, want ErrTypeInactive", err)
		}
	})

	t.Run("clone copies the type with its parent", func(t *testing.T) {
		var lifecycle string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM lifecycle_definition WHERE organization_id IS NULL ORDER BY id LIMIT 1`).
			Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		w := a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp249-src","name":"wp249-src","parent_type_id":"`+server+`",
			"lifecycle_definition_id":"`+lifecycle+`","ui_schema":{"order":["wp249_note"]},
			"compliance_rules":[{"rule":"wp249","severity":"low"}]}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create source: %d %s", w.Code, w.Body.String())
		}
		var src citype.Type
		_ = json.Unmarshal(w.Body.Bytes(), &src)
		a.mustDefine(src.ID, `{"key":"wp249_note","data_type":"text"}`)
		w = a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types/"+src.ID+"/clone", `{"key":"wp249-copy","name":"wp249-copy"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("clone: %d %s", w.Code, w.Body.String())
		}
		var clone citype.Type
		_ = json.Unmarshal(w.Body.Bytes(), &clone)
		if clone.ID == src.ID || clone.Key != "wp249-copy" || clone.ClonedFromID != src.ID || clone.ParentTypeID != server ||
			clone.LifecycleDefinitionID != lifecycle || !clone.IsActive {
			t.Errorf("clone %+v", clone)
		}
		if rules, _ := json.Marshal(clone.ComplianceRules); string(rules) != `[{"rule":"wp249","severity":"low"}]` {
			t.Errorf("compliance_rules %s", rules)
		}
		if ui, _ := json.Marshal(clone.UISchema); string(ui) != `{"order":["wp249_note"]}` {
			t.Errorf("ui_schema %s", ui)
		}
		if !slices.ContainsFunc(clone.Fields, func(fl citype.Field) bool { return fl.Name == "wp249_note" }) {
			t.Errorf("fields %+v, want wp249_note", clone.Fields)
		}
		got := a.get(clone.ID)
		if properties(&got)["wp249_note"] == nil {
			t.Errorf("clone schema %v lacks wp249_note", properties(&got))
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2 AND action = 'ci_type.cloned'`,
			f.OrgA, clone.ID); n != 1 {
			t.Errorf("%d ci_type.cloned entries, want 1", n)
		}
	})
}
