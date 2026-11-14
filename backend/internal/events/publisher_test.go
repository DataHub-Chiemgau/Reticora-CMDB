package events

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Outbox throughput per organization (E-226, WP-982): publishOrg keeps
// publishing batches until the organization's outbox is empty, the time
// budget of the cycle is spent or a delivery fails. The batch source is a
// fake outbox, the clock is simulated.

// fakeOutbox stands for the pending events of one organization: each batch
// takes up to limit of them and advances the simulated clock by perBatch.
type fakeOutbox struct {
	pending  int
	batches  []int
	clock    time.Time
	perBatch time.Duration
	// failAt makes the batch with this number (1-based) fail its delivery
	// after delivering half of its events.
	failAt int
}

func (f *fakeOutbox) publisher() *Publisher {
	p := NewPublisher(nil, nil)
	p.now = func() time.Time { return f.clock }
	p.batch = f.batch
	return p
}

func (f *fakeOutbox) batch(_ context.Context, _ string, limit int) (int, bool, error) {
	n := min(limit, f.pending)
	f.clock = f.clock.Add(f.perBatch)
	if len(f.batches)+1 == f.failAt {
		half := n / 2
		f.pending -= half
		f.batches = append(f.batches, half)
		return half, false, errors.New("consumer down")
	}
	f.pending -= n
	f.batches = append(f.batches, n)
	return n, n == limit, nil
}

func TestPublishOrgDrainsUntilEmpty(t *testing.T) {
	f := &fakeOutbox{pending: 350, clock: time.Unix(0, 0), perBatch: 10 * time.Millisecond}
	n, err := f.publisher().publishOrg(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if n != 350 || f.pending != 0 {
		t.Errorf("published %d, %d left; want 350 and none", n, f.pending)
	}
	if len(f.batches) != 4 || f.batches[3] != 50 {
		t.Errorf("batches %v, want four (100, 100, 100, 50) in one cycle", f.batches)
	}
}

func TestPublishOrgStopsAtTimeBudget(t *testing.T) {
	f := &fakeOutbox{pending: 1000, clock: time.Unix(0, 0), perBatch: 300 * time.Millisecond}
	p := f.publisher()
	n, err := p.publishOrg(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	// 3 × 300 ms reach the budget of 900 ms: the cycle ends after the third
	// batch, the rest waits for the next cycle.
	if n != 300 || len(f.batches) != 3 || f.pending != 700 {
		t.Fatalf("first cycle: published %d in %d batches, %d left; want 300 in 3, 700 left", n, len(f.batches), f.pending)
	}
	n, err = p.publishOrg(context.Background(), "org")
	if err != nil || n != 300 || f.pending != 400 {
		t.Errorf("next cycle: published %d (%v), %d left; want 300 and 400 left", n, err, f.pending)
	}
	if p.Budget != 900*time.Millisecond {
		t.Errorf("budget %v, want 900ms", p.Budget)
	}
}

func TestPublishOrgStopsOnDeliveryError(t *testing.T) {
	f := &fakeOutbox{pending: 1000, clock: time.Unix(0, 0), perBatch: time.Millisecond, failAt: 2}
	n, err := f.publisher().publishOrg(context.Background(), "org")
	if err == nil {
		t.Fatal("no error although a delivery failed")
	}
	if len(f.batches) != 2 || n != 150 || f.pending != 850 {
		t.Errorf("published %d in batches %v, %d left; want 150 in two batches (stop at the failing one), 850 left",
			n, f.batches, f.pending)
	}
}
