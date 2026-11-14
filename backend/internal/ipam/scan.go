package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The subnet scan (IPM-11, WP-1062): a subnet with scan_enabled is checked
// by managed ICMP probes of the probe engine (managed_by ipam_scan, key
// scan:<subnet>:<address>), one per known address of the subnet and, with
// discover_new_hosts, per further host address of an IPv4 subnet up to /22.
// The collector group of the subnet runs them (redundancy after PRB-03,
// WP-936); the probes count against the probe budget like manual ones
// (PKG-04). An unmanaged icmp probe of the organization on the same target,
// collector group and client (e.g. an auto probe of WP-949) is reused
// (owned false) instead of a second one. subnet_scan_target holds the targets;
// switching the scan off deletes them and with them the probes of the scan.
// The results (address state, last_seen_at, new hosts) apply the system
// job ipam.scan.apply (scan_results.go, WP-1063).

// MaxDiscoverPrefixIPv4 is the widest IPv4 subnet discover_new_hosts takes
// (/22: 1,022 hosts).
const MaxDiscoverPrefixIPv4 = 22

// scanProbeType is the probe type of the scan.
const scanProbeType = "icmp"

// ScanProbeSpec is the managed probe of one scan target (probe.ManagedSpec
// of type icmp without CI).
type ScanProbeSpec struct {
	Key              string
	ClientID         string
	CollectorGroupID string
	Target           string
	// IntervalS is the interval override (0: the effective value of the
	// type).
	IntervalS int
}

// ScanProbes writes the managed probes of the scan (probe.ScanProbes; this
// package cannot import the probe package).
type ScanProbes interface {
	// LockProbeBudget takes the probe budget lock of the organization, the
	// first lock of the transaction (WP-940 AK3).
	LockProbeBudget(ctx context.Context, tx pgx.Tx, orgID string) error
	// SyncScanProbes makes the managed scan probes with keyPrefix those of
	// specs and returns the probe id per key.
	SyncScanProbes(ctx context.Context, tx pgx.Tx, orgID, keyPrefix string, specs []ScanProbeSpec) (map[string]string, error)
	// SyncPTRProbes makes the managed dns probes of the reverse check
	// with keyPrefix those of specs (managed_by ipam_ptr, WP-1064).
	SyncPTRProbes(ctx context.Context, tx pgx.Tx, orgID, keyPrefix string, specs []PTRProbeSpec) error
}

// ScanMember is a member of the collector group of the subnet (read only).
type ScanMember struct {
	CollectorID  string `json:"collector_id"`
	Priority     int    `json:"priority"`
	MemberStatus string `json:"member_status"`
}

// SubnetScan is the scan configuration of a subnet with the members of its
// collector group and the target numbers (AK6).
type SubnetScan struct {
	SubnetID         string       `json:"subnet_id"`
	ScanEnabled      bool         `json:"scan_enabled"`
	CollectorGroupID string       `json:"collector_group_id,omitempty"`
	ScanIntervalS    *int         `json:"scan_interval_s"`
	DiscoverNewHosts bool         `json:"discover_new_hosts"`
	OfflineAfterS    int          `json:"offline_after_s"`
	ScanSyncedAt     *time.Time   `json:"scan_synced_at"`
	Members          []ScanMember `json:"members"`
	Targets          int          `json:"targets"`
	Owned            int          `json:"owned"`
	Reused           int          `json:"reused"`
	// LastAppliedAt is the last run of ipam.scan.apply that applied the
	// probe states (subnet.scan_applied_at, WP-1063 AK7).
	LastAppliedAt *time.Time `json:"last_applied_at"`
	// TargetStates counts the targets per state of their address; none is
	// a target without an address or an address without a state.
	TargetStates ScanTargetStates `json:"target_states"`
	// NewHosts counts the addresses the scan created (origin scan, type
	// discovered).
	NewHosts int `json:"new_hosts"`
}

// ScanTargetStates are the targets of a scan per address state (AK7).
type ScanTargetStates struct {
	Active  int `json:"active"`
	Offline int `json:"offline"`
	Unknown int `json:"unknown"`
	None    int `json:"none"`
}

