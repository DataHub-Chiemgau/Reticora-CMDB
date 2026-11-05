package graphqlbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The tests of this file cover GQL-03 (WP-792): the CI aggregates ciForm
// (the resolved form of WP-719 through citype.FormResolver, the preview
// rule with citype:manage), ciHistory (the unified history with the
// relationship changes in a time window, audit entries with audit:read,
// relationship entries with relationship:read, the field permissions of
// RBA-07) and ciMetrics (the series of the CI, feature monitoring); an
// invisible CI is null or empty; the complexity stays within the budget of
// SEC-10.

// Ids of the aggregate fixture.
const (
	aggRoleOwn     = "00000000-0000-4000-8000-00000000a001"
	aggRoleForeign = "00000000-0000-4000-8000-00000000a002"
	aggTemplate    = "00000000-0000-4000-8000-00000000b001"
	aggUnknownCI   = "00000000-0000-4000-8000-00000000c404"
)

// aggFormSource is a citype.FormSource over fixed data: the caller has the
// role aggRoleOwn, each role has a layout of its own.
type aggFormSource struct {
	ciID string
}

func (s *aggFormSource) FormCI(_ context.Context, _, ciID string) (*citype.FormSubject, error) {
	if ciID != s.ciID {
		return nil, citype.ErrFormCINotFound
	}
	return &citype.FormSubject{CITypeID: "server", Chain: []string{"server"}, Values: map[string]any{}}, nil
}

func (s *aggFormSource) FormTypeChain(context.Context, string, string) ([]string, error) {
	return []string{"server"}, nil
}

func (s *aggFormSource) FormTemplate(_ context.Context, _, templateID string) (string, error) {
	if templateID != aggTemplate {
		return "", citype.ErrFormTemplateNotFound
	}
	return "server", nil
}

func (s *aggFormSource) FormFields(context.Context, string, string, string, string) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	return []fieldmeta.FieldDefinition{
		{Name: "hostname", DataType: "string", Required: true, SortOrder: 1},
		{Name: "notes", DataType: "string", SortOrder: 2},
	}, nil, nil
}

func (s *aggFormSource) FormRoles(context.Context, string, string) ([]string, error) {
	return []string{aggRoleOwn}, nil
}

func (s *aggFormSource) FormRoleExists(_ context.Context, _, roleID string) (bool, error) {
	return roleID == aggRoleOwn || roleID == aggRoleForeign, nil
}

func (s *aggFormSource) FormLayouts(_ context.Context, _ string, _, _, roleIDs []string) ([]citype.FormLayout, error) {
	var out []citype.FormLayout
	for _, role := range roleIDs {
		out = append(out, citype.FormLayout{ID: "layout-" + role, CITypeID: "server", RoleID: role, IsActive: true,
			Layout: citype.LayoutDocument{Sections: []citype.LayoutSection{{Key: "section-" + role,
				Title:  citype.LocalizedText{"de": "Abschnitt", "en": "Section"},
				Fields: []citype.LayoutField{{Name: "hostname", Width: 6}, {Name: "notes", Width: 6}}}}}})
	}
	return out, nil
}

func (s *aggFormSource) FormSectionRules(context.Context, string, []string) (map[string]*rules.Rule, error) {
	return map[string]*rules.Rule{}, nil
}

// aggTimeline is a history.CITimeline over fixed entries (newest first)
// that records the options it was asked with.
type aggTimeline struct {
	ciID    string
	entries []history.Change

	mu   sync.Mutex
	opts []history.TimelineOptions
}

func (f *aggTimeline) CITimeline(_ context.Context, _, ciID string, page api.PaginationParams, opts history.TimelineOptions) ([]history.Change, int, error) {
	f.mu.Lock()
	f.opts = append(f.opts, opts)
	f.mu.Unlock()
	if ciID != f.ciID {
		return nil, 0, api.ErrNotFound
	}
	var out []history.Change
	for i := range f.entries {
		e := f.entries[i]
		switch {
		case e.Source == history.SourceAudit && !opts.IncludeAudit,
			e.Source == history.SourceRelationshipChange && !opts.IncludeRelationships,
			!opts.From.IsZero() && e.CreatedAt.Before(opts.From),
			!opts.To.IsZero() && e.CreatedAt.After(opts.To):
			continue
		}
		out = append(out, e)
	}
	total := len(out)
	start := min(page.Offset, total)
	return out[start:min(start+page.Limit, total)], total, nil
}

