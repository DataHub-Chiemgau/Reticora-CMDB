package export

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// The CSV export of subnets and addresses (IPM-09, WP-921). The columns
// are the targets of the IPAM import (WP-920, importer.SubnetTargets and
// importer.AddressTargets) behind id, so an exported file reads back as an
// import of the same entity (round trip; a test pins the equality, the
// import package depends on this one). The rows come from IPAMRows under
// the scope of the job; client, site and section rights work through row
// level security.

// Entities of the IPAM export (filters.entity).
const (
	EntitySubnet    = "subnet"
	EntityIPAddress = "ip_address"
)

// IsIPAM reports whether the filters export subnets or addresses.
func (f *JobFilters) IsIPAM() bool { return f.Entity == EntitySubnet || f.Entity == EntityIPAddress }

// IPAMFilters are the filters of an IPAM export (filters.ipam): those of
// GET /api/v1/subnets for subnets and of GET /api/v1/ips for addresses
// (WP-909).
type IPAMFilters struct {
	// Subnets.
	SectionID string `json:"section_id,omitempty"`
	VRFID     string `json:"vrf_id,omitempty"`
	VLANID    string `json:"vlan_id,omitempty"`
	SiteID    string `json:"site_id,omitempty"`
	// ParentID is the id of the parent or root.
	ParentID string `json:"parent_id,omitempty"`
	IsPool   *bool  `json:"is_pool,omitempty"`
	Contains string `json:"contains,omitempty"`
	// Addresses.
	SubnetID string `json:"subnet_id,omitempty"`
	State    string `json:"state,omitempty"`
	Type     string `json:"type,omitempty"`
	Origin   string `json:"origin,omitempty"`
	CIID     string `json:"ci_id,omitempty"`
	// Both.
	ClientID string `json:"client_id,omitempty"`
	Q        string `json:"q,omitempty"`
}

// SubnetExportColumns are the columns of the subnet export: id and the
// import targets of a subnet (importer.SubnetTargets).
var SubnetExportColumns = []string{"id", "section", "cidr", "description", "vlan", "vrf", "gateway", "customer", "nameserver"}

// AddressExportColumns are the columns of the address export: id and the
// import targets of an address (importer.AddressTargets).
var AddressExportColumns = []string{"id", "ip_addr", "hostname", "description", "mac", "state", "is_gateway", "vrf", "subnet", "type"}

// IPAMRows reads the rows of an IPAM export page by page in the order of
// their id under the scope of ctx; ipam.PGRepository implements it.
type IPAMRows interface {
	ExportSubnets(ctx context.Context, orgID string, f *ipam.SubnetFilter, after string, limit int) ([]ipam.SubnetExportRow, error)
	ExportAddresses(ctx context.Context, orgID string, f *ipam.IPFilter, after string, limit int) ([]ipam.AddressExportRow, error)
}

// ipamExportPage is the number of rows read per statement.
const ipamExportPage = 1000

// ErrIPAMExportUnavailable fails an IPAM export of a worker without
// IPAMRows (the memory profile; fail closed).
var ErrIPAMExportUnavailable = &ipamExportUnavailableError{}

type ipamExportUnavailableError struct{}

func (*ipamExportUnavailableError) Error() string {
	return "the export of subnets and addresses is not available in this deployment"
}

// PublicMessage is the message of the failed job (jobs.PublicError).
func (e *ipamExportUnavailableError) PublicMessage() string { return e.Error() }

// ErrIPAMReadRequired refuses an IPAM export of a principal without
// ipam:read (403).
const ErrIPAMReadRequired = "the export of subnets and addresses requires the permission ipam:read"

// ipamColumns returns the columns of the entity.
func ipamColumns(entity string) []string {
	if entity == EntitySubnet {
		return SubnetExportColumns
	}
	return AddressExportColumns
}

// IPAMManifest is the manifest of an IPAM export: every column, nothing
// omitted. The columns are core fields without field permission (there is
// no object kind of IPAM in permission.ObjectKinds).
func IPAMManifest(entity string) Manifest {
	return Manifest{Format: formatCSV, Fields: slices.Clone(ipamColumns(entity)), Omitted: []OmittedField{}}
}

