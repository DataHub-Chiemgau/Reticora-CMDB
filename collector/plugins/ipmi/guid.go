package ipmi

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// Get System GUID (IPMI App command 0x37) is answered outside a session
// (IPMI 2.0 §22.14); it gives the hardware UUID of the managed server
// (DeviceRecord identity, DIS-04) without credentials.

const (
	netFnApp         = 0x06
	cmdGetSystemGUID = 0x37
	bmcAddress       = 0x20
	remoteAddress    = 0x81
)

// buildRequest frames a session-less IPMI v1.5 request with checksums.
func buildRequest(netFn, cmd byte, data []byte) []byte {
	msg := []byte{bmcAddress, netFn << 2}
	msg = append(msg, checksum(msg...))
	body := append([]byte{remoteAddress, 0x00, cmd}, data...)
	msg = append(msg, body...)
	msg = append(msg, checksum(body...))

	out := []byte{rmcpVersion, 0x00, rmcpSeqNo, 0x07, // RMCP header, class IPMI
		0x00,                   // auth type none
		0x00, 0x00, 0x00, 0x00, // session sequence
		0x00, 0x00, 0x00, 0x00, // session id
		byte(len(msg))}
	return append(out, msg...)
}

func checksum(b ...byte) byte {
	var sum byte
	for _, v := range b {
		sum += v
	}
	return -sum
}

// parseResponse returns the data of a session-less response to cmd.
func parseResponse(raw []byte, cmd byte) ([]byte, error) {
	const header = 14 // RMCP (4) + session header (10)
	if len(raw) < header+8 || raw[0] != rmcpVersion || raw[3]&0x7f != 0x07 {
		return nil, errors.New("ipmi: not an IPMI response")
	}
	msg := raw[header:]
	if int(raw[header-1]) < len(msg) {
		msg = msg[:raw[header-1]]
	}
	if len(msg) < 8 || checksum(msg[:2]...) != msg[2] || checksum(msg[3:len(msg)-1]...) != msg[len(msg)-1] {
		return nil, errors.New("ipmi: bad checksum")
	}
	if msg[5] != cmd {
		return nil, fmt.Errorf("ipmi: response to command %#x, want %#x", msg[5], cmd)
	}
	if msg[6] != 0x00 {
		return nil, fmt.Errorf("ipmi: completion code %#x", msg[6])
	}
	return msg[7 : len(msg)-1], nil
}

// systemGUID asks the BMC for the system GUID and formats it as UUID
// (SMBIOS byte order: the first three fields little-endian).
func (p *Plugin) systemGUID(target string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(target, fmt.Sprint(p.port())), timeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if derr := conn.SetDeadline(time.Now().Add(timeout)); derr != nil {
		return "", derr
	}
	if _, err = conn.Write(buildRequest(netFnApp, cmdGetSystemGUID, nil)); err != nil {
		return "", err
	}
	buf := make([]byte, 512)
	for {
		n, rerr := conn.Read(buf)
		if rerr != nil {
			return "", rerr
		}
		if n >= 4 && buf[3] == rmcpClassASF {
			continue // a late presence pong
		}
		data, perr := parseResponse(buf[:n], cmdGetSystemGUID)
		if perr != nil {
			return "", perr
		}
		if len(data) < 16 {
			return "", errors.New("ipmi: short GUID")
		}
		return formatGUID(data[:16]), nil
	}
}

func formatGUID(g []byte) string {
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		g[3], g[2], g[1], g[0], g[5], g[4], g[7], g[6], g[8], g[9], g[10], g[11], g[12], g[13], g[14], g[15])
}

func (p *Plugin) port() int {
	if p.Port > 0 {
		return p.Port
	}
	return defaultPort
}
