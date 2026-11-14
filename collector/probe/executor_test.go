package probe

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// validTCPSpec is a spec of type tcp that probewire.Validate accepts.
func validTCPSpec(target string, port int) probewire.ProbeSpec {
	params, _ := json.Marshal(map[string]int{"port": port})
	return probewire.ProbeSpec{
		ID: "p-1", Type: TypeTCP, Target: target,
		IntervalMS: 10_000, TimeoutMS: 1_000, DownCount: 1, UpCount: 1,
		Params: params, Epoch: 7,
	}
}

func ptr(s probewire.ProbeSpec) *probewire.ProbeSpec { return &s }

// swapExecutor replaces the executor of typ for the duration of the test.
func swapExecutor(t *testing.T, typ string, e Executor) {
	t.Helper()
	old, had := Lookup(typ)
	unregister(typ)
	if e != nil {
		Register(typ, e)
	}
	t.Cleanup(func() {
		unregister(typ)
		if had {
			Register(typ, old)
		}
	})
}

func TestUnregisteredTypeIsUnsupported(t *testing.T) {
	// A type the register does not know at all.
	spec := validTCPSpec("127.0.0.1", 1)
	spec.Type = "no_such_type"
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("unknown type: got %s/%s, want error/unsupported", r.Status, r.Code)
	}
	// An available type of the type register without executor.
	swapExecutor(t, TypeTCP, nil)
	r = Run(context.Background(), ptr(validTCPSpec("127.0.0.1", 1)), nil)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("unregistered type: got %s/%s, want error/unsupported", r.Status, r.Code)
	}
	if r.ProbeID != "p-1" || r.Epoch != 7 || r.ObservedAt.IsZero() {
		t.Fatalf("result must carry probe id, epoch and observed_at: %+v", r)
	}
}

func TestPlannedTypeIsUnsupported(t *testing.T) {
	var planned string
	for _, typ := range probewire.Types() {
		if !typ.Available() {
			planned = typ.Key
			break
		}
	}
	if planned == "" {
		t.Skip("every type of the register is available")
	}
	var calls atomic.Int32
	swapExecutor(t, planned, ExecutorFunc(func(context.Context, probewire.ProbeSpec, map[string]string) probewire.Result {
		calls.Add(1)
		return probewire.Result{Status: probewire.StatusOK}
	}))
	spec := validTCPSpec("127.0.0.1", 1)
	spec.Type = planned
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("planned type %s: got %s/%s, want error/unsupported", planned, r.Status, r.Code)
	}
	if calls.Load() != 0 {
		t.Fatalf("executor of planned type %s was called", planned)
	}
}

func TestInvalidSpecIsRejectedBeforeExecution(t *testing.T) {
	var calls atomic.Int32
	swapExecutor(t, TypeTCP, ExecutorFunc(func(context.Context, probewire.ProbeSpec, map[string]string) probewire.Result {
		calls.Add(1)
		return probewire.Result{Status: probewire.StatusOK}
	}))
	spec := validTCPSpec("127.0.0.1", 70000) // port above 65535
	r := Run(context.Background(), &spec, nil)
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
		t.Fatalf("got %s/%s, want error/config", r.Status, r.Code)
	}
	if !strings.Contains(r.Labels[LabelField], "params.port") || r.Labels[LabelMessage] == "" {
		t.Fatalf("field error missing in labels: %v", r.Labels)
	}
	if calls.Load() != 0 {
		t.Fatal("executor was called for an invalid spec")
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("rejection result must be a valid wire result: %v", err)
	}
}

func TestExecutorHonoursDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	// An executor that ignores its context: Run must still return at the
	// deadline with fail/timeout.
	swapExecutor(t, TypeTCP, ExecutorFunc(func(context.Context, probewire.ProbeSpec, map[string]string) probewire.Result {
		<-release
		return probewire.Result{Status: probewire.StatusOK}
	}))
	spec := validTCPSpec("127.0.0.1", 1)
	start := time.Now()
	r := Run(context.Background(), &spec, nil)
	elapsed := time.Since(start)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonTimeout {
		t.Fatalf("got %s/%s, want fail/timeout", r.Status, r.Code)
	}
	limit := time.Duration(spec.TimeoutMS)*time.Millisecond + 500*time.Millisecond
	if elapsed > limit {
		t.Fatalf("Run returned after %s, deadline was %d ms", elapsed, spec.TimeoutMS)
	}
	if r.ProbeID != spec.ID || r.Epoch != spec.Epoch {
		t.Fatalf("timeout result lost probe id or epoch: %+v", r)
	}
}

func TestRegisterIsConcurrentlyReadable(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, ok := Lookup(TypeTCP); !ok {
					t.Error("tcp executor not registered")
					return
				}
				_ = Registered()
			}
		}()
	}
	wg.Wait()
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("second registration of tcp did not panic")
		}
	}()
	Register(TypeTCP, &TCPExecutor{})
}

// TestRunExecutorPanicIsError: a panicking executor yields error instead of
// ending the collector process.
func TestRunExecutorPanicIsError(t *testing.T) {
	swapExecutor(t, TypeTCP, ExecutorFunc(func(context.Context, probewire.ProbeSpec, map[string]string) probewire.Result {
		panic("executor bug")
	}))
	r := Run(context.Background(), ptr(validTCPSpec("127.0.0.1", 1)), map[string]string{"password": "s3cret"})
	if r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
		t.Fatalf("got %s/%s, want error/config", r.Status, r.Code)
	}
	if r.ProbeID != "p-1" || r.Epoch != 7 {
		t.Fatalf("result must carry probe id and epoch: %+v", r)
	}
}
