package tenantapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/go-chi/chi/v5"
)

// TestRoomLayoutValidationAgainstPostgres covers WP-526 (UI-18, NFR-07;
// retest F-341 and F-342) through the room handler over PostgreSQL as the
// runtime role with the principal's tenant scope:
//   - PATCH /rooms/{id} refuses coordinates outside 0..1 with 422 naming
//     layout.<ci>.x|y, and layout keys that are no CI of the organization
//     visible to the caller and located in the room (another client's CI,
//     another organization's CI, an unknown id, no UUID; WP-698) with 422
//     naming layout.<ci>; nothing is stored;
//   - x = 0.25 is stored (200) and GET returns it;
//   - a client-1 principal gets 404 on a client-2 room, also with a client-2
//     CI key, and the room stays unchanged;
//   - GET and every PATCH answer an ETag from updated_at; two editors with
//     the same ETag: the second PATCH is 409 without change and the layout
//     holds the first one's change, also when both run at the same time; a
//     PATCH without If-Match (or with "*") stays allowed, an If-Match that
//     is no ETag is 412.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRoomLayoutValidationAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "21")
	bg := context.Background()
	mux := chi.NewRouter()
	tenantapi.NewHandler(tenantapi.NewPGRepository(f.App)).RegisterRoutes(mux)

	room1 := f.Room(t, f.Client1, "wp526-room-c1")
	room2 := f.Room(t, f.Client2, "wp526-room-c2")
	ci1 := f.CI(t, f.OrgA, f.Client1, "wp526-ci-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "wp526-ci-c2")
	ciOrgB := f.CI(t, f.OrgB, "", "wp526-ci-org-b")
	// A layout key is a CI located in the room (WP-698).
	for ci, room := range map[string]string{ci1: room1, ci2: room2} {
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ci, room); err != nil {
			t.Fatalf("locate %s in %s: %v", ci, room, err)
		}
	}

	client1 := f.ClientCtx(f.Client1)
	orgWide := f.OrgCtx(f.OrgA)
	call := func(ctx context.Context, method, path, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		if ifMatch != "" {
			r.Header.Set("If-Match", ifMatch)
		}
		r = r.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	layoutBody := func(ci string, x, y float64) string {
		b, _ := json.Marshal(map[string]any{"layout": map[string]any{ci: map[string]float64{"x": x, "y": y}}})
		return string(b)
	}
	field := func(w *httptest.ResponseRecorder) string {
		var p struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		return p.Field
	}
	stored := func(roomID string) string {
		t.Helper()
		var layout string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(layout, '{}'::jsonb)::text FROM room WHERE id = $1`, roomID).Scan(&layout); err != nil {
			t.Fatalf("read layout of %s: %v", roomID, err)
		}
		return layout
	}
	storedX := func(roomID, ci string) float64 {
		t.Helper()
		var layout map[string]tenantapi.LayoutPosition
		if err := json.Unmarshal([]byte(stored(roomID)), &layout); err != nil {
			t.Fatalf("decode layout of %s: %v", roomID, err)
		}
		return layout[ci].X
	}

	get := call(client1, "GET", "/api/v1/rooms/"+room1, "", "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") == "" {
		t.Fatalf("GET own room: %d ETag=%q %s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
	before := stored(room1)

	// Range and CI keys (F-341): 422 naming the field, nothing stored.
	for _, c := range []struct {
		what, body, field string
	}{
		{"x = 1.5", layoutBody(ci1, 1.5, 0.5), "layout." + ci1 + ".x"},
		{"x = -0.01", layoutBody(ci1, -0.01, 0.5), "layout." + ci1 + ".x"},
		{"y = 1.0001", layoutBody(ci1, 0.5, 1.0001), "layout." + ci1 + ".y"},
		{"CI of client 2", layoutBody(ci2, 0.5, 0.5), "layout." + ci2},
		{"CI of organization B", layoutBody(ciOrgB, 0.5, 0.5), "layout." + ciOrgB},
		{"unknown CI id", layoutBody("c0de2100-0000-4000-8000-00000000ffff", 0.5, 0.5), "layout.c0de2100-0000-4000-8000-00000000ffff"},
		{"no UUID", layoutBody("rack-a", 0.5, 0.5), "layout.rack-a"},
	} {
		w := call(client1, "PATCH", "/api/v1/rooms/"+room1, c.body, "")
		if w.Code != http.StatusUnprocessableEntity || field(w) != c.field {
			t.Errorf("%s: %d field=%q %s, want 422 on %s", c.what, w.Code, field(w), w.Body.String(), c.field)
		}
	}
	if got := stored(room1); got != before {
		t.Fatalf("refused layouts changed the room: %s, want %s", got, before)
	}

	// A valid position is stored and read back with a new ETag.
	ok := call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, 0.25, 0.75), "")
	if ok.Code != http.StatusOK {
		t.Fatalf("PATCH x=0.25: %d %s", ok.Code, ok.Body.String())
	}
	etag := ok.Header().Get("ETag")
	if etag == "" || etag == get.Header().Get("ETag") {
		t.Fatalf("PATCH ETag %q, want a new one (before %q)", etag, get.Header().Get("ETag"))
	}
	get = call(client1, "GET", "/api/v1/rooms/"+room1, "", "")
	var rm tenantapi.Room
	if err := json.Unmarshal(get.Body.Bytes(), &rm); err != nil || get.Code != http.StatusOK {
		t.Fatalf("GET after PATCH: %d %s", get.Code, get.Body.String())
	}
	if p := rm.Layout[ci1]; p.X != 0.25 || p.Y != 0.75 {
		t.Fatalf("GET layout %+v, want x=0.25 y=0.75", rm.Layout)
	}
	if get.Header().Get("ETag") != etag {
		t.Fatalf("GET ETag %q, want the PATCH's %q", get.Header().Get("ETag"), etag)
	}

	// Client isolation: a client-2 room is not found for client 1, whatever
	// the layout names; the org-wide principal places the client-2 CI.
	room2Before := stored(room2)
	for _, body := range []string{layoutBody(ci1, 0.5, 0.5), layoutBody(ci2, 0.5, 0.5)} {
		if w := call(client1, "PATCH", "/api/v1/rooms/"+room2, body, ""); w.Code != http.StatusNotFound {
			t.Errorf("client 1 PATCH of a client-2 room: %d %s, want 404", w.Code, w.Body.String())
		}
	}
	if w := call(client1, "GET", "/api/v1/rooms/"+room2, "", ""); w.Code != http.StatusNotFound {
		t.Errorf("client 1 GET of a client-2 room: %d, want 404", w.Code)
	}
	if got := stored(room2); got != room2Before {
		t.Fatalf("client-2 room changed by client 1: %s", got)
	}
	if w := call(orgWide, "PATCH", "/api/v1/rooms/"+room2, layoutBody(ci2, 0.5, 0.5), ""); w.Code != http.StatusOK {
		t.Fatalf("org-wide PATCH with the client-2 CI: %d %s", w.Code, w.Body.String())
	}

	// Lost update (F-342): two editors read the same ETag; the second PATCH
	// is refused and the first one's change stays.
	first := call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, 0.3, 0.75), etag)
	if first.Code != http.StatusOK {
		t.Fatalf("first editor: %d %s", first.Code, first.Body.String())
	}
	second := call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, 0.9, 0.75), etag)
	if second.Code != http.StatusConflict || field(second) != "If-Match" {
		t.Fatalf("second editor with the stale ETag: %d %s, want 409 on If-Match", second.Code, second.Body.String())
	}
	if x := storedX(room1, ci1); x != 0.3 {
		t.Fatalf("layout after the refused PATCH: x=%v, want the first editor's 0.3", x)
	}
	if w := call(client1, "PATCH", "/api/v1/rooms/"+room1, `{"name":"wp526-renamed"}`, etag); w.Code != http.StatusConflict {
		t.Fatalf("stale ETag without layout: %d, want 409", w.Code)
	}
	if w := call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, 0.4, 0.75), `"not-a-version"`); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match that is no ETag: %d, want 412", w.Code)
	}
	if w := call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, 0.4, 0.75), "*"); w.Code != http.StatusOK {
		t.Fatalf("If-Match *: %d %s", w.Code, w.Body.String())
	}
	current := call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, 0.45, 0.75), "")
	if current.Code != http.StatusOK {
		t.Fatalf("PATCH without If-Match: %d %s", current.Code, current.Body.String())
	}
	if x := storedX(room1, ci1); x != 0.45 {
		t.Fatalf("layout after the PATCH without If-Match: x=%v, want 0.45", x)
	}

	// Both editors at the same time: the row lock lets exactly one through.
	shared := current.Header().Get("ETag")
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, x := range []float64{0.6, 0.7} {
		wg.Add(1)
		go func(i int, x float64) {
			defer wg.Done()
			codes[i] = call(client1, "PATCH", "/api/v1/rooms/"+room1, layoutBody(ci1, x, 0.75), shared).Code
		}(i, x)
	}
	wg.Wait()
	sorted := append([]int(nil), codes...)
	sort.Ints(sorted)
	if sorted[0] != http.StatusOK || sorted[1] != http.StatusConflict {
		t.Fatalf("concurrent editors with one ETag: %v, want one 200 and one 409", codes)
	}
	winner := 0.6
	if codes[1] == http.StatusOK {
		winner = 0.7
	}
	if x := storedX(room1, ci1); x != winner {
		t.Fatalf("layout after concurrent editors: x=%v, want the accepted %v", x, winner)
	}
}

// TestRoomLayoutForeignClientEntriesAgainstPostgres covers WP-698 (UI-18,
// TEN-05, TEN-09; retest F-427) through the room handler over PostgreSQL as
// the runtime role with the principal's tenant scope:
//   - a CI that moved to a room of client 2 (client and location changed)
//     drops out of the old room's layout for client 1 and for the org-wide
//     principal; client 1 positions its remaining CI with what the room plan
//     sends (server layout plus the moved marker): 200, not 422;
//   - the org-wide principal cannot put a client-2 CI into a client-1 room's
//     layout (422); with such an entry stored earlier, a client-1 principal
//     reads only its own entry (GET and list), a PATCH with only its own CI
//     keeps the client-2 position, and the client-2 key is refused exactly
//     like an unknown id (same status, field and detail: no existence
//     oracle);
//   - a layout key must be a CI located in the room: a CI of the
//     organization elsewhere is 422, a deleted CI drops out of GET and is
//     removed by the next PATCH of a principal who sees it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRoomLayoutForeignClientEntriesAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "21")
	bg := context.Background()
	mux := chi.NewRouter()
	tenantapi.NewHandler(tenantapi.NewPGRepository(f.App)).RegisterRoutes(mux)

	client1 := f.ClientCtx(f.Client1)
	client2 := f.ClientCtx(f.Client2)
	orgWide := f.OrgCtx(f.OrgA)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	type position = tenantapi.LayoutPosition
	patch := func(ctx context.Context, room string, layout map[string]position) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"layout": layout})
		return call(ctx, "PATCH", "/api/v1/rooms/"+room, string(b))
	}
	read := func(ctx context.Context, room string) map[string]position {
		t.Helper()
		w := call(ctx, "GET", "/api/v1/rooms/"+room, "")
		var rm tenantapi.Room
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rm) != nil {
			t.Fatalf("GET room %s: %d %s", room, w.Code, w.Body.String())
		}
		if rm.Layout == nil {
			rm.Layout = map[string]position{}
		}
		return rm.Layout
	}
	stored := func(room string) map[string]position {
		t.Helper()
		var raw []byte
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(layout, '{}'::jsonb) FROM room WHERE id = $1`, room).Scan(&raw); err != nil {
			t.Fatalf("read layout of %s: %v", room, err)
		}
		out := map[string]position{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode layout of %s: %v", room, err)
		}
		return out
	}
	locate := func(ci, client, room string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET client_id = $2, location_id = $3 WHERE id = $1`, ci, client, room); err != nil {
			t.Fatalf("locate %s in %s: %v", ci, room, err)
		}
	}
	type problem struct {
		Status int    `json:"status"`
		Field  string `json:"field"`
		Detail string `json:"detail"`
	}
	refusal := func(w *httptest.ResponseRecorder, key string) problem {
		t.Helper()
		var p problem
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		// Field and detail name the key; apart from it both answers are equal.
		p.Detail = strings.ReplaceAll(p.Detail, key, "<key>")
		p.Field = strings.ReplaceAll(p.Field, key, "<key>")
		return p
	}

	// A CI moved to client 2 (the UI path: PATCH /cis/{id} with client_id
	// and location_id; here the same row change through the maintenance pool).
	room1 := f.Room(t, f.Client1, "wp698-room-c1")
	room2 := f.Room(t, f.Client2, "wp698-room-c2")
	m := f.CI(t, f.OrgA, f.Client1, "wp698-moved")
	d := f.CI(t, f.OrgA, f.Client1, "wp698-stays")
	locate(m, f.Client1, room1)
	locate(d, f.Client1, room1)
	if w := patch(client1, room1, map[string]position{m: {X: 0.2, Y: 0.2}, d: {X: 0.8, Y: 0.8}}); w.Code != http.StatusOK {
		t.Fatalf("client 1 positions m and d: %d %s", w.Code, w.Body.String())
	}
	locate(m, f.Client2, room2)
	seen := read(client1, room1)
	if _, ok := seen[m]; ok {
		t.Errorf("client 1 GET room1 after the move still shows the client-2 CI m: %v", seen)
	}
	seen[d] = position{X: 0.4, Y: 0.4}
	if w := patch(client1, room1, seen); w.Code != http.StatusOK {
		t.Errorf("client 1 PATCH of what the room plan sends (server layout + moved d): %d %s, want 200", w.Code, w.Body.String())
	}
	if p := stored(room1)[d]; p.X != 0.4 {
		t.Errorf("position of d after the client-1 PATCH: %+v, want x=0.4", p)
	}
	all := read(orgWide, room1)
	if _, ok := all[m]; ok {
		t.Errorf("org-wide GET room1 shows m, which is no longer located in the room: %v", all)
	}
	if w := patch(orgWide, room1, all); w.Code != http.StatusOK {
		t.Fatalf("org-wide PATCH of room1: %d %s", w.Code, w.Body.String())
	}
	if _, ok := stored(room1)[m]; ok {
		t.Errorf("the org-wide PATCH kept the stale entry of m: %v", stored(room1))
	}
	if w := patch(orgWide, room1, map[string]position{d: {X: 0.4, Y: 0.4}, m: {X: 0.1, Y: 0.1}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("org-wide PATCH naming m, located in another room: %d %s, want 422", w.Code, w.Body.String())
	}

	// The variant over the API: the org-wide principal names a client-2 CI
	// (located in a client-2 room) in a client-1 room's layout. It is no CI
	// of that room: 422. Every site has a client and a CI's location has the
	// CI's client, so CIs of two clients never share a room. A layout stored
	// like this before (the old code accepted it) is seeded directly.
	shared := f.Room(t, f.Client1, "wp698-room-c1-legacy")
	c1 := f.CI(t, f.OrgA, f.Client1, "wp698-legacy-c1")
	c2 := f.CI(t, f.OrgA, f.Client2, "wp698-legacy-c2")
	locate(c1, f.Client1, shared)
	locate(c2, f.Client2, room2)
	if w := patch(orgWide, shared, map[string]position{c1: {X: 0.2, Y: 0.2}, c2: {X: 0.8, Y: 0.8}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("org-wide PATCH of a client-1 room naming a client-2 CI: %d %s, want 422", w.Code, w.Body.String())
	}
	legacy, _ := json.Marshal(map[string]position{c1: {X: 0.2, Y: 0.2}, c2: {X: 0.8, Y: 0.8}})
	if _, err := f.Admin.Exec(bg, `UPDATE room SET layout = $2::jsonb WHERE id = $1`, shared, string(legacy)); err != nil {
		t.Fatalf("seed legacy layout: %v", err)
	}
	if got := read(client1, shared); len(got) != 1 || got[c1] != (position{X: 0.2, Y: 0.2}) {
		t.Errorf("client 1 GET of its room: %v, want only its own c1", got)
	}
	if got := read(orgWide, shared); len(got) != 1 {
		t.Errorf("org-wide GET of the client-1 room: %v, want only c1 (c2 is located elsewhere)", got)
	}
	if w := call(client2, "GET", "/api/v1/rooms/"+shared, ""); w.Code != http.StatusNotFound {
		t.Errorf("client 2 GET of the client-1 room: %d, want 404", w.Code)
	}
	list := call(client1, "GET", "/api/v1/rooms?limit=100", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), c2) {
		t.Errorf("client 1 room list shows the client-2 CI: %d %s", list.Code, list.Body.String())
	}
	own := patch(client1, shared, map[string]position{c1: {X: 0.3, Y: 0.3}})
	if own.Code != http.StatusOK || strings.Contains(own.Body.String(), c2) {
		t.Errorf("client 1 PATCH {c1}: %d %s, want 200 without the client-2 entry", own.Code, own.Body.String())
	}
	if got := stored(shared); got[c2] != (position{X: 0.8, Y: 0.8}) || got[c1] != (position{X: 0.3, Y: 0.3}) {
		t.Errorf("layout after the client-1 PATCH {c1}: %v, want c1 moved and the client-2 position kept", got)
	}
	unknown := "c0de2100-0000-4000-8000-00000000fffe"
	foreign := patch(client1, shared, map[string]position{c1: {X: 0.3, Y: 0.3}, c2: {X: 0.5, Y: 0.5}})
	missing := patch(client1, shared, map[string]position{c1: {X: 0.3, Y: 0.3}, unknown: {X: 0.5, Y: 0.5}})
	if foreign.Code != http.StatusUnprocessableEntity || refusal(foreign, c2) != refusal(missing, unknown) {
		t.Errorf("client 1 PATCH with the client-2 key: %d %s; unknown key: %d %s; want the same 422",
			foreign.Code, foreign.Body.String(), missing.Code, missing.Body.String())
	}
	if got := stored(shared); got[c2] != (position{X: 0.8, Y: 0.8}) {
		t.Errorf("refused PATCH changed the client-2 position: %v", got)
	}

	// A deleted CI drops out of GET; the room plan's next PATCH works and
	// removes it.
	gone := f.CI(t, f.OrgA, f.Client1, "wp698-deleted")
	locate(gone, f.Client1, room1)
	if w := patch(client1, room1, map[string]position{d: {X: 0.4, Y: 0.4}, gone: {X: 0.6, Y: 0.6}}); w.Code != http.StatusOK {
		t.Fatalf("client 1 positions the CI to delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatalf("delete CI: %v", err)
	}
	seen = read(client1, room1)
	if _, ok := seen[gone]; ok {
		t.Errorf("client 1 GET shows the deleted CI: %v", seen)
	}
	seen[d] = position{X: 0.45, Y: 0.4}
	if w := patch(client1, room1, seen); w.Code != http.StatusOK {
		t.Errorf("client 1 PATCH after the deletion: %d %s", w.Code, w.Body.String())
	}
	if _, ok := stored(room1)[gone]; ok {
		t.Errorf("the deleted CI stays in the layout after the PATCH: %v", stored(room1))
	}
}
