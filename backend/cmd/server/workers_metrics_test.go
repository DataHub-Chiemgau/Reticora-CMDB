package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// scheduledWorkers are the registered workers that run as a system job of
// the scheduler (systemJobs): name and job kind.
var scheduledWorkers = map[string]string{config.WorkerOfflineDetector: lifecycle.OfflineDetectorKind}

// testCycle is the interval of the polling workers and of the passes in
// these tests.
const testCycle = 100 * time.Millisecond

// errForced is the failure the fakes of the forced-error cycle return.
var errForced = errors.New("wp-447: forced failure")

// TestEveryWorkerExportsRunAndErrorMetrics covers WP-447 (OPS-05, F-133):
// for every name of config.WorkerNames buildWorkers builds the worker (the
// offline detector runs as the system job ci.offline_detector); a name
// without a worker fails the check. Started with fakes, every worker
// records a cycle in reticora_worker_runs_total{worker=<name>} of /metrics;
// the workers whose cycle needs no database here also record it without
// error and with a last success. With a forced failure — failing stores, a
// database that does not answer, a closed NATS connection — every worker
// increases reticora_worker_errors_total{worker=<name>}. The database of the
// fakes never answers (127.0.0.1:1), so the workers events, nats_edge,
// credential_bundles, discovery_scopes and offline_detector record failed
// cycles in both runs, and no test data is touched. The workers that answer
// requests (credential_bundles, discovery_scopes) succeed while idle only
// as long as NATS answers. NATS is TEST_NATS_URL: ingest and jobs run on
// JetStream.
func TestEveryWorkerExportsRunAndErrorMetrics(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set: the workers ingest and jobs run on NATS JetStream")
	}
	env := newWorkerTestEnv(t, url)
	cs := newChainStack(t)

	// Every registered name has its worker; one without fails the check.
	built, err := buildWorkers(env.deps(t, false))
	if err != nil {
		t.Fatal(err)
	}
	offline := env.systemJobs(t)
	if missing := workersWithoutRunner(config.WorkerNames, built, offline); len(missing) > 0 {
		t.Fatalf("registered workers without a worker: %v", missing)
	}
	if missing := workersWithoutRunner(append(slices.Clone(config.WorkerNames), "wp447_unbuilt"), built, offline); !slices.Equal(missing, []string{"wp447_unbuilt"}) {
		t.Fatalf("negative case: a registered name without a worker must fail the check, got %v", missing)
	}

	t.Run("cycle", func(t *testing.T) {
		before, since := scrapeWorkers(t, cs), float64(time.Now().Unix())
		after := runWorkers(t, cs, built, offline, func(m map[string]workerSeries) bool {
			for _, name := range config.WorkerNames {
				if m[name].runs <= before[name].runs {
					return false
				}
			}
			return true
		})
		for _, name := range config.WorkerNames {
			if !after[name].exported || after[name].runs <= before[name].runs {
				t.Errorf("%s: reticora_worker_runs_total %v (before %v), want a recorded cycle", name, after[name].runs, before[name].runs)
			}
		}
		// Without a database dependency in this run the cycle succeeds.
		for _, name := range []string{config.WorkerWebhookDispatcher, config.WorkerExport, config.WorkerIngest, config.WorkerMonitoring,
			config.WorkerScheduler, config.WorkerJobs, config.WorkerNotifier} {
			if after[name].errors != before[name].errors || after[name].lastSuccess < since {
				t.Errorf("%s: errors %v -> %v, last success %v (started %v), want a cycle without error",
					name, before[name].errors, after[name].errors, after[name].lastSuccess, since)
			}
		}
	})

	t.Run("forced error", func(t *testing.T) {
		failing, err := buildWorkers(env.deps(t, true))
		if err != nil {
			t.Fatal(err)
		}
		before := scrapeWorkers(t, cs)
		after := runWorkers(t, cs, failing, offline, func(m map[string]workerSeries) bool {
			for _, name := range config.WorkerNames {
				if m[name].errors <= before[name].errors {
					return false
				}
			}
			return true
		})
		for _, name := range config.WorkerNames {
			if after[name].errors <= before[name].errors {
				t.Errorf("%s: reticora_worker_errors_total %v (before %v), want the forced failure counted", name, after[name].errors, before[name].errors)
			}
		}
	})

	// The workers that answer requests, idle and without a database: a
	// pass succeeds while NATS answers and fails while it does not.
	t.Run("idle request workers", func(t *testing.T) {
		names := []string{config.WorkerCredentialBundles, config.WorkerDiscoveryScopes}
		pick := func(d *workerDeps) []server.Worker {
			t.Helper()
			d.pool, d.cfg = nil, &config.Config{}
			all, err := buildWorkers(d)
			if err != nil {
				t.Fatal(err)
			}
			return slices.DeleteFunc(all, func(w server.Worker) bool { return !slices.Contains(names, w.Name) })
		}
		before, since := scrapeWorkers(t, cs), float64(time.Now().Unix())
		after := runWorkers(t, cs, pick(env.deps(t, false)), nil, func(m map[string]workerSeries) bool {
			return m[names[0]].runs > before[names[0]].runs && m[names[1]].runs > before[names[1]].runs
		})
		for _, name := range names {
			if after[name].runs <= before[name].runs || after[name].errors != before[name].errors || after[name].lastSuccess < since {
				t.Errorf("%s with NATS: runs %v -> %v, errors %v -> %v, last success %v; want a pass without error",
					name, before[name].runs, after[name].runs, before[name].errors, after[name].errors, after[name].lastSuccess)
			}
		}

		// NATS does not answer: the connection keeps reconnecting.
		down, err := nats.Connect("nats://127.0.0.1:1", nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(down.Close)
		d := env.deps(t, false)
		d.coreNATS = down
		before = after
		after = runWorkers(t, cs, pick(d), nil, func(m map[string]workerSeries) bool {
			return m[names[0]].errors > before[names[0]].errors && m[names[1]].errors > before[names[1]].errors
		})
		for _, name := range names {
			if after[name].errors <= before[name].errors || after[name].lastSuccess != before[name].lastSuccess {
				t.Errorf("%s without NATS: errors %v -> %v, last success %v -> %v; want a failed pass",
					name, before[name].errors, after[name].errors, before[name].lastSuccess, after[name].lastSuccess)
			}
		}
	})
}

