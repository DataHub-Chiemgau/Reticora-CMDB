// Package probewire is the wire contract between collector and server for
// monitoring probes (PRB-05, PRB-07, PRB-11): probe specifications, the
// paginated assignment set, check results, state changes and the monitoring
// alive message.
//
// Ordering and business time follow E-175: messages are ordered by
// (epoch, seq), never by wall-clock time. The epoch is a fencing token per
// probe (ProbeSpec.Epoch), and per probe and epoch exactly one collector is
// responsible; the order of a probe's messages is therefore (epoch, seq)
// per probe. Seq is one counter per collector, shared by Result and
// StateChange, strictly increasing and persisted with the collector's spool
// so that it keeps rising across a restart (it never starts at 0 again).
// The idempotency key is (probe_id, epoch, seq), applied separately to
// results and to state changes. The business time of a result or state
// change is observed_at, the UTC instant of the measurement on the
// collector. The server measures the collector's clock offset through
// Alive.CollectorTime and corrects observed_at when the offset is too large;
// the order stays the one given by (epoch, seq).
//
// Sender identity: the server takes the collector identity only from the
// authenticated edge connection (mTLS certificate and its subject prefix),
// never from a payload field. A collector_id in a payload is informational;
// the server rejects a message whose collector_id differs from the
// authenticated identity, and it accepts results and state changes only for
// probes assigned to that collector with that epoch.
//
// The package is shared by collector and backend and imports neither of them.
package probewire

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is the version every message carries (<major>.<minor>).
// A reader accepts every minor version of its major version and ignores
// unknown fields; a different major version is rejected.
const SchemaVersion = "1.0"

// schemaMajor is the major version this package reads.
const schemaMajor = 1

// ErrUnsupportedVersion is returned by Decode for a missing, malformed or
// unknown major schema version.
var ErrUnsupportedVersion = errors.New("probewire: unsupported schema version")

// State is the state of a probe as evaluated by the collector (PRB-05).
type State string

// Probe states.
const (
	StateUp       State = "up"
	StateDegraded State = "degraded"
	StateDown     State = "down"
	StateUnknown  State = "unknown"
)

// States lists every valid probe state.
var States = []State{StateUp, StateDegraded, StateDown, StateUnknown}

// Valid reports whether s is one of the probe states.
func (s State) Valid() bool {
	for _, v := range States {
		if s == v {
			return true
		}
	}
	return false
}

// Status is the outcome of a single check.
type Status string

// Result statuses: ok (check succeeded), fail (target answered wrong or not
// at all) and error (the check itself could not be executed; the reason
// code tells why, e.g. auth_failed or unsupported).
const (
	StatusOK    Status = "ok"
	StatusFail  Status = "fail"
	StatusError Status = "error"
)

// Valid reports whether s is one of the result statuses.
func (s Status) Valid() bool {
	return s == StatusOK || s == StatusFail || s == StatusError
}

// Label keys of a result. With them a result reports the access type it
// used (credential ID and namespace, never the secret); the ingest (WP-960)
// evaluates them.
const (
	LabelCredentialID        = "credential_id"
	LabelCredentialNamespace = "credential_namespace"
)

// Header carries the schema version of a message.
type Header struct {
	SchemaVersion string `json:"schema_version"`
}

func (h *Header) header() *Header { return h }

// Message is implemented by every top-level wire message.
type Message interface {
	header() *Header
}

// ProbeSpec is the effective configuration of one probe as the collector
// executes it. Epoch is the fencing token of the assignment: a collector
// checks only probes assigned to it with a valid epoch, and every result
// and state change carries the epoch it was produced under (PRB-11).
type ProbeSpec struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Target     string          `json:"target"`
	IntervalMS int64           `json:"interval_ms"`
	TimeoutMS  int64           `json:"timeout_ms"`
	DownCount  int             `json:"down_count"`
	UpCount    int             `json:"up_count"`
	Params     json.RawMessage `json:"params,omitempty"`
	Thresholds json.RawMessage `json:"thresholds,omitempty"`
	ParentID   string          `json:"parent_id,omitempty"`
	Epoch      int64           `json:"epoch"`
	// CredentialIDs name the credentials of the probe in the collector's
	// credential bundle (WP-948); never a secret.
	CredentialIDs []string `json:"credential_ids,omitempty"`
}

