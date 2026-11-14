package importer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// IPAM import (IPM-09, WP-920): subnets and IP addresses from CSV files
// with the column names of phpIPAM. A profile of the entity subnet or
// ip_address maps the columns of the file onto the targets below (in a
// phpIPAM export the column names equal the targets). Every row runs in its
// own transaction through ipam.Repository, so hierarchy, the lock per VRF
// and the longest-prefix assignment (WP-901, WP-912) apply as for the API;
// the history rows of the row carry source import (WP-908). A dry run
// rolls every row back.

// Targets of a subnet row (phpIPAM subnets export).
const (
	// SubnetSectionTarget is the name of the IPAM section; empty is the
	// default section.
	SubnetSectionTarget = "section"
	// SubnetAddressTarget is the network address of the subnet (phpIPAM
	// column subnet); with SubnetMaskTarget it gives the prefix.
	SubnetAddressTarget = "subnet"
	// SubnetMaskTarget is the prefix length or the dotted netmask.
	SubnetMaskTarget = "mask"
	// SubnetCIDRTarget is the prefix in CIDR notation (instead of subnet
	// and mask, or beside them when both agree).
	SubnetCIDRTarget = "cidr"
	// IPAMDescriptionTarget is the description of a subnet or an address.
	IPAMDescriptionTarget = "description"
	// SubnetVLANTarget is the VLAN number; several VLANs with the number
	// (per site or domain) are a row error.
	SubnetVLANTarget = "vlan"
	// IPAMVRFTarget is the name of the VRF; empty is the default VRF.
	IPAMVRFTarget = "vrf"
	// SubnetGatewayTarget is the gateway address of the subnet.
	SubnetGatewayTarget = "gateway"
	// SubnetCustomerTarget is the client by name, slug or external
	// reference (customer number).
	SubnetCustomerTarget = "customer"
	// SubnetNameserverTarget is the name of a nameserver set.
	SubnetNameserverTarget = "nameserver"
	// SubnetPoolTarget marks a container without own addresses (phpIPAM
	// isPool/isFolder).
	SubnetPoolTarget = "is_pool"
)

// Targets of an address row (phpIPAM addresses export).
const (
	AddressIPTarget        = "ip_addr"
	AddressHostnameTarget  = "hostname"
	AddressMACTarget       = "mac"
	AddressStateTarget     = "state"
	AddressIsGatewayTarget = "is_gateway"
	// AddressSubnetTarget is the CIDR of the subnet of the address in its
	// VRF; empty assigns the narrowest subnet (WP-912).
	AddressSubnetTarget = "subnet"
	AddressTypeTarget   = "type"
)

// SubnetTargets are the targets of the entity subnet, in the order of an
// export (WP-921).
var SubnetTargets = []string{SubnetSectionTarget, SubnetCIDRTarget, IPAMDescriptionTarget, SubnetVLANTarget,
	IPAMVRFTarget, SubnetGatewayTarget, SubnetCustomerTarget, SubnetNameserverTarget}

// subnetInputTargets are the targets a subnet row may carry beside them.
var subnetInputTargets = append(slices.Clone(SubnetTargets), SubnetAddressTarget, SubnetMaskTarget, SubnetPoolTarget)

// AddressTargets are the targets of the entity ip_address, in the order of
// an export (WP-921).
var AddressTargets = []string{AddressIPTarget, AddressHostnameTarget, IPAMDescriptionTarget, AddressMACTarget,
	AddressStateTarget, AddressIsGatewayTarget, IPAMVRFTarget, AddressSubnetTarget, AddressTypeTarget}

// Report codes of the IPAM rows.
const (
	// CodeInterfaceNotFound is the warning of a mac without exactly one
	// interface in the organization: the address stays unbound.
	CodeInterfaceNotFound = "interface_not_found"
	// CodePinnedKept names the fields a pinned address kept (IPM-14).
	CodePinnedKept = "pinned_kept"
	// CodeReferenceAmbiguous is a name that matches several objects (VLAN
	// number, client).
	CodeReferenceAmbiguous = "ambiguous_reference"
)

// IPAMStore resolves the references and identities of IPAM rows under the
// row level security of the run (in the transaction of the row).
// PGRepository implements it.
type IPAMStore interface {
	// FindVRFs returns the VRFs named name (case-insensitive); an empty
	// name is the default VRF.
	FindVRFs(ctx context.Context, orgID, name string) ([]string, error)
	FindIPAMSections(ctx context.Context, orgID, name string) ([]string, error)
	// FindClients returns the clients with the name, slug or external
	// reference.
	FindClients(ctx context.Context, orgID, ref string) ([]string, error)
	// FindVLANs returns the VLANs with the number, org-wide or of the
	// client (empty: any client).
	FindVLANs(ctx context.Context, orgID string, number int, clientID string) ([]string, error)
	FindNameserverSets(ctx context.Context, orgID, name string) ([]string, error)
	FindSubnets(ctx context.Context, orgID, vrfID, cidr string) ([]string, error)
	// FindAddressRows returns the rows of the address in the VRF.
	FindAddressRows(ctx context.Context, orgID, vrfID, host string) ([]AddressRow, error)
	// FindInterfacesByMAC returns the interfaces with the MAC address.
	FindInterfacesByMAC(ctx context.Context, orgID, mac string) ([]string, error)
	// MarkImported sets origin import on an address the import created.
	MarkImported(ctx context.Context, orgID, id string) error
	// RecordRowAudit registers the audit entry of a row (EntityStore).
	RecordRowAudit(ctx context.Context, entry *audit.Entry) error
}

