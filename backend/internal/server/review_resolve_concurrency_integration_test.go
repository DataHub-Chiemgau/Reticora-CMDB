package server_test

// WP-675 (REC-08, API-05, REC-12, CI-13; F-404): a review item is resolved
// exactly once. Two principals resolve the same open item at the same
// moment through the complete server.NewRouter (authorization and OpenAPI
// validation, newAuditT3Router) against PostgreSQL; asserted per round:
//   - exactly one request answers 200, the other 409 (review item already
//     resolved);
//   - the effect ran once: new of an unclassified_device item created one
//     CI with attributes.review_item_id = the item; accept of an
//     override_conflict item removed the override only when it won, a
//     winning dismiss kept it;
//   - one review.resolved event of the item, and status, action and
//     resolved_by of the item are those of the winner.
// Cases: new || new and new || dismiss of unclassified_device, dismiss ||
// dismiss of duplicate_ip (no effect), accept || dismiss of
// override_conflict; sequential control: 200, then 409 and no second CI.
// Before the fix both requests answered 200 (8 of 8 rounds), new || new
// created two CIs and wrote two events, resolved_by was the last writer.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run '^TestConcurrentResolutionResolvesOnce$' ./internal/server/

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// raceResult is the answer of one of two concurrent resolutions.
type raceResult struct {
	subject, action string
	code            int
	body            string
}

// resolveConcurrently sends the two resolutions of item id at the same
// moment, each as its principal.
func (r *auditT3Router) resolveConcurrently(id string, a, b raceResult) [2]raceResult {
	out := [2]raceResult{a, b}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(res *raceResult) {
			defer wg.Done()
			<-start
			w := r.callAs(res.subject, identity.PrincipalTypeUser, identity.AllPermissions(), http.MethodPost,
				"/api/v1/review-items/"+id+"/resolve", `{"action":"`+res.action+`"}`)
			res.code, res.body = w.Code, w.Body.String()
		}(&out[i])
	}
	close(start)
	wg.Wait()
	return out
}

// winner returns the resolution that answered 200 when exactly one did and
// the other answered 409.
func winner(t *testing.T, what string, res [2]raceResult) (raceResult, bool) {
	t.Helper()
	switch {
	case res[0].code == http.StatusOK && res[1].code == http.StatusConflict:
		return res[0], true
	case res[1].code == http.StatusOK && res[0].code == http.StatusConflict:
		return res[1], true
	}
	t.Errorf("%s: %s %d | %s %d, want exactly one 200 and one 409 (bodies %.120s || %.120s)", what,
		res[0].action, res[0].code, res[1].action, res[1].code, res[0].body, res[1].body)
	return raceResult{}, false
}

