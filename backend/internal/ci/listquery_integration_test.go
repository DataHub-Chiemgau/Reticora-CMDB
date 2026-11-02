package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestListFiltersAgainstPostgres covers the list query of API-02 (WP-165)
// on GET /cis against PostgreSQL: filter[field][op] on columns and on
// searchable JSONB attributes with their data type, 400 with a hint for an
// attribute that is not searchable, 422 for unknown fields, sort and
// fields.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestListFiltersAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ae")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	seed := f.CI(t, f.OrgA, f.Client1, "lq-seed")
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT ci_type_id::text FROM ci WHERE id = $1`, seed).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	// The seeded type may be shared by other tests: the definitions are
	// replaced on a rerun and removed afterwards.
	if _, err := f.Admin.Exec(bg, `INSERT INTO attribute_definition (ci_type_id, key, data_type, searchable)
		VALUES ($1, 'lq_ram_gb', 'integer', true), ($1, 'lq_note', 'text', false)
		ON CONFLICT (ci_type_id, key) DO UPDATE SET data_type = EXCLUDED.data_type, searchable = EXCLUDED.searchable`, typeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE ci_type_id = $1 AND key IN ('lq_ram_gb', 'lq_note')`, typeID)
	})

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	call := func(method, target string, body any) (int, map[string]any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, target, bytes.NewReader(raw)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for name, ram := range map[string]int{"lq-a": 8, "lq-b": 16, "lq-c": 32} {
		if code, out := call(http.MethodPost, "/api/v1/cis", map[string]any{"ci_type_id": typeID, "name": name, "client_id": f.Client1,
			"attributes": map[string]any{"lq_ram_gb": ram, "lq_note": "n-" + name}}); code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, out)
		}
	}
	names := func(query url.Values) []string {
		t.Helper()
		code, out := call(http.MethodGet, "/api/v1/cis?"+query.Encode(), nil)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %v", query.Encode(), code, out)
		}
		var got []string
		for _, row := range out["data"].([]any) {
			got = append(got, row.(map[string]any)["name"].(string))
		}
		return got
	}

	// A numeric attribute compares as a number: 16 and 32, not "8" > "16".
	if got := names(url.Values{"filter[attributes.lq_ram_gb][gte]": {"16"}, "filter[name][like]": {"lq-*"}, "sort": {"name"}}); strings.Join(got, ",") != "lq-b,lq-c" {
		t.Errorf("ram >= 16: %v", got)
	}
	if got := names(url.Values{"filter[name][in]": {"lq-a,lq-c"}, "sort": {"-name"}}); strings.Join(got, ",") != "lq-c,lq-a" {
		t.Errorf("in, sort desc: %v", got)
	}
	code, out := call(http.MethodGet, "/api/v1/cis?"+url.Values{"filter[name]": {"lq-b"}, "fields": {"name"}}.Encode(), nil)
	if rows := out["data"].([]any); code != http.StatusOK || len(rows) != 1 || len(rows[0].(map[string]any)) != 2 {
		t.Errorf("fields=name: %d %v", code, out)
	}

	for query, want := range map[string]int{
		url.Values{"filter[attributes.lq_note]": {"x"}}.Encode():        http.StatusBadRequest,
		url.Values{"filter[attributes.lq_missing]": {"1"}}.Encode():     http.StatusUnprocessableEntity,
		url.Values{"filter[attributes.lq_ram_gb][gte]": {"x"}}.Encode(): http.StatusBadRequest,
		url.Values{"filter[unknown]": {"1"}}.Encode():                   http.StatusUnprocessableEntity,
		url.Values{"sort": {"serial_number"}}.Encode():                  http.StatusUnprocessableEntity,
		url.Values{"filter[client_id]": {"not-a-uuid"}}.Encode():        http.StatusBadRequest,
	} {
		code, out := call(http.MethodGet, "/api/v1/cis?"+query, nil)
		if code != want {
			t.Errorf("%s: %d %v, want %d", query, code, out, want)
		}
		if strings.Contains(query, "lq_note") && !strings.Contains(out["detail"].(string), "searchable") {
			t.Errorf("no searchable hint: %v", out)
		}
	}
}