// AddressRow is a stored row of an address in its VRF.
type AddressRow struct {
	ID          string
	InterfaceID string
	Pinned      bool
}

// ChangeSourceSetter names the source of the IPAM history rows of the row
// transaction (ipam.PGRepository.SetChangeSource).
type ChangeSourceSetter interface {
	SetChangeSource(ctx context.Context, orgID, source string) error
}

var _ IPAMStore = (*PGRepository)(nil)

// --- parsing (no database) ---

// parseMask reads a prefix length (24) or a dotted IPv4 netmask
// (255.255.255.0); dotted reports the second form, which only fits an IPv4
// address.
func parseMask(text string) (bits int, dotted bool, err error) {
	if n, err := strconv.Atoi(text); err == nil {
		if n < 0 || n > 128 {
			return 0, false, fmt.Errorf("the prefix length %d is not between 0 and 128", n)
		}
		return n, false, nil
	}
	ip := net.ParseIP(text).To4()
	if ip == nil {
		return 0, false, fmt.Errorf("%q is neither a prefix length nor a netmask", text)
	}
	ones, size := net.IPMask(ip).Size()
	if size == 0 {
		return 0, false, fmt.Errorf("the netmask %s is not contiguous", text)
	}
	return ones, true, nil
}

// maskFor reads the mask of a subnet row for the address addr: a dotted
// netmask only for IPv4 (phpIPAM gives IPv6 masks as prefix lengths) and a
// prefix length of at most 32 (IPv4) or 128 (IPv6).
func maskFor(addr netip.Addr, maskText string) (int, error) {
	bits, dotted, err := parseMask(maskText)
	if err != nil {
		return 0, err
	}
	if dotted && !addr.Is4() {
		return 0, fmt.Errorf("the netmask %s is IPv4 notation, but %s is an IPv6 address", maskText, addr)
	}
	if bits > addr.BitLen() {
		return 0, fmt.Errorf("the prefix length %d exceeds %d bits of %s", bits, addr.BitLen(), addr)
	}
	return bits, nil
}

// normalizePrefix returns the prefix in canonical form (network address).
func normalizePrefix(text string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(text))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is no prefix in CIDR notation", text)
	}
	return p.Masked(), nil
}

// subnetPrefix reads the prefix of a subnet row from cidr or subnet with
// mask; both given must name the same network. field names the target of
// an error.
func subnetPrefix(v *MappedRow) (netip.Prefix, string, error) {
	cidrText := rowText(v, SubnetCIDRTarget)
	addrText := rowText(v, SubnetAddressTarget)
	maskText := rowText(v, SubnetMaskTarget)
	var fromParts netip.Prefix
	if addrText != "" {
		if strings.Contains(addrText, "/") {
			p, err := normalizePrefix(addrText)
			if err != nil {
				return netip.Prefix{}, SubnetAddressTarget, err
			}
			// A mask beside a subnet in CIDR notation must agree with it.
			if maskText != "" {
				bits, err := maskFor(p.Addr(), maskText)
				if err != nil {
					return netip.Prefix{}, SubnetMaskTarget, err
				}
				if bits != p.Bits() {
					return netip.Prefix{}, SubnetMaskTarget, fmt.Errorf("subnet %s and mask %s contradict each other", addrText, maskText)
				}
			}
			fromParts = p
		} else {
			if maskText == "" {
				return netip.Prefix{}, SubnetMaskTarget, errors.New("the mask of the subnet is required (or the cidr)")
			}
			addr, err := netip.ParseAddr(addrText)
			if err != nil {
				return netip.Prefix{}, SubnetAddressTarget, fmt.Errorf("%q is no IP address", addrText)
			}
			bits, err := maskFor(addr, maskText)
			if err != nil {
				return netip.Prefix{}, SubnetMaskTarget, err
			}
			fromParts = netip.PrefixFrom(addr.WithZone(""), bits).Masked()
		}
	}
	if cidrText == "" {
		if !fromParts.IsValid() {
			return netip.Prefix{}, SubnetCIDRTarget, errors.New("the subnet is required: cidr, or subnet with mask")
		}
		return fromParts, "", nil
	}
	p, err := normalizePrefix(cidrText)
	if err != nil {
		return netip.Prefix{}, SubnetCIDRTarget, err
	}
	if fromParts.IsValid() && fromParts != p {
		return netip.Prefix{}, SubnetCIDRTarget, fmt.Errorf("cidr %s and subnet with mask %s contradict each other", p, fromParts)
	}
	return p, "", nil
}

