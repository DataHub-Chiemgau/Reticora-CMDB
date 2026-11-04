package importer

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Value is the mapped value of a target: a text, or a list after split.
type Value struct {
	Text   string
	List   []string
	IsList bool
}

// Empty reports whether the value carries nothing: an empty text or an
// empty list.
func (v Value) Empty() bool {
	if v.IsList {
		return len(v.List) == 0
	}
	return v.Text == ""
}

// MappedRow is a data row after the mapping of the profile: the line of
// the file and the value per target (core field, attributes.<key> or
// relations.<type>).
type MappedRow struct {
	Line   int
	Values map[string]Value
}

// Text returns the text of a target; a list is joined with commas. Empty
// when the row has no value for it.
func (r *MappedRow) Text(target string) string {
	v, ok := r.Values[target]
	if !ok {
		return ""
	}
	if v.IsList {
		return strings.Join(v.List, ",")
	}
	return v.Text
}

// mapper applies the mapping of a profile to the rows of one file: the
// column index of the header is built once.
type mapper struct {
	mapping *Mapping
	// columns are the mapped columns sorted by name, with their cell
	// index (-1: not in the header).
	columns []mappedColumn
	// defaults are the targets with a default, sorted.
	defaults []string
}

type mappedColumn struct {
	name  string
	index int
	cm    ColumnMapping
}

// newMapper resolves the mapped columns against the header: an exact
// match first, then one that differs only in case and surrounding space.
// It returns the issues of columns that are not in the header.
func newMapper(m *Mapping, header []string) (*mapper, []Issue) {
	exact := map[string]int{}
	folded := map[string]int{}
	for i, h := range header {
		if _, dup := exact[h]; !dup {
			exact[h] = i
		}
		key := strings.ToLower(strings.TrimSpace(h))
		if _, dup := folded[key]; !dup {
			folded[key] = i
		}
	}
	mp := &mapper{mapping: m}
	var issues []Issue
	for name, cm := range m.Columns {
		idx, ok := exact[name]
		if !ok {
			idx, ok = folded[strings.ToLower(strings.TrimSpace(name))]
		}
		if !ok {
			idx = -1
			issues = append(issues, Issue{Field: name, Code: CodeColumnMissing,
				Message: fmt.Sprintf("the column %q of the mapping is not in the header of the file", name)})
		}
		mp.columns = append(mp.columns, mappedColumn{name: name, index: idx, cm: cm})
	}
	sort.Slice(mp.columns, func(i, j int) bool { return mp.columns[i].name < mp.columns[j].name })
	sort.Slice(issues, func(i, j int) bool { return issues[i].Field < issues[j].Field })
	for target := range m.Defaults {
		mp.defaults = append(mp.defaults, target)
	}
	sort.Strings(mp.defaults)
	return mp, issues
}

// apply maps one row: per column the cell value through its
// transformations onto the target, then the defaults for every target
// that is still empty. A failed transformation is an issue with code
// transform_failed and the target as field; the row is then not written.
func (mp *mapper) apply(row *Row) (*MappedRow, []Issue) {
	out := &MappedRow{Line: row.Line, Values: map[string]Value{}}
	var issues []Issue
	for i := range mp.columns {
		col := &mp.columns[i]
		cell := ""
		if col.index >= 0 && col.index < len(row.Cells) {
			cell = row.Cells[col.index]
		}
		v, err := transform(cell, col.cm.Transforms)
		if err != nil {
			issues = append(issues, Issue{Line: row.Line, Field: col.cm.Target, Code: CodeTransformFailed,
				Message: fmt.Sprintf("column %s: %v", col.name, err)})
			continue
		}
		// Two columns of one target: the first non-empty value wins.
		if prev, ok := out.Values[col.cm.Target]; ok && !prev.Empty() {
			continue
		}
		out.Values[col.cm.Target] = v
	}
	for _, target := range mp.defaults {
		if v, ok := out.Values[target]; !ok || v.Empty() {
			out.Values[target] = Value{Text: mp.mapping.Defaults[target]}
		}
	}
	return out, issues
}

