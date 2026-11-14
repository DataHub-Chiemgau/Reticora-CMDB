// Package snmpstatus is the probe type snmp_status of the collector
// (PRT-05, WP-995, E-183): reachability over sysUpTime with reboot
// detection and up to 20 OID checks with an operator and a severity. It
// registers its executor in init(); the collector links the package in
// through collector/probe/all.
//
// The executor holds no state: the previous sysUpTime of a probe lies in
// the previous-value store of the engine context (collector/probe/rate,
// WP-994).
package snmpstatus

import (
	"context"
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Type is the register key of the probe type.
const Type = "snmp_status"

// MaxChecks is the most OID checks of a probe (PRT-05).
const MaxChecks = 20

// Value names of an snmp_status result.
const (
	// ValueReboot is 1 when sysUpTime is below its previous value of the
	// same probe (the agent restarted), else 0; a reboot alone does not
	// change the state.
	ValueReboot = "probe_snmp_reboot"
	// ValueUptime is sysUpTime in seconds.
	ValueUptime = "probe_snmp_uptime_seconds"
	// ValueResponse is the time of the GET in seconds.
	ValueResponse = "probe_snmp_response_seconds"
	// LabelOID names the OID of the check a fail or warn code refers to.
	LabelOID = "oid"
)

// OIDSysUpTime is sysUpTime.0 (SNMPv2-MIB, TimeTicks).
const OIDSysUpTime = "1.3.6.1.2.1.1.3.0"

// Operators and severities of a check.
const (
	opEq    = "eq"
	opNe    = "ne"
	opLt    = "lt"
	opGt    = "gt"
	opIn    = "in"
	opRegex = "regex"

	severityDown     = "down"
	severityDegraded = "degraded"
)

// now is the clock of the executor (tests replace it).
var now = time.Now

func init() { probe.Register(Type, &Executor{}) }

// Executor is the probe type snmp_status (PRT-05). A GET of sysUpTime.0
// and the OIDs of the checks decides the reachability: no answer within
// the timeout (also a wrong SNMPv2c community) is fail/timeout, a refused
// SNMPv3 credential error/auth_failed (unknown, never down, E-177). A
// violated check with severity down is fail/check_failed, with severity
// degraded ok with the warn code check_failed; an OID the agent does not
// have is ok with the warn code no_such_object. The state degraded is
// derived by the state machine (WP-952).
type Executor struct{}

type check struct {
	OID      string `json:"oid"`
	Op       string `json:"op"`
	Value    string `json:"value"`
	Severity string `json:"severity"`

	re *regexp.Regexp
}

type params struct {
	Checks   []check `json:"checks"`
	Port     int     `json:"port"`
	TimeoutS int     `json:"timeout_s"`
	Retries  int     `json:"retries"`
}

// Execute implements probe.Executor.
func (e *Executor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	p, bad := decode(spec.Params)
	if bad != nil {
		return *bad
	}
	sess, err := probe.OpenSNMPWith(ctx, &spec, creds, probe.SNMPSession{
		Port: p.Port, Timeout: time.Duration(p.TimeoutS) * time.Second, Retries: p.Retries,
	})
	if err != nil {
		return probe.SNMPFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush

	oids := []string{OIDSysUpTime}
	for i := range p.Checks {
		oids = append(oids, p.Checks[i].OID)
	}
	start := time.Now()
	pdus, err := sess.Get(oids)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	elapsed := time.Since(start)
	byOID := make(map[string]gosnmp.SnmpPDU, len(pdus))
	for _, v := range pdus {
		byOID[strings.TrimPrefix(v.Name, ".")] = v
	}

	r := probewire.Result{Status: probewire.StatusOK, Values: map[string]float64{
		ValueResponse: elapsed.Seconds(), ValueReboot: 0,
	}}
	var noSuch, degraded, down string
	if up, ok := byOID[OIDSysUpTime]; ok && !missing(up) {
		if ticks, ok := uptimeTicks(up); ok {
			r.Values[ValueUptime] = float64(ticks) / 100
			if rate.FromContext(ctx).Uptime(ticks, now()) {
				r.Values[ValueReboot] = 1
			}
		}
	} else {
		noSuch = OIDSysUpTime
	}
	for i := range p.Checks {
		c := &p.Checks[i]
		v, ok := byOID[c.OID]
		switch {
		case !ok || missing(v):
			if noSuch == "" {
				noSuch = c.OID
			}
		case !c.holds(v):
			if c.Severity == severityDegraded {
				if degraded == "" {
					degraded = c.OID
				}
			} else if down == "" {
				down = c.OID
			}
		}
	}
	switch {
	case down != "":
		r.Status, r.Code, r.Labels = probewire.StatusFail, probewire.WarnCheckFailed, map[string]string{LabelOID: down}
	case degraded != "":
		r.Code, r.Labels = probewire.WarnCheckFailed, map[string]string{LabelOID: degraded}
	case noSuch != "":
		r.Code, r.Labels = probewire.WarnNoSuchObject, map[string]string{LabelOID: noSuch}
	}
	return r
}

// decode reads and checks the parameters; a rejected configuration is
// returned as error/config (the server validates the same rules with
// probewire.Validate).
func decode(raw json.RawMessage) (params, *probewire.Result) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			r := probe.ConfigResult("params", "parameters cannot be decoded")
			return p, &r
		}
	}
	if len(p.Checks) > MaxChecks {
		r := probe.ConfigResult("params.checks", "at most 20 checks")
		return p, &r
	}
	for i := range p.Checks {
		c := &p.Checks[i]
		c.OID = strings.TrimPrefix(strings.TrimSpace(c.OID), ".")
		if c.Severity == "" {
			c.Severity = severityDown
		}
		field := "params.checks[" + strconv.Itoa(i) + "].value"
		switch c.Op {
		case opLt, opGt:
			if _, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64); err != nil {
				r := probe.ConfigResult(field, "must be a number for lt and gt")
				return p, &r
			}
		case opRegex:
			re, err := regexp.Compile(c.Value)
			if err != nil || len([]rune(c.Value)) > probewire.MaxSNMPRegex {
				r := probe.ConfigResult(field, "must be an RE2 expression of at most 256 characters")
				return p, &r
			}
			c.re = re
		case opEq, opNe, opIn:
		default:
			r := probe.ConfigResult("params.checks["+strconv.Itoa(i)+"].op", "unknown operator")
			return p, &r
		}
	}
	return p, nil
}