func (f *aggTimeline) lastOpts() history.TimelineOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opts[len(f.opts)-1]
}

// aggT0 is the end of the time window of the history tests.
var aggT0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type aggFixture struct {
	h        *Handler
	ci       string
	timeline *aggTimeline
}

// newAggFixture builds a CI with a history of two change sets, an audit
// entry, a relationship change and an entity change outside the window,
// two metrics and its form.
func newAggFixture(t *testing.T) *aggFixture {
	t.Helper()
	bg := context.Background()
	cis := ci.NewMemoryRepository()
	item := &ci.Item{OrganizationID: frOrg, Name: "agg-ci", CITypeID: "server", Status: "active", ClientID: "client-1",
		Hostname: "agg.secret.example", Attributes: map[string]any{}}
	if err := cis.Create(bg, item); err != nil {
		t.Fatal(err)
	}
	timeline := &aggTimeline{ciID: item.ID, entries: []history.Change{
		{ID: "e1", EntityType: "ci", EntityID: item.ID, ChangeType: "update", FieldName: "hostname", OldValue: "old.secret",
			NewValue: "new.secret", Source: history.SourceCIChange, ChangeKind: "updated", ChangeSetID: "set-1",
			FieldChanges: []history.FieldChange{{Field: "hostname", Old: "old.secret", New: "new.secret"}}, CreatedAt: aggT0.Add(-time.Hour)},
		{ID: "e2", EntityType: "ci", EntityID: item.ID, ChangeType: "update", Source: history.SourceCIChange, ChangeKind: "updated",
			ChangeSetID: "set-2", FieldChanges: []history.FieldChange{{Field: "hostname", Old: "a.secret", New: "b.secret"},
				{Field: "model", Old: "R640", New: "R650"}}, CreatedAt: aggT0.Add(-2 * time.Hour)},
		{ID: "e3", EntityType: "ci", EntityID: item.ID, ChangeType: "ci.update", Source: history.SourceAudit, CreatedAt: aggT0.Add(-3 * time.Hour)},
		{ID: "e4", EntityType: "relationship", EntityID: "rel-1", ChangeType: relationship.ChangeRemoved,
			Source: history.SourceRelationshipChange, OldValue: map[string]any{"rel_type": "depends_on"}, CreatedAt: aggT0.Add(-4 * time.Hour)},
		{ID: "e5", EntityType: "ci", EntityID: item.ID, ChangeType: "lifecycle_transition", Source: history.SourceEntityChange,
			Comment: "commissioned", CreatedAt: aggT0.Add(-30 * 24 * time.Hour)},
	}}
	metrics := monitoring.NewMemoryMetricStore()
	now := time.Now().UTC()
	if err := metrics.Ingest(bg, []monitoring.Metric{
		{OrgID: frOrg, CIID: item.ID, Name: "cpu", Value: 10, Timestamp: now.Add(-time.Hour)},
		{OrgID: frOrg, CIID: item.ID, Name: "cpu", Value: 20, Timestamp: now.Add(-30 * time.Minute)},
		{OrgID: frOrg, CIID: item.ID, Name: "mem", Value: 50, Timestamp: now.Add(-time.Hour)},
		{OrgID: frOrg, CIID: item.ID, Name: "disk", Value: 70, Timestamp: now.Add(-48 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cis, relationship.NewMemoryRepository()).
		WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureMonitoring: true}).
		WithSources(&Sources{History: timeline, Metrics: metrics,
			Forms: citype.NewFormResolver(&aggFormSource{ciID: item.ID}, nil)})
	return &aggFixture{h: h, ci: item.ID, timeline: timeline}
}

// aggRun sends query as a session with perms and policy (nil: unrestricted
// for perms).
func aggRun(t *testing.T, h *Handler, perms []identity.Permission, policy *permission.FieldPolicy, query string) assetResponse {
	t.Helper()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(gql(query)))
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: frOrg, UserID: "user-1"})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: frOrg, Permissions: perms,
		Type: identity.PrincipalTypeUser})
	if policy == nil {
		policy = permission.UnrestrictedPolicy(perms)
	}
	ctx = permission.WithPolicy(ctx, policy)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	var out assetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return out
}

