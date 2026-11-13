package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
	// outbox writes ipam.address.state_changed in the writing transaction
	// (IPM-04, Konvention 13); nil writes no events.
	outbox *events.Outbox
}

// NewPGRepository creates a new PostgreSQL-backed IPAM repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// WithEvents attaches the event outbox: every change of the state of an
// address writes ipam.address.state_changed.
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

type scanner interface {
	Scan(dest ...any) error
}

// --- Subnets ---

const subnetCols = `id::text, organization_id::text, COALESCE(client_id::text,''), COALESCE(site_id::text,''),
	cidr::text, vrf_id::text, section_id::text, COALESCE(name,''), COALESCE(vlan_id::text,''), COALESCE(host(gateway),''), COALESCE(dns_servers::text[], '{}'),
	COALESCE(nameserver_set_id::text,''), ` + subnetEffectiveDNS + `,
	COALESCE(description,''), is_management, ` + subnetTreeCols + `, ` + subnetOriginCols + `, created_at, updated_at`

// subnetOriginCols are the origin AS of a subnet row (WP-1067): its id and
// the AS number when the caller sees it (row level security of the
// sub-select).
const subnetOriginCols = `COALESCE(origin_asn_id::text, ''),
	(SELECT jsonb_build_object('id', a.id, 'number', a.number, 'name', a.name) FROM asn a
		WHERE a.id = subnet.origin_asn_id AND a.organization_id = subnet.organization_id)`

// subnetEffectiveDNS are the effective DNS servers of a subnet row
// (IPM-05): its own dns_servers when set, else the servers of its
// nameserver set (readable org-wide), else none.
const subnetEffectiveDNS = `ARRAY(SELECT host(u.dns) FROM unnest(COALESCE(NULLIF(dns_servers, '{}'::inet[]),
		(SELECT ns.servers FROM nameserver_set ns WHERE ns.id = subnet.nameserver_set_id AND ns.organization_id = subnet.organization_id)))
		WITH ORDINALITY AS u(dns, n) ORDER BY u.n)`

// subnetTreeCols are the hierarchy columns of a subnet row (IPM-02): the
// parent only when the caller sees it (row level security of the
// sub-select, Konvention 11), is_pool, and the number of the children the
// caller sees (by idx_subnet_org_vrf_parent).
const subnetTreeCols = `COALESCE((SELECT p.id::text FROM subnet p
		WHERE p.id = subnet.parent_id AND p.organization_id = subnet.organization_id), ''),
	is_pool,
	(SELECT count(*)::int FROM subnet c
		WHERE c.organization_id = subnet.organization_id AND c.vrf_id = subnet.vrf_id AND c.parent_id = subnet.id)`

// subnetRootVisible: the subnet has no parent the caller sees (?parent_id=root).
const subnetRootVisible = `(subnet.parent_id IS NULL OR NOT EXISTS (SELECT 1 FROM subnet p
		WHERE p.id = subnet.parent_id AND p.organization_id = subnet.organization_id))`

