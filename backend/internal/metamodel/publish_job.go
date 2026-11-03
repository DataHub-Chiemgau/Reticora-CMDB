package metamodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The test run of a draft (WP-721, MET-53: draft → test → publish). POST
// /api/v1/metamodel/drafts/{id}/test starts a job of kind TestJobKind; with
// NATS it runs on the job queue, without it in the accepting process
// (jobs.Service.RunDetached). The job runs organization-wide
// (database.OrgWideScope with no user), not in the scope of the caller: a
// change of the metamodel affects the CIs of every client. It reads the
// metamodel of the organization once, lays the items of the draft over it
// (overlay, a citype.FieldLister) and composes, per affected type, the
// layers of citype.ResolveFields with citype.ComposeAll: system base →
// global attributes of the organization that apply to the type → type
// chain → sets → (templates of a CI → instance definitions of a CI). A
// composition conflict (*citype.CompositionError) blocks: the status stays
// draft. Without one the job validates every CI of the affected types that
// is not deleted, in batches (keyset over id, one transaction per batch),
// with the server validation (ci.ValidateAttributes) and the catalog rules
// the draft leaves (WP-257). The violations (CI, type, field, code, rule,
// message; never a value) stand only in metamodel_draft.test_report; the
// job result, the draft and the audit carry the counters (Summary).

// TestJobKind is the job kind of the test run of a draft.
const TestJobKind = "metamodel.test"

// Limits of the test run.
const (
	// testBatch is the number of CIs of one batch transaction.
	testBatch = 200
	// MaxTestViolations bounds the violations stored in the report; the
	// counters stay complete and the report is marked truncated.
	MaxTestViolations = 10000
)

// States of a test report.
const (
	TestStateRunning   = "running"
	TestStateCompleted = "completed"
	TestStateFailed    = "failed"
)

// Codes of blocking findings that are no composition conflict.
const (
	// CodeItemNotApplicable: an item cannot be laid over the metamodel
	// (its target is gone, its payload no longer fits).
	CodeItemNotApplicable = "item-not-applicable"
	// CodeInvalidTemplateItem: an item of a template version cannot be
	// composed (unknown attribute, invalid validation override).
	CodeInvalidTemplateItem = "invalid-template-item"
)

// TestJobParams are the parameters of a test job: the draft and its
// version when the test started.
type TestJobParams struct {
	DraftID string `json:"draft_id"`
	Version int64  `json:"version"`
}

// TestReport is metamodel_draft.test_report: the job, the state, the
// blocking conflicts, the counters per type and attribute and the
// violations. Only GET …/test shows Violations (metamodel:publish).
type TestReport struct {
	JobID string `json:"job_id"`
	State string `json:"state"`
	// DraftVersion is the version of the draft the test started on.
	DraftVersion int64      `json:"draft_version"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	// Blocked: a composition conflict or an item that cannot be applied;
	// the draft cannot become tested.
	Blocked bool `json:"blocked"`
	// Outdated: the items changed while the test ran; the draft stays
	// draft.
	Outdated   bool             `json:"outdated"`
	Conflicts  []TestConflict   `json:"conflicts"`
	Totals     TestTotals       `json:"totals"`
	Types      []TestTypeCounts `json:"types"`
	Truncated  bool             `json:"truncated"`
	Violations []TestViolation  `json:"violations,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// TestConflict is a blocking finding: a composition conflict of a type or
// a template version (MET-16 code) or an item that cannot be applied.
type TestConflict struct {
	CITypeID          string `json:"ci_type_id,omitempty"`
	TemplateVersionID string `json:"template_version_id,omitempty"`
	ItemID            string `json:"item_id,omitempty"`
	Key               string `json:"key,omitempty"`
	Code              string `json:"code"`
	Message           string `json:"message"`
}

// TestTotals are the counters of a test run.
type TestTotals struct {
	Types      int `json:"types"`
	CIs        int `json:"cis"`
	InvalidCIs int `json:"invalid_cis"`
	Violations int `json:"violations"`
}

// TestTypeCounts are the counters of one CI type, with the violations per
// attribute.
type TestTypeCounts struct {
	CITypeID   string               `json:"ci_type_id"`
	CIs        int                  `json:"cis"`
	InvalidCIs int                  `json:"invalid_cis"`
	Violations int                  `json:"violations"`
	Attributes []TestAttributeCount `json:"attributes"`
}

// TestAttributeCount is the number of violations of one attribute.
type TestAttributeCount struct {
	Key        string `json:"key"`
	Violations int    `json:"violations"`
}

// TestViolation is one violation of a CI: no attribute value.
type TestViolation struct {
	CIID     string `json:"ci_id"`
	CITypeID string `json:"ci_type_id"`
	Field    string `json:"field"`
	Code     string `json:"code,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Message  string `json:"message"`
}

// Summary is the report without its violations: what the job result, the
// draft and the audit carry (job:read and citype:manage do not show CI
// ids, E-106).
func (t *TestReport) Summary() *TestReport {
	out := *t
	out.Violations = nil
	return &out
}

// summarizeReport removes the violations from a stored report (any JSON
// object); other JSON is kept.
func summarizeReport(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	if _, has := m["violations"]; !has {
		return raw
	}
	delete(m, "violations")
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

// reportJob reads job id and state of a stored report.
func reportJob(raw []byte) (jobID, state string) {
	var head struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &head) != nil {
		return "", ""
	}
	return head.JobID, head.State
}

// ─── The run ────────────────────────────────────────────────────────────────

// RunTest implements Repository: the test of the draft for job jobID,
// started on version. ctx carries the organization-wide scope of the job.
// The report is stored when the draft still names the job; the draft
// becomes tested when nothing blocks and its version is still version.
func (r *PGRepository) RunTest(ctx context.Context, orgID, draftID, jobID string, version int64) (*TestReport, error) {
	report, err := r.runTest(ctx, orgID, draftID, jobID, version)
	if err != nil {
		failed := &TestReport{JobID: jobID, State: TestStateFailed, DraftVersion: version, StartedAt: time.Now().UTC(),
			Conflicts: []TestConflict{}, Types: []TestTypeCounts{}, Error: "the test run failed"}
		if errors.Is(err, context.Canceled) {
			failed.Error = "the test run was cancelled"
		}
		if _, ferr := r.finishTest(context.WithoutCancel(ctx), orgID, draftID, failed); ferr != nil {
			err = errors.Join(err, ferr)
		}
		return nil, err
	}
	if _, err = r.finishTest(ctx, orgID, draftID, report); err != nil {
		return nil, err
	}
	return report.Summary(), nil
}

// finishTest stores the report under the lock of the draft when the draft
// still names the job (a later test or an item change replaced it) and is
// not final; a completed report without blocking finding on the unchanged
// version makes the draft tested.
func (r *PGRepository) finishTest(ctx context.Context, orgID, draftID string, report *TestReport) (bool, error) {
	stored := false
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var current int64
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT status, version, test_report FROM metamodel_draft WHERE id = $1 FOR UPDATE`, draftID).
			Scan(&status, &current, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock metamodel draft: %w", err)
		}
		if job, _ := reportJob(raw); job != report.JobID || status == StatusPublished || status == StatusDiscarded {
			return nil
		}
		report.Outdated = current != report.DraftVersion
		if report.State == TestStateCompleted && !report.Blocked && !report.Outdated {
			status = StatusTested
		} else if status == StatusTested {
			status = StatusDraft
		}
		body, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("encode test report: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE metamodel_draft SET test_report = $2, status = $3 WHERE id = $1`,
			draftID, body, status); err != nil {
			return fmt.Errorf("store test report: %w", err)
		}
		stored = true
		return nil
	})
	return stored, err
}

