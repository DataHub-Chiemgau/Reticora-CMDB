package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// StateFile is the file of the per-probe counters in the spool directory.
const StateFile = "probe-state"

// PersistInterval is how often the counters are saved (WP-952 AK6); they
// are also saved when the state machine stops.
const PersistInterval = 30 * time.Second

// Threshold parameters evaluated by the state machine for every type
// (E-177): a successful check with an RTT above rtt_warn_ms or a loss of at
// least loss_warn counts as success with a violated threshold.
const (
	ThresholdRTTWarnMS = "rtt_warn_ms"
	ThresholdLossWarn  = "loss_warn"
)

// ProbeCounters is the evaluated state of one probe with its counters.
// ConsecutiveFail and ConsecutiveOK count failures and successes in a row
// (a success with a warn code counts as success); ConsecutiveWarn and
// ConsecutiveClean count the successes with and without a violated
// threshold in a row.
type ProbeCounters struct {
	State            probewire.State `json:"state"`
	Reason           string          `json:"reason,omitempty"`
	ConsecutiveFail  int             `json:"consecutive_fail"`
	ConsecutiveOK    int             `json:"consecutive_ok"`
	ConsecutiveWarn  int             `json:"consecutive_warn"`
	ConsecutiveClean int             `json:"consecutive_clean"`
}

// StateMachine evaluates the results of the probes of a collector
// (PRB-05, E-177):
//
//   - unknown → up after up_count successes in a row, up/degraded → down
//     and unknown → down after down_count failures in a row, down → up
//     after up_count successes in a row;
//   - degraded is a success with a violated threshold (rtt_warn_ms,
//     loss_warn or a warn code of probewire.WarnCodes): up → degraded
//     after down_count such successes in a row, degraded → up after
//     up_count successes without violation in a row; leaving down or
//     unknown with a violated threshold leads to degraded;
//   - an error result (unsupported, limit_exceeded, icmp_unavailable,
//     auth_failed, config, …) leads to unknown with its code as reason,
//     never to down, and resets the counters.
//
// Every change yields a probewire.StateChange with seq from the stream
// state, the epoch of the assignment, observed_at in UTC and the last
// result; every result gets seq from the stream results. The counters are
// kept in <dir>/probe-state and survive a restart (WP-952 AK6).
//
// Deriving "ok with a warn code → degraded" lives only here; probe type
// work packages add their codes in edgecore/probewire/reason.go.
//
// StateMachine is safe for concurrent use.
type StateMachine struct {
	dir string
	seq *Sequence

	saveMu sync.Mutex // orders the saves: a newer snapshot is never overwritten by an older one
	mu     sync.Mutex
	probes map[string]*ProbeCounters
	dirty  bool
}

// OpenStateMachine reads the saved counters from dir and uses seq for the
// sequence numbers. A missing file starts every probe at unknown.
func OpenStateMachine(dir string, seq *Sequence) (*StateMachine, error) {
	m := &StateMachine{dir: dir, seq: seq, probes: map[string]*ProbeCounters{}}
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return m, nil
	case err != nil:
		return nil, fmt.Errorf("probe: read state: %w", err)
	}
	if err := json.Unmarshal(data, &m.probes); err != nil {
		return nil, fmt.Errorf("probe: decode state: %w", err)
	}
	for id, c := range m.probes {
		if c == nil || !c.State.Valid() {
			delete(m.probes, id)
		}
	}
	return m, nil
}

// outcome is the classification of one result.
type outcome int

const (
	outcomeError outcome = iota
	outcomeFail
	outcomeWarn
	outcomeClean
)

// Observe evaluates the result of one check of spec. It returns the
// result with its sequence number (and the warn code threshold_exceeded
// when a threshold is violated and the executor set no code) and, if the
// state changed, the state change. An error is returned only when no
// sequence number could be reserved; the counters are unchanged then.
func (m *StateMachine) Observe(spec *probewire.ProbeSpec, r probewire.Result) (probewire.Result, *probewire.StateChange, error) { //nolint:gocritic // the result is returned as a modified copy
	if r.ProbeID == "" {
		r.ProbeID = spec.ID
	}
	if r.ObservedAt.IsZero() {
		r.ObservedAt = time.Now()
	}
	r.ObservedAt = r.ObservedAt.UTC()
	r.Epoch = spec.Epoch
	out := classify(spec, &r)

	m.mu.Lock()
	defer m.mu.Unlock()
	seq, err := m.seq.Next(StreamResults)
	if err != nil {
		return r, nil, err
	}
	r.Seq = seq

	prev, ok := m.probes[spec.ID]
	if !ok {
		prev = &ProbeCounters{State: probewire.StateUnknown}
	}
	next := transition(*prev, out, r.Code, spec.DownCount, spec.UpCount)
	var change *probewire.StateChange
	if next.State != prev.State || (next.State == probewire.StateUnknown && next.Reason != prev.Reason) {
		cseq, err := m.seq.Next(StreamState)
		if err != nil {
			return r, nil, err
		}
		last := r
		change = &probewire.StateChange{
			Header:     probewire.Header{SchemaVersion: probewire.SchemaVersion},
			ProbeID:    spec.ID,
			Epoch:      spec.Epoch,
			Seq:        cseq,
			From:       prev.State,
			To:         next.State,
			Reason:     next.Reason,
			ObservedAt: r.ObservedAt,
			LastResult: &last,
		}
	}
	m.probes[spec.ID] = &next
	m.dirty = true
	return r, change, nil
}

