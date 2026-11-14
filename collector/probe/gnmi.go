package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/gnmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeGNMI is the register key of the gNMI probe.
const TypeGNMI = "gnmi"

// Kinds of a gnmi path (WP-1016 AK1).
const (
	GNMIKindGauge   = "gauge"
	GNMIKindCounter = "counter"
	GNMIKindState   = "state"
)

// Value prefix and labels of a gnmi result (WP-1016 AK2, AK3): every
// mapped path yields probe_gnmi_<name> with the keys of its path elements
// as labels; a state is 0 (up), 1 (degraded) or 2 (down).
const (
	GNMIValuePrefix   = "probe_gnmi_"
	LabelGNMIPath     = "path"
	LabelGNMITruncate = "truncated"
)

// GNMITemplateIfOper is the template of interface operating states and
// octet counters (WP-1016 AK1).
const GNMITemplateIfOper = "if_oper"

// gnmiTemplates are the predefined path sets.
var gnmiTemplates = map[string][]gnmiPath{
	GNMITemplateIfOper: {
		{Path: "/interfaces/interface/state/oper-status", Name: "if_oper_status", Kind: GNMIKindState,
			Enum: map[string]string{"UP": "up", "DOWN": "down", "LOWER_LAYER_DOWN": "down"}},
		{Path: "/interfaces/interface/state/counters/in-octets", Name: "if_in_octets", Kind: GNMIKindCounter, Unit: "bytes"},
		{Path: "/interfaces/interface/state/counters/out-octets", Name: "if_out_octets", Kind: GNMIKindCounter, Unit: "bytes"},
	},
}

// gnmiScopes holds the TLS scopes of ConfigureGNMI.
var gnmiScopes atomic.Pointer[[]plugins.TLSScope]

// ConfigureGNMI sets the TLS scopes the registered gnmi executor uses when
// the credential names no CA (like ConfigureHTTP).
func ConfigureGNMI(scopes []plugins.TLSScope) {
	s := slices.Clone(scopes)
	gnmiScopes.Store(&s)
}

func init() { Register(TypeGNMI, &GNMIExecutor{}) }

// GNMIExecutor is the probe type gnmi (PRT-16, WP-1016, E-193): Get or
// Subscribe ONCE over TLS (client collector/probe/gnmi, strictly reading:
// no Set, no Subscribe STREAM/POLL) on the configured OpenConfig paths,
// mapped to values and to the probe state:
//
//   - gauge: the value; above warn → ok/threshold_exceeded, above crit →
//     fail/threshold_exceeded (direction below inverts);
//   - counter: the rate per second over the previous-value store of the
//     engine (collector/probe/rate, wrap of 64-bit counters handled); the
//     first check yields no rate; thresholds apply to the rate;
//   - state: the value text mapped by enum to up|degraded|down;
//     degraded → ok/threshold_exceeded, down → fail/threshold_exceeded.
//
// A configured path without update is ok with path_missing (paths in the
// label path), an answer cut at 10,000 updates at least ok with truncated
// (label truncated=true). Errors: untrusted certificate or failed
// handshake error/tls, rejected credential error/auth_failed, an answer
// above 4 MiB error/payload_limit (all unknown); not reachable
// fail/unreachable, deadline fail/timeout.
//
// The executor holds no state: the previous counter readings are kept by
// the store in the context of the check (WP-950 AK4).
type GNMIExecutor struct {
	// TLSScopes replace the configured scopes (tests).
	TLSScopes []plugins.TLSScope
	// Now replaces the clock of the counter readings in tests.
	Now func() time.Time
}

