package probe

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypePON is the register key of the OLT/PON/ONU probe (PRT-11, WP-1007).
const TypePON = "pon"

// PONMaxONUs is the most ONUs a check reads per OLT (max_rows of the type,
// PRT-11); more truncate the walk (probe_pon_truncated = 1).
const PONMaxONUs = 8192

// Value names and labels of a pon result (E-188, option 1): per port the
// counts and the receive level statistics, per ONU only state changes and,
// on a violation or in the first run of a UTC hour, its receive level.
const (
	ValuePONOnline       = "probe_pon_onu_online"
	ValuePONOffline      = "probe_pon_onu_offline"
	ValuePONRx           = "probe_pon_rx_dbm"
	ValuePONStatusChange = "probe_pon_onu_status_change"
	ValuePONONURx        = "probe_pon_onu_rx_dbm"
	ValuePONTruncated    = "probe_pon_truncated"
	ValuePONPowerEvent   = "probe_pon_power_event"
	LabelPONPort         = "port"
	LabelPONStat         = "stat"
	LabelPONSN           = "sn"
	LabelPONFrom         = "from"
	LabelPONTo           = "to"
	LabelPONCause        = "cause"
	// LabelPONSide is olt on the receive level statistics measured at the
	// OLT (rx_olt, upstream); the statistics without it are measured at
	// the ONUs (rx_onu, downstream). The two never mix: their levels
	// differ systematically by several dB.
	LabelPONSide = "side"
	ponSideOLT   = "olt"
)

// Defaults of the pon parameters (PRT-11, api/probe-types.json).
const (
	ponDefaultRxWarn        = -27.0
	ponDefaultOfflineWarn   = 0.1
	ponDefaultPowerEventMin = 8
)

// ponSeriesHour is the series of the UTC hour of the last run; a new hour
// reports the receive level of every ONU once.
const ponSeriesHour = "pon:hour"

// ponNow is the clock of the executor (tests replace it).
var ponNow = time.Now

func init() { Register(TypePON, &PONExecutor{}) }

// PONExecutor is the probe type pon (PRT-11, WP-1007): the generic OLT →
// PON port → ONU core over the monitor section pon of the vendor profile
// (WP-1000, profiles in WP-1008 and WP-1009). Without a profile the result
// is error/unsupported (unknown). The OLT is fail only when it does not
// answer (fail/timeout). A port with a share of offline ONUs above
// port_offline_warn is ok with threshold_exceeded (degraded through
// WP-952). At least power_event_min ONUs going offline with the cause
// dying_gasp in one run mark probe_pon_power_event = 1. The previous state
// per ONU lies in the previous-value store of the engine context (WP-994).
type PONExecutor struct{}

type ponParams struct {
	Ports           []string `json:"ports"`
	ONURxWarnDBm    *float64 `json:"onu_rx_warn_dbm"`
	PortOfflineWarn *float64 `json:"port_offline_warn"`
	PowerEventMin   *int     `json:"power_event_min"`
}

