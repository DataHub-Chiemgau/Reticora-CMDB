package events_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestEventsCommittedWithMutation covers EVT-01 (WP-550, F-319): the
// producers of export.completed, job.failed and webhook.dead write their
// event in the transaction of their status change. A fault in the outbox
// (a trigger refusing the event of one entity) stands for a failure or
// crash between the change and its event:
//   - with the fault the change is rolled back: the export job stays
//     running, the job stays running, the delivery is not dead (no dead
//     letter, subscription unchanged) and no event exists: never a change
//     without its event;
//   - the change is possible again afterwards: the export is resumed and
//     completed, the job failed again, the delivery retried and moved to
//     the dead-letter queue;
//   - then each producer has written exactly one event with the envelope
//     of its entity (organization, type, entity, sequence, entity_seq 1,
//     data), which organization B does not see.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEventsCommittedWithMutation(t *testing.T) {
	f := scopetest.Seed(t, "98")
	bg := context.Background()

	// The fault: an insert into the outbox of the entity named by the
	// trigger function fails; failOn("") lets every event through.
	fn := fmt.Sprintf("wp550_fail_event_%d", time.Now().UnixNano())
	failOn := func(t *testing.T, entityID string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `CREATE OR REPLACE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.organization_id = '`+f.OrgA+`' AND NEW.entity_id = '`+entityID+`' THEN
		RAISE EXCEPTION 'wp550 injected outbox failure';
	END IF;
	RETURN NEW;
END $$`); err != nil {
			t.Fatalf("trigger function: %v", err)
		}
	}
	failOn(t, "")
	t.Cleanup(func() {
		if _, err := f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`); err != nil {
			t.Errorf("drop trigger function: %v", err)
		}
	})
	if _, err := f.Admin.Exec(bg, `CREATE TRIGGER `+fn+` BEFORE INSERT ON event_outbox FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	// outboxOf reads the events of an entity as the application in the
	// scope of org.
	type row struct {
		org, typ, entity    string
		sequence, entitySeq int64
		data                map[string]any
	}
	outboxOf := func(t *testing.T, org, entityID string) []row {
		t.Helper()
		var out []row
		scope := database.OrgWideScope(org, "")
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT organization_id::text, type, entity_id, sequence, entity_seq, data
				FROM event_outbox WHERE entity_id = $1 ORDER BY sequence`, entityID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r row
				var raw []byte
				if err := rows.Scan(&r.org, &r.typ, &r.entity, &r.sequence, &r.entitySeq, &raw); err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &r.data); err != nil {
					return err
				}
				out = append(out, r)
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("read outbox: %v", err)
		}
		return out
	}
	// exactlyOne checks the single event of the entity and that
	// organization B does not see it; it returns the event's data.
	exactlyOne := func(t *testing.T, producer, typ, entityID string) map[string]any {
		t.Helper()
		got := outboxOf(t, f.OrgA, entityID)
		if len(got) != 1 {
			t.Fatalf("%s: %d events of %s, want exactly one: %+v", producer, len(got), entityID, got)
		}
		e := got[0]
		if e.org != f.OrgA || e.typ != typ || e.entity != entityID || e.sequence < 1 || e.entitySeq != 1 {
			t.Errorf("%s: envelope %+v, want organization %s, type %s, entity %s, a sequence and entity_seq 1",
				producer, e, f.OrgA, typ, entityID)
		}
		if other := outboxOf(t, f.OrgB, entityID); len(other) != 0 {
			t.Errorf("%s: organization B sees %d events of organization A", producer, len(other))
		}
		return e.data
	}
	scalar := func(t *testing.T, query string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return s
	}

	t.Run("export.completed", func(t *testing.T) {
		f.CI(t, f.OrgA, "", "wp550-export-ci")
		repo := export.NewPGJobRepository(f.App)
		worker := export.NewJobWorker(repo, ci.NewPGRepository(f.App), blob.NewFileStore(t.TempDir()))
		scope := database.OrgWideScope(f.OrgA, f.User)
		job := &export.Job{InitiatedBy: f.User, Format: "json", Scope: export.SnapshotScope(&scope)}
		if err := repo.CreateJob(f.OrgCtx(f.OrgA), f.OrgA, job); err != nil {
			t.Fatal(err)
		}
		status := func() string {
			return scalar(t, `SELECT status FROM export_job WHERE id = $1`, job.ID)
		}

		failOn(t, job.ID)
		for worker.ProcessOne(bg) {
		}
		if s, n := status(), len(outboxOf(t, f.OrgA, job.ID)); s != export.JobStatusRunning || n != 0 {
			t.Fatalf("export.completed lost: export job %s with %d events after the failed event, want running without event (rolled back)", s, n)
		}

		// The job is resumed (TEC-14) and completed.
		failOn(t, "")
		var started time.Time
		if err := f.Admin.QueryRow(bg, `SELECT started_at FROM export_job WHERE id = $1`, job.ID).Scan(&started); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ReclaimStale(bg, started.Add(time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		for worker.ProcessOne(bg) {
		}
		if s := status(); s != export.JobStatusCompleted {
			t.Fatalf("export job after the retry: %s, want completed", s)
		}
		data := exactlyOne(t, "export", events.ExportCompleted, job.ID)
		if data["id"] != job.ID || data["format"] != "json" || data["row_count"] != float64(1) || data["initiated_by"] != f.User {
			t.Errorf("export.completed data: %v", data)
		}
	})

	t.Run("job.failed", func(t *testing.T) {
		svc := jobs.NewService(jobs.NewPGRepository(f.App))
		svc.Register(jobs.Kind{Name: "wp550.sync", MaxAttempts: 1, Timeout: time.Minute})
		job, _, err := svc.Enqueue(bg, f.OrgA, "wp550.sync", nil, jobs.EnqueueOptions{CreatedBy: f.User})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.ClaimJob(bg, f.OrgA, job.ID); err != nil {
			t.Fatal(err)
		}
		status := func() string {
			return scalar(t, `SELECT status FROM job WHERE id = $1`, job.ID)
		}

		failOn(t, job.ID)
		_, ferr := svc.Fail(bg, f.OrgA, job.ID, fmt.Errorf("upstream gone"))
		if s, n := status(), len(outboxOf(t, f.OrgA, job.ID)); s != string(jobs.StatusRunning) || n != 0 || ferr == nil {
			t.Fatalf("job.failed lost: job %s with %d events after the failed event (Fail: %v), want running without event and the error",
				s, n, ferr)
		}

		failOn(t, "")
		if _, err = svc.Fail(bg, f.OrgA, job.ID, fmt.Errorf("upstream gone")); err != nil {
			t.Fatalf("fail the job again: %v", err)
		}
		if s := status(); s != string(jobs.StatusFailed) {
			t.Fatalf("job after the retry: %s, want failed", s)
		}
		data := exactlyOne(t, "job", events.JobFailed, job.ID)
		if data["id"] != job.ID || data["kind"] != "wp550.sync" || data["error"] != "upstream gone" ||
			data["attempts"] != float64(1) || data["created_by"] != f.User {
			t.Errorf("job.failed data: %v", data)
		}
	})

	t.Run("webhook.dead", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		repo := webhook.NewPGRepository(f.App)
		store := webhook.NewPGDeliveryStore(f.App)
		sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "wp550", URL: srv.URL + "/hook", Secret: "s",
			Events: []string{"ci.created"}, IsActive: true}
		if err := repo.Create(f.OrgCtx(f.OrgA), sub); err != nil {
			t.Fatal(err)
		}
		due := time.Now().Add(-time.Minute)
		rec, err := store.Enqueue(f.OrgCtx(f.OrgA), webhook.DeliveryRecord{OrganizationID: f.OrgA, SubscriptionID: sub.ID,
			Event: "ci.created", Payload: []byte(`{"id":"wp550"}`), MaxAttempts: 1, NextRetryAt: &due})
		if err != nil {
			t.Fatal(err)
		}
		d := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Deliveries: store, MaxAttempts: 1, PollInterval: -1})
		defer func() { _ = d.Shutdown(context.Background()) }()
		state := func() (status, deadLetters, active string) {
			return scalar(t, `SELECT status FROM webhook_delivery WHERE id = $1`, rec.ID),
				scalar(t, `SELECT count(*)::text FROM webhook_dead_letter WHERE delivery_id = $1`, rec.ID),
				scalar(t, `SELECT is_active::text FROM webhook_subscription WHERE id = $1`, sub.ID)
		}

		failOn(t, sub.ID)
		d.ProcessDue(bg)
		status, letters, active := state()
		if n := len(outboxOf(t, f.OrgA, sub.ID)); status == webhook.StatusDead || letters != "0" || active != "true" || n != 0 {
			t.Fatalf("webhook.dead lost: delivery %s, %s dead letters, subscription active %s, %d events after the failed event; "+
				"want a delivery that is not dead, no dead letter, the subscription unchanged and no event (rolled back)",
				status, letters, active, n)
		}
		if status != webhook.StatusFailed {
			t.Errorf("delivery after the failed move: %s, want failed (queued for another attempt)", status)
		}

		// The retry worker tries the delivery again and moves it.
		failOn(t, "")
		if _, err := f.Admin.Exec(bg, `UPDATE webhook_delivery SET next_retry_at = now() - interval '1 second' WHERE id = $1`, rec.ID); err != nil {
			t.Fatal(err)
		}
		d.ProcessDue(bg)
		if status, letters, _ := state(); status != webhook.StatusDead || letters != "1" {
			t.Fatalf("delivery after the retry: %s with %s dead letters, want dead with one", status, letters)
		}
		data := exactlyOne(t, "webhook", events.WebhookDead, sub.ID)
		if data["delivery_id"] != rec.ID || data["subscription_id"] != sub.ID || data["event"] != "ci.created" ||
			data["subscription_disabled"] != false || data["last_error"] == "" {
			t.Errorf("webhook.dead data: %v", data)
		}
	})
}