type gnmiPath struct {
	Path      string            `json:"path"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Enum      map[string]string `json:"enum"`
	Warn      *float64          `json:"warn"`
	Crit      *float64          `json:"crit"`
	Direction string            `json:"direction"`
	Unit      string            `json:"unit"`
}

type gnmiParams struct {
	Port     int        `json:"port"`
	Origin   *string    `json:"origin"`
	Encoding string     `json:"encoding"`
	Mode     string     `json:"mode"`
	Template string     `json:"template"`
	Paths    []gnmiPath `json:"paths"`
}

// gnmi severities, ordered.
const (
	gnmiUp = iota
	gnmiDegraded
	gnmiDown
)

var gnmiStates = map[string]int{"up": gnmiUp, "degraded": gnmiDegraded, "down": gnmiDown}

func (e *GNMIExecutor) tlsScopes() []plugins.TLSScope {
	if e.TLSScopes != nil {
		return e.TLSScopes
	}
	if s := gnmiScopes.Load(); s != nil {
		return *s
	}
	return nil
}

// Execute implements Executor.
func (e *GNMIExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	p := gnmiParams{Port: 9339, Mode: string(gnmi.ModeGet)}
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	origin := "openconfig"
	if p.Origin != nil {
		origin = *p.Origin
	}
	paths, field, msg := gnmiExpand(&p)
	if field != "" {
		return configResult(field, msg)
	}
	target, ok := tcpAddress(spec.Target, p.Port)
	if !ok {
		return configResult("target", "the target must be the address of the gNMI server")
	}
	timeout := gnmi.DefaultTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	client, err := gnmi.Dial(&gnmi.Config{Target: target, Credentials: creds, TLSScopes: e.tlsScopes(),
		Timeout: timeout, Mode: gnmi.Mode(p.Mode), Encoding: p.Encoding, Origin: origin})
	if err != nil {
		return gnmiErrorResult(ctx, err)
	}
	defer func() { _ = client.Close() }()
	req := make([]string, len(paths))
	for i := range paths {
		req[i] = paths[i].Path
	}
	res, err := client.Get(ctx, req)
	if errors.Is(err, gnmi.ErrPathNotFound) {
		// The device refuses the whole request for one unknown path
		// (gNMI 3.3.4); each path is read on its own so that only the
		// unknown ones count as path_missing (AK3).
		res, err = gnmiGetEach(ctx, client, req)
	}
	if err != nil {
		return gnmiErrorResult(ctx, err)
	}
	// The time of a counter reading: the notification time stamp of the
	// device, otherwise the collector clock; a test clock replaces both.
	now := time.Now()
	clock := func(u *gnmi.Update) time.Time {
		if !u.Timestamp.IsZero() && u.Timestamp.Unix() > 0 {
			return u.Timestamp
		}
		return now
	}
	if e.Now != nil {
		at := e.Now()
		clock = func(*gnmi.Update) time.Time { return at }
	}
	return gnmiEvaluate(&res, paths, rate.FromContext(ctx), clock)
}

// gnmiConfigField names the field of the i-th expanded path: the paths of
// the template come first, a configured path is params.paths[j].
func gnmiConfigField(p *gnmiParams, i int, name string) string {
	j := i - len(gnmiTemplates[p.Template])
	if j < 0 {
		return "params.template"
	}
	return fmt.Sprintf("params.paths[%d].%s", j, name)
}

// gnmiGetEach reads every path with its own request; a path the device
// does not know contributes no update.
func gnmiGetEach(ctx context.Context, client *gnmi.Client, paths []string) (gnmi.Result, error) {
	var all gnmi.Result
	for _, p := range paths {
		res, err := client.Get(ctx, []string{p})
		if errors.Is(err, gnmi.ErrPathNotFound) {
			continue
		}
		if err != nil {
			return gnmi.Result{}, err
		}
		for _, u := range res.Updates {
			if len(all.Updates) >= gnmi.MaxUpdates {
				all.Truncated = true
				break
			}
			all.Updates = append(all.Updates, u)
		}
		all.Truncated = all.Truncated || res.Truncated
		all.Once = all.Once || res.Once
	}
	return all, nil
}

// gnmiExpand returns the paths of the template followed by the configured
// paths, with defaults; a field error names the bad entry.
func gnmiExpand(p *gnmiParams) (paths []gnmiPath, field, msg string) {
	if p.Template != "" {
		t, ok := gnmiTemplates[p.Template]
		if !ok {
			return nil, "params.template", "unknown template"
		}
		paths = append(paths, t...)
	}
	paths = append(paths, p.Paths...)
	if len(paths) == 0 {
		return nil, "params.paths", "at least one path or a template is required"
	}
	if len(paths) > gnmi.MaxPaths {
		return nil, "params.paths", fmt.Sprintf("at most %d paths including the template", gnmi.MaxPaths)
	}
	names := make(map[string]bool, len(paths))
	for i := range paths {
		if paths[i].Kind == "" {
			paths[i].Kind = GNMIKindGauge
		}
		// Two paths with one name would write the same value key and,
		// as counters, share one series of the rate store.
		if names[paths[i].Name] {
			return nil, gnmiConfigField(p, i, "name"), "is used twice"
		}
		names[paths[i].Name] = true
		if _, err := gnmi.ParsePath(paths[i].Path); err != nil {
			return nil, gnmiConfigField(p, i, "path"), "invalid gNMI path"
		}
	}
	return paths, "", ""
}

// gnmiErrorResult maps a client error to the result (AK4, E-177).
func gnmiErrorResult(ctx context.Context, err error) probewire.Result {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The deadline of the check ended the call, also within a
		// handshake.
		return failResult(probewire.StatusFail, probewire.ReasonTimeout)
	case errors.Is(err, plugins.ErrTLSUntrusted), errors.Is(err, gnmi.ErrTLSHandshake):
		return failResult(probewire.StatusError, probewire.ReasonTLS)
	case errors.Is(err, plugins.ErrAuthFailed):
		return failResult(probewire.StatusError, probewire.ReasonAuthFailed)
	case errors.Is(err, gnmi.ErrMessageTooLarge):
		return failResult(probewire.StatusError, probewire.ReasonPayloadLimit)
	case errors.Is(err, gnmi.ErrNoEncoding):
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	case errors.Is(err, gnmi.ErrPath), errors.Is(err, gnmi.ErrTooManyPaths):
		return failResult(probewire.StatusError, probewire.ReasonConfig)
	case errors.Is(err, gnmi.ErrCredential):
		// The credential cannot be used: a configuration error, not a
		// down target (E-177).
		return configResult("credential", "the gnmi credential holds no usable CA or key pair")
	case errors.Is(err, gnmi.ErrRejected):
		// The device answered but refused the request: reachable, the
		// state is unknown.
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		return failResult(probewire.StatusFail, probewire.ReasonTimeout)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return failResult(probewire.StatusFail, probewire.ReasonTimeout)
	}
	if errors.Is(err, plugins.ErrUnreachable) || errors.As(err, &ne) {
		return failResult(probewire.StatusFail, probewire.ReasonUnreachable)
	}
	// Anything else did not come from the device: unknown, not down.
	return failResult(probewire.StatusError, probewire.ReasonUnsupported)
}

// gnmiEvaluate maps the updates to values and the result (AK2, AK3).
func gnmiEvaluate(res *gnmi.Result, paths []gnmiPath, store *rate.Store, clock func(*gnmi.Update) time.Time) probewire.Result {
	parsed := make([]*gpb.Path, len(paths))
	for i := range paths {
		parsed[i], _ = gnmi.ParsePath(paths[i].Path)
	}
	values := map[string]float64{}
	matched := make([]bool, len(paths))
	worst := gnmiUp
	for _, u := range res.Updates {
		up, err := gnmi.ParsePath(u.Path)
		if err != nil {
			continue
		}
		for i := range paths {
			if !gnmiMatches(parsed[i], up) {
				continue
			}
			matched[i] = true
			key := probewire.ValueKey(GNMIValuePrefix+paths[i].Name, gnmiLabels(up))
			v, sev, ok := gnmiValue(&paths[i], &u, key, store, clock(&u))
			if ok {
				values[key] = v
			}
			worst = max(worst, sev)
		}
	}
	var missing []string
	for i := range paths {
		if !matched[i] {
			missing = append(missing, paths[i].Path)
		}
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	labels := map[string]string{}
	switch {
	case worst == gnmiDown:
		r.Status, r.Code = probewire.StatusFail, probewire.WarnThresholdExceeded
	case worst == gnmiDegraded:
		r.Code = probewire.WarnThresholdExceeded
	case res.Truncated:
		r.Code = probewire.WarnTruncated
	case len(missing) > 0:
		r.Code = probewire.WarnPathMissing
	}
	if res.Truncated {
		labels[LabelGNMITruncate] = "true"
	}
	if len(missing) > 0 {
		labels[LabelGNMIPath] = strings.Join(missing, ",")
	}
	if len(labels) > 0 {
		r.Labels = labels
	}
	return r
}

// gnmiMatches reports whether the update path is the configured leaf
// path: the same element names, and every configured key with its value
// (an element without keys matches every list entry).
func gnmiMatches(want, got *gpb.Path) bool {
	if len(want.GetElem()) != len(got.GetElem()) {
		return false
	}
	for i, e := range want.GetElem() {
		g := got.GetElem()[i]
		if e.GetName() != g.GetName() {
			return false
		}
		for k, v := range e.GetKey() {
			if v != "*" && g.GetKey()[k] != v {
				return false
			}
		}
	}
	return true
}

// gnmiLabels turns the keys of the path elements into labels, e.g.
// interface[name=et-0/0/1] into name="et-0/0/1"; a key name used by two
// elements is prefixed with the element name (element_key).
func gnmiLabels(p *gpb.Path) map[string]string {
	count := map[string]int{}
	for _, e := range p.GetElem() {
		for k := range e.GetKey() {
			count[k]++
		}
	}
	labels := map[string]string{}
	for _, e := range p.GetElem() {
		for k, v := range e.GetKey() {
			name := gnmiLabelName(k)
			if count[k] > 1 {
				name = gnmiLabelName(e.GetName()) + "_" + name
			}
			labels[name] = v
		}
	}
	return labels
}

// gnmiLabelName makes a key a label name: "-" and other characters
// outside [a-zA-Z0-9_] become "_".
func gnmiLabelName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// gnmiValue evaluates one update of a path: the value to report (ok false
// for none) and its severity.
func gnmiValue(p *gnmiPath, u *gnmi.Update, series string, store *rate.Store, at time.Time) (v float64, sev int, ok bool) {
	switch p.Kind {
	case GNMIKindState:
		text := gnmiText(u)
		state, mapped := p.Enum[text]
		if !mapped {
			// Matching is case-insensitive as a convenience for
			// OpenConfig enums (UP, up).
			for k, s := range p.Enum {
				if strings.EqualFold(k, text) {
					state, mapped = s, true
					break
				}
			}
		}
		if !mapped {
			return 0, gnmiUp, false
		}
		sev = gnmiStates[state]
		return float64(sev), sev, true
	case GNMIKindCounter:
		n, isNum := gnmiUint(u)
		if !isNum {
			return 0, gnmiUp, false
		}
		r, has := store.Observe(series, n, 64, at, 0)
		if !has {
			return 0, gnmiUp, false
		}
		return r, gnmiThreshold(p, r), true
	default:
		f, isNum := u.Float()
		if !isNum || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, gnmiUp, false
		}
		return f, gnmiThreshold(p, f), true
	}
}

// gnmiThreshold is the severity of a value against warn and crit.
func gnmiThreshold(p *gnmiPath, v float64) int {
	over := func(limit float64) bool {
		if p.Direction == "below" {
			return v < limit
		}
		return v > limit
	}
	switch {
	case p.Crit != nil && over(*p.Crit):
		return gnmiDown
	case p.Warn != nil && over(*p.Warn):
		return gnmiDegraded
	}
	return gnmiUp
}

// gnmiText returns the value as text (string, enum or JSON string).
func gnmiText(u *gnmi.Update) string {
	switch v := u.Value.GetValue().(type) {
	case *gpb.TypedValue_StringVal:
		return v.StringVal
	case *gpb.TypedValue_JsonIetfVal:
		return gnmiJSONText(v.JsonIetfVal)
	case *gpb.TypedValue_JsonVal:
		return gnmiJSONText(v.JsonVal)
	case *gpb.TypedValue_BoolVal:
		return strconv.FormatBool(v.BoolVal)
	}
	if f, ok := u.Float(); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

func gnmiJSONText(b []byte) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		// OpenConfig identities may carry their module prefix.
		if _, after, ok := strings.Cut(s, ":"); ok {
			return after
		}
		return s
	}
	return strings.TrimSpace(string(b))
}

// gnmiUint returns a counter reading exactly (64-bit counters arrive as
// uint_val or as a JSON string).
func gnmiUint(u *gnmi.Update) (uint64, bool) {
	switch v := u.Value.GetValue().(type) {
	case *gpb.TypedValue_UintVal:
		return v.UintVal, true
	case *gpb.TypedValue_IntVal:
		if v.IntVal < 0 {
			return 0, false
		}
		return uint64(v.IntVal), true
	case *gpb.TypedValue_JsonIetfVal:
		return gnmiJSONUint(v.JsonIetfVal)
	case *gpb.TypedValue_JsonVal:
		return gnmiJSONUint(v.JsonVal)
	}
	f, ok := u.Float()
	if !ok || f < 0 || f > math.MaxUint64 {
		return 0, false
	}
	return uint64(f), true
}

func gnmiJSONUint(b []byte) (uint64, bool) {
	text := strings.Trim(strings.TrimSpace(string(b)), `"`)
	n, err := strconv.ParseUint(text, 10, 64)
	return n, err == nil
}
