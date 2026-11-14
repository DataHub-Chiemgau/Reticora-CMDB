package snmpcustom

import (
	"encoding/json"
	"sort"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Templates of snmp_custom (MON-03, WP-997 AK4): preset parameter sets in
// the contract (templates at the entry snmp_custom of api/probe-types.json)
// that the UI offers when a probe is created. They replace the fixed
// power polls of the collector (upsPollMetrics and the PDU metering of
// pollPowerValues) with the same values; WP-999 switches the collector
// over.
const (
	// TemplateUPS reads the UPS-MIB (RFC 1628): upsBatteryStatus,
	// upsEstimatedMinutesRemaining (scale 60, unit s),
	// upsEstimatedChargeRemaining and upsOutputPercentLoad of output
	// line 1.
	TemplateUPS = "ups_rfc1628"
	// TemplatePDUApcRpdu2 reads rPDU2PhaseStatusTable of the APC
	// PowerNet-MIB: current in tenths of amps (scale 0.1, unit A) and
	// power in hundredths of kW (scale 10, unit W) per phase.
	TemplatePDUApcRpdu2 = "pdu_apc_rpdu2"
)

// Template returns the parameter set of the named template.
func Template(name string) (json.RawMessage, bool) {
	t, ok := probewire.Lookup(Type)
	if !ok {
		return nil, false
	}
	raw, ok := t.Templates[name]
	return raw, ok
}

// TemplateNames returns the names of the templates, sorted.
func TemplateNames() []string {
	t, _ := probewire.Lookup(Type)
	out := make([]string, 0, len(t.Templates))
	for name := range t.Templates {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