// ScanUpdate are the fields of PATCH /subnets/{id}/scan; a nil field
// keeps its value. CollectorGroupID and ScanIntervalS distinguish null
// (clear) from absent.
type ScanUpdate struct {
	ScanEnabled      *bool
	CollectorGroupID *string
	ClearGroup       bool
	ScanIntervalS    *int
	ClearInterval    bool
	DiscoverNewHosts *bool
	OfflineAfterS    *int
}

// ParseScanUpdate reads the body of PATCH /subnets/{id}/scan.
func ParseScanUpdate(body map[string]json.RawMessage) (*ScanUpdate, error) {
	u := &ScanUpdate{}
	for name, raw := range body {
		isNull := string(raw) == "null"
		var err error
		switch name {
		case "scan_enabled":
			u.ScanEnabled = new(bool)
			err = json.Unmarshal(raw, u.ScanEnabled)
		case "collector_group_id":
			if isNull {
				u.ClearGroup = true
				continue
			}
			u.CollectorGroupID = new(string)
			err = json.Unmarshal(raw, u.CollectorGroupID)
			if err == nil && !api.IsUUID(*u.CollectorGroupID) {
				return nil, scanField("collector_group_id", "collector group not found")
			}
		case "scan_interval_s":
			if isNull {
				u.ClearInterval = true
				continue
			}
			u.ScanIntervalS = new(int)
			err = json.Unmarshal(raw, u.ScanIntervalS)
		case "discover_new_hosts":
			u.DiscoverNewHosts = new(bool)
			err = json.Unmarshal(raw, u.DiscoverNewHosts)
		case "offline_after_s":
			u.OfflineAfterS = new(int)
			err = json.Unmarshal(raw, u.OfflineAfterS)
		default:
			return nil, scanField(name, "unknown field")
		}
		if err != nil || (isNull && name != "collector_group_id" && name != "scan_interval_s") {
			return nil, scanField(name, "invalid value")
		}
	}
	return u, nil
}

