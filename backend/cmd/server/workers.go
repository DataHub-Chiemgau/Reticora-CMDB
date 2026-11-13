package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// serviceCycle is how often a worker that answers requests records a cycle
// (observability.Cycles, OPS-05).
const serviceCycle = time.Minute

// jobBacklogScan bounds the queued jobs one backlog sample of the jobs
// worker reads.
const jobBacklogScan = 10000

// workerDeps is what the background workers run on; pool is nil with
// --no-db.
type workerDeps struct {
	cfg          *config.Config
	pool         *pgxpool.Pool
	encryptor    *crypto.EnvelopeEncryptor
	repos        *server.Repositories
	mfa          user.MFAEnforcer
	dispatcher   *webhook.Dispatcher
	scheduler    *jobs.Scheduler
	blobs        blob.Store
	eventStream  jetstream.JetStream
	notifier     *notify.Notifier
	collectorPKI *discovery.CA
	coreNATS     *nats.Conn
	credentials  *credential.Service
	jobQueue     *jobs.Queue
	ingestQueue  *discovery.NATSIngestQueue
	// probeIngest is the worker probe_ingest main built for /readyz; nil
	// builds one on ingestQueue (probeIngester).
	probeIngest *probe.Ingester
	// entitlements checks the discovery license on the collectors' scope
	// channel (F-400); nil treats every organization as licensed.
	entitlements *entitlement.Service
	// modules are the late modules; the export worker checks inventory for
	// DATEV jobs (WP-790).
	modules *server.ModuleSet
	// fieldPolicyCache holds the field permission snapshots (the cache of
	// server.Options.FieldPolicyCache); nil keeps them per process.
	fieldPolicyCache cache.Store
	// cycle replaces the intervals of the polling workers and of the
	// cycles of the workers that answer requests (tests); 0 keeps them.
	cycle time.Duration
}

// workflowTriggers is the consumer of the workflow event triggers
// (WP-1090): with the module workflows, the database, the job queue and the
// event stream; nil otherwise.
func (d *workerDeps) workflowTriggers() *workflow.TriggerConsumer {
	if d.modules == nil || !d.modules.Enabled("workflows") || d.pool == nil || d.eventStream == nil || d.jobQueue == nil {
		return nil
	}
	store, ok := d.repos.Workflow.(workflow.EventStore)
	if !ok {
		return nil
	}
	dispatcher := d.dispatcher
	if d.repos.EventOutbox {
		dispatcher = nil
	}
	exec := workflow.NewExecutor(d.repos.Workflow, d.repos.Ticket, d.repos.CI, d.repos.Form, dispatcher).
		WithFieldGuard(override.NewGuard(d.repos.Override)).WithQueue(d.jobQueue)
	if d.repos.EventOutbox {
		exec.WithEventOutbox()
	}
	ents := d.entitlements
	return workflow.NewTriggerConsumer(exec, store, d.eventStream, func(ctx context.Context, orgID string) bool {
		return ents != nil && ents.IsEnabled(ctx, orgID, entitlement.FeatureWorkflows)
	})
}

// probeIngester is the worker probe_ingest: the one main built for /readyz,
// else one on the ingest queue's JetStream and the database; nil without
// either.
func (d *workerDeps) probeIngester() *probe.Ingester {
	if d.probeIngest != nil {
		return d.probeIngest
	}
	if d.ingestQueue == nil || d.pool == nil {
		return nil
	}
	d.probeIngest = newProbeIngester(d.ingestQueue, d.pool, d.cfg)
	return d.probeIngest
}

// newProbeIngester builds the worker probe_ingest on the stream
// RETICORA_PROBE.
func newProbeIngester(q *discovery.NATSIngestQueue, pool *pgxpool.Pool, cfg *config.Config) *probe.Ingester {
	// The state changes run in the same worker (WP-961).
	stream := probe.DefaultStream(cfg.ProbeStreamMaxBytes)
	return probe.NewIngester(q.JetStream(), stream, probe.NewPGIngestStore(pool)).
		WithStates(probe.NewStateApplier(q.JetStream(), stream, pool))
}

// every is the interval of a worker: def, or the one of the tests.
func (d *workerDeps) every(def time.Duration) time.Duration {
	if d.cycle > 0 {
		return d.cycle
	}
	return def
}