func TestConcurrentResolutionResolvesOnce(t *testing.T) {
	r := newAuditT3Router(t, "e5")
	f := r.f
	const rounds = 6
	events := func(id string) string {
		return r.str(`SELECT count(*)::text FROM event_outbox WHERE entity_id::text = $1 AND type = 'review.resolved'`, id)
	}
	createdCIs := func(id string) string {
		return r.str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND attributes->>'review_item_id' = $2 AND deleted_at IS NULL`, f.OrgA, id)
	}
	// checkItem asserts one event and status, action and resolver of the
	// winner.
	checkItem := func(what, id string, won raceResult, status string) {
		t.Helper()
		var gotStatus, gotAction, gotBy string
		if err := f.Admin.QueryRow(t.Context(), `SELECT status, coalesce(action, ''), coalesce(resolved_by::text, '') FROM review_item WHERE id = $1`,
			id).Scan(&gotStatus, &gotAction, &gotBy); err != nil {
			t.Fatal(err)
		}
		if gotStatus != status || gotAction != won.action || gotBy != won.subject {
			t.Errorf("%s: item %s/%s resolved_by %s, want %s/%s by the winner %s", what, gotStatus, gotAction, gotBy, status, won.action, won.subject)
		}
	}
	// checkEvents asserts one review.resolved event of the item.
	checkEvents := func(what, id string) {
		t.Helper()
		if n := events(id); n != "1" {
			t.Errorf("%s: %s review.resolved events of the item (stored %s), want 1", what, n,
				r.str(`SELECT status || '/' || coalesce(action, '') || ' resolved_by ' || coalesce(resolved_by::text, '') FROM review_item WHERE id = $1`, id))
		}
	}
	unclassified := func(name, ip string) string {
		return r.str(`INSERT INTO review_item (organization_id, kind, status, payload, object_key) VALUES ($1, 'unclassified_device', 'open',
			jsonb_build_object('ci_type_name', 'server', 'name', $2::text, 'management_ip', $3::text, 'client_id', ''), $2::text) RETURNING id::text`,
			f.OrgA, name, ip)
	}

	for i := range rounds {
		// new || new: one CI.
		what := fmt.Sprintf("unclassified_device new || new, round %d", i)
		id := unclassified(fmt.Sprintf("wp675-new-%d", i), fmt.Sprintf("192.0.2.%d", 10+i))
		res := r.resolveConcurrently(id, raceResult{subject: r.user, action: "new"}, raceResult{subject: r.other, action: "new"})
		checkEvents(what, id)
		if won, ok := winner(t, what, res); ok {
			checkItem(what, id, won, "resolved")
		}
		if n := createdCIs(id); n != "1" {
			t.Errorf("%s: %s CIs with review_item_id of the item, want 1", what, n)
		}

		// new || dismiss: a CI only when new won.
		what = fmt.Sprintf("unclassified_device new || dismiss, round %d", i)
		id = unclassified(fmt.Sprintf("wp675-mix-%d", i), fmt.Sprintf("192.0.2.%d", 40+i))
		res = r.resolveConcurrently(id, raceResult{subject: r.user, action: "new"}, raceResult{subject: r.other, action: "dismiss"})
		checkEvents(what, id)
		if won, ok := winner(t, what, res); ok {
			status, wantCIs := "resolved", "1"
			if won.action == "dismiss" {
				status, wantCIs = "dismissed", "0"
			}
			checkItem(what, id, won, status)
			if n := createdCIs(id); n != wantCIs {
				t.Errorf("%s: %s CIs with review_item_id of the item after %s won, want %s", what, n, won.action, wantCIs)
			}
		}

		// dismiss || dismiss of a type without an effect.
		what = fmt.Sprintf("duplicate_ip dismiss || dismiss, round %d", i)
		candidate := f.CI(t, f.OrgA, "", fmt.Sprintf("wp675-dup-%d", i))
		id = r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids) VALUES ($1, 'duplicate_ip', 'open', '{}', ARRAY[$2::uuid])
			RETURNING id::text`, f.OrgA, candidate)
		res = r.resolveConcurrently(id, raceResult{subject: r.user, action: "dismiss"}, raceResult{subject: r.other, action: "dismiss"})
		checkEvents(what, id)
		if won, ok := winner(t, what, res); ok {
			checkItem(what, id, won, "dismissed")
		}

		// accept || dismiss of override_conflict: the override goes only
		// when accept won.
		what = fmt.Sprintf("override_conflict accept || dismiss, round %d", i)
		target := f.CI(t, f.OrgA, "", fmt.Sprintf("wp675-ovr-%d", i))
		if w := r.call(identity.AllPermissions(), http.MethodPut, "/api/v1/cis/"+target+"/fields/model/override",
			`{"value":"manual","reason":"wp675"}`); w.Code != http.StatusOK {
			t.Fatalf("override: %d %s", w.Code, w.Body.String())
		}
		id = r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids, object_key) VALUES ($1, 'override_conflict', 'open',
			jsonb_build_object('ci_id', $2::text, 'field', 'model', 'observed_value', 'observed', 'source', 'snmp', 'rank', 3), ARRAY[$2::uuid],
			'ci:' || $2 || '/model') RETURNING id::text`, f.OrgA, target)
		res = r.resolveConcurrently(id, raceResult{subject: r.user, action: "accept"}, raceResult{subject: r.other, action: "dismiss"})
		checkEvents(what, id)
		if won, ok := winner(t, what, res); ok {
			status, wantOverride := "resolved", "false"
			if won.action == "dismiss" {
				status, wantOverride = "dismissed", "true"
			}
			checkItem(what, id, won, status)
			if got := r.str(`SELECT coalesce((SELECT (override_value IS NOT NULL)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = 'model'), 'no row')`,
				target); got != wantOverride {
				t.Errorf("%s: override still set = %s after %s won, want %s", what, got, won.action, wantOverride)
			}
		}
	}

	// Sequential control: the second resolution is 409 and creates nothing.
	id := unclassified("wp675-seq", "192.0.2.99")
	first := r.callAs(r.user, identity.PrincipalTypeUser, identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+id+"/resolve", `{"action":"new"}`)
	second := r.callAs(r.other, identity.PrincipalTypeUser, identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+id+"/resolve", `{"action":"new"}`)
	if first.Code != http.StatusOK || second.Code != http.StatusConflict {
		t.Errorf("sequential new then new: %d then %d, want 200 then 409 (%s)", first.Code, second.Code, second.Body.String())
	}
	if n := createdCIs(id); n != "1" {
		t.Errorf("sequential new then new: %s CIs, want 1", n)
	}
	checkItem("sequential new then new", id, raceResult{subject: r.user, action: "new"}, "resolved")
	checkEvents("sequential new then new", id)
}