func scanSubnet(s scanner) (*Subnet, error) {
	sn := &Subnet{}
	var origin []byte
	if err := s.Scan(&sn.ID, &sn.OrganizationID, &sn.ClientID, &sn.SiteID, &sn.CIDR, &sn.VRFID, &sn.SectionID, &sn.Name,
		&sn.VLANID, &sn.Gateway, &sn.DNSServers, &sn.NameserverSetID, &sn.EffectiveDNSServers, &sn.Description, &sn.IsManagement, &sn.ParentID, &sn.IsPool, &sn.ChildCount,
		&sn.OriginASNID, &origin, &sn.CreatedAt, &sn.UpdatedAt); err != nil {
		return nil, err
	}
	if len(origin) > 0 {
		o := &SubnetOriginASN{}
		if err := json.Unmarshal(origin, o); err != nil {
			return nil, fmt.Errorf("subnet origin asn: %w", err)
		}
		o.ASDot = ASDot(o.Number)
		sn.OriginASN = o
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
// address through its subnet, its interface and its CI (ci_id, IPM-13), an
// interface through its CI.
const (
	nicVisible = "EXISTS (SELECT 1 FROM ci WHERE ci.id = network_interface.ci_id)"
	ipVisible  = `(ip_address.subnet_id IS NULL OR EXISTS (SELECT 1 FROM subnet WHERE subnet.id = ip_address.subnet_id))
	AND (ip_address.interface_id IS NULL OR EXISTS (
		SELECT 1 FROM network_interface JOIN ci ON ci.id = network_interface.ci_id
		WHERE network_interface.id = ip_address.interface_id))
	AND (ip_address.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = ip_address.ci_id))`
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
	// Locked until the commit (FOR UPDATE): a concurrent update of client or
	// VLAN waits and checks the committed pair.
	if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(vlan_id::text, '') FROM subnet
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&clientID, &vlanID); err != nil {
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

func (r *PGRepository) ListSubnets(ctx context.Context, orgID string, f *SubnetFilter, page api.PaginationParams) ([]Subnet, int, error) {
	if err := checkSubnetFilterIDs(f); err != nil {
		return nil, 0, err
	}
	var out []Subnet
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args, order := subnetFilterWhere(orgID, f)
		pos := len(args) + 1
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM subnet WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + subnetCols + " FROM subnet WHERE " + where + fmt.Sprintf(" ORDER BY "+order+" LIMIT $%d OFFSET $%d", pos, pos+1)
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

// lockSubnetTrees takes the advisory locks of the subnet trees of the
// given VRFs (subnet_tree_lock, migration 000300) before the write touches
// any subnet row: the placement triggers re-parent other subnets under the
// lock, so a writer that held a row lock first and waited for the tree
// lock in its trigger could deadlock with them. An empty VRF is the
// organization's default VRF; the locks are taken in the order of the
// trigger (by the text of the id), ids that are no UUID are left to the
// write, which refuses them. The trigger takes the same locks again
// (re-entrant).
func lockSubnetTrees(ctx context.Context, tx pgx.Tx, orgID string, vrfIDs ...string) error {
	ids := make([]string, 0, len(vrfIDs))
	for _, v := range vrfIDs {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			if err := tx.QueryRow(ctx, "SELECT COALESCE(default_vrf_of($1)::text, '')", orgID).Scan(&v); err != nil {
				return fmt.Errorf("read default vrf: %w", err)
			}
		}
		if api.IsUUID(v) && !slices.Contains(ids, v) {
			ids = append(ids, v)
		}
	}
	slices.Sort(ids)
	for _, v := range ids {
		if _, err := tx.Exec(ctx, "SELECT subnet_tree_lock($1, $2)", orgID, v); err != nil {
			return fmt.Errorf("lock subnet tree: %w", err)
		}
	}
	return nil
}

// lockTreesOfSubnet takes the tree lock of the current VRF of subnet id
// (read under the caller's row level security, without row lock) and, on
// a VRF change, of the target VRF. A subnet the caller cannot see takes no
// lock; the write then finds nothing.
func lockTreesOfSubnet(ctx context.Context, tx pgx.Tx, orgID, id string, targetVRF *string) error {
	if !api.IsUUID(id) {
		return nil
	}
	var current string
	err := tx.QueryRow(ctx, "SELECT vrf_id::text FROM subnet WHERE organization_id = $1 AND id = $2", orgID, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read subnet vrf: %w", err)
	}
	if targetVRF == nil {
		return lockSubnetTrees(ctx, tx, orgID, current)
	}
	return lockSubnetTrees(ctx, tx, orgID, current, *targetVRF)
}

func (r *PGRepository) CreateSubnet(ctx context.Context, s *Subnet) error {
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockSubnetTrees(ctx, tx, s.OrganizationID, s.VRFID); err != nil {
			return err
		}
		if err := checkVLAN(ctx, tx, s.OrganizationID, s.VLANID, s.ClientID); err != nil {
			return err
		}
		if err := checkSection(ctx, tx, s.OrganizationID, s.SectionID, s.ClientID); err != nil {
			return err
		}
		if err := checkNameserverSet(ctx, tx, s.OrganizationID, s.NameserverSetID); err != nil {
			return err
		}
		if err := checkOriginASN(ctx, tx, s.OrganizationID, s.OriginASNID, s.ClientID); err != nil {
			return err
		}
		if err := placed(ctx, tx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO subnet (organization_id, client_id, site_id, cidr, name, vlan_id, gateway, dns_servers, description, is_management, vrf_id, section_id, is_pool, nameserver_set_id, origin_asn_id)
				 VALUES ($1,$2,$3,$4::cidr,$5,$6,$7::inet,$8::inet[],$9,$10,$11,$12,$13,$14,$15) RETURNING id::text`,
				s.OrganizationID, nilIfEmpty(s.ClientID), nilIfEmpty(s.SiteID), s.CIDR, nilIfEmpty(s.Name),
				nilIfEmpty(s.VLANID), nilIfEmpty(s.Gateway), dnsServersArg(s.DNSServers), nilIfEmpty(s.Description), s.IsManagement,
				nilIfEmpty(s.VRFID), nilIfEmpty(s.SectionID), s.IsPool, nilIfEmpty(s.NameserverSetID), nilIfEmpty(s.OriginASNID),
			).Scan(&s.ID)
		}); err != nil {
			return subnetError(err)
		}
		if err := syncGateway(ctx, tx, s.OrganizationID, s.ID, s.Gateway); err != nil {
			return err
		}
		// The row as the triggers left it (VRF, section, parent).
		stored, err := scanSubnet(tx.QueryRow(ctx, "SELECT "+subnetCols+" FROM subnet WHERE organization_id = $1 AND id = $2", s.OrganizationID, s.ID))
		if err != nil {
			return err
		}
		if stored.Warnings, err = overlapWarnings(ctx, tx, s.ID); err != nil {
			return err
		}
		*s = *stored
		return nil
	})
}

// placed runs write, an insert or update of a subnet, in a savepoint: a
// refusal of the overlap rule of a strict section (subnet_overlap_strict,
// IPM-02) becomes 409 cidr naming the partner only when the caller sees
// it; a pool with addresses (subnet_pool_addresses) 409 is_pool.
func placed(ctx context.Context, tx pgx.Tx, write func(tx pgx.Tx) error) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	err = write(sp)
	if err == nil {
		return sp.Commit(ctx)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
		(pgErr.ConstraintName != "subnet_overlap_strict" && pgErr.ConstraintName != "subnet_pool_addresses") {
		_ = sp.Rollback(ctx)
		return err
	}
	if rbErr := sp.Rollback(ctx); rbErr != nil {
		return rbErr
	}
	if pgErr.ConstraintName == "subnet_pool_addresses" {
		return errPoolWithAddresses
	}
	// The other subnet, read under the caller's row level security: named
	// only when visible.
	partner := ""
	if api.IsUUID(pgErr.Detail) {
		if err := tx.QueryRow(ctx, "SELECT COALESCE(max(id::text), '') FROM subnet WHERE id = $1", pgErr.Detail).Scan(&partner); err != nil {
			return err
		}
	}
	return overlapConflict(partner)
}

// overlapWarnings are the warnings of a subnet outside a strict section
// (IPM-02): one per subnet whose address space it shares, naming it only
// when the caller sees it.
func overlapWarnings(ctx context.Context, tx pgx.Tx, id string) ([]SubnetWarning, error) {
	var partners []string
	if err := tx.QueryRow(ctx, "SELECT subnet_overlaps($1)::text[]", id).Scan(&partners); err != nil {
		return nil, fmt.Errorf("read subnet overlaps: %w", err)
	}
	if len(partners) == 0 {
		return nil, nil
	}
	visible := map[string]bool{}
	rows, err := tx.Query(ctx, "SELECT id::text FROM subnet WHERE id = ANY($1::uuid[])", partners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		visible[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SubnetWarning, 0, len(partners))
	for _, p := range partners {
		w := SubnetWarning{Code: "overlap"}
		if visible[p] {
			w.SubnetID = p
		}
		out = append(out, w)
	}
	return out, nil
}

func (r *PGRepository) UpdateSubnet(ctx context.Context, orgID, id string, req *UpdateSubnetRequest) (*Subnet, error) {
	var sn *Subnet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The tree locks before any row lock (checks and update below).
		if err := lockTreesOfSubnet(ctx, tx, orgID, id, req.VRFID); err != nil {
			return err
		}
		if err := checkSubnetVLAN(ctx, tx, orgID, id, req); err != nil {
			return err
		}
		if err := checkSubnetSection(ctx, tx, orgID, id, req); err != nil {
			return err
		}
		if req.NameserverSetID != nil {
			if err := checkNameserverSet(ctx, tx, orgID, *req.NameserverSetID); err != nil {
				return err
			}
		}
		if err := checkSubnetOriginASN(ctx, tx, orgID, id, req); err != nil {
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
		if req.CIDR != nil {
			sets = append(sets, fmt.Sprintf("cidr = $%d::cidr", pos))
			args = append(args, *req.CIDR)
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
		if req.SectionID != nil {
			// An empty section falls back to the default section (trigger).
			sets = append(sets, fmt.Sprintf("section_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.SectionID))
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
		if req.NameserverSetID != nil {
			sets = append(sets, fmt.Sprintf("nameserver_set_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.NameserverSetID))
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
		if req.IsPool != nil {
			sets = append(sets, fmt.Sprintf("is_pool = $%d", pos))
			args = append(args, *req.IsPool)
			pos++
		}
		if req.OriginASNID != nil {
			sets = append(sets, fmt.Sprintf("origin_asn_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.OriginASNID))
		}
		if len(sets) > 0 {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE subnet SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING host(gateway)"
			var gateway *string
			err := placed(ctx, tx, func(tx pgx.Tx) error { return tx.QueryRow(ctx, q, args...).Scan(&gateway) })
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return subnetError(err)
			}
			if req.Gateway != nil {
				gw := ""
				if gateway != nil {
					gw = *gateway
				}
				if err := syncGateway(ctx, tx, orgID, id, gw); err != nil {
					return err
				}
			}
		}
		// The row as the triggers left it (parent, children).
		var err error
		sn, err = scanSubnet(tx.QueryRow(ctx, "SELECT "+subnetCols+" FROM subnet WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if len(sets) > 0 {
			sn.Warnings, err = overlapWarnings(ctx, tx, id)
		}
		return err
	})
	return sn, err
}

// subnetError maps the prefix check of a subnet update onto 422 gateway
// for a gateway outside the prefix (subnet_gateway_in_cidr checks the
// updated row, so concurrent changes of prefix and gateway cannot pass the
// handler's check both). Addresses left outside a shrunk prefix go to their
// longest prefix (subnet_place, IPM-14).
func subnetError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "subnet_gateway_in_cidr" {
		return errGatewayOutside
	}
	// A nameserver set deleted after checkNameserverSet read it (IPM-05).
	if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "subnet_nameserver_set_fkey" {
		return errNameserverSetNotFound
	}
	return err
}

// syncGateway keeps the gateway mark of the addresses of subnet subnetID in
// step with its gateway (IPM-04), in the writing transaction, through
// ipam_subnet_sync_gateway (migration 000299): the addresses of a subnet
// can belong to other clients than the subnet, so the function checks the
// subnet under the caller's row level security and then keeps the mark
// over all of them with the scope widened. The mark of another address
// goes (the address stays); a gateway marks an address of the subnet's VRF
// in the subnet or without one, or creates one (static, no interface).
func syncGateway(ctx context.Context, tx pgx.Tx, orgID, subnetID, gateway string) error {
	if _, err := tx.Exec(ctx, "SELECT ipam_subnet_sync_gateway($1, $2, $3::inet)", orgID, subnetID, nilIfEmpty(gateway)); err != nil {
		return fmt.Errorf("sync gateway mark: %w", err)
	}
	return nil
}

// DeleteSubnet deletes a visible subnet; its children take its parent
// (subnet_unplace) under the tree lock, taken before the row lock.
func (r *PGRepository) DeleteSubnet(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTreesOfSubnet(ctx, tx, orgID, id, nil); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM subnet WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
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
	address::text, vrf_id::text, type, COALESCE(dns_name,''), COALESCE(description,''), last_seen_at,
	reserved_until, COALESCE(reserved_by::text,''), state, state_changed_at, is_gateway,
	COALESCE(ci_id::text,''), role, origin, pinned, created_at, updated_at`

func scanIP(s scanner) (*IPAddress, error) {
	a := &IPAddress{}
	var lastSeen *time.Time
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.SubnetID, &a.InterfaceID, &a.Address, &a.VRFID, &a.Type,
		&a.DNSName, &a.Description, &lastSeen, &a.ReservedUntil, &a.ReservedBy,
		&a.State, &a.StateChangedAt, &a.IsGateway, &a.CIID, &a.Role, &a.Origin, &a.Pinned, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	normalizeIP(a, lastSeen)
	return a, nil
}

// normalizeIP sets the last observation and turns the times of a scanned
// address to UTC.
func normalizeIP(a *IPAddress, lastSeen *time.Time) {
	if lastSeen != nil {
		s := lastSeen.UTC().Format(time.RFC3339)
		a.LastSeenAt = &s
	}
	if a.ReservedUntil != nil {
		until := a.ReservedUntil.UTC()
		a.ReservedUntil = &until
	}
	if a.StateChangedAt != nil {
		changed := a.StateChangedAt.UTC()
		a.StateChangedAt = &changed
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
}

func (r *PGRepository) ListIPAddresses(ctx context.Context, orgID string, f *IPFilter, page api.PaginationParams) ([]IPAddress, int, error) {
	if err := checkIPFilterIDs(f); err != nil {
		return nil, 0, err
	}
	var out []IPAddress
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := ipFilterWhere(orgID, f)
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
	explicitSubnet := a.SubnetID != ""
	err := database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkAddressCI(ctx, tx, a.OrganizationID, a.CIID); err != nil {
			return err
		}
		if !explicitSubnet {
			if err := checkAddressVRF(ctx, tx, a.OrganizationID, a.VRFID); err != nil {
				return err
			}
		}
		role := a.Role
		if role == "" {
			role = IPRolePrimary
		}
		// Without subnet_id the longest prefix of the VRF (IPM-14, WP-912),
		// in the same statement; trg_ip_address_vrf takes the VRF of the
		// subnet, else the given one, else the default VRF.
		if err := tx.QueryRow(ctx,
			`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, type, dns_name, description, ci_id, role, vrf_id)
			 VALUES ($1, COALESCE($2::uuid, ipam_longest_prefix_subnet($1, COALESCE($10::uuid, default_vrf_of($1)), $4::inet)),
			         $3, $4::inet, $5, $6, $7, $8, $9, $10)
			 RETURNING id::text, COALESCE(subnet_id::text, ''), vrf_id::text, state, is_gateway, COALESCE(ci_id::text, ''), role,
			           origin, pinned, created_at, updated_at`,
			a.OrganizationID, nilIfEmpty(a.SubnetID), nilIfEmpty(a.InterfaceID), a.Address, a.Type,
			nilIfEmpty(a.DNSName), nilIfEmpty(a.Description), nilIfEmpty(a.CIID), role, nilIfEmpty(a.VRFID),
		).Scan(&a.ID, &a.SubnetID, &a.VRFID, &a.State, &a.IsGateway, &a.CIID, &a.Role, &a.Origin, &a.Pinned,
			&a.CreatedAt, &a.UpdatedAt); err != nil {
			return addressCIError(err)
		}
		return requireVisible(ctx, tx, "ip_address", ipVisible, a.ID)
	})
	if err != nil && !explicitSubnet && isRLSViolation(err) && r.autoSubnetAssigned(ctx, a) {
		// The longest prefix put the address into the scope of another
		// client or site: 404 as for a named foreign subnet
		// (requireVisible). Only this error path reads again.
		return ErrNotFound
	}
	return err
}

// autoSubnetAssigned reports whether a new address without subnet_id would
// get a subnet (ipam_longest_prefix_subnet) in its VRF.
func (r *PGRepository) autoSubnetAssigned(ctx context.Context, a *IPAddress) bool {
	var assigned bool
	err := database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ipam_longest_prefix_subnet($1, COALESCE($2::uuid, default_vrf_of($1)), $3::inet) IS NOT NULL`,
			a.OrganizationID, nilIfEmpty(a.VRFID), a.Address).Scan(&assigned)
	})
	return err == nil && assigned
}

// isRLSViolation reports a row refused by a policy (42501).
func isRLSViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

// errAddressVRFNotFound refuses an unknown VRF, one of another organization
// and one the caller cannot see alike (422 vrf_id, IPM-14).
var errAddressVRFNotFound = &api.FieldError{Field: "vrf_id", Message: "VRF not found", Status: http.StatusUnprocessableEntity}

// checkAddressVRF reads the VRF of a new address without subnet under the
// caller's row level security; without a VRF it costs no statement.
func checkAddressVRF(ctx context.Context, tx pgx.Tx, orgID, vrfID string) error {
	if vrfID == "" {
		return nil
	}
	if !api.IsUUID(vrfID) {
		return errAddressVRFNotFound
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vrf WHERE organization_id = $1 AND id = $2)`, orgID, vrfID).Scan(&ok); err != nil {
		return fmt.Errorf("read vrf: %w", err)
	}
	if !ok {
		return errAddressVRFNotFound
	}
	return nil
}

// errAddressCINotFound is the answer to an unknown CI, a CI of another
// organization and a CI the caller cannot see alike (IPM-13).
var errAddressCINotFound = &api.FieldError{Field: "ci_id", Message: "CI not found", Status: http.StatusUnprocessableEntity}

// errAddressCIInterface: the CI of an address with interface is the CI of
// the interface (ip_address_ci_matches_interface).
var errAddressCIInterface = &api.FieldError{Field: "ci_id", Message: "the CI differs from the CI of the interface",
	Status: http.StatusUnprocessableEntity}

// checkAddressCI reads the CI of an address under the caller's row level
// security in the writing transaction: the composite foreign key checks
// only the organization. Without a CI it costs no statement.
func checkAddressCI(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	if ciID == "" {
		return nil
	}
	if !api.IsUUID(ciID) {
		return errAddressCINotFound
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL)`,
		orgID, ciID).Scan(&ok); err != nil {
		return fmt.Errorf("read ci: %w", err)
	}
	if !ok {
		return errAddressCINotFound
	}
	return nil
}

// addressCIError maps the device rules of an address write onto 422 ci_id:
// a CI other than the one of the interface, a CI deleted meanwhile.
func addressCIError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23514" && pgErr.ConstraintName == "ip_address_ci_matches_interface":
			return errAddressCIInterface
		case pgErr.Code == "23503" && pgErr.ConstraintName == "ip_address_ci_fkey":
			return errAddressCINotFound
		}
	}
	return err
}

// ListCIAddresses lists the addresses the caller sees of a CI it sees
// (IPM-13): with the name of the interface and the CIDR of the subnet,
// each only when visible, ordered by address. An unknown or invisible CI
// is ErrNotFound.
func (r *PGRepository) ListCIAddresses(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]CIAddress, int, error) {
	if !api.IsUUID(ciID) {
		return nil, 0, ErrNotFound
	}
	out := []CIAddress{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL)`,
			orgID, ciID).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		const where = "ip_address.organization_id = $1 AND ip_address.ci_id = $2 AND " + ipVisible
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ip_address WHERE "+where, orgID, ciID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+ipCols+`,
			COALESCE((SELECT n.name FROM network_interface n WHERE n.id = ip_address.interface_id AND n.organization_id = ip_address.organization_id), ''),
			COALESCE((SELECT s.cidr::text FROM subnet s WHERE s.id = ip_address.subnet_id AND s.organization_id = ip_address.organization_id), '')
			FROM ip_address WHERE `+where+" ORDER BY address, id LIMIT $3 OFFSET $4", orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c CIAddress
			a := &c.IPAddress
			var lastSeen *time.Time
			if err := rows.Scan(&a.ID, &a.OrganizationID, &a.SubnetID, &a.InterfaceID, &a.Address, &a.VRFID, &a.Type,
				&a.DNSName, &a.Description, &lastSeen, &a.ReservedUntil, &a.ReservedBy, &a.State, &a.StateChangedAt, &a.IsGateway,
				&a.CIID, &a.Role, &a.Origin, &a.Pinned, &a.CreatedAt, &a.UpdatedAt, &c.InterfaceName, &c.SubnetCIDR); err != nil {
				return err
			}
			normalizeIP(a, lastSeen)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) UpdateIPAddress(ctx context.Context, orgID, id string, req UpdateIPAddressRequest) (*IPAddress, error) {
	var a *IPAddress
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		var fromState string
		if req.SubnetID != nil {
			// The gateway mark belongs to the subnet: the gateway address
			// stays in it until the subnet's gateway changes (IPM-04).
			var isGateway bool
			var subnetID string
			if err := tx.QueryRow(ctx, "SELECT is_gateway, COALESCE(subnet_id::text, '') FROM ip_address WHERE organization_id = $1 AND id = $2 AND "+ipVisible+
				" FOR UPDATE", orgID, id).Scan(&isGateway, &subnetID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			if isGateway && subnetID != *req.SubnetID {
				return errGatewayAddressSubnet
			}
		}
		if req.State != nil {
			// The state before the change, locked until the commit.
			if err := tx.QueryRow(ctx, "SELECT state FROM ip_address WHERE organization_id = $1 AND id = $2 AND "+ipVisible+
				" FOR UPDATE", orgID, id).Scan(&fromState); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			sets = append(sets, fmt.Sprintf("state = $%d", pos),
				fmt.Sprintf("state_changed_at = CASE WHEN state IS DISTINCT FROM $%d THEN now() ELSE state_changed_at END", pos))
			args = append(args, *req.State)
			pos++
		}
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
		if req.CIID != nil {
			if err := checkAddressCI(ctx, tx, orgID, *req.CIID); err != nil {
				return err
			}
			sets = append(sets, fmt.Sprintf("ci_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.CIID))
			pos++
		}
		if req.Role != nil {
			sets = append(sets, fmt.Sprintf("role = $%d", pos))
			args = append(args, *req.Role)
			pos++
		}
		if req.Pinned != nil {
			sets = append(sets, fmt.Sprintf("pinned = $%d", pos))
			args = append(args, *req.Pinned)
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
			return addressCIError(err)
		}
		// The update may point the address at another subnet or interface.
		if err := requireVisible(ctx, tx, "ip_address", ipVisible, id); err != nil {
			return err
		}
		if req.State != nil && fromState != a.State {
			return r.emitStateChanged(ctx, tx, a, fromState)
		}
		return nil
	})
	return a, err
}

// emitStateChanged writes ipam.address.state_changed for address a, whose
// state changed from from (IPM-04, Konvention 13), as the last write of the
// transaction (Konvention 20); nothing without an outbox. The payload names
// the address, its subnet, client and site and both states.
func (r *PGRepository) emitStateChanged(ctx context.Context, tx pgx.Tx, a *IPAddress, from string) error {
	if r.outbox == nil {
		return nil
	}
	data := events.AddressStateChangedData{ID: a.ID, SubnetID: a.SubnetID, From: from, To: a.State}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM ip_address
		WHERE organization_id = $1 AND id = $2`, a.OrganizationID, a.ID).Scan(&data.ClientID, &data.SiteID); err != nil {
		return fmt.Errorf("read address scope: %w", err)
	}
	orgID := a.OrganizationID
	return database.BeforeCommit(ctx, func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.IPAMAddressStateChanged,
			EntityID: data.ID, Data: data})
		return err
	})
}

