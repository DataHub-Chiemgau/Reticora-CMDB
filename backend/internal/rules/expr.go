package rules

// Expressions (WP-258, MET-10 computed, MET-45): the AST of $defs/expr in
// api/rules.schema.json. An expression yields a value from the values of a
// record instead of checking one: a field reference (an attribute of the
// record or a whitelisted field of the asset linked through ci.asset_id),
// a literal, arithmetic, dates, strings and coalesce. A computed attribute
// definition carries one (attribute_definition.computed); the write path
// evaluates it (WP-717).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Operators of the expression AST.
const (
	ExprField       = "field"
	ExprLiteral     = "literal"
	ExprAdd         = "add"
	ExprSub         = "sub"
	ExprMul         = "mul"
	ExprDiv         = "div"
	ExprToday       = "today"
	ExprDaysBetween = "days_between"
	ExprConcat      = "concat"
	ExprLower       = "lower"
	ExprUpper       = "upper"
	ExprCoalesce    = "coalesce"
)

// ExprOps lists every operator of the expression AST; they are separate
// from the operators of value rules and conditions (Ops).
var ExprOps = []string{
	ExprField, ExprLiteral, ExprAdd, ExprSub, ExprMul, ExprDiv, ExprToday, ExprDaysBetween,
	ExprConcat, ExprLower, ExprUpper, ExprCoalesce,
}

// Result types of an expression (Expr.ResultType).
const (
	ResultNumber  = "number"
	ResultString  = "string"
	ResultDate    = "date"
	ResultBoolean = "boolean"
	// ResultAny: the type is known only at evaluation, e.g. a reference to
	// an attribute of the record.
	ResultAny = "any"
)

// AssetPrefix starts a reference to a field of the asset linked through
// ci.asset_id.
const AssetPrefix = "asset."

// AssetFields is the whitelist of asset fields an expression can read, as
// asset.<field>.
var AssetFields = []string{"warranty_end", "purchase_date", "purchase_cost", "asset_tag", "serial_number", "status"}

// assetFieldTypes are the result types of the asset fields.
var assetFieldTypes = map[string]string{
	"warranty_end": ResultDate, "purchase_date": ResultDate, "purchase_cost": ResultNumber,
	"asset_tag": ResultString, "serial_number": ResultString, "status": ResultString,
}

// maxExprDepth bounds the nesting of an expression.
const maxExprDepth = 32

// Expr is one node of the expression AST.
type Expr struct {
	Op string
	// Field is the reference of field: an attribute key or asset.<field>.
	Field string
	// Value is the string, number or boolean of literal.
	Value any
	// Args are the operands of the other operators.
	Args []Expr
}

// MarshalJSON writes the expression in the form of $defs/expr: field with
// its field, literal with its value (also 0, false and ""), today alone,
// every other operator with its args.
func (e Expr) MarshalJSON() ([]byte, error) { //nolint:gocritic // value receiver: json.Marshal of values and pointers
	switch e.Op {
	case ExprField:
		return json.Marshal(struct {
			Op    string `json:"op"`
			Field string `json:"field"`
		}{e.Op, e.Field})
	case ExprLiteral:
		return json.Marshal(struct {
			Op    string `json:"op"`
			Value any    `json:"value"`
		}{e.Op, e.Value})
	case ExprToday:
		return json.Marshal(struct {
			Op string `json:"op"`
		}{e.Op})
	}
	args := e.Args
	if args == nil {
		args = []Expr{}
	}
	return json.Marshal(struct {
		Op   string `json:"op"`
		Args []Expr `json:"args"`
	}{e.Op, args})
}

// UnmarshalJSON decodes an expression strictly (ParseExpr).
func (e *Expr) UnmarshalJSON(raw []byte) error {
	parsed, err := ParseExpr(raw)
	if err != nil {
		return err
	}
	*e = parsed
	return nil
}

// exprKeys are the properties each operator allows.
var exprKeys = func() map[string][]string {
	keys := map[string][]string{
		ExprField:   {"op", "field"},
		ExprLiteral: {"op", "value"},
		ExprToday:   {"op"},
	}
	for _, op := range ExprOps {
		if _, ok := keys[op]; !ok {
			keys[op] = []string{"op", "args"}
		}
	}
	return keys
}()

// arity is the number of operands of an operator: exactly min when max is
// min, otherwise at least min.
var arity = map[string][2]int{
	ExprAdd: {2, -1}, ExprMul: {2, -1}, ExprSub: {2, 2}, ExprDiv: {2, 2},
	ExprDaysBetween: {2, 2}, ExprConcat: {1, -1}, ExprLower: {1, 1}, ExprUpper: {1, 1},
	ExprCoalesce: {1, -1},
}

