package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Discovery scopes (DIS-09): the server sends the collector its enabled
// scopes over the NATS channel; the collector discovers each scope with its
// plugins, tags the DeviceRecords with scope, client, site and VRF and
// reports every run. After the connection it reports its local subnets,
// which the server offers as scope_suggested. Without a scope list from the
// server (not enrolled, no NATS endpoint) the collector scans
// RETICORA_SCAN_SUBNETS as before.

// collectorScope is a scope as the server sends it.
type collectorScope struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	VRFID         string   `json:"vrf_id,omitempty"`
	Schedule      string   `json:"schedule"`
	CredentialIDs []string `json:"credential_ids"`
	Plugins       []string `json:"plugins"`
}

type scopeCache struct {
	mu        sync.RWMutex
	scopes    []collectorScope
	intervals *discoveryIntervals
	known     bool
}

func (c *scopeCache) set(scopes []collectorScope, intervals *discoveryIntervals) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes, c.intervals, c.known = scopes, intervals, true
}

func (c *scopeCache) get() ([]collectorScope, *discoveryIntervals, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scopes, c.intervals, c.known
}

var collectorScopes = &scopeCache{}

// scopeChannel is the open NATS connection and the collector's subject
// tree while the collector is connected.
type scopeChannelState struct {
	nc      *nats.Conn
	subject string
}

var scopeChannel atomic.Pointer[scopeChannelState]

const natsRequestTimeout = 10 * time.Second

func natsRequest(ctx context.Context, nc *nats.Conn, subject string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, natsRequestTimeout)
	defer cancel()
	msg, err := nc.RequestWithContext(rctx, subject, raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(msg.Data, out)
}

// fetchScopes asks the server for the collector's enabled scopes.
func fetchScopes(ctx context.Context, nc *nats.Conn, subject string) ([]collectorScope, *discoveryIntervals, error) {
	var resp struct {
		Scopes    []collectorScope    `json:"scopes"`
		Intervals *discoveryIntervals `json:"intervals"`
		Error     string              `json:"error"`
	}
	if err := natsRequest(ctx, nc, subject+".scopes.request", struct{}{}, &resp); err != nil {
		return nil, nil, err
	}
	if resp.Error != "" {
		return nil, nil, errors.New("scopes refused: " + resp.Error)
	}
	if resp.Scopes == nil {
		resp.Scopes = []collectorScope{}
	}
	return resp.Scopes, resp.Intervals, nil
}

