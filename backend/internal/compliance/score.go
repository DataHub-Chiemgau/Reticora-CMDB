package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The aggregated compliance score (SEC-03, CMP-02, WP-1127): weighted by
// the severity of the rule, score = Σ weight(pass) / Σ weight(pass + fail)
// × 100 (not_applicable does not count), aggregated in SQL per CI, CI type,
// client, site, category or severity under the caller's row level
// security, and kept per day by the system job compliance.score.snapshot
// for trends over 400 days.

// Groups of GET /api/v1/compliance/score?group_by=.
const (
	GroupCI       = "ci"
	GroupCIType   = "ci_type"
	GroupClient   = "client"
	GroupSite     = "site"
	GroupCategory = "category"
	GroupSeverity = "severity"
	// GroupOverall is the whole organization (history and snapshot).
	GroupOverall = "overall"
)

// ScoreGroups are the values of group_by of the score.
var ScoreGroups = []string{GroupCI, GroupCIType, GroupClient, GroupSite, GroupCategory, GroupSeverity}

// HistoryGroups are the values of group_by of the score history.
var HistoryGroups = []string{GroupOverall, GroupCIType, GroupCategory, GroupSeverity, GroupClient, GroupSite}

// SnapshotDimensions are the dimensions of compliance_score_snapshot;
// client and site are its columns (every dimension per client and site).
var SnapshotDimensions = []string{GroupOverall, GroupCIType, GroupCategory, GroupSeverity}

// Severities of a rule, highest first (compliance_rule_severity_check).
var Severities = []string{"critical", "high", "medium", "low"}

// MaxWeight bounds a severity weight.
const MaxWeight = 100

// SnapshotRetention is how long the daily score snapshots are kept (CMP-02).
const SnapshotRetention = 400

// MaxHistoryDays is the longest period of the score history.
const MaxHistoryDays = 400

// DefaultHistoryDays is the period of the history without from.
const DefaultHistoryDays = 30

// SnapshotKind is the system job of the daily score snapshot.
const SnapshotKind = "compliance.score.snapshot"

// ErrScoreHistoryUnavailable: the memory profile keeps no snapshots.
var ErrScoreHistoryUnavailable = errors.New("the compliance score history needs PostgreSQL")

// Weights are the weights per severity of a rule.
type Weights map[string]int

// DefaultWeights are critical 10, high 5, medium 2 and low 1 (AK2).
func DefaultWeights() Weights { return Weights{"critical": 10, "high": 5, "medium": 2, "low": 1} }

// weightsJSON is the default as the jsonb of compliance_settings.
func weightsJSON(w Weights) string {
	b, _ := json.Marshal(w)
	return string(b)
}

// Settings are the compliance settings of an organization.
type Settings struct {
	Weights Weights `json:"weights"`
	// UpdatedAt is nil while the defaults apply.
	UpdatedAt *time.Time `json:"updated_at"`
}

// SettingsRequest is the body of PUT /api/v1/compliance/settings.
type SettingsRequest struct {
	Weights json.RawMessage `json:"weights"`
}

// ParseWeights reads the weights of a PUT: an object of the four
// severities with whole numbers 0–100; a missing severity keeps its
// default, another key, a fraction or a value out of range is 422 with the
// field (weights.high).
// validation: fixed texts naming the field.
func ParseWeights(raw json.RawMessage) (Weights, error) {
	out := DefaultWeights()
	trimmed := bytes.TrimSpace(raw)
	var keys map[string]json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &keys) != nil {
		return nil, unprocessableWeights("weights", "must be an object of the severities critical, high, medium and low")
	}
	for key, value := range keys {
		if !slices.Contains(Severities, key) {
			return nil, unprocessableWeights("weights."+key, "unknown severity; known are critical, high, medium and low")
		}
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(value))
		dec.UseNumber()
		// A JSON string decodes into json.Number as well: only a number
		// literal is a weight.
		if v := bytes.TrimSpace(value); len(v) == 0 || v[0] == '"' || dec.Decode(&n) != nil {
			return nil, unprocessableWeights("weights."+key, "must be a whole number between 0 and 100")
		}
		v, err := n.Int64()
		if err != nil || v < 0 || v > MaxWeight {
			return nil, unprocessableWeights("weights."+key, "must be a whole number between 0 and 100")
		}
		out[key] = int(v)
	}
	return out, nil
}