// Assignment set paging limits (AK6): a page holds at most MaxPageProbes
// probes and is at most MaxPageBytes large when encoded.
const (
	MaxPageProbes = 2000
	MaxPageBytes  = 768 << 10
)

// AssignmentSet is one page of the versioned assignment set of a collector.
//
// Paging: Page counts from 1; NextPage names the following page and is empty
// (0, omitted) on the last page. If the version of the set changes while the
// collector is fetching it, the server answers the next page request with
// VersionChanged set, the new Version and no probes; the collector discards
// the pages fetched so far and starts again with page 1 of the new version.
// A collector applies a set only after it fetched every page of one version.
type AssignmentSet struct {
	Header
	CollectorID    string          `json:"collector_id"`
	Version        int64           `json:"version"`
	Page           int             `json:"page"`
	NextPage       int             `json:"next_page,omitempty"`
	VersionChanged bool            `json:"version_changed,omitempty"`
	Probes         []ProbeSpec     `json:"probes"`
	TrapConfig     json.RawMessage `json:"trap_config,omitempty"`
	// Skipped are the probes assigned to the collector that the set leaves
	// out, with the reason (WP-948, e.g. SkipCredentialMissing); on the
	// first page only.
	Skipped []SkippedProbe `json:"skipped,omitempty"`
}

// SkippedProbe is a probe of the set the server did not hand out.
type SkippedProbe struct {
	ProbeID string `json:"probe_id"`
	Reason  string `json:"reason"`
}

// SkipCredentialMissing: a credential of the probe is not in the
// collector's current credential bundle (WP-948).
const SkipCredentialMissing = "credential_missing"

// AssignmentRequest is the body of .probes.request (WP-948): IfNoneMatch is
// the version the collector holds (0: none), Page the page it asks for
// (from 1) and Version the version of the pages it fetched so far (0 on
// page 1).
type AssignmentRequest struct {
	IfNoneMatch int64 `json:"if_none_match,omitempty"`
	Page        int   `json:"page,omitempty"`
	Version     int64 `json:"version,omitempty"`
}

// NotModified answers .probes.request when the collector holds the current
// version (if_none_match).
type NotModified struct {
	NotModified bool  `json:"not_modified"`
	Version     int64 `json:"version"`
}

// ProbesChanged is the push .probes.changed: the new version of the set.
type ProbesChanged struct {
	Version int64 `json:"version"`
}

// ErrProbeTooLarge is returned by Paginate when a single probe does not fit
// into a page of MaxPageBytes.
var ErrProbeTooLarge = errors.New("probewire: probe specification exceeds page size")

// Paginate splits the probes of one set version into pages that respect
// MaxPageProbes and MaxPageBytes. An empty set yields one empty page.
func Paginate(collectorID string, version int64, probes []ProbeSpec) ([]AssignmentSet, error) {
	empty, err := Encode(&AssignmentSet{CollectorID: collectorID, Version: version, Page: 1, NextPage: 1 << 30, Probes: []ProbeSpec{}})
	if err != nil {
		return nil, err
	}
	// Overhead of an empty page plus a generous allowance for the page
	// numbers and the commas between probes.
	overhead := len(empty) + 32
	var pages []AssignmentSet
	current := []ProbeSpec{}
	size := overhead
	flush := func() {
		pages = append(pages, AssignmentSet{
			Header:      Header{SchemaVersion: SchemaVersion},
			CollectorID: collectorID,
			Version:     version,
			Page:        len(pages) + 1,
			Probes:      current,
		})
		current = []ProbeSpec{}
		size = overhead
	}
	for i := range probes {
		p := &probes[i]
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		n := len(b) + 1
		if overhead+n > MaxPageBytes {
			return nil, fmt.Errorf("%w: probe %s", ErrProbeTooLarge, p.ID)
		}
		if len(current) == MaxPageProbes || size+n > MaxPageBytes {
			flush()
		}
		current = append(current, *p)
		size += n
	}
	flush()
	for i := range pages {
		if i+1 < len(pages) {
			pages[i].NextPage = i + 2
		}
	}
	return pages, nil
}

