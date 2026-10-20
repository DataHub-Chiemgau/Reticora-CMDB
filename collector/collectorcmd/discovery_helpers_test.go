// Shared helpers of the collector's discovery tests (WP-378): the credential
// tests moved out of discovery_auditdefect_test.go (discovery_credentials_test.go,
// bundle_test.go) use them without a build tag, the defect tests still in
// that file with the tag auditdefect.
package collectorcmd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// auditSpy is a protocol plugin that records what discovery hands it.
type auditSpy struct {
	name string
	// alive returns the probe hits; nil means nothing answers.
	alive   func(targets []string) []plugins.Result
	collect func(ctx context.Context, target string) (*plugins.DeviceRecord, error)
	// countOnly records only the number of targets per probe.
	countOnly bool

	mu     sync.Mutex
	counts []int
	firsts []string
	creds  map[string][]map[string]string // target -> credentials seen
}

func (s *auditSpy) Name() string { return s.name }

func (s *auditSpy) Probe(_ context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	s.mu.Lock()
	s.counts = append(s.counts, len(targets))
	if len(targets) > 0 {
		s.firsts = append(s.firsts, targets[0])
	}
	if !s.countOnly {
		if s.creds == nil {
			s.creds = map[string][]map[string]string{}
		}
		for _, t := range targets {
			s.creds[t] = append(s.creds[t], maps.Clone(creds))
		}
	}
	s.mu.Unlock()
	if s.alive == nil {
		return nil, nil
	}
	return s.alive(targets), nil
}

func (s *auditSpy) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	s.mu.Lock()
	if !s.countOnly {
		if s.creds == nil {
			s.creds = map[string][]map[string]string{}
		}
		s.creds[target] = append(s.creds[target], maps.Clone(creds))
	}
	s.mu.Unlock()
	if s.collect == nil {
		return nil, nil
	}
	return s.collect(ctx, target)
}

func (s *auditSpy) seen(target string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creds[target]
}

// auditUsePlugins swaps plugins into the registry for one test and resets
// the scope list, the bundle and the scope channel afterwards.
func auditUsePlugins(t *testing.T, plugs ...plugins.Plugin) {
	t.Helper()
	for _, p := range plugs {
		previous, had := pluginRegistry[p.Name()]
		pluginRegistry[p.Name()] = p
		t.Cleanup(func() {
			if had {
				pluginRegistry[p.Name()] = previous
			} else {
				delete(pluginRegistry, p.Name())
			}
		})
	}
	t.Cleanup(func() {
		collectorScopes = &scopeCache{}
		collectorBundle.set(nil)
	})
}

// auditInstallBundle installs an opened credential bundle the way
// syncBundle does after fetchBundle.
func auditInstallBundle(creds ...bundleCredential) {
	collectorBundle.set(&bundleContent{Credentials: creds})
}

// auditScopeRun is a scope run report as the server receives it.
//
//nolint:unused // used by the defect tests (build tag auditdefect)
type auditScopeRun struct {
	ScopeID string `json:"scope_id"`
	Result  struct {
		Records    int        `json:"records"`
		ErrorCount int        `json:"error_count"`
		Errors     []runError `json:"errors"`
	} `json:"result"`
}

// auditCaptureScopeRuns connects the scope channel to TEST_NATS_URL and
// answers the collector's scopes.result requests like the server.
//
//nolint:unused // used by the defect tests (build tag auditdefect)
func auditCaptureScopeRuns(t *testing.T) func() []auditScopeRun {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required: the scope run reports travel over NATS")
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	subject := fmt.Sprintf("collector.audit-%d.c%d", time.Now().UnixNano(), rand.IntN(1_000_000))
	var mu sync.Mutex
	var runs []auditScopeRun
	sub, err := nc.Subscribe(subject+".scopes.result", func(m *nats.Msg) {
		var r auditScopeRun
		if json.Unmarshal(m.Data, &r) == nil {
			mu.Lock()
			runs = append(runs, r)
			mu.Unlock()
		}
		_ = m.Respond([]byte(`{"ok":true}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	scopeChannel.Store(&scopeChannelState{nc: nc, subject: subject})
	t.Cleanup(func() {
		scopeChannel.Store(nil)
		_ = sub.Unsubscribe()
		nc.Close()
	})
	return func() []auditScopeRun {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(runs)
	}
}

// auditUploadSink is an ingest endpoint that keeps the uploaded records.
func auditUploadSink(t *testing.T) (*uploader, func() []plugins.DeviceRecord) {
	t.Helper()
	var mu sync.Mutex
	var got []plugins.DeviceRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/monitoring/metrics") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var batch plugins.DeviceRecordBatch
		if err = json.NewDecoder(gz).Decode(&batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, batch.Records...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	up, err := newUploader(context.Background(), testConfig(srv.URL, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	return up, func() []plugins.DeviceRecord {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// auditLoopback binds port on a random 127.x.y.z address.
//
//nolint:unused // used by the defect tests (build tag auditdefect)
func auditLoopback(t *testing.T, port int) (net.Listener, string) {
	t.Helper()
	var lastErr error
	for range 20 {
		ip := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(250), rand.IntN(250), 1+rand.IntN(250))
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
		if err == nil {
			return ln, ip
		}
		lastErr = err
	}
	t.Fatalf("cannot bind a loopback address on port %d: %v", port, lastErr)
	return nil, ""
}
