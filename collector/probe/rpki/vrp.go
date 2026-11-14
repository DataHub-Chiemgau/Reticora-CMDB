package rpki

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// MaxVRPs bounds the VRPs of one validator (both address families, PRT-17,
// WP-1020 AK3); a larger state is discarded with code vrp_limit.
const MaxVRPs = 2_000_000

// Default intervals of RFC 8210 section 6 for a version 0 session, whose
// End of Data carries none.
const (
	DefaultRefresh = 3600 * time.Second
	DefaultRetry   = 600 * time.Second
	DefaultExpire  = 7200 * time.Second
)

// Ranges of the End of Data intervals (RFC 8210 section 6); values
// outside are clamped, so that a cache cannot make the client spin.
const (
	minRefresh, maxRefresh = 1 * time.Second, 86400 * time.Second
	minRetry, maxRetry     = 1 * time.Second, 7200 * time.Second
	minExpire, maxExpire   = 600 * time.Second, 172800 * time.Second
)

// minRetryWait is the shortest pause between two connection attempts.
const minRetryWait = time.Second

// clampInterval returns the interval of seconds within [lo, hi].
func clampInterval(seconds uint32, lo, hi time.Duration) time.Duration {
	return min(max(time.Duration(seconds)*time.Second, lo), hi)
}

// dialTimeout bounds the TCP connect to a validator.
const dialTimeout = 10 * time.Second

// errVRPLimit is the state of a validator above MaxVRPs.
var errVRPLimit = errors.New("rpki: more than 2,000,000 VRPs")

// vrp4 and vrp6 are the compact VRPs of the table (12 and 24 bytes).
type vrp4 struct {
	addr   uint32
	asn    uint32
	plen   uint8
	maxLen uint8
}

type vrp6 struct {
	hi, lo uint64
	asn    uint32
	plen   uint8
	maxLen uint8
}

func cmp4(a, b vrp4) int {
	if c := cmp.Compare(a.addr, b.addr); c != 0 {
		return c
	}
	if c := cmp.Compare(a.plen, b.plen); c != 0 {
		return c
	}
	if c := cmp.Compare(a.maxLen, b.maxLen); c != 0 {
		return c
	}
	return cmp.Compare(a.asn, b.asn)
}

func cmp6(a, b vrp6) int {
	if c := cmp.Compare(a.hi, b.hi); c != 0 {
		return c
	}
	if c := cmp.Compare(a.lo, b.lo); c != 0 {
		return c
	}
	if c := cmp.Compare(a.plen, b.plen); c != 0 {
		return c
	}
	if c := cmp.Compare(a.maxLen, b.maxLen); c != 0 {
		return c
	}
	return cmp.Compare(a.asn, b.asn)
}

// Table is the VRP set of a validator at one serial: per address family a
// slice sorted by prefix, length, maximum length and AS, without
// duplicates. A Table is never changed; an End of Data replaces it as a
// whole (WP-1020 AK3).
type Table struct {
	v4 []vrp4
	v6 []vrp6
}

// Len returns the number of VRPs per family.
func (t *Table) Len() (v4, v6 int) {
	if t == nil {
		return 0, 0
	}
	return len(t.v4), len(t.v6)
}

// VRP is one validated ROA payload.
type VRP struct {
	Prefix netip.Prefix
	MaxLen uint8
	ASN    uint32
}

// VRPs returns every VRP of the table (tests, small tables).
func (t *Table) VRPs() []VRP {
	if t == nil {
		return nil
	}
	out := make([]VRP, 0, len(t.v4)+len(t.v6))
	for _, v := range t.v4 {
		a := [4]byte{byte(v.addr >> 24), byte(v.addr >> 16), byte(v.addr >> 8), byte(v.addr)}
		out = append(out, VRP{Prefix: netip.PrefixFrom(netip.AddrFrom4(a), int(v.plen)), MaxLen: v.maxLen, ASN: v.asn})
	}
	for _, v := range t.v6 {
		var a [16]byte
		for i := range 8 {
			a[i] = byte(v.hi >> (56 - 8*i))
			a[8+i] = byte(v.lo >> (56 - 8*i))
		}
		out = append(out, VRP{Prefix: netip.PrefixFrom(netip.AddrFrom16(a), int(v.plen)), MaxLen: v.maxLen, ASN: v.asn})
	}
	return out
}

