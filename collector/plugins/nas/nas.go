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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultTimeout     = 10 * time.Second
	defaultConcurrency = 16
	// synologyHTTPSPort is the Synology DSM web API over HTTPS. The plain-HTTP
	// port 5000 is never contacted: the login would carry the account
	// readable (COL-02, F-011).
	synologyHTTPSPort = 5001
	// maxAPIResponse caps the JSON answer read from a NAS API.
	maxAPIResponse = 1 << 20
)

// Known NAS vendor API ports
var nasPorts = []struct {
	port     int
	protocol string
	vendor   string
}{
	{synologyHTTPSPort, "https", "synology"},
	{8080, "http", "qnap"},
	{443, "https", "qnap"},
	{80, "http", "truenas"},
	{443, "https", "truenas"},
}

// Plugin discovers and collects data from NAS platforms.
//
// Credentials are sent only over HTTPS with certificate verification
// (COL-02): by default against the system roots including the target
// address. Appliances with self-signed or internal certificates are
// reachable only through an explicit plugins.TLSScope for their network
// (RETICORA_NAS_TLS_SCOPES), which names a CA bundle or SPKI pins; without
// trust the collection fails with plugins.ErrTLSUntrusted before any request
// reaches the appliance. Synology DSM is spoken only over HTTPS (5001): the
// login is a POST whose body carries account and password, the session ID
// travels only in POST bodies, never in a URL (access logs of devices and
// proxies, F-011). The clients follow no redirects, so no credential follows
// a redirect to plain HTTP or another host.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
	client      *http.Client
	scoped      plugins.TLSClients
}

// Option configures the plugin.
type Option func(*Plugin)

// WithTLSScopes adds TLS exceptions per network. The first matching scope
// wins; targets outside every scope use the default verification.
func WithTLSScopes(scopes ...plugins.TLSScope) Option {
	return func(p *Plugin) {
		p.scoped.Add(p.newClient, scopes...)
	}
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a NAS plugin instance.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
	}
	p.client = p.newClient(plugins.SystemTLSConfig())
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// newClient returns a client that verifies with cfg and follows no
// redirects (plugins.NewTLSClient).
func (p *Plugin) newClient(cfg *tls.Config) *http.Client {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return plugins.NewTLSClient(timeout, cfg)
}

// clientFor returns the HTTP client whose TLS verification applies to target.
func (p *Plugin) clientFor(target string) *http.Client {
	fallback := p.client
	if fallback == nil {
		// Plugin constructed via struct literal instead of New().
		fallback = p.newClient(plugins.SystemTLSConfig())
	}
	return p.scoped.For(target, fallback)
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "nas" }

// Probe locates NAS appliances by probing vendor-specific API endpoints.
func (p *Plugin) Probe(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
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

			vendor, port, _ := p.detectNASVendor(ctx, t)
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

// Collect implements plugins.Plugin: the DeviceRecord of target (DIS-03).
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	observedAt := time.Now()
	res, err := p.collect(ctx, target, creds)
	return plugins.CollectRecord(p.Name(), res, err, observedAt)
}

// collect gathers chassis, storage, and health information from one NAS appliance.
func (p *Plugin) collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	vendor, port, untrusted := p.detectNASVendor(ctx, target)
	if vendor == "" {
		if untrusted != nil {
			return nil, untrusted
		}
		return nil, fmt.Errorf("nas: no NAS vendor detected at %s", target)
	}

	username := creds["username"]
	password := creds["password"]

	var result *plugins.Result
	var err error

	switch vendor {
	case "synology":
		result, err = p.collectSynology(ctx, target, username, password)
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

// detectNASVendor probes known NAS ports and identifies the vendor. An
// HTTPS port whose certificate is not trusted is skipped; when no vendor is
// found, untrusted reports the first such port (plugins.ErrTLSUntrusted).
func (p *Plugin) detectNASVendor(ctx context.Context, target string) (vendor string, port int, untrusted error) {
	client := p.clientFor(target)
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
		baseURL := fmt.Sprintf("%s://%s", probe.protocol, addr)
		verified, err := p.verifyVendor(ctx, client, baseURL, probe.vendor)
		if verified {
			return probe.vendor, probe.port, nil
		}
		if err != nil && untrusted == nil {
			untrusted = err
		}
	}
	return "", 0, untrusted
}

