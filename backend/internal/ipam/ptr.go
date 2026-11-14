package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe/resultsink"
)

// The DNS reverse check (IPM-12, WP-1064): per known address of a scanned
// subnet in a section with ptr_mode report or adopt and with an effective
// DNS server, a managed dns probe (managed_by ipam_ptr, key
// ptr:<subnet>:<address>) asks the first server for the PTR record of the
// address. The collector reports the first name as the value probe_dns_ptr
// with the label name (collector/probe/dns.go); the ingest hands the
// written results to the sink of this file (resultsink, registered in
// init), which sets ptr_name, ptr_checked_at and ptr_status of the address
// set-based:
//
//	name equal to dns_name (lowercase, without the final dot) → match
//	name and dns_name empty or different                      → mismatch
//	the server answered without a name (NXDOMAIN, empty)      → missing
//	no answer (timeout, refused, unreachable)                 → error, ptr_name kept
//
// With ptr_mode adopt a mismatch of an address that is not pinned takes
// the name as dns_name and becomes match. GET /api/v1/ipam/ptr-mismatches
// lists the mismatches (and missing names).

// Section modes of the reverse check.
const (
	PTRModeOff    = "off"
	PTRModeReport = "report"
	PTRModeAdopt  = "adopt"
)

// ValidPTRMode reports whether mode is a ptr_mode of a section.
func ValidPTRMode(mode string) bool {
	return mode == PTRModeOff || mode == PTRModeReport || mode == PTRModeAdopt
}

// PTR states of an address.
const (
	PTRStatusMatch    = "match"
	PTRStatusMismatch = "mismatch"
	PTRStatusMissing  = "missing"
	PTRStatusError    = "error"
)

// Value and label of a dns result that carry the PTR name (collector
// ValueDNSPTR, LabelDNSName) and the value of the response code: a result
// with it got an answer from the server.
const (
	ptrValue      = "probe_dns_ptr"
	ptrLabel      = "name"
	dnsRcodeValue = "probe_dns_rcode"
)

// ptrManagedBy is probe.ManagedByPTR (this package cannot import probe).
const ptrManagedBy = "ipam_ptr"

// ptrMinIntervalS is the shortest interval of a PTR probe.
const ptrMinIntervalS = 3600

// PTRProbeSpec is the managed dns probe of one address.
type PTRProbeSpec struct {
	Key              string
	ClientID         string
	CollectorGroupID string
	// Target is the DNS server, QName the reverse name of the address.
	Target    string
	QName     string
	IntervalS int
}

// PTRKeyPrefix is the managed_key prefix of the PTR probes of a subnet.
func PTRKeyPrefix(subnetID string) string { return "ptr:" + subnetID + ":" }

// PTRKey is the managed_key of the PTR probe of one address.
func PTRKey(subnetID, address string) string { return PTRKeyPrefix(subnetID) + address }

// ReverseName is the name of the PTR record of an address: in-addr.arpa
// for IPv4, ip6.arpa (nibbles) for IPv6.
func ReverseName(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() {
		b := a.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", b[3], b[2], b[1], b[0])
	}
	b := a.As16()
	var sb strings.Builder
	for i := len(b) - 1; i >= 0; i-- {
		sb.WriteString(strconv.FormatUint(uint64(b[i]&0x0f), 16))
		sb.WriteByte('.')
		sb.WriteString(strconv.FormatUint(uint64(b[i]>>4), 16))
		sb.WriteByte('.')
	}
	sb.WriteString("ip6.arpa")
	return sb.String()
}

// syncPTRProbes makes the PTR probes of a subnet those of its known
// addresses (AK2): none while the scan is off, the section's ptr_mode is
// off or the subnet has no effective DNS server.
func syncPTRProbes(ctx context.Context, tx pgx.Tx, probes ScanProbes, orgID string, row *scanRow, known []string, client string) error {
	prefix := PTRKeyPrefix(row.SubnetID)
	if !row.ScanEnabled || row.ptrMode == PTRModeOff || row.ptrMode == "" || row.dnsServer == "" {
		if !row.ptrProbes {
			return nil // nothing to remove: no statement
		}
		return probes.SyncPTRProbes(ctx, tx, orgID, prefix, nil)
	}
	_, _, defS := icmpLimits()
	interval := defS
	if row.ScanIntervalS != nil {
		interval = *row.ScanIntervalS
	}
	interval = max(ptrMinIntervalS, interval)
	specs := make([]PTRProbeSpec, 0, len(known))
	for _, addr := range known {
		a, err := netip.ParseAddr(addr)
		if err != nil {
			continue
		}
		specs = append(specs, PTRProbeSpec{Key: PTRKey(row.SubnetID, addr), ClientID: client,
			CollectorGroupID: row.CollectorGroupID, Target: row.dnsServer, QName: ReverseName(a), IntervalS: interval})
	}
	return probes.SyncPTRProbes(ctx, tx, orgID, prefix, specs)
}