// ipamObjectKind is the object kind of the field permissions of subnets
// and addresses: none yet, so every cell is written. With a kind the
// renderer closes the cells the policy does not let the creator read, as
// for the CI columns (permission.Readable).
var ipamObjectKind permission.ObjectKind

// ipamCell returns value when the policy lets the creator read the column
// of the object kind, otherwise an empty cell.
func ipamCell(policy *permission.FieldPolicy, kind permission.ObjectKind, id, column, value string) string {
	if kind == "" || value == "" || !permission.NeedsProjection(policy, kind) {
		return value
	}
	if !permission.Readable(policy, kind, "", permission.ObjectRef{ID: id}, column) {
		return ""
	}
	return value
}

// validateIPAM checks an IPAM export: format csv, no CI or DATEV filter,
// only the filters of the entity, ids as UUIDs and the enums of an
// address. Fixed texts.
func (f *JobFilters) validateIPAM(format string) error {
	if format != formatCSV {
		return fmt.Errorf("filters.entity %s applies to the format csv only", f.Entity)
	}
	if f.Status != "" || f.CITypeID != "" || f.IDs != nil || f.ClientID != "" || f.LifecycleState != "" {
		return fmt.Errorf("filters.status, filters.ci_type_id, filters.ids, filters.client_id and filters.lifecycle_state do not apply to filters.entity %s; use filters.ipam", f.Entity)
	}
	p := f.IPAM
	if p == nil {
		return nil
	}
	uuids := [][2]string{{"client_id", p.ClientID}}
	if f.Entity == EntitySubnet {
		if p.SubnetID != "" || p.State != "" || p.Type != "" || p.Origin != "" || p.CIID != "" {
			return fmt.Errorf("filters.ipam.subnet_id, state, type, origin and ci_id apply to filters.entity ip_address only")
		}
		uuids = append(uuids, [2]string{"section_id", p.SectionID}, [2]string{"vrf_id", p.VRFID},
			[2]string{"vlan_id", p.VLANID}, [2]string{"site_id", p.SiteID})
		if p.ParentID != "root" {
			uuids = append(uuids, [2]string{"parent_id", p.ParentID})
		}
		if p.Contains != "" {
			if _, err := netip.ParseAddr(p.Contains); err != nil {
				return fmt.Errorf("filters.ipam.contains must be an IP address")
			}
		}
	} else {
		if p.SectionID != "" || p.VRFID != "" || p.VLANID != "" || p.SiteID != "" || p.ParentID != "" || p.IsPool != nil || p.Contains != "" {
			return fmt.Errorf("filters.ipam.section_id, vrf_id, vlan_id, site_id, parent_id, is_pool and contains apply to filters.entity subnet only")
		}
		uuids = append(uuids, [2]string{"subnet_id", p.SubnetID}, [2]string{"ci_id", p.CIID})
		switch {
		case p.State != "" && !ipam.ValidIPStates[p.State]:
			return fmt.Errorf("filters.ipam.state must be active, offline or unknown")
		case p.Type != "" && !ipam.ValidIPTypes[p.Type]:
			return fmt.Errorf("filters.ipam.type must be discovered, reserved, static or dhcp")
		case p.Origin != "" && !ipam.ValidIPOrigins[p.Origin]:
			return fmt.Errorf("filters.ipam.origin must be manual, discovery, scan or import")
		}
	}
	for _, fv := range uuids {
		if fv[1] != "" && !api.IsUUID(fv[1]) {
			return fmt.Errorf("filters.ipam.%s must be a UUID", fv[0])
		}
	}
	return nil
}