// verifyVendor checks the vendor-specific endpoint of baseURL. It sends no
// credentials; an untrusted certificate is reported as
// plugins.ErrTLSUntrusted.
func (p *Plugin) verifyVendor(ctx context.Context, client *http.Client, baseURL, vendor string) (bool, error) {
	var checkURL string
	switch vendor {
	case "synology":
		checkURL = baseURL + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query"
	case "qnap":
		checkURL = baseURL + "/cgi-bin/authLogin.cgi"
	case "truenas":
		checkURL = baseURL + "/api/v2.0/system/info"
	default:
		return false, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return false, nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, untrustedError(req, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// Any response from vendor-specific endpoint confirms the vendor
	return resp.StatusCode < 500, nil
}

// untrustedError reports a failed certificate verification of req as
// plugins.ErrTLSUntrusted (the handshake failed, nothing was sent); other
// failures yield nil.
func untrustedError(req *http.Request, err error) error {
	if !plugins.UntrustedCertificate(err) {
		return nil
	}
	return fmt.Errorf("nas: %w: certificate of %s not trusted by the system roots or RETICORA_NAS_TLS_SCOPES, no credentials sent: %v",
		plugins.ErrTLSUntrusted, req.URL.Host, err)
}

// collectSynology queries the Synology DSM API for system information, only
// over HTTPS (synologyHTTPSPort) with the verification of target.
func (p *Plugin) collectSynology(ctx context.Context, target, username, password string) (*plugins.Result, error) {
	baseURL := "https://" + net.JoinHostPort(target, strconv.Itoa(synologyHTTPSPort))
	client := p.clientFor(target)

	result := &plugins.Result{
		CIType: "nas",
		Name:   target,
		IP:     target,
		Attributes: map[string]any{
			"category": "nas",
			"vendor":   "synology",
			"apiPort":  synologyHTTPSPort,
		},
	}

	// Attempt to authenticate and retrieve system info
	if username != "" {
		sid, err := p.synologyLogin(ctx, client, baseURL, username, password)
		if err != nil {
			return nil, err
		}
		if sid != "" {
			info := p.synologySystemInfo(ctx, client, baseURL, sid)
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

// synologyLogin signs in with POST /webapi/auth.cgi: account and password
// travel only in the form body, never in the URL. It returns the session ID,
// "" when the login failed, and plugins.ErrTLSUntrusted when the
// certificate is not trusted (the credentials were not sent).
func (p *Plugin) synologyLogin(ctx context.Context, client *http.Client, baseURL, username, password string) (string, error) {
	var result struct {
		Data struct {
			SID string `json:"sid"`
		} `json:"data"`
		Success bool `json:"success"`
	}
	ok, err := p.synologyPost(ctx, client, baseURL+"/webapi/auth.cgi", url.Values{
		"api": {"SYNO.API.Auth"}, "version": {"3"}, "method": {"login"},
		"account": {username}, "passwd": {password}, "format": {"sid"},
	}, &result)
	if !ok || !result.Success {
		return "", err
	}
	return result.Data.SID, nil
}

// synologySystemInfo reads SYNO.DSM.Info; the session ID travels only in the
// POST body.
func (p *Plugin) synologySystemInfo(ctx context.Context, client *http.Client, baseURL, sid string) map[string]any {
	var result struct {
		Data    map[string]any `json:"data"`
		Success bool           `json:"success"`
	}
	ok, _ := p.synologyPost(ctx, client, baseURL+"/webapi/entry.cgi", url.Values{
		"api": {"SYNO.DSM.Info"}, "version": {"2"}, "method": {"getinfo"}, "_sid": {sid},
	}, &result)
	if !ok || !result.Success {
		return nil
	}
	return result.Data
}

// synologyPost sends form as the body of a POST to endpoint and decodes the
// JSON answer into out; ok reports a decoded 200 answer. A redirect is not
// followed, so the body never reaches another URL. Neither the URL nor an
// error carries a form value.
func (p *Plugin) synologyPost(ctx context.Context, client *http.Client, endpoint string, form url.Values, out any) (ok bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return false, untrustedError(req, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponse)).Decode(out) == nil, nil
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
		info := p.truenasSystemInfo(ctx, p.clientFor(target), baseURL, username, password)
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

// truenasSystemInfo reads the system info with HTTP Basic credentials, sent
// only after the certificate check of the handshake succeeded.
func (p *Plugin) truenasSystemInfo(ctx context.Context, client *http.Client, baseURL, username, password string) map[string]any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2.0/system/info", http.NoBody)
	if err != nil {
		return nil
	}
	req.SetBasicAuth(username, password)

	resp, err := client.Do(req)
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
