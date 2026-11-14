// Package rpki holds the RPKI probe types of the collector (PRT-17, E-194,
// E-195): rpki_rtr (WP-1020), an RPKI-to-Router client after RFC 8210
// with fallback to RFC 6810, built without a new dependency. The VRP table
// of a validator is protocol state shared by every probe of that validator
// (Manager, vrp.go); the executors themselves hold no state.
//
// The package registers its executors in init(); the collector links it in
// through collector/probe/all.
package rpki

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeRTR is the register key of the RTR probe.
const TypeRTR = "rpki_rtr"

// DefaultPort is the RTR port of RFC 6810 (validators also use 3323 or
// 8282).
const DefaultPort = 323

// PDU types of RFC 8210 section 5.
const (
	TypeSerialNotify  = 0
	TypeSerialQuery   = 1
	TypeResetQuery    = 2
	TypeCacheResponse = 3
	TypeIPv4Prefix    = 4
	TypeIPv6Prefix    = 6
	TypeEndOfData     = 7
	TypeCacheReset    = 8
	TypeRouterKey     = 9
	TypeErrorReport   = 10
)

// Error codes of RFC 8210 section 12.
const (
	ErrCorruptData        = 0
	ErrInternal           = 1
	ErrNoDataAvailable    = 2
	ErrInvalidRequest     = 3
	ErrUnsupportedVersion = 4
	ErrUnsupportedPDU     = 5
	ErrUnknownWithdrawal  = 6
	ErrDuplicateAnnounce  = 7
	ErrUnexpectedVersion  = 8
)

// errorNames are the label values of the error codes.
var errorNames = map[uint16]string{
	ErrCorruptData: "corrupt_data", ErrInternal: "internal_error", ErrNoDataAvailable: "no_data_available",
	ErrInvalidRequest: "invalid_request", ErrUnsupportedVersion: "unsupported_protocol_version",
	ErrUnsupportedPDU: "unsupported_pdu_type", ErrUnknownWithdrawal: "withdrawal_of_unknown_record",
	ErrDuplicateAnnounce: "duplicate_announcement", ErrUnexpectedVersion: "unexpected_protocol_version",
}

// MaxPDU bounds the length of one PDU; a longer or shorter than possible
// length ends the session with Error Report "Corrupt Data" (WP-1020 AK1).
const MaxPDU = 64 << 10

// errCorrupt marks a PDU of invalid length or content.
var errCorrupt = errors.New("rpki: corrupt PDU")

// ReportError is an Error Report of the cache.
type ReportError struct{ Code uint16 }

func (e *ReportError) Error() string {
	return fmt.Sprintf("rpki: cache sent error report %d (%s)", e.Code, errorName(e.Code))
}

func errorName(code uint16) string {
	if n, ok := errorNames[code]; ok {
		return n
	}
	return "code_" + strconv.Itoa(int(code))
}

// pdu is one decoded PDU.
type pdu struct {
	version uint8
	typ     uint8
	// session is the session ID of Serial Notify, Serial Query, Cache
	// Response and End of Data.
	session   uint16
	errorCode uint16
	serial    uint32
	hasTimers bool
	refresh   uint32
	retry     uint32
	expire    uint32
	flags     uint8
	plen      uint8
	maxLen    uint8
	prefix    netip.Addr
	asn       uint32
}

// readPDU reads one PDU: the header, then the body of its length. A
// length below 8 or above MaxPDU is errCorrupt (the header is returned for
// the Error Report).
func readPDU(r *bufio.Reader) ([]byte, error) {
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(hdr[4:8])
	if length < 8 || length > MaxPDU {
		return hdr, errCorrupt
	}
	raw := make([]byte, length)
	copy(raw, hdr)
	if _, err := io.ReadFull(r, raw[8:]); err != nil {
		return nil, err
	}
	return raw, nil
}

