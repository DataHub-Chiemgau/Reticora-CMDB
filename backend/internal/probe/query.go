package probe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The query API of the probe data (PRB-06, MON-03, WP-975, E-172, E-160):
//
//	GET /api/v1/monitoring/probes/{id}/results        one series with its tier (raw, 5m, 1h, 1d)
//	GET /api/v1/monitoring/probes/{id}/state          the current state (probe_state)
//	GET /api/v1/monitoring/probes/{id}/state-changes  the state changes, newest first, cursor-paginated
//
// Every route first loads the probe through the repository under row level
// security: a probe of another organization, client or site is 404 before
// any series is read (the views of the results only filter rows). Results
// come only from probe_result_v and the views of the aggregates
// probe_result_5m_v, _1h_v and _1d_v; state and changes from probe_state and
// probe_state_change with RLS.

// Tier is a storage tier of the probe results.
type Tier string

// The tiers from the finest (E-172, PRB-06).
const (
	TierRaw Tier = "raw"
	Tier5m  Tier = "5m"
	Tier1h  Tier = "1h"
	Tier1d  Tier = "1d"
)

const (
	// MaxResultPoints is the most points of one series.
	MaxResultPoints = 2000
	// MaxResultSpan is the longest span of one query (the retention of the
	// longest tier).
	MaxResultSpan = 400 * 24 * time.Hour
	// DefaultResultSpan is the span without from.
	DefaultResultSpan = 24 * time.Hour
	day               = 24 * time.Hour
)

// tierSpec is a tier with its width and retention (the views of WP-945 and
// WP-972).
type tierSpec struct {
	tier      Tier
	width     time.Duration // 0 for raw: the effective interval of the probe
	retention time.Duration
	view      string
}

var tierSpecs = []tierSpec{
	{TierRaw, 0, 14 * day, "probe_result_v"},
	{Tier5m, 5 * time.Minute, 90 * day, "probe_result_5m_v"},
	{Tier1h, time.Hour, 400 * day, "probe_result_1h_v"},
	{Tier1d, day, 400 * day, "probe_result_1d_v"},
}

func specOf(t Tier) (tierSpec, bool) {
	for _, s := range tierSpecs {
		if s.tier == t {
			return s, true
		}
	}
	return tierSpec{}, false
}

// pointCount is the number of points of a tier of width over span.
func pointCount(span, width time.Duration) int64 {
	if width <= 0 {
		return -1
	}
	return int64((span + width - 1) / width)
}

// tierFits reports whether the tier holds from (retention) and gives at
// most MaxResultPoints points over to − from; an aggregate tier counts from
// the bucket that holds from, like ProbeResults reads it.
func tierFits(s *tierSpec, from, to, now time.Time, interval time.Duration) bool {
	width, lower := s.width, from
	if s.tier == TierRaw {
		width = interval
	} else {
		lower = from.Truncate(width)
	}
	n := pointCount(to.Sub(lower), width)
	return n >= 0 && n <= MaxResultPoints && !from.Before(now.Add(-s.retention))
}

// SelectTier picks the tier of a query (AK1): with step the forced tier,
// else the finest tier of raw, 5m, 1h and 1d whose retention covers from and
// that gives at most MaxResultPoints points (raw at the effective interval of
// the probe). There are no fixed time limits. A span above MaxResultSpan, a
// from before every retention, an unknown step and a step outside its
// retention or above the points are 422 with the field.
func SelectTier(from, to, now time.Time, interval time.Duration, step string) (Tier, error) {
	if !to.After(from) {
		return "", unprocessable("to", "to must be after from")
	}
	if to.Sub(from) > MaxResultSpan {
		return "", unprocessable("from", "from and to are at most 400 days apart")
	}
	if step != "" {
		s, ok := specOf(Tier(step))
		if !ok {
			return "", unprocessable("step", "step must be raw, 5m, 1h or 1d")
		}
		if !tierFits(&s, from, to, now, interval) {
			return "", unprocessable("step", fmt.Sprintf("step %s does not cover from or gives more than %d points", step, MaxResultPoints))
		}
		return s.tier, nil
	}
	for i := range tierSpecs {
		if tierFits(&tierSpecs[i], from, to, now, interval) {
			return tierSpecs[i].tier, nil
		}
	}
	return "", unprocessable("from", "from lies before the retention of every tier (400 days)")
}

