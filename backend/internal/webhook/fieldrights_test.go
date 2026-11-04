package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Field permissions of webhook payloads (RBA-07, E-43, WP-730): the
// dispatcher projects each payload per subscription before it is stored,
// with the policy of the bound service account or, for an unbound
// subscription, the default policy without role.

const (
	frOrg      = "org-730"
	frRole     = "00000000-0000-4000-8000-000000000730"
	frServer   = "00000000-0000-4000-8000-0000000000aa" // CI type
	frCI       = "00000000-0000-4000-8000-0000000000c1"
	frClient   = "00000000-0000-4000-8000-0000000000d1"
	frSecret   = "wp730-secret"
	frSerial   = "sn-730-none"
	frSalary   = "salary-730-sensitive"
	frHostname = "host-730-readable"
)

// frAccess grants the read rights of fakeAccess and resolves the field
// policies like user.ServiceAccountAccess: an account by its grants, an
// unbound subscription ("") by the default grant without role.
type frAccess struct {
	fakeAccess
	resolver *permission.FieldResolver
	grants   map[string][]identity.Grant
}

func (a frAccess) FieldPolicy(ctx context.Context, orgID, serviceAccountID string) (*permission.FieldPolicy, error) {
	if serviceAccountID == "" {
		grant := identity.Grant{}
		for _, kind := range permission.ObjectKinds {
			grant.Permissions = append(grant.Permissions, identity.Permission(string(kind)+":read"))
		}
		return a.resolver.PolicyForGrants(ctx, orgID, []identity.Grant{grant}, nil)
	}
	return a.resolver.PolicyForGrants(ctx, orgID, a.grants[serviceAccountID], nil)
}

// nilPolicyAccess resolves the nil policy (an access without resolver).
type nilPolicyAccess struct{ fakeAccess }

func (nilPolicyAccess) FieldPolicy(context.Context, string, string) (*permission.FieldPolicy, error) {
	return nil, nil
}

// frCIs is the CI lookup of override.changed.
type frCIs map[string]*CIFieldScope

func (c frCIs) CIFieldScope(_ context.Context, _, ciID string) (*CIFieldScope, error) {
	return c[ciID], nil
}

func strPtr(s string) *string { return &s }

