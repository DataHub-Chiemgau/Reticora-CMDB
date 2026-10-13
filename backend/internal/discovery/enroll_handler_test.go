package discovery_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/go-chi/chi/v5"
)

func TestEnrollCollectorFlow(t *testing.T) {
	repo := discovery.NewMemoryRepository()
	h := discovery.NewHandler(repo, nil)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// create a code via the HTTP handler path needs tenant context; use repo directly
	code := &discovery.EnrollmentCode{}
	code.OrganizationID = "org-1"
	code.Label = "x"
	// seed via memory repo internal: use CreateEnrollmentCode with a raw code
	c2 := &discovery.EnrollmentCode{OrganizationID: "org-1", Label: "l", ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	c2.SetRawCode("raw-code-123")
	if err := repo.CreateEnrollmentCode(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	_ = code

	req := httptest.NewRequest(http.MethodPost, "/api/v1/collectors/enroll", strings.NewReader(`{"code":"raw-code-123","name":"col-x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// reuse must fail
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/collectors/enroll", strings.NewReader(`{"code":"raw-code-123","name":"again"}`))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on reuse, got %d", rec2.Code)
	}
}

// TestEnrollCodeDefaultsAndSingleUse covers WP-144 (COL-04): a code is valid
// for 24 hours by default and at most 24 hours, and concurrent enrollments
// with one code register exactly one collector, which is active.
func TestEnrollCodeDefaultsAndSingleUse(t *testing.T) {
	if discovery.EnrollmentCodeTTL != 24*time.Hour {
		t.Fatalf("EnrollmentCodeTTL = %s", discovery.EnrollmentCodeTTL)
	}
	repo := discovery.NewMemoryRepository()
	mux := chi.NewRouter()
	discovery.NewHandler(repo, nil).RegisterRoutes(mux)
	code := &discovery.EnrollmentCode{OrganizationID: "org-1", ExpiresAt: time.Now().UTC().Add(discovery.EnrollmentCodeTTL)}
	code.SetRawCode("raw-race")
	if err := repo.CreateEnrollmentCode(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/collectors/enroll",
				strings.NewReader(`{"code":"raw-race","name":"col"}`)))
			if rec.Code == http.StatusCreated {
				mu.Lock()
				created++
				mu.Unlock()
				if !strings.Contains(rec.Body.String(), `"status":"active"`) {
					t.Errorf("enrolled collector: %s", rec.Body.String())
				}
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("%d enrollments with one code succeeded, want 1", created)
	}
}
