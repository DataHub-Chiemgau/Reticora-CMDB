package snmp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

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

// Poll fetches every configured metric from the target once (GET, at
// most MaxOids per request) and returns the samples. The values come from
// the decoded varbinds: what the device does not answer (noSuchObject,
// noSuchInstance) or answers non-numerically is skipped, so one bad OID
// does not fail the whole poll cycle. SNMPv3 credentials select an
// authPriv session, otherwise v2c with the community. An unreachable
// target is an error.
func (p *Plugin) Poll(ctx context.Context, target string, creds map[string]string, metrics []PollMetric) ([]plugins.MetricSample, error) {
	var samples []plugins.MetricSample
	err := p.request(ctx, target, creds, func(g *gosnmp.GoSNMP) error {
		// v3Request may run this twice (time window resync).
		samples = make([]plugins.MetricSample, 0, len(metrics))
		for start := 0; start < len(metrics); start += g.MaxOids {
			chunk := metrics[start:min(start+g.MaxOids, len(metrics))]
			oids := make([]string, len(chunk))
			for i, m := range chunk {
				oids[i] = m.OID
			}
			pkt, err := g.Get(oids)
			if err != nil {
				return err
			}
			values := map[string]gosnmp.SnmpPDU{}
			for _, v := range pkt.Variables {
				values[strings.TrimPrefix(v.Name, ".")] = v
			}
			for _, m := range chunk {
				value, ok := numericValue(values[strings.TrimPrefix(m.OID, ".")])
				if !ok {
					continue
				}
				labels := map[string]string{"target": target, "oid": m.OID}
				for k, v := range m.Labels {
					labels[k] = v
				}
				samples = append(samples, plugins.MetricSample{Name: m.Name, Labels: labels, Value: value})
			}
		}
		return nil
	})
	return samples, err
}

// numericValue renders a varbind value as a number: INTEGER, the
// application unsigned types (Counter32, Gauge32, Counter64, ...), the
// Opaque floats and printable strings that parse as floats.
func numericValue(v gosnmp.SnmpPDU) (float64, bool) {
	switch v.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.Counter64, gosnmp.Uinteger32, gosnmp.TimeTicks:
		f, _ := gosnmp.ToBigInt(v.Value).Float64()
		return f, true
	case gosnmp.OpaqueFloat:
		f, ok := v.Value.(float32)
		return float64(f), ok
	case gosnmp.OpaqueDouble:
		f, ok := v.Value.(float64)
		return f, ok
	case gosnmp.OctetString:
		if b, ok := v.Value.([]byte); ok && isPrintable(string(b)) {
			f, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			return f, err == nil
		}
	}
	return 0, false
}

// Interface counters of the standard poll (MON-02): ifOperStatus of the
// ifTable and the 64-bit octet counters and ifName of the ifXTable; a
// device without ifXTable falls back to the 32-bit ifInOctets/ifOutOctets.
const (
	oidIfInOctets    = "1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets   = "1.3.6.1.2.1.2.2.1.16"
	oidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"

	// MaxPollInterfaces bounds the interfaces polled per device.
	MaxPollInterfaces = 256
	// pollRepetitions is the max-repetitions of the GETBULK requests of
	// the walk: a column of 256 interfaces takes ceil(256/50) = 6.
	pollRepetitions = 50
)

// Metric names of the interface poll.
const (
	MetricIfInOctets  = "if_in_octets"
	MetricIfOutOctets = "if_out_octets"
	MetricIfOperState = "if_oper_status"
)

// errWalkLimit ends a column walk at its row limit.
var errWalkLimit = errors.New("snmp: walk limit reached")

