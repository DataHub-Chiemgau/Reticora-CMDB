package probewire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

var observed = time.Date(2026, 11, 12, 10, 15, 30, 123000000, time.UTC)

func sampleResult(i int) Result {
	return Result{
		ProbeID:    fmt.Sprintf("8f1c2a4e-0b7d-4c3e-9a51-%012d", i),
		Epoch:      7,
		Seq:        uint64(100000 + i),
		ObservedAt: observed.Add(time.Duration(i) * time.Millisecond),
		Status:     StatusOK,
		RTTMS:      f64(12.345),
		Loss:       f64(0),
		Values: map[string]float64{
			"rtt_min_ms": 11.902,
			"rtt_avg_ms": 12.345,
			"rtt_max_ms": 13.117,
			"jitter_ms":  0.402,
		},
	}
}

func goldenMessages() map[string]struct {
	msg   Message
	empty func() Message
} {
	last := sampleResult(1)
	last.Status = StatusFail
	last.Code = ReasonTimeout
	last.RTTMS = nil
	last.Loss = f64(1)
	last.Values = nil
	authFailed := Result{
		ProbeID:    "8f1c2a4e-0b7d-4c3e-9a51-000000000002",
		Epoch:      7,
		Seq:        100002,
		ObservedAt: observed,
		Status:     StatusError,
		Code:       ReasonAuthFailed,
		Labels: map[string]string{
			LabelCredentialID:        "3b0e7a52-6c1d-4f0a-8e2b-5d9c1f4a7e60",
			LabelCredentialNamespace: "snmp",
		},
	}
	return map[string]struct {
		msg   Message
		empty func() Message
	}{
		"assignment_set.json": {
			msg: &AssignmentSet{
				CollectorID: "c0ffee00-1111-4222-8333-444455556666",
				Version:     42,
				Page:        1,
				NextPage:    2,
				Probes: []ProbeSpec{
					{
						ID: "8f1c2a4e-0b7d-4c3e-9a51-000000000000", Type: "icmp", Target: "192.0.2.10",
						IntervalMS: 30000, TimeoutMS: 10000, DownCount: 5, UpCount: 1,
						Params:     json.RawMessage(`{"count":3,"spacing_ms":1000}`),
						Thresholds: json.RawMessage(`{"loss_warn":0.5}`),
						Epoch:      7,
					},
					{
						ID: "8f1c2a4e-0b7d-4c3e-9a51-000000000001", Type: "tcp", Target: "192.0.2.10:443",
						IntervalMS: 60000, TimeoutMS: 5000, DownCount: 3, UpCount: 1,
						ParentID: "8f1c2a4e-0b7d-4c3e-9a51-000000000000",
						Epoch:    7,
					},
				},
			},
			empty: func() Message { return &AssignmentSet{} },
		},
		"results_batch.json": {
			msg: &ResultBatch{
				CollectorID: "c0ffee00-1111-4222-8333-444455556666",
				Results:     []Result{sampleResult(0), last, authFailed},
			},
			empty: func() Message { return &ResultBatch{} },
		},
		"state_change.json": {
			msg: &StateChange{
				ProbeID:    last.ProbeID,
				Epoch:      7,
				Seq:        100003,
				From:       StateUp,
				To:         StateDown,
				Reason:     ReasonTimeout,
				ObservedAt: last.ObservedAt,
				LastResult: &last,
			},
			empty: func() Message { return &StateChange{} },
		},
		"alive.json": {
			msg: &Alive{
				CollectorTime:  observed,
				ConfigVersion:  42,
				Probes:         530,
				ResultsPerS:    17.65,
				QueueState:     QueueOK,
				QueueResults:   0,
				Dropped:        0,
				Skipped:        2,
				ICMPCapability: ICMPRaw,
			},
			empty: func() Message { return &Alive{} },
		},
	}
}

