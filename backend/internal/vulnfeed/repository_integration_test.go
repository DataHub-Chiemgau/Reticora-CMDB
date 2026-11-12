package vulnfeed_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The global vulnerability catalog on PostgreSQL (SEC-02, WP-1117, E-234):
// advisories are replaced with their ranges atomically in the system
// context, the product read uses the index (vendor, product), and a tenant
// transaction reads every row but writes none.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestVulnReplaceAdvisoryIsAtomic|TestVulnReplaceAdvisoryDropsOldRanges|TestVulnReplaceAdvisoryOfOtherSourceRefused|TestVulnByProductsUsesIndex|TestVulnTenantContextCannotWrite|TestVulnSearchProducts' ./internal/vulnfeed/

// catalogEnv is the catalog with a source of its own; the rows of the
// source are removed when the test ends (the catalog is global).
type catalogEnv struct {
	f      *scopetest.Fixture
	repo   *vulnfeed.PGRepository
	source string
	prefix string
}

func newCatalogEnv(t *testing.T) *catalogEnv {
	t.Helper()
	f := scopetest.Seed(t, "5d")
	prefix := fmt.Sprintf("wp1117-%d", time.Now().UnixNano())
	e := &catalogEnv{f: f, repo: vulnfeed.NewPGRepository(f.App), source: "csaf:https://" + prefix + ".example", prefix: prefix}
	clean := func() {
		_, _ = f.Admin.Exec(context.Background(), `DELETE FROM vuln_source WHERE id LIKE 'csaf:https://wp1117-%'`)
	}
	clean()
	t.Cleanup(clean)
	if err := e.repo.UpdateSource(context.Background(), &vulnfeed.Source{ID: e.source, Kind: vulnfeed.KindCSAF, URL: "https://" + prefix + ".example"}); err != nil {
		t.Fatalf("source: %v", err)
	}
	return e
}

func (e *catalogEnv) advisory(id string) *vulnfeed.Advisory {
	score := 9.8
	return &vulnfeed.Advisory{ID: e.prefix + "-" + id, SourceID: e.source, CVEIDs: []string{"CVE-2026-1000"}, Title: "advisory " + id,
		Severity: "critical", CVSSScore: &score, CVSSVector: "CVSS:3.1/AV:N", KEV: true}
}

