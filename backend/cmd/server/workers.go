package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
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
	// cycle replaces the intervals of the polling workers and of the
	// cycles of the workers that answer requests (tests); 0 keeps them.
	cycle time.Duration
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
			exportWorker.Events = emitterOf(d.pool)
		}
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
		publisher := events.NewPublisher(d.pool, d.eventStream, d.dispatcher,
			notify.NewEventConsumer(notify.NewPGResolver(d.pool, permission.NewPGRepository(d.pool)), d.notifier))
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
		workers = append(workers, server.Worker{Name: config.WorkerJobs, Run: d.jobQueue.Run, Backlog: d.jobBacklog})
	}
	// Consume ingest.> (REC-01).
	if d.ingestQueue != nil {
		workers = append(workers, server.Worker{Name: config.WorkerIngest, Run: d.ingestQueue.Run, Backlog: d.ingestQueue.Backlog})
	}
	// Evaluate monitoring alert rules against the metric store (module
	// alerting, MON-04). Fired alerts are logged via the default notifier;
	// evaluation state is persisted in the alert store so restarts neither
	// re-notify nor lose pending durations.
	if alertStore, ok := d.repos.Metrics.(monitoring.EvaluatingStore); ok {
		evaluator := monitoring.NewEvaluator(d.repos.Metrics, alertStore.AlertStore(), nil)
		workers = append(workers, server.Worker{
			Name: config.WorkerMonitoring,
			Run:  func(ctx context.Context) { evaluator.Run(ctx, d.every(time.Minute)) },
		})
	}
	// The health of the CIs follows the monitoring state every minute
	// (DB-05, MON-02): a worker of its own, so it runs in the G1 default
	// configuration without the module alerting (F-087).
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
// module, is enabled (WP-214).
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
	// notifier bundles the digests (notify.digest); nil: no digest run.
	notifier *notify.Notifier
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
	// inventory module (WP-214).
	var systemJobs []jobs.Schedule
	if d.modules.Enabled("inventory") {
		systemJobs = append(systemJobs, reservation.NewSweeper(d.repos.Reservation, d.dispatcher).Schedule())
	}
	// Retention runs per kind of data (NFR-06, AUD-09) and key rotation
	// (SEC-06).
	if pool := d.pool; pool != nil {
		systemJobs = append(systemJobs, jobs.NewRotation(pool, d.encryptor, dekRotator{repo: credential.NewPGRepository(pool)},
			webhook.NewPGRepository(pool)).Schedules()...)
		systemJobs = append(systemJobs, jobs.NewRetention(pool, jobs.RetentionFromDays(d.cfg.RetentionAuditYears,
			d.cfg.RetentionHistoryDays, d.cfg.RetentionSoftDeleteDays, d.cfg.RetentionReviewItemDays)).Schedules()...)
		// Collector status: offline after five minutes without heartbeat,
		// paused while the discovery license is not active (COL-01, COL-05).
		collectorStatus := discovery.NewStatusMonitor(pool)
		collectorStatus.Licensed = func(ctx context.Context, orgID string) (bool, error) {
			_, status, serr := d.entitlements.Status(ctx, orgID, entitlement.FeatureDiscovery)
			return status == entitlement.StatusActive, serr
		}
		systemJobs = append(systemJobs, collectorStatus.Schedule())
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
		// Personal data of existing history entries into the vault (SEC-05).
		systemJobs = append(systemJobs, jobs.NewPIIBackfill(pool).Schedule())
		// Attribute indexes of searchable/unique attributes (DB-04, MET-15).
		if typeRepo, ok := d.repos.CIType.(*citype.PGRepository); ok && d.repos.Jobs != nil {
			ddlJob := citype.NewDDLJob(pool, jobs.NewService(d.repos.Jobs))
			typeRepo.WithIndexScheduler(ddlJob)
			systemJobs = append(systemJobs, ddlJob.Schedule())
		}
		// Expiry warnings of the entitlements (ENT-07) and expiring
		// collector certificates (COL-04), each notice once (WP-425).
		systemJobs = append(systemJobs, jobs.NewEntitlementNotices(pool).Schedule(),
			jobs.NewCertificateNotices(pool).Schedule())
		// Route audit intents no response completed become audit entries
		// with outcome unknown (AUD-07, WP-504).
		systemJobs = append(systemJobs, routeIntentsJob(audit.NewPGRouteAuditor(pool)))
		// Jobs whose worker was lost (crash, rolling deploy) do not stay
		// running (JOB-01, TEC-14).
		if d.repos.Jobs != nil {
			systemJobs = append(systemJobs, jobs.NewReaper(jobs.NewService(d.repos.Jobs).WithEvents(emitterOf(pool)),
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
// scheduler. The other kinds fail, among them the in-process jobs without
// NATS.
func jobRequeues(queue *jobs.Queue) func(kind string) bool {
	return func(kind string) bool {
		switch kind {
		case citype.IndexJobKind, citype.RebuildJobKind, citype.KeyJobKind:
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