// Execute implements Executor.
func (e *PONExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p ponParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	rxWarn, offlineWarn, powerMin := ponDefaultRxWarn, ponDefaultOfflineWarn, ponDefaultPowerEventMin
	if p.ONURxWarnDBm != nil {
		rxWarn = *p.ONURxWarnDBm
	}
	if p.PortOfflineWarn != nil {
		offlineWarn = *p.PortOfflineWarn
	}
	if p.PowerEventMin != nil {
		powerMin = *p.PowerEventMin
	}
	if offlineWarn < 0 || offlineWarn > 1 || powerMin < 1 {
		return configResult("params.port_offline_warn", "port_offline_warn lies between 0 and 1, power_event_min is at least 1")
	}

	sess, err := openSNMP(ctx, &spec, creds)
	if err != nil {
		return snmpFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := snmpMonitor(sess, TypePON)
	if err != nil {
		return snmpFailure(err)
	}
	if !matched {
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	onus, truncated, lastPort, err := ponRead(sess, &monitor, PONMaxONUs)
	if err != nil {
		return snmpFailure(err)
	}
	if len(onus) == 0 && !truncated {
		if _, ok := monitor.Tables[ponTableONUs]; !ok {
			return failResult(probewire.StatusError, probewire.ReasonUnsupported)
		}
	}
	return ponResult(rate.FromContext(ctx), ponNow(), onus, truncated, lastPort, &p, rxWarn, offlineWarn, powerMin)
}

// ponPort aggregates the ONUs of one port.
type ponPort struct {
	online, offline int
	rx              []float64 // rx_onu of the online ONUs
	rxOLT           []float64 // rx_olt of the online ONUs
}

// ponResult aggregates per port, reports the state changes of the ONUs
// and rates the ports (AK2, AK4).
func ponResult(store *rate.Store, at time.Time, onus []ponONU, truncated bool, lastPort uint32, p *ponParams,
	rxWarn, offlineWarn float64, powerMin int) probewire.Result {
	selected := map[string]bool{}
	for _, name := range p.Ports {
		if name = strings.TrimSpace(name); name != "" {
			selected[name] = true
		}
	}
	values := map[string]float64{}
	ports := map[string]*ponPort{}
	incomplete := map[string]bool{}
	hour := float64(at.UTC().Truncate(time.Hour).Unix())
	prevHour, _, hadHour := store.Swap(ponSeriesHour, rate.Num(hour), at)
	newHour := !hadHour || prevHour.Number != hour
	dyingGasp := 0
	for i := range onus {
		o := &onus[i]
		if len(selected) > 0 && !selected[o.port] {
			continue
		}
		if truncated && o.portIndex == lastPort {
			// The last port of a cut walk may lack ONUs: no port value.
			incomplete[o.port] = true
		}
		agg := ports[o.port]
		if agg == nil {
			agg = &ponPort{}
			ports[o.port] = agg
		}
		status := ponOffline
		if o.online {
			status = ponOnline
			agg.online++
		} else {
			agg.offline++
		}
		if o.online && o.hasRxONU {
			agg.rx = append(agg.rx, o.rxONU)
		}
		if o.online && o.hasRxOLT {
			agg.rxOLT = append(agg.rxOLT, o.rxOLT)
		}
		// State change against the previous state of the ONU (only rows
		// read: an ONU behind a cut has no change).
		prev, _, known := store.Swap("pon:onu:"+o.port+":"+o.id, rate.Enum(status), at)
		if known && prev.IsEnum() && prev.Enum != status {
			labels := map[string]string{LabelPONSN: o.sn, LabelPONPort: o.port, LabelPONFrom: prev.Enum, LabelPONTo: status}
			if status == ponOffline {
				labels[LabelPONCause] = o.cause
				if o.cause == PONCauseDyingGasp {
					dyingGasp++
				}
			}
			values[probewire.ValueKey(ValuePONStatusChange, labels)] = 1
		}
		if o.online && o.hasRxONU && (o.rxONU < rxWarn || newHour) {
			values[probewire.ValueKey(ValuePONONURx, map[string]string{LabelPONSN: o.sn, LabelPONPort: o.port})] = o.rxONU
		}
	}

	var warnPort string
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if incomplete[name] {
			continue
		}
		agg := ports[name]
		labels := map[string]string{LabelPONPort: name}
		values[probewire.ValueKey(ValuePONOnline, labels)] = float64(agg.online)
		values[probewire.ValueKey(ValuePONOffline, labels)] = float64(agg.offline)
		ponRxStats(values, agg.rx, map[string]string{LabelPONPort: name})
		ponRxStats(values, agg.rxOLT, map[string]string{LabelPONPort: name, LabelPONSide: ponSideOLT})
		total := agg.online + agg.offline
		if total > 0 && float64(agg.offline)/float64(total) > offlineWarn && warnPort == "" {
			warnPort = name
		}
	}
	if truncated {
		values[ValuePONTruncated] = 1
	}
	if dyingGasp >= powerMin {
		values[ValuePONPowerEvent] = 1
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	if warnPort != "" {
		r.Code = probewire.WarnThresholdExceeded
		r.Labels = map[string]string{LabelPONPort: warnPort}
	}
	return r
}

// ponRxStats adds min, avg and p05 of the levels as probe_pon_rx_dbm with
// the labels and stat.
func ponRxStats(values map[string]float64, levels []float64, labels map[string]string) {
	if len(levels) == 0 {
		return
	}
	sort.Float64s(levels)
	sum := 0.0
	for _, v := range levels {
		sum += v
	}
	for stat, v := range map[string]float64{
		"min": levels[0], "avg": sum / float64(len(levels)), "p05": percentile05(levels),
	} {
		l := maps.Clone(labels)
		l[LabelPONStat] = stat
		values[probewire.ValueKey(ValuePONRx, l)] = v
	}
}

// percentile05 returns the 5th percentile of sorted values (nearest rank).
func percentile05(sorted []float64) float64 {
	rank := int(math.Ceil(0.05*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	return sorted[rank]
}
