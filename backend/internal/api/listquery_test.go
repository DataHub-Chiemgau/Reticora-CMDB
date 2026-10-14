package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

var testListFields = ListFields{
	Filterable: map[string]string{"name": KindText, "client_id": KindUUID, "created_at": KindTime, "rank": KindNumber, "is_manual": KindBool},
	Sortable:   []string{"created_at", "name"},
	Selectable: []string{"id", "name", "status"},
	Attributes: true,
}

func parse(t *testing.T, query string, spec ListFields) (ListQuery, error) {
	t.Helper()
	return ParseListQuery(httptest.NewRequest(http.MethodGet, "/x?"+query, nil), spec)
}

// TestParseListQuery covers the list query of API-02: the five operators,
// sort with whitelist (sort=-field and sort_by/sort_dir), fields and q.
func TestParseListQuery(t *testing.T) {
	q, err := parse(t, url.Values{
		"filter[name]":                   {"web01"},
		"filter[name][like]":             {"web*"},
		"filter[client_id][in]":          {"11111111-1111-4111-8111-111111111111, 22222222-2222-4222-8222-222222222222"},
		"filter[created_at][gte]":        {"2026-01-01"},
		"filter[rank][lte]":              {"10.5"},
		"filter[attributes.ram_gb][gte]": {"16"},
		"sort":                           {"-name"},
		"fields":                         {"name,status"},
		"q":                              {" core "},
	}.Encode(), testListFields)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]string{}
	for _, f := range q.Filters {
		ops[f.Field+"/"+f.Op] = f.Kind
	}
	want := map[string]string{"name/exact": KindText, "name/like": KindText, "client_id/in": KindUUID,
		"created_at/gte": KindTime, "rank/lte": KindNumber, "attributes.ram_gb/gte": ""}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("filters %v", ops)
	}
	for _, f := range q.Filters {
		if f.Op == OpIn && len(f.Values) != 2 {
			t.Errorf("in values %v", f.Values)
		}
	}
	if q.SortBy != "name" || q.SortDir != "desc" || !reflect.DeepEqual(q.Fields, []string{"name", "status"}) || q.Q != "core" {
		t.Errorf("sort %s %s, fields %v, q %q", q.SortBy, q.SortDir, q.Fields, q.Q)
	}
	if legacy, _ := parse(t, "sort_by=created_at&sort_dir=asc", testListFields); legacy.SortBy != "created_at" || legacy.SortDir != "asc" {
		t.Errorf("sort_by/sort_dir: %+v", legacy)
	}
}

// TestParseListQueryErrors: unknown fields are 422, unknown operators and
// malformed values 400.
func TestParseListQueryErrors(t *testing.T) {
	cases := map[string]int{
		"filter[nope]=x":                    http.StatusUnprocessableEntity,
		"sort=nope":                         http.StatusUnprocessableEntity,
		"sort=-status":                      http.StatusUnprocessableEntity,
		"fields=name,secret":                http.StatusUnprocessableEntity,
		"filter[name][between]=a":           http.StatusBadRequest,
		"filter[client_id]=not-a-uuid":      http.StatusBadRequest,
		"filter[created_at][gte]=yesterday": http.StatusBadRequest,
		"filter[rank][gte]=ten":             http.StatusBadRequest,
		"filter[client_id][like]=11*":       http.StatusBadRequest,
		"filter[is_manual][gte]=true":       http.StatusBadRequest,
		"filter[name][in]=":                 http.StatusBadRequest,
		"filter[name]]=x":                   http.StatusBadRequest,
		"sort_by=name&sort_dir=sideways":    http.StatusBadRequest,
	}
	for query, status := range cases {
		_, err := parse(t, query, testListFields)
		var fe *FieldError
		if !errors.As(err, &fe) {
			t.Errorf("%s: %v", query, err)
			continue
		}
		got := fe.Status
		if got == 0 {
			got = http.StatusBadRequest
		}
		if got != status {
			t.Errorf("%s: status %d, want %d (%v)", query, got, status, err)
		}
	}
	noAttrs := testListFields
	noAttrs.Attributes = false
	if _, err := parse(t, "filter[attributes.x]=1", noAttrs); err == nil {
		t.Error("attribute filter accepted on a list without attributes")
	}
}

// TestFilterSQLAndMatch: the SQL predicate uses placeholders only and the
// in-memory match has the same semantics.
func TestFilterSQLAndMatch(t *testing.T) {
	cases := []struct {
		f       Filter
		sql     string
		args    []any
		yes, no string
	}{
		{Filter{Field: "name", Op: OpExact, Kind: KindText, Values: []string{"Web01"}}, "name = $3::text", []any{"Web01"}, "web01", "web02"},
		{Filter{Field: "name", Op: OpLike, Kind: KindText, Values: []string{"web*"}}, "name ILIKE $3", []any{"web%"}, "WEB-01", "db-web"},
		{Filter{Field: "name", Op: OpLike, Kind: KindText, Values: []string{"50%_x"}}, "name ILIKE $3", []any{`%50\%\_x%`}, "a50%_xb", "a50abxb"},
		{Filter{Field: "rank", Op: OpGTE, Kind: KindNumber, Values: []string{"9"}}, "(rank)::numeric >= $3::numeric", []any{"9"}, "10", "8.5"},
		{Filter{Field: "at", Op: OpLTE, Kind: KindTime, Values: []string{"2026-01-01"}}, "(at)::timestamptz <= $3::timestamptz", []any{"2026-01-01"}, "2025-12-31T23:00:00Z", "2026-01-02T00:00:00Z"},
		{Filter{Field: "id", Op: OpIn, Kind: KindUUID, Values: []string{"a", "b"}}, "(id)::uuid = ANY($3::uuid[])", []any{[]string{"a", "b"}}, "B", "c"},
	}
	for _, c := range cases {
		sql, args := c.f.SQL(c.f.Field, 3)
		if sql != c.sql || !reflect.DeepEqual(args, c.args) {
			t.Errorf("%+v: %s %v", c.f, sql, args)
		}
		if !c.f.Match(c.yes) || c.f.Match(c.no) {
			t.Errorf("%+v: match %q/%q wrong", c.f, c.yes, c.no)
		}
	}
	if (Filter{Op: OpGTE, Kind: KindNumber, Values: []string{"1"}}).Match("") {
		t.Error("an empty value satisfied gte")
	}
}

// TestSelectFields: fields projects every item onto the selection plus id.
func TestSelectFields(t *testing.T) {
	type item struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	out, err := SelectFields([]item{{"1", "a", "s"}}, []string{"name"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0]) != 2 || string(out[0]["name"]) != `"a"` || string(out[0]["id"]) != `"1"` {
		t.Errorf("projection %v", out)
	}
}