// TestGoldenRoundtrip covers AK4: encode equals the fixture byte for byte,
// decoding the fixture yields the value again.
func TestGoldenRoundtrip(t *testing.T) {
	for name, tc := range goldenMessages() {
		t.Run(name, func(t *testing.T) {
			got, err := Encode(tc.msg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", name)
			if os.Getenv("RETICORA_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.TrimSuffix(want, []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Fatalf("encoding differs from %s:\n got %s\nwant %s", path, got, want)
			}
			back := tc.empty()
			if err := Decode(want, back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, tc.msg) {
				t.Fatalf("decode differs:\n got %#v\nwant %#v", back, tc.msg)
			}
		})
	}
}

// TestResultBatchFitsEdgePayload covers AK4: 1,000 results with four values
// stay within a quarter of the 1 MiB edge message size.
func TestResultBatchFitsEdgePayload(t *testing.T) {
	batch := &ResultBatch{CollectorID: "c0ffee00-1111-4222-8333-444455556666"}
	for i := range MaxBatchResults {
		batch.Results = append(batch.Results, sampleResult(i))
	}
	b, err := Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("batch of %d results: %d bytes", MaxBatchResults, len(b))
	if len(b) > 256<<10 {
		t.Fatalf("batch of %d results is %d bytes, want <= %d", MaxBatchResults, len(b), 256<<10)
	}
}

// TestNonFiniteResultDoesNotBlockBatch: json.Marshal rejects NaN and ±Inf,
// so a single such value would fail the whole batch. Validate rejects the
// bad result; the rest of the batch still encodes.
func TestNonFiniteResultDoesNotBlockBatch(t *testing.T) {
	results := []Result{sampleResult(0), sampleResult(1), sampleResult(2), sampleResult(3)}
	results[1].RTTMS = f64(math.NaN())
	results[2].Values = map[string]float64{"jitter_ms": math.Inf(1)}
	if _, err := Encode(&ResultBatch{Results: results}); err == nil {
		t.Fatal("encoding a batch with NaN succeeded; the guard would be unnecessary")
	}
	for i, wantErr := range []bool{false, true, true, false} {
		if err := results[i].Validate(); (err != nil) != wantErr || (err != nil && !errors.Is(err, ErrInvalidResult)) {
			t.Fatalf("result %d: Validate() = %v, want error %v", i, err, wantErr)
		}
	}
	valid, rejected := ValidResults(results)
	if rejected != 2 || len(valid) != 2 || valid[0].ProbeID != results[0].ProbeID || valid[1].ProbeID != results[3].ProbeID {
		t.Fatalf("ValidResults = %d valid, %d rejected", len(valid), rejected)
	}
	if _, err := Encode(&ResultBatch{Results: valid}); err != nil {
		t.Fatalf("encode valid results: %v", err)
	}
	bad := sampleResult(4)
	bad.Status = "maybe"
	if bad.Validate() == nil {
		t.Fatal("unknown status accepted")
	}
	bad = sampleResult(5)
	bad.Code = "not_supported"
	if bad.Validate() == nil {
		t.Fatal("unknown reason code accepted")
	}
	alive := Alive{ResultsPerS: math.NaN()}
	if alive.Validate() == nil {
		t.Fatal("alive with NaN results_per_s accepted")
	}
	alive.ResultsPerS = 353.5
	if err := alive.Validate(); err != nil {
		t.Fatal(err)
	}
}

func manyProbes(n int) []ProbeSpec {
	out := make([]ProbeSpec, n)
	for i := range out {
		out[i] = ProbeSpec{
			ID: fmt.Sprintf("8f1c2a4e-0b7d-4c3e-9a51-%012d", i), Type: "icmp",
			Target:     fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255),
			IntervalMS: 30000, TimeoutMS: 10000, DownCount: 5, UpCount: 1,
			Params:     json.RawMessage(`{"count":3,"spacing_ms":1000}`),
			Thresholds: json.RawMessage(`{"loss_warn":0.5,"rtt_warn_ms":250}`),
			ParentID:   "8f1c2a4e-0b7d-4c3e-9a51-999999999999",
			Epoch:      int64(i + 1),
		}
	}
	return out
}

