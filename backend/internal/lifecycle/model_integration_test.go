package lifecycle_test

// WP-263 (MET-01, MET-06, LCY-02, E-116): lifecycle models in the
// metamodel, against PostgreSQL.
//
//   - Seeds: the system models physical_asset and software are visible in
//     organization A and B, their states and transitions are global
//     (organization_id NULL) and read-only for reticora_app; software is
//     bound to the system types application and software; the memory
//     repository (--no-db) lists both models.
//   - Inheritance and scope: a subtype without a model inherits the
//     software model of its parent (production is accepted, deployed fails
//     with ci_lifecycle_requires_model); a CI type with physical_asset has
//     no CI lifecycle (ErrNoLifecycleModel). An asset gets the asset model
//     of its type chain, without one physical_asset; a linked CI does not
//     change that.
//   - Assignment and change protection (PATCH/POST /api/v1/ci-types): a
//     model of another organization is 422, lifecycle_model_id and
//     lifecycle_definition_id with different values are 422, the deprecated
//     field alone is taken over and both fields are returned. A change of
//     the model or of the parent that leaves CIs with states the new
//     effective model lacks is 409 with the counts per state; without such
//     CIs it is 200. A bound model cannot be deleted (foreign key without
//     ON DELETE action); the organization delete of the fixture cleanup
//     still cascades.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestLifecycleModels' ./internal/lifecycle/

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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

const (
	physicalAssetModel = "00000000-0000-0000-0000-000000000101"
	softwareModel      = lifecycle.SoftwareLifecycleID
)

// modelProblem is the RFC 7807 body of a refused ci-type write.
type modelProblem struct {
	Status      int                          `json:"status"`
	Field       string                       `json:"field"`
	StateCounts []citype.LifecycleStateCount `json:"state_counts"`
}