// buildWorkers lists the background workers, each under its operator switch
// RETICORA_WORKER_<NAME>_ENABLED (OPS-02), so they can run on dedicated
// instances. Every worker records its cycles (observability.ObserveWorkerRun,
// OPS-05); a worker that answers requests records a pass of
// observability.Cycles every serviceCycle. The offline detector runs as a
// system job of the scheduler (systemJobs).
func buildWorkers(d *workerDeps) ([]server.Worker, error) {
	workers := []server.Worker{{
		// Re-send persisted webhook deliveries that are due for a retry.
		Name: config.WorkerWebhookDispatcher,
		Run:  func(ctx context.Context) { d.dispatcher.RunRetries(ctx, d.every(15*time.Second)) },
		Backlog: backlogOf(d.repos.WebhookDeliveries, func(ctx context.Context, c dueCounter) (int, error) {
			return c.CountDue(ctx, time.Now().UTC())
		}),
	}, {
		// System jobs on the scheduler (JOB-02): one replica leads through
		// a PostgreSQL advisory lock and runs the due slots. At start the
		// existing org_admins are bound to MFA (WP-358, idempotent).
		Name: config.WorkerScheduler,
		Run: func(ctx context.Context) {
			if users, ok := d.repos.User.(*user.PGRepository); ok && d.mfa != nil {
				go reconcileMFA(ctx, users)
			}
			d.scheduler.Run(ctx)
		},
	}}
	// Process queued asynchronous export jobs. All replicas share the queue
	// via FOR UPDATE SKIP LOCKED, so jobs are never run twice.
	if d.blobs != nil {
		exportWorker := export.NewJobWorker(d.repos.ExportJobs, d.repos.CI, d.blobs)
		if d.repos.Jobs != nil {
			exportWorker.Tracker = jobs.NewService(d.repos.Jobs)
		}
		// DATEV jobs are checked again before rendering (WP-790); without
		// entitlements DATEV is refused instead of licensed.
		var datevEntitlements export.EntitlementChecker
		if d.entitlements != nil {
			datevEntitlements = d.entitlements
		}
		exportWorker.DATEV = export.NewDATEVGate(datevEntitlements, d.modules.Enabled(server.ModuleInventory))
		// The field policy of the job's creator is resolved at render time
		// with the role assignments of then (RBA-07, WP-729); the snapshots
		// share the cache of the API, so an invalidation reaches the worker.
		exportWorker.Policies = server.WithAssetsFeature(server.NewFieldResolver(d.repos, d.fieldPolicyCache), d.entitlements)
		// DATEV jobs read assets and the mapping override (WP-310).
		exportWorker.Assets, _ = d.repos.Asset.(export.AssetSource)
		exportWorker.Orgs = d.repos.TenantHierarchy
		// Asset jobs of POST /assets/bulk read the selection by id (WP-782).
		exportWorker.AssetRows, _ = d.repos.Asset.(export.AssetRows)
		// Subnet and address jobs read IPAM (WP-921).
		exportWorker.IPAMRows, _ = d.repos.IPAM.(export.IPAMRows)
		workers = append(workers, server.Worker{
			Name: config.WorkerExport,
			Run:  func(ctx context.Context) { exportWorker.Run(ctx, d.every(2*time.Second)) },
			Backlog: backlogOf(d.repos.ExportJobs, func(ctx context.Context, c pendingCounter) (int, error) {
				return c.CountPending(ctx)
			}),
		})
	}
	// Publish the domain events of the outbox to RETICORA_EVENTS and the
	// webhook subscriptions (EVT-01, WP-136).
	if d.repos.EventOutbox && d.pool != nil {
		// Subscribed events become notifications (NTF-05).
		// The alarm texts link to the frontend of the invitation page
		// (WP-987); alarms reach notify only through the outbox, the
		// evaluator has no log notifier with a database.
		consumers := []events.Consumer{d.dispatcher,
			notify.NewEventConsumer(notify.NewPGResolver(d.pool, permission.NewPGRepository(d.pool)), d.notifier).
				WithLinkBase(d.cfg.InvitationAcceptURL)}
		// A changed CI, lifecycle state or finding evaluates its CI against
		// the active compliance rules (SEC-03, WP-1126), with the module
		// compliance and the feature compliance_findings.
		if d.modules != nil && d.modules.Enabled("compliance") {
			ents := d.entitlements
			consumers = append(consumers, compliance.NewIncrementalConsumer(compliance.NewEvaluator(d.repos.Compliance, d.repos.CI),
				func(ctx context.Context, orgID string) bool {
					return ents != nil && ents.IsEnabled(ctx, orgID, entitlement.FeatureComplianceFindings)
				}))
		}
		publisher := events.NewPublisher(d.pool, d.eventStream, consumers...)
		// The stream retention of the installation (EVT-01, NFR-06); the
		// outbox keeps published event data as long.
		publisher.MaxAge, publisher.MaxBytes = d.cfg.EventsMaxAge, d.cfg.EventsMaxBytes
		setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
		err := publisher.Setup(setupCtx)
		cancelSetup()
		if err != nil {
			return nil, fmt.Errorf("set up the event stream on NATS: %w", err)
		}
		workers = append(workers, server.Worker{Name: config.WorkerEvents, Run: publisher.Run})
	}
	// Collector NATS endpoint (COL-08): its cycle checks the database and
	// the connections of the callout and the cut and counts the failed
	// exports, sweeps, answers and cuts.
	if d.collectorPKI != nil && (d.cfg.PKIDir != "" || d.cfg.NATSEdgeURL != "") {
		edgeSecrets, err := discovery.DeriveEdgeSecrets(d.encryptor)
		if err != nil {
			return nil, fmt.Errorf("derive the collector NATS endpoint keys: %w", err)
		}
		// Over the internal NATS the worker learns of the disables of
		// collectors, also from other replicas, and cuts their open
		// connections at the endpoint (COL-04, F-025).
		cycles := observability.NewCycles(config.WorkerNATSEdge)
		if d.pool != nil {
			cycles.Check("database", d.pool.Ping)
		}
		edge := &discovery.EdgeConfig{Pool: d.pool, CA: d.collectorPKI, Secrets: edgeSecrets, PKIDir: d.cfg.PKIDir,
			Hosts: splitList(d.cfg.NATSEdgeHosts), URL: d.cfg.NATSEdgeURL, Bus: d.coreNATS, Cycles: cycles}
		workers = append(workers, server.Worker{Name: config.WorkerNATSEdge,
			Run: serving(cycles, d.every(serviceCycle), func(ctx context.Context) { discovery.RunEdge(ctx, edge) })})
	}
	// Credential bundles for the collectors over their NATS channel,
	// encrypted for each collector's key (COL-02), and the renewal of their
	// certificates on the same channel with proof of the current key
	// (COL-04, F-012). Its cycle checks the NATS connection and the
	// database and counts the requests that failed.
	if d.collectorPKI != nil && d.coreNATS != nil {
		cycles := observability.NewCycles(config.WorkerCredentialBundles)
		cycles.Check("nats", d.coreNATS.FlushWithContext)
		if d.pool != nil {
			cycles.Check("database", d.pool.Ping)
		}
		gate := discovery.NewBundleGate(d.pool, d.collectorPKI)
		// A change of a secret, a reference or a binding tells the
		// collectors holding the credential to reload their bundle after
		// the commit (bundle.changed); a vault value read in a new version
		// emits credential.rotated (CRD-05, WP-897). Set before the routes
		// serve: the service is the one of the API.
		if d.pool != nil {
			d.credentials.WithRotation(credential.NewRotation(d.pool, d.coreNATS, gate))
		}
		renewal := discovery.NewCertificateRenewal(d.pool, d.collectorPKI).WithCycles(cycles)
		bundles := d.credentials.WithCycles(cycles)
		workers = append(workers, server.Worker{Name: config.WorkerCredentialBundles, Run: serving(cycles, d.every(serviceCycle), func(ctx context.Context) {
			go func() {
				if rerr := renewal.Serve(ctx, d.coreNATS); rerr != nil {
					cycles.Stopped("certificate renewal", rerr)
					slog.Error("collector certificate renewal", "error", rerr)
				}
			}()
			if serr := bundles.ServeBundles(ctx, d.coreNATS, gate, gate); serr != nil {
				slog.Error("credential bundles", "error", serr)
			}
		})})
	}
	// Discovery scopes, their run results and the subnets a collector
	// reports after enrollment (scope_suggested) over the collectors' NATS
	// channel (DIS-09). Its cycle checks the NATS connection and the
	// database and counts the requests that failed.
	if d.coreNATS != nil {
		cycles := observability.NewCycles(config.WorkerDiscoveryScopes)
		cycles.Check("nats", d.coreNATS.FlushWithContext)
		if d.pool != nil {
			cycles.Check("database", d.pool.Ping)
		}
		scopeChannel := discovery.NewHandler(d.repos.Discovery, d.repos.CI).WithScopeCycles(cycles)
		// The channel checks the discovery license like the edge data path
		// and the entitlement middleware: without discovery no scopes, jobs
		// or scope suggestions (ENT-05, F-400).
		if d.entitlements != nil {
			scopeChannel.WithEdgeLicense(server.DiscoveryLicense(d.entitlements))
		}
		// The collectors run their ad-hoc discovery jobs over the same
		// channel; the status changes reach the job center (JOB-01, F-358).
		if d.repos.Jobs != nil {
			scopeChannel.WithJobTracker(jobs.NewService(d.repos.Jobs))
		}
		workers = append(workers, server.Worker{Name: config.WorkerDiscoveryScopes, Run: serving(cycles, d.every(serviceCycle), func(ctx context.Context) {
			if serr := scopeChannel.ServeCollectorScopes(ctx, d.coreNATS); serr != nil {
				slog.Error("discovery scopes", "error", serr)
			}
		})})
	}
	// Deliver queued notifications with retry (NTF-04).
	workers = append(workers, server.Worker{Name: config.WorkerNotifier, Run: func(ctx context.Context) { d.notifier.Run(ctx, d.every(5*time.Second)) }})
	// Run the jobs announced on the JOBS work queue (TEC-14); a cycle is a
	// sweep, the backlog the due queued jobs of the kinds it runs.
	if d.jobQueue != nil {
		run := d.jobQueue.Run
		// Event triggers of workflows (WFL-02, WP-1090, E-226): the durable
		// consumer workflow-triggers on RETICORA_EVENTS starts the runs of
		// the event definitions and queues them as workflow.run, with the
		// module workflows; until WP-1114 in the worker jobs.
		if triggers := d.workflowTriggers(); triggers != nil {
			run = func(ctx context.Context) {
				go triggers.Run(ctx)
				d.jobQueue.Run(ctx)
			}
		}
		workers = append(workers, server.Worker{Name: config.WorkerJobs, Run: run, Backlog: d.jobBacklog})
	}
	// Consume ingest.> (REC-01).
	if d.ingestQueue != nil {
		workers = append(workers, server.Worker{Name: config.WorkerIngest, Run: d.ingestQueue.Run, Backlog: d.ingestQueue.Backlog})
	}
	// Answer the assignment set requests of the collectors
	// (.probes.request, PRB-11, WP-948) in the worker probe_assign, with
	// NATS and the probe store on PostgreSQL.
	if store, ok := d.repos.Probes.(*probe.PGRepository); ok && d.coreNATS != nil && d.pool != nil {
		cycles := observability.NewCycles(config.WorkerProbeAssign)
		cycles.Check("nats", d.coreNATS.FlushWithContext)
		cycles.Check("database", d.pool.Ping)
		channel := discovery.NewHandler(d.repos.Discovery, d.repos.CI)
		// The credential ids of a probe must be in the collector's bundle:
		// the gate computes them as VerifyBundleTarget does, the probe
		// bindings of the collector included (AK2).
		bundle := discovery.NewBundleGate(d.pool, d.collectorPKI).CredentialIDs
		distributor := probe.NewDistributor(store, channel.CheckCollectorChannel).WithCycles(cycles).WithBundle(bundle)
		if d.entitlements != nil {
			distributor.WithFeatures(d.entitlements)
		}
		workers = append(workers, server.Worker{Name: config.WorkerProbeAssign, Run: serving(cycles, d.every(serviceCycle), func(ctx context.Context) {
			if serr := distributor.ServeProbeRequests(ctx, d.coreNATS); serr != nil {
				slog.Error("probe distribution", "error", serr)
			}
		})})
	}
	// Write the probe result batches of the stream RETICORA_PROBE to
	// probe_result (NFR-11, PRB-07, WP-960; module monitoring); the backlog
	// is the consumer's unacknowledged messages, also in /readyz.
	if ingester := d.probeIngester(); ingester != nil {
		workers = append(workers, server.Worker{Name: config.WorkerProbeIngest, Run: ingester.Run, Backlog: ingester.Backlog})
	}
	// Evaluate the alert rules per series every 15 seconds (module
	// monitoring, MON-04, ALR-05, WP-984): the state of a series is its
	// alarm (WP-982), so a restart neither reports again nor loses a
	// pending start.
	alertStore, evaluating := d.repos.Metrics.(monitoring.EvaluatingStore)
	series, bySeries := d.repos.Metrics.(monitoring.SeriesStore)
	if evaluating && bySeries && d.repos.Alarms != nil {
		evaluator := monitoring.NewEvaluator(series, alertStore.AlertStore(), alarm.NewRuleSink(d.repos.Alarms))
		// Threshold rules only for organizations with monitoring_network
		// (E-151, WP-985); without entitlements (--no-db) every rule runs.
		if d.entitlements != nil {
			evaluator.WithFeatures(d.entitlements)
		}
		workers = append(workers, server.Worker{
			Name: config.WorkerMonitoring,
			Run:  func(ctx context.Context) { evaluator.Run(ctx, d.every(monitoring.EvaluationInterval)) },
		})
	}
	// The health of the CIs follows the monitoring state every minute
	// (DB-05, MON-02): a worker of its own, so it runs without the module
	// alerting (P3, F-087), which stays off in the release defaults G1 and
	// G2 (WP-830).
	if refresher, ok := d.repos.Metrics.(monitoring.HealthRefreshingStore); ok {
		health := monitoring.NewHealthWorker(refresher.HealthRefresher())
		workers = append(workers, server.Worker{
			Name: config.WorkerHealth,
			Run:  func(ctx context.Context) { health.Run(ctx, d.every(time.Minute)) },
		})
	}
	return workers, nil
}

