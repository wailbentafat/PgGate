package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	ActiveClientConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pggate_active_client_connections",
		Help: "Current number of active client connections",
	})

	TotalQueries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pggate_total_queries_total",
		Help: "Total number of queries handled",
	})

	PrimaryQueries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pggate_primary_queries_total",
		Help: "Total number of queries routed to primary",
	})

	ReplicaQueries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pggate_replica_queries_total",
		Help: "Total number of queries routed to replicas",
	})

	Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pggate_errors_total",
		Help: "Total number of errors by type",
	}, []string{"type"})

	QueryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pggate_query_duration_seconds",
		Help:    "Query latency in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"destination"})

	PoolActiveConnections = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pggate_pool_active_connections",
		Help: "Current active connections per pool",
	}, []string{"role", "address"})

	PoolIdleConnections = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pggate_pool_idle_connections",
		Help: "Current idle connections per pool",
	}, []string{"role", "address"})

	PoolGetDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pggate_pool_get_duration_seconds",
		Help:    "Time to acquire a pool connection",
		Buckets: []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
	}, []string{"role"})

	AuthAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pggate_auth_attempts_total",
		Help: "Authentication attempts",
	}, []string{"status"})

	BackendHealth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pggate_backend_healthy",
		Help: "Whether a backend is healthy (1) or not (0)",
	}, []string{"role", "address"})

	BytesTransferred = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pggate_bytes_transferred_total",
		Help: "Bytes transferred",
	}, []string{"direction"})

	RateLimitRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pggate_rate_limit_rejections_total",
		Help: "Number of connections rejected by rate limiter",
	})
)

func init() {
	prometheus.MustRegister(
		ActiveClientConnections,
		TotalQueries,
		PrimaryQueries,
		ReplicaQueries,
		Errors,
		QueryDuration,
		PoolActiveConnections,
		PoolIdleConnections,
		PoolGetDuration,
		AuthAttempts,
		BackendHealth,
		BytesTransferred,
		RateLimitRejections,
	)
}

func IncActiveConnections()  { ActiveClientConnections.Inc() }
func DecActiveConnections()  { ActiveClientConnections.Dec() }
func IncTotalQueries()       { TotalQueries.Inc() }
func IncPrimaryQueries()     { PrimaryQueries.Inc() }
func IncReplicaQueries()     { ReplicaQueries.Inc() }
func IncErrors(errType string) { Errors.WithLabelValues(errType).Inc() }

func ObserveQueryDuration(dest string, d time.Duration) {
	QueryDuration.WithLabelValues(dest).Observe(d.Seconds())
}

type MetricsServer struct {
	server *http.Server
	ready  bool
}

func NewMetricsServer(addr string) *MetricsServer {
	mux := http.NewServeMux()
	ms := &MetricsServer{
		server: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}

	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if ms.ready {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ready"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready"))
		}
	})

	return ms
}

func (ms *MetricsServer) SetReady(ready bool) {
	ms.ready = ready
}

func (ms *MetricsServer) ListenAndServe() error {
	return ms.server.ListenAndServe()
}

func (ms *MetricsServer) Shutdown(ctx context.Context) error {
	return ms.server.Shutdown(ctx)
}
