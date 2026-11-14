package snmpstatus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const community = "status-ro"

var v2Creds = map[string]string{"community": community}

var v3User = &fakesnmp.V3{User: "probe", AuthProtocol: gosnmp.SHA256, AuthPassphrase: "auth-secret-1",
	PrivProtocol: gosnmp.AES, PrivPassphrase: "priv-secret-1"}

func v3Creds(auth string) map[string]string {
	return map[string]string{"username": "probe", "auth_protocol": "SHA256", "auth_password": auth,
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
}

func statusWalk(t *testing.T) []gosnmp.SnmpPDU {
	t.Helper()
	pdus, err := fakesnmp.LoadWalkFile(filepath.Join("testdata", "status.walk"))
	if err != nil {
		t.Fatal(err)
	}
	return pdus
}

// startAgent serves the status walk on a fake agent (SNMPv3 with v3, else
// SNMPv2c) until the test ends.
func startAgent(t *testing.T, v3 *fakesnmp.V3) *fakesnmp.Agent {
	t.Helper()
	cfg := fakesnmp.Config{Community: community, SysName: "r-1", SysDescr: "Reticora status fake",
		SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}
	var agent *fakesnmp.Agent
	var err error
	if v3 != nil {
		cfg.Community = ""
		agent, err = fakesnmp.ListenV3("127.0.0.1:0", v3, cfg, statusWalk(t)...)
	} else {
		agent, err = fakesnmp.Listen("127.0.0.1:0", cfg, statusWalk(t)...)
	}
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	return agent
}

func spec(agent *fakesnmp.Agent, params string) probewire.ProbeSpec {
	s := probewire.ProbeSpec{ID: "p-status", Type: Type, Target: "127.0.0.1:" + strconv.Itoa(agent.Addr().Port),
		IntervalMS: 60_000, TimeoutMS: 2000, DownCount: 3, UpCount: 1, Epoch: 1}
	if params != "" {
		s.Params = json.RawMessage(params)
	}
	return s
}

// run executes one check with the previous-value store of the probe in
// the context, as the scheduler does (WP-994 AK7).
func run(t *testing.T, store *rate.Store, s probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	ctx := context.Background()
	if store != nil {
		ctx = rate.WithStore(ctx, store)
	}
	r := probe.Run(ctx, &s, creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func want(t *testing.T, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // results are small
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("result = %s/%s (labels %v, values %v), want %s/%s", r.Status, r.Code, r.Labels, r.Values, status, code)
	}
}

func newStore() *rate.Store { return rate.NewStore(64, time.Minute) }

// TestSNMPStatusUp covers AK1 and AK2: sysUpTime answered, uptime and
// response time in the values, no reboot; passing checks of every
// operator keep the result ok, over SNMPv2c and SNMPv3.
func TestSNMPStatusUp(t *testing.T) {
	checks := `{"checks":[
		{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"78"},
		{"oid":".1.3.6.1.2.1.1.4.0","op":"ne","value":"root@localhost"},
		{"oid":"1.3.6.1.2.1.25.1.6.0","op":"lt","value":"500"},
		{"oid":"1.3.6.1.4.1.2021.4.6.0","op":"gt","value":"1024"},
		{"oid":"1.3.6.1.2.1.2.1.0","op":"in","value":"24, 48"},
		{"oid":"1.3.6.1.2.1.1.6.0","op":"regex","value":"^Rack A[0-9]"},
		{"oid":"1.3.6.1.4.1.2021.10.1.6.1","op":"lt","value":"1.5","severity":"degraded"}]}`
	for name, tc := range map[string]struct {
		v3    *fakesnmp.V3
		creds map[string]string
	}{"v2c": {nil, v2Creds}, "v3": {v3User, v3Creds("auth-secret-1")}} {
		t.Run(name, func(t *testing.T) {
			agent := startAgent(t, tc.v3)
			r := run(t, newStore(), spec(agent, checks), tc.creds)
			want(t, r, probewire.StatusOK, "")
			if r.Values[ValueUptime] != 86400 || r.Values[ValueReboot] != 0 {
				t.Fatalf("values = %v, want uptime 86400 s and no reboot", r.Values)
			}
			if v, ok := r.Values[ValueResponse]; !ok || v < 0 || v > 2 {
				t.Fatalf("response = %v (%v)", v, ok)
			}
		})
	}
}

// TestSNMPStatusReboot covers AK2: a smaller sysUpTime than the previous
// value of the same probe (store from the context) reports
// probe_snmp_reboot = 1 without changing the state; a growing one does not.
func TestSNMPStatusReboot(t *testing.T) {
	agent := startAgent(t, nil)
	store := newStore()
	s := spec(agent, "")
	r := run(t, store, s, v2Creds)
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueReboot] != 0 {
		t.Fatalf("first run: reboot = %v", r.Values[ValueReboot])
	}
	agent.Advance(6000)
	if r = run(t, store, s, v2Creds); r.Values[ValueReboot] != 0 || r.Values[ValueUptime] != 86460 {
		t.Fatalf("advanced: values = %v", r.Values)
	}
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(4200)})
	r = run(t, store, s, v2Creds)
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueReboot] != 1 || r.Values[ValueUptime] != 42 {
		t.Fatalf("after the restart: values = %v, want reboot 1 and uptime 42 s", r.Values)
	}
	if r = run(t, store, s, v2Creds); r.Values[ValueReboot] != 0 {
		t.Fatalf("run after the reboot: reboot = %v, want 0", r.Values[ValueReboot])
	}
	// Without a store (no previous value) no reboot is reported.
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(100)})
	if r = run(t, nil, s, v2Creds); r.Values[ValueReboot] != 0 {
		t.Fatalf("without store: reboot = %v", r.Values[ValueReboot])
	}
}

