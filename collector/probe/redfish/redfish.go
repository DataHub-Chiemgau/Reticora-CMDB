// Package redfish is the probe type redfish (MON-03, MON-04, WP-998):
// health, temperatures, fans and power supplies of a server read through
// its Redfish service. It registers its executor in init(); the collector
// links it in through collector/probe/all.
//
// The executor holds no state per probe (WP-950 AK4). It sends only GET
// requests through collector/plugins/redfish (PollHealth), with Basic
// authentication from the namespace redfish and the TLS scopes of the
// collector (Configure).
package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	rfplugin "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/redfish"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Type is the key of the probe type.
const Type = "redfish"

// Value names of a result (labels in the key, probewire.ValueKey).
const (
	ValueHealth      = "probe_redfish_health"
	ValueTemperature = "probe_redfish_temperature_celsius"
	ValueFanRPM      = "probe_redfish_fan_rpm"
	ValuePSUWatts    = "probe_redfish_psu_input_watts"
	ValuePSUState    = "probe_redfish_psu_state"
	ValueTruncated   = "probe_redfish_truncated"
)

// Label of the sensor, fan or power supply that decided a non-ok result.
const (
	LabelSensor = "sensor"
	LabelFan    = "fan"
	LabelPSU    = "psu"
	// LabelTruncated marks a result whose poll hit the request budget.
	LabelTruncated = "truncated"
)

// plugin is the Redfish plugin the executor polls through; Configure sets
// the instance of the collector with its TLS scopes.
var plugin atomic.Pointer[rfplugin.Plugin]

func init() {
	plugin.Store(rfplugin.New())
	probe.Register(Type, Executor{})
}

// Configure hands the Redfish plugin of the collector (with the TLS
// exceptions of RETICORA_REDFISH_TLS_SCOPES) to the executor (WP-998 AK3).
// Without it a plugin without exceptions applies. nil is ignored.
func Configure(p *rfplugin.Plugin) {
	if p != nil {
		plugin.Store(p)
	}
}

// Executor is the probe type redfish.
//
// Outcomes: ok; ok with the warn code check_failed (health or a power
// supply Warning) or threshold_exceeded (a temperature above the warning
// level), which the state machine turns into degraded; fail with
// check_failed (health or a power supply Critical), threshold_exceeded (a
// temperature above the critical level), timeout, refused, unreachable or
// status_mismatch (an HTTP status other than 200, 401, 403); error with
// auth_failed, tls_untrusted, response_too_large, unsupported (no system
// and no chassis) or config. An error leads to unknown, never to down
// (E-177).
type Executor struct {
	// Plugin replaces the configured plugin (tests).
	Plugin *rfplugin.Plugin
}

type params struct {
	Port       int      `json:"port"`
	SystemID   string   `json:"system_id"`
	ChassisIDs []string `json:"chassis_ids"`
	TempWarn   *float64 `json:"temp_warn"`
	TempCrit   *float64 `json:"temp_crit"`
}

type thresholds struct {
	TempWarn *float64 `json:"temp_warn"`
	TempCrit *float64 `json:"temp_crit"`
}

// verdict is the worst finding of a check: severity 2 fail, 1 warn.
type verdict struct {
	severity int
	code     string
	labels   map[string]string
}

func (v *verdict) raise(severity int, code, label, name string) {
	if severity <= v.severity {
		return
	}
	v.severity, v.code = severity, code
	v.labels = nil
	if label != "" {
		v.labels = map[string]string{label: name}
	}
}

// Execute polls the Redfish service of spec.Target once.
func (e Executor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	start := time.Now()
	var p params
	if len(bytes.TrimSpace(spec.Params)) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "the parameters cannot be decoded")
		}
	}
	if len(bytes.TrimSpace(spec.Thresholds)) > 0 {
		var t thresholds
		if err := json.Unmarshal(spec.Thresholds, &t); err != nil {
			return configResult("thresholds", "the thresholds cannot be decoded")
		}
		if t.TempWarn != nil {
			p.TempWarn = t.TempWarn
		}
		if t.TempCrit != nil {
			p.TempCrit = t.TempCrit
		}
	}
	pl := e.Plugin
	if pl == nil {
		pl = plugin.Load()
	}
	host := strings.Trim(strings.TrimSpace(spec.Target), "[]")
	h, err := pl.PollHealth(ctx, host, creds, rfplugin.PollOptions{
		Port: p.Port, SystemID: p.SystemID, ChassisIDs: p.ChassisIDs,
	})
	if err != nil {
		return failure(ctx, err)
	}
	r := evaluate(h, &p)
	rtt := float64(time.Since(start).Microseconds()) / 1000
	r.RTTMS = &rtt
	return r
}

