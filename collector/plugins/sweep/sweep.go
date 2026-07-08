// Package sweep provides network host discovery via ICMP echo and TCP connect probes.
package sweep

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultTimeout     = 2 * time.Second
	defaultConcurrency = 64
	defaultTCPPort     = 443
)

// commonPorts are tried when probing host reachability via TCP.
var commonPorts = []int{22, 80, 443, 3389, 8080, 8443}

// Plugin performs basic host discovery across network ranges using TCP connect probes.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
	TCPPorts    []int
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a sweep plugin instance with default settings.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
		TCPPorts:    commonPorts,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "sweep" }

// Discover probes a list of targets using TCP connect and returns discovered endpoints.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	_ = creds

	concurrency := p.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ports := p.TCPPorts
	if len(ports) == 0 {
		ports = commonPorts
	}

	type probeResult struct {
		target   string
		reachable bool
		openPort int
		rtt      time.Duration
		hostname string
	}

	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	var results []plugins.Result

	var wg sync.WaitGroup
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(t string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			pr := probeResult{target: t}

			// Try TCP connect on common ports
			for _, port := range ports {
				if ctx.Err() != nil {
					return
				}
				addr := net.JoinHostPort(t, fmt.Sprintf("%d", port))
				start := time.Now()
				conn, err := net.DialTimeout("tcp", addr, timeout)
				if err == nil {
					pr.reachable = true
					pr.openPort = port
					pr.rtt = time.Since(start)
					conn.Close()
					break
				}
			}

			if !pr.reachable {
				return
			}

			// Attempt reverse DNS lookup
			names, err := net.LookupAddr(t)
			if err == nil && len(names) > 0 {
				pr.hostname = names[0]
				// Remove trailing dot from DNS names
				if len(pr.hostname) > 0 && pr.hostname[len(pr.hostname)-1] == '.' {
					pr.hostname = pr.hostname[:len(pr.hostname)-1]
				}
			}

			result := plugins.Result{
				CIType: classifyByPort(pr.openPort),
				Name:   pr.hostname,
				IP:     t,
				Attributes: map[string]any{
					"discoveryMethod": "sweep",
					"openPort":        pr.openPort,
					"rttMs":           pr.rtt.Milliseconds(),
					"reachable":       true,
				},
			}
			if result.Name == "" {
				result.Name = t
			}

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(target)
	}
	wg.Wait()

	if results == nil {
		results = []plugins.Result{}
	}
	return results, ctx.Err()
}

// Collect gathers additional service fingerprint data for a single target.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	_ = creds

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ports := p.TCPPorts
	if len(ports) == 0 {
		ports = commonPorts
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

	hostname := target
	names, err := net.LookupAddr(target)
	if err == nil && len(names) > 0 {
		hostname = names[0]
		if len(hostname) > 0 && hostname[len(hostname)-1] == '.' {
			hostname = hostname[:len(hostname)-1]
		}
	}

	ciType := "network.endpoint"
	if len(openPorts) > 0 {
		ciType = classifyByPort(openPorts[0])
	}

	return &plugins.Result{
		CIType:   ciType,
		Name:     hostname,
		IP:       target,
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
	case 3389:
		return "windows-server"
	case 623:
		return "bmc"
	default:
		return "network.endpoint"
	}
}
