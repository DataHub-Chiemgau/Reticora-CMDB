package probewire

// Reason codes of results (Result.Code) and state changes
// (StateChange.Reason), PRB-05, PRT-13, PRT-18 and E-177. Probe type work
// packages add their codes and warn codes only here; deriving "ok with a
// warn code → degraded" is the job of the collector state machine (WP-952).
const (
	// ReasonCollectorUnreachable: no member of the collector group is
	// reachable (network down, PRB-03).
	ReasonCollectorUnreachable = "collector_unreachable"
	// ReasonParentDown: the parent probe is down; the child is not
	// meaningful.
	ReasonParentDown = "parent_down"
	// ReasonLimitExceeded: a parallelism or rate limit of the type was
	// exceeded on the collector (PRT-01).
	ReasonLimitExceeded = "limit_exceeded"
	// ReasonUnsupported: missing collector capability or a type that is not
	// implemented (PRB-05, PRT-13, PRT-18). Never "not_supported".
	ReasonUnsupported = "unsupported"
	// ReasonConfig: the probe configuration is invalid for the target.
	ReasonConfig = "config"
	// ReasonAuthFailed: the credential was rejected by the target.
	ReasonAuthFailed = "auth_failed"
	// ReasonTimeout: no answer within the timeout.
	ReasonTimeout = "timeout"
	// ReasonRefused: the target actively refused the connection.
	ReasonRefused = "refused"
	// ReasonICMPUnavailable: the collector cannot send ICMP (no raw or
	// unprivileged ICMP socket).
	ReasonICMPUnavailable = "icmp_unavailable"
	// ReasonUnreachable: the target is not reachable (base code, used by
	// WP-950 and WP-970).
	ReasonUnreachable = "unreachable"
	// ReasonTLS: TLS handshake failed or the certificate is invalid (base
	// code, used by WP-1016 and WP-1018).
	ReasonTLS = "tls"
	// ReasonExpectMismatch: the target answered, but the answer did not
	// match the expected pattern (tcp banner, WP-970). A failed check, not
	// a warning: a violated expected value counts towards down (E-177).
	ReasonExpectMismatch = "expect_mismatch"
	// ReasonResponseTooLarge: the answer exceeds the response limit of the
	// type and was discarded (dns: 4096 bytes over UDP, 64 KiB over TCP,
	// WP-1010).
	ReasonResponseTooLarge = "response_too_large"
	// ReasonTLSUntrusted: the certificate of the target is not trusted by
	// the system roots or a TLS scope; no request and no credential was
	// sent (http, WP-971). An error (→ unknown), not a failed check.
	ReasonTLSUntrusted = "tls_untrusted"
	// ReasonStatusMismatch: the HTTP status lies outside expected_status
	// (http, WP-971).
	ReasonStatusMismatch = "status_mismatch"
	// ReasonBodyMismatch: the body does not match body_regex (http,
	// WP-971); a violated expected value counts towards down (E-177).
	ReasonBodyMismatch = "body_mismatch"
	// ReasonRedirectLimit: more redirects than follow_redirects, or a
	// redirect from https to http with a credential (http, WP-971).
	ReasonRedirectLimit = "redirect_limit"
	// ReasonSessionDown: an expected BFD session is missing or not up
	// (bfd, WP-1003, PRT-08).
	ReasonSessionDown = "session_down"
	// ReasonLOS: loss of signal, the Rx level of a transceiver below
	// -40 dBm (optical, WP-1004, PRT-09).
	ReasonLOS = "los"
	// ReasonVRPLimit: an RPKI validator delivered more than 2,000,000
	// VRPs (rpki_rtr, WP-1020, PRT-17); the incomplete state is discarded,
	// the previous table stays. An error (→ unknown).
	ReasonVRPLimit = "vrp_limit"

	// WarnThresholdExceeded: the check succeeded but a threshold (RTT,
	// loss, value) was violated.
	WarnThresholdExceeded = "threshold_exceeded"
	// WarnCertExpiring: the check succeeded but the certificate expires
	// soon.
	WarnCertExpiring = "cert_expiring"
	// WarnAnswerMismatch: the answer differs from the expectation (dns:
	// RCODE, expected contents or AD bit, WP-1010). With severity
	// degraded it is a warn code (ok → degraded), with severity down the
	// code of a failed check.
	WarnAnswerMismatch = "answer_mismatch"
	// WarnCheckFailed: a check of the target's own state failed (redfish:
	// health or power supply Warning/Critical, WP-998; snmp_status and
	// snmp_if: a violated OID check or an oper-down interface, WP-995,
	// WP-996). With severity degraded it is a warn code (ok → degraded),
	// with severity down the code of a failed check.
	WarnCheckFailed = "check_failed"
	// WarnInformStale: the last inform of a TR-069 device is older than
	// inform_interval_s x stale_factor (tr069, WP-1018). The code of a
	// failed check when the only or every selected device is stale, a warn
	// code (ok → degraded) when only some are.
	WarnInformStale = "inform_stale"
	// WarnNoDevices: the selector of a tr069 probe found no device in the
	// ACS (WP-1018); ok → degraded.
	WarnNoDevices = "no_devices"
	// WarnRPKIStale: the VRP data of an RPKI validator is older than its
	// Expire interval (rpki_rtr, WP-1020); ok → degraded.
	WarnRPKIStale = "rpki_stale"
)

// WarnCodes are the codes of a successful check with a warning; a result
// with status ok and one of these codes leads to degraded (WP-952). Every
// warn code is a valid reason code.
var WarnCodes = map[string]bool{
	WarnThresholdExceeded: true,
	WarnCertExpiring:      true,
	WarnAnswerMismatch:    true,
	WarnCheckFailed:       true,
	WarnInformStale:       true,
	WarnNoDevices:         true,
	WarnRPKIStale:         true,
}

// reasons lists every valid reason code apart from the warn codes.
var reasons = map[string]bool{
	ReasonCollectorUnreachable: true,
	ReasonParentDown:           true,
	ReasonLimitExceeded:        true,
	ReasonUnsupported:          true,
	ReasonConfig:               true,
	ReasonAuthFailed:           true,
	ReasonTimeout:              true,
	ReasonRefused:              true,
	ReasonICMPUnavailable:      true,
	ReasonUnreachable:          true,
	ReasonTLS:                  true,
	ReasonExpectMismatch:       true,
	ReasonResponseTooLarge:     true,
	ReasonTLSUntrusted:         true,
	ReasonStatusMismatch:       true,
	ReasonBodyMismatch:         true,
	ReasonRedirectLimit:        true,
	ReasonSessionDown:          true,
	ReasonLOS:                  true,
	ReasonVRPLimit:             true,
}

// ValidReason reports whether code is a known reason or warn code.
func ValidReason(code string) bool {
	return reasons[code] || WarnCodes[code]
}

// IsWarnCode reports whether code is a warn code.
func IsWarnCode(code string) bool {
	return WarnCodes[code]
}
