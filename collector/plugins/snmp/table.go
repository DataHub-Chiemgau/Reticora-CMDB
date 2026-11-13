package snmp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// The SNMP session of the monitoring probes (WP-993, PRT-07, PRT-11): a
// probe opens a session with its own parameters (Options), reads scalars
// with Get and tables of any index with WalkColumns. The interface poll of
// the plugin (PollInterfaces) runs on the same session.

// Contract limits of the session parameters of a probe (AK4).
const (
	MinTimeout        = time.Second
	MaxTimeout        = 15 * time.Second
	MaxRetries        = 2
	MinMaxRepetitions = 1
	MaxMaxRepetitions = 50
	// defaultMaxRepetitions is the max-repetitions of a GETBULK walk when
	// the probe sets none: a column of 256 rows takes ceil(256/50) = 6.
	defaultMaxRepetitions = 50
)

// Options are the session parameters of a probe. A zero field takes the
// plugin default (port 161, timeout 5 s, 2 retries, max-repetitions 50);
// Retries is a pointer because 0 retries is a valid value.
type Options struct {
	Port           int
	Timeout        time.Duration
	Retries        *int
	MaxRepetitions int
}

// OptionsError names the field of Options outside its contract limit.
type OptionsError struct {
	Field string
	Value string
}

func (e *OptionsError) Error() string {
	return fmt.Sprintf("snmp: %s %s outside the allowed range", e.Field, e.Value)
}

// Validate refuses a value outside the contract limits (timeout 1–15 s,
// retries 0–2, max-repetitions 1–50, port 1–65535) with an *OptionsError
// naming the field; zero (unset) fields are valid.
func (o Options) Validate() error {
	switch {
	case o.Port != 0 && (o.Port < 1 || o.Port > 65535):
		return &OptionsError{Field: "port", Value: strconv.Itoa(o.Port)}
	case o.Timeout != 0 && (o.Timeout < MinTimeout || o.Timeout > MaxTimeout):
		return &OptionsError{Field: "timeout", Value: o.Timeout.String()}
	case o.Retries != nil && (*o.Retries < 0 || *o.Retries > MaxRetries):
		return &OptionsError{Field: "retries", Value: strconv.Itoa(*o.Retries)}
	case o.MaxRepetitions != 0 && (o.MaxRepetitions < MinMaxRepetitions || o.MaxRepetitions > MaxMaxRepetitions):
		return &OptionsError{Field: "max_repetitions", Value: strconv.Itoa(o.MaxRepetitions)}
	}
	return nil
}

// withDefaults fills the unset fields with the plugin defaults.
func (o Options) withDefaults() Options {
	if o.Port == 0 {
		o.Port = defaultPort
	}
	if o.Timeout == 0 {
		o.Timeout = defaultTimeout
	}
	if o.Retries == nil {
		r := defaultRetries
		o.Retries = &r
	}
	if o.MaxRepetitions == 0 {
		o.MaxRepetitions = defaultMaxRepetitions
	}
	return o
}

// Session is an open SNMP session to one target: SNMPv3 authPriv when the
// credentials carry a v3 account, otherwise SNMPv2c with the community. It
// is not safe for concurrent use.
type Session struct {
	ctx       context.Context
	target    string
	opts      Options
	v3        *v3Config
	community string

	g    *gosnmp.GoSNMP
	conn *reportConn // SNMPv3 only
}

// Open opens a session to target with the credentials of the namespace snmp
// and the probe's options (AK1). The session is chosen like the plugin's
// collection: SNMPv3 authPriv (with a time window resync and
// plugins.ErrAuthFailed for a usmStats report refusing the credentials) or
// SNMPv2c. Invalid options are refused with an *OptionsError.
func Open(ctx context.Context, target string, creds map[string]string, opts Options) (*Session, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return openSession(ctx, target, creds, opts.withDefaults())
}

// openSession opens a session with complete options.
func openSession(ctx context.Context, target string, creds map[string]string, opts Options) (*Session, error) {
	v3, useV3, err := v3FromCreds(creds)
	if err != nil {
		return nil, err
	}
	s := &Session{ctx: ctx, target: target, opts: opts}
	if useV3 {
		s.v3 = v3
	} else if s.community = creds["community"]; s.community == "" {
		s.community = "public"
	}
	if err := s.connect(); err != nil {
		return nil, err
	}
	return s, nil
}

