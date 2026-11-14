package probe

import (
	"encoding/binary"
	"errors"
	"time"
)

// STAMP packet format (RFC 8762, unauthenticated mode). The test packet of
// the Session-Sender (section 4.2.1) and the packet of the
// Session-Reflector (section 4.3.1) are both 44 bytes; the sender pads its
// packet with zeros up to the configured size, a reflector answers with
// the same length (TWAMP-Light reflectors use the same layout, RFC 5357
// section 4.2.1).
const (
	// StampPacketLen is the length of both unauthenticated packets.
	StampPacketLen = 44
	// StampMaxPacket is the largest test packet (an IPv4 UDP payload
	// within an Ethernet MTU of 1500 bytes).
	StampMaxPacket = 1472
)

// Error Estimate (RFC 4656 section 4.1.2, used by RFC 8762): S bit
// (synchronized to UTC), Z bit (0: NTP format), 6 bits scale, 8 bits
// multiplier.
const stampErrS = 0x8000

// ntpEpochOffset is the number of seconds from 1900-01-01 (NTP) to
// 1970-01-01 (Unix).
const ntpEpochOffset = 2208988800

// NTPTime is a 64-bit NTP timestamp: seconds since 1900 and a binary
// fraction of a second.
type NTPTime uint64

// ToNTP converts t to an NTP timestamp.
func ToNTP(t time.Time) NTPTime {
	secs := uint64(t.Unix() + ntpEpochOffset)                    //nolint:gosec // after 1900
	frac := (uint64(t.Nanosecond()) << 32) / uint64(time.Second) //nolint:gosec // below one second
	return NTPTime(secs<<32 | frac)
}

// Time converts the timestamp back (nanosecond resolution).
func (n NTPTime) Time() time.Time {
	secs := int64(n>>32) - ntpEpochOffset //nolint:gosec // 32 bits
	nanos := (uint64(n&0xffffffff) * uint64(time.Second)) >> 32
	return time.Unix(secs, int64(nanos)).UTC() //nolint:gosec // below one second
}

// StampErrorEstimate builds an Error Estimate: synchronized sets the S
// bit, the multiplier is 1 (it must not be 0).
func StampErrorEstimate(synchronized bool) uint16 {
	v := uint16(1)
	if synchronized {
		v |= stampErrS
	}
	return v
}

// StampSynchronized reports the S bit of an Error Estimate.
func StampSynchronized(errEstimate uint16) bool { return errEstimate&stampErrS != 0 }

// StampTestPacket is the packet of the Session-Sender.
type StampTestPacket struct {
	Seq         uint32
	Timestamp   NTPTime
	ErrEstimate uint16
}

// Encode writes the packet padded with zeros to size bytes (44 to 1472).
func (p *StampTestPacket) Encode(size int) ([]byte, error) {
	if size < StampPacketLen || size > StampMaxPacket {
		return nil, errors.New("stamp: packet size must be between 44 and 1472 bytes")
	}
	b := make([]byte, size)
	binary.BigEndian.PutUint32(b[0:4], p.Seq)
	binary.BigEndian.PutUint64(b[4:12], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[12:14], p.ErrEstimate)
	return b, nil
}

// DecodeStampTestPacket reads a sender packet.
func DecodeStampTestPacket(b []byte) (StampTestPacket, error) {
	if len(b) < StampPacketLen {
		return StampTestPacket{}, errors.New("stamp: test packet shorter than 44 bytes")
	}
	return StampTestPacket{
		Seq:         binary.BigEndian.Uint32(b[0:4]),
		Timestamp:   NTPTime(binary.BigEndian.Uint64(b[4:12])),
		ErrEstimate: binary.BigEndian.Uint16(b[12:14]),
	}, nil
}

// StampReflectedPacket is the packet of the Session-Reflector: its own
// sequence number, the transmit timestamp T3, the receive timestamp T2 and
// the copied sender fields (sequence number, T1, Error Estimate, TTL).
type StampReflectedPacket struct {
	Seq               uint32
	Timestamp         NTPTime // T3
	ErrEstimate       uint16
	ReceiveTimestamp  NTPTime // T2
	SenderSeq         uint32
	SenderTimestamp   NTPTime // T1
	SenderErrEstimate uint16
	SenderTTL         uint8
}

// Encode writes the reflector packet padded with zeros to size bytes.
func (p *StampReflectedPacket) Encode(size int) []byte {
	b := make([]byte, max(size, StampPacketLen))
	binary.BigEndian.PutUint32(b[0:4], p.Seq)
	binary.BigEndian.PutUint64(b[4:12], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[12:14], p.ErrEstimate)
	binary.BigEndian.PutUint64(b[16:24], uint64(p.ReceiveTimestamp))
	binary.BigEndian.PutUint32(b[24:28], p.SenderSeq)
	binary.BigEndian.PutUint64(b[28:36], uint64(p.SenderTimestamp))
	binary.BigEndian.PutUint16(b[36:38], p.SenderErrEstimate)
	b[40] = p.SenderTTL
	return b
}

// DecodeStampReflectedPacket reads a reflector packet.
func DecodeStampReflectedPacket(b []byte) (StampReflectedPacket, error) {
	if len(b) < StampPacketLen {
		return StampReflectedPacket{}, errors.New("stamp: reflected packet shorter than 44 bytes")
	}
	return StampReflectedPacket{
		Seq:               binary.BigEndian.Uint32(b[0:4]),
		Timestamp:         NTPTime(binary.BigEndian.Uint64(b[4:12])),
		ErrEstimate:       binary.BigEndian.Uint16(b[12:14]),
		ReceiveTimestamp:  NTPTime(binary.BigEndian.Uint64(b[16:24])),
		SenderSeq:         binary.BigEndian.Uint32(b[24:28]),
		SenderTimestamp:   NTPTime(binary.BigEndian.Uint64(b[28:36])),
		SenderErrEstimate: binary.BigEndian.Uint16(b[36:38]),
		SenderTTL:         b[40],
	}, nil
}
