package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Monitoring events (DIS-06, WP-979): the SNMP traps a collector received
// in the monitoring mode, transmitted with its result batches (E-173) and
// stored by the ingest worker in monitoring_event in the transaction of
// the results. The assignment happens on the server:
//
//  1. the probes of the collector group of the sender with target =
//     source_ip; with several the icmp probe, otherwise the smallest id;
//     probe_id and ci_id follow from it;
//  2. without a probe the CI with this management address in the scope of
//     the collector, only for exactly one match (as CIAddressResolver);
//  3. otherwise ci_id stays NULL; the event is stored all the same.
//
// EventLister lists them for GET /api/v1/monitoring/events (E-160, the
// route itself is WP-1079); the domain events of WP-867 are not part of
// it. The retention keeps EventRetention (NFR-06, PurgeEvents).

// EventRetention is how long monitoring events are kept (WP-979 AK6).
const EventRetention = 30 * 24 * time.Hour

// EventPurgeBatch bounds the rows one purge statement deletes.
const EventPurgeBatch = 10_000

// MonitoringEvent is a stored monitoring event.
type MonitoringEvent struct {
	ID          string            `json:"id"`
	CollectorID string            `json:"collector_id"`
	ClientID    string            `json:"client_id,omitempty"`
	SiteID      string            `json:"site_id,omitempty"`
	CIID        string            `json:"ci_id,omitempty"`
	ProbeID     string            `json:"probe_id,omitempty"`
	SourceIP    string            `json:"source_ip"`
	Kind        string            `json:"kind"`
	TrapOID     string            `json:"trap_oid,omitempty"`
	Varbinds    map[string]string `json:"varbinds"`
	Truncated   bool              `json:"truncated"`
	ObservedAt  time.Time         `json:"observed_at"`
	ReceivedAt  time.Time         `json:"received_at"`
}

// EventFilter selects monitoring events; empty fields select all, To is
// exclusive.
type EventFilter struct {
	CIID, ProbeID, Kind string
	From, To            time.Time
}

// EventLister lists monitoring events in the scope of the context, newest
// observed_at first (then id), keyset cursor or offset, at most one more
// than the limit, and their number (WP-979 AK7).
type EventLister interface {
	ListEvents(ctx context.Context, f *EventFilter, page api.PaginationParams) ([]MonitoringEvent, int, error)
}

// The keyset order of the events.
const (
	eventSort = "observed_at"
	eventDir  = "desc"
)

// eventPosition reads the (observed_at, id) position of a cursor.
// validation: fixed texts wrapping ErrInvalidCursor (API-03).
func eventPosition(c *api.Cursor) (time.Time, string, error) {
	if !c.Matches(eventSort, eventDir) {
		return time.Time{}, "", fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
	}
	at, err := time.Parse(time.RFC3339Nano, c.Value)
	if err != nil || !api.IsUUID(c.ID) {
		return time.Time{}, "", fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
	}
	return at, c.ID, nil
}

// EventCursor is the cursor after an event.
func EventCursor(e *MonitoringEvent) string {
	return api.EncodeCursor(api.Cursor{Sort: eventSort, Dir: eventDir, Value: e.ObservedAt.UTC().Format(time.RFC3339Nano), ID: e.ID})
}

