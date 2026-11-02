package server_test

// Phase-1 acceptance audit, group T3-data-ingest: REGRESSION test (passes
// today; closes an evidence gap).
//
// WP-088 (REC-07, AUTH-04; review:resolve) high test gap "review:resolve
// enforcement through the real router (repository test)": until now only a
// verifier probe showed that the complete NewRouter (authz + OpenAPI
// validation) refuses the resolve routes without review:resolve and the
// list without discovery:read. Asserted here:
// - POST /api/v1/review-items/{id}/resolve and the deprecated
//   /api/v1/discovery/review-items/{id}/resolve without review:resolve -> 403,
//   the item stays open;
// - GET /api/v1/review-items without discovery:read -> 403;
// - with review:resolve -> 200, the item is dismissed and resolved_by is the
//   caller when the body names none.
//
// WP-373 (REC-08, API-05; F-091) "resolved_by and the unlicensed_ci audit
// actor from the request body": TestAuditT3ResolvedByIsThePrincipal (moved
// here from audit_t3_router_auditdefect_test.go). resolved_by is not part
// of ReviewItemResolveRequest: a body naming it is 400 on both resolve
// routes and resolves nothing; resolved_by and the audit actor are the
// principal (user or service account), also where the effect resolves the
// item itself (new of unlicensed_ci and ambiguous_identity).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3ReviewResolveRequiresPermission|TestAuditT3ResolvedByIsThePrincipal' ./internal/server/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT3ReviewResolveRequiresPermission(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	item := r.reviewItem("duplicate_ip")
	status := func() string { return r.str(`SELECT status FROM review_item WHERE id = $1`, item) }

	for _, path := range []string{"/api/v1/review-items/" + item + "/resolve", "/api/v1/discovery/review-items/" + item + "/resolve"} {
		if w := r.call(auditT3Without(identity.PermReviewResolve), http.MethodPost, path, `{"action":"dismiss"}`); w.Code != http.StatusForbidden {
			t.Errorf("POST %s without review:resolve = %d, want 403", path, w.Code)
		}
		if got := status(); got != "open" {
			t.Errorf("item status after refused resolve via %s = %q, want open", path, got)
		}
	}
	if w := r.call(auditT3Without(identity.PermDiscoveryRead), http.MethodGet, "/api/v1/review-items", ""); w.Code != http.StatusForbidden {
		t.Errorf("GET /api/v1/review-items without discovery:read = %d, want 403", w.Code)
	}
	if w := r.call(identity.AllPermissions(), http.MethodGet, "/api/v1/review-items", ""); w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/review-items with discovery:read = %d, want 200", w.Code)
	}

	w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"dismiss"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve with review:resolve = %d %s, want 200", w.Code, w.Body.String())
	}
	if got := status(); got != "dismissed" {
		t.Errorf("item status after dismiss = %q, want dismissed", got)
	}
	if got := r.str(`SELECT coalesce(resolved_by::text, '') FROM review_item WHERE id = $1`, item); got != r.user {
		t.Errorf("resolved_by = %q, want the caller %s", got, r.user)
	}
}