// parseFlag reads a boolean column of phpIPAM (1/0, true/false, yes/no,
// ja/nein); empty is unset.
func parseFlag(text string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return nil, nil
	case "1", "true", "yes", "y", "ja", "x":
		v := true
		return &v, nil
	case "0", "false", "no", "n", "nein":
		v := false
		return &v, nil
	}
	return nil, fmt.Errorf("%q is no yes/no value (1, 0, true, false, yes, no)", text)
}

// mapAddressState maps the state column onto state and type of an address
// (IPM-04, E-211): the states active, offline and unknown, the phpIPAM tags
// Used → active, Offline → offline, Reserved → type reserved, DHCP → type
// dhcp, also as tag ids 1 to 4 of a phpIPAM export.
func mapAddressState(text string) (state, typ string, err error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return "", "", nil
	case "active", "used", "2":
		return "active", "", nil
	case "offline", "1":
		return "offline", "", nil
	case "unknown":
		return "unknown", "", nil
	case "reserved", "3":
		return "", "reserved", nil
	case "dhcp", "4":
		return "", "dhcp", nil
	}
	return "", "", fmt.Errorf("%q is no state (active, offline, unknown or the phpIPAM tags Used, Offline, Reserved, DHCP)", text)
}

// importTypes are the types an import sets; discovered is the type of
// discovery.
var importTypes = []string{"static", "reserved", "dhcp"}

// subnetRow is a subnet row after its parsing.
type subnetRow struct {
	prefix      netip.Prefix
	section     string
	description *string
	vlan        int
	vrf         string
	gateway     string
	customer    string
	nameserver  string
	pool        *bool
}

// parseSubnetRow reads the targets of a subnet row.
func parseSubnetRow(v *MappedRow) (*subnetRow, *Issue) {
	for target := range v.Values {
		if !slices.Contains(subnetInputTargets, target) {
			return nil, &Issue{Field: target, Code: CodeInvalidValue, Message: fmt.Sprintf("%s is no field of a subnet", target)}
		}
	}
	p, field, err := subnetPrefix(v)
	if err != nil {
		return nil, &Issue{Field: field, Code: CodeInvalidValue, Message: err.Error()}
	}
	r := &subnetRow{prefix: p, section: rowText(v, SubnetSectionTarget), vrf: rowText(v, IPAMVRFTarget),
		customer: rowText(v, SubnetCustomerTarget), nameserver: rowText(v, SubnetNameserverTarget)}
	if _, ok := v.Values[IPAMDescriptionTarget]; ok {
		d := rowText(v, IPAMDescriptionTarget)
		r.description = &d
	}
	if text := rowText(v, SubnetVLANTarget); text != "" {
		n, aerr := strconv.Atoi(text)
		if aerr != nil || n < 1 || n > 4094 {
			return nil, &Issue{Field: SubnetVLANTarget, Code: CodeInvalidValue, Message: fmt.Sprintf("%q is no VLAN number (1 to 4094)", text)}
		}
		r.vlan = n
	}
	if text := rowText(v, SubnetGatewayTarget); text != "" {
		gw, perr := netip.ParseAddr(text)
		if perr != nil || !p.Contains(gw) {
			return nil, &Issue{Field: SubnetGatewayTarget, Code: CodeInvalidValue,
				Message: fmt.Sprintf("the gateway %q is no address of %s", text, p)}
		}
		r.gateway = gw.String()
	}
	pool, err := parseFlag(rowText(v, SubnetPoolTarget))
	if err != nil {
		return nil, &Issue{Field: SubnetPoolTarget, Code: CodeInvalidValue, Message: err.Error()}
	}
	r.pool = pool
	return r, nil
}

// addressRow is an address row after its parsing.
type addressRow struct {
	addr        netip.Addr
	hostname    *string
	description *string
	mac         string
	state       string
	typ         string
	isGateway   *bool
	vrf         string
	subnet      string
}

