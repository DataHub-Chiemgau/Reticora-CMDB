package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// Duplicate addresses within a VRF (NET-06, WP-300, E-53): the same host
// address (prefix length ignored) in one organization and VRF at
// interfaces of at least two CIs (an address without interface bound to a
// CI with ci_id counts as that CI's, IPM-13), or an address without
// interface and without CI of type static or dhcp next to an interface
// address (a bound one is its CI's address). Several interfaces of one
// CI (bond, bridge) are no duplicate; a reservation next to an interface
// address is the conflict of WP-772. Nothing is overwritten or refused: the
// check opens a duplicate_ip review item.
//
// The check runs after the writing transactions have committed, in a
// transaction of its own with the organization-wide scope (duplicates cross
// client boundaries), as one set-based query over ip_address.address_host
// (idx_ip_address_org_vrf_host, migration 000213). Two concurrent writers
// need no lock: the check that starts last sees both rows, and the partial
// unique index review_item_open_object keeps a second open item away.

// DuplicateKey is a host address (host(inet), without prefix length) in a
// VRF.
type DuplicateKey struct {
	VRFID string
	Host  string
}

// DuplicateSource names the addresses to check: explicit keys (the
// ingest), the keys of ip_address rows (POST and PATCH /ips) and the keys of
// the addresses of a subnet (PATCH /subnets/{id} with a new VRF).
type DuplicateSource struct {
	Keys     []DuplicateKey
	IPIDs    []string
	SubnetID string
	// Claim is the management IP of a CI without an address row of its
	// own (WP-913): another CI holds the address without interface.
	Claim *DuplicateClaim
}

// DuplicateClaim is the management IP of a CI in a VRF that has no row in
// ip_address (IPM-13, WP-913): the address is a duplicate when a row of
// another CI holds it.
type DuplicateClaim struct {
	DuplicateKey
	CIID string
}

// HolderTypeManagementIP is the type of a holder that is only the
// management IP of its CI (DuplicateClaim).
const HolderTypeManagementIP = "management_ip"

// empty reports whether the source names no address.
func (s *DuplicateSource) empty() bool {
	return len(s.Keys) == 0 && len(s.IPIDs) == 0 && s.SubnetID == "" && s.Claim == nil
}

// DuplicateHolder is one ip_address row of a duplicate address.
type DuplicateHolder struct {
	IPAddressID string `json:"ip_address_id"`
	InterfaceID string `json:"interface_id,omitempty"`
	CIID        string `json:"ci_id,omitempty"`
	Type        string `json:"type"`
}

// sideKey is the side of a holder: <ci_id>/<interface_id>, or
// ip/<ip_address_id> for a row without interface.
func (d *DuplicateHolder) sideKey() string {
	if d.IPAddressID == "" {
		// The management IP of a CI without a row (DuplicateClaim).
		return "ci/" + d.CIID
	}
	if d.InterfaceID == "" {
		return "ip/" + d.IPAddressID
	}
	return d.CIID + "/" + d.InterfaceID
}

// Duplicate is an address of a VRF with all its rows.
type Duplicate struct {
	VRFID   string
	Address string
	Holders []DuplicateHolder
}

// DuplicateFinder finds the duplicate addresses among those src names, in
// one statement of the context's transaction or of a transaction of its
// own. The PostgreSQL repository implements it; the memory repository does
// not check.
type DuplicateFinder interface {
	FindDuplicates(ctx context.Context, orgID string, src DuplicateSource) ([]Duplicate, error)
}

// DuplicateIPReviews counts the duplicate_ip items the duplicate check
// opened; deduplicated and suppressed ones are not counted.
var DuplicateIPReviews = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "reticora",
	Name:      "duplicate_ip_reviews_total",
	Help:      "duplicate_ip review items opened for an address at interfaces of several CIs, or next to a static or dhcp address, in one VRF (NET-06).",
})

// RegisterMetrics registers the IPAM metrics on reg: the duplicate check
// and the results of the subnet scan (ScanUpdates, WP-1063).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(DuplicateIPReviews, ScanUpdates)
}

