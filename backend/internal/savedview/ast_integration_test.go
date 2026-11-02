package savedview_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestFilterASTAgainstPostgres runs the filter AST of SRC-03 (WP-187)
// against PostgreSQL: or, not, a numeric attribute comparison and an asset
// field of the linked asset select exactly the expected CIs.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFilterASTAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bc")
	bg := context.Background()
	big := f.CI(t, f.OrgA, f.Client1, "ast-big")
	small := f.CI(t, f.OrgA, f.Client1, "ast-small")
	lab := f.CI(t, f.OrgA, f.Client1, "ast-lab")
	asset := f.Asset(t, f.OrgA, f.Client1, "AST-1")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE ci SET attributes = '{"ram_gb": 64}' WHERE id = $1`, []any{big}},
		{`UPDATE ci SET attributes = '{"ram_gb": 8}', status = 'maintenance' WHERE id = $1`, []any{small}},
		{`UPDATE ci SET attributes = '{"ram_gb": 128}' WHERE id = $1`, []any{lab}},
		{`UPDATE asset SET warranty_end = '2026-12-31', ci_id = $2 WHERE id = $1`, []any{asset, big}},
		{`UPDATE ci SET asset_id = $2 WHERE id = $1`, []any{big, asset}},
	} {
		if _, err := f.Admin.Exec(bg, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	engine := savedview.NewPGQueryEngine(f.App)
	run := func(raw string) map[string]bool {
		t.Helper()
		var where savedview.Expr
		if err := json.Unmarshal([]byte(raw), &where); err != nil {
			t.Fatal(err)
		}
		results, _, err := engine.Query(f.OrgCtx(f.OrgA), f.OrgA, savedview.FilterSpec{EntityKind: "ci", Where: &where}, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, r := range results {
			out[r.ID] = true
		}
		return out
	}
	// RAM of at least 16 GB and not the lab machine: big only.
	got := run(`{"and": [{"field": "attributes.ram_gb", "op": "gte", "value": 16}, {"not": {"field": "name", "op": "like", "value": "*lab"}}]}`)
	if len(got) != 1 || !got[big] {
		t.Errorf("and/not: %v", got)
	}
	// In maintenance or with a warranty ending in 2026: small and big.
	got = run(`{"or": [{"field": "status", "op": "eq", "value": "maintenance"}, {"field": "asset.warranty_end", "op": "lt", "value": "2027-01-01"}]}`)
	if len(got) != 2 || !got[small] || !got[big] {
		t.Errorf("or with an asset field: %v", got)
	}
}

// sqlLeakMarkers are texts of PostgreSQL and pgx errors that no response
// may carry (API-03, F-047, F-179).
var sqlLeakMarkers = []string{"SQLSTATE", "ERROR:", "invalid input syntax", "syntax error", "invalid byte sequence",
	"value out of range", "displacement", "violates", "constraint \"", "row-level security", "jsonb", "::", "numeric",
	"timestamptz", "can't scan", "failed to encode", "OID "}

// sqlLeak returns the first marker of sqlLeakMarkers in body, or "".
func sqlLeak(body string) string {
	for _, m := range sqlLeakMarkers {
		if strings.Contains(body, m) {
			return m
		}
	}
	return ""
}

// searchQuery posts body to POST /api/v1/search/query as an org-wide
// principal of organization A and returns the status, the sorted names of
// the results, the total, the field of a problem and the raw body.
func searchQuery(v *auditT7Views, body string) (code int, names []string, total int, field, raw string) {
	v.t.Helper()
	code, out, raw := v.call(v.f.OrgA, v.f.User, http.MethodPost, "/api/v1/search/query", body)
	field, _ = out["field"].(string)
	if n, ok := out["total"].(float64); ok {
		total = int(n)
	}
	data, _ := out["data"].([]any)
	for _, item := range data {
		row, _ := item.(map[string]any)
		name, _ := row["name"].(string)
		names = append(names, name)
	}
	sort.Strings(names)
	return code, names, total, field, raw
}

// setAttributes stores the JSON attrs as the dynamic attributes of a new
// client-1 CI named name and returns its id.
func setAttributes(t *testing.T, f *scopetest.Fixture, name, attrs string) string {
	t.Helper()
	id := f.CI(t, f.OrgA, f.Client1, name)
	if _, err := f.Admin.Exec(context.Background(), `UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, id, attrs); err != nil {
		t.Fatalf("attributes of %s: %v", name, err)
	}
	return id
}

