package redfish

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	rfplugin "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/redfish"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// bmc is a Redfish service over TLS (httptest) with one system and one
// chassis serving the older Thermal and Power resources; health, readings
// and thresholds are changed per test.
type bmc struct {
	mu           sync.Mutex
	srv          *httptest.Server
	health       string
	psu2         string
	temp         float64
	nonCrit      float64
	crit         float64
	user, secret string
	nonGets      int
}

func newBMC(t *testing.T) *bmc {
	t.Helper()
	b := &bmc{health: "OK", psu2: "OK", temp: 45, nonCrit: 80, crit: 90, user: "monitor", secret: "s3cret"}
	b.srv = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *bmc) set(f func(*bmc)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b)
}

func (b *bmc) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Method != http.MethodGet {
		b.nonGets++
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if u, p, ok := r.BasicAuth(); !ok || u != b.user || p != b.secret {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body string
	switch r.URL.Path {
	case "/redfish/v1/":
		body = `{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"}}`
	case "/redfish/v1/Systems":
		body = `{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`
	case "/redfish/v1/Systems/1":
		body = fmt.Sprintf(`{"Id":"1","PowerState":"On","Status":{"Health":%q}}`, b.health)
	case "/redfish/v1/Chassis":
		body = `{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`
	case "/redfish/v1/Chassis/1":
		body = `{"Id":"1","Status":{"Health":"OK"},"Thermal":{"@odata.id":"/redfish/v1/Chassis/1/Thermal"},"Power":{"@odata.id":"/redfish/v1/Chassis/1/Power"}}`
	case "/redfish/v1/Chassis/1/Thermal":
		body = fmt.Sprintf(`{"Temperatures":[{"Name":"CPU1","ReadingCelsius":%g,"UpperThresholdNonCritical":%g,"UpperThresholdCritical":%g,"Status":{"Health":"OK"}}],"Fans":[{"Name":"Fan1","Reading":5400,"ReadingUnits":"RPM","Status":{"Health":"OK"}}]}`,
			b.temp, b.nonCrit, b.crit)
	case "/redfish/v1/Chassis/1/Power":
		body = fmt.Sprintf(`{"PowerSupplies":[{"Name":"PSU1","PowerInputWatts":230,"Status":{"Health":"OK"}},{"Name":"PSU2","PowerInputWatts":0,"Status":{"Health":%q}}]}`, b.psu2)
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (b *bmc) port() int { return b.srv.Listener.Addr().(*net.TCPAddr).Port }

// trusting returns a plugin whose TLS scope pins the certificate of the
// test service, as RETICORA_REDFISH_TLS_SCOPES would.
func (b *bmc) trusting(t *testing.T) *rfplugin.Plugin {
	t.Helper()
	pin := sha256.Sum256(b.srv.Certificate().RawSubjectPublicKeyInfo)
	_, network, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	return rfplugin.New(rfplugin.WithTLSScopes(rfplugin.TLSScope{Network: network, PinsSHA256: [][]byte{pin[:]}}))
}

func (b *bmc) spec(params string) probewire.ProbeSpec {
	if params == "" {
		params = "{}"
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(params), &m)
	m["port"] = b.port()
	raw, _ := json.Marshal(m)
	return probewire.ProbeSpec{
		ID: "p-redfish", Type: Type, Target: "127.0.0.1", IntervalMS: 60_000, TimeoutMS: 15_000,
		DownCount: 3, UpCount: 1, Epoch: 1, Params: raw,
	}
}

var bmcCreds = map[string]string{"username": "monitor", "password": "s3cret"}

// run executes one check through probe.Run (contract validation first)
// with the plugin pl in place of the configured one.
func run(t *testing.T, pl *rfplugin.Plugin, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // a copy of the spec per check
	t.Helper()
	prev := plugin.Load()
	Configure(pl)
	t.Cleanup(func() { plugin.Store(prev) })
	r := probe.Run(context.Background(), &spec, creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	for k := range r.Values {
		if _, _, err := probewire.ParseValueKey(k); err != nil {
			t.Fatalf("value key %q: %v", k, err)
		}
	}
	return r
}

func wantResult(t *testing.T, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // results are small
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("result = %s/%s (labels %v), want %s/%s", r.Status, r.Code, r.Labels, status, code)
	}
}

// TestRedfishOK covers WP-998 AK3 and AK6: every sensor, fan and power
// supply is a value with its label; that the metric bridge turns them into
// samples with the label sensor/fan/psu and the ci_id of the probe (WP-976)
// is covered by TestBridgeRedfishSensorsCarryCIID in
// backend/internal/probe/metrics_bridge_test.go.
func TestRedfishOK(t *testing.T) {
	b := newBMC(t)
	r := run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusOK, "")
	want := map[string]float64{
		ValueHealth: 0,
		probewire.ValueKey(ValueTemperature, map[string]string{LabelSensor: "1/CPU1"}): 45,
		probewire.ValueKey(ValueFanRPM, map[string]string{LabelFan: "1/Fan1"}):         5400,
		probewire.ValueKey(ValuePSUWatts, map[string]string{LabelPSU: "1/PSU1"}):       230,
		probewire.ValueKey(ValuePSUState, map[string]string{LabelPSU: "1/PSU1"}):       0,
		probewire.ValueKey(ValuePSUState, map[string]string{LabelPSU: "1/PSU2"}):       0,
	}
	for k, v := range want {
		if got, ok := r.Values[k]; !ok || got != v {
			t.Errorf("value %s = %v (%v), want %v", k, got, ok, v)
		}
	}
	if r.RTTMS == nil {
		t.Error("no rtt_ms")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.nonGets != 0 {
		t.Fatalf("%d requests other than GET", b.nonGets)
	}
}

// TestRedfishWarningDegraded covers WP-998 AK4: health or a power supply
// Warning is ok with the warn code check_failed (degraded via WP-952).
func TestRedfishWarningDegraded(t *testing.T) {
	b := newBMC(t)
	b.set(func(b *bmc) { b.health = "Warning" })
	r := run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Values[ValueHealth] != 1 {
		t.Fatalf("health value = %v, want 1", r.Values[ValueHealth])
	}
	if !probewire.IsWarnCode(r.Code) {
		t.Fatal("check_failed is no warn code")
	}
	b.set(func(b *bmc) { b.health, b.psu2 = "OK", "Warning" })
	r = run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Labels[LabelPSU] != "1/PSU2" {
		t.Fatalf("labels = %v, want psu 1/PSU2", r.Labels)
	}
}

// TestRedfishCriticalDown covers WP-998 AK4: health or a power supply
// Critical is fail with check_failed.
func TestRedfishCriticalDown(t *testing.T) {
	b := newBMC(t)
	b.set(func(b *bmc) { b.health = "Critical" })
	r := run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Values[ValueHealth] != 2 {
		t.Fatalf("health value = %v, want 2", r.Values[ValueHealth])
	}
	b.set(func(b *bmc) { b.health, b.psu2 = "OK", "Critical" })
	r = run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if got := r.Values[probewire.ValueKey(ValuePSUState, map[string]string{LabelPSU: "1/PSU2"})]; got != 2 {
		t.Fatalf("psu state = %v, want 2", got)
	}
}

// TestRedfishDeviceThreshold covers WP-998 AK4: the thresholds of the
// device decide without parameters.
func TestRedfishDeviceThreshold(t *testing.T) {
	b := newBMC(t)
	b.set(func(b *bmc) { b.temp = 85 })
	r := run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusOK, probewire.WarnThresholdExceeded)
	if r.Labels[LabelSensor] != "1/CPU1" {
		t.Fatalf("labels = %v", r.Labels)
	}
	b.set(func(b *bmc) { b.temp = 95 })
	r = run(t, b.trusting(t), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusFail, probewire.WarnThresholdExceeded)
}

// TestRedfishParameterThresholdOverrides covers WP-998 AK4 and E-186:
// temp_warn and temp_crit replace the thresholds of the device, the
// thresholds object of the probe before params.
func TestRedfishParameterThresholdOverrides(t *testing.T) {
	b := newBMC(t)
	pl := b.trusting(t)
	// 45 °C: below the device thresholds, above temp_warn 40.
	wantResult(t, run(t, pl, b.spec(`{"temp_warn":40}`), bmcCreds), probewire.StatusOK, probewire.WarnThresholdExceeded)
	wantResult(t, run(t, pl, b.spec(`{"temp_warn":40,"temp_crit":44}`), bmcCreds), probewire.StatusFail, probewire.WarnThresholdExceeded)
	// 95 °C: above the device's critical 90, below temp_crit 100, above
	// the device's non-critical 80.
	b.set(func(b *bmc) { b.temp = 95 })
	wantResult(t, run(t, pl, b.spec(`{"temp_crit":100}`), bmcCreds), probewire.StatusOK, probewire.WarnThresholdExceeded)
	wantResult(t, run(t, pl, b.spec(`{"temp_warn":96,"temp_crit":100}`), bmcCreds), probewire.StatusOK, "")
	// The thresholds object overrides params.
	spec := b.spec(`{"temp_warn":96,"temp_crit":100}`)
	spec.Thresholds = json.RawMessage(`{"temp_crit":97}`)
	wantResult(t, run(t, pl, spec, bmcCreds), probewire.StatusOK, "")
	spec.Thresholds = json.RawMessage(`{"temp_warn":90,"temp_crit":94}`)
	wantResult(t, run(t, pl, spec, bmcCreds), probewire.StatusFail, probewire.WarnThresholdExceeded)
}

// TestRedfishAuthFailedUnknown covers WP-998 AK4 and E-177: a refused
// credential and an untrusted certificate are error (unknown), never fail.
func TestRedfishAuthFailedUnknown(t *testing.T) {
	b := newBMC(t)
	r := run(t, b.trusting(t), b.spec(""), map[string]string{"username": "monitor", "password": "wrong-pass"})
	wantResult(t, r, probewire.StatusError, probewire.ReasonAuthFailed)
	for k, v := range r.Labels {
		if strings.Contains(k+v, "wrong-pass") {
			t.Fatal("result carries the password")
		}
	}
	// No TLS scope: the self-signed certificate of the service is not
	// trusted, the credential is not sent.
	r = run(t, rfplugin.New(), b.spec(""), bmcCreds)
	wantResult(t, r, probewire.StatusError, probewire.ReasonTLSUntrusted)
	// A closed port is a failed check.
	closed := b.spec("")
	b.srv.Close()
	r = run(t, b.trusting(t), closed, bmcCreds)
	if r.Status != probewire.StatusFail {
		t.Fatalf("closed port: %s/%s, want fail", r.Status, r.Code)
	}
}

// TestRedfishLimits covers WP-998 AK2 and AK5: interval 59 s and timeout
// 61 s are rejected by the contract, as are a resource id with a path and a
// critical temperature not above the warning one.
func TestRedfishLimits(t *testing.T) {
	typ, ok := probewire.Lookup(Type)
	if !ok || !typ.Available() || typ.MinIntervalS != 60 || typ.MaxTimeoutS != 60 || typ.MaxPayloadBytes != rfplugin.MaxBodyBytes {
		t.Fatalf("contract entry = %+v", typ)
	}
	if len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "redfish" {
		t.Fatalf("credential namespaces = %v", typ.CredentialNamespaces)
	}
	b := newBMC(t)
	cases := []struct {
		name  string
		mod   func(*probewire.ProbeSpec)
		field string
	}{
		{"interval 59 s", func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }, "interval_s"},
		{"timeout 61 s", func(s *probewire.ProbeSpec) { s.IntervalMS, s.TimeoutMS = 120_000, 61_000 }, "timeout_s"},
		{"system id with a path", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"system_id":"../Managers"}`) }, "params.system_id"},
		{"chassis id with a query", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"chassis_ids":["1?x=1"]}`) }, "params.chassis_ids[0]"},
		{"temp_crit not above temp_warn", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"temp_warn":80,"temp_crit":80}`) }, "params.temp_crit"},
		{"unknown parameter", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"resources":["/x"]}`) }, "params.resources"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := b.spec("")
			tc.mod(&spec)
			errs := probewire.Validate(&spec)
			found := false
			for _, e := range errs {
				found = found || e.Field == tc.field
			}
			if !found {
				t.Fatalf("errors = %v, want field %s", errs, tc.field)
			}
			r := run(t, b.trusting(t), spec, bmcCreds)
			wantResult(t, r, probewire.StatusError, probewire.ReasonConfig)
		})
	}
	if errs := probewire.Validate(ptr(b.spec(`{"system_id":"System.Embedded.1","chassis_ids":["1"],"temp_warn":70,"temp_crit":85}`))); len(errs) > 0 {
		t.Fatalf("valid params rejected: %v", errs)
	}
	// The executor is registered.
	if _, ok := probe.Lookup(Type); !ok {
		t.Fatal("redfish executor not registered")
	}
}

func ptr[T any](v T) *T { return &v }

// TestRedfishTruncatedIsWarning covers WP-998 AK2: a poll ended by the
// request budget is ok with a warn code, not a plain ok.
func TestRedfishTruncatedIsWarning(t *testing.T) {
	r := evaluate(&rfplugin.Health{Truncated: true}, &params{})
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnCheckFailed || r.Labels[LabelTruncated] != "true" || r.Values[ValueTruncated] != 1 {
		t.Fatalf("result = %+v", r)
	}
}

// TestRedfishForeignLinkIsUnsupported: a link of the device that leaves
// the target is a statement about the device (error/unsupported), not a
// parameter error.
func TestRedfishForeignLinkIsUnsupported(t *testing.T) {
	r := failure(context.Background(), fmt.Errorf("redfish: system: %w", rfplugin.ErrForeignLink))
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("result = %s/%s", r.Status, r.Code)
	}
	r = failure(context.Background(), rfplugin.ErrInvalidOption)
	if r.Code != probewire.ReasonConfig {
		t.Fatalf("invalid option: %s/%s", r.Status, r.Code)
	}
}
