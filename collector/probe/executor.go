// Package probe runs the monitoring probes of a collector (PRT-01, E-183).
//
// Every probe type has an Executor that performs one check of one probe and
// returns a probewire.Result. This file is the only register: simple types
// live flat as collector/probe/<type>.go, types with a larger scope in
// sub-packages collector/probe/<package>/; both register their executors in
// init() through Register, the sub-packages are linked in through blank
// imports in collector/probe/all/all.go.
//
// Executors hold no state per probe (WP-950 AK4). Previous values (counters,
// gauges, enum state, sysUpTime) are kept only by the previous-value store
// collector/probe/rate in the engine context.
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Executor performs one check of a probe. ctx is bounded by the effective
// timeout of the probe (spec.TimeoutMS); an executor returns at the latest
// at the deadline and then reports fail with code timeout. creds holds the
// resolved credential fields of the probe's namespaces; an executor never
// copies a secret into the result.
//
// Execute fills Status, Code and the measurements. ProbeID, Epoch and
// ObservedAt are set by Run; Seq is assigned by the engine.
type Executor interface {
	Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result

// Execute calls f.
func (f ExecutorFunc) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	return f(ctx, spec, creds)
}

// Label keys of a result rejected by the validation before execution: the
// fields (comma separated, sorted) and the first message.
const (
	LabelField   = "field"
	LabelMessage = "message"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Executor{}
)

// Register makes the executor of a probe type known. It panics on an empty
// key, a nil executor or a second registration of the same key, because
// both are programming errors of an init() function.
func Register(typ string, e Executor) {
	if typ == "" || e == nil {
		panic("probe: Register with empty type or nil executor")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[typ]; dup {
		panic(fmt.Sprintf("probe: executor for type %q registered twice", typ))
	}
	registry[typ] = e
}

// Lookup returns the executor registered for the type. The register is safe
// for concurrent reads.
func Lookup(typ string) (Executor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := registry[typ]
	return e, ok
}

// Registered returns the keys of every registered executor, sorted.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// unregister removes a registration; only for tests.
func unregister(typ string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, typ)
}

// Run executes one check of spec (WP-950 AK1, AK2):
//
//   - a type that is not registered or not available in the type register
//     yields status error with code unsupported;
//   - a spec rejected by probewire.Validate yields status error with code
//     config and the field error in the labels; the executor is not called;
//   - otherwise the executor runs with ctx bounded by spec.TimeoutMS. If it
//     does not return by the deadline, Run returns fail with code timeout
//     at the deadline.
//
// A cancellation of ctx by the caller (shutdown) yields error with code
// timeout whatever the executor returned, except ok. A panic of the
// executor yields error with code config. An error result leads to unknown
// in the state machine (WP-952), never to down.
func Run(ctx context.Context, spec *probewire.ProbeSpec, creds map[string]string) probewire.Result {
	start := time.Now()
	base := probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: start.UTC()}

	t, known := probewire.Lookup(spec.Type)
	exec, registered := Lookup(spec.Type)
	if !known || !registered || !t.Available() {
		base.Status, base.Code = probewire.StatusError, probewire.ReasonUnsupported
		return base
	}
	if errs := probewire.Validate(spec); len(errs) > 0 {
		base.Status, base.Code = probewire.StatusError, probewire.ReasonConfig
		base.Labels = fieldLabels(errs)
		return base
	}

	if spec.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	done := make(chan probewire.Result, 1)
	go func(spec probewire.ProbeSpec) {
		// A panicking executor must not end the collector process and
		// with it every probe of every tenant: the check is reported as
		// error (→ unknown). The log names only the type, never the spec
		// or the credentials.
		defer func() {
			if p := recover(); p != nil {
				slog.Error("probe executor panicked", "type", spec.Type)
				done <- probewire.Result{Status: probewire.StatusError, Code: probewire.ReasonConfig}
			}
		}()
		done <- exec.Execute(ctx, spec, creds)
	}(*spec)
	var r probewire.Result
	select {
	case r = <-done:
	case <-ctx.Done():
		// The executor overran its deadline: report the timeout now; the
		// late answer is discarded (the channel is buffered).
		r = probewire.Result{}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.Status, r.Code = probewire.StatusFail, probewire.ReasonTimeout
		} else {
			// Cancelled by the caller (shutdown): the check was not carried
			// out, which is no statement about the target, so error (→
			// unknown) instead of fail.
			r.Status, r.Code = probewire.StatusError, probewire.ReasonTimeout
		}
	}
	// A cancellation by the caller is decided here and only here: an
	// executor whose dial returned at the cancellation may have won the
	// select above with fail/timeout, which would count towards down.
	if r.Status != probewire.StatusOK && errors.Is(ctx.Err(), context.Canceled) {
		r.Status, r.Code = probewire.StatusError, probewire.ReasonTimeout
	}
	r.ProbeID, r.Epoch, r.ObservedAt = spec.ID, spec.Epoch, base.ObservedAt
	return r
}

func fieldLabels(errs []probewire.FieldError) map[string]string {
	fields := make([]string, 0, len(errs))
	seen := map[string]bool{}
	for _, e := range errs {
		if !seen[e.Field] {
			seen[e.Field] = true
			fields = append(fields, e.Field)
		}
	}
	sort.Strings(fields)
	return map[string]string{LabelField: strings.Join(fields, ","), LabelMessage: errs[0].Message}
}

// failResult is a helper for executors: a result with status and code.
func failResult(status probewire.Status, code string) probewire.Result {
	return probewire.Result{Status: status, Code: code}
}

// msSince returns the elapsed time since start in milliseconds.
func msSince(start time.Time) *float64 {
	v := float64(time.Since(start).Microseconds()) / 1000
	return &v
}