func unprocessableWeights(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// Sums are the counts and weights of a group.
type Sums struct {
	Passed        int
	Failed        int
	NotApplicable int
	PassedWeight  int64
	FailedWeight  int64
}

// Add counts one result of a rule of severity under weights.
func (s *Sums) Add(status, severity string, weights Weights) {
	w := int64(weights[severity])
	switch status {
	case "pass":
		s.Passed++
		s.PassedWeight += w
	case "fail":
		s.Failed++
		s.FailedWeight += w
	default:
		s.NotApplicable++
	}
}

// ScoreFromSums is the weighted score of the sums: 0 without weight.
func ScoreFromSums(passedWeight, failedWeight int64) float64 {
	den := passedWeight + failedWeight
	if den <= 0 {
		return 0
	}
	return float64(passedWeight) / float64(den) * 100
}

// Score is the weighted score of the sums.
func (s *Sums) Score() float64 { return ScoreFromSums(s.PassedWeight, s.FailedWeight) }

// ScoreGroup is one group of the score.
type ScoreGroup struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Passed        int     `json:"passed"`
	Failed        int     `json:"failed"`
	NotApplicable int     `json:"not_applicable"`
	Score         float64 `json:"score"`
}

func groupOf(key, label string, s *Sums) ScoreGroup {
	return ScoreGroup{Key: key, Label: label, Passed: s.Passed, Failed: s.Failed, NotApplicable: s.NotApplicable, Score: s.Score()}
}

// ScoreQuery selects the groups of the score.
type ScoreQuery struct {
	// GroupBy is one of ScoreGroups; empty is the whole organization.
	GroupBy  string
	CITypeID string
	// Limit and After page group_by=ci (score ascending, then key).
	Limit int
	After *ScoreCursor
}

// ScoreCursor is the position of a CI group in the order of the page.
type ScoreCursor struct {
	Score float64
	Key   string
}

// ScorePage is the groups of a query; Next is set when more CIs follow.
type ScorePage struct {
	Groups []ScoreGroup
	Next   *ScoreCursor
}

// ScoreResponse is the answer of GET /api/v1/compliance/score: the form
// without group_by (overall, by_ci_type, results empty) and, with
// group_by, the groups (CIs paged by next_cursor).
type ScoreResponse struct {
	EvaluationResponse
	GroupBy    string       `json:"group_by,omitempty"`
	Groups     []ScoreGroup `json:"groups,omitempty"`
	NextCursor string       `json:"next_cursor,omitempty"`
	HasMore    bool         `json:"has_more,omitempty"`
}

// HistoryQuery selects the score history: the days from From to To
// (inclusive).
type HistoryQuery struct {
	GroupBy  string
	From, To time.Time
}

// HistoryPoint is the score of a group on a day.
type HistoryPoint struct {
	Day          string  `json:"day"`
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	Passed       int     `json:"passed"`
	Failed       int     `json:"failed"`
	PassedWeight int64   `json:"passed_weight"`
	FailedWeight int64   `json:"failed_weight"`
	Score        float64 `json:"score"`
}

// HistoryResponse is the answer of GET /api/v1/compliance/score/history.
type HistoryResponse struct {
	GroupBy string         `json:"group_by"`
	From    string         `json:"from"`
	To      string         `json:"to"`
	Data    []HistoryPoint `json:"data"`
}

// ScoreStore aggregates the score, keeps the weights and the snapshots.
type ScoreStore interface {
	// Score aggregates the results under the caller's scope.
	Score(ctx context.Context, orgID string, q *ScoreQuery) (*ScorePage, error)
	GetSettings(ctx context.Context, orgID string) (*Settings, error)
	PutSettings(ctx context.Context, orgID string, weights Weights) (*Settings, error)
	// SnapshotScores writes the sums of day (upsert) and removes the
	// snapshots older than SnapshotRetention days; it returns the rows
	// written.
	SnapshotScores(ctx context.Context, orgID string, day time.Time) (int, error)
	ScoreHistory(ctx context.Context, orgID string, q *HistoryQuery) ([]HistoryPoint, error)
}

// scoreCursorSort names the cursor of group_by=ci.
const scoreCursorSort = "score"

