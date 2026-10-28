// WP-563 (SEC-08, F-204): the egress policy of server.Options (set from
// RETICORA_EGRESS_ALLOW_PRIVATE in cmd/server/main.go) reaches the
// notification handler, which checks the url of a webhook channel with it
// when the channel is stored.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestNotifyHandlerUsesTheEgressPolicy$' ./internal/server/
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestNotifyHandlerUsesTheEgressPolicy(t *testing.T) {
	for _, c := range []struct {
		policy egress.Options
		url    string
		want   int
	}{
		{egress.Options{}, "https://10.0.0.5/hook", http.StatusBadRequest},
		{egress.Options{}, "http://169.254.169.254/", http.StatusBadRequest},
		{egress.Options{}, "https://hooks.example.org/hook", http.StatusCreated},
		{egress.Options{AllowPrivate: true}, "https://10.0.0.5/hook", http.StatusCreated},
		{egress.Options{AllowPrivate: true}, "http://169.254.169.254/", http.StatusBadRequest},
	} {
		mux := chi.NewRouter()
		notifyHandler(&Options{Egress: c.policy}).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notification-channels",
			strings.NewReader(`{"name":"hook","provider":"webhook","config":{"url":"`+c.url+`"}}`))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("AllowPrivate %v: webhook channel %s = %d %s, want %d", c.policy.AllowPrivate, c.url, w.Code, w.Body.String(), c.want)
		}
	}
}
