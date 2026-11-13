package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// NAT translations (IPM-15, WP-1071, E-218): a translation of a device
// between an inside and an outside side, each an address or a subnet.
// Client and site follow the device (located-scope, migration
// nat_translation). Only the PostgreSQL repository keeps them; without a
// database the CRUD routes answer 501 and the addresses list no
// translations.

// Kinds and protocols of a NAT translation (nat_translation_kind_check,
// nat_translation_protocol_check).
var (
	natKinds     = []string{"static", "source", "destination", "pat"}
	natProtocols = []string{"any", "tcp", "udp", "icmp"}
)

// NATTranslation is one row of nat_translation. Inside and Outside are the
// address (host) or CIDR of the two sides, empty when the caller does not
// see that side.
type NATTranslation struct {
	ID               string    `json:"id"`
	OrganizationID   string    `json:"organization_id"`
	ClientID         string    `json:"client_id,omitempty"`
	SiteID           string    `json:"site_id,omitempty"`
	Kind             string    `json:"kind"`
	DeviceCIID       string    `json:"device_ci_id"`
	InsideAddressID  string    `json:"inside_address_id,omitempty"`
	InsideSubnetID   string    `json:"inside_subnet_id,omitempty"`
	OutsideAddressID string    `json:"outside_address_id,omitempty"`
	OutsideSubnetID  string    `json:"outside_subnet_id,omitempty"`
	Inside           string    `json:"inside,omitempty"`
	Outside          string    `json:"outside,omitempty"`
	Protocol         string    `json:"protocol"`
	InsidePort       *int      `json:"inside_port,omitempty"`
	OutsidePort      *int      `json:"outside_port,omitempty"`
	Description      string    `json:"description,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// CreateNATTranslationRequest is the payload of POST /api/v1/nat-translations.
type CreateNATTranslationRequest struct {
	Kind             string `json:"kind"`
	DeviceCIID       string `json:"device_ci_id"`
	InsideAddressID  string `json:"inside_address_id,omitempty"`
	InsideSubnetID   string `json:"inside_subnet_id,omitempty"`
	OutsideAddressID string `json:"outside_address_id,omitempty"`
	OutsideSubnetID  string `json:"outside_subnet_id,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	InsidePort       *int   `json:"inside_port,omitempty"`
	OutsidePort      *int   `json:"outside_port,omitempty"`
	Description      string `json:"description,omitempty"`
}

// UpdateNATTranslationRequest is the payload of PATCH
// /api/v1/nat-translations/{id}. A reference set to an id replaces the
// other reference of its side unless that one is given too; "" clears a
// reference, 0 a port.
type UpdateNATTranslationRequest struct {
	Kind             *string `json:"kind,omitempty"`
	DeviceCIID       *string `json:"device_ci_id,omitempty"`
	InsideAddressID  *string `json:"inside_address_id,omitempty"`
	InsideSubnetID   *string `json:"inside_subnet_id,omitempty"`
	OutsideAddressID *string `json:"outside_address_id,omitempty"`
	OutsideSubnetID  *string `json:"outside_subnet_id,omitempty"`
	Protocol         *string `json:"protocol,omitempty"`
	InsidePort       *int    `json:"inside_port,omitempty"`
	OutsidePort      *int    `json:"outside_port,omitempty"`
	Description      *string `json:"description,omitempty"`
}

// NATFilter holds the filters of the translation list: the device, the
// kind, an address of either side and a subnet of either side.
type NATFilter struct {
	CIID      string
	Kind      string
	AddressID string
	SubnetID  string
}

// NATRepository keeps the NAT translations; only the PostgreSQL repository
// implements it.
type NATRepository interface {
	ListNATTranslations(ctx context.Context, orgID string, f *NATFilter, page api.PaginationParams) ([]NATTranslation, int, error)
	GetNATTranslation(ctx context.Context, orgID, id string) (*NATTranslation, error)
	CreateNATTranslation(ctx context.Context, orgID string, req *CreateNATTranslationRequest) (*NATTranslation, error)
	UpdateNATTranslation(ctx context.Context, orgID, id string, req *UpdateNATTranslationRequest) (*NATTranslation, error)
	DeleteNATTranslation(ctx context.Context, orgID, id string) error
	ListAddressNAT(ctx context.Context, orgID, ipID string) ([]AddressNAT, error)
}