// workerSwitch reports whether a worker starts on this instance: its
// operator switch is on (OPS-02) and its module, if it belongs to a late
// module, is enabled (WP-214). With the release default G2 (WP-830) only
// the workers of the P3 module alerting (monitoring) stay off.
func workerSwitch(cfg *config.Config, modules *server.ModuleSet) func(name string) bool {
	return func(name string) bool {
		return cfg.WorkerEnabled(name) && modules.WorkerAllowed(name)
	}
}

// serving runs serve, a worker that answers requests: its cycles make a
// pass when it starts and every interval while serve runs.
func serving(cycles *observability.Cycles, interval time.Duration, serve func(ctx context.Context)) func(context.Context) {
	return func(ctx context.Context) {
		passCtx, stop := context.WithCancel(ctx)
		defer stop()
		go cycles.Run(passCtx, interval)
		serve(ctx)
	}
}

// jobBacklog is the backlog of the jobs worker (OPS-05): the due queued jobs
// of the kinds the queue runs; the other kinds of the job table are tracked
// for workers of their own (export, scheduler).
func (d *workerDeps) jobBacklog(ctx context.Context) (int, error) {
	due, err := d.repos.Jobs.QueuedDue(ctx, time.Now().UTC(), jobBacklogScan)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range due {
		if d.jobQueue.Handles(due[i].Kind) {
			n++
		}
	}
	return n, nil
}

