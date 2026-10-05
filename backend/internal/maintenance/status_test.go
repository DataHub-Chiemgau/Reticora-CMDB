package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestNotifyQueuesWithoutClaimingDelivery covers WP-063 (MGT-04, NTF-05,
// NFR-04): announcing a maintenance window creates one notification per
// affected client in status pending without sent_at, and the response does
// not count them as notified; the stored notifications show the same.
func TestNotifyQueuesWithoutClaimingDelivery(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SetCIClient("ci-1", "client-a")
	repo.SetCIClient("ci-2", "client-b")
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	w := &Window{OrganizationID: "org-1", Title: "Upgrade", StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), CIIDs: []string{"ci-1", "ci-2"}}
	if err := repo.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")).WithContext(ctx))
		return rec
	}

	rec := call(http.MethodPost, "/api/v1/maintenance-windows/"+w.ID+"/notify")
	var resp struct {
		Notified      int            `json:"notified"`
		Queued        int            `json:"queued"`
		Notifications []Notification `json:"notifications"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("notify: %d %s", rec.Code, rec.Body.String())
	}
	if resp.Notified != 0 || resp.Queued != 2 {
		t.Errorf("notify response notified=%d queued=%d, want 0 and 2", resp.Notified, resp.Queued)
	}

	rec = call(http.MethodGet, "/api/v1/maintenance-windows/"+w.ID+"/notifications")
	var list struct {
		Data []Notification `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 2 {
		t.Fatalf("notifications: %s", rec.Body.String())
	}
	for _, n := range list.Data {
		if n.Status != NotificationPending || n.SentAt != nil {
			t.Errorf("notification %s: status %s sent_at %v, want pending without sent_at", n.ClientID, n.Status, n.SentAt)
		}
	}
}

// TestNotifyQueuesInPostgres checks the same for the PostgreSQL repository:
// the stored rows are pending and carry no sent_at.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNotifyQueuesInPostgres(t *testing.T) {
	f := scopetest.Seed(t, "5f")
	repo := NewPGRepository(f.App)
	ciID := f.CI(t, f.OrgA, f.Client1, "maint-status")
	start := time.Now().UTC().Add(time.Hour)
	w := &Window{OrganizationID: f.OrgA, Title: "Upgrade", StartsAt: start, EndsAt: start.Add(time.Hour), CIIDs: []string{ciID}}
	ctx := f.OrgCtx(f.OrgA)
	if err := repo.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	queued, err := repo.NotifyClients(ctx, f.OrgA, w.ID)
	if err != nil || len(queued) != 1 || queued[0].Status != NotificationPending || queued[0].SentAt != nil {
		t.Fatalf("notify: %+v, %v", queued, err)
	}
	var status string
	var sentAt *time.Time
	if err = f.Admin.QueryRow(context.Background(), `SELECT status, sent_at FROM maintenance_notification WHERE id = $1`, queued[0].ID).
		Scan(&status, &sentAt); err != nil || status != NotificationPending || sentAt != nil {
		t.Errorf("stored notification: %s %v, %v; want pending without sent_at", status, sentAt, err)
	}
}
