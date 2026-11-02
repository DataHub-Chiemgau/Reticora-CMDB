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
	"github.com/jackc/pgx/v5"
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

// TestAuditT3OverrideWritesRaiseVersion covers WP-412 (API-07, OVR-01;
// retest of F-021, formerly an audit defect test): setting and clearing a
// manual override are manual writes. Each raises the CI version by one in
// its transaction, records a ci_change row of type override with the new
// version and answers with the new ETag. A PATCH of the field with the
// If-Match read before is refused with 409 and leaves the override; a PATCH
// of another field passes (field-granular). Attributes count as one field,
// as for a PATCH, and personal values are sealed in the history (SEC-05).
// Clearing a field without an override keeps the version; an unknown field
// or CI is 404.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditT3OverrideWritesRaiseVersion(t *testing.T) {
	r := newAuditT3CIRouter(t, "6b")
	bg := context.Background()
	// overrideChanges lists the override rows of the CI history in version
	// order: field@version old -> new by actor.
	overrideChanges := func(t *testing.T, id string) []string {
		t.Helper()
		rows, err := r.f.Admin.Query(bg, `SELECT field_name || '@' || ci_version || ' ' || old_value::text || ' -> ' || new_value::text
				|| ' by ' || COALESCE(actor_id::text, '-')
			FROM ci_change WHERE ci_id = $1 AND change_type = 'override' ORDER BY ci_version`, id)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	expect := func(t *testing.T, what string, w *httptest.ResponseRecorder, status int, etag string) {
		t.Helper()
		if w.Code != status {
			t.Errorf("%s: status %d, want %d: %s", what, w.Code, status, w.Body.String())
		}
		if got := w.Header().Get("ETag"); got != etag {
			t.Errorf("%s: ETag %q, want %q", what, got, etag)
		}
	}

	t.Run("PUT override raises the version and blocks a stale PATCH of the same field", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-put")
		if _, err := r.f.Admin.Exec(bg, `UPDATE ci SET model = 'row-model' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		base := "/api/v1/cis/" + id
		expect(t, "GET", r.call(http.MethodGet, base, "", ""), http.StatusOK, `"1"`)
		w := r.call(http.MethodPut, base+"/fields/model/override", "", `{"value":"manual-model","reason":"audit","protected":true}`)
		expect(t, "PUT override", w, http.StatusOK, `"2"`)
		if !strings.Contains(w.Body.String(), `"effective_value":"manual-model"`) {
			t.Errorf("PUT override body: %s", w.Body.String())
		}
		w = r.call(http.MethodGet, base, "", "")
		expect(t, "GET after PUT override", w, http.StatusOK, `"2"`)
		if !strings.Contains(w.Body.String(), `"model":"manual-model"`) {
			t.Errorf("GET after PUT override: %s", w.Body.String())
		}
		if got, want := overrideChanges(t, id), []string{`model@2 "row-model" -> "manual-model" by ` + r.user}; !slices.Equal(got, want) {
			t.Errorf("ci_change override rows %q, want %q", got, want)
		}

		w = r.call(http.MethodPatch, base, `"1"`, `{"model":"stale-model"}`)
		expect(t, "PATCH model with the If-Match read before the override", w, http.StatusConflict, `"2"`)
		if !strings.Contains(w.Body.String(), `"fields":["model"]`) || !strings.Contains(w.Body.String(), `"current_version":2`) {
			t.Errorf("409 body: %s, want fields [model] and current_version 2", w.Body.String())
		}
		if got := r.str(`SELECT coalesce(model, '') FROM ci WHERE id = $1`, id); got != "manual-model" {
			t.Errorf("ci.model after the stale PATCH = %q, want the protected override manual-model (silent lost update)", got)
		}
		if got := r.str(`SELECT override_value #>> '{}' || ' ' || protected FROM ci_field_value WHERE ci_id = $1 AND field_name = 'model'`, id); got != "manual-model true" {
			t.Errorf("override after the stale PATCH = %q, want manual-model true", got)
		}
		// A stale PATCH of a different field is not a conflict (field-granular API-07).
		expect(t, "PATCH of another field with the same If-Match", r.call(http.MethodPatch, base, `"1"`, `{"hostname":"audit-t3-other-field"}`),
			http.StatusOK, `"3"`)
	})

	t.Run("DELETE override restoring the observed value raises the version", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-del")
		base := "/api/v1/cis/" + id
		if w := r.call(http.MethodPut, base+"/fields/model/override", "", `{"value":"manual-model","reason":"audit"}`); w.Code != http.StatusOK {
			t.Fatalf("PUT override: %d %s", w.Code, w.Body.String())
		}
		if _, err := r.f.Admin.Exec(bg, `UPDATE ci_field_value SET discovered_value = '"observed-model"',
			discovered_source = 'snmp', discovered_at = now(), discovered_rank = 80 WHERE ci_id = $1 AND field_name = 'model'`, id); err != nil {
			t.Fatal(err)
		}
		v0 := r.version(id)
		w := r.call(http.MethodDelete, base+"/fields/model/override", "", "")
		expect(t, "DELETE override", w, http.StatusOK, auditT3Tag(v0+1))
		if !strings.Contains(w.Body.String(), `"effective_value":"observed-model"`) {
			t.Errorf("DELETE override body: %s", w.Body.String())
		}
		if got := r.str(`SELECT coalesce(model, '') FROM ci WHERE id = $1`, id); got != "observed-model" {
			t.Fatalf("precondition: ci.model after clear = %q, want observed-model", got)
		}
		if v1 := r.version(id); v1 != v0+1 {
			t.Errorf("version after clearing the override (effective value manual-model -> observed-model) = %d, want %d", v1, v0+1)
		}
		want := []string{
			fmt.Sprintf(`model@%d null -> "manual-model" by %s`, v0, r.user),
			fmt.Sprintf(`model@%d "manual-model" -> "observed-model" by %s`, v0+1, r.user),
		}
		if got := overrideChanges(t, id); !slices.Equal(got, want) {
			t.Errorf("ci_change override rows %q, want %q", got, want)
		}
		w = r.call(http.MethodPatch, base, auditT3Tag(v0), `{"model":"stale-model"}`)
		expect(t, "PATCH model with the If-Match read before the clear", w, http.StatusConflict, auditT3Tag(v0+1))
		if !strings.Contains(w.Body.String(), `"fields":["model"]`) {
			t.Errorf("409 body: %s, want fields [model]", w.Body.String())
		}
		w = r.call(http.MethodGet, base, "", "")
		expect(t, "GET after the clear", w, http.StatusOK, auditT3Tag(v0+1))
		if !strings.Contains(w.Body.String(), `"model":"observed-model"`) {
			t.Errorf("GET after the clear: %s", w.Body.String())
		}
	})

	t.Run("an attribute override counts as the field attributes; personal values are sealed", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-attr")
		if _, err := r.f.Admin.Exec(bg, `UPDATE ci SET attributes = '{"owner":"row"}' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		base := "/api/v1/cis/" + id
		expect(t, "PUT override of owner", r.call(http.MethodPut, base+"/fields/owner/override", "", `{"value":"ops","reason":"audit"}`),
			http.StatusOK, `"2"`)
		w := r.call(http.MethodPatch, base, `"1"`, `{"attributes":{"owner":"stale"}}`)
		expect(t, "PATCH attributes with the If-Match read before the override", w, http.StatusConflict, `"2"`)
		if !strings.Contains(w.Body.String(), `"fields":["attributes"]`) {
			t.Errorf("409 body: %s, want fields [attributes]", w.Body.String())
		}
		if got := r.str(`SELECT attributes->>'owner' FROM ci WHERE id = $1`, id); got != "ops" {
			t.Errorf("owner after the stale PATCH = %q, want the override ops", got)
		}
		expect(t, "PUT override of contact_email", r.call(http.MethodPut, base+"/fields/contact_email/override", "",
			`{"value":"jane.doe@example.org","reason":"audit"}`), http.StatusOK, `"3"`)
		expect(t, "DELETE override of contact_email", r.call(http.MethodDelete, base+"/fields/contact_email/override", "", ""),
			http.StatusOK, `"4"`)
		got := overrideChanges(t, id)
		if len(got) != 3 || got[0] != `attributes@2 {"owner": "row"} -> {"owner": "ops"} by `+r.user {
			t.Fatalf("ci_change override rows %q, want owner at version 2 and contact_email at 3 and 4", got)
		}
		for _, change := range got[1:] {
			if strings.Contains(change, "jane.doe") || !strings.Contains(change, `"$pii"`) || !strings.HasPrefix(change, "attributes@") {
				t.Errorf("ci_change row of the personal attribute %q, want the value as vault surrogate (SEC-05)", change)
			}
		}
	})

	t.Run("DELETE without an override keeps the version; unknown field or CI is 404", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-none")
		if _, err := r.f.Admin.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, discovered_value, discovered_source, discovered_at)
			VALUES ($1, $2, 'hostname', '"observed-host"', 'snmp', now())`, r.f.OrgA, id); err != nil {
			t.Fatal(err)
		}
		base := "/api/v1/cis/" + id
		expect(t, "DELETE override of a field without one", r.call(http.MethodDelete, base+"/fields/hostname/override", "", ""),
			http.StatusOK, `"1"`)
		if v := r.version(id); v != 1 {
			t.Errorf("version after clearing no override = %d, want 1", v)
		}
		if got := overrideChanges(t, id); len(got) != 0 {
			t.Errorf("ci_change override rows %q, want none", got)
		}
		other := r.f.CI(t, r.f.OrgB, "", "audit-t3-ovr-org-b")
		if _, err := r.f.Admin.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
			VALUES ($1, $2, 'model', '"org-b-model"', 'org b', now(), true)`, r.f.OrgB, other); err != nil {
			t.Fatal(err)
		}
		for name, path := range map[string]string{
			"unknown field":              base + "/fields/no-such-field/override",
			"unknown CI":                 "/api/v1/cis/" + r.f.ID() + "/fields/model/override",
			"CI of another organization": "/api/v1/cis/" + other + "/fields/model/override",
		} {
			w := r.call(http.MethodDelete, path, "", "")
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "field value not found") || w.Header().Get("ETag") != "" {
				t.Errorf("%s: %d %s (ETag %q), want 404 field value not found", name, w.Code, w.Body.String(), w.Header().Get("ETag"))
			}
		}
		if got := r.str(`SELECT version || ' ' || (SELECT override_value #>> '{}' FROM ci_field_value WHERE ci_id = ci.id AND field_name = 'model')
			FROM ci WHERE id = $1`, other); got != "1 org-b-model" {
			t.Errorf("CI of organization B after the DELETE: %q, want version 1 and its override", got)
		}
	})
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

// TestAutomatedWriteKeepsOverride is the deterministic form of "discovery
// during edit" (ABN-03, OVR-01): discovery decided to write a field before a
// manual change committed its override; the write that follows must leave
// the manual value, because the repository checks the overrides under the
// row lock of the update.
func TestAutomatedWriteKeepsOverride(t *testing.T) {
	f := scopetest.Seed(t, "cc")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "abn03-ovr")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	repo := ci.NewPGRepository(f.App)
	id := f.CI(t, f.OrgA, f.Client1, "abn03-ovr")

	manual, auto, autoOS := "manual-model", "auto-model", "auto-os"
	if _, err := repo.Update(ctx, f.OrgA, id, ci.UpdateRequest{Model: &manual, Manual: &ci.ManualChange{Author: user},
		ChangeReason: "abn03"}); err != nil {
		t.Fatal(err)
	}
	item, err := repo.Update(ctx, f.OrgA, id, ci.UpdateRequest{Model: &auto, OSName: &autoOS})
	if err != nil {
		t.Fatal(err)
	}
	if item.Model != manual || item.OSName != autoOS {
		t.Errorf("after the automated write: model %q, os %q; want the manual model and the new os", item.Model, item.OSName)
	}
}
