package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestNotificationAPI covers API-05 and NTF-04 (WP-139): the channel
// resource never returns secrets and keeps them when the placeholder comes
// back; the notification center lists, filters and marks the own inbox;
// preferences are validated; deliveries show their status.
func TestNotificationAPI(t *testing.T) {
	center := NewMemoryCenter()
	mux := chi.NewRouter()
	NewHandler(center).RegisterRoutes(mux)
	call := func(user, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: user}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	w := call("u-1", http.MethodPost, "/api/v1/notification-channels", `{"name":"own","provider":"smtp",
		"config":{"host":"mail.example.org","from":"cmdb@example.org","username":"cmdb","password":"pw-1"}}`)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "pw-1") || !strings.Contains(w.Body.String(), secretPlaceholder) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id := decode(w)["id"].(string)
	if w = call("u-1", http.MethodPatch, "/api/v1/notification-channels/"+id,
		`{"name":"own-2","config":{"host":"mail2.example.org","from":"cmdb@example.org","username":"cmdb","password":"********"}}`); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	stored, _ := center.Channels.Get(context.Background(), "org-1", id)
	if stored.Config["password"] != "pw-1" || stored.Config["host"] != "mail2.example.org" || stored.Name != "own-2" {
		t.Errorf("stored after update: %+v", stored.Config)
	}
	for name, tc := range map[string]struct {
		method, path, body string
		status             int
	}{
		"invalid":           {http.MethodPost, "/api/v1/notification-channels", `{"name":"x","provider":"smtp","config":{}}`, http.StatusBadRequest},
		"duplicate":         {http.MethodPost, "/api/v1/notification-channels", `{"name":"own-2","provider":"inapp"}`, http.StatusConflict},
		"placeholder":       {http.MethodPost, "/api/v1/notification-channels", `{"name":"y","provider":"webhook","config":{"url":"https://h","secret":"********"}}`, http.StatusBadRequest},
		"provider change":   {http.MethodPatch, "/api/v1/notification-channels/" + id, `{"provider":"inapp"}`, http.StatusBadRequest},
		"unknown":           {http.MethodGet, "/api/v1/notification-channels/nope", "", http.StatusNotFound},
		"list":              {http.MethodGet, "/api/v1/notification-channels", "", http.StatusOK},
		"deliveries":        {http.MethodGet, "/api/v1/notification-deliveries", "", http.StatusOK},
		"bad preference":    {http.MethodPut, "/api/v1/notifications/preferences", `{"quiet_start":"25:00","quiet_end":"06:00"}`, http.StatusBadRequest},
		"unknown read mark": {http.MethodPost, "/api/v1/notifications/nope/read", "", http.StatusNotFound},
	} {
		if w = call("u-1", tc.method, tc.path, tc.body); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), tc.status)
		}
	}
	if w = call("u-1", http.MethodGet, "/api/v1/notification-channels", ""); strings.Contains(w.Body.String(), "pw-1") {
		t.Error("the list shows a secret")
	}

	// The notification center.
	now := time.Now().UTC()
	_ = center.Inbox.Add(context.Background(), []InAppNotification{
		{OrganizationID: "org-1", UserID: "u-1", Category: "review", Subject: "a", CreatedAt: now.Add(-time.Minute)},
		{OrganizationID: "org-1", UserID: "u-1", Category: "export", Subject: "b", CreatedAt: now},
		{OrganizationID: "org-1", UserID: "u-2", Category: "export", Subject: "c", CreatedAt: now},
	})
	w = call("u-1", http.MethodGet, "/api/v1/notifications", "")
	body := decode(w)
	data, _ := body["data"].([]any)
	if w.Code != http.StatusOK || body["total"] != float64(2) || data[0].(map[string]any)["subject"] != "b" {
		t.Fatalf("inbox: %d %s", w.Code, w.Body.String())
	}
	first := data[0].(map[string]any)["id"].(string)
	if w = call("u-2", http.MethodPost, "/api/v1/notifications/"+first+"/read", ""); w.Code != http.StatusNotFound {
		t.Errorf("another user's notification marked: %d", w.Code)
	}
	if w = call("u-1", http.MethodPost, "/api/v1/notifications/"+first+"/read", ""); w.Code != http.StatusNoContent {
		t.Errorf("mark read: %d", w.Code)
	}
	if body = decode(call("u-1", http.MethodGet, "/api/v1/notifications?unread=true", "")); body["total"] != float64(1) {
		t.Errorf("unread after marking one: %v", body["total"])
	}
	if body = decode(call("u-1", http.MethodPost, "/api/v1/notifications/read-all", "")); body["marked"] != float64(1) {
		t.Errorf("read all: %v", body)
	}
	if w = call("", http.MethodGet, "/api/v1/notifications", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("without a user: %d", w.Code)
	}

	// Preferences.
	if w = call("u-1", http.MethodPut, "/api/v1/notifications/preferences",
		`{"quiet_start":"22:00","quiet_end":"06:00","time_zone":"Europe/Berlin","muted_categories":["review"]}`); w.Code != http.StatusOK {
		t.Fatalf("set preference: %d %s", w.Code, w.Body.String())
	}
	if body = decode(call("u-1", http.MethodGet, "/api/v1/notifications/preferences", "")); body["quiet_start"] != "22:00" {
		t.Errorf("preference: %v", body)
	}
	if body = decode(call("u-2", http.MethodGet, "/api/v1/notifications/preferences", "")); body["time_zone"] != "Europe/Berlin" || body["quiet_start"] != nil {
		t.Errorf("default preference: %v", body)
	}
	if w = call("u-1", http.MethodDelete, "/api/v1/notification-channels/"+id, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
}
