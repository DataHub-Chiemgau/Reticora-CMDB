// Package ssh provides an SSH-driven command collection plugin for server inventory.
package ssh

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
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
	// Port is the SSH port (default 22).
	Port int
	// KnownHostsFile pins host keys (default RETICORA_SSH_KNOWN_HOSTS or
	// /var/lib/reticora-collector/ssh_known_hosts).
	KnownHostsFile string

	keysOnce sync.Once
	keys     *hostKeyStore
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates an SSH plugin instance.
func New() *Plugin {
	return &Plugin{Timeout: defaultTimeout}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "ssh" }

// Probe determines which targets accept SSH connections by reading the banner.
func (p *Plugin) Probe(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
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

		addr := net.JoinHostPort(target, fmt.Sprintf("%d", p.port()))
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
				"sshPort":   p.port(),
			},
		}

		results = append(results, result)
	}

	if results == nil {
		results = []plugins.Result{}
	}
	return results, ctx.Err()
}

// Collect implements plugins.Plugin: the DeviceRecord of target (DIS-03).
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	observedAt := time.Now()
	res, err := p.collect(ctx, target, creds)
	return plugins.CollectRecord(p.Name(), res, err, observedAt)
}

// collect fingerprints the target by its banner and, with credentials,
// logs in and runs the allowlisted inventory commands (DIS-04).
func (p *Plugin) collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.timeout()
	cfg, login, err := p.clientConfig(creds)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(target, fmt.Sprintf("%d", p.port()))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("ssh: connect %s: %w", target, err)
	}
	// Read banner for fingerprinting
	if err = conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh: set deadline: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	var banner string
	if scanner.Scan() {
		banner = scanner.Text()
	}
	conn.Close()

	hostname := target
	if names, lerr := net.LookupAddr(target); lerr != nil {
		slog.Debug("ssh: reverse DNS lookup failed, falling back to target address", "target", target, "error", lerr)
	} else if len(names) > 0 {
		hostname = strings.TrimSuffix(names[0], ".")
	}

	result := &plugins.Result{
		CIType: classifyFromBanner(banner),
		Name:   hostname,
		IP:     target,
		Attributes: map[string]any{
			"transport":     "ssh",
			"sshBanner":     banner,
			"osFamily":      parseOSFromBanner(banner),
			"sshPort":       p.port(),
			"authenticated": false,
		},
	}
	if !login {
		return result, nil
	}
	out, err := p.runAllowed(ctx, target, cfg)
	if err != nil {
		return nil, err
	}
	applyCommandOutput(result, out)
	return result, nil
}

// applyCommandOutput maps the command output onto the result.
func applyCommandOutput(result *plugins.Result, out map[string]string) {
	result.Attributes["authenticated"] = true
	if h := firstLine(out["hostname"]); h != "" {
		result.Name = h
		if strings.Contains(h, ".") {
			result.Attributes["fqdn"] = h
		}
		result.Attributes["hostname"] = strings.SplitN(h, ".", 2)[0]
	}
	if u := firstLine(out["uname"]); u != "" {
		result.Attributes["uname"] = u
	}
	if rel := osRelease(out["os_release"]); rel["NAME"] != "" {
		result.Attributes["osName"] = rel["NAME"]
		result.Attributes["osVersion"] = rel["VERSION_ID"]
		result.Attributes["os_release"] = rel["PRETTY_NAME"]
	}
	if f := strings.Fields(out["uptime"]); len(f) > 0 {
		if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
			result.Attributes["uptimeSeconds"] = int64(secs)
		}
	}
	result.Serial = usable(out["serial"])
	result.Manufacturer = usable(out["mfg"])
	result.Model = usable(out["model"])
	result.Firmware = usable(out["bios"])
	if id := usable(out["machine_id"]); id != "" {
		result.Attributes["hardwareUUID"] = strings.ToLower(id)
	}
	if n, err := strconv.Atoi(firstLine(out["cpu"])); err == nil {
		result.Attributes["cpuCount"] = n
	}
	for _, line := range strings.Split(out["memory"], "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[0] == "Mem:" {
			if b, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				result.Attributes["memoryBytes"] = b
			}
		}
	}
	result.CIType = "server"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// usable drops placeholder DMI values.
func usable(s string) string {
	s = firstLine(s)
	switch strings.ToLower(s) {
	case "", "none", "unknown", "not specified", "default string", "to be filled by o.e.m.", "system serial number", "0":
		return ""
	}
	return s
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
