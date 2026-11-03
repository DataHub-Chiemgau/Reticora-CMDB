package lifecycle_test

// WP-723 (API-06, LCY-02): REST of the lifecycle models.
//
//   - Routes: GET /api/v1/lifecycle-models and the deprecated alias
//     /api/v1/lifecycle-definitions answer the same page with next_cursor;
//     PATCH exists only under lifecycle-models.
//   - PATCH: the current If-Match version is 200 with a new ETag, an older
//     one 412, none 200; kept states keep their id; key and applies_to
//     cannot change (422). System models are read-only (403), models of
//     organization B are 404 for organization A.
//   - Protection: DELETE of a model a CI type binds is 409 with ci_types; a
//     PATCH that removes production while a CI stands there is 409 with the
//     counts per state; an unused model is deleted (204).
//   - Guards: a valid guard is stored in lifecycle_transition.validation and
//     returned; an unknown operator or property is 422 with the path. An
//     asset model without disposed and applies_to both are 422.
//   - Allowed transitions: a CI with the software model in production may
//     go to deprecated; a CI without model is 422, a CI of another client
//     404; an asset without type model gets the targets of physical_asset.
//     TestLifecycleModelAPIMemory runs the flow with MemoryRepository and
//     MemoryStateStore (--no-db).
//
// The PostgreSQL test runs only against a database (TEST_DATABASE_URL, see
// scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestLifecycleModelAPI' ./internal/lifecycle/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// apiProblem is the RFC 7807 body of a refused lifecycle model request.
type apiProblem struct {
	Status      int                    `json:"status"`
	Field       string                 `json:"field"`
	CITypes     int                    `json:"ci_types"`
	CIs         int                    `json:"cis"`
	Assets      int                    `json:"assets"`
	StateCounts []lifecycle.StateCount `json:"state_counts"`
}

// modelAPI drives a lifecycle handler through RegisterRoutes.
type modelAPI struct {
	t   *testing.T
	mux chi.Router
}

func newModelAPI(t *testing.T, h *lifecycle.Handler) *modelAPI {
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return &modelAPI{t: t, mux: mux}
}

// with returns the client for the (sub)test t.
func (m *modelAPI) with(t *testing.T) *modelAPI { return &modelAPI{t: t, mux: m.mux} }

func (m *modelAPI) call(ctx context.Context, method, path, body string, header ...string) *httptest.ResponseRecorder {
	m.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	m.mux.ServeHTTP(w, req)
	return w
}

// want checks the status and decodes the body into out (when not nil).
func (m *modelAPI) want(w *httptest.ResponseRecorder, code int, out any) {
	m.t.Helper()
	if w.Code != code {
		m.t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), code)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			m.t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
}

// problem checks a refusal with its field.
func (m *modelAPI) problem(w *httptest.ResponseRecorder, code int, field string) apiProblem {
	m.t.Helper()
	var p apiProblem
	m.want(w, code, &p)
	if p.Field != field {
		m.t.Fatalf("problem %s, want field %q", w.Body.String(), field)
	}
	return p
}

// create posts a model and returns it.
func (m *modelAPI) create(ctx context.Context, body string) lifecycle.Definition {
	m.t.Helper()
	var d lifecycle.Definition
	w := m.call(ctx, http.MethodPost, "/api/v1/lifecycle-models", body)
	m.want(w, http.StatusCreated, &d)
	if got := w.Header().Get("ETag"); got != api.ETag(1) || d.Version != 1 {
		m.t.Fatalf("create: ETag %s version %d, want 1", got, d.Version)
	}
	return d
}

// options reads the allowed transitions of an entity.
func (m *modelAPI) options(ctx context.Context, kind, id string) lifecycle.TransitionOptions {
	m.t.Helper()
	var o lifecycle.TransitionOptions
	m.want(m.call(ctx, http.MethodGet, "/api/v1/"+kind+"/"+id+"/lifecycle-transitions", ""), http.StatusOK, &o)
	return o
}

func targets(o lifecycle.TransitionOptions) []string {
	var out []string
	for _, tr := range o.Transitions {
		out = append(out, tr.ToState)
	}
	slices.Sort(out)
	return out
}

const ciModelBody = `{"key":"%s","name":"Service","applies_to":"ci","states":[
	{"key":"draft","label":"Draft","is_initial":true,"sort_order":10},
	{"key":"production","label":"Production","sort_order":20},
	{"key":"retired","label":"Retired","is_terminal":true,"sort_order":30}],
	"transitions":[{"from_state_key":"draft","to_state_key":"production","key":"go_live"},
	{"from_state_key":"production","to_state_key":"retired","key":"retire"}]}`

