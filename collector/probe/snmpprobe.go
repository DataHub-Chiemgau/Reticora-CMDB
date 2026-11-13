package probe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Shared parts of the SNMP probe types (bfd, optical): the session of a
// probe, the system group for the profile selection (WP-1000) and the
// mapping of SNMP failures to result codes.

// SNMP system group OIDs read for the profile selection.
const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID = "1.3.6.1.2.1.1.2.0"
)

// monitorProfiles is the profile registry the SNMP probe types select their
// monitor section from (tests replace it).
var monitorProfiles = profiles.Default

// snmpHostPort splits the target of an SNMP probe into host and port: an
// address or host name as it is (port 0: the default 161), host:port and
// [v6]:port with an explicit port.
func snmpHostPort(target string) (host string, port int) {
	target = strings.TrimSpace(target)
	if h, ps, err := net.SplitHostPort(target); err == nil {
		if p, perr := strconv.Atoi(ps); perr == nil && p > 0 && p <= 65535 {
			return h, p
		}
	}
	return strings.Trim(target, "[]"), 0
}

// openSNMP opens the SNMP session of a probe with the credentials of the
// namespace snmp: a request waits at most the probe's timeout (within the
// session limits 1-15 s, and within the deadline of ctx), without retries;
// the check as a whole is bounded by ctx.
func openSNMP(ctx context.Context, spec *probewire.ProbeSpec, creds map[string]string) (*snmp.Session, error) {
	host, port := snmpHostPort(spec.Target)
	timeout := time.Duration(spec.TimeoutMS) * time.Millisecond
	if d, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(d))
	}
	timeout = min(max(timeout, snmp.MinTimeout), snmp.MaxTimeout)
	retries := 0
	return snmp.Open(ctx, host, creds, snmp.Options{Port: port, Timeout: timeout, Retries: &retries})
}

// snmpFailure maps an SNMP failure to a result: a refused SNMPv3
// credential (usmStats report) is error/auth_failed, an incomplete or
// invalid credential error/config, anything else (no answer within the
// timeout, unreachable target) fail/timeout.
func snmpFailure(err error) probewire.Result {
	switch {
	case errors.Is(err, plugins.ErrAuthFailed):
		return failResult(probewire.StatusError, probewire.ReasonAuthFailed)
	case errors.Is(err, snmp.ErrV3Incomplete):
		return configResult("credential", "the SNMPv3 credential is incomplete")
	}
	var opts *snmp.OptionsError
	if errors.As(err, &opts) {
		return configResult("params."+opts.Field, "outside the allowed range")
	}
	return failResult(probewire.StatusFail, probewire.ReasonTimeout)
}

// snmpMonitor reads sysObjectID and sysDescr and returns the monitor
// section of probeType of the matching profile (false: the standard-MIB
// core applies).
func snmpMonitor(s *snmp.Session, probeType string) (profiles.MonitorSpec, bool, error) {
	pdus, err := s.Get([]string{oidSysObjectID, oidSysDescr})
	if err != nil {
		return profiles.MonitorSpec{}, false, err
	}
	var oid, descr string
	for _, p := range pdus {
		switch strings.TrimPrefix(p.Name, ".") {
		case oidSysObjectID:
			if v, ok := p.Value.(string); ok {
				oid = strings.TrimPrefix(v, ".")
			}
		case oidSysDescr:
			descr = pduString(p)
		}
	}
	spec, _, ok := monitorProfiles().MonitorFor(probeType, oid, descr)
	return spec, ok, nil
}

// pduString returns the value of a string variable.
func pduString(p gosnmp.SnmpPDU) string {
	switch v := p.Value.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	}
	return ""
}

// pduInt returns the value of an integer variable.
func pduInt(p gosnmp.SnmpPDU) (int64, bool) {
	switch p.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		return gosnmp.ToBigInt(p.Value).Int64(), true
	}
	return 0, false
}

// inetAddress decodes an InetAddressType/InetAddress pair of columns
// (RFC 4001): ipv4(1) with 4, ipv6(2) with 16, ipv4z(3) with 8 and
// ipv6z(4) with 20 octets (the zone index is dropped); a type the column
// lacks is derived from the length.
func inetAddress(typ int64, raw []byte) (netip.Addr, bool) {
	switch {
	case len(raw) == 4 && (typ == 0 || typ == 1):
		return netip.AddrFrom4([4]byte(raw)), true
	case len(raw) == 16 && (typ == 0 || typ == 2):
		return netip.AddrFrom16([16]byte(raw)).Unmap(), true
	case len(raw) == 8 && typ == 3:
		// ipv4z: address and 4-octet zone index (RFC 4001); the zone is
		// not part of the comparison.
		return netip.AddrFrom4([4]byte(raw[:4])), true
	case len(raw) == 20 && typ == 4:
		// ipv6z: link-local neighbors of single-hop IPv6 BFD.
		return netip.AddrFrom16([16]byte(raw[:16])).Unmap(), true
	}
	return netip.Addr{}, false
}

// pduBytes returns the octets of an OctetString or IpAddress variable.
func pduBytes(p gosnmp.SnmpPDU) []byte {
	switch v := p.Value.(type) {
	case []byte:
		return v
	case string:
		if p.Type == gosnmp.IPAddress {
			if ip, err := netip.ParseAddr(v); err == nil {
				return ip.AsSlice()
			}
		}
		return []byte(v)
	}
	return nil
}
