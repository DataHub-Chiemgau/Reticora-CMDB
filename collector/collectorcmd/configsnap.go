package collectorcmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/ssh"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Configuration change detection (ALR-07, WP-1053, E-207): on its schedule
// the collector reads the running configuration of every CI the bundle
// names a credential of the namespace ssh for, masks it, hashes it and
// sends it as probewire.ConfigSnapshot on .config, only when the hash
// differs from the last one the server confirmed. Without the edge the
// newest unconfirmed state per CI waits in <SpoolDir>/config/pending/ and
// goes first after the connection. Neither a configuration nor a secret
// goes into a log; log lines name CI, platform, a hash prefix and the
// masked count.
const (
	edgeConfigSuffix      = ".config"
	defaultConfigInterval = 24 * time.Hour
	minConfigInterval     = time.Hour
	defaultConfigParallel = 4
	configStateName       = "state.json"
	configPendingName     = "pending"
	// defaultEdgeMaxPayload is the NATS default message size, which the
	// edge does not raise (deploy/docker-compose/nats-edge.conf).
	defaultEdgeMaxPayload = 1 << 20
)

// configTick is the period of the schedule check; configPause is how long
// the reads pause after the server refused the feature (tests shorten
// them).
var (
	configTick  = time.Minute
	configPause = time.Hour
)

// configIntervalFromEnv reads RETICORA_CONFIG_INTERVAL: default 24 h, 0
// turns the reads off, below 1 h is raised to 1 h.
func configIntervalFromEnv(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultConfigInterval
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		slog.Warn("invalid duration configuration", "key", "RETICORA_CONFIG_INTERVAL", "value", value,
			"fallback", defaultConfigInterval.String())
		return defaultConfigInterval
	}
	if d == 0 {
		return 0
	}
	if d < minConfigInterval {
		slog.Warn("RETICORA_CONFIG_INTERVAL below the minimum; using 1h", "key", "RETICORA_CONFIG_INTERVAL", "value", value)
		return minConfigInterval
	}
	return d
}

// configParallelFromEnv reads RETICORA_CONFIG_PARALLEL (default 4, at
// least 1).
func configParallelFromEnv(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultConfigParallel
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		slog.Warn("invalid integer configuration", "key", "RETICORA_CONFIG_PARALLEL", "value", value, "fallback", defaultConfigParallel)
		return defaultConfigParallel
	}
	return n
}

// configTarget is a device whose configuration is read: the CI of a bundle
// target with namespace ssh and its ssh credentials (without prefix).
type configTarget struct {
	CIID  string
	IP    string
	Creds map[string]string
}