// TestSNMPStatusStateless covers AK2: two executor instances with the same
// store detect the reboot alike; the executor keeps nothing itself.
func TestSNMPStatusStateless(t *testing.T) {
	agent := startAgent(t, nil)
	store := newStore()
	s := spec(agent, "")
	ctx := rate.WithStore(context.Background(), store)
	first, second := &Executor{}, &Executor{}
	if r := first.Execute(ctx, s, v2Creds); r.Values[ValueReboot] != 0 {
		t.Fatalf("first instance: %v", r.Values)
	}
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(10)})
	if r := second.Execute(ctx, s, v2Creds); r.Values[ValueReboot] != 1 {
		t.Fatalf("second instance does not see the reboot: %v", r.Values)
	}
	// The other way round, with a fresh store: the instances share nothing
	// but the store.
	other := newStore()
	octx := rate.WithStore(context.Background(), other)
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(5000)})
	if r := second.Execute(octx, s, v2Creds); r.Values[ValueReboot] != 0 {
		t.Fatalf("fresh store: %v", r.Values)
	}
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(20)})
	if r := first.Execute(octx, s, v2Creds); r.Values[ValueReboot] != 1 {
		t.Fatalf("first instance on the fresh store: %v", r.Values)
	}
}

// TestSNMPStatusCheckDegraded covers AK3: a violated check with severity
// degraded is ok with the warn code check_failed and the OID in the
// labels.
func TestSNMPStatusCheckDegraded(t *testing.T) {
	agent := startAgent(t, nil)
	r := run(t, newStore(), spec(agent, `{"checks":[
		{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"78"},
		{"oid":"1.3.6.1.2.1.25.1.6.0","op":"lt","value":"100","severity":"degraded"}]}`), v2Creds)
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Labels[LabelOID] != "1.3.6.1.2.1.25.1.6.0" {
		t.Fatalf("labels = %v", r.Labels)
	}
	if !probewire.IsWarnCode(r.Code) {
		t.Fatal("check_failed is no warn code")
	}
}

// TestSNMPStatusCheckDown covers AK3: a violated check with severity down
// (the default) is fail/check_failed and wins over a degraded one; each
// operator can fail.
func TestSNMPStatusCheckDown(t *testing.T) {
	agent := startAgent(t, nil)
	for _, c := range []string{
		`{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"72"}`,
		`{"oid":"1.3.6.1.2.1.1.7.0","op":"ne","value":"78"}`,
		`{"oid":"1.3.6.1.2.1.2.1.0","op":"lt","value":"48"}`,
		`{"oid":"1.3.6.1.2.1.2.1.0","op":"gt","value":"48"}`,
		`{"oid":"1.3.6.1.2.1.2.1.0","op":"in","value":"8,16,24"}`,
		`{"oid":"1.3.6.1.2.1.1.4.0","op":"regex","value":"^root@"}`,
		`{"oid":"1.3.6.1.2.1.1.4.0","op":"gt","value":"1"}`, // text answer, numeric operator
	} {
		r := run(t, newStore(), spec(agent, `{"checks":[
			{"oid":"1.3.6.1.2.1.25.1.6.0","op":"lt","value":"100","severity":"degraded"},`+c+`]}`), v2Creds)
		if r.Status != probewire.StatusFail || r.Code != probewire.WarnCheckFailed || r.Labels[LabelOID] == "1.3.6.1.2.1.25.1.6.0" {
			t.Errorf("%s: result %s/%s labels %v, want fail/check_failed on the down check", c, r.Status, r.Code, r.Labels)
		}
	}
}