// parseAddressRow reads the targets of an address row.
func parseAddressRow(v *MappedRow) (*addressRow, *Issue) {
	refuse := func(field, format string, args ...any) (*addressRow, *Issue) {
		return nil, &Issue{Field: field, Code: CodeInvalidValue, Message: fmt.Sprintf(format, args...)}
	}
	for target := range v.Values {
		if !slices.Contains(AddressTargets, target) {
			return refuse(target, "%s is no field of an IP address", target)
		}
	}
	text := rowText(v, AddressIPTarget)
	if text == "" {
		return refuse(AddressIPTarget, "the address is required (its identity)")
	}
	// A phpIPAM export may carry the prefix length of the subnet.
	host, _, _ := strings.Cut(text, "/")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return refuse(AddressIPTarget, "%q is no IP address", text)
	}
	r := &addressRow{addr: addr.Unmap(), vrf: rowText(v, IPAMVRFTarget)}
	if _, ok := v.Values[AddressHostnameTarget]; ok {
		h := rowText(v, AddressHostnameTarget)
		r.hostname = &h
	}
	if _, ok := v.Values[IPAMDescriptionTarget]; ok {
		d := rowText(v, IPAMDescriptionTarget)
		r.description = &d
	}
	if m := rowText(v, AddressMACTarget); m != "" {
		hw, merr := net.ParseMAC(m)
		if merr != nil || len(hw) != 6 {
			return refuse(AddressMACTarget, "%q is no MAC address", m)
		}
		r.mac = hw.String()
	}
	state, stateType, err := mapAddressState(rowText(v, AddressStateTarget))
	if err != nil {
		return refuse(AddressStateTarget, "%s", err.Error())
	}
	r.state = state
	typ := strings.ToLower(rowText(v, AddressTypeTarget))
	if typ != "" && !slices.Contains(importTypes, typ) {
		return refuse(AddressTypeTarget, "%q is no type an import sets (static, reserved, dhcp)", typ)
	}
	if stateType != "" && typ != "" && stateType != typ {
		return refuse(AddressStateTarget, "the state means type %s, the column type says %s", stateType, typ)
	}
	if typ == "" {
		typ = stateType
	}
	r.typ = typ
	if r.isGateway, err = parseFlag(rowText(v, AddressIsGatewayTarget)); err != nil {
		return refuse(AddressIsGatewayTarget, "%s", err.Error())
	}
	if s := rowText(v, AddressSubnetTarget); s != "" {
		p, err := normalizePrefix(s)
		if err != nil {
			return refuse(AddressSubnetTarget, "%s", err.Error())
		}
		if !p.Contains(r.addr) {
			return refuse(AddressSubnetTarget, "the address %s lies outside the subnet %s", r.addr, p)
		}
		r.subnet = p.String()
	}
	return r, nil
}

// --- shared resolution ---

// resolveOne resolves a reference that must name exactly one object.
func resolveOne(ids []string, err error, field, what, value string) (string, *Issue, error) {
	if err != nil {
		return "", nil, err
	}
	switch len(ids) {
	case 0:
		return "", &Issue{Field: field, Code: CodeReferenceNotFound, Message: fmt.Sprintf("no %s %q", what, value)}, nil
	case 1:
		return ids[0], nil, nil
	}
	return "", &Issue{Field: field, Code: CodeReferenceAmbiguous, Message: fmt.Sprintf("%d objects of kind %s match %q", len(ids), what, value)}, nil
}

// ipamRefusal turns a refusal of ipam.Repository into the issues of a row;
// ok is false for an error the report does not explain.
func ipamRefusal(line int, err error) (RowResult, bool) {
	if res, ok := refusalOf(line, err, nil); ok {
		return res, true
	}
	switch {
	case errors.Is(err, ipam.ErrNotFound):
		return failed(line, "", CodeReferenceNotFound, "a referenced object is not visible"), true
	case errors.Is(err, ipam.ErrConflict), errors.Is(err, ipam.ErrInUse), errors.Is(err, ipam.ErrValidation):
		return failed(line, "", CodeInvalidValue, err.Error()), true
	}
	return RowResult{}, false
}

// markImport names the source import for the history rows of the row.
func markImport(ctx context.Context, repo ipam.Repository, orgID string) error {
	if s, ok := repo.(ChangeSourceSetter); ok {
		return s.SetChangeSource(ctx, orgID, ipam.ChangeSourceImport)
	}
	return nil
}

func textOr(p *string, current string) string {
	if p == nil {
		return current
	}
	return *p
}

// --- subnet writer ---

// SubnetWriter writes the rows of the entity subnet: the identity is (VRF,
// CIDR); a known subnet is changed, an unknown one created (mode of the
// profile). Section, VRF, VLAN, client and nameserver set are resolved by
// name under the run's row level security; the hierarchy follows from the
// prefixes (WP-901).
type SubnetWriter struct {
	repo  ipam.Repository
	store IPAMStore
}

// NewSubnetWriter returns the writer of subnet rows.
func NewSubnetWriter(repo ipam.Repository, store IPAMStore) *SubnetWriter {
	return &SubnetWriter{repo: repo, store: store}
}

// Entity implements Writer.
func (w *SubnetWriter) Entity() string { return "subnet" }

// subnetRefs are the resolved references of a subnet row.
type subnetRefs struct {
	vrf, section, client, vlan, nameserver string
}

