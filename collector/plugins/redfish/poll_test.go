package redfish

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// bmcResources maps the paths of a fake Redfish service to JSON bodies.
type bmcResources map[string]string

// legacyBMC is a service with one system and one chassis that links the
// older Thermal and Power resources.
func legacyBMC() bmcResources {
	return bmcResources{
		"/redfish/v1/":                  `{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"}}`,
		"/redfish/v1/Systems":           `{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`,
		"/redfish/v1/Systems/1":         `{"Id":"1","PowerState":"On","Status":{"State":"Enabled","Health":"OK"}}`,
		"/redfish/v1/Chassis":           `{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`,
		"/redfish/v1/Chassis/1":         `{"Id":"1","Status":{"Health":"OK"},"Thermal":{"@odata.id":"/redfish/v1/Chassis/1/Thermal"},"Power":{"@odata.id":"/redfish/v1/Chassis/1/Power"}}`,
		"/redfish/v1/Chassis/1/Thermal": `{"Temperatures":[{"Name":"CPU1 Temp","ReadingCelsius":45,"UpperThresholdNonCritical":80,"UpperThresholdCritical":90,"Status":{"State":"Enabled","Health":"OK"}},{"Name":"Absent Temp","ReadingCelsius":0,"Status":{"State":"Absent"}}],"Fans":[{"Name":"Fan1","Reading":5400,"ReadingUnits":"RPM","Status":{"Health":"OK"}}]}`,
		"/redfish/v1/Chassis/1/Power":   `{"PowerSupplies":[{"Name":"PSU1","PowerInputWatts":231.5,"Status":{"State":"Enabled","Health":"OK"}},{"Name":"PSU2","PowerInputWatts":0,"Status":{"State":"Enabled","Health":"Critical"}}]}`,
	}
}

// subsystemBMC is a service whose chassis links ThermalSubsystem and
// PowerSubsystem (Redfish 2020.4 and later).
func subsystemBMC() bmcResources {
	return bmcResources{
		"/redfish/v1/":                                                   `{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"}}`,
		"/redfish/v1/Systems":                                            `{"Members":[{"@odata.id":"/redfish/v1/Systems/sys"}]}`,
		"/redfish/v1/Systems/sys":                                        `{"Id":"sys","PowerState":"On","Status":{"Health":"Warning"}}`,
		"/redfish/v1/Chassis":                                            `{"Members":[{"@odata.id":"/redfish/v1/Chassis/c1"}]}`,
		"/redfish/v1/Chassis/c1":                                         `{"Id":"c1","Status":{"Health":"OK"},"ThermalSubsystem":{"@odata.id":"/redfish/v1/Chassis/c1/ThermalSubsystem"},"PowerSubsystem":{"@odata.id":"/redfish/v1/Chassis/c1/PowerSubsystem"}}`,
		"/redfish/v1/Chassis/c1/ThermalSubsystem":                        `{"ThermalMetrics":{"@odata.id":"/redfish/v1/Chassis/c1/ThermalSubsystem/ThermalMetrics"},"Fans":{"@odata.id":"/redfish/v1/Chassis/c1/ThermalSubsystem/Fans"}}`,
		"/redfish/v1/Chassis/c1/ThermalSubsystem/ThermalMetrics":         `{"TemperatureReadingsCelsius":[{"DeviceName":"Inlet","DataSourceUri":"/redfish/v1/Chassis/c1/Sensors/Inlet","Reading":24.5}]}`,
		"/redfish/v1/Chassis/c1/ThermalSubsystem/Fans":                   `{"Members":[{"@odata.id":"/redfish/v1/Chassis/c1/ThermalSubsystem/Fans/F1"}]}`,
		"/redfish/v1/Chassis/c1/ThermalSubsystem/Fans/F1":                `{"Id":"F1","Status":{"Health":"OK"},"SpeedPercent":{"SpeedRPM":7200}}`,
		"/redfish/v1/Chassis/c1/PowerSubsystem":                          `{"PowerSupplies":{"@odata.id":"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies"}}`,
		"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies":            `{"Members":[{"@odata.id":"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies/P1"}]}`,
		"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies/P1":         `{"Id":"P1","Status":{"State":"Enabled","Health":"Warning"},"Metrics":{"@odata.id":"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies/P1/Metrics"}}`,
		"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies/P1/Metrics": `{"InputPowerWatts":{"Reading":412}}`,
	}
}

