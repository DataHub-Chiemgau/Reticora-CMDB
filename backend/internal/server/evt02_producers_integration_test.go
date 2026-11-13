// Producer evidence of EVT-02 and NTF-05 (WP-551; audit WP-136, WP-140).
//
//   - TestEveryEVT02TypeHasAProducer – EVT-01, EVT-02, NTF-05: for every
//     type of events.Types (the 16 of EVT-02, the NTF-05 types
//     entitlement.expired, invitation.created, webhook.dead and
//     certificate.expiring, and apikey.creator_deactivated of TLC-04/E-64;
//     of phase 2 stock.below_minimum, NTF-05 [P2], WP-285)
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
//     compared with events.Types: a type without a frame in
//     events.Payloads needs exactly one producer case; a phase 2 type
//     (EVT-03, WP-246) may have one until its producing work package adds
//     it; a case of an unknown type fails (evt02CoverageProblems,
//     TestEVT02ProducerCoverageRule without a database).
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
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
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
	// callMFA is call with a session opened with a second factor (the
	// deletion of the organization, WP-1152).
	callMFA := func(t *testing.T, as t6As, method, path, body string) t6Response {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: as.User, OrganizationID: as.Org, Type: identity.PrincipalTypeUser, MFA: true,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: as.Perms, Credential: "evt02-mfa"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, req.WithContext(ctx))
		return t6Response{Code: w.Code, Body: w.Body.Bytes()}
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
		statusChanged                      string
		deletedState                       string
		survivor, victim                   string
		relSource, relTarget, removedEdge  string
		enrollCode                         string
		silentCollector                    string
		reviewItem                         string
		exportJob                          string
		orgExportJob                       string
		failingJob                         *jobs.Job
		jobService                         *jobs.Service
		warned, expired                    string
		webhookWorkflow                    string
		runWorkflow, failingWorkflow       string
		approvalWorkflow                   string
		rotatedCredential, agedCredential  string
		usageCredential, usageCollector    string
		usageScope, usageCI                string
		stateAddress, stateSubnet          string
		findingCI, changedFinding          string
		asnID                              string
		resolvedFinding                    string
		admpAccount                        string
		probeGroup                         string
		stateCollector, stateProbe         string
		admpSync                           *admp.Sync
		alarms                             = alarm.NewPGService(f.App)
		ackAlarm, sevAlarm, resAlarm       *alarm.Alarm
		updIncident                        *alarm.Incident
		maintWindow, maintCI               string
		allocParent                        string
		hookServer                         *httptest.Server
		hookSubscription, hookDelivery     string
		hookDispatcher                     *webhook.Dispatcher
		certCollector                      string
		expiringContract, expiringAsset    string
		creator                            string
		stockBin, stockItem                string
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
		// A status change is its own event next to ci.changed (EVT-03,
		// WP-707); its data follow the $def ci.status_changed.
		{typ: events.CIStatusChanged,
			setup: func(t *testing.T) { statusChanged = f.CI(t, f.OrgA, "", "evt02-status-changed") },
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPatch, "/api/v1/cis/"+statusChanged, `{"status":"maintenance","manufacturer":"evt02"}`)
				answered(t, "PATCH /cis/{id}", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "CI status", ciStatus(t, statusChanged), "active")
				}
				return statusChanged
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				fields, _ := data["changed_fields"].([]any)
				if data["id"] != entity || data["from"] != "active" || data["to"] != "maintenance" || data["status"] != "maintenance" ||
					!slices.Contains(fields, any("status")) || !slices.Contains(fields, any("manufacturer")) {
					t.Errorf("ci.status_changed data: id %v, from %v, to %v, status %v, changed_fields %v; want %s, active, maintenance, maintenance, [status manufacturer]",
						data["id"], data["from"], data["to"], data["status"], data["changed_fields"], entity)
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
		{typ: events.CredentialRotated,
			// PUT /credentials/{id}/secret replaces the secret (WP-889).
			setup: func(t *testing.T) {
				r := call(t, admin, http.MethodPost, "/api/v1/credentials",
					`{"name":"evt02-cred","kind":"ssh_password","secret":{"username":"u","password":"evt02-old"}}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("create credential: %d %s", r.Code, t6Short(r.Body))
				}
				var c struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(r.Body, &c); err != nil {
					t.Fatal(err)
				}
				rotatedCredential = c.ID
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPut, "/api/v1/credentials/"+rotatedCredential+"/secret",
					`{"secret":{"username":"u","password":"evt02-new"}}`)
				answered(t, "PUT /credentials/{id}/secret", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "secret_version", scalar(t, `SELECT secret_version::text FROM credential WHERE id = $1`, rotatedCredential), "1")
				}
				return rotatedCredential
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["source"] != "stored" || data["secret_version"] != float64(2) ||
					data["kind"] != "ssh_password" {
					t.Errorf("credential.rotated data %v", data)
				}
				for _, v := range data {
					if s, ok := v.(string); ok && strings.Contains(s, "evt02-") {
						t.Errorf("credential.rotated carries a secret value: %v", data)
					}
				}
			}},
		{typ: events.CredentialRotationDue,
			// credential.age_check (WP-897): a stored secret beyond its
			// max_age_days, once per secret_version.
			setup: func(t *testing.T) {
				r := call(t, admin, http.MethodPost, "/api/v1/credentials",
					`{"name":"evt02-aged","kind":"ssh_password","max_age_days":1,"secret":{"username":"u","password":"evt02-aged"}}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("create credential: %d %s", r.Code, t6Short(r.Body))
				}
				var c struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(r.Body, &c); err != nil {
					t.Fatal(err)
				}
				agedCredential = c.ID
				exec(t, `UPDATE credential SET created_at = now() - interval '3 days' WHERE id = $1`, agedCredential)
			},
			act: func(t *testing.T, refused bool) string {
				_, err := credential.NewAgeCheck(f.App).Run(bg)
				failed(t, "credential age check", err, refused)
				if refused {
					unchanged(t, "notices sent", noticesSent(t, events.CredentialRotationDue, "credential/"+agedCredential), "0")
				}
				return agedCredential
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["credential_id"] != entity || data["max_age_days"] != float64(1) || data["secret_version"] != float64(1) ||
					data["source"] != "stored" || data["due_at"] == nil {
					t.Errorf("credential.rotation_due data %v", data)
				}
				for _, v := range data {
					if s, ok := v.(string); ok && strings.Contains(s, "evt02-") {
						t.Errorf("credential.rotation_due carries a secret value: %v", data)
					}
				}
			}},
		{typ: events.CredentialResolutionFailed,
			// A scope run reports an auth_failed use of a credential of the
			// collector's bundle at a CI (WP-898).
			setup: func(t *testing.T) {
				r := call(t, admin, http.MethodPost, "/api/v1/credentials",
					`{"name":"evt02-usage","kind":"ssh_password","secret":{"username":"u","password":"evt02-usage-secret"}}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("create credential: %d %s", r.Code, t6Short(r.Body))
				}
				usageCredential = t6ID(r.Body)
				usageCollector = scalar(t, `INSERT INTO collector (organization_id, name, status) VALUES ($1, 'evt02-usage', 'active')
					RETURNING id::text`, f.OrgA)
				usageScope = scalar(t, `INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, credential_ids)
					VALUES ($1, $2, 'evt02-usage', 'host', '10.97.0.1', ARRAY[$3]::uuid[]) RETURNING id::text`,
					f.OrgA, usageCollector, usageCredential)
				usageCI = f.CI(t, f.OrgA, "", "evt02-usage-ci")
				exec(t, `UPDATE ci SET management_ip = '10.97.0.1' WHERE id = $1`, usageCI)
			},
			act: func(t *testing.T, refused bool) string {
				h := discovery.NewHandler(discovery.NewPGRepository(f.App), nil)
				err := h.RecordCollectorScopeRun(f.OrgCtx(f.OrgA), f.OrgA, usageCollector, &discovery.ScopeResultReport{
					ScopeID: usageScope, At: time.Now(), Result: map[string]any{"records": 1},
					CredentialUsage: []credential.UsageReport{{Target: "10.97.0.1", Namespace: "ssh", CredentialID: usageCredential,
						Cause: credential.UsageAuthFailed}}})
				failed(t, "scope run with credential usage", err, refused)
				if refused {
					unchanged(t, "credential usage", count(t, `credential_usage WHERE credential_id = $1`, usageCredential), "0")
				}
				return usageCredential
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["credential_id"] != entity || data["ci_id"] != usageCI || data["namespace"] != "ssh" ||
					data["error_class"] != "auth_failed" || data["kind"] != "ssh_password" {
					t.Errorf("credential.resolution_failed data %v", data)
				}
				for _, v := range data {
					if s, ok := v.(string); ok && strings.Contains(s, "evt02-usage-secret") {
						t.Errorf("credential.resolution_failed carries a secret value: %v", data)
					}
				}
			}},
		{typ: events.ProbeConfigChanged,
			// POST /monitoring/probes (WP-939): a new probe is a change of
			// the probe configuration (E-161), in the write transaction.
			setup: func(t *testing.T) {
				probeGroup = scalar(t, `INSERT INTO collector_group (organization_id, client_id, name)
					VALUES ($1, $2, 'evt02-probes') RETURNING id::text`, f.OrgA, f.Client1)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/monitoring/probes",
					`{"type":"icmp","target":"10.250.0.1","client_id":"`+f.Client1+`","collector_group_id":"`+probeGroup+`"}`)
				answered(t, "POST /monitoring/probes", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "probes", count(t, `probe WHERE organization_id = $1`, f.OrgA), "0")
					return ""
				}
				var p struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(r.Body, &p); err != nil {
					t.Fatal(err)
				}
				return p.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				fields, _ := data["changed_fields"].([]any)
				if data["id"] != entity || data["client_id"] != f.Client1 || len(fields) == 0 {
					t.Errorf("config.changed data %v", data)
				}
			}},
		{typ: events.ProbeStateChanged,
			// The worker probe-state applies a state change of the assigned
			// collector (WP-961).
			setup: func(t *testing.T) {
				group := scalar(t, `INSERT INTO collector_group (organization_id, name) VALUES ($1, 'evt02-state') RETURNING id::text`, f.OrgA)
				stateCollector = scalar(t, `INSERT INTO collector (organization_id, name, status) VALUES ($1, 'evt02-state', 'active')
					RETURNING id::text`, f.OrgA)
				exec(t, `UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, group, stateCollector)
				stateProbe = scalar(t, `INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
					VALUES ($1, $2, $3, 'icmp', '10.250.2.1') RETURNING id::text`, f.OrgA, f.Client1, group)
				exec(t, `INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1)`,
					stateProbe, f.OrgA, stateCollector)
			},
			act: func(t *testing.T, refused bool) string {
				a := probe.NewStateApplier(nil, probe.StreamConfig{}, f.App)
				err := a.Apply(context.Background(), f.OrgA, stateCollector, &probewire.StateChange{ProbeID: stateProbe, Epoch: 1, Seq: 1,
					From: probewire.StateUnknown, To: probewire.StateDown, Reason: "timeout", ObservedAt: time.Now()}, time.Now())
				failed(t, "probe state change", err, refused)
				if refused {
					unchanged(t, "probe state", count(t, `probe_state WHERE probe_id = $1`, stateProbe), "0")
					return ""
				}
				return stateProbe
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["probe_id"] != entity || data["client_id"] != f.Client1 || data["from"] != "unknown" ||
					data["to"] != "down" || data["collector_id"] != stateCollector || data["late"] != false || data["notify"] != true {
					t.Errorf("probe.state_changed data %v", data)
				}
			}},
		{typ: events.MonitoringSuspensionChanged,
			// The reconciliation probe.entitlement_reconcile (WP-942): a
			// snmp_status probe without monitoring_network is paused.
			setup: func(t *testing.T) {
				group := scalar(t, `INSERT INTO collector_group (organization_id, client_id, name)
					VALUES ($1, $2, 'evt02-suspend') RETURNING id::text`, f.OrgA, f.Client1)
				exec(t, `INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
					VALUES ($1, $2, $3, 'snmp_status', '10.250.1.1')`, f.OrgA, f.Client1, group)
			},
			act: func(t *testing.T, refused bool) string {
				s := probe.NewSuspender(f.App, func(_ context.Context, _, feature string) (bool, error) {
					return feature == entitlement.FeatureMonitoring, nil
				})
				_, err := s.RunOrg(context.Background(), f.OrgA)
				failed(t, "probe suspension", err, refused)
				if refused {
					unchanged(t, "suspended probes", count(t, `probe WHERE organization_id = $1 AND suspended_reason IS NOT NULL`, f.OrgA), "0")
					return ""
				}
				return f.OrgA
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				types, _ := data["probe_types"].([]any)
				if entity != f.OrgA || data["paused"] != float64(1) || data["resumed"] != float64(0) || data["suspended"] != true ||
					data["feature_key"] != entitlement.FeatureMonitoringNetwork || len(types) != 1 || types[0] != "snmp_status" {
					t.Errorf("monitoring.suspension_changed data %v", data)
				}
			}},
		{typ: events.ADMPConnectionRefreshed,
			// The refresh admp.refresh of a connection against a fake
			// source (WP-929): a new connection is a change.
			setup: func(t *testing.T) {
				cred := f.ID()
				exec(t, `INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'evt02-admp', 'api_token', '\x00')`,
					cred, f.OrgA)
				admpAccount = scalar(t, `INSERT INTO admp_account (organization_id, client_id, name, base_url, credential_id)
					VALUES ($1, $2, 'evt02-admp', 'https://admp.example.com/api', $3) RETURNING id::text`, f.OrgA, f.Client1, cred)
				admpSync = admp.NewSync(&admp.SyncOptions{Accounts: e.Repos.ADMPAccounts, Cache: e.Repos.ADMP, Sources: evt02ADMP{},
					Pool: f.App})
			},
			act: func(t *testing.T, refused bool) string {
				res, err := admpSync.Refresh(f.OrgCtx(f.OrgA), f.OrgA, admp.RefreshParams{AccountID: admpAccount,
					Kind: admp.KindConnection, ExternalID: "evt02-line"})
				failed(t, "admp refresh", err, refused)
				if refused {
					unchanged(t, "admp connections", count(t, `admp_connection WHERE account_id = $1`, admpAccount), "0")
					return ""
				}
				if res.Result != "ok" {
					t.Fatalf("admp refresh result %+v", res)
				}
				return scalar(t, `SELECT id::text FROM admp_connection WHERE account_id = $1 AND external_id = 'evt02-line'`, admpAccount)
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["account_id"] != admpAccount || data["client_id"] != f.Client1 || data["stale"] != false {
					t.Errorf("admp.connection.refreshed data %v", data)
				}
				for _, v := range data {
					if s, ok := v.(string); ok && strings.Contains(s, "evt02 carrier") {
						t.Errorf("admp.connection.refreshed carries connection data: %v", data)
					}
				}
			}},
		{typ: events.IPAMAddressStateChanged,
			// PATCH /ips/{id} sets the state of an address (WP-902).
			setup: func(t *testing.T) {
				stateSubnet = scalar(t, `INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.255.55.0/24')
					RETURNING id::text`, f.OrgA, f.Client1)
				stateAddress = scalar(t, `INSERT INTO ip_address (organization_id, subnet_id, address) VALUES ($1, $2, '10.255.55.7')
					RETURNING id::text`, f.OrgA, stateSubnet)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPatch, "/api/v1/ips/"+stateAddress, `{"state":"active"}`)
				answered(t, "PATCH /ips/{id} state", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "state", scalar(t, `SELECT state FROM ip_address WHERE id = $1`, stateAddress), "unknown")
				}
				return stateAddress
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["subnet_id"] != stateSubnet || data["client_id"] != f.Client1 ||
					data["from"] != "unknown" || data["to"] != "active" {
					t.Errorf("ipam.address.state_changed data %v", data)
				}
			}},
		{typ: events.FindingCreated,
			// POST /security/findings records a manual finding through the
			// upsert (WP-1116).
			setup: func(t *testing.T) { findingCI = f.CI(t, f.OrgA, f.Client1, "evt02-finding-ci") },
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/security/findings",
					`{"ci_id":"`+findingCI+`","kind":"vulnerability","severity":"high","title":"evt02-finding","reference":"CVE-2024-5555"}`)
				answered(t, "POST /security/findings", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "findings named evt02-finding", count(t, `security_finding WHERE organization_id = $1 AND title = 'evt02-finding'`, f.OrgA), "0")
					return ""
				}
				return t6ID(r.Body)
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["ci_id"] != findingCI || data["client_id"] != f.Client1 || data["severity"] != "high" ||
					data["status"] != "open" || data["source"] != "manual" || data["cve_ids"] == nil {
					t.Errorf("finding.created data %v", data)
				}
			}},
		{typ: events.FindingChanged,
			// PATCH /security/findings/{id} acknowledges a finding (WP-1116).
			setup: func(t *testing.T) {
				changedFinding = scalar(t, `INSERT INTO security_finding (organization_id, ci_id, kind, title) VALUES ($1, $2, 'vulnerability', 'evt02-changed')
					RETURNING id::text`, f.OrgA, f.CI(t, f.OrgA, f.Client1, "evt02-finding-changed"))
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPatch, "/api/v1/security/findings/"+changedFinding, `{"status":"acknowledged"}`)
				answered(t, "PATCH /security/findings/{id} acknowledged", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "finding status", scalar(t, `SELECT status FROM security_finding WHERE id = $1`, changedFinding), "open")
				}
				return changedFinding
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["status"] != "acknowledged" || data["client_id"] != f.Client1 || data["source"] != "manual" {
					t.Errorf("finding.changed data %v", data)
				}
			}},
		{typ: events.FindingResolved,
			// PATCH /security/findings/{id} resolves a finding (WP-1116).
			setup: func(t *testing.T) {
				resolvedFinding = scalar(t, `INSERT INTO security_finding (organization_id, ci_id, kind, title) VALUES ($1, $2, 'missing_patch', 'evt02-resolved')
					RETURNING id::text`, f.OrgA, f.CI(t, f.OrgA, f.Client1, "evt02-finding-resolved"))
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPatch, "/api/v1/security/findings/"+resolvedFinding, `{"status":"resolved"}`)
				answered(t, "PATCH /security/findings/{id} resolved", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "finding status", scalar(t, `SELECT status FROM security_finding WHERE id = $1`, resolvedFinding), "open")
				}
				return resolvedFinding
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["status"] != "resolved" || data["severity"] != "medium" {
					t.Errorf("finding.resolved data %v", data)
				}
			}},
		{typ: events.ASNChanged,
			// POST /asns creates an AS number of a client (WP-1066).
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/asns",
					`{"number":"AS4200000055","name":"evt02-asn","kind":"customer","client_id":"`+f.Client1+`"}`)
				answered(t, "POST /asns", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "AS numbers named evt02-asn", count(t, `asn WHERE organization_id = $1 AND name = 'evt02-asn'`, f.OrgA), "0")
					return ""
				}
				asnID = t6ID(r.Body)
				return asnID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["asn"] != float64(4200000055) || data["number"] != float64(4200000055) ||
					data["client_id"] != f.Client1 || data["change"] != "create" {
					t.Errorf("asn.changed data %v", data)
				}
			}},
		{typ: events.IPAMSubnetChanged,
			// POST /subnets/{id}/allocate-subnet allocates a child (WP-904).
			setup: func(t *testing.T) {
				allocParent = scalar(t, `INSERT INTO subnet (organization_id, client_id, cidr, is_pool) VALUES ($1, $2, '10.255.56.0/24', true)
					RETURNING id::text`, f.OrgA, f.Client1)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/subnets/"+allocParent+"/allocate-subnet", `{"prefix_length":26}`)
				answered(t, "POST /subnets/{id}/allocate-subnet", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "allocated subnets", count(t, `subnet WHERE parent_id = $1`, allocParent), "0")
					return ""
				}
				return t6ID(r.Body)
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["cidr"] != "10.255.56.0/26" || data["client_id"] != f.Client1 ||
					data["change_kind"] != "allocated" {
					t.Errorf("ipam.subnet.changed data %v", data)
				}
			}},
		{typ: events.EntitlementChanged,
			// The operator's plan change (WP-882): essential → standard.
			setup: func(t *testing.T) {
				exec(t, `UPDATE organization SET plan = 'essential' WHERE id = $1`, f.OrgA)
			},
			act: func(t *testing.T, refused bool) string {
				const token = "evt02-break-glass-token-with-32-characters"
				svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
				mux := chi.NewRouter()
				operator.NewHandler(operator.Config{BreakGlassToken: token}, nil, nil, operator.NewAuditor(f.App), f.App).
					WithEntitlements(svc).RegisterRoutes(mux)
				req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/orgs/"+f.OrgA+"/plan",
					strings.NewReader(`{"plan":"standard","reason":"evt02"}`))
				req.Header.Set(operator.BreakGlassHeader, token)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				answered(t, "PUT /admin/orgs/{id}/plan", t6Response{Code: w.Code, Body: w.Body.Bytes()}, refused, http.StatusOK)
				if refused {
					unchanged(t, "plan", scalar(t, `SELECT plan FROM organization WHERE id = $1`, f.OrgA), "essential")
				}
				return f.OrgA
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["plan"] != "standard" || data["plan_before"] != "essential" {
					t.Errorf("entitlement.changed data %v, want essential → standard of the organization", data)
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
				rec, err := e.Repos.WebhookDeliveries.Enqueue(f.OrgCtx(f.OrgA), &webhook.DeliveryRecord{OrganizationID: f.OrgA,
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
		// The expiry.check run (WP-270) reports a contract and a warranty
		// five days ahead at the stage of 7 days.
		{typ: events.ContractExpiring,
			setup: func(t *testing.T) {
				expiringContract = scalar(t, `INSERT INTO contract (organization_id, number, end_date, currency)
					VALUES ($1, 'evt02-contract', (now() + interval '5 days')::date, 'EUR') RETURNING id::text`, f.OrgA)
			},
			act: func(t *testing.T, refused bool) string {
				_, err := jobs.NewExpiryCheck(f.App).Run(bg)
				failed(t, "expiry check", err, refused)
				if refused {
					unchanged(t, "notices sent", noticesSent(t, events.ContractExpiring, expiringContract), "0")
				}
				return expiringContract
			},
			data: sameID("id")},
		{typ: events.AssetWarrantyExpiring,
			setup: func(t *testing.T) {
				expiringAsset = scalar(t, `INSERT INTO asset (organization_id, inventory_no, name, warranty_until)
					VALUES ($1, 'evt02-warranty', 'evt02-warranty', (now() + interval '5 days')::date) RETURNING id::text`, f.OrgA)
			},
			act: func(t *testing.T, refused bool) string {
				_, err := jobs.NewExpiryCheck(f.App).Run(bg)
				failed(t, "expiry check", err, refused)
				if refused {
					unchanged(t, "notices sent", noticesSent(t, events.AssetWarrantyExpiring, expiringAsset), "0")
				}
				return expiringAsset
			},
			data: sameID("id")},
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
		// A retrieval through POST /stock-movements takes the total stock
		// of a consumable from 6 below its min_level 5 (WP-285); refused,
		// neither movement nor on_hand_total remain.
		{typ: events.StockBelowMinimum,
			setup: func(t *testing.T) {
				parent := ""
				for _, kind := range []string{"site", "warehouse", "zone", "shelf", "bin"} {
					body := fmt.Sprintf(`{"kind":%q,"name":"evt02-%s","parent_id":%q}`, kind, kind, parent)
					if kind == "site" {
						body = `{"kind":"site","name":"evt02-site","client_id":"` + f.Client1 + `"}`
					}
					r := call(t, admin, http.MethodPost, "/api/v1/locations", body)
					if r.Code != http.StatusCreated {
						t.Fatalf("POST /locations %s = %d %s", kind, r.Code, t6Short(r.Body))
					}
					parent = t6ID(r.Body)
				}
				stockBin = parent
				r := call(t, admin, http.MethodPost, "/api/v1/consumables", `{"name":"evt02-toner","sku":"evt02-toner","min_level":5}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("POST /consumables = %d %s", r.Code, t6Short(r.Body))
				}
				stockItem = t6ID(r.Body)
				r = call(t, admin, http.MethodPost, "/api/v1/stock-movements",
					`{"movement_type":"receipt","consumable_id":"`+stockItem+`","to_location_id":"`+stockBin+`","quantity":6}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("receipt = %d %s", r.Code, t6Short(r.Body))
				}
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/stock-movements",
					`{"movement_type":"retrieval","consumable_id":"`+stockItem+`","from_location_id":"`+stockBin+`","quantity":2}`)
				answered(t, "POST /stock-movements", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "on_hand_total", scalar(t, `SELECT on_hand_total::text FROM consumable WHERE id = $1`, stockItem), "6.00")
					unchanged(t, "movements", count(t, `stock_movement WHERE consumable_id = $1`, stockItem), "1")
				}
				return stockItem
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				t.Helper()
				if data["id"] != entity || data["on_hand_total"] != 4.0 || data["min_level"] != 5.0 || data["sku"] != "evt02-toner" {
					t.Errorf("data %v, want id %s, on_hand_total 4, min_level 5 and sku evt02-toner", data, entity)
				}
			}},
		{typ: events.OrgDeletionRequested,
			// POST /org/deletion (WP-1152): an org_admin with a second
			// factor requests the deletion of the organization.
			act: func(t *testing.T, refused bool) string {
				r := callMFA(t, admin, http.MethodPost, "/api/v1/org/deletion", `{"confirm_slug":"scopetest-55-a"}`)
				answered(t, "POST /org/deletion", r, refused, http.StatusAccepted)
				if refused {
					unchanged(t, "deletion_state", scalar(t, `SELECT deletion_state FROM organization WHERE id = $1`, f.OrgA), "none")
				}
				return f.OrgA
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["requested_by"] != admin.User || data["due_at"] == nil || data["cancelled_by"] != nil {
					t.Errorf("org.deletion_requested data %v", data)
				}
			}},
		{typ: events.OrgExportCompleted,
			// POST /org/exports (WP-1151) queues the full export of an
			// org_admin with a second factor; its job succeeding writes
			// org.export_completed in the transaction of the change.
			setup: func(t *testing.T) {
				r := callMFA(t, admin, http.MethodPost, "/api/v1/org/exports", "")
				if r.Code != http.StatusAccepted {
					t.Fatalf("setup: POST /org/exports = %d %s", r.Code, t6Short(r.Body))
				}
				orgExportJob = t6ID(r.Body)
				if _, err := jobs.NewService(e.Repos.Jobs).ClaimJob(bg, f.OrgA, orgExportJob); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, refused bool) string {
				manifest := &export.OrgExportManifest{OrganizationID: f.OrgA, JobID: orgExportJob, Format: "jsonl",
					Tables: []export.OrgExportTable{{Name: "ci", Rows: 3, Parts: []export.OrgExportPart{{Key: "k", Bytes: 120}}}}}
				_, err := jobs.NewService(e.Repos.Jobs).Succeed(bg, f.OrgA, orgExportJob, manifest)
				failed(t, "complete the full export", err, refused)
				if refused {
					unchanged(t, "job", scalar(t, `SELECT status FROM job WHERE id = $1`, orgExportJob), string(jobs.StatusRunning))
				}
				return orgExportJob
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["initiated_by"] != admin.User || data["rows"] != float64(3) ||
					data["bytes"] != float64(120) || data["expires_at"] == nil {
					t.Errorf("org.export_completed data %v", data)
				}
			}},
		{typ: events.OrgDeletionCancelled,
			// DELETE /org/deletion (WP-1152) withdraws the request of the
			// case before during the grace period.
			act: func(t *testing.T, refused bool) string {
				r := callMFA(t, admin, http.MethodDelete, "/api/v1/org/deletion", "")
				answered(t, "DELETE /org/deletion", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "deletion_state", scalar(t, `SELECT deletion_state FROM organization WHERE id = $1`, f.OrgA), "requested")
				}
				return f.OrgA
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["requested_by"] != admin.User || data["cancelled_by"] != admin.User || data["due_at"] == nil {
					t.Errorf("org.deletion_cancelled data %v", data)
				}
			}},
		{typ: events.MaintenanceScheduled,
			// The announcement of a maintenance window to its affected
			// client (WP-1034): one notification and one event per client.
			setup: func(t *testing.T) {
				maintCI = f.CI(t, f.OrgA, f.Client1, "evt02-maint-ci")
				r := call(t, admin, http.MethodPost, "/api/v1/maintenance-windows",
					`{"title":"evt02 maintenance","starts_at":"2030-01-01T22:00:00Z","ends_at":"2030-01-02T02:00:00Z","ci_ids":["`+maintCI+`"]}`)
				if r.Code != http.StatusCreated {
					t.Fatalf("create window: %d %s", r.Code, t6Short(r.Body))
				}
				maintWindow = t6ID(r.Body)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/maintenance-windows/"+maintWindow+"/notify", `{"contact_roles":["technical"]}`)
				answered(t, "POST /maintenance-windows/{id}/notify", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "maintenance notifications", count(t, `maintenance_notification WHERE maintenance_window_id = $1`, maintWindow), "0")
				}
				return maintWindow
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["client_id"] != f.Client1 || data["maintenance_notification_id"] == nil ||
					data["affected_cis"] != 1.0 || data["starts_at"] != "2030-01-01T22:00:00Z" {
					t.Errorf("maintenance.scheduled data %v", data)
				}
			}},
		{typ: events.AlarmOpened,
			// The lifecycle opens a new alarm of a rule series (WP-982).
			act: func(t *testing.T, refused bool) string {
				a, err := alarms.Raise(f.OrgCtx(f.OrgA), f.OrgA, evt02AlarmInput("evt02-opened", alarm.SeverityWarning))
				failed(t, "raise alarm", err, refused)
				if refused {
					unchanged(t, "alarms", count(t, `alarm WHERE organization_id = $1 AND series_key = 'evt02-opened'`, f.OrgA), "0")
					return ""
				}
				return a.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				want := alarm.DedupKey(alarm.SourceRule, evt02AlarmSource, "evt02-opened")
				if data["id"] != entity || data["alarm_id"] != entity || data["severity"] != "warning" || data["dedup_key"] != want {
					t.Errorf("alarm.opened data %v", data)
				}
			}},
		{typ: events.AlarmAcknowledged,
			setup: func(t *testing.T) {
				var err error
				if ackAlarm, err = alarms.Raise(f.OrgCtx(f.OrgA), f.OrgA, evt02AlarmInput("evt02-ack", alarm.SeverityWarning)); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, refused bool) string {
				_, err := alarms.Acknowledge(f.OrgCtx(f.OrgA), f.OrgA, ackAlarm.ID, f.User, "evt02")
				failed(t, "acknowledge alarm", err, refused)
				if refused {
					unchanged(t, "alarm state", scalar(t, `SELECT state FROM alarm WHERE id = $1`, ackAlarm.ID), "open")
				}
				return ackAlarm.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["alarm_id"] != entity || data["dedup_key"] != ackAlarm.DedupKey {
					t.Errorf("alarm.acknowledged data %v", data)
				}
			}},
		{typ: events.AlarmSeverityChanged,
			setup: func(t *testing.T) {
				var err error
				if sevAlarm, err = alarms.Raise(f.OrgCtx(f.OrgA), f.OrgA, evt02AlarmInput("evt02-sev", alarm.SeverityWarning)); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, refused bool) string {
				_, err := alarms.Raise(f.OrgCtx(f.OrgA), f.OrgA, evt02AlarmInput("evt02-sev", alarm.SeverityCritical))
				failed(t, "raise alarm severity", err, refused)
				if refused {
					unchanged(t, "alarm severity", scalar(t, `SELECT severity FROM alarm WHERE id = $1`, sevAlarm.ID), "warning")
				}
				return sevAlarm.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["alarm_id"] != entity || data["severity"] != "critical" ||
					data["dedup_key"] != sevAlarm.DedupKey {
					t.Errorf("alarm.severity_changed data %v", data)
				}
			}},
		{typ: events.AlarmResolved,
			setup: func(t *testing.T) {
				var err error
				if resAlarm, err = alarms.Raise(f.OrgCtx(f.OrgA), f.OrgA, evt02AlarmInput("evt02-res", alarm.SeverityWarning)); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, refused bool) string {
				_, err := alarms.Clear(f.OrgCtx(f.OrgA), f.OrgA, resAlarm.DedupKey, time.Time{}, "evt02")
				failed(t, "clear alarm", err, refused)
				if refused {
					unchanged(t, "alarm state", scalar(t, `SELECT state FROM alarm WHERE id = $1`, resAlarm.ID), "open")
				}
				return resAlarm.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["alarm_id"] != entity || data["severity"] != "warning" ||
					data["dedup_key"] != resAlarm.DedupKey {
					t.Errorf("alarm.resolved data %v", data)
				}
			}},
		// Acknowledging an incident over the REST route (WP-1036): the
		// incident and its root alarm in one transaction with
		// incident.updated.
		{typ: events.IncidentUpdated,
			setup: func(t *testing.T) {
				root, err := alarms.Raise(f.OrgCtx(f.OrgA), f.OrgA, evt02AlarmInput("evt02-incident", alarm.SeverityCritical))
				if err != nil {
					t.Fatal(err)
				}
				store := alarms.Store()
				if updIncident, _, err = store.UpsertIncident(f.OrgCtx(f.OrgA), f.OrgA, &alarm.IncidentInput{
					CauseClass: alarm.CauseTopology}); err != nil {
					t.Fatal(err)
				}
				if _, err = store.AttachAlarms(f.OrgCtx(f.OrgA), f.OrgA, updIncident.ID, []string{root.ID}, alarm.RoleRoot); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/monitoring/incidents/"+updIncident.ID+"/ack", `{}`)
				answered(t, "acknowledge incident", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "incident state", scalar(t, `SELECT state FROM incident WHERE id = $1`, updIncident.ID), "open")
				}
				return updIncident.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				if data["id"] != entity || data["incident_id"] != entity || data["change"] != "acknowledged" ||
					data["severity"] != "critical" || data["actor_id"] != admin.User {
					t.Errorf("incident.updated data %v", data)
				}
			}},
		// The workflow action send_webhook (WP-1096): the event is written
		// with the succeeded step; while the outbox refuses it the run
		// reports the failed step, which stays without its event.
		{typ: events.WorkflowWebhook,
			setup: func(t *testing.T) {
				r := call(t, admin, http.MethodPost, "/api/v1/workflows", `{"name": "evt02-webhook", "trigger": {"type": "manual"},
					"actions": [{"type": "send_webhook", "topic": "evt02.webhook", "payload": {"host": "${data.host}"}}]}`)
				answered(t, "create workflow", r, false, http.StatusCreated)
				var def struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(r.Body, &def); err != nil || def.ID == "" {
					t.Fatalf("workflow %s: %v", t6Short(r.Body), err)
				}
				webhookWorkflow = def.ID
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/workflows/"+webhookWorkflow+"/runs", `{"context": {"data": {"host": "srv-evt02"}}}`)
				answered(t, "run workflow", r, false, http.StatusCreated)
				var run struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Steps  []struct {
						Status string `json:"status"`
					} `json:"steps"`
				}
				if err := json.Unmarshal(r.Body, &run); err != nil || run.ID == "" || len(run.Steps) != 1 {
					t.Fatalf("run %s: %v", t6Short(r.Body), err)
				}
				if refused {
					unchanged(t, "webhook step", run.Status+"/"+run.Steps[0].Status, "failed/failed")
					return ""
				}
				if run.Status != "succeeded" {
					t.Fatalf("run %s, want succeeded", t6Short(r.Body))
				}
				return run.ID
			},
			data: func(t *testing.T, entity string, data map[string]any) {
				payload, _ := data["payload"].(map[string]any)
				if data["id"] != entity || data["run_id"] != entity || data["workflow_id"] != webhookWorkflow || data["topic"] != "evt02.webhook" ||
					data["step_index"] != 0.0 || payload["host"] != "srv-evt02" {
					t.Errorf("workflow.webhook data %v", data)
				}
			}},
		// The status changes of a workflow run (WP-1092): the run of the
		// router executes in the request (no job queue); each change writes
		// its event in its transaction. While the outbox refuses the event
		// the change is rolled back and the request fails.
		{typ: events.WorkflowRunStarted,
			setup: func(t *testing.T) {
				runWorkflow = evt02Workflow(t, call, admin, "evt02-run", `[{"type": "noop"}]`)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/workflows/"+runWorkflow+"/runs", `{"context": {}}`)
				answered(t, "run workflow", r, refused, http.StatusCreated)
				if refused {
					// The run could not start: it failed without a step.
					unchanged(t, "run that could not start", scalar(t, `SELECT status || '/' || (SELECT count(*) FROM workflow_step s WHERE s.run_id = r.id)
						FROM workflow_run r WHERE workflow_id = $1 ORDER BY created_at DESC LIMIT 1`, runWorkflow), "failed/0")
					return ""
				}
				return evt02RunID(t, r, "succeeded")
			},
			data: evt02RunData(&runWorkflow, "running")},
		{typ: events.WorkflowRunSucceeded,
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/workflows/"+runWorkflow+"/runs", `{"context": {}}`)
				answered(t, "run workflow", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "run whose success was refused", scalar(t, `SELECT status FROM workflow_run WHERE workflow_id = $1
						ORDER BY created_at DESC LIMIT 1`, runWorkflow), "running")
					return ""
				}
				return evt02RunID(t, r, "succeeded")
			},
			data: evt02RunData(&runWorkflow, "succeeded")},
		{typ: events.WorkflowRunFailed,
			setup: func(t *testing.T) {
				// submit_form names no form: its step fails, so does the run.
				failingWorkflow = evt02Workflow(t, call, admin, "evt02-run-failed", `[{"type": "submit_form", "form_id": "none"}]`)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/workflows/"+failingWorkflow+"/runs", `{"context": {}}`)
				answered(t, "run workflow", r, refused, http.StatusCreated)
				if refused {
					unchanged(t, "run whose failure was refused", scalar(t, `SELECT status FROM workflow_run WHERE workflow_id = $1
						ORDER BY created_at DESC LIMIT 1`, failingWorkflow), "running")
					return ""
				}
				return evt02RunID(t, r, "failed")
			},
			data: evt02RunData(&failingWorkflow, "failed")},
		{typ: events.WorkflowRunCancelled,
			setup: func(t *testing.T) {
				approvalWorkflow = evt02Workflow(t, call, admin, "evt02-run-cancelled", `[{"type": "require_approval"}, {"type": "noop"}]`)
			},
			act: func(t *testing.T, refused bool) string {
				r := call(t, admin, http.MethodPost, "/api/v1/workflows/"+approvalWorkflow+"/runs", `{"context": {}}`)
				answered(t, "run workflow", r, false, http.StatusCreated)
				run := evt02RunID(t, r, "waiting_approval")
				r = call(t, admin, http.MethodPost, "/api/v1/workflow-runs/"+run+"/cancel", "")
				answered(t, "cancel run", r, refused, http.StatusOK)
				if refused {
					unchanged(t, "run whose cancel was refused", scalar(t, `SELECT status FROM workflow_run WHERE id = $1`, run), "waiting_approval")
					return ""
				}
				return run
			},
			data: evt02RunData(&approvalWorkflow, "cancelled")},
	}

	// The table covers every type of events.Types without a frame in
	// events.Payloads exactly once; a phase 2 type gets its case with its
	// producing work package (WP-246). A new type without a producer case
	// fails here.
	var covered []string
	for _, c := range cases {
		covered = append(covered, c.typ)
	}
	if problems := evt02CoverageProblems(covered); len(problems) != 0 {
		t.Fatalf("producer cases %v: %s", covered, strings.Join(problems, "; "))
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

// evt02Workflow stores a manual workflow with actions as admin and returns
// its id (WP-1092).
func evt02Workflow(t *testing.T, call func(t *testing.T, as t6As, method, path, body string) t6Response, admin t6As, name, actions string) string {
	t.Helper()
	r := call(t, admin, http.MethodPost, "/api/v1/workflows", `{"name": "`+name+`", "trigger": {"type": "manual"}, "actions": `+actions+`}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("create workflow %s = %d %s", name, r.Code, t6Short(r.Body))
	}
	var def struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(r.Body, &def); err != nil || def.ID == "" {
		t.Fatalf("workflow %s: %v", t6Short(r.Body), err)
	}
	return def.ID
}

// evt02RunID returns the id of the run of a response with status want.
func evt02RunID(t *testing.T, r t6Response, want string) string {
	t.Helper()
	var run struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(r.Body, &run); err != nil || run.ID == "" || run.Status != want {
		t.Fatalf("run %s (%v), want status %s", t6Short(r.Body), err, want)
	}
	return run.ID
}

// evt02RunData checks the data of a workflow.run_* event (WP-1092 AK4).
func evt02RunData(workflowID *string, status string) func(t *testing.T, entity string, data map[string]any) {
	return func(t *testing.T, entity string, data map[string]any) {
		t.Helper()
		if data["id"] != entity || data["run_id"] != entity || data["workflow_id"] != *workflowID || data["status"] != status ||
			data["trigger"] != "manual" || data["definition_version_id"] == nil {
			t.Errorf("workflow run data %v, want run %s of %s with status %s", data, entity, *workflowID, status)
		}
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
	return evt02ValidateValue(schema, "$", schema, doc)
}

var (
	evt02UUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// evt02HasType reports whether v is of the JSON type name.
func evt02HasType(name string, v any) (ok, known bool) {
	switch name {
	case "object":
		_, ok = v.(map[string]any)
	case "array":
		_, ok = v.([]any)
	case "string":
		_, ok = v.(string)
	case "number":
		_, ok = v.(json.Number)
	case "integer":
		if n, isNum := v.(json.Number); isNum {
			_, err := n.Int64()
			ok = err == nil
		}
	case "boolean":
		_, ok = v.(bool)
	case "null":
		ok = v == nil
	default:
		return false, false
	}
	return ok, true
}

// evt02ValidateValue validates v against schema; root resolves $ref
// (#/$defs/<name>).
func evt02ValidateValue(root map[string]any, path string, schema map[string]any, v any) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, path+": "+fmt.Sprintf(format, args...)) }
	obj, isObj := v.(map[string]any)
	for kw, rule := range schema {
		switch kw {
		case "$schema", "$id", "title", "description", "examples", "$defs":
		case "$ref":
			ref, _ := rule.(string)
			defs, _ := root["$defs"].(map[string]any)
			target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			if !strings.HasPrefix(ref, "#/$defs/") || !ok {
				add("unresolvable $ref %v", rule)
				continue
			}
			problems = append(problems, evt02ValidateValue(root, path, target, v)...)
		case "allOf":
			for _, sub := range rule.([]any) {
				problems = append(problems, evt02ValidateValue(root, path, sub.(map[string]any), v)...)
			}
		case "if":
			if len(evt02ValidateValue(root, path, rule.(map[string]any), v)) != 0 {
				continue
			}
			if then, ok := schema["then"].(map[string]any); ok {
				problems = append(problems, evt02ValidateValue(root, path, then, v)...)
			}
		case "then":
			// Evaluated with if.
		case "const":
			if rule != v {
				add("%v is not %v", v, rule)
			}
		case "type":
			names, isList := rule.([]any)
			if !isList {
				names = []any{rule}
			}
			matched := false
			for _, name := range names {
				ok, known := evt02HasType(fmt.Sprint(name), v)
				if !known {
					add("unsupported type %v", name)
				}
				matched = matched || ok
			}
			if !matched {
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
			switch rule {
			case false:
				if isObj {
					for name := range obj {
						if _, declared := props[name]; !declared {
							add("property %s not allowed", name)
						}
					}
				}
			case true:
			default:
				add("unsupported additionalProperties %v", rule)
			}
		case "properties":
			if !isObj {
				continue
			}
			for name, sub := range rule.(map[string]any) {
				if value, present := obj[name]; present {
					problems = append(problems, evt02ValidateValue(root, path+"."+name, sub.(map[string]any), value)...)
				}
			}
		case "items":
			items, _ := v.([]any)
			for i, item := range items {
				problems = append(problems, evt02ValidateValue(root, fmt.Sprintf("%s[%d]", path, i), rule.(map[string]any), item)...)
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
				// A number of the instance (json.Number) against a
				// number of the schema (float64), e.g. stage of WP-270.
				if n, isNum := v.(json.Number); isNum {
					if f, err := n.Float64(); err == nil && allowed == any(f) {
						found = true
					}
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
			s, isString := v.(string)
			if !isString {
				continue
			}
			switch rule {
			case "uuid":
				if !evt02UUID.MatchString(s) {
					add("%q is no uuid", s)
				}
			case "date-time":
				if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
					add("%q is no date-time: %v", s, err)
				}
			case "date":
				if _, err := time.Parse(time.DateOnly, s); err != nil {
					add("%q is no date: %v", s, err)
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

// plannedProducers names the work package of the Phase-3 plan that will
// produce a type of events.Phase3Types (WP-867, E-161). The producing work
// package adds the producer case and removes its entry; before WP-1177
// (p3-gate) the list is empty.
var plannedProducers = map[string]string{
	"alarm.escalated":             "WP-1146",
	"incident.opened":             "WP-1028",
	"incident.resolved":           "WP-1028",
	"collector_group.unreachable": "WP-964",
	"collector_group.reachable":   "WP-964",
	"collector_group.failover":    "WP-963",
	"collector.clock_skew":        "WP-962",
	"ci.config_changed":           "WP-1054",
	"capacity.forecast_exceeded":  "WP-1060",
	"sla.violated":                "WP-1049",
	"maintenance.started":         "WP-1033",
	"maintenance.ended":           "WP-1033",
	"workflow.approval_requested": "WP-1098",
	"workflow.approval_decided":   "WP-1098",
	"notification.escalated":      "WP-1146",
	"notification.acknowledged":   "WP-1146",
}

// evt02AlarmSource is the rule of the alarm cases.
const evt02AlarmSource = "11111111-1111-4111-8111-111111111111"

// evt02AlarmInput is a raise of a rule series of the alarm cases.
func evt02AlarmInput(series, severity string) *alarm.RaiseInput {
	return &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: evt02AlarmSource,
		SeriesKey: series, Severity: severity, Summary: "evt02 " + series}
}

// evt02ADMP is the fake ADMP of the admp.connection.refreshed case: one
// connection, every account's source.
type evt02ADMP struct{}

func (evt02ADMP) For(context.Context, *admp.Account) (admp.Source, error) { return evt02ADMP{}, nil }
func (evt02ADMP) CircuitOpen(*admp.Account) bool                          { return false }
func (evt02ADMP) FetchConnection(_ context.Context, id string) (*admp.ConnectionData, error) {
	return &admp.ConnectionData{ExternalID: id, Carrier: "evt02 carrier", ContractID: "V-evt02"}, nil
}
func (evt02ADMP) FetchService(context.Context, string) (*admp.ServiceData, error) {
	return nil, &admp.SourceError{Class: admp.ClassNotImplemented}
}
func (evt02ADMP) FetchServicePorts(context.Context, string) ([]admp.PortData, error) {
	return nil, &admp.SourceError{Class: admp.ClassNotImplemented}
}
func (evt02ADMP) FetchInventory(context.Context) ([]admp.InventoryPort, error) {
	return nil, &admp.SourceError{Class: admp.ClassNotImplemented}
}
func (evt02ADMP) FetchPortServices(context.Context, string) ([]string, error) {
	return nil, &admp.SourceError{Class: admp.ClassNotImplemented}
}
func (evt02ADMP) CheckSession(context.Context) error {
	return &admp.SourceError{Class: admp.ClassNotImplemented}
}

// evt02CoverageProblems checks the producer cases (their types) against
// the register (WP-246): every type of events.Types without a frame in
// events.Payloads has exactly one case; a phase 2 type (with a frame) may
// have one until its producing work package adds it; every case names a
// type of events.Types.
func evt02CoverageProblems(covered []string) []string {
	var problems []string
	count := map[string]int{}
	for _, typ := range covered {
		count[typ]++
		if count[typ] == 1 && !events.IsType(typ) {
			problems = append(problems, fmt.Sprintf("case for %q, which is no type of events.Types", typ))
		}
	}
	for _, typ := range events.Types {
		_, phase2 := events.Payloads[typ]
		_, planned := plannedProducers[typ]
		phase3 := slices.Contains(events.Phase3Types, typ)
		switch n := count[typ]; {
		case n > 1:
			problems = append(problems, fmt.Sprintf("%d cases for %s, want one", n, typ))
		case phase3 && n == 1 && planned:
			problems = append(problems, typ+" has a producer case and a planned producer")
		case phase3 && n == 0 && !planned:
			problems = append(problems, typ+" has neither a producer case nor a planned producer")
		case n == 0 && !phase2:
			problems = append(problems, typ+" has no producer case")
		}
	}
	for typ := range plannedProducers {
		if !slices.Contains(events.Phase3Types, typ) {
			problems = append(problems, fmt.Sprintf("planned producer for %q, which is no type of events.Phase3Types", typ))
		}
	}
	sort.Strings(problems)
	return problems
}

// TestEVT02ProducerCoverageRule covers EVT-02 and EVT-03 (WP-551,
// WP-246) without a database: the coverage rule of
// TestEveryEVT02TypeHasAProducer accepts the phase 2 types without a
// producer case, and one case for them, and fails a type of EVT-02 without
// a case, two cases of a type and a case of an unknown type. The schema
// validator accepts the example of every phase 2 type in an envelope with
// entity and refuses one without entity_seq and one whose data lacks a
// required field of the type's frame.
func TestEVT02ProducerCoverageRule(t *testing.T) {
	// base: one case per type that needs one, the phase-3 types whose
	// producer exists (no planned entry any more) among them.
	var base []string
	for _, typ := range events.Types {
		_, phase2 := events.Payloads[typ]
		_, planned := plannedProducers[typ]
		if !phase2 || (slices.Contains(events.Phase3Types, typ) && !planned) {
			base = append(base, typ)
		}
	}
	// A planned producer of an unknown type and a phase-3 type without case
	// and without plan fail as well (WP-867).
	plannedProducers["asset.exploded"] = "WP-999"
	if problems := evt02CoverageProblems(base); len(problems) != 1 {
		t.Errorf("unknown planned type: problems %v, want one", problems)
	}
	delete(plannedProducers, "asset.exploded")
	saved := plannedProducers[events.SLAViolated]
	delete(plannedProducers, events.SLAViolated)
	if problems := evt02CoverageProblems(base); len(problems) != 1 {
		t.Errorf("phase-3 type without case and plan: problems %v, want one", problems)
	}
	plannedProducers[events.SLAViolated] = saved
	for name, tc := range map[string]struct {
		cases []string
		fails bool
	}{
		"phase 2 without producer":       {base, false},
		"phase 2 with a producer":        {append(slices.Clone(base), events.StockMovement), false},
		"type of EVT-02 without case":    {base[1:], true},
		"two cases of a type":            {append(slices.Clone(base), events.CICreated), true},
		"two cases of a phase 2 type":    {append(slices.Clone(base), events.AssetCreated, events.AssetCreated), true},
		"unknown type":                   {append(slices.Clone(base), "asset.exploded"), true},
		"reserved type":                  {append(slices.Clone(base), "maintenance.started"), true},
		"phase 3 with a case and a plan": {append(slices.Clone(base), events.AlarmEscalated), true},
	} {
		if problems := evt02CoverageProblems(tc.cases); (len(problems) != 0) != tc.fails {
			t.Errorf("%s: problems %v, want failure %v", name, problems, tc.fails)
		}
	}

	schema := evt02LoadSchema(t)
	defs := schema["$defs"].(map[string]any)
	for typ := range events.Payloads {
		def := defs[typ].(map[string]any)
		example := def["examples"].([]any)[0].(map[string]any)
		envelope := map[string]any{"id": "11111111-1111-4111-8111-111111111111", "type": typ, "org_id": "22222222-2222-4222-8222-222222222222",
			"occurred_at": "2026-11-02T10:00:00Z", "sequence": 1, "entity_id": "e-1", "entity_seq": 1, "data": example}
		raw, _ := json.Marshal(envelope)
		if problems := evt02Validate(schema, raw); len(problems) != 0 {
			t.Errorf("%s: envelope with the example %s: %v", typ, raw, problems)
		}
		delete(envelope, "entity_id")
		delete(envelope, "entity_seq")
		raw, _ = json.Marshal(envelope)
		if problems := evt02Validate(schema, raw); len(problems) != 2 {
			t.Errorf("%s: envelope without entity gives %v, want 2 violations", typ, problems)
		}
		envelope["entity_id"], envelope["entity_seq"] = "e-1", 1
		envelope["data"] = map[string]any{"extra": true}
		raw, _ = json.Marshal(envelope)
		if problems := evt02Validate(schema, raw); len(problems) != len(def["required"].([]any)) {
			t.Errorf("%s: data without its required fields gives %v, want %d violations", typ, problems, len(def["required"].([]any)))
		}
	}
}