// delta collects the prefix PDUs between Cache Response and End of Data.
type delta struct {
	reset bool
	// discard drops the PDUs up to End of Data (the answer of a session
	// the client no longer follows).
	discard   bool
	baseLen   int
	ann4, wd4 []vrp4
	ann6, wd6 []vrp6
}

func (d *delta) size() int { return len(d.ann4) + len(d.ann6) }

// apply builds the table after d on base (nil base or a reset: from
// empty).
func (d *delta) apply(base *Table) *Table {
	var old4 []vrp4
	var old6 []vrp6
	if base != nil && !d.reset {
		old4, old6 = base.v4, base.v6
	}
	return &Table{v4: merge(old4, d.ann4, d.wd4, cmp4), v6: merge(old6, d.ann6, d.wd6, cmp6)}
}

// projected estimates the size of the table after d on its base. It is
// no bound: a withdrawal of an unknown record lowers it without shrinking
// the table, so the size of the applied table is checked again at End of
// Data, and buffered() bounds the memory of the delta.
func (d *delta) projected() int {
	return d.baseLen + d.size() - len(d.wd4) - len(d.wd6)
}

// buffered returns the number of prefix PDUs the delta holds.
func (d *delta) buffered() int {
	return d.size() + len(d.wd4) + len(d.wd6)
}

// merge returns old without the withdrawn entries plus the announced
// ones, sorted and without duplicates; old stays unchanged.
func merge[T any](old, ann, wd []T, compare func(a, b T) int) []T {
	slices.SortFunc(ann, compare)
	ann = slices.CompactFunc(ann, func(a, b T) bool { return compare(a, b) == 0 })
	slices.SortFunc(wd, compare)
	wd = slices.CompactFunc(wd, func(a, b T) bool { return compare(a, b) == 0 })
	out := make([]T, 0, len(old)+len(ann))
	i, j, k := 0, 0, 0
	for i < len(old) || j < len(ann) {
		var next T
		switch {
		case j >= len(ann) || (i < len(old) && compare(old[i], ann[j]) < 0):
			next = old[i]
			i++
			// Withdrawn?
			for k < len(wd) && compare(wd[k], next) < 0 {
				k++
			}
			if k < len(wd) && compare(wd[k], next) == 0 {
				continue
			}
		case i < len(old) && compare(old[i], ann[j]) == 0:
			next = ann[j]
			i++
			j++
		default:
			next = ann[j]
			j++
		}
		out = append(out, next)
	}
	return slices.Clip(out)
}

// Manager holds one RTR session per validator (host:port) for the
// collector (WP-1020 AK2): every rpki_rtr and rpki_roa probe of a
// validator shares it. A session is built on the first use, follows the
// Refresh interval and Serial Notify, rebuilds after Cache Reset or a new
// session ID and ends when no probe used the validator for two Refresh
// intervals.
type Manager struct {
	// Now replaces the clock of the data age and of the idle check
	// (tests); timers run on the wall clock.
	Now func() time.Time
	// Dial replaces the TCP dialer (tests).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	mu         sync.Mutex
	validators map[string]*validator
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// validator returns the session of addr, created on the first use.
func (m *Manager) validator(addr string) *validator {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.validators == nil {
		m.validators = map[string]*validator{}
	}
	v := m.validators[addr]
	if v == nil {
		v = newValidator(m, addr)
		m.validators[addr] = v
		go v.loop()
	}
	return v
}

// remove drops v when it is still the session of its address.
func (m *Manager) remove(v *validator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.validators != nil && m.validators[v.addr] == v {
		delete(m.validators, v.addr)
	}
}

// Sessions returns the number of validators with a session object.
func (m *Manager) Sessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.validators)
}