// TestSNMPStatusNoSuchObject covers AK3: an OID the agent does not have is
// ok with the warn code no_such_object; a violated check ranks above it.
func TestSNMPStatusNoSuchObject(t *testing.T) {
	agent := startAgent(t, nil)
	r := run(t, newStore(), spec(agent, `{"checks":[
		{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"78"},
		{"oid":"1.3.6.1.4.1.99999.1.0","op":"eq","value":"1"}]}`), v2Creds)
	want(t, r, probewire.StatusOK, probewire.WarnNoSuchObject)
	if r.Labels[LabelOID] != "1.3.6.1.4.1.99999.1.0" || r.Values[ValueUptime] != 86400 {
		t.Fatalf("labels %v values %v", r.Labels, r.Values)
	}
	// A deleted variable answers noSuchObject as well.
	agent.Delete(".1.3.6.1.2.1.1.7.0")
	r = run(t, newStore(), spec(agent, `{"checks":[{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"78"}]}`), v2Creds)
	want(t, r, probewire.StatusOK, probewire.WarnNoSuchObject)
	// A failing check and an absent OID: the check decides.
	r = run(t, newStore(), spec(agent, `{"checks":[
		{"oid":"1.3.6.1.4.1.99999.1.0","op":"eq","value":"1"},
		{"oid":"1.3.6.1.2.1.2.1.0","op":"eq","value":"24","severity":"degraded"}]}`), v2Creds)
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
}

// TestSNMPStatusV3AuthFailedIsUnknown covers AK4: a usmStats report for a
// wrong key (wrong digest), a decryption error (right auth, wrong priv
// password) or an unknown user is error/auth_failed (unknown), never fail.
func TestSNMPStatusV3AuthFailedIsUnknown(t *testing.T) {
	agent := startAgent(t, v3User)
	r := run(t, newStore(), spec(agent, ""), v3Creds("wrong-auth-pw"))
	want(t, r, probewire.StatusError, probewire.ReasonAuthFailed)
	wrongPriv := v3Creds("auth-secret-1")
	wrongPriv["priv_password"] = "wrong-priv-pw"
	r = run(t, newStore(), spec(agent, ""), wrongPriv)
	want(t, r, probewire.StatusError, probewire.ReasonAuthFailed)
	unknown := v3Creds("auth-secret-1")
	unknown["username"] = "nobody"
	r = run(t, newStore(), spec(agent, ""), unknown)
	want(t, r, probewire.StatusError, probewire.ReasonAuthFailed)
	for k, v := range r.Labels {
		if strings.Contains(v, "secret") || strings.Contains(k, "password") {
			t.Fatalf("secret in the labels: %v", r.Labels)
		}
	}
}

// TestSNMPStatusErrorStatusIsFail covers AK4: an agent that answers the
// GET with an error-status (tooBig with an empty variable list, genErr
// with Null values) did not deliver sysUpTime or the checks; the result is
// fail/check_failed with the status in the label error_status, not ok
// with no_such_object, over SNMPv2c and SNMPv3.
func TestSNMPStatusErrorStatusIsFail(t *testing.T) {
	for _, tc := range []struct {
		name  string
		v3    *fakesnmp.V3
		creds map[string]string
	}{{"v2c", nil, v2Creds}, {"v3", v3User, v3Creds("auth-secret-1")}} {
		t.Run(tc.name, func(t *testing.T) {
			agent := startAgent(t, tc.v3)
			s := spec(agent, `{"checks":[{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"78","severity":"degraded"}]}`)
			for status, label := range map[gosnmp.SNMPError]string{gosnmp.TooBig: "TooBig", gosnmp.GenErr: "GenErr"} {
				agent.SetGetError(status)
				r := run(t, newStore(), s, tc.creds)
				want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
				if r.Labels[probe.LabelSNMPErrorStatus] != label {
					t.Fatalf("%s: labels = %v", label, r.Labels)
				}
			}
			agent.SetGetError(gosnmp.NoError)
			want(t, run(t, newStore(), s, tc.creds), probewire.StatusOK, "")
		})
	}
}

