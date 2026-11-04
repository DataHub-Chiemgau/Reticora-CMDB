package metamodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
)

// spyDispatcher records the webhook events a handler dispatches.
type spyDispatcher struct {
	mu     sync.Mutex
	events []string
}

func (s *spyDispatcher) Dispatch(_ context.Context, _, event string, _ any) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

// take returns the recorded events, sorted, and forgets them.
func (s *spyDispatcher) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.events
	s.events = nil
	sort.Strings(out)
	return out
}

// publishAPI is draftAPI with the publication (WP-261): the resource
// routes and the publication share the repository with the index
// scheduler; direct records the webhooks of the direct path, published
// those of the publication.
type publishAPI struct {
	*draftAPI
	direct, published *spyDispatcher
}

func newPublishAPI(t *testing.T, f *scopetest.Fixture) *publishAPI {
	catalog := citype.NewPGRepository(f.App)
	catalog.WithIndexScheduler(citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App))))
	direct, published := &spyDispatcher{}, &spyDispatcher{}
	mux := chi.NewRouter()
	citype.NewHandler(catalog, direct).RegisterRoutes(mux)
	metamodel.NewHandler(metamodel.NewPGRepository(f.App)).WithPublish(catalog, published).RegisterRoutes(mux)
	return &publishAPI{draftAPI: &draftAPI{t: t, f: f, mux: mux, user: f.AppUser(t, f.OrgA, "wp261-admin")},
		direct: direct, published: published}
}

// tested adds the items to a new draft and marks it tested, as the test
// run (WP-721) does without violations that block.
func (a *publishAPI) tested(name string, items ...string) (draft string, itemIDs []string) {
	a.t.Helper()
	d := a.newDraft(a.f.OrgA, name)
	for _, body := range items {
		w := a.addItem(a.f.OrgA, d.ID, body)
		if w.Code != http.StatusCreated {
			a.t.Fatalf("add item %s: %d %s", body, w.Code, w.Body.String())
		}
		var it metamodel.Item
		if err := json.Unmarshal(w.Body.Bytes(), &it); err != nil {
			a.t.Fatal(err)
		}
		itemIDs = append(itemIDs, it.ID)
	}
	a.markTested(d.ID)
	return d.ID, itemIDs
}

func (a *publishAPI) markTested(draftID string) {
	a.t.Helper()
	if _, err := a.f.Admin.Exec(context.Background(), `UPDATE metamodel_draft SET status = 'tested' WHERE id = $1`, draftID); err != nil {
		a.t.Fatal(err)
	}
}

func (a *publishAPI) publish(draftID string) response {
	a.t.Helper()
	return a.call(a.f.OrgA, http.MethodPost, "/api/v1/metamodel/drafts/"+draftID+"/publish", "")
}

func (a *publishAPI) count(q string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.f.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		a.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// publishedEvents counts metamodel.published of the organization.
func (a *publishAPI) publishedEvents() int {
	return a.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = $2`, a.f.OrgA, events.MetamodelPublished)
}

// lastEvent reads the newest metamodel.published of entity.
func (a *publishAPI) lastEvent(entity string) events.MetamodelPublishedData {
	a.t.Helper()
	var raw []byte
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = $2 AND entity_id = $3 ORDER BY sequence DESC LIMIT 1`,
		a.f.OrgA, events.MetamodelPublished, entity).Scan(&raw); err != nil {
		a.t.Fatalf("event of %s: %v", entity, err)
	}
	var data events.MetamodelPublishedData
	if err := json.Unmarshal(raw, &data); err != nil {
		a.t.Fatal(err)
	}
	return data
}