// ResultPoint is one point of a series (AK2); a raw point has total 1.
type ResultPoint struct {
	T        time.Time `json:"t"`
	Total    int64     `json:"total"`
	OK       int64     `json:"ok"`
	Fail     int64     `json:"fail"`
	Error    int64     `json:"error"`
	RTTAvg   *float64  `json:"rtt_avg"`
	RTTMin   *float64  `json:"rtt_min"`
	RTTMax   *float64  `json:"rtt_max"`
	LossAvg  *float64  `json:"loss_avg"`
	ValueAvg *float64  `json:"value_avg"`
	ValueMin *float64  `json:"value_min"`
	ValueMax *float64  `json:"value_max"`
}

// ResultSeries is the answer of /results.
type ResultSeries struct {
	Tier   Tier          `json:"tier"`
	From   time.Time     `json:"from"`
	To     time.Time     `json:"to"`
	Points []ResultPoint `json:"points"`
}

// StateView is the current state of a probe as the API answers it (AK3).
type StateView struct {
	ProbeID      string     `json:"probe_id"`
	State        string     `json:"state"`
	Reason       string     `json:"reason"`
	Since        *time.Time `json:"since,omitempty"`
	LastResultAt *time.Time `json:"last_result_at,omitempty"`
	CollectorID  string     `json:"collector_id,omitempty"`
}

// endOfTime is the open upper bound of the state changes.
var endOfTime = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// ReasonNoResult is the reason of a probe without a state row.
const ReasonNoResult = "no_result"

// StateChangeView is one state change of a probe as the API answers it (AK4).
type StateChangeView struct {
	ID          string    `json:"id"`
	ProbeID     string    `json:"probe_id"`
	CIID        string    `json:"ci_id,omitempty"`
	FromState   string    `json:"from_state"`
	ToState     string    `json:"to_state"`
	Reason      string    `json:"reason"`
	ObservedAt  time.Time `json:"observed_at"`
	ReceivedAt  time.Time `json:"received_at"`
	Late        bool      `json:"late"`
	Notify      bool      `json:"notify"`
	RootEventID string    `json:"root_event_id,omitempty"`
}

// QueryStore reads the probe data of a visible probe; PGRepository and
// MemoryRepository implement it.
type QueryStore interface {
	// ProbeResults returns the points of the tier between from and to,
	// oldest first, at most MaxResultPoints.
	ProbeResults(ctx context.Context, probeID string, tier Tier, from, to time.Time) ([]ResultPoint, error)
	// ProbeState returns the state row; ok is false without one.
	ProbeState(ctx context.Context, probeID string) (state StateView, ok bool, err error)
	// ProbeStateChanges returns the changes between from and to after the
	// position of page, newest observed_at first, at most one more than the
	// limit, and their number.
	ProbeStateChanges(ctx context.Context, probeID string, from, to time.Time, page api.PaginationParams) ([]StateChangeView, int, error)
}

// The keyset order of the state changes.
const (
	changeSort = "observed_at"
	changeDir  = "desc"
)

// changePosition reads the (observed_at, id) position of a keyset cursor
// of the state changes; a cursor of another order is ErrInvalidCursor.
// validation: fixed texts wrapping ErrInvalidCursor (API-03).
func changePosition(c *api.Cursor) (time.Time, string, error) {
	if !c.Matches(changeSort, changeDir) {
		return time.Time{}, "", fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
	}
	at, err := time.Parse(time.RFC3339Nano, c.Value)
	if err != nil || !api.IsUUID(c.ID) {
		return time.Time{}, "", fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
	}
	return at, c.ID, nil
}

// changeCursor is the cursor after a state change.
func changeCursor(c *StateChangeView) string {
	return api.EncodeCursor(api.Cursor{Sort: changeSort, Dir: changeDir, Value: c.ObservedAt.UTC().Format(time.RFC3339Nano), ID: c.ID})
}

// registerQueryRoutes mounts the query routes.
func (h *Handler) registerQueryRoutes(r chi.Router) {
	r.Get("/api/v1/monitoring/probes/{id}/results", h.results)
	r.Get("/api/v1/monitoring/probes/{id}/state", h.state)
	r.Get("/api/v1/monitoring/probes/{id}/state-changes", h.stateChanges)
}

// visibleProbe loads the probe under RLS (404 outside the scope) and the
// query store (AK5).
func (h *Handler) visibleProbe(w http.ResponseWriter, r *http.Request) (*Probe, QueryStore, bool) {
	if !requireOrg(w, r) {
		return nil, nil, false
	}
	qs, ok := h.store.(QueryStore)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "probe data are not available")
		return nil, nil, false
	}
	p, err := h.store.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return nil, nil, false
	}
	return p, qs, true
}

