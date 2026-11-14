package profiles

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The profile section monitor (PRT-20, WP-1000, E-187 option 1): the
// vendor-dependent probe types (routing v2 tables, BFD, optics, BNG, PON)
// read their tables, index kinds, enum mappings, scaling and threshold
// columns from the profile of the device instead of a release. The key of
// the section is a probe type of the register (probewire.Types); a profile
// from RETICORA_PROFILES_DIR with the same name replaces the shipped one,
// its monitor section included. Without a matching profile a type uses its
// standard-MIB core.

// MonitorSpec is the monitoring data of one probe type in a profile.
type MonitorSpec struct {
	// Tables are the SNMP tables of the type by logical name.
	Tables map[string]MonitorTable `json:"tables" yaml:"tables"`
}

// MonitorTable is one SNMP table of a monitor section.
type MonitorTable struct {
	// Columns maps a logical column name to its column OID.
	Columns map[string]string `json:"columns" yaml:"columns"`
	// Index is the index kind of the table rows: int, ipv4, ipv6, inet
	// (InetAddressType + InetAddress) or composite:[kind,kind,…].
	Index string `json:"index" yaml:"index"`
	// Enums maps a logical column to its value mapping: raw value → up,
	// degraded, down or a raw value name.
	Enums map[string]map[string]string `json:"enums,omitempty" yaml:"enums,omitempty"`
	// Scale multiplies the numeric values (absent is 1; 0 or below is
	// refused).
	Scale *float64 `json:"scale,omitempty" yaml:"scale,omitempty"`
	// Unit names the unit of the scaled values.
	Unit string `json:"unit,omitempty" yaml:"unit,omitempty"`
	// ThresholdColumns maps a logical threshold (e.g. rx_power_low_alarm)
	// to the logical column that holds it.
	ThresholdColumns map[string]string `json:"threshold_columns,omitempty" yaml:"threshold_columns,omitempty"`
	// Patterns maps a logical quantity (e.g. rx, tx of the optical type)
	// to an RE2 expression matched against the entity name or description
	// of a row (WP-1004).
	Patterns map[string]string `json:"patterns,omitempty" yaml:"patterns,omitempty"`
}

// Index kinds of a monitor table.
const (
	IndexInt  = "int"
	IndexIPv4 = "ipv4"
	IndexIPv6 = "ipv6"
	IndexInet = "inet"
)

// simpleIndexKinds are the index kinds of a single index component.
var simpleIndexKinds = map[string]bool{IndexInt: true, IndexIPv4: true, IndexIPv6: true, IndexInet: true}

// Enum states a raw value may map to.
var enumStates = map[string]bool{"up": true, "degraded": true, "down": true}

// oidPattern is a numeric OID with at least two arcs, optionally with a
// leading dot.
var oidPattern = regexp.MustCompile(`^\.?\d+(\.\d+)+$`)

// rawValueName is the name a raw value may map to instead of a state.
var rawValueName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// ParseIndex splits an index kind into its components: int → [int],
// composite:[int,inet] → [int inet]. An unknown kind is an error.
func ParseIndex(index string) ([]string, error) {
	index = strings.TrimSpace(index)
	if simpleIndexKinds[index] {
		return []string{index}, nil
	}
	inner, ok := strings.CutPrefix(index, "composite:")
	if !ok {
		return nil, fmt.Errorf("unknown index kind %q", index)
	}
	inner = strings.TrimSpace(inner)
	if !strings.HasPrefix(inner, "[") || !strings.HasSuffix(inner, "]") {
		return nil, fmt.Errorf("composite index %q must list its kinds in brackets", index)
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(inner, "["), "]"), ",")
	if len(parts) < 2 {
		return nil, fmt.Errorf("composite index %q needs at least two kinds", index)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !simpleIndexKinds[p] {
			return nil, fmt.Errorf("composite index %q: unknown kind %q", index, p)
		}
		out = append(out, p)
	}
	return out, nil
}

// validateMonitor checks the monitor section of a profile: a known probe
// type per key, at least one table, valid column OIDs, a known index kind,
// enums and thresholds on known columns, a scale above 0 when set. The
// error names the profile and the path of the failing value
// (monitor.bgp.tables.peers.columns.state).
func (p *Profile) validateMonitor() error {
	types := sortedKeys(p.Monitor)
	for _, probeType := range types {
		path := "monitor." + probeType
		if _, ok := probewire.Lookup(probeType); !ok {
			return fmt.Errorf("profiles: %s: %s: unknown probe type %q", p.Name, path, probeType)
		}
		spec := p.Monitor[probeType]
		if len(spec.Tables) == 0 {
			return fmt.Errorf("profiles: %s: %s.tables: at least one table is required", p.Name, path)
		}
		for _, name := range sortedKeys(spec.Tables) {
			if err := spec.Tables[name].validate(path + ".tables." + name); err != nil {
				return fmt.Errorf("profiles: %s: %w", p.Name, err)
			}
		}
	}
	return nil
}

