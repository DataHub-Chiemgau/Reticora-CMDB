package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// BGP peerings (IPM-17, WP-1068, E-217): the target inventory of the BGP
// sessions of a device. Client and site follow the device (located-scope,
// migration bgp_peering). The remote AS is stored as its number and
// resolved against the AS numbers the caller sees when read, so that an AS
// number recorded later shows up without a write. Only the PostgreSQL
// repository keeps them; without a database the routes answer 501. The
// observed session state follows with WP-1069.

// Expected states of a peering (bgp_peering_expected_state_check).
var bgpExpectedStates = []string{"established", "admin_down"}

// BGPASNRef is the AS number of the organization a peering refers to.
type BGPASNRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// BGPPeering is one row of bgp_peering. RemoteASN is the visible AS number
// of the organization with remote_asn_number, nil without one;
// LocalAddress is the host of the local address when the caller sees it.
type BGPPeering struct {
	ID              string     `json:"id"`
	OrganizationID  string     `json:"organization_id"`
	ClientID        string     `json:"client_id,omitempty"`
	SiteID          string     `json:"site_id,omitempty"`
	DeviceCIID      string     `json:"device_ci_id"`
	VRFID           string     `json:"vrf_id"`
	LocalASNID      string     `json:"local_asn_id,omitempty"`
	RemoteASNNumber int64      `json:"remote_asn_number"`
	RemoteASNDot    string     `json:"remote_asn_asdot"`
	RemoteASN       *BGPASNRef `json:"remote_asn"`
	LocalAddressID  string     `json:"local_address_id,omitempty"`
	LocalAddress    string     `json:"local_address,omitempty"`
	RemoteAddress   string     `json:"remote_address"`
	ExpectedState   string     `json:"expected_state"`
	Description     string     `json:"description,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// CreateBGPPeeringRequest is the payload of POST /api/v1/bgp-peerings;
// without vrf_id the default VRF of the organization.
type CreateBGPPeeringRequest struct {
	DeviceCIID      string   `json:"device_ci_id"`
	VRFID           string   `json:"vrf_id,omitempty"`
	LocalASNID      string   `json:"local_asn_id,omitempty"`
	RemoteASNNumber ASNumber `json:"remote_asn_number"`
	LocalAddressID  string   `json:"local_address_id,omitempty"`
	RemoteAddress   string   `json:"remote_address"`
	ExpectedState   string   `json:"expected_state,omitempty"`
	Description     string   `json:"description,omitempty"`

	remoteASN int64
}

// UpdateBGPPeeringRequest is the payload of PATCH /api/v1/bgp-peerings/{id};
// "" clears local_asn_id and local_address_id.
type UpdateBGPPeeringRequest struct {
	DeviceCIID      *string   `json:"device_ci_id,omitempty"`
	VRFID           *string   `json:"vrf_id,omitempty"`
	LocalASNID      *string   `json:"local_asn_id,omitempty"`
	RemoteASNNumber *ASNumber `json:"remote_asn_number,omitempty"`
	LocalAddressID  *string   `json:"local_address_id,omitempty"`
	RemoteAddress   *string   `json:"remote_address,omitempty"`
	ExpectedState   *string   `json:"expected_state,omitempty"`
	Description     *string   `json:"description,omitempty"`
}

// BGPFilter holds the filters of the peering list.
type BGPFilter struct {
	CIID            string
	VRFID           string
	LocalASNID      string
	RemoteASNNumber int64
	ExpectedState   string
}

// BGPRepository keeps the BGP peerings; only the PostgreSQL repository
// implements it.
type BGPRepository interface {
	ListBGPPeerings(ctx context.Context, orgID string, f *BGPFilter, page api.PaginationParams) ([]BGPPeering, int, error)
	GetBGPPeering(ctx context.Context, orgID, id string) (*BGPPeering, error)
	CreateBGPPeering(ctx context.Context, orgID string, req *CreateBGPPeeringRequest) (*BGPPeering, error)
	UpdateBGPPeering(ctx context.Context, orgID, id string, req *UpdateBGPPeeringRequest) (*BGPPeering, error)
	DeleteBGPPeering(ctx context.Context, orgID, id string) error
}

// Fixed answers of the peering rules.
var (
	errBGPRemoteASN     = unprocessable("remote_asn_number", "remote_asn_number must be an AS number (asplain or asdot)")
	errBGPRemoteAddress = unprocessable("remote_address", "remote_address must be an IP address without prefix length")
	errBGPFamily        = unprocessable("remote_address", "remote_address and the local address differ in their address family")
	errBGPState         = unprocessable("expected_state", "expected_state must be established or admin_down")
	errBGPDevice        = unprocessable("device_ci_id", "device not found")
	errBGPVRF           = unprocessable("vrf_id", "VRF not found")
	errBGPLocalASN      = unprocessable("local_asn_id", "AS number not found")
	errBGPLocalASNOther = unprocessable("local_asn_id", "the AS number belongs to another client than the device")
	errBGPLocalAddress  = unprocessable("local_address_id", "address not found")
	errBGPLocalAddrCI   = unprocessable("local_address_id", "the address is not bound to the device")
	errBGPDuplicate     = &api.ConflictError{Field: "remote_address",
		Message: "the device already has a peering with this remote address in the VRF"}
)

// validate checks the request without the database: the remote AS number
// (asplain or asdot), the remote address and the expected state.
func (req *CreateBGPPeeringRequest) validate() error {
	n, err := ParseASN(string(req.RemoteASNNumber))
	if err != nil {
		return errBGPRemoteASN
	}
	req.remoteASN = n
	// inet takes no IPv6 zone (fe80::1%eth0): refused here, not by the cast.
	addr, err := netip.ParseAddr(strings.TrimSpace(req.RemoteAddress))
	if err != nil || addr.Zone() != "" {
		return errBGPRemoteAddress
	}
	req.RemoteAddress = addr.Unmap().String()
	if req.ExpectedState == "" {
		req.ExpectedState = "established"
	}
	switch {
	case !slices.Contains(bgpExpectedStates, req.ExpectedState):
		return errBGPState
	case !api.IsUUID(req.DeviceCIID):
		return errBGPDevice
	}
	return nil
}

// merge applies an update to the stored peering and returns the full
// request to validate and store.
func (req *UpdateBGPPeeringRequest) merge(p *BGPPeering) *CreateBGPPeeringRequest {
	out := &CreateBGPPeeringRequest{DeviceCIID: p.DeviceCIID, VRFID: p.VRFID, LocalASNID: p.LocalASNID,
		RemoteASNNumber: ASNumber(fmt.Sprint(p.RemoteASNNumber)), LocalAddressID: p.LocalAddressID,
		RemoteAddress: p.RemoteAddress, ExpectedState: p.ExpectedState, Description: p.Description}
	for dst, v := range map[*string]*string{&out.DeviceCIID: req.DeviceCIID, &out.VRFID: req.VRFID, &out.LocalASNID: req.LocalASNID,
		&out.LocalAddressID: req.LocalAddressID, &out.RemoteAddress: req.RemoteAddress, &out.ExpectedState: req.ExpectedState,
		&out.Description: req.Description} {
		if v != nil {
			*dst = *v
		}
	}
	if req.RemoteASNNumber != nil {
		out.RemoteASNNumber = *req.RemoteASNNumber
	}
	return out
}

// --- PostgreSQL ---

const bgpCols = `p.id::text, p.organization_id::text, COALESCE(p.client_id::text, ''), COALESCE(p.site_id::text, ''),
	p.device_ci_id::text, p.vrf_id::text, COALESCE(p.local_asn_id::text, ''), p.remote_asn_number,
	ra.id, ra.name, ra.kind, COALESCE(p.local_address_id::text, ''),
	COALESCE((SELECT host(ip_address.address) FROM ip_address WHERE ip_address.id = p.local_address_id
		AND ip_address.organization_id = p.organization_id AND ` + ipVisible + `), ''),
	host(p.remote_address), p.expected_state, COALESCE(p.description, ''), p.created_at, p.updated_at`

// bgpFrom resolves the remote AS number against the AS numbers the caller
// sees (AK5).
const bgpFrom = ` FROM bgp_peering p LEFT JOIN LATERAL (
	SELECT a.id::text AS id, a.name, a.kind FROM asn a
	 WHERE a.organization_id = p.organization_id AND a.number = p.remote_asn_number LIMIT 1) ra ON true`

// bgpVisibleDevice limits peerings to those of a device the caller sees.
const bgpVisibleDevice = `EXISTS (SELECT 1 FROM ci WHERE ci.id = p.device_ci_id AND ci.organization_id = p.organization_id)`

func scanBGP(s scanner) (*BGPPeering, error) {
	p := &BGPPeering{}
	var asnID, asnName, asnKind *string
	if err := s.Scan(&p.ID, &p.OrganizationID, &p.ClientID, &p.SiteID, &p.DeviceCIID, &p.VRFID, &p.LocalASNID,
		&p.RemoteASNNumber, &asnID, &asnName, &asnKind, &p.LocalAddressID, &p.LocalAddress, &p.RemoteAddress,
		&p.ExpectedState, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if asnID != nil {
		p.RemoteASN = &BGPASNRef{ID: *asnID, Name: *asnName, Kind: *asnKind}
	}
	p.RemoteASNDot = ASDot(p.RemoteASNNumber)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

// ListBGPPeerings lists the peerings the caller sees, by device and remote
// address.
func (r *PGRepository) ListBGPPeerings(ctx context.Context, orgID string, f *BGPFilter, page api.PaginationParams) ([]BGPPeering, int, error) {
	if err := uuidFields([2]string{"ci_id", f.CIID}, [2]string{"vrf_id", f.VRFID}, [2]string{"local_asn_id", f.LocalASNID}); err != nil {
		return nil, 0, err
	}
	where := "p.organization_id = $1 AND " + bgpVisibleDevice
	args := []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "$?", fmt.Sprintf("$%d", len(args)))
	}
	if f.CIID != "" {
		add("p.device_ci_id = $?", f.CIID)
	}
	if f.VRFID != "" {
		add("p.vrf_id = $?", f.VRFID)
	}
	if f.LocalASNID != "" {
		add("p.local_asn_id = $?", f.LocalASNID)
	}
	if f.RemoteASNNumber != 0 {
		add("p.remote_asn_number = $?", f.RemoteASNNumber)
	}
	if f.ExpectedState != "" {
		add("p.expected_state = $?", f.ExpectedState)
	}
	out := []BGPPeering{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM bgp_peering p WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+bgpCols+bgpFrom+" WHERE "+where+
			fmt.Sprintf(" ORDER BY p.device_ci_id, p.remote_address, p.id LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2),
			append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanBGP(rows)
			if err != nil {
				return err
			}
			out = append(out, *p)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetBGPPeering returns a peering the caller sees.
func (r *PGRepository) GetBGPPeering(ctx context.Context, orgID, id string) (*BGPPeering, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var p *BGPPeering
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p, err = getBGP(ctx, tx, orgID, id, false)
		return err
	})
	return p, err
}

func getBGP(ctx context.Context, tx pgx.Tx, orgID, id string, lock bool) (*BGPPeering, error) {
	q := "SELECT " + bgpCols + bgpFrom + " WHERE p.organization_id = $1 AND p.id = $2 AND " + bgpVisibleDevice
	if lock {
		q += " FOR UPDATE OF p"
	}
	p, err := scanBGP(tx.QueryRow(ctx, q, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// checkBGPReferences reads the references of a peering under the caller's
// row level security in the writing transaction (AK4): the device, the VRF
// (empty: the default VRF of the organization, set in req), the local ASN
// of the client of the device or org-wide, the local address bound to the
// device and of the address family of the remote address.
func checkBGPReferences(ctx context.Context, tx pgx.Tx, orgID string, req *CreateBGPPeeringRequest) error {
	var deviceClient *string
	err := tx.QueryRow(ctx, `SELECT client_id::text FROM ci WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`,
		orgID, req.DeviceCIID).Scan(&deviceClient)
	if errors.Is(err, pgx.ErrNoRows) {
		return errBGPDevice
	}
	if err != nil {
		return fmt.Errorf("read device: %w", err)
	}
	if req.VRFID == "" {
		if err = tx.QueryRow(ctx, `SELECT default_vrf_of($1)::text`, orgID).Scan(&req.VRFID); err != nil {
			return fmt.Errorf("read default vrf: %w", err)
		}
	} else if err = checkAddressVRF(ctx, tx, orgID, req.VRFID); errors.Is(err, errAddressVRFNotFound) {
		return errBGPVRF
	} else if err != nil {
		return err
	}
	if req.LocalASNID != "" {
		if !api.IsUUID(req.LocalASNID) {
			return errBGPLocalASN
		}
		var client *string
		err = tx.QueryRow(ctx, `SELECT client_id::text FROM asn WHERE organization_id = $1 AND id = $2`, orgID, req.LocalASNID).Scan(&client)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errBGPLocalASN
		case err != nil:
			return fmt.Errorf("read asn: %w", err)
		case client != nil && (deviceClient == nil || *deviceClient != *client):
			return errBGPLocalASNOther
		}
	}
	if req.LocalAddressID != "" {
		if !api.IsUUID(req.LocalAddressID) {
			return errBGPLocalAddress
		}
		var ciID, host string
		err = tx.QueryRow(ctx, `SELECT COALESCE(ci_id::text, ''), host(address) FROM ip_address
			WHERE organization_id = $1 AND id = $2 AND `+ipVisible, orgID, req.LocalAddressID).Scan(&ciID, &host)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errBGPLocalAddress
		case err != nil:
			return fmt.Errorf("read local address: %w", err)
		case ciID != req.DeviceCIID:
			return errBGPLocalAddrCI
		}
		local, perr := netip.ParseAddr(host)
		remote, rerr := netip.ParseAddr(req.RemoteAddress)
		if perr != nil || rerr != nil || local.Unmap().Is4() != remote.Unmap().Is4() {
			return errBGPFamily
		}
	}
	return nil
}

// bgpError maps the constraint violations of bgp_peering: a duplicate
// (device, VRF, remote address) is 409, a reference deleted meanwhile 422.
func bgpError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23505" && pgErr.ConstraintName == "bgp_peering_remote_key":
		return errBGPDuplicate
	case pgErr.Code == "23503" && pgErr.ConstraintName == "bgp_peering_device_fkey":
		return errBGPDevice
	case pgErr.Code == "23503" && pgErr.ConstraintName == "bgp_peering_vrf_fkey":
		return errBGPVRF
	case pgErr.Code == "23503" && pgErr.ConstraintName == "bgp_peering_local_asn_fkey":
		return errBGPLocalASN
	case pgErr.Code == "23503" && pgErr.ConstraintName == "bgp_peering_local_address_fkey":
		return errBGPLocalAddress
	}
	return err
}

// CreateBGPPeering stores a peering.
func (r *PGRepository) CreateBGPPeering(ctx context.Context, orgID string, req *CreateBGPPeeringRequest) (*BGPPeering, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	var p *BGPPeering
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkBGPReferences(ctx, tx, orgID, req); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bgp_peering (organization_id, device_ci_id, vrf_id, local_asn_id,
				remote_asn_number, local_address_id, remote_address, expected_state, description)
			VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9) RETURNING id::text`,
			orgID, req.DeviceCIID, req.VRFID, nilIfEmpty(req.LocalASNID), req.remoteASN, nilIfEmpty(req.LocalAddressID),
			req.RemoteAddress, req.ExpectedState, nilIfEmpty(req.Description)).Scan(&id); err != nil {
			return bgpError(err)
		}
		var err error
		p, err = getBGP(ctx, tx, orgID, id, false)
		return err
	})
	return p, err
}