func (a *publishAPI) schemaVersion(typeID string) int64 {
	a.t.Helper()
	var v int64
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT schema_version FROM ci_type WHERE id = $1`, typeID).Scan(&v); err != nil {
		a.t.Fatal(err)
	}
	return v
}

func (a *publishAPI) status(draftID string) string {
	a.t.Helper()
	var s string
	if err := a.f.Admin.QueryRow(context.Background(), `SELECT status FROM metamodel_draft WHERE id = $1`, draftID).Scan(&s); err != nil {
		a.t.Fatal(err)
	}
	return s
}

// unchanged checks that a refused publication changed nothing: no audit
// entry of its request, no event, no index job, no webhook, the draft
// still tested.
func (a *publishAPI) unchanged(w response, draftID string, eventsBefore, jobsBefore int) {
	a.t.Helper()
	a.noAudit(w)
	if n := a.publishedEvents(); n != eventsBefore {
		a.t.Errorf("a refused publication wrote %d events", n-eventsBefore)
	}
	if n := a.count(`SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, a.f.OrgA, citype.IndexJobKind); n != jobsBefore {
		a.t.Errorf("a refused publication left %d index jobs", n-jobsBefore)
	}
	if got := a.published.take(); len(got) != 0 {
		a.t.Errorf("a refused publication dispatched %v", got)
	}
	if s := a.status(draftID); s != metamodel.StatusTested {
		a.t.Errorf("status after a refused publication = %s, want tested", s)
	}
}