// ListEvents implements EventLister.
func (r *PGRepository) ListEvents(ctx context.Context, f *EventFilter, page api.PaginationParams) ([]MonitoringEvent, int, error) {
	out := make([]MonitoringEvent, 0)
	total := 0
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		base := ` FROM monitoring_event WHERE organization_id = $1`
		args := []any{orgID}
		add := func(clause string, v any) {
			args = append(args, v)
			base += fmt.Sprintf(" AND "+clause, len(args))
		}
		if f.CIID != "" {
			add("ci_id = $%d::uuid", f.CIID)
		}
		if f.ProbeID != "" {
			add("probe_id = $%d::uuid", f.ProbeID)
		}
		if f.Kind != "" {
			add("kind = $%d", f.Kind)
		}
		if !f.From.IsZero() {
			add("observed_at >= $%d", f.From)
		}
		if !f.To.IsZero() {
			add("observed_at < $%d", f.To)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)`+base, args...).Scan(&total); err != nil {
			return fmt.Errorf("count monitoring events: %w", err)
		}
		where := ""
		if page.Cursor != nil {
			at, id, err := eventPosition(page.Cursor)
			if err != nil {
				return err
			}
			where = " AND " + api.KeysetClause(eventSort, "timestamptz", eventDir, len(args)+1)
			args = append(args, at.UTC().Format(time.RFC3339Nano), id)
		}
		tail := fmt.Sprintf(" ORDER BY observed_at DESC, id DESC LIMIT $%d", len(args)+1)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", len(args)+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, `SELECT id::text, collector_id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''),
			COALESCE(ci_id::text, ''), COALESCE(probe_id::text, ''), host(source_ip), kind, trap_oid, varbinds, truncated,
			observed_at, received_at`+base+where+tail, args...)
		if err != nil {
			return fmt.Errorf("monitoring events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e MonitoringEvent
			var raw []byte
			if err := rows.Scan(&e.ID, &e.CollectorID, &e.ClientID, &e.SiteID, &e.CIID, &e.ProbeID, &e.SourceIP, &e.Kind,
				&e.TrapOID, &raw, &e.Truncated, &e.ObservedAt, &e.ReceivedAt); err != nil {
				return fmt.Errorf("monitoring events: %w", err)
			}
			if err := json.Unmarshal(raw, &e.Varbinds); err != nil {
				return fmt.Errorf("monitoring event varbinds: %w", err)
			}
			e.ObservedAt, e.ReceivedAt = e.ObservedAt.UTC(), e.ReceivedAt.UTC()
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// memEvent is a monitoring event of a MemoryRepository.
type memEvent struct {
	orgID string
	ev    MonitoringEvent
}

// AddEvents stores monitoring events of an organization (tests and
// --no-db; the ingest worker writes only to PostgreSQL). An event whose id
// exists already is ignored, as ON CONFLICT DO NOTHING.
func (m *MemoryRepository) AddEvents(orgID string, events ...MonitoringEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range events {
		if slices.ContainsFunc(m.events, func(x memEvent) bool { return x.ev.ID == events[i].ID }) {
			continue
		}
		m.events = append(m.events, memEvent{orgID: orgID, ev: events[i]})
	}
}

// ListEvents implements EventLister with the order and keyset of
// PGRepository.
func (m *MemoryRepository) ListEvents(ctx context.Context, f *EventFilter, page api.PaginationParams) ([]MonitoringEvent, int, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	var matching []MonitoringEvent
	for i := range m.events {
		e := m.events[i].ev
		switch {
		case m.events[i].orgID != s.orgID, !s.reads(e.ClientID, e.SiteID),
			f.CIID != "" && e.CIID != f.CIID, f.ProbeID != "" && e.ProbeID != f.ProbeID, f.Kind != "" && e.Kind != f.Kind,
			!f.From.IsZero() && e.ObservedAt.Before(f.From), !f.To.IsZero() && !e.ObservedAt.Before(f.To):
			continue
		}
		matching = append(matching, e)
	}
	m.mu.Unlock()
	sort.SliceStable(matching, func(i, j int) bool {
		a, b := matching[i], matching[j]
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.After(b.ObservedAt)
		}
		return a.ID > b.ID
	})
	start := min(page.Offset, len(matching))
	if page.Cursor != nil {
		at, id, perr := eventPosition(page.Cursor)
		if perr != nil {
			return nil, 0, perr
		}
		start = len(matching)
		for i := range matching {
			e := &matching[i]
			if e.ObservedAt.Before(at) || (e.ObservedAt.Equal(at) && e.ID < id) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, len(matching))
	return slices.Clone(matching[start:end]), len(matching), nil
}

// eventRow is an event ready for the insert with its assignment.
type eventRow struct {
	ev          probewire.Event
	collectorID string
	probeID     string
	ciID        string
}

// writeEvents stores the events of the messages in the transaction of the
// results (WP-979 AK3): events of an unknown collector are dropped, the
// rest is assigned and inserted with ON CONFLICT (id) DO NOTHING. It
// returns the number of rows written.
func writeEvents(ctx context.Context, tx pgx.Tx, orgID string, msgs []ResultsMessage, collectors map[string]ingestCollector) (int, error) {
	var rows []eventRow
	for _, m := range msgs {
		if _, known := collectors[m.CollectorID]; !known {
			continue
		}
		for i := range m.Events {
			if m.Events[i].Validate() != nil {
				ingestDiscarded.WithLabelValues(DiscardInvalidEvent).Inc()
				continue
			}
			rows = append(rows, eventRow{ev: m.Events[i], collectorID: m.CollectorID})
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := assignEvents(ctx, tx, orgID, rows); err != nil {
		return 0, err
	}
	// A row the database refuses must not cost the results of the batch
	// (the transaction carries every collector of the organization): the
	// events are inserted under a savepoint, after a failure one by one,
	// and a refused event is dropped and counted.
	n, err := insertEventsSavepoint(ctx, tx, orgID, rows)
	if err == nil {
		return n, nil
	}
	if ctx.Err() != nil {
		return 0, err
	}
	n, dropped := 0, 0
	for i := range rows {
		k, rerr := insertEventsSavepoint(ctx, tx, orgID, rows[i:i+1])
		if rerr != nil {
			if ctx.Err() != nil {
				return 0, rerr
			}
			dropped++
			continue
		}
		n += k
	}
	ingestDiscarded.WithLabelValues(DiscardInvalidEvent).Add(float64(dropped))
	slog.Warn("monitoring events refused by the database and dropped", "organization_id", orgID, "dropped", dropped)
	return n, nil
}

// insertEventsSavepoint inserts rows under a savepoint of tx; on an error
// the savepoint is rolled back and the transaction stays usable.
func insertEventsSavepoint(ctx context.Context, tx pgx.Tx, orgID string, rows []eventRow) (int, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, err
	}
	n, err := insertEvents(ctx, sp, orgID, rows)
	if err != nil {
		_ = sp.Rollback(ctx)
		return 0, err
	}
	if err := sp.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// insertEvents writes rows with ON CONFLICT (id) DO NOTHING.
func insertEvents(ctx context.Context, tx pgx.Tx, orgID string, rows []eventRow) (int, error) {
	ids := make([]string, len(rows))
	collectorIDs := make([]string, len(rows))
	ciIDs := make([]*string, len(rows))
	probeIDs := make([]*string, len(rows))
	sources := make([]string, len(rows))
	kinds := make([]string, len(rows))
	oids := make([]string, len(rows))
	varbinds := make([]string, len(rows))
	truncated := make([]bool, len(rows))
	observed := make([]time.Time, len(rows))
	for i := range rows {
		r := &rows[i]
		ids[i], collectorIDs[i], sources[i], kinds[i], oids[i] = strings.ToLower(r.ev.ID), r.collectorID, r.ev.SourceIP, r.ev.Kind, r.ev.TrapOID
		if r.ciID != "" {
			ciIDs[i] = &r.ciID
		}
		if r.probeID != "" {
			probeIDs[i] = &r.probeID
		}
		vb := r.ev.Varbinds
		if vb == nil {
			vb = map[string]string{}
		}
		b, err := json.Marshal(vb)
		if err != nil {
			return 0, fmt.Errorf("encode event varbinds: %w", err)
		}
		varbinds[i], truncated[i], observed[i] = string(b), r.ev.Truncated, r.ev.ObservedAt.UTC()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO monitoring_event (id, organization_id, collector_id, ci_id, probe_id, source_ip, kind,
			trap_oid, varbinds, truncated, observed_at)
		SELECT u.id, $1, u.collector_id, u.ci_id, u.probe_id, u.source_ip, u.kind, u.trap_oid, u.varbinds, u.truncated, u.observed_at
		  FROM unnest($2::uuid[], $3::uuid[], $4::uuid[], $5::uuid[], $6::inet[], $7::text[], $8::text[], $9::jsonb[],
		              $10::boolean[], $11::timestamptz[])
		       AS u (id, collector_id, ci_id, probe_id, source_ip, kind, trap_oid, varbinds, truncated, observed_at)
		ON CONFLICT (id) DO NOTHING`,
		orgID, ids, collectorIDs, ciIDs, probeIDs, sources, kinds, oids, varbinds, truncated, observed)
	if err != nil {
		return 0, fmt.Errorf("insert monitoring events: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// assignEvents finds probe and CI of every event (the rules in the
// package comment of this file).
func assignEvents(ctx context.Context, tx pgx.Tx, orgID string, rows []eventRow) error {
	type key struct{ collector, source string }
	probes := map[key][2]string{}
	cis := map[key]string{}
	for i := range rows {
		k := key{rows[i].collectorID, rows[i].ev.SourceIP}
		if _, done := probes[k]; done {
			continue
		}
		addr, err := netip.ParseAddr(k.source)
		if err != nil {
			probes[k] = [2]string{}
			continue
		}
		var probeID, ciID string
		err = tx.QueryRow(ctx, `SELECT p.id::text, COALESCE(p.ci_id::text, '')
			  FROM probe p
			  JOIN collector_group_member m ON m.group_id = p.collector_group_id AND m.organization_id = p.organization_id
			 WHERE p.organization_id = $1 AND m.collector_id = $2::uuid
			   AND (p.target = $3 OR p.target = $4)
			 ORDER BY (p.type = 'icmp') DESC, p.id
			 LIMIT 1`, orgID, k.collector, addr.String(), addr.Unmap().String()).Scan(&probeID, &ciID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("assign monitoring event to a probe: %w", err)
		}
		probes[k] = [2]string{probeID, ciID}
		if probeID != "" {
			continue
		}
		// Without a probe: the CI with this management address in the
		// scope of the collector, only for exactly one match.
		var matches []string
		r, err := tx.Query(ctx, `SELECT ci.id::text
			  FROM ci JOIN collector c ON c.id = $2::uuid AND c.organization_id = ci.organization_id
			 WHERE ci.organization_id = $1 AND ci.deleted_at IS NULL AND host(ci.management_ip) = $3
			   AND (c.client_id IS NULL OR ci.client_id = c.client_id)
			   AND (c.site_id IS NULL OR ci.site_id = c.site_id)
			 LIMIT 2`, orgID, k.collector, addr.Unmap().String())
		if err != nil {
			return fmt.Errorf("assign monitoring event to a CI: %w", err)
		}
		for r.Next() {
			var id string
			if err := r.Scan(&id); err != nil {
				r.Close()
				return fmt.Errorf("assign monitoring event to a CI: %w", err)
			}
			matches = append(matches, id)
		}
		r.Close()
		if err := r.Err(); err != nil {
			return fmt.Errorf("assign monitoring event to a CI: %w", err)
		}
		if len(matches) == 1 {
			cis[k] = matches[0]
		}
	}
	for i := range rows {
		k := key{rows[i].collectorID, rows[i].ev.SourceIP}
		p := probes[k]
		rows[i].probeID, rows[i].ciID = p[0], p[1]
		if p[0] == "" {
			rows[i].ciID = cis[k]
		}
	}
	return nil
}

// PurgeEvents deletes the monitoring events of an organization observed
// before cutoff, in portions of at most EventPurgeBatch rows, and returns
// the number deleted (WP-979 AK6, NFR-06). The retention job of WP-974
// calls it per organization with now − EventRetention.
func PurgeEvents(ctx context.Context, pool *pgxpool.Pool, orgID string, cutoff time.Time) (int64, error) {
	var total int64
	scope := database.OrgWideScope(orgID, "")
	for {
		var n int64
		err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM monitoring_event WHERE id IN (
				SELECT id FROM monitoring_event WHERE organization_id = $1 AND observed_at < $2 LIMIT $3)`,
				orgID, cutoff, EventPurgeBatch)
			n = tag.RowsAffected()
			return err
		})
		if err != nil {
			return total, fmt.Errorf("purge monitoring events: %w", err)
		}
		total += n
		if n < EventPurgeBatch {
			return total, nil
		}
	}
}