// UpdateBGPPeering changes a peering the caller sees; the result is
// checked as a whole.
func (r *PGRepository) UpdateBGPPeering(ctx context.Context, orgID, id string, req *UpdateBGPPeeringRequest) (*BGPPeering, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var p *BGPPeering
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := getBGP(ctx, tx, orgID, id, true)
		if err != nil {
			return err
		}
		next := req.merge(current)
		err = next.validate()
		if err == nil {
			err = checkBGPReferences(ctx, tx, orgID, next)
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE bgp_peering SET device_ci_id = $3, vrf_id = $4, local_asn_id = $5,
				remote_asn_number = $6, local_address_id = $7, remote_address = $8::inet, expected_state = $9, description = $10
			WHERE organization_id = $1 AND id = $2`,
			orgID, id, next.DeviceCIID, next.VRFID, nilIfEmpty(next.LocalASNID), next.remoteASN, nilIfEmpty(next.LocalAddressID),
			next.RemoteAddress, next.ExpectedState, nilIfEmpty(next.Description)); err != nil {
			return bgpError(err)
		}
		p, err = getBGP(ctx, tx, orgID, id, false)
		return err
	})
	return p, err
}

// DeleteBGPPeering removes a peering the caller sees.
func (r *PGRepository) DeleteBGPPeering(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return r.deleteByID(ctx, orgID, "bgp_peering p", bgpVisibleDevice, id)
}
