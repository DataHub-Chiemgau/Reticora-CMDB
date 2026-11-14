// Package resultsink is the registry of the result sinks of the probe
// ingest (WP-1064 AK4): a package that evaluates the values of the results
// of a probe type (the PTR check of ipam for dns, later bgp and rpki_roa)
// registers a Sink here in its init(), and the ingest worker
// (probe.PGIngestStore) calls every sink of a type once per batch with the
// results it really wrote, in the tenant transaction of the batch. The
// registry lives in a package of its own because the probe package imports
// ipam (managed probes); probe.RegisterResultSink delegates to Register.
package resultsink

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Result is one written probe result as a sink sees it.
type Result struct {
	ProbeID    string
	CIID       string
	ManagedBy  string
	ManagedKey string
	ObservedAt time.Time
	// Status is the wire status (ok, fail, error), Code the reason code.
	Status string
	Code   string
	Values map[string]float64
	Labels map[string]string
}

// Sink evaluates the written results of one probe type of a batch in tx
// (the tenant transaction of the organization). An error rolls the batch
// back, so it is delivered again; a repeated delivery writes no result and
// reaches no sink.
type Sink interface {
	ApplyResults(ctx context.Context, tx pgx.Tx, orgID string, results []Result) error
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(ctx context.Context, tx pgx.Tx, orgID string, results []Result) error

// ApplyResults implements Sink.
func (f SinkFunc) ApplyResults(ctx context.Context, tx pgx.Tx, orgID string, results []Result) error {
	return f(ctx, tx, orgID, results)
}

var (
	mu    sync.RWMutex
	sinks = map[string][]Sink{}
)

// Register adds sink for the results of probeType.
func Register(probeType string, sink Sink) {
	mu.Lock()
	defer mu.Unlock()
	sinks[probeType] = append(sinks[probeType], sink)
}

// For returns the sinks of probeType in the order of their registration.
func For(probeType string) []Sink {
	mu.RLock()
	defer mu.RUnlock()
	return append([]Sink(nil), sinks[probeType]...)
}

// Types returns the probe types with a sink, sorted.
func Types() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(sinks))
	for t := range sinks {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
