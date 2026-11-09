package search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// maxFieldRowLoops bounds how often a plan of the restricted search may
// read search_document_field: once for the custom field rows of the
// prefilter and about once per candidate, not once per document.
const maxFieldRowLoops = 10

// TestRestrictedSearchQueryBudget covers the cost of the search with
// restricted field permissions (RBA-07, WP-266; NFR-02) at 2,000 CIs with
// hyphenated names like the load test (srv-05001 is the phrase
// 'srv' <-> '-05001'): a sensitive field restricts the kind ci for every
// principal, as in the seed of the load test (WP-325). The statements the
// repository sends are explained as reticora_app in the tenant transaction:
// no plan node reads search_document_field more than maxFieldRowLoops
// times. Before, a phrase let every CI document through the prefilter
// (the readable text of each was built per document) and the custom field
// rows were read by document for every ci and asset document (WP-308).
// The phrase still finds the CI, the sensitive serial number none; the
// plans are logged.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -run 'TestRestrictedSearchQueryBudget' ./internal/search/
func TestRestrictedSearchQueryBudget(t *testing.T) {
	f := scopetest.Seed(t, "5b")
	bg := context.Background()
	const objects = 2000
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, hostname, manufacturer, model, serial_number)
		SELECT $1, (ARRAY[$2::uuid, NULL])[i % 2 + 1], `+server+`, 'rsq-' || lpad(i::text, 5, '0'), 'active',
			'rsq-host-' || i, 'Vendor ' || (i % 50), 'model-' || (i % 7), 'rsq-serial-' || i
		FROM generate_series(1, $3::int) AS i`, f.OrgA, f.Client1, objects); err != nil {
		t.Fatalf("seed CIs: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO field_sensitivity (organization_id, object_kind, field_key) VALUES ($1, 'ci', 'core.serial_number')`,
		f.OrgA); err != nil {
		t.Fatal(err)
	}
	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci, search_document, search_document_field`); err != nil {
		t.Fatal(err)
	}

	role := f.ID()
	raw, _ := json.Marshal(frPerms)
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'rsq-reader', $3::jsonb)`,
		role, f.OrgA, string(raw)); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(bg, f.OrgA, []identity.Grant{{Permissions: frPerms, RoleID: role}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Restricted(permission.KindCI) {
		t.Fatal("the sensitive field does not restrict the kind ci")
	}

	cfg := f.App.Config()
	log := &sentLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	traced, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	tracedRepo := search.NewPGRepository(traced)

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	explain := func(stmt sentStatement) []searchPlanNode {
		t.Helper()
		var plan []byte
		if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan)
		}); err != nil {
			t.Fatalf("explain %s: %v", stmt.sql, err)
		}
		return searchPlanNodes(t, plan)
	}

	// The name and the readable hostname find their CI; the serial number
	// (sensitive, no grant) finds none.
	for _, c := range []struct {
		text string
		hits int
	}{{"rsq-01234", 1}, {"rsq-host-77", 1}, {"rsq-serial-77", 0}} {
		text := c.text
		res, qerr := tracedRepo.Query(f.OrgCtx(f.OrgA), &search.Query{OrganizationID: f.OrgA, Text: text, Limit: 5, Highlight: true, Policy: policy})
		if qerr != nil {
			t.Fatalf("query %q: %v", text, qerr)
		}
		if len(res.Data) != c.hits || res.Total != c.hits {
			t.Errorf("%q: %d hits (total %d), want %d", text, len(res.Data), res.Total, c.hits)
		}
		for _, part := range []string{"COALESCE(array_agg(DISTINCT f.field)", "AS read_fields FROM"} {
			stmt, ok := log.last(part)
			if !ok {
				t.Fatalf("%q: no statement with %q", text, part)
			}
			nodes := explain(stmt)
			for _, n := range nodes {
				loops, _ := n["Actual Loops"].(float64)
				if n["Relation Name"] == "search_document_field" && loops > maxFieldRowLoops {
					t.Errorf("%q: %s reads search_document_field %d times, at most %d: %s",
						text, part, int(loops), maxFieldRowLoops, describeSearchPlan(nodes))
				}
			}
			t.Logf("%q (%d hits) %s: %s", text, res.Total, part, describeSearchPlan(nodes))
		}
	}
}