// scanField is the 422 of a scan field.
func scanField(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// ScanStore reads and writes the scan configuration (PostgreSQL only).
type ScanStore interface {
	GetSubnetScan(ctx context.Context, orgID, subnetID string) (*SubnetScan, error)
	UpdateSubnetScan(ctx context.Context, orgID, subnetID string, u *ScanUpdate, probes ScanProbes) (*SubnetScan, error)
}

var _ ScanStore = (*PGRepository)(nil)

// scanRow is the stored configuration of a subnet with its collector
// group.
type scanRow struct {
	SubnetScan
	clientID    string
	cidr        netip.Prefix
	groupClient *string // the client of the requested group (nil: org-wide)
	groupFound  bool
	// ptrMode is the ptr_mode of the section, dnsServer the first effective
	// DNS server of the subnet (WP-1064).
	ptrMode   string
	dnsServer string
	// ptrProbes: the subnet has managed PTR probes (nothing to remove
	// without them).
	ptrProbes bool
}

// readScan reads the subnet (FOR UPDATE when lock) with the requested
// collector group (empty: the stored one) and its members and the target
// numbers, in one statement.
func readScan(ctx context.Context, tx pgx.Tx, orgID, subnetID, groupID string, lock bool) (*scanRow, error) {
	if !api.IsUUID(subnetID) {
		return nil, ErrNotFound
	}
	forUpdate := ""
	if lock {
		forUpdate = " FOR UPDATE OF s"
	}
	var row scanRow
	var group, client *string
	var cidr string
	var members, states []byte
	err := tx.QueryRow(ctx, `WITH cur AS (
			SELECT s.id, s.client_id, s.cidr, s.vrf_id, s.scan_enabled, s.collector_group_id, s.scan_interval_s, s.discover_new_hosts,
			       s.offline_after_s, s.scan_synced_at, s.scan_applied_at,
			       COALESCE((SELECT sec.ptr_mode FROM ipam_section sec
			                  WHERE sec.organization_id = s.organization_id AND sec.id = s.section_id), 'off') AS ptr_mode,
			       COALESCE((SELECT host(u.dns) FROM unnest(COALESCE(NULLIF(s.dns_servers, '{}'::inet[]),
			                   (SELECT ns.servers FROM nameserver_set ns
			                     WHERE ns.id = s.nameserver_set_id AND ns.organization_id = s.organization_id)))
			                   WITH ORDINALITY AS u(dns, n) ORDER BY u.n LIMIT 1), '') AS dns_server
			  FROM subnet s WHERE s.organization_id = $1 AND s.id = $2`+forUpdate+`
		), grp AS (
			SELECT g.id, g.client_id FROM collector_group g, cur
			 WHERE g.organization_id = $1 AND g.id = COALESCE($3::uuid, cur.collector_group_id)
		)
		SELECT cur.id::text, cur.client_id::text, cur.cidr::text, cur.scan_enabled, cur.collector_group_id::text, cur.scan_interval_s,
		       cur.discover_new_hosts, cur.offline_after_s, cur.scan_synced_at,
		       EXISTS (SELECT 1 FROM grp), (SELECT grp.client_id::text FROM grp),
		       COALESCE((SELECT json_agg(json_build_object('collector_id', m.collector_id, 'priority', m.priority,
		                 'member_status', m.member_status) ORDER BY m.priority, m.collector_id)
		                   FROM collector_group_member m, grp WHERE m.organization_id = $1 AND m.group_id = grp.id), '[]'),
		       (SELECT count(*) FROM subnet_scan_target t WHERE t.organization_id = $1 AND t.subnet_id = cur.id),
		       (SELECT count(*) FROM subnet_scan_target t WHERE t.organization_id = $1 AND t.subnet_id = cur.id AND t.owned),
		       cur.scan_applied_at,
		       COALESCE((SELECT json_build_object(
		                   'active', count(*) FILTER (WHERE x.state = 'active'),
		                   'offline', count(*) FILTER (WHERE x.state = 'offline'),
		                   'unknown', count(*) FILTER (WHERE x.state = 'unknown'),
		                   'none', count(*) FILTER (WHERE x.state IS NULL))
		                   FROM subnet_scan_target t
		                   LEFT JOIN LATERAL (SELECT a.state FROM ip_address a
		                                       WHERE a.organization_id = $1 AND a.vrf_id = cur.vrf_id AND a.address_host = host(t.address)
		                                       ORDER BY (a.subnet_id = cur.id) DESC NULLS LAST LIMIT 1) x ON true
		                  WHERE t.organization_id = $1 AND t.subnet_id = cur.id), '{}'),
		       (SELECT count(*) FROM ip_address a
		         WHERE a.organization_id = $1 AND a.subnet_id = cur.id AND a.origin = 'scan' AND a.type = 'discovered'),
		       cur.ptr_mode, cur.dns_server,
		       EXISTS (SELECT 1 FROM probe p WHERE p.organization_id = $1 AND p.managed_by = 'ipam_ptr'
		                AND starts_with(p.managed_key, 'ptr:' || cur.id::text || ':'))
		  FROM cur`, orgID, subnetID, nilIfEmpty(groupID)).Scan(&row.SubnetID, &client, &cidr, &row.ScanEnabled, &group,
		&row.ScanIntervalS, &row.DiscoverNewHosts, &row.OfflineAfterS, &row.ScanSyncedAt, &row.groupFound, &row.groupClient,
		&members, &row.Targets, &row.Owned, &row.LastAppliedAt, &states, &row.NewHosts, &row.ptrMode, &row.dnsServer, &row.ptrProbes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read subnet scan: %w", err)
	}
	if client != nil {
		row.clientID = *client
	}
	if group != nil {
		row.CollectorGroupID = *group
	}
	if row.cidr, err = netip.ParsePrefix(cidr); err != nil {
		return nil, fmt.Errorf("read subnet scan: %w", err)
	}
	if err := json.Unmarshal(members, &row.Members); err != nil {
		return nil, fmt.Errorf("read subnet scan members: %w", err)
	}
	if err := json.Unmarshal(states, &row.TargetStates); err != nil {
		return nil, fmt.Errorf("read subnet scan states: %w", err)
	}
	for _, at := range []**time.Time{&row.LastAppliedAt, &row.ScanSyncedAt} {
		if *at != nil {
			utc := (*at).UTC()
			*at = &utc
		}
	}
	row.Reused = row.Targets - row.Owned
	return &row, nil
}

// GetSubnetScan implements ScanStore.
func (r *PGRepository) GetSubnetScan(ctx context.Context, orgID, subnetID string) (*SubnetScan, error) {
	var out *SubnetScan
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := readScan(ctx, tx, orgID, subnetID, "", false)
		if err != nil {
			return err
		}
		out = &row.SubnetScan
		return nil
	})
	return out, err
}