// transform applies the transformations of a column in order. trim,
// date_format and enum_map apply to every element of a list; split turns
// the text into a list (empty elements dropped); constant replaces the
// value. An empty value passes date_format and enum_map unchanged, so a
// default can fill it.
func transform(cell string, steps []Transform) (Value, error) {
	v := Value{Text: cell}
	for i := range steps {
		t := &steps[i]
		var err error
		switch t.Op {
		case TransformTrim:
			v = v.each(func(s string) (string, error) { return strings.TrimSpace(s), nil })
		case TransformDateFormat:
			v, err = v.eachErr(func(s string) (string, error) { return formatDate(s, t.Layout) })
		case TransformEnumMap:
			v, err = v.eachErr(func(s string) (string, error) { return mapEnum(s, t.Values) })
		case TransformSplit:
			v = split(v, t.Separator)
		case TransformConstant:
			if t.Value != nil {
				v = Value{Text: *t.Value}
			}
		default:
			err = fmt.Errorf("unknown transformation %q", t.Op)
		}
		if err != nil {
			return Value{}, err
		}
	}
	return v, nil
}

func (v Value) each(fn func(string) (string, error)) Value {
	out, _ := v.eachErr(fn)
	return out
}

func (v Value) eachErr(fn func(string) (string, error)) (Value, error) {
	if !v.IsList {
		s, err := fn(v.Text)
		return Value{Text: s}, err
	}
	list := make([]string, 0, len(v.List))
	for _, item := range v.List {
		s, err := fn(item)
		if err != nil {
			return Value{}, err
		}
		list = append(list, s)
	}
	return Value{List: list, IsList: true}, nil
}

// split splits the text (or every element of a list) at sep.
func split(v Value, sep string) Value {
	items := v.List
	if !v.IsList {
		items = []string{v.Text}
	}
	list := []string{}
	for _, item := range items {
		for _, part := range strings.Split(item, sep) {
			if part != "" {
				list = append(list, part)
			}
		}
	}
	return Value{List: list, IsList: true}
}

// mapEnum maps a value through the table of enum_map: an exact key first,
// then one that differs only in case. A value without key fails.
func mapEnum(s string, values map[string]string) (string, error) {
	if s == "" {
		return s, nil
	}
	if out, ok := values[s]; ok {
		return out, nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(k, s) {
			return values[k], nil
		}
	}
	return "", fmt.Errorf("the value %q has no entry in enum_map", s)
}

// dateTokens translate the token form of a date layout (DD.MM.YYYY,
// YYYY-MM-DD HH:mm:ss) into the reference form of Go; longer tokens come
// first.
var dateTokens = strings.NewReplacer(
	"YYYY", "2006", "YY", "06", "MM", "01", "DD", "02", "HH", "15", "hh", "15", "mm", "04", "ss", "05",
	"M", "1", "D", "2",
)

// goLayout returns the Go layout of a date_format layout: the token form
// when it names a year or day token (Y, D), else the layout as Go
// reference form (02.01.2006).
func goLayout(layout string) string {
	if strings.ContainsAny(layout, "YD") {
		return dateTokens.Replace(layout)
	}
	return layout
}

// formatDate parses s with layout and returns the date in ISO 8601
// (2006-01-02), with a time of day RFC 3339 in UTC.
func formatDate(s, layout string) (string, error) {
	if s == "" {
		return s, nil
	}
	gl := goLayout(layout)
	t, err := time.Parse(gl, strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("the value %q does not match the date layout %q", s, layout)
	}
	if strings.Contains(gl, "15") || strings.Contains(gl, "04") {
		return t.UTC().Format(time.RFC3339), nil
	}
	return t.Format("2006-01-02"), nil
}