// Backlog sources of the workers (OPS-05); the PostgreSQL repositories
// implement them, the in-memory ones of --no-db report no backlog.
type (
	pendingCounter interface {
		CountPending(ctx context.Context) (int, error)
	}
	dueCounter interface {
		CountDue(ctx context.Context, now time.Time) (int, error)
	}
)

// backlogOf returns count bound to repo when repo implements C, else nil.
func backlogOf[C any](repo any, count func(context.Context, C) (int, error)) func(context.Context) (int, error) {
	c, ok := repo.(C)
	if !ok {
		return nil
	}
	return func(ctx context.Context) (int, error) { return count(ctx, c) }
}

// systemJobDeps is what the system jobs of the scheduler run on; pool is
// nil with --no-db.
type systemJobDeps struct {
	cfg          *config.Config
	pool         *pgxpool.Pool
	encryptor    *crypto.EnvelopeEncryptor
	repos        *server.Repositories
	modules      *server.ModuleSet
	entitlements *entitlement.Service
	dispatcher   *webhook.Dispatcher
	// jobQueue is the job queue on NATS; nil without NATS.
	jobQueue *jobs.Queue
	// coreNATS sends .probes.changed to the collectors after an
	// assignment change (WP-948); nil logs the changes only.
	coreNATS *nats.Conn
	// admp is the refresh of the ADMP cache the router queues admp.refresh
	// with (WP-929); nil builds one on the repositories (tests).
	admp *admp.Sync
	// notifier bundles the digests (notify.digest); nil: no digest run.
	notifier *notify.Notifier
	// blobs holds the document content the soft-delete run deletes with
	// the documents (WP-732) and the staged rows of the API imports
	// (import.staging.sweep, WP-303); nil keeps documents with stored
	// content and registers no staging sweep.
	blobs blob.Store
}