// TestFilterASTMixedTypesAgainstPostgres is the binding retest of F-165
// (WP-405) on POST /api/v1/search/query: ram_gb is 64, 8, the text "16GB"
// and missing on a fourth CI. The numeric comparison casts numbers only,
// so gte 16 answers 200 with exactly 64 instead of failing the query on
// "16GB", in whatever order the predicates are combined; eq, ne, in, not
// and exists follow the same rule, and boolean and time comparisons on
// attributes check the JSON type the same way.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFilterASTMixedTypesAgainstPostgres(t *testing.T) {
	v := newAuditT7Views(t, "bc")
	setAttributes(t, v.f, "mixed-64", `{"ram_gb": 64, "monitored": true, "installed": "2024-06-01"}`)
	setAttributes(t, v.f, "mixed-8", `{"ram_gb": 8, "monitored": "true", "installed": "2023-01-01T10:00:00+02:00"}`)
	setAttributes(t, v.f, "mixed-16gb", `{"ram_gb": "16GB", "monitored": "yes", "installed": "today"}`)
	setAttributes(t, v.f, "mixed-none", `{"monitored": null, "installed": 20240601}`)
	const all = `{"field": "name", "op": "like", "value": "mixed-*"}`
	for _, c := range []struct {
		name, where string
		want        []string
	}{
		{"gte 16", `{"field": "attributes.ram_gb", "op": "gte", "value": 16}`, []string{"mixed-64"}},
		{"eq 8", `{"field": "attributes.ram_gb", "op": "eq", "value": 8}`, []string{"mixed-8"}},
		// ne is the negation of eq (IS DISTINCT FROM, as for columns):
		// neither the text nor the missing key is the number 8.
		{"ne 8", `{"field": "attributes.ram_gb", "op": "ne", "value": 8}`, []string{"mixed-16gb", "mixed-64", "mixed-none"}},
		{"in [8, 64]", `{"field": "attributes.ram_gb", "op": "in", "value": [8, 64]}`, []string{"mixed-64", "mixed-8"}},
		{"exists false", `{"field": "attributes.ram_gb", "op": "exists", "value": false}`, []string{"mixed-none"}},
		{"exists", `{"field": "attributes.ram_gb", "op": "exists"}`, []string{"mixed-16gb", "mixed-64", "mixed-8"}},
		{"not gte 16", `{"not": {"field": "attributes.ram_gb", "op": "gte", "value": 16}}`, []string{"mixed-16gb", "mixed-8", "mixed-none"}},
		{"text eq", `{"field": "attributes.ram_gb", "op": "eq", "value": "16GB"}`, []string{"mixed-16gb"}},
		// The order of the predicates does not matter.
		{"and, comparison first", `{"and": [{"field": "attributes.ram_gb", "op": "gte", "value": 16}, ` + all + `]}`, []string{"mixed-64"}},
		{"and, comparison last", `{"and": [` + all + `, {"field": "attributes.ram_gb", "op": "gte", "value": 16}]}`, []string{"mixed-64"}},
		{"and on the text CI only", `{"and": [{"field": "name", "op": "eq", "value": "mixed-16gb"}, {"field": "attributes.ram_gb", "op": "gte", "value": 16}]}`, nil},
		{"or", `{"or": [{"field": "name", "op": "eq", "value": "mixed-16gb"}, {"field": "attributes.ram_gb", "op": "lt", "value": 10}]}`,
			[]string{"mixed-16gb", "mixed-8"}},
		// Booleans and times: a value of another JSON type does not match.
		{"boolean eq", `{"field": "attributes.monitored", "op": "eq", "value": true}`, []string{"mixed-64"}},
		{"boolean ne", `{"field": "attributes.monitored", "op": "ne", "value": true}`, []string{"mixed-16gb", "mixed-8", "mixed-none"}},
		{"time gte", `{"field": "attributes.installed", "op": "gte", "value": "2024-01-01"}`, []string{"mixed-64"}},
		{"time lt", `{"field": "attributes.installed", "op": "lt", "value": "2023-01-01T09:00:00Z"}`, []string{"mixed-8"}},
	} {
		code, names, total, _, raw := searchQuery(v, `{"entity_kind": "ci", "where": `+c.where+`}`)
		if code != http.StatusOK || strings.Join(names, ",") != strings.Join(c.want, ",") || total != len(c.want) {
			t.Errorf("%s: %d %v (total %d), want 200 %v: %s", c.name, code, names, total, c.want, raw)
		}
	}
}

