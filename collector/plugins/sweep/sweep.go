// Package sweep provides network host discovery (DIS-01): ICMP echo, ARP
// (neighbour table) and TCP connect probes on the optional ports, with
// reverse DNS. SNMP (UDP 161) is probed by the snmp plugin.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultTimeout = 2 * time.Second
	// MaxConcurrency is the hard upper bound of parallel probes (DIS-01);
	// a higher configured value is capped.
	MaxConcurrency     = 256
	defaultConcurrency = MaxConcurrency
)

// DefaultPorts are the optional TCP ports of DIS-01: SSH, HTTP, HTTPS,
// IPMI/RMCP+ (623), WinRM (5985) and the Proxmox VE API (8006).
var DefaultPorts = []int{22, 80, 443, 623, 5985, 8006}

// Plugin performs host discovery across network ranges.
type Plugin struct {
	Timeout time.Duration
	// Concurrency is the number of parallel probes, at most
	// MaxConcurrency.
	Concurrency int
	TCPPorts    []int
	// Pinger sends ICMP echo requests; nil disables ICMP.
	Pinger Pinger
	// Neighbors reads the ARP neighbour table; nil disables ARP.
	Neighbors NeighborTable
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a sweep plugin with ICMP, ARP and the default ports.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
		TCPPorts:    DefaultPorts,
		Pinger:      &ICMPPinger{},
		Neighbors:   ProcNeighbors,
	}
}

// effectiveConcurrency applies the default and the hard upper bound.
func (p *Plugin) effectiveConcurrency() int {
	switch {
	case p.Concurrency <= 0:
		return defaultConcurrency
	case p.Concurrency > MaxConcurrency:
		return MaxConcurrency
	}
	return p.Concurrency
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "sweep" }

// Probe checks the targets with ICMP echo and the TCP ports (at most
// MaxConcurrency at a time) and adds the targets that only answered ARP.
func (p *Plugin) Probe(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	_ = creds

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ports := p.TCPPorts
	if len(ports) == 0 {
		ports = DefaultPorts
	}

	sem := make(chan struct{}, p.effectiveConcurrency())
	var mu sync.Mutex
	results := []plugins.Result{}
	found := map[string]bool{}
	icmpOff := false

	// A probe takes its slot before its goroutine starts, so a large scope
	// holds at most MaxConcurrency probe goroutines at a time (F-262).
	var wg sync.WaitGroup
	for _, target := range targets {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()

			method, openPort := "", 0
			var rtt time.Duration
			if p.Pinger != nil {
				d, err := p.Pinger.Ping(ctx, t, timeout)
				if err == nil {
					method, rtt = "icmp", d
				} else if errors.Is(err, ErrNoICMP) {
					mu.Lock()
					icmpOff = true
					mu.Unlock()
				}
			}
			for _, port := range ports {
				if ctx.Err() != nil {
					return
				}
				start := time.Now()
				var d net.Dialer
				dctx, cancel := context.WithTimeout(ctx, timeout)
				conn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(t, fmt.Sprintf("%d", port)))
				cancel()
				if err == nil {
					conn.Close()
					openPort = port
					if method == "" {
						method, rtt = "tcp", time.Since(start)
					}
					break
				}
			}
			if method == "" {
				return
			}
			result := sweepResult(t, method, openPort, rtt)
			mu.Lock()
			results = append(results, result)
			found[t] = true
			mu.Unlock()
		}(target)
	}
	wg.Wait()
	if icmpOff {
		slog.Warn("sweep: ICMP unavailable, using TCP and ARP only", "error", ErrNoICMP)
	}
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return p.addNeighbors(targets, results, found), nil
}

// addNeighbors sets the MAC of the found hosts and adds the targets that
// only answered ARP.
func (p *Plugin) addNeighbors(targets []string, results []plugins.Result, found map[string]bool) []plugins.Result {
	if p.Neighbors == nil {
		return results
	}
	table, err := p.Neighbors()
	if err != nil {
		slog.Debug("sweep: ARP neighbour table unavailable", "error", err)
		return results
	}
	for i := range results {
		if mac, ok := table[results[i].IP]; ok {
			results[i].MAC = mac
		}
	}
	for _, t := range targets {
		mac, ok := table[t]
		if !ok || found[t] {
			continue
		}
		r := sweepResult(t, "arp", 0, 0)
		r.MAC = mac
		results = append(results, r)
		found[t] = true
	}
	return results
}

func sweepResult(target, method string, openPort int, rtt time.Duration) plugins.Result {
	result := plugins.Result{
		CIType: classifyByPort(openPort),
		Name:   reverseName(target),
		IP:     target,
		Attributes: map[string]any{
			"discoveryMethod": method,
			"openPort":        openPort,
			"rttMs":           rtt.Milliseconds(),
			"reachable":       true,
		},
	}
	if result.Name == "" {
		result.Name = target
	}
	return result
}

// reverseName resolves the target's PTR record ("" when none).
func reverseName(ip string) string {
	names, err := net.LookupAddr(ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// Collect implements plugins.Plugin: the DeviceRecord of target (DIS-03).
// The record tells that the target answered (open ports, PTR name; the ARP
// MAC comes with the probe hit) and carries no evidence for the
// classification chain: the collector folds it into the record of a
// protocol that classified the target and uploads it as generic_device
// only when no protocol did (profiles.MergeByTarget, DIS-02).
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	observedAt := time.Now()
	res, err := p.collect(ctx, target, creds)
	return plugins.CollectRecord(p.Name(), res, err, observedAt)
}

// collect gathers additional service fingerprint data for a single target.
func (p *Plugin) collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	_ = creds

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ports := p.TCPPorts
	if len(ports) == 0 {
		ports = DefaultPorts
	}

	var openPorts []int
	for _, port := range ports {
		if ctx.Err() != nil {
			break
		}
		addr := net.JoinHostPort(target, fmt.Sprintf("%d", port))
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			openPorts = append(openPorts, port)
			conn.Close()
		}
	}

	hostname := reverseName(target)
	if hostname == "" {
		hostname = target
	}

	ciType := "network.endpoint"
	if len(openPorts) > 0 {
		ciType = classifyByPort(openPorts[0])
	}

	return &plugins.Result{
		CIType: ciType,
		Name:   hostname,
		IP:     target,
		Attributes: map[string]any{
			"collectedBy": "sweep",
			"openPorts":   openPorts,
			"reachable":   len(openPorts) > 0,
		},
	}, nil
}

// classifyByPort provides a basic CI type guess from a TCP port.
func classifyByPort(port int) string {
	switch port {
	case 22:
		return "server"
	case 80, 443, 8080, 8443:
		return "network.endpoint"
	case 161:
		return "network.device"
	case 3389, 5985:
		return "windows-server"
	case 623:
		return "bmc"
	default:
		return "network.endpoint"
	}
}