// newFRAccess builds the grants of the tests: the role of sa-1 reads CIs,
// relationships, assets and contracts, but not core.serial_number, the
// attribute owner_secret, the relationship type backs_up and the asset
// supplier; attributes.salary is sensitive without grant.
func newFRAccess(t *testing.T) frAccess {
	t.Helper()
	bg := context.Background()
	mem := permission.NewMemoryFieldGrants()
	mem.SetRelationshipType("00000000-0000-4000-8000-0000000000b1", "backs_up")
	for _, g := range []permission.FieldGrant{
		{ObjectKind: permission.KindCI, FieldKey: strPtr("core.serial_number"), Level: permission.LevelNone},
		{ObjectKind: permission.KindCI, FieldKey: strPtr("attributes.owner_secret"), Level: permission.LevelNone},
		{ObjectKind: permission.KindRelationship, RelationshipTypeID: strPtr("00000000-0000-4000-8000-0000000000b1"), Level: permission.LevelNone},
		{ObjectKind: permission.KindAsset, FieldKey: strPtr("core.supplier"), Level: permission.LevelNone},
		{ObjectKind: permission.KindContract, FieldKey: strPtr("core.value"), Level: permission.LevelNone},
	} {
		g.RoleID = strPtr(frRole)
		if _, err := mem.CreateFieldGrant(bg, frOrg, &g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mem.CreateSensitiveField(bg, frOrg, &permission.SensitiveField{ObjectKind: permission.KindCI, FieldKey: "attributes.salary"}); err != nil {
		t.Fatal(err)
	}
	perms := []identity.Permission{"ci:read", "relationship:read", "asset:read", "contract:read"}
	return frAccess{
		fakeAccess: fakeAccess{"sa-1|ci:read|" + frClient: true, "sa-1|ci:read|*": true, "sa-1|asset:read|*": true,
			"sa-1|contract:read|" + frClient: true, "sa-1|contract:read|*": true},
		resolver: permission.NewFieldResolver(nil, nil, mem, nil),
		grants:   map[string][]identity.Grant{"sa-1": {{RoleID: frRole, Permissions: perms}}},
	}
}

// frReceived is one delivery the subscriber received.
type frReceived struct {
	path      string
	body      []byte
	signature string
}

// frEnv is a dispatcher with a bound (sa-1) and an unbound subscription
// for every event.
type frEnv struct {
	t        *testing.T
	d        *Dispatcher
	repo     *MemoryRepository
	store    *MemoryDeliveryStore
	mu       sync.Mutex
	received []frReceived
	fail     bool
}

func newFREnv(t *testing.T, access SubscriberAccess, cis CIFieldScopes, events ...string) *frEnv {
	t.Helper()
	e := &frEnv{t: t, repo: NewMemoryRepository(), store: NewMemoryDeliveryStore()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.received = append(e.received, frReceived{path: r.URL.Path, body: body, signature: r.Header.Get("X-Reticora-Signature")})
		if e.fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	for _, sub := range []Subscription{
		{Name: "bound", URL: srv.URL + "/bound", ServiceAccountID: "sa-1"},
		{Name: "unbound", URL: srv.URL + "/unbound"},
	} {
		sub.OrganizationID, sub.Secret, sub.Events, sub.IsActive = frOrg, frSecret, events, true
		if err := e.repo.Create(context.Background(), &sub); err != nil {
			t.Fatal(err)
		}
	}
	e.d = NewDispatcher(e.repo, srv.Client(), DispatcherOptions{Access: access, CIs: cis, Deliveries: e.store, PollInterval: -1})
	t.Cleanup(func() { _ = e.d.Shutdown(context.Background()) })
	return e
}

// dispatch dispatches the event and waits for its stored deliveries to be
// attempted; it returns the payload stored for the event per subscription
// name (none for a subscription the event was withheld from).
func (e *frEnv) dispatch(event string, payload any) map[string][]byte {
	e.t.Helper()
	attempts := e.attempted()
	before := e.stored()
	e.d.Dispatch(context.Background(), frOrg, event, payload)
	out := map[string][]byte{}
	for name, payloads := range e.stored() {
		if len(payloads) > len(before[name]) {
			out[name] = payloads[len(payloads)-1]
		}
	}
	waitFor(e.t, func() bool { return e.attempted()-attempts >= len(out) })
	return out
}

// attempted counts the attempts the subscriber received.
func (e *frEnv) attempted() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.received)
}

// stored returns the stored payloads per subscription name, oldest first.
func (e *frEnv) stored() map[string][][]byte {
	e.t.Helper()
	out := map[string][][]byte{}
	subs, _, err := e.repo.List(context.Background(), frOrg, api.PaginationParams{Limit: 10})
	if err != nil {
		e.t.Fatal(err)
	}
	for i := range subs {
		recs, _, err := e.store.ListBySubscription(context.Background(), frOrg, subs[i].ID, api.PaginationParams{Limit: 100})
		if err != nil {
			e.t.Fatal(err)
		}
		for j := len(recs) - 1; j >= 0; j-- {
			out[subs[i].Name] = append(out[subs[i].Name], recs[j].Payload)
		}
	}
	return out
}

// ciChanged is the data of ci.changed of the outbox (ci.recordEvent).
func ciChanged(changed ...string) map[string]any {
	return map[string]any{
		"id": frCI, "organization_id": frOrg, "client_id": frClient, "ci_type_id": frServer, "name": "ci-730",
		"status": "active", "hostname": frHostname, "serial_number": frSerial, "version": 3,
		"attributes":     map[string]any{"salary": frSalary, "owner_secret": "owner-730", "rack": "r1"},
		"changed_fields": changed,
	}
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func TestWebhookPayloadFieldRights(t *testing.T) {
	access := newFRAccess(t)

	t.Run("ci.changed: sensitive attribute, unreadable fields and changed_fields", func(t *testing.T) {
		e := newFREnv(t, access, nil, "ci.changed")
		got := e.dispatch("ci.changed", ciChanged("hostname", "serial_number", "attributes"))
		bound, unbound := decode(t, got["bound"]), decode(t, got["unbound"])
		for _, s := range []string{frSerial, frSalary, "owner-730"} {
			if strings.Contains(string(got["bound"]), s) {
				t.Errorf("bound payload leaks %q: %s", s, got["bound"])
			}
		}
		if bound["hostname"] != frHostname || bound["name"] != "ci-730" || bound["id"] != frCI {
			t.Errorf("bound payload lost readable fields: %v", bound)
		}
		if _, ok := bound["serial_number"]; ok {
			t.Errorf("bound payload carries serial_number: %v", bound)
		}
		if attrs := bound["attributes"].(map[string]any); len(attrs) != 1 || attrs["rack"] != "r1" {
			t.Errorf("bound attributes %v, want only rack", attrs)
		}
		if changed := bound["changed_fields"].([]any); len(changed) != 2 || changed[0] != "hostname" || changed[1] != "attributes" {
			t.Errorf("bound changed_fields %v, want hostname and attributes", changed)
		}
		// The unbound subscription: the sensitive attribute is closed, the
		// grants of the role do not apply (serial_number, owner_secret).
		if strings.Contains(string(got["unbound"]), frSalary) {
			t.Errorf("unbound payload leaks the sensitive attribute: %s", got["unbound"])
		}
		attrs := unbound["attributes"].(map[string]any)
		if unbound["serial_number"] != frSerial || attrs["owner_secret"] != "owner-730" || attrs["rack"] != "r1" {
			t.Errorf("unbound payload lost fields without sensitivity: %v", unbound)
		}
		if changed := unbound["changed_fields"].([]any); len(changed) != 3 {
			t.Errorf("unbound changed_fields %v, want all three", changed)
		}
	})

	t.Run("ci.changed with only unreadable fields is withheld", func(t *testing.T) {
		e := newFREnv(t, access, nil, "ci.changed")
		got := e.dispatch("ci.changed", ciChanged("serial_number"))
		if _, ok := got["bound"]; ok {
			t.Errorf("bound subscription received a change of an unreadable field only: %s", got["bound"])
		}
		if _, ok := got["unbound"]; !ok {
			t.Error("unbound subscription missed the change")
		}
		// An attribute change whose only readable attribute is gone.
		payload := ciChanged("attributes")
		payload["attributes"] = map[string]any{"owner_secret": "x"}
		if got = e.dispatch("ci.changed", payload); got["bound"] != nil || got["unbound"] == nil {
			t.Errorf("change of unreadable attributes only: bound %s, unbound %s; want only unbound", got["bound"], got["unbound"])
		}
	})

	t.Run("direct dispatch ci.updated of the whole CI", func(t *testing.T) {
		e := newFREnv(t, access, nil, "ci.updated")
		payload := ciChanged()
		delete(payload, "changed_fields")
		payload["asset"] = map[string]any{"id": "a-1", "asset_tag": "AT-1", "name": "asset", "supplier": "supplier-730", "status": "active"}
		got := e.dispatch("ci.updated", payload)
		for _, s := range []string{frSerial, frSalary, "supplier-730"} {
			if strings.Contains(string(got["bound"]), s) {
				t.Errorf("bound payload leaks %q: %s", s, got["bound"])
			}
		}
		if facts := decode(t, got["bound"])["asset"].(map[string]any); facts["asset_tag"] != "AT-1" {
			t.Errorf("bound asset facts %v, want asset_tag", facts)
		}
	})

	t.Run("override.changed on an unreadable field is withheld", func(t *testing.T) {
		cis := frCIs{frCI: {TypeID: frServer, ClientID: frClient}}
		e := newFREnv(t, access, cis, "override.changed")
		got := e.dispatch("override.changed", map[string]any{"id": "fv-1", "ci_id": frCI, "field_name": "serial_number",
			"discovered_value": frSerial, "override_value": "sn-override"})
		if _, ok := got["bound"]; ok {
			t.Errorf("bound subscription received the override of an unreadable field: %s", got["bound"])
		}
		if _, ok := got["unbound"]; !ok {
			t.Error("unbound subscription missed the override of a field without sensitivity")
		}
		got = e.dispatch("override.changed", map[string]any{"id": "fv-2", "ci_id": frCI, "field_name": "salary", "override_value": frSalary})
		if len(got) != 0 {
			t.Errorf("the override of a sensitive attribute was delivered: %v", got)
		}
		got = e.dispatch("override.changed", map[string]any{"id": "fv-3", "ci_id": frCI, "field_name": "hostname", "override_value": "h-730"})
		if !strings.Contains(string(got["bound"]), "h-730") {
			t.Errorf("bound subscription missed the override of a readable field: %s", got["bound"])
		}
		// Without the CI lookup the type is unknown: withheld.
		e = newFREnv(t, access, nil, "override.changed")
		if got = e.dispatch("override.changed", map[string]any{"id": "fv-4", "ci_id": frCI, "field_name": "hostname"}); got["bound"] != nil {
			t.Errorf("override without CI lookup delivered: %s", got["bound"])
		}
	})

	t.Run("relationship of an unreadable type is withheld", func(t *testing.T) {
		e := newFREnv(t, access, nil, "relationship.created")
		got := e.dispatch("relationship.created", map[string]any{"id": "r-1", "source_ci_id": frCI, "target_ci_id": frCI, "rel_type": "backs_up"})
		if _, ok := got["bound"]; ok {
			t.Errorf("bound subscription received an edge of an unreadable type: %s", got["bound"])
		}
		got = e.dispatch("relationship.created", map[string]any{"id": "r-2", "source_ci_id": frCI, "target_ci_id": frCI, "rel_type": "runs_on"})
		if _, ok := got["bound"]; !ok {
			t.Error("bound subscription missed an edge of a readable type")
		}
	})

	t.Run("fail-closed: no rule, foreign form", func(t *testing.T) {
		e := newFREnv(t, access, nil, "ci.changed", "contract.expiring")
		// The run frame of send_webhook under a CI event name.
		got := e.dispatch("ci.changed", map[string]any{"run_id": "run-1", "context": map[string]any{"serial_number": frSerial}})
		if _, ok := got["bound"]; ok {
			t.Errorf("bound subscription received a workflow frame: %s", got["bound"])
		}
		if _, ok := got["unbound"]; ok {
			t.Errorf("unbound subscription received a workflow frame although its policy restricts fields: %s", got["unbound"])
		}
		// contract.expiring has no projection rule: withheld while the
		// policy restricts contract fields.
		got = e.dispatch("contract.expiring", map[string]any{"id": "k-1", "client_id": frClient, "expires_at": "2027-01-01"})
		if _, ok := got["bound"]; ok {
			t.Errorf("bound subscription received an event without projection rule: %s", got["bound"])
		}
		if _, ok := got["unbound"]; !ok {
			t.Error("unbound subscription (no contract restriction) missed contract.expiring")
		}
		// A snapshot without ci_type_id has not the form of its type.
		got = e.dispatch("ci.changed", map[string]any{"id": frCI, "client_id": frClient, "serial_number": frSerial})
		if _, ok := got["bound"]; ok {
			t.Errorf("bound subscription received a CI payload without type: %s", got["bound"])
		}
	})

	t.Run("the nil policy (no resolver) reads nothing", func(t *testing.T) {
		e := newFREnv(t, nilPolicyAccess{access.fakeAccess}, nil, "ci.changed", "relationship.created")
		got := e.dispatch("ci.changed", ciChanged("status", "hostname"))
		for _, s := range []string{frHostname, frSerial, frSalary, "r1"} {
			if strings.Contains(string(got["unbound"])+string(got["bound"]), s) {
				t.Errorf("nil policy leaks %q: %s %s", s, got["bound"], got["unbound"])
			}
		}
		if !strings.Contains(string(got["unbound"]), frCI) {
			t.Errorf("nil policy dropped the structural fields: %s", got["unbound"])
		}
		if got = e.dispatch("relationship.created", map[string]any{"id": "r-3", "source_ci_id": frCI, "target_ci_id": frCI, "rel_type": "runs_on"}); len(got) != 0 {
			t.Errorf("nil policy delivered an edge: %v", got)
		}
	})

	t.Run("an organization without field permissions gets the payload unchanged", func(t *testing.T) {
		open := frAccess{fakeAccess: access.fakeAccess, resolver: permission.NewFieldResolver(nil, nil, permission.NewMemoryFieldGrants(), nil),
			grants: access.grants}
		e := newFREnv(t, open, nil, "ci.changed", "custom.event")
		payload := ciChanged("serial_number")
		want, _ := json.Marshal(payload)
		got := e.dispatch("ci.changed", payload)
		if string(got["bound"]) != string(want) || string(got["unbound"]) != string(want) {
			t.Errorf("payload changed without field permissions:\n%s\n%s\nwant %s", got["bound"], got["unbound"], want)
		}
		if got = e.dispatch("custom.event", map[string]any{"run_id": "r", "context": map[string]any{}}); got["unbound"] == nil {
			t.Error("unbound subscription missed an event without rule in an organization without field permissions")
		}
	})
}

// TestWebhookProjectedPayloadIsStoredSignedAndRetried covers WP-730: the
// stored payload is the projected one, the signature covers it, and a
// retry sends the stored payload unchanged.
func TestWebhookProjectedPayloadIsStoredSignedAndRetried(t *testing.T) {
	e := newFREnv(t, newFRAccess(t), nil, "ci.changed")
	e.mu.Lock()
	e.fail = true
	e.mu.Unlock()
	stored := e.dispatch("ci.changed", ciChanged("hostname", "serial_number"))
	first := e.receivedAt("/bound")
	if len(first) != 1 || string(first[0].body) != string(stored["bound"]) {
		t.Fatalf("sent %d bodies, want the stored payload %s", len(first), stored["bound"])
	}
	if strings.Contains(string(first[0].body), frSerial) {
		t.Fatalf("sent payload leaks the serial number: %s", first[0].body)
	}
	mac := hmac.New(sha256.New, []byte(frSecret))
	_, _ = mac.Write(first[0].body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); first[0].signature != want {
		t.Errorf("signature %s, want the HMAC of the projected payload %s", first[0].signature, want)
	}

	// The failed first attempts are recorded before the clock moves on.
	waitFor(t, func() bool {
		subs, _, _ := e.repo.List(context.Background(), frOrg, api.PaginationParams{Limit: 10})
		for i := range subs {
			recs, _, _ := e.store.ListBySubscription(context.Background(), frOrg, subs[i].ID, api.PaginationParams{Limit: 10})
			if len(recs) != 1 || recs[0].Status != StatusFailed {
				return false
			}
		}
		return len(subs) == 2
	})
	e.mu.Lock()
	e.fail = false
	e.mu.Unlock()
	later := time.Now().Add(time.Hour)
	e.d.now = func() time.Time { return later }
	e.d.ProcessDue(context.Background())
	waitFor(t, func() bool { return len(e.receivedAt("/bound")) == 2 })
	retried := e.receivedAt("/bound")[1]
	if string(retried.body) != string(first[0].body) || retried.signature != first[0].signature {
		t.Errorf("retry sent %s (%s), want the stored payload %s", retried.body, retried.signature, first[0].body)
	}
}

// receivedAt returns the deliveries the subscriber received on path.
func (e *frEnv) receivedAt(path string) []frReceived {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []frReceived
	for _, r := range e.received {
		if r.path == path {
			out = append(out, r)
		}
	}
	return slices.Clip(out)
}
