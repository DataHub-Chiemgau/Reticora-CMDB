// Package power provides a UPS/PDU monitoring plugin using SNMP-based detection.
package power

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	snmpPort           = 161
	defaultTimeout     = 5 * time.Second
	defaultConcurrency = 16
)

// Standard UPS-MIB OIDs
const (
	oidUPSIdentManufacturer = "1.3.6.1.2.1.33.1.1.1.0"
	oidUPSIdentModel        = "1.3.6.1.2.1.33.1.1.2.0"
	oidUPSIdentFirmware     = "1.3.6.1.2.1.33.1.1.3.0"
	oidUPSIdentSerial       = "1.3.6.1.2.1.33.1.1.5.0"
	oidUPSBatteryStatus     = "1.3.6.1.2.1.33.1.2.1.0"
	oidUPSBatteryRunTime    = "1.3.6.1.2.1.33.1.2.3.0"
	oidUPSBatteryCharge     = "1.3.6.1.2.1.33.1.2.4.0"
	oidUPSOutputSource      = "1.3.6.1.2.1.33.1.4.1.0"
	oidUPSOutputVoltage     = "1.3.6.1.2.1.33.1.4.4.1.2"
	oidUPSOutputCurrent     = "1.3.6.1.2.1.33.1.4.4.1.3"
	oidUPSOutputPower       = "1.3.6.1.2.1.33.1.4.4.1.4"
	oidUPSOutputLoad        = "1.3.6.1.2.1.33.1.4.4.1.5"

	// APC specific OIDs
	oidAPCModel    = "1.3.6.1.4.1.318.1.1.1.1.1.1.0"
	oidAPCSerial   = "1.3.6.1.4.1.318.1.1.1.1.2.3.0"
	oidAPCFirmware = "1.3.6.1.4.1.318.1.1.1.1.2.1.0"
	oidAPCLoad     = "1.3.6.1.4.1.318.1.1.1.4.2.3.0"
	oidAPCBatCap   = "1.3.6.1.4.1.318.1.1.1.2.2.1.0"
	oidAPCBatTemp  = "1.3.6.1.4.1.318.1.1.1.2.2.2.0"

	// Eaton/MGE OIDs
	oidEatonModel  = "1.3.6.1.4.1.534.1.1.2.0"
	oidEatonSerial = "1.3.6.1.4.1.534.1.1.3.0"
)

