package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Filter operators of API-02.
const (
	OpExact = "exact"
	OpGTE   = "gte"
	OpLTE   = "lte"
	OpLike  = "like"
	OpIn    = "in"
)

// Kinds of a filterable field: they decide how a value is checked and
// compared.
const (
	KindText   = "text"
	KindUUID   = "uuid"
	KindTime   = "time"
	KindNumber = "number"
	KindBool   = "bool"
)

// AttributePrefix addresses a JSONB attribute in filter and sort
// (filter[attributes.ram_gb][gte]=16).
const AttributePrefix = "attributes."

// maxFilterValues caps the values of one in-filter.
const maxFilterValues = 100

// Filter is one filter[field][op]=value condition.
type Filter struct {
	Field  string
	Op     string
	Values []string
	// Kind is the field kind; for attributes it is set by the repository
	// from the attribute definition.
	Kind string
}

// Attribute returns the attribute key of a filter on a JSONB attribute.
func (f Filter) Attribute() (string, bool) {
	key, ok := strings.CutPrefix(f.Field, AttributePrefix)
	return key, ok && key != ""
}

// ListQuery is the parsed list query of API-02: filters, sort, field
// selection and free text.
type ListQuery struct {
	Filters []Filter
	SortBy  string
	SortDir string
	Fields  []string
	Q       string
}

// ListFields declares what a list allows: the filterable fields with their
// kind, the sortable fields (the first one is the default sort) and the
// selectable response fields. Attributes says whether JSONB attributes may
// be filtered; whether a given attribute is searchable is decided by the
// repository against the attribute definitions.
type ListFields struct {
	Filterable map[string]string
	Sortable   []string
	Selectable []string
	Attributes bool
}

var filterKey = regexp.MustCompile(`^filter\[([a-zA-Z0-9_.]+)\](?:\[([a-z]+)\])?$`)

// ParseListQuery reads filter[field][op], sort (or sort_by/sort_dir),
// fields and q (API-02). An unknown field in filter, sort or fields is a
// *FieldError with status 422; an unknown operator or a malformed value is
// a *FieldError with status 400.
func ParseListQuery(r *http.Request, spec ListFields) (ListQuery, error) {
	query := r.URL.Query()
	var q ListQuery
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !strings.HasPrefix(key, "filter[") {
			continue
		}
		m := filterKey.FindStringSubmatch(key)
		if m == nil {
			return ListQuery{}, &FieldError{Field: key, Message: "malformed filter; use filter[field] or filter[field][op]"}
		}
		f, err := parseFilter(m[1], m[2], query[key], spec)
		if err != nil {
			return ListQuery{}, err
		}
		q.Filters = append(q.Filters, f)
	}

	sortBy, sortDir := query.Get("sort_by"), query.Get("sort_dir")
	if s := strings.TrimSpace(query.Get("sort")); s != "" {
		sortBy, sortDir = strings.TrimPrefix(s, "-"), "asc"
		if strings.HasPrefix(s, "-") {
			sortDir = "desc"
		}
	}
	if sortBy != "" && !slices.Contains(spec.Sortable, sortBy) {
		return ListQuery{}, &FieldError{Field: "sort", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("%q is not sortable; allowed: %s", sortBy, strings.Join(spec.Sortable, ", "))}
	}
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return ListQuery{}, &FieldError{Field: "sort_dir", Message: "must be asc or desc"}
	}
	q.SortBy, q.SortDir = sortBy, sortDir

	if raw := strings.TrimSpace(query.Get("fields")); raw != "" {
		for _, field := range strings.Split(raw, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			if !slices.Contains(spec.Selectable, field) {
				return ListQuery{}, &FieldError{Field: "fields", Status: http.StatusUnprocessableEntity,
					Message: fmt.Sprintf("unknown field %q", field)}
			}
			q.Fields = append(q.Fields, field)
		}
	}
	q.Q = strings.TrimSpace(query.Get("q"))
	return q, nil
}

func parseFilter(field, op string, raw []string, spec ListFields) (Filter, error) {
	if op == "" {
		op = OpExact
	}
	switch op {
	case OpExact, OpGTE, OpLTE, OpLike, OpIn:
	default:
		return Filter{}, &FieldError{Field: "filter[" + field + "]", Message: fmt.Sprintf("unknown operator %q; use exact, gte, lte, like or in", op)}
	}
	f := Filter{Field: field, Op: op}
	if _, ok := f.Attribute(); ok {
		if !spec.Attributes {
			return Filter{}, &FieldError{Field: "filter[" + field + "]", Status: http.StatusUnprocessableEntity, Message: "attributes cannot be filtered on this list"}
		}
	} else {
		kind, known := spec.Filterable[field]
		if !known {
			return Filter{}, &FieldError{Field: "filter[" + field + "]", Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("unknown filter field %q", field)}
		}
		f.Kind = kind
	}
	value := ""
	if len(raw) > 0 {
		value = raw[len(raw)-1]
	}
	if op == OpIn {
		for _, v := range strings.Split(value, ",") {
			if v = strings.TrimSpace(v); v != "" {
				f.Values = append(f.Values, v)
			}
		}
		if len(f.Values) == 0 || len(f.Values) > maxFilterValues {
			return Filter{}, &FieldError{Field: "filter[" + field + "][in]", Message: fmt.Sprintf("needs 1 to %d comma-separated values", maxFilterValues)}
		}
	} else {
		f.Values = []string{value}
	}
	if f.Kind != "" {
		if err := f.Check(); err != nil {
			return Filter{}, err
		}
	}
	return f, nil
}

