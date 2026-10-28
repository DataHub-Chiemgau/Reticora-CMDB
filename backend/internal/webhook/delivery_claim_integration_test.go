package webhook_test

// WP-562 (OPS-05, F-184): the retry claim of the webhook dispatcher is
// fault tolerant and fair. A failing organization does not stop the claim
// of the others, the deliveries leased before or beside the failure are
// sent in the same cycle instead of waiting for their lease to run out, and
// the organizations are served by their oldest due delivery, each up to a
// share of the batch.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestDeliveryClaim' ./internal/webhook/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// injectClaimFailure makes every claim of a delivery of orgID fail: a
// trigger of the test refuses to lease them (status delivering). The
// returned function removes it; it is removed when the test ends anyway.
func injectClaimFailure(t *testing.T, f *scopetest.Fixture, orgID string) func() {
	t.Helper()
	bg := context.Background()
	const fn = "wp562_fail_claim"
	if _, err := f.Admin.Exec(bg, `CREATE OR REPLACE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	RAISE EXCEPTION 'wp562 injected claim failure';
END $$`); err != nil {
		t.Fatalf("trigger function: %v", err)
	}
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		if _, err := f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`); err != nil {
			t.Errorf("drop trigger function: %v", err)
		}
	}
	t.Cleanup(remove)
	if _, err := f.Admin.Exec(bg, `CREATE TRIGGER `+fn+` BEFORE UPDATE ON webhook_delivery FOR EACH ROW
		WHEN (NEW.organization_id = '`+orgID+`'::uuid AND NEW.status = 'delivering') EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	return remove
}

// claimFixture holds one subscription per organization of the fixture and
// enqueues deliveries for them.
type claimFixture struct {
	f     *scopetest.Fixture
	repo  *webhook.PGRepository
	store *webhook.PGDeliveryStore
	subs  map[string]string // organization -> subscription
}

func newClaimFixture(t *testing.T, url func(org string) string) *claimFixture {
	t.Helper()
	f := scopetest.Seed(t, "cd")
	c := &claimFixture{f: f, repo: webhook.NewPGRepository(f.App), store: webhook.NewPGDeliveryStore(f.App), subs: map[string]string{}}
	for _, org := range []string{f.OrgA, f.OrgB} {
		sub := &webhook.Subscription{OrganizationID: org, Name: "wp562-claim", URL: url(org), Secret: "wp562-secret",
			Events: []string{events.CICreated}, IsActive: true}
		if err := c.repo.Create(f.OrgCtx(org), sub); err != nil {
			t.Fatalf("create subscription: %v", err)
		}
		c.subs[org] = sub.ID
	}
	return c
}

// enqueue stores a pending delivery of org that is due at due.
func (c *claimFixture) enqueue(t *testing.T, org string, due time.Time) string {
	t.Helper()
	rec, err := c.store.Enqueue(context.Background(), webhook.DeliveryRecord{OrganizationID: org, SubscriptionID: c.subs[org],
		Event: events.CICreated, Payload: []byte(`{"id":"wp562"}`), Status: webhook.StatusPending, NextRetryAt: &due})
	if err != nil {
		t.Fatalf("enqueue delivery: %v", err)
	}
	return rec.ID
}

// state reads status and next retry time of the deliveries of org.
func (c *claimFixture) state(t *testing.T, org string) (statuses []string, nextRetry []time.Time) {
	t.Helper()
	rows, err := c.f.Admin.Query(context.Background(), `SELECT status, COALESCE(next_retry_at, 'epoch') FROM webhook_delivery
		WHERE organization_id = $1 ORDER BY id`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n time.Time
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		statuses, nextRetry = append(statuses, s), append(nextRetry, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return statuses, nextRetry
}

// TestDeliveryClaimContinuesAfterOrganizationFailure is the retest of
// F-184: the claim of organization 1 fails (injected store error); the due
// deliveries of organization 2 are claimed and sent in the same cycle of
// the retry worker, and those of organization 1 are not leased, so they do
// not wait for a lease to run out: the next cycle after the failure is gone
// sends them.
func TestDeliveryClaimContinuesAfterOrganizationFailure(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[strings.TrimPrefix(r.URL.Path, "/")]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	sent := func(org string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[org]
	}
	c := newClaimFixture(t, func(org string) string { return srv.URL + "/" + org })
	org1, org2 := c.f.OrgA, c.f.OrgB
	// Due long ago: they come before the due deliveries other tests may
	// have left in the database.
	due := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for _, org := range []string{org1, org2} {
		c.enqueue(t, org, due)
		c.enqueue(t, org, due.Add(time.Second))
	}

	d := webhook.NewDispatcher(c.repo, srv.Client(), webhook.DispatcherOptions{Deliveries: c.store, PollInterval: -1})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	})

	removeFailure := injectClaimFailure(t, c.f, org1)
	d.ProcessDue(context.Background())

	if got := sent(org2); got != 2 {
		t.Errorf("deliveries of organization 2 sent in the cycle with the failing organization 1: %d, want 2", got)
	}
	if statuses, _ := c.state(t, org2); !slices.Equal(statuses, []string{webhook.StatusDelivered, webhook.StatusDelivered}) {
		t.Errorf("deliveries of organization 2 after the cycle: %v, want both delivered", statuses)
	}
	if got := sent(org1); got != 0 {
		t.Errorf("deliveries of organization 1 sent despite the failed claim: %d", got)
	}
	statuses, nextRetry := c.state(t, org1)
	now := time.Now()
	for i, s := range statuses {
		if s != webhook.StatusPending || nextRetry[i].After(now) {
			t.Errorf("delivery %d of organization 1 after its failed claim: %s, next retry %v; want pending and still due (not leased)",
				i+1, s, nextRetry[i])
		}
	}

	removeFailure()
	d.ProcessDue(context.Background())
	if got := sent(org1); got != 2 {
		t.Errorf("deliveries of organization 1 sent in the next cycle: %d, want 2", got)
	}
	if statuses, _ = c.state(t, org1); !slices.Equal(statuses, []string{webhook.StatusDelivered, webhook.StatusDelivered}) {
		t.Errorf("deliveries of organization 1 after the next cycle: %v, want both delivered", statuses)
	}
	if got := sent(org2); got != 2 {
		t.Errorf("deliveries of organization 2 sent again: %d in total, want 2", got)
	}
}

