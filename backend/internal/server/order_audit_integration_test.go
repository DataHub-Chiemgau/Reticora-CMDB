package server_test

// The order transitions of WP-286 (STK-05) leave their audit trail: every
// successful order route writes exactly one route entry with its action,
// resource and actor; the refused self-approval of the four-eyes rule
// writes none. TestEveryWriteRouteAuditedOnPostgreSQL runs every route on a
// single order, so the state machine refuses most of them there; this test
// walks the states.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestOrderTransitionsAudited' ./internal/server/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
)

// TestOrderTransitionsAudited runs the order routes through the production
// router and route auditor on PostgreSQL (AUD-05, AUD-07, WP-286).
func TestOrderTransitionsAudited(t *testing.T) {
	e := t6NewEnv(t, "ed", nil)
	f := e.F
	bg := context.Background()
	requester := f.AppUser(t, f.OrgA, "order-audit-requester")
	approver := f.AppUser(t, f.OrgA, "order-audit-approver")
	// The requester holds order:approve as well: the self-approval fails on
	// the four-eyes rule, not on the permission.
	asRequester := t6As{Org: f.OrgA, User: requester, Perms: identity.AllPermissions()}
	asApprover := t6As{Org: f.OrgA, User: approver,
		Perms: []identity.Permission{identity.PermOrderRead, identity.PermOrderApprove}}

	// call sends one request and returns status, body and request id.
	call := func(as t6As, method, path, body string) (code int, respBody []byte, requestID string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: as.User, OrganizationID: as.Org, Type: identity.PrincipalTypeUser,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: as.Perms, Credential: "order-audit"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, req.WithContext(ctx))
		rid := w.Header().Get("X-Request-ID")
		if rid == "" {
			t.Fatalf("%s %s: no X-Request-ID", method, path)
		}
		return w.Code, w.Body.Bytes(), rid
	}

	// audited checks the single audit_log entry of a request: the route
	// entry of route with its action, resource and actor, holding the row
	// values when the snapshot register names a table (WP-505).
	audited := func(requestID, route, orderID, actor string) {
		t.Helper()
		method, pattern, _ := strings.Cut(route, " ")
		wantResource, wantAction := middleware.RouteAction(method, pattern)
		rows, err := f.Admin.Query(bg, `SELECT action, resource_type, COALESCE(resource_id::text, ''), COALESCE(actor_id::text, ''),
				actor_type, COALESCE(changes->>'route', ''), changes ? 'values'
			FROM audit_log WHERE organization_id = $1 AND request_id = $2`, f.OrgA, requestID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
			var action, resourceType, resourceID, actorID, actorType, gotRoute string
			var values bool
			if err = rows.Scan(&action, &resourceType, &resourceID, &actorID, &actorType, &gotRoute, &values); err != nil {
				t.Fatal(err)
			}
			if action != wantAction || resourceType != wantResource || resourceID != orderID || gotRoute != route {
				t.Errorf("%s: entry %s %s %s route %q, want %s %s %s", route, action, resourceType, resourceID, gotRoute,
					wantAction, wantResource, orderID)
			}
			if actorID != actor || actorType != "user" {
				t.Errorf("%s: actor %s (%s), want %s (user)", route, actorID, actorType, actor)
			}
			spec, _ := audit.RouteSnapshotFor(route)
			if values != (spec.Table != "") {
				t.Errorf("%s: entry with values %v, register table %q", route, values, spec.Table)
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: %d audit entries for the request, want 1", route, n)
		}
	}

	// step runs a route that must succeed with want and checks its entry.
	step := func(as t6As, route, orderID, body string, want int) []byte {
		t.Helper()
		method, pattern, _ := strings.Cut(route, " ")
		path := strings.ReplaceAll(pattern, "{id}", orderID)
		code, out, rid := call(as, method, path, body)
		if code != want {
			t.Fatalf("%s as %s = %d, want %d: %s", route, as.User, code, want, t6Short(out))
		}
		if orderID == "" {
			orderID = t6ID(out)
		}
		audited(rid, route, orderID, as.User)
		return out
	}
	create := func(title string) string {
		t.Helper()
		id := t6ID(step(asRequester, "POST /api/v1/orders", "", `{"title":"`+title+`"}`, http.StatusCreated))
		if id == "" {
			t.Fatalf("create %s: no id", title)
		}
		return id
	}
	status := func(orderID string) (st, approvedBy string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(approved_by::text, '') FROM internal_order WHERE id = $1`,
			orderID).Scan(&st, &approvedBy); err != nil {
			t.Fatal(err)
		}
		return st, approvedBy
	}

	// 1. The full way: draft → submitted → approved (by a second user) →
	// ordered → received → closed.
	placed := create("o286 placed")
	step(asRequester, "POST /api/v1/orders/{id}/items", placed, `{"description":"o286 toner","quantity":2,"unit_price":19.5}`,
		http.StatusCreated)
	step(asRequester, "POST /api/v1/orders/{id}/submit", placed, `{}`, http.StatusOK)

	// The requester approving the own order: 403, no entry, no state change.
	code, out, rid := call(asRequester, http.MethodPost, "/api/v1/orders/"+placed+"/approve", `{}`)
	if code != http.StatusForbidden {
		t.Fatalf("self-approval = %d, want 403: %s", code, t6Short(out))
	}
	if n := e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND request_id = $2`, f.OrgA, rid); n != 0 {
		t.Errorf("self-approval wrote %d audit entries, want 0", n)
	}
	if st, by := status(placed); st != "submitted" || by != "" {
		t.Errorf("after the self-approval: status %s, approved_by %q; want submitted and none", st, by)
	}

	step(asApprover, "POST /api/v1/orders/{id}/approve", placed, `{}`, http.StatusOK)
	if st, by := status(placed); st != "approved" || by != approver {
		t.Errorf("after the approval: status %s, approved_by %s; want approved by %s", st, by, approver)
	}
	step(asRequester, "POST /api/v1/orders/{id}/place", placed, `{}`, http.StatusOK)
	// The goods receipt (WP-760) has no route yet: the order is received
	// as the receipt would leave it.
	if _, err := f.Admin.Exec(bg, `UPDATE internal_order SET status = 'received' WHERE id = $1 AND status = 'ordered'`, placed); err != nil {
		t.Fatal(err)
	}
	step(asRequester, "POST /api/v1/orders/{id}/close", placed, `{}`, http.StatusOK)

	// 2. Submitted and rejected by the approver.
	rejected := create("o286 rejected")
	step(asRequester, "POST /api/v1/orders/{id}/submit", rejected, `{}`, http.StatusOK)
	step(asApprover, "POST /api/v1/orders/{id}/reject", rejected, `{}`, http.StatusOK)

	// 3. Cancelled in draft.
	cancelled := create("o286 cancelled")
	step(asRequester, "POST /api/v1/orders/{id}/cancel", cancelled, `{}`, http.StatusOK)

	// 4. A draft deleted.
	deleted := create("o286 deleted")
	step(asRequester, "DELETE /api/v1/orders/{id}", deleted, "", http.StatusNoContent)

	for _, want := range []struct {
		order   string
		actions []string
	}{
		{placed, []string{"orders.approve", "orders.close", "orders.create", "orders.items", "orders.place", "orders.submit"}},
		{rejected, []string{"orders.create", "orders.reject", "orders.submit"}},
		{cancelled, []string{"orders.cancel", "orders.create"}},
		{deleted, []string{"orders.create", "orders.delete"}},
	} {
		// Exactly the entries of the successful requests; the refused
		// self-approval left none.
		var got string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(string_agg(action, ',' ORDER BY action), '')
			FROM audit_log WHERE organization_id = $1 AND resource_id = $2`, f.OrgA, want.order).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != strings.Join(want.actions, ",") {
			t.Errorf("audit entries of order %s: %s, want %s", want.order, got, strings.Join(want.actions, ","))
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM audit_route_intent WHERE organization_id = $1`, f.OrgA); n != 0 {
		t.Errorf("%d audit intents outlived their requests", n)
	}
}