// icmpLimits returns the interval limits and the default interval of the
// type icmp (api/probe-types.json, WP-934).
func icmpLimits() (minS, maxS, defS int) {
	t, ok := probewire.Lookup(scanProbeType)
	if !ok {
		return 10, 3600, 30
	}
	return int(t.MinIntervalS), int(t.MaxIntervalS), int(t.DefaultIntervalS)
}

// UpdateSubnetScan implements ScanStore (AK6): the fields are checked,
// stored and, while the scan is on, the targets synchronized in the same
// transaction (SyncScanTargets); switching off deletes every target.
func (r *PGRepository) UpdateSubnetScan(ctx context.Context, orgID, subnetID string, u *ScanUpdate, probes ScanProbes) (*SubnetScan, error) {
	if probes == nil {
		return nil, errors.New("ipam: the subnet scan needs the probe engine")
	}
	var out *SubnetScan
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := probes.LockProbeBudget(ctx, tx, orgID); err != nil {
			return err
		}
		group := ""
		if u.CollectorGroupID != nil {
			group = *u.CollectorGroupID
		}
		row, err := readScan(ctx, tx, orgID, subnetID, group, true)
		if err != nil {
			return err
		}
		next := row.SubnetScan
		if u.ScanEnabled != nil {
			next.ScanEnabled = *u.ScanEnabled
		}
		switch {
		case u.ClearGroup:
			next.CollectorGroupID = ""
		case u.CollectorGroupID != nil:
			next.CollectorGroupID = *u.CollectorGroupID
		}
		switch {
		case u.ClearInterval:
			next.ScanIntervalS = nil
		case u.ScanIntervalS != nil:
			next.ScanIntervalS = u.ScanIntervalS
		}
		if u.DiscoverNewHosts != nil {
			next.DiscoverNewHosts = *u.DiscoverNewHosts
		}
		if u.OfflineAfterS != nil {
			next.OfflineAfterS = *u.OfflineAfterS
		}
		if cerr := checkScan(row, &next, u); cerr != nil {
			return cerr
		}
		if _, err = tx.Exec(ctx, `UPDATE subnet SET scan_enabled = $3, collector_group_id = $4, scan_interval_s = $5,
				discover_new_hosts = $6, offline_after_s = $7,
				scan_synced_at = CASE WHEN $3 THEN now() ELSE scan_synced_at END
			WHERE organization_id = $1 AND id = $2`,
			orgID, subnetID, next.ScanEnabled, nilIfEmpty(next.CollectorGroupID), next.ScanIntervalS, next.DiscoverNewHosts,
			next.OfflineAfterS); err != nil {
			return scanWriteError(err)
		}
		row.SubnetScan = next
		if _, err = syncScanTargets(ctx, tx, probes, orgID, row); err != nil {
			return err
		}
		// The answer is the stored state: the targets, their address
		// states and scan_synced_at as written (WP-1063 AK7).
		stored, err := readScan(ctx, tx, orgID, subnetID, "", false)
		if err != nil {
			return err
		}
		out = &stored.SubnetScan
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkScan checks the next configuration (AK6): the collector group
// visible and org-wide or of the subnet's client, required while the scan
// is on; the interval within the limits of icmp; offline_after_s at least
// the effective interval and within 60 s to 30 days; discover_new_hosts
// only for IPv4 up to /22; a probe client (the subnet's, else the group's).
func checkScan(row *scanRow, next *SubnetScan, u *ScanUpdate) error {
	if next.CollectorGroupID != "" {
		if !row.groupFound {
			return scanField("collector_group_id", "collector group not found")
		}
		if row.groupClient != nil && row.clientID != "" && *row.groupClient != row.clientID {
			return scanField("collector_group_id", "the collector group belongs to another client than the subnet")
		}
	}
	minS, maxS, defS := icmpLimits()
	interval := defS
	if next.ScanIntervalS != nil {
		interval = *next.ScanIntervalS
		if interval < minS || interval > maxS {
			return scanField("scan_interval_s", fmt.Sprintf("scan_interval_s must be between %d and %d seconds", minS, maxS))
		}
	}
	if next.OfflineAfterS < 60 || next.OfflineAfterS > 2592000 {
		return scanField("offline_after_s", "offline_after_s must be between 60 and 2592000 seconds")
	}
	if next.OfflineAfterS < interval {
		return scanField("offline_after_s", "offline_after_s must not be below the scan interval")
	}
	if next.DiscoverNewHosts {
		if !row.cidr.Addr().Is4() {
			return scanField("discover_new_hosts", "discover_new_hosts applies to IPv4 subnets only")
		}
		if row.cidr.Bits() < MaxDiscoverPrefixIPv4 {
			return scanField("discover_new_hosts", fmt.Sprintf("discover_new_hosts takes subnets up to /%d", MaxDiscoverPrefixIPv4))
		}
	}
	if !next.ScanEnabled {
		return nil
	}
	if next.CollectorGroupID == "" {
		field := "collector_group_id"
		if u.ScanEnabled == nil {
			field = "scan_enabled"
		}
		return scanField(field, "a scanned subnet needs a collector group")
	}
	if row.clientID == "" && row.groupClient == nil {
		return scanField("collector_group_id", "the probes of the scan need a client: the subnet's or the collector group's")
	}
	return nil
}

// scanWriteError maps the constraints of the scan columns.
func scanWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "subnet_collector_group_fkey", "subnet_scan_collector_group_required":
		return scanField("collector_group_id", "collector group not found")
	case "subnet_offline_after_s_check":
		return scanField("offline_after_s", "offline_after_s must be between 60 and 2592000 seconds")
	case "subnet_scan_interval_s_check":
		return scanField("scan_interval_s", "invalid scan interval")
	}
	return err
}

