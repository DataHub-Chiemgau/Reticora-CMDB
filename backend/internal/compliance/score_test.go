package compliance

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// The weighted compliance score (SEC-03, CMP-02, WP-1127): Σ weight(pass) /
// Σ weight(pass + fail) × 100 with the weights of the severity of the rule,
// not_applicable does not count.

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWeightedScore(t *testing.T) {
	w := DefaultWeights()
	if w["critical"] != 10 || w["high"] != 5 || w["medium"] != 2 || w["low"] != 1 {
		t.Fatalf("default weights %v", w)
	}
	var s Sums
	s.Add("pass", "critical", w)
	s.Add("fail", "low", w)
	s.Add("fail", "high", w)
	s.Add("not_applicable", "critical", w)
	if s.Passed != 1 || s.Failed != 2 || s.NotApplicable != 1 || s.PassedWeight != 10 || s.FailedWeight != 6 {
		t.Fatalf("sums %+v", s)
	}
	if got := s.Score(); !near(got, 10.0/16*100) {
		t.Errorf("score %v, want %v", got, 10.0/16*100)
	}
	// Only not_applicable: score 0 with passed = failed = 0.
	var na Sums
	na.Add("not_applicable", "critical", w)
	na.Add("not_applicable", "low", w)
	if na.Passed != 0 || na.Failed != 0 || na.Score() != 0 {
		t.Errorf("only not_applicable: %+v score %v", na, na.Score())
	}
	// A weight of 0 takes the severity out of the score.
	zero := Weights{"critical": 0, "high": 5, "medium": 2, "low": 1}
	var z Sums
	z.Add("fail", "critical", zero)
	z.Add("pass", "low", zero)
	if !near(z.Score(), 100) {
		t.Errorf("critical weight 0: %v, want 100", z.Score())
	}
}

func TestScoreFromSnapshotSums(t *testing.T) {
	for _, c := range []struct {
		pw, fw int64
		want   float64
	}{{0, 0, 0}, {10, 0, 100}, {0, 7, 0}, {21, 16, 21.0 / 37 * 100}} {
		if got := ScoreFromSums(c.pw, c.fw); !near(got, c.want) {
			t.Errorf("ScoreFromSums(%d, %d) = %v, want %v", c.pw, c.fw, got, c.want)
		}
	}
	// Sums of two days of one client added before dividing: not the mean of
	// the two scores.
	if got := ScoreFromSums(10+1, 0+9); !near(got, 55) {
		t.Errorf("summed snapshot rows: %v, want 55", got)
	}
}

func TestParseWeights(t *testing.T) {
	got, err := ParseWeights(json.RawMessage(`{"critical": 20, "low": 0}`))
	if err != nil || got["critical"] != 20 || got["low"] != 0 || got["high"] != 5 {
		t.Fatalf("weights %v %v", got, err)
	}
	for raw, field := range map[string]string{
		`{"critical": 101}`: "weights.critical", `{"high": -1}`: "weights.high", `{"medium": 1.5}`: "weights.medium",
		`{"low": "1"}`: "weights.low", `{"urgent": 1}`: "weights.urgent", `[]`: "weights", ``: "weights",
	} {
		_, err := ParseWeights(json.RawMessage(raw))
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422 %s", raw, err, field)
		}
	}
}

func TestParseScoreAndHistoryQuery(t *testing.T) {
	if _, err := parseScoreQuery(httptest.NewRequest(http.MethodGet, "/x?group_by=team", nil), ""); err == nil {
		t.Error("group_by=team accepted")
	}
	q, err := parseScoreQuery(httptest.NewRequest(http.MethodGet, "/x?group_by=ci&limit=5", nil), "")
	if err != nil || q.Limit != 5 || q.GroupBy != GroupCI {
		t.Errorf("group_by=ci: %+v %v", q, err)
	}
	for _, c := range []struct {
		query string
		ok    bool
	}{
		{"", true}, {"?from=2025-01-01&to=2026-02-05", true}, {"?from=2025-01-01&to=2026-02-06", false},
		{"?from=2026-02-06&to=2026-02-05", false}, {"?group_by=ci", false}, {"?from=yesterday", false},
	} {
		if _, err := parseHistoryQuery(httptest.NewRequest(http.MethodGet, "/x"+c.query, nil)); (err == nil) != c.ok {
			t.Errorf("history %q: %v, want ok %v", c.query, err, c.ok)
		}
	}
}
