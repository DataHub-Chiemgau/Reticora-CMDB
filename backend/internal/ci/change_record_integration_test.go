package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// changeRow is a ci_change row with the AUD-03 columns (WP-487).
type changeRow struct {
	ChangeType, Field, Kind, Set, RequestID, CorrelationID, ActorUser, Source string
}

// TestCIChangeRecordsAUD03 covers AUD-03 and CI-11 (WP-487, E-82 option 2)
// against PostgreSQL: every write of a CI gives its field rows one
// change_set_id and the change_kind of the change, the user, the source and
// the request and correlation id of the request. A PATCH of three fields is
// three rows of one change set, kind updated, request_id the X-Request-ID
// of the response; a status alone is status_changed, a status with another
// field updated; the type change is change_type and change_kind
// type_changed; delete, restore, merge and unmerge are deleted, restored,
// merged and restored; a CI created alone or in a batch is created, each
// with a change set of its own; an API key is no user (no actor_user_id,
// source api).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIChangeRecordsAUD03(t *testing.T) {
	f := scopetest.Seed(t, "b2")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	// The request chain of the server: request and correlation id, then the
	// request context of the audit (source, AUD-07).
	mux := chi.NewRouter()
	mux.Use(middleware.RequestID, middleware.AuditRoute(http.MethodPatch, "/api/v1/cis/{id}", nil, middleware.AuditNone, ""))
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	call := func(method, path, body string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	// changes lists the rows of ciID written since the time since.
	changes := func(ciID string, since time.Time) []changeRow {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT change_type, COALESCE(field_name, ''), change_kind, COALESCE(change_set_id::text, ''),
				COALESCE(request_id, ''), COALESCE(correlation_id, ''), COALESCE(actor_user_id::text, ''), COALESCE(source, '')
			FROM ci_change WHERE ci_id = $1 AND created_at >= $2 ORDER BY created_at, field_name`, ciID, since)
		if err != nil {
			t.Fatalf("read ci_change: %v", err)
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (c changeRow, scanErr error) {
			scanErr = row.Scan(&c.ChangeType, &c.Field, &c.Kind, &c.Set, &c.RequestID, &c.CorrelationID, &c.ActorUser, &c.Source)
			return c, scanErr
		})
		if err != nil {
			t.Fatalf("read ci_change: %v", err)
		}
		return out
	}
	// mark is the time of the database before a write.
	mark := func() time.Time {
		t.Helper()
		var now time.Time
		if err := f.Admin.QueryRow(bg, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		return now
	}
	// oneSet checks that rows are one change set of kind with the request
	// id of w and the user and source of ctx.
	oneSet := func(step string, rows []changeRow, n int, kind string, w *httptest.ResponseRecorder) {
		t.Helper()
		if len(rows) != n {
			t.Fatalf("%s: %d ci_change rows, want %d: %+v", step, len(rows), n, rows)
		}
		requestID := w.Header().Get("X-Request-ID")
		for _, row := range rows {
			if row.Set == "" || row.Set != rows[0].Set || row.Kind != kind || requestID == "" || row.RequestID != requestID ||
				row.CorrelationID == "" || row.ActorUser != f.User || row.Source != audit.SourceUI {
				t.Errorf("%s: row %+v, want one change set of kind %s, request_id %q, actor_user_id %s, source ui",
					step, row, kind, requestID, f.User)
			}
		}
	}

	id := f.CI(t, f.OrgA, f.Client1, "aud03-1")

	// PATCH of three fields: three rows, one change set, updated, the
	// request and the correlation id of the request.
	v := mark()
	w := call(http.MethodPatch, "/api/v1/cis/"+id, `{"name":"aud03-1b","model":"M-487","os_name":"Linux"}`,
		"X-Correlation-ID", "corr-wp487")
	rows := changes(id, v)
	oneSet("PATCH of three fields", rows, 3, "updated", w)
	for _, row := range rows {
		if row.CorrelationID != "corr-wp487" {
			t.Errorf("PATCH of three fields: correlation_id %q, want the X-Correlation-ID corr-wp487", row.CorrelationID)
		}
	}
	patchSet := rows[0].Set

	// A status alone is status_changed, a status with another field is
	// updated (change_kind holds for the change set).
	v = mark()
	w = call(http.MethodPatch, "/api/v1/cis/"+id, `{"status":"maintenance"}`)
	oneSet("status alone", changes(id, v), 1, "status_changed", w)
	v = mark()
	w = call(http.MethodPatch, "/api/v1/cis/"+id, `{"status":"active","model":"M-488"}`)
	rows = changes(id, v)
	oneSet("status with model", rows, 2, "updated", w)
	if len(rows) == 2 && rows[0].Set == patchSet {
		t.Errorf("the second PATCH reuses the change set %s of the first", patchSet)
	}

	// The type change is type_changed (CI-11).
	var router string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'router' AND organization_id IS NULL`).Scan(&router); err != nil {
		t.Fatal(err)
	}
	v = mark()
	w = call(http.MethodPost, "/api/v1/cis/"+id+"/type", `{"ci_type_id":"`+router+`"}`)
	rows = changes(id, v)
	oneSet("type change", rows, 1, "type_changed", w)
	if len(rows) == 1 && rows[0].ChangeType != "type_changed" {
		t.Errorf("type change: change_type %q, want type_changed", rows[0].ChangeType)
	}

	// Delete and restore: deleted, restored.
	v = mark()
	w = call(http.MethodDelete, "/api/v1/cis/"+id, "")
	oneSet("delete", changes(id, v), 1, "deleted", w)
	v = mark()
	w = call(http.MethodPost, "/api/v1/cis/"+id+"/restore", "")
	oneSet("restore", changes(id, v), 1, "restored", w)

	// Merge: the survivor's and the victim's row are merged; the unmerge
	// restores both.
	survivor := f.CI(t, f.OrgA, f.Client1, "aud03-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "aud03-victim")
	start := mark()
	w = call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`)
	oneSet("merge (survivor)", changes(survivor, start), 1, "merged", w)
	oneSet("merge (victim)", changes(victim, start), 1, "merged", w)
	start = mark()
	w = call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge", "")
	oneSet("unmerge (survivor)", changes(survivor, start), 1, "restored", w)
	oneSet("unmerge (victim)", changes(victim, start), 1, "restored", w)

	// Create, alone and in a batch: created, one change set per CI.
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	start = mark()
	w = call(http.MethodPost, "/api/v1/cis", `{"ci_type_id":"`+serverType+`","name":"aud03-created","client_id":"`+f.Client1+`"}`)
	var created ci.Item
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	oneSet("create", changes(created.ID, start), 1, "created", w)
	batch := []*ci.Item{
		{OrganizationID: f.OrgA, CITypeID: serverType, Name: "aud03-batch-1", Status: "active", ClientID: f.Client1},
		{OrganizationID: f.OrgA, CITypeID: serverType, Name: "aud03-batch-2", Status: "active", ClientID: f.Client1},
	}
	if _, err := repo.CreateMany(ctx, f.OrgA, batch); err != nil {
		t.Fatal(err)
	}
	first, second := changes(batch[0].ID, start), changes(batch[1].ID, start)
	if len(first) != 1 || len(second) != 1 || first[0].Kind != "created" || second[0].Kind != "created" ||
		first[0].Set == "" || second[0].Set == "" || first[0].Set == second[0].Set ||
		first[0].ActorUser != f.User || second[0].ActorUser != f.User {
		t.Errorf("batch create: %+v %+v, want one row of kind created per CI, each its own change set, actor_user_id %s",
			first, second, f.User)
	}

	// An API key is no user: its id stays actor_id, actor_user_id is empty
	// and the source is api.
	key := f.ID()
	ctx = tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: key})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: key, OrganizationID: f.OrgA, Type: identity.PrincipalTypeAPIKey})
	v = mark()
	w = call(http.MethodPatch, "/api/v1/cis/"+created.ID, `{"model":"M-KEY"}`)
	rows = changes(created.ID, v)
	var actor string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(actor_id::text, '') FROM ci_change WHERE ci_id = $1 AND field_name = 'model'`,
		created.ID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != "updated" || rows[0].ActorUser != "" || rows[0].Source != audit.SourceAPI ||
		rows[0].RequestID != w.Header().Get("X-Request-ID") || actor != key {
		t.Errorf("PATCH with an API key: %+v, actor_id %s; want kind updated, no actor_user_id, source api, actor_id %s", rows, actor, key)
	}
}

// TestCIChangeKindOfOtherWriters covers the trigger of 000167 (WP-487): a
// writer that sets no change_kind (ipam, rack, override) gets it from
// change_type, field and values as in the backfill; 'type_change' is stored
// as 'type_changed'.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIChangeKindOfOtherWriters(t *testing.T) {
	f := scopetest.Seed(t, "b2")
	bg := context.Background()
	id := f.CI(t, f.OrgA, f.Client1, "aud03-trigger")
	set := f.ID()
	cases := []struct {
		changeType, field, oldValue, newValue string
		set                                   any
		want                                  string
	}{
		{"relationship_change", "relationships", `{"id": "r1"}`, "", nil, "relationship_removed"},
		{"relationship_change", "relationships", "", `{"id": "r2"}`, nil, "relationship_added"},
		{"movement", "rack_mount", "null", `{"rack_id": "r"}`, nil, "updated"},
		{"location_change", "location_id", "null", `"loc"`, nil, "updated"},
		{"override", "model", `"a"`, `"b"`, nil, "updated"},
		{"update", "interfaces", `["eth0"]`, "", nil, "updated"},
		{"update", "deleted_at", `"deleted"`, "null", nil, "restored"},
		{"reconciliation_decision", "deleted_at", `"deleted"`, "null", nil, "restored"},
		{"reconciliation_decision", "merged_ci", "null", `{"victim_id": "v"}`, nil, "merged"},
		{"reconciliation_decision", "unmerged_ci", "null", `{"merge_id": "m"}`, nil, "restored"},
		{"delete", "merged_into_id", `{}`, `{"merged_into_id": "s"}`, nil, "merged"},
		{"delete", "", `{}`, `{}`, nil, "deleted"},
		{"create", "", "null", `{}`, nil, "created"},
		{"type_changed", "ci_type_id", `"a"`, `"b"`, nil, "type_changed"},
		{"status_change", "status", `"active"`, `"maintenance"`, nil, "status_changed"},
		{"status_change", "status", `"active"`, `"maintenance"`, set, "status_changed"},
		// The second row of a change set: the status is part of an update.
		{"status_change", "status", `"maintenance"`, `"active"`, set, "updated"},
	}
	for _, c := range cases {
		var kind string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, old_value, new_value, change_set_id)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')::jsonb, NULLIF($6, '')::jsonb, $7) RETURNING change_kind`,
			f.OrgA, id, c.changeType, c.field, c.oldValue, c.newValue, c.set).Scan(&kind); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if kind != c.want {
			t.Errorf("%s %s (%s -> %s, set %v): change_kind %q, want %q", c.changeType, c.field, c.oldValue, c.newValue, c.set, kind, c.want)
		}
	}
	// The old vocabulary is not stored (CI-11): a writer of the previous
	// version and a merge snapshot taken before 000167 that the unmerge
	// writes back (UPDATE without change_kind) give type_changed.
	var rowID, changeType, kind string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name)
		VALUES ($1, $2, 'type_change', 'ci_type_id') RETURNING id::text, change_type, change_kind`, f.OrgA, id).Scan(&rowID, &changeType, &kind); err != nil {
		t.Fatal(err)
	}
	if changeType != "type_changed" || kind != "type_changed" {
		t.Errorf("insert of type_change: change_type %q, change_kind %q, want type_changed", changeType, kind)
	}
	if err := f.Admin.QueryRow(bg, `UPDATE ci_change SET change_type = 'type_change', change_kind = NULL WHERE id = $1
		RETURNING change_type, change_kind`, rowID).Scan(&changeType, &kind); err != nil {
		t.Fatal(err)
	}
	if changeType != "type_changed" || kind != "type_changed" {
		t.Errorf("restore of an old snapshot row: change_type %q, change_kind %q, want type_changed", changeType, kind)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_change (organization_id, ci_id, change_type, change_kind) VALUES ($1, $2, 'update', 'changed')`,
		f.OrgA, id); err == nil {
		t.Error("ci_change accepts change_kind changed, want only the AUD-03 kinds")
	}
}
