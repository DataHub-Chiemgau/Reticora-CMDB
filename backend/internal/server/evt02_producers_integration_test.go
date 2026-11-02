// Producer evidence of EVT-02 and NTF-05 (WP-551; audit WP-136, WP-140).
//
//   - TestEveryEVT02TypeHasAProducer – EVT-01, EVT-02, NTF-05: for every
//     type of events.Types (the 16 of EVT-02, the NTF-05 types
//     entitlement.expired, invitation.created, webhook.dead and
//     certificate.expiring, and apikey.creator_deactivated of TLC-04/E-64)
//     its production action – a request through the complete router or the
//     scheduler run that owns the type, on the PostgreSQL repositories –
//     writes exactly one event of the type. The envelope names the
//     organization, continues the organization's sequence without a gap,
//     names the entity and continues its entity_seq, validates against
//     api/events.schema.json and is invisible to organization B. While the
//     outbox refuses the event (a trigger on event_outbox stands for a
//     failure between mutation and event) the action fails and leaves no
//     event; the mutation is rolled back with it, so the same action
//     succeeds afterwards with exactly one event. The table of the test is
//     compared with events.Types: a type without a producer case fails.
//
// The router is the one of audit_t6_harness_test.go with an invitation
// accept URL and a blob store, as cmd/server/main.go wires them.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -run 'TestEveryEVT02TypeHasAProducer' ./internal/server/
package server_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// evt02Case is the production action of one event type.
type evt02Case struct {
	typ string
	// setup prepares what the action needs.
	setup func(t *testing.T)
	// act runs the action once. refused is true while the outbox refuses
	// the event: the action must fail (or report the failure) and leave its
	// mutation undone. Otherwise it must succeed; it returns the entity the
	// event concerns.
	act func(t *testing.T, refused bool) string
	// again prepares the second run after the refused one (optional).
	again func(t *testing.T)
	// data checks the data of the event (optional).
	data func(t *testing.T, entity string, data map[string]any)
}

// evt02NewEnv builds the router of t6NewEnv on the fixture with tag, with
// the invitation accept URL and the blob store of cmd/server (invitations
// and export jobs answer 503 without them).
func evt02NewEnv(t *testing.T, tag string, blobs blob.Store) *t6Env {
	t.Helper()
	f := scopetest.Seed(t, tag)
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	policy := egress.Options{AllowPrivate: true}
	dispatcher := webhook.NewDispatcher(repos.Webhook, egress.NewClient(policy), webhook.DispatcherOptions{
		Deliveries: repos.WebhookDeliveries, PollInterval: -1})
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	notifications := &notify.Center{
		Channels:    notify.NewPGRepository(f.App, notify.NewSealer(encryptor)),
		Inbox:       notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App),
		Deliveries:  notify.NewPGDeliveryStore(f.App),
	}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "evt02", Entitlements: entitlements, Dispatcher: dispatcher,
		CIService:           ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials:         credential.NewService(repos.Credential, encryptor),
		OIDC:                identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:              policy,
		AuditPool:           f.App,
		Audit:               audit.NewHandler(f.App),
		Notifications:       notifications,
		Events:              events.NewPGEmitter(f.App),
		Blobs:               blobs,
		InvitationAcceptURL: "https://cmdb.example.invalid/invitations/accept",
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return &t6Env{F: f, Repos: repos, handler: middleware.RequestID(middleware.TenantMiddleware(mux))}
}