// duplicatesQuery finds the duplicate addresses among the keys k (a
// subquery of the VRF and host addresses to check) and returns all rows of
// each duplicate address. The keys are compared with address_host through
// idx_ip_address_org_vrf_host.
const duplicatesQuery = `WITH k AS (%s), dup AS (
		SELECT a.vrf_id, a.address_host
		FROM k JOIN ip_address a ON a.organization_id = $1 AND a.vrf_id = k.vrf_id AND a.address_host = k.host
		LEFT JOIN network_interface n ON n.id = a.interface_id AND n.organization_id = a.organization_id
		GROUP BY a.vrf_id, a.address_host
		HAVING count(DISTINCT COALESCE(n.ci_id, a.ci_id)) >= 2
			OR (count(n.ci_id) >= 1 AND bool_or(a.interface_id IS NULL AND a.ci_id IS NULL AND a.type IN ('static', 'dhcp')))
	)
	SELECT a.vrf_id::text, a.address_host, a.id::text, COALESCE(a.interface_id::text, ''), COALESCE(n.ci_id::text, a.ci_id::text, ''), a.type
	FROM dup JOIN ip_address a ON a.organization_id = $1 AND a.vrf_id = dup.vrf_id AND a.address_host = dup.address_host
	LEFT JOIN network_interface n ON n.id = a.interface_id AND n.organization_id = a.organization_id
	ORDER BY 1, 2, 3`

// keys returns the subquery of the addresses src names and its arguments
// after the organization ($1): only the parts src uses, so that every
// statement plans its own index access.
func (s *DuplicateSource) keys() (query string, args []any) {
	var parts []string
	param := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args)+1)
	}
	if len(s.Keys) > 0 {
		vrfs := make([]string, 0, len(s.Keys))
		hosts := make([]string, 0, len(s.Keys))
		for _, k := range s.Keys {
			vrfs, hosts = append(vrfs, k.VRFID), append(hosts, k.Host)
		}
		parts = append(parts, "SELECT u.vrf_id, u.host FROM unnest("+param(vrfs)+"::uuid[], "+param(hosts)+"::text[]) AS u(vrf_id, host)")
	}
	if len(s.IPIDs) > 0 {
		parts = append(parts, "SELECT s.vrf_id, s.address_host AS host FROM ip_address s WHERE s.organization_id = $1 AND s.id = ANY ("+
			param(s.IPIDs)+"::uuid[])")
	}
	if s.SubnetID != "" {
		parts = append(parts, "SELECT s.vrf_id, s.address_host AS host FROM ip_address s WHERE s.organization_id = $1 AND s.subnet_id = "+
			param(s.SubnetID)+"::uuid")
	}
	return strings.Join(parts, " UNION "), args
}

// FindDuplicates implements DuplicateFinder with one query, and one more
// for the claim of a management IP (WP-913).
func (r *PGRepository) FindDuplicates(ctx context.Context, orgID string, src DuplicateSource) ([]Duplicate, error) {
	if src.empty() {
		return nil, nil
	}
	keys, args := src.keys()
	var out []Duplicate
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if keys != "" {
			rows, err := tx.Query(ctx, fmt.Sprintf(duplicatesQuery, keys), append([]any{orgID}, args...)...)
			if err != nil {
				return err
			}
			if out, err = scanDuplicates(rows, out); err != nil {
				return err
			}
		}
		if src.Claim == nil {
			return nil
		}
		claimed, err := claimDuplicate(ctx, tx, orgID, src.Claim)
		if claimed != nil {
			out = append(out, *claimed)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("find duplicate addresses: %w", err)
	}
	return out, nil
}

// scanDuplicates groups the rows of the duplicates query (ordered by VRF
// and address) into out.
func scanDuplicates(rows pgx.Rows, out []Duplicate) ([]Duplicate, error) {
	defer rows.Close()
	for rows.Next() {
		var vrfID, host string
		var h DuplicateHolder
		if err := rows.Scan(&vrfID, &host, &h.IPAddressID, &h.InterfaceID, &h.CIID, &h.Type); err != nil {
			return out, err
		}
		if n := len(out); n == 0 || out[n-1].VRFID != vrfID || out[n-1].Address != host {
			out = append(out, Duplicate{VRFID: vrfID, Address: host})
		}
		last := &out[len(out)-1]
		last.Holders = append(last.Holders, h)
	}
	return out, rows.Err()
}