// workersWithoutRunner returns the names of names that neither a worker of
// workers nor a system job of a scheduled worker runs.
func workersWithoutRunner(names []string, workers []server.Worker, systemJobs []jobs.Schedule) []string {
	runs := map[string]bool{}
	for _, w := range workers {
		runs[w.Name] = true
	}
	for name, kind := range scheduledWorkers {
		for i := range systemJobs {
			if systemJobs[i].Kind == kind {
				runs[name] = true
			}
		}
	}
	var missing []string
	for _, name := range names {
		if !runs[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// runWorkers starts the workers as main does (StartWorkers, every switch
// on), runs each system job of a scheduled worker once and reads /metrics
// until done holds or 45 s passed; then it stops the workers.
func runWorkers(t *testing.T, cs *chainStack, workers []server.Worker, systemJobs []jobs.Schedule,
	done func(map[string]workerSeries) bool) map[string]workerSeries {
	t.Helper()
	set, err := server.StartWorkers(func(string) bool { return true }, workers...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if serr := set.Stop(ctx); serr != nil {
			t.Errorf("stop the workers: %v", serr)
		}
	}()
	for _, kind := range scheduledWorkers {
		for i := range systemJobs {
			if systemJobs[i].Kind == kind {
				_ = systemJobs[i].Run(context.Background())
			}
		}
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		m := scrapeWorkers(t, cs)
		if done(m) || time.Now().After(deadline) {
			return m
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// workerSeries are the worker metrics of one worker in /metrics.
type workerSeries struct {
	exported                  bool
	runs, errors, lastSuccess float64
}

// scrapeWorkers reads the worker metrics of every registered worker from
// the /metrics the server serves.
func scrapeWorkers(t *testing.T, cs *chainStack) map[string]workerSeries {
	t.Helper()
	w := cs.do(http.MethodGet, "/metrics", "", "", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", w.Code)
	}
	body := w.Body.String()
	out := map[string]workerSeries{}
	for _, name := range config.WorkerNames {
		var s workerSeries
		s.runs, s.exported = seriesValue(body, "reticora_worker_runs_total", name)
		s.errors, _ = seriesValue(body, "reticora_worker_errors_total", name)
		s.lastSuccess, _ = seriesValue(body, "reticora_worker_last_success_timestamp_seconds", name)
		out[name] = s
	}
	return out
}

// seriesValue returns the value of metric{worker="<worker>"} in the
// exposition body.
func seriesValue(body, metric, worker string) (float64, bool) {
	prefix := fmt.Sprintf("%s{worker=%q} ", metric, worker)
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
	}
	return 0, false
}

// workerTestEnv is the infrastructure of the worker tests: NATS with a
// stream of the jobs queue of its own, a database that never answers and
// the configuration.
type workerTestEnv struct {
	url                string
	nc                 *nats.Conn
	js                 jetstream.JetStream
	pool               *pgxpool.Pool
	enc                *crypto.EnvelopeEncryptor
	cfg                *config.Config
	jobStream, jobSubj string
}

func newWorkerTestEnv(t *testing.T, url string) *workerTestEnv {
	t.Helper()
	env := &workerTestEnv{url: url}
	var err error
	if env.nc, err = nats.Connect(url, nats.Name("wp447-workers")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.nc.Close)
	if env.js, err = jetstream.New(env.nc); err != nil {
		t.Fatal(err)
	}
	// The database never answers: no test data is read or written.
	if env.pool, err = pgxpool.New(context.Background(), "postgres://reticora@127.0.0.1:1/reticora?sslmode=disable"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.pool.Close)
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	if env.enc, err = crypto.NewEnvelopeEncryptor(masterKey); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.WorkerEnvKey(config.WorkerOfflineDetector), "true")
	env.cfg = config.Load()
	env.cfg.PKIDir = t.TempDir()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	env.jobStream, env.jobSubj = "JOBS_WP447_"+suffix, "wp447_"+suffix+"."
	q := env.jobQueue(server.MemoryRepositories().Jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = q.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.js.DeleteStream(context.Background(), env.jobStream) })
	return env
}

// jobQueue is a queue on the test stream with the sweep every testCycle.
func (env *workerTestEnv) jobQueue(repo jobs.Repository) *jobs.Queue {
	q := jobs.NewQueue(jobs.NewService(repo), env.js)
	q.Stream, q.SubjectPrefix, q.SweepInterval = env.jobStream, env.jobSubj, testCycle
	return q
}

// systemJobs are the system jobs on the database that never answers, with
// the offline detector switched on.
func (env *workerTestEnv) systemJobs(t *testing.T) []jobs.Schedule {
	t.Helper()
	d := systemJobTestDeps(t, env.cfg, true)
	d.pool = env.pool
	return systemJobs(d)
}

// deps are the dependencies of buildWorkers with the fakes; failing makes
// every worker's cycle fail.
func (env *workerTestEnv) deps(t *testing.T, failing bool) *workerDeps {
	t.Helper()
	repos := server.MemoryRepositories()
	repos.EventOutbox = true
	center := notify.NewMemoryCenter()
	var (
		alerts                     = fakeAlerts{}
		state      jobs.StateStore = jobs.NewMemoryStateStore()
		jobRepo                    = repos.Jobs
		deliveries                 = center.Deliveries
	)
	if failing {
		repos.WebhookDeliveries = failingWebhookDeliveries{repos.WebhookDeliveries}
		repos.ExportJobs = failingExportJobs{repos.ExportJobs}
		alerts.err = errForced
		state = failingRunState{state}
		jobRepo = failingJobs{jobRepo}
		deliveries = failingNotifications{deliveries}
	}
	repos.Metrics = evaluatingMetrics{MetricStore: repos.Metrics, alerts: alerts}
	dispatcher := webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries, PollInterval: -1})
	t.Cleanup(func() { _ = dispatcher.Shutdown(context.Background()) })
	scheduler := jobs.NewScheduler(jobs.NewMemoryLocker(), state)
	scheduler.Interval = testCycle
	if err := scheduler.Register(&jobs.Schedule{Kind: "wp447.idle", Spec: "@every 1h", Run: func(context.Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ingest, err := discovery.NewNATSIngestQueue(ctx, env.url, config.DefaultIngestLanes)
	if err != nil {
		t.Fatal(err)
	}
	ingest.Attach(discovery.NewHandler(repos.Discovery, repos.CI))
	if failing {
		// The connection of the queue is closed: every fetch fails.
		ingest.Close()
	} else {
		t.Cleanup(ingest.Close)
	}
	return &workerDeps{cfg: env.cfg, pool: env.pool, encryptor: env.enc, repos: &repos, dispatcher: dispatcher,
		scheduler: scheduler, blobs: blob.NewFileStore(t.TempDir()), notifier: notify.NewNotifier(center.Channels, deliveries,
			center.Preferences, notify.NewSender(map[string]notify.Provider{})),
		collectorPKI: discovery.NewCA(env.pool, env.enc), coreNATS: env.nc, credentials: credential.NewService(repos.Credential, env.enc),
		jobQueue: env.jobQueue(jobRepo), ingestQueue: ingest, cycle: testCycle}
}

// Fakes of the forced-error cycle.
type (
	failingWebhookDeliveries struct{ webhook.DeliveryStore }
	failingExportJobs        struct{ export.JobRepository }
	failingJobs              struct{ jobs.Repository }
	failingRunState          struct{ jobs.StateStore }
	failingNotifications     struct{ notify.DeliveryStore }
	// fakeAlerts holds no alert rule; with err listing them fails.
	fakeAlerts struct {
		monitoring.AlertStore
		err error
	}
	// evaluatingMetrics lets the monitoring worker evaluate the in-memory
	// metric store with alerts.
	evaluatingMetrics struct {
		monitoring.MetricStore
		alerts monitoring.AlertStore
	}
)

func (failingWebhookDeliveries) ClaimDue(context.Context, time.Time, int, time.Duration) ([]webhook.DeliveryRecord, error) {
	return nil, errForced
}

func (failingExportJobs) ReclaimStale(context.Context, time.Time) (int, error) { return 0, errForced }

func (failingExportJobs) ClaimPending(context.Context, int) ([]export.Job, error) {
	return nil, errForced
}

func (failingJobs) QueuedDue(context.Context, time.Time, int) ([]jobs.Job, error) {
	return nil, errForced
}

func (failingRunState) LastDue(context.Context, string) (time.Time, bool, error) {
	return time.Time{}, false, errForced
}

func (failingNotifications) ClaimDue(context.Context, time.Time, int, time.Duration) ([]notify.QueuedDelivery, error) {
	return nil, errForced
}

func (a fakeAlerts) ListEnabled(context.Context) ([]monitoring.AlertRule, error) { return nil, a.err }

func (m evaluatingMetrics) AlertStore() monitoring.AlertStore { return m.alerts }

// TestEveryWorkerSwitchIsDeployable covers WP-447 (OPS-02, F-289): the
// switch of every registered worker is set in the server service of
// deploy/docker-compose/docker-compose.yml from .env
// ("${RETICORA_WORKER_<NAME>_ENABLED:-true}") and in the server container
// of deploy/k8s/base/deployment.yaml. A switch missing in either file is
// reported (negative case).
func TestEveryWorkerSwitchIsDeployable(t *testing.T) {
	compose, k8s := deployedWorkerSwitches(t)
	for _, name := range config.WorkerNames {
		key := config.WorkerEnvKey(name)
		if got, ok := compose[key]; !ok || got != "${"+key+":-true}" {
			t.Errorf("docker-compose.yml, service server: %s = %q (set %v), want \"${%s:-true}\" (settable from .env)", key, got, ok, key)
		}
		if !k8s[key] {
			t.Errorf("deploy/k8s/base/deployment.yaml, container server: %s is not set", key)
		}
	}
	// Negative case: the check reports a switch the files do not set.
	if key := config.WorkerEnvKey("wp447_undeployed"); compose[key] != "" || k8s[key] {
		t.Errorf("negative case: %s found in the deployment files", key)
	}
}

// deployedWorkerSwitches reads the RETICORA_WORKER_*_ENABLED variables of the
// server in the Compose file (with their values) and in the Kubernetes
// deployment.
func deployedWorkerSwitches(t *testing.T) (compose map[string]string, k8s map[string]bool) {
	t.Helper()
	var c struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	readDeployYAML(t, "../../../deploy/docker-compose/docker-compose.yml", &c)
	svc, ok := c.Services["server"]
	if !ok {
		t.Fatal("docker-compose.yml has no service server")
	}
	compose = map[string]string{}
	for k, v := range svc.Environment {
		if strings.HasPrefix(k, "RETICORA_WORKER_") {
			compose[k] = v
		}
	}
	var d struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `yaml:"name"`
						Env  []struct {
							Name string `yaml:"name"`
						} `yaml:"env"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	readDeployYAML(t, "../../../deploy/k8s/base/deployment.yaml", &d)
	k8s = map[string]bool{}
	for _, ctr := range d.Spec.Template.Spec.Containers {
		if d.Kind != "Deployment" || ctr.Name != "server" {
			continue
		}
		for _, e := range ctr.Env {
			if strings.HasPrefix(e.Name, "RETICORA_WORKER_") {
				k8s[e.Name] = true
			}
		}
	}
	if len(compose) == 0 || len(k8s) == 0 {
		t.Fatalf("no worker switches found: compose %v, k8s %v", compose, k8s)
	}
	return compose, k8s
}

func readDeployYAML(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
