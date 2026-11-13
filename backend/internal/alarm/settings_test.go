package alarm_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// The alarm settings of an organization (ALR-02, ALR-04, ALR-06, ALR-12,
// WP-1030) on the MemoryStore: bounds per field, the defaults without a
// row, the routes behind their permissions. The PostgreSQL cases are in
// settings_integration_test.go.

const settingsPath = "/api/v1/monitoring/alarm-settings"

// settingsMux mounts the settings routes behind their permissions.
func settingsMux(svc *alarm.Service) http.Handler {
	inner := chi.NewRouter()
	alarm.NewHandler(svc).RegisterRoutes(inner)
	mux := chi.NewRouter()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		mux.With(server.AuthorizeRoute(method, settingsPath)).Method(method, settingsPath, inner)
	}
	return mux
}

var settingsWriter = []identity.Permission{identity.PermMonitoringRead, identity.PermMonitoringWrite}

func TestAlarmSettingsBounds(t *testing.T) {
	svc, _ := memService()
	mux := settingsMux(svc)
	for _, c := range []struct {
		field    string
		min, max int
	}{
		{"settle_seconds", 0, 300},
		{"flap_transitions", 2, 100},
		{"flap_window_seconds", 60, 86400},
		{"flap_hold_seconds", 60, 86400},
		{"notify_rate_per_min", 1, 10000},
	} {
		for _, v := range []int{c.min, c.max} {
			body := `{"` + c.field + `":` + strconv.Itoa(v) + `}`
			if rec := call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, body); rec.Code != http.StatusOK ||
				!strings.Contains(rec.Body.String(), `"`+c.field+`":`+strconv.Itoa(v)) {
				t.Errorf("%s = %d (bound): %d %s, want 200", c.field, v, rec.Code, rec.Body.String())
			}
		}
		for _, v := range []int{c.min - 1, c.max + 1} {
			body := `{"` + c.field + `":` + strconv.Itoa(v) + `}`
			rec := call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, body)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"`+c.field+`"`) {
				t.Errorf("%s = %d (out of bounds): %d %s, want 422 with the field", c.field, v, rec.Code, rec.Body.String())
			}
		}
	}
	for _, mode := range []string{"excluded", "down", "up"} {
		if rec := call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, `{"unknown_mode":"`+mode+`"}`); rec.Code != http.StatusOK {
			t.Errorf("unknown_mode %s: %d %s", mode, rec.Code, rec.Body.String())
		}
	}
	for _, mode := range []string{"available", "", "EXCLUDED"} {
		rec := call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, `{"unknown_mode":"`+mode+`"}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"unknown_mode"`) {
			t.Errorf("unknown_mode %q: %d %s, want 422", mode, rec.Code, rec.Body.String())
		}
	}
	// Unknown members, a body that is no object (null among them, which
	// would reset every setting) and trailing data are 400 and change
	// nothing.
	if rec := call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, `{"settle_seconds":42}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT settle_seconds 42: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"settle":3}`, `[1]`, `{"settle_seconds":"30"}`, `null`, ` null `, ``, `{} {}`,
		`{} {"settle_seconds":999}`, `{}x`} {
		if rec := call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: %d, want 400", body, rec.Code)
		}
	}
	if rec := call(t, mux, memOrg, settingsWriter, http.MethodGet, settingsPath, ""); !strings.Contains(rec.Body.String(), `"settle_seconds":42`) {
		t.Errorf("a refused PUT changed the settings: %s", rec.Body.String())
	}
	// Without monitoring:write the PUT is 403, the GET reads.
	reader := []identity.Permission{identity.PermMonitoringRead}
	if rec := call(t, mux, memOrg, reader, http.MethodPut, settingsPath, `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("PUT without monitoring:write: %d, want 403", rec.Code)
	}
	if rec := call(t, mux, memOrg, reader, http.MethodGet, settingsPath, ""); rec.Code != http.StatusOK {
		t.Errorf("GET with monitoring:read: %d, want 200", rec.Code)
	}
	// The domain check is the same.
	s := alarm.Defaults()
	s.FlapTransitions = 1
	if err := s.Validate(); err == nil {
		t.Error("Validate accepts flap_transitions 1")
	}
}

func TestAlarmSettingsDefaultsWithoutRow(t *testing.T) {
	svc, _ := memService()
	mux := settingsMux(svc)
	rec := call(t, mux, memOrg, settingsWriter, http.MethodGet, settingsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var got alarm.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := alarm.Defaults()
	if got != want || got.UnknownMode != alarm.UnknownExcluded || got.SettleSeconds != 30 || got.FlapTransitions != 5 ||
		got.FlapWindowSeconds != 900 || got.FlapHoldSeconds != 900 || got.NotifyRatePerMin != 60 || !got.NotifyAfterMaintenance {
		t.Errorf("defaults %+v, want %+v", got, want)
	}
	if !strings.Contains(rec.Body.String(), `"updated_at":null`) || !strings.Contains(rec.Body.String(), `"updated_by":null`) {
		t.Errorf("defaults name an update: %s", rec.Body.String())
	}
	// A PUT with some members takes the defaults for the others; another
	// organization keeps its defaults.
	rec = call(t, mux, memOrg, settingsWriter, http.MethodPut, settingsPath, `{"settle_seconds":120,"unknown_mode":"down"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"settle_seconds":120`) ||
		!strings.Contains(rec.Body.String(), `"flap_transitions":5`) || !strings.Contains(rec.Body.String(), `"updated_by":"`+memUser+`"`) {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, mux, otherOrg, settingsWriter, http.MethodGet, settingsPath, "")
	if !strings.Contains(rec.Body.String(), `"settle_seconds":30`) {
		t.Errorf("other organization: %s", rec.Body.String())
	}
}
