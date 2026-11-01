package server_test

// WP-676 (REC-08, API-03, API-05, DIS-09, TOP-01; F-405): a review
// resolution whose effect is refused keeps the error contract of the
// resource the effect writes instead of answering 500, and the item stays
// open with nothing written. Through the complete server.NewRouter
// (authorization and OpenAPI validation, newAuditT3Router) against
// PostgreSQL, POST /api/v1/review-items/{id}/resolve:
//   - accept of a scope_suggested item whose CIDR the collector already
//     scans: 409 on field target, as POST /api/v1/discovery-scopes answers;
//     an unknown collector: 422 on collector_id; a collector id that is no
//     UUID: 422 on collector_id; a CIDR that is none: 422 on target;
//   - accept of an override_conflict item whose field has no field value
//     row: 404;
//   - new of an unlicensed_ci item without a device record: 422;
//   - accept of an unknown_relationship item whose source is no UUID: 422,
//     whose source CI does not exist: 404, and no edge is recorded;
//   - the mappings that were right stay: conflicting_values accept of a
//     serial another CI holds 409, the merge of a resurrected_device item
//     whose serial another CI holds 409, a merge with a management_ip that
//     is no address 400, new with an unknown ci_type_id 422 on ci_type_id,
//     a merge into a candidate deleted meanwhile 404; the controls 200.
// Every refusal is application/problem+json. Before the fix the six first
// cases answered 500 and the unknown_relationship accept 200 without an
// edge.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run '^TestReviewResolutionKeepsErrorContract$' ./internal/server/

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestReviewResolutionKeepsErrorContract(t *testing.T) {
	r := newAuditT3Router(t, "e6")
	f := r.f
	all := identity.AllPermissions()
	// try resolves item id with body and checks status, field and the
	// status of the item afterwards.
	try := func(what, id, body string, wantCode int, wantField, wantItem string) {
		t.Helper()
		w := r.call(all, http.MethodPost, "/api/v1/review-items/"+id+"/resolve", body)
		item := r.str(`SELECT status FROM review_item WHERE id = $1`, id)
		if w.Code != wantCode || item != wantItem {
			t.Errorf("%s: %d (item %s), want %d (item %s); body %.240s", what, w.Code, item, wantCode, wantItem, w.Body.String())
			return
		}
		if wantCode == http.StatusOK {
			return
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s: Content-Type %q, want application/problem+json", what, ct)
		}
		var problem struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if problem.Field != wantField {
			t.Errorf("%s: field %q, want %q; body %.240s", what, problem.Field, wantField, w.Body.String())
		}
	}

	// scope_suggested accept.
	col := r.str(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp676-col', 'active') RETURNING id::text`, f.OrgA)
	if w := r.call(all, http.MethodPost, "/api/v1/discovery-scopes",
		`{"collector_id":"`+col+`","name":"manual","kind":"cidr","target":"10.98.0.0/24"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /discovery-scopes: %d %s", w.Code, w.Body.String())
	}
	if w := r.call(all, http.MethodPost, "/api/v1/discovery-scopes",
		`{"collector_id":"`+col+`","name":"again","kind":"cidr","target":"10.98.0.0/24"}`); w.Code != http.StatusConflict {
		t.Fatalf("POST /discovery-scopes of the same target: %d %s, want the 409 the resolution mirrors", w.Code, w.Body.String())
	}
	scope := func(collector, cidr string) string {
		return r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'scope_suggested', 'open',
			jsonb_build_object('collector_id', $2::text, 'cidr', $3::text, 'collector_name', 'wp676-col', 'site_id', '', 'client_id', '')) RETURNING id::text`,
			f.OrgA, collector, cidr)
	}
	accept := `{"action":"accept"}`
	try("scope_suggested accept, CIDR already a scope of the collector", scope(col, "10.98.0.0/24"), accept, http.StatusConflict, "target", "open")
	if n := r.str(`SELECT count(*)::text FROM discovery_scope WHERE collector_id = $1`, col); n != "1" {
		t.Errorf("scopes of the collector after the refused accept: %s, want 1", n)
	}
	try("scope_suggested accept, unknown collector", scope("00000000-0000-4000-8000-0000000000ab", "10.97.0.0/24"), accept,
		http.StatusUnprocessableEntity, "collector_id", "open")
	try("scope_suggested accept, collector id not a UUID", scope("gone", "10.96.0.0/24"), accept, http.StatusUnprocessableEntity, "collector_id", "open")
	try("scope_suggested accept, CIDR invalid", scope(col, "not-a-cidr"), accept, http.StatusUnprocessableEntity, "target", "open")
	try("scope_suggested accept (control)", scope(col, "10.95.0.0/24"), accept, http.StatusOK, "", "resolved")
	try("scope_suggested dismiss (control)", scope(col, "10.94.0.0/24"), `{"action":"dismiss"}`, http.StatusOK, "", "dismissed")

	// override_conflict accept without a field value row.
	override := func(ciID string) string {
		return r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids, object_key) VALUES ($1, 'override_conflict', 'open',
			jsonb_build_object('ci_id', $2::text, 'field', 'model', 'observed_value', 'x', 'source', 'snmp', 'rank', 3), ARRAY[$2::uuid],
			'ci:' || $2 || '/model') RETURNING id::text`, f.OrgA, ciID)
	}
	try("override_conflict accept, field without a field value row", override(f.CI(t, f.OrgA, "", "wp676-ovr-none")), accept,
		http.StatusNotFound, "", "open")
	withOverride := f.CI(t, f.OrgA, "", "wp676-ovr-ok")
	if w := r.call(all, http.MethodPut, "/api/v1/cis/"+withOverride+"/fields/model/override", `{"value":"manual","reason":"wp676"}`); w.Code != http.StatusOK {
		t.Fatalf("override: %d %s", w.Code, w.Body.String())
	}
	try("override_conflict accept (control)", override(withOverride), accept, http.StatusOK, "", "resolved")

	// unlicensed_ci new without a device record.
	try("unlicensed_ci new with an empty payload",
		r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'unlicensed_ci', 'open', '{}') RETURNING id::text`, f.OrgA),
		`{"action":"new"}`, http.StatusUnprocessableEntity, "", "open")

	// unknown_relationship accept (TOP-01).
	source, target := f.CI(t, f.OrgA, "", "wp676-ur-s"), f.CI(t, f.OrgA, "", "wp676-ur-t")
	relationshipItem := func(sourceID string) string {
		return r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'unknown_relationship', 'open',
			jsonb_build_object('source_ci_id', $2::text, 'target_ci_id', $3::text, 'type_key', 'weird')) RETURNING id::text`, f.OrgA, sourceID, target)
	}
	try("unknown_relationship accept, source id not a UUID", relationshipItem("not-a-uuid"), accept, http.StatusUnprocessableEntity, "", "open")
	try("unknown_relationship accept, source CI unknown", relationshipItem("00000000-0000-4000-8000-0000000000ef"), accept,
		http.StatusNotFound, "", "open")
	if n := r.str(`SELECT count(*)::text FROM ci_relationship WHERE organization_id = $1 AND target_ci_id = $2`, f.OrgA, target); n != "0" {
		t.Errorf("edges to the target after the refused accepts: %s, want 0", n)
	}
	try("unknown_relationship accept (control)", relationshipItem(source), accept, http.StatusOK, "", "resolved")
	if n := r.str(`SELECT count(*)::text FROM ci_relationship WHERE organization_id = $1 AND source_ci_id = $2 AND target_ci_id = $3
		AND rel_type = 'connected_to'`, f.OrgA, source, target); n != "1" {
		t.Errorf("edges after the accept: %s, want 1", n)
	}

	// The mappings that were right stay.
	holder := f.CI(t, f.OrgA, "", "wp676-holder")
	r.str(`UPDATE ci SET serial_number = 'WP676-TAKEN' WHERE id = $1 RETURNING id::text`, holder)
	conflicting := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids) VALUES ($1, 'conflicting_values', 'open',
		jsonb_build_object('matched_ci_id', $2::text, 'incoming', jsonb_build_object('serial_number', 'WP676-TAKEN'),
		'conflict_fields', jsonb_build_array('serial_number')), ARRAY[$2::uuid]) RETURNING id::text`, f.OrgA, f.CI(t, f.OrgA, "", "wp676-cv"))
	try("conflicting_values accept, serial held by another CI", conflicting, accept, http.StatusConflict, "", "open")

	resurrected := func(name, serial, payload string) (string, string) {
		gone := f.CI(t, f.OrgA, "", name)
		if serial != "" {
			r.str(`UPDATE ci SET serial_number = $2 WHERE id = $1 RETURNING id::text`, gone, serial)
		}
		if w := r.call(all, http.MethodDelete, "/api/v1/cis/"+gone, ""); w.Code >= 300 {
			t.Fatalf("delete %s: %d %s", name, w.Code, w.Body.String())
		}
		return gone, r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids) VALUES ($1, 'resurrected_device', 'open',
			$2::jsonb, ARRAY[$3::uuid]) RETURNING id::text`, f.OrgA, payload, gone)
	}
	gone, item := resurrected("wp676-res", "WP676-RTAKEN", `{"ci_type_name":"server","name":"wp676-back","client_id":""}`)
	r.str(`UPDATE ci SET serial_number = 'WP676-RTAKEN' WHERE id = $1 RETURNING id::text`, f.CI(t, f.OrgA, "", "wp676-res-other"))
	try("resurrected_device merge, serial held by another CI", item, `{"action":"merge","ci_id":"`+gone+`"}`, http.StatusConflict, "", "open")
	gone, item = resurrected("wp676-res-ip", "", `{"ci_type_name":"server","name":"wp676-back-ip","management_ip":"not-an-ip","client_id":""}`)
	try("resurrected_device merge, management_ip not an address", item, `{"action":"merge","ci_id":"`+gone+`"}`, http.StatusBadRequest, "", "open")

	live, deleted := f.CI(t, f.OrgA, "", "wp676-amb-a"), f.CI(t, f.OrgA, "", "wp676-amb-b")
	ambiguous := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids) VALUES ($1, 'ambiguous_identity', 'open',
		jsonb_build_object('ci_type_name', 'server', 'name', 'wp676-amb', 'client_id', ''), ARRAY[$2::uuid, $3::uuid]) RETURNING id::text`, f.OrgA, live, deleted)
	try("ambiguous_identity new, unknown ci_type_id", ambiguous, `{"action":"new","ci_type_id":"00000000-0000-4000-8000-0000000000cd"}`,
		http.StatusUnprocessableEntity, "ci_type_id", "open")
	if w := r.call(all, http.MethodDelete, "/api/v1/cis/"+deleted, ""); w.Code >= 300 {
		t.Fatalf("delete candidate: %d %s", w.Code, w.Body.String())
	}
	try("ambiguous_identity merge into a candidate deleted meanwhile", ambiguous, `{"action":"merge","ci_id":"`+deleted+`"}`,
		http.StatusNotFound, "", "open")
}