const formFields = `ciTypeId templateId roleId layoutId source requiredFields
	sections { key title visible fields { name width definition state { visible required readOnly allowedValues } } }
	rules { key attributeKey kind condition ast message }`

const historyFields = `id entityType entityId source siteId actorId actorUserId changeType changeKind changeSetId fieldName
	oldValue newValue fieldChanges { field old new } comment changeSource requestId correlationId createdAt observedAt`

func formQuery(id, args string) string {
	return `{ ciForm(id: "` + id + `"` + args + `) { ` + formFields + ` } }`
}

// TestCIForm: ciForm resolves through citype.FormResolver with the
// caller's roles or one of them; another role needs citype:manage and is
// then the preview of that role, a template is a preview and needs
// citype:manage; an invisible CI is null.
func TestCIForm(t *testing.T) {
	f := newAggFixture(t)
	read := []identity.Permission{identity.PermCIRead}
	manage := []identity.Permission{identity.PermCIRead, identity.PermCITypeManage}
	form := func(resp *assetResponse) map[string]any {
		t.Helper()
		if len(resp.Errors) != 0 {
			t.Fatalf("errors: %+v", resp.Errors)
		}
		out, _ := resp.Data["ciForm"].(map[string]any)
		if out == nil {
			t.Fatalf("no form: %v", resp.Data)
		}
		return out
	}
	for _, args := range []string{"", `, role: "` + aggRoleOwn + `"`} {
		resp := aggRun(t, f.h, read, nil, formQuery(f.ci, args))
		got := form(&resp)
		sections, _ := got["sections"].([]any)
		if got["roleId"] != aggRoleOwn || got["layoutId"] != "layout-"+aggRoleOwn || got["source"] != citype.FormSourceLayout ||
			len(sections) != 1 {
			t.Fatalf("own role (%q): %v", args, got)
		}
		section, _ := sections[0].(map[string]any)
		title, _ := section["title"].(map[string]any)
		fields, _ := section["fields"].([]any)
		if section["key"] != "section-"+aggRoleOwn || title["en"] != "Section" || len(fields) != 2 {
			t.Errorf("own role section: %v", section)
		}
		first, _ := fields[0].(map[string]any)
		def, _ := first["definition"].(map[string]any)
		state, _ := first["state"].(map[string]any)
		if first["name"] != "hostname" || def["data_type"] != "string" || state["required"] != true {
			t.Errorf("own role field: %v", first)
		}
		if req, _ := got["requiredFields"].([]any); len(req) != 1 || req[0] != "hostname" {
			t.Errorf("required fields: %v", got["requiredFields"])
		}
	}

	foreign := formQuery(f.ci, `, role: "`+aggRoleForeign+`"`)
	if resp := aggRun(t, f.h, read, nil, foreign); resp.codeAt("ciForm") != codeForbidden {
		t.Errorf("foreign role without citype:manage: %+v", resp)
	}
	resp := aggRun(t, f.h, manage, nil, foreign)
	if got := form(&resp); got["roleId"] != aggRoleForeign || got["layoutId"] != "layout-"+aggRoleForeign {
		t.Errorf("preview of the foreign role: %v", got)
	}

	tpl := formQuery(f.ci, `, template: "`+aggTemplate+`"`)
	if resp = aggRun(t, f.h, read, nil, tpl); resp.codeAt("ciForm") != codeForbidden {
		t.Errorf("template preview without citype:manage: %+v", resp)
	}
	if resp = aggRun(t, f.h, manage, nil, tpl); len(resp.Errors) != 0 || resp.Data["ciForm"] == nil {
		t.Errorf("template preview with citype:manage: %+v", resp)
	}

	for _, id := range []string{aggUnknownCI, "not-a-uuid"} {
		if resp = aggRun(t, f.h, manage, nil, formQuery(id, "")); len(resp.Errors) != 0 || resp.Data["ciForm"] != nil {
			t.Errorf("invisible CI %s: %+v", id, resp)
		}
	}
	if resp = aggRun(t, f.h, []identity.Permission{identity.PermCITypeManage}, nil, formQuery(f.ci, "")); resp.codeAt("ciForm") != codeForbidden {
		t.Errorf("without ci:read: %+v", resp)
	}
}

