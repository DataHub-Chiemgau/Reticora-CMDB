package admp

import (
	"github.com/prometheus/client_golang/prometheus"
)

// The match of an ADMP port to a network interface (ADM-04, E-251,
// WP-1073). The documentation names a port only by AccessPort.id; node
// name, unit index and port index locate it. The rule, until A-86 names
// the interface names of the vendors:
//
//   - Device: exactly one CI of the organization whose hostname_lc equals
//     lower(node_name); when there is none, exactly one whose first label
//     (up to the first dot) equals lower(node_name).
//   - Port: exactly one interface of that CI whose name, lower case and
//     without white space, ends with <unit_index>/<port_index>, preceded by
//     the start of the name or a non-digit. The indexes are taken without
//     offset (A-86).
//   - A match sets network_interface_id and matched_by rule (the trigger
//     sets ci_id, client_id and site_id); no or an ambiguous match leaves
//     the port without interface and CI, matched_by none. A manual match
//     stays as it is.
//
// The match of an account's inventory is one set-based statement under
// row level security in the tenant context of the job (MatchAccessPorts),
// plus one count of the manual matches.

// Rules of the match (label of reticora_admp_port_match_total).
const (
	MatchRuleHostname      = "hostname"
	MatchRuleHostnameLabel = "hostname_label"
	MatchRuleNone          = "none"
)

// maxUnmatchedPorts bounds the unmatched ports a job result names.
const maxUnmatchedPorts = 100

// UnmatchedPort names a port without interface in the result of the
// inventory job.
type UnmatchedPort struct {
	PortKey   string `json:"port_key"`
	NodeName  string `json:"node_name"`
	UnitIndex int64  `json:"unit_index"`
	PortIndex int64  `json:"port_index"`
}

// MatchResult is the outcome of a match of an account's inventory:
// matched counts the ports with an interface (rule or manual), unmatched
// those without; Unmatched names the first maxUnmatchedPorts of them by
// port key.
type MatchResult struct {
	Matched        int             `json:"matched"`
	Unmatched      int             `json:"unmatched"`
	UnmatchedPorts []UnmatchedPort `json:"unmatched_ports"`
	// Rules counts the non-manual ports per rule.
	Rules map[string]int `json:"-"`
}

// portMatchTotal counts the applied rules.
var portMatchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "reticora_admp_port_match_total",
	Help: "ADMP ports matched to a network interface by rule (hostname, hostname_label or none).",
}, []string{"rule"})

// countMatch adds the rules of a match to the metric.
func countMatch(r *MatchResult) {
	for _, rule := range []string{MatchRuleHostname, MatchRuleHostnameLabel, MatchRuleNone} {
		portMatchTotal.WithLabelValues(rule).Add(float64(r.Rules[rule]))
	}
}

// matchStatement matches the non-manual ports of an account ($1) in
// organization $2 and returns each with its rule. The CI and interface
// reads run under the caller's row level security.
const matchStatement = `WITH ports AS (
		SELECT p.id, lower(p.node_name) AS node, p.unit_index, p.port_index FROM admp_access_port p
		WHERE p.account_id = $1 AND p.organization_id = $2 AND p.matched_by <> 'manual'),
	by_host AS (
		SELECT p.id AS port_id, min(c.id::text) AS ci_id, count(*) AS n FROM ports p
		JOIN ci c ON c.organization_id = $2 AND c.hostname_lc = p.node AND c.deleted_at IS NULL
		GROUP BY p.id),
	by_label AS (
		SELECT p.id AS port_id, min(c.id::text) AS ci_id, count(*) AS n FROM ports p
		JOIN ci c ON c.organization_id = $2 AND split_part(c.hostname_lc, '.', 1) = p.node AND c.deleted_at IS NULL
		WHERE NOT EXISTS (SELECT 1 FROM by_host h WHERE h.port_id = p.id)
		GROUP BY p.id),
	device AS (
		SELECT port_id, ci_id::uuid AS ci_id, 'hostname' AS rule FROM by_host WHERE n = 1
		UNION ALL
		SELECT port_id, ci_id::uuid, 'hostname_label' FROM by_label WHERE n = 1),
	iface AS (
		SELECT d.port_id, min(ni.id::text) AS ni_id, count(*) AS n FROM device d
		JOIN ports p ON p.id = d.port_id
		JOIN network_interface ni ON ni.ci_id = d.ci_id AND ni.organization_id = $2
		WHERE regexp_replace(lower(ni.name), '\s', '', 'g') ~ ('(^|[^0-9])' || p.unit_index::text || '/' || p.port_index::text || '$')
		GROUP BY d.port_id),
	matched AS (
		SELECT i.port_id, i.ni_id::uuid AS ni_id, d.rule FROM iface i JOIN device d ON d.port_id = i.port_id WHERE i.n = 1)
	UPDATE admp_access_port a SET network_interface_id = m.ni_id,
		ci_id = CASE WHEN m.port_id IS NULL THEN NULL ELSE a.ci_id END,
		matched_by = CASE WHEN m.port_id IS NULL THEN 'none' ELSE 'rule' END
	FROM ports p LEFT JOIN matched m ON m.port_id = p.id
	WHERE a.id = p.id
	RETURNING a.external_id, COALESCE(m.rule, 'none'), a.node_name, a.unit_index, a.port_index`