func TestEveryEVT02TypeHasAProducer(t *testing.T) {
	blobs := blob.NewFileStore(t.TempDir())
	e := evt02NewEnv(t, "55", blobs)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "evt02-admin"), identity.AllPermissions()}
	schema := evt02LoadSchema(t)
	// The validator refuses what the schema forbids: id no uuid, unknown
	// type, occurred_at no date-time, sequence 0, entity_id without
	// entity_seq; data missing, an undeclared property.
	for bad, violations := range map[string]int{
		`{"id":"x","type":"ci.updated","org_id":"` + f.OrgA + `","occurred_at":"yesterday","sequence":0,"entity_id":"e","data":{}}`:         5,
		`{"id":"` + f.OrgA + `","type":"ci.created","org_id":"` + f.OrgA + `","occurred_at":"2026-10-27T10:00:00Z","sequence":1,"extra":1}`: 2,
	} {
		if problems := evt02Validate(schema, []byte(bad)); len(problems) != violations {
			t.Fatalf("validator: %s gives %v, want %d schema violations", bad, problems, violations)
		}
	}

	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(t *testing.T, sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	call := func(t *testing.T, as t6As, method, path, body string) t6Response {
		t.Helper()
		return e.Call(t, as, method, path, body)
	}
	// answered checks the answer of a request: refused, the request must
	// fail; otherwise it must answer want.
	answered := func(t *testing.T, what string, r t6Response, refused bool, want int) {
		t.Helper()
		if refused {
			if r.Code < 400 {
				t.Fatalf("%s = %d %s although the outbox refused its event, want an error", what, r.Code, t6Short(r.Body))
			}
			return
		}
		if r.Code != want {
			t.Fatalf("%s = %d %s, want %d", what, r.Code, t6Short(r.Body), want)
		}
	}
	// failed checks the error of a run: refused, the run must report one.
	failed := func(t *testing.T, what string, err error, refused bool) {
		t.Helper()
		if refused && err == nil {
			t.Fatalf("%s reported no error although the outbox refused its event", what)
		}
		if !refused && err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// unchanged checks after a refused action that its mutation is undone.
	unchanged := func(t *testing.T, what, got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s after the refused event: %s, want %s (mutation not rolled back)", what, got, want)
		}
	}
	sameID := func(field string) func(t *testing.T, entity string, data map[string]any) {
		return func(t *testing.T, entity string, data map[string]any) {
			t.Helper()
			if data[field] != entity {
				t.Errorf("data.%s = %v, want the entity %s", field, data[field], entity)
			}
		}
	}

	serverType := scalar(t, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
	viewerRole := scalar(t, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA)
	// observed inserts a discovered CI of the observable type server.
	observed := func(t *testing.T, name, status string, lastSeen time.Time) string {
		t.Helper()
		id := f.ID()
		exec(t, `INSERT INTO ci (id, organization_id, ci_type_id, name, status, is_manual, discovery_source, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, false, 'snmp', $6)`, id, f.OrgA, serverType, name, status, lastSeen)
		return id
	}
	ciStatus := func(t *testing.T, id string) string {
		return scalar(t, `SELECT status || CASE WHEN deleted_at IS NULL THEN '' ELSE ' (deleted)' END FROM ci WHERE id = $1`, id)
	}
	count := func(t *testing.T, sql string, args ...any) string {
		t.Helper()
		return scalar(t, `SELECT count(*)::text FROM `+sql, args...)
	}

	var (
		changed, deleted, restored, seen   string
		deletedState                       string
		survivor, victim                   string
		relSource, relTarget, removedEdge  string
		enrollCode                         string
		silentCollector                    string
		reviewItem                         string
		exportJob                          string
		failingJob                         *jobs.Job
		jobService                         *jobs.Service
		warned, expired                    string
		hookServer                         *httptest.Server
		hookSubscription, hookDelivery     string
		hookDispatcher                     *webhook.Dispatcher
		certCollector                      string
		creator                            string
		exportWorker                       = export.NewJobWorker(e.Repos.ExportJobs, e.Repos.CI, blobs)
		exportJobs                         = export.NewPGJobRepository(f.App)
		unclassifiedSerial, unclassifiedIP = "EVT02-UNCL-1", "10.55.1.1"
	)
	upsertEntitlement := func(t *testing.T, feature string, validUntil time.Time) string {
		t.Helper()
		return scalar(t, `INSERT INTO entitlement (organization_id, feature_key, enabled, valid_until) VALUES ($1, $2, true, $3)
			ON CONFLICT (organization_id, feature_key) DO UPDATE SET enabled = true, valid_until = EXCLUDED.valid_until
			RETURNING id::text`, f.OrgA, feature, validUntil)
	}
	noticesSent := func(t *testing.T, kind, key string) string {
		return count(t, `notice_sent WHERE organization_id = $1 AND kind = $2 AND dedup_key LIKE $3 || '%'`, f.OrgA, kind, key)
	}

	cases := []evt02Case{
		{typ: events.CICreated,
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/cis", `{"name":"evt02-created","ci_type_id":"`+serverType+`"}`)
				answered(t, "POST /cis", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "CIs named evt02-created", count(t, `ci WHERE organization_id = $1 AND name = 'evt02-created'`, f.OrgA), "0")
				}
				return t6ID(r.Body)
			},
			data: sameID("id")},
		{typ: events.CIChanged,
			setup: func(t *testing.T) { changed = f.CI(t, f.OrgA, "", "evt02-changed") },
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPatch, "/api/v1/cis/"+changed, `{"status":"maintenance"}`)
				answered(t, "PATCH /cis/{id}", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "CI status", ciStatus(t, changed), "active")
				}
				return changed
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				fields, _ := data["changed_fields"].([]any)
				if data["id"] != entity || data["status"] != "maintenance" || len(fields) != 1 || fields[0] != "status" {
					t.Errorf("ci.changed data: id %v, status %v, changed_fields %v; want %s, maintenance, [status]",
						data["id"], data["status"], data["changed_fields"], entity)
				}
			}},
		{typ: events.CIDeleted,
			setup: func(t *testing.T) { deleted = f.CI(t, f.OrgA, "", "evt02-deleted") },
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodDelete, "/api/v1/cis/"+deleted, ``)
				answered(t, "DELETE /cis/{id}", r, refused, http.StatusNoContent)
				if refused {
					unchanged(t, "CI", ciStatus(t, deleted), "active")
				}
				return deleted
			},
			data: sameID("id")},
		{typ: events.CIRestored,
			setup: func(t *testing.T) {
				restored = f.CI(t, f.OrgA, "", "evt02-restored")
				if r := call(t, admin, http.MethodDelete, "/api/v1/cis/"+restored, ``); r.Code != http.StatusNoContent {
					t.Fatalf("setup: DELETE /cis/{id} = %d %s", r.Code, t6Short(r.Body))
				}
				deletedState = ciStatus(t, restored)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/cis/"+restored+"/restore", ``)
				answered(t, "POST /cis/{id}/restore", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "CI", ciStatus(t, restored), deletedState)
				}
				return restored
			},
			data: sameID("id")},
		{typ: events.CIMerged,
			setup: func(t *testing.T) {
				survivor, victim = f.CI(t, f.OrgA, "", "evt02-survivor"), f.CI(t, f.OrgA, "", "evt02-victim")
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`)
				answered(t, "POST /cis/{id}/merge", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "merges of the victim", count(t, `ci_merge WHERE organization_id = $1 AND victim_id = $2`, f.OrgA, victim), "0")
					unchanged(t, "victim", ciStatus(t, victim), "active")
				}
				return survivor
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["survivor_id"] != entity || data["victim_id"] != victim || data["merge_id"] == "" {
					t.Errorf("ci.merged data %v, want survivor %s, victim %s and the merge", data, entity, victim)
				}
			}},
		{typ: events.CIOffline,
			setup: func(t *testing.T) {
				// Not seen for 30 days, far beyond three times the default
				// scope interval of 24 hours.
				seen = observed(t, "evt02-offline", "active", time.Now().Add(-30*24*time.Hour))
			},
			act: func(t *testing.T, refused bool) string {
				c, err := lifecycle.NewOfflineDetector(f.App).RunOrg(bg, f.OrgA)
				failed(t, "offline detector", err, refused)
				if refused {
					unchanged(t, "CI status", ciStatus(t, seen), "active")
				} else if c.Offline != 1 || c.Online != 0 {
					t.Fatalf("offline detector: %+v, want one CI offline", c)
				}
				return seen
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["status"] != "unknown" {
					t.Errorf("ci.offline data %v, want CI %s with status unknown", data, entity)
				}
			}},
		{typ: events.CIOnline,
			setup: func(t *testing.T) { seen = observed(t, "evt02-online", "unknown", time.Now()) },
			act: func(t *testing.T, refused bool) string {
				c, err := lifecycle.NewOfflineDetector(f.App).RunOrg(bg, f.OrgA)
				failed(t, "offline detector", err, refused)
				if refused {
					unchanged(t, "CI status", ciStatus(t, seen), "unknown")
				} else if c.Online != 1 || c.Offline != 0 {
					t.Fatalf("offline detector: %+v, want one CI online again", c)
				}
				return seen
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["status"] != "active" {
					t.Errorf("ci.online data %v, want CI %s with status active", data, entity)
				}
			}},
		{typ: events.RelationshipCreated,
			setup: func(t *testing.T) {
				relSource, relTarget = f.CI(t, f.OrgA, "", "evt02-rel-source"), f.CI(t, f.OrgA, "", "evt02-rel-target")
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/relationships",
					`{"source_ci_id":"`+relSource+`","target_ci_id":"`+relTarget+`","rel_type":"depends_on"}`)
				answered(t, "POST /relationships", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "edges", count(t, `ci_relationship WHERE organization_id = $1 AND source_ci_id = $2`, f.OrgA, relSource), "0")
				}
				return t6ID(r.Body)
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["source_ci_id"] != relSource || data["target_ci_id"] != relTarget || data["rel_type"] != "depends_on" {
					t.Errorf("relationship.created data %v, want edge %s %s -> %s depends_on", data, entity, relSource, relTarget)
				}
			}},
		{typ: events.RelationshipRemoved,
			setup: func(t *testing.T) {
				a, b := f.CI(t, f.OrgA, "", "evt02-rm-source"), f.CI(t, f.OrgA, "", "evt02-rm-target")
				r := call(t, admin, http.MethodPost, "/api/v1/relationships", `{"source_ci_id":"`+a+`","target_ci_id":"`+b+`","rel_type":"depends_on"}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("setup: POST /relationships = %d %s", r.Code, t6Short(r.Body))
				}
				removedEdge = t6ID(r.Body)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodDelete, "/api/v1/relationships/"+removedEdge, ``)
				answered(t, "DELETE /relationships/{id}", r, refused, http.StatusNoContent)
				if refused {
					unchanged(t, "edge rows", count(t, `ci_relationship WHERE id = $1`, removedEdge), "1")
				}
				return removedEdge
			},
			data: sameID("id")},
		{typ: events.CollectorEnrolled,
			setup: func(t *testing.T) {
				r := call(t, admin, http.MethodPost, "/api/v1/collectors/enrollment-codes", `{"label":"evt02"}`)
				var code struct {
					Code string `json:"code"`
				}
				if r.Code != http.StatusCreated || json.Unmarshal(r.Body, &code) != nil || code.Code == "" {
					t.Fatalf("setup: POST /collectors/enrollment-codes = %d %s", r.Code, t6Short(r.Body))
				}
				enrollCode = code.Code
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, t6As{}, http.MethodPost, "/api/v1/collectors/enroll", `{"code":"`+enrollCode+`","name":"evt02-collector"}`)
				answered(t, "POST /collectors/enroll", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "collectors", count(t, `collector WHERE organization_id = $1 AND name = 'evt02-collector'`, f.OrgA), "0")
				}
				return t6ID(r.Body)
			},
			data: sameID("id")},
		{typ: events.CollectorOffline,
			setup: func(t *testing.T) {
				silentCollector = scalar(t, `INSERT INTO collector (organization_id, name, status, last_heartbeat_at) VALUES ($1, 'evt02-silent', 'active', $2)
					RETURNING id::text`, f.OrgA, time.Now().Add(-time.Hour))
			},
			act: func(t *testing.T, refused bool) string {
				c, err := discovery.NewStatusMonitor(f.App).RunOrg(bg, f.OrgA)
				failed(t, "collector status monitor", err, refused)
				if refused {
					unchanged(t, "collector status", scalar(t, `SELECT status FROM collector WHERE id = $1`, silentCollector), "active")
				} else if c.Offline != 1 {
					t.Fatalf("collector status monitor: %+v, want one collector offline", c)
				}
				return silentCollector
			},
			data: sameID("id")},
		{typ: events.ReviewCreated,
			act: func(t *testing.T, refused bool) string {
				// A device of a CI type the server does not know opens an
				// unclassified_device review (REC-08).
				rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "evt02-unknown-type", Name: "evt02-appliance",
					IP: unclassifiedIP, Serial: unclassifiedSerial}, time.Now().Add(-time.Minute))
				body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
				if err != nil {
					t.Fatal(err)
				}
				r := call(t, admin, http.MethodPost, "/api/v1/ingest/bulk", string(body))
				items := count(t, `review_item WHERE organization_id = $1 AND kind = 'unclassified_device' AND payload::text LIKE '%' || $2 || '%'`,
					f.OrgA, unclassifiedSerial)
				if refused {
					if items != "0" {
						t.Fatalf("POST /ingest/bulk = %d %s left %s review items although the outbox refused review.created",
							r.Code, t6Short(r.Body), items)
					}
					return ""
				}
				if r.Code != http.StatusAccepted || items != "1" {
					t.Fatalf("POST /ingest/bulk = %d %s with %s review items, want 202 and one", r.Code, t6Short(r.Body), items)
				}
				return scalar(t, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'unclassified_device'
					AND payload::text LIKE '%' || $2 || '%'`, f.OrgA, unclassifiedSerial)
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["kind"] != "unclassified_device" {
					t.Errorf("review.created data %v, want item %s of kind unclassified_device", data, entity)
				}
			}},
		{typ: events.ReviewResolved,
			setup: func(t *testing.T) {
				reviewItem = scalar(t, `INSERT INTO review_item (organization_id, kind, payload) VALUES ($1, 'unclassified_device', '{"ip":"192.0.2.55"}')
					RETURNING id::text`, f.OrgA)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/review-items/"+reviewItem+"/resolve", `{"action":"dismiss"}`)
				answered(t, "POST /review-items/{id}/resolve", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "review item", scalar(t, `SELECT status FROM review_item WHERE id = $1`, reviewItem), "open")
				}
				return reviewItem
			},
			data: sameID("id")},
		{typ: events.ExportCompleted,
			setup: func(t *testing.T) {
				r := call(t, admin, http.MethodPost, "/api/v1/exports", `{"format":"json"}`)
				if r.Code != http.StatusAccepted {
					t.Fatalf("setup: POST /exports = %d %s", r.Code, t6Short(r.Body))
				}
				exportJob = t6ID(r.Body)
			},
			act: func(t *testing.T, refused bool) string {
				for exportWorker.ProcessOne(bg) {
				}
				status := scalar(t, `SELECT status FROM export_job WHERE id = $1`, exportJob)
				if refused {
					unchanged(t, "export job", status, export.JobStatusRunning)
				} else if status != export.JobStatusCompleted {
					t.Fatalf("export job %s, want completed", status)
				}
				return exportJob
			},
			// The export runs again once its stale run is reclaimed (TEC-14).
			again: func(t *testing.T) {
				var started time.Time
				if err := f.Admin.QueryRow(bg, `SELECT started_at FROM export_job WHERE id = $1`, exportJob).Scan(&started); err != nil {
					t.Fatal(err)
				}
				if _, err := exportJobs.ReclaimStale(bg, started.Add(time.Millisecond)); err != nil {
					t.Fatal(err)
				}
			},
			data: sameID("id")},
		{typ: events.JobFailed,
			setup: func(t *testing.T) {
				jobService = jobs.NewService(e.Repos.Jobs)
				jobService.Register(jobs.Kind{Name: "evt02.sync", MaxAttempts: 1, Timeout: time.Minute})
				job, _, err := jobService.Enqueue(bg, f.OrgA, "evt02.sync", nil, jobs.EnqueueOptions{CreatedBy: admin.User})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = jobService.ClaimJob(bg, f.OrgA, job.ID); err != nil {
					t.Fatal(err)
				}
				failingJob = job
			},
			act: func(t *testing.T, refused bool) string {
				_, err := jobService.Fail(bg, f.OrgA, failingJob.ID, errors.New("evt02: upstream gone"))
				failed(t, "fail the job", err, refused)
				if refused {
					unchanged(t, "job", scalar(t, `SELECT status FROM job WHERE id = $1`, failingJob.ID), string(jobs.StatusRunning))
				}
				return failingJob.ID
			},
			data: sameID("id")},
		{typ: events.EntitlementWarning,
			setup: func(t *testing.T) { warned = upsertEntitlement(t, "topology", time.Now().Add(5*24*time.Hour)) },
			act: func(t *testing.T, refused bool) string {
				_, err := jobs.NewEntitlementNotices(f.App).Run(bg)
				failed(t, "entitlement notices", err, refused)
				if refused {
					unchanged(t, "notices sent", noticesSent(t, events.EntitlementWarning, warned), "0")
				}
				return warned
			},
			data: func(t *testing.T, _ string, data map[string]any) {
				if data["feature"] != "topology" || data["days_left"] != float64(5) {
					t.Errorf("entitlement.warning data %v, want topology with 5 days left", data)
				}
			}},
		{typ: events.EntitlementExpired,
			setup: func(t *testing.T) { expired = upsertEntitlement(t, "monitoring", time.Now().Add(-time.Hour)) },
			act: func(t *testing.T, refused bool) string {
				_, err := jobs.NewEntitlementNotices(f.App).Run(bg)
				failed(t, "entitlement notices", err, refused)
				if refused {
					unchanged(t, "notices sent", noticesSent(t, events.EntitlementExpired, expired), "0")
				}
				return expired
			},
			data: func(t *testing.T, _ string, data map[string]any) {
				if data["feature"] != "monitoring" || data["expired_at"] == nil {
					t.Errorf("entitlement.expired data %v, want monitoring with expired_at", data)
				}
			}},
		{typ: events.InvitationCreated,
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/invitations", `{"email":"evt02-invitee@example.org","role_id":"`+viewerRole+`"}`)
				answered(t, "POST /invitations", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "invitations", count(t, `user_invitation WHERE organization_id = $1`, f.OrgA), "0")
				}
				return t6ID(r.Body)
			},
			data: sameID("id")},
		{typ: events.WebhookDead,
			setup: func(t *testing.T) {
				hookServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadGateway)
				}))
				t.Cleanup(hookServer.Close)
				r := call(t, admin, http.MethodPost, "/api/v1/webhooks", `{"name":"evt02","url":"`+hookServer.URL+`/hook",`+
					`"secret":"evt02-secret-0123456789","events":["ci.created"]}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("setup: POST /webhooks = %d %s", r.Code, t6Short(r.Body))
				}
				hookSubscription = t6ID(r.Body)
				due := time.Now().Add(-time.Minute)
				rec, err := e.Repos.WebhookDeliveries.Enqueue(f.OrgCtx(f.OrgA), webhook.DeliveryRecord{OrganizationID: f.OrgA,
					SubscriptionID: hookSubscription, Event: "ci.created", Payload: []byte(`{"id":"evt02"}`), MaxAttempts: 1, NextRetryAt: &due})
				if err != nil {
					t.Fatal(err)
				}
				hookDelivery = rec.ID
				hookDispatcher = webhook.NewDispatcher(e.Repos.Webhook, hookServer.Client(),
					webhook.DispatcherOptions{Deliveries: e.Repos.WebhookDeliveries, MaxAttempts: 1, PollInterval: -1})
				t.Cleanup(func() { _ = hookDispatcher.Shutdown(context.Background()) })
			},
			act: func(t *testing.T, refused bool) string {
				hookDispatcher.ProcessDue(bg)
				status := scalar(t, `SELECT status FROM webhook_delivery WHERE id = $1`, hookDelivery)
				letters := count(t, `webhook_dead_letter WHERE delivery_id = $1`, hookDelivery)
				if refused {
					unchanged(t, "delivery and dead letters", status+"/"+letters, webhook.StatusFailed+"/0")
				} else if status != webhook.StatusDead || letters != "1" {
					t.Fatalf("delivery %s with %s dead letters, want dead with one", status, letters)
				}
				return hookSubscription
			},
			// The retry worker tries the failed delivery again.
			again: func(t *testing.T) {
				exec(t, `UPDATE webhook_delivery SET next_retry_at = now() - interval '1 second' WHERE id = $1`, hookDelivery)
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["subscription_id"] != entity || data["delivery_id"] != hookDelivery || data["event"] != "ci.created" {
					t.Errorf("webhook.dead data %v, want subscription %s, delivery %s, event ci.created", data, entity, hookDelivery)
				}
			}},
		{typ: events.CertificateExpiring,
			setup: func(t *testing.T) {
				// The run also reports a due collector CA to every
				// organization with a certificate; the one of this test is
				// the collector certificate alone.
				evt02NoDueCA(t, f)
				certCollector = scalar(t, `INSERT INTO collector (organization_id, name, status) VALUES ($1, 'evt02-cert', 'offline') RETURNING id::text`, f.OrgA)
				now := time.Now().UTC()
				notAfter := now.Add(30 * 24 * time.Hour)
				fp := fmt.Sprintf("%064x", time.Now().UnixNano())
				exec(t, `INSERT INTO collector_certificate (organization_id, collector_id, serial, fingerprint, not_before, not_after)
					VALUES ($1, $2, $3, $4, $5, $6)`, f.OrgA, certCollector, fp[:32], fp, now.Add(-60*24*time.Hour), notAfter)
				exec(t, `UPDATE collector SET cert_fingerprint = $2, cert_not_after = $3 WHERE id = $1`, certCollector, fp, notAfter)
			},
			act: func(t *testing.T, refused bool) string {
				_, err := jobs.NewCertificateNotices(f.App).Run(bg)
				failed(t, "certificate notices", err, refused)
				if refused {
					unchanged(t, "notices sent", count(t, `notice_sent WHERE organization_id = $1 AND kind = $2`, f.OrgA, events.CertificateExpiring), "0")
				}
				return certCollector
			},
			data: sameID("collector_id")},
		{typ: events.APIKeyCreatorDeactivated,
			setup: func(t *testing.T) {
				creator = f.AppUser(t, f.OrgA, "evt02-creator")
				account := f.ID()
				exec(t, `INSERT INTO service_account (id, organization_id, name, created_by) VALUES ($1, $2, 'evt02-account', $3)`, account, f.OrgA, creator)
				exec(t, `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, created_by, service_account_id)
					VALUES ($1, 'evt02-key', 'evt02-hash', 'evt02pfx', $2, $3)`, f.OrgA, creator, account)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPatch, "/api/v1/users/"+creator, `{"status":"inactive"}`)
				answered(t, "PATCH /users/{id}", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "user active", scalar(t, `SELECT is_active::text FROM app_user WHERE id = $1`, creator), "true")
				}
				return creator
			},
			data: sameID("user_id")},
	}

	// The table covers events.Types exactly: a new type without a producer
	// case fails here.
	var covered, known []string
	for _, c := range cases {
		covered = append(covered, c.typ)
	}
	known = append(known, events.Types...)
	sort.Strings(covered)
	sort.Strings(known)
	if strings.Join(covered, ",") != strings.Join(known, ",") {
		t.Fatalf("producer cases %v, event types %v: every type of events.Types needs exactly one producer case", covered, known)
	}

	refuse := evt02RefuseEvents(t, f)
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			// The outbox refuses the event: the action fails and leaves
			// no event.
			before := evt02MaxSequence(t, f)
			refuse(t, c.typ)
			c.act(t, true)
			refuse(t, "")
			if n := len(evt02EventsAfter(t, f, f.OrgA, before, c.typ)); n != 0 {
				t.Fatalf("%d %s events after the refused action, want none (rolled back)", n, c.typ)
			}
			if c.again != nil {
				c.again(t)
			}

			// The same action succeeds and writes exactly one event.
			before = evt02MaxSequence(t, f)
			entity := c.act(t, false)
			if entity == "" {
				t.Fatal("the action names no entity")
			}
			all := evt02EventsAfter(t, f, f.OrgA, before, "")
			for i, env := range all {
				if env.OrgID != f.OrgA || env.Sequence != before+int64(i)+1 {
					t.Errorf("event %d (%s): organization %s, sequence %d; want %s and %d (sequence per organization without gaps)",
						i, env.Type, env.OrgID, env.Sequence, f.OrgA, before+int64(i)+1)
				}
			}
			mine := evt02EventsAfter(t, f, f.OrgA, before, c.typ)
			if len(mine) != 1 {
				t.Fatalf("%d %s events from one action, want exactly one: %+v", len(mine), c.typ, mine)
			}
			env := mine[0]
			if env.EntityID != entity {
				t.Errorf("entity_id %q, want %s", env.EntityID, entity)
			}
			var earlier int64
			if err := f.Admin.QueryRow(bg, `SELECT COALESCE(max(entity_seq), 0) FROM event_outbox
				WHERE organization_id = $1 AND entity_id = $2 AND sequence < $3`, f.OrgA, entity, env.Sequence).Scan(&earlier); err != nil {
				t.Fatal(err)
			}
			if env.EntitySeq != earlier+1 {
				t.Errorf("entity_seq %d, want %d (entity sequence of %s without gaps)", env.EntitySeq, earlier+1, entity)
			}
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			if problems := evt02Validate(schema, raw); len(problems) != 0 {
				t.Errorf("envelope %s does not validate against api/events.schema.json: %v", raw, problems)
			}
			if n := len(evt02EventsAfter(t, f, f.OrgB, 0, c.typ)); n != 0 {
				t.Errorf("organization B sees %d %s events", n, c.typ)
			}
			if c.data != nil {
				var data map[string]any
				if err = json.Unmarshal(env.Data, &data); err != nil {
					t.Fatalf("data %s: %v", env.Data, err)
				}
				c.data(t, entity, data)
			}
		})
	}
}