// runTest reads the draft and the metamodel, lays the items over it,
// composes the affected types and validates their CIs.
func (r *PGRepository) runTest(ctx context.Context, orgID, draftID, jobID string, version int64) (*TestReport, error) {
	report := &TestReport{JobID: jobID, State: TestStateCompleted, DraftVersion: version, StartedAt: time.Now().UTC(),
		Conflicts: []TestConflict{}, Types: []TestTypeCounts{}}
	var (
		o     *overlay
		items []Item
	)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftColumns+` FROM metamodel_draft WHERE id = $1`, draftID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDraftNotFound
		}
		if err != nil {
			return fmt.Errorf("read metamodel draft: %w", err)
		}
		items = d.Items
		if o, err = loadOverlay(database.ContextWithTx(ctx, tx), tx, r.catalog, orgID); err != nil {
			return err
		}
		for i := range items {
			if aerr := o.apply(ctx, tx, &items[i]); aerr != nil {
				var blocked *itemError
				if !errors.As(aerr, &blocked) {
					return aerr
				}
				report.Conflicts = append(report.Conflicts, TestConflict{ItemID: items[i].ID, Code: CodeItemNotApplicable,
					Message: blocked.Error()})
			}
		}
		o.finish()
		return o.compose(ctx, tx, report)
	})
	if err != nil {
		return nil, err
	}
	report.Blocked = len(report.Conflicts) > 0
	report.Totals.Types = len(o.validated)
	if !report.Blocked {
		if verr := r.validateCIs(ctx, orgID, o, report); verr != nil {
			return nil, verr
		}
	}
	now := time.Now().UTC()
	report.FinishedAt = &now
	return report, nil
}

// testCI is a CI of a batch.
type testCI struct {
	id, typeID string
	attributes map[string]any
	typed      map[string]string
	instance   bool
	versions   []string
}

