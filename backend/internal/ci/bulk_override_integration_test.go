package ci_test

// The test matrix of OVR-03 over the bulk operations and the template
// migration (WP-306) against PostgreSQL, wired like the undo of WP-783
// (newRevertExtEnv: bulk jobs also below 100 ids, the reverter of ci.bulk
// and the kind bulk.revert).
//
// Operation {set_status, set_fields, set_location} × state before the bulk
// {no override, override, override with value null, observed value only} ×
// {bulk, bulk + revert, bulk + ingest + revert}, each synchronous (< 100
// ids, a job ci.bulk of the request) and as a job (100 ids):
//
//   - bulk: every field takes the bulk value with a protected override of
//     the caller, reason bulk:<job_id>; the version rises by one; per CI
//     one audit entry (ci.updated/ci.status_changed) with correlation_id
//     <job_id> and the request id of the bulk request, one event
//     ci.changed; the bulk request counts as audited (audit.Recorded).
//   - ingest: an automated write and an observed value after the bulk
//     change neither the value nor the override.
//   - revert: per field the override state before the bulk comes back in
//     the transaction of the inverse write: an override the bulk created
//     goes and the field falls back to the observed value (a structural
//     column like location_id keeps the old value); an override from
//     before gets value, author, reason, protection and time back. The
//     version rises once (the inverse write), no ci_change row override.
//   - an override changed since the bulk is skipped in the revert report
//     and stays with the value.
//
// change_template (template.migrate of WP-253 through the bulk): no row of
// ci_field_value with override_at changes, an overridden field (also with
// value null) gets no default; the revert removes the defaults without
// leaving an override and keeps the overrides.
//
// set_fields on an asset-owned field of a linked CI is 422 per id.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run 'TestBulkOverride' ./internal/ci/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
)

// States of a field before the bulk.
const (
	ovrNone     = "none"
	ovrSet      = "override"
	ovrNull     = "override-null"
	ovrObserved = "observed"
	// ovrPreAt is the time of the overrides from before the bulk.
	ovrPreAt = "2026-01-02T03:04:05.123456Z"
	// ovrPreReason is their reason.
	ovrPreReason = "pre-306"
)

// ovrEnv is the environment of the matrix: the env of WP-783 with the
// author of the overrides from before the bulk.
type ovrEnv struct {
	*revertExtEnv
	preAuthor string
	preAt     string
	defs      map[string]citype.AttributeDefinition
}

func newOvrEnv(t *testing.T) *ovrEnv {
	t.Helper()
	e := &ovrEnv{revertExtEnv: newRevertExtEnv(t), defs: map[string]citype.AttributeDefinition{}}
	e.preAuthor = e.f.AppUser(t, e.f.OrgA, "pre-306")
	e.preAt = e.text(`SELECT $1::timestamptz::text`, ovrPreAt)
	for _, key := range []string{"w306_note", "w306_tpl"} {
		d := citype.AttributeDefinition{CITypeID: e.typeID, Key: key, DataType: "text"}
		if err := e.types.CreateAttributeDefinition(e.full, e.f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", key, err)
		}
		e.defs[key] = d
	}
	return e
}

// isColumn reports whether a field of the matrix is a CI column.
func ovrIsColumn(field string) bool {
	return field == "status" || field == "model" || field == "location_id"
}

// value returns the value of a field of a CI and whether it has one.
func (e *ovrEnv) value(id, field string) (string, bool) {
	e.t.Helper()
	var v string
	var ok bool
	query := `SELECT COALESCE(attributes->>$2, ''), attributes ? $2 FROM ci WHERE id = $1`
	args := []any{id, field}
	if ovrIsColumn(field) {
		query, args = fmt.Sprintf(`SELECT COALESCE(%[1]s::text, ''), %[1]s IS NOT NULL FROM ci WHERE id = $1`, field), []any{id}
	}
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&v, &ok); err != nil {
		e.t.Fatalf("value %s of %s: %v", field, id, err)
	}
	return v, ok
}