// TestAssignmentSetPageLimits covers AK6.
func TestAssignmentSetPageLimits(t *testing.T) {
	pages, err := Paginate("c1", 9, manyProbes(MaxPageProbes+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	total := 0
	for i, p := range pages {
		if p.Page != i+1 {
			t.Fatalf("page %d numbered %d", i, p.Page)
		}
		if len(p.Probes) > MaxPageProbes {
			t.Fatalf("page %d holds %d probes", p.Page, len(p.Probes))
		}
		b, err := Encode(&p)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > MaxPageBytes {
			t.Fatalf("page %d is %d bytes", p.Page, len(b))
		}
		total += len(p.Probes)
	}
	if pages[0].NextPage != 2 || pages[1].NextPage != 0 {
		t.Fatalf("next_page = %d, %d; want 2 and empty", pages[0].NextPage, pages[1].NextPage)
	}
	last, _ := Encode(&pages[1])
	if strings.Contains(string(last), `"next_page"`) {
		t.Fatalf("last page carries next_page: %s", last[:120])
	}
	if total != MaxPageProbes+1 {
		t.Fatalf("total probes = %d", total)
	}

	// Large probes: the byte limit splits before the count limit.
	big := manyProbes(400)
	pad := json.RawMessage(`{"pad":"` + strings.Repeat("x", 4000) + `"}`)
	for i := range big {
		big[i].Params = pad
	}
	pages, err = Paginate("c1", 9, big)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 3 {
		t.Fatalf("pages = %d, want a byte split", len(pages))
	}
	for _, p := range pages {
		b, _ := Encode(&p)
		if len(b) > MaxPageBytes {
			t.Fatalf("page %d is %d bytes", p.Page, len(b))
		}
	}

	empty, err := Paginate("c1", 1, nil)
	if err != nil || len(empty) != 1 || empty[0].NextPage != 0 || empty[0].Probes == nil {
		t.Fatalf("empty set = %+v, %v", empty, err)
	}

	huge := manyProbes(1)
	huge[0].Params = json.RawMessage(`{"pad":"` + strings.Repeat("x", MaxPageBytes) + `"}`)
	if _, err := Paginate("c1", 1, huge); !errors.Is(err, ErrProbeTooLarge) {
		t.Fatalf("oversized probe: err = %v", err)
	}
}

// TestAssignmentSetVersionChanged covers AK6: a version change during the
// fetch is announced on the next page, the collector restarts with page 1.
func TestAssignmentSetVersionChanged(t *testing.T) {
	v1, err := Paginate("c1", 1, manyProbes(MaxPageProbes+1))
	if err != nil {
		t.Fatal(err)
	}
	first, ok := PageOf(v1, 0, 1)
	if !ok || first.Version != 1 || first.VersionChanged {
		t.Fatalf("first page = %+v", first.Version)
	}
	v2, err := Paginate("c1", 2, manyProbes(10))
	if err != nil {
		t.Fatal(err)
	}
	next, ok := PageOf(v2, first.Version, first.NextPage)
	if !ok || !next.VersionChanged || next.Version != 2 || len(next.Probes) != 0 || next.NextPage != 1 {
		t.Fatalf("next page after change = %+v", next)
	}
	b, _ := Encode(&next)
	if !strings.Contains(string(b), `"version_changed":true`) {
		t.Fatalf("encoded = %s", b)
	}
	restart, ok := PageOf(v2, 0, next.NextPage)
	if !ok || restart.VersionChanged || restart.Version != 2 || len(restart.Probes) != 10 || restart.NextPage != 0 {
		t.Fatalf("restart = %+v", restart)
	}
	// Same version: no change flag.
	same, ok := PageOf(v1, 1, 2)
	if !ok || same.VersionChanged || len(same.Probes) != 1 {
		t.Fatalf("same version page 2 = %+v", same.VersionChanged)
	}
	if _, ok := PageOf(v1, 1, 3); ok {
		t.Fatal("page beyond the last page answered")
	}
}

// TestResultHasNoDetailField covers AK1 and E-177.
func TestResultHasNoDetailField(t *testing.T) {
	typ := reflect.TypeFor[Result]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		name := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
		if strings.Contains(name, "detail") || strings.Contains(name, "message") || strings.Contains(name, "error") {
			t.Fatalf("Result has a detail field: %s", f.Name)
		}
	}
	var r Result
	if err := Decode([]byte(`{"schema_version":"1.0","probe_id":"p","detail":"secret text"}`), &resultMsg{&r}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "detail") {
		t.Fatalf("detail survived: %s", b)
	}
}

// resultMsg lets Decode read a bare Result in the test.
type resultMsg struct{ *Result }

func (resultMsg) header() *Header { return &Header{} }

// TestUnknownMajorVersionRejected covers AK2.
func TestUnknownMajorVersionRejected(t *testing.T) {
	for _, v := range []string{"2.0", "0.9", "", "1", "x.y", "1.x"} {
		err := Decode([]byte(`{"schema_version":"`+v+`","version":1}`), &AssignmentSet{})
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("version %q: err = %v", v, err)
		}
	}
	if err := Decode([]byte(`{"version":1}`), &AssignmentSet{}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("missing version: err = %v", err)
	}
	var a Alive
	if err := Decode([]byte(`{"schema_version":"1.7","config_version":3}`), &a); err != nil || a.ConfigVersion != 3 {
		t.Fatalf("newer minor version: %v, %+v", err, a)
	}
}