// syncScopes refreshes the scopes until ctx ends.
func syncScopes(ctx context.Context, nc *nats.Conn, subject string) {
	ticker := time.NewTicker(bundleRefresh)
	defer ticker.Stop()
	for {
		if scopes, intervals, err := fetchScopes(ctx, nc, subject); err != nil {
			slog.Warn("discovery scopes not updated", "error", err)
		} else {
			collectorScopes.set(scopes, intervals)
			slog.Info("discovery scopes updated", "scopes", len(scopes))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// localSubnets returns the IPv4 subnets of the collector's interfaces
// (loopback, link-local and host routes excluded).
func localSubnets(addrs func() ([]net.Addr, error)) []string {
	list, err := addrs()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range list {
		p, perr := netip.ParsePrefix(a.String())
		if perr != nil {
			continue
		}
		ip := p.Addr()
		if !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || p.Bits() >= 31 {
			continue
		}
		cidr := p.Masked().String()
		if !seen[cidr] {
			seen[cidr] = true
			out = append(out, cidr)
		}
	}
	return out
}

// reportSubnets sends the local subnets once after the connection
// (Zero-Config, INS-06).
func reportSubnets(ctx context.Context, nc *nats.Conn, subject string, subnets []string) {
	if len(subnets) == 0 {
		return
	}
	var resp map[string]any
	if err := natsRequest(ctx, nc, subject+".subnets.report", map[string]any{"subnets": subnets}, &resp); err != nil {
		slog.Warn("local subnets not reported", "error", err)
		return
	}
	slog.Info("local subnets reported", "subnets", subnets, "suggested", resp["suggested"])
}

// scopeTargets expands a scope into the addresses or names to probe.
func scopeTargets(s *collectorScope) []string {
	switch s.Kind {
	case "cidr":
		return expandSubnets([]string{s.Target})
	case "host":
		return []string{s.Target}
	case "range":
		from, to, ok := strings.Cut(s.Target, "-")
		a, errA := netip.ParseAddr(strings.TrimSpace(from))
		b, errB := netip.ParseAddr(strings.TrimSpace(to))
		if !ok || errA != nil || errB != nil {
			return nil
		}
		var out []string
		for ip := a; ip.IsValid() && !b.Less(ip) && len(out) <= maxSubnetExpansionTargets; ip = ip.Next() {
			out = append(out, ip.String())
		}
		return out
	}
	return nil
}

// discoveryPass is one set of targets discovered with a set of protocols;
// scope is nil for the RETICORA_SCAN_SUBNETS fallback.
type discoveryPass struct {
	key       string
	scope     *collectorScope
	targets   []string
	protocols []string
	sweep     schedule
	// poll is the poll interval of the fallback pass; scopes use intervals.
	poll      time.Duration
	intervals *discoveryIntervals
}

// discoveryPasses returns the passes: one per scope when the server sent a
// scope list, otherwise the configured subnets.
func discoveryPasses(cfg *collectorConfig) []discoveryPass {
	scopes, intervals, known := collectorScopes.get()
	if !known {
		sweep := cfg.SweepInterval
		if sweep <= 0 {
			sweep = defaultSweepInterval
		}
		poll := cfg.DiscoveryInterval
		if poll <= 0 {
			poll = defaultPollInterval
		}
		return []discoveryPass{{key: "local", targets: expandSubnets(cfg.ScanSubnets), protocols: cfg.Protocols,
			sweep: schedule{every: sweep}, poll: poll}}
	}
	sweepDefault := defaultSweepInterval
	if intervals != nil {
		sweepDefault = parseInterval(intervals.Sweep, defaultSweepInterval, minPollInterval)
	}
	out := make([]discoveryPass, 0, len(scopes))
	for i := range scopes {
		s := scopes[i]
		out = append(out, discoveryPass{key: s.ID, scope: &s, targets: scopeTargets(&s), protocols: s.Plugins,
			sweep: parseSchedule(s.Schedule, sweepDefault), intervals: intervals})
	}
	return out
}

// protocolSchedule returns when a protocol of the pass runs (DIS-10).
func (p *discoveryPass) protocolSchedule(protocol string) schedule {
	if protocol == "sweep" {
		return p.sweep
	}
	if p.scope == nil {
		return schedule{every: p.poll}
	}
	return schedule{every: p.intervals.pollInterval(protocol)}
}

// dueProtocols returns the protocols due at now, the sweep first so the
// polls of the same run use its hosts.
func (p *discoveryPass) dueProtocols(sched *scheduler, now time.Time) []string {
	var due []string
	for _, protocol := range p.protocols {
		if sched.due(p.key+"/"+protocol, now, p.protocolSchedule(protocol)) {
			if protocol == "sweep" {
				due = append([]string{protocol}, due...)
			} else {
				due = append(due, protocol)
			}
		}
	}
	return due
}

// targetsFor returns the targets of a protocol: polls of a pass with a
// sweep go to the hosts the last sweep found.
func (p *discoveryPass) targetsFor(protocol string, hosts *hostCache) []string {
	if protocol == "sweep" || !slices.Contains(p.protocols, "sweep") {
		return p.targets
	}
	if found := hosts.get(p.key); len(found) > 0 {
		return found
	}
	return p.targets
}

// nextRun is the next sweep of the pass (or its next poll without sweep).
func (p *discoveryPass) nextRun(sched *scheduler) time.Time {
	var next time.Time
	for _, protocol := range p.protocols {
		at := sched.nextRun(p.key + "/" + protocol)
		if protocol == "sweep" {
			return at
		}
		if next.IsZero() || (!at.IsZero() && at.Before(next)) {
			next = at
		}
	}
	return next
}

// hitTargets returns the targets of probe hits.
func hitTargets(hits []plugins.Result) []string {
	out := make([]string, 0, len(hits))
	for i := range hits {
		if hits[i].IP != "" {
			out = append(out, hits[i].IP)
		}
	}
	return out
}

// tagRecords sets the scope fields of the records (DIS-05: client and site
// come from the scope).
func tagRecords(records []plugins.DeviceRecord, s *collectorScope) {
	if s == nil {
		return
	}
	for i := range records {
		records[i].ScopeID, records[i].ClientID, records[i].SiteID, records[i].VRFID = s.ID, s.ClientID, s.SiteID, s.VRFID
	}
}

// runError is a failure of a scope run with its target and protocol; the
// server derives the cause (DIS-08) unless the collector knows it.
type runError struct {
	Protocol string `json:"protocol"`
	Target   string `json:"target,omitempty"`
	Cause    string `json:"cause,omitempty"`
	Message  string `json:"message"`
}

// maxReportedErrors bounds the errors of one run report.
const maxReportedErrors = 50

// reportScopeRun sends the result of a scope run to the server.
func reportScopeRun(ctx context.Context, s *collectorScope, started time.Time, records int, errs []runError, next time.Time) {
	ch := scopeChannel.Load()
	if ch == nil || s == nil {
		return
	}
	result := map[string]any{"records": records, "duration_ms": time.Since(started).Milliseconds()}
	if len(errs) > 0 {
		result["error_count"] = len(errs)
		if len(errs) > maxReportedErrors {
			errs = errs[:maxReportedErrors]
		}
		result["errors"] = errs
	}
	var resp map[string]any
	if err := natsRequest(ctx, ch.nc, ch.subject+".scopes.result", map[string]any{
		"scope_id": s.ID, "at": started.UTC(), "next_run": next.UTC(), "result": result,
	}, &resp); err != nil {
		slog.Warn("scope run not reported", "scope_id", s.ID, "error", err)
	}
}
