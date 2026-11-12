package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Atomic allocation (IPM-03, WP-904): POST /subnets/{id}/allocate gives the
// lowest free host address of a subnet, POST /subnets/{id}/allocate-subnet
// the lowest aligned free prefix of a length inside it. Both take the
// transaction-scoped advisory lock of the subnet (allocationLockSQL), so
// two allocations of the same subnet never choose the same value; they
// read the occupancy through ipam_subnet_occupancy (migration 000301) and
// compute with the rules of WP-903 (space.go). Writers that do not take the
// lock (POST /ips, reservations, Discovery) can still occupy the chosen
// address between the read and the insert: the unique index of
// interface-less addresses per VRF then refuses the insert, and the
// allocation tries the next free address, at most allocationRetries times.

// AllocationRepository allocates the next free address or subnet of a
// subnet under the allocation lock.
type AllocationRepository interface {
	// AllocateAddress stores a (Type, InterfaceID, DNSName, Description,
	// ReservedBy set by the caller) at the lowest free host address of
	// subnet a.SubnetID and fills a with the stored row.
	AllocateAddress(ctx context.Context, a *IPAddress) error
	// AllocateSubnet stores s (Name, ClientID, IsPool, SectionID; empty
	// ones from the parent) as the lowest aligned free prefix of length
	// prefixLen inside subnet parentID and fills s with the stored row.
	AllocateSubnet(ctx context.Context, parentID string, prefixLen int, s *Subnet) error
}

// AllocateAddressRequest is the payload of POST /subnets/{id}/allocate.
type AllocateAddressRequest struct {
	// Type is static (default) or reserved.
	Type        string `json:"type,omitempty"`
	InterfaceID string `json:"interface_id,omitempty"`
	DNSName     string `json:"dns_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// AllocateSubnetRequest is the payload of POST /subnets/{id}/allocate-subnet.
type AllocateSubnetRequest struct {
	PrefixLength *int   `json:"prefix_length"`
	Name         string `json:"name,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	IsPool       bool   `json:"is_pool,omitempty"`
	SectionID    string `json:"section_id,omitempty"`
}

// allocationRetries is the number of further attempts after a writer
// without the lock took the chosen address (WP-904 AK1).
const allocationRetries = 3

// allocationLockSQL takes the allocation lock of subnet $1 until the end
// of the transaction.
const allocationLockSQL = "SELECT pg_advisory_xact_lock(hashtextextended('subnet_alloc:' || $1::text, 0))"

// heldBatch is the number of free candidates checked at once against the
// addresses of the VRF outside the subnet; maxHeldBatches bounds the scan.
const (
	heldBatch      = 32
	maxHeldBatches = 64
)

// ProblemSubnetExhausted is the problem type of a subnet without room for
// the requested allocation (409).
const ProblemSubnetExhausted = "urn:reticora:problem:subnet-exhausted"

// Answers of the allocation rules.
var (
	// ErrSubnetExhausted: no free address or no free prefix of the length.
	ErrSubnetExhausted = errors.New("the subnet has no free space for the allocation")
	// errAllocationContended: writers without the lock took the chosen
	// address on every attempt.
	errAllocationContended = &api.ConflictError{Field: "address",
		Message: "the free addresses were taken concurrently; retry the allocation"}
	errAllocateType = &api.FieldError{Field: "type", Message: "type must be static or reserved",
		Status: http.StatusUnprocessableEntity}
	errAllocateInterface = &api.FieldError{Field: "interface_id", Message: "network interface not found",
		Status: http.StatusUnprocessableEntity}
)

// nextFreeAddress is the lowest free host address of o that held does not
// name, checking candidates in batches; "" when there is none.
func nextFreeAddress(o *Occupancy, held func(candidates []string) (map[string]bool, error)) (string, error) {
	work := *o
	work.Addresses = append([]OccupiedAddress(nil), o.Addresses...)
	for batch := 0; batch < maxHeldBatches; batch++ {
		candidates, err := ComputeFreeAddresses(&work, heldBatch)
		if err != nil {
			return "", err
		}
		if len(candidates) == 0 {
			return "", nil
		}
		taken, err := held(candidates)
		if err != nil {
			return "", err
		}
		for _, c := range candidates {
			if !taken[c] {
				return c, nil
			}
			work.Addresses = append(work.Addresses, OccupiedAddress{Host: c})
		}
	}
	return "", errAllocationContended
}