// override returns the override of a field as value|author|reason|protected
// and its time; "" without override.
func (e *ovrEnv) override(id, field string) (state, at string) {
	e.t.Helper()
	err := e.f.Admin.QueryRow(context.Background(), `SELECT concat_ws('|', COALESCE(override_value::text, 'null'),
			COALESCE(override_author::text, ''), COALESCE(override_reason, ''), protected::text), override_at::text
		FROM ci_field_value WHERE ci_id = $1 AND field_name = $2 AND override_at IS NOT NULL`, id, field).Scan(&state, &at)
	if err != nil {
		return "", ""
	}
	return state, at
}

// jsonText is the jsonb text of a string value, null for none.
func jsonText(v string, null bool) string {
	if null {
		return "null"
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// setValue writes the value of a field directly (null: none).
func (e *ovrEnv) setValue(id, field, v string, null bool) {
	e.t.Helper()
	switch {
	case ovrIsColumn(field) && null:
		e.exec(fmt.Sprintf(`UPDATE ci SET %s = NULL WHERE id = $1`, field), id)
	case ovrIsColumn(field) && field == "location_id":
		e.exec(`UPDATE ci SET location_id = $2::uuid WHERE id = $1`, id, v)
	case ovrIsColumn(field):
		e.exec(fmt.Sprintf(`UPDATE ci SET %s = $2 WHERE id = $1`, field), id, v)
	case null:
		e.exec(`UPDATE ci SET attributes = COALESCE(attributes, '{}'::jsonb) - $2::text WHERE id = $1`, id, field)
	default:
		e.exec(`UPDATE ci SET attributes = COALESCE(attributes, '{}'::jsonb) || jsonb_build_object($2::text, $3::text) WHERE id = $1`,
			id, field, v)
	}
}

// prepare puts a field of a CI into a state before the bulk with the
// value v0.
func (e *ovrEnv) prepare(id, field, state, v0 string) {
	e.t.Helper()
	e.setValue(id, field, v0, state == ovrNull)
	switch state {
	case ovrSet, ovrNull:
		e.exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_author,
			override_reason, override_at, protected) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, true)`,
			e.f.OrgA, id, field, map[bool]any{true: nil, false: jsonText(v0, false)}[state == ovrNull], e.preAuthor, ovrPreReason, ovrPreAt)
	case ovrObserved:
		e.observe(id, field, v0)
	}
}

// observe records an observed value of a field (discovery).
func (e *ovrEnv) observe(id, field, v string) {
	e.t.Helper()
	if _, err := override.NewPGRepository(e.f.App).RecordDiscovered(e.full, e.f.OrgA, id, field, v, "snmp", time.Now()); err != nil {
		e.t.Fatalf("observe %s of %s: %v", field, id, err)
	}
}

// ingest writes the fields as an automated write (discovery, no Manual)
// and records them as observed.
func (e *ovrEnv) ingest(id string, values map[string]string) {
	e.t.Helper()
	snmp := ci.SourceSNMP
	req := ci.UpdateRequest{DiscoverySource: &snmp}
	for field, v := range values {
		e.observe(id, field, v)
		switch field {
		case "status":
			req.Status = &v
		case "model":
			req.Model = &v
		case "location_id":
			req.LocationID = &v
		default:
			if req.Attributes == nil {
				req.Attributes = map[string]any{}
			}
			req.Attributes[field] = v
		}
	}
	if _, err := ci.NewPGRepository(e.f.App).Update(e.full, e.f.OrgA, id, req); err != nil {
		e.t.Fatalf("ingest of %s: %v", id, err)
	}
}

// runBulk posts a bulk as ctx and returns its report with the bulk job:
// below 100 ids the 200 report, from 100 ids on the result of the job.
// Every id must succeed.
func (e *ovrEnv) runBulk(ctx context.Context, body map[string]any) ci.BulkResult {
	e.t.Helper()
	ids, _ := body["ids"].([]string)
	w := e.do(ctx, http.MethodPost, "/api/v1/cis/bulk", body)
	var res ci.BulkResult
	switch w.Code {
	case http.StatusOK:
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.BulkJobID == "" {
			e.t.Fatalf("bulk: %s, want the report with the bulk job", w.Body.String())
		}
	case http.StatusAccepted:
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.JobID == "" {
			e.t.Fatalf("bulk job: %s", w.Body.String())
		}
		for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			job, err := e.jobSvc.Get(context.Background(), e.f.OrgA, accepted.JobID)
			if err == nil && job.Status.Terminal() {
				if job.Status != jobs.StatusSucceeded {
					e.t.Fatalf("bulk job ended %s: %s", job.Status, job.Error)
				}
				break
			}
			if time.Now().After(deadline) {
				e.t.Fatalf("bulk job %s did not end", accepted.JobID)
			}
		}
		r := e.do(ctx, http.MethodGet, "/api/v1/jobs/"+accepted.JobID+"/result", nil)
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &res) != nil {
			e.t.Fatalf("result of the bulk job: %d %s", r.Code, r.Body.String())
		}
		res.BulkJobID = accepted.JobID
	default:
		e.t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	if res.Succeeded != len(ids) {
		e.t.Fatalf("bulk: %d of %d ids ok: %+v", res.Succeeded, len(ids), res.Items)
	}
	return res
}

// version returns the version of a CI.
func (e *ovrEnv) version(id string) int {
	e.t.Helper()
	return e.count(`SELECT version::int FROM ci WHERE id = $1`, id)
}

// ovrOp is one field operation of the matrix: its body, the fields it
// writes with their values before, of the bulk and of the ingest, and the
// states it can start from.
type ovrOp struct {
	name   string
	body   func(ids []string) map[string]any
	fields []string
	v0, vb map[string]string
	vi     map[string]string
	states []string
}

// ovrCI is a CI of the matrix: its state and whether the ingest runs on it.
type ovrCI struct {
	id, state string
	ingested  bool
}

// TestBulkOverrideMatrix runs the matrix of the field operations.
func TestBulkOverrideMatrix(t *testing.T) {
	e := newOvrEnv(t)
	tree := locations.NewPGRepository(e.f.App)
	site := func(name string) string {
		t.Helper()
		s, err := tree.Create(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: e.f.Client1, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	l0, lb, li := site("w306-l0"), site("w306-lb"), site("w306-li")
	all := []string{ovrNone, ovrSet, ovrNull, ovrObserved}
	ops := []ovrOp{
		{name: ci.BulkSetStatus, fields: []string{"status"},
			body: func(ids []string) map[string]any {
				return map[string]any{"action": ci.BulkSetStatus, "status": ci.StatusMaintenance, "ids": ids}
			},
			v0: map[string]string{"status": ci.StatusActive}, vb: map[string]string{"status": ci.StatusMaintenance},
			vi: map[string]string{"status": ci.StatusInactive},
			// A status override with value null does not exist: the
			// column is NOT NULL.
			states: []string{ovrNone, ovrSet, ovrObserved}},
		{name: ci.BulkSetFields, fields: []string{"model", "w306_note"},
			body: func(ids []string) map[string]any {
				return map[string]any{"action": ci.BulkSetFields, "ids": ids,
					"params": map[string]any{"fields": map[string]any{"model": "b-model", "attributes": map[string]any{"w306_note": "b-note"}}}}
			},
			v0: map[string]string{"model": "0-model", "w306_note": "0-note"}, vb: map[string]string{"model": "b-model", "w306_note": "b-note"},
			vi: map[string]string{"model": "i-model", "w306_note": "i-note"}, states: all},
		{name: ci.BulkSetLocation, fields: []string{"location_id"},
			body: func(ids []string) map[string]any {
				return map[string]any{"action": ci.BulkSetLocation, "ids": ids, "params": map[string]any{"location_id": lb}}
			},
			v0: map[string]string{"location_id": l0}, vb: map[string]string{"location_id": lb},
			vi: map[string]string{"location_id": li}, states: all},
	}
	for i := range ops {
		op := &ops[i]
		for _, asJob := range []bool{false, true} {
			mode := "sync"
			if asJob {
				mode = "job"
			}
			t.Run(op.name+"/"+mode, func(t *testing.T) {
				defer func(parent *testing.T) { e.t = parent }(e.t)
				e.t = t
				runOvrOp(t, e, op, asJob)
			})
		}
	}
}

// runOvrOp runs one operation of the matrix in one mode.
func runOvrOp(t *testing.T, e *ovrEnv, op *ovrOp, asJob bool) {
	prefix := fmt.Sprintf("w306-%s-%v-", op.name, asJob)
	var cis []ovrCI
	for _, ingested := range []bool{false, true} {
		for _, state := range op.states {
			id := e.ci(fmt.Sprintf("%s%s-%v", prefix, state, ingested))
			for _, field := range op.fields {
				e.prepare(id, field, state, op.v0[field])
			}
			cis = append(cis, ovrCI{id: id, state: state, ingested: ingested})
		}
	}
	// skipped: an override changed since the bulk (synchronous only).
	var skipped string
	if !asJob {
		skipped = e.ci(prefix + "skipped")
		for _, field := range op.fields {
			e.prepare(skipped, field, ovrNone, op.v0[field])
		}
	}
	ids := make([]string, 0, ci.BulkJobFromIDs)
	for i := range cis {
		ids = append(ids, cis[i].id)
	}
	if skipped != "" {
		ids = append(ids, skipped)
	}
	for asJob && len(ids) < ci.BulkJobFromIDs {
		filler := e.ci(fmt.Sprintf("%sfiller-%d", prefix, len(ids)))
		for _, field := range op.fields {
			e.prepare(filler, field, ovrNone, op.v0[field])
		}
		ids = append(ids, filler)
	}
	versions := map[string]int{}
	for _, id := range ids {
		versions[id] = e.version(id)
	}

	reqCtx := audit.WithRequestInfo(e.full, audit.RequestInfo{RequestID: "wp306-" + op.name, Source: audit.SourceUI})
	res := e.runBulk(reqCtx, op.body(ids))
	jobID := res.BulkJobID
	if !asJob && !audit.Recorded(reqCtx) {
		t.Error("the synchronous bulk request is not audited by its CI entries: the route audit would add an entry")
	}

	// Bulk: value, override of the caller with bulk:<job_id>, version,
	// audit entry with the correlation id, one event.
	bulkAt := map[string]string{}
	wantBulk := func(field string) string {
		return jsonText(op.vb[field], false) + "|" + e.user + "|bulk:" + jobID + "|true"
	}
	for _, id := range ids {
		for _, field := range op.fields {
			if v, _ := e.value(id, field); v != op.vb[field] {
				t.Errorf("%s after the bulk: %s = %q, want %q", id, field, v, op.vb[field])
			}
			state, at := e.override(id, field)
			if state != wantBulk(field) {
				t.Errorf("%s after the bulk: override of %s %q, want %q", id, field, state, wantBulk(field))
			}
			bulkAt[id+"/"+field] = at
		}
		if v := e.version(id); v != versions[id]+1 {
			t.Errorf("%s after the bulk: version %d, want %d", id, v, versions[id]+1)
		}
		versions[id]++
		if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action IN ('ci.updated', 'ci.status_changed')`, id); n != 1 {
			t.Errorf("%s: %d audit entries of the bulk, want 1", id, n)
		}
		if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action IN ('ci.updated', 'ci.status_changed')
			AND correlation_id = $2 AND request_id = $3`, id, jobID, "wp306-"+op.name); n != 1 {
			t.Errorf("%s: %d audit entries with correlation_id %s and the request id of the bulk, want 1", id, n, jobID)
		}
		if n := e.count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'`, id); n != 1 {
			t.Errorf("%s: %d events ci.changed, want 1", id, n)
		}
	}

	// Ingest after the bulk overwrites nothing.
	for i := range cis {
		if !cis[i].ingested {
			continue
		}
		id := cis[i].id
		e.ingest(id, op.vi)
		for _, field := range op.fields {
			if v, _ := e.value(id, field); v != op.vb[field] {
				t.Errorf("%s (%s) after the ingest: %s = %q, want the bulk value %q", id, cis[i].state, field, v, op.vb[field])
			}
			if state, at := e.override(id, field); state != wantBulk(field) || at != bulkAt[id+"/"+field] {
				t.Errorf("%s (%s) after the ingest: override of %s %q at %s, want the bulk's at %s", id, cis[i].state, field, state, at,
					bulkAt[id+"/"+field])
			}
		}
	}
	if skipped != "" {
		e.exec(`UPDATE ci_field_value SET override_reason = 'changed since' WHERE ci_id = $1 AND field_name = $2`, skipped, op.fields[0])
	}

	rev := e.revert(e.full, jobID)
	for i := range cis {
		c := &cis[i]
		item := wantOutcome(t, &rev, c.id, "reverted", http.StatusOK, "")
		if len(item.Fields) != 0 {
			t.Errorf("%s (%s): skipped fields %v, want none", c.id, c.state, item.Fields)
		}
		for _, field := range op.fields {
			checkReverted(t, e, op, c, field)
		}
		if v := e.version(c.id); v != versions[c.id]+1 {
			t.Errorf("%s (%s) after the revert: version %d, want %d (one raise of the inverse write)", c.id, c.state, v, versions[c.id]+1)
		}
		if n := e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'override'`, c.id); n != 0 {
			t.Errorf("%s (%s): %d ci_change rows override, want none (the inverse write raised the version)", c.id, c.state, n)
		}
	}
	if skipped != "" {
		checkSkippedOverride(t, e, op, &rev, skipped, jobID)
	}
	if rev.Failed != 0 {
		t.Errorf("revert report: %+v", rev)
	}
}

// checkReverted checks one field of a CI after the revert.
func checkReverted(t *testing.T, e *ovrEnv, op *ovrOp, c *ovrCI, field string) {
	t.Helper()
	v, has := e.value(c.id, field)
	state, at := e.override(c.id, field)
	pre := jsonText(op.v0[field], c.state == ovrNull) + "|" + e.preAuthor + "|" + ovrPreReason + "|true"
	// The observed value a field without override before the bulk falls
	// back to; a structural column keeps the old value.
	fallback := op.v0[field]
	if c.ingested && field != "location_id" {
		fallback = op.vi[field]
	}
	switch c.state {
	case ovrSet:
		if v != op.v0[field] || state != pre || at != e.preAt {
			t.Errorf("%s (override, ingest %v): %s = %q, override %q at %s, want %q with %q at %s", c.id, c.ingested, field, v, state, at,
				op.v0[field], pre, e.preAt)
		}
	case ovrNull:
		if has || state != pre || at != e.preAt {
			t.Errorf("%s (override null, ingest %v): %s = %q (%v), override %q at %s, want none with %q at %s", c.id, c.ingested, field,
				v, has, state, at, pre, e.preAt)
		}
	default:
		if v != fallback || state != "" {
			t.Errorf("%s (%s, ingest %v): %s = %q, override %q, want %q without override", c.id, c.state, c.ingested, field, v, state, fallback)
		}
	}
	if c.state == ovrSet || c.state == ovrNull {
		if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.override_set'
			AND changes->>'field' = $2`, c.id, field); n != 1 {
			t.Errorf("%s: %d audit entries ci.override_set of %s, want 1", c.id, n, field)
		}
	} else if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.override_cleared'
		AND changes->>'field' = $2`, c.id, field); n != 1 {
		t.Errorf("%s: %d audit entries ci.override_cleared of %s, want 1", c.id, n, field)
	}
}

// checkSkippedOverride checks the CI whose override of the first field
// changed since the bulk: the field is skipped with value and override.
func checkSkippedOverride(t *testing.T, e *ovrEnv, op *ovrOp, rev *ci.BulkRevertResult, id, jobID string) {
	t.Helper()
	first := op.fields[0]
	if len(op.fields) == 1 {
		wantOutcome(t, rev, id, "skipped", http.StatusConflict, "")
	} else {
		wantOutcome(t, rev, id, "reverted", http.StatusOK, "")
	}
	if got := outcome(t, rev, id); !slices.Equal(got.Fields, []string{first}) {
		t.Errorf("skipped fields %v, want %s", got.Fields, first)
	}
	if v, _ := e.value(id, first); v != op.vb[first] {
		t.Errorf("%s with a changed override: %s = %q, want the bulk value %q", id, first, v, op.vb[first])
	}
	want := jsonText(op.vb[first], false) + "|" + e.user + "|changed since|true"
	if state, _ := e.override(id, first); state != want {
		t.Errorf("%s with a changed override: override %q, want %q", id, state, want)
	}
	if n := e.count(`SELECT count(*) FROM bulk_change_set WHERE job_id = $1 AND entity_id = $2 AND field_name = $3
		AND revert_outcome = 'skipped'`, jobID, id, first); n != 1 {
		t.Errorf("%d skipped change set rows of %s, want 1", n, first)
	}
	for _, field := range op.fields[1:] {
		if v, _ := e.value(id, field); v != op.v0[field] {
			t.Errorf("%s: the other field %s = %q, want reverted to %q", id, field, v, op.v0[field])
		}
		if state, _ := e.override(id, field); state != "" {
			t.Errorf("%s: the other field %s keeps the override %q", id, field, state)
		}
	}
}

// TestBulkOverrideChangeTemplate covers the template migration through the
// bulk (change_template): overrides stay untouched, also one with value
// null, and get no default; the revert removes the defaults without an
// override and keeps the overrides; synchronous and as a job.
func TestBulkOverrideChangeTemplate(t *testing.T) {
	e := newOvrEnv(t)
	ctx := e.full
	tplField := e.defs["w306_tpl"]
	tpl := citype.Template{Key: "w306-template", CITypeID: e.typeID, IsActive: true}
	var err error
	if err = e.types.CreateTemplate(ctx, e.f.OrgA, &tpl); err != nil {
		t.Fatal(err)
	}
	versions := [][]citype.TemplateVersionItem{
		{{AttributeDefinitionID: tplField.ID}},
		{{AttributeDefinitionID: tplField.ID, DefaultOverride: json.RawMessage(`"tpl-default"`)}},
	}
	for i, items := range versions {
		if i > 0 {
			if _, err = e.types.CreateTemplateVersion(ctx, e.f.OrgA, tpl.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = e.types.SetTemplateVersionItems(ctx, e.f.OrgA, tpl.ID, i+1, items); err != nil {
			t.Fatal(err)
		}
		if _, err = e.types.PublishTemplateVersion(ctx, e.f.OrgA, tpl.ID, i+1); err != nil {
			t.Fatal(err)
		}
	}
	const field = "w306_tpl"
	values := map[string]string{ovrNone: "", ovrSet: "manual", ovrNull: "", ovrObserved: "seen"}
	for _, asJob := range []bool{false, true} {
		t.Run(fmt.Sprintf("job=%v", asJob), func(t *testing.T) {
			defer func(parent *testing.T) { e.t = parent }(e.t)
			e.t = t
			states := map[string]string{}
			var ids []string
			add := func(state string) {
				id := e.ci(fmt.Sprintf("w306-tpl-%v-%s-%d", asJob, state, len(ids)))
				e.exec(`INSERT INTO ci_template (organization_id, ci_id, template_id, version, position) VALUES ($1, $2, $3, 1, 0)`,
					e.f.OrgA, id, tpl.ID)
				switch state {
				case ovrNone:
				default:
					e.prepare(id, field, state, values[state])
				}
				states[id] = state
				ids = append(ids, id)
			}
			for _, state := range []string{ovrNone, ovrSet, ovrNull, ovrObserved} {
				add(state)
			}
			for asJob && len(ids) < ci.BulkJobFromIDs {
				add(ovrNone)
			}
			ovrSnapshot := func() string {
				return e.text(`SELECT COALESCE(string_agg(concat_ws('|', ci_id::text, field_name, COALESCE(override_value::text, 'null'),
					COALESCE(override_author::text, ''), override_reason, override_at::text, protected::text, updated_at::text), ',' ORDER BY ci_id),
					'') FROM ci_field_value WHERE ci_id = ANY($1::uuid[]) AND override_at IS NOT NULL`, ids)
			}
			before := ovrSnapshot()
			res := e.runBulk(ctx, map[string]any{"action": ci.BulkChangeTemplate, "ids": ids, "params": map[string]any{"template_id": tpl.ID}})
			if after := ovrSnapshot(); after != before {
				t.Errorf("the template migration changed overrides:\n before %s\n after  %s", before, after)
			}
			pre := func(null bool) string {
				return jsonText(values[ovrSet], null) + "|" + e.preAuthor + "|" + ovrPreReason + "|true"
			}
			check := func(phase string, wantDefault bool) {
				t.Helper()
				for _, id := range ids {
					v, has := e.value(id, field)
					state, at := e.override(id, field)
					switch states[id] {
					case ovrNone:
						if (wantDefault && v != "tpl-default") || (!wantDefault && has) || state != "" {
							t.Errorf("%s %s (none): %q (%v), override %q, want default %v without override", phase, id, v, has, state, wantDefault)
						}
					case ovrSet:
						if v != "manual" || state != pre(false) || at != e.preAt {
							t.Errorf("%s %s (override): %q, override %q at %s", phase, id, v, state, at)
						}
					case ovrNull:
						if has || state != pre(true) || at != e.preAt {
							t.Errorf("%s %s (override null): %q (%v), override %q at %s, want no default", phase, id, v, has, state, at)
						}
					case ovrObserved:
						if v != "seen" || state != "" {
							t.Errorf("%s %s (observed): %q, override %q", phase, id, v, state)
						}
					}
				}
			}
			check("after the migration", true)
			for _, id := range ids {
				if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND correlation_id = $2`, id, res.BulkJobID); n < 1 {
					t.Errorf("%s: no audit entry with correlation_id %s", id, res.BulkJobID)
				}
			}
			rev := e.revert(ctx, res.BulkJobID)
			if rev.Failed != 0 || rev.Reverted != len(ids) {
				t.Errorf("revert report: reverted %d failed %d of %d", rev.Reverted, rev.Failed, len(ids))
			}
			check("after the revert", false)
			if after := ovrSnapshot(); after != before {
				t.Errorf("the revert changed overrides:\n before %s\n after  %s", before, after)
			}
		})
	}
}

