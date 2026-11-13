package collectorcmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The notice of the deprecated environment metric poll (MON-03, WP-976 AK6).

func deprecationLog(interval time.Duration) (*envPollDeprecation, *bytes.Buffer) {
	var buf bytes.Buffer
	cfg := &collectorConfig{MetricsInterval: interval}
	return newEnvPollDeprecation(cfg, slog.New(slog.NewTextHandler(&buf, nil))), &buf
}

func TestEnvPollDeprecationLoggedWithProbes(t *testing.T) {
	d, buf := deprecationLog(defaultMetricsInterval)
	if !d.observe(7, 3) {
		t.Fatal("no notice with the default poll and a set of 3 probes")
	}
	if d.observe(7, 3) {
		t.Error("the notice repeated for the same version")
	}
	if !d.observe(8, 3) {
		t.Error("no notice for the next version")
	}
	out := buf.String()
	if strings.Count(out, "collector.metrics.env_poll_deprecated") != 2 || !strings.Contains(out, "level=WARN") ||
		!strings.Contains(out, "snmp_if") {
		t.Errorf("log %q, want two warnings naming the probe types", out)
	}
}

func TestNoDeprecationWithoutProbes(t *testing.T) {
	d, buf := deprecationLog(defaultMetricsInterval)
	if d.observe(1, 0) {
		t.Error("notice for an empty set")
	}
	off, offBuf := deprecationLog(0)
	if off.observe(1, 5) {
		t.Error("notice with the poll switched off")
	}
	if buf.Len()+offBuf.Len() != 0 {
		t.Errorf("logged %q %q", buf.String(), offBuf.String())
	}
}
