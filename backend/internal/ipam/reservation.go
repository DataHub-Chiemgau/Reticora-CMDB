package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// IP reservations of a subnet (NET-09, WP-299): a reservation is a row of
// ip_address with type reserved, the subnet of the path and no interface.
// The VRF comes from the subnet (trg_ip_address_vrf), client and site as
// well (ip_address_scope); reserved_by is the reserving user, never a value
// of the request. reserved_until is informative: nothing releases a
// reservation when it passes. A conflict with an address that Discovery
// observes only later is the matter of WP-772; the REST path opens no
// review (E-53).

// CreateReservationRequest is the payload of POST
// /api/v1/subnets/{id}/reservations.
type CreateReservationRequest struct {
	Address     string `json:"address"`
	Description string `json:"description,omitempty"`
	// ReservedUntil is an RFC 3339 time.
	ReservedUntil *time.Time `json:"reserved_until,omitempty"`
}

// reservationConstraints maps the unique index of interface-less addresses
// per VRF onto the request field: a second reservation of the address in
// the VRF, or another interface-less address there (409).
var reservationConstraints = map[string]string{
	"uq_ip_address_org_vrf_address_no_interface": "address",
}

// addressInUse is the conflict of an address the VRF of the subnet already
// holds: on an interface (with its id and, when the caller sees its CI, its
// name) or as an interface-less address.
func addressInUse(address, interfaceID, interfaceName string) error {
	switch {
	case interfaceID != "" && interfaceName != "":
		return &api.ConflictError{Field: "address",
			Message: fmt.Sprintf("address %s is in use on interface %s (%s) in the VRF of the subnet", address, interfaceName, interfaceID)}
	case interfaceID != "":
		return &api.ConflictError{Field: "address",
			Message: fmt.Sprintf("address %s is in use on interface %s in the VRF of the subnet", address, interfaceID)}
	default:
		return &api.ConflictError{Field: "address",
			Message: fmt.Sprintf("address %s is already reserved or recorded in the VRF of the subnet", address)}
	}
}

// --- PostgreSQL ---

// reservationVisible restricts the rows of ip_address to the reservations
// of subnet $2 the caller sees.
const reservationVisible = "ip_address.organization_id = $1 AND ip_address.subnet_id = $2 AND ip_address.type = 'reserved' AND " + ipVisible

// ListReservations lists the reservations of the subnet subnetID, the
// newest first; total counts only within the subnet (idx_ip_address_subnet).
func (r *PGRepository) ListReservations(ctx context.Context, orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error) {
	var out []IPAddress
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ip_address WHERE "+reservationVisible, orgID, subnetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+ipCols+" FROM ip_address WHERE "+reservationVisible+
			" ORDER BY created_at DESC, id LIMIT $3 OFFSET $4", orgID, subnetID, page.Limit, page.Offset)
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

// CreateReservation stores the reservation a of subnet a.SubnetID. In one
// transaction it reads the VRF of the subnet (ErrNotFound when the caller
// does not see it), refuses an address that the VRF already holds, compared
// by host(), on an interface or without one (409), and inserts the row; the
// unique index of interface-less addresses per VRF catches a concurrent
// second reservation (409).
func (r *PGRepository) CreateReservation(ctx context.Context, a *IPAddress) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var vrfID string
		err := tx.QueryRow(ctx, "SELECT vrf_id::text FROM subnet WHERE organization_id = $1 AND id = $2",
			a.OrganizationID, a.SubnetID).Scan(&vrfID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read subnet: %w", err)
		}
		var interfaceID, interfaceName string
		err = tx.QueryRow(ctx, `SELECT COALESCE(ip_address.interface_id::text, ''), COALESCE((
				SELECT network_interface.name FROM network_interface
				WHERE network_interface.id = ip_address.interface_id AND network_interface.organization_id = $1 AND `+nicVisible+`), '')
			FROM ip_address
			WHERE ip_address.organization_id = $1 AND ip_address.vrf_id = $2 AND host(ip_address.address) = host($3::inet)
			ORDER BY ip_address.interface_id IS NULL, ip_address.created_at LIMIT 1`,
			a.OrganizationID, vrfID, a.Address).Scan(&interfaceID, &interfaceName)
		switch {
		case err == nil:
			return addressInUse(a.Address, interfaceID, interfaceName)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check address: %w", err)
		}
		created, err := scanIP(tx.QueryRow(ctx, `INSERT INTO ip_address
				(organization_id, subnet_id, address, type, description, reserved_until, reserved_by)
			VALUES ($1, $2, $3::inet, 'reserved', $4, $5, $6) RETURNING `+ipCols,
			a.OrganizationID, a.SubnetID, a.Address, nilIfEmpty(a.Description), a.ReservedUntil, nilIfEmpty(a.ReservedBy)))
		if err != nil {
			return api.FieldDBError(err, reservationConstraints)
		}
		*a = *created
		return nil
	})
}

