package probe

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The metric bridge (MON-02, MON-03, WP-976, E-179 option 1): the values
// table of a probe result (probewire.Result.values) becomes samples of
// metric_sample with the CI of the probe, written by the ingest in the
// transaction of the results (ingest_repository.go) for the results that
// insert actually wrote. A value name the type does not declare
// (probewire.DeclaresMetric) is dropped and counted, as are the values of a
// probe without CI and of a result with a stale epoch. The labels are
// kind=probe, probe_id and the labels of the result (at most
// MaxBridgeLabels); the time is observed_at of the result.

// MaxBridgeLabels bounds the labels of one sample.
const MaxBridgeLabels = 16

// Label keys and values of a bridged sample.
const (
	LabelKind      = "kind"
	LabelKindProbe = "probe"
	LabelProbeID   = "probe_id"
)

// Reasons of probe_metrics_discarded_total.
const (
	BridgeUnknownMetric = "unknown_metric"
	BridgeNoCI          = "no_ci"
	BridgeStaleEpoch    = "stale_epoch"
)

var bridgeDiscarded = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "reticora", Name: "probe_metrics_discarded_total",
	Help: "Probe result values the metric bridge did not store, by reason (unknown_metric, no_ci, stale_epoch).",
}, []string{"reason"})

// BridgeDiscarded returns the counter of dropped values of reason (tests).
func BridgeDiscarded(reason string) prometheus.Counter {
	return bridgeDiscarded.WithLabelValues(reason)
}

// BridgeProbe is what the bridge needs of the probe of a result.
type BridgeProbe struct {
	ID, Type, CIID string
}

// MetricsBridge maps result values to metric samples.
type MetricsBridge struct{}

// Samples returns the samples of the result r of probe p in organization
// orgID; stale marks a result of another epoch or collector than the
// assignment.
func (MetricsBridge) Samples(orgID string, p *BridgeProbe, r *probewire.Result, stale bool) []monitoring.Metric {
	if len(r.Values) == 0 {
		return nil
	}
	switch {
	case p.CIID == "":
		bridgeDiscarded.WithLabelValues(BridgeNoCI).Add(float64(len(r.Values)))
		return nil
	case stale:
		bridgeDiscarded.WithLabelValues(BridgeStaleEpoch).Add(float64(len(r.Values)))
		return nil
	}
	names := make([]string, 0, len(r.Values))
	for name := range r.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	labels := bridgeLabels(p.ID, r.Labels)
	out := make([]monitoring.Metric, 0, len(names))
	for _, key := range names {
		name, keyLabels, err := probewire.ParseValueKey(key)
		if err != nil || !probewire.DeclaresMetric(p.Type, name) {
			bridgeDiscarded.WithLabelValues(BridgeUnknownMetric).Inc()
			continue
		}
		ls := labels
		if len(keyLabels) > 0 {
			ls = bridgeKeyLabels(p.ID, keyLabels, r.Labels)
		}
		out = append(out, monitoring.Metric{OrgID: orgID, CIID: p.CIID, Name: name, Value: r.Values[key], Labels: ls,
			Timestamp: r.ObservedAt.UTC()})
	}
	return out
}

// bridgeKeyLabels are the labels of a labeled value: kind=probe,
// probe_id, the labels of the key and then those of the result, at most
// MaxBridgeLabels in all.
func bridgeKeyLabels(probeID string, key, extra map[string]string) map[string]string {
	out := bridgeLabels(probeID, nil)
	for _, src := range []map[string]string{key, extra} {
		keys := make([]string, 0, len(src))
		for k := range src {
			if _, taken := out[k]; !taken && k != LabelKind && k != LabelProbeID {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(out) >= MaxBridgeLabels {
				return out
			}
			out[k] = src[k]
		}
	}
	return out
}

// bridgeLabels are kind=probe, probe_id and the labels of the result in
// key order, at most MaxBridgeLabels in all.
func bridgeLabels(probeID string, extra map[string]string) map[string]string {
	out := map[string]string{LabelKind: LabelKindProbe, LabelProbeID: probeID}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if k != LabelKind && k != LabelProbeID {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= MaxBridgeLabels {
			break
		}
		out[k] = extra[k]
	}
	return out
}