// validateCIs validates the CIs of the validated types batch by batch.
func (r *PGRepository) validateCIs(ctx context.Context, orgID string, o *overlay, report *TestReport) error {
	types := make([]string, 0, len(o.validated))
	for id := range o.validated {
		if api.IsUUID(id) {
			types = append(types, id)
		}
	}
	sort.Strings(types)
	counts := map[string]*TestTypeCounts{}
	attrs := map[string]map[string]int{}
	for _, id := range types {
		counts[id] = &TestTypeCounts{CITypeID: id}
		attrs[id] = map[string]int{}
	}
	columns := ci.TypedColumnNames()
	selectTyped := make([]string, len(columns))
	for i, name := range columns {
		expr := "c." + name + "::text"
		if name == "management_ip" {
			expr = "host(c.management_ip)"
		}
		selectTyped[i] = "COALESCE(" + expr + ", '')"
	}
	after := "00000000-0000-0000-0000-000000000000"
	for len(types) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch []testCI
		err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT c.id::text, c.ci_type_id::text, c.attributes, `+strings.Join(selectTyped, ", ")+`,
					EXISTS (SELECT 1 FROM ci_instance_field_definition d WHERE d.ci_id = c.id),
					COALESCE((SELECT array_agg(tv.id::text ORDER BY ct.position) FROM ci_template ct
						JOIN template_version tv ON tv.template_id = ct.template_id AND tv.version = ct.version
						WHERE ct.ci_id = c.id), '{}'::text[])
				FROM ci c WHERE c.deleted_at IS NULL AND c.ci_type_id = ANY($1::uuid[]) AND c.id > $2::uuid
				ORDER BY c.id LIMIT $3`, types, after, testBatch)
			if err != nil {
				return fmt.Errorf("read cis of the test: %w", err)
			}
			batch, err = scanTestCIs(rows, columns)
			if err != nil {
				return err
			}
			txCtx := database.ContextWithTx(ctx, tx)
			for i := range batch {
				if verr := r.validateCI(txCtx, tx, orgID, o, &batch[i], report, counts, attrs); verr != nil {
					return verr
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(batch) < testBatch {
			break
		}
		after = batch[len(batch)-1].id
	}
	for _, id := range types {
		c := counts[id]
		c.Attributes = make([]TestAttributeCount, 0, len(attrs[id]))
		for key, n := range attrs[id] {
			c.Attributes = append(c.Attributes, TestAttributeCount{Key: key, Violations: n})
		}
		sort.Slice(c.Attributes, func(i, j int) bool { return c.Attributes[i].Key < c.Attributes[j].Key })
		report.Types = append(report.Types, *c)
	}
	return nil
}

// scanTestCIs reads the CIs of a batch.
func scanTestCIs(rows pgx.Rows, columns []string) ([]testCI, error) {
	defer rows.Close()
	var out []testCI
	for rows.Next() {
		var c testCI
		var raw []byte
		values := make([]string, len(columns))
		dest := []any{&c.id, &c.typeID, &raw}
		for i := range values {
			dest = append(dest, &values[i])
		}
		dest = append(dest, &c.instance, &c.versions)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("read ci of the test: %w", err)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.attributes); err != nil {
				return nil, fmt.Errorf("decode attributes of ci %s: %w", c.id, err)
			}
		}
		c.typed = make(map[string]string, len(columns))
		for i, name := range columns {
			c.typed[name] = values[i]
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// validateCI composes the layers of one CI and validates its values; a
// conflict with its templates or instance definitions is a violation of
// the CI.
func (r *PGRepository) validateCI(ctx context.Context, tx pgx.Tx, orgID string, o *overlay, c *testCI,
	report *TestReport, counts map[string]*TestTypeCounts, attrs map[string]map[string]int) error {
	layers := o.typeLayers(c.typeID)
	templates, err := o.versionItems(ctx, tx, c.versions)
	if err != nil {
		return err
	}
	var violations []TestViolation
	tl, err := o.templateLayers(layers, templates)
	var blocked *itemError
	switch {
	case errors.As(err, &blocked):
		// A template of the CI uses a definition the draft removes or
		// cannot compose: a violation of the CI.
		violations = append(violations, TestViolation{CIID: c.id, CITypeID: c.typeID, Code: CodeInvalidTemplateItem,
			Message: blocked.Error()})
	case err != nil:
		return fmt.Errorf("template layers of ci %s: %w", c.id, err)
	}
	layers = append(layers, tl...)
	if c.instance {
		fields, ferr := r.catalog.ListInstanceFields(ctx, orgID, c.id)
		if ferr != nil {
			return ferr
		}
		instance := make([]fieldmeta.FieldDefinition, 0, len(fields))
		for i := range fields {
			instance = append(instance, fields[i].Definition())
		}
		layers = append(layers, instance)
	}
	defs, conflicts := citype.ComposeAll(layers...)
	for _, conflict := range conflicts {
		violations = append(violations, TestViolation{CIID: c.id, CITypeID: c.typeID, Field: conflict.Key, Code: conflict.Code,
			Message: conflict.Error()})
	}
	if len(violations) == 0 {
		catalog, cerr := o.catalogRules(c.typeID, c.versions)
		if cerr != nil {
			return cerr
		}
		for _, v := range ci.ValidateAttributes(defs, catalog, ci.StoredValues(c.attributes, c.typed)) {
			violations = append(violations, TestViolation{CIID: c.id, CITypeID: c.typeID, Field: v.Field, Code: v.Code,
				Rule: v.Rule, Message: withoutValues(v.Detail)})
		}
	}
	count := counts[c.typeID]
	if count == nil {
		return nil
	}
	count.CIs++
	report.Totals.CIs++
	if len(violations) == 0 {
		return nil
	}
	count.InvalidCIs++
	count.Violations += len(violations)
	report.Totals.InvalidCIs++
	report.Totals.Violations += len(violations)
	for i := range violations {
		attrs[c.typeID][violations[i].Field]++
		if len(report.Violations) >= MaxTestViolations {
			report.Truncated = true
			continue
		}
		report.Violations = append(report.Violations, violations[i])
	}
	return nil
}

// valueInMessage finds the messages that quote the value of an attribute
// (rules: enum; fieldmeta: a value listed twice).
var valueInMessage = []struct {
	pattern *regexp.Regexp
	text    string
}{
	{regexp.MustCompile(`(?s)^value .* is not one of the allowed values$`), "value is not one of the allowed values"},
	{regexp.MustCompile(`(?s)^value .* is (listed|selected) twice$`), "a value is listed twice"},
}

// withoutValues is the message of a violation without the attribute value
// it may quote: the report names CIs and fields, never values.
func withoutValues(message string) string {
	for _, v := range valueInMessage {
		if v.pattern.MatchString(message) {
			return v.text
		}
	}
	return message
}

// ─── The overlay ───────────────────────────────────────────────────────────

// Catalog is what the test run reads through the metamodel repository
// (citype.PGRepository): the attribute definitions the organization sees
// and the instance definitions of a CI. Both join the transaction of the
// context (database.ContextWithTx).
type Catalog interface {
	ListAttributeDefinitions(ctx context.Context, orgID string, filter citype.AttributeFilter) ([]citype.AttributeDefinition, error)
	ListInstanceFields(ctx context.Context, orgID, ciID string) ([]citype.InstanceField, error)
}

// itemError: an item cannot be laid over the metamodel; it blocks the
// test like a conflict.
type itemError struct{ msg string }

func (e *itemError) Error() string { return e.msg }

func notApplicable(format string, args ...any) error {
	return &itemError{msg: fmt.Sprintf(format, args...)}
}

// typeNode is a CI type of the overlay.
type typeNode struct {
	parent, category string
}

// versionNode is a template version of the overlay: its template and its
// items (read lazily from template_version_item).
type versionNode struct {
	templateID string
	items      []citype.TemplateVersionItem
	loaded     bool
}

// ruleNode is a catalog rule of the overlay.
type ruleNode struct {
	id, key, attribute, kind string
	typeID, setID, versionID string
	condition, ast           json.RawMessage
	message                  citype.LocalizedText
	active                   bool
}

// overlay is the metamodel of the organization with the items of a draft
// laid over it: definitions by id (new parts by the id of their item),
// types with parent and category, set assignments in position order, set
// activity, rules, templates and template versions. It is a
// citype.FieldLister; typeLayers puts its layers in the order of
// citype.ResolveFields.
type overlay struct {
	defs      map[string]*citype.AttributeDefinition
	types     map[string]*typeNode
	assign    map[string][]string
	sets      map[string]bool
	rules     map[string]*ruleNode
	templates map[string]string
	versions  map[string]*versionNode
	// touched are the types an item changes directly; globals the global
	// definitions it changes (before and after), resolved by finish.
	touched  map[string]bool
	globals  []*citype.AttributeDefinition
	checked  []string
	composed map[string]bool
	// validated are the types whose CIs the test validates: the touched
	// types and their subtypes.
	validated map[string]bool
	cache     map[string][][]fieldmeta.FieldDefinition
}

// loadOverlay reads the metamodel the organization sees in tx.
func loadOverlay(ctx context.Context, tx pgx.Tx, catalog Catalog, orgID string) (*overlay, error) {
	o := &overlay{defs: map[string]*citype.AttributeDefinition{}, types: map[string]*typeNode{}, assign: map[string][]string{},
		sets: map[string]bool{}, rules: map[string]*ruleNode{}, templates: map[string]string{}, versions: map[string]*versionNode{},
		touched: map[string]bool{}, composed: map[string]bool{}, validated: map[string]bool{}}
	defs, lerr := catalog.ListAttributeDefinitions(ctx, orgID, citype.AttributeFilter{})
	if lerr != nil {
		return nil, lerr
	}
	for i := range defs {
		d := defs[i]
		o.defs[d.ID] = &d
	}
	for _, q := range []struct {
		sql  string
		scan func(rows pgx.Rows) error
	}{
		{`SELECT id::text, COALESCE(parent_type_id::text, ''), COALESCE(category, '') FROM ci_type`, func(rows pgx.Rows) error {
			var id string
			n := &typeNode{}
			if err := rows.Scan(&id, &n.parent, &n.category); err != nil {
				return err
			}
			o.types[id] = n
			return nil
		}},
		{`SELECT ci_type_id::text, attribute_set_id::text FROM ci_type_attribute_set ORDER BY ci_type_id, position, attribute_set_id`,
			func(rows pgx.Rows) error {
				var typeID, setID string
				if err := rows.Scan(&typeID, &setID); err != nil {
					return err
				}
				o.assign[typeID] = append(o.assign[typeID], setID)
				return nil
			}},
		{`SELECT id::text, is_active FROM attribute_set`, func(rows pgx.Rows) error {
			var id string
			var active bool
			if err := rows.Scan(&id, &active); err != nil {
				return err
			}
			o.sets[id] = active
			return nil
		}},
		{`SELECT id::text, ci_type_id::text FROM template`, func(rows pgx.Rows) error {
			var id, typeID string
			if err := rows.Scan(&id, &typeID); err != nil {
				return err
			}
			o.templates[id] = typeID
			return nil
		}},
		{`SELECT id::text, key, attribute_key, kind, COALESCE(ci_type_id::text, ''), COALESCE(attribute_set_id::text, ''),
				COALESCE(template_version_id::text, ''), condition, ast, message, is_active FROM metamodel_rule`,
			func(rows pgx.Rows) error {
				n := &ruleNode{}
				var message []byte
				if err := rows.Scan(&n.id, &n.key, &n.attribute, &n.kind, &n.typeID, &n.setID, &n.versionID,
					&n.condition, &n.ast, &message, &n.active); err != nil {
					return err
				}
				if len(message) > 0 && string(message) != "null" {
					if err := json.Unmarshal(message, &n.message); err != nil {
						return err
					}
				}
				o.rules[n.id] = n
				return nil
			}},
	} {
		if qerr := queryEach(ctx, tx, q.sql, q.scan); qerr != nil {
			return nil, fmt.Errorf("read the metamodel of the test: %w", qerr)
		}
	}
	return o, nil
}

func queryEach(ctx context.Context, tx pgx.Tx, query string, scan func(rows pgx.Rows) error) error {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if serr := scan(rows); serr != nil {
			return serr
		}
	}
	return rows.Err()
}

// decodeItem decodes the payload of an item with its item references
// replaced by the referenced ids, which are the ids of the new parts in the
// overlay.
func decodeItem(it *Item, v any) error {
	raw := bytes.TrimSpace(it.Payload)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return notApplicable("item %s: payload: %v", it.ID, err)
	}
	plain, _, err := substituteRefs(generic)
	if err != nil {
		return notApplicable("item %s: %v", it.ID, err)
	}
	body, err := json.Marshal(plain)
	if err != nil {
		return notApplicable("item %s: payload: %v", it.ID, err)
	}
	if err = json.Unmarshal(body, v); err != nil {
		return notApplicable("item %s: payload: %v", it.ID, err)
	}
	return nil
}

func itemTarget(it *Item) string {
	if it.TargetID == nil {
		return ""
	}
	return *it.TargetID
}

func targetKey(it *Item) string {
	if it.TargetKey == nil {
		return ""
	}
	return *it.TargetKey
}

// apply lays one item over the metamodel and records what it touches. An
// item that no longer fits is an *itemError.
func (o *overlay) apply(ctx context.Context, tx pgx.Tx, it *Item) error {
	switch it.Kind {
	case KindCIType:
		return o.applyType(it)
	case KindCITypeField, KindGlobalField:
		return o.applyField(it)
	case KindAttributeDefinition:
		return o.applyDefinition(it)
	case KindAttributeSet:
		return o.applySet(it)
	case KindTemplate:
		return o.applyTemplate(it)
	case KindTemplateVersion:
		return o.applyTemplateVersion(ctx, tx, it)
	case KindRule:
		return o.applyRule(ctx, tx, it)
	}
	// layout: presentation only (WP-259).
	return nil
}

func (o *overlay) applyType(it *Item) error {
	id := itemTarget(it)
	switch it.Op {
	case OpCreate:
		var in citype.CreateTypeRequest
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		o.types[it.ID] = &typeNode{parent: strings.TrimSpace(in.ParentTypeID), category: in.Category}
		for i := range in.Fields {
			f := in.Fields[i]
			d := &citype.AttributeDefinition{ID: it.ID + "/" + f.Name, OrganizationID: "draft", CITypeID: it.ID, Scope: "type",
				UniqueScope: "org", Inheritable: true}
			fieldInto(d, &citype.UpsertFieldRequest{Name: f.Name, Label: f.Label, Description: f.Description, DataType: f.DataType,
				Required: f.Required, DefaultValue: f.DefaultValue, EnumValues: f.EnumValues, UIGroup: f.UIGroup,
				SortOrder: f.SortOrder, Validation: f.Validation, Conditional: f.Conditional, ReferenceTarget: f.ReferenceTarget})
			o.defs[d.ID] = d
		}
		o.touched[it.ID] = true
	case OpClone:
		source, ok := o.types[id]
		if !ok {
			return notApplicable("item %s: the ci type %s does not exist", it.ID, id)
		}
		o.types[it.ID] = &typeNode{parent: source.parent, category: source.category}
		o.assign[it.ID] = slices.Clone(o.assign[id])
		for _, d := range o.sortedDefs(func(d *citype.AttributeDefinition) bool { return d.CITypeID == id }) {
			c := *d
			c.ID, c.CITypeID, c.OrganizationID = it.ID+"/"+d.Key, it.ID, "draft"
			o.defs[c.ID] = &c
		}
		o.touched[it.ID] = true
	case OpUpdate:
		node, ok := o.types[id]
		if !ok {
			return notApplicable("item %s: the ci type %s does not exist", it.ID, id)
		}
		var in citype.UpdateTypeRequest
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		if in.ParentTypeID != nil {
			node.parent = strings.TrimSpace(*in.ParentTypeID)
		}
		if in.Category != nil {
			node.category = *in.Category
		}
		o.touched[id] = true
	case OpAssign:
		if _, ok := o.types[id]; !ok {
			return notApplicable("item %s: the ci type %s does not exist", it.ID, id)
		}
		var in TypeAttributeSetsInput
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		list := slices.Clone(in.AttributeSets)
		sort.SliceStable(list, func(i, j int) bool { return list[i].Position < list[j].Position })
		sets := make([]string, 0, len(list))
		for _, a := range list {
			sets = append(sets, a.AttributeSetID)
		}
		o.assign[id] = sets
		o.touched[id] = true
	}
	// activate and deactivate leave the composition and the CIs as they are.
	return nil
}

// fieldInto sets the definition of a field request on d, as PUT
// /ci-types/{id}/fields and PUT /ci-fields/global do.
func fieldInto(d *citype.AttributeDefinition, req *citype.UpsertFieldRequest) {
	d.Key = req.Name
	d.DisplayName = nil
	if strings.TrimSpace(req.Label) != "" {
		d.DisplayName = citype.LocalizedText{"de": req.Label, "en": req.Label}
	}
	d.Description, d.DataType, d.Required = req.Description, req.DataType, req.Required
	d.Default = nil
	if req.DefaultValue != "" {
		if raw, err := json.Marshal(req.DefaultValue); err == nil {
			d.Default = raw
		}
	}
	d.EnumValues, d.Group, d.Order = req.EnumValues, req.UIGroup, req.SortOrder
	d.Validation, d.Conditional, d.ReferenceTarget = req.Validation.AST(), req.Conditional, req.ReferenceTarget
}

// findField returns the field of a type (typeID) or the global field of
// the organization (typeID "") named key.
func (o *overlay) findField(typeID, key string) *citype.AttributeDefinition {
	for _, d := range o.defs {
		switch {
		case d.Key != key:
		case typeID != "" && d.CITypeID == typeID:
			return d
		case typeID == "" && d.Scope == "global" && !d.IsSystem:
			return d
		}
	}
	return nil
}

func (o *overlay) applyField(it *Item) error {
	typeID, key := "", targetKey(it)
	if it.Kind == KindCITypeField {
		typeID = itemTarget(it)
		if _, ok := o.types[typeID]; !ok {
			return notApplicable("item %s: the ci type %s does not exist", it.ID, typeID)
		}
	}
	existing := o.findField(typeID, key)
	if existing != nil {
		o.touchDefinition(existing)
	}
	if it.Op == OpDelete {
		if existing == nil {
			return notApplicable("item %s: the field %s does not exist", it.ID, key)
		}
		delete(o.defs, existing.ID)
		return nil
	}
	var req citype.UpsertFieldRequest
	if err := decodeItem(it, &req); err != nil {
		return err
	}
	d := existing
	if d == nil {
		scope := "type"
		if typeID == "" {
			scope = "global"
		}
		d = &citype.AttributeDefinition{ID: it.ID, OrganizationID: "draft", CITypeID: typeID, Scope: scope, UniqueScope: "org",
			Inheritable: true, AppliesTo: citype.AppliesToAll}
		o.defs[d.ID] = d
	} else {
		c := *existing
		d = &c
		o.defs[d.ID] = d
	}
	fieldInto(d, &req)
	o.touchDefinition(d)
	return nil
}

// applyPatch changes d like PATCH /attribute-definitions/{id}.
func applyPatch(d *citype.AttributeDefinition, p *citype.AttributeDefinitionPatch) error {
	if p.Validation != nil {
		rule, err := fieldmeta.ParseValidation(p.Validation)
		if err != nil {
			return err
		}
		d.Validation = rule
	}
	if p.Computed != nil {
		d.Computed = nil
		if string(p.Computed) != "null" {
			expr, err := rules.ParseExpr(p.Computed)
			if err != nil {
				return err
			}
			d.Computed = &expr
		}
	}
	if p.DisplayName != nil {
		d.DisplayName = p.DisplayName
	}
	for _, s := range []struct{ dst, v *string }{
		{&d.Description, p.Description}, {&d.DataType, p.DataType}, {&d.UniqueScope, p.UniqueScope},
		{&d.ReferenceTarget, p.ReferenceTarget}, {&d.Group, p.Group}, {&d.Unit, p.Unit},
	} {
		if s.v != nil {
			*s.dst = *s.v
		}
	}
	for _, b := range []struct{ dst, v *bool }{
		{&d.Required, p.Required}, {&d.MultiValue, p.MultiValue}, {&d.Searchable, p.Searchable}, {&d.Unique, p.Unique},
		{&d.Inheritable, p.Inheritable},
	} {
		if b.v != nil {
			*b.dst = *b.v
		}
	}
	if p.Default != nil {
		d.Default = p.Default
	}
	if p.EnumValues != nil {
		d.EnumValues = p.EnumValues
	}
	if p.Conditional != nil {
		d.Conditional = p.Conditional
	}
	if p.Order != nil {
		d.Order = *p.Order
	}
	if p.AppliesTo != nil {
		if *p.AppliesTo != d.AppliesTo {
			d.AppliesToCategories, d.AppliesToTypeIDs = nil, nil
		}
		d.AppliesTo = *p.AppliesTo
	}
	if p.AppliesToCategories != nil {
		d.AppliesToCategories = p.AppliesToCategories
	}
	if p.AppliesToTypeIDs != nil {
		d.AppliesToTypeIDs = p.AppliesToTypeIDs
	}
	return nil
}

func (o *overlay) applyDefinition(it *Item) error {
	id := itemTarget(it)
	switch it.Op {
	case OpCreate:
		var in AttributeDefinitionCreate
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		d := &citype.AttributeDefinition{ID: it.ID, OrganizationID: "draft", CITypeID: strings.TrimSpace(in.CITypeID),
			AttributeSetID: strings.TrimSpace(in.AttributeSetID), Key: strings.TrimSpace(in.Key), UniqueScope: "org",
			Inheritable: true, AppliesTo: citype.AppliesToAll}
		switch {
		case d.CITypeID != "":
			d.Scope = "type"
		case d.AttributeSetID != "":
			d.Scope = "set"
		default:
			d.Scope = "global"
		}
		if err := applyPatch(d, &in.AttributeDefinitionPatch); err != nil {
			return notApplicable("item %s: %v", it.ID, err)
		}
		o.defs[d.ID] = d
		o.touchDefinition(d)
		return nil
	case OpUpdate, OpDelete:
		existing, ok := o.defs[id]
		if !ok {
			return notApplicable("item %s: the attribute definition %s does not exist", it.ID, id)
		}
		o.touchDefinition(existing)
		if it.Op == OpDelete {
			delete(o.defs, id)
			return nil
		}
		var p citype.AttributeDefinitionPatch
		if err := decodeItem(it, &p); err != nil {
			return err
		}
		c := *existing
		if err := applyPatch(&c, &p); err != nil {
			return notApplicable("item %s: %v", it.ID, err)
		}
		o.defs[id] = &c
		o.touchDefinition(&c)
	}
	return nil
}

func (o *overlay) applySet(it *Item) error {
	id := itemTarget(it)
	switch it.Op {
	case OpCreate:
		var in AttributeSetCreate
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		o.sets[it.ID] = in.IsActive == nil || *in.IsActive
		return nil
	case OpUpdate:
		if _, ok := o.sets[id]; !ok {
			return notApplicable("item %s: the attribute set %s does not exist", it.ID, id)
		}
		var p citype.AttributeSetPatch
		if err := decodeItem(it, &p); err != nil {
			return err
		}
		if p.IsActive != nil {
			o.sets[id] = *p.IsActive
		}
		o.touchSet(id)
	case OpDelete:
		if _, ok := o.sets[id]; !ok {
			return notApplicable("item %s: the attribute set %s does not exist", it.ID, id)
		}
		o.touchSet(id)
		delete(o.sets, id)
		for typeID, sets := range o.assign {
			o.assign[typeID] = slices.DeleteFunc(sets, func(s string) bool { return s == id })
		}
		for defID, d := range o.defs {
			if d.AttributeSetID == id {
				delete(o.defs, defID)
			}
		}
	}
	return nil
}

func (o *overlay) applyTemplate(it *Item) error {
	switch it.Op {
	case OpCreate:
		var in TemplateCreate
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		o.templates[it.ID] = in.CITypeID
	case OpClone:
		o.templates[it.ID] = o.templates[itemTarget(it)]
	}
	// update and delete leave the versions the CIs use as they are (WP-710).
	return nil
}

// applyTemplateVersion lays a template version item over the metamodel; the
// test composes the version with its type (only composition: the CIs stay on
// their versions, WP-710; their migration is WP-253).
func (o *overlay) applyTemplateVersion(ctx context.Context, tx pgx.Tx, it *Item) error {
	id := itemTarget(it)
	switch it.Op {
	case OpCreate:
		templateID := id
		if _, ok := o.templates[templateID]; !ok {
			return notApplicable("item %s: the template %s does not exist", it.ID, templateID)
		}
		var current string
		err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT v.id::text FROM template_version v JOIN template t ON t.id = v.template_id
			WHERE t.id = $1 AND v.version = t.current_version), '')`, templateID).Scan(&current)
		if err != nil {
			return fmt.Errorf("read the current template version: %w", err)
		}
		node := &versionNode{templateID: templateID, loaded: true}
		if current != "" {
			items, lerr := o.versionItems(ctx, tx, []string{current})
			if lerr != nil {
				return lerr
			}
			node.items = items[0]
		}
		o.versions[it.ID] = node
		o.checked = append(o.checked, it.ID)
	case OpUpdate:
		node, err := o.version(ctx, tx, id)
		if err != nil {
			return err
		}
		if node == nil {
			return notApplicable("item %s: the template version %s does not exist", it.ID, id)
		}
		var in TemplateVersionItemsInput
		if derr := decodeItem(it, &in); derr != nil {
			return derr
		}
		items := make([]citype.TemplateVersionItem, 0, len(in.Items))
		for i := range in.Items {
			items = append(items, in.Items[i].TemplateVersionItem)
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
		node.items, node.loaded = items, true
		o.checked = append(o.checked, id)
	case OpPublish:
		node, err := o.version(ctx, tx, id)
		if err != nil {
			return err
		}
		if node == nil {
			return notApplicable("item %s: the template version %s does not exist", it.ID, id)
		}
		o.checked = append(o.checked, id)
	}
	return nil
}