// DeleteIPAddress deletes a visible address; the gateway address of a
// subnet stays until the subnet's gateway changes (409 is_gateway, IPM-04).
func (r *PGRepository) DeleteIPAddress(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var isGateway bool
		if err := tx.QueryRow(ctx, "SELECT is_gateway FROM ip_address WHERE organization_id = $1 AND id = $2 AND "+ipVisible+
			" FOR UPDATE", orgID, id).Scan(&isGateway); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if isGateway {
			return errGatewayAddressDelete
		}
		_, err := tx.Exec(ctx, "DELETE FROM ip_address WHERE organization_id = $1 AND id = $2 AND "+ipVisible, orgID, id)
		return err
	})
}

// --- Network projection (IMP-11) ---

// projectionLinks reads the links of the CIs $2 to their subnets and VLANs:
// the interfaces of the CIs by idx_network_interface_ci under nicVisible,
// their addresses by idx_ip_address_interface, the subnet of an address and
// the VLAN of an interface by a JOIN, so the row level security of subnet
// and vlan hides one outside the scope. An address is visible by ipVisible:
// its interface hangs off a visible CI (nicVisible), its subnet is joined.
// An address without interface or subnet links nothing; UNION keeps each
// link once.
const projectionLinks = `WITH nic AS (
	SELECT network_interface.id, network_interface.ci_id, network_interface.vlan_id FROM network_interface
	WHERE network_interface.organization_id = $1 AND network_interface.ci_id = ANY($2::uuid[]) AND ` + nicVisible + `)
SELECT 's', nic.ci_id::text, subnet.id::text FROM nic
	JOIN ip_address ON ip_address.interface_id = nic.id AND ip_address.organization_id = $1
	JOIN subnet ON subnet.id = ip_address.subnet_id AND subnet.organization_id = $1
UNION
SELECT 'v', nic.ci_id::text, vlan.id::text FROM nic
	JOIN vlan ON vlan.id = nic.vlan_id AND vlan.organization_id = $1`