// --- PostgreSQL ---

// readOccupancy reads the occupancy of the visible subnet subnetID in tx
// (SubnetOccupancy).
func readOccupancy(ctx context.Context, tx pgx.Tx, orgID, subnetID string) (*Occupancy, error) {
	o := &Occupancy{}
	if err := tx.QueryRow(ctx, `SELECT cidr::text, COALESCE(host(gateway), ''), is_pool FROM subnet
		WHERE organization_id = $1 AND id = $2`, orgID, subnetID).Scan(&o.CIDR, &o.Gateway, &o.IsPool); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT kind, value, COALESCE(ip_type, ''), COALESCE(ip_state, '') FROM ipam_subnet_occupancy($1)", subnetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, value, ipType, state string
		if err := rows.Scan(&kind, &value, &ipType, &state); err != nil {
			return nil, err
		}
		if kind == "child" {
			o.Children = append(o.Children, value)
		} else {
			o.Addresses = append(o.Addresses, OccupiedAddress{Host: value, Type: ipType, State: state})
		}
	}
	return o, rows.Err()
}

// lockAllocation takes the allocation lock of the visible subnet subnetID
// and returns its VRF; ErrNotFound when the caller does not see it.
func lockAllocation(ctx context.Context, tx pgx.Tx, orgID, subnetID string) (string, error) {
	var vrfID string
	err := tx.QueryRow(ctx, "SELECT vrf_id::text FROM subnet WHERE organization_id = $1 AND id = $2", orgID, subnetID).Scan(&vrfID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read subnet: %w", err)
	}
	if _, err := tx.Exec(ctx, allocationLockSQL, subnetID); err != nil {
		return "", fmt.Errorf("lock subnet allocation: %w", err)
	}
	return vrfID, nil
}

// isUniqueViolation reports a unique violation of the insert.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// heldAddresses returns those of the candidate host addresses the VRF
// vrfID already holds, in any subnet, on an interface or without a subnet.
// The check runs with the client, site and team scope widened
// (app_scope_widen, migration 000139; pattern nextOrderNumber and
// ipam_subnet_occupancy, 000301), restored before the insert: under the
// caller's scope an address of another client (on its interface, or in its
// subnet) looked free, so the allocation handed out an address in use or
// failed on the unique index again and again. Only the host addresses
// leave the widened statement.
func heldAddresses(ctx context.Context, tx pgx.Tx, orgID, vrfID string, candidates []string) (map[string]bool, error) {
	var previous string
	if err := tx.QueryRow(ctx, `SELECT app_scope_widen()::text`).Scan(&previous); err != nil {
		return nil, fmt.Errorf("check held addresses: widen scope: %w", err)
	}
	out := map[string]bool{}
	err := func() error {
		rows, err := tx.Query(ctx, `SELECT address_host FROM ip_address
			WHERE organization_id = $1 AND vrf_id = $2 AND address_host = ANY($3::text[])`, orgID, vrfID, candidates)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return err
			}
			out[h] = true
		}
		return rows.Err()
	}()
	if _, restoreErr := tx.Exec(ctx, `SELECT app_scope_restore($1::text[])`, previous); restoreErr != nil && err == nil {
		err = restoreErr
	}
	if err != nil {
		return nil, fmt.Errorf("check held addresses: %w", err)
	}
	return out, nil
}