// version returns the template version id of the overlay, read on first
// use; nil when it does not exist.
func (o *overlay) version(ctx context.Context, tx pgx.Tx, id string) (*versionNode, error) {
	if node, ok := o.versions[id]; ok {
		return node, nil
	}
	if !api.IsUUID(id) {
		return nil, nil
	}
	var templateID string
	err := tx.QueryRow(ctx, `SELECT template_id::text FROM template_version WHERE id = $1`, id).Scan(&templateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read template version: %w", err)
	}
	node := &versionNode{templateID: templateID}
	o.versions[id] = node
	return node, nil
}

// versionItems returns the items of the template versions ids in order,
// read from template_version_item where the overlay has none.
func (o *overlay) versionItems(ctx context.Context, tx pgx.Tx, ids []string) ([][]citype.TemplateVersionItem, error) {
	out := make([][]citype.TemplateVersionItem, 0, len(ids))
	for _, id := range ids {
		node, err := o.version(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if node == nil {
			out = append(out, nil)
			continue
		}
		if !node.loaded {
			rows, qerr := tx.Query(ctx, `SELECT attribute_definition_id::text, position, required_override, default_override,
					validation_override FROM template_version_item WHERE template_version_id = $1 ORDER BY position`, id)
			if qerr != nil {
				return nil, fmt.Errorf("read template version items: %w", qerr)
			}
			items, cerr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (citype.TemplateVersionItem, error) {
				var item citype.TemplateVersionItem
				var def, validation []byte
				serr := row.Scan(&item.AttributeDefinitionID, &item.Position, &item.RequiredOverride, &def, &validation)
				if len(def) > 0 && string(def) != "null" {
					item.DefaultOverride = def
				}
				if len(validation) > 0 && string(validation) != "null" {
					item.ValidationOverride = validation
				}
				return item, serr
			})
			if cerr != nil {
				return nil, fmt.Errorf("read template version items: %w", cerr)
			}
			node.items, node.loaded = items, true
		}
		out = append(out, node.items)
	}
	return out, nil
}