// TestListAttributeFilterPerCIType covers WP-566 (API-02, F-216): the
// searchability and the data type of an attribute filter on GET /cis come
// from the attribute definitions of the CIs' types, not from every
// definition of the key in the organization. Before the fix the repository
// took bool_or(searchable) over all definitions of the key and one data
// type of them, and the predicate had no type reference.
//
// Organization A has the types A and B and a third one without the keys:
//
//   - wp566_color is text, searchable in A, not searchable in B: exact, like
//     and in return only CIs of A, never the CIs of B with the same values;
//     with ci_type_id=B the filter is 400 (B's definition applies).
//   - wp566_size is integer in A and text in B, searchable in both: without
//     a type 400 with the hint to give ci_type_id; with ci_type_id=A (query
//     parameter or filter[ci_type_id]) gte, lte and in compare numbers on
//     the CIs of A, like is 400 (not a text attribute in A); with
//     ci_type_id=B like, in and lte compare text on the CIs of B.
//   - wp566_off is not searchable anywhere: 400 as before; a type that does
//     not define a key: 422.
//
// Organization B defines wp566_color (integer) and wp566_off as searchable
// global definitions and holds CIs with A's values; neither changes a
// result or a status in organization A (the definitions of B are not
// visible under row level security), and B filters by its own definitions.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestListAttributeFilterPerCIType' ./internal/ci/
func TestListAttributeFilterPerCIType(t *testing.T) {
	f := scopetest.Seed(t, "ae")
	bg := context.Background()
	orgCtx := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	ctxA := orgCtx(f.OrgA)
	types := citype.NewPGRepository(f.App)
	newType := func(org, key string) string {
		t.Helper()
		typ := &citype.Type{OrganizationID: org, Key: key, Name: key, IsObservable: true}
		if err := types.Create(orgCtx(org), typ); err != nil {
			t.Fatalf("create type %s: %v", key, err)
		}
		return typ.ID
	}
	define := func(org, typeID, key, dataType string, searchable bool) {
		t.Helper()
		d := &citype.AttributeDefinition{CITypeID: typeID, Key: key, DataType: dataType, Searchable: searchable}
		if err := types.CreateAttributeDefinition(orgCtx(org), org, d); err != nil {
			t.Fatalf("define %s (%s) in type %q of organization %s: %v", key, dataType, typeID, org, err)
		}
	}
	insertCI := func(org, typeID, name string, attrs map[string]any) {
		t.Helper()
		raw, err := json.Marshal(attrs)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, attributes)
			VALUES ($1, $2, $3, 'active', $4)`, org, typeID, name, raw); err != nil {
			t.Fatalf("insert ci %s: %v", name, err)
		}
	}

	typeA, typeB, typeC := newType(f.OrgA, "wp566-a"), newType(f.OrgA, "wp566-b"), newType(f.OrgA, "wp566-c")
	define(f.OrgA, typeA, "wp566_color", "text", true)
	define(f.OrgA, typeB, "wp566_color", "text", false)
	define(f.OrgA, typeA, "wp566_size", "integer", true)
	define(f.OrgA, typeB, "wp566_size", "text", true)
	define(f.OrgA, typeA, "wp566_off", "text", false)
	define(f.OrgA, typeB, "wp566_off", "text", false)
	insertCI(f.OrgA, typeA, "wp566-a5", map[string]any{"wp566_color": "red", "wp566_size": 5, "wp566_off": "x"})
	insertCI(f.OrgA, typeA, "wp566-a20", map[string]any{"wp566_color": "redwood", "wp566_size": 20})
	insertCI(f.OrgA, typeA, "wp566-a100", map[string]any{"wp566_color": "blue", "wp566_size": 100})
	insertCI(f.OrgA, typeB, "wp566-b", map[string]any{"wp566_color": "red", "wp566_size": "30x", "wp566_off": "x"})
	insertCI(f.OrgA, typeB, "wp566-b2", map[string]any{"wp566_color": "redwood", "wp566_size": "9"})
	insertCI(f.OrgA, typeC, "wp566-c", map[string]any{})
	// Organization B: the same keys, searchable and global, another type.
	typeOrgB := newType(f.OrgB, "wp566-a")
	define(f.OrgB, "", "wp566_color", "integer", true)
	define(f.OrgB, "", "wp566_off", "text", true)
	insertCI(f.OrgB, typeOrgB, "wp566-org-b", map[string]any{"wp566_color": 7, "wp566_off": "x", "wp566_size": 20})

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	call := func(ctx context.Context, query url.Values) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+query.Encode(), nil).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	names := func(out map[string]any) string {
		var got []string
		rows, _ := out["data"].([]any)
		for _, row := range rows {
			got = append(got, row.(map[string]any)["name"].(string))
		}
		slices.Sort(got)
		return strings.Join(got, ",")
	}
	q := func(kv ...string) url.Values {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}

	for _, c := range []struct {
		name  string
		ctx   context.Context
		query url.Values
		want  string
	}{
		// Searchable in A, not in B: only CIs of A.
		{"exact, searchable in A only", ctxA, q("filter[attributes.wp566_color]", "red"), "wp566-a5"},
		{"like, searchable in A only", ctxA, q("filter[attributes.wp566_color][like]", "red"), "wp566-a20,wp566-a5"},
		{"in, searchable in A only", ctxA, q("filter[attributes.wp566_color][in]", "red,redwood,blue"), "wp566-a100,wp566-a20,wp566-a5"},
		{"exact with type A", ctxA, q("filter[attributes.wp566_color]", "red", "filter[ci_type_id]", typeA), "wp566-a5"},
		{"like with types A and B", ctxA, q("filter[attributes.wp566_color][like]", "red", "filter[ci_type_id][in]", typeA+","+typeB), "wp566-a20,wp566-a5"},
		// integer in A: numbers, not text ("100" < "20" as text).
		{"gte with type A (filter)", ctxA, q("filter[attributes.wp566_size][gte]", "20", "filter[ci_type_id]", typeA), "wp566-a100,wp566-a20"},
		{"gte with type A (parameter)", ctxA, q("filter[attributes.wp566_size][gte]", "20", "ci_type_id", typeA), "wp566-a100,wp566-a20"},
		{"lte with type A", ctxA, q("filter[attributes.wp566_size][lte]", "20", "ci_type_id", typeA), "wp566-a20,wp566-a5"},
		{"in with type A", ctxA, q("filter[attributes.wp566_size][in]", "5,100.0", "ci_type_id", typeA), "wp566-a100,wp566-a5"},
		// text in B.
		{"like with type B", ctxA, q("filter[attributes.wp566_size][like]", "x", "ci_type_id", typeB), "wp566-b"},
		{"in with type B", ctxA, q("filter[attributes.wp566_size][in]", "9,30x", "filter[ci_type_id]", typeB), "wp566-b,wp566-b2"},
		{"lte as text with type B", ctxA, q("filter[attributes.wp566_size][lte]", "5", "ci_type_id", typeB), "wp566-b"},
		// Organization B: its own definitions only.
		{"organization B, integer", orgCtx(f.OrgB), q("filter[attributes.wp566_color][gte]", "7"), "wp566-org-b"},
		{"organization B, searchable there", orgCtx(f.OrgB), q("filter[attributes.wp566_off]", "x"), "wp566-org-b"},
	} {
		code, out := call(c.ctx, c.query)
		if code != http.StatusOK {
			t.Errorf("%s (%s): %d %v, want 200", c.name, c.query.Encode(), code, out)
			continue
		}
		if got := names(out); got != c.want {
			t.Errorf("%s (%s): %q, want %q", c.name, c.query.Encode(), got, c.want)
		}
	}

	for _, c := range []struct {
		name  string
		query url.Values
		want  int
		hint  string
	}{
		{"different data types without a type", q("filter[attributes.wp566_size][gte]", "20"), http.StatusBadRequest, "ci_type_id"},
		{"different data types, exact without a type", q("filter[attributes.wp566_size]", "20"), http.StatusBadRequest, "ci_type_id"},
		{"different data types, types A and B", q("filter[attributes.wp566_size]", "20", "filter[ci_type_id][in]", typeA+","+typeB), http.StatusBadRequest, "ci_type_id"},
		{"like on the integer of A", q("filter[attributes.wp566_size][like]", "2", "ci_type_id", typeA), http.StatusBadRequest, "like"},
		{"not searchable in B", q("filter[attributes.wp566_color]", "red", "ci_type_id", typeB), http.StatusBadRequest, "searchable"},
		{"not searchable anywhere", q("filter[attributes.wp566_off]", "x"), http.StatusBadRequest, "searchable"},
		{"not searchable, with type A", q("filter[attributes.wp566_off]", "x", "ci_type_id", typeA), http.StatusBadRequest, "searchable"},
		{"not defined in type C", q("filter[attributes.wp566_color]", "red", "ci_type_id", typeC), http.StatusUnprocessableEntity, "unknown attribute"},
		{"not defined anywhere", q("filter[attributes.wp566_missing]", "x"), http.StatusUnprocessableEntity, "unknown attribute"},
	} {
		code, out := call(ctxA, c.query)
		detail, _ := out["detail"].(string)
		if code != c.want || !strings.Contains(detail, c.hint) {
			t.Errorf("%s (%s): %d %v, want %d with %q", c.name, c.query.Encode(), code, out, c.want, c.hint)
		}
	}
}
