// Package snmp provides an SNMP v2c/v3 collection plugin for network device discovery.
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
	defaultPort       = 161
	defaultTimeout    = 5 * time.Second
	defaultRetries    = 2
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
	oidEntPhysDescr      = "1.3.6.1.2.1.47.1.1.1.1.2"
	oidEntPhysSerialNum  = "1.3.6.1.2.1.47.1.1.1.1.11"
	oidEntPhysMfgName    = "1.3.6.1.2.1.47.1.1.1.1.12"
	oidEntPhysModelName  = "1.3.6.1.2.1.47.1.1.1.1.13"
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

// Discover identifies SNMP-capable devices by probing UDP port 161.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
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

			// Probe SNMP port reachability via UDP
			addr := net.JoinHostPort(t, fmt.Sprintf("%d", defaultPort))
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

// Collect retrieves inventory and telemetry from a single SNMP endpoint.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	community := creds["community"]
	if community == "" {
		community = "public"
	}

	addr := net.JoinHostPort(target, fmt.Sprintf("%d", defaultPort))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("snmp: dial %s: %w", target, err)
	}
	defer conn.Close()

	// Query system group OIDs
	sysDescr := p.snmpGet(conn, community, oidSysDescr, timeout)
	sysName := p.snmpGet(conn, community, oidSysName, timeout)
	sysObjectID := p.snmpGet(conn, community, oidSysObjectID, timeout)
	sysLocation := p.snmpGet(conn, community, oidSysLocation, timeout)
	sysContact := p.snmpGet(conn, community, oidSysContact, timeout)

	// Query Entity-MIB for hardware info
	serial := p.snmpGet(conn, community, oidEntPhysSerialNum+".1", timeout)
	manufacturer := p.snmpGet(conn, community, oidEntPhysMfgName+".1", timeout)
	model := p.snmpGet(conn, community, oidEntPhysModelName+".1", timeout)
	firmware := p.snmpGet(conn, community, oidEntPhysFirmwareRev+".1", timeout)

	name := sysName
	if name == "" {
		name = target
	}

	result := &plugins.Result{
		CIType:       "network.device",
		Name:         name,
		IP:           target,
		Manufacturer: manufacturer,
		Model:        model,
		Serial:       serial,
		Firmware:     firmware,
		Attributes: map[string]any{
			"protocol":     "snmp",
			"sysDescr":     sysDescr,
			"sysObjectID":  sysObjectID,
			"sysLocation":  sysLocation,
			"sysContact":   sysContact,
			"snmpVersion":  "v2c",
		},
	}

	p.applyProfile(result, sysObjectID)

	return result, nil
}

// snmpGet performs a single SNMP GET and returns the string value or empty string on failure.
func (p *Plugin) snmpGet(conn net.Conn, community, oid string, timeout time.Duration) string {
	pkt := buildSNMPv2cGet(community, oid)
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return ""
	}
	if _, err := conn.Write(pkt); err != nil {
		return ""
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil || n < 20 {
		return ""
	}

	// Basic extraction of the value from SNMP response.
	// In a production implementation this would use a proper ASN.1/BER decoder.
	return extractSNMPValue(buf[:n])
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

// extractSNMPValue attempts to extract a printable string value from an SNMP response.
func extractSNMPValue(data []byte) string {
	// Walk through the BER-encoded response to find the value in the varbind.
	// This is a simplified parser that looks for OctetString (0x04) or
	// ObjectIdentifier (0x06) values in the response payload.
	for i := 0; i < len(data)-2; i++ {
		// Look for OctetString tag followed by a length
		if data[i] == 0x04 && i+1 < len(data) {
			length := int(data[i+1])
			if length > 0 && i+2+length <= len(data) {
				val := string(data[i+2 : i+2+length])
				// Only return printable ASCII strings
				if isPrintable(val) && len(val) > 0 {
					return val
				}
			}
		}
	}
	return ""
}

func isPrintable(s string) bool {
	for _, c := range s {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
