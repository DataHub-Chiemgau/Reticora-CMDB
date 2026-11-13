package probe_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The query API of the probe data on PostgreSQL and on the memory
// repository (PRB-06, MON-03, WP-975): results with their tier from
// probe_result_v and the aggregate views, the state and the state changes
// with cursor paging, and 404 for a probe outside the caller's scope before
// any series is read.
//
// Retest:
//
//	cd backend && go test -count=1 -p 1 -run 'TestProbeResults|TestProbeState|TestProbeQueryScope' ./internal/probe/

// queryCaller is the scope of a request: org-wide without clients and
// sites.
type queryCaller struct {
	org            string
	clients, sites []string
}

// serveQuery sends a GET of the caller to mux and decodes the body into
// out (when not nil).
func serveQuery(t *testing.T, mux http.Handler, as queryCaller, path string, out any) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	scope := database.OrgWideScope(as.org, hUser)
	if as.clients != nil {
		scope.Clients = database.ScopeIDs(as.clients...)
	}
	if as.sites != nil {
		scope.Sites = database.ScopeIDs(as.sites...)
	}
	ctx := database.ContextWithTenantScope(req.Context(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: as.org, UserID: hUser})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: %v in %s", path, err, rec.Body.String())
		}
	}
	var problem struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	if rec.Code == http.StatusUnprocessableEntity {
		return rec.Code, problem.Field
	}
	return rec.Code, rec.Body.String()
}

// queryEnv is the probe API on PostgreSQL with the result environment of
// WP-945/WP-972.
type queryEnv struct {
	*resultEnv
	mux       *chi.Mux
	collector string
}

func newQueryEnv(t *testing.T) *queryEnv {
	t.Helper()
	e := &queryEnv{resultEnv: newRollupEnv(t), mux: chi.NewRouter()}
	probe.NewHandler(probe.NewPGRepository(e.f.App)).RegisterRoutes(e.mux)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp975', 'active') RETURNING id::text`, e.f.OrgA)
	return e
}

func (e *queryEnv) orgA() queryCaller { return queryCaller{org: e.f.OrgA} }

func rfc(t time.Time) string { return url.QueryEscape(t.UTC().Format(time.RFC3339Nano)) }