// timeRange reads from and to: to defaults to now, from to to minus
// defaultSpan; a malformed value is 400.
func timeRange(w http.ResponseWriter, r *http.Request, now time.Time, defaultSpan time.Duration) (from, to time.Time, ok bool) {
	q := r.URL.Query()
	to = now
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "to must be an RFC 3339 time")
			return from, to, false
		}
		to = t
	}
	from = to.Add(-defaultSpan)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "from must be an RFC 3339 time")
			return from, to, false
		}
		from = t
	}
	return from.UTC(), to.UTC(), true
}

func (h *Handler) results(w http.ResponseWriter, r *http.Request) {
	p, qs, ok := h.visibleProbe(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	from, to, ok := timeRange(w, r, now, DefaultResultSpan)
	if !ok {
		return
	}
	layers, err := h.store.Layers(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	interval := time.Duration(Resolve(p, layers).IntervalS.Int()) * time.Second
	tier, err := SelectTier(from, to, now, interval, r.URL.Query().Get("step"))
	if err != nil {
		writeError(w, err)
		return
	}
	points, err := qs.ProbeResults(r.Context(), p.ID, tier, from, to)
	if err != nil {
		writeError(w, err)
		return
	}
	if points == nil {
		points = []ResultPoint{}
	}
	api.WriteJSON(w, http.StatusOK, ResultSeries{Tier: tier, From: from, To: to, Points: points})
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	p, qs, ok := h.visibleProbe(w, r)
	if !ok {
		return
	}
	st, found, err := qs.ProbeState(r.Context(), p.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		st = StateView{ProbeID: p.ID, State: string(probewire.StateUnknown), Reason: ReasonNoResult}
	}
	api.WriteJSON(w, http.StatusOK, st)
}

func (h *Handler) stateChanges(w http.ResponseWriter, r *http.Request) {
	p, qs, ok := h.visibleProbe(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	err := page.CursorError
	if err == nil && page.Cursor != nil {
		_, _, err = changePosition(page.Cursor)
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	q := r.URL.Query()
	// Without from and to every change is listed (within the 400 days of
	// retention).
	from, to := time.Time{}, endOfTime
	if q.Get("from") != "" || q.Get("to") != "" {
		if from, to, ok = timeRange(w, r, endOfTime, MaxResultSpan); !ok {
			return
		}
		if !to.After(from) {
			writeError(w, unprocessable("to", "to must be after from"))
			return
		}
	}
	rows, total, err := qs.ProbeStateChanges(r.Context(), p.ID, from, to, page)
	if err != nil {
		writeError(w, err)
		return
	}
	list := api.ListResponse[StateChangeView]{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		list.HasMore = true
		list.NextCursor = changeCursor(&list.Data[page.Limit-1])
	}
	if list.Data == nil {
		list.Data = []StateChangeView{}
	}
	api.WriteJSON(w, http.StatusOK, list)
}

// ── PostgreSQL ────────────────────────────────────────────────────────────

// ProbeResults implements QueryStore: raw rows of probe_result_v without
// stale epochs, or the buckets of the view of the tier from the bucket that
// holds from. It reads the newest MaxResultPoints points and returns them
// oldest first: a raw series with more rows than its interval suggests (a
// shorter interval inside the span, several collectors) loses its oldest
// points, never the most recent ones.
func (r *PGRepository) ProbeResults(ctx context.Context, probeID string, tier Tier, from, to time.Time) ([]ResultPoint, error) {
	spec, ok := specOf(tier)
	if !ok {
		return nil, fmt.Errorf("probe results: unknown tier %q", tier)
	}
	out := make([]ResultPoint, 0)
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		var rows pgx.Rows
		var err error
		if tier == TierRaw {
			rows, err = tx.Query(ctx, `SELECT time, status, rtt_ms::float8, loss::float8, value
				FROM probe_result_v
				WHERE organization_id = $1 AND probe_id = $2 AND time >= $3 AND time < $4 AND NOT stale_epoch
				ORDER BY time DESC LIMIT $5`, orgID, probeID, from, to, MaxResultPoints)
		} else {
			// The view name comes from the fixed tier list, never from input.
			rows, err = tx.Query(ctx, `SELECT bucket, total, ok, fail, error, rtt_avg, rtt_min::float8, rtt_max::float8,
				loss_avg, value_avg, value_min, value_max
				FROM `+spec.view+`
				WHERE organization_id = $1 AND probe_id = $2 AND bucket >= $3 AND bucket < $4
				ORDER BY bucket DESC LIMIT $5`, orgID, probeID, from.Truncate(spec.width), to, MaxResultPoints)
		}
		if err != nil {
			return fmt.Errorf("probe results: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var pt ResultPoint
			if tier == TierRaw {
				var status int16
				var rtt, loss, value *float64
				if err := rows.Scan(&pt.T, &status, &rtt, &loss, &value); err != nil {
					return fmt.Errorf("probe results: %w", err)
				}
				pt = rawPoint(pt.T, status, rtt, loss, value)
			} else if err := rows.Scan(&pt.T, &pt.Total, &pt.OK, &pt.Fail, &pt.Error, &pt.RTTAvg, &pt.RTTMin, &pt.RTTMax,
				&pt.LossAvg, &pt.ValueAvg, &pt.ValueMin, &pt.ValueMax); err != nil {
				return fmt.Errorf("probe results: %w", err)
			}
			pt.T = pt.T.UTC()
			out = append(out, pt)
		}
		return rows.Err()
	})
	slices.Reverse(out)
	return out, err
}

// rawPoint is the point of one raw result: total 1, the status counted by
// the encoding of probe_result.status (0 ok, 1 fail, 2 error) and the
// measurements as mean, minimum and maximum.
func rawPoint(at time.Time, status int16, rtt, loss, value *float64) ResultPoint {
	pt := ResultPoint{T: at, Total: 1, RTTAvg: rtt, RTTMin: rtt, RTTMax: rtt, LossAvg: loss,
		ValueAvg: value, ValueMin: value, ValueMax: value}
	switch status {
	case 0:
		pt.OK = 1
	case 1:
		pt.Fail = 1
	default:
		pt.Error = 1
	}
	return pt
}

// ProbeState implements QueryStore.
func (r *PGRepository) ProbeState(ctx context.Context, probeID string) (StateView, bool, error) {
	st := StateView{ProbeID: probeID}
	found := false
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		var since, last *time.Time
		var collector *string
		err := tx.QueryRow(ctx, `SELECT state, reason, since, last_result_at, collector_id::text
			FROM probe_state WHERE organization_id = $1 AND probe_id = $2`, orgID, probeID).
			Scan(&st.State, &st.Reason, &since, &last, &collector)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("probe state: %w", err)
		}
		found = true
		st.Since, st.LastResultAt = utcPtr(since), utcPtr(last)
		if collector != nil {
			st.CollectorID = *collector
		}
		return nil
	})
	return st, found, err
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ProbeStateChanges implements QueryStore.
func (r *PGRepository) ProbeStateChanges(ctx context.Context, probeID string, from, to time.Time, page api.PaginationParams) ([]StateChangeView, int, error) {
	out := make([]StateChangeView, 0)
	total := 0
	err := r.tenant(ctx, func(ctx context.Context, tx pgx.Tx, orgID string) error {
		base := `FROM probe_state_change WHERE organization_id = $1 AND probe_id = $2 AND observed_at >= $3 AND observed_at < $4`
		args := []any{orgID, probeID, from, to}
		if err := tx.QueryRow(ctx, `SELECT count(*) `+base, args...).Scan(&total); err != nil {
			return fmt.Errorf("count probe state changes: %w", err)
		}
		where := ""
		if page.Cursor != nil {
			at, id, err := changePosition(page.Cursor)
			if err != nil {
				return err
			}
			where = " AND " + api.KeysetClause(changeSort, "timestamptz", changeDir, len(args)+1)
			args = append(args, at.UTC().Format(time.RFC3339Nano), id)
		}
		tail := fmt.Sprintf(" ORDER BY observed_at DESC, id DESC LIMIT $%d", len(args)+1)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", len(args)+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, `SELECT id::text, probe_id::text, COALESCE(ci_id::text, ''), from_state, to_state, reason,
			observed_at, received_at, late, notify, COALESCE(root_event_id::text, '') `+base+where+tail, args...)
		if err != nil {
			return fmt.Errorf("probe state changes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c StateChangeView
			if err := rows.Scan(&c.ID, &c.ProbeID, &c.CIID, &c.FromState, &c.ToState, &c.Reason, &c.ObservedAt, &c.ReceivedAt,
				&c.Late, &c.Notify, &c.RootEventID); err != nil {
				return fmt.Errorf("probe state changes: %w", err)
			}
			c.ObservedAt, c.ReceivedAt = c.ObservedAt.UTC(), c.ReceivedAt.UTC()
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
