// Package tr069 is the probe type tr069 (PRT-15, E-192, WP-1018): the
// collector reads device states from the customer's existing ACS over its
// northbound API, only with GET requests (last inform, selected
// parameters). Reticora is no ACS: no connection request, no task, no
// parameter is written. The reference adapter is the GenieACS NBI; other
// ACS products need an adapter of their own behind Reader.
//
// The package registers its executor in init(); the collector links it in
// through collector/probe/all. The executor holds no state.
package tr069

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Type is the register key of the probe type.
const Type = "tr069"

// Limits of a check (WP-1018 AK3).
const (
	// MaxDevices is the most devices a check evaluates (max_rows).
	MaxDevices = 10_000
	// PageSize is the number of devices per page.
	PageSize = 100
	// DefaultStaleFactor multiplies inform_interval_s.
	DefaultStaleFactor = 3
)

// Value names of a result. probe_tr069_last_inform_age_seconds and
// probe_tr069_parameter carry the labels serial and device_id (the device
// id of the ACS: a serial number is unique only within OUI and product
// class), the parameter values also parameter (the TR-069 path).
const (
	ValueDevices        = "probe_tr069_devices"
	ValueDevicesStale   = "probe_tr069_devices_stale"
	ValueLastInformAge  = "probe_tr069_last_inform_age_seconds"
	ValueParameter      = "probe_tr069_parameter"
	ValueTruncated      = "probe_tr069_truncated"
	LabelSerial         = "serial"
	LabelDeviceID       = "device_id"
	LabelParameter      = "parameter"
	labelFieldACSURL    = "params.acs_url"
	directionBelow      = "below"
	severityWarn        = 1
	severityCrit        = 2
	defaultCheckTimeout = 30 * time.Second
)

// scopes are the TLS exceptions of the collector (Configure).
var scopes atomic.Pointer[[]plugins.TLSScope]

func init() { probe.Register(Type, &Executor{}) }

// Configure sets the TLS exceptions (CA bundle or SPKI pins per network)
// the ACS connection is verified with; without it the system roots apply.
func Configure(s []plugins.TLSScope) {
	c := append([]plugins.TLSScope(nil), s...)
	scopes.Store(&c)
}

// Executor is the probe type tr069.
//
// Outcomes (the state machine of WP-952 turns ok with a warn code into
// degraded): with exactly one selected device a stale inform is
// fail/inform_stale, a critical parameter fail/threshold_exceeded, a
// warning ok/threshold_exceeded; with several devices all stale is
// fail/inform_stale, one stale ok/inform_stale, a violated threshold
// ok/threshold_exceeded. No device is ok/no_devices. A refused credential
// is error/auth_failed, an untrusted certificate error/tls, an ACS out of
// reach fail/unreachable or fail/timeout.
type Executor struct {
	// TLSScopes replace the configured scopes (tests).
	TLSScopes []plugins.TLSScope
	// Now replaces the clock of the inform age (tests).
	Now func() time.Time
	// newReader replaces the adapter (tests); nil is the GenieACS NBI.
	newReader func(base *url.URL, timeout time.Duration, auth string) Reader
}

// Parameter is one requested TR-069 parameter with optional thresholds.
type Parameter struct {
	Path      string   `json:"path"`
	Warn      *float64 `json:"warn"`
	Crit      *float64 `json:"crit"`
	Direction string   `json:"direction"`
}

type params struct {
	ACSURL          string      `json:"acs_url"`
	Selector        Selector    `json:"selector"`
	Parameters      []Parameter `json:"parameters"`
	InformIntervalS int64       `json:"inform_interval_s"`
	StaleFactor     int64       `json:"stale_factor"`
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) tlsScopes() []plugins.TLSScope {
	if e.TLSScopes != nil {
		return e.TLSScopes
	}
	if s := scopes.Load(); s != nil {
		return *s
	}
	return nil
}

// Execute implements probe.Executor.
func (e *Executor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	start := time.Now()
	var p params
	if len(bytes.TrimSpace(spec.Params)) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	if p.StaleFactor <= 0 {
		p.StaleFactor = DefaultStaleFactor
	}
	base, err := url.Parse(strings.TrimSpace(p.ACSURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return probe.ConfigResult(labelFieldACSURL, "must be an http or https URL")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return probe.ConfigResult(labelFieldACSURL, "must not carry user info, a query or a fragment")
	}
	auth := authorization(creds)
	if auth != "" && base.Scheme != "https" {
		// A credential goes only over HTTPS (WP-1018 AK4).
		return probewire.Result{Status: probewire.StatusError, Code: probewire.ReasonUnsupported,
			Labels: map[string]string{probe.LabelField: labelFieldACSURL, probe.LabelMessage: "credentials are sent over https only"}}
	}
	paths := make([]string, len(p.Parameters))
	for i, prm := range p.Parameters {
		paths[i] = prm.Path
	}

	timeout := defaultCheckTimeout
	if d, ok := ctx.Deadline(); ok {
		timeout = time.Until(d)
	}
	var reader Reader
	if e.newReader != nil {
		reader = e.newReader(base, timeout, auth)
	} else {
		cfg := plugins.TLSConfigFor(base.Hostname(), e.tlsScopes()).Clone()
		cfg.ServerName = base.Hostname()
		reader = newGenieACS(base, newReadOnlyClient(timeout, cfg), auth)
	}
	devices, truncated, err := listAll(ctx, reader, p.Selector, paths)
	if err != nil {
		return failure(ctx, err)
	}
	r := evaluate(devices, &p, truncated, e.now())
	rtt := float64(time.Since(start).Microseconds()) / 1000
	r.RTTMS = &rtt
	return r
}

// authorization returns the Authorization header of the namespace acs:
// Bearer with token, Basic with username and password, else empty.
func authorization(creds map[string]string) string {
	if t := creds["token"]; t != "" {
		return "Bearer " + t
	}
	if u := creds["username"]; u != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+creds["password"]))
	}
	return ""
}