// TestBulkOverrideRevertWithoutFieldChange: a field whose observed value
// since the bulk is the bulk value falls back to it in the revert, so the
// inverse write leaves the value; the CI keeps the observed value without
// override and the version rises once (the manual inverse write), with no
// second raise for the removed override.
func TestBulkOverrideRevertWithoutFieldChange(t *testing.T) {
	e := newOvrEnv(t)
	id := e.ci("w306-unchanged")
	res := e.runBulk(e.full, map[string]any{"action": ci.BulkSetStatus, "status": ci.StatusMaintenance, "ids": []string{id}})
	e.observe(id, "status", ci.StatusMaintenance)
	v := e.version(id)
	rev := e.revert(e.full, res.BulkJobID)
	wantOutcome(t, &rev, id, "reverted", http.StatusOK, "")
	if s, _ := e.value(id, "status"); s != ci.StatusMaintenance {
		t.Errorf("status %q, want the observed maintenance", s)
	}
	if state, _ := e.override(id, "status"); state != "" {
		t.Errorf("override %q after the revert, want none", state)
	}
	if got := e.version(id); got != v+1 {
		t.Errorf("version %d, want %d", got, v+1)
	}
	if n := e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'override'`, id); n != 0 {
		t.Errorf("%d ci_change rows override, want none", n)
	}
}

// TestBulkOverrideTxFunctionsRaiseVersion covers ClearOverrideTx and
// RestoreOverrideTx outside a field update (raise): each locks the CI,
// raises its version with a ci_change row override and audits; the clear
// falls back to the observed value, the restore writes value, author,
// reason and the explicit time; a field without override is no clear.
func TestBulkOverrideTxFunctionsRaiseVersion(t *testing.T) {
	e := newOvrEnv(t)
	id := e.ci("w306-tx")
	e.prepare(id, "model", ovrObserved, "seen-model")
	e.exec(`UPDATE ci_field_value SET override_value = '"manual"', override_reason = 'x', override_at = now(), protected = true
		WHERE ci_id = $1 AND field_name = 'model'`, id)
	e.setValue(id, "model", "manual", false)
	v := e.version(id)
	var cleared, noop bool
	err := database.WithRequestTenant(e.full, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		var txErr error
		if cleared, txErr = override.ClearOverrideTx(ctx, tx, e.f.OrgA, id, "model", true); txErr != nil {
			return txErr
		}
		if noop, txErr = override.ClearOverrideTx(ctx, tx, e.f.OrgA, id, "w306_note", true); txErr != nil {
			return txErr
		}
		at, _ := time.Parse(time.RFC3339Nano, ovrPreAt)
		return override.RestoreOverrideTx(ctx, tx, e.f.OrgA, id, "w306_note", &override.OverrideState{Value: "restored",
			Author: e.preAuthor, Reason: ovrPreReason, At: at, Protected: true}, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cleared || noop {
		t.Errorf("cleared %v, cleared without override %v; want true, false", cleared, noop)
	}
	if m, _ := e.value(id, "model"); m != "seen-model" {
		t.Errorf("model %q after the clear, want the observed seen-model", m)
	}
	if state, _ := e.override(id, "model"); state != "" {
		t.Errorf("model keeps the override %q", state)
	}
	want := `"restored"|` + e.preAuthor + "|" + ovrPreReason + "|true"
	if state, at := e.override(id, "w306_note"); state != want || at != e.preAt {
		t.Errorf("restored override %q at %s, want %q at %s", state, at, want, e.preAt)
	}
	if n, _ := e.value(id, "w306_note"); n != "restored" {
		t.Errorf("note %q, want the restored value", n)
	}
	if got := e.version(id); got != v+2 {
		t.Errorf("version %d, want %d (one raise per change)", got, v+2)
	}
	if n := e.count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'override'`, id); n != 2 {
		t.Errorf("%d ci_change rows override, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action IN ('ci.override_cleared', 'ci.override_set')`, id); n != 2 {
		t.Errorf("%d override audit entries, want 2", n)
	}
}