// PageOf answers a page request of a collector. pages is the current
// version of the set (from Paginate); requestedVersion is the version the
// collector fetched its earlier pages of (0 when it starts with page 1).
// A request for a page after the first of an outdated version yields a page
// with VersionChanged set and NextPage 1 of the current version.
func PageOf(pages []AssignmentSet, requestedVersion int64, page int) (AssignmentSet, bool) {
	if len(pages) == 0 || page < 1 {
		return AssignmentSet{}, false
	}
	cur := pages[0]
	if page > 1 && requestedVersion != 0 && requestedVersion != cur.Version {
		return AssignmentSet{
			Header:         Header{SchemaVersion: SchemaVersion},
			CollectorID:    cur.CollectorID,
			Version:        cur.Version,
			Page:           page,
			NextPage:       1,
			VersionChanged: true,
			Probes:         []ProbeSpec{},
		}, true
	}
	if page > len(pages) {
		return AssignmentSet{}, false
	}
	return pages[page-1], true
}

// Result is the outcome of one check. Seq is the collector-wide counter
// shared with StateChange (see the package comment); (epoch, seq) orders the
// results of a probe, and (probe_id, epoch, seq) makes a repeated
// transmission detectable (PRB-07). ObservedAt is the business time (E-175).
// A collector checks every result with Validate before it batches it, so
// that a single non-finite value cannot make the whole batch unencodable.
//
// Result has no free-text detail field (E-177): additional information of a
// probe type goes into Values (numbers) or Labels (e.g. LabelCredentialID).
type Result struct {
	ProbeID    string             `json:"probe_id"`
	Epoch      int64              `json:"epoch"`
	Seq        uint64             `json:"seq"`
	ObservedAt time.Time          `json:"observed_at"`
	Status     Status             `json:"status"`
	Code       string             `json:"code,omitempty"`
	RTTMS      *float64           `json:"rtt_ms,omitempty"`
	Loss       *float64           `json:"loss,omitempty"`
	Value      *float64           `json:"value,omitempty"`
	Values     map[string]float64 `json:"values,omitempty"`
	Labels     map[string]string  `json:"labels,omitempty"`
}

// ErrInvalidResult is returned by Result.Validate and Alive.Validate.
var ErrInvalidResult = errors.New("probewire: invalid message")

// Validate checks a result before it is batched: known status, a reason
// code that is empty or valid (ValidReason), and finite numbers only
// (json.Marshal rejects NaN and ±Inf, which would fail the whole batch).
func (r *Result) Validate() error {
	if r.ProbeID == "" {
		return fmt.Errorf("%w: probe_id is required", ErrInvalidResult)
	}
	if !r.Status.Valid() {
		return fmt.Errorf("%w: status %q", ErrInvalidResult, r.Status)
	}
	if r.Code != "" && !ValidReason(r.Code) {
		return fmt.Errorf("%w: code %q", ErrInvalidResult, r.Code)
	}
	for _, f := range []struct {
		name string
		v    *float64
	}{{"rtt_ms", r.RTTMS}, {"loss", r.Loss}, {"value", r.Value}} {
		if f.v != nil && !finite(*f.v) {
			return fmt.Errorf("%w: %s is not finite", ErrInvalidResult, f.name)
		}
	}
	for k, v := range r.Values {
		if !finite(v) {
			return fmt.Errorf("%w: values[%s] is not finite", ErrInvalidResult, k)
		}
	}
	return nil
}

