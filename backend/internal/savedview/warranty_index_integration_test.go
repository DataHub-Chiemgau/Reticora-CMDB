package savedview

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestWarrantyDaysLeftIndexKeepsResults compares the filter on the computed
// attribute warranty_days_left with the index condition on the stored
// column ci.warranty_days_left_num (numberColumns, 000274, finding of
// WP-824) against the filter without it (the comparison before the index)
// over boundary values: the hits are the same for every operator, also
// under not and in a related node.
//
// The CIs carry no value, JSON null, a string, a boolean, negative and
// positive integers, fractions, values beyond the range of double
// precision (1e400, -1e400), values below it (1e-400, -1e-400), integers
// beyond 2^53 and a value within rounding of 30; the operands cover the
// same values. The generated column never raises on writing them.
func TestWarrantyDaysLeftIndexKeepsResults(t *testing.T) {
	f := scopetest.Seed(t, "bc")
	bg := context.Background()
	stored := []string{"", "null", `"5"`, `"abc"`, "true", "-100000", "-5", "-1", "-0", "0", "0.5", "1", "5", "29", "30",
		"30.0000000000000001", "30.5", "31", "365", "9007199254740992", "9007199254740993", "1e300", "1e308", "1.7976931348623157e308",
		"1e400", "-1e308", "-1e400", "1e-301", "1e-400", "-1e-400"}
	want := map[string]string{} // CI id → stored value, for messages
	for i, v := range stored {
		id := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("wdl-%02d", i))
		attrs := "{}"
		if v != "" {
			attrs = `{"warranty_days_left": ` + v + `}`
		}
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, id, attrs); err != nil {
			t.Fatalf("CI with %s: %v", attrs, err)
		}
		want[id] = attrs
	}

	// run selects the ids with the WHERE of PGQueryEngine.Query as the
	// application role (the rows of Query decode attributes as float64,
	// which 1e400 exceeds); the engine itself runs a case at the end.
	ctx := f.OrgCtx(f.OrgA)
	run := func(where *Expr) ([]string, error) {
		var out []string
		err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			spec := &FilterSpec{EntityKind: "ci", Where: where}
			cat, err := resolveCatalog(ctx, tx, spec)
			if err != nil {
				return err
			}
			sql, args, err := spec.compile("ci", cat, []any{f.OrgA}, 2)
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "SELECT ci.id::text FROM ci WHERE ci.deleted_at IS NULL AND ci.organization_id = $1 AND "+sql+
				" ORDER BY ci.id::text", args...)
			if err != nil {
				return err
			}
			out, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		return out, err
	}
	const field = "attributes.warranty_days_left"
	operands := []any{float64(-5), float64(0), float64(5), float64(30), 30.5, "30", "30.0000000000000001", "-0", "9007199254740993",
		"1e308", "1.7976931348623157e308", "1e400", "-1e400", "1e-400", "-1e-400", "1e-301"}
	var cases []*Expr
	for _, op := range []string{OpEq, OpNe, OpLt, OpLte, OpGt, OpGte} {
		for _, v := range operands {
			cases = append(cases, &Expr{Field: field, Op: op, Value: v})
		}
	}
	cases = append(cases,
		// in and between take numbers of JSON only (the first element sets
		// the kind for Validate).
		&Expr{Field: field, Op: OpIn, Value: []any{float64(5), math.Copysign(0, -1), float64(1 << 53), math.MaxFloat64}},
		&Expr{Field: field, Op: OpIn, Value: []any{5e-324, float64(31), -1e308}},
		&Expr{Field: field, Op: OpBetween, Value: []any{float64(0), float64(30)}},
		&Expr{Field: field, Op: OpBetween, Value: []any{-math.MaxFloat64, 5e-324}},
		&Expr{Field: field, Op: OpBetween, Value: []any{30.000000000000004, math.MaxFloat64}},
		&Expr{Field: field, Op: OpBetween, Value: []any{float64(30), float64(-30)}},
		&Expr{Field: field, Op: OpExists, Value: true},
		&Expr{Field: field, Op: OpExists, Value: false},
		&Expr{Not: &Expr{Field: field, Op: OpLte, Value: float64(30)}},
		&Expr{Not: &Expr{Field: field, Op: OpIn, Value: []any{float64(5), float64(-5)}}},
		&Expr{Or: []Expr{{Field: field, Op: OpLt, Value: float64(0)}, {Field: field, Op: OpGte, Value: "1e308"}}},
		&Expr{Related: &RelatedExpr{Direction: DirectionBoth, Where: &Expr{Field: field, Op: OpLte, Value: float64(30)}}},
	)
	saved := numberColumns
	t.Cleanup(func() { numberColumns = saved })
	matched := 0
	for _, c := range cases {
		numberColumns = saved
		got, gotErr := run(c)
		numberColumns = nil
		before, beforeErr := run(c)
		numberColumns = saved
		name := fmt.Sprintf("%+v", *c)
		if (gotErr == nil) != (beforeErr == nil) {
			t.Errorf("%s: error %v with the index condition, %v without", name, gotErr, beforeErr)
			continue
		}
		if gotErr != nil {
			t.Errorf("%s: %v", name, gotErr)
			continue
		}
		if !slices.Equal(got, before) {
			names := func(ids []string) []string {
				out := make([]string, len(ids))
				for i, id := range ids {
					out[i] = want[id]
				}
				return out
			}
			t.Errorf("%s: %v with the index condition, %v without", name, names(got), names(before))
		}
		if len(got) > 0 {
			matched++
		}
	}
	if matched < len(cases)/2 {
		t.Errorf("only %d of %d cases match any CI: the fixture does not cover the values", matched, len(cases))
	}

	// The engine returns the same CIs (values it can decode).
	between := &Expr{Field: field, Op: OpBetween, Value: []any{float64(0), float64(30)}}
	rows, _, err := NewPGQueryEngine(f.App).Query(ctx, f.OrgA, &FilterSpec{EntityKind: "ci", Where: between}, api.PaginationParams{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	slices.Sort(ids)
	if direct, derr := run(between); derr != nil || !slices.Equal(ids, direct) || len(ids) == 0 {
		t.Errorf("engine: %d CIs %v, the WHERE %d %v (%v)", len(ids), ids, len(direct), direct, derr)
	}

	// The SQL of the comparison carries the index condition on the column
	// besides the exact comparison, under every alias.
	cat := &catalog{
		attrs:    map[attrRef]resolvedAttribute{{"ci", "warranty_days_left"}: {kind: kindNumber}},
		relAttrs: map[attrRef]resolvedAttribute{{"ci", "warranty_days_left"}: {kind: kindNumber}},
	}
	sql, _, _, err := (&Expr{Field: field, Op: OpLt, Value: float64(30)}).compile("ci", "rn1", cat, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if prefix := "(rn1.warranty_days_left_num <= CASE WHEN $2::numeric >= 1e308"; !strings.HasPrefix(sql, prefix) {
		t.Errorf("SQL %s, want it to start with %s", sql, prefix)
	}
}
