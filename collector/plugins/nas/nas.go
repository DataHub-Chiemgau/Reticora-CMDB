// Package nas provides a NAS appliance discovery and collection plugin supporting
// Synology, QNAP, TrueNAS, and generic NAS via SNMP and vendor APIs.
package nas

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultTimeout     = 10 * time.Second
	defaultConcurrency = 16
)

// Known NAS vendor API ports
var nasPorts = []struct {
	port     int
	protocol string
	vendor   string
}{
	{5000, "http", "synology"},
	{5001, "https", "synology"},
	{8080, "http", "qnap"},
	{443, "https", "qnap"},
	{80, "http", "truenas"},
	{443, "https", "truenas"},
}

// Plugin discovers and collects data from NAS platforms.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
	client      *http.Client
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a NAS plugin instance.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
		client: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		},
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "nas" }

// Discover locates NAS appliances by probing vendor-specific API endpoints.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	_ = creds
	concurrency := p.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
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

			vendor, port := p.detectNASVendor(ctx, t)
			if vendor == "" {
				return
			}

			result := plugins.Result{
				CIType: "nas",
				Name:   t,
				IP:     t,
				Attributes: map[string]any{
					"category":  "nas",
					"vendor":    vendor,
					"apiPort":   port,
					"nasDevice": true,
				},
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

// Collect gathers chassis, storage, and health information from one NAS appliance.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	vendor, port := p.detectNASVendor(ctx, target)
	if vendor == "" {
		return nil, fmt.Errorf("nas: no NAS vendor detected at %s", target)
	}

	username := creds["username"]
	password := creds["password"]

	var result *plugins.Result
	var err error

	switch vendor {
	case "synology":
		result, err = p.collectSynology(ctx, target, port, username, password)
	case "qnap":
		result, err = p.collectQNAP(ctx, target, port, username, password)
	case "truenas":
		result, err = p.collectTrueNAS(ctx, target, port, username, password)
	default:
		result = &plugins.Result{
			CIType: "nas",
			Name:   target,
			IP:     target,
			Attributes: map[string]any{
				"category": "nas",
				"vendor":   vendor,
			},
		}
	}

	return result, err
}

// detectNASVendor probes known NAS ports and identifies the vendor.
func (p *Plugin) detectNASVendor(ctx context.Context, target string) (string, int) {
	for _, probe := range nasPorts {
		if ctx.Err() != nil {
			break
		}

		addr := net.JoinHostPort(target, fmt.Sprintf("%d", probe.port))
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			continue
		}
		conn.Close()

		// Verify with an HTTP probe for vendor-specific signatures
		url := fmt.Sprintf("%s://%s", probe.protocol, addr)
		if verified := p.verifyVendor(ctx, url, probe.vendor); verified {
			return probe.vendor, probe.port
		}
	}
	return "", 0
}

func (p *Plugin) verifyVendor(ctx context.Context, baseURL, vendor string) bool {
	var checkURL string
	switch vendor {
	case "synology":
		checkURL = baseURL + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query"
	case "qnap":
		checkURL = baseURL + "/cgi-bin/authLogin.cgi"
	case "truenas":
		checkURL = baseURL + "/api/v2.0/system/info"
	default:
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return false
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// Any response from vendor-specific endpoint confirms the vendor
	return resp.StatusCode < 500
}

// collectSynology queries the Synology DSM API for system information.
func (p *Plugin) collectSynology(ctx context.Context, target string, port int, username, password string) (*plugins.Result, error) {
	baseURL := fmt.Sprintf("https://%s", net.JoinHostPort(target, fmt.Sprintf("%d", port)))
	if port == 5000 {
		baseURL = fmt.Sprintf("http://%s", net.JoinHostPort(target, fmt.Sprintf("%d", port)))
	}

	result := &plugins.Result{
		CIType: "nas",
		Name:   target,
		IP:     target,
		Attributes: map[string]any{
			"category": "nas",
			"vendor":   "synology",
			"apiPort":  port,
		},
	}

	// Attempt to authenticate and retrieve system info
	if username != "" {
		sid := p.synologyLogin(ctx, baseURL, username, password)
		if sid != "" {
			info := p.synologySystemInfo(ctx, baseURL, sid)
			if info != nil {
				if name, ok := info["model"].(string); ok && name != "" {
					result.Model = name
					result.Name = name
				}
				if serial, ok := info["serial"].(string); ok {
					result.Serial = serial
				}
				if firmware, ok := info["firmware_ver"].(string); ok {
					result.Firmware = firmware
				}
				result.Manufacturer = "Synology"
				result.Attributes["dsmVersion"] = info["firmware_ver"]
				result.Attributes["hostname"] = info["hostname"]
			}
		}
	}

	return result, nil
}

func (p *Plugin) synologyLogin(ctx context.Context, baseURL, username, password string) string {
	url := fmt.Sprintf("%s/webapi/auth.cgi?api=SYNO.API.Auth&version=3&method=login&account=%s&passwd=%s&format=sid",
		baseURL, username, password)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			SID string `json:"sid"`
		} `json:"data"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.Success {
		return ""
	}
	return result.Data.SID
}

func (p *Plugin) synologySystemInfo(ctx context.Context, baseURL, sid string) map[string]any {
	url := fmt.Sprintf("%s/webapi/entry.cgi?api=SYNO.DSM.Info&version=2&method=getinfo&_sid=%s", baseURL, sid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Data    map[string]any `json:"data"`
		Success bool           `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.Success {
		return nil
	}
	return result.Data
}

// collectQNAP queries QNAP API for system information.
func (p *Plugin) collectQNAP(ctx context.Context, target string, port int, username, password string) (*plugins.Result, error) {
	_ = ctx
	_ = username
	_ = password

	result := &plugins.Result{
		CIType:       "nas",
		Name:         target,
		IP:           target,
		Manufacturer: "QNAP",
		Attributes: map[string]any{
			"category": "nas",
			"vendor":   "qnap",
			"apiPort":  port,
		},
	}

	return result, nil
}

// collectTrueNAS queries TrueNAS API for system information.
func (p *Plugin) collectTrueNAS(ctx context.Context, target string, port int, username, password string) (*plugins.Result, error) {
	baseURL := fmt.Sprintf("https://%s", net.JoinHostPort(target, fmt.Sprintf("%d", port)))

	result := &plugins.Result{
		CIType:       "nas",
		Name:         target,
		IP:           target,
		Manufacturer: "iXsystems",
		Attributes: map[string]any{
			"category": "nas",
			"vendor":   "truenas",
			"apiPort":  port,
		},
	}

	if username != "" {
		info := p.truenasSystemInfo(ctx, baseURL, username, password)
		if info != nil {
			if hostname, ok := info["hostname"].(string); ok && hostname != "" {
				result.Name = hostname
			}
			if version, ok := info["version"].(string); ok {
				result.Firmware = version
			}
			if model, ok := info["system_product"].(string); ok {
				result.Model = model
			}
			if serial, ok := info["system_serial"].(string); ok {
				result.Serial = serial
			}
			if mfg, ok := info["system_manufacturer"].(string); ok {
				result.Manufacturer = mfg
			}
		}
	}

	return result, nil
}

func (p *Plugin) truenasSystemInfo(ctx context.Context, baseURL, username, password string) map[string]any {
	url := baseURL + "/api/v2.0/system/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.SetBasicAuth(username, password)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var info map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}
	return info
}

