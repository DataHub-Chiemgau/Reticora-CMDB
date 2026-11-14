package rpki

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeROA is the register key of the ROA validation probe.
const TypeROA = "rpki_roa"

// Value and label names of an rpki_roa result (WP-1021 AK3).
const (
	ValueROAState  = "probe_rpki_roa_state"
	ValueROARoutes = "probe_rpki_roa_routes"
	LabelPrefix    = "prefix"
	LabelOriginAS  = "origin_as"
	LabelState     = "state"
	// LabelInvalid and LabelNotFound name the affected routes
	// ("prefix AS<n>", comma separated, at most roaLabelRoutes).
	LabelInvalid  = "invalid"
	LabelNotFound = "notfound"
)

// roaLabelRoutes bounds the routes named in a label; more are counted as
// "+n".
const roaLabelRoutes = 50

// ROAState is the validation state of a route after RFC 6811.
type ROAState int

// States in the order of the value probe_rpki_roa_state.
const (
	ROAValid    ROAState = 0
	ROANotFound ROAState = 1
	ROAInvalid  ROAState = 2
)

func (s ROAState) String() string {
	switch s {
	case ROAValid:
		return "valid"
	case ROANotFound:
		return "notfound"
	}
	return "invalid"
}

func init() { probe.Register(TypeROA, &ROAExecutor{}) }

// roaDeadlineMargin is the time before the deadline of the check at which
// the wait for the table ends (at most a tenth of the remaining time).
const roaDeadlineMargin = 100 * time.Millisecond

// ROAExecutor is the probe type rpki_roa (PRT-17, WP-1021): the route
// origin validation of configured prefix/origin AS pairs after RFC 6811
// against the VRP table of a validator. The table is the one of the shared
// session of the Manager (WP-1020): an rpki_rtr probe of the same
// validator (host:port) uses the same session; without one the first
// check opens it.
//
// Outcomes: a route invalid → fail/rpki_invalid (down); a route notfound
// with notfound_degraded → ok with rpki_notfound (degraded); otherwise ok.
// Without End of Data within the timeout, or with data older than the
// Expire interval, error/rpki_stale (unknown). The affected routes are in
// the labels invalid and notfound.
type ROAExecutor struct {
	// Manager replaces the sessions of the collector (tests).
	Manager *Manager
}

type roaRoute struct {
	Prefix   string `json:"prefix"`
	OriginAS uint32 `json:"origin_as"`
}