// connect builds and connects the gosnmp session.
func (s *Session) connect() error {
	if s.v3 == nil {
		s.g = &gosnmp.GoSNMP{
			Context: s.ctx, Target: s.target, Port: uint16(s.opts.Port), Transport: "udp", Community: s.community,
			Version: gosnmp.Version2c, Timeout: s.opts.Timeout, Retries: *s.opts.Retries, MaxOids: gosnmp.MaxOids,
			MaxRepetitions: uint32(s.opts.MaxRepetitions),
		}
		if err := s.g.Connect(); err != nil {
			return fmt.Errorf("snmp: dial %s: %w", s.target, err)
		}
		return nil
	}
	cfg := s.v3
	s.g = &gosnmp.GoSNMP{
		Context:        s.ctx,
		Target:         s.target,
		Port:           uint16(s.opts.Port),
		Transport:      "udp",
		Version:        gosnmp.Version3,
		Timeout:        s.opts.Timeout,
		Retries:        *s.opts.Retries,
		MaxOids:        gosnmp.MaxOids,
		MaxRepetitions: uint32(s.opts.MaxRepetitions),
		SecurityModel:  gosnmp.UserSecurityModel,
		MsgFlags:       gosnmp.AuthPriv,
		ContextName:    cfg.contextName,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			UserName:                 cfg.user,
			AuthenticationProtocol:   cfg.authProto,
			AuthenticationPassphrase: cfg.authPass,
			PrivacyProtocol:          cfg.privProto,
			PrivacyPassphrase:        cfg.privPass,
		},
	}
	if err := s.g.Connect(); err != nil {
		return fmt.Errorf("snmp v3: connect %s: %w", s.target, err)
	}
	s.conn = &reportConn{Conn: s.g.Conn, user: cfg.user}
	s.g.Conn = s.conn
	return nil
}

// Close closes the session.
func (s *Session) Close() error {
	if s.g == nil || s.g.Conn == nil {
		return nil
	}
	return s.g.Conn.Close()
}

// Timeout is the effective timeout of a request of the session.
func (s *Session) Timeout() time.Duration { return s.opts.Timeout }

// Version is "v3" or "v2c".
func (s *Session) Version() string {
	if s.v3 != nil {
		return "v3"
	}
	return "v2c"
}

// run runs do on the session. On SNMPv3 a failure is mapped by v3Failure
// (plugins.ErrAuthFailed for a refused credential); a request the agent
// refuses with usmStatsNotInTimeWindows is run once more on a new session,
// whose engine discovery resynchronises boots and time; only a second
// refusal is reported. do must start from scratch when it runs again.
func (s *Session) run(do func(g *gosnmp.GoSNMP) error) error {
	if s.v3 == nil {
		return do(s.g)
	}
	err := s.attempt(do)
	if !errors.Is(err, errNotInTimeWindow) {
		return err
	}
	_ = s.Close()
	if cerr := s.connect(); cerr != nil {
		return cerr
	}
	if err = s.attempt(do); errors.Is(err, errNotInTimeWindow) {
		return fmt.Errorf("%w, also after a time resync", err)
	}
	return err
}

func (s *Session) attempt(do func(g *gosnmp.GoSNMP) error) error {
	if err := do(s.g); err != nil {
		return v3Failure(s.target, s.conn, err)
	}
	return nil
}

// Get reads the variables oids (at most MaxOids per request). An SNMPv3
// Report PDU instead of a response is an error.
func (s *Session) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	var out []gosnmp.SnmpPDU
	err := s.run(func(g *gosnmp.GoSNMP) error {
		out = make([]gosnmp.SnmpPDU, 0, len(oids))
		for start := 0; start < len(oids); start += g.MaxOids {
			pkt, err := g.Get(oids[start:min(start+g.MaxOids, len(oids))])
			if err == nil && s.v3 != nil {
				err = reportPDU(pkt)
			}
			if err != nil {
				return err
			}
			out = append(out, pkt.Variables...)
		}
		return nil
	})
	return out, err
}