var uuidValue = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Check validates the operator and values against the filter's kind:
// like only on text, gte/lte not on uuid or bool, and every value parses.
func (f Filter) Check() error {
	name := "filter[" + f.Field + "][" + f.Op + "]"
	if f.Op == OpLike && f.Kind != KindText {
		return &FieldError{Field: name, Message: "like is only allowed on text fields"}
	}
	if (f.Op == OpGTE || f.Op == OpLTE) && (f.Kind == KindUUID || f.Kind == KindBool) {
		return &FieldError{Field: name, Message: f.Op + " is not allowed on " + f.Kind + " fields"}
	}
	for _, v := range f.Values {
		if !validValue(f.Kind, v) {
			return &FieldError{Field: name, Message: fmt.Sprintf("%q is not a valid %s", v, f.Kind)}
		}
	}
	return nil
}

func validValue(kind, v string) bool {
	switch kind {
	case KindUUID:
		return uuidValue.MatchString(v)
	case KindNumber:
		_, err := strconv.ParseFloat(v, 64)
		return err == nil
	case KindBool:
		_, err := strconv.ParseBool(v)
		return err == nil
	case KindTime:
		_, ok := ParseFilterTime(v)
		return ok
	}
	return true
}

// ParseFilterTime accepts RFC 3339 timestamps and dates (YYYY-MM-DD).
func ParseFilterTime(v string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// SQL renders the filter as a predicate on expr (a column or a JSONB
// attribute expression) with placeholders from argPos on. expr and the cast
// come from the repository's whitelist, never from the request.
func (f Filter) SQL(expr string, argPos int) (clause string, args []any) {
	cast := sqlCast(f.Kind)
	lhs := expr
	if cast != "" {
		lhs = "(" + expr + ")::" + cast
	}
	switch f.Op {
	case OpGTE:
		return fmt.Sprintf("%s >= $%d::%s", lhs, argPos, castOr(cast)), []any{f.Values[0]}
	case OpLTE:
		return fmt.Sprintf("%s <= $%d::%s", lhs, argPos, castOr(cast)), []any{f.Values[0]}
	case OpLike:
		return fmt.Sprintf("%s ILIKE $%d", expr, argPos), []any{LikePattern(f.Values[0])}
	case OpIn:
		return fmt.Sprintf("%s = ANY($%d::%s[])", lhs, argPos, castOr(cast)), []any{f.Values}
	}
	return fmt.Sprintf("%s = $%d::%s", lhs, argPos, castOr(cast)), []any{f.Values[0]}
}

func sqlCast(kind string) string {
	switch kind {
	case KindNumber:
		return "numeric"
	case KindTime:
		return "timestamptz"
	case KindBool:
		return "boolean"
	case KindUUID:
		return "uuid"
	}
	return ""
}

func castOr(cast string) string {
	if cast == "" {
		return "text"
	}
	return cast
}

// LikePattern turns a like value into an ILIKE pattern: * is the wildcard;
// without one the value matches as a substring. % and _ match literally.
func LikePattern(v string) string {
	v = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v)
	if strings.Contains(v, "*") {
		return strings.ReplaceAll(v, "*", "%")
	}
	return "%" + v + "%"
}

// Match evaluates the filter against a value in memory (the memory
// repositories), with the same semantics as SQL.
func (f Filter) Match(value string) bool {
	switch f.Op {
	case OpIn:
		for _, v := range f.Values {
			if compare(f.Kind, value, v) == 0 {
				return true
			}
		}
		return false
	case OpLike:
		return likeMatch(LikePattern(f.Values[0]), value)
	case OpGTE:
		return value != "" && compare(f.Kind, value, f.Values[0]) >= 0
	case OpLTE:
		return value != "" && compare(f.Kind, value, f.Values[0]) <= 0
	}
	return compare(f.Kind, value, f.Values[0]) == 0
}

func compare(kind, a, b string) int {
	switch kind {
	case KindNumber:
		x, errA := strconv.ParseFloat(a, 64)
		y, errB := strconv.ParseFloat(b, 64)
		if errA == nil && errB == nil {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		}
	case KindTime:
		x, okA := ParseFilterTime(a)
		y, okB := ParseFilterTime(b)
		if okA && okB {
			return x.Compare(y)
		}
	case KindUUID, KindText, KindBool:
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	}
	return strings.Compare(a, b)
}

func likeMatch(pattern, value string) bool {
	var re strings.Builder
	re.WriteString("(?is)^")
	escaped := false
	for _, r := range pattern {
		switch {
		case escaped:
			re.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
		case r == '\\':
			escaped = true
		case r == '%':
			re.WriteString(".*")
		case r == '_':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString(re.String(), value)
	return ok
}

// SelectFields projects items onto the selected JSON fields (API-02); id
// is always kept.
func SelectFields[T any](items []T, fields []string) ([]map[string]json.RawMessage, error) {
	keep := map[string]bool{"id": true}
	for _, f := range fields {
		keep[f] = true
	}
	out := make([]map[string]json.RawMessage, len(items))
	for i := range items {
		raw, err := json.Marshal(items[i])
		if err != nil {
			return nil, err
		}
		var all map[string]json.RawMessage
		if uerr := json.Unmarshal(raw, &all); uerr != nil {
			return nil, uerr
		}
		out[i] = map[string]json.RawMessage{}
		for k, v := range all {
			if keep[k] {
				out[i][k] = v
			}
		}
	}
	return out, nil
}

// FilterQueryString renders filters back into query parameters (tests and
// links).
func FilterQueryString(filters []Filter) string {
	v := url.Values{}
	for _, f := range filters {
		v.Set("filter["+f.Field+"]["+f.Op+"]", strings.Join(f.Values, ","))
	}
	return v.Encode()
}
