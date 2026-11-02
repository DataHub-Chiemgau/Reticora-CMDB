package reviews

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type recordingNotifier struct{ opened []string }

func (n *recordingNotifier) ReviewOpened(_ context.Context, item *Item) {
	n.opened = append(n.opened, item.ID)
}

// TestReviewModel covers WP-088 (REC-08, API-05): the ten types and four
// actions, the actions each type allows, deduplication per object and type,
// the notification when an item is opened, the 14-day escalation and the
// review-items resource with its deprecated discovery path.
func TestReviewModel(t *testing.T) {
	// The ten types of REC-08 plus unknown_relationship (TOP-01, WP-158).
	if len(Kinds) != 11 || len(AllowedActions) != 11 {
		t.Fatalf("types: %d, action table: %d", len(Kinds), len(AllowedActions))
	}
	for _, kind := range Kinds {
		actions := AllowedActions[kind]
		if len(actions) == 0 || !Allowed(kind, ActionDismiss) {
			t.Errorf("%s: actions %v, dismiss must always resolve", kind, actions)
		}
		for _, a := range actions {
			if a != ActionMerge && a != ActionNew && a != ActionDismiss && a != ActionAccept {
				t.Errorf("%s: unknown action %s", kind, a)
			}
		}
	}

	repo := NewMemoryRepository()
	notifier := &recordingNotifier{}
	var effects []string
	svc := NewService(repo).WithNotifier(notifier).
		WithEffect(KindAmbiguousIdentity, func(_ context.Context, item *Item, res *Resolution) (bool, error) {
			effects = append(effects, item.Kind+"/"+res.Action)
			if res.Action == ActionMerge && res.CIID == "" {
				return false, &Error{Status: http.StatusBadRequest, Message: "ci_id is required to merge"}
			}
			return false, nil
		})
	ctx := context.Background()

	// Deduplication per object and type.
	open := func(kind, key string) (*Item, bool) {
		item := &Item{OrganizationID: "org", Kind: kind, ObjectKey: key, Payload: map[string]any{"k": key}}
		created, err := svc.Open(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		return item, created
	}
	first, created := open(KindAmbiguousIdentity, "serial:sn-1")
	if !created {
		t.Fatal("first item not created")
	}
	if again, isNew := open(KindAmbiguousIdentity, "serial:sn-1"); isNew || again.ID != first.ID {
		t.Errorf("same object and type: created=%v id=%s, want the open item %s", isNew, again.ID, first.ID)
	}
	if _, isNew := open(KindUnclassifiedDevice, "serial:sn-1"); !isNew {
		t.Error("same object, other type: not created")
	}
	if len(notifier.opened) != 2 {
		t.Errorf("notifications: %v, want one per opened item", notifier.opened)
	}

	// Actions per type.
	if _, err := svc.Resolve(ctx, "org", first.ID, &Resolution{Action: ActionAccept}); !isStatus(err, http.StatusUnprocessableEntity) {
		t.Errorf("accept on ambiguous_identity: %v, want 422", err)
	}
	if _, err := svc.Resolve(ctx, "org", first.ID, &Resolution{Action: ActionMerge}); !isStatus(err, http.StatusBadRequest) {
		t.Errorf("merge without ci_id: %v, want the effect's 400", err)
	}
	resolved, err := svc.Resolve(ctx, "org", first.ID, &Resolution{Action: "create", ResolvedBy: "u1"})
	if err != nil || resolved.Status != StatusResolved || resolved.Action != ActionNew || resolved.ResolvedBy != "u1" {
		t.Fatalf("new (legacy create): %+v, %v", resolved, err)
	}
	if _, err = svc.Resolve(ctx, "org", first.ID, &Resolution{Action: ActionDismiss}); !errors.Is(err, ErrAlreadyResolved) {
		t.Errorf("second resolution: %v", err)
	}
	if len(effects) != 2 || effects[1] != "ambiguous_identity/new" {
		t.Errorf("effects: %v", effects)
	}
	// After the resolution the object may be reviewed again.
	if _, created = open(KindAmbiguousIdentity, "serial:sn-1"); !created {
		t.Error("new item after resolution not created")
	}
	// Types without an effect: dismiss works, the other actions wait for one.
	scope, _ := open(KindScopeSuggested, "cidr:10.0.0.0/24")
	if _, err = svc.Resolve(ctx, "org", scope.ID, &Resolution{Action: ActionAccept}); !isStatus(err, http.StatusUnprocessableEntity) {
		t.Errorf("accept without effect: %v", err)
	}
	if item, dismissErr := svc.Resolve(ctx, "org", scope.ID, &Resolution{Action: ActionDismiss}); dismissErr != nil || item.Status != StatusDismissed {
		t.Errorf("dismiss without effect: %+v, %v", item, dismissErr)
	}
	serial, _ := open(KindSerialMismatch, "ci:1")
	if _, err = svc.Resolve(ctx, "org", serial.ID, &Resolution{Action: ActionMerge}); !isStatus(err, http.StatusUnprocessableEntity) {
		t.Errorf("merge on serial_mismatch: %v", err)
	}

	// Escalation after 14 days.
	old, _ := open(KindDuplicateIP, "ip:10.0.0.9")
	repo.SetCreatedAt(old.ID, time.Now().Add(-EscalationAge-time.Hour))
	young, _ := open(KindDuplicateIP, "ip:10.0.0.10")
	escalated, total, err := svc.List(ctx, "org", Filter{Escalated: true}, api.PaginationParams{Limit: 50})
	if err != nil || total != 1 || escalated[0].ID != old.ID || !escalated[0].Escalated {
		t.Errorf("escalated: %+v, %d, %v", escalated, total, err)
	}
	if item, _ := svc.Get(ctx, "org", young.ID); item.Escalated {
		t.Error("a young item is escalated")
	}

	// REST: review-items and the deprecated discovery path.
	mux := chi.NewRouter()
	NewHandler(svc).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org", UserID: "u2"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	w := call(http.MethodGet, "/api/v1/review-items?escalated=true", "")
	var list api.ListResponse[Item]
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Total != 1 {
		t.Errorf("GET review-items?escalated=true: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodGet, "/api/v1/review-items/"+young.ID, ""); w.Code != http.StatusOK {
		t.Errorf("GET review item: %d", w.Code)
	}
	if w = call(http.MethodGet, "/api/v1/review-items/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET unknown review item: %d", w.Code)
	}
	if w = call(http.MethodPost, "/api/v1/review-items/"+young.ID+"/resolve", `{"action":"merge"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("merge on duplicate_ip: %d", w.Code)
	}
	// The resolver is the principal: a body naming one is refused on both
	// paths and resolves nothing (F-091).
	for _, path := range []string{"/api/v1/review-items/", "/api/v1/discovery/review-items/"} {
		if w = call(http.MethodPost, path+young.ID+"/resolve", `{"action":"dismiss","resolved_by":"u9"}`); w.Code != http.StatusBadRequest {
			t.Errorf("POST %s with resolved_by: %d %s, want 400", path, w.Code, w.Body.String())
		}
	}
	w = call(http.MethodPost, "/api/v1/discovery/review-items/"+young.ID+"/resolve", `{"action":"dismiss"}`)
	var item Item
	if w.Code != http.StatusOK || w.Header().Get("Deprecation") == "" || json.Unmarshal(w.Body.Bytes(), &item) != nil ||
		item.ResolvedBy != "u2" || item.Status != StatusDismissed {
		t.Errorf("deprecated resolve: %d %s (Deprecation %q)", w.Code, w.Body.String(), w.Header().Get("Deprecation"))
	}
	// The successor link names the item, not the template (F-219).
	if got, want := w.Header().Get("Link"), "</api/v1/review-items/"+young.ID+`/resolve>; rel="successor-version"`; got != want {
		t.Errorf("deprecated resolve: Link %q, want %q", got, want)
	}
	if w = call(http.MethodPost, "/api/v1/review-items/"+young.ID+"/resolve", `{"action":"dismiss"}`); w.Code != http.StatusConflict {
		t.Errorf("resolve twice: %d", w.Code)
	}
}

func isStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// TestOpenUnlessSettled covers WP-652 (REC-08, TOP-01; F-382): an accepted
// item of an object suppresses a new item of the same object and type while
// its outcome holds (holds is asked with the accepted item); once it no
// longer holds, a new item opens, and a dismissal still suppresses its
// value (OpenUnlessDismissed). An error of holds opens nothing.
func TestOpenUnlessSettled(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo).WithEffect(KindUnknownRelationship, func(context.Context, *Item, *Resolution) (bool, error) {
		return false, nil
	})
	ctx := context.Background()
	item := func() *Item {
		return &Item{OrganizationID: "org", Kind: KindUnknownRelationship, ObjectKey: "rel:a:b",
			Payload: map[string]any{"source_ci_id": "a", "target_ci_id": "b", "type_key": "vxlan_peer"}}
	}
	held, asked := true, []string{}
	holds := func(_ context.Context, accepted *Item) (bool, error) {
		asked = append(asked, accepted.ID)
		return held, nil
	}
	open := func(stage string) (string, bool) {
		t.Helper()
		it := item()
		opened, err := svc.OpenUnlessSettled(ctx, it, "type_key", holds)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		return it.ID, opened
	}

	first, opened := open("first report")
	if !opened || len(asked) != 0 {
		t.Fatalf("first report: opened %v, holds asked %v; want a new item without asking", opened, asked)
	}
	if _, err := svc.Resolve(ctx, "org", first, &Resolution{Action: ActionAccept}); err != nil {
		t.Fatal(err)
	}
	if _, reopened := open("accepted, holds"); reopened || len(asked) != 1 || asked[0] != first {
		t.Errorf("accepted item holds: opened %v, holds asked %v; want no item, asked with %s", reopened, asked, first)
	}
	if _, err := svc.OpenUnlessSettled(ctx, item(), "type_key", func(context.Context, *Item) (bool, error) {
		return false, errors.New("lookup failed")
	}); err == nil {
		t.Error("an error of holds: want the error")
	}
	if items, _, _ := repo.ListReviewItems(ctx, "org", Filter{Status: StatusOpen}, api.PaginationParams{Limit: 10}); len(items) != 0 {
		t.Errorf("open items after a held acceptance and a failed holds = %d, want 0", len(items))
	}

	held = false
	second, opened := open("accepted, no longer holds")
	if !opened {
		t.Fatal("accepted item no longer holds: want a new item")
	}
	if _, err := svc.Resolve(ctx, "org", second, &Resolution{Action: ActionDismiss}); err != nil {
		t.Fatal(err)
	}
	if _, reopened := open("dismissed with the same value"); reopened {
		t.Error("dismissed with the same value: want no item (dismissal rule)")
	}
}
