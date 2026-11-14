package probe

import (
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
)

// Mapping of an entity (a sensor of ENTITY-SENSOR-MIB or of a vendor MIB
// indexed by entPhysicalIndex) to its interface (WP-1004 AK2): from the
// sensor up entPhysicalContainedIn (ENTITY-MIB, RFC 6933) to the first
// entity whose entAliasMappingIdentifier points to an ifIndex of IF-MIB,
// at most opticalMaxDepth levels above the sensor.

// ENTITY-MIB and IF-MIB OIDs of the mapping.
const (
	oidEntPhysicalDescr       = "1.3.6.1.2.1.47.1.1.1.1.2"
	oidEntPhysicalContainedIn = "1.3.6.1.2.1.47.1.1.1.1.4"
	oidEntPhysicalName        = "1.3.6.1.2.1.47.1.1.1.1.7"
	// entAliasMappingIdentifier, index entPhysicalIndex.entAliasLogicalIndexOrZero.
	oidEntAliasMappingIdentifier = "1.3.6.1.2.1.47.1.3.2.1.2"
	// oidIfIndexColumn is the ifIndex column of ifTable; an alias mapping
	// identifier is ifIndex.<n> of it.
	oidIfIndexColumn = "1.3.6.1.2.1.2.2.1.1"
	oidIfName        = "1.3.6.1.2.1.31.1.1.1.1"
)

// opticalMaxDepth is the most containment levels between a sensor and the
// entity mapped to its interface.
const opticalMaxDepth = 4

// opticalMaxEntities bounds the walk of the entity tables.
const opticalMaxEntities = 16384

// entityMap is the containment and interface mapping of a device.
type entityMap struct {
	parent  map[int]int
	name    map[int]string
	descr   map[int]string
	ifIndex map[int]int // entity → ifIndex (entAliasMappingTable)
	ifName  map[int]string
	// truncated is true when one of the walks was cut at
	// opticalMaxEntities.
	truncated bool
}

// readEntities walks entPhysicalTable (descr, containedIn, name),
// entAliasMappingTable and ifName; a cut walk sets truncated.
func readEntities(s *snmp.Session) (*entityMap, error) {
	m := &entityMap{parent: map[int]int{}, name: map[int]string{}, descr: map[int]string{}, ifIndex: map[int]int{},
		ifName: map[int]string{}}
	rows, err := s.WalkColumns([]string{oidEntPhysicalContainedIn, oidEntPhysicalName, oidEntPhysicalDescr}, opticalMaxEntities)
	if err != nil {
		return nil, err
	}
	m.truncated = rows.Truncated
	for _, row := range rows.Rows {
		idx, _, ierr := snmp.IndexInt(row.Index)
		if ierr != nil {
			continue
		}
		if v, ok := row.Value(oidEntPhysicalContainedIn); ok {
			if p, ok := pduInt(v); ok {
				m.parent[idx] = int(p)
			}
		}
		if v, ok := row.Value(oidEntPhysicalName); ok {
			m.name[idx] = pduString(v)
		}
		if v, ok := row.Value(oidEntPhysicalDescr); ok {
			m.descr[idx] = pduString(v)
		}
	}
	alias, err := s.WalkColumns([]string{oidEntAliasMappingIdentifier}, opticalMaxEntities)
	if err != nil {
		return nil, err
	}
	m.truncated = m.truncated || alias.Truncated
	for _, row := range alias.Rows {
		idx, _, ierr := snmp.IndexInt(row.Index)
		if ierr != nil {
			continue
		}
		v, _ := row.Value(oidEntAliasMappingIdentifier)
		target, _ := v.Value.(string)
		rest, ok := strings.CutPrefix(strings.TrimPrefix(target, "."), oidIfIndexColumn+".")
		if !ok {
			continue
		}
		if n, perr := strconv.Atoi(rest); perr == nil && n > 0 {
			if _, seen := m.ifIndex[idx]; !seen {
				m.ifIndex[idx] = n
			}
		}
	}
	names, err := s.WalkColumns([]string{oidIfName}, opticalMaxEntities)
	if err != nil {
		return nil, err
	}
	m.truncated = m.truncated || names.Truncated
	for _, row := range names.Rows {
		if idx, _, ierr := snmp.IndexInt(row.Index); ierr == nil {
			v, _ := row.Value(oidIfName)
			m.ifName[idx] = pduString(v)
		}
	}
	return m, nil
}

// interfaceOf returns the ifIndex of the entity: the entity itself or one
// of its containers up to opticalMaxDepth levels above it.
func (m *entityMap) interfaceOf(entity int) (int, bool) {
	cur := entity
	for range opticalMaxDepth + 1 {
		if n, ok := m.ifIndex[cur]; ok {
			return n, true
		}
		p, ok := m.parent[cur]
		if !ok || p == 0 || p == cur {
			return 0, false
		}
		cur = p
	}
	return 0, false
}

// label returns the name and description of an entity for the quantity
// patterns.
func (m *entityMap) label(entity int) string {
	return m.name[entity] + " " + m.descr[entity]
}
