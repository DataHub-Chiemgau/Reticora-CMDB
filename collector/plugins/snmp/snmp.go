// Package snmp provides an SNMP v2c/v3 (authPriv) collection plugin for network device discovery.
package snmp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
)

const (
	defaultPort        = 161
	defaultTimeout     = 5 * time.Second
	defaultRetries     = 2
	defaultConcurrency = 32
)

// Standard SNMP OIDs for device identification.
const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID = "1.3.6.1.2.1.1.2.0"
	oidSysName     = "1.3.6.1.2.1.1.5.0"
	oidSysLocation = "1.3.6.1.2.1.1.6.0"
	oidSysUpTime   = "1.3.6.1.2.1.1.3.0"
	oidSysContact  = "1.3.6.1.2.1.1.4.0"

	// IF-MIB interface table
	oidIfNumber   = "1.3.6.1.2.1.2.1.0"
	oidIfDescr    = "1.3.6.1.2.1.2.2.1.2"
	oidIfType     = "1.3.6.1.2.1.2.2.1.3"
	oidIfSpeed    = "1.3.6.1.2.1.2.2.1.5"
	oidIfPhysAddr = "1.3.6.1.2.1.2.2.1.6"
	oidIfAdminSt  = "1.3.6.1.2.1.2.2.1.7"
	oidIfOperSt   = "1.3.6.1.2.1.2.2.1.8"
	oidIfMtu      = "1.3.6.1.2.1.2.2.1.4"

	// Entity-MIB for hardware info
	oidEntPhysDescr       = "1.3.6.1.2.1.47.1.1.1.1.2"
	oidEntPhysSerialNum   = "1.3.6.1.2.1.47.1.1.1.1.11"
	oidEntPhysMfgName     = "1.3.6.1.2.1.47.1.1.1.1.12"
	oidEntPhysModelName   = "1.3.6.1.2.1.47.1.1.1.1.13"
	oidEntPhysFirmwareRev = "1.3.6.1.2.1.47.1.1.1.1.9"
)

// Plugin collects device inventory and metrics over SNMP.
type Plugin struct {
	Timeout     time.Duration
	Retries     int
	Concurrency int
	// Profiles resolves sysObjectID/MAC to vendor-specific classification.
	// nil loads the embedded profile set lazily.
	Profiles *profiles.Registry
	// Port is the agent port (default 161).
	Port int

	loadOnce sync.Once
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates an SNMP plugin instance.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Retries:     defaultRetries,
		Concurrency: defaultConcurrency,
	}
}

// profileRegistry returns the configured registry, lazily loading the
// embedded vendor profiles when none was set.
func (p *Plugin) profileRegistry() *profiles.Registry {
	p.loadOnce.Do(func() {
		if p.Profiles == nil {
			r := profiles.NewRegistry()
			if err := r.LoadEmbedded(); err != nil {
				r = profiles.NewRegistry() // defensive: never return nil
			}
			p.Profiles = r
		}
	})
	return p.Profiles
}

