package ipam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type scanner interface {
	Scan(dest ...any) error
}

// --- Subnets ---

const subnetCols = `id::text, organization_id::text, COALESCE(client_id::text,''), COALESCE(site_id::text,''),
	cidr::text, COALESCE(name,''), vlan_id, COALESCE(gateway::text,''), COALESCE(dns_servers::text[], '{}'),
	COALESCE(description,''), is_management, created_at, updated_at`

func scanSubnet(s scanner) (*Subnet, error) {
	sn := &Subnet{}
	if err := s.Scan(&sn.ID, &sn.OrganizationID, &sn.ClientID, &sn.SiteID, &sn.CIDR, &sn.Name,
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

func (r *PGRepository) ListSubnets(orgID string, f SubnetFilter, page api.PaginationParams) ([]Subnet, int, error) {
	ctx := context.Background()
	var out []Subnet
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

func (r *PGRepository) GetSubnet(orgID, id string) (*Subnet, error) {
	ctx := context.Background()
	var sn *Subnet
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		sn, err = scanSubnet(tx.QueryRow(ctx, "SELECT "+subnetCols+" FROM subnet WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return sn, err
}

func (r *PGRepository) CreateSubnet(s *Subnet) error {
	ctx := context.Background()
	return r.withTenant(ctx, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO subnet (organization_id, client_id, site_id, cidr, name, vlan_id, gateway, dns_servers, description, is_management)
			 VALUES ($1,$2,$3,$4::cidr,$5,$6,$7::inet,$8::inet[],$9,$10) RETURNING id::text, created_at, updated_at`,
			s.OrganizationID, nilIfEmpty(s.ClientID), nilIfEmpty(s.SiteID), s.CIDR, nilIfEmpty(s.Name),
			s.VLANID, nilIfEmpty(s.Gateway), dnsServersArg(s.DNSServers), nilIfEmpty(s.Description), s.IsManagement,
		).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	})
}

func (r *PGRepository) UpdateSubnet(orgID, id string, req UpdateSubnetRequest) (*Subnet, error) {
	ctx := context.Background()
	var sn *Subnet
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
		if req.VLANID != nil {
			sets = append(sets, fmt.Sprintf("vlan_id = $%d", pos))
			args = append(args, *req.VLANID)
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

func (r *PGRepository) DeleteSubnet(orgID, id string) error {
	return r.deleteByID(orgID, "subnet", id)
}

func (r *PGRepository) deleteByID(orgID, table, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE organization_id = $1 AND id = $2", orgID, id)
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
	address::text, status, COALESCE(dns_name,''), COALESCE(description,''), last_seen_at, created_at, updated_at`

func scanIP(s scanner) (*IPAddress, error) {
	a := &IPAddress{}
	var lastSeen *time.Time
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.SubnetID, &a.InterfaceID, &a.Address, &a.Status,
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

func (r *PGRepository) ListIPAddresses(orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error) {
	ctx := context.Background()
	var out []IPAddress
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
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

func (r *PGRepository) GetIPAddress(orgID, id string) (*IPAddress, error) {
	ctx := context.Background()
	var a *IPAddress
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanIP(tx.QueryRow(ctx, "SELECT "+ipCols+" FROM ip_address WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return a, err
}

func (r *PGRepository) CreateIPAddress(a *IPAddress) error {
	ctx := context.Background()
	return r.withTenant(ctx, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, status, dns_name, description)
			 VALUES ($1,$2,$3,$4::inet,$5,$6,$7) RETURNING id::text, created_at, updated_at`,
			a.OrganizationID, nilIfEmpty(a.SubnetID), nilIfEmpty(a.InterfaceID), a.Address, a.Status,
			nilIfEmpty(a.DNSName), nilIfEmpty(a.Description),
		).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	})
}

func (r *PGRepository) UpdateIPAddress(orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error) {
	ctx := context.Background()
	var a *IPAddress
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
		if req.Status != nil {
			sets = append(sets, fmt.Sprintf("status = $%d", pos))
			args = append(args, *req.Status)
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
			a, err = scanIP(tx.QueryRow(ctx, "SELECT "+ipCols+" FROM ip_address WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE ip_address SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + ipCols
			a, err = scanIP(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return a, err
}

func (r *PGRepository) DeleteIPAddress(orgID, id string) error {
	return r.deleteByID(orgID, "ip_address", id)
}

// --- Network Interfaces ---

const nicCols = `id::text, organization_id::text, ci_id::text, name, COALESCE(mac_address::text,''), interface_type,
	speed_mbps, is_management, is_uplink, admin_status, oper_status, COALESCE(description,''), created_at, updated_at`

func scanNIC(s scanner) (*NetworkInterface, error) {
	ni := &NetworkInterface{}
	if err := s.Scan(&ni.ID, &ni.OrganizationID, &ni.CIID, &ni.Name, &ni.MACAddress, &ni.InterfaceType,
		&ni.SpeedMbps, &ni.IsManagement, &ni.IsUplink, &ni.AdminStatus, &ni.OperStatus, &ni.Description,
		&ni.CreatedAt, &ni.UpdatedAt); err != nil {
		return nil, err
	}
	ni.CreatedAt = ni.CreatedAt.UTC()
	ni.UpdatedAt = ni.UpdatedAt.UTC()
	return ni, nil
}

func (r *PGRepository) ListInterfacesForCI(orgID, ciID string, page api.PaginationParams) ([]NetworkInterface, int, error) {
	ctx := context.Background()
	var out []NetworkInterface
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM network_interface WHERE organization_id = $1 AND ci_id = $2", orgID, ciID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+nicCols+" FROM network_interface WHERE organization_id = $1 AND ci_id = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4", orgID, ciID, page.Limit, page.Offset)
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

func (r *PGRepository) GetInterface(orgID, id string) (*NetworkInterface, error) {
	ctx := context.Background()
	var ni *NetworkInterface
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ni, err = scanNIC(tx.QueryRow(ctx, "SELECT "+nicCols+" FROM network_interface WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return ni, err
}

func (r *PGRepository) CreateInterface(ni *NetworkInterface) error {
	ctx := context.Background()
	return r.withTenant(ctx, ni.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO network_interface (organization_id, ci_id, name, mac_address, interface_type, speed_mbps, is_management, is_uplink, admin_status, oper_status, description)
			 VALUES ($1,$2,$3,$4::macaddr,$5,$6,$7,$8,$9,$10,$11) RETURNING id::text, created_at, updated_at`,
			ni.OrganizationID, ni.CIID, ni.Name, nilIfEmpty(ni.MACAddress), ni.InterfaceType, ni.SpeedMbps,
			ni.IsManagement, ni.IsUplink, ni.AdminStatus, ni.OperStatus, nilIfEmpty(ni.Description),
		).Scan(&ni.ID, &ni.CreatedAt, &ni.UpdatedAt)
	})
}

func (r *PGRepository) UpdateInterface(orgID, id string, req UpdateInterfaceRequest) (*NetworkInterface, error) {
	ctx := context.Background()
	var ni *NetworkInterface
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.MACAddress != nil {
			sets = append(sets, fmt.Sprintf("mac_address = $%d::macaddr", pos))
			args = append(args, nilIfEmpty(*req.MACAddress))
			pos++
		}
		if req.InterfaceType != nil {
			sets = append(sets, fmt.Sprintf("interface_type = $%d", pos))
			args = append(args, *req.InterfaceType)
			pos++
		}
		if req.SpeedMbps != nil {
			sets = append(sets, fmt.Sprintf("speed_mbps = $%d", pos))
			args = append(args, *req.SpeedMbps)
			pos++
		}
		if req.IsManagement != nil {
			sets = append(sets, fmt.Sprintf("is_management = $%d", pos))
			args = append(args, *req.IsManagement)
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
			ni, err = scanNIC(tx.QueryRow(ctx, "SELECT "+nicCols+" FROM network_interface WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE network_interface SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + nicCols
			ni, err = scanNIC(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return ni, err
}

func (r *PGRepository) DeleteInterface(orgID, id string) error {
	return r.deleteByID(orgID, "network_interface", id)
}