// Row is one row of a walked table: its index as the OID suffix after the
// column and the values of its columns by column OID (without leading dot).
type Row struct {
	Index  []uint32
	Values map[string]gosnmp.SnmpPDU
}

// Value returns the value of column in the row.
func (r Row) Value(column string) (gosnmp.SnmpPDU, bool) {
	v, ok := r.Values[strings.TrimPrefix(column, ".")]
	return v, ok
}

// Rows are the rows of a walk in index order of the first column; rows only
// later columns have follow in their walk order. Truncated is true when the
// walk stopped at its row limit.
type Rows struct {
	Rows      []Row
	Truncated bool
}

// WalkColumns walks the columns of a table and merges them into rows by
// their index (AK2): an index of any form (integer, IPv4, InetAddress,
// several parts) is kept as the OID suffix. At most maxRows rows are kept
// (maxRows ≤ 0: no limit); a walk that reaches the limit ends cleanly with
// Rows.Truncated and the rows read so far (AK3). An error in the middle of
// the walk returns the rows read so far with the error.
func (s *Session) WalkColumns(columns []string, maxRows int) (Rows, error) {
	var rows Rows
	err := s.run(func(g *gosnmp.GoSNMP) error {
		var err error
		rows, err = walkColumns(g, columns, maxRows)
		return err
	})
	return rows, err
}

// errWalkLimit ends a column walk at its row limit.
var errWalkLimit = errors.New("snmp: walk limit reached")

// walkColumn walks one column and hands each variable below it with its
// index to fn; fn returns errWalkLimit to end the walk without error. A
// variable whose index is not numeric is skipped.
func walkColumn(g *gosnmp.GoSNMP, column string, fn func(index []uint32, v gosnmp.SnmpPDU) error) error {
	column = strings.TrimPrefix(column, ".")
	walk := g.BulkWalk
	if g.Version == gosnmp.Version1 {
		walk = g.Walk
	}
	err := walk(column, func(v gosnmp.SnmpPDU) error {
		rest, ok := strings.CutPrefix(strings.TrimPrefix(v.Name, "."), column+".")
		if !ok {
			return nil
		}
		index, ok := parseIndex(rest)
		if !ok {
			return nil
		}
		return fn(index, v)
	})
	if errors.Is(err, errWalkLimit) {
		return nil
	}
	return err
}

// walkColumns implements WalkColumns on an open gosnmp session. The first
// column decides the rows up to maxRows; a later column adds its values to
// these rows, adds rows the first column lacks while there is room and,
// once the walk is truncated, stops after the last row kept.
func walkColumns(g *gosnmp.GoSNMP, columns []string, maxRows int) (Rows, error) {
	var rows Rows
	byIndex := map[string]int{}
	var last []uint32
	for i, column := range columns {
		column = strings.TrimPrefix(column, ".")
		err := walkColumn(g, column, func(index []uint32, v gosnmp.SnmpPDU) error {
			if i > 0 && rows.Truncated && compareIndex(index, last) > 0 {
				return errWalkLimit
			}
			key := indexKey(index)
			n, seen := byIndex[key]
			if !seen {
				if maxRows > 0 && len(rows.Rows) >= maxRows {
					rows.Truncated = true
					if i == 0 {
						return errWalkLimit
					}
					return nil
				}
				n = len(rows.Rows)
				byIndex[key] = n
				rows.Rows = append(rows.Rows, Row{Index: index, Values: map[string]gosnmp.SnmpPDU{}})
				// last is the highest index kept over all columns: a
				// sparse first column leaves rows to later columns, and
				// a cut in such a column must not cut rows kept before.
				if last == nil || compareIndex(index, last) > 0 {
					last = index
				}
			}
			// At the limit the walk goes on to the next row, which tells
			// whether the table has more (Truncated) or ends here.
			rows.Rows[n].Values[column] = v
			return nil
		})
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}

// parseIndex parses the OID suffix "a.b.c" into its sub-identifiers.
func parseIndex(s string) ([]uint32, bool) {
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]uint32, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, false
		}
		out[i] = uint32(n)
	}
	return out, true
}

func indexKey(index []uint32) string {
	var b strings.Builder
	for i, n := range index {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatUint(uint64(n), 10))
	}
	return b.String()
}

