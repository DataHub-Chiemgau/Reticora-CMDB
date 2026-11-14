package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakegnmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
	"google.golang.org/grpc/codes"
)

const gnmiIfTree = `{"interfaces": {
	"interface[name=et-0/0/1]": {"state": {"oper-status": "UP", "counters": {"in-octets": 1000, "out-octets": 2000}}},
	"interface[name=et-0/0/2]": {"state": {"oper-status": "UP", "counters": {"in-octets": 10, "out-octets": 20}}}
}, "components": {"component[name=cpu0]": {"state": {"temperature": {"instant": 75}}}}}`

func startGNMI(t *testing.T, cfg *fakegnmi.Config) *fakegnmi.Server {
	t.Helper()
	s, err := fakegnmi.Start(cfg)
	if err != nil {
		t.Skipf("fakegnmi: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func gnmiPort(t *testing.T, s *fakegnmi.Server) int {
	t.Helper()
	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_, _ = fmt.Sscan(port, &n)
	return n
}

func gnmiSpec(t *testing.T, s *fakegnmi.Server, params map[string]any) probewire.ProbeSpec {
	t.Helper()
	p := map[string]any{"port": gnmiPort(t, s)}
	for k, v := range params {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	return probewire.ProbeSpec{ID: "gnmi-1", Type: TypeGNMI, Target: "127.0.0.1", IntervalMS: 60_000, TimeoutMS: 5_000,
		DownCount: 3, UpCount: 1, Params: raw, Epoch: 1}
}

func gnmiCreds(s *fakegnmi.Server) map[string]string {
	return map[string]string{"ca": string(s.CAPEM())}
}

func runGNMI(ctx context.Context, t *testing.T, e *GNMIExecutor, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // test helper
	t.Helper()
	if errs := probewire.Validate(&spec); len(errs) > 0 {
		t.Fatalf("spec rejected: %v", errs)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutMS)*time.Millisecond)
	defer cancel()
	r := e.Execute(ctx, spec, creds)
	r.ProbeID = spec.ID
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid wire result %+v: %v", r, err)
	}
	for k := range r.Values {
		name, _, err := probewire.ParseValueKey(k)
		if err != nil || !probewire.DeclaresMetric(TypeGNMI, name) {
			t.Fatalf("value %s is not declared (%v)", k, err)
		}
	}
	return r
}

func gnmiKey(name string, labels map[string]string) string {
	return probewire.ValueKey(GNMIValuePrefix+name, labels)
}

// TestGNMIKeysBecomeLabels covers AK2: keys of the path elements become
// labels, the value name is probe_gnmi_<name>, a wildcard element matches
// every list entry.
func TestGNMIKeysBecomeLabels(t *testing.T) {
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(gnmiIfTree)})
	r := runGNMI(context.Background(), t, &GNMIExecutor{}, gnmiSpec(t, s, map[string]any{"paths": []map[string]any{
		{"path": "/interfaces/interface/state/counters/out-octets", "name": "out_octets_raw"},
		{"path": "/components/component[name=cpu0]/state/temperature/instant", "name": "temperature", "unit": "celsius"},
	}}), gnmiCreds(s))
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("result = %+v", r)
	}
	want := map[string]float64{
		gnmiKey("out_octets_raw", map[string]string{"name": "et-0/0/1"}): 2000,
		gnmiKey("out_octets_raw", map[string]string{"name": "et-0/0/2"}): 20,
		gnmiKey("temperature", map[string]string{"name": "cpu0"}):        75,
	}
	for k, v := range want {
		if r.Values[k] != v {
			t.Errorf("%s = %v, want %v (values %v)", k, r.Values[k], v, r.Values)
		}
	}
	if len(r.Values) != len(want) {
		t.Fatalf("values = %v", r.Values)
	}
}