func (o *overlay) applyRule(ctx context.Context, tx pgx.Tx, it *Item) error {
	id := itemTarget(it)
	switch it.Op {
	case OpCreate:
		var in citype.RuleInput
		if err := decodeItem(it, &in); err != nil {
			return err
		}
		n := &ruleNode{id: it.ID, key: strings.TrimSpace(in.Key), attribute: strings.TrimSpace(in.AttributeKey), kind: in.Kind,
			typeID: strings.TrimSpace(in.CITypeID), setID: strings.TrimSpace(in.AttributeSetID),
			versionID: strings.TrimSpace(in.TemplateVersionID), condition: in.Condition, ast: in.AST,
			active: in.IsActive == nil || *in.IsActive}
		if err := n.setMessage(in.Message); err != nil {
			return notApplicable("item %s: message: %v", it.ID, err)
		}
		o.rules[n.id] = n
		return o.touchRule(ctx, tx, n)
	case OpUpdate:
		existing, ok := o.rules[id]
		if !ok {
			return notApplicable("item %s: the rule %s does not exist", it.ID, id)
		}
		var p citype.RulePatch
		if err := decodeItem(it, &p); err != nil {
			return err
		}
		n := *existing
		if p.AttributeKey != nil {
			n.attribute = strings.TrimSpace(*p.AttributeKey)
		}
		if p.Kind != nil {
			n.kind = *p.Kind
			if n.kind != citype.RuleKindValidation && p.AST == nil {
				n.ast = nil
			}
		}
		if p.Condition != nil {
			n.condition = p.Condition
		}
		if p.AST != nil {
			n.ast = p.AST
		}
		if p.Message != nil {
			if err := n.setMessage(p.Message); err != nil {
				return notApplicable("item %s: message: %v", it.ID, err)
			}
		}
		if p.IsActive != nil {
			n.active = *p.IsActive
		}
		o.rules[id] = &n
		return o.touchRule(ctx, tx, &n)
	case OpDelete:
		existing, ok := o.rules[id]
		if !ok {
			return notApplicable("item %s: the rule %s does not exist", it.ID, id)
		}
		delete(o.rules, id)
		return o.touchRule(ctx, tx, existing)
	}
	return nil
}