// vulnfeedJobs are the system jobs of the vulnerability feeds on store:
// vulnfeed.csaf (WP-1118), unless the air-gapped profile has no explicit
// provider list, and vulnfeed.nvd with the KEV sync (WP-1119), not in the
// air-gapped profile.
func vulnfeedJobs(cfg *config.Config, store vulnfeed.NVDStore) []jobs.Schedule {
	var out []jobs.Schedule
	csaf, err := vulnfeed.NewCSAF(store, &vulnfeed.CSAFConfig{Providers: cfg.VulnfeedCSAFProviders,
		Explicit: cfg.VulnfeedCSAFExplicit(), AirGapped: cfg.AirGapped, Interval: cfg.VulnfeedCSAFInterval,
		Egress: egress.Options{AllowPrivate: cfg.EgressAllowPrivate}})
	switch {
	case err == nil:
		out = append(out, csaf.Schedule())
	case errors.Is(err, vulnfeed.ErrCSAFDisabled):
		slog.Info("CSAF import off: air-gapped without RETICORA_VULNFEED_CSAF_PROVIDERS", "event", "vulnfeed.csaf.disabled")
	default:
		// Validate refuses the start for an invalid provider list.
		slog.Error("CSAF import not registered", "event", "vulnfeed.csaf.disabled", "error", err)
	}
	if !cfg.VulnfeedNVDEnabled() {
		slog.Info("NVD and KEV import off in the air-gapped profile", "event", "vulnfeed.nvd.disabled")
		return out
	}
	nvd, err := vulnfeed.NewNVD(store, &vulnfeed.NVDConfig{APIKey: cfg.VulnfeedNVDAPIKey, Interval: cfg.VulnfeedNVDInterval,
		KEVURL: cfg.VulnfeedKEVURL, Egress: egress.Options{AllowPrivate: cfg.EgressAllowPrivate}})
	if err != nil {
		slog.Error("NVD import not registered", "event", "vulnfeed.nvd.disabled", "error", err)
		return out
	}
	return append(out, nvd.Schedule())
}

// registerSystemJobs registers the system jobs (systemJobs) with the
// scheduler.
func registerSystemJobs(scheduler *jobs.Scheduler, d *systemJobDeps) error {
	systemJobs := systemJobs(d)
	for i := range systemJobs {
		if err := scheduler.Register(&systemJobs[i]); err != nil {
			return err
		}
	}
	return nil
}

