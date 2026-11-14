package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	// The probe types of the subpackages of collector/probe register
	// themselves there (E-183, WP-956 AK6).
	_ "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/all"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The assignment set of the monitoring probes (PRB-11, WP-956): the
// collector fetches it on collector.<org>.<id>.probes.request when its
// session connects, on every push .probes.changed and every probesRefresh,
// with the version it holds (if_none_match). It reads every page of one
// version before it applies the set, saves it atomically below the spool
// directory and hands the diff to the probe engine; a start without the
// server goes on with the saved set and its epochs.
const (
	// probesFile is the saved set below the spool directory.
	probesFile = "probes.json"
	// probesFetchAttempts bounds the restarts after version_changed.
	probesFetchAttempts = 5
)

// probesRefresh is the interval of the periodic fetch (AK1); tests shorten
// it.
var probesRefresh = 60 * time.Second

// probeApplier takes an assignment set (probe.Engine).
type probeApplier interface {
	Apply(version int64, probes []probewire.ProbeSpec) probe.ApplyStats
}

// probeAssignment holds the active assignment set of the collector.
type probeAssignment struct {
	mu          sync.Mutex
	engine      probeApplier
	path        string
	active      *probewire.AssignmentSet
	deprecation *envPollDeprecation
	logger      *slog.Logger
	// traps takes the trap configuration of every applied set (WP-977).
	traps interface {
		apply(cfg *probewire.TrapConfig)
	}
}

// collectorProbes is the assignment set of the running collector; run
// attaches the probe engine before the NATS session starts.
var collectorProbes = &probeAssignment{traps: collectorTraps}

func (a *probeAssignment) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// attach binds the engine and the spool directory and applies the saved
// set (AK2): a start without the server checks with its probes and epochs.
// A saved set that cannot be read or has an unknown major version is
// logged and ignored; the engine then waits for the server.
func (a *probeAssignment) attach(engine probeApplier, spoolDir string, deprecation *envPollDeprecation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.engine, a.deprecation = engine, deprecation
	a.path = ""
	if spoolDir != "" {
		a.path = filepath.Join(spoolDir, probesFile)
	}
	set, err := loadProbeSet(a.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		a.log().Warn("saved probe assignment set not used", "event", "collector.probes.saved_invalid",
			"path", a.path, "error", err)
		return
	}
	a.applyLocked(set)
	a.log().Info("probe assignment set restored from the spool", "event", "collector.probes.restored",
		"config_version", set.Version, "probes", len(set.Probes))
}

// attached reports whether an engine takes the sets.
func (a *probeAssignment) attached() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.engine != nil
}

// version is the version of the active set (0: none).
func (a *probeAssignment) version() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == nil {
		return 0
	}
	return a.active.Version
}

// versionFor is the version of the active set when it belongs to the
// collector id (0: none or a set of another collector). The versions count
// per collector from 0, so a set saved under an earlier identity (a new
// enrollment with the same spool directory) must not answer for the
// current one (PRB-11).
func (a *probeAssignment) versionFor(collectorID string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == nil || !sameCollector(a.active.CollectorID, collectorID) {
		return 0
	}
	return a.active.Version
}

// sameCollector compares two collector ids; an empty id is never the same.
func sameCollector(x, y string) bool {
	return x != "" && strings.EqualFold(x, y)
}

// subjectCollectorID is the collector id of the subject tree
// collector.<org>.<id>.
func subjectCollectorID(subject string) string {
	return subject[strings.LastIndex(subject, ".")+1:]
}

// adopt saves a complete set and applies it. A set of the version already
// active for the same collector changes nothing.
func (a *probeAssignment) adopt(set *probewire.AssignmentSet) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != nil && a.active.Version == set.Version && sameCollector(a.active.CollectorID, set.CollectorID) {
		return
	}
	if a.path != "" {
		if err := saveProbeSet(a.path, set); err != nil {
			// The set is applied all the same; a restart without the
			// server then starts with the previous one.
			a.log().Warn("probe assignment set not saved", "event", "collector.probes.save_failed", "error", err)
		}
	}
	a.applyLocked(set)
}

func (a *probeAssignment) applyLocked(set *probewire.AssignmentSet) {
	a.active = set
	if a.traps != nil {
		// The trap receiver follows every set without a restart (DIS-06,
		// WP-977); a set without trap_config keeps the environment.
		a.traps.apply(set.TrapConfig)
	}
	if a.engine == nil {
		return
	}
	st := a.engine.Apply(set.Version, set.Probes)
	a.log().Info("probe assignment set applied", "event", "collector.probes.applied", "config_version", set.Version,
		"probes", len(set.Probes), "added", st.Added, "changed", st.Changed, "removed", st.Removed,
		"skipped", len(set.Skipped))
	if a.deprecation != nil {
		// The environment metric poll is deprecated once the collector
		// holds probes (MON-03, WP-976 AK6).
		a.deprecation.observe(set.Version, len(set.Probes))
	}
}