// Fixed answers of the NAT rules.
var (
	errNATKind     = unprocessable("kind", "kind must be static, source, destination or pat")
	errNATProtocol = unprocessable("protocol", "protocol must be any, tcp, udp or icmp")
	errNATInside   = unprocessable("inside", "exactly one of inside_address_id and inside_subnet_id is required")
	errNATOutside  = unprocessable("outside", "exactly one of outside_address_id and outside_subnet_id is required")
	errNATPorts    = unprocessable("protocol", "ports need protocol tcp or udp")
	errNATPATPort  = unprocessable("outside_port", "a pat translation needs outside_port")
)

// validate checks the request as it would be stored (AK4): kind and
// protocol, one reference per side, ports in range and only with tcp or
// udp, pat with an outside port. It fills the default protocol any.
func (req *CreateNATTranslationRequest) validate() error {
	if req.Protocol == "" {
		req.Protocol = "any"
	}
	switch {
	case !slices.Contains(natKinds, req.Kind):
		return errNATKind
	case !slices.Contains(natProtocols, req.Protocol):
		return errNATProtocol
	case !api.IsUUID(req.DeviceCIID):
		return unprocessable("device_ci_id", "device not found")
	case (req.InsideAddressID == "") == (req.InsideSubnetID == ""):
		return errNATInside
	case (req.OutsideAddressID == "") == (req.OutsideSubnetID == ""):
		return errNATOutside
	}
	for field, p := range map[string]*int{"inside_port": req.InsidePort, "outside_port": req.OutsidePort} {
		if p != nil && (*p < 1 || *p > 65535) {
			return unprocessable(field, field+" must be between 1 and 65535")
		}
	}
	if (req.InsidePort != nil || req.OutsidePort != nil) && req.Protocol != "tcp" && req.Protocol != "udp" {
		return errNATPorts
	}
	if req.Kind == "pat" && req.OutsidePort == nil {
		return errNATPATPort
	}
	return nil
}

// merge applies an update to the stored translation and returns the full
// request to validate and store.
func (req *UpdateNATTranslationRequest) merge(n *NATTranslation) *CreateNATTranslationRequest {
	out := &CreateNATTranslationRequest{Kind: n.Kind, DeviceCIID: n.DeviceCIID, InsideAddressID: n.InsideAddressID,
		InsideSubnetID: n.InsideSubnetID, OutsideAddressID: n.OutsideAddressID, OutsideSubnetID: n.OutsideSubnetID,
		Protocol: n.Protocol, InsidePort: n.InsidePort, OutsidePort: n.OutsidePort, Description: n.Description}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&out.Kind, req.Kind)
	set(&out.DeviceCIID, req.DeviceCIID)
	set(&out.Protocol, req.Protocol)
	if req.Description != nil {
		out.Description = *req.Description
	}
	// One reference per side: a new address replaces the subnet of its
	// side and the other way round, unless both are given.
	side := func(addr, subnet *string, reqAddr, reqSubnet *string) {
		set(addr, reqAddr)
		set(subnet, reqSubnet)
		if reqAddr != nil && *reqAddr != "" && reqSubnet == nil {
			*subnet = ""
		}
		if reqSubnet != nil && *reqSubnet != "" && reqAddr == nil {
			*addr = ""
		}
	}
	side(&out.InsideAddressID, &out.InsideSubnetID, req.InsideAddressID, req.InsideSubnetID)
	side(&out.OutsideAddressID, &out.OutsideSubnetID, req.OutsideAddressID, req.OutsideSubnetID)
	port := func(dst **int, v *int) {
		if v == nil {
			return
		}
		if *v == 0 {
			*dst = nil
			return
		}
		p := *v
		*dst = &p
	}
	port(&out.InsidePort, req.InsidePort)
	port(&out.OutsidePort, req.OutsidePort)
	return out
}

// --- PostgreSQL ---

// natSide is the address (host) or CIDR of a side, under the caller's row
// level security; empty when the caller does not see it.
func natSide(addressCol, subnetCol string) string {
	return `COALESCE((SELECT host(ip_address.address) FROM ip_address WHERE ip_address.id = n.` + addressCol +
		` AND ip_address.organization_id = n.organization_id AND ` + ipVisible + `),
		(SELECT s.cidr::text FROM subnet s WHERE s.id = n.` + subnetCol + ` AND s.organization_id = n.organization_id), '')`
}

// natAddressID and natSubnetID are the reference of a side, under the
// caller's row level security like natSide: empty when the caller does not
// see the address or subnet, so that its id crosses no client boundary.
func natAddressID(col string) string {
	return `COALESCE((SELECT ip_address.id::text FROM ip_address WHERE ip_address.id = n.` + col +
		` AND ip_address.organization_id = n.organization_id AND ` + ipVisible + `), '')`
}

