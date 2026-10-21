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

// channelAPI serves the notification handler of center for org-1.
func channelAPI(center *Center) func(method, path, body string) *httptest.ResponseRecorder {
	mux := chi.NewRouter()
	NewHandler(center).RegisterRoutes(mux)
	return func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
}

// TestChannelResponsesRedactSecretsPerProvider covers API-05, NTF-01 and
// F-154 (WP-398): create, read, list and update answer smtp with the
// password and webhook with the secret as the placeholder, and the webhook
// url as scheme and host only: path, query and user info carry the token
// of an incoming webhook. central_smtp and inapp keep their settings.
func TestChannelResponsesRedactSecretsPerProvider(t *testing.T) {
	call := channelAPI(NewMemoryCenter())
	const token, queryToken, userInfo = "T0KENPATH", "T0KENQUERY", "T0KENUSER"
	for _, tc := range []struct {
		name, body string
		secrets    []string
		want       map[string]any
	}{
		{"smtp", `{"name":"smtp","provider":"smtp","config":{"host":"mail.example.org","port":587,"from":"cmdb@example.org",
			"username":"cmdb","password":"pw-` + token + `"}}`, []string{"pw-" + token},
			map[string]any{"host": "mail.example.org", "username": "cmdb", "password": secretPlaceholder}},
		{"webhook", `{"name":"chat","provider":"webhook","config":{"url":"https://hooks.example.org/services/T0/B0/` + token +
			`?key=` + queryToken + `#frag","secret":"hmac-` + token + `"}}`, []string{token, queryToken, "hmac-"},
			map[string]any{"url": "https://hooks.example.org/" + secretPlaceholder, "secret": secretPlaceholder}},
		{"webhook with port and user info", `{"name":"chat-2","provider":"webhook","config":{"url":"https://svc:` + userInfo +
			`@hooks.example.org:8443/` + token + `"}}`, []string{token, userInfo, "svc:"},
			map[string]any{"url": "https://hooks.example.org:8443/" + secretPlaceholder}},
		{"central_smtp", `{"name":"central","provider":"central_smtp","config":{"sender_name":"CMDB","reply_to":"ops@example.org"}}`, nil,
			map[string]any{"sender_name": "CMDB", "reply_to": "ops@example.org"}},
		{"inapp", `{"name":"center","provider":"inapp","config":{}}`, nil, map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(http.MethodPost, "/api/v1/notification-channels", tc.body)
			if w.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			var created Channel
			_ = json.Unmarshal(w.Body.Bytes(), &created)
			responses := map[string]*httptest.ResponseRecorder{
				"create": w,
				"get":    call(http.MethodGet, "/api/v1/notification-channels/"+created.ID, ""),
				"list":   call(http.MethodGet, "/api/v1/notification-channels", ""),
				"rename": call(http.MethodPatch, "/api/v1/notification-channels/"+created.ID, `{"name":"`+tc.name+`-renamed"}`),
			}
			for op, r := range responses {
				if r.Code != http.StatusOK && r.Code != http.StatusCreated {
					t.Fatalf("%s: %d %s", op, r.Code, r.Body.String())
				}
				for _, s := range tc.secrets {
					if strings.Contains(r.Body.String(), s) {
						t.Errorf("%s response contains %q: %s", op, s, r.Body.String())
					}
				}
			}
			var got Channel
			_ = json.Unmarshal(responses["get"].Body.Bytes(), &got)
			for k, v := range tc.want {
				if got.Config[k] != v {
					t.Errorf("config.%s = %v, want %v", k, got.Config[k], v)
				}
			}
		})
	}
}

