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
	// ReasonInstanceAttribute refuses a write of an instance attribute of
	// the CI: automation never writes one, and the refusal is no conflict
	// (MET-14, CI-10, OVR-02).
	ReasonInstanceAttribute = "instance_attribute"
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

// FieldReader reads the provenance and override of a field. Get sets
// FieldValue.InstanceAttribute when the field is an instance attribute of
// the CI, also for a field without provenance row: then it returns a
// FieldValue with only the keys and that flag instead of ErrNotFound.
type FieldReader interface {
	Get(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error)
}

// decisionReader reads a field without its effective value, which the
// decision does not use (PGRepository).
type decisionReader interface {
	getForDecision(ctx context.Context, orgID, ciID, fieldName string) (*FieldValue, error)
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
//   - An instance attribute of the CI is never written, with or without
//     override; the refusal is no conflict (MET-14, OVR-02).
//   - A manual override is never overwritten, by any source. A differing
//     value is a conflict for the review queue; an equal one needs no write.
//   - An empty current value may be filled by any source.
//   - Otherwise a value wins with a higher rank than the stored value's
//     source, or with the same rank and a newer source time (observed_at,
//     REC-05, GLO-12). Provenance is compared per field.
//
// Both ranks are effective ranks (REC-04): the entry of the CI type for the
// field, else the organization-wide entry, else SourceRanks, when the
// reader keeps source ranks (rankReader); manual stays 100.
func DecideAutomatedWrite(ctx context.Context, reader FieldReader, w *Write) Decision {
	var fv *FieldValue
	var ranks *RankTable
	var ciTypeID string
	if rt, ok := reader.(rankTabler); ok {
		var err error
		if ranks, err = rt.rankTable(ctx, w.OrganizationID); err != nil {
			decisionFailures.Add(1)
			slog.WarnContext(ctx, "automated write refused: source ranks unreadable",
				"ci_id", w.CIID, "field", w.Field, "source", w.Source, "error", err)
			return Decision{Reason: ReasonError}
		}
	}
	if tr, ok := reader.(typedDecisionReader); ok {
		var err error
		if fv, ciTypeID, err = tr.decisionState(ctx, w.OrganizationID, w.CIID, w.Field); err != nil {
			decisionFailures.Add(1)
			slog.WarnContext(ctx, "automated write refused: provenance unreadable",
				"ci_id", w.CIID, "field", w.Field, "source", w.Source, "error", err)
			return Decision{Reason: ReasonError}
		}
	} else if reader != nil {
		var err error
		if dr, ok := reader.(decisionReader); ok {
			fv, err = dr.getForDecision(ctx, w.OrganizationID, w.CIID, w.Field)
		} else {
			fv, err = reader.Get(ctx, w.OrganizationID, w.CIID, w.Field)
		}
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

	if fv != nil && fv.InstanceAttribute {
		return Decision{Reason: ReasonInstanceAttribute}
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

	var currentAt *time.Time
	currentRank := ranks.Rank(ciTypeID, w.Field, w.FallbackSource)
	if fv != nil && fv.DiscoveredSource != "" {
		currentRank = storedRank(ranks, ciTypeID, w.Field, fv)
		currentAt = fv.ObservedAt
		if currentAt == nil {
			currentAt = fv.DiscoveredAt
		}
	}
	rank := ranks.Rank(ciTypeID, w.Field, w.Source)
	if rank < currentRank {
		return Decision{Reason: ReasonLowerRank}
	}
	// A source of the same rank wins only with a newer source time (REC-05,
	// GLO-12); the arrival time does not count.
	if rank == currentRank && currentAt != nil && !w.ObservedAt.After(*currentAt) {
		return Decision{Reason: ReasonStale}
	}
	return Decision{Write: true, Reason: ReasonAllowed}
}

// storedRank is the rank of the stored value's source: with a rank table
// the effective rank of a known source now (REC-04: a changed policy
// applies to stored values as well), else the rank recorded with the value
// (a source outside REC-03, e.g. computed attributes with 100), else
// SourceRanks.
func storedRank(ranks *RankTable, ciTypeID, field string, fv *FieldValue) int {
	if ranks != nil {
		if _, known := SourceRanks[fv.DiscoveredSource]; known {
			return ranks.Rank(ciTypeID, field, fv.DiscoveredSource)
		}
	}
	if fv.DiscoveredRank != 0 {
		return fv.DiscoveredRank
	}
	return SourceRank(fv.DiscoveredSource)
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

// Guard applies the central decision for one automation path and keeps the
// provenance of what it writes (REC-10). A nil Guard decides by the rank
// table alone and records nothing.
type Guard struct {
	repo Repository
}

// NewGuard returns the guard over the override repository.
func NewGuard(repo Repository) *Guard {
	return &Guard{repo: repo}
}

// Decide returns the decision for w.
func (g *Guard) Decide(ctx context.Context, w *Write) Decision {
	if g == nil || g.repo == nil {
		return DecideAutomatedWrite(ctx, nil, w)
	}
	return DecideAutomatedWrite(ctx, g.repo, w)
}

// Record stores the value of w as the field's provenance once it is written
// or observed under an override.
func (g *Guard) Record(ctx context.Context, w *Write) error {
	if g == nil || g.repo == nil {
		return nil
	}
	_, err := g.repo.RecordDiscovered(ctx, w.OrganizationID, w.CIID, w.Field, w.Value, w.Source, w.ObservedAt)
	return err
}
