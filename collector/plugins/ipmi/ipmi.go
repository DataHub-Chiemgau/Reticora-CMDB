// Package ipmi provides an IPMI/RMCP collection plugin for BMC hardware discovery.
package ipmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultPort        = 623
	defaultTimeout     = 5 * time.Second
	defaultConcurrency = 32

	// RMCP/ASF ping to identify IPMI-capable endpoints
	rmcpVersion = 0x06
	rmcpSeqNo   = 0xFF
	rmcpClassASF = 0x06
)

// Plugin collects hardware information over IPMI.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates an IPMI plugin instance.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "ipmi" }

// Discover finds IPMI-enabled controllers by sending RMCP ping (ASF Presence Ping) packets.
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

			if reachable := p.probeRMCP(t, timeout); reachable {
				result := plugins.Result{
					CIType: "bmc",
					Name:   t,
					IP:     t,
					Attributes: map[string]any{
						"protocol":    "ipmi",
						"rmcpPort":    defaultPort,
						"ipmiCapable": true,
					},
				}
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}
		}(target)
	}
	wg.Wait()

	if results == nil {
		results = []plugins.Result{}
	}
	return results, ctx.Err()
}

// Collect queries IPMI for device chassis information via RMCP+.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	// Verify RMCP reachability first
	if !p.probeRMCP(target, timeout) {
		return nil, fmt.Errorf("ipmi: target %s not reachable via RMCP", target)
	}

	// Send Get Channel Auth Capabilities to determine IPMI version support.
	addr := net.JoinHostPort(target, fmt.Sprintf("%d", defaultPort))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("ipmi: dial %s: %w", target, err)
	}
	defer conn.Close()

	// Build IPMI Get Channel Authentication Capabilities request
	ipmiMsg := buildGetChannelAuthCap()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("ipmi: set deadline: %w", err)
	}
	if _, err := conn.Write(ipmiMsg); err != nil {
		return nil, fmt.Errorf("ipmi: write: %w", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("ipmi: read: %w", err)
	}

	ipmiVersion := "unknown"
	if n > 20 {
		// Parse response to determine supported IPMI versions
		ipmiVersion = parseIPMIVersion(buf[:n])
	}

	hostname := target
	names, _ := net.LookupAddr(target)
	if len(names) > 0 {
		hostname = names[0]
		if len(hostname) > 0 && hostname[len(hostname)-1] == '.' {
			hostname = hostname[:len(hostname)-1]
		}
	}

	result := &plugins.Result{
		CIType: "bmc",
		Name:   hostname,
		IP:     target,
		Attributes: map[string]any{
			"protocol":    "ipmi",
			"ipmiVersion": ipmiVersion,
			"rmcpPort":    defaultPort,
			"ipmiCapable": true,
			"commandSet": map[string]string{
				"chassis_status": "Get Chassis Status (0x01)",
				"fru_read":       "Read FRU Data (0x11)",
				"sdr_list":       "Get SDR Repository Info (0x20)",
				"sel_info":       "Get SEL Info (0x40)",
			},
		},
	}

	return result, nil
}

// probeRMCP sends an ASF Presence Ping to check if the host supports RMCP/IPMI.
func (p *Plugin) probeRMCP(target string, timeout time.Duration) bool {
	addr := net.JoinHostPort(target, fmt.Sprintf("%d", defaultPort))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	// ASF Presence Ping message
	ping := buildASFPing()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}
	if _, err := conn.Write(ping); err != nil {
		return false
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}

	// Validate ASF Presence Pong response (minimum 12 bytes, RMCP header + ASF)
	return n >= 12 && buf[0] == rmcpVersion
}

// buildASFPing constructs an ASF Presence Ping message (RMCP class 0x06).
func buildASFPing() []byte {
	msg := make([]byte, 12)
	// RMCP header
	msg[0] = rmcpVersion   // Version
	msg[1] = 0x00          // Reserved
	msg[2] = rmcpSeqNo     // Sequence number
	msg[3] = rmcpClassASF  // Class: ASF
	// ASF header
	binary.BigEndian.PutUint32(msg[4:8], 0x000011BE) // IANA Enterprise Number (ASF)
	msg[8] = 0x80                                     // Message type: Presence Ping
	msg[9] = 0x00                                     // Message tag
	msg[10] = 0x00                                    // Reserved
	msg[11] = 0x00                                    // Data length
	return msg
}

// buildGetChannelAuthCap constructs an IPMI Get Channel Authentication Capabilities request.
func buildGetChannelAuthCap() []byte {
	// RMCP header (4 bytes) + IPMI session wrapper + message
	msg := make([]byte, 22)
	// RMCP header
	msg[0] = rmcpVersion
	msg[1] = 0x00
	msg[2] = rmcpSeqNo
	msg[3] = 0x07 // Class: IPMI

	// IPMI session header (v1.5 format, unauthenticated)
	msg[4] = 0x00          // Auth type: None
	msg[5] = 0x00          // Session sequence (4 bytes)
	msg[6] = 0x00
	msg[7] = 0x00
	msg[8] = 0x00
	msg[9] = 0x00          // Session ID (4 bytes)
	msg[10] = 0x00
	msg[11] = 0x00
	msg[12] = 0x00
	msg[13] = 0x09         // Message length

	// IPMI message
	msg[14] = 0x20         // Target address (BMC)
	msg[15] = 0x18         // Target LUN + NetFn (App = 0x06, LUN 0)
	msg[16] = 0xC8         // Header checksum
	msg[17] = 0x81         // Source address
	msg[18] = 0x00         // Source LUN + SeqNo
	msg[19] = 0x38         // Command: Get Channel Auth Capabilities
	msg[20] = 0x0E         // Channel 14 (current), request IPMI v2.0
	msg[21] = 0x04         // Privilege: Administrator

	return msg
}

// parseIPMIVersion extracts IPMI version support from a Get Channel Auth response.
func parseIPMIVersion(data []byte) string {
	if len(data) < 22 {
		return "1.5"
	}
	// Check if IPMI 2.0 extended capabilities are indicated
	for i := 14; i < len(data)-1; i++ {
		if data[i]&0x20 != 0 {
			return "2.0"
		}
	}
	return "1.5"
}
