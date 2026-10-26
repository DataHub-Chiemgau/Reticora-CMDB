package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// recorder is a consumer that keeps what it receives and can fail.
type recorder struct {
	mu     sync.Mutex
	got    []events.Envelope
	failAt string
}

func (r *recorder) Consume(_ context.Context, env *events.Envelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if env.ID == r.failAt {
		return errors.New("subscriber down")
	}
	r.got = append(r.got, *env)
	return nil
}

// TestOutboxAgainstPostgres covers EVT-01 and EVT-02 (WP-136) against
// PostgreSQL (and NATS with TEST_NATS_URL):
//   - an event is written in the transaction of its mutation: a rollback
//     leaves none;
//   - the CI repository writes ci.created, ci.changed (with the changed
//     fields) and ci.deleted; sequence and entity_seq count per
//     organization and entity without gaps, also for concurrent writers;
//   - the publisher delivers in sequence order, marks the events published,
//     stops an organization's batch at a failed delivery (order kept) and
//     retries it; the stream RETICORA_EVENTS holds each event once under
//     events.<org_id>.<type>;
//   - cleanup removes old published events but keeps the newest ones.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOutboxAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "98")
	bg := context.Background()
	outbox := events.NewOutbox()
	inOrg := func(org string, fn func(ctx context.Context, tx pgx.Tx) error) error {
		scope := database.OrgWideScope(org, "")
		return database.WithTenant(bg, f.App, &scope, fn)
	}
	count := func(org string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Rollback: no event.
	rollback := errors.New("rollback")
	if err := inOrg(f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := outbox.Append(ctx, tx, &events.Event{OrgID: f.OrgB, Type: events.JobFailed, Data: map[string]string{"job": "x"}}); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if n := count(f.OrgB); n != 0 {
		t.Fatalf("events after a rollback: %d", n)
	}
	if err := inOrg(f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := outbox.Append(ctx, tx, &events.Event{OrgID: f.OrgB, Type: "ci.updated"})
		return err
	}); err == nil {
		t.Error("an unknown event type was written")
	}

	// The CI repository.
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(outbox)
	ctx := f.OrgCtx(f.OrgA)
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	item := &ci.Item{OrganizationID: f.OrgA, CITypeID: typeID, Name: "evt-host", Status: "active", Attributes: map[string]any{}}
	if err := repo.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	maintenance := "maintenance"
	if _, err := repo.Update(ctx, f.OrgA, item.ID, ci.UpdateRequest{Status: &maintenance}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, f.OrgA, item.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.Admin.Query(bg, `SELECT type, sequence, entity_id, entity_seq, data FROM event_outbox
		WHERE organization_id = $1 ORDER BY sequence`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		typ       string
		seq       int64
		entity    string
		entitySeq int64
		data      map[string]any
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		scanErr := r.Scan(&x.typ, &x.seq, &x.entity, &x.entitySeq, &x.data)
		return x, scanErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].typ != events.CICreated || got[1].typ != events.CIChanged || got[2].typ != events.CIDeleted {
		t.Fatalf("CI events: %+v", got)
	}
	for i, g := range got {
		if g.seq != int64(i+1) || g.entitySeq != int64(i+1) || g.entity != item.ID || g.data["id"] != item.ID {
			t.Errorf("event %d: %+v", i, g)
		}
	}
	if fields, _ := got[1].data["changed_fields"].([]any); len(fields) != 1 || fields[0] != "status" {
		t.Errorf("changed fields: %v", got[1].data["changed_fields"])
	}

	// Concurrent writers: sequence without gaps or doubles.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- inOrg(f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
				_, appendErr := outbox.Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.ReviewCreated,
					EntityID: fmt.Sprintf("review-%d", i%2), Data: map[string]int{"n": i}})
				return appendErr
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var maxSeq, distinct int
	var maxEntity int64
	if err = f.Admin.QueryRow(bg, `SELECT max(sequence), count(DISTINCT sequence),
		(SELECT max(entity_seq) FROM event_outbox WHERE organization_id = $1 AND entity_id = 'review-0')
		FROM event_outbox WHERE organization_id = $1`, f.OrgA).Scan(&maxSeq, &distinct, &maxEntity); err != nil {
		t.Fatal(err)
	}
	if maxSeq != 23 || distinct != 23 || maxEntity != 10 {
		t.Errorf("after concurrent writers: max %d, distinct %d, review-0 entity_seq %d", maxSeq, distinct, maxEntity)
	}

	// The publisher.
	var js jetstream.JetStream
	if url := os.Getenv("TEST_NATS_URL"); url != "" {
		nc, connErr := nats.Connect(url)
		if connErr != nil {
			t.Fatal(connErr)
		}
		defer nc.Close()
		if js, err = jetstream.New(nc); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	var fifth string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM event_outbox WHERE organization_id = $1 AND sequence = 5`, f.OrgA).Scan(&fifth); err != nil {
		t.Fatal(err)
	}
	rec.failAt = fifth
	pub := events.NewPublisher(f.App, js, rec)
	if err = pub.Setup(bg); err != nil {
		t.Fatal(err)
	}
	if _, err = pub.PublishPending(bg); err == nil {
		t.Error("a failed delivery was not reported")
	}
	ofA := func() []events.Envelope {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		var out []events.Envelope
		for _, e := range rec.got {
			if e.OrgID == f.OrgA {
				out = append(out, e)
			}
		}
		return out
	}
	if a := ofA(); len(a) != 4 || a[3].Sequence != 4 {
		t.Fatalf("delivered before the failure: %d", len(a))
	}
	var attempts int
	var lastError string
	if err = f.Admin.QueryRow(bg, `SELECT attempts, COALESCE(last_error, '') FROM event_outbox WHERE id = $1`, fifth).Scan(&attempts, &lastError); err != nil ||
		attempts != 1 || lastError == "" {
		t.Errorf("failed event: attempts %d, error %q (%v)", attempts, lastError, err)
	}
	rec.failAt = ""
	if _, err = pub.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	a := ofA()
	if len(a) != 23 {
		t.Fatalf("delivered: %d, want 23", len(a))
	}
	for i := range a {
		if a[i].Sequence != int64(i+1) {
			t.Fatalf("order: position %d has sequence %d", i, a[i].Sequence)
		}
	}
	var pending int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND published_at IS NULL`, f.OrgA).Scan(&pending); err != nil || pending != 0 {
		t.Errorf("pending after publishing: %d %v", pending, err)
	}
	// Events of other organizations (rows of earlier test packages in the
	// shared database, WP-427) may still be pending; none of A's may go out
	// a second time.
	_, _ = pub.PublishPending(bg)
	if again := len(ofA()) - len(a); again != 0 {
		t.Errorf("published again: %d", again)
	}
	if js != nil {
		stream, streamErr := js.Stream(bg, events.StreamName)
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		msg, getErr := stream.GetLastMsgForSubject(bg, events.SubjectPrefix+f.OrgA+"."+events.CIDeleted)
		if getErr != nil {
			t.Fatal(getErr)
		}
		var env events.Envelope
		if err = json.Unmarshal(msg.Data, &env); err != nil || env.EntityID != item.ID || env.Sequence != 3 {
			t.Errorf("stream message: %+v %v", env, err)
		}
		info, _ := stream.Info(bg)
		if info.Config.MaxAge != events.StreamMaxAge || info.Config.MaxBytes != events.StreamMaxBytes {
			t.Errorf("stream retention: %v %d", info.Config.MaxAge, info.Config.MaxBytes)
		}
	}

	// Cleanup keeps the newest event of the organization and of each entity.
	pub.WithClock(func() time.Time { return time.Now().UTC().Add(events.KeepPublished + time.Hour) })
	if err = pub.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	var left []int64
	if err = f.Admin.QueryRow(bg, `SELECT array_agg(sequence ORDER BY sequence) FROM event_outbox WHERE organization_id = $1`, f.OrgA).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if len(left) != 3 || left[2] != 23 {
		t.Errorf("after cleanup: %v (newest of the CI, of each review and of the organization)", left)
	}
	if err = inOrg(f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		env, appendErr := outbox.Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.CIChanged, EntityID: item.ID})
		if appendErr == nil && (env.Sequence != 24 || env.EntitySeq != 4) {
			return fmt.Errorf("numbering after cleanup: %d/%d", env.Sequence, env.EntitySeq)
		}
		return appendErr
	}); err != nil {
		t.Error(err)
	}
}
