package probe

import (
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
)

// The logical PON model of the pon type (PRT-11, WP-1007): OLT → PON port
// → ONU. A vendor profile maps it in its monitor section pon (WP-1000):
//
//   - table onus (required): one row per ONU; the first index part is the
//     PON port (its ifIndex), the rest the ONU on the port. Columns sn
//     (serial number), status (enum on online/offline or up/down; a value
//     mapped to neither skips the row),
//     rx_onu and rx_olt (receive level at the ONU and at the OLT, scaled
//     with the table's scale to dBm), last_down_cause (enum on los,
//     dying_gasp or other) and optionally distance (metres).
//   - table ports (optional): column name per port ifIndex (e.g. ifName
//     1.3.6.1.2.1.31.1.1.1.1); without it the port is named by its index.
const (
	ponTableONUs   = "onus"
	ponTablePorts  = "ports"
	ponColSN       = "sn"
	ponColStatus   = "status"
	ponColRxONU    = "rx_onu"
	ponColRxOLT    = "rx_olt"
	ponColCause    = "last_down_cause"
	ponColDistance = "distance"
	ponColName     = "name"

	ponOnline  = "online"
	ponOffline = "offline"

	PONCauseLOS       = "los"
	PONCauseDyingGasp = "dying_gasp"
	PONCauseOther     = "other"
)

// ponONU is one ONU of the model.
type ponONU struct {
	port      string // port label (name or index)
	portIndex uint32
	id        string // ONU index below the port
	sn        string
	online    bool
	rxONU     float64
	hasRxONU  bool
	rxOLT     float64
	hasRxOLT  bool
	cause     string
	distanceM float64
	hasDist   bool
}

// ponRead walks the tables of the monitor section: the ONUs (at most
// maxRows) and the port names. truncated reports a walk cut at maxRows;
// lastPort is then the port of the last row read, whose ONUs may be
// incomplete.
func ponRead(s *snmp.Session, m *profiles.MonitorSpec, maxRows int) (onus []ponONU, truncated bool, lastPort uint32, err error) {
	t, ok := m.Tables[ponTableONUs]
	if !ok {
		return nil, false, 0, nil
	}
	names := map[uint32]string{}
	if pt, ok := m.Tables[ponTablePorts]; ok {
		if oid, ok := pt.ColumnOID(ponColName); ok && oid != "" {
			rows, werr := s.WalkColumns([]string{oid}, maxRows)
			if werr != nil {
				return nil, false, 0, werr
			}
			for _, row := range rows.Rows {
				if v, ok := row.Value(oid); ok && len(row.Index) == 1 {
					if name := strings.TrimSpace(pduString(v)); name != "" {
						names[row.Index[0]] = name
					}
				}
			}
		}
	}
	cols, walk := monitorColumns(&t, ponColStatus, ponColSN, ponColRxONU, ponColRxOLT, ponColCause, ponColDistance)
	if cols[ponColStatus] == "" {
		return nil, false, 0, nil
	}
	rows, err := s.WalkColumns(walk, maxRows)
	if err != nil {
		return nil, false, 0, err
	}
	factor := t.Factor()
	for _, row := range rows.Rows {
		if len(row.Index) < 2 {
			continue
		}
		v, ok := row.Value(cols[ponColStatus])
		if !ok {
			continue
		}
		raw, ok := pduInt(v)
		if !ok {
			continue
		}
		o := ponONU{portIndex: row.Index[0], id: indexString(row.Index[1:])}
		o.port = names[o.portIndex]
		if o.port == "" {
			o.port = strconv.FormatUint(uint64(o.portIndex), 10)
		}
		// Only a real state counts: a value the profile does not map to
		// online/up or offline/down (initializing, unknown, …) is neither
		// online nor offline, so the row enters no count and no state
		// change.
		state, mapped := t.MapEnum(ponColStatus, raw)
		switch {
		case mapped && (state == ponOnline || state == "up"):
			o.online = true
		case mapped && (state == ponOffline || state == "down"):
		default:
			continue
		}
		if sv, ok := row.Value(cols[ponColSN]); ok && cols[ponColSN] != "" {
			o.sn = ponSerial(sv)
		}
		if o.sn == "" {
			o.sn = o.port + "/" + o.id
		}
		o.rxONU, o.hasRxONU = ponLevel(row, cols[ponColRxONU], factor)
		o.rxOLT, o.hasRxOLT = ponLevel(row, cols[ponColRxOLT], factor)
		o.cause = PONCauseOther
		if cv, ok := row.Value(cols[ponColCause]); ok && cols[ponColCause] != "" {
			if c, ok := pduInt(cv); ok {
				if name, ok := t.MapEnum(ponColCause, c); ok && (name == PONCauseLOS || name == PONCauseDyingGasp) {
					o.cause = name
				}
			}
		}
		if dv, ok := row.Value(cols[ponColDistance]); ok && cols[ponColDistance] != "" {
			if d, ok := pduInt(dv); ok && d >= 0 {
				o.distanceM, o.hasDist = float64(d), true
			}
		}
		onus = append(onus, o)
	}
	if rows.Truncated && len(rows.Rows) > 0 {
		lastPort = rows.Rows[len(rows.Rows)-1].Index[0]
	}
	return onus, rows.Truncated, lastPort, nil
}

// ponLevel reads a receive level column, scaled to dBm. Vendors report an
// absent level (ONU offline) as a sentinel far outside the optical range;
// a level outside -60 to +10 dBm is no level.
func ponLevel(row snmp.Row, oid string, factor float64) (float64, bool) {
	if oid == "" {
		return 0, false
	}
	v, ok := row.Value(oid)
	if !ok {
		return 0, false
	}
	n, ok := pduInt(v)
	if !ok {
		return 0, false
	}
	dbm := float64(n) * factor
	if dbm < -60 || dbm > 10 {
		return 0, false
	}
	return dbm, true
}

// ponSerial formats an ONU serial number: printable text as it is, eight
// octets (4 vendor letters and 4 octets, ITU-T G.984.3) as vendor letters
// plus hex.
func ponSerial(v gosnmp.SnmpPDU) string { //nolint:gocritic // a PDU as gosnmp hands it out
	raw := pduBytes(v)
	printable := len(raw) > 0
	for _, c := range raw {
		if c < 0x20 || c > 0x7e {
			printable = false
			break
		}
	}
	if printable {
		return strings.TrimSpace(string(raw))
	}
	if len(raw) == 8 {
		vendor := string(raw[:4])
		ok := true
		for _, c := range raw[:4] {
			if c < 'A' || c > 'Z' {
				ok = false
			}
		}
		if ok {
			return vendor + strings.ToUpper(hexString(raw[4:]))
		}
	}
	return strings.ToUpper(hexString(raw))
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}
