// Package wmi provides a WMI/WinRM collection plugin for Windows system inventory.
package wmi

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	// winRMHTTPSPort is WinRM over HTTPS. Plain-HTTP WinRM (5985) is never
	// used: HTTP Basic would carry the account readable (COL-02).
	winRMHTTPSPort     = 5986
	defaultTimeout     = 15 * time.Second
	defaultConcurrency = 16
)

// wmiClasses are the WMI classes one collection enumerates.
var wmiClasses = []string{"Win32_OperatingSystem", "Win32_ComputerSystem", "Win32_BIOS", "Win32_ComputerSystemProduct", "Win32_NetworkAdapterConfiguration"}

// Plugin collects Windows inventory and telemetry using WinRM.
//
// WinRM is spoken only over HTTPS (5986) with certificate verification
// (COL-02): by default against the system roots including the target
// address. Hosts with self-signed or internal certificates are reachable
// only through an explicit plugins.TLSScope for their network
// (RETICORA_WMI_TLS_SCOPES), which names a CA bundle or SPKI pins. Without
// trust the collection fails with plugins.ErrTLSUntrusted before any
// credential is sent; there is no plain-HTTP opt-in.
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

// New creates a WMI plugin instance.
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

// newClient returns a client that verifies with cfg. It follows no
// redirects: a redirect could send the Basic credentials to plain HTTP.
func (p *Plugin) newClient(cfg *tls.Config) *http.Client {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := plugins.NewTLSClient(timeout, cfg)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
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
func (p *Plugin) Name() string { return "wmi" }

// Probe identifies Windows hosts by probing the WinRM HTTPS port (5986); the
// plain-HTTP port is not contacted.
func (p *Plugin) Probe(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
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

			conn, err := net.DialTimeout("tcp", net.JoinHostPort(t, strconv.Itoa(winRMHTTPSPort)), timeout)
			if err != nil {
				return
			}
			conn.Close()

			result := plugins.Result{
				CIType: "windows-server",
				Name:   t,
				IP:     t,
				Attributes: map[string]any{
					"transport":    "winrm",
					"winrmPort":    winRMHTTPSPort,
					"winrmScheme":  "https",
					"winrmCapable": true,
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

// collect retrieves hardware, OS, and service inventory from a Windows target via WinRM.
func (p *Plugin) collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	username := creds["username"]
	password := creds["password"]
	if username == "" {
		return nil, fmt.Errorf("wmi: credentials required for WinRM collection")
	}

	baseURL := fmt.Sprintf("https://%s/wsman", net.JoinHostPort(target, strconv.Itoa(winRMHTTPSPort)))
	client := p.clientFor(target)

	// Execute WQL queries for system information via WS-Management
	info := make([]string, len(wmiClasses))
	for i, class := range wmiClasses {
		body, err := p.wsmanEnumerate(ctx, client, baseURL, username, password, class)
		if err != nil {
			return nil, err
		}
		info[i] = body
	}
	osInfo, csInfo, biosInfo, productInfo, nicInfo := info[0], info[1], info[2], info[3], info[4]

	hostname := target
	if v := extractWMIField(csInfo, "Name"); v != "" {
		hostname = v
	}

	result := &plugins.Result{
		CIType:       "windows-server",
		Name:         hostname,
		IP:           target,
		Manufacturer: extractWMIField(csInfo, "Manufacturer"),
		Model:        extractWMIField(csInfo, "Model"),
		Serial:       extractWMIField(biosInfo, "SerialNumber"),
		Firmware:     extractWMIField(biosInfo, "SMBIOSBIOSVersion"),
		Attributes: map[string]any{
			"transport":   "winrm",
			"osName":      extractWMIField(osInfo, "Caption"),
			"osVersion":   extractWMIField(osInfo, "Version"),
			"osBuild":     extractWMIField(osInfo, "BuildNumber"),
			"domain":      extractWMIField(csInfo, "Domain"),
			"totalMemory": extractWMIField(csInfo, "TotalPhysicalMemory"),
			"cpuCount":    extractWMIField(csInfo, "NumberOfProcessors"),
		},
	}
	applyIdentity(result, csInfo, productInfo, nicInfo, extractWMIField(osInfo, "LastBootUpTime"), time.Now())

	return result, nil
}

// wsmanEnumerate performs a WS-Management Enumerate request for a WMI class.
// It fails with plugins.ErrTLSUntrusted when the certificate of the endpoint
// is not trusted; the handshake failed, the credentials were not sent.
func (p *Plugin) wsmanEnumerate(ctx context.Context, client *http.Client, baseURL, username, password, wmiClass string) (string, error) {
	// Build WS-Management SOAP Enumerate request
	soapEnvelope := buildWSManEnumerate(wmiClass)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(soapEnvelope))
	if err != nil {
		return "", nil
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")

	// The credentials are sent after the certificate check of the handshake
	// succeeded (COL-02).
	resp, err := client.Do(req)
	if err != nil {
		if plugins.UntrustedCertificate(err) {
			return "", fmt.Errorf("wmi: %w: certificate of %s not trusted by the system roots or RETICORA_WMI_TLS_SCOPES, no credentials sent: %v",
				plugins.ErrTLSUntrusted, req.URL.Host, err)
		}
		return "", nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return "", nil
	}
	return string(body), nil
}

// buildWSManEnumerate creates a SOAP envelope for WS-Management Enumerate.
func buildWSManEnumerate(wmiClass string) string {
	resourceURI := fmt.Sprintf("http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/%s", wmiClass)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"
            xmlns:wsen="http://schemas.xmlsoap.org/ws/2004/09/enumeration"
            xmlns:wsman="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd">
  <s:Header>
    <wsa:To>https://windows-host:5986/wsman</wsa:To>
    <wsman:ResourceURI>%s</wsman:ResourceURI>
    <wsa:ReplyTo>
      <wsa:Address>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:Address>
    </wsa:ReplyTo>
    <wsa:Action>http://schemas.xmlsoap.org/ws/2004/09/enumeration/Enumerate</wsa:Action>
    <wsman:MaxEnvelopeSize>32768</wsman:MaxEnvelopeSize>
    <wsman:OperationTimeout>PT60S</wsman:OperationTimeout>
  </s:Header>
  <s:Body>
    <wsen:Enumerate/>
  </s:Body>
</s:Envelope>`, resourceURI)
}

// extractWMIField attempts to find a named XML element value in a WS-Management response.
func extractWMIField(xmlResponse, fieldName string) string {
	if xmlResponse == "" {
		return ""
	}

	// Use simple XML token parsing to find the field value
	decoder := xml.NewDecoder(strings.NewReader(xmlResponse))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if startEl, ok := token.(xml.StartElement); ok {
			if startEl.Name.Local == fieldName {
				var value string
				if err := decoder.DecodeElement(&value, &startEl); err == nil {
					return value
				}
			}
		}
	}
	return ""
}

// applyIdentity fills the DeviceRecord identity (DIS-04): hardware UUID,
// host name and FQDN, the network adapters with their MAC addresses and the
// uptime.
func applyIdentity(result *plugins.Result, csInfo, productInfo, nicInfo, lastBoot string, now time.Time) {
	if id := strings.ToLower(strings.TrimSpace(extractWMIField(productInfo, "UUID"))); len(id) == 36 && id != "ffffffff-ffff-ffff-ffff-ffffffffffff" && id != "00000000-0000-0000-0000-000000000000" {
		result.Attributes["hardwareUUID"] = id
	}
	host := extractWMIField(csInfo, "DNSHostName")
	if host == "" {
		host = extractWMIField(csInfo, "Name")
	}
	if host != "" {
		result.Attributes["hostname"] = strings.ToLower(host)
		if domain := extractWMIField(csInfo, "Domain"); strings.Contains(domain, ".") {
			result.Attributes["fqdn"] = strings.ToLower(host + "." + domain)
		}
	}
	for i, item := range extractWMIItems(nicInfo, "Win32_NetworkAdapterConfiguration") {
		mac := strings.ToLower(strings.TrimSpace(item["MACAddress"]))
		if mac == "" || !strings.EqualFold(item["IPEnabled"], "true") {
			continue
		}
		name := item["Description"]
		if name == "" {
			name = fmt.Sprintf("nic%d", i)
		}
		result.Interfaces = append(result.Interfaces, plugins.NetworkInterface{Name: name, MAC: mac, AdminUp: true, OperUp: true})
		if result.MAC == "" {
			result.MAC = mac
		}
	}
	if boot, err := parseCIMDateTime(lastBoot); err == nil && now.After(boot) {
		result.Attributes["uptimeSeconds"] = int64(now.Sub(boot).Seconds())
	}
}

// extractWMIItems returns the fields of every instance of class in an
// enumeration response.
func extractWMIItems(xmlResponse, class string) []map[string]string {
	var items []map[string]string
	var current map[string]string
	depth := 0
	decoder := xml.NewDecoder(strings.NewReader(xmlResponse))
	for {
		token, err := decoder.Token()
		if err != nil {
			return items
		}
		switch el := token.(type) {
		case xml.StartElement:
			switch {
			case el.Name.Local == class && current == nil:
				current, depth = map[string]string{}, 0
			case current != nil:
				depth++
				if depth == 1 {
					var value string
					if decoder.DecodeElement(&value, &el) == nil {
						current[el.Name.Local] = value
					}
					depth--
				}
			}
		case xml.EndElement:
			if current != nil && el.Name.Local == class {
				items = append(items, current)
				current = nil
			}
		}
	}
}

// parseCIMDateTime parses a CIM datetime ("20261013081500.000000+120",
// offset in minutes).
func parseCIMDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) < 25 {
		return time.Time{}, fmt.Errorf("wmi: bad datetime %q", s)
	}
	t, err := time.Parse("20060102150405", s[:14])
	if err != nil {
		return time.Time{}, err
	}
	offset, err := strconv.Atoi(s[21:])
	if err != nil {
		return time.Time{}, err
	}
	return t.Add(-time.Duration(offset) * time.Minute).UTC(), nil
}