func TestLifecycleModels(t *testing.T) {
	f := scopetest.Seed(t, "63")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp263-admin")
	tenantCtx := func(org string) context.Context {
		scope := database.OrgWideScope(org, user)
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: org, UserID: user})
		return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp263", CorrelationID: "wp263", Source: audit.SourceUI})
	}
	inTenant := func(org, sql string, args ...any) (pgconn.CommandTag, error) {
		var tag pgconn.CommandTag
		scope := database.OrgWideScope(org, user)
		err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var execErr error
			tag, execErr = tx.Exec(ctx, sql, args...)
			return execErr
		})
		return tag, err
	}
	violates := func(err error, constraint string) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.ConstraintName == constraint
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	mux := chi.NewRouter()
	citype.NewHandler(citype.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(org, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenantCtx(org))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	problem := func(w *httptest.ResponseRecorder, code int, field string) modelProblem {
		t.Helper()
		var p modelProblem
		if w.Code != code {
			t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Field != field {
			t.Fatalf("problem %s (%v), want field %s", w.Body.String(), err, field)
		}
		return p
	}
	createType := func(body string) citype.Type {
		t.Helper()
		w := call(f.OrgA, http.MethodPost, "/api/v1/ci-types", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create type %s: %d %s", body, w.Code, w.Body.String())
		}
		var typ citype.Type
		if err := json.Unmarshal(w.Body.Bytes(), &typ); err != nil {
			t.Fatal(err)
		}
		return typ
	}
	ciOfType := func(name, typeID string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, "", name)
		if _, err := inTenant(f.OrgA, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, id, typeID); err != nil {
			t.Fatalf("type of %s: %v", name, err)
		}
		return id
	}
	resolver := lifecycle.NewResolver(f.App)
	resolve := func(entityType, id string) (string, error) {
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(f.OrgCtx(f.OrgA))
		return resolver.LifecycleKeyFor(req, f.OrgA, entityType, id)
	}
	defs := lifecycle.NewPGRepository(f.App)
	orgModel := func(org, key, appliesTo string, states ...string) string {
		t.Helper()
		d := &lifecycle.Definition{OrganizationID: org, Key: key, Name: key, AppliesTo: appliesTo}
		for i, s := range states {
			d.States = append(d.States, lifecycle.State{Key: s, Label: s, IsInitial: i == 0, SortOrder: i})
		}
		if err := defs.Create(f.OrgCtx(org), d); err != nil {
			t.Fatalf("create model %s: %v", key, err)
		}
		return d.ID
	}

	t.Run("seeds", func(t *testing.T) {
		for _, org := range []string{f.OrgA, f.OrgB} {
			for id, want := range map[string][2]int{physicalAssetModel: {9, 15}, softwareModel: {5, 6}} {
				d, err := defs.GetByID(f.OrgCtx(org), org, id)
				if err != nil {
					t.Fatalf("org %s: system model %s: %v", org, id, err)
				}
				if !d.IsSystem || d.OrganizationID != "" || len(d.States) != want[0] || len(d.Transitions) != want[1] {
					t.Errorf("org %s: %s system=%v org=%q states=%d transitions=%d, want %v",
						org, d.Key, d.IsSystem, d.OrganizationID, len(d.States), len(d.Transitions), want)
				}
			}
		}
		sw, err := defs.GetByID(f.OrgCtx(f.OrgA), f.OrgA, softwareModel)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, s := range sw.States {
			keys = append(keys, s.Key)
			if s.IsInitial != (s.Key == "evaluation") || s.IsTerminal != (s.Key == "eol") {
				t.Errorf("state %s initial=%v terminal=%v", s.Key, s.IsInitial, s.IsTerminal)
			}
		}
		if !slices.Equal(keys, []string{"evaluation", "approved", "production", "deprecated", "eol"}) {
			t.Errorf("software states %v", keys)
		}
		var pairs []string
		for _, tr := range sw.Transitions {
			pairs = append(pairs, tr.FromStateKey+">"+tr.ToStateKey)
		}
		slices.Sort(pairs)
		if want := []string{"approved>eol", "approved>production", "deprecated>eol", "deprecated>production",
			"evaluation>approved", "production>deprecated"}; !slices.Equal(pairs, want) {
			t.Errorf("software transitions %v, want %v", pairs, want)
		}
		if sw.Key != "software" || sw.AppliesTo != "ci" {
			t.Errorf("software model key %q applies_to %q", sw.Key, sw.AppliesTo)
		}
		if n := scalar(`SELECT (SELECT count(*) FROM lifecycle_state WHERE definition_id IN ($1, $2) AND organization_id IS NOT NULL)
			+ (SELECT count(*) FROM lifecycle_transition WHERE definition_id IN ($1, $2) AND organization_id IS NOT NULL)`,
			physicalAssetModel, softwareModel); n != "0" {
			t.Errorf("%s state or transition rows of the system models carry an organization", n)
		}
		if v := scalar(`SELECT version::text FROM lifecycle_definition WHERE id = $1`, softwareModel); v != "1" {
			t.Errorf("version %s, want 1", v)
		}
		if bound := scalar(`SELECT string_agg(key, ',' ORDER BY key) FROM ci_type
			WHERE organization_id IS NULL AND lifecycle_model_id = $1`, softwareModel); bound != "application,software" {
			t.Errorf("system types bound to software: %s", bound)
		}
		// Read-only for reticora_app.
		for _, table := range []string{"lifecycle_state", "lifecycle_transition", "lifecycle_definition"} {
			column := "definition_id"
			if table == "lifecycle_definition" {
				column = "id"
			}
			tag, updErr := inTenant(f.OrgA, `UPDATE `+table+` SET organization_id = organization_id WHERE `+column+` = $1`, softwareModel)
			if updErr == nil && tag.RowsAffected() != 0 {
				t.Errorf("reticora_app changed %d rows of %s of the software model", tag.RowsAffected(), table)
			}
			if tag, updErr = inTenant(f.OrgA, `DELETE FROM `+table+` WHERE `+column+` = $1`, softwareModel); updErr == nil && tag.RowsAffected() != 0 {
				t.Errorf("reticora_app deleted %d rows of %s of the software model", tag.RowsAffected(), table)
			}
		}
		if _, err = inTenant(f.OrgA, `INSERT INTO lifecycle_state (organization_id, definition_id, key, label)
			VALUES (NULL, $1, 'wp263', 'wp263')`, softwareModel); err == nil {
			t.Error("reticora_app added a global state to the software model")
		}
		mem, _, err := lifecycle.NewMemoryRepository().List(bg, f.OrgA, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var memKeys []string
		for _, d := range mem {
			memKeys = append(memKeys, d.Key)
		}
		if !slices.Equal(memKeys, []string{"physical_asset", "software"}) {
			t.Errorf("memory repository lists %v", memKeys)
		}
	})

	t.Run("inheritance and scope", func(t *testing.T) {
		parent := createType(`{"key":"wp263-app","name":"wp263-app","lifecycle_model_id":"` + softwareModel + `"}`)
		child := createType(`{"key":"wp263-app-sub","name":"wp263-app-sub","parent_type_id":"` + parent.ID + `"}`)
		if child.LifecycleModelID != "" {
			t.Errorf("subtype carries model %q itself", child.LifecycleModelID)
		}
		sub := ciOfType("wp263-sub-ci", child.ID)
		if _, err := inTenant(f.OrgA, `UPDATE ci SET lifecycle_state = 'production' WHERE id = $1`, sub); err != nil {
			t.Errorf("inherited state production: %v", err)
		}
		if _, err := inTenant(f.OrgA, `UPDATE ci SET lifecycle_state = 'deployed' WHERE id = $1`, sub); !violates(err, "ci_lifecycle_requires_model") {
			t.Errorf("physical state deployed on a software CI: %v", err)
		}
		if key, err := resolve("ci", sub); err != nil || key != "software" {
			t.Errorf("model of the subtype CI: %q, %v; want software", key, err)
		}

		physical := createType(`{"key":"wp263-phys","name":"wp263-phys","lifecycle_model_id":"` + physicalAssetModel + `"}`)
		phys := ciOfType("wp263-phys-ci", physical.ID)
		if _, err := resolve("ci", phys); !errors.Is(err, lifecycle.ErrNoLifecycleModel) {
			t.Errorf("CI of a type with physical_asset: %v, want ErrNoLifecycleModel", err)
		}
		for _, state := range []string{"deployed", "production"} {
			if _, err := inTenant(f.OrgA, `UPDATE ci SET lifecycle_state = $2 WHERE id = $1`, phys, state); !violates(err, "ci_lifecycle_requires_model") {
				t.Errorf("state %s on a CI type with a physical model: %v", state, err)
			}
		}

		// Assets: the asset model of the type chain, otherwise physical_asset.
		assetModel := orgModel(f.OrgA, "wp263_asset", "asset", "new", "used")
		assetRoot := createType(`{"key":"wp263-asset","name":"wp263-asset","lifecycle_model_id":"` + assetModel + `"}`)
		assetSub := createType(`{"key":"wp263-asset-sub","name":"wp263-asset-sub","parent_type_id":"` + assetRoot.ID + `"}`)
		typed := f.Asset(t, f.OrgA, "", "wp263-typed")
		untyped := f.Asset(t, f.OrgA, "", "wp263-untyped")
		linkedCI := ciOfType("wp263-linked-ci", assetRoot.ID)
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET asset_type_id = $2 WHERE id = $1`, typed, assetSub.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET ci_id = $2 WHERE id = $1`, untyped, linkedCI); err != nil {
			t.Fatal(err)
		}
		if key, err := resolve("asset", typed); err != nil || key != "wp263_asset" {
			t.Errorf("asset of a subtype of an asset-model type: %q, %v; want wp263_asset", key, err)
		}
		if key, err := resolve("asset", untyped); err != nil || key != "physical_asset" {
			t.Errorf("asset without type, linked to a CI of an asset-model type: %q, %v; want physical_asset", key, err)
		}
		if _, err := resolve("asset", "c0de2630-ffff-4000-8000-00000000dead"); err == nil {
			t.Error("unknown asset resolved")
		}
	})

	t.Run("assignment and change protection", func(t *testing.T) {
		foreign := orgModel(f.OrgB, "wp263_foreign", "ci", "a")
		problem(call(f.OrgA, http.MethodPost, "/api/v1/ci-types",
			`{"key":"wp263-foreign","name":"wp263-foreign","lifecycle_model_id":"`+foreign+`"}`),
			http.StatusUnprocessableEntity, "lifecycle_model_id")
		problem(call(f.OrgA, http.MethodPost, "/api/v1/ci-types",
			`{"key":"wp263-mismatch","name":"wp263-mismatch","lifecycle_model_id":"`+softwareModel+
				`","lifecycle_definition_id":"`+physicalAssetModel+`"}`),
			http.StatusUnprocessableEntity, "lifecycle_model_id")
		legacy := createType(`{"key":"wp263-legacy","name":"wp263-legacy","lifecycle_definition_id":"` + softwareModel + `"}`)
		if legacy.LifecycleModelID != softwareModel || legacy.LifecycleDefinitionID != softwareModel {
			t.Errorf("deprecated field: model %q definition %q, want both %s", legacy.LifecycleModelID, legacy.LifecycleDefinitionID, softwareModel)
		}
		if got := scalar(`SELECT lifecycle_model_id::text FROM ci_type WHERE id = $1`, legacy.ID); got != softwareModel {
			t.Errorf("stored model %s", got)
		}
		problem(call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+legacy.ID, `{"lifecycle_model_id":"`+foreign+`"}`),
			http.StatusUnprocessableEntity, "lifecycle_model_id")
		problem(call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+legacy.ID,
			`{"lifecycle_model_id":"`+softwareModel+`","lifecycle_definition_id":"`+physicalAssetModel+`"}`),
			http.StatusUnprocessableEntity, "lifecycle_model_id")
		// A type bound to a foreign model (written past the API) is not cloned.
		smuggled := scalar(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, 'wp263-smuggled', 'wp263-smuggled', $2) RETURNING id::text`, f.OrgA, foreign)
		problem(call(f.OrgA, http.MethodPost, "/api/v1/ci-types/"+smuggled+"/clone", `{"key":"wp263-smuggled-clone"}`),
			http.StatusUnprocessableEntity, "lifecycle_model_id")

		// Change protection: a CI in production.
		other := orgModel(f.OrgA, "wp263_other", "ci", "planned", "live")
		withProd := orgModel(f.OrgA, "wp263_with_prod", "ci", "evaluation", "production")
		root := createType(`{"key":"wp263-root","name":"wp263-root","lifecycle_model_id":"` + softwareModel + `"}`)
		leaf := createType(`{"key":"wp263-leaf","name":"wp263-leaf","parent_type_id":"` + root.ID + `"}`)
		bare := createType(`{"key":"wp263-bare","name":"wp263-bare"}`)
		live := ciOfType("wp263-live", leaf.ID)
		if _, err := inTenant(f.OrgA, `UPDATE ci SET lifecycle_state = 'production' WHERE id = $1`, live); err != nil {
			t.Fatal(err)
		}
		p := problem(call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+root.ID, `{"lifecycle_model_id":"`+other+`"}`),
			http.StatusConflict, "lifecycle_model_id")
		if len(p.StateCounts) != 1 || p.StateCounts[0] != (citype.LifecycleStateCount{State: "production", Count: 1}) {
			t.Errorf("state counts %+v, want production: 1", p.StateCounts)
		}
		problem(call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+root.ID, `{"lifecycle_model_id":""}`),
			http.StatusConflict, "lifecycle_model_id")
		problem(call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+leaf.ID, `{"parent_type_id":"`+bare.ID+`"}`),
			http.StatusConflict, "parent_type_id")
		problem(call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+leaf.ID, `{"parent_type_id":""}`),
			http.StatusConflict, "parent_type_id")
		if got := scalar(`SELECT lifecycle_model_id::text || ' ' || (SELECT parent_type_id::text FROM ci_type WHERE id = $2)
			FROM ci_type WHERE id = $1`, root.ID, leaf.ID); got != softwareModel+" "+root.ID {
			t.Errorf("after the refused changes: %s", got)
		}
		// A model with the state, and changes without affected CIs, are 200.
		if w := call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+root.ID, `{"lifecycle_model_id":"`+withProd+`"}`); w.Code != http.StatusOK {
			t.Errorf("change to a model with production: %d %s", w.Code, w.Body.String())
		}
		if w := call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+legacy.ID, `{"lifecycle_model_id":"`+other+`"}`); w.Code != http.StatusOK {
			t.Errorf("change without CIs: %d %s", w.Code, w.Body.String())
		} else {
			var typ citype.Type
			_ = json.Unmarshal(w.Body.Bytes(), &typ)
			if typ.LifecycleModelID != other || typ.LifecycleDefinitionID != other {
				t.Errorf("response model %q definition %q, want %s", typ.LifecycleModelID, typ.LifecycleDefinitionID, other)
			}
		}
		if w := call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+bare.ID, `{"parent_type_id":"`+root.ID+`"}`); w.Code != http.StatusOK {
			t.Errorf("parent change without CIs: %d %s", w.Code, w.Body.String())
		}

		// A bound model cannot be deleted.
		if _, err := f.Admin.Exec(bg, `DELETE FROM lifecycle_definition WHERE id = $1`, other); !violates(err, "ci_type_lifecycle_model_fk") {
			t.Errorf("delete of a bound model: %v", err)
		}
	})
}
