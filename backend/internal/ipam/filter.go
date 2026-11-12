package ipam

import (
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Filters of the subnet and address lists (IPM-20, WP-909). Every filter
// is joined with AND and goes into the statement as a parameter. A
// malformed id is 422 with its field (the PostgreSQL repository checks the
// UUIDs; the memory repository has ids of its own), an unknown one an
// empty list. The statements use B-tree indexes on columns under row level
// security (Konvention 22): subnet by idx_subnet_org_vrf_parent and
// idx_subnet_org_section, ip_address by idx_ip_address_org_state and the
// indexes on subnet_id and interface_id.

// subnetFilter reads the filters of GET /subnets: client_id, site_id,
// parent_id (root: the subnets without a visible parent, IPM-02),
// section_id, vrf_id, vlan_id, is_pool, q and contains; is_pool that is
// no boolean and contains that is no address are 422.
func subnetFilter(w http.ResponseWriter, r *http.Request) (SubnetFilter, bool) {
	q := r.URL.Query()
	f := SubnetFilter{
		ClientID:  q.Get("client_id"),
		SiteID:    q.Get("site_id"),
		SectionID: q.Get("section_id"),
		VRFID:     q.Get("vrf_id"),
		VLANID:    q.Get("vlan_id"),
		Q:         strings.TrimSpace(q.Get("q")),
	}
	switch parent := q.Get("parent_id"); parent {
	case "":
	case "root":
		f.Root = true
	default:
		f.ParentID = parent
	}
	if v := q.Get("is_pool"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			api.WriteFieldError(w, &api.FieldError{Field: "is_pool", Message: "is_pool must be true or false",
				Status: http.StatusUnprocessableEntity})
			return f, false
		}
		f.IsPool = &b
	}
	if v := q.Get("contains"); v != "" {
		addr, err := validateIP(v)
		if err != nil {
			api.WriteFieldError(w, &api.FieldError{Field: "contains", Message: "contains must be an IP address",
				Status: http.StatusUnprocessableEntity})
			return f, false
		}
		f.Contains = addr
	}
	return f, true
}

// ipListFilter reads the filters type, client_id, vrf_id, ci_id and q of
// the address lists into f; an unknown type is 422.
func ipListFilter(w http.ResponseWriter, r *http.Request, f *IPFilter) bool {
	q := r.URL.Query()
	if v := q.Get("type"); v != "" {
		if !ValidIPTypes[v] {
			api.WriteFieldError(w, &api.FieldError{Field: "type", Message: "invalid ip address type",
				Status: http.StatusUnprocessableEntity})
			return false
		}
		f.Type = v
	}
	f.ClientID, f.VRFID, f.CIID = q.Get("client_id"), q.Get("vrf_id"), q.Get("ci_id")
	f.Q = strings.TrimSpace(q.Get("q"))
	return true
}