// TestDeliveryClaimJoinsOrganizationErrors: ClaimDue returns the
// deliveries it leased together with the error of the failing
// organization and leaves that organization's deliveries due.
func TestDeliveryClaimJoinsOrganizationErrors(t *testing.T) {
	c := newClaimFixture(t, func(string) string { return "https://wp562-claim.example.invalid/hook" })
	org1, org2 := c.f.OrgA, c.f.OrgB
	// Due in 2001: a claim at that time sees only the deliveries of this
	// test.
	base := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	c.enqueue(t, org1, base)
	want := c.enqueue(t, org2, base.Add(time.Minute))
	injectClaimFailure(t, c.f, org1)

	claimed, err := c.store.ClaimDue(context.Background(), base.Add(time.Hour), 10, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "wp562 injected claim failure") || !strings.Contains(err.Error(), org1) {
		t.Errorf("claim error: %v, want the injected failure of organization 1", err)
	}
	if len(claimed) != 1 || claimed[0].ID != want {
		t.Errorf("claimed with the failure of organization 1: %+v, want the delivery %s of organization 2", claimed, want)
	}
	if statuses, nextRetry := c.state(t, org1); len(statuses) != 1 || statuses[0] != webhook.StatusPending || !nextRetry[0].Equal(base) {
		t.Errorf("delivery of organization 1 after its failed claim: %v %v, want pending and due at %v", statuses, nextRetry, base)
	}
}

// TestDeliveryClaimServesOrganizationsFairly: the organization with the
// oldest due delivery is served first, and the backlog of one organization
// does not crowd another out of the batch: each organization first gets
// its share of the batch, the rest goes to those that filled theirs.
func TestDeliveryClaimServesOrganizationsFairly(t *testing.T) {
	c := newClaimFixture(t, func(string) string { return "https://wp562-claim.example.invalid/hook" })
	org1, org2 := c.f.OrgA, c.f.OrgB
	base := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base.Add(time.Hour)
	oldest := c.enqueue(t, org2, base)
	c.enqueue(t, org2, base.Add(30*time.Minute))
	for i := 1; i <= 6; i++ {
		c.enqueue(t, org1, base.Add(time.Duration(i)*time.Minute))
	}
	claim := func(limit int) map[string]int {
		t.Helper()
		claimed, err := c.store.ClaimDue(context.Background(), now, limit, time.Hour)
		if err != nil {
			t.Fatalf("claim %d: %v", limit, err)
		}
		per := map[string]int{}
		for _, rec := range claimed {
			per[rec.OrganizationID]++
		}
		if limit == 1 && (len(claimed) != 1 || claimed[0].ID != oldest) {
			t.Errorf("claim of one delivery: %+v, want the oldest due delivery %s (organization 2)", claimed, oldest)
		}
		return per
	}

	claim(1)
	// Organization 1 now has the oldest due delivery and a backlog of 6,
	// organization 2 one delivery: a batch of 4 gives each a share of 2,
	// the place organization 2 leaves goes to organization 1.
	if per := claim(4); per[org1] != 3 || per[org2] != 1 {
		t.Errorf("batch of 4: organization 1 %d, organization 2 %d; want 3 and 1", per[org1], per[org2])
	}
	if per := claim(10); per[org1] != 3 || per[org2] != 0 {
		t.Errorf("last batch: organization 1 %d, organization 2 %d; want the remaining 3 and 0", per[org1], per[org2])
	}
	if per := claim(10); len(per) != 0 {
		t.Errorf("claim after all deliveries are leased: %v, want none", per)
	}
}
