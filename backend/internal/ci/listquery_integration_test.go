package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestListFiltersAgainstPostgres covers the list query of API-02 (WP-165)
// on GET /cis against PostgreSQL: filter[field][op] on columns and on
// searchable JSONB attributes with their data type, 400 with a hint for an
// attribute that is not searchable, 422 for unknown fields, sort and
// fields. checkCappedTotalAndNameSort adds total after E-104 and the page
// sorted by name (WP-839).
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

	t.Run("capped total and sort by name", func(t *testing.T) { checkCappedTotalAndNameSort(t, f) })
}

// listNameIndex is the index of the CI list sorted by name (WP-839).
const listNameIndex = "idx_ci_list_name"

// cappedPage is the body of GET /cis with the mark of a capped total.
type cappedPage struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Total       int   `json:"total"`
	TotalCapped *bool `json:"total_capped"`
	HasMore     bool  `json:"has_more"`
}

// ciRowsRead returns the rows the nodes of an EXPLAIN (ANALYZE, FORMAT
// JSON) plan read from ci: returned rows plus rows removed by a filter or
// an index recheck, over all loops.
func ciRowsRead(nodes []map[string]any) int64 {
	var read float64
	for _, n := range nodes {
		if n["Relation Name"] != "ci" {
			continue
		}
		rows, _ := n["Actual Rows"].(float64)
		filtered, _ := n["Rows Removed by Filter"].(float64)
		rechecked, _ := n["Rows Removed by Index Recheck"].(float64)
		loops, _ := n["Actual Loops"].(float64)
		read += (rows + filtered + rechecked) * loops
	}
	return int64(read)
}

