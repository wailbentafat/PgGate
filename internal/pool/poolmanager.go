package pool

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/user/pggate/internal/logging"
)

type PoolManager struct {
	RWPool        *Pool
	ROPool        []*Pool
	nextRO        int
	mu            sync.Mutex
	done          chan struct{}
	maxReplicaLag int64
}

type PoolManagerConfig struct {
	PrimaryAddr     string
	ReplicaAddrs    []string
	PrimarySize     int
	ReplicaSize     int
	IdleTimeout     time.Duration
	CleanupInterval time.Duration
	TLSConfig       *tls.Config
	MaxReplicaLag   int64 // seconds; 0 disables lag detection
}

func NewPoolManager(cfg PoolManagerConfig) *PoolManager {
	pm := &PoolManager{
		maxReplicaLag: cfg.MaxReplicaLag,
		RWPool: NewPool(PoolConfig{
			Address:         cfg.PrimaryAddr,
			Role:            "primary",
			MaxSize:         cfg.PrimarySize,
			IdleTimeout:     cfg.IdleTimeout,
			CleanupInterval: cfg.CleanupInterval,
			TLSConfig:       cfg.TLSConfig,
		}),
		done: make(chan struct{}),
	}

	for _, addr := range cfg.ReplicaAddrs {
		pm.ROPool = append(pm.ROPool, NewPool(PoolConfig{
			Address:         addr,
			Role:            "replica",
			MaxSize:         cfg.ReplicaSize,
			IdleTimeout:     cfg.IdleTimeout,
			CleanupInterval: cfg.CleanupInterval,
			TLSConfig:       cfg.TLSConfig,
		}))
	}

	go pm.healthCheckLoop()

	return pm
}

func (pm *PoolManager) GetRW(ctx context.Context) (net.Conn, error) {
	return pm.RWPool.Get(ctx)
}

func (pm *PoolManager) PutRW(conn net.Conn) {
	pm.RWPool.Put(conn)
}

func (pm *PoolManager) GetRO(ctx context.Context) (net.Conn, *Pool, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if len(pm.ROPool) == 0 {
		conn, err := pm.RWPool.Get(ctx)
		return conn, pm.RWPool, err
	}

	// Try healthy replicas first, round-robin
	tried := 0
	for tried < len(pm.ROPool) {
		pool := pm.ROPool[pm.nextRO]
		pm.nextRO = (pm.nextRO + 1) % len(pm.ROPool)
		tried++

		if !pool.Healthy() {
			continue
		}

		conn, err := pool.Get(ctx)
		if err != nil {
			pool.RecordFailure()
			logging.L().Warn("replica get failed, trying next", "address", pool.Address(), "error", err)
			continue
		}
		return conn, pool, nil
	}

	// All replicas unhealthy — fallback to primary
	logging.L().Warn("all replicas unhealthy, falling back to primary")
	conn, err := pm.RWPool.Get(ctx)
	return conn, pm.RWPool, err
}

func (pm *PoolManager) PutRO(conn net.Conn, pool *Pool) {
	pool.Put(conn)
}

func (pm *PoolManager) Close() {
	close(pm.done)
	pm.RWPool.Close()
	for _, p := range pm.ROPool {
		p.Close()
	}
}

func (pm *PoolManager) healthCheckLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pm.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			pm.RWPool.CheckHealth(ctx)
			for _, p := range pm.ROPool {
				if !p.CheckHealth(ctx) {
					continue
				}
				// If lag detection is enabled, check replica lag
				if pm.maxReplicaLag > 0 {
					pm.checkReplicaLag(p)
				}
			}
			cancel()
		}
	}
}

func (pm *PoolManager) checkReplicaLag(p *Pool) {
	conn, err := net.DialTimeout("tcp", p.Address(), 3*time.Second)
	if err != nil {
		return // already handled by CheckHealth
	}
	defer conn.Close()

	// We need an authenticated connection to query lag.
	// Use a pooled connection instead if available.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	poolConn, err := p.Get(ctx)
	if err != nil {
		return
	}
	defer p.Put(poolConn)

	lagSeconds, err := QueryReplicaLagBytes(poolConn, 3*time.Second)
	if err != nil {
		logging.L().Debug("replica lag check failed", "address", p.Address(), "error", err)
		return
	}

	if lagSeconds < 0 {
		// Cannot determine lag (not a replica or WAL not received yet)
		return
	}

	if lagSeconds > pm.maxReplicaLag {
		logging.L().Warn("replica lag too high, marking unhealthy",
			"address", p.Address(), "lag_seconds", lagSeconds, "max", pm.maxReplicaLag)
		p.SetHealthy(false)
	} else if !p.Healthy() {
		// Lag recovered
		logging.L().Info("replica lag recovered", "address", p.Address(), "lag_seconds", lagSeconds)
		p.SetHealthy(true)
	}
}

func (pm *PoolManager) ReloadReplicas(addrs []string, replicaSize int, idleTimeout, cleanupInterval time.Duration, tlsCfg *tls.Config) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	existing := make(map[string]*Pool)
	for _, p := range pm.ROPool {
		existing[p.Address()] = p
	}

	newPools := make([]*Pool, 0, len(addrs))
	for _, addr := range addrs {
		if p, ok := existing[addr]; ok {
			newPools = append(newPools, p)
			delete(existing, addr)
		} else {
			newPools = append(newPools, NewPool(PoolConfig{
				Address:         addr,
				Role:            "replica",
				MaxSize:         replicaSize,
				IdleTimeout:     idleTimeout,
				CleanupInterval: cleanupInterval,
				TLSConfig:       tlsCfg,
			}))
		}
	}

	// Close removed pools
	for _, p := range existing {
		p.Close()
	}

	pm.ROPool = newPools
	pm.nextRO = 0

	logging.L().Info("replicas reloaded", "count", len(newPools))
}

// HealthStatus returns a summary of pool health for use in health check endpoints.
func (pm *PoolManager) HealthStatus() map[string]bool {
	status := make(map[string]bool)
	status[fmt.Sprintf("primary:%s", pm.RWPool.Address())] = pm.RWPool.Healthy()
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, p := range pm.ROPool {
		status[fmt.Sprintf("replica:%s", p.Address())] = p.Healthy()
	}
	return status
}