// evaluate turns a poll into a result (WP-998 AK3, AK4).
func evaluate(h *rfplugin.Health, p *params) probewire.Result {
	values := map[string]float64{ValueHealth: float64(h.Health)}
	var v verdict
	switch h.Health {
	case rfplugin.HealthCritical:
		v.raise(2, probewire.WarnCheckFailed, "", "")
	case rfplugin.HealthWarning:
		v.raise(1, probewire.WarnCheckFailed, "", "")
	}
	for _, t := range h.Temperatures {
		values[probewire.ValueKey(ValueTemperature, map[string]string{LabelSensor: t.Name})] = t.Celsius
		warn, crit := t.UpperNonCritical, t.UpperCritical
		if p.TempWarn != nil {
			warn = p.TempWarn
		}
		if p.TempCrit != nil {
			crit = p.TempCrit
		}
		switch {
		case crit != nil && t.Celsius > *crit:
			v.raise(2, probewire.WarnThresholdExceeded, LabelSensor, t.Name)
		case warn != nil && t.Celsius > *warn:
			v.raise(1, probewire.WarnThresholdExceeded, LabelSensor, t.Name)
		}
	}
	for _, f := range h.Fans {
		if f.RPM != nil {
			values[probewire.ValueKey(ValueFanRPM, map[string]string{LabelFan: f.Name})] = *f.RPM
		}
	}
	for _, s := range h.PowerSupplies {
		labels := map[string]string{LabelPSU: s.Name}
		values[probewire.ValueKey(ValuePSUState, labels)] = float64(s.Health)
		if s.InputWatts != nil {
			values[probewire.ValueKey(ValuePSUWatts, labels)] = *s.InputWatts
		}
		switch s.Health {
		case rfplugin.HealthCritical:
			v.raise(2, probewire.WarnCheckFailed, LabelPSU, s.Name)
		case rfplugin.HealthWarning:
			v.raise(1, probewire.WarnCheckFailed, LabelPSU, s.Name)
		}
	}
	if h.Truncated {
		// Not every resource was read within the request budget: a gap
		// is at least a warning, never a plain ok.
		values[ValueTruncated] = 1
		v.raise(1, probewire.WarnCheckFailed, LabelTruncated, "true")
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values, Labels: v.labels}
	switch v.severity {
	case 2:
		r.Status, r.Code = probewire.StatusFail, v.code
	case 1:
		r.Code = v.code
	}
	return r
}

// failure maps a failed poll to a result. A refused credential and an
// untrusted certificate are error (unknown), never down (E-177).
func failure(ctx context.Context, err error) probewire.Result {
	var status *rfplugin.StatusError
	var opErr *net.OpError
	switch {
	case errors.Is(err, plugins.ErrAuthFailed):
		return result(probewire.StatusError, probewire.ReasonAuthFailed)
	case plugins.UntrustedCertificate(err):
		return result(probewire.StatusError, probewire.ReasonTLSUntrusted)
	case errors.Is(err, rfplugin.ErrBodyTooLarge):
		return result(probewire.StatusError, probewire.ReasonResponseTooLarge)
	case errors.Is(err, rfplugin.ErrNoSystem):
		return result(probewire.StatusError, probewire.ReasonUnsupported)
	case errors.Is(err, rfplugin.ErrInvalidOption):
		return configResult("params", "a system or chassis id is invalid")
	case errors.Is(err, rfplugin.ErrForeignLink):
		// The device sent a link off the target; the parameters are fine.
		return result(probewire.StatusError, probewire.ReasonUnsupported)
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return result(probewire.StatusFail, probewire.ReasonTimeout)
	case errors.As(err, &status):
		return result(probewire.StatusFail, probewire.ReasonStatusMismatch)
	case errors.Is(err, syscall.ECONNREFUSED):
		return result(probewire.StatusFail, probewire.ReasonRefused)
	case errors.As(err, &opErr) && opErr.Timeout():
		return result(probewire.StatusFail, probewire.ReasonTimeout)
	}
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &typeErr) {
		// The service answered with something that is no Redfish
		// resource: a statement about the target, not a failed check.
		return result(probewire.StatusError, probewire.ReasonUnsupported)
	}
	return result(probewire.StatusFail, probewire.ReasonUnreachable)
}

func result(status probewire.Status, code string) probewire.Result {
	return probewire.Result{Status: status, Code: code}
}

func configResult(field, message string) probewire.Result {
	return probewire.Result{Status: probewire.StatusError, Code: probewire.ReasonConfig,
		Labels: map[string]string{probe.LabelField: field, probe.LabelMessage: message}}
}