func (w *SubnetWriter) resolve(ctx context.Context, orgID string, r *subnetRow) (*subnetRefs, *Issue, error) {
	refs := &subnetRefs{}
	var issue *Issue
	var err error
	vrfIDs, ferr := w.store.FindVRFs(ctx, orgID, r.vrf)
	if refs.vrf, issue, err = resolveOne(vrfIDs, ferr, IPAMVRFTarget, "VRF", r.vrf); err != nil || issue != nil {
		return nil, issue, err
	}
	if r.section != "" {
		ids, ferr := w.store.FindIPAMSections(ctx, orgID, r.section)
		if refs.section, issue, err = resolveOne(ids, ferr, SubnetSectionTarget, "section", r.section); err != nil || issue != nil {
			return nil, issue, err
		}
	}
	if r.customer != "" {
		ids, ferr := w.store.FindClients(ctx, orgID, r.customer)
		if refs.client, issue, err = resolveOne(ids, ferr, SubnetCustomerTarget, "client", r.customer); err != nil || issue != nil {
			return nil, issue, err
		}
	}
	if r.vlan != 0 {
		ids, ferr := w.store.FindVLANs(ctx, orgID, r.vlan, refs.client)
		if refs.vlan, issue, err = resolveOne(ids, ferr, SubnetVLANTarget, "VLAN", strconv.Itoa(r.vlan)); err != nil || issue != nil {
			if issue != nil && issue.Code == CodeReferenceAmbiguous {
				issue.Message = fmt.Sprintf("%d VLANs have the number %d (per site or domain); the row cannot choose", len(ids), r.vlan)
			}
			return nil, issue, err
		}
	}
	if r.nameserver != "" {
		ids, ferr := w.store.FindNameserverSets(ctx, orgID, r.nameserver)
		if refs.nameserver, issue, err = resolveOne(ids, ferr, SubnetNameserverTarget, "nameserver set", r.nameserver); err != nil || issue != nil {
			return nil, issue, err
		}
	}
	return refs, nil, nil
}

// Write implements Writer.
func (w *SubnetWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	r, issue := parseSubnetRow(row.Values)
	if issue != nil {
		issue.Line = line
		return RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}, nil
	}
	refs, issue, err := w.resolve(ctx, orgID, r)
	if err != nil {
		return RowResult{}, err
	}
	if issue != nil {
		issue.Line = line
		return RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}, nil
	}
	ids, err := w.store.FindSubnets(ctx, orgID, refs.vrf, r.prefix.String())
	if err != nil {
		return RowResult{}, err
	}
	switch mode := row.Run.Mode; {
	case len(ids) > 1:
		return failed(line, SubnetCIDRTarget, CodeAmbiguousIdentity, "several subnets have the prefix in the VRF"), nil
	case len(ids) == 1 && mode == "create_missing":
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the subnet %s exists (mode create_missing)", ids[0])}, nil
	case len(ids) == 0 && mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no subnet has the prefix in the VRF (mode update_existing)",
			Issues: []Issue{{Field: SubnetCIDRTarget, Code: CodeNoIdentityMatch, Message: "no subnet has the prefix in the VRF"}}}, nil
	}
	if err := markImport(ctx, w.repo, orgID); err != nil {
		return RowResult{}, err
	}
	if len(ids) == 1 {
		return w.update(ctx, row, r, refs, ids[0])
	}
	s := &ipam.Subnet{OrganizationID: orgID, ClientID: refs.client, CIDR: r.prefix.String(), VRFID: refs.vrf,
		SectionID: refs.section, VLANID: refs.vlan, Gateway: r.gateway, NameserverSetID: refs.nameserver,
		Description: textOr(r.description, "")}
	if r.pool != nil {
		s.IsPool = *r.pool
	}
	if err := w.repo.CreateSubnet(ctx, s); err != nil {
		if res, ok := ipamRefusal(line, err); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "subnet.created", ResourceType: "subnets", ResourceID: s.ID,
		Changes: map[string]any{"cidr": s.CIDR, "vrf_id": s.VRFID, "section_id": s.SectionID}}); err != nil {
		return RowResult{}, err
	}
	return RowResult{Outcome: OutcomeCreated, Message: s.ID}, nil
}

// update changes the fields of the row that differ from the subnet id.
func (w *SubnetWriter) update(ctx context.Context, row *WriteRow, r *subnetRow, refs *subnetRefs, id string) (RowResult, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	current, err := w.repo.GetSubnet(ctx, orgID, id)
	if errors.Is(err, ipam.ErrNotFound) {
		return failed(line, "", CodeOutOfScope, "the subnet lies outside the scope of the user who started the import"), nil
	}
	if err != nil {
		return RowResult{}, err
	}
	req := &ipam.UpdateSubnetRequest{}
	changes := map[string]any{}
	set := func(field string, dst **string, old, value string, given bool) {
		if given && old != value {
			v := value
			*dst = &v
			changes[field] = map[string]any{"old": old, "new": value}
		}
	}
	set("section_id", &req.SectionID, current.SectionID, refs.section, r.section != "")
	set("client_id", &req.ClientID, current.ClientID, refs.client, r.customer != "")
	set("vlan_id", &req.VLANID, current.VLANID, refs.vlan, r.vlan != 0)
	set("gateway", &req.Gateway, current.Gateway, r.gateway, r.gateway != "")
	set("nameserver_set_id", &req.NameserverSetID, current.NameserverSetID, refs.nameserver, r.nameserver != "")
	set("description", &req.Description, current.Description, textOr(r.description, ""), r.description != nil)
	if r.pool != nil && *r.pool != current.IsPool {
		req.IsPool = r.pool
		changes["is_pool"] = map[string]any{"old": current.IsPool, "new": *r.pool}
	}
	if len(changes) == 0 {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the subnet %s is unchanged", id)}, nil
	}
	if _, err := w.repo.UpdateSubnet(ctx, orgID, id, req); err != nil {
		if res, ok := ipamRefusal(line, err); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "subnet.updated", ResourceType: "subnets", ResourceID: id, Changes: changes}); err != nil {
		return RowResult{}, err
	}
	return RowResult{Outcome: OutcomeUpdated, Message: id}, nil
}

