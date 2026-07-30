package api

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	original := Cursor{Sort: "created_at", Dir: "desc", Value: "2026-01-01T00:00:00Z", ID: "ci-1"}
	decoded, err := DecodeCursor(EncodeCursor(original))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != original {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", decoded, original)
	}
	if !decoded.Matches("created_at", "DESC") {
		t.Error("expected cursor to match its own sort order case-insensitively")
	}
	if decoded.Matches("name", "desc") {
		t.Error("expected cursor not to match a different sort column")
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for name, raw := range map[string]string{
		"not base64":   "!!!not-base64!!!",
		"not json":     "bm90LWpzb24",
		"missing id":   EncodeCursor(Cursor{Sort: "name", Dir: "asc", Value: "a"}),
		"empty string": " ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCursor(raw); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("expected ErrInvalidCursor, got %v", err)
			}
		})
	}
}

func TestParsePagination(t *testing.T) {
	valid := EncodeCursor(Cursor{Sort: "created_at", Dir: "desc", Value: "v", ID: "id"})

	tests := []struct {
		name        string
		url         string
		wantLimit   int
		wantOffset  int
		wantCursor  bool
		wantCursErr bool
	}{
		{name: "defaults", url: "/x", wantLimit: DefaultPageLimit},
		{name: "limit and offset", url: "/x?limit=10&offset=20", wantLimit: 10, wantOffset: 20},
		{name: "limit above max ignored", url: "/x?limit=1000", wantLimit: DefaultPageLimit},
		{name: "valid cursor", url: "/x?cursor=" + valid, wantLimit: DefaultPageLimit, wantCursor: true},
		{name: "invalid cursor", url: "/x?cursor=%21%21", wantLimit: DefaultPageLimit, wantCursErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := ParsePagination(httptest.NewRequest("GET", tc.url, nil))
			if p.Limit != tc.wantLimit {
				t.Errorf("limit: got %d, want %d", p.Limit, tc.wantLimit)
			}
			if p.Offset != tc.wantOffset {
				t.Errorf("offset: got %d, want %d", p.Offset, tc.wantOffset)
			}
			if (p.Cursor != nil) != tc.wantCursor {
				t.Errorf("cursor presence: got %v, want %v", p.Cursor != nil, tc.wantCursor)
			}
			if (p.CursorError != nil) != tc.wantCursErr {
				t.Errorf("cursor error: got %v, want error=%v", p.CursorError, tc.wantCursErr)
			}
		})
	}
}

func TestKeysetClause(t *testing.T) {
	if got := KeysetClause("created_at", "timestamptz", "desc", 3); got != "(created_at, id) < ($3::timestamptz, $4::uuid)" {
		t.Errorf("desc clause: %s", got)
	}
	if got := KeysetClause("name", "text", "asc", 1); got != "(name, id) > ($1::text, $2::uuid)" {
		t.Errorf("asc clause: %s", got)
	}
}

func TestKeysetCompare(t *testing.T) {
	cursor := Cursor{Value: "m", ID: "5"}

	tests := []struct {
		name  string
		value string
		id    string
		dir   string
		want  bool
	}{
		{name: "desc smaller value", value: "a", id: "9", dir: "desc", want: true},
		{name: "desc larger value", value: "z", id: "1", dir: "desc", want: false},
		{name: "desc tie breaks on id", value: "m", id: "4", dir: "desc", want: true},
		{name: "desc same row excluded", value: "m", id: "5", dir: "desc", want: false},
		{name: "asc larger value", value: "z", id: "1", dir: "asc", want: true},
		{name: "asc tie breaks on id", value: "m", id: "6", dir: "asc", want: true},
		{name: "asc same row excluded", value: "m", id: "5", dir: "asc", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := KeysetCompare(tc.value, tc.id, cursor, tc.dir); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