// claimDuplicate returns the address of the claim with all its rows and
// the claiming CI as holder of type management_ip, when a row of another
// CI holds the address and none of the claiming CI does; nil otherwise.
func claimDuplicate(ctx context.Context, tx pgx.Tx, orgID string, c *DuplicateClaim) (*Duplicate, error) {
	rows, err := tx.Query(ctx, `SELECT a.vrf_id::text, a.address_host, a.id::text, COALESCE(a.interface_id::text, ''),
			COALESCE(n.ci_id::text, a.ci_id::text, ''), a.type
		FROM ip_address a
		LEFT JOIN network_interface n ON n.id = a.interface_id AND n.organization_id = a.organization_id
		WHERE a.organization_id = $1 AND a.vrf_id = $2 AND a.address_host = $3
		ORDER BY a.id`, orgID, c.VRFID, c.Host)
	if err != nil {
		return nil, err
	}
	found, err := scanDuplicates(rows, nil)
	if err != nil || len(found) != 1 {
		return nil, err
	}
	d := found[0]
	other, own := false, false
	for i := range d.Holders {
		other = other || (d.Holders[i].CIID != "" && d.Holders[i].CIID != c.CIID)
		own = own || d.Holders[i].CIID == c.CIID
	}
	if !other || own {
		return nil, nil
	}
	d.Holders = append(d.Holders, DuplicateHolder{CIID: c.CIID, Type: HolderTypeManagementIP})
	return &d, nil
}

// SideKeys returns the sorted sides of the duplicate (E-53).
func (d *Duplicate) SideKeys() []string {
	keys := make([]string, 0, len(d.Holders))
	for i := range d.Holders {
		keys = append(keys, d.Holders[i].sideKey())
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// candidateCIs returns the sorted CIs of the duplicate.
func (d *Duplicate) candidateCIs() []string {
	var ids []string
	for i := range d.Holders {
		if d.Holders[i].CIID != "" {
			ids = append(ids, d.Holders[i].CIID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// DuplicateReview is the duplicate_ip item of a duplicate address (NET-06,
// E-53): the object is the address in its VRF, the payload names the
// address, the VRF, all rows (holders) and the sorted sides (side_keys),
// the candidates are the CIs involved.
func DuplicateReview(orgID string, d *Duplicate) *reviews.Item {
	return &reviews.Item{
		OrganizationID: orgID,
		Kind:           reviews.KindDuplicateIP,
		Status:         reviews.StatusOpen,
		ObjectKey:      "ip:" + d.VRFID + "/" + d.Address,
		Payload: map[string]any{
			"address":   d.Address,
			"vrf_id":    d.VRFID,
			"holders":   d.Holders,
			"side_keys": d.SideKeys(),
		},
		CandidateCIIDs: d.candidateCIs(),
	}
}

// sameSides reports whether the side_keys of a stored payload equal keys.
func sameSides(stored any, keys []string) bool {
	a, errA := json.Marshal(stored)
	b, errB := json.Marshal(keys)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// CheckDuplicates finds the duplicate addresses among those src names and
// opens a duplicate_ip item for each through svc. It runs detached from a
// transaction in ctx, with the organization-wide scope of the acting user
// (database.OrgWideScope): without duplicate one transaction of three
// statements. A dismissed or accepted item suppresses a new one while the
// sides are the same (OpenUnlessSettled with side_keys, E-53); an open item
// of the address in the VRF, also one of WP-772, is reused.
func CheckDuplicates(ctx context.Context, finder DuplicateFinder, svc *reviews.Service, orgID string, src DuplicateSource) error {
	if finder == nil || svc == nil || src.empty() {
		return nil
	}
	userID := ""
	if scope, ok := database.TenantScopeFromContext(ctx); ok {
		userID = scope.UserID
	}
	scope := database.OrgWideScope(orgID, userID)
	ctx = database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
	dups, err := finder.FindDuplicates(ctx, orgID, src)
	if err != nil {
		return err
	}
	for i := range dups {
		item := DuplicateReview(orgID, &dups[i])
		sides := dups[i].SideKeys()
		opened, err := svc.OpenUnlessSettled(ctx, item, "side_keys", func(_ context.Context, accepted *reviews.Item) (bool, error) {
			return sameSides(accepted.Payload["side_keys"], sides), nil
		})
		if err != nil {
			return fmt.Errorf("open duplicate_ip item of %s: %w", item.ObjectKey, err)
		}
		if opened {
			DuplicateIPReviews.Inc()
		}
	}
	return nil
}