// TestWebhookURLPlaceholderKeepsTheStoredURL covers API-05 (WP-398): the
// masked url sent back unchanged keeps the stored url with its token, like
// the password placeholder; a new url replaces it, and the masked form of
// another host is a new url that does not take the token along.
func TestWebhookURLPlaceholderKeepsTheStoredURL(t *testing.T) {
	center := NewMemoryCenter()
	call := channelAPI(center)
	const stored = "https://hooks.example.org/services/T0/B0/T0KEN?key=Q"
	w := call(http.MethodPost, "/api/v1/notification-channels",
		`{"name":"chat","provider":"webhook","config":{"url":"`+stored+`","secret":"hmac-1"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var ch Channel
	_ = json.Unmarshal(w.Body.Bytes(), &ch)
	masked, _ := ch.Config["url"].(string)
	if masked != "https://hooks.example.org/"+secretPlaceholder {
		t.Fatalf("returned url = %q", masked)
	}
	storedURL := func() string {
		t.Helper()
		c, err := center.Channels.Get(context.Background(), "org-1", ch.ID)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := c.Config["url"].(string)
		return u
	}
	for _, tc := range []struct{ name, url, want string }{
		{"placeholder keeps the url", masked, stored},
		{"new url replaces it", "https://hooks.example.org/services/T0/B0/N3W", "https://hooks.example.org/services/T0/B0/N3W"},
		{"placeholder of the new url keeps it", masked, "https://hooks.example.org/services/T0/B0/N3W"},
		{"masked url of another host is a new url", "https://attacker.example/" + secretPlaceholder, "https://attacker.example/" + secretPlaceholder},
	} {
		w = call(http.MethodPatch, "/api/v1/notification-channels/"+ch.ID, `{"config":{"url":"`+tc.url+`","secret":"hmac-2"}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: PATCH %d %s", tc.name, w.Code, w.Body.String())
		}
		if got := storedURL(); got != tc.want {
			t.Errorf("%s: stored url = %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(w.Body.String(), "T0KEN") || strings.Contains(w.Body.String(), "N3W") {
			t.Errorf("%s: PATCH response reveals the url: %s", tc.name, w.Body.String())
		}
	}
}

// TestDeliveryListShowsNoContent covers NTF-04 and F-154 (WP-398): the
// delivery list names recipients, category, status, attempts and the last
// error, but neither body nor data; the invitation link and the webhook
// token appear in no response, also not in the error a failed webhook send
// stores, nor in an older error that still holds the url.
func TestDeliveryListShowsNoContent(t *testing.T) {
	ctx := context.Background()
	center := NewMemoryCenter()
	call := channelAPI(center)
	const hookToken, inviteToken = "H00KT0KEN", "INV1TET0KEN"
	for _, body := range []string{
		// Port 1 refuses the connection: the send fails with the url in its error.
		`{"name":"chat","provider":"webhook","config":{"url":"http://127.0.0.1:1/services/` + hookToken + `?key=` + hookToken + `"}}`,
		`{"name":"mail","provider":"central_smtp","config":{}}`,
	} {
		if w := call(http.MethodPost, "/api/v1/notification-channels", body); w.Code != http.StatusCreated {
			t.Fatalf("create channel: %d %s", w.Code, w.Body.String())
		}
	}
	mail := &flakyProvider{}
	sender := NewSender(map[string]Provider{ProviderCentralSMTP: mail,
		ProviderWebhook: NewWebhookProvider(&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}})})
	store := center.Deliveries.(*MemoryStore)
	n := NewNotifier(center.Channels, store, store, sender)
	link := "https://cmdb.example.org/invite/" + inviteToken
	if _, err := n.Notify(ctx, "org-1", &Request{Category: "invitation", Critical: true, Subject: "Einladung zu Reticora",
		Body: "Sie wurden eingeladen:\n" + link, Data: map[string]any{"link": link, "email": "new@example.org"},
		DedupKey: "invitation.created:e-1", Recipients: []Recipient{{Email: "new@example.org"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProcessDue(ctx); err == nil {
		t.Fatal("the webhook send to port 1 succeeded")
	}
	rows, _ := store.List(ctx, "org-1", 10, 0)
	for _, r := range rows {
		if strings.Contains(r.LastError, hookToken) {
			t.Errorf("stored last_error contains the webhook token: %s", r.LastError)
		}
	}
	// An error stored before the provider masked the url.
	if _, err := store.Enqueue(ctx, "org-1", []QueuedDelivery{{OrganizationID: "org-1", ChannelID: rows[0].ChannelID,
		RecipientKey: "*", Category: "export", Subject: "Export fertig (csv)", Body: link, Status: StatusFailed, Attempts: 5,
		MaxAttempts: 5, LastError: `webhook: Post "https://hooks.example.org/services/` + hookToken + `": EOF`}}); err != nil {
		t.Fatal(err)
	}

	w := call(http.MethodGet, "/api/v1/notification-deliveries", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deliveries: %d %s", w.Code, w.Body.String())
	}
	for _, leak := range []string{hookToken, inviteToken, "/invite/", "Sie wurden eingeladen"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("delivery list contains %q: %s", leak, w.Body.String())
		}
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Data) != 3 {
		t.Fatalf("deliveries: %v %s", err, w.Body.String())
	}
	byStatus := map[string]map[string]any{}
	for _, d := range list.Data {
		for _, k := range []string{"body", "data"} {
			if _, ok := d[k]; ok {
				t.Errorf("delivery %v has %q", d["id"], k)
			}
		}
		for _, k := range []string{"recipient", "category", "status", "attempts", "channel_id"} {
			if _, ok := d[k]; !ok {
				t.Errorf("delivery %v lacks %q", d["id"], k)
			}
		}
		byStatus[d["status"].(string)] = d
	}
	sent, pending, failed := byStatus[StatusSent], byStatus[StatusPending], byStatus[StatusFailed]
	if sent == nil || pending == nil || failed == nil {
		t.Fatalf("statuses: %v", list.Data)
	}
	if rcpt, _ := sent["recipient"].(map[string]any); rcpt["email"] != "new@example.org" || sent["category"] != "invitation" || sent["attempts"] != float64(1) {
		t.Errorf("e-mail delivery: %v", sent)
	}
	if rcpts, _ := pending["recipients"].([]any); len(rcpts) != 1 || pending["attempts"] != float64(1) ||
		!strings.Contains(pending["last_error"].(string), "http://127.0.0.1:1/"+secretPlaceholder) {
		t.Errorf("webhook delivery: %v", pending)
	}
	if failed["last_error"] != `webhook: Post "https://hooks.example.org/`+secretPlaceholder+`": EOF` {
		t.Errorf("older last_error = %v", failed["last_error"])
	}
}