// DeleteReservation releases the reservation id of subnet subnetID; a row
// of another subnet, of another type or outside the caller's scope is
// ErrNotFound.
func (r *PGRepository) DeleteReservation(ctx context.Context, orgID, subnetID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM ip_address WHERE "+reservationVisible+" AND ip_address.id = $3", orgID, subnetID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// --- Memory ---

// hostOf is the address of an IP value with or without prefix length, as
// host() reads it.
func hostOf(v string) string {
	if p, err := netip.ParsePrefix(strings.TrimSpace(v)); err == nil {
		return p.Addr().String()
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
		return addr.String()
	}
	return v
}

// ListReservations lists the reservations of the subnet subnetID, the
// newest first.
func (r *MemoryRepository) ListReservations(_ context.Context, orgID, subnetID string, page api.PaginationParams) ([]IPAddress, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []IPAddress
	for _, a := range r.addresses {
		if a.OrganizationID == orgID && a.SubnetID == subnetID && a.Type == IPTypeReserved {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	items, total := pageSlice(out, page)
	return items, total, nil
}

// CreateReservation stores the reservation a of subnet a.SubnetID in the
// VRF of the subnet; an address the VRF already holds is a conflict.
func (r *MemoryRepository) CreateReservation(_ context.Context, a *IPAddress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	sn, ok := r.subnets[a.SubnetID]
	if !ok || sn.OrganizationID != a.OrganizationID {
		return ErrNotFound
	}
	vrfID := sn.VRFID
	if vrfID == "" {
		vrfID = r.defaultVRF(a.OrganizationID).ID
	}
	host := hostOf(a.Address)
	var holder *IPAddress
	for _, e := range r.addresses {
		if e.OrganizationID != a.OrganizationID || e.VRFID != vrfID || hostOf(e.Address) != host {
			continue
		}
		if holder == nil || (holder.InterfaceID == "" && e.InterfaceID != "") {
			holder = e
		}
	}
	if holder != nil {
		name := ""
		if ni, found := r.interfaces[holder.InterfaceID]; found {
			name = ni.Name
		}
		return addressInUse(a.Address, holder.InterfaceID, name)
	}
	r.seq++
	now := time.Now().UTC()
	stored := IPAddress{ID: fmt.Sprintf("ip-%d", r.seq), OrganizationID: a.OrganizationID, SubnetID: a.SubnetID,
		Address: a.Address, VRFID: vrfID, Type: IPTypeReserved, State: IPStateUnknown, Description: a.Description,
		ReservedUntil: a.ReservedUntil, ReservedBy: a.ReservedBy, CreatedAt: now, UpdatedAt: now}
	r.addresses[stored.ID] = &stored
	*a = stored
	return nil
}

// DeleteReservation releases the reservation id of subnet subnetID.
func (r *MemoryRepository) DeleteReservation(_ context.Context, orgID, subnetID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.addresses[id]
	if !ok || a.OrganizationID != orgID || a.SubnetID != subnetID || a.Type != IPTypeReserved {
		return ErrNotFound
	}
	delete(r.addresses, id)
	return nil
}