// TestBulkOverrideAssetOwnedFields: set_fields on a field the asset owns of
// a linked CI (serial_number) is 422 for the id with the field, not
// silently dropped; the other id is applied (WP-306, checkAssetOwnedFields).
func TestBulkOverrideAssetOwnedFields(t *testing.T) {
	e := newOvrEnv(t)
	loose, linked := e.ci("w306-loose"), e.ci("w306-linked")
	e.exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, linked, e.f.Asset(t, e.f.OrgA, e.f.Client1, "w306-asset"))
	w := e.do(e.full, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": ci.BulkSetFields, "ids": []string{loose, linked},
		"params": map[string]any{"fields": map[string]any{"serial_number": "SN-306"}}})
	var res ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	expectItems(t, res, []string{loose, linked}, map[string]ci.BulkItemResult{
		linked: {Code: http.StatusUnprocessableEntity, Fields: []string{"serial_number"}}})
	if e.text(`SELECT COALESCE(serial_number, '') FROM ci WHERE id = $1`, linked) != "" ||
		e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND override_at IS NOT NULL`, linked) != 0 {
		t.Error("the linked CI took the serial number or an override")
	}
	if e.text(`SELECT COALESCE(serial_number, '') FROM ci WHERE id = $1`, loose) != "SN-306" {
		t.Error("the CI without asset did not take the serial number")
	}
}
