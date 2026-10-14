package ci_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOptimisticConcurrency covers WP-061 (API-07, API-03): GET returns the
// version as ETag; only writes of rank >= 92 raise it; a PATCH with an older
// If-Match is refused with 409 only when one of its fields changed since;
// an unknown version is 412; an organization can require If-Match (412
// without it); of two concurrent PATCHes of the same field one wins.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOptimisticConcurrency(t *testing.T) {
	f := scopetest.Seed(t, "5e")
	bg := context.Background()
	ciID := f.CI(t, f.OrgA, f.Client1, "occ-ci")
	user := f.AppUser(t, f.OrgA, "occ-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	repo := ci.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)

	call := func(method, ifMatch, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/cis/"+ciID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int, etag string) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
		}
		if etag != "" && w.Header().Get("ETag") != etag {
			t.Errorf("ETag %q, want %q", w.Header().Get("ETag"), etag)
		}
	}

	expect(call(http.MethodGet, "", ""), http.StatusOK, `"1"`)
	expect(call(http.MethodPatch, `"1"`, `{"name":"occ-a"}`), http.StatusOK, `"2"`)

	// An observed update (discovery) leaves the version.
	src := ci.SourceSNMP
	if _, err := repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{Model: strPtr("observed"), DiscoverySource: &src}); err != nil {
		t.Fatal(err)
	}
	expect(call(http.MethodGet, "", ""), http.StatusOK, `"2"`)
	// So does a write of a lower rank; a workflow write (92) raises it.
	if _, err := repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{OSName: strPtr("linux"), Authoritative: true}); err != nil {
		t.Fatal(err)
	}
	expect(call(http.MethodGet, "", ""), http.StatusOK, `"3"`)

	// Field-granular: an old If-Match passes when its fields did not change
	// since, and is refused with the changed fields otherwise.
	expect(call(http.MethodPatch, `"1"`, `{"manufacturer":"Acme"}`), http.StatusOK, `"4"`)
	w := call(http.MethodPatch, `W/"1"`, `{"name":"occ-b","os_name":"bsd"}`)
	expect(w, http.StatusConflict, `"4"`)
	if !strings.Contains(w.Body.String(), `"fields":["name","os_name"]`) || !strings.Contains(w.Body.String(), `"current_version":4`) {
		t.Errorf("409 body: %s", w.Body.String())
	}
	// Unknown or malformed versions are 412.
	for _, tag := range []string{`"99"`, `"abc"`, `"0"`} {
		if w = call(http.MethodPatch, tag, `{"name":"occ-c"}`); w.Code != http.StatusPreconditionFailed ||
			!strings.Contains(w.Body.String(), "precondition-failed") {
			t.Errorf("If-Match %s: %d %s, want 412", tag, w.Code, w.Body.String())
		}
	}

	// Without the organization setting If-Match is optional; with it, it is
	// required ("*" satisfies it).
	expect(call(http.MethodPatch, "", `{"model":"m1"}`), http.StatusOK, `"5"`)
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	expect(call(http.MethodPatch, "", `{"model":"m2"}`), http.StatusPreconditionFailed, "")
	expect(call(http.MethodPatch, "*", `{"model":"m2"}`), http.StatusOK, `"6"`)

	// Two editors with the same version change the same field: one wins.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = call(http.MethodPatch, `"6"`, `{"name":"race"}`).Code
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict {
		t.Errorf("concurrent PATCHes: %v, want one 200 and one 409", codes)
	}
}

func strPtr(s string) *string { return &s }

// integrityViolations runs tests/integrity-check.sql for one organization
// and returns the checks with violations (ABN-03).
func integrityViolations(t *testing.T, pool *pgxpool.Pool, orgID string) map[string]int {
	t.Helper()
	script, err := os.ReadFile("../../../tests/integrity-check.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('integrity.org', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, string(script))
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	checks := 0
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		checks++
		if n > 0 {
			out[name] = n
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checks == 0 {
		t.Fatal("integrity check returned no checks")
	}
	return out
}

// TestParallelMerge covers the merge part of the concurrency matrix of
// ABN-03 (WP-207) with real parallel transactions against PostgreSQL: two
// merges of the same victim into different survivors, and two merges of a
// pair in opposite directions, each let exactly one merge through; the
// victim ends merged into one survivor, its edges move with it, and the
// integrity check finds no orphans, duplicates or cycles.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestParallelMerge(t *testing.T) {
	f := scopetest.Seed(t, "c7")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "abn03-merger")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	svc := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))

	// parallel starts the functions at once and returns their errors.
	parallel := func(fns ...func() error) []error {
		errs := make([]error, len(fns))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, fn := range fns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = fn()
			}()
		}
		close(start)
		wg.Wait()
		return errs
	}
	succeeded := func(errs []error) int {
		n := 0
		for _, err := range errs {
			if err == nil {
				n++
			}
		}
		return n
	}

	for round := 0; round < 5; round++ {
		s1 := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("abn03-s1-%d", round))
		s2 := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("abn03-s2-%d", round))
		victim := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("abn03-v-%d", round))
		peer := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("abn03-peer-%d", round))
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, 'depends_on')`, f.OrgA, victim, peer); err != nil {
			t.Fatal(err)
		}
		errs := parallel(
			func() error { _, err := svc.Merge(ctx, f.OrgA, s1, victim); return err },
			func() error { _, err := svc.Merge(ctx, f.OrgA, s2, victim); return err },
		)
		if succeeded(errs) != 1 {
			t.Fatalf("round %d: merges of one victim: %v, want exactly one success", round, errs)
		}
		into, found, err := svc.MergedInto(ctx, f.OrgA, victim)
		if err != nil || !found || (into != s1 && into != s2) {
			t.Fatalf("round %d: victim merged into %q (found %v, err %v)", round, into, found, err)
		}
		var moved int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2`,
			into, peer).Scan(&moved); err != nil || moved != 1 {
			t.Errorf("round %d: edge moved to the survivor: %d (err %v)", round, moved, err)
		}

		a := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("abn03-a-%d", round))
		b := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("abn03-b-%d", round))
		errs = parallel(
			func() error { _, err := svc.Merge(ctx, f.OrgA, a, b); return err },
			func() error { _, err := svc.Merge(ctx, f.OrgA, b, a); return err },
		)
		if succeeded(errs) != 1 {
			t.Fatalf("round %d: crossing merges: %v, want exactly one success", round, errs)
		}
	}

	if v := integrityViolations(t, f.Admin, f.OrgA); len(v) > 0 {
		t.Errorf("integrity violations after the parallel merges: %v", v)
	}
}
