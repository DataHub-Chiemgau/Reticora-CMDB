package notify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestNotificationLanguageSelfService covers NTF-03 (WP-653, F-383)
// against PostgreSQL: a user sets the language of their notifications
// through PUT /api/v1/notifications/preferences (de-DE, en-US, or empty
// for the organization's locale); it is stored in app_user.locale (as
// reticora_app under RLS) and decides the language of the user's e-mails
// and in-app notifications. GET names it; a PUT without locale keeps it; a
// language without templates is refused with 400 and changes nothing.
// Choosing the organization's locale after a language stores it as empty,
// which a later login's locale claim does not replace; without a language
// chosen before it keeps the column default de, so the next login still
// takes the claim (identity TestLoginTakesLocaleClaim). The organization's
// locale is de-DE.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNotificationLanguageSelfService(t *testing.T) {
	f := scopetest.Seed(t, "9a")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// The organization's locale is German.
	exec(`UPDATE organization SET locale = 'de-DE' WHERE id = $1`, f.OrgA)
	role := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions)
		VALUES ($1, $2, 'ntf653-ops', 'org', '["review:resolve", "discovery:manage"]'::jsonb)`, role, f.OrgA)
	users := map[string]string{}
	for _, name := range []string{"en", "back", "fresh"} {
		users[name] = f.AppUser(t, f.OrgA, name)
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, users[name], role)
	}
	stored := func(name string) string {
		t.Helper()
		var locale string
		if err := f.Admin.QueryRow(bg, `SELECT locale FROM app_user WHERE id = $1`, users[name]).Scan(&locale); err != nil {
			t.Fatalf("app_user.locale of %s: %v", name, err)
		}
		return locale
	}

	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	mux := chi.NewRouter()
	notify.NewHandler(center).RegisterRoutes(mux)
	call := func(name, method, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/notifications/preferences", strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: users[name]}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	put := func(name, body string, want int) {
		t.Helper()
		if code, out := call(name, http.MethodPut, body); code != want {
			t.Errorf("PUT %s of %s: %d %v, want %d", body, name, code, out, want)
		}
	}

	if code, out := call("en", http.MethodGet, ""); code != http.StatusOK || out["locale"] != "" {
		t.Errorf("GET without a choice: %d locale %#v, want 200 and empty", code, out["locale"])
	}
	put("en", `{"locale":"en-US"}`, http.StatusOK)
	if got := stored("en"); got != "en-US" {
		t.Errorf("app_user.locale after PUT en-US: %q, want en-US (F-383)", got)
	}
	put("en", `{"time_zone":"Europe/Berlin","quiet_start":"23:00","quiet_end":"23:30"}`, http.StatusOK)
	put("en", `{"locale":"fr-FR"}`, http.StatusBadRequest)
	if code, out := call("en", http.MethodGet, ""); code != http.StatusOK || out["locale"] != "en-US" || out["quiet_start"] != "23:00" {
		t.Errorf("GET after a PUT without locale and a refused one: %d %v, want en-US and the quiet hours", code, out)
	}
	if got := stored("en"); got != "en-US" {
		t.Errorf("app_user.locale after a PUT without locale and a refused one: %q, want en-US", got)
	}

	// The organization's locale chosen after a language, and without one.
	put("back", `{"locale":"en-US"}`, http.StatusOK)
	put("back", `{"locale":""}`, http.StatusOK)
	put("fresh", `{"locale":""}`, http.StatusOK)
	if got := stored("back"); got != "" {
		t.Errorf("app_user.locale after en-US and empty: %q, want empty", got)
	}
	if got := stored("fresh"); got != "de" {
		t.Errorf("app_user.locale after empty without a choice: %q, want the column default de", got)
	}
	if code, out := call("back", http.MethodGet, ""); code != http.StatusOK || out["locale"] != "" {
		t.Errorf("GET after choosing the organization's locale: %d locale %#v, want empty", code, out["locale"])
	}

	// The notifications follow the language.
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err := center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	// A clock in the past outside the quiet hours: the run never claims
	// deliveries of other tests.
	now := time.Date(2020, 1, 7, 10, 0, 0, 0, time.UTC)
	mail := &recordingProvider{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return now })
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	for typ, data := range map[string]map[string]any{
		events.ReviewCreated:    {"id": "r-653", "kind": "probable_duplicate"},
		events.CollectorOffline: {"id": "c-653", "name": "dc1-collector", "last_seen_at": "2020-01-07T09:00:00Z"},
	} {
		raw, _ := json.Marshal(data)
		if err := consumer.Consume(bg, &events.Envelope{ID: f.ID(), Type: typ, OrgID: f.OrgA, OccurredAt: now, Data: raw}); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	// Review e-mails are bundled hourly by default, the in-app center is
	// not; collector notifications are critical and sent at once.
	for range 3 {
		if _, err := notifier.ProcessDue(bg); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{
		"en":    "New review item: probable_duplicate",
		"back":  "Neues Review-Item: probable_duplicate",
		"fresh": "Neues Review-Item: probable_duplicate",
	} {
		items, _, err := center.Inbox.List(bg, f.OrgA, users[name], false, 50, 0)
		if err != nil || countCategory(items, "review") != 1 {
			t.Fatalf("in-app reviews of %s: %d (%v), want 1", name, countCategory(items, "review"), err)
		}
		for _, it := range items {
			if it.Category == "review" && it.Subject != want {
				t.Errorf("in-app review of %s: %q, want %q (F-383)", name, it.Subject, want)
			}
		}
	}
	for name, want := range map[string]string{
		"en":    "Collector dc1-collector has not reported since 2020-01-07T09:00:00Z.",
		"back":  "Der Collector dc1-collector meldet sich seit 2020-01-07T09:00:00Z nicht mehr.",
		"fresh": "Der Collector dc1-collector meldet sich seit 2020-01-07T09:00:00Z nicht mehr.",
	} {
		var bodies []string
		for _, m := range mail.sent {
			if m.Category == "collector" && len(m.To) == 1 && m.To[0].Email == "scopetest-9a-"+name+"@scopetest.invalid" {
				bodies = append(bodies, m.Body)
			}
		}
		if len(bodies) != 1 || !strings.HasPrefix(bodies[0], want) {
			t.Errorf("collector e-mails to %s: %q, want one starting with %q (F-383)", name, bodies, want)
		}
	}
}
