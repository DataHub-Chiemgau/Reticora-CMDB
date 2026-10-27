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
//     visible to the caller (another client's CI, another organization's CI,
//     an unknown id, no UUID) with 422 naming layout.<ci>; nothing is stored;
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
