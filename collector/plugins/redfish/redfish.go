// Package redfish provides a Redfish/BMC API collection plugin for hardware inventory.
package redfish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
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
	defaultPort        = 443
	defaultTimeout     = 15 * time.Second
	defaultConcurrency = 16
	serviceRootPath    = "/redfish/v1/"
)

// Plugin collects hardware facts from Redfish-capable management controllers.
//
// TLS certificates are always verified (COL-02): by default against the
// system roots including the target address. BMCs with self-signed or
// internal certificates are reachable only through an explicit TLSScope for
// their network, which names a CA bundle or SPKI pins.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
	client      *http.Client
	scoped      []scopedClient
	port        int
}

// TLSScope is a TLS exception for the targets of one network: certificates
// are verified against RootCAs instead of the system roots, and/or the
// SHA-256 hash of the leaf certificate's SubjectPublicKeyInfo must match one
// of PinsSHA256. With pins only, the chain is not verified (self-signed BMC
// certificates); the pin is the trust anchor.
type TLSScope struct {
	Network    *net.IPNet
	RootCAs    *x509.CertPool
	PinsSHA256 [][]byte
}

type scopedClient struct {
	network *net.IPNet
	client  *http.Client
}

// Option configures the plugin.
type Option func(*Plugin)

// WithTLSScopes adds TLS exceptions per network. The first matching scope
// wins; targets outside every scope use the default verification.
func WithTLSScopes(scopes ...TLSScope) Option {
	return func(p *Plugin) {
		for _, s := range scopes {
			if s.Network == nil || (s.RootCAs == nil && len(s.PinsSHA256) == 0) {
				continue
			}
			p.scoped = append(p.scoped, scopedClient{network: s.Network, client: p.newClient(scopeTLSConfig(s))})
		}
	}
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a Redfish plugin instance.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
		port:        defaultPort,
	}
	p.client = p.newClient(&tls.Config{MinVersion: tls.VersionTLS12})
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Plugin) newClient(cfg *tls.Config) *http.Client {
	return &http.Client{
		Timeout: p.Timeout,
		Transport: &http.Transport{
			TLSClientConfig:     cfg,
			MaxIdleConnsPerHost: 4,
		},
	}
}

// scopeTLSConfig builds the verification of one TLS scope.
func scopeTLSConfig(s TLSScope) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.RootCAs}
	if len(s.PinsSHA256) == 0 {
		return cfg
	}
	pins := s.PinsSHA256
	if s.RootCAs == nil {
		// Pinned self-signed certificate: the chain cannot be verified, the
		// pin check below replaces it.
		cfg.InsecureSkipVerify = true //nolint:gosec // verified by VerifyConnection (SPKI pin)
	}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("redfish: no peer certificate")
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
		for _, pin := range pins {
			if bytes.Equal(pin, sum[:]) {
				return nil
			}
		}
		return errors.New("redfish: certificate does not match a configured pin")
	}
	return cfg
}

// clientFor returns the HTTP client whose TLS verification applies to target.
func (p *Plugin) clientFor(target string) *http.Client {
	if ip := net.ParseIP(target); ip != nil {
		for _, s := range p.scoped {
			if s.network.Contains(ip) {
				return s.client
			}
		}
	}
	return p.client
}

func (p *Plugin) baseURL(target string) string {
	port := p.port
	if port == 0 {
		port = defaultPort
	}
	return fmt.Sprintf("https://%s", net.JoinHostPort(target, fmt.Sprintf("%d", port)))
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "redfish" }

// Discover identifies Redfish endpoints from a set of candidate targets.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
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

			url := p.baseURL(t) + serviceRootPath
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return
			}

			if user := creds["username"]; user != "" {
				req.SetBasicAuth(user, creds["password"])
			}

			// Credentials (Redfish only, see plugins.CredentialsFor) are sent
			// after the certificate check of the handshake succeeded.
			resp, err := p.clientFor(t).Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)

			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				result := plugins.Result{
					CIType: "bmc",
					Name:   t,
					IP:     t,
					Attributes: map[string]any{
						"api":           "redfish",
						"redfishRoot":   url,
						"authenticated": resp.StatusCode == http.StatusOK,
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

// serviceRoot represents the Redfish ServiceRoot response.
type serviceRoot struct {
	RedfishVersion string `json:"RedfishVersion"`
	UUID           string `json:"UUID"`
	Product        string `json:"Product"`
	Vendor         string `json:"Vendor"`
	Systems        link   `json:"Systems"`
	Chassis        link   `json:"Chassis"`
	Managers       link   `json:"Managers"`
}

type link struct {
	ID string `json:"@odata.id"`
}

type systemCollection struct {
	Members []link `json:"Members"`
}

type computerSystem struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
	SerialNumber string `json:"SerialNumber"`
	UUID         string `json:"UUID"`
	HostName     string `json:"HostName"`
	BiosVersion  string `json:"BiosVersion"`
	PowerState   string `json:"PowerState"`
	SKU          string `json:"SKU"`
	Status       struct {
		State  string `json:"State"`
		Health string `json:"Health"`
	} `json:"Status"`
	ProcessorSummary struct {
		Count int    `json:"Count"`
		Model string `json:"Model"`
	} `json:"ProcessorSummary"`
	MemorySummary struct {
		TotalSystemMemoryGiB float64 `json:"TotalSystemMemoryGiB"`
	} `json:"MemorySummary"`
}

// Collect retrieves system, chassis, and sensor details from a single Redfish endpoint.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	baseURL := p.baseURL(target)
	client := p.clientFor(target)
	username := creds["username"]
	password := creds["password"]

	// Fetch service root
	var root serviceRoot
	if err := p.redfishGet(ctx, client, baseURL+serviceRootPath, username, password, &root); err != nil {
		return nil, fmt.Errorf("redfish: service root %s: %w", target, err)
	}

	result := &plugins.Result{
		CIType: "bmc",
		Name:   target,
		IP:     target,
		Attributes: map[string]any{
			"api":            "redfish",
			"redfishVersion": root.RedfishVersion,
			"bmcUUID":        root.UUID,
			"bmcProduct":     root.Product,
			"bmcVendor":      root.Vendor,
		},
	}

	// Fetch systems collection
	if root.Systems.ID != "" {
		var systems systemCollection
		if err := p.redfishGet(ctx, client, baseURL+root.Systems.ID, username, password, &systems); err == nil && len(systems.Members) > 0 {
			var sys computerSystem
			if err := p.redfishGet(ctx, client, baseURL+systems.Members[0].ID, username, password, &sys); err == nil {
				result.Name = sys.Name
				if result.Name == "" {
					result.Name = sys.HostName
				}
				if result.Name == "" {
					result.Name = target
				}
				result.Manufacturer = sys.Manufacturer
				result.Model = sys.Model
				result.Serial = sys.SerialNumber
				result.Firmware = sys.BiosVersion
				result.CIType = "server"

				result.Attributes["powerState"] = sys.PowerState
				result.Attributes["systemUUID"] = sys.UUID
				result.Attributes["sku"] = sys.SKU
				result.Attributes["healthState"] = sys.Status.Health
				result.Attributes["cpuCount"] = sys.ProcessorSummary.Count
				result.Attributes["cpuModel"] = sys.ProcessorSummary.Model
				result.Attributes["memoryGiB"] = sys.MemorySummary.TotalSystemMemoryGiB
			}
		}
	}

	return result, nil
}

func (p *Plugin) redfishGet(ctx context.Context, client *http.Client, url, username, password string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if username != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if rerr != nil {
			return fmt.Errorf("HTTP %d (error body unreadable: %v)", resp.StatusCode, rerr)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