// transition applies one outcome to the counters (WP-952 AK1–AK3).
func transition(c ProbeCounters, out outcome, code string, downCount, upCount int) ProbeCounters {
	if downCount < 1 {
		downCount = 1
	}
	if upCount < 1 {
		upCount = 1
	}
	switch out {
	case outcomeError:
		return ProbeCounters{State: probewire.StateUnknown, Reason: code}
	case outcomeFail:
		c.ConsecutiveFail++
		c.ConsecutiveOK, c.ConsecutiveWarn, c.ConsecutiveClean = 0, 0, 0
	case outcomeWarn:
		c.ConsecutiveOK++
		c.ConsecutiveWarn++
		c.ConsecutiveFail, c.ConsecutiveClean = 0, 0
	case outcomeClean:
		c.ConsecutiveOK++
		c.ConsecutiveClean++
		c.ConsecutiveFail, c.ConsecutiveWarn = 0, 0
	}
	success := func() (probewire.State, string) {
		if out == outcomeWarn {
			return probewire.StateDegraded, code
		}
		return probewire.StateUp, ""
	}
	switch c.State {
	case probewire.StateUp:
		switch {
		case c.ConsecutiveFail >= downCount:
			c.State, c.Reason = probewire.StateDown, code
		case c.ConsecutiveWarn >= downCount:
			c.State, c.Reason = probewire.StateDegraded, code
		}
	case probewire.StateDegraded:
		switch {
		case c.ConsecutiveFail >= downCount:
			c.State, c.Reason = probewire.StateDown, code
		case c.ConsecutiveClean >= upCount:
			c.State, c.Reason = probewire.StateUp, ""
		}
	case probewire.StateDown:
		if c.ConsecutiveOK >= upCount {
			c.State, c.Reason = success()
		}
	default: // unknown
		switch {
		case c.ConsecutiveFail >= downCount:
			c.State, c.Reason = probewire.StateDown, code
		case c.ConsecutiveOK >= upCount:
			c.State, c.Reason = success()
		}
	}
	return c
}

// classify sorts a result into error, fail, success with violated
// threshold (warn) and success. A success whose RTT or loss violates a
// threshold of the spec gets the warn code threshold_exceeded when the
// executor set no code.
func classify(spec *probewire.ProbeSpec, r *probewire.Result) outcome {
	switch r.Status {
	case probewire.StatusError:
		if r.Code == "" {
			r.Code = probewire.ReasonConfig
		}
		return outcomeError
	case probewire.StatusFail:
		if r.Code == "" {
			r.Code = probewire.ReasonTimeout
		}
		return outcomeFail
	case probewire.StatusOK:
	default:
		r.Status, r.Code = probewire.StatusError, probewire.ReasonConfig
		return outcomeError
	}
	if probewire.IsWarnCode(r.Code) {
		return outcomeWarn
	}
	if thresholdViolated(spec, r) {
		if r.Code == "" {
			r.Code = probewire.WarnThresholdExceeded
		}
		return outcomeWarn
	}
	return outcomeClean
}

// thresholdViolated checks rtt_warn_ms and loss_warn of the spec
// (thresholds before params, then the default of the type) against the
// result.
func thresholdViolated(spec *probewire.ProbeSpec, r *probewire.Result) bool {
	if r.RTTMS != nil {
		if v, ok := thresholdOf(spec, ThresholdRTTWarnMS); ok && *r.RTTMS > v {
			return true
		}
	}
	if r.Loss != nil {
		if v, ok := thresholdOf(spec, ThresholdLossWarn); ok && *r.Loss >= v {
			return true
		}
	}
	return false
}

func thresholdOf(spec *probewire.ProbeSpec, name string) (float64, bool) {
	for _, raw := range []json.RawMessage{spec.Thresholds, spec.Params} {
		if v, ok := numberIn(raw, name); ok {
			return v, true
		}
	}
	if t, ok := probewire.Lookup(spec.Type); ok {
		if v, ok := t.ParamDefault(name); ok {
			if f, ok := v.(float64); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func numberIn(raw json.RawMessage, name string) (float64, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, false
	}
	v, ok := m[name]
	if !ok {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return 0, false
	}
	return f, true
}

// Counters returns the state and counters of a probe.
func (m *StateMachine) Counters(id string) (ProbeCounters, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.probes[id]
	if !ok {
		return ProbeCounters{}, false
	}
	return *c, true
}

// Remove forgets a probe that is no longer assigned.
func (m *StateMachine) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.probes[id]; ok {
		delete(m.probes, id)
		m.dirty = true
	}
}

// Save writes the counters atomically to <dir>/probe-state when they
// changed since the last save.
func (m *StateMachine) Save() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	if !m.dirty {
		m.mu.Unlock()
		return nil
	}
	data, err := json.Marshal(m.probes)
	m.dirty = false
	m.mu.Unlock()
	if err == nil {
		err = writeFileAtomic(m.dir, StateFile, data)
	}
	if err != nil {
		m.mu.Lock()
		m.dirty = true
		m.mu.Unlock()
		return fmt.Errorf("probe: save state: %w", err)
	}
	return nil
}

// RunPersist saves the counters every PersistInterval and once more when
// ctx ends (WP-952 AK6). It returns the error of the final save.
func (m *StateMachine) RunPersist(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = PersistInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return m.Save()
		case <-t.C:
			_ = m.Save() // retried at the next tick and at the end
		}
	}
}
