package server_test

// WP-720 (MET-53, E-113): writes with ?draft to the metamodel resource
// routes land in the draft only. Through the complete server.NewRouter
// against PostgreSQL, with the index scheduler of the attribute
// definitions wired like cmd/server/workers.go and a webhook subscription
// on ci_type.updated and ci_attribute.changed:
//   - (a) a required field and a searchable attribute of type T in the
//     draft: a CI of T without the value is created, the type reads
//     unchanged, no attribute.index job, no delivery, no audit entry of the
//     type or the attribute;
//   - (b) a new type in the draft is neither listed nor found by discovery;
//   - (c) a required rule in the draft does not apply to a new CI;
//   - (d) a layout in the draft is not listed, the form keeps the standard
//     layout; the preview ?draft shows it with the required field (E-113).
//
// The draft then holds exactly these five items. Control: the same
// searchable attribute without ?draft schedules the job, is delivered and
// audited.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestDraftWritesStayInTheDraft$' ./internal/server/

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

func TestDraftWritesStayInTheDraft(t *testing.T) {
	f := scopetest.Seed(t, "ed")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp720-admin")

	var mu sync.Mutex
	var received []string
	subscriber := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if r.Header.Get("X-Webhook-Signature") != "" {
			mu.Lock()
			received = append(received, r.Header.Get("X-Webhook-Event"))
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer subscriber.Close()

	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	// The index scheduler as in cmd/server/workers.go.
	typeRepo, ok := repos.CIType.(*citype.PGRepository)
	if !ok {
		t.Fatalf("CI type repository %T", repos.CIType)
	}
	typeRepo.WithIndexScheduler(citype.NewDDLJob(f.App, jobs.NewService(repos.Jobs)))
	policy := egress.Options{AllowPrivate: true}
	dispatcher := webhook.NewDispatcher(repos.Webhook, egress.NewClient(policy), webhook.DispatcherOptions{
		Deliveries: repos.WebhookDeliveries, PollInterval: -1})
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp720", Entitlements: entitlements, Dispatcher: dispatcher,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.TenantMiddleware(mux)
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var reader io.Reader = http.NoBody
		if body != nil {
			raw, merr := json.Marshal(body)
			if merr != nil {
				t.Fatal(merr)
			}
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		principal := identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: identity.AllPermissions(), Credential: "wp720"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w.Code, w.Body.Bytes()
	}
	decode := func(raw []byte) map[string]any {
		t.Helper()
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return out
	}
	expect := func(want int, method, path string, body any) map[string]any {
		t.Helper()
		code, raw := call(method, path, body)
		if code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, code, raw, want)
		}
		if len(raw) == 0 {
			return nil
		}
		return decode(raw)
	}
	publisher := events.NewPublisher(f.Admin, nil, dispatcher)
	deliver := func() []string {
		t.Helper()
		if _, perr := publisher.PublishPending(bg); perr != nil {
			t.Fatalf("publish: %v", perr)
		}
		dispatcher.ProcessDue(bg)
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), received...)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	indexJobs := func() int {
		return count(`SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, f.OrgA, citype.IndexJobKind)
	}
	metamodelAudit := func() int {
		return count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type IN ('ci_type', 'attribute_definition')`, f.OrgA)
	}

	typ := expect(http.StatusCreated, http.MethodPost, "/api/v1/ci-types", map[string]any{"key": "wp720_t", "name": "WP-720 T"})
	typeID, _ := typ["id"].(string)
	expect(http.StatusCreated, http.MethodPost, "/api/v1/webhooks", map[string]any{
		"name": "wp720", "url": subscriber.URL + "/hook", "secret": "wp720-secret-0123456789",
		"events": []string{"ci_type.updated", "ci_attribute.changed"}})
	before := expect(http.StatusOK, http.MethodGet, "/api/v1/ci-types/"+typeID, nil)
	jobsBefore, auditBefore := indexJobs(), metamodelAudit()
	if got := deliver(); len(got) != 0 {
		t.Fatalf("deliveries before the draft: %v", got)
	}

	draft := expect(http.StatusCreated, http.MethodPost, "/api/v1/metamodel/drafts", map[string]any{"name": "wp720"})
	draftID, _ := draft["id"].(string)
	q := "?draft=" + draftID
	captured := func(method, path string, body any) map[string]any {
		t.Helper()
		item := expect(http.StatusAccepted, method, path+q, body)
		if item["id"] == "" || item["kind"] == "" {
			t.Fatalf("%s %s: answer %v, want the item", method, path, item)
		}
		return item
	}

	// (a) A required field and a searchable attribute of T.
	captured(http.MethodPut, "/api/v1/ci-types/"+typeID+"/fields", map[string]any{"name": "wp720_req", "data_type": "text", "required": true})
	captured(http.MethodPost, "/api/v1/attribute-definitions", map[string]any{"key": "wp720_search", "data_type": "text",
		"ci_type_id": typeID, "searchable": true})
	expect(http.StatusCreated, http.MethodPost, "/api/v1/cis", map[string]any{"name": "wp720-a", "ci_type_id": typeID, "status": "active"})
	after := expect(http.StatusOK, http.MethodGet, "/api/v1/ci-types/"+typeID, nil)
	for _, field := range []string{"attribute_schema", "required_fields", "schema_version", "updated_at"} {
		b, _ := json.Marshal(before[field])
		a, _ := json.Marshal(after[field])
		if !bytes.Equal(a, b) {
			t.Errorf("(a) %s of T changed: %s → %s", field, b, a)
		}
	}
	if n := indexJobs(); n != jobsBefore {
		t.Errorf("(a) attribute.index jobs: %d, want %d", n, jobsBefore)
	}
	if n := metamodelAudit(); n != auditBefore {
		t.Errorf("(a) audit entries of ci_type or attribute_definition: %d, want %d", n, auditBefore)
	}
	if got := deliver(); len(got) != 0 {
		t.Errorf("(a) deliveries: %v, want none", got)
	}

	// (b) A new type.
	captured(http.MethodPost, "/api/v1/ci-types", map[string]any{"key": "draft_probe", "name": "Draft probe"})
	listed := expect(http.StatusOK, http.MethodGet, "/api/v1/ci-types?limit=500&include_inactive=true", nil)
	if raw, _ := json.Marshal(listed["data"]); strings.Contains(string(raw), "draft_probe") {
		t.Errorf("(b) GET /api/v1/ci-types lists the draft type")
	}
	if id, lerr := discovery.NewPGRepository(f.App).LookupCITypeID(f.OrgCtx(f.OrgA), f.OrgA, "draft_probe"); lerr == nil {
		t.Errorf("(b) discovery finds the draft type: %s", id)
	}

	// (c) A required rule on the searchable attribute.
	captured(http.MethodPost, "/api/v1/rules", map[string]any{"key": "wp720_rule", "attribute_key": "wp720_search",
		"kind": "required", "ci_type_id": typeID})
	expect(http.StatusCreated, http.MethodPost, "/api/v1/cis", map[string]any{"name": "wp720-c", "ci_type_id": typeID, "status": "active"})

	// (d) A layout of T.
	layout := captured(http.MethodPost, "/api/v1/layouts", map[string]any{"key": "wp720_layout", "ci_type_id": typeID,
		"layout": map[string]any{"sections": []any{map[string]any{"key": "main", "title": map[string]string{"de": "Haupt", "en": "Main"},
			"fields": []any{map[string]any{"name": "wp720_search", "width": 6}}}}}})
	layouts := expect(http.StatusOK, http.MethodGet, "/api/v1/layouts?ci_type_id="+typeID, nil)
	if data, _ := layouts["data"].([]any); len(data) != 0 {
		t.Errorf("(d) GET /api/v1/layouts lists %v", data)
	}
	if form := expect(http.StatusOK, http.MethodGet, "/api/v1/ci-types/"+typeID+"/form", nil); form["source"] != citype.FormSourceDefault {
		t.Errorf("(d) published form: source %v, want default", form["source"])
	}

	got := expect(http.StatusOK, http.MethodGet, "/api/v1/metamodel/drafts/"+draftID, nil)
	if n, _ := got["item_count"].(float64); n != 5 {
		t.Errorf("draft holds %v items, want 5", got["item_count"])
	}

	// The preview of the draft (E-113): the layout of (d) with the
	// attribute of (a), required by the rule of (c), and the required
	// field of (a) in _required.
	preview := expect(http.StatusOK, http.MethodGet, "/api/v1/ci-types/"+typeID+"/form"+q, nil)
	if preview["source"] != citype.FormSourceLayout || preview["layout_id"] != layout["id"] {
		t.Errorf("preview: source %v layout %v, want the layout item %v", preview["source"], preview["layout_id"], layout["id"])
	}
	required := map[string]string{}
	sections, _ := preview["sections"].([]any)
	for _, s := range sections {
		section, _ := s.(map[string]any)
		fields, _ := section["fields"].([]any)
		for _, fd := range fields {
			field, _ := fd.(map[string]any)
			state, _ := field["state"].(map[string]any)
			if state["required"] == true {
				required[field["name"].(string)], _ = section["key"].(string)
			}
		}
	}
	if required["wp720_search"] != "main" || required["wp720_req"] != citype.FormSectionRequired {
		t.Errorf("preview: required fields by section %v, want wp720_search in main and wp720_req in _required", required)
	}
	if n := indexJobs(); n != jobsBefore {
		t.Errorf("attribute.index jobs after the draft: %d, want %d", n, jobsBefore)
	}

	// An unknown draft is 404, a discarded one 409, for the preview and
	// the capture alike.
	const unknown = "?draft=00000000-0000-4000-8000-0000000007a0"
	if code, raw := call(http.MethodGet, "/api/v1/ci-types/"+typeID+"/form"+unknown, nil); code != http.StatusNotFound {
		t.Errorf("preview of an unknown draft: %d %s, want 404", code, raw)
	}
	discarded := expect(http.StatusCreated, http.MethodPost, "/api/v1/metamodel/drafts", map[string]any{"name": "wp720-discarded"})
	discardedID, _ := discarded["id"].(string)
	expect(http.StatusOK, http.MethodDelete, "/api/v1/metamodel/drafts/"+discardedID, nil)
	if code, raw := call(http.MethodGet, "/api/v1/ci-types/"+typeID+"/form?draft="+discardedID, nil); code != http.StatusConflict {
		t.Errorf("preview of a discarded draft: %d %s, want 409", code, raw)
	}
	if code, raw := call(http.MethodPatch, "/api/v1/ci-types/"+typeID+"?draft="+discardedID, map[string]any{"description": "x"}); code != http.StatusConflict {
		t.Errorf("capture into a discarded draft: %d %s, want 409", code, raw)
	}
	if code, raw := call(http.MethodPatch, "/api/v1/ci-types/"+typeID+unknown, map[string]any{"description": "x"}); code != http.StatusNotFound {
		t.Errorf("capture into an unknown draft: %d %s, want 404", code, raw)
	}

	// Control: the same attribute without ?draft.
	expect(http.StatusCreated, http.MethodPost, "/api/v1/attribute-definitions", map[string]any{"key": "wp720_search",
		"data_type": "text", "ci_type_id": typeID, "searchable": true})
	if n := indexJobs(); n != jobsBefore+1 {
		t.Errorf("control: attribute.index jobs %d, want %d", n, jobsBefore+1)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = 'attribute_definition'
		AND action = 'attribute_definition.created'`, f.OrgA); n != 1 {
		t.Errorf("control: %d audit entries attribute_definition.created, want 1", n)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		delivered := deliver()
		if strings.Contains(strings.Join(delivered, ","), "ci_attribute.changed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("control: no delivery of ci_attribute.changed; received %v", delivered)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
