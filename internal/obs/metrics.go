package obs

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Labels only take values from small fixed sets (route patterns, statuses,
// actions): never user IDs, job IDs or SQL.
var (
	Registry = prometheus.NewRegistry()

	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbmp_http_requests_total", Help: "HTTP requests by route pattern and status code.",
	}, []string{"route", "method", "code"})
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "dbmp_http_request_duration_seconds", Help: "HTTP request duration.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"route", "method"})

	JobsCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbmp_jobs_completed_total", Help: "Finished migration jobs by result.",
	}, []string{"result"})
	JobRetries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "dbmp_job_retries_total", Help: "Job attempts put back in the queue after a transient failure.",
	})
	QueueWait = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "dbmp_queue_wait_seconds", Help: "Time a job waited in the queue before a worker claimed it.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 14),
	})
	LockWait = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "dbmp_lock_wait_seconds", Help: "Time to take the per-database migration lock.",
		Buckets: prometheus.ExponentialBuckets(0.001, 4, 10),
	})
	MigrationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "dbmp_migration_duration_seconds", Help: "Duration of one migration attempt.",
		Buckets: prometheus.ExponentialBuckets(0.01, 2.5, 14),
	}, []string{"action", "mode", "result"})
	Batches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbmp_migration_batches_total", Help: "Committed migration batches.",
	}, []string{"direction"})
	Rows = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbmp_migration_rows_total", Help: "Rows changed by committed migration batches.",
	}, []string{"direction"})
)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, JobsCompleted, JobRetries, QueueWait, LockWait, MigrationDuration, Batches, Rows)
}

// ServeMetrics exposes /metrics in Prometheus format on its own address, so
// it is not reachable through the public application port.
func ServeMetrics(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	go func() {
		srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("metrics server stopped", "event", "metrics.error", "error", err)
		}
	}()
}

// PoolStats returns the statistics of one or more pgx pools.
type PoolStats func() []*pgxpool.Stat

// poolCollector reports connection pool state with a "pool" label
// (platform or target; target pools are summed).
type poolCollector struct {
	pools                                 map[string]PoolStats
	conns, waitTotal, acquires, emptyWait *prometheus.Desc
}

// RegisterPools adds connection pool metrics.
func RegisterPools(pools map[string]PoolStats) {
	Registry.MustRegister(&poolCollector{
		pools:     pools,
		conns:     prometheus.NewDesc("dbmp_db_pool_connections", "Connections by state.", []string{"pool", "state"}, nil),
		waitTotal: prometheus.NewDesc("dbmp_db_pool_acquire_wait_seconds_total", "Total time spent waiting for a connection.", []string{"pool"}, nil),
		acquires:  prometheus.NewDesc("dbmp_db_pool_acquires_total", "Connection acquisitions.", []string{"pool"}, nil),
		emptyWait: prometheus.NewDesc("dbmp_db_pool_empty_acquires_total", "Acquisitions that had to wait because the pool was empty.", []string{"pool"}, nil),
	})
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.conns
	ch <- c.waitTotal
	ch <- c.acquires
	ch <- c.emptyWait
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	for name, stats := range c.pools {
		var acquired, idle, total, acquires, empty int64
		var wait time.Duration
		for _, s := range stats() {
			acquired += int64(s.AcquiredConns())
			idle += int64(s.IdleConns())
			total += int64(s.TotalConns())
			acquires += s.AcquireCount()
			empty += s.EmptyAcquireCount()
			wait += s.AcquireDuration()
		}
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(acquired), name, "active")
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(idle), name, "idle")
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(total), name, "total")
		ch <- prometheus.MustNewConstMetric(c.waitTotal, prometheus.CounterValue, wait.Seconds(), name)
		ch <- prometheus.MustNewConstMetric(c.acquires, prometheus.CounterValue, float64(acquires), name)
		ch <- prometheus.MustNewConstMetric(c.emptyWait, prometheus.CounterValue, float64(empty), name)
	}
}

// jobCollector reports the number of jobs per status from the queue table.
type jobCollector struct {
	counts func(context.Context) (map[string]int, error)
	desc   *prometheus.Desc
}

// RegisterJobCounts adds the dbmp_jobs{status} gauge (queued, running, done, failed).
func RegisterJobCounts(counts func(context.Context) (map[string]int, error)) {
	Registry.MustRegister(&jobCollector{counts: counts,
		desc: prometheus.NewDesc("dbmp_jobs", "Migration jobs by status.", []string{"status"}, nil)})
}

func (c *jobCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *jobCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	counts, err := c.counts(ctx)
	if err != nil {
		return
	}
	for status, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n), status)
	}
}
