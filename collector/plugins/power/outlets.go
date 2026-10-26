package power

import (
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Outlet mapping (IMP-09): the names of PDU outlets and UPS outlet groups
// name what they feed ("web01 PSU1", "pdu-a"). The server turns them into
// powered_by edges device → PDU → UPS, marking redundant feeds.

// outletTable is a vendor table of outlet names and (optionally) states.
type outletTable struct {
	vendor string
	// pdu marks the tables of PDUs (the others are UPS outlet groups).
	pdu      bool
	nameOID  string
	stateOID string
	// on is the state value of a switched-on outlet.
	on int
}

var outletTables = []outletTable{
	// APC Rack PDU 2 (rPDU2OutletSwitchedStatusName/State: off 1, on 2).
	{vendor: "apc-rpdu2", pdu: true, nameOID: "1.3.6.1.4.1.318.1.1.26.9.2.3.1.3", stateOID: "1.3.6.1.4.1.318.1.1.26.9.2.3.1.5", on: 2},
	// APC MasterSwitch / Rack PDU, PowerNet-MIB sPDU (sPDUOutletName of
	// sPDUOutletConfigTable, sPDUOutletCtl of sPDUOutletControlTable:
	// on 1, off 2).
	{vendor: "apc-spdu", pdu: true, nameOID: "1.3.6.1.4.1.318.1.1.4.5.2.1.3", stateOID: "1.3.6.1.4.1.318.1.1.4.4.2.1.3", on: 1},
	// Raritan PX2/PX3, PDU2-MIB (outletName/outletSwitchingState of PDU 1:
	// on 7, off 8).
	{vendor: "raritan", pdu: true, nameOID: "1.3.6.1.4.1.13742.6.3.5.3.1.3.1", stateOID: "1.3.6.1.4.1.13742.6.4.1.2.1.3.1", on: 7},
	// APC UPS outlet groups (upsOutletGroupStatusName, column 2, and
	// upsOutletGroupStatusGroupState, column 3: on 1, off 2). Column 3 is
	// the state, not the name (WP-498).
	{vendor: "apc-ups", nameOID: "1.3.6.1.4.1.318.1.1.1.12.1.2.1.2", stateOID: "1.3.6.1.4.1.318.1.1.1.12.1.2.1.3", on: 1},
}

// walkOutlets reads the outlet names of the first vendor table the device
// answers; outlets without a name are left out. pdu reports a PDU table.
// A device that answers none of the tables has no outlets; that is no
// error.
func walkOutlets(g *gosnmp.GoSNMP) (outlets []plugins.DeviceOutlet, pdu bool) {
	for _, table := range outletTables {
		names := walkColumn(g, table.nameOID)
		if len(names) == 0 {
			continue
		}
		states := walkColumn(g, table.stateOID)
		idx := make([]int, 0, len(names))
		for i := range names {
			idx = append(idx, i)
		}
		sortInts(idx)
		out := make([]plugins.DeviceOutlet, 0, len(idx))
		for _, i := range idx {
			name := strings.TrimSpace(names[i])
			if name == "" || isDefaultOutletName(name) {
				continue
			}
			o := plugins.DeviceOutlet{Name: name}
			if st, ok := states[i]; ok {
				if n, err := strconv.Atoi(st); err == nil {
					o.State = map[bool]string{true: "on", false: "off"}[n == table.on]
				}
			}
			out = append(out, o)
		}
		return out, table.pdu
	}
	return nil, false
}

// walkColumn returns the values of a table column by row index.
func walkColumn(g *gosnmp.GoSNMP, column string) map[int]string {
	out := map[int]string{}
	_ = g.BulkWalk(column, func(v gosnmp.SnmpPDU) error {
		oid := strings.TrimPrefix(v.Name, ".")
		i := strings.LastIndexByte(oid, '.')
		if i < 0 {
			return nil
		}
		n, err := strconv.Atoi(oid[i+1:])
		if err != nil {
			return nil
		}
		switch val := v.Value.(type) {
		case []byte:
			out[n] = string(val)
		default:
			out[n] = gosnmp.ToBigInt(v.Value).String()
		}
		return nil
	})
	return out
}

// isDefaultOutletName reports factory names ("Outlet 1", "Outlet_12",
// the UPS outlet group "Outlet Group 3") that name nothing.
func isDefaultOutletName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimLeft(strings.TrimPrefix(n, "outlet"), " _-#")
	n = strings.TrimLeft(strings.TrimPrefix(n, "group"), " _-#")
	if _, err := strconv.Atoi(n); err == nil {
		return true
	}
	return n == ""
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func (p *Plugin) port() int {
	if p.Port > 0 {
		return p.Port
	}
	return snmpPort
}