func natSubnetID(col string) string {
	return `COALESCE((SELECT s.id::text FROM subnet s WHERE s.id = n.` + col + ` AND s.organization_id = n.organization_id), '')`
}

var natCols = `n.id::text, n.organization_id::text, COALESCE(n.client_id::text, ''), COALESCE(n.site_id::text, ''), n.kind,
	n.device_ci_id::text, ` + natAddressID("inside_address_id") + `, ` + natSubnetID("inside_subnet_id") + `,
	` + natAddressID("outside_address_id") + `, ` + natSubnetID("outside_subnet_id") + `, ` +
	natSide("inside_address_id", "inside_subnet_id") + `, ` + natSide("outside_address_id", "outside_subnet_id") + `,
	n.protocol, n.inside_port, n.outside_port, COALESCE(n.description, ''), n.created_at, n.updated_at`

func scanNAT(s scanner) (*NATTranslation, error) {
	n := &NATTranslation{}
	if err := s.Scan(&n.ID, &n.OrganizationID, &n.ClientID, &n.SiteID, &n.Kind, &n.DeviceCIID, &n.InsideAddressID,
		&n.InsideSubnetID, &n.OutsideAddressID, &n.OutsideSubnetID, &n.Inside, &n.Outside, &n.Protocol, &n.InsidePort,
		&n.OutsidePort, &n.Description, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, err
	}
	n.CreatedAt, n.UpdatedAt = n.CreatedAt.UTC(), n.UpdatedAt.UTC()
	return n, nil
}

// natVisibleDevice limits translations to those of a device the caller
// sees (a CI outside the scope hides its translations).
const natVisibleDevice = `EXISTS (SELECT 1 FROM ci WHERE ci.id = n.device_ci_id AND ci.organization_id = n.organization_id)`

// ListNATTranslations lists the translations the caller sees, the newest
// first.
func (r *PGRepository) ListNATTranslations(ctx context.Context, orgID string, f *NATFilter, page api.PaginationParams) ([]NATTranslation, int, error) {
	if err := uuidFields([2]string{"ci_id", f.CIID}, [2]string{"address_id", f.AddressID}, [2]string{"subnet_id", f.SubnetID}); err != nil {
		return nil, 0, err
	}
	where := "n.organization_id = $1 AND " + natVisibleDevice
	args := []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "$?", fmt.Sprintf("$%d", len(args)))
	}
	if f.CIID != "" {
		add("n.device_ci_id = $?", f.CIID)
	}
	if f.Kind != "" {
		add("n.kind = $?", f.Kind)
	}
	// A filter by an address or subnet the caller does not see matches
	// nothing, so that it does not confirm the reference.
	if f.AddressID != "" {
		add("(n.inside_address_id = $? OR n.outside_address_id = $?) AND EXISTS (SELECT 1 FROM ip_address WHERE ip_address.id = $? "+
			"AND ip_address.organization_id = n.organization_id AND "+ipVisible+")", f.AddressID)
	}
	if f.SubnetID != "" {
		add("(n.inside_subnet_id = $? OR n.outside_subnet_id = $?) AND EXISTS (SELECT 1 FROM subnet s WHERE s.id = $? "+
			"AND s.organization_id = n.organization_id)", f.SubnetID)
	}
	out := []NATTranslation{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM nat_translation n WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+natCols+" FROM nat_translation n WHERE "+where+
			fmt.Sprintf(" ORDER BY n.created_at DESC, n.id LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2),
			append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanNAT(rows)
			if err != nil {
				return err
			}
			out = append(out, *n)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetNATTranslation returns a translation the caller sees.
func (r *PGRepository) GetNATTranslation(ctx context.Context, orgID, id string) (*NATTranslation, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var n *NATTranslation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		n, err = getNAT(ctx, tx, orgID, id, false)
		return err
	})
	return n, err
}

