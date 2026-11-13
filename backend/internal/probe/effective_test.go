package probe

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

func icmpProbe(params string) *Probe {
	return &Probe{ID: "p1", Type: "icmp", Target: "192.0.2.1", Params: json.RawMessage(params)}
}

func ov(raw string) Overrides { return ParseOverrides(json.RawMessage(raw)) }

func number(t *testing.T, f Field) float64 {
	t.Helper()
	switch v := f.Value.(type) {
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			t.Fatal(err)
		}
		return n
	case int64:
		return float64(v)
	}
	t.Fatalf("value %#v is no number", f.Value)
	return 0
}

// TestResolveLayerOrder: every level overrides the one below it, and every
// field names the level it comes from (AK1).
func TestResolveLayerOrder(t *testing.T) {
	org := ov(`{"interval_s": 60, "timeout_s": 5, "down_count": 3, "up_count": 2, "count": 2, "thresholds": {"rtt_warn_ms": 100}}`)
	client := ov(`{"interval_s": 120, "count": 4}`)
	group := []GroupOverrides{{GroupID: "g1", Name: "core", Priority: 1, Overrides: ov(`{"interval_s": 180, "thresholds": {"rtt_warn_ms": 150}}`)}}
	ci := ov(`{"interval_s": 240, "timeout_s": 8}`)
	cases := []struct {
		name    string
		layers  Layers
		own     string
		want    map[string]any // field → value
		origins map[string]string
	}{
		{"default", Layers{}, `{}`,
			map[string]any{"interval_s": 30.0, "timeout_s": 10.0, "down_count": 5.0, "up_count": 1.0, "count": 3.0},
			map[string]string{"interval_s": OriginDefault, "timeout_s": OriginDefault, "down_count": OriginDefault, "up_count": OriginDefault, "count": OriginDefault}},
		{"org", Layers{Org: org}, `{}`,
			map[string]any{"interval_s": 60.0, "timeout_s": 5.0, "down_count": 3.0, "up_count": 2.0, "count": 2.0, "rtt_warn_ms": 100.0},
			map[string]string{"interval_s": LevelOrg, "timeout_s": LevelOrg, "down_count": LevelOrg, "up_count": LevelOrg, "count": LevelOrg, "rtt_warn_ms": LevelOrg}},
		{"client", Layers{Org: org, Client: client}, `{}`,
			map[string]any{"interval_s": 120.0, "timeout_s": 5.0, "count": 4.0},
			map[string]string{"interval_s": LevelClient, "timeout_s": LevelOrg, "count": LevelClient, "spacing_ms": OriginDefault}},
		{"group", Layers{Org: org, Client: client, Groups: group}, `{}`,
			map[string]any{"interval_s": 180.0, "count": 4.0, "rtt_warn_ms": 150.0},
			map[string]string{"interval_s": LevelGroup, "count": LevelClient, "rtt_warn_ms": LevelGroup}},
		{"ci", Layers{Org: org, Client: client, Groups: group, CI: ci}, `{}`,
			map[string]any{"interval_s": 240.0, "timeout_s": 8.0, "rtt_warn_ms": 150.0},
			map[string]string{"interval_s": LevelCI, "timeout_s": LevelCI, "rtt_warn_ms": LevelGroup, "down_count": LevelOrg}},
		{"probe", Layers{Org: org, Client: client, Groups: group, CI: ci}, `{"interval_s": 300, "count": 5, "thresholds": {"rtt_warn_ms": 50}}`,
			map[string]any{"interval_s": 300.0, "timeout_s": 8.0, "count": 5.0, "rtt_warn_ms": 50.0},
			map[string]string{"interval_s": LevelProbe, "timeout_s": LevelCI, "count": LevelProbe, "rtt_warn_ms": LevelProbe}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := Resolve(icmpProbe(tc.own), tc.layers)
			fields := map[string]Field{
				"interval_s": e.IntervalS, "timeout_s": e.TimeoutS, "down_count": e.DownCount, "up_count": e.UpCount,
				"count": e.Params["count"], "spacing_ms": e.Params["spacing_ms"], "rtt_warn_ms": e.Thresholds["rtt_warn_ms"],
			}
			for name, want := range tc.want {
				if got := number(t, fields[name]); got != want {
					t.Errorf("%s = %v, want %v", name, got, want)
				}
			}
			for name, want := range tc.origins {
				if got := fields[name].Origin; got != want {
					t.Errorf("%s origin = %q, want %q", name, got, want)
				}
				if fields[name].Clamped {
					t.Errorf("%s clamped without limit change", name)
				}
			}
			if f := fields["interval_s"]; f.Origin == LevelGroup && f.GroupID != "g1" {
				t.Errorf("interval_s group = %q, want g1", f.GroupID)
			}
		})
	}
}

