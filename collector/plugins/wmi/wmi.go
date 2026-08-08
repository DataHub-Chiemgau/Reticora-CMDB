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
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	defaultHTTPPort    = 5985
	defaultHTTPSPort   = 5986
	defaultTimeout     = 15 * time.Second
	defaultConcurrency = 16
)

// Plugin collects Windows inventory and telemetry using WinRM.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
	UseHTTPS    bool
	client      *http.Client
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a WMI plugin instance.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
		UseHTTPS:    false,
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
func (p *Plugin) Name() string { return "wmi" }

// Discover identifies Windows hosts by probing WinRM ports (5985/5986).
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

			// Try HTTP port first, then HTTPS
			httpAddr := net.JoinHostPort(t, fmt.Sprintf("%d", defaultHTTPPort))
			httpsAddr := net.JoinHostPort(t, fmt.Sprintf("%d", defaultHTTPSPort))

			var reachable bool
			var port int
			var protocol string

			conn, err := net.DialTimeout("tcp", httpAddr, timeout)
			if err == nil {
				conn.Close()
				reachable = true
				port = defaultHTTPPort
				protocol = "http"
			} else {
				conn, err = net.DialTimeout("tcp", httpsAddr, timeout)
				if err == nil {
					conn.Close()
					reachable = true
					port = defaultHTTPSPort
					protocol = "https"
				}
			}

			if !reachable {
				return
			}

			result := plugins.Result{
				CIType: "windows-server",
				Name:   t,
				IP:     t,
				Attributes: map[string]any{
					"transport":    "winrm",
					"winrmPort":    port,
					"winrmScheme":  protocol,
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

// Collect retrieves hardware, OS, and service inventory from a Windows target via WinRM.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	username := creds["username"]
	password := creds["password"]
	if username == "" {
		return nil, fmt.Errorf("wmi: credentials required for WinRM collection")
	}

	port := defaultHTTPPort
	scheme := "http"
	if p.UseHTTPS {
		port = defaultHTTPSPort
		scheme = "https"
	}

	baseURL := fmt.Sprintf("%s://%s/wsman", scheme, net.JoinHostPort(target, fmt.Sprintf("%d", port)))

	// Execute WQL queries for system information via WS-Management
	osInfo := p.wsmanEnumerate(ctx, baseURL, username, password, "Win32_OperatingSystem")
	csInfo := p.wsmanEnumerate(ctx, baseURL, username, password, "Win32_ComputerSystem")
	biosInfo := p.wsmanEnumerate(ctx, baseURL, username, password, "Win32_BIOS")

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

	return result, nil
}

// wsmanEnumerate performs a WS-Management Enumerate request for a WMI class.
func (p *Plugin) wsmanEnumerate(ctx context.Context, baseURL, username, password, wmiClass string) string {
	// Build WS-Management SOAP Enumerate request
	soapEnvelope := buildWSManEnumerate(wmiClass)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(soapEnvelope))
	if err != nil {
		return ""
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")

	client := p.client
	if client == nil {
		// Plugin constructed via struct literal instead of New().
		client = &http.Client{Timeout: p.Timeout}
		if client.Timeout <= 0 {
			client.Timeout = defaultTimeout
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return ""
	}
	return string(body)
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
    <wsa:To>http://windows-host:5985/wsman</wsa:To>
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