// Table returns the current VRP table of a validator (nil: none yet).
func (m *Manager) Table(addr string) *Table {
	m.mu.Lock()
	v := m.validators[addr]
	m.mu.Unlock()
	if v == nil {
		return nil
	}
	return v.snapshot().table
}

// Close ends every session.
func (m *Manager) Close() {
	m.mu.Lock()
	vs := make([]*validator, 0, len(m.validators))
	for _, v := range m.validators {
		vs = append(vs, v)
	}
	m.validators = nil
	m.mu.Unlock()
	for _, v := range vs {
		v.close()
	}
}

// state is a snapshot of a validator for the executors.
type state struct {
	connected bool
	table     *Table
	version   uint8
	sessionID uint16
	serial    uint32
	lastEOD   time.Time
	expire    time.Duration
	err       error
	errSeq    uint64
	changed   chan struct{}
}

// validator is the RTR session of one validator.
type validator struct {
	m    *Manager
	addr string

	mu       sync.Mutex
	st       state
	haveSess bool
	refresh  time.Duration
	retry    time.Duration
	version  uint8 // version to speak; 0 after a fallback
	lastUsed time.Time
	// closed is set under mu when the validator ends (idle or Close); a
	// probe that finds it closed takes a new one (await).
	closed   bool
	kick     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

func newValidator(m *Manager, addr string) *validator {
	v := &validator{m: m, addr: addr, version: 1, refresh: DefaultRefresh, retry: DefaultRetry,
		kick: make(chan struct{}, 1), stop: make(chan struct{})}
	v.st.expire = DefaultExpire
	v.st.changed = make(chan struct{})
	v.lastUsed = m.now()
	return v
}

func (v *validator) snapshot() state {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.st
}

// touch marks the validator as used by a probe; false: it has ended and
// the probe must take a new one.
func (v *validator) touch() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return false
	}
	v.lastUsed = v.m.now()
	return true
}

// isClosed reports whether the validator has ended.
func (v *validator) isClosed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closed
}

// endIfIdle ends the validator when no probe used it for two Refresh
// intervals (WP-1020 AK2). The decision and the closed flag are taken
// under one lock with touch, so a probe either keeps it alive or sees it
// closed.
func (v *validator) endIfIdle() bool {
	v.mu.Lock()
	idle := v.m.now().Sub(v.lastUsed) > 2*v.refresh
	if idle {
		v.closed = true
	}
	v.mu.Unlock()
	if !idle {
		return false
	}
	v.m.remove(v)
	v.close()
	v.update(func(st *state) { st.connected = false })
	return true
}

// ensure asks the loop to connect now when no session is up.
func (v *validator) ensure() {
	v.mu.Lock()
	connected := v.st.connected
	v.mu.Unlock()
	if !connected {
		select {
		case v.kick <- struct{}{}:
		default:
		}
	}
}

// update changes the state under the lock and wakes the waiting probes.
func (v *validator) update(f func(st *state)) {
	v.mu.Lock()
	f(&v.st)
	close(v.st.changed)
	v.st.changed = make(chan struct{})
	v.mu.Unlock()
}

func (v *validator) fail(err error) {
	v.update(func(st *state) {
		st.connected = false
		st.err = err
		st.errSeq++
	})
}

func (v *validator) close() {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	v.stopOnce.Do(func() { close(v.stop) })
}