// parsePDU decodes a whole PDU; a length that does not fit its type, a
// prefix length above the family or a maximum length below the prefix
// length is errCorrupt.
func parsePDU(b []byte) (pdu, error) {
	var p pdu
	if len(b) < 8 {
		return p, errCorrupt
	}
	p.version, p.typ = b[0], b[1]
	if p.version > 1 {
		return p, errCorrupt
	}
	length := binary.BigEndian.Uint32(b[4:8])
	if int(length) != len(b) {
		return p, errCorrupt
	}
	p.session = binary.BigEndian.Uint16(b[2:4])
	want := func(n int) error {
		if len(b) != n {
			return errCorrupt
		}
		return nil
	}
	switch p.typ {
	case TypeSerialNotify, TypeSerialQuery:
		if err := want(12); err != nil {
			return p, err
		}
		p.serial = binary.BigEndian.Uint32(b[8:12])
	case TypeResetQuery, TypeCacheResponse, TypeCacheReset:
		return p, want(8)
	case TypeIPv4Prefix, TypeIPv6Prefix:
		size, bits := 20, 32
		if p.typ == TypeIPv6Prefix {
			size, bits = 32, 128
		}
		if err := want(size); err != nil {
			return p, err
		}
		p.flags, p.plen, p.maxLen = b[8], b[9], b[10]
		if int(p.plen) > bits || int(p.maxLen) > bits || p.maxLen < p.plen {
			return p, errCorrupt
		}
		if size == 20 {
			p.prefix = netip.AddrFrom4([4]byte(b[12:16]))
			p.asn = binary.BigEndian.Uint32(b[16:20])
		} else {
			p.prefix = netip.AddrFrom16([16]byte(b[12:28]))
			p.asn = binary.BigEndian.Uint32(b[28:32])
		}
		// Bits beyond the prefix length must be zero; mask them so that
		// equal VRPs compare equal.
		pfx, err := p.prefix.Prefix(int(p.plen))
		if err != nil {
			return p, errCorrupt
		}
		p.prefix = pfx.Addr()
	case TypeEndOfData:
		switch {
		case p.version == 0 && len(b) == 12:
		case p.version == 1 && len(b) == 24:
			p.hasTimers = true
			p.refresh = binary.BigEndian.Uint32(b[12:16])
			p.retry = binary.BigEndian.Uint32(b[16:20])
			p.expire = binary.BigEndian.Uint32(b[20:24])
		default:
			return p, errCorrupt
		}
		p.serial = binary.BigEndian.Uint32(b[8:12])
	case TypeRouterKey:
		// Skipped (BGPsec); only the length is checked: header, SKI (20)
		// and AS (4) at least.
		if len(b) < 32 {
			return p, errCorrupt
		}
	case TypeErrorReport:
		p.errorCode = p.session
		if len(b) < 16 {
			return p, errCorrupt
		}
		encLen := binary.BigEndian.Uint32(b[8:12])
		if uint64(encLen)+16 > uint64(len(b)) {
			return p, errCorrupt
		}
		off := 12 + int(encLen)
		textLen := binary.BigEndian.Uint32(b[off : off+4])
		if uint64(off)+4+uint64(textLen) != uint64(len(b)) {
			return p, errCorrupt
		}
	default:
		return p, errCorrupt
	}
	return p, nil
}

func header(version, typ uint8, session uint16, length int) []byte {
	b := make([]byte, length)
	b[0], b[1] = version, typ
	binary.BigEndian.PutUint16(b[2:4], session)
	binary.BigEndian.PutUint32(b[4:8], uint32(length)) //nolint:gosec // PDU lengths are small
	return b
}

func resetQuery(version uint8) []byte { return header(version, TypeResetQuery, 0, 8) }

func serialQuery(version uint8, session uint16, serial uint32) []byte {
	b := header(version, TypeSerialQuery, session, 12)
	binary.BigEndian.PutUint32(b[8:12], serial)
	return b
}

// errorReport encloses the offending PDU (at most its first MaxPDU/2
// bytes) without a text.
func errorReport(version uint8, code uint16, offending []byte) []byte {
	if len(offending) > MaxPDU/2 {
		offending = offending[:MaxPDU/2]
	}
	b := header(version, TypeErrorReport, code, 16+len(offending))
	binary.BigEndian.PutUint32(b[8:12], uint32(len(offending))) //nolint:gosec // bounded above
	copy(b[12:], offending)
	return b
}

// defaultManager holds the sessions of the collector.
var defaultManager = &Manager{}

func init() { probe.Register(TypeRTR, &RTRExecutor{}) }

