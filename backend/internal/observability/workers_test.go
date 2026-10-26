package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCyclesRecordPassesOfTheirChecks covers WP-447 (OPS-05, F-133): a pass
// of a worker that answers requests is one cycle. It succeeds while every
// check answers; each failing check and each request that failed since the
// previous pass is a failure; a stopped service fails every following pass;
// a pass at shutdown records nothing. Run passes at once and then every
// interval. The methods of a nil *Cycles do nothing.
func TestCyclesRecordPassesOfTheirChecks(t *testing.T) {
	const worker = "wp447_cycles"
	runs := func() float64 { return testutil.ToFloat64(WorkerRuns.WithLabelValues(worker)) }
	errs := func() float64 { return testutil.ToFloat64(WorkerErrors.WithLabelValues(worker)) }
	last := func() float64 { return testutil.ToFloat64(WorkerLastSuccess.WithLabelValues(worker)) }
	ctx := context.Background()

	c := NewCycles(worker)
	var down error
	c.Check("nats", func(context.Context) error { return down })
	c.Check("database", func(checkCtx context.Context) error {
		if _, ok := checkCtx.Deadline(); !ok {
			return errors.New("a check runs without a deadline")
		}
		return nil
	})
	if got := c.Pass(ctx); got != 0 || runs() != 1 || errs() != 0 || last() == 0 {
		t.Fatalf("idle pass with answering checks: failures %d, runs %v, errors %v, last success %v", got, runs(), errs(), last())
	}
	c.Fail()
	c.Fail()
	if got := c.Pass(ctx); got != 2 || runs() != 2 || errs() != 2 {
		t.Errorf("two failed requests: failures %d, runs %v, errors %v", got, runs(), errs())
	}
	down = errors.New("no answer")
	if got := c.Pass(ctx); got != 1 || runs() != 3 || errs() != 3 {
		t.Errorf("idle pass while NATS does not answer: failures %d, runs %v, errors %v", got, runs(), errs())
	}
	// A check of the same name replaces the former one.
	c.Check("nats", func(context.Context) error { return nil })
	WorkerLastSuccess.WithLabelValues(worker).Set(0)
	if got := c.Pass(ctx); got != 0 || last() == 0 {
		t.Errorf("pass after the replaced check: failures %d, last success %v", got, last())
	}
	c.Stopped("certificate renewal", errors.New("subscribe failed"))
	for range 2 {
		if got := c.Pass(ctx); got != 1 {
			t.Errorf("pass after a stopped service: failures %d, want 1", got)
		}
	}
	before := runs()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := c.Pass(cancelled); got != 0 || runs() != before {
		t.Errorf("pass at shutdown: failures %d, runs %v -> %v; want nothing recorded", got, before, runs())
	}

	// Run passes when it starts.
	runCtx, stop := context.WithCancel(ctx)
	ran := make(chan struct{})
	go func() {
		c.Run(runCtx, time.Hour)
		close(ran)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for runs() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	<-ran
	if runs() != before+1 {
		t.Errorf("Run: runs %v -> %v, want one pass at the start", before, runs())
	}

	var none *Cycles
	none.Check("nats", func(context.Context) error { return errors.New("unused") })
	none.Stopped("x", errors.New("unused"))
	none.Fail()
	none.Run(ctx, time.Millisecond)
	if none.Pass(ctx) != 0 {
		t.Error("a nil *Cycles recorded a failure")
	}
}