// subnetFilter maps filters.ipam onto the filter of the subnet list.
func (p *IPAMFilters) subnetFilter() *ipam.SubnetFilter {
	f := &ipam.SubnetFilter{}
	if p == nil {
		return f
	}
	f.ClientID, f.SiteID, f.SectionID, f.VRFID, f.VLANID = p.ClientID, p.SiteID, p.SectionID, p.VRFID, p.VLANID
	// q is trimmed like the list handlers of WP-909 do (ipam/filter.go).
	f.IsPool, f.Q, f.Contains = p.IsPool, strings.TrimSpace(p.Q), p.Contains
	if p.ParentID == "root" {
		f.Root = true
	} else {
		f.ParentID = p.ParentID
	}
	return f
}

// addressFilter maps filters.ipam onto the filter of the address list.
func (p *IPAMFilters) addressFilter() *ipam.IPFilter {
	if p == nil {
		return &ipam.IPFilter{}
	}
	return &ipam.IPFilter{SubnetID: p.SubnetID, State: p.State, Type: p.Type, Origin: p.Origin, ClientID: p.ClientID,
		CIID: p.CIID, Q: strings.TrimSpace(p.Q)}
}

// subnetRecord is the CSV row of a subnet.
func subnetRecord(s *ipam.SubnetExportRow) []string {
	return []string{s.ID, s.Section, s.CIDR, s.Description, s.VLAN, s.VRF, s.Gateway, s.Customer, s.Nameserver}
}

// addressRecord is the CSV row of an address. The type discovered is no
// type an import sets: its cell stays empty, so the row reads back and an
// existing address keeps its type. is_gateway is 1 or empty (an empty
// cell leaves the gateway of the subnet as it is).
func addressRecord(a *ipam.AddressExportRow) []string {
	typ := a.Type
	if typ == ipam.IPTypeDiscovered {
		typ = ""
	}
	gateway := ""
	if a.IsGateway {
		gateway = "1"
	}
	return []string{a.ID, a.Address, a.Hostname, a.Description, a.MAC, a.State, gateway, a.VRF, a.Subnet, typ}
}

// RenderIPAM writes the CSV export of subnets or addresses of job to w and
// returns the number of rows and the manifest. policy is the field policy
// of the creator (ipamCell).
func RenderIPAM(ctx context.Context, src IPAMRows, job *Job, policy *permission.FieldPolicy, w io.Writer) (int, Manifest, error) {
	if src == nil {
		return 0, Manifest{}, ErrIPAMExportUnavailable
	}
	columns := ipamColumns(job.Filters.Entity)
	// UTF-8 with BOM and semicolons like every CSV export (EXP-01); the
	// import detects both.
	if _, err := io.WriteString(w, utf8BOM); err != nil {
		return 0, Manifest{}, err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	if err := cw.Write(columns); err != nil {
		return 0, Manifest{}, err
	}
	write := func(id string, record []string) error {
		for i := range record {
			record[i] = neutralizeFormula(ipamCell(policy, ipamObjectKind, id, columns[i], record[i]))
		}
		return cw.Write(record)
	}
	count, after := 0, ""
	for {
		var n int
		if job.Filters.Entity == EntitySubnet {
			rows, err := src.ExportSubnets(ctx, job.OrganizationID, job.Filters.IPAM.subnetFilter(), after, ipamExportPage)
			if err != nil {
				return 0, Manifest{}, fmt.Errorf("read subnets of the export: %w", err)
			}
			for i := range rows {
				if err := write(rows[i].ID, subnetRecord(&rows[i])); err != nil {
					return 0, Manifest{}, err
				}
				after = rows[i].ID
			}
			n = len(rows)
		} else {
			rows, err := src.ExportAddresses(ctx, job.OrganizationID, job.Filters.IPAM.addressFilter(), after, ipamExportPage)
			if err != nil {
				return 0, Manifest{}, fmt.Errorf("read addresses of the export: %w", err)
			}
			for i := range rows {
				if err := write(rows[i].ID, addressRecord(&rows[i])); err != nil {
					return 0, Manifest{}, err
				}
				after = rows[i].ID
			}
			n = len(rows)
		}
		count += n
		if n < ipamExportPage {
			break
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, Manifest{}, err
	}
	return count, IPAMManifest(job.Filters.Entity), nil
}

// formatCSV is the only format of an IPAM export.
const formatCSV = "csv"