// compareIndex orders two indexes as the agent orders their OIDs.
func compareIndex(a, b []uint32) int { return slices.Compare(a, b) }

// ErrIndex is returned by the index decoders for an index of the wrong form.
var ErrIndex = errors.New("snmp: malformed table index")

// IndexInt decodes an integer index (one sub-identifier), e.g. ifIndex,
// and returns the remaining sub-identifiers.
func IndexInt(index []uint32) (value int, rest []uint32, err error) {
	if len(index) < 1 {
		return 0, nil, fmt.Errorf("%w: integer expected", ErrIndex)
	}
	return int(index[0]), index[1:], nil
}

// IndexIPv4 decodes an IpAddress index (four sub-identifiers), e.g. the
// peer address of bgpPeerTable.
func IndexIPv4(index []uint32) (netip.Addr, []uint32, error) {
	if len(index) < 4 {
		return netip.Addr{}, nil, fmt.Errorf("%w: IPv4 address expected", ErrIndex)
	}
	var b [4]byte
	for i := range b {
		if index[i] > 255 {
			return netip.Addr{}, nil, fmt.Errorf("%w: IPv4 octet %d", ErrIndex, index[i])
		}
		b[i] = byte(index[i])
	}
	return netip.AddrFrom4(b), index[4:], nil
}

// InetAddressType values (RFC 4001).
const (
	inetIPv4  = 1
	inetIPv6  = 2
	inetIPv4z = 3
	inetIPv6z = 4
)

// IndexInetAddress decodes an InetAddressType/InetAddress index pair (RFC
// 4001): the type, the length and the octets, e.g. the peer of the
// BGP4-V2 tables. Only ipv4 (length 4) and ipv6 (length 16) are accepted;
// ipv4z/ipv6z, unknown and dns addresses are refused.
func IndexInetAddress(index []uint32) (netip.Addr, []uint32, error) {
	if len(index) < 2 {
		return netip.Addr{}, nil, fmt.Errorf("%w: InetAddress type and length expected", ErrIndex)
	}
	typ, n := index[0], int(index[1])
	var want int
	switch typ {
	case inetIPv4:
		want = 4
	case inetIPv6:
		want = 16
	case inetIPv4z, inetIPv6z:
		return netip.Addr{}, nil, fmt.Errorf("%w: InetAddress with zone (type %d) not supported", ErrIndex, typ)
	default:
		return netip.Addr{}, nil, fmt.Errorf("%w: InetAddress type %d not supported", ErrIndex, typ)
	}
	if n != want || len(index) < 2+n {
		return netip.Addr{}, nil, fmt.Errorf("%w: InetAddress of type %d with length %d", ErrIndex, typ, n)
	}
	b := make([]byte, n)
	for i := range b {
		o := index[2+i]
		if o > 255 {
			return netip.Addr{}, nil, fmt.Errorf("%w: InetAddress octet %d", ErrIndex, o)
		}
		b[i] = byte(o)
	}
	addr, _ := netip.AddrFromSlice(b)
	return addr, index[2+n:], nil
}

// IndexPart decodes one part of a composite index and returns the rest.
type IndexPart func(index []uint32) (value any, rest []uint32, err error)

// Part turns a typed decoder (IndexInt, IndexIPv4, IndexInetAddress) into
// an IndexPart of IndexComposite.
func Part[T any](decode func([]uint32) (T, []uint32, error)) IndexPart {
	return func(index []uint32) (any, []uint32, error) {
		v, rest, err := decode(index)
		return v, rest, err
	}
}

// IndexComposite returns a decoder of an index of several parts, e.g.
// ospfNbrTable (IPv4 address and integer): the parts in order, and nothing
// may remain.
func IndexComposite(parts ...IndexPart) func(index []uint32) ([]any, error) {
	return func(index []uint32) ([]any, error) {
		values := make([]any, 0, len(parts))
		rest := index
		for _, part := range parts {
			v, r, err := part(rest)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
			rest = r
		}
		if len(rest) != 0 {
			return nil, fmt.Errorf("%w: %d sub-identifiers left", ErrIndex, len(rest))
		}
		return values, nil
	}
}