// ScanKeyPrefix is the managed_key prefix of the probes of a subnet.
func ScanKeyPrefix(subnetID string) string { return "scan:" + subnetID + ":" }

// ScanKey is the managed_key of the probe of one target.
func ScanKey(subnetID, address string) string { return ScanKeyPrefix(subnetID) + address }

// discoverHosts returns the host addresses of an IPv4 prefix up to /22
// (without network and broadcast address; /31 and /32 all of them).
func discoverHosts(p netip.Prefix) []string {
	p = p.Masked()
	bits := p.Bits()
	first := p.Addr()
	n := 1 << (32 - bits)
	out := make([]string, 0, n)
	a := first
	for i := range n {
		if bits <= 30 && (i == 0 || i == n-1) {
			a = a.Next()
			continue
		}
		out = append(out, a.String())
		a = a.Next()
	}
	return out
}

// SetScanProbes attaches the managed probes of the subnet scan
// (probe.ScanProbes): SyncScanTargets, the delete of a subnet and a changed
// client or CIDR of a scanned subnet synchronize the scan with them.
func (r *PGRepository) SetScanProbes(p ScanProbes) { r.scanProbes = p }

// SyncScanTargets synchronizes the targets of the subnets in tx (AK5):
// each subnet with scan_enabled gets its targets and managed probes, the
// targets of a subnet without the scan are deleted; scan_synced_at of a
// scanned subnet is now(). It takes the probe budget lock of the
// organization before it locks the first subnet (WP-940 AK3, the order of
// UpdateSubnetScan); a caller that holds other row locks takes the budget
// lock (ScanProbes.LockProbeBudget) first.
func (r *PGRepository) SyncScanTargets(ctx context.Context, tx pgx.Tx, orgID string, subnetIDs []string) error {
	if r.scanProbes == nil {
		return errors.New("ipam: the subnet scan needs the probe engine")
	}
	if err := r.scanProbes.LockProbeBudget(ctx, tx, orgID); err != nil {
		return err
	}
	ids := slices.Compact(slices.Sorted(slices.Values(subnetIDs)))
	for _, id := range ids {
		row, err := readScan(ctx, tx, orgID, id, "", true)
		if err != nil {
			return err
		}
		if err := r.resyncScan(ctx, tx, orgID, row); err != nil {
			return err
		}
	}
	return nil
}

