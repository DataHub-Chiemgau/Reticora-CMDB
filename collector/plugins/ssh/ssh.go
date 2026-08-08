// Package ssh provides an SSH-driven command collection plugin for server inventory.
package ssh

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultPort    = 22
	defaultTimeout = 10 * time.Second
)

// Plugin collects inventory from systems reachable over SSH.
type Plugin struct {
	Timeout time.Duration
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates an SSH plugin instance.
func New() *Plugin {
	return &Plugin{Timeout: defaultTimeout}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "ssh" }

// Discover determines which targets accept SSH connections by reading the banner.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	_ = creds
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	var results []plugins.Result
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}

		addr := net.JoinHostPort(target, fmt.Sprintf("%d", defaultPort))
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			continue
		}

		// Read SSH banner (server identification string)
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			conn.Close()
			continue
		}
		scanner := bufio.NewScanner(conn)
		var banner string
		if scanner.Scan() {
			banner = scanner.Text()
		}
		conn.Close()

		if banner == "" {
			continue
		}

		ciType := classifyFromBanner(banner)
		result := plugins.Result{
			CIType: ciType,
			Name:   target,
			IP:     target,
			Attributes: map[string]any{
				"transport": "ssh",
				"sshBanner": banner,
				"sshPort":   defaultPort,
			},
		}

		results = append(results, result)
	}

	if results == nil {
		results = []plugins.Result{}
	}
	return results, ctx.Err()
}

// Collect executes common system information commands on a single target.
// Note: Full SSH authentication requires golang.org/x/crypto/ssh which would
// be added as a dependency. This implementation performs banner-based fingerprinting
// and provides the command set that would be executed with proper credentials.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	addr := net.JoinHostPort(target, fmt.Sprintf("%d", defaultPort))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("ssh: connect %s: %w", target, err)
	}
	defer conn.Close()

	// Read banner for fingerprinting
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("ssh: set deadline: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	var banner string
	if scanner.Scan() {
		banner = scanner.Text()
	}

	ciType := classifyFromBanner(banner)
	osInfo := parseOSFromBanner(banner)

	hostname := target
	if names, err := net.LookupAddr(target); err != nil {
		slog.Debug("ssh: reverse DNS lookup failed, falling back to target address", "target", target, "error", err)
	} else if len(names) > 0 {
		hostname = strings.TrimSuffix(names[0], ".")
	}

	// Define the commands that would be executed with proper authentication.
	// In production, these would run over an authenticated SSH session.
	commandSet := map[string]string{
		"hostname":   "hostname -f",
		"uname":      "uname -a",
		"os_release": "cat /etc/os-release",
		"cpu":        "lscpu",
		"memory":     "free -b",
		"disk":       "lsblk -J",
		"uptime":     "uptime -s",
		"serial":     "dmidecode -s system-serial-number",
		"mfg":        "dmidecode -s system-manufacturer",
		"model":      "dmidecode -s system-product-name",
	}

	result := &plugins.Result{
		CIType:   ciType,
		Name:     hostname,
		IP:       target,
		Firmware: banner,
		Attributes: map[string]any{
			"transport":  "ssh",
			"sshBanner":  banner,
			"osFamily":   osInfo,
			"commandSet": commandSet,
			"sshPort":    defaultPort,
		},
	}

	return result, nil
}

// classifyFromBanner derives a CI type from the SSH identification string.
func classifyFromBanner(banner string) string {
	lower := strings.ToLower(banner)
	switch {
	case strings.Contains(lower, "cisco"):
		return "network.device"
	case strings.Contains(lower, "junos") || strings.Contains(lower, "juniper"):
		return "network.device"
	case strings.Contains(lower, "mikrotik"):
		return "network.device"
	case strings.Contains(lower, "fortios") || strings.Contains(lower, "fortigate"):
		return "network.device"
	case strings.Contains(lower, "arista"):
		return "network.device"
	case strings.Contains(lower, "ubuntu"), strings.Contains(lower, "debian"),
		strings.Contains(lower, "openssh"):
		return "server"
	default:
		return "server"
	}
}

// parseOSFromBanner extracts OS hints from the SSH banner.
func parseOSFromBanner(banner string) string {
	lower := strings.ToLower(banner)
	switch {
	case strings.Contains(lower, "ubuntu"):
		return "linux/ubuntu"
	case strings.Contains(lower, "debian"):
		return "linux/debian"
	case strings.Contains(lower, "cisco"):
		return "cisco/ios"
	case strings.Contains(lower, "junos"):
		return "juniper/junos"
	case strings.Contains(lower, "freebsd"):
		return "freebsd"
	default:
		return "linux"
	}
}
