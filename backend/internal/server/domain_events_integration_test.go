// Domain events of the relationship, collector and review mutations
// (WP-400; F-043).
//
//   - TestAuditDefectDomainEventsEmitted – WP-136 (EVT-01, EVT-02; moved
//     from audit_t6_router_auditdefect_test.go): POST /relationships, DELETE
//     /relationships/{id}, POST /collectors/enroll and POST
//     /review-items/{id}/resolve write relationship.created,
//     relationship.removed, collector.enrolled and review.resolved in the
//     transaction of the mutation, with the entity as entity_id and its
//     entity_seq. A refused mutation (409, 422, 401, 404) and a rolled back
//     one write nothing. The publisher hands the envelopes on (subject
//     events.<org_id>.<type>); a webhook subscription of organization A on
//     relationship.created gets exactly one delivery, organization B's
//     subscription none, and organization B sees none of A's events.
//
// The test runs the complete router of audit_t6_harness_test.go on
// PostgreSQL.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditDefectDomainEvents' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// domainEventRecorder is a publisher consumer that keeps the envelopes.
type domainEventRecorder struct {
	mu  sync.Mutex
	got []events.Envelope
}

func (c *domainEventRecorder) Consume(_ context.Context, env *events.Envelope) error {
	c.mu.Lock()
	c.got = append(c.got, *env)
	c.mu.Unlock()
	return nil
}

// of returns the envelopes of organization org.
func (c *domainEventRecorder) of(org string) []events.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []events.Envelope
	for _, env := range c.got {
		if env.OrgID == org {
			out = append(out, env)
		}
	}
	return out
}