// PollInterfaces walks the interface counters of target: per interface
// (at most MaxPollInterfaces, the first by ifIndex) if_in_octets and
// if_out_octets (ifHCInOctets/ifHCOutOctets, without ifXTable the 32-bit
// counters) and if_oper_status, labelled target, ifIndex and ifName when
// the device names the interface. SNMPv3 credentials select an authPriv
// session, otherwise v2c with the community. A device without ifTable
// yields no samples; an unreachable one an error.
func (p *Plugin) PollInterfaces(ctx context.Context, target string, creds map[string]string) ([]plugins.MetricSample, error) {
	var samples []plugins.MetricSample
	err := p.request(ctx, target, creds, func(g *gosnmp.GoSNMP) error {
		samples = nil // v3Request may run this twice (time window resync).
		g.MaxRepetitions = pollRepetitions
		status, err := walkTable(g, oidIfOperSt, MaxPollInterfaces, 0)
		if err != nil || len(status.order) == 0 {
			return err
		}
		last := status.order[len(status.order)-1]
		names, _ := walkTable(g, oidIfName, MaxPollInterfaces, last)
		in, _ := walkTable(g, oidIfHCInOctets, MaxPollInterfaces, last)
		out, _ := walkTable(g, oidIfHCOutOctets, MaxPollInterfaces, last)
		if len(in.order) == 0 && len(out.order) == 0 {
			in, _ = walkTable(g, oidIfInOctets, MaxPollInterfaces, last)
			out, _ = walkTable(g, oidIfOutOctets, MaxPollInterfaces, last)
		}
		for _, idx := range status.order {
			labels := func() map[string]string {
				l := map[string]string{"target": target, "ifIndex": strconv.Itoa(idx)}
				if name := names.text(idx); name != "" {
					l["ifName"] = name
				}
				return l
			}
			for _, c := range []struct {
				name  string
				table table
			}{{MetricIfInOctets, in}, {MetricIfOutOctets, out}, {MetricIfOperState, status}} {
				if v, ok := c.table.number(idx); ok {
					samples = append(samples, plugins.MetricSample{Name: c.name, Value: v, Labels: labels()})
				}
			}
		}
		return nil
	})
	return samples, err
}

// request runs do on a session to target: SNMPv3 authPriv when creds
// carry a v3 account, otherwise v2c with the community.
func (p *Plugin) request(ctx context.Context, target string, creds map[string]string, do func(g *gosnmp.GoSNMP) error) error {
	v3, useV3, err := v3FromCreds(creds)
	if err != nil {
		return err
	}
	if useV3 {
		return p.v3Request(ctx, target, v3, do)
	}
	community := creds["community"]
	if community == "" {
		community = "public"
	}
	g := p.v2cSession(ctx, target, community)
	if err := g.Connect(); err != nil {
		return fmt.Errorf("snmp: dial %s: %w", target, err)
	}
	defer g.Conn.Close()
	return do(g)
}

// table is a walked column by ifIndex, in walk order.
type table struct {
	order  []int
	values map[int]gosnmp.SnmpPDU
}

func (t table) number(idx int) (float64, bool) {
	v, ok := t.values[idx]
	if !ok {
		return 0, false
	}
	return numericValue(v)
}

func (t table) text(idx int) string {
	if b, ok := t.values[idx].Value.([]byte); ok {
		return strings.TrimSpace(plugins.OctetText(b))
	}
	return ""
}

// walkTable walks a column with a single-number index (ifIndex) and keeps
// at most limit rows; with last > 0 it stops after the row last, so the
// columns of a table cover the rows of its first column. An error ends
// the walk, the rows read so far stay.
func walkTable(g *gosnmp.GoSNMP, column string, limit, last int) (table, error) {
	t := table{values: map[int]gosnmp.SnmpPDU{}}
	walk := g.BulkWalk
	if g.Version == gosnmp.Version1 {
		walk = g.Walk
	}
	err := walk(column, func(v gosnmp.SnmpPDU) error {
		rest, ok := strings.CutPrefix(strings.TrimPrefix(v.Name, "."), column+".")
		if !ok {
			return nil
		}
		idx, err := strconv.Atoi(rest)
		if err != nil {
			return nil
		}
		if last > 0 && idx > last {
			return errWalkLimit
		}
		if _, seen := t.values[idx]; !seen {
			t.order = append(t.order, idx)
		}
		t.values[idx] = v
		if len(t.order) >= limit {
			return errWalkLimit
		}
		return nil
	})
	if errors.Is(err, errWalkLimit) {
		err = nil
	}
	return t, err
}