// --- address writer ---

// AddressWriter writes the rows of the entity ip_address: the identity is
// (VRF, address); with a mac the row binds the interface with that MAC
// (its CI follows by trigger, WP-911). A created address has origin import;
// a pinned address keeps interface, CI, DNS name and description (IPM-14).
// After a real run Finish checks the written addresses for duplicates
// (duplicate_ip, WP-300) in one pass.
type AddressWriter struct {
	repo    ipam.Repository
	store   IPAMStore
	reviews *reviews.Service
}

// NewAddressWriter returns the writer of address rows; svc opens the
// duplicate_ip reviews (nil: no duplicate check).
func NewAddressWriter(repo ipam.Repository, store IPAMStore, svc *reviews.Service) *AddressWriter {
	return &AddressWriter{repo: repo, store: store, reviews: svc}
}

// Entity implements Writer.
func (w *AddressWriter) Entity() string { return "ip_address" }

// writtenAddresses are the addresses the committed rows of a run wrote.
type writtenAddresses struct {
	mu  sync.Mutex
	ids []string
}

type writtenAddressesKey struct{}

func runAddresses(run *RunInfo) *writtenAddresses {
	return run.State(writtenAddressesKey{}, func() any { return &writtenAddresses{} }).(*writtenAddresses)
}

// remember keeps the address for the duplicate check once its row committed.
func remember(ctx context.Context, run *RunInfo, id string) {
	set := runAddresses(run)
	database.AfterCommit(ctx, func(context.Context) {
		set.mu.Lock()
		defer set.mu.Unlock()
		set.ids = append(set.ids, id)
	})
}

// duplicateBatch bounds the addresses of one duplicate query.
const duplicateBatch = 1000

// Finish implements Finisher: the duplicate check of the written addresses.
func (w *AddressWriter) Finish(ctx context.Context, run *RunInfo) error {
	finder, ok := w.repo.(ipam.DuplicateFinder)
	if !ok || w.reviews == nil {
		return nil
	}
	set := runAddresses(run)
	set.mu.Lock()
	ids := slices.Clone(set.ids)
	set.mu.Unlock()
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for start := 0; start < len(ids); start += duplicateBatch {
		batch := ids[start:min(len(ids), start+duplicateBatch)]
		if err := ipam.CheckDuplicates(ctx, finder, w.reviews, run.OrganizationID, ipam.DuplicateSource{IPIDs: batch}); err != nil {
			return err
		}
	}
	return nil
}

// Write implements Writer.
func (w *AddressWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	r, issue := parseAddressRow(row.Values)
	if issue != nil {
		issue.Line = line
		return RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}, nil
	}
	vrfIDs, err := w.store.FindVRFs(ctx, orgID, r.vrf)
	vrfID, issue, err := resolveOne(vrfIDs, err, IPAMVRFTarget, "VRF", r.vrf)
	if err != nil {
		return RowResult{}, err
	}
	if issue != nil {
		issue.Line = line
		return RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}, nil
	}
	subnetID := ""
	if r.subnet != "" {
		ids, ferr := w.store.FindSubnets(ctx, orgID, vrfID, r.subnet)
		if subnetID, issue, err = resolveOne(ids, ferr, AddressSubnetTarget, "subnet", r.subnet); err != nil {
			return RowResult{}, err
		}
		if issue != nil {
			issue.Line = line
			return RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}, nil
		}
	}
	var notes []Issue
	interfaceID := ""
	if r.mac != "" {
		ifaces, ferr := w.store.FindInterfacesByMAC(ctx, orgID, r.mac)
		if ferr != nil {
			return RowResult{}, ferr
		}
		if len(ifaces) == 1 {
			interfaceID = ifaces[0]
		} else {
			notes = append(notes, Issue{Field: AddressMACTarget, Code: CodeInterfaceNotFound,
				Message: fmt.Sprintf("%d interfaces have the MAC %s; the address stays unbound", len(ifaces), r.mac)})
		}
	}
	rows, err := w.store.FindAddressRows(ctx, orgID, vrfID, r.addr.String())
	if err != nil {
		return RowResult{}, err
	}
	target, ambiguous := chooseAddressRow(rows, interfaceID)
	mode := row.Run.Mode
	switch {
	case ambiguous:
		return failed(line, AddressIPTarget, CodeAmbiguousIdentity, fmt.Sprintf("%d rows hold the address in the VRF", len(rows))), nil
	case target != nil && mode == "create_missing":
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the address %s exists (mode create_missing)", target.ID), Issues: notes}, nil
	case target == nil && mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no row holds the address in the VRF (mode update_existing)",
			Issues: append(notes, Issue{Field: AddressIPTarget, Code: CodeNoIdentityMatch, Message: "no row holds the address in the VRF"})}, nil
	}
	if merr := markImport(ctx, w.repo, orgID); merr != nil {
		return RowResult{}, merr
	}
	var res RowResult
	var addr *ipam.IPAddress
	if target != nil {
		res, addr, err = w.update(ctx, row, r, target, subnetID, interfaceID)
	} else {
		res, addr, err = w.create(ctx, row, r, vrfID, subnetID, interfaceID)
	}
	if err != nil || res.Outcome == OutcomeFailed {
		return res, err
	}
	if res, err = w.gateway(ctx, row, r, addr, res); err != nil || res.Outcome == OutcomeFailed {
		return res, err
	}
	res.Issues = append(notes, res.Issues...)
	sortIssues(res.Issues)
	remember(ctx, row.Run, addr.ID)
	return res, nil
}

