package api

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"
)

type newestRow struct {
	id string
	at time.Time
}

func newestKey(r *newestRow) (time.Time, string) { return r.at, r.id }

// TestNewestFirstPaging covers the paging of the lists that F-215 bounded
// (API-02): newest first on (created_at, id), at most limit rows per page,
// next_cursor after the last row while more follow, every row once also
// across rows of the same instant; offset pages; the SQL of the same order.
func TestNewestFirstPaging(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)
	var rows []newestRow
	for i := range 7 {
		rows = append(rows, newestRow{id: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), at: base.Add(time.Duration(i/2) * time.Second)})
	}
	want := slices.Clone(rows)
	slices.Reverse(want)

	var got []newestRow
	page := PaginationParams{Limit: 3}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging does not end")
		}
		read, total, err := NewestFirstSlice(rows, page, newestKey)
		if err != nil || total != 7 {
			t.Fatalf("slice: total %d, %v", total, err)
		}
		list := NewestFirstPage(read, total, page, newestKey)
		got = append(got, list.Data...)
		if len(list.Data) > 3 || list.HasMore != (list.NextCursor != "") {
			t.Fatalf("page %+v", list)
		}
		if list.NextCursor == "" {
			break
		}
		r := httptest.NewRequest("GET", "/x?limit=3&cursor="+url.QueryEscape(list.NextCursor), nil)
		w := httptest.NewRecorder()
		var ok bool
		if page, ok = ParseNewestFirstPage(w, r); !ok {
			t.Fatalf("own cursor refused: %d %s", w.Code, w.Body.String())
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("paged rows %v, want %v", got, want)
	}

	read, _, _ := NewestFirstSlice(rows, PaginationParams{Limit: 2, Offset: 5}, newestKey)
	if list := NewestFirstPage(read, 7, PaginationParams{Limit: 2, Offset: 5}, newestKey); !slices.Equal(list.Data, want[5:]) || list.HasMore {
		t.Errorf("offset page %+v, want the last two rows", list)
	}

	where, tail, args, err := NewestFirstSQL(PaginationParams{Limit: 50}, 2)
	if err != nil || where != "TRUE" || tail != "ORDER BY created_at DESC, id DESC LIMIT $2" || !slices.Equal(args, []any{51}) {
		t.Errorf("first page SQL %q %q %v %v", where, tail, args, err)
	}
	cursor := Cursor{Sort: "created_at", Dir: "desc", Value: base.Format(time.RFC3339Nano), ID: rows[0].id}
	where, tail, args, err = NewestFirstSQL(PaginationParams{Limit: 50, Offset: 9, Cursor: &cursor}, 2)
	if err != nil || where != "(created_at, id) < ($2::timestamptz, $3::uuid)" || tail != "ORDER BY created_at DESC, id DESC LIMIT $4" ||
		len(args) != 3 || args[1] != rows[0].id || args[2] != 51 {
		t.Errorf("keyset SQL %q %q %v %v", where, tail, args, err)
	}
	if _, _, _, err = NewestFirstSQL(PaginationParams{Limit: 50, Offset: 9}, 1); err != nil {
		t.Error(err)
	}

	for name, c := range map[string]Cursor{
		"other order":  {Sort: "name", Dir: "asc", Value: "a", ID: rows[0].id},
		"no timestamp": {Sort: "created_at", Dir: "desc", Value: "yesterday", ID: rows[0].id},
		"no uuid":      {Sort: "created_at", Dir: "desc", Value: cursor.Value, ID: "ci-1"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x?cursor="+url.QueryEscape(EncodeCursor(c)), nil)
		if _, ok := ParseNewestFirstPage(w, r); ok || w.Code != 400 {
			t.Errorf("%s: accepted (%d)", name, w.Code)
		}
		if _, _, _, err := NewestFirstSQL(PaginationParams{Limit: 1, Cursor: &c}, 1); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: SQL error %v, want ErrInvalidCursor", name, err)
		}
		if _, _, err := NewestFirstSlice(rows, PaginationParams{Limit: 1, Cursor: &c}, newestKey); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: slice error %v, want ErrInvalidCursor", name, err)
		}
	}
}