// TestSNMPStatusV2cWrongCommunityIsTimeout covers AK4: the agent does not
// answer a wrong community; the check ends after the timeout as
// fail/timeout, with retries within the probe timeout.
func TestSNMPStatusV2cWrongCommunityIsTimeout(t *testing.T) {
	agent := startAgent(t, nil)
	s := spec(agent, `{"retries":1}`)
	s.TimeoutMS = 2000
	start := time.Now()
	r := run(t, newStore(), s, map[string]string{"community": "wrong"})
	want(t, r, probewire.StatusFail, probewire.ReasonTimeout)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("check took %s, beyond the probe timeout", d)
	}
}

// TestSNMPStatusLimits covers AK3 and AK6: 21 checks, a timeout of 16 s
// (probe and request), 3 retries, a regex over 256 characters, a
// non-numeric lt value and a bad OID are field errors of
// probewire.Validate; the check is not executed (error/config).
func TestSNMPStatusLimits(t *testing.T) {
	typ, ok := probewire.Lookup(Type)
	if !ok || !typ.Available() || typ.MinIntervalS != 30 || typ.MaxTimeoutS != 15 ||
		len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("contract entry = %+v", typ)
	}
	checks := make([]string, MaxChecks+1)
	for i := range checks {
		checks[i] = `{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"78"}`
	}
	long := strings.Repeat("a", probewire.MaxSNMPRegex+1)
	base := probewire.ProbeSpec{ID: "p", Type: Type, Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 5000, DownCount: 3, UpCount: 1}
	for name, tc := range map[string]struct {
		mutate func(*probewire.ProbeSpec)
		field  string
	}{
		"21 checks": {func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"checks":[` + strings.Join(checks, ",") + `]}`)
		}, "params.checks"},
		"timeout 16 s":    {func(s *probewire.ProbeSpec) { s.TimeoutMS = 16_000 }, "timeout_s"},
		"interval 29 s":   {func(s *probewire.ProbeSpec) { s.IntervalMS = 29_000 }, "interval_s"},
		"request timeout": {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"timeout_s":16}`) }, "params.timeout_s"},
		"retries 3":       {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"retries":3}`) }, "params.retries"},
		"regex 257": {func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"checks":[{"oid":"1.3.6.1.2.1.1.4.0","op":"regex","value":"` + long + `"}]}`)
		}, "params.checks[0].value"},
		"bad regex": {func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"checks":[{"oid":"1.3.6.1.2.1.1.4.0","op":"regex","value":"(a"}]}`)
		}, "params.checks[0].value"},
		"lt text": {func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"checks":[{"oid":"1.3.6.1.2.1.1.7.0","op":"lt","value":"many"}]}`)
		}, "params.checks[0].value"},
		"bad oid": {func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"checks":[{"oid":"sysUpTime.0","op":"eq","value":"1"}]}`)
		}, "params.checks[0].oid"},
		"bad severity": {func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"checks":[{"oid":"1.3.6.1.2.1.1.7.0","op":"eq","value":"1","severity":"warn"}]}`)
		}, "params.checks[0].severity"},
	} {
		s := base
		tc.mutate(&s)
		errs := probewire.Validate(&s)
		found := false
		for _, e := range errs {
			found = found || e.Field == tc.field
		}
		if !found {
			t.Errorf("%s: errs = %v, want field %s", name, errs, tc.field)
			continue
		}
		r := probe.Run(context.Background(), &s, v2Creds)
		if r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
			t.Errorf("%s: result %s/%s, want error/config", name, r.Status, r.Code)
		}
	}
	edge := base
	edge.Params = json.RawMessage(`{"checks":[` + strings.Join(checks[:MaxChecks], ",") + `],"timeout_s":15,"retries":2,"port":161}`)
	edge.TimeoutMS = 15_000
	if errs := probewire.Validate(&edge); len(errs) > 0 {
		t.Fatalf("edges rejected: %v", errs)
	}
}