// parseScoreQuery reads group_by and the page of the score.
func parseScoreQuery(r *http.Request, ciTypeID string) (*ScoreQuery, error) {
	q := &ScoreQuery{GroupBy: r.URL.Query().Get("group_by"), CITypeID: ciTypeID}
	if q.GroupBy != "" && !slices.Contains(ScoreGroups, q.GroupBy) {
		return nil, badRequest("group_by must be one of ci, ci_type, client, site, category, severity")
	}
	if q.GroupBy == GroupCI {
		page := api.ParsePagination(r)
		if page.CursorError != nil {
			return nil, badRequest("invalid cursor")
		}
		q.Limit = page.Limit
		if page.Cursor != nil {
			var score float64
			if !page.Cursor.Matches(scoreCursorSort, "asc") || json.Unmarshal([]byte(page.Cursor.Value), &score) != nil {
				return nil, badRequest("invalid cursor")
			}
			q.After = &ScoreCursor{Score: score, Key: page.Cursor.ID}
		}
	}
	return q, nil
}

// requestError is a 400 of the score routes.
type requestError struct{ msg string }

// validation: the fixed texts of badRequest name the parameter.
func (e *requestError) Error() string { return e.msg }

func badRequest(msg string) error { return &requestError{msg: msg} }

// parseDay reads a date (YYYY-MM-DD) or an RFC 3339 time as its UTC day.
func parseDay(name, value string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		t = t.UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, badRequest(name + " must be a date (YYYY-MM-DD)")
}