func init() { resultsink.Register("dns", resultsink.SinkFunc(ApplyPTRResults)) }

// The DNS response codes that mean "no PTR name" (AK5): NOERROR without a
// name and NXDOMAIN. Every other code (SERVFAIL, REFUSED, …) is an error of
// the check that keeps ptr_name.
const (
	dnsRcodeNoError  = 0
	dnsRcodeNXDomain = 3
)

// ptrResultKind classifies a result: name (with the name), missing
// (NOERROR without a name, NXDOMAIN) or error (no answer, timeout, every
// other response code such as REFUSED or SERVFAIL).
func ptrResultKind(r *resultsink.Result) (kind, name string) {
	if v, ok := r.Values[ptrValue]; ok && v == 1 {
		if n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Labels[ptrLabel])), "."); n != "" {
			return "name", n
		}
	}
	if rcode, answered := r.Values[dnsRcodeValue]; answered && (rcode == dnsRcodeNoError || rcode == dnsRcodeNXDomain) {
		return PTRStatusMissing, ""
	}
	return PTRStatusError, ""
}

// ApplyPTRResults is the result sink of the type dns (AK4, AK5): the
// results of the probes managed by ipam_ptr set ptr_name, ptr_checked_at
// and ptr_status of their address, with ptr_mode adopt also dns_name, in
// three statements whatever their number: the change source scan with the
// widened scope (the ingest has no user; sections with rights), the update
// of the changed rows and the restore of both. A result older than the last
// check of its address changes nothing.
func ApplyPTRResults(ctx context.Context, tx pgx.Tx, orgID string, results []resultsink.Result) error {
	var subnets, addrs, names, kinds []string
	var at []time.Time
	for i := range results {
		r := &results[i]
		if r.ManagedBy != ptrManagedBy {
			continue
		}
		rest, ok := strings.CutPrefix(r.ManagedKey, "ptr:")
		if !ok {
			continue
		}
		subnet, addr, ok := strings.Cut(rest, ":")
		if !ok || !api.IsUUID(subnet) {
			continue
		}
		kind, name := ptrResultKind(r)
		subnets, addrs, names, kinds = append(subnets, subnet), append(addrs, addr), append(names, name), append(kinds, kind)
		at = append(at, r.ObservedAt.UTC())
	}
	if len(subnets) == 0 {
		return nil
	}
	var source, scope string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(current_setting('app.ipam_change_source', true), ''),
			set_config('app.ipam_change_source', 'scan', true) IS NULL,
			app_scope_widen()::text`).Scan(&source, new(bool), &scope); err != nil {
		return fmt.Errorf("ptr results: set the change source: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH input AS (
			SELECT DISTINCT ON (i.subnet_id, i.addr) i.*
			  FROM unnest($2::uuid[], $3::text[], $4::timestamptz[], $5::text[], $6::text[])
			    AS i(subnet_id, addr, observed_at, name, kind)
			 ORDER BY i.subnet_id, i.addr, i.observed_at DESC
		), calc AS (
			SELECT a.id, i.observed_at,
			       (i.kind = 'name' AND sec.ptr_mode = 'adopt' AND NOT a.pinned
			        AND lower(rtrim(COALESCE(a.dns_name, ''), '.')) IS DISTINCT FROM i.name) AS adopt,
			       CASE i.kind WHEN 'error' THEN a.ptr_name WHEN 'missing' THEN NULL ELSE i.name END AS ptr_name,
			       CASE WHEN i.kind <> 'name' THEN i.kind
			            WHEN lower(rtrim(COALESCE(a.dns_name, ''), '.')) = i.name THEN 'match'
			            WHEN sec.ptr_mode = 'adopt' AND NOT a.pinned THEN 'match'
			            ELSE 'mismatch' END AS ptr_status,
			       i.name
			  FROM input i
			  JOIN subnet s ON s.organization_id = $1 AND s.id = i.subnet_id
			  JOIN ipam_section sec ON sec.organization_id = $1 AND sec.id = s.section_id AND sec.ptr_mode <> 'off'
			  JOIN ip_address a ON a.organization_id = $1 AND a.vrf_id = s.vrf_id AND a.address_host = i.addr
		)
		UPDATE ip_address a
		   SET ptr_checked_at = c.observed_at, ptr_name = c.ptr_name, ptr_status = c.ptr_status,
		       dns_name = CASE WHEN c.adopt THEN c.name ELSE a.dns_name END
		  FROM calc c
		 WHERE a.organization_id = $1 AND a.id = c.id
		   AND (a.ptr_checked_at IS NULL OR a.ptr_checked_at <= c.observed_at)
		   AND (a.ptr_checked_at, a.ptr_name, a.ptr_status, a.dns_name)
		       IS DISTINCT FROM (c.observed_at, c.ptr_name, c.ptr_status, CASE WHEN c.adopt THEN c.name ELSE a.dns_name END)`,
		orgID, subnets, addrs, at, names, kinds); err != nil {
		return fmt.Errorf("ptr results: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.ipam_change_source', $1, true), app_scope_restore($2::text[])`,
		source, scope); err != nil {
		return fmt.Errorf("ptr results: restore the change source: %w", err)
	}
	return nil
}

