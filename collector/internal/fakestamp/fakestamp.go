// Package fakestamp is a STAMP Session-Reflector for the tests of the
// stamp probe (WP-1013, PRT-14, PRT-21): unauthenticated mode after
// RFC 8762 section 4.3.1 on UDP, with its own packet codec (independent of
// collector/probe). It is test infrastructure like collector/internal/
// fakedhcp; the release build does not use it.
//
// Knobs (all changeable while the reflector runs): a delay before every
// answer (counted as path delay: T2 and T3 are taken on receipt), a loss
// rate, reordering (every n-th answer is held back and sent after the
// next one), duplicates (every n-th answer is sent twice), the S bit of
// the Error Estimate and silence.
package fakestamp

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"
)

const packetLen = 44

// ntpEpochOffset is the number of seconds from 1900 to 1970.
const ntpEpochOffset = 2208988800

func ntp(t time.Time) uint64 {
	secs := uint64(t.Unix() + ntpEpochOffset)                    //nolint:gosec // after 1900
	frac := (uint64(t.Nanosecond()) << 32) / uint64(time.Second) //nolint:gosec // below one second
	return secs<<32 | frac
}

// Reflector is a running fake reflector.
type Reflector struct {
	conn *net.UDPConn

	mu        sync.Mutex
	delay     time.Duration
	loss      float64
	reorderN  int
	dupN      int
	synced    bool
	silent    bool
	received  int
	answered  int
	seq       uint32
	held      *pending
	closed    bool
	wg        sync.WaitGroup
	serveDone chan struct{}
}

type pending struct {
	b  []byte
	to netip.AddrPort
}

// Listen starts a reflector on addr (e.g. "127.0.0.1:0").
func Listen(addr string) (*Reflector, error) {
	udp, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udp)
	if err != nil {
		return nil, err
	}
	r := &Reflector{conn: conn, serveDone: make(chan struct{})}
	go r.serve()
	return r, nil
}

// Addr is the address the reflector listens on.
func (r *Reflector) Addr() *net.UDPAddr { return r.conn.LocalAddr().(*net.UDPAddr) }

// Close stops the reflector and waits for delayed answers.
func (r *Reflector) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	err := r.conn.Close()
	<-r.serveDone
	r.wg.Wait()
	return err
}

// SetDelay delays every answer by d.
func (r *Reflector) SetDelay(d time.Duration) { r.mu.Lock(); r.delay = d; r.mu.Unlock() }

// SetLoss drops the given fraction (0 to 1) of the test packets,
// deterministically spread over the received packets.
func (r *Reflector) SetLoss(rate float64) { r.mu.Lock(); r.loss = rate; r.mu.Unlock() }

// SetReorder holds back every n-th answer and sends it after the next one
// (0: off).
func (r *Reflector) SetReorder(n int) { r.mu.Lock(); r.reorderN = n; r.mu.Unlock() }

// SetDuplicate sends every n-th answer twice (0: off).
func (r *Reflector) SetDuplicate(n int) { r.mu.Lock(); r.dupN = n; r.mu.Unlock() }

// SetSynchronized sets the S bit of the reflector's Error Estimate.
func (r *Reflector) SetSynchronized(s bool) { r.mu.Lock(); r.synced = s; r.mu.Unlock() }

// SetSilent switches every answer off (true) or on.
func (r *Reflector) SetSilent(s bool) { r.mu.Lock(); r.silent = s; r.mu.Unlock() }

// Received is the number of test packets received.
func (r *Reflector) Received() int { r.mu.Lock(); defer r.mu.Unlock(); return r.received }

func (r *Reflector) serve() {
	defer close(r.serveDone)
	buf := make([]byte, 2048)
	for {
		n, peer, err := r.conn.ReadFromUDPAddrPort(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			continue
		}
		t2 := time.Now()
		if n < packetLen {
			continue
		}
		r.mu.Lock()
		idx := r.received
		r.received++
		// Deterministic loss: packet idx is dropped when the running
		// total of rate x packets crosses an integer.
		lost := math.Floor(float64(idx+1)*r.loss) > math.Floor(float64(idx)*r.loss)
		if r.silent || lost || r.closed {
			r.mu.Unlock()
			continue
		}
		out := make([]byte, n)
		binary.BigEndian.PutUint32(out[0:4], r.seq)
		r.seq++
		est := uint16(1)
		if r.synced {
			est |= 0x8000
		}
		t3 := time.Now()
		binary.BigEndian.PutUint64(out[4:12], ntp(t3))
		binary.BigEndian.PutUint16(out[12:14], est)
		binary.BigEndian.PutUint64(out[16:24], ntp(t2))
		copy(out[24:36], buf[0:12])  // sender sequence number and timestamp
		copy(out[36:38], buf[12:14]) // sender error estimate
		out[40] = 64                 // sender TTL (not measured)
		r.answered++
		k := r.answered
		var sends []pending
		cur := pending{b: out, to: peer}
		switch {
		case r.reorderN > 0 && k%r.reorderN == 0 && r.held == nil:
			r.held = &cur
		default:
			sends = append(sends, cur)
			if r.dupN > 0 && k%r.dupN == 0 {
				sends = append(sends, cur)
			}
			if r.held != nil {
				sends = append(sends, *r.held)
				r.held = nil
			}
		}
		delay := r.delay
		if len(sends) > 0 {
			r.wg.Add(1)
		}
		r.mu.Unlock()
		if len(sends) == 0 {
			continue
		}
		go func() {
			defer r.wg.Done()
			if delay > 0 {
				time.Sleep(delay)
			}
			for _, s := range sends {
				_, _ = r.conn.WriteToUDPAddrPort(s.b, s.to)
			}
		}()
	}
}