// TestGNMIEnumState covers AK1 (template if_oper) and AK3: oper-status
// DOWN is down (fail/threshold_exceeded), UP up.
func TestGNMIEnumState(t *testing.T) {
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(gnmiIfTree)})
	spec := gnmiSpec(t, s, map[string]any{"template": "if_oper"})
	r := runGNMI(context.Background(), t, &GNMIExecutor{}, spec, gnmiCreds(s))
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("all up: %+v", r)
	}
	if v, ok := r.Values[gnmiKey("if_oper_status", map[string]string{"name": "et-0/0/1"})]; !ok || v != 0 {
		t.Fatalf("state value %v (%v), want 0", v, ok)
	}
	s.SetValue("/interfaces/interface[name=et-0/0/2]/state/oper-status", "DOWN")
	r = runGNMI(context.Background(), t, &GNMIExecutor{}, spec, gnmiCreds(s))
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("one down: %+v", r)
	}
	if v := r.Values[gnmiKey("if_oper_status", map[string]string{"name": "et-0/0/2"})]; v != 2 {
		t.Fatalf("state value %v, want 2", v)
	}
	s.SetValue("/interfaces/interface[name=et-0/0/2]/state/oper-status", "TESTING")
	r = runGNMI(context.Background(), t, &GNMIExecutor{}, gnmiSpec(t, s, map[string]any{"paths": []map[string]any{
		{"path": "/interfaces/interface/state/oper-status", "name": "oper", "kind": "state", "enum": map[string]string{"UP": "up", "TESTING": "degraded"}},
	}}), gnmiCreds(s))
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("degraded enum: %+v", r)
	}
}

// TestGNMIThresholds covers AK3 and E-186: warn → ok/threshold_exceeded,
// crit → fail/threshold_exceeded; the worst value decides.
func TestGNMIThresholds(t *testing.T) {
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(gnmiIfTree)})
	path := "/components/component[name=cpu0]/state/temperature/instant"
	for _, c := range []struct {
		warn, crit float64
		dir        string
		status     probewire.Status
		code       string
	}{
		{80, 90, "", probewire.StatusOK, ""},
		{70, 90, "", probewire.StatusOK, probewire.WarnThresholdExceeded},
		{60, 72, "", probewire.StatusFail, probewire.WarnThresholdExceeded},
		{80, 78, "below", probewire.StatusFail, probewire.WarnThresholdExceeded},
	} {
		entry := map[string]any{"path": path, "name": "temp", "warn": c.warn, "crit": c.crit}
		if c.dir != "" {
			entry["direction"] = c.dir
		}
		r := runGNMI(context.Background(), t, &GNMIExecutor{}, gnmiSpec(t, s, map[string]any{"paths": []map[string]any{entry}}), gnmiCreds(s))
		if r.Status != c.status || r.Code != c.code {
			t.Errorf("warn %v crit %v %s: %+v", c.warn, c.crit, c.dir, r)
		}
	}
}

// TestGNMICounterRate covers AK2: counters become rates per second over
// the previous-value store of WP-994; the first check yields no rate, a
// wrap of a 64-bit counter is handled.
func TestGNMICounterRate(t *testing.T) {
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(gnmiIfTree)})
	store := rate.NewStore(100, time.Minute)
	ctx := rate.WithStore(context.Background(), store)
	t0 := time.Unix(1_800_000_000, 0)
	spec := gnmiSpec(t, s, map[string]any{"paths": []map[string]any{
		{"path": "/interfaces/interface[name=et-0/0/1]/state/counters/in-octets", "name": "in_octets", "kind": "counter"},
	}})
	key := gnmiKey("in_octets", map[string]string{"name": "et-0/0/1"})
	clock := t0
	e := &GNMIExecutor{Now: func() time.Time { return clock }}
	const wrapFrom = uint64(18446744073709551116) // 2^64 - 500
	s.SetValue("/interfaces/interface[name=et-0/0/1]/state/counters/in-octets", wrapFrom)
	if r := runGNMI(ctx, t, e, spec, gnmiCreds(s)); r.Status != probewire.StatusOK || len(r.Values) != 0 {
		t.Fatalf("first check: %+v, want no rate", r)
	}
	clock = t0.Add(10 * time.Second)
	s.SetValue("/interfaces/interface[name=et-0/0/1]/state/counters/in-octets", uint64(9500))
	r := runGNMI(ctx, t, e, spec, gnmiCreds(s))
	if got := r.Values[key]; got != 1000 {
		t.Fatalf("rate over the wrap = %v, want 1000/s (values %v)", got, r.Values)
	}
	clock = t0.Add(20 * time.Second)
	s.SetValue("/interfaces/interface[name=et-0/0/1]/state/counters/in-octets", uint64(19500))
	if got := runGNMI(ctx, t, e, spec, gnmiCreds(s)).Values[key]; got != 1000 {
		t.Fatalf("rate = %v, want 1000/s", got)
	}
}