// fullFieldRowReads counts the plan nodes that read search_document_field
// without the document as index condition (or recheck condition of a
// bitmap scan): reads of the custom field rows of the whole organization,
// whose policy looks up the document of every row before the match (@@ is
// not leakproof).
func fullFieldRowReads(nodes []searchPlanNode) int {
	n := 0
	for _, node := range nodes {
		cond, _ := node["Index Cond"].(string)
		recheck, _ := node["Recheck Cond"].(string)
		if node["Relation Name"] == "search_document_field" && !strings.Contains(cond+recheck, "document_id") {
			n++
		}
	}
	return n
}

// fieldPolicyProblem returns why the policy of search_document_field costs
// more than its own columns, or "": since 000280 the policy checks the
// client and site columns of a field row, so search_document is read about
// once per statement (the candidates), not once per field row (the lookup
// of 000228, about 45,000 buffers per global search of the load run
// 42582336513), and the custom field rows and tag rows of the organization
// are read through idx_search_document_field_match, not by a scan of every
// field row.
func fieldPolicyProblem(nodes []searchPlanNode) string {
	documentLoops := 0
	for _, node := range nodes {
		loops, _ := node["Actual Loops"].(float64)
		if node["Relation Name"] == "search_document" {
			documentLoops += int(loops)
		}
		cond, _ := node["Index Cond"].(string)
		recheck, _ := node["Recheck Cond"].(string)
		if node["Relation Name"] == "search_document_field" && !strings.Contains(cond+recheck, "document_id") &&
			node["Index Name"] != "idx_search_document_field_match" {
			return "the custom field rows are not read through idx_search_document_field_match"
		}
	}
	if documentLoops > maxFieldRowLoops {
		return fmt.Sprintf("search_document is read %d times, at most %d (the policy of the field rows looks up their document)",
			documentLoops, maxFieldRowLoops)
	}
	return ""
}

