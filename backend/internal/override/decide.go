package override

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ErrNotFound reports that a field has no provenance or override row yet.
var ErrNotFound = errors.New("not found")

// SourceRanks is the rank table of REC-03: a source may replace a value only
// with a rank at least that of the value's source. IPMI ranks with Redfish
// (E-32). Unknown sources rank 0 and only fill empty values.
var SourceRanks = map[string]int{
	"manual":          100,
	"override":        100,
	"manual_override": 100,
	"import":          95,
	"workflow":        92,
	"redfish":         90,
	"ipmi":            90,
	"agent":           85,
	"snmp":            80,
	"api":             75,
	"integration":     75,
	"wmi":             70,
	"ssh":             60,
	"nas":             60,
	"sweep":           20,
}

// SourceRank returns the REC-03 rank of source.
func SourceRank(source string) int {
	return SourceRanks[source]
}

// Reasons of a write decision.
const (
	ReasonAllowed          = "allowed"
	ReasonEmpty            = "empty"
	ReasonOverride         = "override"
	ReasonOverrideConflict = "override_conflict"
	ReasonLowerRank        = "lower_rank"
	ReasonStale            = "stale"
	ReasonError            = "error"
)

// Decision is the outcome of DecideAutomatedWrite.
type Decision struct {
	// Write permits the automated source to write the value.
	Write bool
	// Conflict is set when the value differs from a manual override: the
	// caller queues an override_conflict review instead of writing (REC-12).
	Conflict bool
	Reason   string
}

// Write describes one value an automation source wants to write.
type Write struct {
	OrganizationID string
	CIID           string
	Field          string
	Value          any
	Source         string
	ObservedAt     time.Time
	// Current is the value the CI carries now; empty values may be filled
	// by any source (REC-05).
	Current any
	// FallbackSource is the source of the current value when the field has
	// no provenance row, usually the CI's discovery_source.
	FallbackSource string
}

// FieldReader reads the provenance and override of a field.
type FieldReader interface {
	Get(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error)
}

var decisionFailures atomic.Int64

// DecisionFailures returns how many decisions failed closed because the
// provenance or override could not be read.
func DecisionFailures() int64 { return decisionFailures.Load() }

// RegisterMetrics exports the failure counter as
// reticora_override_decision_failures_total.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "override_decision_failures_total",
		Help:      "Automated field writes refused because provenance or overrides could not be read.",
	}, func() float64 { return float64(DecisionFailures()) }))
}

// DecideAutomatedWrite is the only place that decides whether an automation
// source (discovery, import, workflow, agent, integration) may write a CI
// field (REC-03, REC-05, REC-12, OVR-01, CH9):
//
//   - It fails closed: when the field's provenance or override cannot be
//     read, nothing is written and the failure is counted.
//   - A manual override is never overwritten, by any source. A differing
//     value is a conflict for the review queue; an equal one needs no write.
//   - An empty current value may be filled by any source.
//   - Otherwise the source's rank must be at least that of the source of the
//     current value, and the observation must not be older than the stored
//     one of the same or a higher-ranked source.
func DecideAutomatedWrite(ctx context.Context, reader FieldReader, w *Write) Decision {
	var fv *FieldValue
	if reader != nil {
		var err error
		fv, err = reader.Get(ctx, w.OrganizationID, w.CIID, w.Field)
		switch {
		case errors.Is(err, ErrNotFound):
			fv = nil
		case err != nil:
			decisionFailures.Add(1)
			slog.WarnContext(ctx, "automated write refused: provenance unreadable",
				"ci_id", w.CIID, "field", w.Field, "source", w.Source, "error", err)
			return Decision{Reason: ReasonError}
		}
	}

	if fv != nil && fv.OverrideAt != nil {
		if valuesEqual(fv.OverrideValue, w.Value) {
			return Decision{Reason: ReasonOverride}
		}
		return Decision{Conflict: true, Reason: ReasonOverrideConflict}
	}
	if isEmpty(w.Current) {
		return Decision{Write: true, Reason: ReasonEmpty}
	}

	currentSource, currentAt := w.FallbackSource, (*time.Time)(nil)
	if fv != nil && fv.DiscoveredSource != "" {
		currentSource, currentAt = fv.DiscoveredSource, fv.DiscoveredAt
	}
	if SourceRank(w.Source) < SourceRank(currentSource) {
		return Decision{Reason: ReasonLowerRank}
	}
	// Ranks are equal here at most: an observation of the same rank must not
	// be older than the stored one.
	if currentAt != nil && SourceRank(currentSource) >= SourceRank(w.Source) && !w.ObservedAt.IsZero() && w.ObservedAt.Before(*currentAt) {
		return Decision{Reason: ReasonStale}
	}
	return Decision{Write: true, Reason: ReasonAllowed}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}