// Value names of an rpki_rtr result.
const (
	ValueUp              = "probe_rpki_rtr_up"
	ValueSerial          = "probe_rpki_serial"
	ValueVRPs            = "probe_rpki_vrps"
	ValueDataAge         = "probe_rpki_data_age_seconds"
	ValueProtocolVersion = "probe_rpki_protocol_version"
	LabelAF              = "af"
	LabelErrorCode       = "error_code"
	LabelError           = "error"
)

// RTRExecutor is the probe type rpki_rtr (PRT-17, WP-1020): reachability,
// serial, VRP counts per family, data age and protocol version of a
// validator. The session is the shared one of the Manager; the first check
// opens it and waits for the first End of Data.
//
// Outcomes: ok; ok with the warn code rpki_stale when the data is older
// than the Expire interval (degraded); fail with refused, timeout or
// unreachable when the validator cannot be reached; error with vrp_limit
// above 2,000,000 VRPs (the old table stays) and error/unsupported with
// the labels error_code and error after an Error Report of the cache or a
// corrupt PDU (unknown).
type RTRExecutor struct {
	// Manager replaces the sessions of the collector (tests).
	Manager *Manager
}

type rtrParams struct {
	Port int `json:"port"`
}

// validatorAddr returns host:port of the validator of a probe: the target
// without a port of its own and the port parameter (DefaultPort).
func validatorAddr(target string, port int) string {
	host := strings.TrimSpace(target)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if port <= 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Execute implements probe.Executor.
func (e *RTRExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p rtrParams
	if len(bytes.TrimSpace(spec.Params)) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	m := e.Manager
	if m == nil {
		m = defaultManager
	}
	st, err := await(ctx, m, validatorAddr(spec.Target, p.Port))
	return rtrResult(m, &st, err)
}

// await uses the shared session of addr (for rpki_rtr and rpki_roa): it
// opens it when needed and waits until the validator has delivered data
// or failed after the call began. err is the failure that is current (nil:
// connected with data), or ctx.Err().
func await(ctx context.Context, m *Manager, addr string) (state, error) {
	for {
		v := m.validator(addr)
		if !v.touch() {
			// Ended between the lookup and the use (idle): take a new one.
			m.remove(v)
			continue
		}
		start := v.snapshot()
		v.ensure()
		for {
			st := v.snapshot()
			if st.connected && st.table != nil {
				return st, nil
			}
			if st.err != nil && st.errSeq > start.errSeq {
				return st, st.err
			}
			if v.isClosed() {
				break
			}
			select {
			case <-st.changed:
			case <-ctx.Done():
				return v.snapshot(), ctx.Err()
			}
		}
		if ctx.Err() != nil {
			return v.snapshot(), ctx.Err()
		}
	}
}

// rtrResult builds the result (WP-1020 AK4, AK5).
func rtrResult(m *Manager, st *state, err error) probewire.Result {
	values := map[string]float64{ValueUp: 0}
	if err == nil {
		values[ValueUp] = 1
	}
	if st.table != nil {
		v4, v6 := st.table.Len()
		values[probewire.ValueKey(ValueVRPs, map[string]string{LabelAF: "ipv4"})] = float64(v4)
		values[probewire.ValueKey(ValueVRPs, map[string]string{LabelAF: "ipv6"})] = float64(v6)
		values[ValueSerial] = float64(st.serial)
		values[ValueProtocolVersion] = float64(st.version)
		values[ValueDataAge] = max(m.now().Sub(st.lastEOD).Seconds(), 0)
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	var report *ReportError
	var opErr *net.OpError
	switch {
	case err == nil:
		if m.now().Sub(st.lastEOD) > st.expire {
			r.Code = probewire.WarnRPKIStale
		}
	case errors.Is(err, errVRPLimit):
		r.Status, r.Code = probewire.StatusError, probewire.ReasonVRPLimit
	case errors.As(err, &report):
		r.Status, r.Code = probewire.StatusError, probewire.ReasonUnsupported
		r.Labels = map[string]string{LabelErrorCode: strconv.Itoa(int(report.Code)), LabelError: errorName(report.Code)}
	case errors.Is(err, errCorrupt):
		r.Status, r.Code = probewire.StatusError, probewire.ReasonUnsupported
		r.Labels = map[string]string{LabelErrorCode: strconv.Itoa(ErrCorruptData), LabelError: errorName(ErrCorruptData)}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonRefused
	case errors.As(err, &opErr) && opErr.Timeout():
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonTimeout
	default:
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonUnreachable
	}
	return r
}
