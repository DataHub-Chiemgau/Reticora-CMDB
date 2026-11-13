package compliance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The aggregated compliance score on PostgreSQL (SEC-03, CMP-02, WP-1127):
// the six groups with known values, the client scope of the caller, the
// form without group_by, the daily snapshot with its retention, the
// history under the scope and its period limit, the weights and the index
// of the aggregation under row level security.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).

// scoreEnv is organization A with three CIs (c1a at site s1 and c1b of
// client 1, c2 of client 2), three rules and their results:
//
//	c1a: patch-critical pass, hygiene-low fail, patch-high not_applicable
//	c1b: patch-critical fail, hygiene-low pass
//	c2:  patch-critical pass, patch-high fail
//
// With the default weights (critical 10, high 5, low 1) the organization
// has pass weight 21 and fail weight 16.
type scoreEnv struct {
	t             *testing.T
	f             *scopetest.Fixture
	repo          *PGRepository
	mux           http.Handler
	c1a, c1b, c2  string
	site, ciType  string
	critical, low string
	high          string
}

func newScoreEnv(t *testing.T) *scoreEnv {
	t.Helper()
	f := scopetest.Seed(t, "3c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	e := &scoreEnv{t: t, f: f, repo: NewPGRepository(f.App)}
	e.c1a = f.CI(t, f.OrgA, f.Client1, "score-c1a")
	e.c1b = f.CI(t, f.OrgA, f.Client1, "score-c1b")
	e.c2 = f.CI(t, f.OrgA, f.Client2, "score-c2")
	e.site = scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'score-site') RETURNING id::text`, f.OrgA, f.Client1)
	exec(`UPDATE ci SET location_id = $1 WHERE id = $2`, e.site, e.c1a)
	e.ciType = scalar(`SELECT ci_type_id::text FROM ci WHERE id = $1`, e.c1a)
	rule := func(name, severity, category string) string {
		return scalar(`INSERT INTO compliance_rule (organization_id, name, severity, category, expression, active)
			VALUES ($1, $2, $3, $4, '{}', true) RETURNING id::text`, f.OrgA, name, severity, category)
	}
	e.critical, e.low, e.high = rule("patch-critical", "critical", "patch"), rule("hygiene-low", "low", "hygiene"), rule("patch-high", "high", "patch")
	result := func(ruleID, ciID, status string, typed bool) {
		var ciType any
		if typed {
			ciType = e.ciType
		}
		exec(`INSERT INTO compliance_result (organization_id, rule_id, ci_id, ci_type_id, status) VALUES ($1, $2, $3, $4, $5)`,
			f.OrgA, ruleID, ciID, ciType, status)
	}
	result(e.critical, e.c1a, "pass", true)
	result(e.low, e.c1a, "fail", true)
	result(e.high, e.c1a, "not_applicable", true)
	result(e.critical, e.c1b, "fail", true)
	result(e.low, e.c1b, "pass", true)
	result(e.critical, e.c2, "pass", false)
	result(e.high, e.c2, "fail", false)
	r := chi.NewRouter()
	NewHandler(e.repo, nil).RegisterRoutes(r)
	e.mux = r
	return e
}

// ctx is the context of a principal of organization A, org-wide or
// restricted to client.
func (e *scoreEnv) ctx(client string) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	if client != "" {
		scope.Clients = database.ScopeIDs(client)
	}
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
}

func (e *scoreEnv) get(client, path string, out any) int {
	e.t.Helper()
	return e.do(client, http.MethodGet, path, "", out)
}

func (e *scoreEnv) do(client, method, path, body string, out any) int {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(e.ctx(client))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s %s: %v %s", method, path, err, rec.Body)
		}
	}
	return rec.Code
}

type groupWant struct {
	key            string
	passed, failed int
	na             int
	score          float64
}

func (e *scoreEnv) groups(client, groupBy string) []ScoreGroup {
	e.t.Helper()
	var out ScoreResponse
	if code := e.get(client, "/api/v1/compliance/score?group_by="+groupBy, &out); code != http.StatusOK {
		e.t.Fatalf("group_by=%s: %d", groupBy, code)
	}
	return out.Groups
}

func checkGroups(t *testing.T, name string, got []ScoreGroup, want []groupWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d groups %+v, want %d", name, len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Key != w.key || g.Passed != w.passed || g.Failed != w.failed || g.NotApplicable != w.na || !near(g.Score, w.score) {
			t.Errorf("%s[%d] = %+v, want %+v", name, i, g, w)
		}
	}
}

func TestScoreGroupBy(t *testing.T) {
	e := newScoreEnv(t)
	pct := func(p, f float64) float64 { return p / (p + f) * 100 }
	// Sorted by score ascending, then key.
	checkGroups(t, "ci", e.groups("", GroupCI), []groupWant{
		{e.c1b, 1, 1, 0, pct(1, 10)}, {e.c2, 1, 1, 0, pct(10, 5)}, {e.c1a, 1, 1, 1, pct(10, 1)},
	})
	checkGroups(t, "ci_type", e.groups("", GroupCIType), []groupWant{
		{e.ciType, 2, 2, 1, 50}, {"", 1, 1, 0, pct(10, 5)},
	})
	client := e.groups("", GroupClient)
	checkGroups(t, "client", client, []groupWant{{e.f.Client1, 2, 2, 1, 50}, {e.f.Client2, 1, 1, 0, pct(10, 5)}})
	if client[0].Label != "Client 1" {
		t.Errorf("client label %q", client[0].Label)
	}
	site := e.groups("", GroupSite)
	checkGroups(t, "site", site, []groupWant{{"", 2, 2, 0, pct(11, 15)}, {e.site, 1, 1, 1, pct(10, 1)}})
	if site[1].Label != "score-site" {
		t.Errorf("site label %q", site[1].Label)
	}
	checkGroups(t, "category", e.groups("", GroupCategory), []groupWant{
		{"hygiene", 1, 1, 0, 50}, {"patch", 2, 2, 1, pct(20, 15)},
	})
	checkGroups(t, "severity", e.groups("", GroupSeverity), []groupWant{
		{"high", 0, 1, 1, 0}, {"low", 1, 1, 0, 50}, {"critical", 2, 1, 0, pct(20, 10)},
	})
	// group_by=ci in pages of two along next_cursor.
	var first ScoreResponse
	e.get("", "/api/v1/compliance/score?group_by=ci&limit=2", &first)
	if len(first.Groups) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	var second ScoreResponse
	e.get("", "/api/v1/compliance/score?group_by=ci&limit=2&cursor="+first.NextCursor, &second)
	if len(second.Groups) != 1 || second.Groups[0].Key != e.c1a || second.HasMore {
		t.Errorf("second page %+v", second)
	}
	if code := e.get("", "/api/v1/compliance/score?group_by=team", nil); code != http.StatusBadRequest {
		t.Errorf("group_by=team: %d, want 400", code)
	}
}

func TestScoreRespectsClientScope(t *testing.T) {
	e := newScoreEnv(t)
	checkGroups(t, "client 1 by client", e.groups(e.f.Client1, GroupClient), []groupWant{{e.f.Client1, 2, 2, 1, 50}})
	for _, g := range e.groups(e.f.Client1, GroupCI) {
		if g.Key == e.c2 {
			t.Errorf("client 1 sees the CI of client 2: %+v", g)
		}
	}
	var out ScoreResponse
	e.get(e.f.Client1, "/api/v1/compliance/score", &out)
	if out.Overall.Passed != 2 || out.Overall.Failed != 2 || !near(out.Overall.Score, 50) {
		t.Errorf("overall of client 1 %+v, want 2/2 and 50", out.Overall)
	}
}

func TestScoreWithoutGroupByKeepsShape(t *testing.T) {
	e := newScoreEnv(t)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/compliance/score", nil).WithContext(e.ctx("")))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, key := range []string{"overall", "by_ci_type", "results"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("answer without %s: %s", key, rec.Body)
		}
	}
	if _, ok := raw["groups"]; ok {
		t.Errorf("groups without group_by: %s", rec.Body)
	}
	if string(raw["results"]) != "[]" {
		t.Errorf("results %s, want []", raw["results"])
	}
	var out ScoreResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Overall.Passed != 3 || out.Overall.Failed != 3 || out.Overall.NotApplicable != 1 || !near(out.Overall.Score, 21.0/37*100) {
		t.Errorf("overall %+v", out.Overall)
	}
	if len(out.ByCIType) != 2 {
		t.Errorf("by_ci_type %+v", out.ByCIType)
	}
	// ci_type_id filters as before.
	e.get("", "/api/v1/compliance/score?ci_type_id="+e.ciType, &out)
	if out.Overall.Passed != 2 || out.Overall.Failed != 2 {
		t.Errorf("overall of the CI type %+v", out.Overall)
	}
}

// snapshotRows counts the snapshot rows of organization A on day.
func (e *scoreEnv) snapshotRows(day string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM compliance_score_snapshot
		WHERE organization_id = $1 AND day = $2::date`, e.f.OrgA, day).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestScoreSnapshotIdempotentAndRetention(t *testing.T) {
	e := newScoreEnv(t)
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	old, kept := day.AddDate(0, 0, -401).Format(time.DateOnly), day.AddDate(0, 0, -399).Format(time.DateOnly)
	for _, d := range []string{old, kept} {
		if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO compliance_score_snapshot
			(organization_id, day, dimension, group_key, passed, failed, passed_weight, failed_weight)
			VALUES ($1, $2::date, 'overall', '', 1, 0, 10, 0)`, e.f.OrgA, d); err != nil {
			t.Fatal(err)
		}
	}
	snap := NewSnapshot(e.repo, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil }, nil)
	snap.clock = func() time.Time { return day.Add(4*time.Hour + 30*time.Minute) }
	first, err := snap.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows := e.snapshotRows(day.Format(time.DateOnly))
	// Per (client, site): overall, the CI types, categories and severities.
	if first == 0 || rows != first {
		t.Fatalf("first run wrote %d, %d rows of the day", first, rows)
	}
	if _, err = snap.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := e.snapshotRows(day.Format(time.DateOnly)); again != rows {
		t.Errorf("second run: %d rows, want %d (no duplicates)", again, rows)
	}
	if n := e.snapshotRows(old); n != 0 {
		t.Errorf("row of 401 days ago kept: %d", n)
	}
	if n := e.snapshotRows(kept); n != 1 {
		t.Errorf("row of 399 days ago: %d, want 1", n)
	}
	// The overall sums of the day are those of the score.
	var pw, fw int64
	if err = e.f.Admin.QueryRow(context.Background(), `SELECT sum(passed_weight), sum(failed_weight) FROM compliance_score_snapshot
		WHERE organization_id = $1 AND day = $2::date AND dimension = 'overall'`, e.f.OrgA, day.Format(time.DateOnly)).Scan(&pw, &fw); err != nil {
		t.Fatal(err)
	}
	if pw != 21 || fw != 16 {
		t.Errorf("overall sums %d/%d, want 21/16", pw, fw)
	}
}

func TestScoreHistoryScoped(t *testing.T) {
	e := newScoreEnv(t)
	snap := NewSnapshot(e.repo, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil }, nil)
	today := time.Now().UTC()
	if _, err := snap.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	day := today.Format(time.DateOnly)
	var org HistoryResponse
	if code := e.get("", "/api/v1/compliance/score/history", &org); code != http.StatusOK {
		t.Fatalf("history: %d", code)
	}
	if len(org.Data) != 1 || org.Data[0].Day != day || org.Data[0].PassedWeight != 21 || org.Data[0].FailedWeight != 16 ||
		!near(org.Data[0].Score, 21.0/37*100) {
		t.Fatalf("org-wide history %+v", org)
	}
	var mine HistoryResponse
	e.get(e.f.Client1, "/api/v1/compliance/score/history", &mine)
	if len(mine.Data) != 1 || mine.Data[0].PassedWeight != 11 || mine.Data[0].FailedWeight != 11 || !near(mine.Data[0].Score, 50) {
		t.Errorf("history of client 1 %+v, want 11/11", mine)
	}
	var byClient HistoryResponse
	e.get(e.f.Client1, "/api/v1/compliance/score/history?group_by=client", &byClient)
	if len(byClient.Data) != 1 || byClient.Data[0].Key != e.f.Client1 || byClient.Data[0].Label != "Client 1" {
		t.Errorf("history of client 1 by client %+v", byClient)
	}
	var bySeverity HistoryResponse
	e.get("", "/api/v1/compliance/score/history?group_by=severity", &bySeverity)
	if len(bySeverity.Data) != 3 {
		t.Errorf("history by severity %+v", bySeverity)
	}
}

func TestScoreHistoryRangeLimit(t *testing.T) {
	e := newScoreEnv(t)
	to := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	path := func(days int) string {
		return "/api/v1/compliance/score/history?from=" + to.AddDate(0, 0, -days).Format(time.DateOnly) + "&to=" + to.Format(time.DateOnly)
	}
	if code := e.get("", path(401), nil); code != http.StatusBadRequest {
		t.Errorf("401 days: %d, want 400", code)
	}
	if code := e.get("", path(400), nil); code != http.StatusOK {
		t.Errorf("400 days: %d, want 200", code)
	}
}

func TestComplianceSettingsValidation(t *testing.T) {
	e := newScoreEnv(t)
	var s Settings
	if code := e.get("", "/api/v1/compliance/settings", &s); code != http.StatusOK || s.Weights["critical"] != 10 || s.UpdatedAt != nil {
		t.Fatalf("defaults: %d %+v", code, s)
	}
	for _, body := range []string{`{"weights":{"critical":101}}`, `{"weights":{"high":1.5}}`, `{"weights":{"urgent":1}}`, `{"weights":[]}`} {
		if code := e.do("", http.MethodPut, "/api/v1/compliance/settings", body, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", body, code)
		}
	}
	// A principal bound to client 1 changes no weights of the whole
	// organization: 403, the row stays (E-08, E-09).
	if code := e.do(e.f.Client1, http.MethodPut, "/api/v1/compliance/settings", `{"weights":{"critical":0,"low":100}}`, nil); code != http.StatusForbidden {
		t.Errorf("client-bound put: %d, want 403", code)
	}
	if code := e.get("", "/api/v1/compliance/settings", &s); code != http.StatusOK || s.Weights["critical"] != 10 || s.UpdatedAt != nil {
		t.Errorf("after the refused put: %d %+v, want the defaults", code, s)
	}
	if code := e.do("", http.MethodPut, "/api/v1/compliance/settings", `{"weights":{"critical":1,"low":10}}`, &s); code != http.StatusOK ||
		s.Weights["critical"] != 1 || s.Weights["low"] != 10 || s.Weights["high"] != 5 || s.UpdatedAt == nil {
		t.Fatalf("put: %d %+v", code, s)
	}
	// The weights change the score: pass 1+10+1, fail 10+1+5 → 12/28.
	var out ScoreResponse
	e.get("", "/api/v1/compliance/score", &out)
	if !near(out.Overall.Score, 12.0/28*100) {
		t.Errorf("score with the new weights %v, want %v", out.Overall.Score, 12.0/28*100)
	}
}

// TestScoreUsesIndexUnderRLS: the aggregation reads compliance_result
// through an index, as the app role with active row level security
// (org-wide and bound to client 1) and without switching the sequential
// scan off. The fixture is filled to 20,000 results and vacuumed, so the
// planner decides on statistics. At this size it joins the visible CIs to
// their results over idx_compliance_result_ci; the covering index
// idx_compliance_result_rule_status serves the index-only scan of a large
// organization. Both lead with organization_id; the load proof at 1 million
// results follows in the scenario compliance_eval (WP-1138).
func TestScoreUsesIndexUnderRLS(t *testing.T) {
	e := newScoreEnv(t)
	bg := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
			SELECT $1, CASE WHEN g % 2 = 0 THEN $2::uuid ELSE $3::uuid END, $4, 'score-bulk-' || g, 'active'
			FROM generate_series(1, 2000) g`, []any{e.f.OrgA, e.f.Client1, e.f.Client2, e.ciType}},
		{`INSERT INTO compliance_rule (organization_id, name, severity, category, expression, active)
			SELECT $1, 'score-bulk-rule-' || g, 'medium', 'bulk', '{}', true FROM generate_series(1, 10) g`, []any{e.f.OrgA}},
		{`INSERT INTO compliance_result (organization_id, rule_id, ci_id, ci_type_id, status)
			SELECT $1, ru.id, ci.id, ci.ci_type_id, CASE WHEN (hashtext(ci.name || ru.name) % 10) < 7 THEN 'pass' ELSE 'fail' END
			FROM ci, compliance_rule ru
			WHERE ci.organization_id = $1 AND ci.name LIKE 'score-bulk-%' AND ru.organization_id = $1 AND ru.category = 'bulk'`, []any{e.f.OrgA}},
		{`VACUUM (ANALYZE) compliance_result`, nil},
		{`ANALYZE ci`, nil},
		{`ANALYZE compliance_rule`, nil},
	} {
		if _, err := e.f.Admin.Exec(bg, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	for _, c := range []struct{ group, client string }{{"", ""}, {GroupClient, ""}, {GroupClient, e.f.Client1}} {
		group := c.group
		sql, args, err := scoreStatement(e.f.OrgA, &ScoreQuery{GroupBy: group})
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		err = database.WithRequestTenant(e.ctx(c.client), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			var role string
			if qerr := tx.QueryRow(ctx, `SELECT current_user`).Scan(&role); qerr != nil {
				return qerr
			}
			if role == "postgres" {
				t.Errorf("EXPLAIN runs as %s, want the app role", role)
			}
			rows, qerr := tx.Query(ctx, "EXPLAIN "+sql, args...)
			if qerr != nil {
				return qerr
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if serr := rows.Scan(&line); serr != nil {
					return serr
				}
				plan = append(plan, line)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(plan, "\n")
		if strings.Contains(text, "Seq Scan on compliance_result") {
			t.Errorf("group_by %q client %q: the aggregation scans compliance_result sequentially:\n%s", group, c.client, text)
		}
		if !strings.Contains(text, "using idx_compliance_result_rule_status on compliance_result") &&
			!strings.Contains(text, "using idx_compliance_result_ci on compliance_result") {
			t.Errorf("group_by %q client %q: the aggregation reads compliance_result through neither the aggregation index nor the CI index:\n%s",
				group, c.client, text)
		}
	}
}