// chooseAddressRow picks the row of the address a row writes: the row on
// the interface of the mac, else the one row without interface (it gets
// bound), else, without interface of the row, the only row. A row whose
// interface holds no row of the address creates one (another interface
// holding it is a duplicate the check after the run reports). ambiguous
// when several rows qualify.
func chooseAddressRow(rows []AddressRow, interfaceID string) (*AddressRow, bool) {
	var unbound []*AddressRow
	for i := range rows {
		if interfaceID != "" && rows[i].InterfaceID == interfaceID {
			return &rows[i], false
		}
		if rows[i].InterfaceID == "" {
			unbound = append(unbound, &rows[i])
		}
	}
	switch {
	case len(unbound) == 1:
		return unbound[0], false
	case len(unbound) > 1:
		return nil, true
	case interfaceID != "":
		return nil, false
	case len(rows) == 1:
		return &rows[0], false
	case len(rows) > 1:
		return nil, true
	}
	return nil, false
}

func (w *AddressWriter) create(ctx context.Context, row *WriteRow, r *addressRow, vrfID, subnetID, interfaceID string) (RowResult, *ipam.IPAddress, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	typ := r.typ
	if typ == "" {
		typ = "static"
	}
	a := &ipam.IPAddress{OrganizationID: orgID, SubnetID: subnetID, InterfaceID: interfaceID, Address: r.addr.String(),
		VRFID: vrfID, Type: typ, DNSName: textOr(r.hostname, ""), Description: textOr(r.description, "")}
	if err := w.repo.CreateIPAddress(ctx, a); err != nil {
		if res, ok := ipamRefusal(line, err); ok {
			return res, nil, nil
		}
		return RowResult{}, nil, err
	}
	if err := w.store.MarkImported(ctx, orgID, a.ID); err != nil {
		return RowResult{}, nil, err
	}
	if r.state != "" && r.state != a.State {
		state := r.state
		updated, err := w.repo.UpdateIPAddress(ctx, orgID, a.ID, ipam.UpdateIPAddressRequest{State: &state})
		if err != nil {
			if res, ok := ipamRefusal(line, err); ok {
				return res, nil, nil
			}
			return RowResult{}, nil, err
		}
		a = updated
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "ip_address.created", ResourceType: "ips", ResourceID: a.ID,
		Changes: map[string]any{"address": a.Address, "vrf_id": a.VRFID, "subnet_id": a.SubnetID, "interface_id": a.InterfaceID}}); err != nil {
		return RowResult{}, nil, err
	}
	return RowResult{Outcome: OutcomeCreated, Message: a.ID}, a, nil
}

func (w *AddressWriter) update(ctx context.Context, row *WriteRow, r *addressRow, target *AddressRow, subnetID, interfaceID string) (RowResult, *ipam.IPAddress, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	current, err := w.repo.GetIPAddress(ctx, orgID, target.ID)
	if errors.Is(err, ipam.ErrNotFound) {
		return failed(line, "", CodeOutOfScope, "the address lies outside the scope of the user who started the import"), nil, nil
	}
	if err != nil {
		return RowResult{}, nil, err
	}
	req := ipam.UpdateIPAddressRequest{}
	changes := map[string]any{}
	var kept []string
	set := func(field string, dst **string, old, value string, given, binding bool) {
		if !given || old == value {
			return
		}
		if binding && current.Pinned {
			kept = append(kept, field)
			return
		}
		v := value
		*dst = &v
		changes[field] = map[string]any{"old": old, "new": value}
	}
	set("interface_id", &req.InterfaceID, current.InterfaceID, interfaceID, interfaceID != "", true)
	set("dns_name", &req.DNSName, current.DNSName, textOr(r.hostname, ""), r.hostname != nil, true)
	set("description", &req.Description, current.Description, textOr(r.description, ""), r.description != nil, true)
	set("subnet_id", &req.SubnetID, current.SubnetID, subnetID, subnetID != "", false)
	set("type", &req.Type, current.Type, r.typ, r.typ != "", false)
	set("state", &req.State, current.State, r.state, r.state != "", false)
	var notes []Issue
	if len(kept) > 0 {
		slices.Sort(kept)
		notes = append(notes, Issue{Field: strings.Join(kept, ","), Code: CodePinnedKept,
			Message: "the address is pinned: the import keeps " + strings.Join(kept, ", ")})
	}
	if len(changes) == 0 {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the address %s is unchanged", current.ID), Issues: notes}, current, nil
	}
	updated, err := w.repo.UpdateIPAddress(ctx, orgID, current.ID, req)
	if err != nil {
		if res, ok := ipamRefusal(line, err); ok {
			return res, nil, nil
		}
		return RowResult{}, nil, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "ip_address.updated", ResourceType: "ips", ResourceID: current.ID, Changes: changes}); err != nil {
		return RowResult{}, nil, err
	}
	return RowResult{Outcome: OutcomeUpdated, Message: current.ID, Issues: notes}, updated, nil
}