// AllocateAddress implements AllocationRepository: under the allocation
// lock it computes the lowest free host address of the subnet, skips an
// address the VRF holds outside the subnet (on an interface or without a
// subnet, of any client: heldAddresses), and inserts the address in a
// savepoint; a unique violation by a writer without the lock is retried
// with the next free address, never with the contended one.
func (r *PGRepository) AllocateAddress(ctx context.Context, a *IPAddress) error {
	if !api.IsUUID(a.SubnetID) {
		return ErrNotFound
	}
	if a.InterfaceID != "" && !api.IsUUID(a.InterfaceID) {
		return errAllocateInterface
	}
	orgID := a.OrganizationID
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		vrfID, err := lockAllocation(ctx, tx, orgID, a.SubnetID)
		if err != nil {
			return err
		}
		// The interface under the caller's row level security, as POST
		// /ips sees it (requireVisible after the insert).
		if a.InterfaceID != "" {
			var visible bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM network_interface WHERE organization_id = $1 AND id = $2 AND "+
				nicVisible+")", orgID, a.InterfaceID).Scan(&visible); err != nil {
				return fmt.Errorf("check interface: %w", err)
			}
			if !visible {
				return errAllocateInterface
			}
		}
		// contended are the addresses whose insert failed on a unique
		// violation; a retry never picks them again.
		contended := map[string]bool{}
		held := func(candidates []string) (map[string]bool, error) {
			out, err := heldAddresses(ctx, tx, orgID, vrfID, candidates)
			if err != nil {
				return nil, err
			}
			for _, c := range candidates {
				if contended[c] {
					out[c] = true
				}
			}
			return out, nil
		}
		for attempt := 0; attempt <= allocationRetries; attempt++ {
			o, err := readOccupancy(ctx, tx, orgID, a.SubnetID)
			if err != nil {
				return err
			}
			if o.IsPool {
				return errFreeAddressesOfPool
			}
			address, err := nextFreeAddress(o, held)
			if err != nil {
				return err
			}
			if address == "" {
				return ErrSubnetExhausted
			}
			var created *IPAddress
			err = database.Savepoint(database.ContextWithTx(ctx, tx), func(ctx context.Context) error {
				var insertErr error
				created, insertErr = scanIP(tx.QueryRow(ctx, `INSERT INTO ip_address
						(organization_id, subnet_id, interface_id, address, type, dns_name, description, reserved_by)
					VALUES ($1, $2, $3, $4::inet, $5, $6, $7, $8) RETURNING `+ipCols,
					orgID, a.SubnetID, nilIfEmpty(a.InterfaceID), address, a.Type, nilIfEmpty(a.DNSName),
					nilIfEmpty(a.Description), nilIfEmpty(a.ReservedBy)))
				return insertErr
			})
			if isUniqueViolation(err) {
				contended[address] = true
				continue
			}
			if err != nil {
				return err
			}
			if err := requireVisible(ctx, tx, "ip_address", ipVisible, created.ID); err != nil {
				return err
			}
			*a = *created
			return nil
		}
		return errAllocationContended
	})
}

