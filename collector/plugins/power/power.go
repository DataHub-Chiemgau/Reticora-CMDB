// Package power provides a UPS/PDU monitoring plugin using SNMP-based detection.
package power

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

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

// probeOIDs are the UPS identity a power device answers (Probe).
var probeOIDs = []string{oidUPSIdentModel, oidAPCModel}

// identityOIDs are read in one GET (collect): the UPS-MIB identity, the
// APC and Eaton OIDs as fallback.
var identityOIDs = []string{
	oidUPSIdentManufacturer, oidUPSIdentModel, oidUPSIdentSerial, oidUPSIdentFirmware,
	oidAPCModel, oidAPCSerial, oidAPCFirmware, oidEatonModel, oidEatonSerial,
}

// Plugin collects power infrastructure data from UPS and PDU systems.
type Plugin struct {
	Timeout     time.Duration
	Concurrency int
	// Port is the SNMP port (default 161).
	Port int
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

// Probe identifies power devices by probing SNMP for UPS-MIB presence.
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

			// Probe the UPS identity via SNMP
			if p.isPowerDevice(ctx, t, community, timeout) {
				result := plugins.Result{
					CIType: "power-device",
					Name:   t,
					IP:     t,
					Attributes: map[string]any{
						"category":   "power",
						"protocol":   "snmp",
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

// Collect implements plugins.Plugin: the DeviceRecord of target (DIS-03).
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	observedAt := time.Now()
	res, err := p.collect(ctx, target, creds)
	return plugins.CollectRecord(p.Name(), res, err, observedAt)
}

// collect gathers UPS identity and runtime metrics from a power device.
func (p *Plugin) collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	community := creds["community"]
	if community == "" {
		community = "public"
	}

	g := p.v2cSession(ctx, target, community, timeout)
	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("power: dial %s: %w", target, err)
	}
	defer g.Conn.Close()

	// UPS-MIB identity; if it is empty, the APC and then the Eaton OIDs.
	values := getStrings(g, identityOIDs)
	manufacturer := values[oidUPSIdentManufacturer]
	model := cmp.Or(values[oidUPSIdentModel], values[oidAPCModel], values[oidEatonModel])
	serial := cmp.Or(values[oidUPSIdentSerial], values[oidAPCSerial], values[oidEatonSerial])
	firmware := cmp.Or(values[oidUPSIdentFirmware], values[oidAPCFirmware])

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
			"category":   "power",
			"protocol":   "snmp",
			"deviceType": classifyPowerDevice(manufacturer, model),
			"upsCapable": true,
		},
	}
	// The outlet mapping (IMP-09).
	if outlets, pdu := walkOutlets(g); len(outlets) > 0 {
		result.Attributes["outlets"] = outlets
		if pdu {
			result.Attributes["deviceType"] = "pdu"
		}
	}

	return result, nil
}

// isPowerDevice reports whether target answers the UPS identity model of
// the UPS-MIB or the APC PowerNet-MIB with a value.
func (p *Plugin) isPowerDevice(ctx context.Context, target, community string, timeout time.Duration) bool {
	g := p.v2cSession(ctx, target, community, timeout)
	if err := g.Connect(); err != nil {
		return false
	}
	defer g.Conn.Close()
	values := getStrings(g, probeOIDs)
	return values[oidUPSIdentModel] != "" || values[oidAPCModel] != ""
}

// v2cSession builds an SNMPv2c session to target (identity GET and outlet
// walk).
func (p *Plugin) v2cSession(ctx context.Context, target, community string, timeout time.Duration) *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{Context: ctx, Target: target, Port: uint16(p.port()), Transport: "udp", Community: community,
		Version: gosnmp.Version2c, Timeout: timeout, Retries: 0, MaxOids: gosnmp.MaxOids}
}

// getStrings reads oids in one GET and returns the OctetString values by
// OID. The values come from the decoded varbinds, never from the message
// header (which echoes the community); what the device does not answer
// (noSuchObject, no response) stays empty.
func getStrings(g *gosnmp.GoSNMP, oids []string) map[string]string {
	values := map[string]string{}
	pkt, err := g.Get(oids)
	if err != nil {
		return values
	}
	for _, v := range pkt.Variables {
		if b, ok := v.Value.([]byte); ok && v.Type == gosnmp.OctetString {
			values[strings.TrimPrefix(v.Name, ".")] = plugins.OctetText(b)
		}
	}
	return values
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