// TestResolveIcmpDefaults: icmp without overrides is 30 s / 10 s / 5 / 1
// and the type parameters of E-177, every field with origin default (AK2).
func TestResolveIcmpDefaults(t *testing.T) {
	e := Resolve(icmpProbe(`{}`), Layers{})
	if !e.TypeKnown {
		t.Fatal("icmp unknown")
	}
	for name, c := range map[string]struct {
		f    Field
		want int64
	}{"interval_s": {e.IntervalS, 30}, "timeout_s": {e.TimeoutS, 10}, "down_count": {e.DownCount, 5}, "up_count": {e.UpCount, 1}} {
		if c.f.Int() != c.want || c.f.Origin != OriginDefault || c.f.Clamped {
			t.Errorf("%s = %+v, want %d from default", name, c.f, c.want)
		}
	}
	for name, want := range map[string]any{"count": 3.0, "spacing_ms": 1000.0, "loss_warn": 0.5, "rtt_warn_ms": nil} {
		f, ok := e.Params[name]
		if !ok {
			t.Fatalf("parameter %s missing", name)
		}
		if f.Origin != OriginDefault || f.Clamped {
			t.Errorf("%s = %+v, want origin default", name, f)
		}
		if want == nil {
			if f.Value != nil {
				t.Errorf("%s = %v, want no value", name, f.Value)
			}
			continue
		}
		if got := number(t, f); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if len(e.Thresholds) != 0 {
		t.Errorf("thresholds = %v, want none", e.Thresholds)
	}
	spec := e.ProbeSpec(7)
	if spec.IntervalMS != 30000 || spec.TimeoutMS != 10000 || spec.DownCount != 5 || spec.UpCount != 1 || spec.Epoch != 7 {
		t.Errorf("probe spec = %+v", spec)
	}
	if errs := probewire.Validate(&spec); len(errs) != 0 {
		t.Errorf("probe spec of the defaults invalid: %v", errs)
	}
}

// TestResolveGroupPriorityAndTie: of several groups of the CI the higher
// priority wins per field, on a tie the smaller name; the read order does
// not matter (AK3, E-178).
func TestResolveGroupPriorityAndTie(t *testing.T) {
	groups := []GroupOverrides{
		{GroupID: "g-low", Name: "a-low", Priority: 1, Overrides: ov(`{"interval_s": 60, "timeout_s": 4, "count": 2}`)},
		{GroupID: "g-high", Name: "z-high", Priority: 5, Overrides: ov(`{"interval_s": 90}`)},
		{GroupID: "g-tie-b", Name: "b-tie", Priority: 3, Overrides: ov(`{"down_count": 7, "up_count": 3}`)},
		{GroupID: "g-tie-a", Name: "a-tie", Priority: 3, Overrides: ov(`{"down_count": 9}`)},
	}
	check := func(t *testing.T, e EffectiveSpec) {
		t.Helper()
		for name, c := range map[string]struct {
			f     Field
			want  int64
			group string
		}{
			"interval_s": {e.IntervalS, 90, "g-high"},
			"timeout_s":  {e.TimeoutS, 4, "g-low"},
			"down_count": {e.DownCount, 9, "g-tie-a"},
			"up_count":   {e.UpCount, 3, "g-tie-b"},
		} {
			if c.f.Int() != c.want || c.f.Origin != LevelGroup || c.f.GroupID != c.group {
				t.Errorf("%s = %+v, want %d from %s", name, c.f, c.want, c.group)
			}
		}
		if f := e.Params["count"]; f.GroupID != "g-low" || number(t, f) != 2 {
			t.Errorf("count = %+v, want 2 from g-low", f)
		}
	}
	check(t, Resolve(icmpProbe(`{}`), Layers{Groups: groups}))
	reversed := slices.Clone(groups)
	slices.Reverse(reversed)
	check(t, Resolve(icmpProbe(`{}`), Layers{Groups: reversed}))
	// The CI level still overrides every group.
	e := Resolve(icmpProbe(`{}`), Layers{Groups: groups, CI: ov(`{"interval_s": 45}`)})
	if e.IntervalS.Int() != 45 || e.IntervalS.Origin != LevelCI || e.IntervalS.GroupID != "" {
		t.Errorf("interval_s = %+v, want 45 from ci", e.IntervalS)
	}
}

// TestResolveClampsAfterLimitChange: stored values outside the limits of a
// changed register entry are clamped and marked; the timeout is lowered
// below the interval (AK4).
func TestResolveClampsAfterLimitChange(t *testing.T) {
	icmp, ok := probewire.Lookup("icmp")
	if !ok {
		t.Fatal("icmp missing")
	}
	changed := icmp
	changed.MinIntervalS, changed.MaxIntervalS = 60, 600
	changed.MaxTimeoutS = 5
	changed.Params = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{
		"count":{"type":"integer","minimum":1,"maximum":3,"default":3},
		"spacing_ms":{"type":"integer","minimum":10,"maximum":10000,"default":1000},
		"loss_warn":{"type":"number","minimum":0,"maximum":1,"default":0.5},
		"rtt_warn_ms":{"type":"number","minimum":1}}}`)
	p := icmpProbe(`{"count": 5, "spacing_ms": "fast", "thresholds": {"rtt_warn_ms": 0.5, "gone": 1}}`)
	e := resolveWith(&changed, p, ParseOverrides(p.Params), Layers{Org: ov(`{"interval_s": 30, "timeout_s": 8}`)})
	if e.IntervalS.Int() != 60 || !e.IntervalS.Clamped || e.IntervalS.Origin != LevelOrg {
		t.Errorf("interval_s = %+v, want 60 clamped from org", e.IntervalS)
	}
	if e.TimeoutS.Int() != 5 || !e.TimeoutS.Clamped {
		t.Errorf("timeout_s = %+v, want 5 clamped", e.TimeoutS)
	}
	if f := e.Params["count"]; number(t, f) != 3 || !f.Clamped || f.Origin != LevelProbe {
		t.Errorf("count = %+v, want 3 clamped from probe", f)
	}
	if f := e.Params["spacing_ms"]; number(t, f) != 1000 || !f.Clamped || f.Origin != OriginDefault {
		t.Errorf("spacing_ms = %+v, want default 1000 clamped", f)
	}
	if f := e.Thresholds["rtt_warn_ms"]; number(t, f) != 1 || !f.Clamped {
		t.Errorf("rtt_warn_ms = %+v, want 1 clamped", f)
	}
	if _, ok := e.Thresholds["gone"]; ok {
		t.Error("threshold of no parameter kept")
	}
	if !e.Clamped() {
		t.Error("Clamped() = false")
	}

	// Timeout at or above the interval goes below it.
	e = Resolve(icmpProbe(`{"interval_s": 10, "timeout_s": 10}`), Layers{})
	if e.IntervalS.Int() != 10 || e.TimeoutS.Int() != 9 || !e.TimeoutS.Clamped || e.IntervalS.Clamped {
		t.Errorf("interval/timeout = %+v / %+v, want 10 / 9 clamped", e.IntervalS, e.TimeoutS)
	}
	clamped := e.ProbeSpec(1)
	if errs := probewire.Validate(&clamped); len(errs) != 0 {
		t.Errorf("clamped spec invalid: %v", errs)
	}
	// Counts are clamped to 1..20.
	e = Resolve(icmpProbe(`{"down_count": 50, "up_count": 0}`), Layers{})
	if e.DownCount.Int() != 20 || !e.DownCount.Clamped || e.UpCount.Int() != 1 || !e.UpCount.Clamped {
		t.Errorf("counts = %+v / %+v", e.DownCount, e.UpCount)
	}
}

// TestValidateLayerRejectsOutOfRange: a level's set with values outside the
// type limits is refused per field when written (AK4).
func TestValidateLayerRejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name, typ, params string
		want              []string
	}{
		{"empty", "icmp", `{}`, nil},
		{"valid partial", "icmp", `{"interval_s": 60, "count": 4, "thresholds": {"rtt_warn_ms": 200}}`, nil},
		{"interval only at the minimum", "icmp", `{"interval_s": 10}`, nil},
		{"timeout only", "icmp", `{"timeout_s": 10}`, nil},
		{"interval below minimum", "icmp", `{"interval_s": 5}`, []string{"interval_s"}},
		{"interval above maximum", "icmp", `{"interval_s": 7200}`, []string{"interval_s"}},
		{"timeout above maximum", "icmp", `{"timeout_s": 11}`, []string{"timeout_s"}},
		{"interval overflowing into the range", "icmp", `{"interval_s": 2305843009213693982}`, []string{"interval_s"}},
		{"timeout overflowing into the range", "icmp", `{"timeout_s": 2305843009213693957}`, []string{"timeout_s"}},
		{"interval very negative", "icmp", `{"interval_s": -9223372036854775808}`, []string{"interval_s"}},
		{"timeout not below interval", "icmp", `{"interval_s": 10, "timeout_s": 10}`, []string{"timeout_s"}},
		{"interval no integer", "icmp", `{"interval_s": "30"}`, []string{"interval_s"}},
		{"down count", "icmp", `{"down_count": 21, "up_count": 0}`, []string{"down_count", "up_count"}},
		{"parameter range", "icmp", `{"count": 9}`, []string{"params.count"}},
		{"unknown parameter", "icmp", `{"bogus": 1}`, []string{"params.bogus"}},
		{"threshold range", "icmp", `{"thresholds": {"loss_warn": 2}}`, []string{"thresholds.loss_warn"}},
		{"no object", "icmp", `[1]`, []string{"params"}},
		{"unknown type", "icmp_ping", `{}`, []string{"type"}},
		{"planned type", "snmp_status", `{}`, []string{"type"}},
		{"required parameter left to another level", "tcp", `{"interval_s": 60}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, fe := range ValidateLayer(tc.typ, json.RawMessage(tc.params)) {
				got = append(got, fe.Field)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("fields = %v, want %v", got, tc.want)
			}
		})
	}
}