// historyNodes returns the nodes of ciHistory by id, in their order.
func historyNodes(t *testing.T, resp *assetResponse) ([]string, map[string]map[string]any) {
	t.Helper()
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	conn, _ := resp.Data["ciHistory"].(map[string]any)
	nodes, _ := conn["nodes"].([]any)
	var ids []string
	byID := map[string]map[string]any{}
	for _, raw := range nodes {
		n, _ := raw.(map[string]any)
		id, _ := n["id"].(string)
		ids = append(ids, id)
		byID[id] = n
	}
	return ids, byID
}

// TestCIHistory: the page of the window holds the change sets, the audit
// entry and the relationship change, newest first; the entity change
// before the window is left out. The audit entries need audit:read, the
// relationship entries relationship:read, the history ci:read; an
// invisible CI is null; the cursor pages on.
func TestCIHistory(t *testing.T) {
	f := newAggFixture(t)
	all := []identity.Permission{identity.PermCIRead, identity.PermAuditRead, identity.PermRelationshipRead}
	q := func(id, args string) string {
		return `{ ciHistory(id: "` + id + `", from: "` + aggT0.Add(-24*time.Hour).Format(time.RFC3339) + `", to: "` +
			aggT0.Format(time.RFC3339) + `"` + args + `) { nodes { ` + historyFields + ` } pageInfo { hasNextPage endCursor } totalCount } }`
	}
	resp := aggRun(t, f.h, all, nil, q(f.ci, ""))
	ids, byID := historyNodes(t, &resp)
	if !slices.Equal(ids, []string{"e1", "e2", "e3", "e4"}) {
		t.Fatalf("window: %v", ids)
	}
	opts := f.timeline.lastOpts()
	if !opts.From.Equal(aggT0.Add(-24*time.Hour)) || !opts.To.Equal(aggT0) || !opts.IncludeAudit || !opts.IncludeRelationships {
		t.Errorf("options: %+v", opts)
	}
	rel := byID["e4"]
	if rel["source"] != history.SourceRelationshipChange || rel["entityType"] != "relationship" || rel["changeType"] != relationship.ChangeRemoved ||
		rel["changeKind"] != nil {
		t.Errorf("relationship entry: %v", rel)
	}
	if e1 := byID["e1"]; e1["fieldName"] != "hostname" || e1["oldValue"] != "old.secret" || e1["changeSetId"] != "set-1" || e1["actorId"] != nil {
		t.Errorf("change set: %v", e1)
	}
	conn, _ := resp.Data["ciHistory"].(map[string]any)
	if conn["totalCount"] != float64(4) {
		t.Errorf("totalCount: %v", conn["totalCount"])
	}

	resp = aggRun(t, f.h, []identity.Permission{identity.PermCIRead, identity.PermRelationshipRead}, nil, q(f.ci, ""))
	if ids, _ = historyNodes(t, &resp); !slices.Equal(ids, []string{"e1", "e2", "e4"}) || f.timeline.lastOpts().IncludeAudit {
		t.Errorf("without audit:read: %v", ids)
	}
	resp = aggRun(t, f.h, []identity.Permission{identity.PermCIRead, identity.PermAuditRead}, nil, q(f.ci, ""))
	if ids, _ = historyNodes(t, &resp); !slices.Equal(ids, []string{"e1", "e2", "e3"}) || f.timeline.lastOpts().IncludeRelationships {
		t.Errorf("without relationship:read: %v", ids)
	}
	if resp = aggRun(t, f.h, []identity.Permission{identity.PermAuditRead, identity.PermRelationshipRead}, nil, q(f.ci, "")); resp.codeAt("ciHistory") != codeForbidden {
		t.Errorf("without ci:read: %+v", resp)
	}
	if resp = aggRun(t, f.h, all, nil, q(aggUnknownCI, "")); len(resp.Errors) != 0 || resp.Data["ciHistory"] != nil {
		t.Errorf("invisible CI: %+v", resp)
	}

	// Pages of two.
	resp = aggRun(t, f.h, all, nil, q(f.ci, ", first: 2"))
	ids, _ = historyNodes(t, &resp)
	conn, _ = resp.Data["ciHistory"].(map[string]any)
	info, _ := conn["pageInfo"].(map[string]any)
	cursor, _ := info["endCursor"].(string)
	if !slices.Equal(ids, []string{"e1", "e2"}) || info["hasNextPage"] != true || cursor == "" {
		t.Fatalf("first page: %v %v", ids, info)
	}
	resp = aggRun(t, f.h, all, nil, q(f.ci, `, first: 2, after: "`+cursor+`"`))
	if ids, _ = historyNodes(t, &resp); !slices.Equal(ids, []string{"e3", "e4"}) {
		t.Errorf("second page: %v", ids)
	}

	// Without a window the whole history; to before from is bad input.
	resp = aggRun(t, f.h, all, nil, `{ ciHistory(id: "`+f.ci+`") { nodes { id } } }`)
	if ids, _ = historyNodes(t, &resp); len(ids) != 5 {
		t.Errorf("without a window: %v", ids)
	}
	bad := `{ ciHistory(id: "` + f.ci + `", from: "` + aggT0.Format(time.RFC3339) + `", to: "` +
		aggT0.Add(-time.Hour).Format(time.RFC3339) + `") { totalCount } }`
	if resp = aggRun(t, f.h, all, nil, bad); resp.codeAt("ciHistory") != codeBadInput {
		t.Errorf("to before from: %+v", resp)
	}
}