func problemItem(t *testing.T, w response) string {
	t.Helper()
	var p struct {
		ItemID string `json:"item_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p.ItemID
}

// TestMetamodelDraftPublicationAgainstPostgres covers MET-53, AUD-07 and
// EVT-03 (WP-261): the atomic publication of a tested draft, the check of
// the base, one schema_version per type, the event metamodel.published of
// the draft and of direct changes, the audit of the publication as a unit
// and the webhooks after the commit.
func TestMetamodelDraftPublicationAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e2")
	a := newPublishAPI(t, f)
	bg := context.Background()

	// Direct changes (MET-04): each writes one metamodel.published with
	// direct true; a refused one writes none.
	eventsBefore := a.publishedEvents()
	typeID := a.id(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp261_type","name":"wp261 type"}`)
	if got := a.lastEvent(typeID); !got.Direct || got.ID != typeID || got.DraftID != "" || got.Counts["ci_type"] != 1 {
		t.Errorf("event of POST /ci-types = %+v", got)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+typeID, `{"description":"wp261 direct"}`, nil)
	directDef := a.id(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typeID+`","key":"wp261_direct","data_type":"string"}`)
	got := a.lastEvent(directDef)
	if !got.Direct || got.Counts["attribute_definition"] != 1 || len(got.Types) != 1 || got.Types[0].ID != typeID ||
		got.Types[0].SchemaVersion != a.schemaVersion(typeID) {
		t.Errorf("event of POST /attribute-definitions = %+v (schema_version %d)", got, a.schemaVersion(typeID))
	}
	if n := a.publishedEvents() - eventsBefore; n != 3 {
		t.Errorf("three direct changes wrote %d metamodel.published, want 3", n)
	}
	if w := a.call(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typeID+`","key":"wp261_direct","data_type":"string"}`); w.Code < 400 {
		t.Fatalf("duplicate definition: %d", w.Code)
	}
	if n := a.publishedEvents() - eventsBefore; n != 3 {
		t.Errorf("a refused direct change wrote an event (%d)", n)
	}
	a.direct.take()

	t.Run("three items as one publication", func(t *testing.T) {
		version := a.schemaVersion(typeID)
		eventsBefore := a.publishedEvents()
		draftID, _ := a.tested("wp261 three",
			`{"kind":"attribute_definition","op":"create","payload":{"ci_type_id":"`+typeID+`","key":"wp261_a","data_type":"string","searchable":true}}`,
			`{"kind":"attribute_definition","op":"create","payload":{"ci_type_id":"`+typeID+`","key":"wp261_b","data_type":"integer"}}`,
			`{"kind":"attribute_definition","op":"update","target_id":"`+directDef+`","payload":{"required":true,"default":"x"}}`)
		w := a.publish(draftID)
		if w.Code != http.StatusOK {
			t.Fatalf("publish: %d %s", w.Code, w.Body.String())
		}
		var res metamodel.PublishResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		// Three definition changes of one type: schema_version + 1.
		if after := a.schemaVersion(typeID); after != version+1 {
			t.Errorf("schema_version %d → %d, want + 1", version, after)
		}
		wantTypes := []events.MetamodelPublishedType{{ID: typeID, SchemaVersion: version + 1}}
		if !slices.Equal(res.Types, wantTypes) || res.Counts["attribute_definition"] != 3 || len(res.Counts) != 1 ||
			res.Draft.Status != metamodel.StatusPublished || res.Draft.PublishedAt == nil {
			t.Errorf("answer: types %v counts %v status %s", res.Types, res.Counts, res.Draft.Status)
		}
		if n := a.publishedEvents() - eventsBefore; n != 1 {
			t.Errorf("the publication wrote %d metamodel.published, want exactly 1", n)
		}
		ev := a.lastEvent(draftID)
		if ev.Direct || ev.ID != draftID || ev.DraftID != draftID || ev.Counts["attribute_definition"] != 3 || !slices.Equal(ev.Types, wantTypes) {
			t.Errorf("event data %+v", ev)
		}
		// The audit: the publication as a unit and the three elements with
		// their values.
		if n := a.count(`SELECT count(*) FROM audit_log WHERE request_id = $1 AND action = 'metamodel.published'
				AND resource_type = 'metamodel_draft' AND resource_id = $2 AND changes ? 'before' AND changes ? 'after'`,
			w.requestID, draftID); n != 1 {
			t.Errorf("%d audit entries metamodel.published, want 1", n)
		}
		if n := a.count(`SELECT count(*) FROM audit_log WHERE request_id = $1 AND resource_type = 'attribute_definition'
				AND changes ? 'after'`, w.requestID); n != 3 {
			t.Errorf("%d audit entries of the elements, want 3", n)
		}
		// The searchable definition got its index job in the publication.
		if n := a.count(`SELECT count(*) FROM job j JOIN attribute_definition d ON d.id::text = j.params->>'definition_id'
				WHERE j.organization_id = $1 AND j.kind = $2 AND d.key = 'wp261_a'`, f.OrgA, citype.IndexJobKind); n != 1 {
			t.Errorf("%d attribute.index jobs of the searchable definition, want 1", n)
		}
		// The webhooks of the elements after the commit.
		if hooks := a.published.take(); !slices.Equal(hooks, []string{"ci_attribute.changed", "ci_attribute.changed", "ci_attribute.changed"}) {
			t.Errorf("webhooks %v", hooks)
		}
		if hooks := a.direct.take(); len(hooks) != 0 {
			t.Errorf("the handler of the direct path dispatched %v", hooks)
		}
		// Published once: a second publication is 409.
		if w2 := a.publish(draftID); w2.Code != http.StatusConflict {
			t.Errorf("second publication: %d", w2.Code)
		}
	})

	t.Run("a type item fires ci_type.updated once and item references resolve", func(t *testing.T) {
		draftID, items := a.tested("wp261 type",
			`{"kind":"ci_type","op":"update","target_id":"`+typeID+`","payload":{"name":"wp261 type renamed"}}`,
			`{"kind":"ci_type","op":"create","payload":{"key":"wp261_new","name":"wp261 new"}}`)
		if _, err := a.f.Admin.Exec(bg, `UPDATE metamodel_draft SET status = 'draft' WHERE id = $1`, draftID); err != nil {
			t.Fatal(err)
		}
		w := a.addItem(f.OrgA, draftID, `{"kind":"attribute_definition","op":"create","payload":{"ci_type_id":{"$item":"`+items[1]+`"},"key":"wp261_ref","data_type":"string"}}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("item with reference: %d %s", w.Code, w.Body.String())
		}
		a.markTested(draftID)
		pw := a.publish(draftID)
		if pw.Code != http.StatusOK {
			t.Fatalf("publish: %d %s", pw.Code, pw.Body.String())
		}
		var res metamodel.PublishResult
		if err := json.Unmarshal(pw.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		var newType string
		if err := f.Admin.QueryRow(bg, `SELECT t.id::text FROM ci_type t JOIN attribute_definition d ON d.ci_type_id = t.id
			WHERE t.organization_id = $1 AND t.key = 'wp261_new' AND d.key = 'wp261_ref'`, f.OrgA).Scan(&newType); err != nil {
			t.Fatalf("the referenced type carries the definition: %v", err)
		}
		if res.Counts["ci_type"] != 2 || res.Counts["attribute_definition"] != 1 {
			t.Errorf("counts %v", res.Counts)
		}
		found := false
		for _, typ := range res.Types {
			found = found || (typ.ID == newType && typ.SchemaVersion == a.schemaVersion(newType))
		}
		if !found {
			t.Errorf("types %v lack the new type %s", res.Types, newType)
		}
		hooks := a.published.take()
		updated := 0
		for _, h := range hooks {
			if h == "ci_type.updated" {
				updated++
			}
		}
		if updated != 1 || !slices.Contains(hooks, "ci_type.created") || !slices.Contains(hooks, "ci_attribute.changed") {
			t.Errorf("webhooks %v, want ci_type.updated exactly once", hooks)
		}
	})

	t.Run("a failing element rolls back every element", func(t *testing.T) {
		eventsBefore := a.publishedEvents()
		jobsBefore := a.count(`SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, f.OrgA, citype.IndexJobKind)
		draftID, items := a.tested("wp261 rollback",
			`{"kind":"attribute_definition","op":"create","payload":{"ci_type_id":"`+typeID+`","key":"wp261_rolled_back","data_type":"string","searchable":true}}`,
			`{"kind":"ci_type","op":"create","payload":{"key":"wp261_type","name":"wp261 duplicate"}}`)
		w := a.publish(draftID)
		if w.Code != http.StatusConflict || problemItem(t, w) != items[1] {
			t.Fatalf("publish with a duplicate type: %d %s, want 409 naming item %s", w.Code, w.Body.String(), items[1])
		}
		a.unchanged(w, draftID, eventsBefore, jobsBefore)
		if n := a.count(`SELECT count(*) FROM attribute_definition WHERE key = 'wp261_rolled_back'`); n != 0 {
			t.Errorf("the first element stayed (%d)", n)
		}
	})

	t.Run("a composition conflict of the generator is 422", func(t *testing.T) {
		parent := a.id(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp261_parent","name":"wp261 parent"}`)
		child := a.id(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp261_child","name":"wp261 child","parent_type_id":"`+parent+`"}`)
		def := a.id(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+child+`","key":"wp261_x","data_type":"string"}`)
		a.id(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions",
			`{"ci_type_id":"`+parent+`","key":"wp261_k","data_type":"string","inheritable":true}`)
		// A conflicting definition of another key on the child, written
		// without the generator: only the regeneration finds it.
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err = tx.Exec(bg, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(bg, `INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, display_name)
			VALUES ($1, $2, 'type', 'wp261_k', 'integer', '{"de":"k","en":"k"}')`, f.OrgA, child); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(bg); err != nil {
			t.Fatal(err)
		}
		version := a.schemaVersion(child)
		eventsBefore := a.publishedEvents()
		jobsBefore := a.count(`SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, f.OrgA, citype.IndexJobKind)
		draftID, _ := a.tested("wp261 composition",
			`{"kind":"attribute_definition","op":"update","target_id":"`+def+`","payload":{"description":"wp261 changed"}}`)
		w := a.publish(draftID)
		// The item itself passes its route; the generator refuses at SET
		// CONSTRAINTS … IMMEDIATE, so the problem names no item.
		if w.Code != http.StatusUnprocessableEntity || problemItem(t, w) != "" {
			t.Fatalf("publish with a conflict of the generator: %d %s, want 422 without item", w.Code, w.Body.String())
		}
		a.unchanged(w, draftID, eventsBefore, jobsBefore)
		if a.schemaVersion(child) != version {
			t.Error("the refused publication changed schema_version")
		}
		if n := a.count(`SELECT count(*) FROM attribute_definition WHERE id = $1 AND description = 'wp261 changed'`, def); n != 0 {
			t.Error("the refused publication changed the definition")
		}
	})

	t.Run("preconditions", func(t *testing.T) {
		// Without a test: 409.
		d := a.newDraft(f.OrgA, "wp261 untested")
		if w := a.addItem(f.OrgA, d.ID, `{"kind":"ci_type","op":"create","payload":{"key":"wp261_untested","name":"x"}}`); w.Code != http.StatusCreated {
			t.Fatalf("add item: %d", w.Code)
		}
		if w := a.publish(d.ID); w.Code != http.StatusConflict {
			t.Errorf("publish without test: %d %s", w.Code, w.Body.String())
		}
		// Concurrent publications: exactly one succeeds.
		draftID, _ := a.tested("wp261 parallel", `{"kind":"ci_type","op":"create","payload":{"key":"wp261_parallel","name":"x"}}`)
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = a.publish(draftID).Code
			}()
		}
		wg.Wait()
		sort.Ints(codes)
		if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
			t.Errorf("parallel publications: %v, want one 200 and one 409", codes)
		}
		if n := a.count(`SELECT count(*) FROM ci_type WHERE organization_id = $1 AND key = 'wp261_parallel'`, f.OrgA); n != 1 {
			t.Errorf("%d types of the parallel publication", n)
		}
		a.published.take()
	})

	t.Run("a stale base is 409 with the items", func(t *testing.T) {
		def := a.id(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+typeID+`","key":"wp261_stale","data_type":"string"}`)
		rule := a.id(f.OrgA, http.MethodPost, "/api/v1/rules",
			`{"key":"wp261_rule","ci_type_id":"`+typeID+`","attribute_key":"wp261_stale","kind":"required"}`)
		draftID, items := a.tested("wp261 stale",
			`{"kind":"ci_type","op":"update","target_id":"`+typeID+`","payload":{"description":"wp261 draft"}}`,
			`{"kind":"attribute_definition","op":"update","target_id":"`+def+`","payload":{"description":"wp261 draft"}}`,
			`{"kind":"rule","op":"update","target_id":"`+rule+`","payload":{"kind":"readonly"}}`,
			`{"kind":"ci_type","op":"create","payload":{"key":"wp261_stale_new","name":"x"}}`)
		// The targets change after the capture.
		a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+typeID, `{"description":"wp261 later"}`, nil)
		a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/attribute-definitions/"+def, `{"description":"wp261 later"}`, nil)
		a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/rules/"+rule, `{"message":{"de":"wp261 später","en":"wp261 later"}}`, nil)
		a.direct.take()
		eventsBefore := a.publishedEvents()
		jobsBefore := a.count(`SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, f.OrgA, citype.IndexJobKind)
		w := a.publish(draftID)
		if w.Code != http.StatusConflict {
			t.Fatalf("publish a stale draft: %d %s", w.Code, w.Body.String())
		}
		var p struct {
			StaleItems []metamodel.StaleItem `json:"stale_items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		var stale []string
		for _, s := range p.StaleItems {
			stale = append(stale, s.ItemID)
		}
		want := slices.Clone(items[:3])
		sort.Strings(stale)
		sort.Strings(want)
		if !slices.Equal(stale, want) {
			t.Errorf("stale items %v, want %v", stale, want)
		}
		a.unchanged(w, draftID, eventsBefore, jobsBefore)
		if n := a.count(`SELECT count(*) FROM ci_type WHERE key = 'wp261_stale_new'`); n != 0 {
			t.Error("the stale publication created a type")
		}
	})

	t.Run("every regenerating trigger is deferrable", func(t *testing.T) {
		rows, err := f.Admin.Query(bg, `WITH RECURSIVE f AS (
				SELECT p.oid, p.proname FROM pg_proc p WHERE p.proname = 'regenerate_attribute_schema'
				UNION
				SELECT p.oid, p.proname FROM pg_proc p, f WHERE p.prosrc ~ ('\m' || f.proname || '\M') AND p.oid <> f.oid)
			SELECT t.tgname, t.tgdeferrable FROM pg_trigger t WHERE NOT t.tgisinternal AND t.tgfoid IN (SELECT oid FROM f)
			ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var name string
			var deferrable bool
			if err = rows.Scan(&name, &deferrable); err != nil {
				t.Fatal(err)
			}
			n++
			if !deferrable {
				t.Errorf("trigger %s regenerates the schema but is not deferrable", name)
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if n < 4 {
			t.Errorf("%d regenerating triggers, want at least 4", n)
		}
	})
}

// TestMetamodelDraftPublishWithoutDatabase: without the database the
// publication answers 503 (API-01).
func TestMetamodelDraftPublishWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	metamodel.NewHandler(nil).WithPublish(nil, nil).RegisterRoutes(mux)
	a := &draftAPI{t: t, mux: mux, user: "00000000-0000-4000-8000-000000000261"}
	if w := a.call("00000000-0000-4000-8000-000000000001", http.MethodPost,
		"/api/v1/metamodel/drafts/00000000-0000-4000-8000-000000000002/publish", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("publish without database: %d", w.Code)
	}
}