// TestGNMIMissingPathIsWarn covers AK3: a configured path without update
// is ok with path_missing and the path as label.
func TestGNMIMissingPathIsWarn(t *testing.T) {
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(gnmiIfTree)})
	missing := "/system/state/hostname"
	r := runGNMI(context.Background(), t, &GNMIExecutor{}, gnmiSpec(t, s, map[string]any{"paths": []map[string]any{
		{"path": "/interfaces/interface/state/counters/in-octets", "name": "in_raw"},
		{"path": missing, "name": "hostname"},
	}}), gnmiCreds(s))
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnPathMissing || r.Labels[LabelGNMIPath] != missing {
		t.Fatalf("result = %+v", r)
	}
	if !probewire.IsWarnCode(r.Code) {
		t.Fatal("path_missing is no warn code")
	}
}

// TestGNMITruncatesAt10000Updates covers AK3 and PRT-16: an answer with
// more than 10,000 updates is cut, ok with truncated.
func TestGNMITruncatesAt10000Updates(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"data": {`)
	for i := 0; i <= 10_000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"item[id=%05d]": {"value": %d}`, i, i)
	}
	b.WriteString(`}}`)
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(b.String())})
	r := runGNMI(context.Background(), t, &GNMIExecutor{}, gnmiSpec(t, s, map[string]any{"paths": []map[string]any{
		{"path": "/data/item/value", "name": "item"},
	}}), gnmiCreds(s))
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnTruncated || r.Labels[LabelGNMITruncate] != "true" {
		t.Fatalf("result code %s labels %v", r.Code, r.Labels)
	}
	if len(r.Values) != 10_000 {
		t.Fatalf("%d values, want 10000", len(r.Values))
	}
}

// TestGNMIErrorsMapToReasons covers AK4 and E-177: tls, auth_failed and
// payload_limit are errors (unknown), unreachable and timeout failures
// (down).
func TestGNMIErrorsMapToReasons(t *testing.T) {
	paths := map[string]any{"paths": []map[string]any{{"path": "/interfaces/interface/state/oper-status", "name": "oper", "kind": "state"}}}
	s := startGNMI(t, &fakegnmi.Config{Tree: []byte(gnmiIfTree), Username: "reader", Password: "secret"})
	spec := gnmiSpec(t, s, paths)
	check := func(name string, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // test helper
		t.Helper()
		if r.Status != status || r.Code != code {
			t.Errorf("%s: %+v, want %s/%s", name, r, status, code)
		}
	}
	ok := gnmiCreds(s)
	ok["username"], ok["password"] = "reader", "secret"
	check("ok", runGNMI(context.Background(), t, &GNMIExecutor{}, spec, ok), probewire.StatusOK, "")
	check("untrusted", runGNMI(context.Background(), t, &GNMIExecutor{}, spec, map[string]string{"username": "reader", "password": "secret"}),
		probewire.StatusError, probewire.ReasonTLS)
	bad := gnmiCreds(s)
	bad["username"], bad["password"] = "reader", "wrong"
	check("auth", runGNMI(context.Background(), t, &GNMIExecutor{}, spec, bad), probewire.StatusError, probewire.ReasonAuthFailed)
	s.SetResponseBytes(5 << 20)
	check("payload", runGNMI(context.Background(), t, &GNMIExecutor{}, spec, ok), probewire.StatusError, probewire.ReasonPayloadLimit)
	s.SetResponseBytes(0)

	// A device that does not know a path answers NotFound for the whole
	// request: the known paths still count, the unknown one is missing.
	s.SetStrictPaths(true)
	two := gnmiSpec(t, s, map[string]any{"paths": []map[string]any{
		{"path": "/interfaces/interface/state/oper-status", "name": "oper", "kind": "state"},
		{"path": "/system/state/unknown-leaf", "name": "unknown"}}})
	r := runGNMI(context.Background(), t, &GNMIExecutor{}, two, ok)
	check("not found", r, probewire.StatusOK, probewire.WarnPathMissing)
	if !strings.Contains(r.Labels[LabelGNMIPath], "/system/state/unknown-leaf") || strings.Contains(r.Labels[LabelGNMIPath], "oper-status") {
		t.Errorf("not found: labels %v", r.Labels)
	}
	s.SetStrictPaths(false)
	// Another refusal of a reachable device is unknown, not down.
	for _, c := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.Internal} {
		s.SetFailCode(c)
		check(c.String(), runGNMI(context.Background(), t, &GNMIExecutor{}, spec, ok), probewire.StatusError, probewire.ReasonUnsupported)
	}
	s.SetFailCode(codes.OK)
	// An unusable credential is a configuration error (E-177).
	check("ca without PEM", runGNMI(context.Background(), t, &GNMIExecutor{}, spec, map[string]string{"ca": "not a certificate"}),
		probewire.StatusError, probewire.ReasonConfig)
	noPair := gnmiCreds(s)
	noPair["client_cert"], noPair["client_key"] = "-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n", "nope"
	check("no key pair", runGNMI(context.Background(), t, &GNMIExecutor{}, spec, noPair), probewire.StatusError, probewire.ReasonConfig)

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	unreachable := spec
	unreachable.Params, _ = json.Marshal(map[string]any{"port": port, "paths": paths["paths"]})
	unreachable.TimeoutMS = 2_000
	check("unreachable", runGNMI(context.Background(), t, &GNMIExecutor{}, unreachable, ok), probewire.StatusFail, probewire.ReasonUnreachable)

	// A listener that accepts and never answers: the handshake runs into
	// the deadline.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
		}
	}()
	timeout := spec
	timeout.Params, _ = json.Marshal(map[string]any{"port": silent.Addr().(*net.TCPAddr).Port, "paths": paths["paths"]})
	timeout.TimeoutMS = 1_000
	check("timeout", runGNMI(context.Background(), t, &GNMIExecutor{}, timeout, ok), probewire.StatusFail, probewire.ReasonTimeout)
}

