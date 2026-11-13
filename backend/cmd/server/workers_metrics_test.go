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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// scheduledWorkers are the registered workers that run as a system job of
// the scheduler (systemJobs): name and job kind.
var scheduledWorkers = map[string]string{config.WorkerOfflineDetector: lifecycle.OfflineDetectorKind,
	config.WorkerComputedRefresh: ci.RefreshJobKind, config.WorkerProbeAssign: probe.AssignKind}

// monitoringWorker reports whether name is a worker of the module
// monitoring: those run in the deployment profile monitoring (WP-869), the
// others do not.
func monitoringWorker(name string) bool {
	for _, m := range server.LateModules {
		if m.Key == server.ModuleMonitoring {
			return slices.Contains(m.Workers, name)
		}
	}
	return false
}

// testCycle is the interval of the polling workers and of the passes in
// these tests.
const testCycle = 100 * time.Millisecond

// errForced is the failure the fakes of the forced-error cycle return.
var errForced = errors.New("wp-447: forced failure")

// TestEveryWorkerExportsRunAndErrorMetrics covers WP-447 (OPS-05, F-133):
// for every name of config.WorkerNames buildWorkers builds the worker (the
// offline detector and the computed refresh run as the system jobs
// ci.offline_detector and attribute.computed_refresh); a name
// without a worker fails the check. Started with fakes, every worker
// records a cycle in reticora_worker_runs_total{worker=<name>} of /metrics;
// the workers whose cycle needs no database here also record it without
// error and with a last success. With a forced failure — failing stores, a
// database that does not answer, a closed NATS connection — every worker
// increases reticora_worker_errors_total{worker=<name>}. The database of the
// fakes never answers (127.0.0.1:1), so the workers events, nats_edge,
// credential_bundles, discovery_scopes, offline_detector and
// computed_refresh record failed
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
			config.WorkerHealth, config.WorkerScheduler, config.WorkerJobs, config.WorkerNotifier} {
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
	// The keys of the edge worker come from a derivation root (WP-459).
	if err = env.enc.UseEphemeralDerivationRoot(); err != nil {
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
	// The module monitoring brings the system job of probe_assign.
	modules, err := server.ParseModules(server.ModuleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	d.modules = modules
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
		health                     = fakeHealth{}
		state      jobs.StateStore = jobs.NewMemoryStateStore()
		jobRepo                    = repos.Jobs
		deliveries                 = center.Deliveries
	)
	if failing {
		repos.WebhookDeliveries = failingWebhookDeliveries{repos.WebhookDeliveries}
		repos.ExportJobs = failingExportJobs{repos.ExportJobs}
		alerts.err = errForced
		health.err = errForced
		state = failingRunState{state}
		jobRepo = failingJobs{jobRepo}
		deliveries = failingNotifications{deliveries}
	}
	repos.Metrics = evaluatingMetrics{MetricStore: repos.Metrics, alerts: alerts, health: health}
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
	// fakeHealth refreshes nothing; with err the refresh fails.
	fakeHealth struct{ err error }
	// evaluatingMetrics lets the monitoring worker evaluate the in-memory
	// metric store with alerts and the health worker refresh with health.
	evaluatingMetrics struct {
		monitoring.MetricStore
		alerts monitoring.AlertStore
		health monitoring.HealthRefresher
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

func (m evaluatingMetrics) HealthRefresher() monitoring.HealthRefresher { return m.health }

// LatestBySeries makes evaluatingMetrics a monitoring.SeriesStore (the
// evaluator per series, WP-984) over the wrapped store; a store without
// series yields none.
func (m evaluatingMetrics) LatestBySeries(ctx context.Context, q *monitoring.SeriesQuery) ([]monitoring.SeriesPoint, error) {
	if s, ok := m.MetricStore.(monitoring.SeriesStore); ok {
		return s.LatestBySeries(ctx, q)
	}
	return nil, nil
}

func (h fakeHealth) RefreshHealth(context.Context) error { return h.err }

// TestEveryWorkerSwitchIsDeployable covers WP-447 (OPS-02, F-289): the
// switch of every registered worker is set in the server service of
// deploy/docker-compose/docker-compose.yml from .env
// ("${RETICORA_WORKER_<NAME>_ENABLED:-true}") and in the server container
// of deploy/k8s/base/deployment.yaml. A switch missing in either file is
// TestEveryWorkerSwitchIsDeployable covers OPS-02 (WP-447, WP-869): every
// worker switch is set in both Compose services (server with "${KEY:-true}",
// monitoring with "${RETICORA_MONITORING_<NAME>:-…}", true only for the
// monitoring worker) and in both Kubernetes deployments (reticora-server and
// TestEveryWorkerSwitchIsDeployable covers OPS-02 (WP-447, WP-869): every
// worker switch is set in both Compose services (server with "${KEY:-true}",
// monitoring with "${RETICORA_MONITORING_<NAME>:-…}", true only for the
// monitoring worker) and in both Kubernetes deployments (reticora-server and
// reticora-monitoring, E-162).
func TestEveryWorkerSwitchIsDeployable(t *testing.T) {
	deployed := deployedWorkerSwitches(t)
	for _, problem := range workerSwitchProblems(deployed) {
		t.Error(problem)
	}
	// Negative cases: a switch the files do not set, and one missing in the
	// monitoring deployment only, are reported.
	if key := config.WorkerEnvKey("wp447_undeployed"); deployed.compose["server"][key] != "" || deployed.k8s["reticora-server"][key] != "" {
		t.Errorf("negative case: %s found in the deployment files", key)
	}
	delete(deployed.k8s["reticora-monitoring"], config.WorkerEnvKey(config.WorkerHealth))
	if problems := workerSwitchProblems(deployed); len(problems) != 1 || !strings.Contains(problems[0], "reticora-monitoring") {
		t.Errorf("negative case: a switch missing in reticora-monitoring gives %v, want one problem", problems)
	}
}

// workerSwitchProblems checks every worker switch in both Compose services
// and both Kubernetes deployments.
func workerSwitchProblems(deployed deployedSwitches) []string {
	var problems []string
	for _, name := range config.WorkerNames {
		key := config.WorkerEnvKey(name)
		if got, ok := deployed.compose["server"][key]; !ok || got != "${"+key+":-true}" {
			problems = append(problems, fmt.Sprintf("docker-compose.yml, service server: %s = %q (set %v), want \"${%s:-true}\" (settable from .env)", key, got, ok, key))
		}
		def := "false"
		if monitoringWorker(name) {
			def = "true"
		}
		monitoringKey := "RETICORA_MONITORING_" + strings.TrimPrefix(key, "RETICORA_")
		if got := deployed.compose["monitoring"][key]; got != "${"+monitoringKey+":-"+def+"}" {
			problems = append(problems, fmt.Sprintf("docker-compose.yml, service monitoring: %s = %q, want \"${%s:-%s}\"", key, got, monitoringKey, def))
		}
		for _, deployment := range []string{"reticora-server", "reticora-monitoring"} {
			value, ok := deployed.k8s[deployment][key]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("deploy/k8s/base, deployment %s: %s is not set", deployment, key))
			case deployment == "reticora-monitoring" && (value == "true") != monitoringWorker(name):
				problems = append(problems, fmt.Sprintf("deployment reticora-monitoring: %s = %q, only the monitoring workers run there", key, value))
			}
		}
	}
	return problems
}

// deployedSwitches are the RETICORA_WORKER_*_ENABLED variables per Compose
// service and per Kubernetes deployment, with their values.
type deployedSwitches struct {
	compose map[string]map[string]string
	k8s     map[string]map[string]string
}

// deployedWorkerSwitches reads the RETICORA_WORKER_*_ENABLED variables of the
// Compose services server and monitoring and of the Kubernetes deployments
// reticora-server and reticora-monitoring.
func deployedWorkerSwitches(t *testing.T) deployedSwitches {
	t.Helper()
	var c struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	readDeployYAML(t, "../../../deploy/docker-compose/docker-compose.yml", &c)
	out := deployedSwitches{compose: map[string]map[string]string{}, k8s: map[string]map[string]string{}}
	for _, name := range []string{"server", "monitoring"} {
		svc, ok := c.Services[name]
		if !ok {
			t.Fatalf("docker-compose.yml has no service %s", name)
		}
		out.compose[name] = map[string]string{}
		for k, v := range svc.Environment {
			if strings.HasPrefix(k, "RETICORA_WORKER_") {
				out.compose[name][k] = v
			}
		}
	}
	type deployment struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `yaml:"name"`
						Env  []struct {
							Name  string `yaml:"name"`
							Value string `yaml:"value"`
						} `yaml:"env"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	for _, file := range []string{"deployment.yaml", "deployment-monitoring.yaml"} {
		var d deployment
		readDeployYAML(t, "../../../deploy/k8s/base/"+file, &d)
		switches := map[string]string{}
		for _, ctr := range d.Spec.Template.Spec.Containers {
			if d.Kind != "Deployment" || ctr.Name != "server" {
				continue
			}
			for _, e := range ctr.Env {
				if strings.HasPrefix(e.Name, "RETICORA_WORKER_") {
					switches[e.Name] = e.Value
				}
			}
		}
		out.k8s[d.Metadata.Name] = switches
	}
	for name, m := range out.compose {
		if len(m) == 0 {
			t.Fatalf("no worker switches in the Compose service %s", name)
		}
	}
	for _, name := range []string{"reticora-server", "reticora-monitoring"} {
		if len(out.k8s[name]) == 0 {
			t.Fatalf("no worker switches in the deployment %s", name)
		}
	}
	return out
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