// loadProbeSet reads the saved set; an unknown major version is
// probewire.ErrUnsupportedVersion.
func loadProbeSet(path string) (*probewire.AssignmentSet, error) {
	if path == "" {
		return nil, fs.ErrNotExist
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the spool directory of the collector
	if err != nil {
		return nil, err
	}
	var set probewire.AssignmentSet
	if err := probewire.Decode(raw, &set); err != nil {
		return nil, err
	}
	return &set, nil
}

// saveProbeSet writes the set atomically: a temporary file in the same
// directory, fsync, rename and fsync of the directory.
func saveProbeSet(path string, set *probewire.AssignmentSet) error {
	raw, err := probewire.Encode(set)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-probes-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(raw); err == nil {
			err = tmp.Sync()
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	if d, derr := os.Open(dir); derr == nil { //nolint:gosec // the spool directory of the collector
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// probesReply is what the fields of every answer to .probes.request tell
// apart: a page (schema_version), not_modified or a refusal (status).
type probesReply struct {
	SchemaVersion string `json:"schema_version"`
	NotModified   bool   `json:"not_modified"`
	Status        int    `json:"status"`
	Error         string `json:"error"`
}

// errProbeVersionChanged: the version changed while the pages were read.
var errProbeVersionChanged = errors.New("assignment set changed while it was read")

// fetchProbeSet fetches the complete set (AK1). have is the version the
// collector holds; nil without an error means not modified. A
// version_changed answer discards the pages read so far and starts again
// with page 1, at most probesFetchAttempts times.
func fetchProbeSet(ctx context.Context, nc *nats.Conn, subject string, have int64) (*probewire.AssignmentSet, error) {
	for range probesFetchAttempts {
		set, err := fetchProbePages(ctx, nc, subject, have)
		if errors.Is(err, errProbeVersionChanged) {
			continue
		}
		return set, err
	}
	return nil, fmt.Errorf("%w on %d attempts", errProbeVersionChanged, probesFetchAttempts)
}

// fetchProbePages reads the pages of one version.
func fetchProbePages(ctx context.Context, nc *nats.Conn, subject string, have int64) (*probewire.AssignmentSet, error) {
	var merged *probewire.AssignmentSet
	page := 1
	for {
		req := probewire.AssignmentRequest{Page: page}
		if merged == nil {
			req.IfNoneMatch = have
		} else {
			req.Version = merged.Version
		}
		body, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, natsRequestTimeout)
		msg, err := nc.RequestWithContext(rctx, subject+".probes.request", body)
		cancel()
		if err != nil {
			return nil, err
		}
		var head probesReply
		if err := json.Unmarshal(msg.Data, &head); err != nil {
			return nil, fmt.Errorf("assignment set reply: %w", err)
		}
		switch {
		case head.NotModified:
			return nil, nil
		case head.SchemaVersion == "" && head.Status != 0:
			return nil, fmt.Errorf("assignment set refused: status %d: %s", head.Status, head.Error)
		}
		var set probewire.AssignmentSet
		if err := probewire.Decode(msg.Data, &set); err != nil {
			return nil, err
		}
		if set.VersionChanged || (merged != nil && set.Version != merged.Version) {
			return nil, errProbeVersionChanged
		}
		if merged == nil {
			merged = &set
		} else {
			merged.Probes = append(merged.Probes, set.Probes...)
		}
		if set.NextPage == 0 {
			merged.Page, merged.NextPage = 1, 0
			if merged.Probes == nil {
				merged.Probes = []probewire.ProbeSpec{}
			}
			return merged, nil
		}
		if set.NextPage <= page {
			return nil, fmt.Errorf("assignment set page %d names next page %d", page, set.NextPage)
		}
		page = set.NextPage
	}
}

// syncProbes keeps the assignment set of a until ctx ends (AK1): a fetch
// when the session starts, on every .probes.changed and every
// probesRefresh. Without an attached engine (no spool directory) it does
// nothing.
func syncProbes(ctx context.Context, nc *nats.Conn, subject string, a *probeAssignment) {
	if !a.attached() {
		return
	}
	kick := make(chan struct{}, 1)
	sub, err := nc.Subscribe(subject+".probes.changed", func(*nats.Msg) {
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	if err != nil {
		a.log().Warn("probe assignment changes not subscribed; fetched every "+probesRefresh.String(), "error", err)
	} else {
		defer func() { _ = sub.Unsubscribe() }()
	}
	ticker := time.NewTicker(probesRefresh)
	defer ticker.Stop()
	for {
		refreshProbes(ctx, nc, subject, a)
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-ticker.C:
		}
	}
}

// refreshProbes fetches the set once; the active set stays on every
// failure, an unknown major version included (AK5).
//
// The set held is offered as if_none_match only when it belongs to the
// collector of subject: a set of another collector (a saved set of an
// earlier enrollment) is fetched anew and replaced.
func refreshProbes(ctx context.Context, nc *nats.Conn, subject string, a *probeAssignment) {
	id := subjectCollectorID(subject)
	have := a.versionFor(id)
	if have == 0 && a.version() != 0 {
		a.log().Warn("probe assignment set of another collector replaced", "event", "collector.probes.foreign_set",
			"config_version", a.version())
	}
	set, err := fetchProbeSet(ctx, nc, subject, have)
	switch {
	case errors.Is(err, probewire.ErrUnsupportedVersion):
		a.log().Error("probe assignment set refused: unknown schema version; the active set stays",
			"event", "collector.probes.version_refused", "config_version", a.version(), "error", err)
	case err != nil:
		if ctx.Err() == nil {
			a.log().Warn("probe assignment set not updated", "event", "collector.probes.fetch_failed", "error", err)
		}
	case set != nil:
		a.adopt(set)
	}
}
