package discovery_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestEnrollmentCodeIsConsumedOnce covers WP-144 (COL-04) against
// PostgreSQL: a code is valid for 24 hours; twelve concurrent enrollments
// with one code register exactly one collector, which is active and
// recorded on the code; an expired code registers nothing; a failed
// registration leaves the code unused.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEnrollmentCodeIsConsumedOnce(t *testing.T) {
	f := scopetest.Seed(t, "a4")
	bg := context.Background()
	repo := discovery.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, nil).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	// The default validity is 24 hours.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/collectors/enrollment-codes",
		strings.NewReader(`{"label": "wp144"}`)).WithContext(ctx))
	if w.Code != http.StatusCreated {
		t.Fatalf("create code: %d %s", w.Code, w.Body.String())
	}
	var validity time.Duration
	if err := f.Admin.QueryRow(bg, `SELECT EXTRACT(EPOCH FROM expires_at - created_at)::bigint * interval '1 second'
		FROM collector_enrollment_code WHERE organization_id = $1 AND label = 'wp144'`, f.OrgA).Scan(&validity); err != nil {
		t.Fatal(err)
	}
	if validity < 24*time.Hour-time.Minute || validity > 24*time.Hour+time.Minute {
		t.Fatalf("code valid for %s, want 24h", validity)
	}

	seed := func(raw string, expires time.Time) {
		t.Helper()
		code := &discovery.EnrollmentCode{OrganizationID: f.OrgA, Label: raw, ExpiresAt: expires}
		code.SetRawCode(raw)
		if err := repo.CreateEnrollmentCode(ctx, code); err != nil {
			t.Fatal(err)
		}
	}
	count := func(name string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM collector WHERE organization_id = $1 AND name LIKE $2`, f.OrgA, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Concurrent enrollments: exactly one registers.
	seed("scopetest-a4-race", time.Now().Add(time.Hour))
	const racers = 12
	var wg sync.WaitGroup
	codes := make([]int, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/collectors/enroll",
				strings.NewReader(`{"code": "scopetest-a4-race", "name": "wp144-race"}`)))
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusUnauthorized:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 || count("wp144-race") != 1 {
		t.Fatalf("%d of %d enrollments succeeded, %d collectors registered; want exactly one", created, racers, count("wp144-race"))
	}
	var status string
	var bound bool
	if err := f.Admin.QueryRow(bg, `SELECT c.status, e.used_by_collector_id = c.id FROM collector c
		JOIN collector_enrollment_code e ON e.organization_id = c.organization_id AND e.label = 'scopetest-a4-race'
		WHERE c.name = 'wp144-race' AND c.organization_id = $1`, f.OrgA).Scan(&status, &bound); err != nil || status != "active" || !bound {
		t.Fatalf("enrolled collector status %s, recorded on the code %v (%v)", status, bound, err)
	}

	// Expired codes register nothing.
	seed("scopetest-a4-expired", time.Now().Add(-time.Minute))
	col := &discovery.Collector{Name: "wp144-expired"}
	if _, err := repo.EnrollCollector(bg, "scopetest-a4-expired", col, nil); err == nil || count("wp144-expired") != 0 {
		t.Fatalf("expired code: %v", err)
	}

	// A registration that fails leaves the code usable.
	seed("scopetest-a4-retry", time.Now().Add(time.Hour))
	bad := &discovery.Collector{Name: "wp144-retry", SiteID: "00000000-0000-4000-8000-000000000000"}
	if _, err := repo.EnrollCollector(bg, "scopetest-a4-retry", bad, nil); err == nil {
		t.Fatal("enrollment with an unknown site succeeded")
	}
	if _, err := repo.EnrollCollector(bg, "scopetest-a4-retry", &discovery.Collector{Name: "wp144-retry"}, nil); err != nil || count("wp144-retry") != 1 {
		t.Fatalf("retry after a failed registration: %v", err)
	}
}
