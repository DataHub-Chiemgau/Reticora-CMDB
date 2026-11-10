package webhook

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// TestEventScopeResolution covers WP-356 (RBA-08): every subscribable event
// type that EventReadPermission assigns a read permission has a scope source
// (payload field, object ID or explicitly org-wide) or is explicitly not
// resolvable; per type the payloads of its emitters yield the object scope
// or the references to resolve, and a payload without scope and without a
// resolvable object ID yields neither (unknown scope, not org-wide).
func TestEventScopeResolution(t *testing.T) {
	const ci1, ci2, asset1, item1 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	ref := func(kind ObjectKind, id string) ObjectRef { return ObjectRef{Kind: kind, ID: id} }
	shared := []ObjectScope{{}}
	type result struct {
		objects []ObjectScope
		refs    []ObjectRef
	}
	unknown := result{}
	cases := []struct {
		event   string
		payload string
		want    result
	}{
		{"ci.created", `{"id":"` + ci1 + `","client_id":"c1","name":"a"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"ci.created", `{"id":"` + ci1 + `","name":"a"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"ci.created", `{"name":"a"}`, unknown},
		{"ci.updated", `{"id":"` + ci1 + `","client_id":"c1","site_id":"s1"}`, result{objects: []ObjectScope{{ClientID: "c1", SiteID: "s1"}}}},
		{"ci.updated", `{"id":"` + ci1 + `","site_id":"s1"}`, result{objects: []ObjectScope{{SiteID: "s1"}}}},
		{"ci.changed", `{"id":"` + ci1 + `","changed_fields":["status"]}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"ci.changed", `{"id":"` + ci1 + `","client_id":7}`, unknown},
		{"ci.deleted", `{"id":"` + ci1 + `"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"ci.restored", `{"id":"` + ci1 + `","client_id":null}`, result{objects: shared}},
		{"ci.status_changed", `{"id":"` + ci1 + `"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"ci.offline", `{"id":"` + ci1 + `","client_id":"c1","site_id":null,"status":"unknown"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"ci.online", `{"id":"` + ci1 + `","client_id":null,"site_id":null}`, result{objects: shared}},
		{"ci.merged", `{"survivor_id":"` + ci1 + `","victim_id":"` + ci2 + `","merge_id":"m"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1), ref(ObjectCI, ci2)}}},
		{"ci.merged", `{"survivor_id":"` + ci1 + `"}`, unknown},
		{"ci.merged", `{"survivor_id":"` + ci1 + `","victim_id":"` + ci2 + `","client_id":"c1"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1), ref(ObjectCI, ci2)}}},
		{"relationship.created", `{"id":"r","source_ci_id":"` + ci1 + `","target_ci_id":"` + ci2 + `"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1), ref(ObjectCI, ci2)}}},
		{"relationship.removed", `{"id":"r","source_ci_id":"` + ci1 + `","target_ci_id":"` + ci2 + `","reason":"stale"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1), ref(ObjectCI, ci2)}}},
		{"relationship.removed", `{"id":"r","source_ci_id":"` + ci1 + `"}`, unknown},
		{"relationship.deleted", `{"id":"r","source_ci_id":"` + ci1 + `","target_ci_id":""}`, unknown},
		{"ci_type.created", `{"id":"t","key":"server"}`, result{objects: shared}},
		{"ci_type.updated", `{"id":"t"}`, result{objects: shared}},
		{"ci_type.deactivated", `{"id":"t"}`, result{objects: shared}},
		{"ci_attribute.changed", `{"id":"d","ci_type_id":"t","key":"rack"}`, result{objects: shared}},
		{"ci_attribute.changed", `{"name":"rack"}`, result{objects: shared}},
		{"ci_attribute.changed", `{"ci_id":"` + ci1 + `","name":"rack"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"ci_attribute.changed", `{"ci_id":"","name":"rack"}`, unknown},
		// lifecycle.transitioned follows lifecycle.changed (WP-246).
		{"lifecycle.transitioned", `{"ci_id":"` + ci1 + `"}`, unknown},
		{"lifecycle.transitioned", `{"entity_type":"ci","entity_id":"` + ci1 + `"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"override.changed", `{"id":"f","ci_id":"` + ci1 + `","field_name":"name"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"override.changed", `{"id":"f"}`, unknown},
		{"reconciliation.policy_changed", `{"id":"p","priorities":["manual"]}`, result{objects: shared}},
		// A workflow's send_webhook payload under an organizational type.
		{"reconciliation.policy_changed", `{"run_id":"r","context":{"client_id":"c2"}}`, unknown},
		{"ci_type.created", `{"id":""}`, unknown},
		// asset.movement and inventory.movement follow stock.movement.
		{"asset.movement", `{"asset_id":"` + asset1 + `","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"asset.movement", `{"id":"m"}`, unknown},
		{"inventory.movement", `{"id":"m","item_kind":"asset","asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"inventory.movement", `{"id":"m","item_kind":"quantity_item","quantity_item_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		// consumable_id (WP-284, WP-844) beside the alias quantity_item_id (WP-757).
		{"inventory.movement", `{"id":"m","item_kind":"consumable","consumable_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectConsumable, item1)}}},
		{"inventory.movement", `{"id":"m"}`, unknown},
		{"reservation.created", `{"id":"r","item_kind":"consumable","consumable_id":"` + item1 + `","location_id":"` + ci2 + `"}`, result{refs: []ObjectRef{ref(ObjectConsumable, item1)}}},
		{"reservation.created", `{"id":"r","consumable_id":"` + item1 + `","location_id":"` + ci2 + `","client_id":"c1","site_id":"s2"}`, result{objects: []ObjectScope{{ClientID: "c1", SiteID: "s2"}}}},
		{"reservation.created", `{"id":"r","consumable_id":""}`, unknown},
		{"reservation.created", `{"id":"r","asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"reservation.released", `{"id":"r","quantity_item_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		{"composition.created", `{"id":"c","parent_asset_id":"` + asset1 + `","child_ci_id":"` + ci1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1), ref(ObjectCI, ci1)}}},
		{"composition.updated", `{"id":"c","parent_asset_id":"` + asset1 + `","child_asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1), ref(ObjectAsset, asset1)}}},
		{"composition.updated", `{"id":"c","child_ci_id":"` + ci1 + `"}`, unknown},
		{"composition.deleted", `{"id":"c"}`, unknown},
		{"discovery.completed", `{"run_id":"r","client_id":"c1"}`, unknown},
		// The phase 2 types (EVT-03, WP-246): the payload frame's
		// client_id/site_id, else CIs, assets and quantity items by ID.
		{"lifecycle.changed", `{"entity_type":"asset","entity_id":"` + asset1 + `","to":"in_use"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"lifecycle.changed", `{"entity_type":"ci","entity_id":"` + ci1 + `","client_id":"c1","to":"retired"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"lifecycle.changed", `{"entity_type":"location","entity_id":"` + ci1 + `"}`, unknown},
		{"lifecycle.changed", `{"entity_type":"asset"}`, unknown},
		{"relationship.changed", `{"id":"r","source_ci_id":"` + ci1 + `","target_ci_id":"` + ci2 + `"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1), ref(ObjectCI, ci2)}}},
		{"asset.created", `{"id":"` + asset1 + `","client_id":"c1","site_id":"s1"}`, result{objects: []ObjectScope{{ClientID: "c1", SiteID: "s1"}}}},
		{"asset.changed", `{"id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"asset.deleted", `{"id":"` + asset1 + `","client_id":null}`, result{objects: shared}},
		{"asset.assigned", `{"id":"` + asset1 + `","site_id":"s1"}`, result{objects: []ObjectScope{{SiteID: "s1"}}}},
		{"asset.returned", `{"id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"asset.warranty_expiring", `{"id":"` + asset1 + `","client_id":"c2","expires_at":"2026-12-31"}`, result{objects: []ObjectScope{{ClientID: "c2"}}}},
		{"asset.warranty_expiring", `{"expires_at":"2026-12-31"}`, unknown},
		{"contract.expiring", `{"id":"k","client_id":"c1","expires_at":"2026-12-31"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"contract.expiring", `{"id":"k","expires_at":"2026-12-31"}`, unknown},
		{"stock.movement", `{"id":"m","item_kind":"quantity_item","quantity_item_id":"` + item1 + `","movement_type":"issue"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		{"stock.movement", `{"id":"m","item_kind":"asset","asset_id":"` + asset1 + `","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"stock.below_minimum", `{"id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		{"stock.below_minimum", `{"id":"` + item1 + `","site_id":"s1"}`, result{objects: []ObjectScope{{SiteID: "s1"}}}},
		{"reservation.expired", `{"id":"r","item_kind":"asset","asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"reservation.converted", `{"id":"r","item_kind":"quantity_item","quantity_item_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		{"reservation.expired", `{"id":"r","item_kind":"consumable","consumable_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectConsumable, item1)}}},
		{"stock.movement", `{"id":"m","item_kind":"consumable","consumable_id":"` + item1 + `","movement_type":"issue"}`, result{refs: []ObjectRef{ref(ObjectConsumable, item1)}}},
		{"ticket.updated", `{"id":"t","client_id":"c1","site_id":"s1"}`, result{objects: []ObjectScope{{ClientID: "c1", SiteID: "s1"}}}},
		{"ticket.updated", `{"id":"t"}`, unknown},
		{"ticket.sla_breached", `{"id":"t","client_id":"c2"}`, result{objects: []ObjectScope{{ClientID: "c2"}}}},
		{"metamodel.published", `{"id":"p"}`, result{objects: shared}},
		{"document.scanned", `{"id":"d"}`, result{objects: shared}},
		{"import.completed", `{"id":"i"}`, result{objects: shared}},
		{"import.completed", `{}`, unknown},
		// Monitoring of phase 3 (WP-867): client_id/site_id of the payload.
		{"probe.state_changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"alarm.opened", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"alarm.acknowledged", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"alarm.escalated", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"alarm.severity_changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"alarm.resolved", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"incident.opened", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"incident.updated", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"incident.resolved", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"collector_group.unreachable", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"collector_group.reachable", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"collector_group.failover", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"collector.clock_skew", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"config.changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"ci.config_changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"capacity.forecast_exceeded", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"sla.violated", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"monitoring.suspension_changed", `{"id":"o","feature_key":"monitoring_network"}`, result{objects: shared}},
		// Modules of phase 3 (WP-868).
		{"maintenance.scheduled", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"maintenance.started", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"maintenance.ended", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.run_started", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.run_succeeded", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.run_failed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.run_cancelled", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.approval_requested", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.approval_decided", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"workflow.webhook", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"finding.created", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"finding.changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"finding.resolved", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"notification.escalated", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"notification.acknowledged", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"entitlement.changed", `{"id":"o"}`, result{objects: shared}},
		{"org.export_completed", `{"id":"o"}`, result{objects: shared}},
		{"org.deletion_requested", `{"id":"o"}`, result{objects: shared}},
		{"org.deletion_cancelled", `{"id":"o"}`, result{objects: shared}},
		// Network domain of phase 3 (WP-870).
		{"ipam.subnet.changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"ipam.address.state_changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"asn.changed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"credential.rotated", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"credential.rotation_due", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"credential.resolution_failed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		{"admp.connection.refreshed", `{"id":"x","client_id":"c1"}`, result{objects: []ObjectScope{{ClientID: "c1"}}}},
		// Payloads that are no JSON object.
		{"ci.created", `["` + ci1 + `"]`, unknown},
		{"ci_type.created", `null`, unknown},
		// Types without a read permission have no rule.
		{"workflow.event", `{"id":"` + ci1 + `","client_id":"c1"}`, unknown},
	}

	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.event] = true
		objects, refs := payloadObjects(tc.event, []byte(tc.payload))
		if got := (result{objects: objects, refs: refs}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s: objects %v refs %v, want objects %v refs %v", tc.event, tc.payload, objects, refs, tc.want.objects, tc.want.refs)
		}
	}

	// Completeness: every subscribable type with a read permission has a
	// rule and a case; every rule names a subscribable type.
	var missing []string
	for event := range ValidEvents {
		if !hasReadPermission(event) {
			continue
		}
		if _, ok := scopeRules[event]; !ok {
			missing = append(missing, event+" (no scope rule)")
		}
		if !covered[event] {
			missing = append(missing, event+" (no case)")
		}
	}
	for event := range scopeRules {
		if !ValidEvents[event] || !hasReadPermission(event) {
			missing = append(missing, event+" (rule for a type that is not subscribable with a read permission)")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("scope resolution incomplete:\n%s", strings.Join(missing, "\n"))
	}
}

// hasReadPermission reports whether the event has a read permission, by
// its name or by the entity_type of its payload (lifecycle.*).
func hasReadPermission(event string) bool {
	return EventReadPermission(event) != "" || readByEntityType(event)
}

// fakeResolver resolves the CIs it knows.
type fakeResolver map[string]ObjectScope

func (f fakeResolver) ResolveObjects(_ context.Context, _ string, refs []ObjectRef) ([]ObjectScope, error) {
	out := make([]ObjectScope, 0, len(refs))
	for _, ref := range refs {
		s, ok := f[ref.ID]
		if !ok {
			return nil, ErrObjectUnresolved
		}
		out = append(out, s)
	}
	return out, nil
}

// TestDispatcherDeliversEventsOfUnknownScopeOnlyOrgWide covers WP-356
// (RBA-08): an event without client_id/site_id and without a resolvable
// object reaches unbound subscriptions and accounts that read org-wide, not
// a client-1 account; a resolved object without client and an
// organizational event reach the client-1 account as well.
func TestDispatcherDeliversEventsOfUnknownScopeOnlyOrgWide(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	access := fakeAccess{"sa-c1|ci:read|c1": true, "sa-c1|ci:read|": true,
		"sa-all|ci:read|c1": true, "sa-all|ci:read|c2": true, "sa-all|ci:read|": true, "sa-all|ci:read|*": true}
	const ciC1, ciShared, ciC2 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	objects := fakeResolver{ciC1: {ClientID: "c1"}, ciShared: {}, ciC2: {ClientID: "c2"}}

	run := func(resolver ObjectResolver, event string, payload any) map[string]int {
		t.Helper()
		mu.Lock()
		got = map[string]int{}
		mu.Unlock()
		repo := NewMemoryRepository()
		for _, sub := range []Subscription{
			{Name: "unbound", URL: server.URL + "/unbound"},
			{Name: "client1", URL: server.URL + "/client1", ServiceAccountID: "sa-c1"},
			{Name: "orgwide", URL: server.URL + "/orgwide", ServiceAccountID: "sa-all"},
		} {
			sub.OrganizationID, sub.Secret, sub.Events, sub.IsActive = "org-1", "secret", []string{event}, true
			if err := repo.Create(context.Background(), &sub); err != nil {
				t.Fatal(err)
			}
		}
		d := NewDispatcher(repo, server.Client(), DispatcherOptions{Access: access, Objects: resolver, PollInterval: -1})
		d.Dispatch(context.Background(), "org-1", event, payload)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range got {
			out[strings.TrimPrefix(k, "/")] = v
		}
		return out
	}

	for _, tc := range []struct {
		name     string
		resolver ObjectResolver
		event    string
		payload  any
		want     map[string]int
	}{
		{"no scope, no object", objects, "ci.changed", map[string]any{"name": "x"}, map[string]int{"unbound": 1, "orgwide": 1}},
		{"unknown object", objects, "ci.changed", map[string]any{"id": "44444444-4444-4444-8444-444444444444"}, map[string]int{"unbound": 1, "orgwide": 1}},
		{"no resolver", nil, "ci.changed", map[string]any{"id": ciC1}, map[string]int{"unbound": 1, "orgwide": 1}},
		{"not resolvable payload", objects, "lifecycle.transitioned", map[string]any{"entity_type": "ci", "ci_id": ciC1}, map[string]int{"unbound": 1, "orgwide": 1}},
		{"lifecycle without entity_type", objects, "lifecycle.changed", map[string]any{"entity_id": ciC1, "client_id": "c1"}, map[string]int{"unbound": 1}},
		{"resolved client 1", objects, "ci.changed", map[string]any{"id": ciC1}, map[string]int{"unbound": 1, "client1": 1, "orgwide": 1}},
		{"resolved without client", objects, "ci.changed", map[string]any{"id": ciShared}, map[string]int{"unbound": 1, "client1": 1, "orgwide": 1}},
		{"merge across clients", objects, "ci.merged", map[string]any{"survivor_id": ciC1, "victim_id": ciC2}, map[string]int{"unbound": 1, "orgwide": 1}},
		{"merge within client 1", objects, "ci.merged", map[string]any{"survivor_id": ciC1, "victim_id": ciShared}, map[string]int{"unbound": 1, "client1": 1, "orgwide": 1}},
		{"organizational", nil, "ci_type.created", map[string]any{"id": "t"}, map[string]int{"unbound": 1, "client1": 1, "orgwide": 1}},
		{"workflow payload under an organizational type", nil, "ci_type.created", map[string]any{"run_id": "r", "context": map[string]any{"ci_id": ciC2}},
			map[string]int{"unbound": 1, "orgwide": 1}},
	} {
		if g := run(tc.resolver, tc.event, tc.payload); !reflect.DeepEqual(g, tc.want) {
			raw, _ := json.Marshal(tc.payload)
			t.Errorf("%s: %s %s delivered %v, want %v", tc.name, tc.event, raw, g, tc.want)
		}
	}
}

// TestDispatcherDeliversWithinTheScopeSnapshot covers E-98 (WP-601,
// TEN-09): a subscription with the scope snapshot of a restricted creator
// receives only events whose objects all lie in the snapshot; events of
// unknown scope and events about objects of the whole organization reach
// only subscriptions with an org-wide snapshot or without one (the existing
// subscriptions, delivery unchanged). The objects carry no team, so a
// team-only snapshot is not restricted by them. A subscription with a
// client-1 snapshot bound to an org-wide service account receives the
// client-1 events only: the binding does not widen the creator's scope.
func TestDispatcherDeliversWithinTheScopeSnapshot(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[strings.TrimPrefix(r.URL.Path, "/")]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	access := fakeAccess{"sa-all|ci:read|c1": true, "sa-all|ci:read|C2": true, "sa-all|ci:read|": true, "sa-all|ci:read|*": true}
	const ciC1, ciC1S2, ciC1NoSite = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	const ciShared, ciC2 = "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"
	objects := fakeResolver{ciC1: {ClientID: "c1", SiteID: "s1"}, ciC1S2: {ClientID: "c1", SiteID: "s2"}, ciC1NoSite: {ClientID: "c1"},
		ciShared: {}, ciC2: {ClientID: "C2", SiteID: "s3"}}

	run := func(event string, payload any) map[string]int {
		t.Helper()
		mu.Lock()
		got = map[string]int{}
		mu.Unlock()
		repo := NewMemoryRepository()
		for _, sub := range []Subscription{
			{Name: "legacy"},
			{Name: "orgwide", Scope: &Scope{}},
			{Name: "client1", Scope: &Scope{Clients: []string{"C1"}}},
			{Name: "site1", Scope: &Scope{Clients: []string{"c1"}, Sites: []string{"s1"}}},
			{Name: "team", Scope: &Scope{Teams: []string{"t1"}}},
			{Name: "noclient", Scope: &Scope{Clients: []string{}}},
			{Name: "bound", Scope: &Scope{Clients: []string{"c1"}}, ServiceAccountID: "sa-all"},
		} {
			sub.OrganizationID, sub.URL, sub.Secret, sub.Events, sub.IsActive = "org-1", server.URL+"/"+sub.Name, "secret", []string{event}, true
			if err := repo.Create(context.Background(), &sub); err != nil {
				t.Fatal(err)
			}
		}
		d := NewDispatcher(repo, server.Client(), DispatcherOptions{Access: access, Objects: objects, PollInterval: -1})
		// The scope of the acting principal does not matter: the dispatcher
		// reads the subscriptions of the whole organization.
		scope := database.OrgWideScope("org-1", "")
		scope.Clients = database.ScopeIDs("c2")
		d.Dispatch(database.ContextWithTenantScope(context.Background(), &scope), "org-1", event, payload)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(got)
	}

	orgWideAnd := func(names ...string) map[string]int {
		out := map[string]int{"legacy": 1, "orgwide": 1}
		for _, name := range names {
			out[name] = 1
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		event   string
		payload any
		want    map[string]int
	}{
		{"CI of client 1 at site 1", "ci.changed", map[string]any{"id": ciC1}, orgWideAnd("client1", "site1", "team", "bound")},
		{"CI of client 1 at site 2", "ci.changed", map[string]any{"id": ciC1S2}, orgWideAnd("client1", "team", "bound")},
		{"CI of client 1 without site", "ci.changed", map[string]any{"id": ciC1NoSite}, orgWideAnd("client1", "team", "bound")},
		{"payload scope client 1", "ci.created", map[string]any{"id": ciC2, "client_id": "c1"}, orgWideAnd("client1", "team", "bound")},
		{"CI of client 2", "ci.changed", map[string]any{"id": ciC2}, orgWideAnd("team")},
		{"relationship client 1 to client 2", "relationship.created", map[string]any{"source_ci_id": ciC1, "target_ci_id": ciC2}, orgWideAnd("team")},
		{"merge within client 1", "ci.merged", map[string]any{"survivor_id": ciC1, "victim_id": ciC1S2}, orgWideAnd("client1", "team", "bound")},
		{"CI without client and site", "ci.changed", map[string]any{"id": ciShared}, orgWideAnd()},
		{"merge of client 1 with a CI without client", "ci.merged", map[string]any{"survivor_id": ciC1, "victim_id": ciShared}, orgWideAnd()},
		{"organizational", "ci_type.created", map[string]any{"id": "t"}, orgWideAnd()},
		{"unknown CI", "ci.changed", map[string]any{"id": "66666666-6666-4666-8666-666666666666"}, orgWideAnd()},
		{"not resolvable type", "composition.deleted", map[string]any{"id": ciC1, "client_id": "c1"}, orgWideAnd()},
		{"event without scope rule", "workflow.custom", map[string]any{"id": ciC1, "client_id": "c1"}, orgWideAnd()},
	} {
		if g := run(tc.event, tc.payload); !reflect.DeepEqual(g, tc.want) {
			raw, _ := json.Marshal(tc.payload)
			t.Errorf("%s: %s %s delivered %v, want %v", tc.name, tc.event, raw, g, tc.want)
		}
	}
}

// TestP2EventsReachBoundSubscriptionsInScope covers EVT-03 and RBA-08
// (WP-246): per phase 2 type an event about an object of client 1 reaches
// a subscription with a client-1 snapshot bound to a client-1 account,
// one about an object of client 2 does not; the unbound org-wide
// subscription receives both. Objects of the whole organization
// (metamodel, document, import) reach both subscriptions.
func TestP2EventsReachBoundSubscriptionsInScope(t *testing.T) {
	const ci1, ci2 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	const asset1, asset2 = "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	const item1, item2 = "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"
	objects := fakeResolver{ci1: {ClientID: "c1"}, ci2: {ClientID: "c2"}, asset1: {ClientID: "c1"}, asset2: {ClientID: "c2"},
		item1: {ClientID: "c1", SiteID: "s1"}, item2: {ClientID: "c2", SiteID: "s2"}}
	access := fakeAccess{}
	for _, p := range []string{"ci:read", "asset:read", "stock:read", "contract:read", "ticket:read", "document:read", "import:run", "reservation:read", "monitoring:read",
		"maintenance:read", "workflow:read", "finding:read", "escalation:manage", "entitlement:read", "org:export",
		"ipam:read", "credential:read", "admp:read"} {
		access["sa-c1|"+p+"|c1"], access["sa-c1|"+p+"|"] = true, true
	}
	byClient := func(client string) map[string]map[string]any {
		ci, asset, item := ci1, asset1, item1
		if client == "c2" {
			ci, asset, item = ci2, asset2, item2
		}
		frame := map[string]any{"id": "x", "client_id": client}
		reservation := map[string]any{"id": "r", "item_kind": "consumable", "consumable_id": item}
		return map[string]map[string]any{
			"ci.status_changed":       {"id": ci, "changed_fields": []string{"status"}, "from": "a", "to": "b"},
			"lifecycle.changed":       {"entity_type": "asset", "entity_id": asset, "to": "in_use"},
			"relationship.changed":    {"id": "r", "source_ci_id": ci1, "target_ci_id": ci},
			"asset.created":           {"id": asset, "client_id": client},
			"asset.changed":           {"id": asset},
			"asset.deleted":           {"id": asset, "client_id": client},
			"asset.assigned":          {"id": asset},
			"asset.returned":          {"id": asset},
			"asset.warranty_expiring": {"id": asset, "expires_at": "2026-12-31"},
			"contract.expiring":       {"id": "k", "client_id": client, "expires_at": "2026-12-31"},
			"stock.movement":          {"id": "m", "item_kind": "asset", "asset_id": asset, "movement_type": "issue"},
			"stock.below_minimum":     {"id": item},
			"reservation.created":     reservation,
			"reservation.released":    reservation,
			"reservation.expired":     reservation,
			"reservation.converted":   reservation,
			"ticket.updated":          frame,
			"ticket.sla_breached":     frame,
		}
	}
	organizational := map[string]bool{"metamodel.published": true, "document.scanned": true, "import.completed": true,
		"monitoring.suspension_changed": true, "entitlement.changed": true, "org.export_completed": true,
		"org.deletion_requested": true, "org.deletion_cancelled": true}
	// The monitoring types of phase 3 (WP-867) carry the client of their
	// object, the CI types also name it as ci_id.
	withMonitoring := byClient
	byClient = func(client string) map[string]map[string]any {
		m := withMonitoring(client)
		ci := ci1
		if client == "c2" {
			ci = ci2
		}
		for _, typ := range events.Phase3Types {
			if !organizational[typ] {
				m[typ] = map[string]any{"id": "x", "ci_id": ci, "client_id": client}
			}
		}
		return m
	}
	in, out := byClient("c1"), byClient("c2")
	for typ := range events.Payloads {
		subs := []Subscription{
			{Name: "unbound", Events: []string{typ}},
			{Name: "client1", Events: []string{typ}, ServiceAccountID: "sa-c1", Scope: &Scope{Clients: []string{"c1"}}},
		}
		opts := DispatcherOptions{Access: access, Objects: objects}
		if organizational[typ] {
			// Organizational objects reach org-wide snapshots only (E-98),
			// and a bound org-wide subscription of a client-1 account.
			subs[1].Scope = nil
			got := deliverOnce(t, subs, opts, nil, typ, map[string]any{"id": "x"})
			if len(got["unbound"]) != 1 || len(got["client1"]) != 1 {
				t.Errorf("%s: delivered %v, want both subscriptions", typ, got)
			}
			continue
		}
		if in[typ] == nil {
			t.Errorf("%s: no case", typ)
			continue
		}
		if got := deliverOnce(t, subs, opts, nil, typ, in[typ]); len(got["unbound"]) != 1 || len(got["client1"]) != 1 {
			t.Errorf("%s about client 1 %v: delivered %v, want both subscriptions", typ, in[typ], got)
		}
		if got := deliverOnce(t, subs, opts, nil, typ, out[typ]); len(got["unbound"]) != 1 || len(got["client1"]) != 0 {
			t.Errorf("%s about client 2 %v: delivered %v, want the unbound subscription only", typ, out[typ], got)
		}
	}
}

// TestMonitoringEventScopes covers WP-867 (EVT-03 [P3], E-161, RBA-08):
// every monitoring type has a scope rule; probes and alarms resolve by their
// CI or by client/site of the payload, collector groups by client/site, the
// suspension of monitoring functions only org-wide; the device configuration
// ci.config_changed needs monitoring:read, never ci:read.
func TestMonitoringEventScopes(t *testing.T) {
	const ci1 = "11111111-1111-4111-8111-111111111111"
	for _, typ := range events.Phase3Types {
		if _, ok := scopeRules[typ]; !ok {
			t.Errorf("%s has no scope rule", typ)
		}
		if EventReadPermission(typ) == "" {
			t.Errorf("EventReadPermission(%s) is empty", typ)
		}
	}
	for _, c := range []struct {
		event, payload string
		objects        []ObjectScope
		refs           []ObjectRef
	}{
		{"probe.state_changed", `{"id":"p","client_id":"c1","site_id":"s1","from":"up","to":"down"}`, []ObjectScope{{ClientID: "c1", SiteID: "s1"}}, nil},
		{"alarm.opened", `{"id":"a","ci_id":"` + ci1 + `","severity":"critical"}`, nil, []ObjectRef{{Kind: ObjectCI, ID: ci1}}},
		{"collector_group.unreachable", `{"id":"g","client_id":"c1"}`, []ObjectScope{{ClientID: "c1"}}, nil},
		{"ci.config_changed", `{"id":"v","ci_id":"` + ci1 + `","hash":"h"}`, nil, []ObjectRef{{Kind: ObjectCI, ID: ci1}}},
		{"monitoring.suspension_changed", `{"id":"o","feature_key":"monitoring_network","suspended":true}`, []ObjectScope{{}}, nil},
	} {
		objects, refs := payloadObjects(c.event, []byte(c.payload))
		if !reflect.DeepEqual(objects, c.objects) || !reflect.DeepEqual(refs, c.refs) {
			t.Errorf("%s %s: objects %v refs %v, want %v %v", c.event, c.payload, objects, refs, c.objects, c.refs)
		}
	}
	if got := EventReadPermission(events.CIConfigChanged); got == "ci:read" {
		t.Error("ci.config_changed must not be readable with ci:read")
	}
}

// TestOrganizationEventsReachOrgWideSubscriptionsOnly covers WP-868 (RBA-08):
// entitlement.changed and org.deletion_requested concern the whole
// organization; a client-bound subscription does not receive them, an
// org-wide one of an account with the read permission does.
func TestOrganizationEventsReachOrgWideSubscriptionsOnly(t *testing.T) {
	for typ, perm := range map[string]string{events.EntitlementChanged: "entitlement:read", events.OrgDeletionRequested: "org:export"} {
		access := fakeAccess{"sa|" + perm + "|": true, "sa|" + perm + "|c1": true, "sa|" + perm + "|*": true}
		got := deliverOnce(t, []Subscription{
			{Name: "orgwide", Events: []string{typ}, ServiceAccountID: "sa"},
			{Name: "client1", Events: []string{typ}, ServiceAccountID: "sa", Scope: &Scope{Clients: []string{"c1"}}},
		}, DispatcherOptions{Access: access, Objects: fakeResolver{}}, nil, typ, map[string]any{"id": "o"})
		if len(got["orgwide"]) != 1 || len(got["client1"]) != 0 {
			t.Errorf("%s: delivered %v, want the org-wide subscription only", typ, got)
		}
	}
}