// today is the current UTC day.
func today() time.Time {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// parseHistoryQuery reads group_by, from and to of the history: to is
// today without one, from DefaultHistoryDays before to; a period longer
// than MaxHistoryDays is 400.
func parseHistoryQuery(r *http.Request) (*HistoryQuery, error) {
	v := r.URL.Query()
	q := &HistoryQuery{GroupBy: v.Get("group_by"), To: today()}
	if q.GroupBy == "" {
		q.GroupBy = GroupOverall
	}
	if !slices.Contains(HistoryGroups, q.GroupBy) {
		return nil, badRequest("group_by must be one of overall, ci_type, category, severity, client, site")
	}
	var err error
	if s := v.Get("to"); s != "" {
		if q.To, err = parseDay("to", s); err != nil {
			return nil, err
		}
	}
	q.From = q.To.AddDate(0, 0, -DefaultHistoryDays)
	if s := v.Get("from"); s != "" {
		if q.From, err = parseDay("from", s); err != nil {
			return nil, err
		}
	}
	switch {
	case q.From.After(q.To):
		return nil, badRequest("from must not be after to")
	case q.To.Sub(q.From) > MaxHistoryDays*24*time.Hour:
		return nil, badRequest("the period is at most 400 days")
	}
	return q, nil
}

// writeScoreError answers a failed score request.
func writeScoreError(w http.ResponseWriter, r *http.Request, err error) {
	var bad *requestError
	switch {
	case errors.As(err, &bad):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", bad.msg)
	case errors.Is(err, ErrScoreHistoryUnavailable):
		api.WriteUnavailable(w, ErrScoreHistoryUnavailable.Error())
	default:
		writeComplianceError(w, r, err)
	}
}

// Score handles GET /api/v1/compliance/score.
func (h *Handler) Score(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	ciTypeID, ok := ciTypeFilter(w, r)
	if !ok {
		return
	}
	q, err := parseScoreQuery(r, ciTypeID)
	if err != nil {
		writeScoreError(w, r, err)
		return
	}
	out := ScoreResponse{EvaluationResponse: EvaluationResponse{ByCIType: []Score{}, Results: []Result{}}}
	overall, err := h.repo.Score(r.Context(), t.OrganizationID, &ScoreQuery{CITypeID: ciTypeID})
	if err != nil {
		writeScoreError(w, r, err)
		return
	}
	if len(overall.Groups) > 0 {
		out.Overall = scoreOf("", &overall.Groups[0])
	}
	if q.GroupBy == "" {
		byType, berr := h.repo.Score(r.Context(), t.OrganizationID, &ScoreQuery{GroupBy: GroupCIType, CITypeID: ciTypeID})
		if berr != nil {
			writeScoreError(w, r, berr)
			return
		}
		for i := range byType.Groups {
			out.ByCIType = append(out.ByCIType, scoreOf(byType.Groups[i].Key, &byType.Groups[i]))
		}
		api.WriteJSON(w, http.StatusOK, out)
		return
	}
	page, err := h.repo.Score(r.Context(), t.OrganizationID, q)
	if err != nil {
		writeScoreError(w, r, err)
		return
	}
	out.GroupBy, out.Groups = q.GroupBy, api.NonNilSlice(page.Groups)
	if page.Next != nil {
		value, _ := json.Marshal(page.Next.Score)
		out.HasMore = true
		out.NextCursor = api.EncodeCursor(api.Cursor{Sort: scoreCursorSort, Dir: "asc", Value: string(value), ID: page.Next.Key})
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func scoreOf(ciTypeID string, g *ScoreGroup) Score {
	return Score{CITypeID: ciTypeID, Passed: g.Passed, Failed: g.Failed, NotApplicable: g.NotApplicable, Score: g.Score}
}

// ScoreHistory handles GET /api/v1/compliance/score/history.
func (h *Handler) ScoreHistory(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	q, err := parseHistoryQuery(r)
	if err != nil {
		writeScoreError(w, r, err)
		return
	}
	points, err := h.repo.ScoreHistory(r.Context(), t.OrganizationID, q)
	if err != nil {
		writeScoreError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, HistoryResponse{GroupBy: q.GroupBy, From: q.From.Format(time.DateOnly),
		To: q.To.Format(time.DateOnly), Data: api.NonNilSlice(points)})
}

// GetSettings handles GET /api/v1/compliance/settings.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	s, err := h.repo.GetSettings(r.Context(), t.OrganizationID)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// PutSettings handles PUT /api/v1/compliance/settings; the route audit
// records the row before and after (snapshot register).
func (h *Handler) PutSettings(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	// The weights apply to the score and the snapshots of every client
	// and site: a restricted principal is refused (E-08, E-09, pattern
	// Evaluate and alarm settings).
	err := requireOrgWide(r.Context(), t.OrganizationID)
	if errors.Is(err, ErrOrgWideScopeRequired) {
		api.WriteError(w, http.StatusForbidden, "Forbidden",
			"the compliance weights apply to the whole organization; compliance:write must be held organization-wide")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	var req SettingsRequest
	if err = api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "request body must be a JSON object with weights")
		return
	}
	weights, err := ParseWeights(req.Weights)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	s, err := h.repo.PutSettings(r.Context(), t.OrganizationID, weights)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// Snapshot is the system job compliance.score.snapshot (AK3).
type Snapshot struct {
	repo    ScoreStore
	orgs    func(ctx context.Context) ([]string, error)
	enabled func(ctx context.Context, orgID string) bool
	clock   func() time.Time
}

// NewSnapshot returns the daily snapshot over the organizations orgs lists
// that enabled admits (nil: every one).
func NewSnapshot(repo ScoreStore, orgs func(ctx context.Context) ([]string, error), enabled func(ctx context.Context, orgID string) bool) *Snapshot {
	return &Snapshot{repo: repo, orgs: orgs, enabled: enabled, clock: time.Now}
}

// Schedule is the registration with the scheduler: daily 04:30 UTC, after
// the nightly evaluation (02:30), at most one missed run caught up.
func (s *Snapshot) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: SnapshotKind, Spec: "30 4 * * *", MaxCatchUp: 1, Timeout: 30 * time.Minute,
		Run: func(ctx context.Context) error {
			rows, err := s.Run(ctx)
			if rows > 0 {
				slog.InfoContext(ctx, "compliance score snapshots written", "rows", rows)
			}
			return err
		}}
}

// Run writes the snapshot of the current UTC day for every organization,
// org-wide in its own tenant transaction, and returns the rows written.
func (s *Snapshot) Run(ctx context.Context) (int, error) {
	orgs, err := s.orgs(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", SnapshotKind, err)
	}
	now := s.clock().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	total := 0
	var errs []error
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		octx := tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
		if s.enabled != nil && !s.enabled(octx, orgID) {
			continue
		}
		n, serr := s.repo.SnapshotScores(octx, orgID, day)
		if serr != nil {
			errs = append(errs, fmt.Errorf("%s: organization %s: %w", SnapshotKind, orgID, serr))
			continue
		}
		total += n
	}
	return total, errors.Join(errs...)
}

// tallyGroups aggregates results in Go (the memory profile): per group key
// of key the sums, sorted by score ascending, then key.
func tallyGroups(results []Result, rules map[string]*Rule, weights Weights, key func(*Result, *Rule) (string, string)) []ScoreGroup {
	sums := map[string]*Sums{}
	labels := map[string]string{}
	for i := range results {
		res := &results[i]
		rule := rules[res.RuleID]
		if rule == nil {
			continue
		}
		k, label := key(res, rule)
		s := sums[k]
		if s == nil {
			s = &Sums{}
			sums[k] = s
			labels[k] = label
		}
		s.Add(res.Status, rule.Severity, weights)
	}
	out := make([]ScoreGroup, 0, len(sums))
	for k, s := range sums {
		out = append(out, groupOf(k, labels[k], s))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Key < out[j].Key
	})
	return out
}
