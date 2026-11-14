package tr069

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// now is the clock of the fixture: CPE0001 informed 5 min, CPE0002 2 h
// and CPE0003 90 s before it.
var now = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

// fakeACS is a GenieACS NBI over TLS: GET /devices/ with query (exact
// matches on dotted paths, _tags as membership), skip and limit. It records
// every request.
type fakeACS struct {
	mu       sync.Mutex
	srv      *httptest.Server
	devices  []map[string]any
	requests []*http.Request
	nonGets  int
	status   int
	auth     string
}

func loadFixture(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "genieacs-devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	if err := json.Unmarshal(raw, &docs); err != nil {
		t.Fatal(err)
	}
	return docs
}

// generated returns n devices that informed a minute before now.
func generated(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		serial := fmt.Sprintf("GEN%05d", i)
		out[i] = map[string]any{
			"_id": "000000-Gen-" + serial, "_lastInform": now.Add(-time.Minute).Format(time.RFC3339Nano),
			"_deviceId": map[string]any{"_SerialNumber": serial, "_OUI": "000000", "_ProductClass": "Gen"},
		}
	}
	return out
}

func newFakeACS(t *testing.T, tlsServer bool, devices []map[string]any) *fakeACS {
	t.Helper()
	f := &fakeACS{devices: devices}
	h := http.HandlerFunc(f.serve)
	if tlsServer {
		f.srv = httptest.NewTLSServer(h)
	} else {
		f.srv = httptest.NewServer(h)
	}
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeACS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	if r.Method != http.MethodGet {
		f.nonGets++
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.auth != "" && r.Header.Get("Authorization") != f.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/devices/" {
		http.NotFound(w, r)
		return
	}
	var query map[string]string
	if err := json.Unmarshal([]byte(r.URL.Query().Get("query")), &query); err != nil {
		http.Error(w, "bad query", http.StatusBadRequest)
		return
	}
	skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var match []map[string]any
	for _, d := range f.devices {
		if matches(d, query) {
			match = append(match, d)
		}
	}
	if skip > len(match) {
		skip = len(match)
	}
	end := len(match)
	if limit > 0 && skip+limit < end {
		end = skip + limit
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(match[skip:end])
}

func matches(doc map[string]any, query map[string]string) bool {
	for path, want := range query {
		if path == "_tags" {
			tags, _ := doc["_tags"].([]any)
			found := false
			for _, tag := range tags {
				found = found || tag == want
			}
			if !found {
				return false
			}
			continue
		}
		var node any = doc
		for _, part := range strings.Split(path, ".") {
			m, _ := node.(map[string]any)
			node = m[part]
		}
		if node != want {
			return false
		}
	}
	return true
}

func (f *fakeACS) recorded() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

// trusting returns an executor whose TLS scope pins the certificate of the
// fake ACS.
func (f *fakeACS) trusting(t *testing.T) *Executor {
	t.Helper()
	pin := sha256.Sum256(f.srv.Certificate().RawSubjectPublicKeyInfo)
	_, network, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{TLSScopes: []plugins.TLSScope{{Network: network, PinsSHA256: [][]byte{pin[:]}}}, Now: func() time.Time { return now }}
}

func (f *fakeACS) spec(params string) probewire.ProbeSpec {
	var m map[string]any
	_ = json.Unmarshal([]byte(params), &m)
	if m == nil {
		m = map[string]any{}
	}
	if _, ok := m["acs_url"]; !ok {
		m["acs_url"] = f.srv.URL
	}
	if _, ok := m["inform_interval_s"]; !ok {
		m["inform_interval_s"] = 300
	}
	raw, _ := json.Marshal(m)
	return probewire.ProbeSpec{ID: "p-tr069", Type: Type, Target: "acs", IntervalMS: 900_000, TimeoutMS: 30_000,
		DownCount: 3, UpCount: 1, Epoch: 1, Params: raw}
}

var acsCreds = map[string]string{"username": "reticora", "password": "acs-secret"}

// execute validates the spec against the contract and runs the executor.
func execute(t *testing.T, e *Executor, s probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	if errs := probewire.Validate(&s); len(errs) > 0 {
		t.Fatalf("spec rejected: %v", errs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := e.Execute(ctx, s, creds)
	r.ProbeID = s.ID
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func want(t *testing.T, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // results are small
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("result = %s/%s (labels %v), want %s/%s", r.Status, r.Code, r.Labels, status, code)
	}
}

// TestTR069Pagination covers WP-1018 AK3: 250 devices in three pages of
// at most 100, only GET.
func TestTR069Pagination(t *testing.T) {
	f := newFakeACS(t, true, generated(250))
	r := execute(t, f.trusting(t), f.spec(`{}`), acsCreds)
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueDevices] != 250 || r.Values[ValueDevicesStale] != 0 {
		t.Fatalf("values devices = %v, stale = %v", r.Values[ValueDevices], r.Values[ValueDevicesStale])
	}
	reqs := f.recorded()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	for i, req := range reqs {
		q := req.URL.Query()
		if req.Method != http.MethodGet || q.Get("skip") != strconv.Itoa(i*100) || q.Get("limit") != "100" {
			t.Fatalf("request %d: %s %s", i, req.Method, req.URL)
		}
		if req.Header.Get("Authorization") == "" {
			t.Fatal("credential not sent over https")
		}
	}
	if _, ok := r.Values[ValueTruncated]; ok {
		t.Fatal("truncated below the limit")
	}
}

// TestTR069StaleInformIsDown covers WP-1018 AK5: a single device whose
// last inform is older than 3 × the interval is fail/inform_stale.
func TestTR069StaleInformIsDown(t *testing.T) {
	f := newFakeACS(t, true, loadFixture(t))
	r := execute(t, f.trusting(t), f.spec(`{"selector":{"serial":"CPE0002"}}`), acsCreds)
	want(t, r, probewire.StatusFail, probewire.WarnInformStale)
	if r.Labels[LabelSerial] != "CPE0002" {
		t.Fatalf("labels = %v", r.Labels)
	}
	age := r.Values[probewire.ValueKey(ValueLastInformAge, map[string]string{LabelSerial: "CPE0002", LabelDeviceID: "00259E-HG8245-CPE0002"})]
	if age != 7200 {
		t.Fatalf("last inform age = %v, want 7200", age)
	}
	// A stale_factor of 10 (50 min) still flags 2 h; a longer interval
	// does not.
	want(t, execute(t, f.trusting(t), f.spec(`{"selector":{"serial":"CPE0002"},"inform_interval_s":3600}`), acsCreds), probewire.StatusOK, "")
	// The fresh device alone is up.
	want(t, execute(t, f.trusting(t), f.spec(`{"selector":{"serial":"CPE0001"}}`), acsCreds), probewire.StatusOK, "")
}

// TestTR069MixedDevicesWarn covers WP-1018 AK5: one of several devices
// stale is ok/inform_stale (degraded), all stale fail/inform_stale.
func TestTR069MixedDevicesWarn(t *testing.T) {
	f := newFakeACS(t, true, loadFixture(t))
	r := execute(t, f.trusting(t), f.spec(`{"selector":{"tag":"berlin"}}`), acsCreds)
	want(t, r, probewire.StatusOK, probewire.WarnInformStale)
	if !probewire.IsWarnCode(r.Code) || r.Values[ValueDevices] != 2 || r.Values[ValueDevicesStale] != 1 {
		t.Fatalf("result = %+v", r)
	}
	e := f.trusting(t)
	e.Now = func() time.Time { return now.Add(time.Hour) }
	want(t, execute(t, e, f.spec(`{"selector":{"tag":"berlin"}}`), acsCreds), probewire.StatusFail, probewire.WarnInformStale)
}

// TestTR069ParameterThresholds covers WP-1018 AK5: numeric parameters as
// values, warn and crit per device, direction below.
func TestTR069ParameterThresholds(t *testing.T) {
	f := newFakeACS(t, true, loadFixture(t))
	e := f.trusting(t)
	free := `{"path":"Device.DeviceInfo.MemoryStatus.Free","warn":49152,"crit":2048,"direction":"below"}`
	// CPE0001 alone: 51200 free, above both levels.
	r := execute(t, e, f.spec(`{"selector":{"serial":"CPE0001"},"parameters":[`+free+`,{"path":"Device.DeviceInfo.UpTime"}]}`), acsCreds)
	want(t, r, probewire.StatusOK, "")
	if r.Values[probewire.ValueKey(ValueParameter, map[string]string{LabelSerial: "CPE0001", LabelDeviceID: "00259E-HG8245-CPE0001", LabelParameter: "Device.DeviceInfo.UpTime"})] != 86400 {
		t.Fatalf("values = %v", r.Values)
	}
	// CPE0003 alone: 1024 free is critical (down); its UpTime is a string.
	r = execute(t, e, f.spec(`{"selector":{"serial":"CPE0003"},"parameters":[`+free+`,{"path":"Device.DeviceInfo.UpTime","warn":100}]}`), acsCreds)
	want(t, r, probewire.StatusFail, probewire.WarnThresholdExceeded)
	if r.Values[probewire.ValueKey(ValueParameter, map[string]string{LabelSerial: "CPE0003", LabelDeviceID: "A0B1C2-FritzBox-CPE0003", LabelParameter: "Device.DeviceInfo.UpTime"})] != 120 {
		t.Fatalf("string value not read as number: %v", r.Values)
	}
	// UpTime 120 above warn 100 only: warning (degraded).
	r = execute(t, e, f.spec(`{"selector":{"serial":"CPE0003"},"parameters":[{"path":"Device.DeviceInfo.UpTime","warn":100,"crit":1000}]}`), acsCreds)
	want(t, r, probewire.StatusOK, probewire.WarnThresholdExceeded)
	// A critical device selected alone is down; among several it is
	// degraded (below).
	e.Now = func() time.Time { return now.Add(-30 * time.Second) }
	r = execute(t, e, f.spec(`{"selector":{"oui":"A0B1C2"},"parameters":[`+free+`]}`), acsCreds)
	want(t, r, probewire.StatusFail, probewire.WarnThresholdExceeded)
	r = execute(t, e, f.spec(`{"inform_interval_s":7200,"parameters":[`+free+`]}`), acsCreds)
	want(t, r, probewire.StatusOK, probewire.WarnThresholdExceeded)
	if r.Labels[LabelSerial] != "CPE0003" {
		t.Fatalf("labels = %v", r.Labels)
	}
}

// TestTR069DeviceLimit covers WP-1018 AK3: of 10,001 devices 10,000 are
// evaluated and the result is truncated.
func TestTR069DeviceLimit(t *testing.T) {
	f := newFakeACS(t, true, generated(MaxDevices+1))
	r := execute(t, f.trusting(t), f.spec(`{}`), acsCreds)
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueDevices] != MaxDevices || r.Values[ValueTruncated] != 1 {
		t.Fatalf("devices = %v, truncated = %v", r.Values[ValueDevices], r.Values[ValueTruncated])
	}
	if n := len(f.recorded()); n != MaxDevices/PageSize {
		t.Fatalf("requests = %d, want %d", n, MaxDevices/PageSize)
	}
}

// TestTR069SelectorBuildsQuery covers WP-1018 AK3: the query holds only
// the fields of the selector, special characters are escaped, the
// projection holds the fixed fields and the parameters.
func TestTR069SelectorBuildsQuery(t *testing.T) {
	f := newFakeACS(t, true, loadFixture(t))
	serial := `x"},{"$where":"1==1`
	s := f.spec(`{"parameters":[{"path":"Device.DeviceInfo.UpTime"}]}`)
	var m map[string]any
	_ = json.Unmarshal(s.Params, &m)
	m["selector"] = map[string]any{"serial": serial, "oui": "00259E", "product_class": `HG\8245`, "tag": "a&b=c"}
	s.Params, _ = json.Marshal(m)
	r := execute(t, f.trusting(t), s, acsCreds)
	want(t, r, probewire.StatusOK, probewire.WarnNoDevices)
	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	q := reqs[0].URL.Query()
	var query map[string]any
	if err := json.Unmarshal([]byte(q.Get("query")), &query); err != nil {
		t.Fatalf("query %q: %v", q.Get("query"), err)
	}
	wantQuery := map[string]any{"_deviceId._SerialNumber": serial, "_deviceId._OUI": "00259E", "_deviceId._ProductClass": `HG\8245`, "_tags": "a&b=c"}
	if len(query) != len(wantQuery) {
		t.Fatalf("query = %v", query)
	}
	for k, v := range wantQuery {
		if query[k] != v {
			t.Fatalf("query[%s] = %v, want %v", k, query[k], v)
		}
	}
	if got := q.Get("projection"); got != "_id,_lastInform,_deviceId,Device.DeviceInfo.UpTime" {
		t.Fatalf("projection = %q", got)
	}
	if got, err := selectorQuery(Selector{}); err != nil || got != "{}" {
		t.Fatalf("empty selector = %q, %v", got, err)
	}
	// A parameter path that is no TR-069 path is rejected by the contract.
	bad := f.spec(`{"parameters":[{"path":"Device.X&limit=1"}]}`)
	if errs := probewire.Validate(&bad); len(errs) == 0 || errs[0].Field != "params.parameters[0].path" {
		t.Fatalf("errors = %v", errs)
	}
}

// TestTR069AuthAndTLSAreUnknown covers WP-1018 AK6: 401/403 and an
// untrusted certificate are error (unknown); an ACS out of reach is fail.
func TestTR069AuthAndTLSAreUnknown(t *testing.T) {
	f := newFakeACS(t, true, loadFixture(t))
	f.auth = "Basic other"
	r := execute(t, f.trusting(t), f.spec(`{}`), acsCreds)
	want(t, r, probewire.StatusError, probewire.ReasonAuthFailed)
	f.auth, f.status = "", http.StatusForbidden
	want(t, execute(t, f.trusting(t), f.spec(`{}`), acsCreds), probewire.StatusError, probewire.ReasonAuthFailed)
	f.status = 0
	// No TLS scope: the certificate of the fake ACS is not trusted, no
	// request reaches it.
	before := len(f.recorded())
	r = execute(t, &Executor{Now: func() time.Time { return now }}, f.spec(`{}`), acsCreds)
	want(t, r, probewire.StatusError, probewire.ReasonTLS)
	if len(f.recorded()) != before {
		t.Fatal("a request reached the ACS despite the untrusted certificate")
	}
	f.status = http.StatusInternalServerError
	want(t, execute(t, f.trusting(t), f.spec(`{}`), acsCreds), probewire.StatusFail, probewire.ReasonStatusMismatch)
	s := f.spec(`{}`)
	f.srv.Close()
	r = execute(t, f.trusting(t), s, acsCreds)
	if r.Status != probewire.StatusFail || (r.Code != probewire.ReasonUnreachable && r.Code != probewire.ReasonTimeout) {
		t.Fatalf("closed ACS: %s/%s, want fail/unreachable", r.Status, r.Code)
	}
	for k, v := range r.Labels {
		if strings.Contains(k+v, "acs-secret") {
			t.Fatal("result carries the password")
		}
	}
}

// TestTR069NoCredentialsOverPlainHTTP covers WP-1018 AK4: with a
// credential an http URL is error/unsupported with the field label; no
// request is sent. Without a credential plain http is read.
func TestTR069NoCredentialsOverPlainHTTP(t *testing.T) {
	f := newFakeACS(t, false, loadFixture(t))
	e := &Executor{Now: func() time.Time { return now }}
	r := execute(t, e, f.spec(`{"selector":{"serial":"CPE0001"}}`), acsCreds)
	want(t, r, probewire.StatusError, probewire.ReasonUnsupported)
	if r.Labels[probe.LabelField] != "params.acs_url" {
		t.Fatalf("labels = %v", r.Labels)
	}
	if n := len(f.recorded()); n != 0 {
		t.Fatalf("%d requests over plain http with a credential", n)
	}
	r = execute(t, e, f.spec(`{"selector":{"serial":"CPE0001"}}`), map[string]string{"token": "acs-token"})
	want(t, r, probewire.StatusError, probewire.ReasonUnsupported)
	want(t, execute(t, e, f.spec(`{"selector":{"serial":"CPE0001"}}`), nil), probewire.StatusOK, "")
	if got := f.recorded()[0].Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization %q without credential", got)
	}
}