// evt02RefuseEvents installs a trigger on event_outbox that refuses the
// events of one type of organization A (refuse(t, typ)); refuse(t, "")
// lets every event through. It stands for a failure between a mutation and
// its event.
func evt02RefuseEvents(t *testing.T, f *scopetest.Fixture) func(t *testing.T, typ string) {
	t.Helper()
	bg := context.Background()
	fn := fmt.Sprintf("wp551_refuse_event_%d", time.Now().UnixNano())
	refuse := func(t *testing.T, typ string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `CREATE OR REPLACE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.organization_id = '`+f.OrgA+`' AND NEW.type = '`+typ+`' THEN
		RAISE EXCEPTION 'wp551: the outbox refuses %', NEW.type;
	END IF;
	RETURN NEW;
END $$`); err != nil {
			t.Fatalf("trigger function: %v", err)
		}
	}
	refuse(t, "")
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`); err != nil {
			t.Errorf("drop trigger function: %v", err)
		}
	})
	if _, err := f.Admin.Exec(bg, `CREATE TRIGGER `+fn+` BEFORE INSERT ON event_outbox FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	return refuse
}

// evt02MaxSequence is the last sequence of organization A.
func evt02MaxSequence(t *testing.T, f *scopetest.Fixture) int64 {
	t.Helper()
	var n int64
	if err := f.Admin.QueryRow(context.Background(), `SELECT COALESCE(max(sequence), 0) FROM event_outbox WHERE organization_id = $1`,
		f.OrgA).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// evt02EventsAfter reads the envelopes of organization A after sequence
// after (of type typ, all types when empty) as the publisher does: as the
// application in the scope of organization viewer, in sequence order.
func evt02EventsAfter(t *testing.T, f *scopetest.Fixture, viewer string, after int64, typ string) []events.Envelope {
	t.Helper()
	var out []events.Envelope
	scope := database.OrgWideScope(viewer, "")
	if err := database.WithTenant(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, type, organization_id::text, occurred_at, sequence,
			COALESCE(entity_id, ''), COALESCE(entity_seq, 0), data
			FROM event_outbox WHERE organization_id = $1 AND sequence > $2 AND ($3 = '' OR type = $3) ORDER BY sequence`,
			f.OrgA, after, typ)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (events.Envelope, error) {
			var e events.Envelope
			scanErr := row.Scan(&e.ID, &e.Type, &e.OrgID, &e.OccurredAt, &e.Sequence, &e.EntityID, &e.EntitySeq, &e.Data)
			e.OccurredAt = e.OccurredAt.UTC()
			return e, scanErr
		})
		return err
	}); err != nil {
		t.Fatalf("read the outbox as organization %s: %v", viewer, err)
	}
	return out
}

// evt02NoDueCA fails when the active collector CA reached its renewal
// point: its notice would be a second certificate.expiring.
func evt02NoDueCA(t *testing.T, f *scopetest.Fixture) {
	t.Helper()
	var certPEM string
	err := f.Admin.QueryRow(context.Background(), `SELECT cert_pem FROM collector_ca WHERE state = 'active'`).Scan(&certPEM)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("precondition: the active collector CA is no PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	renewal := cert.NotBefore.Add(time.Duration(float64(cert.NotAfter.Sub(cert.NotBefore)) * jobs.CertificateRenewAfter))
	if !time.Now().Before(renewal) {
		t.Fatalf("precondition: the active collector CA is due for renewal since %v; its notice would be a second certificate.expiring", renewal)
	}
}

// evt02LoadSchema reads api/events.schema.json.
func evt02LoadSchema(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../api/events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

// evt02Validate validates the JSON document raw against schema. It
// evaluates the keywords api/events.schema.json uses; any other keyword is
// reported as a problem, so a schema change cannot pass unchecked.
func evt02Validate(schema map[string]any, raw []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return []string{err.Error()}
	}
	return evt02ValidateValue("$", schema, doc)
}

var (
	evt02UUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func evt02ValidateValue(path string, schema map[string]any, v any) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, path+": "+fmt.Sprintf(format, args...)) }
	obj, isObj := v.(map[string]any)
	for kw, rule := range schema {
		switch kw {
		case "$schema", "$id", "title", "description":
		case "type":
			ok := false
			switch rule {
			case "object":
				ok = isObj
			case "string":
				_, ok = v.(string)
			case "integer":
				if n, isNum := v.(json.Number); isNum {
					_, err := n.Int64()
					ok = err == nil
				}
			default:
				add("unsupported type %v", rule)
				continue
			}
			if !ok {
				add("%v is not of type %v", v, rule)
			}
		case "required":
			for _, name := range rule.([]any) {
				if _, present := obj[name.(string)]; isObj && !present {
					add("required property %v missing", name)
				}
			}
		case "additionalProperties":
			props, _ := schema["properties"].(map[string]any)
			if rule == false && isObj {
				for name := range obj {
					if _, declared := props[name]; !declared {
						add("property %s not allowed", name)
					}
				}
			} else if rule != false {
				add("unsupported additionalProperties %v", rule)
			}
		case "properties":
			if !isObj {
				continue
			}
			for name, sub := range rule.(map[string]any) {
				if value, present := obj[name]; present {
					problems = append(problems, evt02ValidateValue(path+"."+name, sub.(map[string]any), value)...)
				}
			}
		case "dependentRequired":
			if !isObj {
				continue
			}
			for name, deps := range rule.(map[string]any) {
				if _, present := obj[name]; !present {
					continue
				}
				for _, dep := range deps.([]any) {
					if _, ok := obj[dep.(string)]; !ok {
						add("%s requires %v", name, dep)
					}
				}
			}
		case "enum":
			found := false
			for _, allowed := range rule.([]any) {
				if allowed == v {
					found = true
				}
			}
			if !found {
				add("%v not in the enum", v)
			}
		case "minimum":
			n, isNum := v.(json.Number)
			minimum, _ := rule.(float64)
			if f, err := n.Float64(); !isNum || err != nil || f < minimum {
				add("%v below the minimum %v", v, rule)
			}
		case "format":
			s, _ := v.(string)
			switch rule {
			case "uuid":
				if !evt02UUID.MatchString(s) {
					add("%q is no uuid", s)
				}
			case "date-time":
				if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
					add("%q is no date-time: %v", s, err)
				}
			default:
				add("unsupported format %v", rule)
			}
		default:
			add("unsupported schema keyword %s", kw)
		}
	}
	return problems
}