// PTRMismatch is a row of GET /api/v1/ipam/ptr-mismatches (AK7).
type PTRMismatch struct {
	ID           string     `json:"id"`
	Address      string     `json:"address"`
	SubnetID     string     `json:"subnet_id,omitempty"`
	SubnetCIDR   string     `json:"subnet_cidr,omitempty"`
	DNSName      string     `json:"dns_name,omitempty"`
	PTRName      string     `json:"ptr_name,omitempty"`
	PTRStatus    string     `json:"ptr_status"`
	PTRCheckedAt *time.Time `json:"ptr_checked_at,omitempty"`
	Pinned       bool       `json:"pinned"`
}

// PTRFilter are the filters of the list: section, subnet and status
// (mismatch or missing).
type PTRFilter struct {
	SectionID string
	SubnetID  string
	Status    string
}

// PTRStore lists the PTR deviations (PostgreSQL only).
type PTRStore interface {
	ListPTRMismatches(ctx context.Context, orgID string, f PTRFilter, page api.PaginationParams) ([]PTRMismatch, int, error)
}

var _ PTRStore = (*PGRepository)(nil)

// ListPTRMismatches implements PTRStore: the addresses of the status the
// caller sees (client and site scope and the section rights through row
// level security), ordered by address, through the partial index
// idx_ip_address_ptr_deviation.
func (r *PGRepository) ListPTRMismatches(ctx context.Context, orgID string, f PTRFilter, page api.PaginationParams) ([]PTRMismatch, int, error) {
	var out []PTRMismatch
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id::text, host(a.address), COALESCE(a.subnet_id::text, ''),
				COALESCE((SELECT s.cidr::text FROM subnet s WHERE s.id = a.subnet_id AND s.organization_id = a.organization_id), ''),
				COALESCE(a.dns_name, ''), COALESCE(a.ptr_name, ''), a.ptr_status, a.ptr_checked_at, a.pinned,
				count(*) OVER ()
			  FROM ip_address a
			 WHERE a.organization_id = $1 AND a.ptr_status = $2 AND a.ptr_status IN ('mismatch', 'missing')
			   AND ($3::uuid IS NULL OR a.subnet_id = $3::uuid)
			   AND ($4::uuid IS NULL OR EXISTS (SELECT 1 FROM subnet s WHERE s.organization_id = $1 AND s.id = a.subnet_id
			                                     AND s.section_id = $4::uuid))
			 ORDER BY a.address, a.id
			 LIMIT $5 OFFSET $6`, orgID, f.Status, nilIfEmpty(f.SubnetID), nilIfEmpty(f.SectionID), page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list ptr mismatches: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m PTRMismatch
			if err := rows.Scan(&m.ID, &m.Address, &m.SubnetID, &m.SubnetCIDR, &m.DNSName, &m.PTRName, &m.PTRStatus,
				&m.PTRCheckedAt, &m.Pinned, &total); err != nil {
				return fmt.Errorf("list ptr mismatches: %w", err)
			}
			if m.PTRCheckedAt != nil {
				checked := m.PTRCheckedAt.UTC()
				m.PTRCheckedAt = &checked
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	if out == nil {
		out = []PTRMismatch{}
	}
	return out, total, nil
}

// registerPTRRoutes registers the routes of ptr.go.
func (h *Handler) registerPTRRoutes(r chi.Router) {
	r.Get("/api/v1/ipam/ptr-mismatches", h.ListPTRMismatches)
}

// ListPTRMismatches handles GET /api/v1/ipam/ptr-mismatches (AK7); 501
// without PostgreSQL.
func (h *Handler) ListPTRMismatches(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	store, ok := h.repo.(PTRStore)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "the DNS reverse check needs PostgreSQL")
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := PTRFilter{SectionID: q.Get("section_id"), SubnetID: q.Get("subnet_id"), Status: q.Get("status")}
	if f.Status == "" {
		f.Status = PTRStatusMismatch
	}
	if f.Status != PTRStatusMismatch && f.Status != PTRStatusMissing {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "status must be mismatch or missing")
		return
	}
	for name, v := range map[string]string{"section_id": f.SectionID, "subnet_id": f.SubnetID} {
		if v != "" && !api.IsUUID(v) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", name+" must be a UUID")
			return
		}
	}
	items, total, err := store.ListPTRMismatches(r.Context(), orgID, f, page)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[PTRMismatch]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(items) < total,
	})
}