// projectionNodes reads the subnets $2 with their visible VLAN and the
// VLANs $3 together with the VLANs of the subnets $2, each under the row
// level security of its table.
const projectionNodes = `SELECT 's', subnet.id::text, COALESCE(subnet.name, ''), subnet.cidr::text, subnet.vrf_id::text,
	COALESCE(subnet.client_id::text, ''), COALESCE(subnet.site_id::text, ''), COALESCE(vlan.id::text, ''), 0
FROM subnet LEFT JOIN vlan ON vlan.id = subnet.vlan_id AND vlan.organization_id = subnet.organization_id
WHERE subnet.organization_id = $1 AND subnet.id = ANY($2::uuid[])
UNION ALL
SELECT 'v', vlan.id::text, vlan.name, '', '', COALESCE(vlan.client_id::text, ''), COALESCE(vlan.site_id::text, ''), '', vlan.vlan_id
FROM vlan WHERE vlan.organization_id = $1 AND (vlan.id = ANY($3::uuid[])
	OR vlan.id IN (SELECT subnet.vlan_id FROM subnet WHERE subnet.organization_id = $1 AND subnet.id = ANY($2::uuid[])))`

// NetworkProjection reads the network of the CIs ciIDs (IMP-11, WP-298)
// in one transaction under the request's tenant scope with at most two
// statements: the links of the CIs to their subnets and VLANs, then those
// subnets and VLANs with the VLANs of the subnets. The CI ids go in as one
// array, so the statements do not grow with the CIs, interfaces or
// addresses.
func (r *PGRepository) NetworkProjection(ctx context.Context, orgID string, ciIDs []string) (NetworkProjection, error) {
	var p NetworkProjection
	if len(ciIDs) == 0 {
		return p, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, projectionLinks, orgID, ciIDs)
		if err != nil {
			return fmt.Errorf("read network links: %w", err)
		}
		var subnetIDs, vlanIDs []string
		seenSubnet, seenVLAN := map[string]bool{}, map[string]bool{}
		for rows.Next() {
			var kind string
			var link NetworkLink
			if err = rows.Scan(&kind, &link.CIID, &link.Target); err != nil {
				rows.Close()
				return err
			}
			if kind == "s" {
				p.CISubnets = append(p.CISubnets, link)
				if !seenSubnet[link.Target] {
					seenSubnet[link.Target] = true
					subnetIDs = append(subnetIDs, link.Target)
				}
				continue
			}
			p.CIVLANs = append(p.CIVLANs, link)
			if !seenVLAN[link.Target] {
				seenVLAN[link.Target] = true
				vlanIDs = append(vlanIDs, link.Target)
			}
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		if len(subnetIDs) == 0 && len(vlanIDs) == 0 {
			return nil
		}
		nodes, err := tx.Query(ctx, projectionNodes, orgID, subnetIDs, vlanIDs)
		if err != nil {
			return fmt.Errorf("read network nodes: %w", err)
		}
		defer nodes.Close()
		for nodes.Next() {
			var kind, id, name, cidr, vrfID, clientID, siteID, vlanID string
			var number int
			if scanErr := nodes.Scan(&kind, &id, &name, &cidr, &vrfID, &clientID, &siteID, &vlanID, &number); scanErr != nil {
				return scanErr
			}
			if kind == "s" {
				p.Subnets = append(p.Subnets, ProjectedSubnet{ID: id, Name: name, CIDR: cidr, VRFID: vrfID,
					ClientID: clientID, SiteID: siteID, VLANID: vlanID})
				continue
			}
			p.VLANs = append(p.VLANs, ProjectedVLAN{ID: id, Name: name, Number: number, ClientID: clientID, SiteID: siteID})
		}
		return nodes.Err()
	})
	if err != nil {
		return NetworkProjection{}, err
	}
	sortProjection(&p)
	return p, nil
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
			// An unknown CI is not found, like a CI the caller cannot see
			// (requireVisible, F-311).
			if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.ConstraintName == "network_interface_ci_id_fkey" {
				return ErrNotFound
			}
			return api.FieldDBError(err, interfaceFields)
		}
		return requireVisible(ctx, tx, "network_interface", nicVisible, ni.ID)
	})
}

