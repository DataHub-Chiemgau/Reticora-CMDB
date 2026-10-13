package discovery

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Discovery error states (DIS-08): every failure of a target, a scope run
// or a job carries a cause, so the UI, the metrics and the logs say why
// discovery found nothing – not only that it did not.

// Error causes.
const (
	CauseUnreachable  = "unreachable"
	CauseTimeout      = "timeout"
	CauseRefused      = "refused"
	CauseAuthFailed   = "auth_failed"
	CauseTLS          = "tls"
	CauseProtocol     = "protocol_error"
	CauseConfig       = "config_error"
	CauseUpload       = "upload_failed"
	CauseUnknown      = "unknown"
	maxRunErrors      = 50
	maxErrorMessage   = 300
	ScopeStateOK      = "ok"
	ScopeStatePartial = "partial"
	ScopeStateFailed  = "failed"
	ScopeStateNever   = "never_run"
)

// ErrorState is one failure with its cause.
type ErrorState struct {
	Cause    string `json:"cause"`
	Message  string `json:"message,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Target   string `json:"target,omitempty"`
}

// ScopeHealth is the state of a scope after its last run.
type ScopeHealth struct {
	State  string       `json:"state"`
	Errors []ErrorState `json:"errors,omitempty"`
}

// causePatterns map error texts onto causes; the first match wins.
var causePatterns = []struct {
	cause string
	parts []string
}{
	{CauseAuthFailed, []string{"unable to authenticate", "authentication failed", "auth failed", "unauthorized", "401", "permission denied", "wrong digest", "unknown user", "access denied", "login"}},
	{CauseTLS, []string{"x509", "certificate", "tls", "handshake failure"}},
	{CauseTimeout, []string{"timeout", "timed out", "deadline exceeded"}},
	{CauseRefused, []string{"connection refused", "connection reset"}},
	{CauseUnreachable, []string{"no route to host", "network is unreachable", "host is down", "no such host", "unreachable"}},
	{CauseConfig, []string{"requires authpriv", "not allowed", "required", "invalid", "not configured", "unknown channel"}},
	{CauseUpload, []string{"upload"}},
	{CauseProtocol, []string{"unexpected", "malformed", "decode", "parse", "status", "not an ipmi", "checksum"}},
}

// ClassifyCause derives the cause of an error message.
func ClassifyCause(message string) string {
	m := strings.ToLower(message)
	for _, p := range causePatterns {
		for _, part := range p.parts {
			if strings.Contains(m, part) {
				return p.cause
			}
		}
	}
	return CauseUnknown
}

var knownCauses = map[string]bool{CauseUnreachable: true, CauseTimeout: true, CauseRefused: true, CauseAuthFailed: true,
	CauseTLS: true, CauseProtocol: true, CauseConfig: true, CauseUpload: true, CauseUnknown: true}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// NormalizeRunResult checks a scope run result as the collector reported
// it: every error gets a known cause and a bounded message, the state is
// ok, partial (errors and records) or failed (errors, no records). It
// counts the run and its errors in the metrics.
func NormalizeRunResult(result map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range result {
		out[k] = v
	}
	var errs []ErrorState
	raw, _ := result["errors"].([]any)
	for _, e := range raw {
		var es ErrorState
		switch v := e.(type) {
		case string:
			protocol, msg, found := strings.Cut(v, ": ")
			if !found {
				protocol, msg = "", v
			}
			es = ErrorState{Protocol: protocol, Message: msg}
		case map[string]any:
			es.Protocol, _ = v["protocol"].(string)
			es.Target, _ = v["target"].(string)
			es.Message, _ = v["message"].(string)
			es.Cause, _ = v["cause"].(string)
		default:
			continue
		}
		if !knownCauses[es.Cause] {
			es.Cause = ClassifyCause(es.Message)
		}
		es.Message = truncate(es.Message, maxErrorMessage)
		es.Protocol = truncate(es.Protocol, 32)
		es.Target = truncate(es.Target, 255)
		errs = append(errs, es)
		if len(errs) == maxRunErrors {
			break
		}
	}
	records := 0
	switch n := result["records"].(type) {
	case float64:
		records = int(n)
	case int:
		records = n
	}
	state := ScopeStateOK
	switch {
	case len(errs) > 0 && records == 0:
		state = ScopeStateFailed
	case len(errs) > 0:
		state = ScopeStatePartial
	}
	out["status"] = state
	if len(errs) > 0 {
		out["errors"] = errs
	} else {
		delete(out, "errors")
	}
	scopeRuns.WithLabelValues(state).Inc()
	for _, e := range errs {
		protocol := e.Protocol
		if protocol == "" {
			protocol = "unknown"
		}
		discoveryErrors.WithLabelValues(protocol, e.Cause).Inc()
	}
	return out
}

// ScopeHealthOf derives the health of a scope from its last run result.
func ScopeHealthOf(s *Scope) ScopeHealth {
	if s.LastRunAt == "" || s.LastResult == nil {
		return ScopeHealth{State: ScopeStateNever}
	}
	h := ScopeHealth{State: ScopeStateOK}
	if st, ok := s.LastResult["status"].(string); ok && (st == ScopeStatePartial || st == ScopeStateFailed) {
		h.State = st
	}
	raw, _ := s.LastResult["errors"].([]any)
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		es := ErrorState{}
		es.Cause, _ = m["cause"].(string)
		es.Message, _ = m["message"].(string)
		es.Protocol, _ = m["protocol"].(string)
		es.Target, _ = m["target"].(string)
		h.Errors = append(h.Errors, es)
	}
	if typed, ok := s.LastResult["errors"].([]ErrorState); ok {
		h.Errors = append(h.Errors, typed...)
	}
	return h
}

// JobErrorOf derives the error state of a failed discovery job.
func JobErrorOf(j *Job) *ErrorState {
	if j.Status != "failed" {
		return nil
	}
	msg, _ := j.ResultSummary["error"].(string)
	cause, _ := j.ResultSummary["cause"].(string)
	if !knownCauses[cause] {
		cause = ClassifyCause(msg)
	}
	return &ErrorState{Cause: cause, Message: truncate(msg, maxErrorMessage)}
}

var (
	scopeRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "discovery_scope_runs_total",
		Help: "Discovery scope runs reported by the collectors, by state (ok, partial, failed).",
	}, []string{"state"})
	discoveryErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "discovery_errors_total",
		Help: "Discovery errors reported by the collectors, by protocol and cause.",
	}, []string{"protocol", "cause"})
)