// gateway applies is_gateway: true makes the address the gateway of its
// subnet (the mark belongs to the subnet, IPM-04); false and empty leave
// the gateway as it is.
func (w *AddressWriter) gateway(ctx context.Context, row *WriteRow, r *addressRow, a *ipam.IPAddress, res RowResult) (RowResult, error) {
	if r.isGateway == nil || !*r.isGateway || a.IsGateway {
		return res, nil
	}
	if a.SubnetID == "" {
		return failed(row.Line, AddressIsGatewayTarget, CodeInvalidValue, "an address without subnet cannot be its gateway"), nil
	}
	gw := r.addr.String()
	if _, err := w.repo.UpdateSubnet(ctx, row.Run.OrganizationID, a.SubnetID, &ipam.UpdateSubnetRequest{Gateway: &gw}); err != nil {
		if refused, ok := ipamRefusal(row.Line, err); ok {
			return refused, nil
		}
		return RowResult{}, err
	}
	if res.Outcome == OutcomeSkipped {
		res.Outcome, res.Message = OutcomeUpdated, a.ID
	}
	return res, nil
}

// --- PostgreSQL lookups ---

// FindVRFs implements IPAMStore.
func (r *PGRepository) FindVRFs(ctx context.Context, orgID, name string) ([]string, error) {
	if name == "" {
		return r.findIDs(ctx, orgID, `SELECT id::text FROM vrf WHERE organization_id = $1 AND is_default ORDER BY id LIMIT $2`, orgID, findLimit)
	}
	return r.findIDs(ctx, orgID, `SELECT id::text FROM vrf WHERE organization_id = $1 AND lower(name) = lower($2) ORDER BY id LIMIT $3`,
		orgID, name, findLimit)
}

// FindIPAMSections implements IPAMStore.
func (r *PGRepository) FindIPAMSections(ctx context.Context, orgID, name string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM ipam_section WHERE organization_id = $1 AND lower(name) = lower($2) ORDER BY id LIMIT $3`,
		orgID, name, findLimit)
}

// FindClients implements IPAMStore.
func (r *PGRepository) FindClients(ctx context.Context, orgID, ref string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM client WHERE organization_id = $1
		AND (lower(name) = lower($2) OR slug = $2 OR external_ref = $2) ORDER BY id LIMIT $3`, orgID, ref, findLimit)
}

// FindVLANs implements IPAMStore.
func (r *PGRepository) FindVLANs(ctx context.Context, orgID string, number int, clientID string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM vlan WHERE organization_id = $1 AND vlan_id = $2
		AND ($3::uuid IS NULL OR client_id IS NULL OR client_id = $3::uuid) ORDER BY id LIMIT $4`,
		orgID, number, nilIfBlank(clientID), findLimit)
}

// FindNameserverSets implements IPAMStore.
func (r *PGRepository) FindNameserverSets(ctx context.Context, orgID, name string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM nameserver_set WHERE organization_id = $1 AND lower(name) = lower($2) ORDER BY id LIMIT $3`,
		orgID, name, findLimit)
}

// FindSubnets implements IPAMStore.
func (r *PGRepository) FindSubnets(ctx context.Context, orgID, vrfID, cidr string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM subnet WHERE organization_id = $1 AND vrf_id = $2::uuid AND cidr = $3::cidr ORDER BY id LIMIT $4`,
		orgID, vrfID, cidr, findLimit)
}

// FindAddressRows implements IPAMStore.
func (r *PGRepository) FindAddressRows(ctx context.Context, orgID, vrfID, host string) ([]AddressRow, error) {
	var out []AddressRow
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(interface_id::text, ''), pinned FROM ip_address
			WHERE organization_id = $1 AND vrf_id = $2::uuid AND address_host = host($3::inet) ORDER BY id LIMIT $4`,
			orgID, vrfID, host, findLimit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AddressRow, error) {
			var a AddressRow
			serr := row.Scan(&a.ID, &a.InterfaceID, &a.Pinned)
			return a, serr
		})
		return err
	})
	return out, err
}

// FindInterfacesByMAC implements IPAMStore.
func (r *PGRepository) FindInterfacesByMAC(ctx context.Context, orgID, mac string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM network_interface WHERE organization_id = $1 AND mac = $2::macaddr ORDER BY id LIMIT $3`,
		orgID, mac, findLimit)
}

// MarkImported implements IPAMStore.
func (r *PGRepository) MarkImported(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ip_address SET origin = 'import' WHERE organization_id = $1 AND id = $2::uuid`, orgID, id)
		return err
	})
}