// systemJobs are the system jobs of the scheduler (JOB-02, OPS-03). Without
// a database only the reservation sweeper runs.
func systemJobs(d *systemJobDeps) []jobs.Schedule {
	// Release expired reservations (spec §10); reservations belong to the
	// inventory module (WP-214), which the release default G2 switches on
	// (WP-830).
	var systemJobs []jobs.Schedule
	if d.modules.Enabled(server.ModuleInventory) {
		systemJobs = append(systemJobs, reservation.NewSweeper(d.repos.Reservation, d.dispatcher).Schedule())
	}
	// The ADMP cache (ADM-03, E-225, WP-929), with the module admp: every
	// 15 minutes the services linked to a port and their connections that
	// expire within seven days are queued for admp.refresh within the
	// rate of their account; hourly the entries past their TTL are removed
	// with their personal surrogates. Without a database on the memory
	// repositories.
	if d.modules.Enabled("admp") {
		admpSync := d.admp
		if admpSync == nil {
			admpSync = server.NewADMPSync(d.repos, nil, egress.Options{}, d.pool, d.cfg.ADMPCacheTTLDays)
		}
		systemJobs = append(systemJobs, admpSync.RefreshLinkedSchedule(), admpSync.ExpireSchedule())
	}
	// Document versions without a scan result are submitted to the
	// malware scan again every 15 minutes (SEC-09, E-42, WP-734), with
	// the module documents and the job queue only.
	if pool := d.pool; pool != nil && d.modules.Enabled("documents") && d.jobQueue != nil && d.repos.Jobs != nil {
		systemJobs = append(systemJobs, document.NewScanSweeper(d.repos.Document,
			func(ctx context.Context) ([]string, error) { return database.OrganizationIDs(ctx, pool) },
			d.jobQueue, jobs.NewService(d.repos.Jobs)).Schedule())
	}
	// Retention runs per kind of data (NFR-06, AUD-09) and key rotation
	// (SEC-06).
	if pool := d.pool; pool != nil {
		systemJobs = append(systemJobs, jobs.NewRotation(pool, d.encryptor, dekRotator{repo: credential.NewPGRepository(pool)},
			webhook.NewPGRepository(pool)).Schedules()...)
		// Ended workflow runs are kept RETICORA_RETENTION_WORKFLOW_RUN_DAYS
		// (400 days, E-241, WP-1093) by retention.workflow_runs.
		retention := jobs.RetentionFromDays(d.cfg.RetentionAuditYears,
			d.cfg.RetentionHistoryDays, d.cfg.RetentionSoftDeleteDays, d.cfg.RetentionReviewItemDays)
		if days := d.cfg.RetentionWorkflowRunDays; days > 0 {
			retention.WorkflowRuns = time.Duration(days) * 24 * time.Hour
		}
		systemJobs = append(systemJobs, jobs.NewRetention(pool, retention).WithBlobs(d.blobs).Schedules()...)
		// Collector status: offline after five minutes without heartbeat,
		// paused while the discovery license is not active (COL-01, COL-05).
		collectorStatus := discovery.NewStatusMonitor(pool)
		// The collectors of an organization in the grace period of its
		// deletion are paused as well (TLC-06, WP-1153); after the cancel
		// the next run sets them offline, their next heartbeat active.
		collectorStatus.Licensed = discovery.LicensedUnlessPendingDeletion(
			func(ctx context.Context, orgID string) (bool, error) {
				_, status, serr := d.entitlements.Status(ctx, orgID, entitlement.FeatureDiscovery)
				return status == entitlement.StatusActive, serr
			},
			func(ctx context.Context, orgID string) (bool, error) {
				return tenantapi.OrgPendingDeletion(ctx, pool, orgID)
			})
		systemJobs = append(systemJobs, collectorStatus.Schedule())
		// Workflow runs of a CI whose wake-up was lost (WP-1089, EVT-01):
		// one executor queues the runs of both workflow system jobs on the
		// job queue (without it a scheduled run executes in the job).
		// every minute the next pending run of each free CI that waited
		// longer than five minutes is queued again, with the module
		// workflows; without the job queue the runs execute in the request
		// and the sweep queues nothing.
		if d.modules.Enabled("workflows") {
			sweepDispatcher := d.dispatcher
			if d.repos.EventOutbox {
				sweepDispatcher = nil
			}
			sweepRuns := workflow.NewExecutor(d.repos.Workflow, d.repos.Ticket, d.repos.CI, d.repos.Form, sweepDispatcher).
				WithFieldGuard(override.NewGuard(d.repos.Override))
			// send_webhook of a run executed here writes to the outbox
			// like the runs of the router (WP-1096).
			if d.repos.EventOutbox {
				sweepRuns.WithEventOutbox()
			}
			if d.jobQueue != nil {
				sweepRuns.WithQueue(d.jobQueue)
			}
			orgs := func(ctx context.Context) ([]string, error) { return database.OrganizationIDs(ctx, pool) }
			systemJobs = append(systemJobs, workflow.NewSerialSweep(sweepRuns, orgs).Schedule())
			// Schedule triggers of workflows (JOB-02 [P3], WP-1091): every
			// minute on one instance the due slot of each active schedule
			// definition of the organizations with the feature workflows,
			// at most the newest missed slot.
			if store, ok := d.repos.Workflow.(workflow.ScheduleStore); ok {
				ents := d.entitlements
				systemJobs = append(systemJobs, workflow.NewScheduler(sweepRuns, store, orgs,
					func(ctx context.Context, orgID string) bool {
						return ents != nil && ents.IsEnabled(ctx, orgID, entitlement.FeatureWorkflows)
					}).Schedule())
			}
		}
		// The nightly compliance evaluation (SEC-03, WP-1126, E-237): daily
		// 02:30 UTC one job compliance.evaluate per organization with the
		// feature compliance_findings and an active rule, with the module
		// compliance.
		if d.modules.Enabled("compliance") && d.repos.Jobs != nil {
			// Without the job queue the job runs in this process: the service
			// knows its kind (NewJobService).
			evalJobs := compliance.NewJobs(compliance.NewEvaluator(d.repos.Compliance, d.repos.CI)).WithJobs(compliance.NewJobService(d.repos.Jobs))
			if d.jobQueue != nil {
				evalJobs.WithQueue(d.jobQueue)
			}
			ents := d.entitlements
			orgs := func(ctx context.Context) ([]string, error) { return database.OrganizationIDs(ctx, pool) }
			systemJobs = append(systemJobs, compliance.NewNightly(evalJobs, d.repos.Compliance, orgs, func(ctx context.Context, orgID string) bool {
				return ents != nil && ents.IsEnabled(ctx, orgID, entitlement.FeatureComplianceFindings)
			}).Schedule())
			// The daily score snapshot (CMP-02, WP-1127): 04:30 UTC, after
			// the nightly evaluation, for trends over 400 days.
			systemJobs = append(systemJobs, compliance.NewSnapshot(d.repos.Compliance, orgs, func(ctx context.Context, orgID string) bool {
				return ents != nil && ents.IsEnabled(ctx, orgID, entitlement.FeatureComplianceFindings)
			}).Schedule())
		}
		// Alarm retention (ALR-01, WP-981): daily the resolved alarms
		// older than 400 days with their transitions, with the module
		// monitoring.
		if d.modules.Enabled(server.ModuleMonitoring) {
			systemJobs = append(systemJobs, alarm.NewRetention(pool).Schedule())
			// Probe assignment (PRB-03, PRB-11, WP-941): every 10 s each
			// collector group under its lock probe_assign:<group>, the
			// worker probe_assign under RETICORA_WORKER_PROBE_ASSIGN_ENABLED;
			// a committed probe write assigns its groups at once on the
			// instance that served it.
			assigner := probe.NewAssigner(pool)
			var notifier probe.AssignmentNotifier = probe.LogNotifier{}
			if d.coreNATS != nil {
				notifier = probe.NewNATSAssignmentNotifier(d.coreNATS)
			}
			assigner.WithNotifier(notifier)
			if store, ok := d.repos.Probes.(*probe.PGRepository); ok {
				store.SetAssignTrigger(assigner)
			}
			if d.cfg.WorkerEnabled(config.WorkerProbeAssign) {
				systemJobs = append(systemJobs, workerJob(config.WorkerProbeAssign, assigner.Schedule()))
			}
			// Suspension of the probes of a lost monitoring feature
			// (PKG-09, ENT-08, WP-942): every minute per organization;
			// resumed when the feature is active again.
			systemJobs = append(systemJobs, probe.NewSuspender(pool, probe.LicensedBy(d.entitlements)).WithNotifier(notifier).Schedule())
			// Automatic ping probes (MON-02, WP-949): every 15 minutes one
			// job probe.autocreate per organization with
			// monitoring.auto_ping and the feature monitoring, on the job
			// queue only (the kind runs on its handlers, jobkinds.go).
			if d.jobQueue != nil {
				systemJobs = append(systemJobs, probe.NewAutocreateDispatcher(pool, d.jobQueue,
					probe.LicensedBy(d.entitlements)).Schedule())
			}
		}
		// Credential age: daily each stored secret beyond its max_age_days
		// once per secret_version as credential.rotation_due (CRD-05,
		// WP-897).
		systemJobs = append(systemJobs, credential.NewAgeCheck(pool).Schedule())
		// Vulnerability feeds (SEC-02, E-234, WP-1118, WP-1119): vulnfeed.csaf
		// every RETICORA_VULNFEED_CSAF_INTERVAL and vulnfeed.nvd with the
		// KEV sync every RETICORA_VULNFEED_NVD_INTERVAL, with the module
		// findings, over the egress client; air-gapped only the CSAF import
		// with an explicit provider list (E-235).
		if d.modules.Enabled("findings") {
			systemJobs = append(systemJobs, vulnfeedJobs(d.cfg, vulnfeed.NewPGRepository(pool))...)
		}
		// The full exports of an organization can be downloaded for 24
		// hours (TLC-05, WP-1151): hourly org.export_cleanup removes the
		// objects of the expired ones, with the module org_lifecycle.
		if d.modules.Enabled("org_lifecycle") {
			systemJobs = append(systemJobs, export.NewOrgExportCleanup(pool, d.blobs).Schedule())
			// After the grace period of a deletion the rows of the
			// organization go (TLC-06, WP-1154): hourly org.purge, with the
			// reminders 7 and 1 day before to the org_admins.
			purger := tenantapi.NewPurger(pool, purgeTables)
			if d.notifier != nil {
				purger.WithNotices(d.notifier, notify.NewPGResolver(pool, permission.NewPGRepository(pool)))
			}
			systemJobs = append(systemJobs, purger.Schedule())
		}
		// SLA breaches: every minute each missed due time once as
		// ticket.sla_breached, the escalation to assignee and team lead
		// (TKT-01, WP-769); module ticketing, ticketing_standard only.
		if d.modules.Enabled("ticketing") {
			slaCheck := sla.NewBreachCheck(pool)
			slaCheck.Licensed = func(ctx context.Context, orgID string) (bool, error) {
				_, status, serr := d.entitlements.Status(ctx, orgID, entitlement.FeatureTicketingStandard)
				return status == entitlement.StatusActive, serr
			}
			systemJobs = append(systemJobs, slaCheck.Schedule())
		}
		// Contract and warranty expiry: daily each reached stage once as
		// contract.expiring or asset.warranty_expiring (NTF-05 [P2],
		// MGT-12, WP-270); module inventory, assets_basic only.
		if d.modules.Enabled(server.ModuleInventory) {
			expiry := jobs.NewExpiryCheck(pool)
			expiry.Licensed = func(ctx context.Context, orgID string) (bool, error) {
				_, status, serr := d.entitlements.Status(ctx, orgID, entitlement.FeatureAssetsBasic)
				return status == entitlement.StatusActive, serr
			}
			systemJobs = append(systemJobs, expiry.Schedule())
			// Legacy signatures of 000047 as signature documents at their
			// movement (AST-02, E-51, WP-749), with the blob storage only.
			if d.blobs != nil {
				legacy := assignment.NewLegacySignatures(pool, d.blobs)
				if d.entitlements != nil {
					legacy.WithLimits(d.entitlements)
				}
				if d.jobQueue != nil && d.repos.Jobs != nil {
					legacy.WithScans(d.jobQueue)
				}
				systemJobs = append(systemJobs, legacy.Schedule())
			}
		}
		// Ad-hoc discovery jobs without a report from their collector within
		// discovery.JobDeadline fail with cause timeout (JOB-01, F-358).
		if reaper := discovery.NewJobReaper(pool, d.repos.Discovery); reaper != nil {
			if d.repos.Jobs != nil {
				reaper.WithJobTracker(jobs.NewService(d.repos.Jobs))
			}
			systemJobs = append(systemJobs, reaper.Schedule())
		}
		// CIs not seen for three scope intervals become unknown (REC-09,
		// LCY-05): the worker offline_detector under its switch
		// RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED (F-289); each run is a
		// cycle of it.
		if d.cfg.WorkerEnabled(config.WorkerOfflineDetector) {
			systemJobs = append(systemJobs, workerJob(config.WorkerOfflineDetector, lifecycle.NewOfflineDetector(pool).Schedule()))
		}
		// Unconfirmed and suppressed discovery edges are removed (TOP-02).
		systemJobs = append(systemJobs, relationship.NewCleanup(pool).Schedule())
		// Staged rows of API imports whose job ended without a run
		// (cancelled before the start, failed by the reaper) are deleted
		// every hour (IMP-IO-01, WP-303); with the blob storage only.
		if d.blobs != nil {
			systemJobs = append(systemJobs, importer.NewStagingSweep(pool, d.blobs).Schedule())
		}
		// Weekly CI snapshots, the base of the replay at a point in time
		// (AUD-08, WP-312); asset snapshots only with the module inventory
		// (WP-795), like the reservation sweeper.
		systemJobs = append(systemJobs, history.NewWeeklySnapshot(pool).WithAssets(d.modules.Enabled(server.ModuleInventory)).Schedule())
		// Personal data of existing history entries into the vault (SEC-05).
		systemJobs = append(systemJobs, jobs.NewPIIBackfill(pool).Schedule())
		// Attribute indexes of searchable/unique attributes (DB-04, MET-15).
		if typeRepo, ok := d.repos.CIType.(*citype.PGRepository); ok && d.repos.Jobs != nil {
			ddlJob := citype.NewDDLJob(pool, jobs.NewService(d.repos.Jobs))
			typeRepo.WithIndexScheduler(ddlJob)
			systemJobs = append(systemJobs, ddlJob.Schedule())
		}
		// Computed attributes (WP-718, JOB-02 [P2]): the backfill after a
		// change of a computed definition, queued by the type repository
		// and run every minute like the index jobs, and the hourly refresh
		// of the values with today or asset fields, the worker
		// computed_refresh under RETICORA_WORKER_COMPUTED_REFRESH_ENABLED.
		// Both run without a module condition (E-105: computed lies on the
		// G1 path attribute-definitions).
		if typeRepo, ok := d.repos.CIType.(*citype.PGRepository); ok {
			computed := ci.NewComputedRefresh(pool, typeRepo)
			if d.entitlements != nil {
				// No asset source without assets_basic (SR-58).
				computed.WithFeatureCheck(d.entitlements.IsEnabled)
			}
			if d.repos.Jobs != nil {
				computed.WithJobs(jobs.NewService(d.repos.Jobs))
				typeRepo.WithComputedScheduler(computed)
				systemJobs = append(systemJobs, computed.BackfillSchedule())
			}
			if d.cfg.WorkerEnabled(config.WorkerComputedRefresh) {
				systemJobs = append(systemJobs, workerJob(config.WorkerComputedRefresh, computed.Schedule()))
			}
		}
		// Expiry warnings of the entitlements (ENT-07) and expiring
		// collector certificates (COL-04), each notice once (WP-425).
		systemJobs = append(systemJobs, jobs.NewEntitlementNotices(pool).Schedule(),
			jobs.NewCertificateNotices(pool).Schedule())
		// The trial end switches an ended trial to its fallback plan with the
		// limits of that plan (ENT-07, E-157, WP-884).
		if d.entitlements != nil {
			systemJobs = append(systemJobs, operator.NewTrialEnd(pool, d.entitlements).Schedule())
		}
		// Route audit intents no response completed become audit entries
		// with outcome unknown (AUD-07, WP-504).
		systemJobs = append(systemJobs, routeIntentsJob(audit.NewPGRouteAuditor(pool)))
		// Jobs whose worker was lost (crash, rolling deploy) do not stay
		// running (JOB-01, TEC-14).
		if d.repos.Jobs != nil {
			systemJobs = append(systemJobs, jobs.NewReaper(jobs.NewService(d.repos.Jobs),
				jobRequeues(d.jobQueue)).Schedule())
		}
		// Digests of the non-critical notification categories (NTF-04,
		// WP-508).
		if d.notifier != nil {
			systemJobs = append(systemJobs, digestJob(d.notifier))
		}
	}
	return systemJobs
}