func TestAuditDefectDomainEventsEmitted(t *testing.T) {
	e := t6NewEnv(t, "d5", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d5-admin"), identity.AllPermissions()}
	otherOrg := t6As{f.OrgB, f.AppUser(t, f.OrgB, "audit-d5-b"), identity.AllPermissions()}
	outbox := func(typ, entity string) int {
		return e.Count(t, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = $2 AND ($3 = '' OR entity_id = $3)`, f.OrgA, typ, entity)
	}
	allEvents := func(org string) int {
		return e.Count(t, `SELECT count(*) FROM event_outbox WHERE organization_id = $1`, org)
	}
	a, b := f.CI(t, f.OrgA, "", "audit-d5-a"), f.CI(t, f.OrgA, "", "audit-d5-b")
	foreign := f.CI(t, f.OrgB, "", "audit-d5-foreign")

	// Webhook subscribers of both organizations on relationship.created; the
	// path names the organization.
	var mu sync.Mutex
	deliveries := map[string][]string{}
	subscriber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		deliveries[r.URL.Path] = append(deliveries[r.URL.Path], r.Header.Get("X-Reticora-Event")+" "+payload.ID)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer subscriber.Close()
	subscriptions := map[string]string{}
	for path, as := range map[string]t6As{"/org-a": admin, "/org-b": otherOrg} {
		r := e.Call(t, as, http.MethodPost, "/api/v1/webhooks", `{"name":"audit-d5`+path[1:]+`","url":"`+subscriber.URL+path+
			`","secret":"audit-d5-secret-0123456789","events":["relationship.created"]}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /webhooks %s = %d %s", path, r.Code, t6Short(r.Body))
		}
		subscriptions[path] = t6ID(r.Body)
	}

	var edge string
	t.Run("relationship.created on POST /relationships", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodPost, "/api/v1/relationships", `{"source_ci_id":"`+a+`","target_ci_id":"`+b+`","rel_type":"depends_on"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /relationships = %d %s", r.Code, t6Short(r.Body))
		}
		edge = t6ID(r.Body)
		if n := outbox("relationship.created", edge); n != 1 {
			t.Errorf("relationship.created events for edge %s = %d, want 1 (EVT-02)", edge, n)
		}
	})
	t.Run("duplicate relationship (409) emits nothing more", func(t *testing.T) {
		before := outbox("relationship.created", "")
		r := e.Call(t, admin, http.MethodPost, "/api/v1/relationships", `{"source_ci_id":"`+a+`","target_ci_id":"`+b+`","rel_type":"depends_on"}`)
		if r.Code != http.StatusConflict {
			t.Fatalf("duplicate POST /relationships = %d, want 409", r.Code)
		}
		if after := outbox("relationship.created", ""); after != before {
			t.Errorf("relationship.created after refused duplicate = %d, want %d", after, before)
		}
	})
	t.Run("refused relationship writes emit nothing", func(t *testing.T) {
		var discoveryEdge string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
			VALUES ($1, $2, $3, 'connected_to', 'discovery') RETURNING id::text`, f.OrgA, b, a).Scan(&discoveryEdge); err != nil {
			t.Fatal(err)
		}
		before, beforeB := allEvents(f.OrgA), allEvents(f.OrgB)
		for _, tc := range []struct {
			name, method, path, body string
			as                       t6As
			status                   int
		}{
			// Projected types are refused before the repository (422).
			{"projected rel_type", http.MethodPost, "/api/v1/relationships",
				`{"source_ci_id":"` + a + `","target_ci_id":"` + b + `","rel_type":"contains"}`, admin, http.StatusUnprocessableEntity},
			// No row: the target is invisible or unknown (422 on
			// target_ci_id, WP-401).
			{"CI of organization B as target", http.MethodPost, "/api/v1/relationships",
				`{"source_ci_id":"` + a + `","target_ci_id":"` + foreign + `","rel_type":"depends_on"}`, admin, http.StatusUnprocessableEntity},
			{"unknown CI as target", http.MethodPost, "/api/v1/relationships",
				`{"source_ci_id":"` + a + `","target_ci_id":"` + f.ID() + `","rel_type":"depends_on"}`, admin, http.StatusUnprocessableEntity},
			{"discovery edge without force", http.MethodDelete, "/api/v1/relationships/" + discoveryEdge, ``, admin, http.StatusConflict},
			{"organization B deletes A's edge", http.MethodDelete, "/api/v1/relationships/" + edge, ``, otherOrg, http.StatusNotFound},
		} {
			r := e.Call(t, tc.as, tc.method, tc.path, tc.body)
			if r.Code != tc.status {
				t.Errorf("%s: %s %s = %d %s, want %d", tc.name, tc.method, tc.path, r.Code, t6Short(r.Body), tc.status)
			}
		}
		if after, afterB := allEvents(f.OrgA), allEvents(f.OrgB); after != before || afterB != beforeB {
			t.Errorf("events after refused relationship writes: organization A %d -> %d, B %d -> %d", before, after, beforeB, afterB)
		}
		if n := e.Count(t, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND target_ci_id = $2`, f.OrgA, foreign); n != 0 {
			t.Errorf("%d edges of organization A to organization B's CI", n)
		}
		if n := e.Count(t, `SELECT count(*) FROM ci_relationship WHERE id = $1`, discoveryEdge); n != 1 {
			t.Errorf("discovery edge deleted without force (%d rows)", n)
		}
	})
	t.Run("relationship.removed on DELETE /relationships/{id}", func(t *testing.T) {
		if edge == "" {
			t.Fatal("no edge created")
		}
		r := e.Call(t, admin, http.MethodDelete, "/api/v1/relationships/"+edge, ``)
		if r.Code != http.StatusNoContent {
			t.Fatalf("DELETE /relationships = %d %s", r.Code, t6Short(r.Body))
		}
		if n := outbox("relationship.removed", edge); n != 1 {
			t.Errorf("relationship.removed events for edge %s = %d, want 1 (EVT-02)", edge, n)
		}
		// The second delete finds nothing and writes nothing.
		if r = e.Call(t, admin, http.MethodDelete, "/api/v1/relationships/"+edge, ``); r.Code != http.StatusNotFound {
			t.Errorf("second DELETE = %d, want 404", r.Code)
		}
		if n := outbox("relationship.removed", edge); n != 1 {
			t.Errorf("relationship.removed events after the second delete = %d, want 1", n)
		}
	})
	t.Run("collector.enrolled on POST /collectors/enroll", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodPost, "/api/v1/collectors/enrollment-codes", `{"label":"audit-d5"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /collectors/enrollment-codes = %d %s", r.Code, t6Short(r.Body))
		}
		var code struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(r.Body, &code); err != nil {
			t.Fatal(err)
		}
		// A registration whose certificate cannot be issued rolls back:
		// no collector, no event, the code stays unused.
		_, err := discovery.NewPGRepository(f.App).EnrollCollector(bg, code.Code, &discovery.Collector{Name: "audit-d5-rolled-back", Config: map[string]any{}},
			func(context.Context, string, string) (*discovery.IssuedCert, error) {
				return nil, errors.New("audit-d5: CA unavailable")
			})
		if err == nil {
			t.Fatal("enrollment with a failing certificate issuer succeeded")
		}
		if n := outbox("collector.enrolled", ""); n != 0 {
			t.Errorf("collector.enrolled events after a rolled back enrollment = %d, want 0", n)
		}
		if n := e.Count(t, `SELECT count(*) FROM collector WHERE organization_id = $1 AND name = 'audit-d5-rolled-back'`, f.OrgA); n != 0 {
			t.Errorf("rolled back enrollment left %d collectors", n)
		}
		r = e.Call(t, t6As{}, http.MethodPost, "/api/v1/collectors/enroll", `{"code":"`+code.Code+`","name":"audit-d5-collector"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("POST /collectors/enroll = %d %s", r.Code, t6Short(r.Body))
		}
		collector := t6ID(r.Body)
		if n := outbox("collector.enrolled", collector); n != 1 {
			t.Errorf("collector.enrolled events for collector %s = %d, want 1 (EVT-02)", collector, n)
		}
		// A second use of the code is refused and emits nothing.
		before := outbox("collector.enrolled", "")
		if r = e.Call(t, t6As{}, http.MethodPost, "/api/v1/collectors/enroll", `{"code":"`+code.Code+`","name":"audit-d5-collector-2"}`); r.Code == http.StatusCreated {
			t.Fatalf("second enrollment with one code = 201")
		}
		if after := outbox("collector.enrolled", ""); after != before {
			t.Errorf("collector.enrolled after refused enrollment = %d, want %d", after, before)
		}
	})
	t.Run("review.resolved on POST /review-items/{id}/resolve", func(t *testing.T) {
		var item string
		if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, payload) VALUES ($1, 'unclassified_device', '{"ip":"192.0.2.55"}') RETURNING id::text`,
			f.OrgA).Scan(&item); err != nil {
			t.Fatal(err)
		}
		// A resolution whose transaction rolls back writes nothing.
		errRollback := errors.New("audit-d5: roll back")
		err := database.WithRequestTenantTx(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context) error {
			if _, rerr := reviews.NewPGRepository(f.App).ResolveReviewItem(ctx, f.OrgA, item,
				&reviews.Resolution{Status: reviews.StatusDismissed, Action: reviews.ActionDismiss, Note: "rolled back"}); rerr != nil {
				return rerr
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("resolution in a rolled back transaction: %v", err)
		}
		// A refused action (422) writes nothing either.
		if r := e.Call(t, admin, http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"merge"}`); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("resolve with a refused action = %d %s, want 422", r.Code, t6Short(r.Body))
		}
		if n := outbox("review.resolved", item); n != 0 {
			t.Fatalf("review.resolved events before the resolution = %d, want 0", n)
		}
		r := e.Call(t, admin, http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"dismiss"}`)
		if r.Code != http.StatusOK {
			t.Fatalf("resolve = %d %s", r.Code, t6Short(r.Body))
		}
		if n := outbox("review.resolved", item); n != 1 {
			t.Errorf("review.resolved events for item %s = %d, want 1 (EVT-02)", item, n)
		}
		// The resolved item is refused (409) and writes no second event.
		if r = e.Call(t, admin, http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"dismiss"}`); r.Code != http.StatusConflict {
			t.Errorf("second resolve = %d, want 409", r.Code)
		}
		if n := outbox("review.resolved", item); n != 1 {
			t.Errorf("review.resolved events after the refused second resolve = %d, want 1", n)
		}
	})
	t.Run("envelopes, webhook delivery and organization isolation", func(t *testing.T) {
		if edge == "" {
			t.Fatal("no edge created")
		}
		// Organization B sees none of A's events.
		scopeB := database.OrgWideScope(f.OrgB, "")
		var visible int
		if err := database.WithTenant(bg, f.App, &scopeB, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE organization_id = $1`, f.OrgA).Scan(&visible)
		}); err != nil {
			t.Fatal(err)
		}
		if total := allEvents(f.OrgA); visible != 0 || total < 4 {
			t.Errorf("organization B sees %d of A's %d events, want 0 of at least 4", visible, total)
		}
		if n := allEvents(f.OrgB); n != 0 {
			t.Errorf("organization B has %d events, want 0", n)
		}

		recorder := &domainEventRecorder{}
		dispatcher := webhook.NewDispatcher(webhook.NewPGRepository(f.App), egress.NewClient(egress.Options{AllowPrivate: true}),
			webhook.DispatcherOptions{Deliveries: webhook.NewPGDeliveryStore(f.App), PollInterval: -1})
		publisher := events.NewPublisher(f.Admin, nil, recorder, dispatcher)
		delivered := func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), deliveries["/org-a"]...)
		}
		deadline := time.Now().Add(15 * time.Second)
		for len(delivered()) == 0 && time.Now().Before(deadline) {
			if _, err := publisher.PublishPending(bg); err != nil {
				t.Fatalf("publish: %v", err)
			}
			dispatcher.ProcessDue(bg)
			time.Sleep(100 * time.Millisecond)
		}
		// Another cycle must not deliver the event again.
		if _, err := publisher.PublishPending(bg); err != nil {
			t.Fatalf("publish: %v", err)
		}
		dispatcher.ProcessDue(bg)
		ctx, cancel := context.WithTimeout(bg, 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}

		if got := delivered(); len(got) != 1 || got[0] != "relationship.created "+edge {
			t.Errorf("deliveries to organization A's relationship.created subscription = %v, want exactly [relationship.created %s]", got, edge)
		}
		mu.Lock()
		toB := deliveries["/org-b"]
		mu.Unlock()
		if len(toB) != 0 {
			t.Errorf("organization B's subscription got %v, want nothing", toB)
		}
		if n := e.Count(t, `SELECT count(*) FROM webhook_delivery WHERE subscription_id = $1`, subscriptions["/org-a"]); n != 1 {
			t.Errorf("webhook_delivery rows of organization A's subscription = %d, want 1", n)
		}
		if n := e.Count(t, `SELECT count(*) FROM webhook_delivery WHERE subscription_id = $1`, subscriptions["/org-b"]); n != 0 {
			t.Errorf("webhook_delivery rows of organization B's subscription = %d, want 0", n)
		}

		// The envelopes follow EVT-01: subject events.<org_id>.<type>,
		// sequence per organization, entity_id and entity_seq per entity.
		if n := len(recorder.of(f.OrgB)); n != 0 {
			t.Errorf("organization B published %d events, want 0", n)
		}
		var lastSeq int64
		seen := map[string]events.Envelope{}
		for _, env := range recorder.of(f.OrgA) {
			if env.Sequence <= lastSeq || env.ID == "" || env.OccurredAt.IsZero() {
				t.Errorf("envelope %s %s: sequence %d after %d, occurred_at %v", env.Type, env.ID, env.Sequence, lastSeq, env.OccurredAt)
			}
			lastSeq = env.Sequence
			if want := events.SubjectPrefix + f.OrgA + "." + env.Type; env.Subject() != want {
				t.Errorf("subject %s, want %s", env.Subject(), want)
			}
			if _, dup := seen[env.Type]; dup {
				t.Errorf("%s published twice", env.Type)
			}
			seen[env.Type] = env
		}
		for typ, entitySeq := range map[string]int64{events.RelationshipCreated: 1, events.RelationshipRemoved: 2,
			events.CollectorEnrolled: 1, events.ReviewResolved: 1} {
			env, ok := seen[typ]
			if !ok {
				t.Errorf("no %s envelope published", typ)
				continue
			}
			if env.EntityID == "" || env.EntitySeq != entitySeq {
				t.Errorf("%s: entity_id %q entity_seq %d, want entity_seq %d", typ, env.EntityID, env.EntitySeq, entitySeq)
			}
		}
		for _, typ := range []string{events.RelationshipCreated, events.RelationshipRemoved} {
			var data struct {
				ID         string `json:"id"`
				SourceCIID string `json:"source_ci_id"`
				TargetCIID string `json:"target_ci_id"`
				RelType    string `json:"rel_type"`
			}
			if err := json.Unmarshal(seen[typ].Data, &data); err != nil {
				t.Errorf("%s data %s: %v", typ, seen[typ].Data, err)
			}
			if seen[typ].EntityID != edge || data.ID != edge || data.SourceCIID != a || data.TargetCIID != b || data.RelType != "depends_on" {
				t.Errorf("%s: entity %s data %+v, want edge %s %s -> %s depends_on", typ, seen[typ].EntityID, data, edge, a, b)
			}
		}
	})
}