// TestCIHistoryFieldPermissions: an entry of a field the principal may not
// read stays without old and new value; the field is left out of the
// field changes (RBA-07, E-43).
func TestCIHistoryFieldPermissions(t *testing.T) {
	f := newAggFixture(t)
	resp := aggRun(t, f.h, []identity.Permission{identity.PermCIRead}, restrictedBFFPolicy(t),
		`{ ciHistory(id: "`+f.ci+`") { nodes { `+historyFields+` } } }`)
	_, byID := historyNodes(t, &resp)
	e1 := byID["e1"]
	if e1 == nil || e1["fieldName"] != "hostname" || e1["oldValue"] != nil || e1["newValue"] != nil {
		t.Errorf("unreadable field: %v", e1)
	}
	if changes, _ := e1["fieldChanges"].([]any); len(changes) != 0 {
		t.Errorf("unreadable field in its field changes: %v", changes)
	}
	e2 := byID["e2"]
	changes, _ := e2["fieldChanges"].([]any)
	if len(changes) != 1 {
		t.Fatalf("change set of two fields: %v", e2)
	}
	if fc, _ := changes[0].(map[string]any); fc["field"] != "model" || fc["new"] != "R650" {
		t.Errorf("readable field change: %v", fc)
	}
	raw, _ := json.Marshal(resp.Data)
	if strings.Contains(string(raw), "secret") {
		t.Errorf("unreadable values in the answer: %s", raw)
	}
}

// seriesNames returns the names of the series of ciMetrics.
func seriesNames(t *testing.T, resp *assetResponse) []string {
	t.Helper()
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	list, ok := resp.Data["ciMetrics"].([]any)
	if !ok {
		t.Fatalf("no list: %v", resp.Data)
	}
	out := []string{}
	for _, raw := range list {
		s, _ := raw.(map[string]any)
		name, _ := s["name"].(string)
		out = append(out, name)
	}
	return out
}

