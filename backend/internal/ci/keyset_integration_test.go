package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestCIKeysetPaginationAgainstPostgres covers the keyset pagination of
// API-02 (WP-466) on GET /cis against PostgreSQL as the application role:
// 250 CIs, five of each with the same created_at, read in pages of the
// default 50 through next_cursor give 250 distinct ids in the order
// created_at DESC, id DESC; a CI inserted before the read position while
// paging neither shifts nor repeats a row, one inserted behind it is read
// once; a manipulated cursor is 400.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIKeysetPaginationAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ae")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	insert := func(from, to int, createdAt string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `
			INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, created_at)
			SELECT gen_random_uuid(), $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1),
			       'ks-' || lpad(g::text, 3, '0'), 'active', `+createdAt+`
			  FROM generate_series($3::int, $4::int) AS g`, f.OrgA, f.Client1, from, to); err != nil {
			t.Fatalf("insert cis: %v", err)
		}
	}
	// Groups of five share created_at, so the id decides within a group.
	insert(1, 250, `timestamptz '2026-01-01 00:00:00+00' + (g / 5) * interval '1 second'`)
	var want []string
	rows, err := f.Admin.Query(bg, `SELECT id::text FROM ci WHERE organization_id = $1 ORDER BY created_at DESC, id DESC`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}
	rows.Close()
	if len(want) != 250 {
		t.Fatalf("seeded %d CIs, want 250", len(want))
	}

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	get := func(target string) (int, string, []string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			NextCursor string `json:"next_cursor"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		ids := make([]string, 0, len(page.Data))
		for _, item := range page.Data {
			ids = append(ids, item.ID)
		}
		return w.Code, page.NextCursor, ids
	}

	var got []string
	seen := map[string]bool{}
	firstCursor := ""
	for page, cursor := 1, ""; ; page++ {
		target := "/api/v1/cis"
		if cursor != "" {
			target += "?cursor=" + url.QueryEscape(cursor)
		}
		code, next, ids := get(target)
		if code != http.StatusOK {
			t.Fatalf("page %d: status %d", page, code)
		}
		if next != "" && len(ids) != 50 {
			t.Fatalf("page %d: %d rows with a next_cursor, want the default limit 50", page, len(ids))
		}
		for _, id := range ids {
			if seen[id] {
				t.Errorf("page %d: id %s returned twice", page, id)
			}
			seen[id] = true
			got = append(got, id)
		}
		if page == 1 {
			firstCursor = next
			// While the client pages: a CI newer than every read row sorts
			// before the read position (an offset would repeat the last row
			// of page 1), one older than every row sorts behind it.
			insert(998, 998, "now()")
			insert(999, 999, `timestamptz '2025-01-01 00:00:00+00'`)
		}
		if next == "" {
			break
		}
		if page > 10 {
			t.Fatal("more than 10 pages for 252 CIs; next_cursor does not advance")
		}
		cursor = next
	}
	var late []string
	originals := got[:0:0]
	for _, id := range got {
		if slices.Contains(want, id) {
			originals = append(originals, id)
		} else {
			late = append(late, id)
		}
	}
	if !slices.Equal(originals, want) {
		t.Errorf("read %d of the 250 CIs; want all of them once in the order created_at DESC, id DESC", len(originals))
	}
	if len(late) != 1 {
		t.Errorf("read %d CIs inserted while paging, want exactly the one behind the read position", len(late))
	}

	// A manipulated cursor is refused: the signature of another payload, a
	// changed signature, or no signature.
	if firstCursor == "" {
		t.Fatal("page 1 has no next_cursor")
	}
	body, sig, _ := strings.Cut(firstCursor, ".")
	_, otherSig, _ := strings.Cut(ci.NextCursor([]ci.Item{{ID: want[0], CreatedAt: "2026-01-01T00:00:00Z"}}, ci.FilterParams{}, 1), ".")
	flipped := []byte(sig)
	flipped[0] ^= 1
	for _, cursor := range []string{body + "." + otherSig, body + "." + string(flipped), body} {
		if code, _, _ := get("/api/v1/cis?cursor=" + url.QueryEscape(cursor)); code != http.StatusBadRequest {
			t.Errorf("manipulated cursor %q: status %d, want 400", cursor, code)
		}
	}
}
