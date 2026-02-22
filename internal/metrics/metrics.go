package metrics

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

type Metrics struct {
	ActiveClientConnections  int64
	TotalQueries             int64
	PrimaryQueries           int64
	ReplicaQueries           int64
	Errors                   int64
	BackendRWConnectionsOpen int64
	BackendROConnectionsOpen int64
}

var (
	GlobalMetrics = &Metrics{}
	mu            sync.Mutex
)

func IncActiveConnections() {
	atomic.AddInt64(&GlobalMetrics.ActiveClientConnections, 1)
}

func DecActiveConnections() {
	atomic.AddInt64(&GlobalMetrics.ActiveClientConnections, -1)
}

func IncTotalQueries() {
	atomic.AddInt64(&GlobalMetrics.TotalQueries, 1)
}

func IncPrimaryQueries() {
	atomic.AddInt64(&GlobalMetrics.PrimaryQueries, 1)
}

func IncReplicaQueries() {
	atomic.AddInt64(&GlobalMetrics.ReplicaQueries, 1)
}

func IncErrors() {
	atomic.AddInt64(&GlobalMetrics.Errors, 1)
}

func ServeMetrics(addr string) error {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "# HELP pggate_active_client_connections Current number of active client connections\n")
		fmt.Fprintf(w, "# TYPE pggate_active_client_connections gauge\n")
		fmt.Fprintf(w, "pggate_active_client_connections %d\n", atomic.LoadInt64(&GlobalMetrics.ActiveClientConnections))

		fmt.Fprintf(w, "# HELP pggate_total_queries_total Total number of queries handled\n")
		fmt.Fprintf(w, "# TYPE pggate_total_queries_total counter\n")
		fmt.Fprintf(w, "pggate_total_queries_total %d\n", atomic.LoadInt64(&GlobalMetrics.TotalQueries))

		fmt.Fprintf(w, "# HELP pggate_primary_queries_total Total number of queries routed to primary\n")
		fmt.Fprintf(w, "# TYPE pggate_primary_queries_total counter\n")
		fmt.Fprintf(w, "pggate_primary_queries_total %d\n", atomic.LoadInt64(&GlobalMetrics.PrimaryQueries))

		fmt.Fprintf(w, "# HELP pggate_replica_queries_total Total number of queries routed to replicas\n")
		fmt.Fprintf(w, "# TYPE pggate_replica_queries_total counter\n")
		fmt.Fprintf(w, "pggate_replica_queries_total %d\n", atomic.LoadInt64(&GlobalMetrics.ReplicaQueries))

		fmt.Fprintf(w, "# HELP pggate_errors_total Total number of errors encountered\n")
		fmt.Fprintf(w, "# TYPE pggate_errors_total counter\n")
		fmt.Fprintf(w, "pggate_errors_total %d\n", atomic.LoadInt64(&GlobalMetrics.Errors))
	})

	return http.ListenAndServe(addr, nil)
}