func (t MonitorTable) validate(path string) error { //nolint:gocritic // value of the map
	if len(t.Columns) == 0 {
		return fmt.Errorf("%s.columns: at least one column is required", path)
	}
	for _, col := range sortedKeys(t.Columns) {
		if strings.TrimSpace(col) == "" {
			return fmt.Errorf("%s.columns: empty column name", path)
		}
		if !oidPattern.MatchString(strings.TrimSpace(t.Columns[col])) {
			return fmt.Errorf("%s.columns.%s: invalid OID %q", path, col, t.Columns[col])
		}
	}
	if _, err := ParseIndex(t.Index); err != nil {
		return fmt.Errorf("%s.index: %w", path, err)
	}
	for _, col := range sortedKeys(t.Enums) {
		if _, ok := t.Columns[col]; !ok {
			return fmt.Errorf("%s.enums.%s: unknown column", path, col)
		}
		for _, raw := range sortedKeys(t.Enums[col]) {
			// MapEnum looks raw values up in their canonical decimal form;
			// any other spelling ("06", "+1", "established") never matches.
			if v, err := strconv.ParseInt(raw, 10, 64); err != nil || strconv.FormatInt(v, 10) != raw {
				return fmt.Errorf("%s.enums.%s.%s: raw value must be an integer", path, col, raw)
			}
			target := t.Enums[col][raw]
			if !enumStates[target] && !rawValueName.MatchString(target) {
				return fmt.Errorf("%s.enums.%s.%s: %q is no state (up, degraded, down) or value name", path, col, raw, target)
			}
		}
	}
	if t.Scale != nil && (!(*t.Scale > 0) || math.IsInf(*t.Scale, 0)) {
		return fmt.Errorf("%s.scale: must be a finite number above 0", path)
	}
	for _, name := range sortedKeys(t.Patterns) {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s.patterns: empty quantity name", path)
		}
		if _, err := regexp.Compile(t.Patterns[name]); err != nil {
			return fmt.Errorf("%s.patterns.%s: invalid RE2 expression", path, name)
		}
	}
	for _, threshold := range sortedKeys(t.ThresholdColumns) {
		if _, ok := t.Columns[t.ThresholdColumns[threshold]]; !ok {
			return fmt.Errorf("%s.threshold_columns.%s: unknown column %q", path, threshold, t.ThresholdColumns[threshold])
		}
	}
	return nil
}

// Factor is the scale of the table's values (1 without scale).
func (t MonitorTable) Factor() float64 { //nolint:gocritic // value of the map
	if t.Scale == nil {
		return 1
	}
	return *t.Scale
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MonitorFor returns the monitor section of probeType of the profile the
// device matches, with the profile's name: the same SNMP classification as
// the discovery (longest sysObjectID prefix; a matching sysDescrPattern
// beats a longer prefix, a non-matching one excludes the profile), among
// the profiles with a section for the type. Without a match it reports
// false and the type uses its standard-MIB core.
func (r *Registry) MonitorFor(probeType, sysObjectID, sysDescr string) (MonitorSpec, string, bool) {
	oid := strings.TrimSpace(sysObjectID)
	if oid == "" && sysDescr == "" {
		return MonitorSpec{}, "", false
	}
	r.mu.RLock()
	candidates := make([]Profile, 0, len(r.profiles))
	for name := range r.profiles {
		if _, ok := r.profiles[name].Monitor[probeType]; ok {
			candidates = append(candidates, r.profiles[name])
		}
	}
	r.mu.RUnlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	var best *Profile
	bestScore := -1
	for i := range candidates {
		c, err := compile(&candidates[i])
		if err != nil {
			continue
		}
		if score, ok := snmpScore(c, oid, sysDescr); ok && score > bestScore {
			best, bestScore = &candidates[i], score
		}
	}
	if best == nil {
		return MonitorSpec{}, "", false
	}
	return best.Monitor[probeType], best.Name, true
}

// snmpScore is the score of classifySNMP for one profile: the length of the
// longest matching sysObjectID prefix, plus 1000 for a matching sysDescr
// pattern; ok is false when the profile is no candidate.
func snmpScore(c *compiledProfile, oid, descr string) (score int, ok bool) {
	prefix := 0
	for _, candidate := range c.profile.SysObjectIDs {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && oid != "" && oidMatches(candidate, oid) && len(candidate) > prefix {
			prefix = len(candidate)
		}
	}
	switch {
	case c.sysDescr != nil && descr != "" && c.sysDescr.MatchString(descr):
		return prefix + 1000, true
	case c.sysDescr != nil, prefix == 0:
		return 0, false
	}
	return prefix, true
}

// ColumnOID returns the OID of a logical column (without leading dot).
func (t MonitorTable) ColumnOID(column string) (string, bool) { //nolint:gocritic // value of the map
	oid, ok := t.Columns[column]
	return strings.TrimPrefix(strings.TrimSpace(oid), "."), ok
}

// MapEnum maps a raw value of a logical column through the table's enums;
// ok is false when the column has no mapping for the value.
func (t MonitorTable) MapEnum(column string, raw int64) (string, bool) { //nolint:gocritic // value of the map
	v, ok := t.Enums[column][strconv.FormatInt(raw, 10)]
	return v, ok
}