// TestGNMILimits covers AK1, AK5 and PRT-21 (limits test): 33 paths, a
// timeout above 60 s, bad names and enum states are field errors; the
// register entry has the limits of E-154.
func TestGNMILimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeGNMI)
	if !ok || !typ.Available() || typ.DefaultIntervalS != 60 || typ.MinIntervalS != 10 || typ.DefaultTimeoutS != 10 ||
		typ.MaxTimeoutS != 60 || typ.MaxParallelPerTarget != 1 || typ.MaxRatePerCollector != 50 ||
		typ.MaxPayloadBytes != 4<<20 || typ.MaxRows != 10_000 || !slices.Equal(typ.CredentialNamespaces, []string{"gnmi"}) {
		t.Fatalf("register entry = %+v", typ)
	}
	paths := make([]map[string]any, 33)
	for i := range paths {
		paths[i] = map[string]any{"path": fmt.Sprintf("/a/b%d", i), "name": fmt.Sprintf("v%d", i)}
	}
	base := probewire.ProbeSpec{ID: "g", Type: TypeGNMI, Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 10_000, DownCount: 3, UpCount: 1}
	for name, c := range map[string]struct {
		params  any
		timeout int64
		field   string
	}{
		"33 paths":            {map[string]any{"paths": paths}, 0, "params.paths"},
		"template + 30 paths": {map[string]any{"template": "if_oper", "paths": paths[:30]}, 0, "params.paths"},
		"no path":             {map[string]any{}, 0, "params.paths"},
		"timeout 61 s":        {map[string]any{"template": "if_oper"}, 61_000, "timeout_s"},
		"bad name":            {map[string]any{"paths": []map[string]any{{"path": "/a", "name": "Bad-Name"}}}, 0, "params.paths[0].name"},
		"bad enum":            {map[string]any{"paths": []map[string]any{{"path": "/a", "name": "a", "kind": "state", "enum": map[string]string{"UP": "fine"}}}}, 0, "params.paths[0].enum.UP"},
		"relative path":       {map[string]any{"paths": []map[string]any{{"path": "a/b", "name": "a"}}}, 0, "params.paths[0].path"},
		"unknown encoding":    {map[string]any{"template": "if_oper", "encoding": "xml"}, 0, "params.encoding"},
		"name of the template": {map[string]any{"template": "if_oper", "paths": []map[string]any{
			{"path": "/interfaces/interface/state/counters/in-errors", "name": "if_in_octets", "kind": "counter"}}}, 0, "params.paths[0].name"},
	} {
		spec := base
		spec.Params, _ = json.Marshal(c.params)
		if c.timeout > 0 {
			spec.TimeoutMS = c.timeout
			spec.IntervalMS = 120_000
		}
		errs := probewire.Validate(&spec)
		if !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == c.field }) {
			t.Errorf("%s: errs = %v, want %s", name, errs, c.field)
		}
	}
	// The executor gets specs without that validation and rejects the
	// collision itself.
	spec := base
	spec.Params, _ = json.Marshal(map[string]any{"template": "if_oper", "paths": []map[string]any{
		{"path": "/interfaces/interface/state/counters/in-errors", "name": "if_in_octets", "kind": "counter"}}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := (&GNMIExecutor{}).Execute(ctx, spec, nil)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig || r.Labels[LabelField] != "params.paths[0].name" {
		t.Fatalf("collision with the template: %+v", r)
	}
}