// uuidFields refuses the first named value that is set but no UUID (422).
func uuidFields(fields ...[2]string) error {
	for _, fv := range fields {
		if fv[1] != "" && !api.IsUUID(fv[1]) {
			return &api.FieldError{Field: fv[0], Message: fv[0] + " must be a UUID", Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

// checkSubnetFilterIDs checks the id filters of the subnet list.
func checkSubnetFilterIDs(f *SubnetFilter) error {
	return uuidFields([2]string{"client_id", f.ClientID}, [2]string{"site_id", f.SiteID},
		[2]string{"parent_id", f.ParentID}, [2]string{"section_id", f.SectionID}, [2]string{"vrf_id", f.VRFID}, [2]string{"vlan_id", f.VLANID})
}

// checkIPFilterIDs checks the id filters of the address lists.
func checkIPFilterIDs(f *IPFilter) error {
	return uuidFields([2]string{"subnet_id", f.SubnetID}, [2]string{"client_id", f.ClientID},
		[2]string{"vrf_id", f.VRFID}, [2]string{"ci_id", f.CIID})
}

// likeEscape escapes the wildcards of a LIKE pattern (escape character
// backslash, the default).
func likeEscape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// subnetFilterWhere is the condition, its arguments ($1 the organization)
// and the order of the subnet list under f.
func subnetFilterWhere(orgID string, f *SubnetFilter) (where string, args []any, order string) {
	where = "organization_id = $1"
	args = []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "$?", fmt.Sprintf("$%d", len(args)))
	}
	if f.ClientID != "" {
		add("client_id = $?", f.ClientID)
	}
	if f.SiteID != "" {
		add("site_id = $?", f.SiteID)
	}
	if f.ParentID != "" {
		add("parent_id = $?", f.ParentID)
	}
	if f.Root {
		where += " AND " + subnetRootVisible
	}
	if f.SectionID != "" {
		add("section_id = $?", f.SectionID)
	}
	switch {
	case f.VRFID != "":
		add("vrf_id = $?", f.VRFID)
	case f.Contains != "":
		where += " AND vrf_id = default_vrf_of($1)"
	}
	if f.VLANID != "" {
		add("vlan_id = $?", f.VLANID)
	}
	if f.IsPool != nil {
		add("is_pool = $?", *f.IsPool)
	}
	if f.Q != "" {
		add("(name ILIKE $? OR description ILIKE $? OR cidr::text ILIKE $?)", "%"+likeEscape(f.Q)+"%")
	}
	order = "created_at DESC"
	if f.Contains != "" {
		add("cidr >>= $?::inet", f.Contains)
		order = "masklen(cidr) DESC, created_at DESC"
	}
	return where, args, order
}

// ipFilterWhere is the condition of the address lists under f and its
// arguments ($1 the organization).
func ipFilterWhere(orgID string, f *IPFilter) (where string, args []any) {
	where = "organization_id = $1 AND " + ipVisible
	args = []any{orgID}
	add := func(cond string, v ...any) {
		for _, a := range v {
			args = append(args, a)
			cond = strings.Replace(cond, "$?", fmt.Sprintf("$%d", len(args)), 1)
		}
		where += " AND " + cond
	}
	if f.SubnetID != "" {
		add("subnet_id = $?", f.SubnetID)
	}
	if f.State != "" {
		add("state = $?", f.State)
	}
	if f.IsGateway != nil {
		add("is_gateway = $?", *f.IsGateway)
	}
	if f.Type != "" {
		add("type = $?", f.Type)
	}
	if f.ClientID != "" {
		add("client_id = $?", f.ClientID)
	}
	if f.VRFID != "" {
		add("vrf_id = $?", f.VRFID)
	}
	if f.CIID != "" {
		// Until the direct binding (WP-911): the addresses of the
		// interfaces of the CI (idx_network_interface_ci,
		// idx_ip_address_interface).
		add(`interface_id IN (SELECT n.id FROM network_interface n
			WHERE n.organization_id = $1 AND n.ci_id = $?)`, f.CIID)
	}
	if f.Q != "" {
		// host() writes IPv6 in lower case: the prefix is compared in
		// lower case as well (a B-tree prefix match, no ILIKE).
		q := likeEscape(f.Q)
		add("(address_host LIKE $? OR dns_name ILIKE $?)", strings.ToLower(q)+"%", "%"+q+"%")
	}
	return where, args
}

// --- Memory ---

// subnetMatches applies the filters of IPM-20 to s.
func (r *MemoryRepository) subnetMatches(s *Subnet, f *SubnetFilter) bool {
	if (f.SectionID != "" && s.SectionID != f.SectionID) || (f.VLANID != "" && s.VLANID != f.VLANID) ||
		(f.IsPool != nil && s.IsPool != *f.IsPool) {
		return false
	}
	vrfID := f.VRFID
	if vrfID == "" && f.Contains != "" {
		vrfID = r.defaultVRFID(s.OrganizationID)
	}
	if vrfID != "" && s.VRFID != vrfID {
		return false
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		if !strings.Contains(strings.ToLower(s.Name), q) && !strings.Contains(strings.ToLower(s.Description), q) &&
			!strings.Contains(strings.ToLower(s.CIDR), q) {
			return false
		}
	}
	if f.Contains != "" {
		p, err := netip.ParsePrefix(s.CIDR)
		addr, aerr := netip.ParseAddr(f.Contains)
		if err != nil || aerr != nil || !p.Contains(addr) {
			return false
		}
	}
	return true
}

// defaultVRFID is the id of the default VRF of the organization, "" when
// it has none yet (no write under a read lock).
func (r *MemoryRepository) defaultVRFID(orgID string) string {
	for _, v := range r.vrfs {
		if v.OrganizationID == orgID && v.IsDefault {
			return v.ID
		}
	}
	return ""
}

// sortNarrowestFirst orders subnets by their prefix length, longest first.
func sortNarrowestFirst(out []Subnet) {
	bits := func(cidr string) int {
		if p, err := netip.ParsePrefix(cidr); err == nil {
			return p.Bits()
		}
		return -1
	}
	sort.SliceStable(out, func(i, j int) bool { return bits(out[i].CIDR) > bits(out[j].CIDR) })
}

// addressMatches applies the filters of IPM-20 to a: the client of an
// address is derived as derive_ip_scope (000061) does, from its interface
// when it has one, else from its subnet; the memory repository keeps no
// CIs, so an address on an interface has the client of an org-wide CI
// (none). Its CI is the one of its interface.
func (r *MemoryRepository) addressMatches(a *IPAddress, f *IPFilter) bool {
	if (f.Type != "" && a.Type != f.Type) || (f.VRFID != "" && a.VRFID != f.VRFID) {
		return false
	}
	if f.ClientID != "" {
		if a.InterfaceID != "" {
			return false
		}
		sn, ok := r.subnets[a.SubnetID]
		if !ok || sn.ClientID != f.ClientID {
			return false
		}
	}
	if f.CIID != "" {
		ni, ok := r.interfaces[a.InterfaceID]
		if !ok || ni.CIID != f.CIID {
			return false
		}
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		if !strings.HasPrefix(hostOf(a.Address), q) && !strings.Contains(strings.ToLower(a.DNSName), q) {
			return false
		}
	}
	return true
}