// listAll reads the pages of the selected devices, at most MaxDevices
// (WP-1018 AK3): truncated reports that the limit ended the paging while
// the last page was full.
func listAll(ctx context.Context, r Reader, sel Selector, paths []string) (devices []Device, truncated bool, err error) {
	for len(devices) < MaxDevices {
		limit := min(PageSize, MaxDevices-len(devices))
		page, err := r.ListDevices(ctx, sel, paths, Page{Skip: len(devices), Limit: limit})
		if err != nil {
			return nil, false, err
		}
		if len(page) > limit {
			page = page[:limit]
		}
		devices = append(devices, page...)
		if len(page) < limit {
			return devices, false, nil
		}
	}
	return devices, true, nil
}

// failure maps a failed read (WP-1018 AK6, E-177).
func failure(ctx context.Context, err error) probewire.Result {
	var status *StatusError
	var opErr *net.OpError
	switch {
	case errors.Is(err, ErrAuth):
		return result(probewire.StatusError, probewire.ReasonAuthFailed)
	case plugins.UntrustedCertificate(err):
		return result(probewire.StatusError, probewire.ReasonTLS)
	case errors.Is(err, ErrWriteMethod):
		return result(probewire.StatusError, probewire.ReasonConfig)
	case errors.Is(err, ErrPageTooLarge):
		return result(probewire.StatusError, probewire.ReasonResponseTooLarge)
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return result(probewire.StatusFail, probewire.ReasonTimeout)
	case errors.As(err, &status):
		return result(probewire.StatusFail, probewire.ReasonStatusMismatch)
	case errors.Is(err, syscall.ECONNREFUSED):
		return result(probewire.StatusFail, probewire.ReasonUnreachable)
	case errors.As(err, &opErr) && opErr.Timeout():
		return result(probewire.StatusFail, probewire.ReasonTimeout)
	}
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &typeErr) {
		return result(probewire.StatusError, probewire.ReasonUnsupported)
	}
	return result(probewire.StatusFail, probewire.ReasonUnreachable)
}

func result(status probewire.Status, code string) probewire.Result {
	return probewire.Result{Status: status, Code: code}
}

// numeric returns a parameter value as a number (TR-069 numbers may come
// as JSON numbers or as strings).
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// violation returns the severity a value violates (0: none).
func violation(v float64, prm *Parameter) int {
	over := func(limit *float64) bool {
		if limit == nil {
			return false
		}
		if prm.Direction == directionBelow {
			return v < *limit
		}
		return v > *limit
	}
	switch {
	case over(prm.Crit):
		return severityCrit
	case over(prm.Warn):
		return severityWarn
	}
	return 0
}

// evaluate judges the devices (WP-1018 AK5).
func evaluate(devices []Device, p *params, truncated bool, now time.Time) probewire.Result {
	values := map[string]float64{ValueDevices: float64(len(devices))}
	if truncated {
		values[ValueTruncated] = 1
	}
	if len(devices) == 0 {
		values[ValueDevicesStale] = 0
		return probewire.Result{Status: probewire.StatusOK, Code: probewire.WarnNoDevices, Values: values}
	}
	staleAfter := time.Duration(p.InformIntervalS*p.StaleFactor) * time.Second
	var staleSerials, critSerials, warnSerials []*Device
	for i := range devices {
		d := &devices[i]
		labels := map[string]string{LabelSerial: d.Serial, LabelDeviceID: d.ID}
		stale := d.LastInform.IsZero()
		if !stale {
			age := now.Sub(d.LastInform)
			values[probewire.ValueKey(ValueLastInformAge, labels)] = max(age.Seconds(), 0)
			stale = age > staleAfter
		}
		if stale {
			staleSerials = append(staleSerials, d)
		}
		worst := 0
		for j := range p.Parameters {
			prm := &p.Parameters[j]
			v, ok := numeric(d.Values[prm.Path])
			if !ok {
				continue
			}
			values[probewire.ValueKey(ValueParameter, map[string]string{LabelSerial: d.Serial, LabelDeviceID: d.ID, LabelParameter: prm.Path})] = v
			worst = max(worst, violation(v, prm))
		}
		switch worst {
		case severityCrit:
			critSerials = append(critSerials, d)
		case severityWarn:
			warnSerials = append(warnSerials, d)
		}
	}
	values[ValueDevicesStale] = float64(len(staleSerials))
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	label := func(ds []*Device) map[string]string {
		sort.Slice(ds, func(i, j int) bool {
			if ds[i].Serial != ds[j].Serial {
				return ds[i].Serial < ds[j].Serial
			}
			return ds[i].ID < ds[j].ID
		})
		return map[string]string{LabelSerial: ds[0].Serial, LabelDeviceID: ds[0].ID}
	}
	single := len(devices) == 1
	switch {
	case len(staleSerials) > 0 && (single || len(staleSerials) == len(devices)):
		r.Status, r.Code, r.Labels = probewire.StatusFail, probewire.WarnInformStale, label(staleSerials)
	case single && len(critSerials) > 0:
		r.Status, r.Code, r.Labels = probewire.StatusFail, probewire.WarnThresholdExceeded, label(critSerials)
	case len(staleSerials) > 0:
		r.Code, r.Labels = probewire.WarnInformStale, label(staleSerials)
	case len(critSerials) > 0:
		r.Code, r.Labels = probewire.WarnThresholdExceeded, label(critSerials)
	case len(warnSerials) > 0:
		r.Code, r.Labels = probewire.WarnThresholdExceeded, label(warnSerials)
	}
	return r
}
