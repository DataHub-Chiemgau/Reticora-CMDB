package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed IPAM repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

type scanner interface {
	Scan(dest ...any) error
}

// --- Subnets ---

const subnetCols = `id::text, organization_id::text, COALESCE(client_id::text,''), COALESCE(site_id::text,''),
	cidr::text, vrf_id::text, COALESCE(name,''), COALESCE(vlan_id::text,''), COALESCE(gateway::text,''), COALESCE(dns_servers::text[], '{}'),
	COALESCE(description,''), is_management, created_at, updated_at`

func scanSubnet(s scanner) (*Subnet, error) {
	sn := &Subnet{}
	if err := s.Scan(&sn.ID, &sn.OrganizationID, &sn.ClientID, &sn.SiteID, &sn.CIDR, &sn.VRFID, &sn.Name,
		&sn.VLANID, &sn.Gateway, &sn.DNSServers, &sn.Description, &sn.IsManagement, &sn.CreatedAt, &sn.UpdatedAt); err != nil {
		return nil, err
	}
	sn.CreatedAt = sn.CreatedAt.UTC()
	sn.UpdatedAt = sn.UpdatedAt.UTC()
	return sn, nil
}

func dnsServersArg(dns []string) any {
	if len(dns) == 0 {
		return nil
	}
	return dns
}

// IP addresses and interfaces carry no client column yet (WP-025). They are
// visible only when the objects they hang off are visible under the
// transaction's tenant scope, whose policies filter the subqueries: an
// address through its subnet and its interface, an interface through its CI.
const (
	nicVisible = "EXISTS (SELECT 1 FROM ci WHERE ci.id = network_interface.ci_id)"
	ipVisible  = `(ip_address.subnet_id IS NULL OR EXISTS (SELECT 1 FROM subnet WHERE subnet.id = ip_address.subnet_id))
	AND (ip_address.interface_id IS NULL OR EXISTS (
		SELECT 1 FROM network_interface JOIN ci ON ci.id = network_interface.ci_id
		WHERE network_interface.id = ip_address.interface_id))`
)

// requireVisible fails with ErrNotFound when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// vlanOfAnotherClient is the violation of a VLAN outside the client of the
// subnet or interface.
const vlanOfAnotherClient = "the VLAN belongs to another client"