// AllocateSubnet implements AllocationRepository: under the allocation lock
// of the parent and the tree lock of its VRF it computes the lowest aligned
// free prefix of length prefixLen and inserts it as a child (the placement
// trigger sets the parent); ipam.subnet.changed (allocated) is written in
// the transaction.
func (r *PGRepository) AllocateSubnet(ctx context.Context, parentID string, prefixLen int, s *Subnet) error {
	if !api.IsUUID(parentID) {
		return ErrNotFound
	}
	orgID := s.OrganizationID
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var vrfID, parentClient, parentSite, parentSection string
		err := tx.QueryRow(ctx, `SELECT vrf_id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), section_id::text
			FROM subnet WHERE organization_id = $1 AND id = $2`, orgID, parentID).Scan(&vrfID, &parentClient, &parentSite, &parentSection)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read parent subnet: %w", err)
		}
		if _, err = tx.Exec(ctx, allocationLockSQL, parentID); err != nil {
			return fmt.Errorf("lock subnet allocation: %w", err)
		}
		if s.ClientID == "" {
			s.ClientID = parentClient
		}
		if s.SectionID == "" {
			s.SectionID = parentSection
		}
		err = lockSubnetTrees(ctx, tx, orgID, vrfID)
		if err != nil {
			return err
		}
		err = checkSection(ctx, tx, orgID, s.SectionID, s.ClientID)
		if err != nil {
			return err
		}
		o, err := readOccupancy(ctx, tx, orgID, parentID)
		if err != nil {
			return err
		}
		free, err := ComputeFreeSubnets(o, prefixLen, 1)
		if err != nil {
			return err
		}
		if len(free) == 0 {
			return ErrSubnetExhausted
		}
		err = placed(ctx, tx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO subnet (organization_id, client_id, site_id, cidr, name, vrf_id, section_id, is_pool)
				VALUES ($1, $2, $3, $4::cidr, $5, $6, $7, $8) RETURNING id::text`,
				orgID, nilIfEmpty(s.ClientID), nilIfEmpty(parentSite), free[0], nilIfEmpty(s.Name), vrfID,
				nilIfEmpty(s.SectionID), s.IsPool).Scan(&s.ID)
		})
		if err != nil {
			return err
		}
		stored, err := scanSubnet(tx.QueryRow(ctx, "SELECT "+subnetCols+" FROM subnet WHERE organization_id = $1 AND id = $2", orgID, s.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			// Written outside the caller's scope.
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		*s = *stored
		return r.emitSubnetChanged(ctx, tx, s, "allocated")
	})
}

// emitSubnetChanged writes ipam.subnet.changed for subnet s (IPM-02,
// Konvention 13) as the last write of the transaction (Konvention 20);
// nothing without an outbox.
func (r *PGRepository) emitSubnetChanged(ctx context.Context, tx pgx.Tx, s *Subnet, kind string) error {
	if r.outbox == nil {
		return nil
	}
	data := events.SubnetChangedData{ID: s.ID, ClientID: s.ClientID, SiteID: s.SiteID, CIDR: s.CIDR, ChangeKind: kind}
	orgID := s.OrganizationID
	return database.BeforeCommit(ctx, func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.IPAMSubnetChanged, EntityID: data.ID, Data: data})
		return err
	})
}

// --- Memory ---

// occupancyLocked is SubnetOccupancy under a held lock.
func (r *MemoryRepository) occupancyLocked(orgID, subnetID string) (*Occupancy, error) {
	s, ok := r.subnets[subnetID]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	o := &Occupancy{CIDR: s.CIDR, Gateway: s.Gateway, IsPool: s.IsPool}
	for _, a := range r.addresses {
		if a.OrganizationID == orgID && a.SubnetID == subnetID {
			o.Addresses = append(o.Addresses, OccupiedAddress{Host: hostOf(a.Address), Type: a.Type, State: a.State})
		}
	}
	for _, c := range r.subnets {
		if c.OrganizationID == orgID && c.ParentID == subnetID {
			o.Children = append(o.Children, c.CIDR)
		}
	}
	return o, nil
}

// AllocateAddress implements AllocationRepository under the mutex.
func (r *MemoryRepository) AllocateAddress(_ context.Context, a *IPAddress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.occupancyLocked(a.OrganizationID, a.SubnetID)
	if err != nil {
		return err
	}
	if o.IsPool {
		return errFreeAddressesOfPool
	}
	if a.InterfaceID != "" {
		if ni, ok := r.interfaces[a.InterfaceID]; !ok || ni.OrganizationID != a.OrganizationID {
			return errAllocateInterface
		}
	}
	sn := r.subnets[a.SubnetID]
	vrfID := sn.VRFID
	if vrfID == "" {
		vrfID = r.defaultVRF(a.OrganizationID).ID
	}
	address, err := nextFreeAddress(o, func(candidates []string) (map[string]bool, error) {
		want := map[string]bool{}
		for _, c := range candidates {
			want[c] = true
		}
		out := map[string]bool{}
		for _, e := range r.addresses {
			if h := hostOf(e.Address); e.OrganizationID == a.OrganizationID && e.VRFID == vrfID && want[h] {
				out[h] = true
			}
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	if address == "" {
		return ErrSubnetExhausted
	}
	r.seq++
	now := time.Now().UTC()
	stored := IPAddress{ID: fmt.Sprintf("ip-%d", r.seq), OrganizationID: a.OrganizationID, SubnetID: a.SubnetID,
		InterfaceID: a.InterfaceID, Address: address, VRFID: vrfID, Type: a.Type, State: IPStateUnknown,
		DNSName: a.DNSName, Description: a.Description, ReservedBy: a.ReservedBy, CreatedAt: now, UpdatedAt: now}
	r.addresses[stored.ID] = &stored
	*a = stored
	return nil
}

// AllocateSubnet implements AllocationRepository under the mutex.
func (r *MemoryRepository) AllocateSubnet(_ context.Context, parentID string, prefixLen int, s *Subnet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, err := r.occupancyLocked(s.OrganizationID, parentID)
	if err != nil {
		return err
	}
	parent := r.subnets[parentID]
	free, err := ComputeFreeSubnets(o, prefixLen, 1)
	if err != nil {
		return err
	}
	if len(free) == 0 {
		return ErrSubnetExhausted
	}
	if s.ClientID == "" {
		s.ClientID = parent.ClientID
	}
	if s.SectionID == "" {
		s.SectionID = parent.SectionID
	}
	s.CIDR, s.VRFID, s.SiteID = free[0], parent.VRFID, parent.SiteID
	return r.createSubnetLocked(s)
}
