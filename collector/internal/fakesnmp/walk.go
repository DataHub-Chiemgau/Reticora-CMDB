package fakesnmp

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// LoadWalkFile reads a walk file (see LoadWalk).
func LoadWalkFile(path string) ([]gosnmp.SnmpPDU, error) {
	f, err := os.Open(path) //nolint:gosec // test infrastructure: the path names a fixture
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	pdus, err := LoadWalk(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pdus, nil
}

// LoadWalk reads the output of "snmpwalk -On" ("OID = TYPE: value") as the
// variables of the agent, in file order (WP-992, PRT-21). Types: STRING,
// Hex-STRING, INTEGER (also "up(1)"), Gauge32, Counter32, Counter64,
// Timeticks/TimeTicks ("(12345) 0:02:03.45"), OID, IpAddress and Opaque
// ("Opaque: Float: 1.5", "Opaque: Double: 2.5"); an empty string may also
// stand as `""` without a type. Empty lines and lines starting with # are
// skipped. A malformed line or an unknown type is an error naming the line
// number (the first line of a variable that spans several).
//
// A value may span several lines as net-snmp prints it: a Hex-STRING
// longer than 16 bytes continues on lines holding only hex pairs, a quoted
// STRING with embedded line breaks continues up to its closing quote (the
// breaks are kept as "\n").
//
// The values have the Go types the agent works with: []byte for STRING
// and Hex-STRING, int for INTEGER, uint32 for Gauge32, Counter32 and
// TimeTicks, uint64 for Counter64, string for OID and IpAddress, float32
// for Opaque Float and float64 for Opaque Double.
func LoadWalk(r io.Reader) ([]gosnmp.SnmpPDU, error) {
	var out []gosnmp.SnmpPDU
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	var (
		pending string // the variable read so far
		start   int    // its first line
		mode    continuation
	)
	flush := func() error {
		if pending == "" {
			return nil
		}
		if mode == openString {
			return fmt.Errorf("line %d: string not closed before the end of the walk", start)
		}
		pdu, err := parseWalkLine(pending)
		if err != nil {
			return fmt.Errorf("line %d: %w", start, err)
		}
		out = append(out, pdu)
		pending = ""
		return nil
	}
	for sc.Scan() {
		n++
		raw := strings.TrimRight(sc.Text(), "\r")
		if mode == openString {
			pending += "\n" + raw
			if stringClosed(pending) {
				mode = noContinuation
			}
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !isWalkStart(line) {
			if mode == hexContinuation && hexPairs(line) {
				pending += " " + line
				continue
			}
			if pending != "" {
				// The malformed line ends the variable before it, which is
				// reported first if it is broken too.
				if err := flush(); err != nil {
					return nil, err
				}
			}
			return nil, fmt.Errorf("line %d: %q is no walk line (OID = TYPE: value)", n, line)
		}
		if err := flush(); err != nil {
			return nil, err
		}
		pending, start, mode = line, n, continuationOf(line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("line %d: %w", n+1, err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// continuation is how a variable may continue on the next line.
type continuation int

const (
	noContinuation  continuation = iota
	hexContinuation              // a Hex-STRING: lines of hex pairs follow
	openString                   // a quoted STRING without its closing quote
)

// isWalkStart reports whether line starts a variable: an OID, " = " and
// the typed value.
func isWalkStart(line string) bool {
	oid, _, ok := strings.Cut(line, " = ")
	return ok && validOID(strings.TrimSpace(oid))
}

// continuationOf returns how the variable starting with line continues.
func continuationOf(line string) continuation {
	_, typed, _ := strings.Cut(line, " = ")
	typed = strings.TrimSpace(typed)
	switch {
	case strings.HasPrefix(typed, "Hex-STRING:"):
		return hexContinuation
	case strings.HasPrefix(typed, "STRING:") && !stringClosed(line):
		return openString
	}
	return noContinuation
}

// stringClosed reports whether the quoted value of a STRING variable is
// complete: an unquoted value is, a quoted one when it ends with a quote
// that is not escaped.
func stringClosed(variable string) bool {
	_, typed, _ := strings.Cut(variable, " = ")
	_, value, _ := strings.Cut(typed, ":")
	value = strings.TrimLeft(value, " ")
	if !strings.HasPrefix(value, `"`) {
		return true
	}
	rest := strings.TrimRight(value[1:], " ")
	if !strings.HasSuffix(rest, `"`) {
		return false
	}
	backslashes := 0
	for i := len(rest) - 2; i >= 0 && rest[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 0
}

// hexPairs reports whether line holds only space separated hex pairs, the
// continuation of a Hex-STRING.
func hexPairs(line string) bool {
	fields := strings.Fields(line)
	for _, f := range fields {
		if len(f) != 2 {
			return false
		}
		if _, err := hex.DecodeString(f); err != nil {
			return false
		}
	}
	return len(fields) > 0
}

func parseWalkLine(line string) (gosnmp.SnmpPDU, error) {
	oid, typed, ok := strings.Cut(line, " = ")
	oid = strings.TrimSpace(oid)
	if !ok || !validOID(oid) {
		return gosnmp.SnmpPDU{}, fmt.Errorf("%q is no walk line (OID = TYPE: value)", line)
	}
	name := "." + strings.TrimPrefix(oid, ".")
	typed = strings.TrimSpace(typed)
	if typed == `""` {
		return gosnmp.SnmpPDU{Name: name, Type: gosnmp.OctetString, Value: []byte{}}, nil
	}
	kind, value, ok := strings.Cut(typed, ":")
	if !ok {
		return gosnmp.SnmpPDU{}, fmt.Errorf("%q has no type", line)
	}
	value = strings.TrimSpace(value)
	pdu := gosnmp.SnmpPDU{Name: name}
	var err error
	switch kind {
	case "STRING":
		pdu.Type, pdu.Value = gosnmp.OctetString, []byte(unquote(value))
	case "Hex-STRING":
		var b []byte
		b, err = hex.DecodeString(strings.Join(strings.Fields(value), ""))
		pdu.Type, pdu.Value = gosnmp.OctetString, b
	case "INTEGER":
		var v int64
		v, err = strconv.ParseInt(enumNumber(value), 10, 32)
		pdu.Type, pdu.Value = gosnmp.Integer, int(v)
	case "Gauge32", "Counter32":
		var v uint64
		v, err = strconv.ParseUint(value, 10, 32)
		pdu.Type, pdu.Value = gosnmp.Gauge32, uint32(v)
		if kind == "Counter32" {
			pdu.Type = gosnmp.Counter32
		}
	case "Counter64":
		var v uint64
		v, err = strconv.ParseUint(value, 10, 64)
		pdu.Type, pdu.Value = gosnmp.Counter64, v
	case "Timeticks", "TimeTicks":
		var v uint64
		v, err = strconv.ParseUint(ticksNumber(value), 10, 32)
		pdu.Type, pdu.Value = gosnmp.TimeTicks, uint32(v)
	case "OID":
		if !validOID(value) {
			err = fmt.Errorf("%q is no OID", value)
		}
		pdu.Type, pdu.Value = gosnmp.ObjectIdentifier, "."+strings.TrimPrefix(value, ".")
	case "IpAddress":
		if ip := net.ParseIP(value); ip == nil || ip.To4() == nil {
			err = fmt.Errorf("%q is no IPv4 address", value)
		}
		pdu.Type, pdu.Value = gosnmp.IPAddress, value
	case "Opaque":
		pdu, err = parseOpaque(name, value)
	default:
		return gosnmp.SnmpPDU{}, fmt.Errorf("type %q is not supported", kind)
	}
	if err != nil {
		return gosnmp.SnmpPDU{}, fmt.Errorf("%s value %q: %w", kind, value, err)
	}
	return pdu, nil
}

// parseOpaque reads "Float: 1.5" or "Double: 2.5".
func parseOpaque(name, value string) (gosnmp.SnmpPDU, error) {
	kind, number, ok := strings.Cut(value, ":")
	if !ok {
		return gosnmp.SnmpPDU{}, fmt.Errorf("opaque value without Float or Double")
	}
	number = strings.TrimSpace(number)
	switch strings.TrimSpace(kind) {
	case "Float":
		v, err := strconv.ParseFloat(number, 32)
		if err != nil {
			return gosnmp.SnmpPDU{}, err
		}
		return gosnmp.SnmpPDU{Name: name, Type: gosnmp.OpaqueFloat, Value: float32(v)}, nil
	case "Double":
		v, err := strconv.ParseFloat(number, 64)
		if err != nil || math.IsInf(v, 0) {
			return gosnmp.SnmpPDU{}, fmt.Errorf("%q is no double", number)
		}
		return gosnmp.SnmpPDU{Name: name, Type: gosnmp.OpaqueDouble, Value: v}, nil
	}
	return gosnmp.SnmpPDU{}, fmt.Errorf("opaque type %q is not supported", kind)
}

// unquote removes the quotes of a STRING value; an unquoted value stays.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	return s
}

// enumNumber returns the number of an enumerated INTEGER ("up(1)" → "1").
func enumNumber(s string) string {
	if open := strings.LastIndexByte(s, '('); open >= 0 && strings.HasSuffix(s, ")") {
		return s[open+1 : len(s)-1]
	}
	return s
}

// ticksNumber returns the number of a TimeTicks value ("(12345) 0:02:03.45"
// → "12345"); a plain number stays.
func ticksNumber(s string) string {
	if strings.HasPrefix(s, "(") {
		if end := strings.IndexByte(s, ')'); end > 0 {
			return s[1:end]
		}
	}
	return s
}

// validOID reports whether s is a dotted numeric OID (leading dot optional).
func validOID(s string) bool {
	s = strings.TrimPrefix(s, ".")
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}