// checkVLAN keeps the VLAN of a subnet or interface within its client
// (NET-08, TEN-05; F-088). The composite foreign key of vlan_id checks only
// the organization and bypasses row level security, so the VLAN is read
// here, in the writing transaction under the principal's row level security
// and before vlan_id is written: a VLAN the principal cannot see is "VLAN
// not found"; a VLAN of another client than clientID (empty: the subnet or
// interface is org-wide) is refused; a VLAN of that client or an org-wide
// VLAN (E-09) is accepted. Without a VLAN it costs no statement.
func checkVLAN(ctx context.Context, tx pgx.Tx, orgID, vlanID, clientID string) error {
	if strings.TrimSpace(vlanID) == "" {
		return nil
	}
	var visible, sameClient bool
	if err := tx.QueryRow(ctx, `SELECT count(*) > 0, COALESCE(bool_and(client_id IS NULL OR client_id = $3::uuid), false)
		FROM vlan WHERE organization_id = $1 AND id = $2::uuid`, orgID, vlanID, nilIfEmpty(clientID)).Scan(&visible, &sameClient); err != nil {
		return fmt.Errorf("read vlan: %w", err)
	}
	switch {
	case !visible:
		return &api.FieldError{Field: "vlan_id", Message: "VLAN not found", Status: http.StatusUnprocessableEntity}
	case !sameClient:
		return &api.FieldError{Field: "vlan_id", Message: vlanOfAnotherClient, Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// checkSubnetVLAN applies checkVLAN to an update of subnet id: to a new VLAN
// and to the VLAN of a subnet that moves to another client, against the
// client the subnet has after the update.
func checkSubnetVLAN(ctx context.Context, tx pgx.Tx, orgID, id string, req *UpdateSubnetRequest) error {
	if req.ClientID == nil && (req.VLANID == nil || strings.TrimSpace(*req.VLANID) == "") {
		return nil
	}
	var clientID, vlanID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(vlan_id::text, '') FROM subnet
		WHERE organization_id = $1 AND id = $2`, orgID, id).Scan(&clientID, &vlanID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read subnet: %w", err)
	}
	if req.ClientID != nil {
		clientID = *req.ClientID
	}
	if req.VLANID != nil {
		vlanID = *req.VLANID
	}
	return checkVLAN(ctx, tx, orgID, vlanID, clientID)
}

func (r *PGRepository) ListSubnets(ctx context.Context, orgID string, f SubnetFilter, page api.PaginationParams) ([]Subnet, int, error) {
	var out []Subnet
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		pos := 2
		if f.ClientID != "" {
			where += fmt.Sprintf(" AND client_id = $%d", pos)
			args = append(args, f.ClientID)
			pos++
		}
		if f.SiteID != "" {
			where += fmt.Sprintf(" AND site_id = $%d", pos)
			args = append(args, f.SiteID)
			pos++
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM subnet WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + subnetCols + " FROM subnet WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", pos, pos+1)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sn, err := scanSubnet(rows)
			if err != nil {
				return err
			}
			out = append(out, *sn)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetSubnet(ctx context.Context, orgID, id string) (*Subnet, error) {
	var sn *Subnet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		sn, err = scanSubnet(tx.QueryRow(ctx, "SELECT "+subnetCols+" FROM subnet WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return sn, err
}

func (r *PGRepository) CreateSubnet(ctx context.Context, s *Subnet) error {
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkVLAN(ctx, tx, s.OrganizationID, s.VLANID, s.ClientID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO subnet (organization_id, client_id, site_id, cidr, name, vlan_id, gateway, dns_servers, description, is_management, vrf_id)
			 VALUES ($1,$2,$3,$4::cidr,$5,$6,$7::inet,$8::inet[],$9,$10,$11) RETURNING id::text, vrf_id::text, created_at, updated_at`,
			s.OrganizationID, nilIfEmpty(s.ClientID), nilIfEmpty(s.SiteID), s.CIDR, nilIfEmpty(s.Name),
			nilIfEmpty(s.VLANID), nilIfEmpty(s.Gateway), dnsServersArg(s.DNSServers), nilIfEmpty(s.Description), s.IsManagement,
			nilIfEmpty(s.VRFID),
		).Scan(&s.ID, &s.VRFID, &s.CreatedAt, &s.UpdatedAt)
	})
}