// setMessage sets the message of a rule from a request: a map of languages
// or a text for both languages; null removes it.
func (n *ruleNode) setMessage(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		n.message = nil
		return nil
	}
	var m citype.LocalizedText
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	n.message = m
	return nil
}

// ─── What an item touches ──────────────────────────────────────────────────

// touchDefinition records the types a definition affects: its type, the
// types its set is assigned to, or (global) every type it applies to.
func (o *overlay) touchDefinition(d *citype.AttributeDefinition) {
	switch {
	case d.CITypeID != "":
		o.touched[d.CITypeID] = true
	case d.AttributeSetID != "":
		o.touchSet(d.AttributeSetID)
	default:
		c := *d
		o.globals = append(o.globals, &c)
	}
}

// touchSet records the types a set is assigned to.
func (o *overlay) touchSet(setID string) {
	for typeID, sets := range o.assign {
		if slices.Contains(sets, setID) {
			o.touched[typeID] = true
		}
	}
}

// touchRule records the target of a rule: its type, the types of its set,
// the type of the template of its template version (and so the subtypes
// whose CIs may carry the version).
func (o *overlay) touchRule(ctx context.Context, tx pgx.Tx, n *ruleNode) error {
	switch {
	case n.typeID != "":
		o.touched[n.typeID] = true
	case n.setID != "":
		o.touchSet(n.setID)
	case n.versionID != "":
		node, err := o.version(ctx, tx, n.versionID)
		if err != nil || node == nil {
			return err
		}
		o.touched[o.templates[node.templateID]] = true
	}
	return nil
}