// ParseExpr decodes and validates an expression strictly: unknown
// operators and properties, a wrong number of operands, a literal that is
// no string, number or boolean, an asset field outside AssetFields and
// operands of the wrong type (e.g. text in add) are errors; the message
// starts with the path of the failing property, e.g. args[1].op.
// validation: fixed texts naming the path and the operator that fail.
func ParseExpr(raw []byte) (Expr, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return Expr{}, fmt.Errorf("expression: %w", err)
	}
	e, err := parseExpr(v, "", 0)
	if err != nil {
		return Expr{}, err
	}
	if _, err := e.resultType(""); err != nil {
		return Expr{}, err
	}
	return e, nil
}

// parseExpr checks one node of an expression below path.
// validation: fixed texts naming the path and the operator that fail.
func parseExpr(v any, path string, depth int) (Expr, error) {
	where := path
	if where == "" {
		where = "expression"
	}
	if depth > maxExprDepth {
		return Expr{}, fmt.Errorf("%s: the expression is nested deeper than %d", where, maxExprDepth)
	}
	node, ok := v.(map[string]any)
	if !ok {
		return Expr{}, fmt.Errorf("%s: an expression is an object", where)
	}
	op, _ := node["op"].(string)
	keys, known := exprKeys[op]
	if !known {
		return Expr{}, fmt.Errorf("%s: unknown operator %q", at(path, "op"), fmt.Sprint(node["op"]))
	}
	props := make([]string, 0, len(node))
	for k := range node {
		props = append(props, k)
	}
	sort.Strings(props)
	for _, k := range props {
		if !slices.Contains(keys, k) {
			return Expr{}, fmt.Errorf("%s: unknown property of %s", at(path, k), op)
		}
	}
	switch op {
	case ExprField:
		field, isText := node["field"].(string)
		if !isText || strings.TrimSpace(field) == "" || field != strings.TrimSpace(field) {
			return Expr{}, fmt.Errorf("%s: field requires the key of an attribute or asset.<field>", at(path, "field"))
		}
		if name, isAsset := strings.CutPrefix(field, AssetPrefix); isAsset && !slices.Contains(AssetFields, name) {
			return Expr{}, fmt.Errorf("%s: %s is no asset field of an expression (%s)", at(path, "field"), field,
				strings.Join(AssetFields, ", "))
		}
		return Expr{Op: op, Field: field}, nil
	case ExprLiteral:
		value, has := node["value"]
		if !has || !scalar(value) {
			return Expr{}, fmt.Errorf("%s: literal requires a string, number or boolean", at(path, "value"))
		}
		return Expr{Op: op, Value: value}, nil
	case ExprToday:
		return Expr{Op: op}, nil
	}
	list, ok := node["args"].([]any)
	if !ok {
		return Expr{}, fmt.Errorf("%s: %s requires an array of expressions", at(path, "args"), op)
	}
	n := arity[op]
	switch {
	case n[1] == n[0] && len(list) != n[0]:
		return Expr{}, fmt.Errorf("%s: %s takes exactly %d expressions", at(path, "args"), op, n[0])
	case len(list) < n[0]:
		return Expr{}, fmt.Errorf("%s: %s takes at least %d expressions", at(path, "args"), op, n[0])
	}
	e := Expr{Op: op, Args: make([]Expr, 0, len(list))}
	for i, child := range list {
		arg, err := parseExpr(child, at(path, fmt.Sprintf("args[%d]", i)), depth+1)
		if err != nil {
			return Expr{}, err
		}
		e.Args = append(e.Args, arg)
	}
	return e, nil
}

// ResultType is the type of the value the expression yields: number,
// string, date, boolean or any (a reference to an attribute of the
// record). The expression is assumed valid (ParseExpr).
func (e *Expr) ResultType() string {
	t, _ := e.resultType("")
	return t
}

// resultType checks the operand types below path and returns the type of
// e.
// validation: fixed texts naming the path, the operator and the types.
func (e *Expr) resultType(path string) (string, error) {
	switch e.Op {
	case ExprField:
		if name, ok := strings.CutPrefix(e.Field, AssetPrefix); ok {
			return assetFieldTypes[name], nil
		}
		return ResultAny, nil
	case ExprLiteral:
		switch e.Value.(type) {
		case string:
			return ResultString, nil
		case bool:
			return ResultBoolean, nil
		}
		return ResultNumber, nil
	case ExprToday:
		return ResultDate, nil
	}
	types := make([]string, len(e.Args))
	for i := range e.Args {
		t, err := e.Args[i].resultType(at(path, fmt.Sprintf("args[%d]", i)))
		if err != nil {
			return "", err
		}
		types[i] = t
	}
	accept := func(result string, allowed ...string) (string, error) {
		for i, t := range types {
			if t != ResultAny && !slices.Contains(allowed, t) {
				return "", fmt.Errorf("%s: %s takes no %s", at(path, fmt.Sprintf("args[%d]", i)), e.Op, t)
			}
		}
		return result, nil
	}
	switch e.Op {
	case ExprAdd, ExprSub, ExprMul, ExprDiv:
		return accept(ResultNumber, ResultNumber)
	case ExprDaysBetween:
		return accept(ResultNumber, ResultDate, ResultString)
	case ExprLower, ExprUpper:
		return accept(ResultString, ResultString)
	case ExprConcat:
		return ResultString, nil
	}
	// coalesce: the common type of its operands.
	result := ResultAny
	for i, t := range types {
		switch {
		case t == ResultAny || t == result:
		case result == ResultAny:
			result = t
		default:
			return "", fmt.Errorf("%s: coalesce mixes %s and %s", at(path, fmt.Sprintf("args[%d]", i)), result, t)
		}
	}
	return result, nil
}