// jobRequeues reports the job kinds the reaper puts back into the queue
// after a lost worker, those with an executor that runs queued jobs again:
// the kinds of the job queue on NATS and the attribute index jobs of the
// scheduler and the backfill of computed attributes (WP-718). The other
// kinds fail, among them the in-process jobs without NATS.
func jobRequeues(queue *jobs.Queue) func(kind string) bool {
	return func(kind string) bool {
		switch kind {
		case citype.IndexJobKind, citype.RebuildJobKind, citype.KeyJobKind, ci.BackfillJobKind:
			return true
		}
		return queue != nil && queue.Handles(kind)
	}
}

// digestJob is the scheduler kind notify.digest: every minute, on one
// instance (the scheduler's advisory lock), it queues one delivery per
// channel, recipient, category and window for the digest items of the
// windows that ended; the deliveries go out through the notifier worker.
func digestJob(notifier *notify.Notifier) jobs.Schedule {
	return jobs.Schedule{Kind: notify.DigestKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: 50 * time.Second,
		Run: func(ctx context.Context) error {
			queued, err := notifier.RunDigest(ctx)
			if queued > 0 {
				slog.Info("notification digests queued", "deliveries", queued)
			}
			return err
		}}
}

// routeIntentsJob is the scheduler kind audit.route_intents: every minute,
// on one instance (the scheduler's advisory lock), it turns the route audit
// intents older than audit.RouteIntentGrace into audit entries with outcome
// unknown; an organization whose entries fail keeps its intents for the
// next run. Each run sets reticora_audit_route_intents_pending.
func routeIntentsJob(auditor *audit.PGRouteAuditor) jobs.Schedule {
	return jobs.Schedule{Kind: audit.RouteIntentsKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: 50 * time.Second,
		Run: func(ctx context.Context) error {
			written, err := auditor.SweepRouteIntents(ctx)
			if written > 0 {
				slog.Warn("route audit entries written from intents no response completed", "entries", written,
					"outcome", audit.OutcomeUnknown)
			}
			return err
		}}
}