// TestCIMetrics: the series of the CI in the last 24 hours, all of them or
// the named one; an invisible CI has none; the feature monitoring and
// ci:read are required; a step that is no duration is bad input.
func TestCIMetrics(t *testing.T) {
	f := newAggFixture(t)
	read := []identity.Permission{identity.PermCIRead}
	q := func(id, args string) string {
		return `{ ciMetrics(id: "` + id + `"` + args + `) { name points { timestamp value } } }`
	}
	resp := aggRun(t, f.h, read, nil, q(f.ci, ""))
	if got := seriesNames(t, &resp); !slices.Equal(got, []string{"cpu", "mem"}) {
		t.Errorf("all series in 24 h: %v", got)
	}
	resp = aggRun(t, f.h, read, nil, q(f.ci, `, name: "cpu"`))
	if got := seriesNames(t, &resp); !slices.Equal(got, []string{"cpu"}) {
		t.Fatalf("named series: %v", got)
	}
	series, _ := resp.Data["ciMetrics"].([]any)
	if points, _ := series[0].(map[string]any)["points"].([]any); len(points) != 2 {
		t.Errorf("cpu points: %v", points)
	}
	from := time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339)
	resp = aggRun(t, f.h, read, nil, q(f.ci, `, from: "`+from+`"`))
	if got := seriesNames(t, &resp); !slices.Equal(got, []string{"cpu", "disk", "mem"}) {
		t.Errorf("series since three days: %v", got)
	}
	for _, id := range []string{aggUnknownCI, "not-a-uuid"} {
		resp = aggRun(t, f.h, read, nil, q(id, ""))
		if got := seriesNames(t, &resp); len(got) != 0 {
			t.Errorf("invisible CI %s: %v", id, got)
		}
	}
	if resp = aggRun(t, f.h, read, nil, q(f.ci, `, step: "often"`)); resp.codeAt("ciMetrics") != codeBadInput {
		t.Errorf("bad step: %+v", resp)
	}
	if resp = aggRun(t, f.h, read, nil, q(f.ci, `, step: "5m"`)); len(resp.Errors) != 0 {
		t.Errorf("step 5m: %+v", resp)
	}
	if resp = aggRun(t, f.h, []identity.Permission{identity.PermAuditRead}, nil, q(f.ci, "")); resp.codeAt("ciMetrics") != codeForbidden {
		t.Errorf("without ci:read: %+v", resp)
	}
	f.h.WithEntitlements(entitled{FeatureCMDBCore: true})
	if resp = aggRun(t, f.h, read, nil, q(f.ci, "")); resp.codeAt("ciMetrics") != codeForbidden {
		t.Errorf("without the feature monitoring: %+v", resp)
	}
}

// TestCIAggregatesUnavailable: a missing source answers UNAVAILABLE.
func TestCIAggregatesUnavailable(t *testing.T) {
	f := newAggFixture(t)
	h := NewHandler(ci.NewMemoryRepository(), relationship.NewMemoryRepository()).
		WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureMonitoring: true})
	perms := []identity.Permission{identity.PermCIRead}
	for field, query := range map[string]string{
		"ciForm":    formQuery(f.ci, ""),
		"ciHistory": `{ ciHistory(id: "` + f.ci + `") { totalCount } }`,
		"ciMetrics": `{ ciMetrics(id: "` + f.ci + `") { name } }`,
	} {
		if resp := aggRun(t, h, perms, nil, query); resp.codeAt(field) != "UNAVAILABLE" {
			t.Errorf("%s without its source: %+v", field, resp)
		}
	}
}

// TestCIAggregatesComplexity covers SEC-10 (E-16): a full page of
// ciHistory together with ciForm and ciMetrics stays within MaxComplexity;
// five full pages of ciHistory as aliases are refused.
func TestCIAggregatesComplexity(t *testing.T) {
	f := newAggFixture(t)
	perms := []identity.Permission{identity.PermCIRead, identity.PermAuditRead, identity.PermRelationshipRead}
	page := func(alias string) string {
		return alias + `: ciHistory(id: "` + f.ci + `", first: 100) { nodes { ` + historyFields + ` } pageInfo { hasNextPage endCursor } totalCount }`
	}
	together := `{ ` + page("h") + ` ciForm(id: "` + f.ci + `") { ` + formFields + ` } ciMetrics(id: "` + f.ci +
		`") { name points { timestamp value } } }`
	if resp := aggRun(t, f.h, perms, nil, together); len(resp.Errors) != 0 || resp.Data["h"] == nil || resp.Data["ciForm"] == nil {
		t.Errorf("history, form and metrics: %+v", resp)
	}
	five := "{ " + page("a") + " " + page("b") + " " + page("c") + " " + page("d") + " " + page("e") + " }"
	if resp := aggRun(t, f.h, perms, nil, five); len(resp.Errors) == 0 || !strings.Contains(resp.Errors[0].Message, "complexity") {
		t.Errorf("five full history pages: %+v", resp)
	}
}