// configStateEntry is the confirmed state of a CI: the hash the server
// took last and the detected platform.
type configStateEntry struct {
	SHA256   string `json:"sha256,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type configStateDoc struct {
	CIs map[string]configStateEntry `json:"cis"`
}

// configCounters are the counters of the reads.
type configCounters struct {
	Sent, Unchanged, Superseded, TooLarge, Unsupported, Failed, Discarded atomic.Int64
}

// configCollector runs the reads and holds their state.
type configCollector struct {
	dir        string
	interval   time.Duration
	parallel   int
	now        func() time.Time
	collect    func(ctx context.Context, t *configTarget, platform config.Platform) (*config.Snapshot, error)
	send       func(ctx context.Context, suffix string, data []byte) ([]byte, error)
	maxPayload func() int64
	targets    func() []configTarget
	logger     *slog.Logger

	mu          sync.Mutex
	state       map[string]configStateEntry
	next        map[string]time.Time
	running     map[string]bool
	pausedUntil time.Time
	pauseLogged bool
	flushMu     sync.Mutex

	counters configCounters
}

// newConfigCollector loads the state of dir (<SpoolDir>/config).
func newConfigCollector(dir string, interval time.Duration, parallel int) (*configCollector, error) {
	c := &configCollector{dir: dir, interval: interval, parallel: max(parallel, 1), now: time.Now,
		send: edgeRequest, maxPayload: edgeMaxPayload,
		state: map[string]configStateEntry{}, next: map[string]time.Time{}, running: map[string]bool{}}
	if err := os.MkdirAll(filepath.Join(dir, configPendingName), 0o700); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, configStateName))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var doc configStateDoc
		if jerr := json.Unmarshal(raw, &doc); jerr != nil {
			c.log().Warn("configuration state unreadable; starting without confirmed hashes", "event", "collector.config.state_invalid")
		} else if doc.CIs != nil {
			c.state = doc.CIs
		}
	}
	return c, nil
}

func (c *configCollector) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// edgeMaxPayload is the message size of the connected edge (the NATS
// default without a connection).
func edgeMaxPayload() int64 {
	if ch := scopeChannel.Load(); ch != nil && ch.nc != nil && ch.nc.IsConnected() {
		return ch.nc.MaxPayload()
	}
	return defaultEdgeMaxPayload
}

// writeFileAtomic writes data with mode 0600 through a temporary file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) //nolint:errcheck // gone after the rename
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// saveStateLocked persists the confirmed hashes and platforms.
func (c *configCollector) saveStateLocked() {
	raw, err := json.Marshal(configStateDoc{CIs: c.state})
	if err == nil {
		err = writeFileAtomic(filepath.Join(c.dir, configStateName), raw)
	}
	if err != nil {
		c.log().Error("configuration state not saved", "event", "collector.config.state_failed", "error", err)
	}
}

var safeCIID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// pendingPath is the file of the unconfirmed state of a CI.
func (c *configCollector) pendingPath(ciID string) string {
	name := ciID
	if !safeCIID.MatchString(name) {
		sum := sha256.Sum256([]byte(ciID))
		name = hex.EncodeToString(sum[:16])
	}
	return filepath.Join(c.dir, configPendingName, name+".json")
}

// hashPrefix is the part of a hash a log line may show.
func hashPrefix(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// paused reports whether the reads pause at now (feature not entitled).
func (c *configCollector) paused(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Before(c.pausedUntil)
}

// startOf returns the first start of a CI: its hash phase within the
// interval (probe.NextStart), so the reads spread over the interval and
// keep their times across restarts (WP-1053 AK4).
func startOf(ciID string, interval time.Duration, now time.Time) time.Time {
	return probe.NextStart(ciID, interval.Milliseconds(), now)
}

// run checks the schedule every configTick until ctx ends.
func (c *configCollector) run(ctx context.Context) {
	t := time.NewTicker(configTick)
	defer t.Stop()
	for {
		c.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick sends the pending states first, then reads the CIs that are due,
// at most parallel at a time, and waits for them.
func (c *configCollector) tick(ctx context.Context) {
	c.flush(ctx)
	now := c.now()
	if c.paused(now) {
		return
	}
	var due []configTarget
	c.mu.Lock()
	seen := map[string]bool{}
	for _, t := range c.targets() {
		seen[t.CIID] = true
		next, ok := c.next[t.CIID]
		if !ok {
			c.next[t.CIID] = startOf(t.CIID, c.interval, now)
			continue
		}
		if now.Before(next) || c.running[t.CIID] {
			continue
		}
		next = next.Add(c.interval)
		if !next.After(now) {
			next = startOf(t.CIID, c.interval, now.Add(time.Millisecond))
		}
		c.next[t.CIID] = next
		c.running[t.CIID] = true
		due = append(due, t)
	}
	for ci := range c.next {
		if !seen[ci] {
			delete(c.next, ci)
		}
	}
	c.mu.Unlock()

	sem := make(chan struct{}, c.parallel)
	var wg sync.WaitGroup
	for i := range due {
		t := &due[i]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			c.collectOne(ctx, t)
			c.mu.Lock()
			delete(c.running, t.CIID)
			c.mu.Unlock()
		}()
	}
	wg.Wait()
	if len(due) > 0 {
		c.flush(ctx)
	}
}

// collectOne reads one CI and puts a changed state into pending.
func (c *configCollector) collectOne(ctx context.Context, t *configTarget) {
	c.mu.Lock()
	prev := c.state[t.CIID]
	c.mu.Unlock()
	snap, err := c.collect(ctx, t, config.Platform(prev.Platform))
	switch {
	case errors.Is(err, config.ErrUnsupportedPlatform):
		c.counters.Unsupported.Add(1)
		c.log().Warn("configuration not read: unsupported platform", "event", "collector.config.unsupported_platform",
			"ci_id", t.CIID, "code", "unsupported_platform")
		return
	case errors.Is(err, config.ErrTooLarge), errors.Is(err, ssh.ErrConfigTooLarge):
		c.counters.TooLarge.Add(1)
		c.log().Warn("configuration not sent: above 2 MiB", "event", "collector.config.too_large", "ci_id", t.CIID, "code", "config_too_large")
		return
	case err != nil:
		c.counters.Failed.Add(1)
		c.log().Warn("configuration not read", "event", "collector.config.failed", "ci_id", t.CIID, "error", err)
		if prev.Platform != "" {
			// The device behind the CI may have been replaced: the next
			// read detects the platform again instead of sending the
			// command of the old one for ever.
			c.mu.Lock()
			e := c.state[t.CIID]
			e.Platform = ""
			c.state[t.CIID] = e
			c.saveStateLocked()
			c.mu.Unlock()
		}
		return
	}
	c.mu.Lock()
	if prev.Platform != string(snap.Platform) {
		e := c.state[t.CIID]
		e.Platform = string(snap.Platform)
		c.state[t.CIID] = e
		c.saveStateLocked()
	}
	confirmed := c.state[t.CIID].SHA256
	c.mu.Unlock()

	pending := c.pendingPath(t.CIID)
	if snap.SHA256 == confirmed {
		c.counters.Unchanged.Add(1)
		// An unconfirmed newer state is obsolete: the device is back at the
		// confirmed one.
		if _, serr := os.Stat(pending); serr == nil {
			_ = os.Remove(pending)
			c.counters.Superseded.Add(1)
		}
		c.log().Debug("configuration unchanged", "event", "collector.config.unchanged", "ci_id", t.CIID,
			"platform", snap.Platform, "sha256_prefix", hashPrefix(snap.SHA256))
		return
	}
	content, err := probewire.GzipConfig(snap.Text)
	if err != nil {
		c.counters.Failed.Add(1)
		return
	}
	msg := probewire.ConfigSnapshot{
		Header: probewire.Header{SchemaVersion: probewire.SchemaVersion},
		CIID:   t.CIID, ManagementIP: t.IP, CollectedAt: c.now().UTC(),
		Source: probewire.ConfigSourceSSH, Trigger: probewire.ConfigTriggerSchedule,
		Platform: string(snap.Platform), SHA256: snap.SHA256, SizeBytes: int64(snap.SizeBytes),
		MaskedCount: snap.MaskedCount, Content: content,
	}
	data, err := probewire.Encode(&msg)
	if err != nil {
		c.counters.Failed.Add(1)
		return
	}
	if int64(len(data)) > c.maxPayload() || snap.SizeBytes > probewire.MaxConfigBytes {
		c.counters.TooLarge.Add(1)
		c.log().Warn("configuration not sent: above the message size of the edge", "event", "collector.config.too_large",
			"ci_id", t.CIID, "platform", snap.Platform, "code", "config_too_large", "bytes", len(data))
		return
	}
	c.flushMu.Lock()
	if _, serr := os.Stat(pending); serr == nil {
		c.counters.Superseded.Add(1)
		c.log().Info("configuration state replaced before it was sent", "event", "collector.config.superseded",
			"ci_id", t.CIID, "code", "config_superseded")
	}
	err = writeFileAtomic(pending, data)
	c.flushMu.Unlock()
	if err != nil {
		c.counters.Failed.Add(1)
		c.log().Error("configuration state not buffered", "event", "collector.config.pending_failed", "ci_id", t.CIID, "error", err)
		return
	}
	c.log().Info("configuration changed", "event", "collector.config.changed", "ci_id", t.CIID, "platform", snap.Platform,
		"sha256_prefix", hashPrefix(snap.SHA256), "masked_count", snap.MaskedCount)
}

// pendingEntry is a buffered state.
type pendingEntry struct {
	path string
	data []byte
	msg  probewire.ConfigSnapshot
}

// pendingStates reads the buffered states, oldest first.
func (c *configCollector) pendingStates() []pendingEntry {
	dir := filepath.Join(c.dir, configPendingName)
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []pendingEntry
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var msg probewire.ConfigSnapshot
		if probewire.Decode(data, &msg) != nil || msg.Validate() != nil {
			c.log().Warn("buffered configuration state unreadable; dropped", "event", "collector.config.pending_invalid", "file", f.Name())
			_ = os.Remove(path)
			continue
		}
		out = append(out, pendingEntry{path: path, data: data, msg: msg})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].msg.CollectedAt.Before(out[j].msg.CollectedAt) })
	return out
}

// flush sends the buffered states, oldest first, until the edge does not
// take one (WP-1053 AK5, AK6).
func (c *configCollector) flush(ctx context.Context) {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	for _, p := range c.pendingStates() {
		if c.paused(c.now()) || ctx.Err() != nil {
			return
		}
		raw, err := c.send(ctx, edgeConfigSuffix, p.data)
		if err != nil {
			if !errors.Is(err, errNoEdge) {
				c.log().Warn("configuration state not delivered; kept", "event", "collector.config.held", "ci_id", p.msg.CIID, "error", err)
			}
			return
		}
		var reply edgeReply
		if err = json.Unmarshal(raw, &reply); err != nil {
			c.log().Warn("configuration state: invalid reply; kept", "event", "collector.config.held", "ci_id", p.msg.CIID)
			return
		}
		switch {
		case reply.Status >= 200 && reply.Status < 300:
			c.mu.Lock()
			e := c.state[p.msg.CIID]
			e.SHA256, e.Platform = p.msg.SHA256, p.msg.Platform
			c.state[p.msg.CIID] = e
			c.saveStateLocked()
			c.pauseLogged = false
			c.mu.Unlock()
			_ = os.Remove(p.path)
			c.counters.Sent.Add(1)
			c.log().Info("configuration state delivered", "event", "collector.config.sent", "ci_id", p.msg.CIID,
				"platform", p.msg.Platform, "sha256_prefix", hashPrefix(p.msg.SHA256), "masked_count", p.msg.MaskedCount)
		case reply.Status == http.StatusForbidden && reply.Type == problemFeatureNotEntitled:
			c.mu.Lock()
			c.pausedUntil = c.now().Add(configPause)
			logged := c.pauseLogged
			c.pauseLogged = true
			c.mu.Unlock()
			if !logged {
				c.log().Warn("configuration change detection not licensed; reads pause", "event", "collector.config.not_licensed",
					"pause", configPause.String())
			}
			return
		case reply.Status == http.StatusNotFound:
			_ = os.Remove(p.path)
			c.counters.Discarded.Add(1)
			c.log().Warn("configuration state discarded: CI unknown or outside the collector's scope", "event", "collector.config.discarded",
				"ci_id", p.msg.CIID, "sha256_prefix", hashPrefix(p.msg.SHA256))
		case permanentRefusal(reply.Status):
			// The server will refuse this state again (400, 409, 422 …):
			// kept, it would block the states of every other CI.
			_ = os.Remove(p.path)
			c.counters.Discarded.Add(1)
			c.log().Warn("configuration state discarded: refused by the server", "event", "collector.config.discarded",
				"ci_id", p.msg.CIID, "sha256_prefix", hashPrefix(p.msg.SHA256), "status", reply.Status)
		case reply.Status == http.StatusRequestEntityTooLarge:
			_ = os.Remove(p.path)
			c.counters.TooLarge.Add(1)
			c.log().Warn("configuration state refused as too large", "event", "collector.config.too_large", "ci_id", p.msg.CIID, "code", "config_too_large")
		default:
			c.log().Warn("configuration state refused; kept", "event", "collector.config.held", "ci_id", p.msg.CIID,
				"status", reply.Status)
			return
		}
	}
}

// permanentRefusal reports a 4xx answer that a retry of the same state
// cannot change; 403 (licence or authorization), 408 and 429 are
// transient and keep the state.
func permanentRefusal(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return status >= 400 && status < 500
}

// bundleConfigTargets returns the CIs with a bundle target of the
// namespace ssh in a known scope, with their credentials from
// credentialsForTarget (WP-1053 AK4). A CI of several scopes is read once,
// in the first scope by ID.
func bundleConfigTargets(cfg *collectorConfig) []configTarget {
	scopes, _, _ := collectorScopes.get()
	byID := map[string]*collectorScope{}
	for i := range scopes {
		byID[scopes[i].ID] = &scopes[i]
	}
	type entry struct{ scope, ci, ip string }
	var entries []entry
	collectorBundle.mu.RLock()
	for scopeID, st := range collectorBundle.scopes {
		for ci, list := range st.byCI {
			for _, tgt := range list {
				if tgt.Namespace == "ssh" && tgt.ManagementIP != "" {
					entries = append(entries, entry{scopeID, ci, tgt.ManagementIP})
					break
				}
			}
		}
	}
	collectorBundle.mu.RUnlock()
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.scope+"/"+a.ci, b.scope+"/"+b.ci) })
	seen := map[string]bool{}
	var out []configTarget
	for _, e := range entries {
		scope := byID[e.scope]
		if scope == nil || seen[e.ci] {
			continue
		}
		creds, _ := credentialsForTarget(cfg, scope, e.ip, e.ci)
		sshCreds := plugins.CredentialsFor("ssh", creds)
		if sshCreds["username"] == "" {
			continue
		}
		seen[e.ci] = true
		out = append(out, configTarget{CIID: e.ci, IP: canonicalIP(e.ip), Creds: sshCreds})
	}
	return out
}

// startConfigReads starts the configuration reads of the collector, unless
// RETICORA_CONFIG_INTERVAL is 0 or there is no spool directory.
func startConfigReads(ctx context.Context, cfg *collectorConfig) {
	if cfg.ConfigInterval <= 0 || cfg.SpoolDir == "" {
		slog.Info("configuration change detection off", "event", "collector.config.disabled")
		return
	}
	c, err := newConfigCollector(filepath.Join(cfg.SpoolDir, "config"), cfg.ConfigInterval, cfg.ConfigParallel)
	if err != nil {
		slog.Error("configuration change detection not started", "event", "collector.config.disabled", "error", err)
		return
	}
	sshPlugin, _ := pluginRegistry["ssh"].(*ssh.Plugin)
	if sshPlugin == nil {
		sshPlugin = ssh.New()
	}
	c.collect = func(ctx context.Context, t *configTarget, platform config.Platform) (*config.Snapshot, error) {
		return config.Collect(ctx, sshPlugin, t.IP, t.Creds, platform)
	}
	c.targets = func() []configTarget { return bundleConfigTargets(cfg) }
	slog.Info("configuration change detection started", "event", "collector.config.started",
		"interval", cfg.ConfigInterval.String(), "parallel", c.parallel)
	go c.run(ctx)
}

// String reports the counters (diagnosis).
func (c *configCollector) String() string {
	return fmt.Sprintf("sent=%d unchanged=%d superseded=%d too_large=%d unsupported=%d failed=%d discarded=%d",
		c.counters.Sent.Load(), c.counters.Unchanged.Load(), c.counters.Superseded.Load(), c.counters.TooLarge.Load(),
		c.counters.Unsupported.Load(), c.counters.Failed.Load(), c.counters.Discarded.Load())
}
