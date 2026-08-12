package snmp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// PollMetric describes one numeric OID the collector polls periodically
// (Epic E: Collector-Polling). The resulting sample carries the configured
// metric name so backend alert rules can target it directly.
type PollMetric struct {
	// Name is the metric name, e.g. "ifInOctets".
	Name string
	// OID is the numeric OID to GET, e.g. "1.3.6.1.2.1.2.2.1.10.1".
	OID string
	// Labels are attached to every sample of this metric.
	Labels map[string]string
}

// Poll fetches every configured metric from the target once and returns the
// samples. Unreadable/non-numeric values are skipped so one bad OID does not
// fail the whole poll cycle.
func (p *Plugin) Poll(ctx context.Context, target string, creds map[string]string, metrics []PollMetric) ([]plugins.MetricSample, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	community := creds["community"]
	if community == "" {
		community = "public"
	}

	addr := net.JoinHostPort(target, fmt.Sprintf("%d", defaultPort))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("snmp: dial %s: %w", target, err)
	}
	defer conn.Close()

	samples := make([]plugins.MetricSample, 0, len(metrics))
	for _, m := range metrics {
		if ctx.Err() != nil {
			return samples, ctx.Err()
		}
		value, ok := p.snmpGetNumeric(conn, community, m.OID, timeout)
		if !ok {
			continue
		}
		labels := map[string]string{"target": target, "oid": m.OID}
		for k, v := range m.Labels {
			labels[k] = v
		}
		samples = append(samples, plugins.MetricSample{Name: m.Name, Labels: labels, Value: value})
	}
	return samples, nil
}

// snmpGetNumeric performs a single SNMP GET and decodes the varbind value as
// a number. It accepts INTEGER and the application unsigned types (Counter32,
// Gauge32, Counter64, ...) via the shared BER parser, plus printable strings
// that parse as floats.
func (p *Plugin) snmpGetNumeric(conn net.Conn, community, oid string, timeout time.Duration) (float64, bool) {
	pkt := buildSNMPv2cGet(community, oid)
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, false
	}
	if _, err := conn.Write(pkt); err != nil {
		return 0, false
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return 0, false
	}
	return extractNumericValue(buf[:n])
}

// extractNumericValue finds the response varbind value and renders it as a
// float. It walks every TLV tag in the packet (offsets shift by two for the
// tag and the length byte) and decodes the LAST numeric value — the response
// varbind — while header INTEGERs (version, request-id, error status) are
// skipped. INTEGER and the application unsigned types are decoded directly,
// octet strings via float parsing.
func extractNumericValue(data []byte) (float64, bool) {
	var (
		found bool
		out   float64
	)
	for i := 0; i+1 < len(data); i++ {
		tag := data[i]
		length, hdr, ok := berLengthAt(data, i)
		if !ok || i+hdr+length > len(data) {
			continue
		}
		value := data[i+hdr : i+hdr+length]
		switch tag {
		case 0x41, 0x42, 0x43, 0x46, 0x47: // application unsigned (Counter32, Gauge32, ...)
			if f, err := strconv.ParseFloat(berIntString(value), 64); err == nil {
				out, found = f, true
			}
		case 0x02: // INTEGER: last one wins (header fields come first)
			if f, err := strconv.ParseFloat(berIntString(value), 64); err == nil {
				out, found = f, true
			}
		case 0x04: // OctetString
			if isPrintable(string(value)) {
				if f, err := strconv.ParseFloat(strings.TrimSpace(string(value)), 64); err == nil {
					out, found = f, true
				}
			}
		}
	}
	return out, found
}

// berLengthAt decodes the length field following the tag at offset i and
// returns value length and total header size (tag + length bytes).
func berLengthAt(data []byte, i int) (length, header int, ok bool) {
	if i+1 >= len(data) {
		return 0, 0, false
	}
	first := data[i+1]
	if first&0x80 == 0 {
		return int(first), 2, true
	}
	numBytes := int(first & 0x7f)
	if numBytes > 4 || i+2+numBytes > len(data) {
		return 0, 0, false
	}
	for j := 0; j < numBytes; j++ {
		length = length<<8 | int(data[i+2+j])
	}
	return length, 2 + numBytes, true
}