// change inserts a state change of the probe observed at at.
func (e *queryEnv) changeAt(probeID string, seq int, at time.Time, from, to string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, reason, observed_at, received_at,
			epoch, seq, collector_id, late, notify)
		VALUES ($1, $2, $3, $4, 'timeout', $5, $5::timestamptz + interval '2 seconds', 1, $6, $7, $8, $9) RETURNING id::text`,
		e.f.OrgA, probeID, from, to, at, seq, e.collector, seq%2 == 0, seq%2 == 1)
}

// TestProbeResultsReturnsTierAndPoints covers AK1 and AK2: the raw tier of
// a short span with one point per result (total 1, status counted, stale
// epochs left out), an aggregate tier of a long span with the sums of its
// buckets, a forced step and 422 for a step outside its window; the memory
// repository answers with its tier and no points.
func TestProbeResultsReturnsTierAndPoints(t *testing.T) {
	t.Run("postgres", func(t *testing.T) {
		e := newQueryEnv(t)
		p := e.probeOfClient(e.f.OrgA, e.f.Client1, "10.75.0.1:22")
		now := time.Now().UTC()
		recent := now.Add(-30 * time.Minute).Truncate(time.Second)
		e.insertResult(e.f.OrgA, p, 1, recent, 0, float32(2), float32(0), nil, false)
		e.insertResult(e.f.OrgA, p, 2, recent.Add(time.Minute), 1, nil, float32(1), nil, false)
		e.insertResult(e.f.OrgA, p, 3, recent.Add(2*time.Minute), 2, nil, nil, 7.5, false)
		e.insertResult(e.f.OrgA, p, 4, recent.Add(3*time.Minute), 0, float32(9), nil, nil, true) // stale epoch
		// Three days back: two results in one 5-minute bucket.
		old := now.Add(-3 * 24 * time.Hour).Truncate(time.Hour)
		e.insertResult(e.f.OrgA, p, 5, old.Add(time.Minute), 0, float32(4), nil, nil, false)
		e.insertResult(e.f.OrgA, p, 6, old.Add(2*time.Minute), 1, float32(6), nil, nil, false)
		e.refreshAll()

		var raw probe.ResultSeries
		path := "/api/v1/monitoring/probes/" + p + "/results?from=" + rfc(now.Add(-time.Hour)) + "&to=" + rfc(now)
		if code, body := serveQuery(t, e.mux, e.orgA(), path, &raw); code != http.StatusOK {
			t.Fatalf("raw: %d %s", code, body)
		}
		if raw.Tier != probe.TierRaw || len(raw.Points) != 3 {
			t.Fatalf("raw: tier %s with %d points, want raw with 3 (no stale epoch)", raw.Tier, len(raw.Points))
		}
		first, second, third := raw.Points[0], raw.Points[1], raw.Points[2]
		if first.Total != 1 || first.OK != 1 || first.RTTAvg == nil || *first.RTTAvg != 2 || *first.RTTMin != 2 || *first.RTTMax != 2 ||
			first.LossAvg == nil || *first.LossAvg != 0 || !first.T.Equal(recent) {
			t.Errorf("first raw point %+v", first)
		}
		if second.Fail != 1 || second.RTTAvg != nil || third.Error != 1 || third.ValueAvg == nil || *third.ValueAvg != 7.5 {
			t.Errorf("raw points %+v %+v", second, third)
		}

		var agg probe.ResultSeries
		path = "/api/v1/monitoring/probes/" + p + "/results?from=" + rfc(now.Add(-30*24*time.Hour)) + "&to=" + rfc(now)
		if code, body := serveQuery(t, e.mux, e.orgA(), path, &agg); code != http.StatusOK {
			t.Fatalf("30 days: %d %s", code, body)
		}
		if agg.Tier != probe.Tier1h {
			t.Fatalf("30 days: tier %s, want 1h", agg.Tier)
		}
		var bucket *probe.ResultPoint
		for i := range agg.Points {
			if agg.Points[i].T.Equal(old) {
				bucket = &agg.Points[i]
			}
		}
		if bucket == nil || bucket.Total != 2 || bucket.OK != 1 || bucket.Fail != 1 || bucket.RTTAvg == nil || *bucket.RTTAvg != 5 ||
			*bucket.RTTMin != 4 || *bucket.RTTMax != 6 {
			t.Fatalf("1h bucket of %v: %+v in %+v", old, bucket, agg.Points)
		}

		var forced probe.ResultSeries
		path = "/api/v1/monitoring/probes/" + p + "/results?step=5m&from=" + rfc(old.Add(-time.Hour)) + "&to=" + rfc(old.Add(time.Hour))
		if code, body := serveQuery(t, e.mux, e.orgA(), path, &forced); code != http.StatusOK {
			t.Fatalf("step 5m: %d %s", code, body)
		}
		if forced.Tier != probe.Tier5m || len(forced.Points) != 1 || forced.Points[0].Total != 2 {
			t.Fatalf("step 5m: %+v", forced)
		}
		path = "/api/v1/monitoring/probes/" + p + "/results?step=raw&from=" + rfc(now.Add(-20*24*time.Hour)) + "&to=" + rfc(now.Add(-20*24*time.Hour+time.Hour))
		if code, field := serveQuery(t, e.mux, e.orgA(), path, nil); code != http.StatusUnprocessableEntity || field != "step" {
			t.Fatalf("step raw 20 days back: %d %s, want 422 step", code, field)
		}
		path = "/api/v1/monitoring/probes/" + p + "/results?from=" + rfc(now.Add(-401*24*time.Hour)) + "&to=" + rfc(now)
		if code, field := serveQuery(t, e.mux, e.orgA(), path, nil); code != http.StatusUnprocessableEntity || field != "from" {
			t.Fatalf("401 days: %d %s, want 422 from", code, field)
		}
		if code, _ := serveQuery(t, e.mux, e.orgA(), "/api/v1/monitoring/probes/"+p+"/results?from=yesterday", nil); code != http.StatusBadRequest {
			t.Fatalf("malformed from: %d, want 400", code)
		}
	})
	t.Run("memory", func(t *testing.T) {
		e := newHandlerEnv(t)
		p := e.mustCreate(`{"type":"tcp","target":"db.example","client_id":"` + hClient + `","params":{"port":5432}}`)
		var s probe.ResultSeries
		if code, body := serveQuery(t, e.mux, queryCaller{org: hOrg}, "/api/v1/monitoring/probes/"+p.ID+"/results", &s); code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		if s.Tier != probe.TierRaw || s.Points == nil || len(s.Points) != 0 || !s.To.After(s.From) {
			t.Fatalf("memory series %+v, want tier raw with an empty point list", s)
		}
	})
}

// TestProbeResultsKeepNewestPoints covers AK1 and AK2: a raw series with
// more rows than its interval suggests is cut at 2,000 points on its oldest
// side, so the newest result is in the answer, oldest first; an aggregate
// query with from off the bucket grid keeps the bucket of the newest result.
func TestProbeResultsKeepNewestPoints(t *testing.T) {
	e := newQueryEnv(t)
	p := e.probeOfClient(e.f.OrgA, e.f.Client1, "10.75.9.1:22")
	now := time.Now().UTC().Truncate(time.Second)
	newest := now.Add(-10 * time.Minute)
	// 2,100 results one second apart (the probe's interval is 60 s).
	e.mustExec(`INSERT INTO probe_result (time, organization_id, probe_id, collector_id, seq, epoch, status, rtt_ms)
		SELECT $1::timestamptz - make_interval(secs => g), $2, $3, $4, g + 1, 1, 0, 1
		FROM generate_series(0, 2099) AS g`, newest, e.f.OrgA, p, e.collector)
	e.refreshAll()

	var raw probe.ResultSeries
	path := "/api/v1/monitoring/probes/" + p + "/results?from=" + rfc(now.Add(-time.Hour)) + "&to=" + rfc(now)
	if code, body := serveQuery(t, e.mux, e.orgA(), path, &raw); code != http.StatusOK {
		t.Fatalf("raw: %d %s", code, body)
	}
	if raw.Tier != probe.TierRaw || len(raw.Points) != probe.MaxResultPoints {
		t.Fatalf("raw: tier %s with %d points, want raw with %d", raw.Tier, len(raw.Points), probe.MaxResultPoints)
	}
	if last := raw.Points[len(raw.Points)-1].T; !last.Equal(newest) {
		t.Fatalf("raw: last point at %v, want the newest result at %v", last, newest)
	}
	if first := raw.Points[0].T; !first.Equal(newest.Add(-(probe.MaxResultPoints - 1) * time.Second)) {
		t.Fatalf("raw: first point at %v, want the series cut on its oldest side", first)
	}

	// from off the 5-minute grid, exactly 2,000 five-minute spans to to.
	to := newest.Truncate(5 * time.Minute).Add(5 * time.Minute)
	from := to.Add(-2000 * 5 * time.Minute).Add(2 * time.Minute)
	to = from.Add(2000 * 5 * time.Minute)
	var agg probe.ResultSeries
	path = "/api/v1/monitoring/probes/" + p + "/results?from=" + rfc(from) + "&to=" + rfc(to)
	if code, body := serveQuery(t, e.mux, e.orgA(), path, &agg); code != http.StatusOK {
		t.Fatalf("aggregate: %d %s", code, body)
	}
	if agg.Tier == probe.Tier5m || len(agg.Points) == 0 || len(agg.Points) > probe.MaxResultPoints {
		t.Fatalf("aggregate: tier %s with %d points, want a coarser tier than 5m (2,001 buckets)", agg.Tier, len(agg.Points))
	}
	if last := agg.Points[len(agg.Points)-1].T; newest.Before(last) || newest.Sub(last) >= time.Hour {
		t.Fatalf("aggregate: last bucket %v does not hold the newest result %v", last, newest)
	}
	path = "/api/v1/monitoring/probes/" + p + "/results?step=5m&from=" + rfc(from) + "&to=" + rfc(to)
	if code, field := serveQuery(t, e.mux, e.orgA(), path, nil); code != http.StatusUnprocessableEntity || field != "step" {
		t.Fatalf("step 5m over 2,001 buckets: %d %s, want 422 step", code, field)
	}
}

// TestProbeStateAndChanges covers AK3 and AK4: without a state row unknown
// with no_result, else the row; the changes newest first with every field.
func TestProbeStateAndChanges(t *testing.T) {
	checkChanges := func(t *testing.T, got []probe.StateChangeView, wantIDs []string) {
		t.Helper()
		if len(got) != len(wantIDs) {
			t.Fatalf("%d changes, want %d", len(got), len(wantIDs))
		}
		for i, id := range wantIDs {
			c := got[i]
			if c.ID != id || c.FromState == "" || c.ToState == "" || c.Reason != "timeout" || c.ObservedAt.IsZero() ||
				!c.ReceivedAt.After(c.ObservedAt) {
				t.Fatalf("change %d = %+v, want id %s with every field", i, c, id)
			}
		}
	}
	t.Run("postgres", func(t *testing.T) {
		e := newQueryEnv(t)
		p := e.probeOfClient(e.f.OrgA, e.f.Client1, "10.75.1.1:22")
		var st probe.StateView
		if code, body := serveQuery(t, e.mux, e.orgA(), "/api/v1/monitoring/probes/"+p+"/state", &st); code != http.StatusOK {
			t.Fatalf("state: %d %s", code, body)
		}
		if st.State != "unknown" || st.Reason != probe.ReasonNoResult || st.ProbeID != p {
			t.Fatalf("state without a row %+v, want unknown/no_result", st)
		}
		since := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		e.mustExec(`INSERT INTO probe_state (probe_id, organization_id, state, reason, since, last_result_at, collector_id)
			VALUES ($1, $2, 'down', 'timeout', $3, $3::timestamptz + interval '1 minute', $4)`, p, e.f.OrgA, since, e.collector)
		if code, body := serveQuery(t, e.mux, e.orgA(), "/api/v1/monitoring/probes/"+p+"/state", &st); code != http.StatusOK {
			t.Fatalf("state: %d %s", code, body)
		}
		if st.State != "down" || st.Reason != "timeout" || st.Since == nil || !st.Since.Equal(since) || st.LastResultAt == nil ||
			st.CollectorID != e.collector {
			t.Fatalf("state %+v", st)
		}
		base := time.Now().UTC().Add(-2 * time.Hour)
		a := e.changeAt(p, 1, base, "unknown", "up")
		b := e.changeAt(p, 2, base.Add(time.Minute), "up", "down")
		c := e.changeAt(p, 3, base.Add(2*time.Minute), "down", "up")
		// The change to down follows from the first change as its root event.
		e.mustExec(`UPDATE probe_state_change SET root_event_id = $1 WHERE id = $2`, a, b)
		var list struct {
			Data  []probe.StateChangeView `json:"data"`
			Total int                     `json:"total"`
		}
		if code, body := serveQuery(t, e.mux, e.orgA(), "/api/v1/monitoring/probes/"+p+"/state-changes", &list); code != http.StatusOK {
			t.Fatalf("changes: %d %s", code, body)
		}
		checkChanges(t, list.Data, []string{c, b, a})
		if list.Total != 3 || !list.Data[1].Late || list.Data[1].Notify || list.Data[0].Late || !list.Data[0].Notify {
			t.Fatalf("changes %+v: late and notify not as stored", list)
		}
		if list.Data[1].RootEventID != a || list.Data[0].RootEventID != "" || list.Data[2].RootEventID != "" {
			t.Fatalf("root_event_id %q/%q/%q, want only the change to down rooted in %s",
				list.Data[0].RootEventID, list.Data[1].RootEventID, list.Data[2].RootEventID, a)
		}
	})
	t.Run("memory", func(t *testing.T) {
		e := newHandlerEnv(t)
		p := e.mustCreate(`{"type":"tcp","target":"db.example","client_id":"` + hClient + `","params":{"port":5432}}`)
		var st probe.StateView
		if code, _ := serveQuery(t, e.mux, queryCaller{org: hOrg}, "/api/v1/monitoring/probes/"+p.ID+"/state", &st); code != http.StatusOK ||
			st.State != "unknown" || st.Reason != probe.ReasonNoResult {
			t.Fatalf("memory state without a row: %d %+v", code, st)
		}
		since := time.Now().UTC().Add(-time.Hour)
		e.repo.RecordState(probe.StateView{ProbeID: p.ID, State: "up", Reason: "", Since: &since})
		if code, _ := serveQuery(t, e.mux, queryCaller{org: hOrg}, "/api/v1/monitoring/probes/"+p.ID+"/state", &st); code != http.StatusOK ||
			st.State != "up" {
			t.Fatalf("memory state: %d %+v", code, st)
		}
		base := time.Now().UTC().Add(-2 * time.Hour)
		ids := make([]string, 3)
		for i := range ids {
			ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			c := probe.StateChangeView{ID: ids[i], ProbeID: p.ID, FromState: "up", ToState: "down",
				Reason: "timeout", ObservedAt: base.Add(time.Duration(i) * time.Minute),
				ReceivedAt: base.Add(time.Duration(i)*time.Minute + time.Second), Late: i == 1, Notify: i != 1}
			if i > 0 {
				c.RootEventID = ids[0]
			}
			if err := e.repo.RecordStateChange(c); err != nil {
				t.Fatal(err)
			}
		}
		var list struct {
			Data []probe.StateChangeView `json:"data"`
		}
		if code, body := serveQuery(t, e.mux, queryCaller{org: hOrg}, "/api/v1/monitoring/probes/"+p.ID+"/state-changes", &list); code != http.StatusOK {
			t.Fatalf("memory changes: %d %s", code, body)
		}
		checkChanges(t, list.Data, []string{ids[2], ids[1], ids[0]})
		for i, c := range list.Data {
			n := 2 - i
			wantRoot := ""
			if n > 0 {
				wantRoot = ids[0]
			}
			if c.Late != (n == 1) || c.Notify != (n != 1) || c.RootEventID != wantRoot {
				t.Fatalf("memory change %d = %+v: late, notify or root_event_id not as recorded", n, c)
			}
		}
	})
}

// changePage is one page of /state-changes.
type changePage struct {
	Data       []probe.StateChangeView `json:"data"`
	Total      int                     `json:"total"`
	HasMore    bool                    `json:"has_more"`
	NextCursor string                  `json:"next_cursor"`
}

// walkChanges follows next_cursor from the first page of path and returns
// the ids in order.
func walkChanges(t *testing.T, mux http.Handler, as queryCaller, path string) []string {
	t.Helper()
	var ids []string
	next := path
	for range 20 {
		var page changePage
		if code, body := serveQuery(t, mux, as, next, &page); code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", next, code, body)
		}
		for _, c := range page.Data {
			ids = append(ids, c.ID)
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatalf("last page with next_cursor")
			}
			return ids
		}
		next = path + "&cursor=" + url.QueryEscape(page.NextCursor)
	}
	t.Fatal("more than 20 pages")
	return nil
}

// TestProbeStateChangesCursorPagination covers AK4: the pages of limit 2
// follow next_cursor without gap or repetition, also for two changes at the
// same instant; from and to narrow the list; a forged cursor is 400.
func TestProbeStateChangesCursorPagination(t *testing.T) {
	check := func(t *testing.T, mux http.Handler, as queryCaller, probeID string, want []string, base time.Time) {
		t.Helper()
		got := walkChanges(t, mux, as, "/api/v1/monitoring/probes/"+probeID+"/state-changes?limit=2")
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("pages %v, want %v", got, want)
		}
		// from and to: the changes in [base+1m, base+3m).
		got = walkChanges(t, mux, as, "/api/v1/monitoring/probes/"+probeID+"/state-changes?limit=2&from="+
			rfc(base.Add(time.Minute))+"&to="+rfc(base.Add(3*time.Minute)))
		if fmt.Sprint(got) != fmt.Sprint(want[1:4]) {
			t.Fatalf("narrowed pages %v, want %v", got, want[1:4])
		}
		if code, _ := serveQuery(t, mux, as, "/api/v1/monitoring/probes/"+probeID+"/state-changes?cursor=forged", nil); code != http.StatusBadRequest {
			t.Fatalf("forged cursor: %d, want 400", code)
		}
		if code, field := serveQuery(t, mux, as, "/api/v1/monitoring/probes/"+probeID+"/state-changes?from="+rfc(base)+"&to="+rfc(base), nil); code != http.StatusUnprocessableEntity || field != "to" {
			t.Fatalf("empty range: %d %s, want 422 to", code, field)
		}
	}
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	t.Run("postgres", func(t *testing.T) {
		e := newQueryEnv(t)
		p := e.probeOfClient(e.f.OrgA, e.f.Client1, "10.75.2.1:22")
		ids := map[int]string{}
		at := []time.Duration{0, time.Minute, 2 * time.Minute, 2 * time.Minute, 4 * time.Minute}
		for i, d := range at {
			ids[i] = e.changeAt(p, i+1, base.Add(d), "up", "down")
		}
		// Newest first; the two at base+2m by id descending.
		mid := []string{ids[2], ids[3]}
		if mid[0] < mid[1] {
			mid[0], mid[1] = mid[1], mid[0]
		}
		check(t, e.mux, e.orgA(), p, []string{ids[4], mid[0], mid[1], ids[1], ids[0]}, base)
	})
	t.Run("memory", func(t *testing.T) {
		e := newHandlerEnv(t)
		p := e.mustCreate(`{"type":"tcp","target":"db.example","client_id":"` + hClient + `","params":{"port":5432}}`)
		at := []time.Duration{0, time.Minute, 2 * time.Minute, 2 * time.Minute, 4 * time.Minute}
		ids := make([]string, len(at))
		for i, d := range at {
			ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			if err := e.repo.RecordStateChange(probe.StateChangeView{ID: ids[i], ProbeID: p.ID, FromState: "up", ToState: "down",
				Reason: "timeout", ObservedAt: base.Add(d), ReceivedAt: base.Add(d + time.Second)}); err != nil {
				t.Fatal(err)
			}
		}
		check(t, e.mux, queryCaller{org: hOrg}, p.ID, []string{ids[4], ids[3], ids[2], ids[1], ids[0]}, base)
	})
}

// TestProbeQueryScope covers AK5: a probe of another organization, of
// another client and of another site is 404 on all three routes, before a
// series is read; inside the scope the routes answer.
func TestProbeQueryScope(t *testing.T) {
	routes := []string{"results", "state", "state-changes"}
	t.Run("postgres", func(t *testing.T) {
		e := newQueryEnv(t)
		f := e.f
		site1 := e.site(f.OrgA, f.Client1, "wp975-site-1")
		site2 := e.site(f.OrgA, f.Client1, "wp975-site-2")
		pClient1 := e.probeOfClient(f.OrgA, f.Client1, "10.75.3.1:22")
		pClient2 := e.probeOfClient(f.OrgA, f.Client2, "10.75.3.2:22")
		pSite2 := e.scalar(`INSERT INTO probe (organization_id, collector_group_id, client_id, site_id, type, target)
			VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1),
			$2, $3, 'tcp', '10.75.3.3:22') RETURNING id::text`, f.OrgA, f.Client1, site2)
		clientB := e.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp975 b', 'wp975-b') RETURNING id::text`, f.OrgB)
		pOrgB := e.probeOfClient(f.OrgB, clientB, "10.75.3.4:22")
		e.insertResult(f.OrgA, pClient2, 1, time.Now().UTC().Add(-10*time.Minute), 0, float32(1), nil, nil, false)
		for _, c := range []struct {
			who     string
			as      queryCaller
			probeID string
			want    int
		}{
			{"organization A on a probe of B", e.orgA(), pOrgB, http.StatusNotFound},
			{"client 1 on a probe of client 2", queryCaller{org: f.OrgA, clients: []string{f.Client1}}, pClient2, http.StatusNotFound},
			{"site 1 on a probe of site 2", queryCaller{org: f.OrgA, sites: []string{site1}}, pSite2, http.StatusNotFound},
			{"client 1 on its probe", queryCaller{org: f.OrgA, clients: []string{f.Client1}}, pClient1, http.StatusOK},
			{"site 2 on its probe", queryCaller{org: f.OrgA, sites: []string{site2}}, pSite2, http.StatusOK},
			{"org-wide on client 2", e.orgA(), pClient2, http.StatusOK},
		} {
			for _, route := range routes {
				if code, body := serveQuery(t, e.mux, c.as, "/api/v1/monitoring/probes/"+c.probeID+"/"+route, nil); code != c.want {
					t.Errorf("%s, %s: %d %s, want %d", c.who, route, code, body, c.want)
				}
			}
		}
		if code, _ := serveQuery(t, e.mux, e.orgA(), "/api/v1/monitoring/probes/not-a-uuid/state", nil); code != http.StatusNotFound {
			t.Errorf("malformed id: %d, want 404", code)
		}
	})
	t.Run("memory", func(t *testing.T) {
		e := newHandlerEnv(t)
		const site1, site2 = "66666666-6666-4666-8666-666666666661", "66666666-6666-4666-8666-666666666662"
		e.repo.AddSite(hOrg, site1, hClient)
		e.repo.AddSite(hOrg, site2, hClient)
		p := e.mustCreate(`{"type":"tcp","target":"db.example","client_id":"` + hClient + `","params":{"port":5432}}`)
		pSite2 := e.mustCreate(`{"type":"tcp","target":"db2.example","client_id":"` + hClient + `","site_id":"` + site2 + `","params":{"port":5432}}`)
		for _, route := range routes {
			path := "/api/v1/monitoring/probes/" + pSite2.ID + "/" + route
			if code, _ := serveQuery(t, e.mux, queryCaller{org: hOrg, sites: []string{site1}}, path, nil); code != http.StatusNotFound {
				t.Errorf("other site, %s: %d, want 404", route, code)
			}
			if code, body := serveQuery(t, e.mux, queryCaller{org: hOrg, sites: []string{site2}}, path, nil); code != http.StatusOK {
				t.Errorf("own site, %s: %d %s, want 200", route, code, body)
			}
		}
		for _, route := range routes {
			path := "/api/v1/monitoring/probes/" + p.ID + "/" + route
			if code, _ := serveQuery(t, e.mux, queryCaller{org: hOrg, clients: []string{hClient2}}, path, nil); code != http.StatusNotFound {
				t.Errorf("other client, %s: %d, want 404", route, code)
			}
			if code, _ := serveQuery(t, e.mux, queryCaller{org: "99999999-9999-4999-8999-999999999999"}, path, nil); code != http.StatusNotFound {
				t.Errorf("other organization, %s: %d, want 404", route, code)
			}
		}
	})
}
