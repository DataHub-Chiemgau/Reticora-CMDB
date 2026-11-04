package relationship_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// metadataFields returns the fields of the violations of a 422 and checks
// its problem type.
func metadataFields(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var problem struct {
		Type       string `json:"type"`
		Violations []struct {
			Field  string `json:"field"`
			Detail string `json:"detail"`
		} `json:"violations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem %s: %v", w.Body.String(), err)
	}
	if problem.Type != "https://reticora.io/problems/422" {
		t.Errorf("problem type %q, want https://reticora.io/problems/422", problem.Type)
	}
	fields := make([]string, 0, len(problem.Violations))
	for _, v := range problem.Violations {
		fields = append(fields, v.Field)
	}
	return fields
}

// cabledSchema is the attribute schema of the type wp295-cabled: cable is
// required and follows a pattern (a MET-45 rule), media is an enum, length
// an integer with a legacy validation, mode and speed form a conditional
// pair (speed is required when mode is fast).
const cabledSchema = `[
	{"key":"cable","display_name":{"de":"Kabel","en":"Cable"},"data_type":"text","required":true,
	 "validation":{"op":"pattern","pattern":"^C-[0-9]+$"}},
	{"key":"media","data_type":"enum","enum_values":["copper","fiber"],"default":"copper"},
	{"key":"length","data_type":"integer","validation":{"min":1}},
	{"key":"mode","data_type":"text"},
	{"key":"speed","data_type":"text","conditional":{"required_when":[{"field":"mode","op":"eq","value":"fast"}]}}
]`

// TestRelationshipMetadataSchema covers REL-07 (WP-295): a relationship
// type defines an attribute schema for the attributes of its edges
// (metadata_schema), the type endpoints check its definitions (400), POST
// and PATCH /relationships check the attributes against it (422, PATCH
// the merged state under FOR UPDATE), reserved keys are 422 on
// attributes.<key>, the field permissions come before the schema (403),
// discovery-like writes through the repository are not checked and a
// tenant row with a system key brings its own schema.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipMetadataSchema(t *testing.T) {
	x := seedRelRights(t)
	relationshiptype.NewHandler(x.types).RegisterRoutes(x.mux)
	// A principal without field permission restrictions.
	ctx := tenant.WithTenant(x.f.OrgCtx(x.f.OrgA), tenant.TenantInfo{OrganizationID: x.f.OrgA, UserID: x.f.User})
	bg := context.Background()

	var cabledID string
	t.Run("type endpoints", func(t *testing.T) {
		w := x.do(ctx, http.MethodPost, "/api/v1/relationship-types",
			`{"key":"wp295-cabled","display_name":"cabled","reverse_name":"cabled","metadata_schema":`+cabledSchema+`}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST type with schema: %d %s", w.Code, w.Body.String())
		}
		var created relationshiptype.Type
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		cabledID = created.ID
		x.typeIDs["wp295-cabled"] = cabledID
		if len(created.MetadataSchema) != 5 || created.MetadataSchema[0].Key != "cable" || !created.MetadataSchema[0].Required ||
			created.MetadataSchema[0].DisplayName["de"] != "Kabel" || created.MetadataSchema[3].DisplayName["en"] != "mode" {
			t.Fatalf("created schema %+v", created.MetadataSchema)
		}
		// The legacy validation is stored as a MET-45 rule.
		var stored string
		if err := x.f.Admin.QueryRow(bg, `SELECT metadata_schema->2->>'validation' FROM relationship_type WHERE id = $1`, cabledID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stored, `"op": "range"`) || !strings.Contains(stored, `"min": 1`) {
			t.Errorf("stored legacy validation %s, want a range rule", stored)
		}

		for name, schema := range map[string]string{
			"unknown data_type":       `[{"key":"a","data_type":"colour"}]`,
			"reference":               `[{"key":"a","data_type":"reference","reference_target":"ci"}]`,
			"user":                    `[{"key":"a","data_type":"user"}]`,
			"file":                    `[{"key":"a","data_type":"file"}]`,
			"json":                    `[{"key":"a","data_type":"json"}]`,
			"legacy type name":        `[{"key":"a","data_type":"string"}]`,
			"duplicate key":           `[{"key":"a","data_type":"text"},{"key":"a","data_type":"integer"}]`,
			"key provenance":          `[{"key":"provenance","data_type":"text"}]`,
			"key source_ci_id":        `[{"key":"source_ci_id","data_type":"text"}]`,
			"empty key":               `[{"key":" ","data_type":"text"}]`,
			"legacy invalid pattern":  `[{"key":"a","data_type":"text","validation":{"pattern":"("}}]`,
			"rule invalid pattern":    `[{"key":"a","data_type":"text","validation":{"op":"pattern","pattern":"("}}]`,
			"unsatisfiable rule":      `[{"key":"a","data_type":"integer","validation":{"op":"range","min":5,"max":1}}]`,
			"unknown member":          `[{"key":"a","data_type":"text","unit":"m"}]`,
			"invalid conditional":     `[{"key":"a","data_type":"text","conditional":{"visible_when":[{"field":"b","op":"like"}]}}]`,
			"default of another type": `[{"key":"a","data_type":"integer","default":"x"}]`,
			"not an array":            `{"key":"a","data_type":"text"}`,
		} {
			w := x.do(ctx, http.MethodPost, "/api/v1/relationship-types",
				`{"key":"wp295-bad","display_name":"bad","reverse_name":"bad","metadata_schema":`+schema+`}`)
			if w.Code != http.StatusBadRequest {
				t.Errorf("POST type with %s: %d %s, want 400", name, w.Code, w.Body.String())
			}
			w = x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/wp295-cabled", `{"metadata_schema":`+schema+`}`)
			if w.Code != http.StatusBadRequest {
				t.Errorf("PATCH type with %s: %d %s, want 400", name, w.Code, w.Body.String())
			}
		}

		schemaLen := func(path string) int {
			t.Helper()
			w := x.do(ctx, http.MethodGet, path, "")
			var typ struct {
				MetadataSchema []json.RawMessage `json:"metadata_schema"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &typ) != nil || typ.MetadataSchema == nil {
				t.Fatalf("GET %s: %d %s, want a metadata_schema array", path, w.Code, w.Body.String())
			}
			return len(typ.MetadataSchema)
		}
		// Without metadata_schema the schema stays, [] clears it.
		if w := x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/wp295-cabled", `{"description":"cabled edges"}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH without schema: %d %s", w.Code, w.Body.String())
		}
		if n := schemaLen("/api/v1/relationship-types/wp295-cabled"); n != 5 {
			t.Errorf("schema after PATCH without metadata_schema: %d definitions, want 5", n)
		}
		if w := x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/wp295-cabled", `{"metadata_schema":[]}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH schema []: %d %s", w.Code, w.Body.String())
		}
		if n := schemaLen("/api/v1/relationship-types/wp295-cabled"); n != 0 {
			t.Errorf("schema after PATCH []: %d definitions, want 0", n)
		}
		if w := x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/wp295-cabled", `{"metadata_schema":`+cabledSchema+`}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH schema: %d %s", w.Code, w.Body.String())
		}

		// System types are read-only and have no schema.
		if w := x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/powered_by",
			`{"metadata_schema":[{"key":"a","data_type":"text"}]}`); w.Code != http.StatusNotFound {
			t.Errorf("PATCH system type: %d %s, want 404", w.Code, w.Body.String())
		}
		if n := schemaLen("/api/v1/relationship-types/powered_by"); n != 0 {
			t.Errorf("system type powered_by: %d definitions, want 0", n)
		}
		var systemSchemas int
		if err := x.f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_type WHERE organization_id IS NULL AND metadata_schema <> '[]'::jsonb`).Scan(&systemSchemas); err != nil || systemSchemas != 0 {
			t.Errorf("system rows with a schema: %d (%v)", systemSchemas, err)
		}
	})
	if cabledID == "" {
		t.Fatal("type wp295-cabled missing")
	}

	post := func(ctx context.Context, attrs string) *httptest.ResponseRecorder {
		return x.do(ctx, http.MethodPost, "/api/v1/relationships",
			`{"source_ci_id":"`+x.cis["a1"]+`","target_ci_id":"`+x.cis["a2"]+`","rel_type":"wp295-cabled","attributes":`+attrs+`}`)
	}
	storedAttrs := func(id string) map[string]any {
		t.Helper()
		var raw []byte
		if err := x.f.Admin.QueryRow(bg, `SELECT attributes FROM ci_relationship WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatalf("read edge %s: %v", id, err)
		}
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	var edge string
	t.Run("POST", func(t *testing.T) {
		for _, c := range []struct {
			attrs string
			want  []string
		}{
			{`{"media":"copper"}`, []string{"cable"}},
			{`{"cable":"X-1","media":"radio","length":0}`, []string{"cable", "length", "media"}},
			{`{"cable":"C-1","mode":"fast"}`, []string{"speed"}},
			{`{"cable":"C-1","length":"long"}`, []string{"length"}},
			{`{"provenance":{"actor":"forged"},"source_ci_id":"x","cable":"C-1"}`, []string{"attributes.provenance", "attributes.source_ci_id"}},
			{`{"notes":"n","media":"radio"}`, []string{"attributes.notes"}},
		} {
			w := post(ctx, c.attrs)
			if w.Code != http.StatusUnprocessableEntity || !slices.Equal(metadataFields(t, w), c.want) {
				t.Errorf("POST %s: %d %s, want 422 on %v", c.attrs, w.Code, w.Body.String(), c.want)
			}
		}
		var count int
		if err := x.f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE rel_type = 'wp295-cabled'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("refused POSTs stored %d edges (%v)", count, err)
		}
		// A key without definition is accepted; the server records the
		// provenance.
		w := post(ctx, `{"cable":"C-7","media":"fiber","label":"free text"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST valid attributes: %d %s", w.Code, w.Body.String())
		}
		var rel relationship.Relationship
		if err := json.Unmarshal(w.Body.Bytes(), &rel); err != nil {
			t.Fatal(err)
		}
		edge = rel.ID
		got := storedAttrs(edge)
		if got["cable"] != "C-7" || got["label"] != "free text" || got[relationship.ProvenanceAttribute] == nil {
			t.Errorf("stored attributes %v", got)
		}
		// default only prefills a form: media is not set by the server.
		w = x.do(ctx, http.MethodPost, "/api/v1/relationships",
			`{"source_ci_id":"`+x.cis["a1"]+`","target_ci_id":"`+x.cis["ow"]+`","rel_type":"wp295-cabled","attributes":{"cable":"C-8"}}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST without media: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rel); err != nil {
			t.Fatal(err)
		}
		if _, set := storedAttrs(rel.ID)["media"]; set {
			t.Errorf("the default of media was set by the server")
		}
		if _, err := x.f.Admin.Exec(bg, `DELETE FROM ci_relationship WHERE id = $1`, rel.ID); err != nil {
			t.Fatal(err)
		}
	})
	if edge == "" {
		t.Fatal("edge missing")
	}

	patch := func(body string) *httptest.ResponseRecorder {
		return x.do(ctx, http.MethodPatch, "/api/v1/relationships/"+edge, body)
	}
	t.Run("PATCH", func(t *testing.T) {
		for _, c := range []struct {
			body string
			want []string
		}{
			{`{"attributes":{"source_ci_id":"x"}}`, []string{"attributes.source_ci_id"}},
			{`{"attributes":{"provenance":"forged","rel_type":"x"}}`, []string{"attributes.provenance", "attributes.rel_type"}},
			{`{"attributes":{"cable":null}}`, []string{"cable"}},
			{`{"attributes":{"cable":"D-1","media":"radio"}}`, []string{"cable", "media"}},
			// The merged state: mode fast asks for speed.
			{`{"attributes":{"mode":"fast"}}`, []string{"speed"}},
		} {
			w := patch(c.body)
			if w.Code != http.StatusUnprocessableEntity || !slices.Equal(metadataFields(t, w), c.want) {
				t.Errorf("PATCH %s: %d %s, want 422 on %v", c.body, w.Code, w.Body.String(), c.want)
			}
		}
		if got := storedAttrs(edge); got["cable"] != "C-7" || got["media"] != "fiber" || got["mode"] != nil {
			t.Fatalf("refused PATCHes changed the edge: %v", got)
		}
		// The required cable of the stored state satisfies the requirement.
		if w := patch(`{"attributes":{"media":"copper","label":null}}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH media: %d %s", w.Code, w.Body.String())
		}
		if w := patch(`{"attributes":{"mode":"fast","speed":"10G"}}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH mode and speed: %d %s", w.Code, w.Body.String())
		}

		// A schema change makes the edge invalid: a PATCH of the
		// verification state keeps working, one of the attributes is
		// checked against the new schema.
		changed := strings.Replace(cabledSchema, `{"key":"mode","data_type":"text"}`,
			`{"key":"mode","data_type":"text"},{"key":"rack","data_type":"text","required":true}`, 1)
		if w := x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/wp295-cabled", `{"metadata_schema":`+changed+`}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH schema with rack: %d %s", w.Code, w.Body.String())
		}
		if w := patch(`{"verification_state":"disputed"}`); w.Code != http.StatusOK {
			t.Errorf("PATCH verification_state of an edge the schema change made invalid: %d %s, want 200", w.Code, w.Body.String())
		}
		if w := patch(`{"notes":"checked"}`); w.Code != http.StatusOK {
			t.Errorf("PATCH notes: %d %s, want 200", w.Code, w.Body.String())
		}
		if w := patch(`{"attributes":{"media":"fiber"}}`); w.Code != http.StatusUnprocessableEntity || !slices.Equal(metadataFields(t, w), []string{"rack"}) {
			t.Errorf("PATCH attributes after the schema change: %d %s, want 422 on rack", w.Code, w.Body.String())
		}
		if w := patch(`{"attributes":{"rack":"R1"}}`); w.Code != http.StatusOK {
			t.Errorf("PATCH rack: %d %s", w.Code, w.Body.String())
		}
		if w := x.do(ctx, http.MethodPatch, "/api/v1/relationship-types/wp295-cabled", `{"metadata_schema":`+cabledSchema+`}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH schema back: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("parallel PATCHes are serialized", func(t *testing.T) {
		// mode fast asks for speed: one PATCH sets both, the other clears
		// speed. Whatever the order, the stored edge never has mode fast
		// without speed: the second PATCH checks the state after the first.
		for range 8 {
			if _, err := x.f.Admin.Exec(bg, `UPDATE ci_relationship SET attributes = attributes - 'mode' || '{"speed":"1G"}'::jsonb WHERE id = $1`, edge); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i, body := range []string{`{"attributes":{"mode":"fast","speed":"10G"}}`, `{"attributes":{"speed":null}}`} {
				wg.Go(func() { codes[i] = patch(body).Code })
			}
			wg.Wait()
			got := storedAttrs(edge)
			if got["mode"] == "fast" && (got["speed"] == nil || got["speed"] == "") {
				t.Fatalf("parallel PATCHes stored mode fast without speed (codes %v): %v", codes, got)
			}
			ok := (codes[0] == http.StatusOK && codes[1] == http.StatusUnprocessableEntity) || (codes[0] == http.StatusOK && codes[1] == http.StatusOK)
			if !ok {
				t.Fatalf("parallel PATCHes answered %v, want [200 422] or [200 200]", codes)
			}
		}
	})

	t.Run("field permissions before the schema", func(t *testing.T) {
		x.reset(t)
		defer x.reset(t)
		x.grant(t, "full", "wp295-cabled", "", "write")
		x.grant(t, "full", "", "attributes.secret", "none")
		restricted := x.ctx(t, assignment{role: "full"})
		// Reserved keys (422) before the field permissions (403).
		if w := post(restricted, `{"provenance":"x","secret":"s"}`); w.Code != http.StatusUnprocessableEntity ||
			!slices.Equal(metadataFields(t, w), []string{"attributes.provenance"}) {
			t.Errorf("POST reserved and forbidden: %d %s, want 422 on attributes.provenance", w.Code, w.Body.String())
		}
		// The field permissions (403) before the schema (422: cable).
		if w := post(restricted, `{"secret":"s"}`); w.Code != http.StatusForbidden || !slices.Equal(forbiddenFields(t, w), []string{"attributes.secret"}) {
			t.Errorf("POST forbidden and invalid: %d %s, want 403 on attributes.secret", w.Code, w.Body.String())
		}
		w := x.do(restricted, http.MethodPatch, "/api/v1/relationships/"+edge, `{"attributes":{"secret":"s","cable":null}}`)
		if w.Code != http.StatusForbidden {
			t.Errorf("PATCH forbidden and invalid: %d %s, want 403", w.Code, w.Body.String())
		}
		w = x.do(restricted, http.MethodPatch, "/api/v1/relationships/"+edge, `{"attributes":{"cable":null}}`)
		if w.Code != http.StatusUnprocessableEntity || !slices.Equal(metadataFields(t, w), []string{"cable"}) {
			t.Errorf("PATCH allowed and invalid: %d %s, want 422 on cable", w.Code, w.Body.String())
		}
	})

	t.Run("repository writes are not checked", func(t *testing.T) {
		// Discovery creates edges through the repository with its own
		// attributes: they are stored as they are.
		rel := &relationship.Relationship{OrganizationID: x.f.OrgA, SourceCIID: x.cis["a2"], TargetCIID: x.cis["a1"],
			RelType: "wp295-cabled", Source: "discovery", Attributes: map[string]any{"media": "radio", "outlet": "4"}}
		if err := x.repo.Create(x.f.OrgCtx(x.f.OrgA), rel); err != nil {
			t.Fatalf("repository create with schema violations: %v", err)
		}
		if got := storedAttrs(rel.ID); got["media"] != "radio" || got["outlet"] != "4" {
			t.Errorf("repository edge stored %v", got)
		}
		// The read paths return the attributes unchanged.
		edges := x.list(t, ctx, "/api/v1/relationships?rel_type=wp295-cabled")
		if e, ok := edges[rel.ID]; !ok || !slices.Equal(e.attrKeys(), []string{"media", "outlet"}) {
			t.Errorf("GET of the repository edge: %+v", edges[rel.ID])
		}
		if _, err := x.f.Admin.Exec(bg, `DELETE FROM ci_relationship WHERE id = $1`, rel.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tenant row with a system key", func(t *testing.T) {
		// A tenant row with the key of a system type (from before WP-294,
		// inserted by an administrator) brings its own schema.
		if _, err := x.f.Admin.Exec(bg, `INSERT INTO relationship_type (id, organization_id, key, display_name, reverse_name, metadata_schema)
			VALUES ($1, $2, 'powered_by', 'powered by', 'powers', $3::jsonb)`, x.f.ID(), x.f.OrgA,
			`[{"key":"feed","data_type":"enum","required":true,"enum_values":["A","B"]}]`); err != nil {
			t.Fatalf("tenant row powered_by: %v", err)
		}
		powered := func(attrs string) *httptest.ResponseRecorder {
			return x.do(ctx, http.MethodPost, "/api/v1/relationships",
				`{"source_ci_id":"`+x.cis["a2"]+`","target_ci_id":"`+x.cis["ow"]+`","rel_type":"powered_by","attributes":`+attrs+`}`)
		}
		if w := powered(`{"outlet":"3"}`); w.Code != http.StatusUnprocessableEntity || !slices.Equal(metadataFields(t, w), []string{"feed"}) {
			t.Errorf("POST powered_by without feed: %d %s, want 422 on feed", w.Code, w.Body.String())
		}
		if w := powered(`{"outlet":"3","feed":"C"}`); w.Code != http.StatusUnprocessableEntity || !slices.Equal(metadataFields(t, w), []string{"feed"}) {
			t.Errorf("POST powered_by with feed C: %d %s, want 422 on feed", w.Code, w.Body.String())
		}
		if w := powered(`{"outlet":"3","feed":"A"}`); w.Code != http.StatusCreated {
			t.Errorf("POST powered_by with feed A: %d %s, want 201", w.Code, w.Body.String())
		}
	})
}