// Fields returns the references of the expression, sorted and each once:
// attribute keys and asset.<field>.
func (e *Expr) Fields() []string {
	var out []string
	e.walk(func(n *Expr) {
		if n.Op == ExprField && !slices.Contains(out, n.Field) {
			out = append(out, n.Field)
		}
	})
	sort.Strings(out)
	return out
}

// UsesToday reports whether the expression depends on the current date.
func (e *Expr) UsesToday() bool {
	uses := false
	e.walk(func(n *Expr) { uses = uses || n.Op == ExprToday })
	return uses
}

func (e *Expr) walk(visit func(*Expr)) {
	visit(e)
	for i := range e.Args {
		e.Args[i].walk(visit)
	}
}

// Equal reports whether two expressions are the same AST.
func (e *Expr) Equal(o *Expr) bool {
	a, errA := json.Marshal(e)
	b, errB := json.Marshal(o)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// EvalExpr evaluates the expression for the values of a record: inputs
// holds the attributes by key and the asset fields as asset.<field>; now
// and loc give today's date (loc nil: UTC). It returns nil for an empty
// value: a missing input (absent, null or blank text), a value of the
// wrong kind, a date that cannot be read or a division by 0 make the
// expression empty, except within coalesce, which takes its first
// non-empty operand. Numbers are float64, dates text YYYY-MM-DD. The
// expression is assumed valid (ParseExpr).
func EvalExpr(e *Expr, inputs map[string]any, now time.Time, loc *time.Location) any {
	if loc == nil {
		loc = time.UTC
	}
	return evalNode(e, inputs, now, loc)
}

func evalNode(e *Expr, inputs map[string]any, now time.Time, loc *time.Location) any {
	if e.Op == ExprCoalesce {
		for i := range e.Args {
			if v := evalNode(&e.Args[i], inputs, now, loc); v != nil {
				return v
			}
		}
		return nil
	}
	var v any
	switch e.Op {
	case ExprField:
		v = inputs[e.Field]
	case ExprLiteral:
		v = e.Value
	case ExprToday:
		v = now.In(loc).Format(time.DateOnly)
	default:
		args := make([]any, len(e.Args))
		for i := range e.Args {
			if args[i] = evalNode(&e.Args[i], inputs, now, loc); args[i] == nil {
				return nil
			}
		}
		v = apply(e.Op, args, loc)
	}
	if blank(v) {
		return nil
	}
	return v
}

// apply computes an operator from non-empty operands; nil is empty.
func apply(op string, args []any, loc *time.Location) any {
	switch op {
	case ExprAdd, ExprSub, ExprMul, ExprDiv:
		return arithmetic(op, args)
	case ExprDaysBetween:
		from, ok := civilDate(args[0], loc)
		if !ok {
			return nil
		}
		to, ok := civilDate(args[1], loc)
		if !ok {
			return nil
		}
		return math.Round(to.Sub(from).Hours() / 24)
	case ExprConcat:
		var b strings.Builder
		for _, a := range args {
			s, ok := text(a)
			if !ok {
				return nil
			}
			b.WriteString(s)
		}
		return b.String()
	case ExprLower, ExprUpper:
		s, ok := text(args[0])
		if !ok {
			return nil
		}
		if op == ExprLower {
			return strings.ToLower(s)
		}
		return strings.ToUpper(s)
	}
	return nil
}

func arithmetic(op string, args []any) any {
	nums := make([]float64, len(args))
	for i, a := range args {
		n, ok := looseNumber(a)
		if !ok {
			return nil
		}
		nums[i] = n
	}
	r := nums[0]
	for _, n := range nums[1:] {
		switch op {
		case ExprAdd:
			r += n
		case ExprSub:
			r -= n
		case ExprMul:
			r *= n
		case ExprDiv:
			if n == 0 {
				return nil
			}
			r /= n
		}
	}
	if math.IsInf(r, 0) || math.IsNaN(r) {
		return nil
	}
	return r
}

// errNoDate: a value that is no date.
var errNoDate = errors.New("no date")

// civilDate reads a date as midnight UTC of its calendar day: text
// YYYY-MM-DD as that day, an RFC 3339 timestamp (text or time.Time) with
// its date in loc.
func civilDate(v any, loc *time.Location) (time.Time, bool) {
	t, err := dateOf(v, loc)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true
}

func dateOf(v any, loc *time.Location) (time.Time, error) {
	switch d := v.(type) {
	case time.Time:
		return d.In(loc), nil
	case string:
		s := strings.TrimSpace(d)
		if t, err := time.Parse(time.DateOnly, s); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.In(loc), nil
		}
	}
	return time.Time{}, errNoDate
}