// TestTR069Limits covers WP-1018 AK4 and AK7: stale_factor 11 and 21
// parameters are field errors; the contract entry has the limits of E-154.
func TestTR069Limits(t *testing.T) {
	typ, ok := probewire.Lookup(Type)
	if !ok || !typ.Available() || typ.DefaultIntervalS != 900 || typ.MinIntervalS != 300 || typ.DefaultTimeoutS != 30 ||
		typ.MaxTimeoutS != 60 || typ.MaxParallelPerTarget != 1 || typ.MaxRatePerCollector != 5 || typ.MaxRows != MaxDevices {
		t.Fatalf("contract entry = %+v", typ)
	}
	if len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "acs" {
		t.Fatalf("credential namespaces = %v", typ.CredentialNamespaces)
	}
	f := newFakeACS(t, true, nil)
	params := make([]string, 21)
	for i := range params {
		params[i] = fmt.Sprintf(`{"path":"Device.X.%d"}`, i)
	}
	cases := []struct {
		name   string
		params string
		mod    func(*probewire.ProbeSpec)
		field  string
	}{
		{"stale_factor 11", `{"stale_factor":11}`, nil, "params.stale_factor"},
		{"21 parameters", `{"parameters":[` + strings.Join(params, ",") + `]}`, nil, "params.parameters"},
		{"no inform interval", `{"inform_interval_s":null}`, nil, "params.inform_interval_s"},
		{"ftp url", `{"acs_url":"ftp://acs.example.org"}`, nil, "params.acs_url"},
		{"url with user info", `{"acs_url":"https://u:p@acs.example.org"}`, nil, "params.acs_url"},
		{"duplicate parameter", `{"parameters":[{"path":"Device.A"},{"path":"Device.A"}]}`, nil, "params.parameters[1].path"},
		{"crit not beyond warn", `{"parameters":[{"path":"Device.A","warn":5,"crit":3}]}`, nil, "params.parameters[0].crit"},
		{"interval 299 s", `{}`, func(s *probewire.ProbeSpec) { s.IntervalMS = 299_000 }, "interval_s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := f.spec(tc.params)
			if tc.name == "no inform interval" {
				var m map[string]any
				_ = json.Unmarshal(s.Params, &m)
				delete(m, "inform_interval_s")
				s.Params, _ = json.Marshal(m)
			}
			if tc.mod != nil {
				tc.mod(&s)
			}
			errs := probewire.Validate(&s)
			found := false
			for _, e := range errs {
				found = found || e.Field == tc.field
			}
			if !found {
				t.Fatalf("errors = %v, want field %s", errs, tc.field)
			}
			// The registered executor is not called for a rejected spec.
			r := probe.Run(context.Background(), &s, acsCreds)
			want(t, r, probewire.StatusError, probewire.ReasonConfig)
		})
	}
	if n := len(f.recorded()); n != 0 {
		t.Fatalf("%d requests for rejected specs", n)
	}
	if _, ok := probe.Lookup(Type); !ok {
		t.Fatal("tr069 executor not registered")
	}
}