// applyProfile enriches a result with the vendor profile matching the
// device's sysObjectID (datengetriebenes Mapping, spec §5.4): vendor and
// model fill empty result fields and profile attributes are merged into the
// result (result attributes win on conflict).
func (p *Plugin) applyProfile(result *plugins.Result, sysObjectID string) {
	profile, ok := p.profileRegistry().BySysObjectID(sysObjectID)
	if !ok {
		return
	}
	if result.Manufacturer == "" {
		result.Manufacturer = profile.Vendor
	}
	if result.Model == "" {
		result.Model = profile.Model
	}
	result.Attributes["profile"] = profile.Name
	for key, value := range profile.Attributes {
		if _, exists := result.Attributes[key]; !exists {
			result.Attributes[key] = value
		}
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "snmp" }

// Probe identifies SNMP-capable devices by probing UDP port 161.
func (p *Plugin) Probe(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	concurrency := p.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	community := creds["community"]
	if community == "" {
		community = "public"
	}
	v3, useV3, err := v3FromCreds(creds)
	if err != nil {
		return []plugins.Result{}, err
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

			if useV3 {
				if result, ok := p.probeV3(ctx, t, v3); ok {
					mu.Lock()
					results = append(results, *result)
					mu.Unlock()
				}
				return
			}

			// Probe SNMP port reachability via UDP
			addr := net.JoinHostPort(t, fmt.Sprintf("%d", p.port()))
			conn, err := net.DialTimeout("udp", addr, timeout)
			if err != nil {
				return
			}
			defer conn.Close()

			// Send an SNMP v2c GET request for sysObjectID
			pkt := buildSNMPv2cGet(community, oidSysObjectID)
			if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
				return
			}
			if _, err := conn.Write(pkt); err != nil {
				return
			}

			buf := make([]byte, 4096)
			n, err := conn.Read(buf)
			if err != nil || n == 0 {
				return
			}

			// If we got a response, the device speaks SNMP
			result := plugins.Result{
				CIType: "network.device",
				Name:   t,
				IP:     t,
				Attributes: map[string]any{
					"protocol":       "snmp",
					"snmpVersion":    "v2c",
					"snmpReachable":  true,
					"responseLength": n,
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

// Collect implements plugins.Plugin: the DeviceRecord of target (DIS-03)
// with the neighbours of its LLDP and bridge tables (DIS-05).
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	observedAt := time.Now()
	res, neighbors, err := p.collect(ctx, target, creds)
	rec, err := plugins.CollectRecord(p.Name(), res, err, observedAt)
	if err != nil {
		return nil, err
	}
	rec.Neighbors = neighbors
	return rec, nil
}

// collect retrieves inventory, telemetry and neighbours from a single SNMP
// endpoint.
func (p *Plugin) collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, []plugins.DeviceNeighbor, error) {
	community := creds["community"]
	if community == "" {
		community = "public"
	}
	if v3, useV3, err := v3FromCreds(creds); err != nil {
		return nil, nil, err
	} else if useV3 {
		return p.collectV3(ctx, target, v3)
	}

	// The values come from the decoded varbinds of the response, never
	// from the message header (which echoes the community).
	g := p.v2cSession(ctx, target, community)
	if err := g.Connect(); err != nil {
		return nil, nil, fmt.Errorf("snmp: dial %s: %w", target, err)
	}
	defer g.Conn.Close()
	if result, err := p.collectSession(g, target, "v2c"); err == nil {
		return result, walkNeighbors(g, target, result.Interfaces), nil
	}
	// A device that leaves the GET unanswered keeps its address as name.
	return &plugins.Result{
		CIType:     "network.device",
		Name:       target,
		IP:         target,
		Attributes: map[string]any{"protocol": "snmp", "snmpVersion": "v2c"},
	}, nil, nil
}

// buildSNMPv2cGet constructs a minimal SNMPv2c GET-Request packet.
func buildSNMPv2cGet(community, oid string) []byte {
	oidBytes := encodeOID(oid)

	// VarBind: SEQUENCE { OID, NULL }
	varbind := asn1Sequence(append(oidBytes, 0x05, 0x00))
	// VarBindList: SEQUENCE { VarBind }
	varbindList := asn1Sequence(varbind)

	// Request ID (integer 1)
	requestID := []byte{0x02, 0x01, 0x01}
	// Error status (integer 0)
	errorStatus := []byte{0x02, 0x01, 0x00}
	// Error index (integer 0)
	errorIndex := []byte{0x02, 0x01, 0x00}

	// PDU: GetRequest-PDU (0xA0)
	pduContent := append(requestID, errorStatus...)
	pduContent = append(pduContent, errorIndex...)
	pduContent = append(pduContent, varbindList...)
	pdu := append([]byte{0xA0, byte(len(pduContent))}, pduContent...)

	// SNMP version (integer 1 = SNMPv2c)
	version := []byte{0x02, 0x01, 0x01}
	// Community string
	communityBytes := append([]byte{0x04, byte(len(community))}, []byte(community)...)

	// SNMP Message: SEQUENCE { version, community, PDU }
	messageContent := append(version, communityBytes...)
	messageContent = append(messageContent, pdu...)

	return asn1Sequence(messageContent)
}

func asn1Sequence(content []byte) []byte {
	return append([]byte{0x30, byte(len(content))}, content...)
}

func encodeOID(oid string) []byte {
	var parts []int
	current := 0
	for _, c := range oid {
		if c == '.' {
			parts = append(parts, current)
			current = 0
		} else {
			current = current*10 + int(c-'0')
		}
	}
	parts = append(parts, current)

	if len(parts) < 2 {
		return []byte{0x06, 0x01, 0x00}
	}

	encoded := []byte{byte(parts[0]*40 + parts[1])}
	for i := 2; i < len(parts); i++ {
		encoded = append(encoded, encodeOIDComponent(parts[i])...)
	}

	return append([]byte{0x06, byte(len(encoded))}, encoded...)
}

func encodeOIDComponent(value int) []byte {
	if value < 128 {
		return []byte{byte(value)}
	}
	var result []byte
	result = append(result, byte(value&0x7f))
	value >>= 7
	for value > 0 {
		result = append([]byte{byte(value&0x7f) | 0x80}, result...)
		value >>= 7
	}
	return result
}

func isPrintable(s string) bool {
	for _, c := range s {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