func (r *PGRepository) UpdateInterface(ctx context.Context, orgID, id string, req *UpdateInterfaceRequest) (*NetworkInterface, error) {
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

// DeleteInterface deletes an interface with removeInterfaces (REL-05,
// F-042, F-312). An edge that keeps it refuses the delete with
// *InterfaceInUseError naming the edges the caller can see; the removal of
// its discovery edges is rolled back then.
func (r *PGRepository) DeleteInterface(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var ciID, name string
		err := tx.QueryRow(ctx, "SELECT ci_id::text, name FROM network_interface WHERE organization_id = $1 AND id = $2 AND "+nicVisible+" FOR UPDATE",
			orgID, id).Scan(&ciID, &name)
		var pgErr *pgconn.PgError
		switch {
		case errors.Is(err, pgx.ErrNoRows), errors.As(err, &pgErr) && pgErr.Code == "22P02":
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read interface: %w", err)
		}
		removed, err := removeInterfaces(ctx, tx, orgID, ciID, []string{id}, "")
		if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "ci_relationship" {
			// An edge outside the caller's scope names the interface.
			return &InterfaceInUseError{}
		}
		if err != nil || len(removed) > 0 {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM ci_relationship WHERE organization_id = $1
			AND (source_interface_id = $2 OR target_interface_id = $2) ORDER BY id`, orgID, id)
		if err != nil {
			return fmt.Errorf("read relationships of interface %s: %w", name, err)
		}
		edges, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read relationships of interface %s: %w", name, err)
		}
		return &InterfaceInUseError{RelationshipIDs: edges}
	})
}

// changeActor is the user of ctx as actor_id of a ci_change row, or nil
// when the subject of ctx is no user id.
func changeActor(ctx context.Context) any {
	if id := tenant.FromContext(ctx).UserID; audit.IsUUID(id) {
		return id
	}
	return nil
}

// removeInterfaces removes the interfaces ids of the CI ciID and returns the
// names of those it removed (REL-05, F-042). An interface that an edge
// other than an unverified discovery edge names as an end – manual,
// verified or of another source – stays; the foreign keys of the interface
// ends are NO ACTION (000159). The unverified discovery edges at the
// interfaces are deleted first, whether the interface stays or not, and
// each is recorded in the history of the CI (ci_change relationship_change
// with the edge as old_value, AUD-03; reason completes the comment) and in
// the history of the edge (relationship_change removed with reason
// interface_removed, REL-09). A bound
// address whose address is also unbound in the same VRF is deleted with its
// interface; the foreign key would unbind it into a duplicate of that row
// (F-312). Other addresses are unbound as before.
func removeInterfaces(ctx context.Context, tx pgx.Tx, orgID, ciID string, ids []string, reason string) ([]string, error) {
	rows, err := tx.Query(ctx, `WITH removed AS (
			DELETE FROM ci_relationship r
			WHERE r.organization_id = $1 AND (r.source_interface_id = ANY ($3::uuid[]) OR r.target_interface_id = ANY ($3::uuid[]))
			  AND r.source = 'discovery' AND r.verification_state <> 'verified'
			RETURNING r.*
		), logged AS (
			INSERT INTO ci_change (organization_id, ci_id, actor_id, change_type, field_name, old_value, new_value, comment)
			SELECT $1, $2, $4::uuid, 'relationship_change', 'relationships',
				jsonb_build_object('id', removed.id, 'source_ci_id', removed.source_ci_id, 'target_ci_id', removed.target_ci_id,
					'rel_type', removed.rel_type, 'source', removed.source, 'verification_state', removed.verification_state,
					'source_interface_id', removed.source_interface_id, 'target_interface_id', removed.target_interface_id), NULL,
				format('removed with interface %s%s', (SELECT string_agg(n.name, ', ' ORDER BY n.name) FROM network_interface n
					WHERE n.id = ANY ($3::uuid[]) AND n.id IN (removed.source_interface_id, removed.target_interface_id)), $5::text)
			FROM removed ORDER BY removed.id
		)
		SELECT `+relationship.EdgeColumns+` FROM removed ORDER BY id`, orgID, ciID, ids, changeActor(ctx), reason)
	if err != nil {
		return nil, fmt.Errorf("remove discovery edges: %w", err)
	}
	edges, err := relationship.ScanEdges(rows)
	if err != nil {
		return nil, fmt.Errorf("remove discovery edges: %w", err)
	}
	// Each removed edge gets its history row removed (REL-09, WP-771):
	// source and actor of the request, collector without one; no event.
	recs := make([]*relationship.ChangeRecord, len(edges))
	for i, e := range edges {
		recs[i] = &relationship.ChangeRecord{Kind: relationship.ChangeRemoved, Before: e, Reason: "interface_removed"}
	}
	if recErr := relationship.RecordChangesTx(ctx, tx, recs); recErr != nil {
		return nil, recErr
	}
	var free []string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(n.id::text), '{}') FROM network_interface n
		WHERE n.id = ANY ($1::uuid[]) AND NOT EXISTS (SELECT 1 FROM ci_relationship r
			WHERE r.source_interface_id = n.id OR r.target_interface_id = n.id)`, ids).Scan(&free); err != nil {
		return nil, fmt.Errorf("read relationships of interfaces: %w", err)
	}
	if len(free) == 0 {
		return nil, nil
	}
	if _, err = tx.Exec(ctx, `DELETE FROM ip_address a WHERE a.interface_id = ANY ($1::uuid[]) AND EXISTS (
			SELECT 1 FROM ip_address u WHERE u.organization_id = a.organization_id AND u.vrf_id = a.vrf_id
			  AND u.address = a.address AND u.interface_id IS NULL)`, free); err != nil {
		return nil, fmt.Errorf("remove addresses: %w", err)
	}
	rows, err = tx.Query(ctx, `DELETE FROM network_interface WHERE id = ANY ($1::uuid[]) RETURNING name`, free)
	if err != nil {
		return nil, fmt.Errorf("remove interfaces: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("remove interfaces: %w", err)
	}
	slices.Sort(names)
	return names, nil
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
	// VRFID is the VRF of the record (IngestItem.VRFID); empty or unknown
	// in the organization means the default VRF (trg_ip_address_vrf).
	VRFID string
}

// SyncResult counts what a sync changed.
type SyncResult struct {
	Added, Updated, Removed int
	// Conflicts are the reservations (WP-299) of an address this sync
	// observed in the same VRF (NET-09); the reservations are unchanged.
	Conflicts []ReservationConflict
	// Observed are the VRF and host address of every address row the sync
	// wrote (from RETURNING), for the duplicate check after the commit
	// (NET-06, WP-300).
	Observed []DuplicateKey
	// BindingConflicts are the pinned addresses of another device the
	// sync observed at an interface of its CI in the same VRF (IPM-14,
	// E-216); the sync wrote no row for them.
	BindingConflicts []BindingConflict
}

// BindingConflict is a pinned address (IPM-14) of another CI that discovery
// observed at interface ObservedInterfaceID of CI ObservedCIID in the same
// VRF; the pinned row is unchanged.
type BindingConflict struct {
	IPAddressID         string
	Address             string
	VRFID               string
	PinnedCIID          string
	PinnedInterfaceID   string
	ObservedCIID        string
	ObservedInterfaceID string
}

// ReservationConflict is a reservation of an address that discovery
// observed at an interface in the same VRF (NET-09, WP-772).
type ReservationConflict struct {
	ReservationIPID string
	Address         string
	VRFID           string
	SubnetID        string
	DiscoveredIPID  string
	InterfaceID     string
	ReservedBy      string
}

// MaxMissedObservations is how many consecutive observations of a CI may
// miss an interface or address before it is removed (REC-07).
const MaxMissedObservations = 3

// SyncDiscoveredInterfaces applies one discovery observation of the
// interfaces of a CI (REC-07, NET-01): reported interfaces and addresses are
// created or updated and their miss_count is reset; a reported interface
// becomes of origin discovered. Discovered interfaces and addresses of origin
// discovery that are not pinned the observation did not report count one
// miss and are removed at the third consecutive miss, with an entry in the
// CI's history; manual interfaces and manual or pinned addresses are kept
// (E-90, E-216). Of a manual or pinned address of the CI the sync updates
// only the observation (last_seen_at, miss_count); a pinned address of
// another CI in the same VRF is not written again at the observed interface
// but reported in BindingConflicts (IPM-14), found by one set-based query
// per sync. An
// interface that a manual or verified edge names is not removed and keeps
// counting misses (REL-05, removeInterfaces). The caller syncs only observations that report
// interfaces at all. Observed addresses take the VRF of the record
// (ObservedInterface.VRFID); one set-based query afterwards reports the
// reservations of an observed address in its VRF as Conflicts (NET-09).
// Observed reports the VRF and host of the written addresses (NET-06).
func (r *PGRepository) SyncDiscoveredInterfaces(ctx context.Context, orgID, ciID string, observed []ObservedInterface) (SyncResult, error) {
	var res SyncResult
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		existing := map[string]string{} // name -> id
		// The first statement also names the source of the IPAM history
		// rows of the transaction (app.ipam_change_source, WP-908): the
		// uncorrelated sub-select runs once before the scan, without a
		// statement of its own (TestIngestStatementBudget).
		rows, err := tx.Query(ctx, `SELECT id::text, name FROM network_interface WHERE ci_id = $1 AND organization_id = $2
			AND (SELECT set_config('app.ipam_change_source', $3, true)) IS NOT NULL`, ciID, orgID, ChangeSourceDiscovery)
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

		pinnedElsewhere, err := pinnedAtOtherCI(ctx, tx, orgID, ciID, observed)
		if err != nil {
			return err
		}
		seenInterfaces := map[string]bool{}
		var seenIPs []string
		ord := 0
		for i := range observed {
			ifc := &observed[i]
			id, ok := existing[ifc.Name]
			if ok {
				if _, err = tx.Exec(ctx, `UPDATE network_interface SET if_index = NULLIF($2, 0), mac = NULLIF($3, '')::macaddr,
					speed_mbps = NULLIF($4, 0), mtu = NULLIF($5, 0), admin_status = COALESCE(NULLIF($6, ''), admin_status),
					oper_status = COALESCE(NULLIF($7, ''), oper_status), is_management = $8, miss_count = 0, origin = $9, updated_at = now()
					WHERE id = $1`, id, ifc.IfIndex, ifc.MAC, ifc.SpeedMbps, ifc.MTU, ifc.AdminStatus, ifc.OperStatus, ifc.IsManagement,
					InterfaceOriginDiscovered); err != nil {
					return fmt.Errorf("update interface %s: %w", ifc.Name, err)
				}
				res.Updated++
			} else {
				if err = tx.QueryRow(ctx, `INSERT INTO network_interface (organization_id, ci_id, name, if_index, mac, speed_mbps, mtu,
					admin_status, oper_status, is_management, origin)
					VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, '')::macaddr, NULLIF($6, 0), NULLIF($7, 0),
						COALESCE(NULLIF($8, ''), 'up'), COALESCE(NULLIF($9, ''), 'unknown'), $10, $11)
					RETURNING id::text`, orgID, ciID, ifc.Name, ifc.IfIndex, ifc.MAC, ifc.SpeedMbps, ifc.MTU,
					ifc.AdminStatus, ifc.OperStatus, ifc.IsManagement, InterfaceOriginDiscovered).Scan(&id); err != nil {
					return fmt.Errorf("create interface %s: %w", ifc.Name, err)
				}
				res.Added++
			}
			seenInterfaces[id] = true
			for _, address := range ifc.IPs {
				ord++
				// A pinned address of another CI in the same VRF: no row
				// at the observed interface, a review instead (IPM-14).
				if c, pinned := pinnedElsewhere[ord]; pinned {
					c.ObservedCIID, c.ObservedInterfaceID = ciID, id
					res.BindingConflicts = append(res.BindingConflicts, c)
					continue
				}
				var ipID string
				var key DuplicateKey
				// The VRF of the record, resolved in the organization; an
				// empty or unknown one leaves NULL and trg_ip_address_vrf
				// sets the default VRF (WP-772). An address without subnet
				// gets the longest prefix of that VRF in the same statement
				// (IPM-14, WP-912). A manual or pinned row keeps its VRF
				// and subnet; the sync records only the observation
				// (E-216).
				err = tx.QueryRow(ctx, `UPDATE ip_address SET miss_count = 0, last_seen_at = now(), updated_at = now(),
					vrf_id = CASE WHEN origin = 'discovery' AND NOT pinned THEN r.vrf ELSE vrf_id END,
					subnet_id = CASE WHEN origin = 'discovery' AND NOT pinned
						THEN COALESCE(subnet_id, ipam_longest_prefix_subnet($4, COALESCE(r.vrf, default_vrf_of($4)), address))
						ELSE subnet_id END
					FROM (SELECT (SELECT v.id FROM vrf v WHERE v.id = `+vrfParam(3)+` AND v.organization_id = $4) AS vrf) r
					WHERE interface_id = $1 AND host(address) = host($2::inet) RETURNING id::text, vrf_id::text, host(address)`,
					id, address, ifc.VRFID, orgID).Scan(&ipID, &key.VRFID, &key.Host)
				// A new address at the interface: the CI's row without
				// interface of the same host and VRF (its management
				// address, WP-913, written before the interfaces) moves to
				// the interface with its role and type, a pinned one stays
				// where it is and records only the observation (E-216);
				// else a new row of origin discovery. One statement either
				// way, through idx_ip_address_org_ci.
				if errors.Is(err, pgx.ErrNoRows) {
					err = tx.QueryRow(ctx, `WITH r AS (
							SELECT (SELECT v.id FROM vrf v WHERE v.id = `+vrfParam(4)+` AND v.organization_id = $1) AS vrf
						), adopted AS (
							UPDATE ip_address a SET interface_id = CASE WHEN a.pinned THEN a.interface_id ELSE $2 END,
								miss_count = 0, last_seen_at = now(), updated_at = now()
							  FROM r
							 WHERE a.organization_id = $1 AND a.interface_id IS NULL AND a.ci_id = $5
							   AND a.vrf_id = COALESCE(r.vrf, default_vrf_of($1)) AND host(a.address) = host($3::inet)
							RETURNING a.id, a.vrf_id, host(a.address) AS host
						), inserted AS (
							INSERT INTO ip_address (organization_id, interface_id, address, type, origin, last_seen_at, vrf_id, subnet_id)
							SELECT $1, $2, $3::inet, 'discovered', 'discovery', now(), r.vrf,
								ipam_longest_prefix_subnet($1, COALESCE(r.vrf, default_vrf_of($1)), $3::inet)
							FROM r WHERE NOT EXISTS (SELECT 1 FROM adopted)
							RETURNING id, vrf_id, host(address) AS host
						)
						SELECT id::text, vrf_id::text, host FROM adopted
						UNION ALL SELECT id::text, vrf_id::text, host FROM inserted`,
						orgID, id, address, ifc.VRFID, ciID).Scan(&ipID, &key.VRFID, &key.Host)
				}
				if err != nil {
					return fmt.Errorf("sync address %s: %w", address, err)
				}
				seenIPs = append(seenIPs, ipID)
				res.Observed = append(res.Observed, key)
			}
		}

		// Addresses of origin discovery of the CI's interfaces the
		// observation did not report; manual and pinned addresses are kept
		// (E-90, E-216).
		removedIPs, err := missAndRemove(ctx, tx, `UPDATE ip_address a SET miss_count = a.miss_count + 1
			FROM network_interface n
			WHERE a.interface_id = n.id AND n.ci_id = $1 AND a.origin = 'discovery' AND NOT a.pinned
			  AND NOT (a.id::text = ANY($2::text[]))
			RETURNING a.id::text, host(a.address), a.miss_count`, ciID, seenIPs, "ip_address", nil)
		if err != nil {
			return err
		}
		var seenIDs []string
		for id := range seenInterfaces {
			seenIDs = append(seenIDs, id)
		}
		// Discovered interfaces likewise; manual ones are kept (E-90).
		removedInterfaces, err := missAndRemove(ctx, tx, `UPDATE network_interface SET miss_count = miss_count + 1
			WHERE ci_id = $1 AND origin = 'discovered' AND NOT (id::text = ANY($2::text[]))
			RETURNING id::text, name, miss_count`, ciID, seenIDs, "network_interface", func(ids []string) ([]string, error) {
			return removeInterfaces(ctx, tx, orgID, ciID, ids,
				fmt.Sprintf(" after %d observations without it (REC-07)", MaxMissedObservations))
		})
		if err != nil {
			return err
		}
		res.Removed = len(removedIPs) + len(removedInterfaces)
		if res.Conflicts, err = reservationConflicts(ctx, tx, orgID, seenIPs); err != nil {
			return err
		}
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

// BindObservedAddress carries out accept of an ip_binding_conflict item
// (IPM-14, E-216) in the caller's transaction: the pinned address ipID moves
// to the observed interface interfaceID (its CI follows through
// ip_address_ci_bind, WP-911) and loses its pin; the history names the
// source discovery (WP-908). A row of origin discovery of the same address
// and VRF at that interface, observed before the pin, gives way. An address
// or interface that is gone or invisible is ErrNotFound. An address that is
// no longer pinned to pinnedCIID (the pin was released or the address moved
// since the item opened), or an observed interface that already holds the
// address in a manual or pinned row, is a field error (422, WP-676).
func (r *PGRepository) BindObservedAddress(ctx context.Context, orgID, ipID, interfaceID, pinnedCIID string) error {
	if !api.IsUUID(ipID) || !api.IsUUID(interfaceID) || !api.IsUUID(pinnedCIID) {
		return ErrNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM network_interface WHERE organization_id = $1 AND id = $2)`,
			orgID, interfaceID).Scan(&exists); err != nil {
			return fmt.Errorf("read interface: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		var pinnedThere bool
		if err := tx.QueryRow(ctx, `SELECT pinned AND ci_id IS NOT DISTINCT FROM $3::uuid FROM ip_address
			WHERE organization_id = $1 AND id = $2 AND `+ipVisible+` FOR UPDATE OF ip_address`,
			orgID, ipID, pinnedCIID).Scan(&pinnedThere); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("read address: %w", err)
		}
		if !pinnedThere {
			return errBindingNotPinned
		}
		if err := setChangeSource(ctx, tx, ChangeSourceDiscovery); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ip_address d USING ip_address a
			WHERE a.organization_id = $1 AND a.id = $2 AND d.organization_id = $1 AND d.interface_id = $3
			  AND d.vrf_id = a.vrf_id AND d.address_host = a.address_host AND d.id <> a.id
			  AND d.origin = 'discovery' AND NOT d.pinned`, orgID, ipID, interfaceID); err != nil {
			return fmt.Errorf("remove observed duplicate: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE ip_address SET interface_id = $3, pinned = false, updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND `+ipVisible, orgID, ipID, interfaceID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_ip_address_org_vrf_interface_address" {
				return errBindingInterfaceHolds
			}
			return addressCIError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return requireVisible(ctx, tx, "ip_address", ipVisible, ipID)
	})
}

// errBindingNotPinned: accept of an ip_binding_conflict item whose address
// is no longer pinned to the CI the item names (WP-676).
var errBindingNotPinned = &api.FieldError{Field: "ip_address_id",
	Message: "the address is no longer pinned to that CI; dismiss the item", Status: http.StatusUnprocessableEntity}

// errBindingInterfaceHolds: the observed interface already holds the
// address in a manual or pinned row (uq_ip_address_org_vrf_interface_address).
var errBindingInterfaceHolds = &api.FieldError{Field: "observed_interface_id",
	Message: "the observed interface already holds the address; dismiss the item", Status: http.StatusUnprocessableEntity}

// pinnedAtOtherCI finds, in one set-based query for all addresses of the
// observation, the pinned addresses of another CI in the VRF of the
// observation (IPM-14, E-216). The result is keyed by the position of the
// address in the observation (1-based, interfaces and their addresses in
// order). Without observed addresses it costs no statement.
func pinnedAtOtherCI(ctx context.Context, tx pgx.Tx, orgID, ciID string, observed []ObservedInterface) (map[int]BindingConflict, error) {
	var addrs, vrfs []string
	for i := range observed {
		for _, address := range observed[i].IPs {
			addrs = append(addrs, address)
			vrfs = append(vrfs, observed[i].VRFID)
		}
	}
	if len(addrs) == 0 {
		return nil, nil
	}
	// Through the partial index idx_ip_address_pinned, which holds only
	// the pinned rows.
	rows, err := tx.Query(ctx, `SELECT o.ord::int, a.id::text, a.address_host, a.vrf_id::text, a.ci_id::text,
			COALESCE(a.interface_id::text, '')
		FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS o(addr, vrf, ord)
		JOIN ip_address a ON a.organization_id = $1 AND a.pinned AND a.ci_id IS NOT NULL AND a.ci_id <> $4
		 AND a.vrf_id = COALESCE((SELECT v.id FROM vrf v WHERE v.organization_id = $1
		         AND v.id = (CASE WHEN o.vrf ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN o.vrf::uuid END)),
		       default_vrf_of($1))
		 AND a.address_host = host(o.addr::inet)
		ORDER BY o.ord, a.id`, orgID, addrs, vrfs, ciID)
	if err != nil {
		return nil, fmt.Errorf("find pinned addresses: %w", err)
	}
	type pinnedRow struct {
		ord int
		c   BindingConflict
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pinnedRow, error) {
		var p pinnedRow
		scanErr := row.Scan(&p.ord, &p.c.IPAddressID, &p.c.Address, &p.c.VRFID, &p.c.PinnedCIID, &p.c.PinnedInterfaceID)
		return p, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("find pinned addresses: %w", err)
	}
	out := make(map[int]BindingConflict, len(found))
	for _, p := range found {
		if _, dup := out[p.ord]; !dup {
			out[p.ord] = p.c
		}
	}
	return out, nil
}

// vrfParam is the VRF id parameter $n as uuid: empty or not a UUID is NULL,
// so that a record with a malformed VRF falls back to the default VRF
// instead of failing.
func vrfParam(n int) string {
	p := fmt.Sprintf("$%d", n)
	return `(CASE WHEN ` + p + `::text ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN ` + p + `::text::uuid END)`
}

// reservationConflicts finds, in one set-based query, the reservations
// (type reserved, no interface) of the organization whose address equals
// (host()) an address seenIPs names, in the VRF of that address (NET-09,
// WP-772). It changes nothing.
func reservationConflicts(ctx context.Context, tx pgx.Tx, orgID string, seenIPs []string) ([]ReservationConflict, error) {
	if len(seenIPs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text, host(r.address), r.vrf_id::text, COALESCE(r.subnet_id::text, ''),
			d.id::text, d.interface_id::text, COALESCE(r.reserved_by::text, '')
		FROM ip_address d
		JOIN ip_address r ON r.organization_id = d.organization_id AND r.vrf_id = d.vrf_id
			AND r.type = 'reserved' AND r.interface_id IS NULL AND host(r.address) = host(d.address)
		WHERE d.organization_id = $1 AND d.id = ANY ($2::uuid[]) AND d.interface_id IS NOT NULL
		ORDER BY host(r.address), d.id`, orgID, seenIPs)
	if err != nil {
		return nil, fmt.Errorf("find reservation conflicts: %w", err)
	}
	conflicts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReservationConflict, error) {
		var c ReservationConflict
		scanErr := row.Scan(&c.ReservationIPID, &c.Address, &c.VRFID, &c.SubnetID, &c.DiscoveredIPID, &c.InterfaceID, &c.ReservedBy)
		return c, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("find reservation conflicts: %w", err)
	}
	return conflicts, nil
}

// missAndRemove counts one miss with countQuery and deletes the rows of
// table that reached MaxMissedObservations; it returns their names. remove,
// when set, removes those rows by their ids instead and returns the names of
// the rows it removed.
func missAndRemove(ctx context.Context, tx pgx.Tx, countQuery, ciID string, seen []string, table string,
	remove func(ids []string) ([]string, error)) ([]string, error) {
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
	switch {
	case len(ids) == 0:
	case remove != nil:
		return remove(ids)
	default:
		if _, err = tx.Exec(ctx, "DELETE FROM "+table+" WHERE id::text = ANY($1::text[])", ids); err != nil {
			return nil, fmt.Errorf("remove %s: %w", table, err)
		}
	}
	return names, nil
}

// --- Usage and free space (IPM-03, WP-903) ---

// SubnetOccupancy reads the occupancy of a subnet through
// ipam_subnet_occupancy (migration 000301), which checks the subnet under
// the caller's row level security before it widens the scope.
func (r *PGRepository) SubnetOccupancy(ctx context.Context, orgID, subnetID string) (*Occupancy, error) {
	if !api.IsUUID(subnetID) {
		return nil, ErrNotFound
	}
	var o *Occupancy
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		o, err = readOccupancy(ctx, tx, orgID, subnetID)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "P0002" {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}