// TestUnknownFieldsTolerated covers AK2.
func TestUnknownFieldsTolerated(t *testing.T) {
	var s StateChange
	in := `{"schema_version":"1.3","probe_id":"p1","epoch":2,"seq":5,"from":"up","to":"down","reason":"timeout",` +
		`"observed_at":"2026-11-12T10:15:30Z","future_field":{"a":[1,2]},"hint":"x"}`
	if err := Decode([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	if s.ProbeID != "p1" || s.Seq != 5 || s.To != StateDown || s.SchemaVersion != "1.3" {
		t.Fatalf("decoded = %+v", s)
	}
	if !s.From.Valid() || !s.To.Valid() || State("gone").Valid() {
		t.Fatal("state validation")
	}
}

// TestReasonCodes covers AK3.
func TestReasonCodes(t *testing.T) {
	for _, code := range []string{
		ReasonCollectorUnreachable, ReasonParentDown, ReasonLimitExceeded, ReasonUnsupported,
		ReasonConfig, ReasonAuthFailed, ReasonTimeout, ReasonRefused, ReasonICMPUnavailable,
		"unsupported", "unreachable", "tls",
	} {
		if !ValidReason(code) {
			t.Fatalf("%q rejected", code)
		}
	}
	for _, code := range []string{"not_supported", "", "down", "TIMEOUT"} {
		if ValidReason(code) {
			t.Fatalf("%q accepted", code)
		}
	}
	for _, code := range []string{"threshold_exceeded", "cert_expiring"} {
		if !WarnCodes[code] || !IsWarnCode(code) {
			t.Fatalf("WarnCodes lacks %q", code)
		}
	}
	for code := range WarnCodes {
		if !ValidReason(code) {
			t.Fatalf("warn code %q is no valid reason", code)
		}
	}
	if IsWarnCode(ReasonTimeout) {
		t.Fatal("timeout is no warn code")
	}
}

// TestCredentialLabelKeys covers AK1.
func TestCredentialLabelKeys(t *testing.T) {
	if LabelCredentialID != "credential_id" || LabelCredentialNamespace != "credential_namespace" {
		t.Fatalf("label keys = %q, %q", LabelCredentialID, LabelCredentialNamespace)
	}
}

// TestNoBackendOrCollectorImports covers AK5.
func TestNoBackendOrCollectorImports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not available")
	}
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/DataHub-Chiemgau/Reticora-CMDB/backend") ||
			strings.HasPrefix(dep, "github.com/DataHub-Chiemgau/Reticora-CMDB/collector") {
			t.Fatalf("probewire depends on %s", dep)
		}
	}
}