// ranges counts the ranges of an advisory and returns the products.
func (e *catalogEnv) ranges(t *testing.T, advisoryID string) []string {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT vendor || '/' || product FROM vuln_affected WHERE advisory_id = $1 ORDER BY id`, advisoryID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestVulnReplaceAdvisoryIsAtomic: a failing second range leaves nothing
// of the new advisory, and a failing replacement keeps the stored one.
func TestVulnReplaceAdvisoryIsAtomic(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	adv := e.advisory("atomic")
	bad := []vulnfeed.Affected{{Vendor: "Cisco Systems", Product: "IOS XE", VersionStart: "17.0"}, {Vendor: "  ", Product: "ios"}}
	if err := e.repo.ReplaceAdvisory(ctx, adv, bad); err == nil {
		t.Fatal("a range without vendor was stored")
	}
	var n int
	if err := e.f.Admin.QueryRow(ctx, `SELECT count(*) FROM vuln_advisory WHERE id = $1`, adv.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("advisory after the failed replace: %d %v, want none", n, err)
	}
	if got := e.ranges(t, adv.ID); len(got) != 0 {
		t.Fatalf("ranges after the failed replace: %v, want none", got)
	}
	good := []vulnfeed.Affected{{Vendor: "Cisco Systems", Product: "IOS XE", VersionStart: "17.0", VersionEnd: "17.9.4"}}
	if err := e.repo.ReplaceAdvisory(ctx, adv, good); err != nil {
		t.Fatal(err)
	}
	changed := *adv
	changed.Title = "changed"
	if err := e.repo.ReplaceAdvisory(ctx, &changed, bad); err == nil {
		t.Fatal("replacement with a range without vendor was stored")
	}
	var title string
	if err := e.f.Admin.QueryRow(ctx, `SELECT title FROM vuln_advisory WHERE id = $1`, adv.ID).Scan(&title); err != nil || title != adv.Title {
		t.Errorf("title after the failed replacement: %q %v, want %q", title, err, adv.Title)
	}
	if got := e.ranges(t, adv.ID); len(got) != 1 || got[0] != "cisco_systems/ios_xe" {
		t.Errorf("ranges after the failed replacement: %v, want the normalized former one", got)
	}
}

// TestVulnReplaceAdvisoryDropsOldRanges: a replacement drops the ranges
// the new version no longer names.
func TestVulnReplaceAdvisoryDropsOldRanges(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	adv := e.advisory("replace")
	if err := e.repo.ReplaceAdvisory(ctx, adv, []vulnfeed.Affected{
		{Vendor: "Fortinet", Product: "FortiOS", VersionExact: "7.2.0"},
		{Vendor: "Fortinet", Product: "FortiProxy", VersionStart: "7.0", VersionEnd: "7.0.9"},
	}); err != nil {
		t.Fatal(err)
	}
	adv.CVEIDs = []string{"CVE-2026-1000", "CVE-2026-1001"}
	if err := e.repo.ReplaceAdvisory(ctx, adv, []vulnfeed.Affected{{Vendor: "Fortinet", Product: "FortiOS", VersionStart: "7.2.0", VersionEnd: "7.2.5", FixedVersion: "7.2.6"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.ranges(t, adv.ID); len(got) != 1 || got[0] != "fortinet/fortios" {
		t.Errorf("ranges after the replacement: %v, want only fortinet/fortios", got)
	}
	matches, err := e.repo.ByProducts(ctx, "Fortinet", []string{"FortiOS", "FortiProxy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Affected.FixedVersion != "7.2.6" || len(matches[0].Advisory.CVEIDs) != 2 ||
		matches[0].Advisory.CVSSScore == nil || *matches[0].Advisory.CVSSScore != 9.8 || !matches[0].Advisory.KEV {
		t.Errorf("matches: %+v", matches)
	}
}

// TestVulnReplaceAdvisoryOfOtherSourceRefused: a second source that writes
// an advisory under an id another source stored is refused; the advisory
// keeps its source, title and ranges.
func TestVulnReplaceAdvisoryOfOtherSourceRefused(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	other := e.source + "/other"
	if err := e.repo.UpdateSource(ctx, &vulnfeed.Source{ID: other, Kind: vulnfeed.KindCSAF}); err != nil {
		t.Fatal(err)
	}
	adv := e.advisory("owned")
	if err := e.repo.ReplaceAdvisory(ctx, adv, []vulnfeed.Affected{{Vendor: "acme", Product: "router", VersionExact: "1"}}); err != nil {
		t.Fatal(err)
	}
	takeover := *adv
	takeover.SourceID, takeover.Title = other, "taken over"
	err := e.repo.ReplaceAdvisory(ctx, &takeover, []vulnfeed.Affected{{Vendor: "acme", Product: "switch", VersionExact: "2"}})
	if !errors.Is(err, vulnfeed.ErrAdvisoryOwnedByOtherSource) {
		t.Fatalf("write under the id of another source: %v, want ErrAdvisoryOwnedByOtherSource", err)
	}
	var source, title string
	if err = e.f.Admin.QueryRow(ctx, `SELECT source_id, title FROM vuln_advisory WHERE id = $1`, adv.ID).Scan(&source, &title); err != nil ||
		source != e.source || title != adv.Title {
		t.Errorf("advisory after the refused write: %q %q %v, want %q %q", source, title, err, e.source, adv.Title)
	}
	if got := e.ranges(t, adv.ID); len(got) != 1 || got[0] != "acme/router" {
		t.Errorf("ranges after the refused write: %v, want acme/router", got)
	}
}

// TestVulnByProductsUsesIndex: the plan of the product read uses the index
// (vendor, product) of vuln_affected.
func TestVulnByProductsUsesIndex(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	for i := range 20 {
		affected := make([]vulnfeed.Affected, 0, 50)
		for j := range 50 {
			affected = append(affected, vulnfeed.Affected{Vendor: fmt.Sprintf("vendor-%d", i), Product: fmt.Sprintf("product-%d", j), VersionExact: "1.0"})
		}
		if err := e.repo.ReplaceAdvisory(ctx, e.advisory(fmt.Sprintf("bulk-%d", i)), affected); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.f.Admin.Exec(ctx, `ANALYZE vuln_affected`); err != nil {
		t.Fatal(err)
	}
	tx, err := e.f.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The catalog is small in a test database; the planner must be able to
	// serve the read from the index, which it then does on a full catalog.
	if _, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+vulnfeed.ByProductsQuery, "vendor-3", []string{"product-7", "product-8"})
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "idx_vuln_affected_product") {
		t.Errorf("the product read does not use idx_vuln_affected_product:\n%s", plan)
	}
	matches, err := e.repo.ByProducts(ctx, "Vendor-3", []string{"product-7", "PRODUCT-8", " "})
	if err != nil || len(matches) != 2 {
		t.Errorf("ByProducts: %d matches %v, want 2", len(matches), err)
	}
}

// TestVulnTenantContextCannotWrite: in a tenant transaction INSERT,
// UPDATE and DELETE on the three tables fail with a row level security
// error, while SELECT returns every row.
func TestVulnTenantContextCannotWrite(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	adv := e.advisory("tenant")
	if err := e.repo.ReplaceAdvisory(ctx, adv, []vulnfeed.Affected{{Vendor: "acme", Product: "router", VersionExact: "1"}}); err != nil {
		t.Fatal(err)
	}
	scope := database.OrgWideScope(e.f.OrgA, "")
	tenantCtx := database.ContextWithTenantScope(ctx, &scope)
	tenant := func(sql string, args ...any) error {
		return database.WithRequestTenant(tenantCtx, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	for _, c := range []struct {
		name, sql string
		args      []any
	}{
		{"insert source", `INSERT INTO vuln_source (id, kind) VALUES ($1, 'csaf')`, []any{e.source + "/x"}},
		{"insert advisory", `INSERT INTO vuln_advisory (id, source_id) VALUES ($1, $2)`, []any{adv.ID + "-x", e.source}},
		{"insert range", `INSERT INTO vuln_affected (advisory_id, vendor, product) VALUES ($1, 'acme', 'switch')`, []any{adv.ID}},
		{"update source", `UPDATE vuln_source SET etag = 'x' WHERE id = $1`, []any{e.source}},
		{"update advisory", `UPDATE vuln_advisory SET title = 'x' WHERE id = $1`, []any{adv.ID}},
		{"update range", `UPDATE vuln_affected SET product = 'x' WHERE advisory_id = $1`, []any{adv.ID}},
		{"delete range", `DELETE FROM vuln_affected WHERE advisory_id = $1`, []any{adv.ID}},
		{"delete advisory", `DELETE FROM vuln_advisory WHERE id = $1`, []any{adv.ID}},
		{"delete source", `DELETE FROM vuln_source WHERE id = $1`, []any{e.source}},
	} {
		// INSERT is refused by the WITH CHECK of the policy, UPDATE and
		// DELETE by the statement guard of the catalog (42501 both).
		err := tenant(c.sql, c.args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s in a tenant transaction: %v, want insufficient_privilege (42501)", c.name, err)
		}
	}
	var title string
	var ranges int
	if err := database.WithRequestTenant(tenantCtx, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT title FROM vuln_advisory WHERE id = $1`, adv.ID).Scan(&title); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM vuln_affected WHERE advisory_id = $1`, adv.ID).Scan(&ranges)
	}); err != nil || title != adv.Title || ranges != 1 {
		t.Errorf("tenant read: %q %d %v, want the unchanged advisory with 1 range", title, ranges, err)
	}
	if matches, err := e.repo.ByProducts(tenantCtx, "acme", []string{"router"}); err != nil || len(matches) != 1 {
		t.Errorf("ByProducts in the tenant context: %d %v, want 1", len(matches), err)
	}
}

// TestVulnSearchProducts: the search finds vendors and products by part of
// their normalized name, wildcards are taken literally.
func TestVulnSearchProducts(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	if err := e.repo.ReplaceAdvisory(ctx, e.advisory("search"), []vulnfeed.Affected{
		{Vendor: "Palo Alto Networks", Product: "PAN-OS wp1117", VersionExact: "10.1"},
		{Vendor: "Palo Alto Networks", Product: "GlobalProtect wp1117", VersionExact: "6.0"},
		{Vendor: "Juniper", Product: "Junos wp1117", VersionExact: "21.4"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := e.repo.SearchProducts(ctx, "Pan-OS WP1117", 10)
	if err != nil || len(got) != 1 || got[0] != (vulnfeed.Product{Vendor: "palo_alto_networks", Product: "pan-os_wp1117"}) {
		t.Errorf("search pan-os: %+v %v", got, err)
	}
	got, err = e.repo.SearchProducts(ctx, "wp1117", 2)
	if err != nil || len(got) != 2 || got[0].Vendor != "juniper" {
		t.Errorf("search with limit 2: %+v %v, want juniper first", got, err)
	}
	if got, err = e.repo.SearchProducts(ctx, "%", 10); err != nil || len(got) != 0 {
		t.Errorf("search for a literal %%: %+v %v, want none", got, err)
	}
	if vulnfeed.Normalize("  Cisco   Systems\tInc ") != "cisco_systems_inc" {
		t.Errorf("Normalize: %q", vulnfeed.Normalize("  Cisco   Systems\tInc "))
	}
}