// ValidResults splits results into the valid ones, in their order, and the
// number of rejected ones; the collector counts rejected results as loss
// (PRB-07) instead of dropping the whole batch.
func ValidResults(results []Result) (valid []Result, rejected int) {
	valid = make([]Result, 0, len(results))
	for i := range results {
		if results[i].Validate() != nil {
			rejected++
			continue
		}
		valid = append(valid, results[i])
	}
	return valid, rejected
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// ResultBatch bundles results for one transmission (at most 10 s or 1,000
// results, PRB-05). MaxBatchResults is the upper bound; a full batch with
// typical values stays below a quarter of the 1 MiB edge message size.
// CollectorID is informational only: the server takes the sender from the
// authenticated edge identity and rejects a batch whose CollectorID differs
// from it (see the package comment).
type ResultBatch struct {
	Header
	CollectorID string   `json:"collector_id"`
	Results     []Result `json:"results"`
}

// MaxBatchResults is the maximum number of results in one ResultBatch.
const MaxBatchResults = 1000

// StateChange reports a state transition of one probe (PRB-05). It carries
// sequence number, epoch, reason, timestamp and the last result. Seq comes
// from the same collector-wide counter as Result.Seq; the server applies the
// changes of a probe in (epoch, seq) order and ignores repeats of
// (probe_id, epoch, seq). The sender is the authenticated edge identity.
type StateChange struct {
	Header
	ProbeID    string    `json:"probe_id"`
	Epoch      int64     `json:"epoch"`
	Seq        uint64    `json:"seq"`
	From       State     `json:"from"`
	To         State     `json:"to"`
	Reason     string    `json:"reason,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	LastResult *Result   `json:"last_result,omitempty"`
}

// Alive is the monitoring alive message a collector sends every 10 s
// (PRB-10): clock, configuration version, load figures and buffer state.
// CollectorTime is the collector's wall clock when sending; the server
// derives the clock offset from it (E-175, PRB-09). The sender is the
// authenticated edge identity.
type Alive struct {
	Header
	CollectorTime  time.Time `json:"collector_time"`
	ConfigVersion  int64     `json:"config_version"`
	Probes         int       `json:"probes"`
	ResultsPerS    float64   `json:"results_per_s"`
	QueueState     string    `json:"queue_state"`
	QueueResults   int64     `json:"queue_results"`
	Dropped        int64     `json:"dropped"`
	Skipped        int64     `json:"skipped"`
	ICMPCapability string    `json:"icmp_capability"`
}

// Queue states of Alive.QueueState.
const (
	QueueOK       = "ok"
	QueueBacklog  = "backlog"
	QueueOverflow = "overflow"
)

// ICMP capabilities of Alive.ICMPCapability.
const (
	ICMPRaw          = "raw"
	ICMPUnprivileged = "unprivileged"
	ICMPUnavailable  = "unavailable"
)

// Validate checks that the load figures are finite and not negative.
func (a *Alive) Validate() error {
	if !finite(a.ResultsPerS) || a.ResultsPerS < 0 {
		return fmt.Errorf("%w: results_per_s must be finite and not negative", ErrInvalidResult)
	}
	return nil
}

// Encode marshals a message, setting its schema version when unset.
func Encode(m Message) ([]byte, error) {
	if h := m.header(); h.SchemaVersion == "" {
		h.SchemaVersion = SchemaVersion
	}
	return json.Marshal(m)
}

// Decode unmarshals a message. Unknown fields are ignored; a missing or
// unknown major schema version yields ErrUnsupportedVersion.
func Decode(data []byte, m Message) error {
	var h Header
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
	major, ok := majorOf(h.SchemaVersion)
	if !ok || major != schemaMajor {
		return fmt.Errorf("%w: %q", ErrUnsupportedVersion, h.SchemaVersion)
	}
	return json.Unmarshal(data, m)
}

func majorOf(v string) (int, bool) {
	maj, minor, found := strings.Cut(v, ".")
	if !found {
		return 0, false
	}
	n, err := strconv.Atoi(maj)
	if err != nil || n < 0 {
		return 0, false
	}
	if _, err := strconv.Atoi(minor); err != nil {
		return 0, false
	}
	return n, true
}