// workerJob records every run of a system job as a cycle of worker, a
// worker that runs on the scheduler (OPS-05).
func workerJob(worker string, s jobs.Schedule) jobs.Schedule {
	run := s.Run
	s.Run = func(ctx context.Context) error {
		err := run(ctx)
		failures := 0
		if err != nil {
			failures = 1
		}
		observability.ObserveWorkerRun(worker, failures)
		return err
	}
	return s
}

// dekRotator adapts the credential store to the rotation runs.
type dekRotator struct{ repo *credential.PGRepository }

func (d dekRotator) RotateOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (keyVersion, rotated int, err error) {
	res, err := d.repo.RotateOrgDEK(ctx, orgID, enc)
	if err != nil {
		return 0, 0, err
	}
	return res.KeyVersion, res.Rotated, nil
}

func (d dekRotator) RewrapOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (bool, error) {
	return d.repo.RewrapOrgDEK(ctx, orgID, enc)
}

// readOrgDeletionState is the deletion state of an organization for the
// read-only grace period (WP-1153, tenantapi.ReadDeletionState).
func readOrgDeletionState(ctx context.Context, pool *pgxpool.Pool, orgID string) (middleware.OrgDeletionState, error) {
	state, due, err := tenantapi.ReadDeletionState(ctx, pool, orgID)
	return middleware.OrgDeletionState{State: state, DueAt: due}, err
}

// purgeTables is the table catalog of the purge (export.OrgPurgeTables).
func purgeTables(ctx context.Context, tx pgx.Tx) ([]string, error) {
	tables, err := export.OrgPurgeTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tables))
	for i := range tables {
		names = append(names, tables[i].Name)
	}
	return names, nil
}