// finish resolves what the items touched: the global definitions to the
// types they apply to (before or after the change), the touched types to
// their subtrees.
func (o *overlay) finish() {
	for _, g := range o.globals {
		for id := range o.types {
			if o.applies(g, id) {
				o.touched[id] = true
			}
		}
	}
	for id := range o.types {
		for _, ancestor := range o.chain(id) {
			if o.touched[ancestor] {
				o.validated[id] = true
				break
			}
		}
	}
	for id := range o.touched {
		if _, ok := o.types[id]; ok {
			o.validated[id] = true
		}
	}
}

// chain returns the types root → id (MET-06); a cycle ends it.
func (o *overlay) chain(id string) []string {
	var out []string
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		node, ok := o.types[cur]
		if !ok {
			break
		}
		seen[cur] = true
		out = append(out, cur)
		cur = node.parent
	}
	slices.Reverse(out)
	return out
}

// applies reports whether the global definition d applies to the type id
// (MET-13, WP-255, the rule of citype): all, the category of the type, or
// the type or one of its ancestors listed.
func (o *overlay) applies(d *citype.AttributeDefinition, id string) bool {
	return d.IsSystem || o.appliesTo(d.AppliesTo, d.AppliesToCategories, d.AppliesToTypeIDs, id)
}

// appliesTo is the applicability of a global attribute of the
// organization with mode, categories and typeIDs to the type id.
func (o *overlay) appliesTo(mode string, categories, typeIDs []string, id string) bool {
	switch mode {
	case "", citype.AppliesToAll:
		return true
	case citype.AppliesToCategories:
		node := o.types[id]
		return node != nil && node.category != "" && slices.Contains(categories, node.category)
	case citype.AppliesToTypes:
		for _, t := range o.chain(id) {
			if slices.ContainsFunc(typeIDs, func(x string) bool { return strings.EqualFold(x, t) }) {
				return true
			}
		}
	}
	return false
}

// ─── Composition ──────────────────────────────────────────────────────────

