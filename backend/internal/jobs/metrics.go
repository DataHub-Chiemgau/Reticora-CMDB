package jobs

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Job metrics (JOB-03): duration and failures per kind are observed when a
// job ends; backlog and the age of the oldest queued job are read from the
// job table on every scrape.
var (
	// JobDuration is the run time of finished jobs by kind and status.
	JobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "reticora",
		Name:      "job_duration_seconds",
		Help:      "Run time of finished jobs from start to end by kind and final status.",
		Buckets:   []float64{0.1, 0.5, 1, 5, 15, 60, 300, 900, 3600},
	}, []string{"kind", "status"})
	// JobFailures counts failed attempts by kind (retried or final).
	JobFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "job_failures_total",
		Help:      "Failed job attempts by kind, retried or final.",
	}, []string{"kind"})
)

var (
	backlogMu     sync.RWMutex
	backlogSource Repository
)

// SetBacklogSource makes the backlog metrics read repo on every scrape.
func SetBacklogSource(repo Repository) {
	backlogMu.Lock()
	defer backlogMu.Unlock()
	backlogSource = repo
}

// backlogCollector reports the queued jobs and the age of the oldest
// queued job per kind.
type backlogCollector struct {
	backlog *prometheus.Desc
	oldest  *prometheus.Desc
}

func newBacklogCollector() *backlogCollector {
	return &backlogCollector{
		backlog: prometheus.NewDesc("reticora_job_backlog", "Queued jobs by kind.", []string{"kind"}, nil),
		oldest: prometheus.NewDesc("reticora_job_oldest_queued_age_seconds",
			"Age of the oldest queued job by kind.", []string{"kind"}, nil),
	}
}

func (c *backlogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.backlog
	ch <- c.oldest
}

// maxBacklogScan bounds the queued jobs one scrape reads.
const maxBacklogScan = 10000

func (c *backlogCollector) Collect(ch chan<- prometheus.Metric) {
	backlogMu.RLock()
	repo := backlogSource
	backlogMu.RUnlock()
	if repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	now := time.Now().UTC()
	// Every queued job counts, also one whose next attempt is not due yet.
	queued, err := repo.QueuedDue(ctx, now.Add(100*365*24*time.Hour), maxBacklogScan)
	if err != nil {
		return
	}
	count, oldest := backlogByKind(queued, now)
	for kind, n := range count {
		ch <- prometheus.MustNewConstMetric(c.backlog, prometheus.GaugeValue, float64(n), kind)
		ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, oldest[kind], kind)
	}
}

// backlogByKind counts queued jobs and the age of the oldest per kind.
func backlogByKind(queued []Job, now time.Time) (count map[string]int, oldest map[string]float64) {
	count, oldest = map[string]int{}, map[string]float64{}
	for i := range queued {
		kind := queued[i].Kind
		count[kind]++
		if age := now.Sub(queued[i].CreatedAt).Seconds(); age > oldest[kind] {
			oldest[kind] = age
		}
	}
	return count, oldest
}

// RegisterMetrics registers the job metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(JobDuration, JobFailures, newBacklogCollector())
}

// observeEnd records a job that reached a final status.
func observeEnd(job *Job) {
	if job == nil || !job.Status.Terminal() || job.StartedAt == nil || job.FinishedAt == nil {
		return
	}
	JobDuration.WithLabelValues(job.Kind, string(job.Status)).Observe(job.FinishedAt.Sub(*job.StartedAt).Seconds())
}