// loop connects on a kick (a probe found no session) or after the Retry
// interval and runs the session until it ends.
func (v *validator) loop() {
	var retry <-chan time.Time
	for {
		select {
		case <-v.stop:
			return
		case <-v.kick:
		case <-retry:
			// Not reachable and unused: a deleted probe or a changed
			// target must not be dialled forever.
			if v.endIfIdle() {
				return
			}
		}
		for {
			done, again := v.session()
			if done {
				return
			}
			if !again {
				break
			}
		}
		v.mu.Lock()
		d := max(v.retry, minRetryWait)
		v.mu.Unlock()
		retry = time.After(d)
	}
}

// pduResult is a PDU or the error that ended reading.
type pduResult struct {
	raw []byte
	err error
}

// session runs one TCP session; done reports the end of the validator
// (stopped or idle), again a reconnect at once (version fallback).
func (v *validator) session() (done, again bool) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	dial := v.m.Dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", v.addr)
	cancel()
	if err != nil {
		v.fail(err)
		return false, false
	}
	defer conn.Close() //nolint:errcheck // the session ends
	ended := make(chan struct{})
	defer close(ended)
	v.mu.Lock()
	version := v.version
	query := resetQuery(version)
	if v.haveSess {
		query = serialQuery(version, v.st.sessionID, v.st.serial)
	}
	v.mu.Unlock()
	if _, err := conn.Write(query); err != nil {
		v.fail(err)
		return false, false
	}
	pdus := make(chan pduResult, 64)
	go func() {
		r := bufio.NewReaderSize(conn, 64<<10)
		for {
			raw, err := readPDU(r)
			select {
			case pdus <- pduResult{raw: raw, err: err}:
			case <-ended:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	v.mu.Lock()
	refreshIn := v.refresh
	v.mu.Unlock()
	refresh := time.NewTimer(refreshIn)
	defer refresh.Stop()
	var pending *delta
	for {
		select {
		case <-v.stop:
			return true, false
		case <-refresh.C:
			if v.endIfIdle() {
				// No probe used the validator for two Refresh intervals.
				return true, false
			}
			v.mu.Lock()
			refreshIn = v.refresh
			sess, serial := v.st.sessionID, v.st.serial
			v.mu.Unlock()
			if pending == nil {
				if _, err := conn.Write(serialQuery(version, sess, serial)); err != nil {
					v.fail(err)
					return false, false
				}
			}
			refresh.Reset(refreshIn)
		case r := <-pdus:
			if r.err != nil {
				if errors.Is(r.err, errCorrupt) {
					_, _ = conn.Write(errorReport(version, ErrCorruptData, r.raw))
				}
				v.fail(r.err)
				return false, false
			}
			p, err := parsePDU(r.raw)
			if err != nil {
				_, _ = conn.Write(errorReport(version, ErrCorruptData, r.raw))
				v.fail(err)
				return false, false
			}
			next, end := v.handle(conn, version, &p, pending)
			if end != nil {
				if errors.Is(end, errFallback) {
					// Reconnect at once with version 0 (RFC 6810).
					return false, true
				}
				v.fail(end)
				return false, false
			}
			pending = next
			if p.typ == TypeEndOfData {
				v.mu.Lock()
				refreshIn = v.refresh
				v.mu.Unlock()
				if !refresh.Stop() {
					select {
					case <-refresh.C:
					default:
					}
				}
				refresh.Reset(refreshIn)
			}
		}
	}
}

// errFallback asks the session to reconnect with version 0.
var errFallback = errors.New("rpki: cache speaks version 0")

// handle processes one PDU; it returns the pending delta and an error that
// ends the session.
func (v *validator) handle(conn net.Conn, version uint8, p *pdu, pending *delta) (*delta, error) {
	switch p.typ {
	case TypeSerialNotify:
		if pending == nil {
			v.mu.Lock()
			sess, serial := v.st.sessionID, v.st.serial
			v.mu.Unlock()
			if _, err := conn.Write(serialQuery(version, sess, serial)); err != nil {
				return nil, err
			}
		}
		return pending, nil
	case TypeCacheResponse:
		v.mu.Lock()
		changed := v.haveSess && p.session != v.st.sessionID
		reset := !v.haveSess
		base := 0
		if !reset {
			a, b := v.st.table.Len()
			base = a + b
		}
		if changed {
			v.haveSess = false
		}
		v.mu.Unlock()
		if changed {
			// A new session ID: the cache restarted; its answer is dropped
			// and the table rebuilt from scratch.
			if _, err := conn.Write(resetQuery(version)); err != nil {
				return nil, err
			}
			return &delta{discard: true}, nil
		}
		return &delta{reset: reset, baseLen: base}, nil
	case TypeIPv4Prefix, TypeIPv6Prefix:
		if pending == nil {
			return nil, errCorrupt
		}
		if pending.discard {
			return pending, nil
		}
		pending.add(p)
		// At most MaxVRPs announcements beyond the base and withdrawals of
		// at most the base: a flood of announcements and withdrawals
		// without net growth must not grow the buffer without bound.
		if pending.projected() > MaxVRPs || pending.buffered() > MaxVRPs+2*pending.baseLen {
			// The incomplete state is discarded; the old table stays.
			return nil, errVRPLimit
		}
		return pending, nil
	case TypeEndOfData:
		if pending == nil {
			return nil, errCorrupt
		}
		if pending.discard {
			return nil, nil
		}
		v.mu.Lock()
		base := v.st.table
		v.mu.Unlock()
		table := pending.apply(base)
		if a, b := table.Len(); a+b > MaxVRPs {
			// Withdrawals of unknown records hid the growth from
			// projected(); the old table stays.
			return nil, errVRPLimit
		}
		refresh, retry, expire := DefaultRefresh, DefaultRetry, DefaultExpire
		if p.hasTimers {
			refresh = clampInterval(p.refresh, minRefresh, maxRefresh)
			retry = clampInterval(p.retry, minRetry, maxRetry)
			expire = clampInterval(p.expire, minExpire, maxExpire)
		}
		now := v.m.now()
		v.mu.Lock()
		v.haveSess = true
		v.refresh, v.retry = refresh, retry
		v.mu.Unlock()
		v.update(func(st *state) {
			st.connected = true
			st.table = table
			st.version = version
			st.sessionID = p.session
			st.serial = p.serial
			st.lastEOD = now
			st.expire = expire
			st.err = nil
		})
		return nil, nil
	case TypeCacheReset:
		v.mu.Lock()
		v.haveSess = false
		v.mu.Unlock()
		if _, err := conn.Write(resetQuery(version)); err != nil {
			return nil, err
		}
		return nil, nil
	case TypeRouterKey:
		return pending, nil // BGPsec router keys are not used
	case TypeErrorReport:
		if p.errorCode == ErrUnsupportedVersion && p.version == 0 && version > 0 {
			v.mu.Lock()
			v.version = 0
			v.mu.Unlock()
			return nil, errFallback
		}
		return nil, &ReportError{Code: p.errorCode}
	}
	return nil, errCorrupt
}

// add records a prefix PDU.
func (d *delta) add(p *pdu) {
	announce := p.flags&1 == 1
	if p.prefix.Is4() {
		a := p.prefix.As4()
		e := vrp4{addr: uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3]), asn: p.asn, plen: p.plen, maxLen: p.maxLen}
		if announce {
			d.ann4 = append(d.ann4, e)
		} else {
			d.wd4 = append(d.wd4, e)
		}
		return
	}
	a := p.prefix.As16()
	var hi, lo uint64
	for i := range 8 {
		hi = hi<<8 | uint64(a[i])
		lo = lo<<8 | uint64(a[8+i])
	}
	e := vrp6{hi: hi, lo: lo, asn: p.asn, plen: p.plen, maxLen: p.maxLen}
	if announce {
		d.ann6 = append(d.ann6, e)
	} else {
		d.wd6 = append(d.wd6, e)
	}
}
