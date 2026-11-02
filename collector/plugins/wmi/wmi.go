// Package wmi provides a WMI/WinRM collection plugin for Windows system inventory.
package wmi

import (
	"context"
	"crypto/rand"
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

	wsmanActionEnumerate = "http://schemas.xmlsoap.org/ws/2004/09/enumeration/Enumerate"
	wsmanActionPull      = "http://schemas.xmlsoap.org/ws/2004/09/enumeration/Pull"
	// maxEnvelopeSize is the largest response WinRM may send
	// (wsman:MaxEnvelopeSize); a longer response is refused, not truncated.
	maxEnvelopeSize  = 32768
	maxResponseBytes = 2 * maxEnvelopeSize
	// maxPulls and maxEnumerationBytes bound one enumeration: a WinRM that
	// never ends the sequence fails instead of looping.
	maxPulls            = 256
	maxEnumerationBytes = 4 << 20
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

// wsmanEnumerate enumerates the instances of a WMI class as DSP0226 (8.2)
// and WS-Enumeration specify: Enumerate with OptimizeEnumeration, then Pull
// with the latest enumeration context until EndOfSequence. It returns the
// Items of all responses. Every failure is an error (DIS-08), see wsmanCall.
func (p *Plugin) wsmanEnumerate(ctx context.Context, client *http.Client, baseURL, username, password, wmiClass string) (string, error) {
	resp, err := p.wsmanCall(ctx, client, baseURL, username, password, "Enumerate "+wmiClass, buildWSManEnumerate(baseURL, wmiClass, newMessageID()))
	if err != nil {
		return "", err
	}
	batch := resp.Body.Enumerate
	if batch == nil {
		return "", fmt.Errorf("wmi: Enumerate %s: unexpected response without EnumerateResponse", wmiClass)
	}
	var items strings.Builder
	items.WriteString("<Items>")
	for pulls := 0; ; pulls++ {
		items.WriteString(batch.Items.XML)
		if items.Len() > maxEnumerationBytes {
			return "", fmt.Errorf("wmi: Enumerate %s: the instances exceed %d bytes", wmiClass, maxEnumerationBytes)
		}
		if batch.EndOfSequence != nil {
			break
		}
		if batch.Context == "" {
			return "", fmt.Errorf("wmi: Enumerate %s: unexpected response without EnumerationContext and EndOfSequence", wmiClass)
		}
		if pulls == maxPulls {
			return "", fmt.Errorf("wmi: Enumerate %s: no EndOfSequence after %d Pull requests", wmiClass, maxPulls)
		}
		resp, err = p.wsmanCall(ctx, client, baseURL, username, password, "Pull "+wmiClass, buildWSManPull(baseURL, wmiClass, newMessageID(), batch.Context))
		if err != nil {
			return "", err
		}
		next := resp.Body.Pull
		if next == nil {
			return "", fmt.Errorf("wmi: Pull %s: unexpected response without PullResponse", wmiClass)
		}
		if next.Context == "" {
			// Without a new context the previous one stays valid.
			next.Context = batch.Context
		}
		batch = next
	}
	items.WriteString("</Items>")
	return items.String(), nil
}

// wsmanResponse is the part of a WS-Management response the plugin reads.
type wsmanResponse struct {
	XMLName xml.Name `xml:"http://www.w3.org/2003/05/soap-envelope Envelope"`
	Body    struct {
		Fault     *wsmanFault       `xml:"Fault"`
		Enumerate *enumerationBatch `xml:"EnumerateResponse"`
		Pull      *enumerationBatch `xml:"PullResponse"`
	} `xml:"Body"`
}

// enumerationBatch is an EnumerateResponse or PullResponse: the context for
// the next Pull, the instances (wsman:Items or wsen:Items) and the end of the
// sequence.
type enumerationBatch struct {
	Context string `xml:"EnumerationContext"`
	Items   struct {
		XML string `xml:",innerxml"`
	} `xml:"Items"`
	EndOfSequence *struct{} `xml:"EndOfSequence"`
}

// wsmanFault is a SOAP 1.2 fault; WinRM names the error in the subcode
// (e.g. w:AccessDenied, w:InvalidSelectors).
type wsmanFault struct {
	Subcode string `xml:"Code>Subcode>Value"`
	Reason  string `xml:"Reason>Text"`
}

// wsmanCall posts one WS-Management request (op names it in errors). It
// fails with plugins.ErrTLSUntrusted when the certificate of the endpoint is
// not trusted (the handshake failed, the credentials were not sent), with
// plugins.ErrUnreachable when the connection fails or breaks off, with
// plugins.ErrAuthFailed when WinRM refuses the credentials (HTTP 401/403,
// SOAP fault AccessDenied), and with an error on any other SOAP fault,
// non-2xx status or a response that is no SOAP envelope.
func (p *Plugin) wsmanCall(ctx context.Context, client *http.Client, baseURL, username, password, op, envelope string) (*wsmanResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(envelope))
	if err != nil {
		return nil, fmt.Errorf("wmi: %s: %w", op, err)
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")

	// The credentials are sent after the certificate check of the handshake
	// succeeded (COL-02).
	resp, err := client.Do(req)
	if err != nil {
		switch {
		case plugins.UntrustedCertificate(err):
			return nil, fmt.Errorf("wmi: %w: certificate of %s not trusted by the system roots or RETICORA_WMI_TLS_SCOPES, no credentials sent: %v",
				plugins.ErrTLSUntrusted, req.URL.Host, err)
		case ctx.Err() != nil:
			return nil, fmt.Errorf("wmi: %s: %w", op, ctx.Err())
		}
		return nil, fmt.Errorf("wmi: %w: %s: %v", plugins.ErrUnreachable, op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("wmi: %w: %s: WinRM refused the credentials (HTTP %s)", plugins.ErrAuthFailed, op, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("wmi: %w: %s: reading the response: %v", plugins.ErrUnreachable, op, err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("wmi: %s: response exceeds %d bytes (HTTP status %d)", op, maxResponseBytes, resp.StatusCode)
	}
	var env wsmanResponse
	decodeErr := xml.Unmarshal(body, &env)
	if f := env.Body.Fault; decodeErr == nil && f != nil {
		// WinRM answers faults with HTTP 500; the subcode says why.
		_, subcode, _ := strings.Cut(strings.TrimSpace(f.Subcode), ":")
		if subcode == "" {
			subcode = strings.TrimSpace(f.Subcode)
		}
		reason := strings.Join(strings.Fields(f.Reason), " ")
		if subcode == "AccessDenied" {
			return nil, fmt.Errorf("wmi: %w: %s: SOAP fault %s: %s", plugins.ErrAuthFailed, op, subcode, reason)
		}
		return nil, fmt.Errorf("wmi: %s: SOAP fault %s (HTTP status %d): %s", op, subcode, resp.StatusCode, reason)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("wmi: %s: unexpected HTTP status %s", op, resp.Status)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("wmi: %s: malformed response: %v", op, decodeErr)
	}
	return &env, nil
}

// buildWSManEnumerate creates the Enumerate request for a WMI class
// (DSP0226 8.2): OptimizeEnumeration asks for the first instances in the
// EnumerateResponse.
func buildWSManEnumerate(to, wmiClass, messageID string) string {
	return buildWSManRequest(to, wmiClass, wsmanActionEnumerate, messageID, `<wsen:Enumerate>
      <wsman:OptimizeEnumeration/>
      <wsman:MaxElements>32000</wsman:MaxElements>
    </wsen:Enumerate>`)
}

// buildWSManPull creates the Pull request that continues an enumeration.
func buildWSManPull(to, wmiClass, messageID, enumerationContext string) string {
	return buildWSManRequest(to, wmiClass, wsmanActionPull, messageID, `<wsen:Pull>
      <wsen:EnumerationContext>`+xmlText(enumerationContext)+`</wsen:EnumerationContext>
      <wsen:MaxElements>32000</wsen:MaxElements>
    </wsen:Pull>`)
}

// buildWSManRequest creates a SOAP envelope for WS-Management with the
// addressing headers DSP0226 requires (To, ResourceURI, ReplyTo, Action,
// MessageID).
func buildWSManRequest(to, wmiClass, action, messageID, body string) string {
	resourceURI := fmt.Sprintf("http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/%s", wmiClass)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"
            xmlns:wsen="http://schemas.xmlsoap.org/ws/2004/09/enumeration"
            xmlns:wsman="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd">
  <s:Header>
    <wsa:To>%s</wsa:To>
    <wsman:ResourceURI s:mustUnderstand="true">%s</wsman:ResourceURI>
    <wsa:ReplyTo>
      <wsa:Address s:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:Address>
    </wsa:ReplyTo>
    <wsa:Action s:mustUnderstand="true">%s</wsa:Action>
    <wsa:MessageID>%s</wsa:MessageID>
    <wsman:MaxEnvelopeSize s:mustUnderstand="true">%d</wsman:MaxEnvelopeSize>
    <wsman:OperationTimeout>PT60S</wsman:OperationTimeout>
  </s:Header>
  <s:Body>
    %s
  </s:Body>
</s:Envelope>`, xmlText(to), resourceURI, action, messageID, maxEnvelopeSize, body)
}

// newMessageID returns a fresh wsa:MessageID (uuid: plus a random UUID).
func newMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// xmlText escapes s for XML character data.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
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