// checkCappedTotalAndNameSort covers WP-839 (API-01, NFR-02; E-104, E-17)
// on GET /cis with organization A at 10,000 CIs and one more, placed as the
// seed places them (client, site):
//
//   - total is exact up to api.TotalCap: 10,000 CIs answer 10,000 without
//     total_capped. With 10,001 CIs total is 10,000 and total_capped true;
//     the count reads at most api.TotalCountLimit CIs (LIMIT in the
//     statement), has_more stays right on a page next to the cap and on the
//     last page. A filter below the cap keeps the exact count.
//   - The first page with sort_by=name (descending, and ascending with
//     sort_dir) follows name and id. Its product SQL, explained with
//     ANALYZE as the application role after the settings of the request
//     (the frame of WP-436), reads the CIs from idx_ci_list_name without
//     sorting them and reads at most 1 % of the organization's CIs.
func checkCappedTotalAndNameSort(t *testing.T, f *scopetest.Fixture) {
	bg := context.Background()
	var existing int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, f.OrgA).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	seedListCIs(t, f, api.TotalCap-existing)
	if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
		t.Fatal(err)
	}

	cfg := f.App.Config()
	trace := &statementLog{}
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(pool))).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	get := func(query url.Values) (cappedPage, []loggedStatement) {
		t.Helper()
		before := trace.mark()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+query.Encode(), nil).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /cis?%s: %d %s", query.Encode(), w.Code, w.Body.String())
		}
		var page cappedPage
		if jerr := json.Unmarshal(w.Body.Bytes(), &page); jerr != nil {
			t.Fatal(jerr)
		}
		return page, trace.since(before)
	}
	isCapped := func(p cappedPage) bool { return p.TotalCapped != nil && *p.TotalCapped }
	countsOf := func(stmts []loggedStatement) []string {
		var out []string
		for _, s := range stmts {
			if strings.Contains(s.sql, "COUNT(*)") {
				out = append(out, s.sql)
			}
		}
		return out
	}
	get(url.Values{"limit": {"1"}}) // warms the pool

	// At the cap: exact, without the mark.
	first, firstStmts := get(url.Values{"limit": {"50"}})
	if first.Total != api.TotalCap || first.TotalCapped != nil || !first.HasMore {
		t.Errorf("%d CIs: total %d, total_capped %v, has_more %v; want %d, absent, true",
			api.TotalCap, first.Total, first.TotalCapped, first.HasMore, api.TotalCap)
	}
	if c := countsOf(firstStmts); len(c) != 1 || !strings.Contains(c[0], fmt.Sprintf("LIMIT %d", api.TotalCountLimit)) {
		t.Errorf("%d CIs: count statements %q, want one that reads at most %d CIs", api.TotalCap, c, api.TotalCountLimit)
	}

	// The first page sorted by name, both directions, and its plan.
	var byName []string
	rows, err := f.Admin.Query(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL ORDER BY name DESC, id DESC`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		byName = append(byName, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	ascending := make([]string, 0, 50)
	for i := len(byName) - 1; i >= len(byName)-50; i-- {
		ascending = append(ascending, byName[i])
	}
	for _, c := range []struct {
		dir  string
		want []string
	}{{"desc", byName[:50]}, {"asc", ascending}} {
		named, nstmts := get(url.Values{"limit": {"50"}, "sort_by": {"name"}, "sort_dir": {c.dir}})
		got := make([]string, len(named.Data))
		for i, d := range named.Data {
			got[i] = d.ID
		}
		if !slices.Equal(got, c.want) || named.Total != api.TotalCap {
			t.Errorf("sort_by=name %s: total %d, ids %v; want %d and the first 50 of name, id %s", c.dir, named.Total, got, api.TotalCap, c.dir)
		}
		var pageStmt *loggedStatement
		var settings []string
		for i := range nstmts {
			if strings.Contains(nstmts[i].sql, "ORDER BY") {
				pageStmt = &nstmts[i]
				break
			}
			if strings.HasPrefix(nstmts[i].sql, "SET LOCAL ") {
				settings = append(settings, nstmts[i].sql)
			}
		}
		if pageStmt == nil {
			t.Fatalf("sort_by=name %s: no page statement among %d", c.dir, len(nstmts))
		}
		nodes := planNodes(t, explainAsAppWith(t, f.App, f.OrgA, "ANALYZE, FORMAT JSON", *pageStmt, settings...))
		usesIndex := false
		for _, n := range nodes {
			switch n["Node Type"] {
			case "Index Scan", "Index Only Scan":
				usesIndex = usesIndex || (n["Index Name"] == listNameIndex && n["Relation Name"] == "ci")
			case "Sort", "Incremental Sort":
				t.Errorf("sort_by=name %s: the page sorts by %v: %s", c.dir, n["Sort Key"], describePlan(nodes))
			}
		}
		if !usesIndex {
			t.Errorf("sort_by=name %s: the page does not read %s: %s", c.dir, listNameIndex, describePlan(nodes))
		}
		if read, most := ciRowsRead(nodes), int64(api.TotalCap/100); read > most {
			t.Errorf("sort_by=name %s: the page reads %d CIs, at most %d (1 %% of the organization): %s", c.dir, read, most, describePlan(nodes))
		} else {
			t.Logf("sort_by=name %s: %d CIs read: %s", c.dir, read, describePlan(nodes))
		}
	}

	// Beyond the cap: total 10,000 and total_capped, on the first page, on
	// a full page next to the cap and on the last page.
	f.CI(t, f.OrgA, f.Client1, "list-beyond-cap")
	for _, c := range []struct {
		offset  int
		rows    int
		hasMore bool
	}{{0, 50, true}, {api.TotalCap - 50, 50, true}, {api.TotalCap - 9, 10, false}} {
		p, _ := get(url.Values{"limit": {"50"}, "offset": {fmt.Sprint(c.offset)}})
		if p.Total != api.TotalCap || !isCapped(p) || p.HasMore != c.hasMore || len(p.Data) != c.rows {
			t.Errorf("%d CIs, offset %d: %d rows, total %d, total_capped %v, has_more %v; want %d, %d, true, %v",
				api.TotalCountLimit, c.offset, len(p.Data), p.Total, p.TotalCapped, p.HasMore, c.rows, api.TotalCap, c.hasMore)
		}
	}
	// A filter below the cap counts exactly.
	if p, _ := get(url.Values{"limit": {"50"}, "status": {"maintenance"}}); p.Total == 0 || p.Total >= api.TotalCap || isCapped(p) {
		t.Errorf("filter status=maintenance: total %d, total_capped %v; want the exact count below the cap", p.Total, p.TotalCapped)
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
