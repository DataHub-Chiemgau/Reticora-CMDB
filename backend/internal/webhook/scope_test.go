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
		{"lifecycle.transitioned", `{"ci_id":"` + ci1 + `","client_id":"c1"}`, unknown},
		{"override.changed", `{"id":"f","ci_id":"` + ci1 + `","field_name":"name"}`, result{refs: []ObjectRef{ref(ObjectCI, ci1)}}},
		{"override.changed", `{"id":"f"}`, unknown},
		{"reconciliation.policy_changed", `{"id":"p","priorities":["manual"]}`, result{objects: shared}},
		// A workflow's send_webhook payload under an organizational type.
		{"reconciliation.policy_changed", `{"run_id":"r","context":{"client_id":"c2"}}`, unknown},
		{"ci_type.created", `{"id":""}`, unknown},
		{"asset.movement", `{"asset_id":"` + asset1 + `","client_id":"c1"}`, unknown},
		{"inventory.movement", `{"id":"m","item_kind":"asset","asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"inventory.movement", `{"id":"m","item_kind":"quantity_item","quantity_item_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		{"inventory.movement", `{"id":"m"}`, unknown},
		{"reservation.created", `{"id":"r","asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1)}}},
		{"reservation.released", `{"id":"r","quantity_item_id":"` + item1 + `"}`, result{refs: []ObjectRef{ref(ObjectQuantityItem, item1)}}},
		{"composition.created", `{"id":"c","parent_asset_id":"` + asset1 + `","child_ci_id":"` + ci1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1), ref(ObjectCI, ci1)}}},
		{"composition.updated", `{"id":"c","parent_asset_id":"` + asset1 + `","child_asset_id":"` + asset1 + `"}`, result{refs: []ObjectRef{ref(ObjectAsset, asset1), ref(ObjectAsset, asset1)}}},
		{"composition.updated", `{"id":"c","child_ci_id":"` + ci1 + `"}`, unknown},
		{"composition.deleted", `{"id":"c"}`, unknown},
		{"discovery.completed", `{"run_id":"r","client_id":"c1"}`, unknown},
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
		if EventReadPermission(event) == "" {
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
		if !ValidEvents[event] || EventReadPermission(event) == "" {
			missing = append(missing, event+" (rule for a type that is not subscribable with a read permission)")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("scope resolution incomplete:\n%s", strings.Join(missing, "\n"))
	}
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
		{"not resolvable type", objects, "lifecycle.transitioned", map[string]any{"ci_id": ciC1, "client_id": "c1"}, map[string]int{"unbound": 1, "orgwide": 1}},
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
		{"not resolvable type", "lifecycle.transitioned", map[string]any{"ci_id": ciC1, "client_id": "c1"}, orgWideAnd()},
		{"event without scope rule", "workflow.custom", map[string]any{"id": ciC1, "client_id": "c1"}, orgWideAnd()},
	} {
		if g := run(tc.event, tc.payload); !reflect.DeepEqual(g, tc.want) {
			raw, _ := json.Marshal(tc.payload)
			t.Errorf("%s: %s %s delivered %v, want %v", tc.name, tc.event, raw, g, tc.want)
		}
	}
}