func ciModel(key string) string { return strings.Replace(ciModelBody, "%s", key, 1) }

// TestLifecycleModelAPI runs the lifecycle model REST against PostgreSQL.
func TestLifecycleModelAPI(t *testing.T) {
	f := scopetest.Seed(t, "18")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp723-admin")
	ctxOf := func(org string, clients ...string) context.Context {
		scope := database.OrgWideScope(org, user)
		if len(clients) > 0 {
			scope.Clients = database.ScopeIDs(clients...)
		}
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: org, UserID: user})
		return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp723", CorrelationID: "wp723", Source: audit.SourceUI})
	}
	orgA, orgB := ctxOf(f.OrgA), ctxOf(f.OrgB)
	admin := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	repo := lifecycle.NewPGRepository(f.App)
	root := newModelAPI(t, lifecycle.NewHandler(repo,
		lifecycle.NewService(repo, lifecycle.NewPGStateStore(f.App), nil), lifecycle.NewResolver(f.App)))

	t.Run("routes and alias", func(t *testing.T) {
		m := root.with(t)
		var models, alias api.ListResponse[lifecycle.Definition]
		m.want(m.call(orgA, http.MethodGet, "/api/v1/lifecycle-models?limit=1", ""), http.StatusOK, &models)
		m.want(m.call(orgA, http.MethodGet, "/api/v1/lifecycle-definitions?limit=1", ""), http.StatusOK, &alias)
		if len(models.Data) != 1 || models.NextCursor == "" || models.Total < 2 {
			t.Fatalf("page %+v, want one model and a next_cursor", models)
		}
		if alias.NextCursor != models.NextCursor || alias.Total != models.Total || alias.Data[0].ID != models.Data[0].ID {
			t.Errorf("alias page %+v differs from %+v", alias, models)
		}
		var next api.ListResponse[lifecycle.Definition]
		m.want(m.call(orgA, http.MethodGet, "/api/v1/lifecycle-models?limit=1&cursor="+models.NextCursor, ""), http.StatusOK, &next)
		if len(next.Data) != 1 || next.Data[0].ID == models.Data[0].ID {
			t.Errorf("second page %+v", next)
		}
		m.want(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-definitions/"+lifecycle.SoftwareLifecycleID, `{"name":"x"}`),
			http.StatusMethodNotAllowed, nil)
	})

	t.Run("create checks", func(t *testing.T) {
		m := root.with(t)
		m.problem(m.call(orgA, http.MethodPost, "/api/v1/lifecycle-models",
			`{"key":"wp723_both","name":"Both","applies_to":"both","states":[{"key":"a","label":"A"}]}`),
			http.StatusUnprocessableEntity, "applies_to")
		m.problem(m.call(orgA, http.MethodPost, "/api/v1/lifecycle-definitions",
			`{"key":"wp723_both","name":"Both","applies_to":"both","states":[{"key":"a","label":"A"}]}`),
			http.StatusUnprocessableEntity, "applies_to")
		// Without applies_to the model applies to assets; disposed is missing.
		m.problem(m.call(orgA, http.MethodPost, "/api/v1/lifecycle-models",
			`{"key":"wp723_asset","name":"Asset","states":[{"key":"in_stock"},{"key":"reserved"},{"key":"deployed"},{"key":"repair"},{"key":"retired"}]}`),
			http.StatusUnprocessableEntity, "states")
		asset := m.create(orgA, `{"key":"wp723_asset","name":"Asset","states":[{"key":"in_stock","is_initial":true},
			{"key":"reserved"},{"key":"deployed"},{"key":"repair"},{"key":"retired"},{"key":"disposed","is_terminal":true}]}`)
		if asset.AppliesTo != "asset" {
			t.Errorf("default applies_to %q", asset.AppliesTo)
		}
		m.problem(m.call(orgA, http.MethodPost, "/api/v1/lifecycle-models",
			`{"key":"wp723_bad","name":"Bad","applies_to":"ci","states":[{"key":"a"}],"transitions":[{"to_state_key":"b","key":"ab"}]}`),
			http.StatusUnprocessableEntity, "transitions[0].to_state_key")
	})

	t.Run("guards", func(t *testing.T) {
		m := root.with(t)
		base := `{"key":"wp723_guard","name":"Guard","applies_to":"ci","states":[{"key":"a","is_initial":true},{"key":"b"}],
			"transitions":[{"from_state_key":"a","to_state_key":"b","key":"ab"},
			{"from_state_key":"b","to_state_key":"a","key":"ba","guard":{"fields":{"serial_number":%s}}}]}`
		for _, rule := range []string{`{"op":"bogus"}`, `{"op":"required","bogus":1}`} {
			m.problem(m.call(orgA, http.MethodPost, "/api/v1/lifecycle-models", strings.Replace(base, "%s", rule, 1)),
				http.StatusUnprocessableEntity, "transitions[1].guard.fields.serial_number")
		}
		created := m.create(orgA, strings.Replace(base, "%s", `{"op":"pattern","pattern":"^SN-"}`, 1))
		var d lifecycle.Definition
		m.want(m.call(orgA, http.MethodGet, "/api/v1/lifecycle-models/"+created.ID, ""), http.StatusOK, &d)
		var guarded *lifecycle.Transition
		for i := range d.Transitions {
			if d.Transitions[i].Key == "ba" {
				guarded = &d.Transitions[i]
			}
		}
		if guarded == nil || guarded.Guard == nil || guarded.Guard.Fields["serial_number"].Pattern != "^SN-" {
			t.Fatalf("guard not returned: %+v", d.Transitions)
		}
		if fields, ok := guarded.Validation["fields"].(map[string]any); !ok || fields["serial_number"] == nil {
			t.Errorf("validation %v does not carry the guard", guarded.Validation)
		}
		if stored := admin(`SELECT validation->'fields'->'serial_number'->>'op' FROM lifecycle_transition
			WHERE definition_id = $1 AND key = 'ba'`, created.ID); stored != "pattern" {
			t.Errorf("stored guard op %q", stored)
		}
		// A PATCH without transitions keeps the guard.
		var patched lifecycle.Definition
		m.want(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+created.ID, `{"name":"Guard 2"}`), http.StatusOK, &patched)
		for _, tr := range patched.Transitions {
			if tr.Key == "ba" && (tr.Guard == nil || tr.Guard.Fields["serial_number"].Op != "pattern") {
				t.Errorf("guard lost by PATCH: %+v", tr)
			}
		}
		m.problem(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+created.ID,
			`{"transitions":[{"from_state_key":"a","to_state_key":"b","key":"ab"},{"from_state_key":"b","to_state_key":"a","key":"ba","guard":{"fields":{"x":{"op":"eqq"}}}}]}`),
			http.StatusUnprocessableEntity, "transitions[1].guard.fields.x")
	})

	t.Run("patch with If-Match", func(t *testing.T) {
		m := root.with(t)
		d := m.create(orgA, ciModel("wp723_patch"))
		var before lifecycle.Definition
		w := m.call(orgA, http.MethodGet, "/api/v1/lifecycle-models/"+d.ID, "")
		m.want(w, http.StatusOK, &before)
		if w.Header().Get("ETag") != api.ETag(1) {
			t.Fatalf("detail ETag %s", w.Header().Get("ETag"))
		}
		stateID := func(def lifecycle.Definition, key string) string {
			for _, s := range def.States {
				if s.Key == key {
					return s.ID
				}
			}
			return ""
		}
		var after lifecycle.Definition
		w = m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID,
			`{"name":"Service 2","states":[{"key":"draft","is_initial":true},{"key":"production","label":"Live"},{"key":"retired"},{"key":"archived"}],
			"transitions":[{"from_state_key":"retired","to_state_key":"archived","key":"archive"}]}`, "If-Match", api.ETag(1))
		m.want(w, http.StatusOK, &after)
		if w.Header().Get("ETag") != api.ETag(2) || after.Version != 2 || after.Name != "Service 2" {
			t.Fatalf("PATCH: ETag %s version %d name %q", w.Header().Get("ETag"), after.Version, after.Name)
		}
		if id := stateID(after, "production"); id == "" || id != stateID(before, "production") {
			t.Errorf("production changed its id: %s -> %s", stateID(before, "production"), id)
		}
		if len(after.States) != 4 || len(after.Transitions) != 1 || after.Transitions[0].Key != "archive" {
			t.Errorf("states %d transitions %+v after PATCH", len(after.States), after.Transitions)
		}
		m.want(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"name":"stale"}`, "If-Match", api.ETag(1)),
			http.StatusPreconditionFailed, nil)
		w = m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"description":"no If-Match"}`)
		m.want(w, http.StatusOK, &after)
		if w.Header().Get("ETag") != api.ETag(3) || after.Name != "Service 2" {
			t.Errorf("PATCH without If-Match: ETag %s name %q", w.Header().Get("ETag"), after.Name)
		}
		m.problem(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"key":"other"}`),
			http.StatusUnprocessableEntity, "key")
		m.problem(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"applies_to":"asset"}`),
			http.StatusUnprocessableEntity, "applies_to")
		// The unchanged values are accepted.
		m.want(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"key":"wp723_patch","applies_to":"ci"}`),
			http.StatusOK, nil)
	})

	t.Run("system and foreign models", func(t *testing.T) {
		m := root.with(t)
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			m.want(m.call(orgA, method, "/api/v1/lifecycle-models/"+lifecycle.SoftwareLifecycleID, `{"name":"x"}`),
				http.StatusForbidden, nil)
			m.want(m.call(orgA, method, "/api/v1/lifecycle-models/"+physicalAssetModel, `{"name":"x"}`),
				http.StatusForbidden, nil)
		}
		m.want(m.call(orgA, http.MethodDelete, "/api/v1/lifecycle-definitions/"+lifecycle.SoftwareLifecycleID, ""),
			http.StatusForbidden, nil)
		foreign := m.create(orgB, ciModel("wp723_org_b"))
		for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
			m.want(m.call(orgA, method, "/api/v1/lifecycle-models/"+foreign.ID, `{"name":"x"}`), http.StatusNotFound, nil)
		}
		m.want(m.call(orgB, http.MethodGet, "/api/v1/lifecycle-models/"+foreign.ID, ""), http.StatusOK, nil)
	})

	t.Run("protection of used models", func(t *testing.T) {
		m := root.with(t)
		d := m.create(orgA, ciModel("wp723_used"))
		typeID := admin(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, 'wp723-service', 'WP-723 service', $2) RETURNING id::text`, f.OrgA, d.ID)
		p := m.problem(m.call(orgA, http.MethodDelete, "/api/v1/lifecycle-models/"+d.ID, ""), http.StatusConflict, "")
		if p.CITypes != 1 || p.CIs != 0 || p.Assets != 0 {
			t.Errorf("DELETE of a bound model: %+v, want ci_types 1", p)
		}
		ci := f.CI(t, f.OrgA, "", "wp723-ci")
		admin(`UPDATE ci SET ci_type_id = $2, lifecycle_state = 'production' WHERE id = $1 RETURNING id::text`, ci, typeID)
		p = m.problem(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID,
			`{"states":[{"key":"draft","is_initial":true},{"key":"retired"}]}`), http.StatusConflict, "states")
		if !slices.Equal(p.StateCounts, []lifecycle.StateCount{{State: "production", Count: 1}}) {
			t.Errorf("state counts %+v, want production 1", p.StateCounts)
		}
		p = m.problem(m.call(orgA, http.MethodDelete, "/api/v1/lifecycle-models/"+d.ID, ""), http.StatusConflict, "")
		if p.CITypes != 1 || p.CIs != 1 {
			t.Errorf("DELETE of a used model: %+v, want ci_types 1 and cis 1", p)
		}
		// Removing an empty state is accepted.
		m.want(m.call(orgA, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID,
			`{"states":[{"key":"draft","is_initial":true},{"key":"production"}]}`), http.StatusOK, nil)
		unused := m.create(orgA, ciModel("wp723_unused"))
		m.want(m.call(orgA, http.MethodDelete, "/api/v1/lifecycle-models/"+unused.ID, ""), http.StatusNoContent, nil)
		m.want(m.call(orgA, http.MethodGet, "/api/v1/lifecycle-models/"+unused.ID, ""), http.StatusNotFound, nil)
	})

	t.Run("allowed transitions", func(t *testing.T) {
		m := root.with(t)
		softwareType := admin(`SELECT id::text FROM ci_type WHERE organization_id IS NULL AND key = 'software'`)
		sw := f.CI(t, f.OrgA, f.Client1, "wp723-software")
		admin(`UPDATE ci SET ci_type_id = $2, lifecycle_state = 'production' WHERE id = $1 RETURNING id::text`, sw, softwareType)
		o := m.options(orgA, "cis", sw)
		if o.Model.ID != lifecycle.SoftwareLifecycleID || o.Model.Key != "software" || o.CurrentState != "production" ||
			!slices.Equal(targets(o), []string{"deprecated"}) {
			t.Errorf("software CI options %+v", o)
		}
		m.problem(m.call(orgA, http.MethodGet, "/api/v1/cis/"+f.CI(t, f.OrgA, "", "wp723-server")+"/lifecycle-transitions", ""),
			http.StatusUnprocessableEntity, "to_state")
		other := f.CI(t, f.OrgA, f.Client2, "wp723-client2")
		admin(`UPDATE ci SET ci_type_id = $2 WHERE id = $1 RETURNING id::text`, other, softwareType)
		m.want(m.call(ctxOf(f.OrgA, f.Client1), http.MethodGet, "/api/v1/cis/"+other+"/lifecycle-transitions", ""),
			http.StatusNotFound, nil)
		m.want(m.call(orgB, http.MethodGet, "/api/v1/cis/"+sw+"/lifecycle-transitions", ""), http.StatusNotFound, nil)

		asset := f.Asset(t, f.OrgA, "", "wp723-asset")
		o = m.options(orgA, "assets", asset)
		if o.Model.ID != physicalAssetModel || o.CurrentState != "" || !slices.Equal(targets(o), []string{"ordered"}) {
			t.Errorf("new asset options %+v", o)
		}
		admin(`UPDATE asset SET lifecycle_state = 'in_stock' WHERE id = $1 RETURNING id::text`, asset)
		o = m.options(orgA, "assets", asset)
		if want := []string{"deployed", "reserved", "retired"}; !slices.Equal(targets(o), want) {
			t.Errorf("in_stock asset targets %v, want %v", targets(o), want)
		}
	})
}