func (r *PGRepository) UpdateSubnet(ctx context.Context, orgID, id string, req UpdateSubnetRequest) (*Subnet, error) {
	var sn *Subnet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkSubnetVLAN(ctx, tx, orgID, id, &req); err != nil {
			return err
		}
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.ClientID != nil {
			sets = append(sets, fmt.Sprintf("client_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.ClientID))
			pos++
		}
		if req.SiteID != nil {
			sets = append(sets, fmt.Sprintf("site_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.SiteID))
			pos++
		}
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, nilIfEmpty(*req.Name))
			pos++
		}
		if req.VRFID != nil {
			// An empty VRF falls back to the default VRF (trigger).
			sets = append(sets, fmt.Sprintf("vrf_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.VRFID))
			pos++
		}
		if req.VLANID != nil {
			sets = append(sets, fmt.Sprintf("vlan_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.VLANID))
			pos++
		}
		if req.Gateway != nil {
			sets = append(sets, fmt.Sprintf("gateway = $%d::inet", pos))
			args = append(args, nilIfEmpty(*req.Gateway))
			pos++
		}
		if req.DNSServers != nil {
			sets = append(sets, fmt.Sprintf("dns_servers = $%d::inet[]", pos))
			args = append(args, dnsServersArg(req.DNSServers))
			pos++
		}
		if req.Description != nil {
			sets = append(sets, fmt.Sprintf("description = $%d", pos))
			args = append(args, nilIfEmpty(*req.Description))
			pos++
		}
		if req.IsManagement != nil {
			sets = append(sets, fmt.Sprintf("is_management = $%d", pos))
			args = append(args, *req.IsManagement)
			pos++
		}
		var err error
		if len(sets) == 0 {
			sn, err = scanSubnet(tx.QueryRow(ctx, "SELECT "+subnetCols+" FROM subnet WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE subnet SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + subnetCols
			sn, err = scanSubnet(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return sn, err
}

func (r *PGRepository) DeleteSubnet(ctx context.Context, orgID, id string) error {
	return r.deleteByID(ctx, orgID, "subnet", "true", id)
}

// deleteByID deletes a row of table that is visible under visible.
func (r *PGRepository) deleteByID(ctx context.Context, orgID, table, visible, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE organization_id = $1 AND id = $2 AND "+visible, orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// --- IP Addresses ---

const ipCols = `id::text, organization_id::text, COALESCE(subnet_id::text,''), COALESCE(interface_id::text,''),
	address::text, vrf_id::text, type, COALESCE(dns_name,''), COALESCE(description,''), last_seen_at, created_at, updated_at`

func scanIP(s scanner) (*IPAddress, error) {
	a := &IPAddress{}
	var lastSeen *time.Time
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.SubnetID, &a.InterfaceID, &a.Address, &a.VRFID, &a.Type,
		&a.DNSName, &a.Description, &lastSeen, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if lastSeen != nil {
		s := lastSeen.UTC().Format(time.RFC3339)
		a.LastSeenAt = &s
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
	return a, nil
}

func (r *PGRepository) ListIPAddresses(ctx context.Context, orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error) {
	var out []IPAddress
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1 AND " + ipVisible
		args := []any{orgID}
		if subnetID != "" {
			where += " AND subnet_id = $2"
			args = append(args, subnetID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ip_address WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + ipCols + " FROM ip_address WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanIP(rows)
			if err != nil {
				return err
			}
			out = append(out, *a)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetIPAddress(ctx context.Context, orgID, id string) (*IPAddress, error) {
	var a *IPAddress
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanIP(tx.QueryRow(ctx, "SELECT "+ipCols+" FROM ip_address WHERE organization_id = $1 AND id = $2 AND "+ipVisible, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return a, err
}

func (r *PGRepository) CreateIPAddress(ctx context.Context, a *IPAddress) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, type, dns_name, description)
			 VALUES ($1,$2,$3,$4::inet,$5,$6,$7) RETURNING id::text, vrf_id::text, created_at, updated_at`,
			a.OrganizationID, nilIfEmpty(a.SubnetID), nilIfEmpty(a.InterfaceID), a.Address, a.Type,
			nilIfEmpty(a.DNSName), nilIfEmpty(a.Description),
		).Scan(&a.ID, &a.VRFID, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return err
		}
		return requireVisible(ctx, tx, "ip_address", ipVisible, a.ID)
	})
}

func (r *PGRepository) UpdateIPAddress(ctx context.Context, orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error) {
	var a *IPAddress
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.SubnetID != nil {
			sets = append(sets, fmt.Sprintf("subnet_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.SubnetID))
			pos++
		}
		if req.InterfaceID != nil {
			sets = append(sets, fmt.Sprintf("interface_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.InterfaceID))
			pos++
		}
		if req.Type != nil {
			sets = append(sets, fmt.Sprintf("type = $%d", pos))
			args = append(args, *req.Type)
			pos++
		}
		if req.DNSName != nil {
			sets = append(sets, fmt.Sprintf("dns_name = $%d", pos))
			args = append(args, nilIfEmpty(*req.DNSName))
			pos++
		}
		if req.Description != nil {
			sets = append(sets, fmt.Sprintf("description = $%d", pos))
			args = append(args, nilIfEmpty(*req.Description))
			pos++
		}
		var err error
		if len(sets) == 0 {
			a, err = scanIP(tx.QueryRow(ctx, "SELECT "+ipCols+" FROM ip_address WHERE organization_id = $1 AND id = $2 AND "+ipVisible, orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE ip_address SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 AND " + ipVisible + " RETURNING " + ipCols
			a, err = scanIP(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// The update may point the address at another subnet or interface.
		return requireVisible(ctx, tx, "ip_address", ipVisible, id)
	})
	return a, err
}

func (r *PGRepository) DeleteIPAddress(ctx context.Context, orgID, id string) error {
	return r.deleteByID(ctx, orgID, "ip_address", ipVisible, id)
}

// --- Network Interfaces ---

const nicCols = `id::text, organization_id::text, ci_id::text, name, if_index, COALESCE(mac::text,''), if_type,
	speed_mbps, mtu, COALESCE(vlan_id::text,''), is_management, is_uplink, admin_status, oper_status, COALESCE(description,''),
	miss_count, created_at, updated_at`

// interfaceFields maps the constraints of network_interface onto request
// fields.
var interfaceFields = map[string]string{"network_interface_ci_name_key": "name"}

func scanNIC(s scanner) (*NetworkInterface, error) {
	ni := &NetworkInterface{}
	if err := s.Scan(&ni.ID, &ni.OrganizationID, &ni.CIID, &ni.Name, &ni.IfIndex, &ni.MAC, &ni.IfType,
		&ni.SpeedMbps, &ni.MTU, &ni.VLANID, &ni.IsManagement, &ni.IsUplink, &ni.AdminStatus, &ni.OperStatus, &ni.Description,
		&ni.MissCount, &ni.CreatedAt, &ni.UpdatedAt); err != nil {
		return nil, err
	}
	ni.CreatedAt = ni.CreatedAt.UTC()
	ni.UpdatedAt = ni.UpdatedAt.UTC()
	return ni, nil
}

func (r *PGRepository) ListInterfacesForCI(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]NetworkInterface, int, error) {
	var out []NetworkInterface
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM network_interface WHERE organization_id = $1 AND ci_id = $2 AND "+nicVisible, orgID, ciID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+nicCols+" FROM network_interface WHERE organization_id = $1 AND ci_id = $2 AND "+nicVisible+" ORDER BY created_at DESC LIMIT $3 OFFSET $4", orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			ni, err := scanNIC(rows)
			if err != nil {
				return err
			}
			out = append(out, *ni)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetInterface(ctx context.Context, orgID, id string) (*NetworkInterface, error) {
	var ni *NetworkInterface
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ni, err = scanNIC(tx.QueryRow(ctx, "SELECT "+nicCols+" FROM network_interface WHERE organization_id = $1 AND id = $2 AND "+nicVisible, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return ni, err
}

func (r *PGRepository) CreateInterface(ctx context.Context, ni *NetworkInterface) error {
	return database.WithRequestTenant(ctx, r.pool, ni.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if strings.TrimSpace(ni.VLANID) != "" {
			// The interface takes the client of its CI (migration 000061).
			// A CI the principal cannot see counts as org-wide here; the
			// insert refuses it anyway.
			var clientID string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM ci WHERE organization_id = $1 AND id = $2`,
				ni.OrganizationID, ni.CIID).Scan(&clientID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read ci: %w", err)
			}
			if err := checkVLAN(ctx, tx, ni.OrganizationID, ni.VLANID, clientID); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO network_interface (organization_id, ci_id, name, if_index, mac, if_type, speed_mbps, mtu,
				is_management, is_uplink, admin_status, oper_status, description, vlan_id)
			 VALUES ($1,$2,$3,$4,$5::macaddr,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id::text, created_at, updated_at`,
			ni.OrganizationID, ni.CIID, ni.Name, ni.IfIndex, nilIfEmpty(ni.MAC), ni.IfType, ni.SpeedMbps, ni.MTU,
			ni.IsManagement, ni.IsUplink, ni.AdminStatus, ni.OperStatus, nilIfEmpty(ni.Description), nilIfEmpty(ni.VLANID),
		).Scan(&ni.ID, &ni.CreatedAt, &ni.UpdatedAt); err != nil {
			return api.FieldDBError(err, interfaceFields)
		}
		return requireVisible(ctx, tx, "network_interface", nicVisible, ni.ID)
	})
}

func (r *PGRepository) UpdateInterface(ctx context.Context, orgID, id string, req UpdateInterfaceRequest) (*NetworkInterface, error) {
	var ni *NetworkInterface
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if req.VLANID != nil && strings.TrimSpace(*req.VLANID) != "" {
			// The client of the interface is the client of its CI.
			var clientID string
			if err := tx.QueryRow(ctx, "SELECT COALESCE(client_id::text, '') FROM network_interface WHERE organization_id = $1 AND id = $2 AND "+nicVisible,
				orgID, id).Scan(&clientID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return fmt.Errorf("read interface: %w", err)
			}
			if err := checkVLAN(ctx, tx, orgID, *req.VLANID, clientID); err != nil {
				return err
			}
		}
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.IfIndex != nil {
			sets = append(sets, fmt.Sprintf("if_index = $%d", pos))
			args = append(args, *req.IfIndex)
			pos++
		}
		if req.MAC != nil {
			sets = append(sets, fmt.Sprintf("mac = $%d::macaddr", pos))
			args = append(args, nilIfEmpty(*req.MAC))
			pos++
		}
		if req.IfType != nil {
			sets = append(sets, fmt.Sprintf("if_type = $%d", pos))
			args = append(args, *req.IfType)
			pos++
		}
		if req.SpeedMbps != nil {
			sets = append(sets, fmt.Sprintf("speed_mbps = $%d", pos))
			args = append(args, *req.SpeedMbps)
			pos++
		}
		if req.MTU != nil {
			sets = append(sets, fmt.Sprintf("mtu = $%d", pos))
			args = append(args, *req.MTU)
			pos++
		}
		if req.IsManagement != nil {
			sets = append(sets, fmt.Sprintf("is_management = $%d", pos))
			args = append(args, *req.IsManagement)
			pos++
		}
		if req.VLANID != nil {
			sets = append(sets, fmt.Sprintf("vlan_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.VLANID))
			pos++
		}
		if req.IsUplink != nil {
			sets = append(sets, fmt.Sprintf("is_uplink = $%d", pos))
			args = append(args, *req.IsUplink)
			pos++
		}
		if req.AdminStatus != nil {
			sets = append(sets, fmt.Sprintf("admin_status = $%d", pos))
			args = append(args, *req.AdminStatus)
			pos++
		}
		if req.OperStatus != nil {
			sets = append(sets, fmt.Sprintf("oper_status = $%d", pos))
			args = append(args, *req.OperStatus)
			pos++
		}
		if req.Description != nil {
			sets = append(sets, fmt.Sprintf("description = $%d", pos))
			args = append(args, nilIfEmpty(*req.Description))
			pos++
		}
		var err error
		if len(sets) == 0 {
			ni, err = scanNIC(tx.QueryRow(ctx, "SELECT "+nicCols+" FROM network_interface WHERE organization_id = $1 AND id = $2 AND "+nicVisible, orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE network_interface SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 AND " + nicVisible + " RETURNING " + nicCols
			ni, err = scanNIC(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return api.FieldDBError(err, interfaceFields)
		}
		return requireVisible(ctx, tx, "network_interface", nicVisible, id)
	})
	return ni, err
}

func (r *PGRepository) DeleteInterface(ctx context.Context, orgID, id string) error {
	return r.deleteByID(ctx, orgID, "network_interface", nicVisible, id)
}

// ObservedInterface is an interface of a CI as one discovery record reported
// it, with its addresses in CIDR notation.
type ObservedInterface struct {
	Name         string
	IfIndex      int
	MAC          string
	SpeedMbps    int
	MTU          int
	AdminStatus  string
	OperStatus   string
	IsManagement bool
	IPs          []string
}

// SyncResult counts what a sync changed.
type SyncResult struct {
	Added, Updated, Removed int
}

// MaxMissedObservations is how many consecutive observations of a CI may
// miss an interface or address before it is removed (REC-07).
const MaxMissedObservations = 3

// SyncDiscoveredInterfaces applies one discovery observation of the
// interfaces of a CI (REC-07, NET-01): reported interfaces and addresses are
// created or updated and their miss_count is reset; those of the CI the
// observation did not report count one miss and are removed at the third
// consecutive miss, with an entry in the CI's history. The caller syncs only
// observations that report interfaces at all.
func (r *PGRepository) SyncDiscoveredInterfaces(ctx context.Context, orgID, ciID string, observed []ObservedInterface) (SyncResult, error) {
	var res SyncResult
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		existing := map[string]string{} // name -> id
		rows, err := tx.Query(ctx, `SELECT id::text, name FROM network_interface WHERE ci_id = $1 AND organization_id = $2`, ciID, orgID)
		if err != nil {
			return fmt.Errorf("list interfaces: %w", err)
		}
		for rows.Next() {
			var id, name string
			if err = rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			existing[name] = id
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}

		seenInterfaces := map[string]bool{}
		var seenIPs []string
		for i := range observed {
			ifc := &observed[i]
			id, ok := existing[ifc.Name]
			if ok {
				if _, err = tx.Exec(ctx, `UPDATE network_interface SET if_index = NULLIF($2, 0), mac = NULLIF($3, '')::macaddr,
					speed_mbps = NULLIF($4, 0), mtu = NULLIF($5, 0), admin_status = COALESCE(NULLIF($6, ''), admin_status),
					oper_status = COALESCE(NULLIF($7, ''), oper_status), is_management = $8, miss_count = 0, updated_at = now()
					WHERE id = $1`, id, ifc.IfIndex, ifc.MAC, ifc.SpeedMbps, ifc.MTU, ifc.AdminStatus, ifc.OperStatus, ifc.IsManagement); err != nil {
					return fmt.Errorf("update interface %s: %w", ifc.Name, err)
				}
				res.Updated++
			} else {
				if err = tx.QueryRow(ctx, `INSERT INTO network_interface (organization_id, ci_id, name, if_index, mac, speed_mbps, mtu,
					admin_status, oper_status, is_management)
					VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, '')::macaddr, NULLIF($6, 0), NULLIF($7, 0),
						COALESCE(NULLIF($8, ''), 'up'), COALESCE(NULLIF($9, ''), 'unknown'), $10)
					RETURNING id::text`, orgID, ciID, ifc.Name, ifc.IfIndex, ifc.MAC, ifc.SpeedMbps, ifc.MTU,
					ifc.AdminStatus, ifc.OperStatus, ifc.IsManagement).Scan(&id); err != nil {
					return fmt.Errorf("create interface %s: %w", ifc.Name, err)
				}
				res.Added++
			}
			seenInterfaces[id] = true
			for _, address := range ifc.IPs {
				var ipID string
				err = tx.QueryRow(ctx, `UPDATE ip_address SET miss_count = 0, last_seen_at = now(), updated_at = now()
					WHERE interface_id = $1 AND host(address) = host($2::inet) RETURNING id::text`, id, address).Scan(&ipID)
				if errors.Is(err, pgx.ErrNoRows) {
					err = tx.QueryRow(ctx, `INSERT INTO ip_address (organization_id, interface_id, address, type, last_seen_at)
						VALUES ($1, $2, $3::inet, 'discovered', now()) RETURNING id::text`, orgID, id, address).Scan(&ipID)
				}
				if err != nil {
					return fmt.Errorf("sync address %s: %w", address, err)
				}
				seenIPs = append(seenIPs, ipID)
			}
		}

		// Addresses of the CI's interfaces the observation did not report.
		removedIPs, err := missAndRemove(ctx, tx, `UPDATE ip_address a SET miss_count = a.miss_count + 1
			FROM network_interface n
			WHERE a.interface_id = n.id AND n.ci_id = $1 AND NOT (a.id::text = ANY($2::text[]))
			RETURNING a.id::text, host(a.address), a.miss_count`, ciID, seenIPs, "ip_address")
		if err != nil {
			return err
		}
		var seenIDs []string
		for id := range seenInterfaces {
			seenIDs = append(seenIDs, id)
		}
		removedInterfaces, err := missAndRemove(ctx, tx, `UPDATE network_interface SET miss_count = miss_count + 1
			WHERE ci_id = $1 AND NOT (id::text = ANY($2::text[]))
			RETURNING id::text, name, miss_count`, ciID, seenIDs, "network_interface")
		if err != nil {
			return err
		}
		res.Removed = len(removedIPs) + len(removedInterfaces)
		for _, removed := range []struct {
			field string
			names []string
		}{{"ip_addresses", removedIPs}, {"interfaces", removedInterfaces}} {
			if len(removed.names) == 0 {
				continue
			}
			if _, err = tx.Exec(ctx, `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, old_value, new_value, comment)
				VALUES ($1, $2, 'update', $3, to_jsonb($4::text[]), NULL, $5)`, orgID, ciID, removed.field, removed.names,
				fmt.Sprintf("removed after %d observations without them (REC-07)", MaxMissedObservations)); err != nil {
				return fmt.Errorf("record removal: %w", err)
			}
		}
		return nil
	})
	return res, err
}

// missAndRemove counts one miss with countQuery and deletes the rows of
// table that reached MaxMissedObservations; it returns their names.
func missAndRemove(ctx context.Context, tx pgx.Tx, countQuery, ciID string, seen []string, table string) ([]string, error) {
	if seen == nil {
		seen = []string{}
	}
	rows, err := tx.Query(ctx, countQuery, ciID, seen)
	if err != nil {
		return nil, fmt.Errorf("count misses of %s: %w", table, err)
	}
	var ids, names []string
	for rows.Next() {
		var id, name string
		var misses int
		if err = rows.Scan(&id, &name, &misses); err != nil {
			rows.Close()
			return nil, err
		}
		if misses >= MaxMissedObservations {
			ids, names = append(ids, id), append(names, name)
		}
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, "DELETE FROM "+table+" WHERE id::text = ANY($1::text[])", ids); err != nil {
			return nil, fmt.Errorf("remove %s: %w", table, err)
		}
	}
	return names, nil
}