func getNAT(ctx context.Context, tx pgx.Tx, orgID, id string, lock bool) (*NATTranslation, error) {
	q := "SELECT " + natCols + " FROM nat_translation n WHERE n.organization_id = $1 AND n.id = $2 AND " + natVisibleDevice
	if lock {
		q += " FOR UPDATE OF n"
	}
	n, err := scanNAT(tx.QueryRow(ctx, q, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// checkNATReferences reads the references of a translation under the
// caller's row level security in the writing transaction (the composite
// foreign keys check only the organization): the device, the addresses and
// the subnets must be visible (422 per field); the inside side belongs to
// the client of the device or is org-wide (422 inside_address_id or
// inside_subnet_id).
func checkNATReferences(ctx context.Context, tx pgx.Tx, orgID string, req *CreateNATTranslationRequest) error {
	var deviceClient *string
	err := tx.QueryRow(ctx, `SELECT client_id::text FROM ci WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`,
		orgID, req.DeviceCIID).Scan(&deviceClient)
	if errors.Is(err, pgx.ErrNoRows) {
		return unprocessable("device_ci_id", "device not found")
	}
	if err != nil {
		return fmt.Errorf("read device: %w", err)
	}
	type ref struct {
		field, id, query string
		inside           bool
	}
	refs := []ref{
		{"inside_address_id", req.InsideAddressID, `SELECT COALESCE(client_id::text, '') FROM ip_address
			WHERE organization_id = $1 AND id = $2 AND ` + ipVisible, true},
		{"inside_subnet_id", req.InsideSubnetID, `SELECT COALESCE(client_id::text, '') FROM subnet WHERE organization_id = $1 AND id = $2`, true},
		{"outside_address_id", req.OutsideAddressID, `SELECT COALESCE(client_id::text, '') FROM ip_address
			WHERE organization_id = $1 AND id = $2 AND ` + ipVisible, false},
		{"outside_subnet_id", req.OutsideSubnetID, `SELECT COALESCE(client_id::text, '') FROM subnet WHERE organization_id = $1 AND id = $2`, false},
	}
	for _, rf := range refs {
		if rf.id == "" {
			continue
		}
		notFound := unprocessable(rf.field, strings.TrimSuffix(strings.ReplaceAll(rf.field, "_", " "), " id")+" not found")
		if !api.IsUUID(rf.id) {
			return notFound
		}
		var client string
		err := tx.QueryRow(ctx, rf.query, orgID, rf.id).Scan(&client)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", rf.field, err)
		}
		if rf.inside && client != "" && (deviceClient == nil || *deviceClient != client) {
			return unprocessable(rf.field, "the inside side belongs to another client than the device")
		}
	}
	return nil
}

// natConstraintErrors are the fixed answers to the constraint violations
// of nat_translation (a row the Go checks let through, e.g. a reference
// deleted meanwhile): 422 per field.
var natConstraintErrors = map[string]*api.FieldError{
	"nat_translation_inside_check":         errNATInside,
	"nat_translation_outside_check":        errNATOutside,
	"nat_translation_ports_check":          errNATPorts,
	"nat_translation_pat_port_check":       errNATPATPort,
	"nat_translation_kind_check":           errNATKind,
	"nat_translation_protocol_check":       errNATProtocol,
	"nat_translation_inside_port_check":    unprocessable("inside_port", "inside_port must be between 1 and 65535"),
	"nat_translation_outside_port_check":   unprocessable("outside_port", "outside_port must be between 1 and 65535"),
	"nat_translation_device_fkey":          unprocessable("device_ci_id", "device not found"),
	"nat_translation_inside_address_fkey":  unprocessable("inside_address_id", "inside address not found"),
	"nat_translation_inside_subnet_fkey":   unprocessable("inside_subnet_id", "inside subnet not found"),
	"nat_translation_outside_address_fkey": unprocessable("outside_address_id", "outside address not found"),
	"nat_translation_outside_subnet_fkey":  unprocessable("outside_subnet_id", "outside subnet not found"),
}

// natError maps the constraint violations of nat_translation onto
// natConstraintErrors.
func natError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || (pgErr.Code != "23514" && pgErr.Code != "23503") {
		return err
	}
	if fe, ok := natConstraintErrors[pgErr.ConstraintName]; ok {
		return fe
	}
	return err
}

// CreateNATTranslation stores a translation; a device outside the caller's
// scope is 422 device_ci_id.
func (r *PGRepository) CreateNATTranslation(ctx context.Context, orgID string, req *CreateNATTranslationRequest) (*NATTranslation, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	var n *NATTranslation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkNATReferences(ctx, tx, orgID, req); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO nat_translation (organization_id, kind, device_ci_id, inside_address_id,
				inside_subnet_id, outside_address_id, outside_subnet_id, protocol, inside_port, outside_port, description)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
			orgID, req.Kind, req.DeviceCIID, nilIfEmpty(req.InsideAddressID), nilIfEmpty(req.InsideSubnetID),
			nilIfEmpty(req.OutsideAddressID), nilIfEmpty(req.OutsideSubnetID), req.Protocol, req.InsidePort, req.OutsidePort,
			nilIfEmpty(req.Description)).Scan(&id); err != nil {
			return natError(err)
		}
		var err error
		n, err = getNAT(ctx, tx, orgID, id, false)
		return err
	})
	return n, err
}