// TestLifecycleModelAPIMemory runs the routes, PATCH and the allowed
// transitions with MemoryRepository and MemoryStateStore (--no-db).
func TestLifecycleModelAPIMemory(t *testing.T) {
	const org = "00000000-0000-4000-8000-0000000000a1"
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org})
	repo := lifecycle.NewMemoryRepository()
	store := lifecycle.NewMemoryStateStore()
	m := newModelAPI(t, lifecycle.NewHandler(repo, lifecycle.NewService(repo, store, nil), store))

	var models, alias api.ListResponse[lifecycle.Definition]
	m.want(m.call(ctx, http.MethodGet, "/api/v1/lifecycle-models?limit=1", ""), http.StatusOK, &models)
	m.want(m.call(ctx, http.MethodGet, "/api/v1/lifecycle-definitions?limit=1", ""), http.StatusOK, &alias)
	if len(models.Data) != 1 || models.NextCursor == "" || alias.NextCursor != models.NextCursor || alias.Data[0].ID != models.Data[0].ID {
		t.Fatalf("pages %+v and %+v", models, alias)
	}

	d := m.create(ctx, ciModel("mem_service"))
	m.want(m.call(ctx, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"name":"Service 2"}`, "If-Match", api.ETag(1)),
		http.StatusOK, nil)
	m.want(m.call(ctx, http.MethodPatch, "/api/v1/lifecycle-models/"+d.ID, `{"name":"stale"}`, "If-Match", api.ETag(1)),
		http.StatusPreconditionFailed, nil)
	m.want(m.call(ctx, http.MethodPatch, "/api/v1/lifecycle-models/"+lifecycle.SoftwareLifecycleID, `{"name":"x"}`),
		http.StatusForbidden, nil)
	m.problem(m.call(ctx, http.MethodPost, "/api/v1/lifecycle-models",
		`{"key":"mem_both","name":"Both","applies_to":"both","states":[{"key":"a"}]}`), http.StatusUnprocessableEntity, "applies_to")
	m.want(m.call(ctx, http.MethodDelete, "/api/v1/lifecycle-models/"+d.ID, ""), http.StatusNoContent, nil)

	const ci, asset = "00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2"
	store.SeedEntity("ci", ci, "software")
	if err := store.SetState(ctx, org, "ci", ci, "production"); err != nil {
		t.Fatal(err)
	}
	o := m.options(ctx, "cis", ci)
	if o.Model.ID != lifecycle.SoftwareLifecycleID || o.CurrentState != "production" || !slices.Equal(targets(o), []string{"deprecated"}) {
		t.Errorf("software CI options %+v", o)
	}
	m.want(m.call(ctx, http.MethodGet, "/api/v1/cis/00000000-0000-4000-8000-0000000000c9/lifecycle-transitions", ""),
		http.StatusNotFound, nil)
	store.SeedEntity("asset", asset, "physical_asset")
	o = m.options(ctx, "assets", asset)
	if o.Model.ID != physicalAssetModel || !slices.Equal(targets(o), []string{"ordered"}) {
		t.Errorf("asset options %+v", o)
	}
}