// TestRestrictedSearchFieldRowBudget covers the cost of the search with
// restricted field permissions over the custom field rows (WP-308, tags of
// WP-788; NFR-02) like search_fields of the load test (tests/load/phase2.js:
// the load tag on 500 CIs): 1,000 CIs with a sensitive field, 300 of them
// with a tag. The scope statement and the hit statement of the search are
// explained as reticora_app in the tenant transaction. The custom field rows
// of the organization are read by the scope statement only (the match,
// and at most once more for the custom field names of its candidates,
// both through idx_search_document_field_match since 000280), which hands
// the ids of the matching documents to the hit statement; the
// hit statement reads the field rows of a candidate at most twice (its
// readable summary and its readable custom fields). Before, both
// statements read all custom field rows, and the readable fields and the
// readable summary of a candidate were evaluated for each reference
// (score, match, snippet): four reads of its field rows. The tag finds its
// 300 CIs, the name one, the sensitive serial number none.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -run 'TestRestrictedSearchFieldRowBudget' ./internal/search/
func TestRestrictedSearchFieldRowBudget(t *testing.T) {
	f := scopetest.Seed(t, "5b")
	bg := context.Background()
	const objects, tagged = 1000, 300
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, hostname, serial_number)
		SELECT $1, (ARRAY[$2::uuid, NULL])[i % 2 + 1], `+server+`, 'rfb-' || lpad(i::text, 5, '0'), 'active',
			'rfb-host-' || i, 'rfb-serial-' || i
		FROM generate_series(1, $3::int) AS i`, f.OrgA, f.Client1, objects); err != nil {
		t.Fatalf("seed CIs: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `WITH tg AS (
			INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'rfbtag', '{"en": "rfbtag"}'::jsonb) RETURNING id
		)
		INSERT INTO ci_tag (organization_id, ci_id, tag_id)
		SELECT $1, c.id, tg.id FROM tg, (SELECT id FROM ci WHERE organization_id = $1 AND name LIKE 'rfb-%' ORDER BY name LIMIT $2) AS c`,
		f.OrgA, tagged); err != nil {
		t.Fatalf("seed tags: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO field_sensitivity (organization_id, object_kind, field_key) VALUES ($1, 'ci', 'core.serial_number')`,
		f.OrgA); err != nil {
		t.Fatal(err)
	}
	repo := search.NewPGRepository(f.App)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci, search_document, search_document_field`); err != nil {
		t.Fatal(err)
	}

	role := f.ID()
	raw, _ := json.Marshal(frPerms)
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'rfb-reader', $3::jsonb)`,
		role, f.OrgA, string(raw)); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(bg, f.OrgA, []identity.Grant{{Permissions: frPerms, RoleID: role}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Restricted(permission.KindCI) {
		t.Fatal("the sensitive field does not restrict the kind ci")
	}

	cfg := f.App.Config()
	log := &sentLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	traced, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	tracedRepo := search.NewPGRepository(traced)

	orgWide := database.OrgWideScope(f.OrgA, f.User)
	explain := func(stmt sentStatement) []searchPlanNode {
		t.Helper()
		var plan []byte
		if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan)
		}); err != nil {
			t.Fatalf("explain %s: %v", stmt.sql, err)
		}
		return searchPlanNodes(t, plan)
	}

	for _, c := range []struct {
		text string
		hits int
	}{{"rfbtag", tagged}, {"rfb-00042", 1}, {"rfb-serial-42", 0}} {
		text := c.text
		res, qerr := tracedRepo.Query(f.OrgCtx(f.OrgA), &search.Query{OrganizationID: f.OrgA, Text: text, Limit: 5, Highlight: true, Policy: policy})
		if qerr != nil {
			t.Fatalf("query %q: %v", text, qerr)
		}
		if res.Total != c.hits || len(res.Data) != min(c.hits, 5) {
			t.Errorf("%q: %d hits (total %d), want %d", text, len(res.Data), res.Total, c.hits)
		}
		full := map[string]int{}
		for _, part := range []string{"COALESCE(array_agg(DISTINCT f.field)", "AS read_fields FROM"} {
			stmt, ok := log.last(part)
			if !ok {
				t.Fatalf("%q: no statement with %q", text, part)
			}
			nodes := explain(stmt)
			full[part] = fullFieldRowReads(nodes)
			if problem := fieldPolicyProblem(nodes); problem != "" {
				t.Errorf("%q: %s: %s: %s", text, part, problem, describeSearchPlan(nodes))
			}
			if part == "AS read_fields FROM" {
				loops := 0
				for _, n := range nodes {
					if n["Relation Name"] == "search_document_field" {
						l, _ := n["Actual Loops"].(float64)
						loops += int(l)
					}
				}
				if limit := 2*c.hits + maxFieldRowLoops; loops > limit {
					t.Errorf("%q: the hit statement reads search_document_field %d times, at most %d (two per hit): %s",
						text, loops, limit, describeSearchPlan(nodes))
				}
			}
			t.Logf("%q (%d hits) %s: %s", text, res.Total, part, describeSearchPlan(nodes))
		}
		// The scope statement reads the matching rows (attr) and may read
		// the custom field names of its candidates with one read of the
		// organization's rows through idx_search_document_field_match
		// (hash join) instead of one lookup per candidate; the hit
		// statement reads none of them.
		if scope := full["COALESCE(array_agg(DISTINCT f.field)"]; scope < 1 || scope > 2 {
			t.Errorf("%q: the scope statement reads the custom field rows of the organization %d times, want once or twice", text, scope)
		}
		if hit := full["AS read_fields FROM"]; hit != 0 {
			t.Errorf("%q: the hit statement reads the custom field rows of the organization %d times, want none", text, hit)
		}
	}
}