// Plugin collects power infrastructure data from UPS and PDU systems.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates a power plugin instance.
func New() *Plugin {
	return &Plugin{
		Timeout:     defaultTimeout,
		Concurrency: defaultConcurrency,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "power" }

// Discover identifies power devices by probing SNMP for UPS-MIB presence.
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

			// Probe the UPS-MIB sysObjectID via SNMP
			if isPowerDevice(t, community, timeout) {
				result := plugins.Result{
					CIType: "power-device",
					Name:   t,
					IP:     t,
					Attributes: map[string]any{
						"category": "power",
						"protocol": "snmp",
						"upsCapable": true,
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

// Collect gathers UPS identity and runtime metrics from a power device.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	community := creds["community"]
	if community == "" {
		community = "public"
	}

	addr := net.JoinHostPort(target, fmt.Sprintf("%d", snmpPort))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("power: dial %s: %w", target, err)
	}
	defer conn.Close()

	// Query UPS-MIB identity OIDs
	manufacturer := snmpGetString(conn, community, oidUPSIdentManufacturer, timeout)
	model := snmpGetString(conn, community, oidUPSIdentModel, timeout)
	serial := snmpGetString(conn, community, oidUPSIdentSerial, timeout)
	firmware := snmpGetString(conn, community, oidUPSIdentFirmware, timeout)

	// If standard UPS-MIB is empty, try APC-specific OIDs
	if model == "" {
		model = snmpGetString(conn, community, oidAPCModel, timeout)
	}
	if serial == "" {
		serial = snmpGetString(conn, community, oidAPCSerial, timeout)
	}
	if firmware == "" {
		firmware = snmpGetString(conn, community, oidAPCFirmware, timeout)
	}
	// Try Eaton if still empty
	if model == "" {
		model = snmpGetString(conn, community, oidEatonModel, timeout)
	}
	if serial == "" {
		serial = snmpGetString(conn, community, oidEatonSerial, timeout)
	}

	name := model
	if name == "" {
		name = target
	}

	result := &plugins.Result{
		CIType:       "power-device",
		Name:         name,
		IP:           target,
		Manufacturer: manufacturer,
		Model:        model,
		Serial:       serial,
		Firmware:     firmware,
		Metrics: []plugins.MetricSample{
			{Name: "ups_battery_status", Labels: map[string]string{"target": target}},
			{Name: "ups_output_load_percent", Labels: map[string]string{"target": target}},
			{Name: "ups_battery_charge_percent", Labels: map[string]string{"target": target}},
			{Name: "ups_battery_runtime_seconds", Labels: map[string]string{"target": target}},
		},
		Attributes: map[string]any{
			"category":    "power",
			"protocol":    "snmp",
			"deviceType":  classifyPowerDevice(manufacturer, model),
			"upsCapable":  true,
		},
	}

	return result, nil
}

// isPowerDevice checks if the target responds to UPS-MIB OIDs.
func isPowerDevice(target, community string, timeout time.Duration) bool {
	addr := net.JoinHostPort(target, fmt.Sprintf("%d", snmpPort))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Try UPS-MIB identity OID
	val := snmpGetString(conn, community, oidUPSIdentModel, timeout)
	if val != "" {
		return true
	}
	// Try APC OID
	val = snmpGetString(conn, community, oidAPCModel, timeout)
	return val != ""
}

// snmpGetString sends an SNMP v2c GET and returns the response value as string.
func snmpGetString(conn net.Conn, community, oid string, timeout time.Duration) string {
	pkt := buildSNMPGet(community, oid)
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return ""
	}
	if _, err := conn.Write(pkt); err != nil {
		return ""
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil || n < 10 {
		return ""
	}

	return extractStringValue(buf[:n])
}

// buildSNMPGet constructs a minimal SNMPv2c GET-Request packet.
func buildSNMPGet(community, oid string) []byte {
	oidBytes := encodeOID(oid)
	varbind := asn1Seq(append(oidBytes, 0x05, 0x00))
	varbindList := asn1Seq(varbind)

	requestID := []byte{0x02, 0x01, 0x01}
	errStatus := []byte{0x02, 0x01, 0x00}
	errIndex := []byte{0x02, 0x01, 0x00}

	pduContent := append(requestID, errStatus...)
	pduContent = append(pduContent, errIndex...)
	pduContent = append(pduContent, varbindList...)
	pdu := append([]byte{0xA0, byte(len(pduContent))}, pduContent...)

	version := []byte{0x02, 0x01, 0x01}
	comm := append([]byte{0x04, byte(len(community))}, []byte(community)...)

	msgContent := append(version, comm...)
	msgContent = append(msgContent, pdu...)

	return asn1Seq(msgContent)
}

func asn1Seq(content []byte) []byte {
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
		encoded = append(encoded, encodeComponent(parts[i])...)
	}
	return append([]byte{0x06, byte(len(encoded))}, encoded...)
}

func encodeComponent(value int) []byte {
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

func extractStringValue(data []byte) string {
	for i := 0; i < len(data)-2; i++ {
		if data[i] == 0x04 && i+1 < len(data) {
			length := int(data[i+1])
			if length > 0 && i+2+length <= len(data) {
				val := string(data[i+2 : i+2+length])
				if isPrintable(val) {
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

func classifyPowerDevice(manufacturer, model string) string {
	lower := strings.ToLower(manufacturer + " " + model)
	switch {
	case strings.Contains(lower, "pdu"):
		return "pdu"
	case strings.Contains(lower, "ats") || strings.Contains(lower, "transfer switch"):
		return "ats"
	default:
		return "ups"
	}
}