// missing reports whether the agent answered without the variable.
func missing(v gosnmp.SnmpPDU) bool { //nolint:gocritic // a PDU as gosnmp hands it out
	switch v.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return true
	}
	return false
}

// uptimeTicks returns sysUpTime in hundredths of a second.
func uptimeTicks(v gosnmp.SnmpPDU) (uint64, bool) { //nolint:gocritic // a PDU as gosnmp hands it out
	n, ok := probe.PDUInt(v)
	if !ok || n < 0 {
		return 0, false
	}
	return uint64(n), true
}

// holds evaluates the check against the answered variable.
func (c *check) holds(v gosnmp.SnmpPDU) bool { //nolint:gocritic // a PDU as gosnmp hands it out
	text := pduText(v)
	switch c.Op {
	case opEq:
		return equal(text, c.Value)
	case opNe:
		return !equal(text, c.Value)
	case opIn:
		for _, want := range strings.Split(c.Value, ",") {
			if equal(text, strings.TrimSpace(want)) {
				return true
			}
		}
		return false
	case opLt, opGt:
		got, err1 := strconv.ParseFloat(text, 64)
		want, err2 := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if err1 != nil || err2 != nil {
			// A non-numeric answer cannot satisfy a numeric comparison.
			return false
		}
		if c.Op == opLt {
			return got < want
		}
		return got > want
	case opRegex:
		return c.re != nil && c.re.MatchString(text)
	}
	return false
}

// equal compares two values numerically when both are numbers, else as
// text.
func equal(got, want string) bool {
	want = strings.TrimSpace(want)
	a, err1 := strconv.ParseFloat(got, 64)
	b, err2 := strconv.ParseFloat(want, 64)
	if err1 == nil && err2 == nil {
		return a == b
	}
	return got == want
}

// pduText renders a variable as the text the checks compare: numbers in
// decimal, strings as they are, OIDs without the leading dot, addresses in
// their usual form.
func pduText(v gosnmp.SnmpPDU) string { //nolint:gocritic // a PDU as gosnmp hands it out
	switch v.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		return gosnmp.ToBigInt(v.Value).String()
	case gosnmp.OpaqueFloat, gosnmp.OpaqueDouble:
		switch f := v.Value.(type) {
		case float32:
			return strconv.FormatFloat(float64(f), 'f', -1, 32)
		case float64:
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
	case gosnmp.ObjectIdentifier:
		if s, ok := v.Value.(string); ok {
			return strings.TrimPrefix(s, ".")
		}
	}
	switch x := v.Value.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case *big.Int:
		return x.String()
	}
	return ""
}