// sortedDefs returns the definitions match selects by sort order and key,
// as the repository reads them.
func (o *overlay) sortedDefs(match func(d *citype.AttributeDefinition) bool) []*citype.AttributeDefinition {
	var out []*citype.AttributeDefinition
	for _, d := range o.defs {
		if match(d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// typeLayers returns the layers of a type in the order of
// citype.ResolveFields: system base → the organization's global
// attributes that apply to it → type chain (of the ancestors the
// inheritable definitions) → one layer per active set of the chain in
// position order (of the sets of an ancestor the inheritable attributes).
// The result is cached; callers append to a copy.
func (o *overlay) typeLayers(typeID string) [][]fieldmeta.FieldDefinition {
	if o.cache == nil {
		o.cache = map[string][][]fieldmeta.FieldDefinition{}
	}
	if layers, ok := o.cache[typeID]; ok {
		return slices.Clone(layers)
	}
	ctx := context.Background()
	var system, org, typed []fieldmeta.FieldDefinition
	globals, _ := o.ListGlobalFields(ctx, "")
	for i := range globals {
		f := &globals[i]
		switch {
		case f.IsSystem:
			system = append(system, f.Definition())
		case o.appliesTo(f.AppliesTo, f.AppliesToCategories, f.AppliesToTypeIDs, typeID):
			org = append(org, f.Definition())
		}
	}
	chain, _ := o.TypeChain(ctx, "", typeID)
	for i := range chain {
		own := i == len(chain)-1
		for j := range chain[i].Fields {
			if own || chain[i].Fields[j].Inheritable {
				typed = append(typed, chain[i].Fields[j].Definition())
			}
		}
	}
	layers := [][]fieldmeta.FieldDefinition{system, org, typed}
	sets, _ := o.ListSetFields(ctx, "", typeID)
	for _, set := range sets {
		layer := make([]fieldmeta.FieldDefinition, 0, len(set))
		for i := range set {
			layer = append(layer, set[i].Definition())
		}
		layers = append(layers, layer)
	}
	o.cache[typeID] = layers
	return slices.Clone(layers)
}

// templateLayers turns the items of template versions into their layers
// between the sets and the instance, as citype does (WP-710): one layer per
// version with the overrides of each item; a key no lower layer and no
// earlier template defines brings its definition first. The definitions
// are those of the overlay, so a draft change of a definition reaches the
// templates that use it. An item whose definition is gone or whose
// validation override is invalid is an *itemError.
func (o *overlay) templateLayers(lower [][]fieldmeta.FieldDefinition,
	versions [][]citype.TemplateVersionItem) ([][]fieldmeta.FieldDefinition, error) {
	present := map[string]bool{}
	for _, layer := range lower {
		for i := range layer {
			present[layer[i].Name] = true
		}
	}
	out := make([][]fieldmeta.FieldDefinition, 0, len(versions))
	for _, items := range versions {
		layer := make([]fieldmeta.FieldDefinition, 0, 2*len(items))
		for i := range items {
			d, ok := o.defs[items[i].AttributeDefinitionID]
			if !ok {
				return nil, notApplicable("template item: the attribute definition %s does not exist", items[i].AttributeDefinitionID)
			}
			base, top, err := templateItemLayers(d, &items[i])
			if err != nil {
				return nil, notApplicable("template item %s: %v", d.Key, err)
			}
			if !present[base.Name] {
				layer = append(layer, base)
				present[base.Name] = true
			}
			layer = append(layer, top)
		}
		out = append(out, layer)
	}
	return out, nil
}

// templateItemLayers is the definition of a template item and its
// override layer (citype: required is OR-ed through the composition, the
// default override replaces the default, the validation override
// composes).
func templateItemLayers(d *citype.AttributeDefinition, item *citype.TemplateVersionItem) (base, top fieldmeta.FieldDefinition, err error) {
	f := d.Field()
	base = f.Definition()
	top = base
	top.Required = item.RequiredOverride != nil && *item.RequiredOverride
	top.DefaultValue = ""
	if raw := item.DefaultOverride; len(raw) > 0 && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			top.DefaultValue = s
		} else {
			top.DefaultValue = string(raw)
		}
	}
	top.Validation = nil
	rule, err := fieldmeta.ParseValidation(item.ValidationOverride)
	if err != nil {
		return base, top, err
	}
	if rule != nil {
		if verr := rule.Validate(); verr != nil {
			return base, top, verr
		}
		v, required, verr := fieldmeta.ValidationFromRule(rule)
		if verr != nil {
			return base, top, verr
		}
		top.Validation = v
		top.Required = top.Required || required
	}
	return base, top, nil
}

// compose composes every validated type and every checked template version
// with citype.ComposeAll; each conflict blocks the test.
func (o *overlay) compose(ctx context.Context, tx pgx.Tx, report *TestReport) error {
	types := make([]string, 0, len(o.validated))
	for id := range o.validated {
		types = append(types, id)
	}
	sort.Strings(types)
	for _, id := range types {
		_, conflicts := citype.ComposeAll(o.typeLayers(id)...)
		for _, c := range conflicts {
			report.Conflicts = append(report.Conflicts, TestConflict{CITypeID: id, Key: c.Key, Code: c.Code, Message: c.Error()})
		}
	}
	seen := map[string]bool{}
	for _, versionID := range o.checked {
		if seen[versionID] {
			continue
		}
		seen[versionID] = true
		node := o.versions[versionID]
		typeID := o.templates[node.templateID]
		items, err := o.versionItems(ctx, tx, []string{versionID})
		if err != nil {
			return err
		}
		lower := o.typeLayers(typeID)
		upper, err := o.templateLayers(lower, items)
		if err != nil {
			var blocked *itemError
			if !errors.As(err, &blocked) {
				return err
			}
			report.Conflicts = append(report.Conflicts, TestConflict{CITypeID: typeID, TemplateVersionID: versionID,
				Code: CodeInvalidTemplateItem, Message: blocked.Error()})
			continue
		}
		_, conflicts := citype.ComposeAll(append(lower, upper...)...)
		for _, c := range conflicts {
			report.Conflicts = append(report.Conflicts, TestConflict{CITypeID: typeID, TemplateVersionID: versionID,
				Key: c.Key, Code: c.Code, Message: c.Error()})
		}
	}
	return nil
}

// catalogRules returns the active catalog rules of a CI of typeID with the
// template versions versions, as citype.ResolveRules orders them: the
// rules of the type chain root → type, of the active sets of the chain in
// position order, of the template versions in position order; within a
// target by key and id.
func (o *overlay) catalogRules(typeID string, versions []string) ([]fieldmeta.CatalogRule, error) {
	if len(o.rules) == 0 {
		return nil, nil
	}
	chain := o.chain(typeID)
	targets := slices.Clone(chain)
	for _, level := range chain {
		for _, setID := range o.assign[level] {
			if o.sets[setID] {
				targets = append(targets, setID)
			}
		}
	}
	targets = append(targets, versions...)
	rank := map[string]int{}
	for _, id := range targets {
		if _, seen := rank[id]; !seen {
			rank[id] = len(rank)
		}
	}
	position := func(n *ruleNode) (int, bool) {
		for _, id := range []string{n.typeID, n.setID, n.versionID} {
			if pos, ok := rank[id]; ok && id != "" {
				return pos, true
			}
		}
		return 0, false
	}
	var found []*ruleNode
	for _, n := range o.rules {
		if _, ok := position(n); ok && n.active {
			found = append(found, n)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		pi, _ := position(found[i])
		pj, _ := position(found[j])
		if pi != pj {
			return pi < pj
		}
		if found[i].key != found[j].key {
			return found[i].key < found[j].key
		}
		return found[i].id < found[j].id
	})
	out := make([]fieldmeta.CatalogRule, 0, len(found))
	for _, n := range found {
		rule := fieldmeta.CatalogRule{Key: n.key, AttributeKey: n.attribute, Kind: n.kind}
		if len(n.message) > 0 {
			rule.Message = map[string]string(n.message)
		}
		if len(n.condition) > 0 && string(n.condition) != "null" {
			cond, err := rules.ParseCondition(n.condition)
			if err != nil {
				return nil, fmt.Errorf("rule %s: condition: %w", n.key, err)
			}
			rule.Condition = &cond
		}
		if len(n.ast) > 0 && string(n.ast) != "null" {
			ast, err := rules.Parse(n.ast)
			if err != nil {
				return nil, fmt.Errorf("rule %s: ast: %w", n.key, err)
			}
			rule.AST = &ast
		}
		out = append(out, rule)
	}
	return out, nil
}

// ─── The overlay as citype.FieldLister ─────────────────────────────────────

// fieldsOf returns the definitions as legacy fields.
func fieldsOf(defs []*citype.AttributeDefinition) []citype.Field {
	out := make([]citype.Field, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Field())
	}
	return out
}

// ListGlobalFields implements citype.FieldLister: the system's and the
// organization's global attributes with the items laid over them.
func (o *overlay) ListGlobalFields(_ context.Context, _ string) ([]citype.Field, error) {
	return fieldsOf(o.sortedDefs(func(d *citype.AttributeDefinition) bool { return d.Scope == "global" })), nil
}

// TypeChain implements citype.FieldLister.
func (o *overlay) TypeChain(_ context.Context, _, typeID string) ([]citype.Type, error) {
	chain := o.chain(typeID)
	out := make([]citype.Type, 0, len(chain))
	for _, id := range chain {
		out = append(out, citype.Type{ID: id, Category: o.types[id].category,
			Fields: fieldsOf(o.sortedDefs(func(d *citype.AttributeDefinition) bool { return d.CITypeID == id }))})
	}
	return out, nil
}

// TypeCategory implements citype.FieldLister.
func (o *overlay) TypeCategory(_ context.Context, _, typeID string) (string, error) {
	if node, ok := o.types[typeID]; ok {
		return node.category, nil
	}
	return "", nil
}

// ListSetFields implements citype.FieldLister.
func (o *overlay) ListSetFields(_ context.Context, _, typeID string) ([][]citype.Field, error) {
	chain := o.chain(typeID)
	var out [][]citype.Field
	for i, level := range chain {
		own := i == len(chain)-1
		for _, setID := range o.assign[level] {
			if !o.sets[setID] {
				continue
			}
			layer := o.sortedDefs(func(d *citype.AttributeDefinition) bool {
				return d.AttributeSetID == setID && (own || d.Inheritable)
			})
			if len(layer) > 0 {
				out = append(out, fieldsOf(layer))
			}
		}
	}
	return out, nil
}

// ListInstanceFields implements citype.FieldLister: the overlay keeps no
// instance definitions; the test reads them per CI.
func (o *overlay) ListInstanceFields(_ context.Context, _, _ string) ([]citype.InstanceField, error) {
	return nil, nil
}

var _ citype.FieldLister = (*overlay)(nil)

// ─── The job ────────────────────────────────────────────────────────────────

// testContext is ctx with the tenant and the organization-wide scope of
// the test job (no user): the job checks the CIs of every client.
func testContext(ctx context.Context, orgID string) context.Context {
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID})
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
}