type roaParams struct {
	Validator struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"validator"`
	Routes           []roaRoute `json:"routes"`
	NotFoundDegraded *bool      `json:"notfound_degraded"`
}

// Execute implements probe.Executor.
func (e *ROAExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, _ map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p roaParams
	if len(bytes.TrimSpace(spec.Params)) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	routes := make([]netip.Prefix, len(p.Routes))
	for i, r := range p.Routes {
		pfx, err := netip.ParsePrefix(strings.TrimSpace(r.Prefix))
		if err != nil || pfx != pfx.Masked() || pfx.Addr().Is4In6() {
			return probe.ConfigResult(fmt.Sprintf("params.routes[%d].prefix", i), "must be a prefix without host bits")
		}
		routes[i] = pfx
	}
	host := p.Validator.Host
	if strings.TrimSpace(host) == "" {
		host = spec.Target
	}
	m := e.Manager
	if m == nil {
		m = defaultManager
	}
	// Wait for End of Data only until shortly before the deadline of the
	// check: past it probe.Run reports fail/timeout (down), while a
	// missing table is unknown (AK3).
	wctx := ctx
	if dl, ok := ctx.Deadline(); ok {
		margin := min(roaDeadlineMargin, time.Until(dl)/10)
		var cancel context.CancelFunc
		wctx, cancel = context.WithDeadline(ctx, dl.Add(-margin))
		defer cancel()
	}
	st, _ := await(wctx, m, validatorAddr(host, p.Validator.Port))
	// The table counts while it is fresh, also when the session failed
	// meanwhile (the old table stays, WP-1020); no table or stale data is
	// unknown, not down.
	if st.table == nil || m.now().Sub(st.lastEOD) > st.expire {
		return probewire.Result{Status: probewire.StatusError, Code: probewire.WarnRPKIStale}
	}
	notFoundDegraded := p.NotFoundDegraded == nil || *p.NotFoundDegraded
	return roaResult(st.table, p.Routes, routes, notFoundDegraded)
}

// roaResult validates every route and builds the result (AK2, AK3).
func roaResult(t *Table, cfg []roaRoute, routes []netip.Prefix, notFoundDegraded bool) probewire.Result {
	values := map[string]float64{}
	counts := map[ROAState]int{}
	var invalid, notFound []string
	for i, pfx := range routes {
		s := t.Validate(pfx, cfg[i].OriginAS)
		counts[s]++
		as := strconv.FormatUint(uint64(cfg[i].OriginAS), 10)
		values[probewire.ValueKey(ValueROAState, map[string]string{LabelPrefix: pfx.String(), LabelOriginAS: as})] = float64(s)
		switch s {
		case ROAInvalid:
			invalid = append(invalid, pfx.String()+" AS"+as)
		case ROANotFound:
			notFound = append(notFound, pfx.String()+" AS"+as)
		}
	}
	for _, s := range []ROAState{ROAValid, ROANotFound, ROAInvalid} {
		values[probewire.ValueKey(ValueROARoutes, map[string]string{LabelState: s.String()})] = float64(counts[s])
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	labels := map[string]string{}
	if len(invalid) > 0 {
		labels[LabelInvalid] = roaLabel(invalid)
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonRPKIInvalid
	}
	if len(notFound) > 0 {
		labels[LabelNotFound] = roaLabel(notFound)
		if r.Code == "" && notFoundDegraded {
			r.Code = probewire.WarnRPKINotFound
		}
	}
	if len(labels) > 0 {
		r.Labels = labels
	}
	return r
}

func roaLabel(routes []string) string {
	if len(routes) <= roaLabelRoutes {
		return strings.Join(routes, ",")
	}
	return strings.Join(routes[:roaLabelRoutes], ",") + ",+" + strconv.Itoa(len(routes)-roaLabelRoutes)
}

// Validate returns the state of the route prefix/origin after RFC 6811
// section 2: a VRP covers the route when its prefix contains the route
// prefix; it matches when it covers it, the route length is at most its
// maximum length, its AS equals the origin and is not 0. valid with a
// matched VRP, invalid with covering VRPs and none matched, notfound
// without a covering VRP.
//
// The search uses the prefix structure of the sorted table: for every
// length up to the route length one binary search for the covering prefix
// of that length (at most 33 resp. 129 searches), no linear scan
// (WP-1021 AK4).
func (t *Table) Validate(prefix netip.Prefix, origin uint32) ROAState {
	if t == nil {
		return ROANotFound
	}
	prefix = prefix.Masked()
	covered, matched := false, false
	consider := func(maxLen uint8, asn uint32) {
		covered = true
		if prefix.Bits() <= int(maxLen) && asn == origin && asn != 0 {
			matched = true
		}
	}
	// IPv4 and IPv6 are strictly apart: an IPv4-mapped IPv6 prefix is an
	// IPv6 prefix (the parameter check rejects it).
	addr := prefix.Addr()
	if addr.Is4() {
		a4 := addr.As4()
		full := binary.BigEndian.Uint32(a4[:])
		for l := 0; l <= prefix.Bits() && !matched; l++ {
			key := full & mask32(l)
			plen := uint8(l) //nolint:gosec // at most 32
			i := sort.Search(len(t.v4), func(i int) bool {
				v := t.v4[i]
				return v.addr > key || (v.addr == key && v.plen >= plen)
			})
			for ; i < len(t.v4) && t.v4[i].addr == key && t.v4[i].plen == plen; i++ {
				consider(t.v4[i].maxLen, t.v4[i].asn)
			}
		}
	} else {
		a16 := addr.As16()
		hi, lo := binary.BigEndian.Uint64(a16[:8]), binary.BigEndian.Uint64(a16[8:])
		for l := 0; l <= prefix.Bits() && !matched; l++ {
			mh, ml := mask128(l)
			kh, kl := hi&mh, lo&ml
			plen := uint8(l) //nolint:gosec // at most 128
			i := sort.Search(len(t.v6), func(i int) bool {
				v := t.v6[i]
				if v.hi != kh {
					return v.hi > kh
				}
				if v.lo != kl {
					return v.lo > kl
				}
				return v.plen >= plen
			})
			for ; i < len(t.v6) && t.v6[i].hi == kh && t.v6[i].lo == kl && t.v6[i].plen == plen; i++ {
				consider(t.v6[i].maxLen, t.v6[i].asn)
			}
		}
	}
	switch {
	case matched:
		return ROAValid
	case covered:
		return ROAInvalid
	}
	return ROANotFound
}

func mask32(l int) uint32 {
	if l <= 0 {
		return 0
	}
	return ^uint32(0) << (32 - l)
}

func mask128(l int) (hi, lo uint64) {
	switch {
	case l <= 0:
		return 0, 0
	case l < 64:
		return ^uint64(0) << (64 - l), 0
	case l == 64:
		return ^uint64(0), 0
	case l < 128:
		return ^uint64(0), ^uint64(0) << (128 - l)
	}
	return ^uint64(0), ^uint64(0)
}