// TestFilterASTClientScopeAgainstPostgres: the filter AST runs under the
// principal's scope (TEN-06, SRC-03). An OR over a client-1 and a client-2
// CI returns only the client-1 CI to a principal restricted to client 1
// (total 1) and both to an org-wide principal.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFilterASTClientScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bc")
	own := f.CI(t, f.OrgA, f.Client1, "ast-scope-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "ast-scope-c2")
	var where savedview.Expr
	if err := json.Unmarshal([]byte(`{"or": [{"field": "name", "op": "eq", "value": "ast-scope-c1"},
		{"field": "client_id", "op": "eq", "value": "`+f.Client2+`"}]}`), &where); err != nil {
		t.Fatal(err)
	}
	engine := savedview.NewPGQueryEngine(f.App)
	for _, c := range []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"client 1", f.ClientCtx(f.Client1), []string{own}},
		{"org-wide", f.OrgCtx(f.OrgA), []string{own, foreign}},
	} {
		results, total, err := engine.Query(c.ctx, f.OrgA, savedview.FilterSpec{EntityKind: "ci", Where: &where}, api.PaginationParams{Limit: 100})
		var got []string
		for _, r := range results {
			got = append(got, r.ID)
		}
		sort.Strings(got)
		sort.Strings(c.want)
		if err != nil || strings.Join(got, ",") != strings.Join(c.want, ",") || total != len(c.want) {
			t.Errorf("%s: %v (total %d, %v), want %v", c.name, got, total, err, c.want)
		}
	}
}