// UpdateNATTranslation changes a translation the caller sees; the result
// is validated as a whole.
func (r *PGRepository) UpdateNATTranslation(ctx context.Context, orgID, id string, req *UpdateNATTranslationRequest) (*NATTranslation, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var n *NATTranslation
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := getNAT(ctx, tx, orgID, id, true)
		if err != nil {
			return err
		}
		next := req.merge(current)
		err = next.validate()
		if err == nil {
			err = checkNATReferences(ctx, tx, orgID, next)
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE nat_translation SET kind = $3, device_ci_id = $4, inside_address_id = $5,
				inside_subnet_id = $6, outside_address_id = $7, outside_subnet_id = $8, protocol = $9, inside_port = $10,
				outside_port = $11, description = $12
			WHERE organization_id = $1 AND id = $2`,
			orgID, id, next.Kind, next.DeviceCIID, nilIfEmpty(next.InsideAddressID), nilIfEmpty(next.InsideSubnetID),
			nilIfEmpty(next.OutsideAddressID), nilIfEmpty(next.OutsideSubnetID), next.Protocol, next.InsidePort, next.OutsidePort,
			nilIfEmpty(next.Description))
		if err != nil {
			return natError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		n, err = getNAT(ctx, tx, orgID, id, false)
		return err
	})
	return n, err
}

// DeleteNATTranslation removes a translation the caller sees.
func (r *PGRepository) DeleteNATTranslation(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrNotFound
	}
	return r.deleteByID(ctx, orgID, "nat_translation n", natVisibleDevice, id)
}

// ListAddressNAT lists the translations the caller sees in which the
// address ipID takes part (AK5): directly as the inside or outside address,
// or through a subnet of its VRF that contains it. The address itself must
// be visible (ErrNotFound otherwise).
func (r *PGRepository) ListAddressNAT(ctx context.Context, orgID, ipID string) ([]AddressNAT, error) {
	out := []AddressNAT{}
	if !api.IsUUID(ipID) {
		return out, ErrNotFound
	}
	inside, outside := natSide("inside_address_id", "inside_subnet_id"), natSide("outside_address_id", "outside_subnet_id")
	part := func(role, counterpart, join string) string {
		return `SELECT n.id::text AS id, n.kind AS kind, '` + role + `' AS role, ` + counterpart + ` AS counterpart,
			n.protocol AS protocol, n.inside_port AS inside_port, n.outside_port AS outside_port,
			n.device_ci_id AS device_ci_id, n.created_at AS created_at
			FROM nat_translation n ` + join + ` WHERE n.organization_id = $1 AND ` + natVisibleDevice
	}
	subnetJoin := func(col string) string {
		return `JOIN subnet s ON s.id = n.` + col + ` AND s.organization_id = n.organization_id
			JOIN addr ON s.vrf_id = addr.vrf_id AND s.cidr >>= addr.address`
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ip_address WHERE ip_address.organization_id = $1
			AND ip_address.id = $2 AND `+ipVisible+`)`, orgID, ipID).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `WITH addr AS (
				SELECT ip_address.id, ip_address.address, ip_address.vrf_id FROM ip_address
				 WHERE ip_address.organization_id = $1 AND ip_address.id = $2
			)
			SELECT id, kind, role, counterpart, protocol, inside_port, outside_port, device_ci_id::text FROM (
				`+part("inside", outside, "JOIN addr ON n.inside_address_id = addr.id")+`
				UNION ALL `+part("outside", inside, "JOIN addr ON n.outside_address_id = addr.id")+`
				UNION ALL `+part("inside_subnet", outside, subnetJoin("inside_subnet_id"))+`
				UNION ALL `+part("outside_subnet", inside, subnetJoin("outside_subnet_id"))+`
			) hit ORDER BY created_at, id, role`, orgID, ipID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a AddressNAT
			if err := rows.Scan(&a.ID, &a.Kind, &a.Role, &a.Counterpart, &a.Protocol, &a.InsidePort, &a.OutsidePort,
				&a.DeviceCIID); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// --- HTTP ---

// natRepo returns the repository of the NAT translations, or answers 501
// without one (no database).
func (h *Handler) natRepo(w http.ResponseWriter, r *http.Request) (NATRepository, string, bool) {
	repo, ok := h.repo.(NATRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "NAT translations are not available")
		return nil, "", false
	}
	orgID, ok := org(w, r)
	return repo, orgID, ok
}