// resyncScan checks the stored configuration of a locked subnet against its
// current client and CIDR (checkScan) and synchronizes its targets; a
// scanned subnet is marked synced.
func (r *PGRepository) resyncScan(ctx context.Context, tx pgx.Tx, orgID string, row *scanRow) error {
	if row.ScanEnabled {
		if err := checkScan(row, &row.SubnetScan, &ScanUpdate{}); err != nil {
			return err
		}
	}
	if _, err := syncScanTargets(ctx, tx, r.scanProbes, orgID, row); err != nil {
		return err
	}
	if row.ScanEnabled {
		if _, err := tx.Exec(ctx, `UPDATE subnet SET scan_synced_at = now() WHERE organization_id = $1 AND id = $2`, orgID, row.SubnetID); err != nil {
			return fmt.Errorf("mark subnet scan synced: %w", err)
		}
	}
	return nil
}

// releaseScanOf switches the scan of a subnet about to be deleted off
// through the probe engine, so the collectors of its probes get a new
// config version and config.changed is written (the delete trigger of the
// targets alone would leave them checking deleted probes). The probe
// budget lock is taken by the caller first. Without the probe engine or
// for a subnet without the scan nothing happens.
func (r *PGRepository) releaseScanOf(ctx context.Context, tx pgx.Tx, orgID, subnetID string) error {
	if r.scanProbes == nil {
		return nil
	}
	row, err := readScan(ctx, tx, orgID, subnetID, "", true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || (!row.ScanEnabled && row.Targets == 0) {
		return err
	}
	row.ScanEnabled = false
	_, err = syncScanTargets(ctx, tx, r.scanProbes, orgID, row)
	return err
}

type targetCounts struct {
	targets, owned int
}

// syncScanTargets computes the targets of one subnet and writes probes and
// targets: one read of the known addresses with the reusable probes, the
// managed probes (SyncScanProbes), one write of the targets.
func syncScanTargets(ctx context.Context, tx pgx.Tx, probes ScanProbes, orgID string, row *scanRow) (targetCounts, error) {
	prefix := ScanKeyPrefix(row.SubnetID)
	if !row.ScanEnabled {
		// Off: the managed probes go (and with them their targets); the
		// reused targets are deleted (their probes stay).
		if _, err := probes.SyncScanProbes(ctx, tx, orgID, prefix, nil); err != nil {
			return targetCounts{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM subnet_scan_target WHERE organization_id = $1 AND subnet_id = $2`, orgID, row.SubnetID); err != nil {
			return targetCounts{}, fmt.Errorf("delete scan targets: %w", err)
		}
		// Without the scan the reverse check goes as well (WP-1064 AK2).
		return targetCounts{}, syncPTRProbes(ctx, tx, probes, orgID, row, nil, "")
	}
	// The client of the probes: the subnet's, else the group's. Only an
	// unmanaged probe of that client is reused: the probe of another client
	// on the same address (overlapping private networks, an org-wide
	// group) does not check this subnet.
	client := row.clientID
	if client == "" && row.groupClient != nil {
		client = *row.groupClient
	}
	var extra []string
	if row.DiscoverNewHosts && row.cidr.Addr().Is4() {
		extra = discoverHosts(row.cidr)
	}
	rows, err := tx.Query(ctx, `WITH cand AS (
			SELECT addr, bool_or(known) AS known FROM (
				SELECT host(a.address) AS addr, true AS known FROM ip_address a WHERE a.organization_id = $1 AND a.subnet_id = $2
				UNION ALL
				SELECT unnest($3::text[]), false) x
			 GROUP BY addr
		)
		SELECT c.addr, c.known, COALESCE((SELECT p.id::text FROM probe p
		         WHERE p.organization_id = $1 AND p.managed_by IS NULL AND p.type = $4 AND p.target = c.addr
		           AND p.collector_group_id = $5 AND p.client_id IS NOT DISTINCT FROM $6::uuid ORDER BY p.id LIMIT 1), '')
		  FROM cand c ORDER BY c.addr`, orgID, row.SubnetID, extra, scanProbeType, row.CollectorGroupID, nilIfEmpty(client))
	if err != nil {
		return targetCounts{}, fmt.Errorf("read scan targets: %w", err)
	}
	type target struct {
		addr, reuse string
		known       bool
	}
	var targets []target
	var known []string
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.addr, &t.known, &t.reuse); err != nil {
			rows.Close()
			return targetCounts{}, fmt.Errorf("read scan targets: %w", err)
		}
		targets = append(targets, t)
		if t.known {
			known = append(known, t.addr)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return targetCounts{}, fmt.Errorf("read scan targets: %w", err)
	}
	interval := 0
	if row.ScanIntervalS != nil {
		interval = *row.ScanIntervalS
	}
	specs := make([]ScanProbeSpec, 0, len(targets))
	for _, t := range targets {
		if t.reuse == "" {
			specs = append(specs, ScanProbeSpec{Key: ScanKey(row.SubnetID, t.addr), ClientID: client,
				CollectorGroupID: row.CollectorGroupID, Target: t.addr, IntervalS: interval})
		}
	}
	ids, err := probes.SyncScanProbes(ctx, tx, orgID, prefix, specs)
	if err != nil {
		return targetCounts{}, err
	}
	addrs, probeIDs, owned := make([]string, len(targets)), make([]string, len(targets)), make([]bool, len(targets))
	counts := targetCounts{targets: len(targets)}
	for i, t := range targets {
		addrs[i] = t.addr
		if t.reuse != "" {
			probeIDs[i] = t.reuse
			continue
		}
		id, ok := ids[ScanKey(row.SubnetID, t.addr)]
		if !ok {
			return targetCounts{}, fmt.Errorf("scan target %s: no managed probe", t.addr)
		}
		probeIDs[i], owned[i] = id, true
		counts.owned++
	}
	if _, err := tx.Exec(ctx, `WITH input AS (
			SELECT * FROM unnest($3::inet[], $4::uuid[], $5::boolean[]) AS i(address, probe_id, owned)
		), del AS (
			DELETE FROM subnet_scan_target t WHERE t.organization_id = $1 AND t.subnet_id = $2 AND NOT t.address = ANY ($3::inet[])
		)
		INSERT INTO subnet_scan_target (organization_id, subnet_id, address, probe_id, owned)
		SELECT $1, $2, i.address, i.probe_id, i.owned FROM input i
		ON CONFLICT (subnet_id, address) DO UPDATE SET probe_id = EXCLUDED.probe_id, owned = EXCLUDED.owned
		 WHERE (subnet_scan_target.probe_id, subnet_scan_target.owned) IS DISTINCT FROM (EXCLUDED.probe_id, EXCLUDED.owned)`,
		orgID, row.SubnetID, addrs, probeIDs, owned); err != nil {
		return targetCounts{}, fmt.Errorf("write scan targets: %w", err)
	}
	// The PTR probes of the known addresses (WP-1064 AK2), in the same call.
	return counts, syncPTRProbes(ctx, tx, probes, orgID, row, known, client)
}

// rescanSubnet checks and synchronizes the scan of a subnet whose client,
// CIDR, DNS servers, nameserver set or section changed (PATCH
// /subnets/{id}): the collector group must still be org-wide or of the new
// client (422 client_id), discover_new_hosts must still fit the new CIDR
// (422 cidr); the managed probes take the new client, the targets the new
// host addresses and the PTR probes the first effective DNS server and the
// section's ptr_mode (none without a server or with ptr_mode off), all in
// the same transaction.
func (r *PGRepository) rescanSubnet(ctx context.Context, tx pgx.Tx, orgID, subnetID string, req *UpdateSubnetRequest) error {
	row, err := readScan(ctx, tx, orgID, subnetID, "", true)
	if err != nil || (!row.ScanEnabled && row.Targets == 0 && !row.ptrProbes) {
		return err
	}
	err = r.resyncScan(ctx, tx, orgID, row)
	var fe *api.FieldError
	if errors.As(err, &fe) {
		switch {
		case fe.Field == "discover_new_hosts" && req.CIDR != nil:
			return scanField("cidr", fe.Message)
		case fe.Field == "collector_group_id" && req.ClientID != nil:
			return scanField("client_id", fe.Message)
		}
	}
	return err
}