// TestSearchQueryRejectsInvalidValues is the binding retest of F-179 and
// the saved-view part of F-047 (WP-405): POST /api/v1/search/query answers
// a value that does not fit its field with 422 naming the field (for the
// AST where.<path>) before any SQL runs, saving a view checks filter_spec
// the same way, and no response carries database text, for any field kind
// and operator.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchQueryRejectsInvalidValues(t *testing.T) {
	v := newAuditT7Views(t, "bc")
	f := v.f
	bg := context.Background()
	const id = "00000000-0000-4000-8000-0000000000aa"

	for _, c := range []struct{ name, body, field string }{
		{"client_id eq not-a-uuid", `{"where": {"field": "client_id", "op": "eq", "value": "not-a-uuid"}}`, "where.value"},
		{"created_at gt garbage", `{"where": {"field": "created_at", "op": "gt", "value": "garbage"}}`, "where.value"},
		{"created_at gt 2020", `{"where": {"field": "created_at", "op": "gt", "value": 2020}}`, "where.value"},
		{"client_id in [x]", `{"where": {"field": "client_id", "op": "in", "value": ["x"]}}`, "where.value[0]"},
		{"attributes.ram in [{a:1}]", `{"where": {"field": "attributes.ram", "op": "in", "value": [{"a": 1}]}}`, "where.value[0]"},
		{"upstream_of x", `{"entity_kind": "ci", "upstream_of": "x"}`, "upstream_of"},
		{"downstream_of not-a-uuid", `{"downstream_of": "not-a-uuid"}`, "downstream_of"},
		{"flat client_id", `{"client_id": "client-1"}`, "client_id"},
		{"flat location_id", `{"entity_kind": "asset", "location_id": "berlin"}`, "location_id"},
		{"asset purchase_cost as text", `{"entity_kind": "asset", "where": {"field": "purchase_cost", "op": "gt", "value": "100"}}`, "where.value"},
		{"is_manual as text", `{"where": {"field": "is_manual", "op": "eq", "value": "maybe"}}`, "where.value"},
		{"nested asset date", `{"where": {"and": [{"field": "name", "op": "like", "value": "*"},
			{"not": {"field": "asset.warranty_end", "op": "lt", "value": "2024-02-30"}}]}}`, "where.and[1].not.value"},
		{"time offset", `{"where": {"field": "last_seen_at", "op": "gte", "value": "2024-01-01T00:00:00+16:00"}}`, "where.value"},
		{"NUL in a text value", `{"where": {"field": "hostname", "op": "eq", "value": "a\u0000b"}}`, "where.value"},
	} {
		code, _, _, field, raw := searchQuery(v, c.body)
		if code != http.StatusUnprocessableEntity || field != c.field || sqlLeak(raw) != "" {
			t.Errorf("%s: %d field %q, want 422 field %q without database text: %s", c.name, code, field, c.field, raw)
		}
	}

	// Saving a view checks its filter_spec the same way (VIE-01).
	view := v.create(f.User, `{"name": "wp405-view", "entity_kind": "ci", "filter_spec": {"where": {"field": "attributes.ram_gb", "op": "gte", "value": 16}}}`)
	for _, c := range []struct{ name, method, path, body, field string }{
		{"create with a where value", http.MethodPost, "/api/v1/saved-views",
			`{"name": "wp405-bad", "entity_kind": "ci", "filter_spec": {"where": {"field": "client_id", "op": "eq", "value": "not-a-uuid"}}}`, "filter_spec.where.value"},
		{"create with upstream_of", http.MethodPost, "/api/v1/saved-views",
			`{"name": "wp405-bad", "entity_kind": "ci", "filter_spec": {"upstream_of": "x"}}`, "filter_spec.upstream_of"},
		{"update with an in element", http.MethodPatch, "/api/v1/saved-views/" + view,
			`{"name": "wp405-changed", "filter_spec": {"where": {"field": "created_at", "op": "in", "value": ["garbage"]}}}`, "filter_spec.where.value[0]"},
	} {
		code, out, raw := v.call(f.OrgA, f.User, c.method, c.path, c.body)
		if field, _ := out["field"].(string); code != http.StatusUnprocessableEntity || field != c.field || sqlLeak(raw) != "" {
			t.Errorf("saved view, %s: %d field %q, want 422 field %q: %s", c.name, code, field, c.field, raw)
		}
	}
	if name, _, _ := v.row(view); name != "wp405-view" {
		t.Errorf("the refused PATCH changed the view: name %q", name)
	}
	var stored int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM saved_view WHERE organization_id = $1 AND name = 'wp405-bad'`, f.OrgA).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("refused views stored: %d (%v)", stored, err)
	}

	// Every field kind and operator with valid and invalid values, over
	// rows whose attributes and custom fields hold every JSON type: each
	// answer is 200 or 422 (where.*) and none carries database text. The
	// list holds every field whose values are cast and the text fields with
	// their own SQL expression.
	for name, attrs := range map[string]string{"sweep-number": `{"k": 64}`, "sweep-text": `{"k": "16GB"}`, "sweep-bool": `{"k": true}`,
		"sweep-date": `{"k": "2024-05-01"}`, "sweep-word": `{"k": "today"}`, "sweep-object": `{"k": {"a": 1}}`, "sweep-array": `{"k": [1]}`,
		"sweep-null": `{"k": null}`, "sweep-missing": `{}`} {
		ci := setAttributes(t, f, name, attrs)
		asset := f.Asset(t, f.OrgA, f.Client1, "WP405-"+name)
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET custom_fields = $2::jsonb, purchase_cost = 100, purchase_date = '2024-01-01',
			warranty_end = '2027-01-01', ci_id = $3 WHERE id = $1`, asset, attrs, ci); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET asset_id = $2, is_manual = true, last_seen_at = now() WHERE id = $1`, ci, asset); err != nil {
			t.Fatal(err)
		}
	}
	fields := map[string][]string{
		"ci": {"name", "management_ip", "ci_type", "client_id", "site_id", "location_id", "is_manual", "created_at", "updated_at",
			"last_seen_at", "attributes.k", "asset.name", "asset.purchase_cost", "asset.purchase_date", "asset.warranty_end",
			"asset.created_at", "asset.client_id", "asset.location_id"},
		"asset": {"name", "purchase_cost", "purchase_date", "warranty_end", "created_at", "client_id", "location_id", "attributes.k"},
	}
	ops := []string{"eq", "ne", "lt", "lte", "gt", "gte", "like", "in", "exists"}
	values := []string{`"x"`, `""`, `"\\"`, `"not-a-uuid"`, `"garbage"`, `"2024-01-01"`, `"2024-01-01T00:00:00+15:59"`,
		`"0000-01-01"`, `"2024-01-01T00:00:00+16:00"`, `"2024-01-01T00:00:00+00:60"`, `"2024-01-01T00:00:00,5Z"`,
		`"2024-01-01T00:00:00.12345678901234567890-15:59"`, `"2024-02-30"`, `"a\u0000b"`, `"` + id + `"`, `16`, `-1.5`, `1e300`, `true`,
		`null`, `{"a": 1}`, `[]`, `["x"]`, `[8, 64]`, `["` + id + `"]`, `["2024-01-01"]`, `[true]`, `[{"a": 1}]`, `[8, "x"]`}
	requests, accepted := 0, 0
	var failures []string
	for kind, list := range fields {
		for _, field := range list {
			for _, op := range ops {
				for _, value := range values {
					body := fmt.Sprintf(`{"entity_kind": %q, "where": {"field": %q, "op": %q, "value": %s}}`, kind, field, op, value)
					code, _, _, problem, raw := searchQuery(v, body)
					requests++
					switch {
					case sqlLeak(raw) != "":
						failures = append(failures, fmt.Sprintf("%s: %d discloses %q: %s", body, code, sqlLeak(raw), raw))
					case code == http.StatusOK:
						accepted++
					case code != http.StatusUnprocessableEntity || !strings.HasPrefix(problem, "where"):
						failures = append(failures, fmt.Sprintf("%s: %d field %q: %s", body, code, problem, raw))
					}
				}
			}
		}
	}
	if len(failures) > 0 {
		t.Errorf("%d of %d requests are neither 200 nor 422 or disclose database text, e.g.:\n%s",
			len(failures), requests, strings.Join(failures[:min(len(failures), 20)], "\n"))
	}
	if accepted == 0 {
		t.Errorf("none of %d requests was accepted", requests)
	}
	t.Logf("%d requests, %d answered 200, the rest 422", requests, accepted)

	// What fails in PostgreSQL after the validation is answered
	// generically: PostgreSQL text cannot hold NUL (22021).
	if code, _, _, _, raw := searchQuery(v, `{"search": "a\u0000b"}`); code != http.StatusBadRequest || sqlLeak(raw) != "" {
		t.Errorf("search with NUL: %d, want a generic 400: %s", code, raw)
	}
}

// failingEngine is a QueryEngine whose queries fail with err.
type failingEngine struct{ err error }

func (e failingEngine) Query(context.Context, string, savedview.FilterSpec, api.PaginationParams) ([]savedview.QueryResult, int, error) {
	return nil, 0, e.err
}

// TestSearchQueryHidesEngineErrors (F-179, F-047, WP-405): an engine error
// after the validation goes to the log and the client gets the generic
// answer of api.WriteRepoError, without PostgreSQL text, SQLSTATE, value
// or column name.
func TestSearchQueryHidesEngineErrors(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	for _, c := range []struct {
		err    error
		status int
		secret string
	}{
		{fmt.Errorf("count query results: %w", &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type numeric: "16GB"`}),
			http.StatusBadRequest, "16GB"},
		{fmt.Errorf("run filter query: %w", &pgconn.PgError{Code: "42703", Message: `column ci.secret_column does not exist`}),
			http.StatusInternalServerError, "secret_column"},
		{errors.New("scan ci.hidden_attribute: can't scan into dest"), http.StatusInternalServerError, "hidden_attribute"},
	} {
		logged.Reset()
		mux := chi.NewRouter()
		savedview.NewHandler(savedview.NewMemoryRepository()).WithQueryEngine(failingEngine{c.err}).
			WithPermissions(grants{"ci:read": true}).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/search/query",
			strings.NewReader(`{"where": {"field": "attributes.ram_gb", "op": "gte", "value": 16}}`))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != c.status || sqlLeak(body) != "" || strings.Contains(body, c.secret) {
			t.Errorf("%v: %d %s, want a generic %d", c.err, w.Code, body, c.status)
		}
		if !strings.Contains(logged.String(), "structured search failed") || !strings.Contains(logged.String(), c.secret) {
			t.Errorf("%v: not logged: %s", c.err, logged.String())
		}
	}
}