// TestTR069SharedSerialKeptApart covers WP-1018 AK5: two devices of
// different manufacturers with the same serial keep their own series
// (label device_id); the stale one is named by its device id.
func TestTR069SharedSerialKeptApart(t *testing.T) {
	devices := []Device{
		{ID: "00259E-HG8245-123", Serial: "123", LastInform: now.Add(-10000 * time.Second)},
		{ID: "A0B1C2-FritzBox-123", Serial: "123", LastInform: now.Add(-10 * time.Second)},
	}
	p := params{InformIntervalS: 300, StaleFactor: DefaultStaleFactor}
	r := evaluate(devices, &p, false, now)
	stale := probewire.ValueKey(ValueLastInformAge, map[string]string{LabelSerial: "123", LabelDeviceID: "00259E-HG8245-123"})
	fresh := probewire.ValueKey(ValueLastInformAge, map[string]string{LabelSerial: "123", LabelDeviceID: "A0B1C2-FritzBox-123"})
	if r.Values[stale] != 10000 || r.Values[fresh] != 10 || r.Values[ValueDevicesStale] != 1 {
		t.Fatalf("values = %v", r.Values)
	}
	if r.Code != probewire.WarnInformStale || r.Labels[LabelDeviceID] != "00259E-HG8245-123" {
		t.Fatalf("result = %s/%s %v", r.Status, r.Code, r.Labels)
	}
}