func TestAuditT3ResolvedByIsThePrincipal(t *testing.T) {
	r := newAuditT3Router(t, "f7")
	routes := []string{"/api/v1/review-items/%s/resolve", "/api/v1/discovery/review-items/%s/resolve"}
	serverType := r.str(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	// item opens a review item of kind; unlicensed_ci and ambiguous_identity
	// keep a device record that new adopts or creates as a CI.
	seq := 0
	item := func(kind string) (id, serial string) {
		t.Helper()
		seq++
		serial = fmt.Sprintf("WP373-%s-%d", kind, seq)
		payload := map[string]any{}
		switch kind {
		case "unlicensed_ci":
			payload = map[string]any{"device_key": "serial:" + serial, "ci_type_id": serverType, "ci_type_name": serverType, "source": "snmp",
				"device": map[string]any{"ci_type_name": "server", "name": "wp373-" + serial, "serial_number": serial,
					"fingerprint": map[string]any{}, "raw_data": map[string]any{}}}
		case "ambiguous_identity":
			payload = map[string]any{"ci_type_name": "server", "name": "wp373-" + serial, "serial_number": serial, "source": "snmp", "client_id": ""}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		id = r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, $2, 'open', $3::jsonb) RETURNING id::text`,
			r.f.OrgA, kind, string(raw))
		return id, serial
	}
	stored := func(id string) (status, by string) {
		t.Helper()
		return r.str(`SELECT status FROM review_item WHERE id = $1`, id), r.str(`SELECT coalesce(resolved_by::text, '') FROM review_item WHERE id = $1`, id)
	}
	audits := func(id string) string {
		t.Helper()
		return r.str(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id::text = $2`, r.f.OrgA, id)
	}
	liveCIs := func(serial string) string {
		t.Helper()
		return r.str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, r.f.OrgA, serial)
	}

	// A body naming a resolver is refused on both routes, whoever it names,
	// and resolves nothing: no status, no resolved_by, no audit, no CI.
	for _, kind := range []string{"duplicate_ip", "unlicensed_ci", "ambiguous_identity"} {
		for _, route := range routes {
			for _, spoof := range []string{r.other, r.user, "not-a-uuid"} {
				action := "dismiss"
				if kind != "duplicate_ip" {
					action = "new"
				}
				id, serial := item(kind)
				path := fmt.Sprintf(route, id)
				w := r.call(identity.AllPermissions(), http.MethodPost, path, `{"action":"`+action+`","resolved_by":"`+spoof+`"}`)
				if w.Code != http.StatusBadRequest {
					t.Errorf("%s %s with body resolved_by=%s = %d %s, want 400 (resolved_by is the principal)", kind, path, spoof, w.Code, w.Body.String())
				}
				if status, by := stored(id); status != "open" || by != "" {
					t.Errorf("%s after the refused resolve via %s: status %q, resolved_by %q; want open and none", kind, path, status, by)
				}
				if n := audits(id); n != "0" {
					t.Errorf("%s: %s audit entries after the refused resolve via %s, want 0", kind, n, path)
				}
				if n := liveCIs(serial); n != "0" {
					t.Errorf("%s: %s CIs after the refused new via %s, want 0", kind, n, path)
				}
			}
		}
	}

	// Without resolved_by: stored, returned and audited is the principal, a
	// user or a service account (an API key acts as its owner), also where
	// the effect resolves the item itself (new of unlicensed_ci adopts the
	// device, new of ambiguous_identity creates the CI in one transaction).
	account := r.f.ID()
	if _, err := r.f.Admin.Exec(t.Context(), `INSERT INTO service_account (id, organization_id, name) VALUES ($1, $2, 'audit-t3-resolver')`,
		account, r.f.OrgA); err != nil {
		t.Fatal(err)
	}
	principals := []struct {
		name    string
		subject string
		typ     identity.PrincipalType
	}{{"user", r.user, identity.PrincipalTypeUser}, {"service account", account, identity.PrincipalTypeAPIKey}}
	for _, p := range principals {
		cases := []struct{ kind, action, route, status string }{
			{"duplicate_ip", "dismiss", routes[0], "dismissed"},
			{"duplicate_ip", "dismiss", routes[1], "dismissed"},
			{"unlicensed_ci", "dismiss", routes[0], "dismissed"},
			{"unlicensed_ci", "dismiss", routes[1], "dismissed"},
			{"unlicensed_ci", "new", routes[0], "resolved"},
			{"unlicensed_ci", "create", routes[1], "resolved"},
			{"ambiguous_identity", "new", routes[0], "resolved"},
			{"ambiguous_identity", "new", routes[1], "resolved"},
		}
		for _, c := range cases {
			route := "review-items"
			if c.route == routes[1] {
				route = "deprecated"
			}
			t.Run(p.name+"/"+c.kind+"/"+c.action+"/"+route, func(t *testing.T) {
				id, serial := item(c.kind)
				path := fmt.Sprintf(c.route, id)
				w := r.callAs(p.subject, p.typ, identity.AllPermissions(), http.MethodPost, path, `{"action":"`+c.action+`"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("resolve via %s: %d %s", path, w.Code, w.Body.String())
				}
				var resp struct {
					ResolvedBy string `json:"resolved_by"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.ResolvedBy != p.subject {
					t.Errorf("response resolved_by = %q (%v), want the principal %s", resp.ResolvedBy, err, p.subject)
				}
				if status, by := stored(id); status != c.status || by != p.subject {
					t.Errorf("stored: status %q, resolved_by %q; want %s and the principal %s", status, by, c.status, p.subject)
				}
				if c.action != "dismiss" {
					if n := liveCIs(serial); n != "1" {
						t.Errorf("%s CIs after %s, want 1", n, c.action)
					}
				}
				if c.kind != "unlicensed_ci" {
					return
				}
				// The resolution of an unlicensed_ci item is audited with the
				// principal as actor (ENT-03), not the system.
				want := "unlicensed_ci." + c.status
				entries := r.str(`SELECT coalesce(string_agg(coalesce(actor_id::text, '') || '|' || actor_type, ','), '') FROM audit_log
					WHERE organization_id = $1 AND resource_id::text = $2 AND action = $3`, r.f.OrgA, id, want)
				actor, actorType, _ := strings.Cut(entries, "|")
				if strings.Contains(entries, ",") || actor != p.subject || actorType == "system" ||
					(p.typ == identity.PrincipalTypeUser && actorType != "user") {
					t.Errorf("audit entries %s: %q, want one with the principal %s as actor", want, entries, p.subject)
				}
			})
		}
	}
}
