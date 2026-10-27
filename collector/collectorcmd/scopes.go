package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// collectorScope is a scope as the server sends it, with the run state the
// server holds (RFC 3339; last_run_at empty before the first run) and the
// organization's time zone of its cron schedule (DIS-10).
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
	LastRunAt     string   `json:"last_run_at,omitempty"`
	NextRunAt     string   `json:"next_run_at,omitempty"`
	Timezone      string   `json:"timezone,omitempty"`
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

// localInterface is a network interface of the collector host with its
// addresses.
type localInterface struct {
	Name  string
	Addrs []net.Addr
}

// systemInterfaces reads the interfaces of the collector host.
func systemInterfaces() ([]localInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]localInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, aerr := iface.Addrs()
		if aerr != nil {
			continue
		}
		out = append(out, localInterface{Name: iface.Name, Addrs: addrs})
	}
	return out, nil
}

// containerInterfacePrefixes are the name prefixes of the container and
// bridge interfaces (Docker, Linux bridges of Docker networks, veth pairs,
// CNI, Flannel, Calico): their networks exist only on the collector host
// and are no scope suggestions (INS-06).
var containerInterfacePrefixes = []string{"docker", "br-", "veth", "cni", "flannel", "cali"}

// localSubnets returns the IPv4 subnets of the collector's interfaces
// (container and bridge interfaces, loopback, link-local and host routes
// excluded).
func localSubnets(interfaces func() ([]localInterface, error)) []string {
	list, err := interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, iface := range list {
		if slices.ContainsFunc(containerInterfacePrefixes, func(p string) bool { return strings.HasPrefix(iface.Name, p) }) {
			continue
		}
		for _, a := range iface.Addrs {
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

// scopeTargets expands a scope into the addresses or names to probe;
// truncated reports a CIDR or range with more than maxScopeAddresses
// addresses, of which only the first are probed.
func scopeTargets(s *collectorScope) (targets []string, truncated bool) {
	switch s.Kind {
	case "cidr":
		return expandCIDR(s.Target)
	case "host":
		return []string{s.Target}, false
	case "range":
		from, to, ok := strings.Cut(s.Target, "-")
		a, errA := netip.ParseAddr(strings.TrimSpace(from))
		b, errB := netip.ParseAddr(strings.TrimSpace(to))
		if !ok || errA != nil || errB != nil {
			return nil, false
		}
		for ip := a; ip.IsValid() && !b.Less(ip); ip = ip.Next() {
			if len(targets) == maxScopeAddresses {
				return targets, true
			}
			targets = append(targets, ip.String())
		}
		return targets, false
	}
	return nil, false
}

// truncationError is the run error of a scope cut at maxScopeAddresses:
// the run reports it instead of discovering the scope partly in silence
// (DIS-08, DIS-09).
func truncationError(s *collectorScope) runError {
	return runError{Protocol: "scope", Target: s.Target, Cause: causeScopeTruncated,
		Message: fmt.Sprintf("more than %d addresses per scope are not allowed: only the first %d were discovered", maxScopeAddresses, maxScopeAddresses)}
}

// causeScopeTruncated is the cause of a scope cut at maxScopeAddresses.
const causeScopeTruncated = "scope_truncated"

// discoveryPass is one set of targets discovered with a set of protocols;
// scope is nil for the RETICORA_SCAN_SUBNETS fallback.
type discoveryPass struct {
	key       string
	scope     *collectorScope
	targets   []string
	truncated bool // the scope has more addresses than targets holds
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
	// The simulated fleet ignores the server's scopes (SIM-02).
	if !known || cfg.Simulate {
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
		targets, truncated := scopeTargets(&s)
		sweep := parseSchedule(s.Schedule, sweepDefault)
		sweep.loc = scheduleLocation(s.Timezone)
		out = append(out, discoveryPass{key: s.ID, scope: &s, targets: targets, truncated: truncated, protocols: s.Plugins,
			sweep: sweep, intervals: intervals})
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

// firstSweep is when the sweep of a scope is due after a (re)start of the
// collector (DIS-10, F-109): at the next_run_at the server holds; without
// it a cron scope at its next match and an interval scope one interval
// after its last run; a scope that never ran at once.
func (p *discoveryPass) firstSweep(now time.Time) time.Time {
	if at, err := time.Parse(time.RFC3339, p.scope.NextRunAt); err == nil {
		return at
	}
	if p.sweep.cron != nil {
		return p.sweep.next(now)
	}
	if last, err := time.Parse(time.RFC3339, p.scope.LastRunAt); err == nil {
		return p.sweep.next(last)
	}
	return now
}

// dueProtocols returns the protocols due at now, the sweep first so the
// polls of the same run use its hosts.
func (p *discoveryPass) dueProtocols(sched *scheduler, now time.Time) []string {
	var due []string
	for _, protocol := range p.protocols {
		if protocol == "sweep" && p.scope != nil {
			sched.seed(p.key+"/"+protocol, func() time.Time { return p.firstSweep(now) })
		}
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

// Ad-hoc discovery jobs (JOB-01, F-358): a job created through the API
// (POST /api/v1/discovery/jobs) waits on the server until its collector
// asks for it over the scope channel. The collector reports it running,
// discovers its passes with the plugins of the job type, uploads the
// records like a scheduled run and reports the result; the server sets the
// job's status. A job the server does not let run (another request took
// it, it timed out) is skipped.

// collectorJob is an open discovery job as the server sends it.
type collectorJob struct {
	ID      string         `json:"id"`
	JobType string         `json:"job_type"`
	Config  map[string]any `json:"config,omitempty"`
}

// Job statuses the collector reports.
const (
	jobRunning   = "running"
	jobCompleted = "completed"
	jobFailed    = "failed"
)

// fetchJobs asks the server for the collector's open discovery jobs.
func fetchJobs(ctx context.Context, nc *nats.Conn, subject string) ([]collectorJob, error) {
	var resp struct {
		Jobs  []collectorJob `json:"jobs"`
		Error string         `json:"error"`
	}
	if err := natsRequest(ctx, nc, subject+".jobs.request", struct{}{}, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New("jobs refused: " + resp.Error)
	}
	return resp.Jobs, nil
}

// reportJobStatus sends the status of a job, with its error and result
// once it ended; it reports whether the server recorded it.
func reportJobStatus(ctx context.Context, nc *nats.Conn, subject, jobID, status, errMsg string, result map[string]any) (bool, error) {
	body := map[string]any{"job_id": jobID, "status": status}
	if errMsg != "" {
		body["error"] = errMsg
	}
	if result != nil {
		body["result"] = result
	}
	var resp map[string]any
	if err := natsRequest(ctx, nc, subject+".jobs.status", body, &resp); err != nil {
		return false, err
	}
	return resp["status"] == "recorded", nil
}

// jobProtocols returns the plugins a job of jobType runs on a pass with
// plugins: a sweep job the sweep, a poll job the pass's plugins without the
// sweep, a full job all of them with the sweep first. An unknown job type
// runs nothing.
func jobProtocols(jobType string, plugins []string) []string {
	polls := slices.DeleteFunc(slices.Clone(plugins), func(p string) bool { return p == "sweep" })
	switch jobType {
	case "sweep":
		return []string{"sweep"}
	case "poll":
		return polls
	case "full":
		if slices.Contains(plugins, "sweep") {
			return append([]string{"sweep"}, polls...)
		}
		return polls
	}
	return nil
}

// runOpenJobs runs the open jobs of the collector one after the other and
// returns how many it ran; without the scope channel there are none.
func runOpenJobs(ctx context.Context, cfg *collectorConfig, up *uploader, hosts *hostCache) int {
	ch := scopeChannel.Load()
	if ch == nil {
		return 0
	}
	open, err := fetchJobs(ctx, ch.nc, ch.subject)
	if err != nil {
		slog.Warn("discovery jobs not fetched", "error", err)
		return 0
	}
	ran := 0
	for i := range open {
		job := &open[i]
		if ctx.Err() != nil {
			break
		}
		if recorded, rerr := reportJobStatus(ctx, ch.nc, ch.subject, job.ID, jobRunning, "", nil); rerr != nil || !recorded {
			slog.Warn("discovery job not started", "job_id", job.ID, "error", rerr)
			continue
		}
		status, errMsg, result := runJob(ctx, cfg, up, hosts, job)
		if recorded, rerr := reportJobStatus(ctx, ch.nc, ch.subject, job.ID, status, errMsg, result); rerr != nil || !recorded {
			// The server fails the job after its deadline (timeout).
			slog.Warn("discovery job result not recorded", "job_id", job.ID, "status", status, "error", rerr)
		}
		slog.Info("discovery job ended", "event", "discovery.job."+status, "job_id", job.ID, "job_type", job.JobType,
			"records", result["records"])
		ran++
	}
	return ran
}

// runJob discovers the passes of the collector (its scopes, or the
// configured subnets without a scope list) with the plugins of the job
// type and uploads the records. It returns the status the job ended with,
// the error of a failed job and the result: records, passes, duration and
// the run errors (DIS-08). A job that ran no plugin fails with cause
// config_error.
func runJob(ctx context.Context, cfg *collectorConfig, up *uploader, hosts *hostCache, job *collectorJob) (status, errMsg string, result map[string]any) {
	started := time.Now()
	var records []plugins.DeviceRecord
	var errs []runError
	passes := 0
	for _, pass := range discoveryPasses(cfg) {
		protocols := jobProtocols(job.JobType, pass.protocols)
		if len(protocols) == 0 {
			continue
		}
		passRecords, passErrs := runPass(ctx, cfg, up, &pass, protocols, hosts)
		records = append(records, passRecords...)
		errs = append(errs, passErrs...)
		passes++
	}
	result = map[string]any{"records": 0, "scopes": passes}
	status = jobCompleted
	if passes == 0 {
		status = jobFailed
		errMsg = fmt.Sprintf("no discovery scope of the collector has a plugin for job type %q", job.JobType)
		errs = append(errs, runError{Protocol: "job", Cause: "config_error", Message: errMsg})
	} else {
		var uploadErr *runError
		records, uploadErr = uploadDiscovered(ctx, up, records, started)
		if uploadErr != nil {
			errs = append(errs, *uploadErr)
		}
		result["records"] = len(records)
	}
	result["duration_ms"] = time.Since(started).Milliseconds()
	if len(errs) > 0 {
		result["error_count"] = len(errs)
		if len(errs) > maxReportedErrors {
			errs = errs[:maxReportedErrors]
		}
		result["errors"] = errs
	}
	return status, errMsg, result
}