// fakeRedfish serves resources and counts requests; requests that are not
// GET are counted separately and refused.
type fakeRedfish struct {
	srv     *httptest.Server
	gets    atomic.Int64
	nonGets atomic.Int64
}

func newFakeRedfish(t *testing.T, res bmcResources, user, pass string) *fakeRedfish {
	t.Helper()
	f := &fakeRedfish{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			f.nonGets.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.gets.Add(1)
		if u, p, ok := r.BasicAuth(); user != "" && (!ok || u != user || p != pass) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := res[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

var bmcCreds = map[string]string{"username": "monitor", "password": "s3cret"}

func pollFake(t *testing.T, f *fakeRedfish, creds map[string]string, opts PollOptions) (*Health, error) {
	t.Helper()
	return newTestPlugin(t, f.srv).PollHealth(context.Background(), "192.0.2.10", creds, opts)
}

// TestPollHealthLegacyThermalPower covers WP-998 AK1 with Thermal and
// Power.
func TestPollHealthLegacyThermalPower(t *testing.T) {
	f := newFakeRedfish(t, legacyBMC(), "monitor", "s3cret")
	h, err := pollFake(t, f, bmcCreds, PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Health != HealthOK || h.PowerState != "On" || h.Truncated {
		t.Fatalf("health = %+v", h)
	}
	if len(h.Temperatures) != 1 {
		t.Fatalf("temperatures = %+v, want the present sensor only", h.Temperatures)
	}
	tmp := h.Temperatures[0]
	if tmp.Name != "1/CPU1 Temp" || tmp.Celsius != 45 || tmp.UpperNonCritical == nil || *tmp.UpperNonCritical != 80 || tmp.UpperCritical == nil || *tmp.UpperCritical != 90 {
		t.Fatalf("temperature = %+v", tmp)
	}
	if len(h.Fans) != 1 || h.Fans[0].RPM == nil || *h.Fans[0].RPM != 5400 {
		t.Fatalf("fans = %+v", h.Fans)
	}
	if len(h.PowerSupplies) != 2 || *h.PowerSupplies[0].InputWatts != 231.5 || h.PowerSupplies[1].Health != HealthCritical {
		t.Fatalf("power supplies = %+v", h.PowerSupplies)
	}
	if h.Requests != 7 {
		t.Fatalf("requests = %d, want 7", h.Requests)
	}
}

// TestPollHealthSubsystems covers WP-998 AK1 with ThermalSubsystem and
// PowerSubsystem on a chassis without the older resources.
func TestPollHealthSubsystems(t *testing.T) {
	f := newFakeRedfish(t, subsystemBMC(), "", "")
	h, err := pollFake(t, f, nil, PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Health != HealthWarning {
		t.Fatalf("health = %v, want warning from the system", h.Health)
	}
	if len(h.Temperatures) != 1 || h.Temperatures[0].Name != "c1/Inlet" || h.Temperatures[0].Celsius != 24.5 {
		t.Fatalf("temperatures = %+v", h.Temperatures)
	}
	if len(h.Fans) != 1 || h.Fans[0].Name != "c1/F1" || *h.Fans[0].RPM != 7200 {
		t.Fatalf("fans = %+v", h.Fans)
	}
	if len(h.PowerSupplies) != 1 || h.PowerSupplies[0].Health != HealthWarning || *h.PowerSupplies[0].InputWatts != 412 {
		t.Fatalf("power supplies = %+v", h.PowerSupplies)
	}
}

// TestPollHealthUnauthorizedIsAuthFailed covers WP-998 AK2: 401 and 403
// are plugins.ErrAuthFailed; the error carries no credential.
func TestPollHealthUnauthorizedIsAuthFailed(t *testing.T) {
	f := newFakeRedfish(t, legacyBMC(), "monitor", "s3cret")
	_, err := pollFake(t, f, map[string]string{"username": "monitor", "password": "wrong-pass"}, PollOptions{})
	if !errors.Is(err, plugins.ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	if strings.Contains(err.Error(), "wrong-pass") {
		t.Fatal("error carries the password")
	}
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()
	_, err = newTestPlugin(t, forbidden).PollHealth(context.Background(), "192.0.2.10", bmcCreds, PollOptions{})
	if !errors.Is(err, plugins.ErrAuthFailed) {
		t.Fatalf("403: err = %v, want ErrAuthFailed", err)
	}
}

// TestPollHealthBodyLimit covers WP-998 AK2: a resource of 1 MiB + 1 byte
// is discarded with ErrBodyTooLarge.
func TestPollHealthBodyLimit(t *testing.T) {
	res := legacyBMC()
	// Valid JSON padded to one byte over the limit.
	head := `{"Id":"1","PowerState":"On","Pad":"`
	pad := strings.Repeat("x", MaxBodyBytes+1-len(head)-2)
	res["/redfish/v1/Systems/1"] = head + pad + `"}`
	if len(res["/redfish/v1/Systems/1"]) != MaxBodyBytes+1 {
		t.Fatalf("fixture length %d", len(res["/redfish/v1/Systems/1"]))
	}
	f := newFakeRedfish(t, res, "", "")
	if _, err := pollFake(t, f, nil, PollOptions{}); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	// Exactly 1 MiB is accepted.
	res["/redfish/v1/Systems/1"] = head + pad[1:] + `"}`
	f = newFakeRedfish(t, res, "", "")
	if _, err := pollFake(t, f, nil, PollOptions{}); err != nil {
		t.Fatalf("1 MiB body: %v", err)
	}
}

// TestPollHealthResourceLimit covers WP-998 AK2: the 21st request is not
// sent; the poll returns what it read with Truncated.
func TestPollHealthResourceLimit(t *testing.T) {
	res := legacyBMC()
	var members []string
	for i := range 30 {
		id := fmt.Sprintf("c%d", i)
		members = append(members, fmt.Sprintf(`{"@odata.id":"/redfish/v1/Chassis/%s"}`, id))
		res["/redfish/v1/Chassis/"+id] = fmt.Sprintf(`{"Id":%q,"Status":{"Health":"OK"}}`, id)
	}
	res["/redfish/v1/Chassis"] = `{"Members":[` + strings.Join(members, ",") + `]}`
	f := newFakeRedfish(t, res, "", "")
	h, err := pollFake(t, f, nil, PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !h.Truncated || h.Requests != DefaultMaxRequests {
		t.Fatalf("truncated = %v, requests = %d", h.Truncated, h.Requests)
	}
	if got := f.gets.Load(); got != DefaultMaxRequests {
		t.Fatalf("server saw %d requests, want %d", got, DefaultMaxRequests)
	}
}

// TestPollHealthGetOnly covers WP-998 AK1: the poll sends GET requests
// only, and no request leaves the target through a crafted link.
func TestPollHealthGetOnly(t *testing.T) {
	for name, res := range map[string]bmcResources{"legacy": legacyBMC(), "subsystems": subsystemBMC()} {
		f := newFakeRedfish(t, res, "", "")
		if _, err := pollFake(t, f, nil, PollOptions{}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n := f.nonGets.Load(); n != 0 {
			t.Fatalf("%s: %d requests other than GET", name, n)
		}
		if f.gets.Load() == 0 {
			t.Fatalf("%s: no request", name)
		}
	}
	res := legacyBMC()
	res["/redfish/v1/Systems"] = `{"Members":[{"@odata.id":"@evil.example/x"}]}`
	f := newFakeRedfish(t, res, "", "")
	if _, err := pollFake(t, f, bmcCreds, PollOptions{}); !errors.Is(err, ErrForeignLink) || errors.Is(err, ErrInvalidOption) {
		t.Fatalf("crafted link: err = %v, want ErrForeignLink", err)
	}
	if _, err := pollFake(t, f, bmcCreds, PollOptions{SystemID: "../Managers"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("system id with path: err = %v", err)
	}
}

// TestPollHealthPrefersLegacyThermalThresholds covers WP-998 AK1 and AK4:
// a chassis that links both ThermalSubsystem and Thermal is read through
// Thermal, which carries the thresholds of the device.
func TestPollHealthPrefersLegacyThermalThresholds(t *testing.T) {
	res := subsystemBMC()
	res["/redfish/v1/Chassis/c1"] = `{"Id":"c1","Status":{"Health":"OK"},"ThermalSubsystem":{"@odata.id":"/redfish/v1/Chassis/c1/ThermalSubsystem"},"Thermal":{"@odata.id":"/redfish/v1/Chassis/c1/Thermal"},"PowerSubsystem":{"@odata.id":"/redfish/v1/Chassis/c1/PowerSubsystem"}}`
	res["/redfish/v1/Chassis/c1/Thermal"] = `{"Temperatures":[{"Name":"Inlet","ReadingCelsius":95,"UpperThresholdNonCritical":80,"UpperThresholdCritical":90,"Status":{"Health":"OK"}}]}`
	f := newFakeRedfish(t, res, "", "")
	h, err := pollFake(t, f, nil, PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Temperatures) != 1 {
		t.Fatalf("temperatures = %+v", h.Temperatures)
	}
	tmp := h.Temperatures[0]
	if tmp.Name != "c1/Inlet" || tmp.Celsius != 95 || tmp.UpperCritical == nil || *tmp.UpperCritical != 90 {
		t.Fatalf("temperature = %+v, want the reading with the device threshold", tmp)
	}
}

// TestPollHealthManyFansCriticalPSU covers WP-998 AK2 and AK4: on the
// subsystem path every fan costs a request; the power supplies are read
// first, so a critical one is reported even when the fans exhaust the
// budget.
func TestPollHealthManyFansCriticalPSU(t *testing.T) {
	res := subsystemBMC()
	var fans []string
	for i := range 12 {
		id := fmt.Sprintf("F%d", i)
		fans = append(fans, fmt.Sprintf(`{"@odata.id":"/redfish/v1/Chassis/c1/ThermalSubsystem/Fans/%s"}`, id))
		res["/redfish/v1/Chassis/c1/ThermalSubsystem/Fans/"+id] = fmt.Sprintf(`{"Id":%q,"Status":{"Health":"OK"},"SpeedPercent":{"SpeedRPM":7000}}`, id)
	}
	res["/redfish/v1/Chassis/c1/ThermalSubsystem/Fans"] = `{"Members":[` + strings.Join(fans, ",") + `]}`
	res["/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies/P1"] = `{"Id":"P1","Status":{"State":"Enabled","Health":"Critical"},"Metrics":{"@odata.id":"/redfish/v1/Chassis/c1/PowerSubsystem/PowerSupplies/P1/Metrics"}}`
	f := newFakeRedfish(t, res, "", "")
	h, err := pollFake(t, f, nil, PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !h.Truncated {
		t.Fatalf("12 fans fit into the budget (%d requests); the fixture is too small", h.Requests)
	}
	if len(h.PowerSupplies) != 1 || h.PowerSupplies[0].Health != HealthCritical {
		t.Fatalf("power supplies = %+v, want the critical supply despite the truncated poll", h.PowerSupplies)
	}
}
