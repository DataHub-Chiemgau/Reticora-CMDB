package fakestamp

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func start(t *testing.T) (*Reflector, *net.UDPConn) {
	t.Helper()
	r, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Skipf("fakestamp: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	c, err := net.DialUDP("udp", nil, r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return r, c
}

func send(t *testing.T, c *net.UDPConn, seq uint32, size int) {
	t.Helper()
	b := make([]byte, size)
	binary.BigEndian.PutUint32(b[0:4], seq)
	binary.BigEndian.PutUint64(b[4:12], ntp(time.Now()))
	binary.BigEndian.PutUint16(b[12:14], 1)
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

// recv reads answers until the deadline and returns their sender
// sequence numbers in arrival order.
func recv(t *testing.T, c *net.UDPConn, wait time.Duration) (seqs []uint32, last []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 2048)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return seqs, last
		}
		last = append([]byte(nil), buf[:n]...)
		seqs = append(seqs, binary.BigEndian.Uint32(buf[24:28]))
	}
}

// TestFakeStampReflects: an answer of the same length with the copied
// sender fields, T2 and T3 set, S bit off by default.
func TestFakeStampReflects(t *testing.T) {
	_, c := start(t)
	send(t, c, 7, 100)
	seqs, b := recv(t, c, 300*time.Millisecond)
	if len(seqs) != 1 || seqs[0] != 7 || len(b) != 100 {
		t.Fatalf("answers %v, len %d", seqs, len(b))
	}
	if binary.BigEndian.Uint64(b[4:12]) == 0 || binary.BigEndian.Uint64(b[16:24]) == 0 {
		t.Fatal("T2 or T3 missing")
	}
	if binary.BigEndian.Uint16(b[12:14])&0x8000 != 0 {
		t.Fatal("S bit set by default")
	}
}

// TestFakeStampKnobs: delay, loss, reordering, duplicates, S bit, silence.
func TestFakeStampKnobs(t *testing.T) {
	r, c := start(t)
	r.SetDelay(30 * time.Millisecond)
	r.SetSynchronized(true)
	begin := time.Now()
	send(t, c, 1, 44)
	_, b := recv(t, c, 200*time.Millisecond)
	if b == nil || binary.BigEndian.Uint16(b[12:14])&0x8000 == 0 {
		t.Fatal("no answer with S bit")
	}
	if time.Since(begin) < 30*time.Millisecond {
		t.Fatal("delay ignored")
	}
	r.SetDelay(0)

	r.SetLoss(0.5)
	for i := uint32(0); i < 10; i++ {
		send(t, c, 100+i, 44)
	}
	if seqs, _ := recv(t, c, 200*time.Millisecond); len(seqs) != 5 {
		t.Fatalf("loss 0.5: %d answers, want 5", len(seqs))
	}
	r.SetLoss(0)

	r.SetReorder(2)
	r.SetDuplicate(3)
	for i := uint32(0); i < 4; i++ {
		send(t, c, 200+i, 44)
		time.Sleep(5 * time.Millisecond)
	}
	seqs, _ := recv(t, c, 200*time.Millisecond)
	reordered, dup := false, map[uint32]int{}
	for i, s := range seqs {
		dup[s]++
		if i > 0 && s < seqs[i-1] {
			reordered = true
		}
	}
	twice := false
	for _, n := range dup {
		twice = twice || n > 1
	}
	if !reordered || !twice {
		t.Fatalf("answers %v: reordered %v, duplicate %v", seqs, reordered, twice)
	}
	r.SetReorder(0)
	r.SetDuplicate(0)

	r.SetSilent(true)
	send(t, c, 300, 44)
	if seqs, _ := recv(t, c, 100*time.Millisecond); len(seqs) != 0 {
		t.Fatalf("silent reflector answered %v", seqs)
	}
}
