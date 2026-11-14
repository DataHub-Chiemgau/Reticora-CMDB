package ipam

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// The CSV export of subnets and addresses (IPM-09, WP-921): the rows of
// the export job, read page by page in the order of their id under the
// tenant scope of the job (row level security, section rights of WP-910).
// The references are the names the import resolves (WP-920): section, VLAN
// number, VRF, client and nameserver set. A reference the scope does not
// show stays empty, because the joined row is not visible.

// SubnetExportRow is one subnet of the CSV export.
type SubnetExportRow struct {
	ID          string
	Section     string
	CIDR        string
	Description string
	// VLAN is the VLAN number (empty without VLAN).
	VLAN       string
	VRF        string
	Gateway    string
	Customer   string
	Nameserver string
}

// AddressExportRow is one address of the CSV export.
type AddressExportRow struct {
	ID          string
	Address     string
	Hostname    string
	Description string
	// MAC is the MAC address of the bound interface.
	MAC       string
	State     string
	IsGateway bool
	VRF       string
	// Subnet is the CIDR of the subnet of the address.
	Subnet string
	Type   string
}

// ExportSubnets returns at most limit subnets under f with an id after
// after (empty: from the first), ordered by id.
func (r *PGRepository) ExportSubnets(ctx context.Context, orgID string, f *SubnetFilter, after string, limit int) ([]SubnetExportRow, error) {
	if err := checkSubnetFilterIDs(f); err != nil {
		return nil, err
	}
	var out []SubnetExportRow
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args, _ := subnetFilterWhere(orgID, f)
		args = append(args, nilIfEmpty(after), limit)
		q := fmt.Sprintf(`SELECT s.id::text, COALESCE(sec.name, ''), s.cidr::text, COALESCE(s.description, ''),
			COALESCE(vl.vlan_id::text, ''), COALESCE(v.name, ''), COALESCE(host(s.gateway), ''),
			COALESCE(c.name, ''), COALESCE(ns.name, '')
		FROM (SELECT * FROM subnet WHERE %s AND ($%d::uuid IS NULL OR id > $%d::uuid) ORDER BY id LIMIT $%d) s
		LEFT JOIN ipam_section sec ON sec.id = s.section_id AND sec.organization_id = s.organization_id
		LEFT JOIN vlan vl ON vl.id = s.vlan_id AND vl.organization_id = s.organization_id
		LEFT JOIN vrf v ON v.id = s.vrf_id AND v.organization_id = s.organization_id
		LEFT JOIN client c ON c.id = s.client_id AND c.organization_id = s.organization_id
		LEFT JOIN nameserver_set ns ON ns.id = s.nameserver_set_id AND ns.organization_id = s.organization_id
		ORDER BY s.id`, where, len(args)-1, len(args)-1, len(args))
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (SubnetExportRow, error) {
			var s SubnetExportRow
			serr := row.Scan(&s.ID, &s.Section, &s.CIDR, &s.Description, &s.VLAN, &s.VRF, &s.Gateway, &s.Customer, &s.Nameserver)
			return s, serr
		})
		return err
	})
	return out, err
}

// ExportAddresses returns at most limit addresses under f with an id after
// after (empty: from the first), ordered by id.
func (r *PGRepository) ExportAddresses(ctx context.Context, orgID string, f *IPFilter, after string, limit int) ([]AddressExportRow, error) {
	if err := checkIPFilterIDs(f); err != nil {
		return nil, err
	}
	var out []AddressExportRow
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := ipFilterWhere(orgID, f)
		args = append(args, nilIfEmpty(after), limit)
		q := fmt.Sprintf(`SELECT a.id::text, host(a.address), COALESCE(a.dns_name, ''), COALESCE(a.description, ''),
			COALESCE(ni.mac::text, ''), COALESCE(a.state, ''), a.is_gateway, COALESCE(v.name, ''),
			COALESCE(sn.cidr::text, ''), a.type
		FROM (SELECT * FROM ip_address WHERE %s AND ($%d::uuid IS NULL OR id > $%d::uuid) ORDER BY id LIMIT $%d) a
		LEFT JOIN network_interface ni ON ni.id = a.interface_id AND ni.organization_id = a.organization_id
		LEFT JOIN vrf v ON v.id = a.vrf_id AND v.organization_id = a.organization_id
		LEFT JOIN subnet sn ON sn.id = a.subnet_id AND sn.organization_id = a.organization_id
		ORDER BY a.id`, where, len(args)-1, len(args)-1, len(args))
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AddressExportRow, error) {
			var a AddressExportRow
			serr := row.Scan(&a.ID, &a.Address, &a.Hostname, &a.Description, &a.MAC, &a.State, &a.IsGateway, &a.VRF,
				&a.Subnet, &a.Type)
			return a, serr
		})
		return err
	})
	return out, err
}
