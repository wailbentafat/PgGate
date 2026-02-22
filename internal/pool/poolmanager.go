
package pool

import (
	"net"
	"sync"
	"time"
)

type PoolManager struct {
	RWPool *Pool     // primary
	ROPool []*Pool   // replicas
	nextRO int
	mu     sync.Mutex
}

// NewPoolManager initializes primary + replicas
func NewPoolManager(primaryAddr string, replicaAddrs []string, rwSize, roSize int, idleTimeout time.Duration) *PoolManager {
	pm := &PoolManager{
		RWPool: NewPool(primaryAddr, rwSize, idleTimeout),
	}

	for _, addr := range replicaAddrs {
		pm.ROPool = append(pm.ROPool, NewPool(addr, roSize, idleTimeout))
	}

	return pm
}

// GetRW returns a primary (read/write) connection
func (pm *PoolManager) GetRW() (net.Conn, error) {
	return pm.RWPool.Get()
}

// PutRW returns a primary connection to pool
func (pm *PoolManager) PutRW(conn net.Conn) {
	pm.RWPool.Put(conn)
}

// GetRO returns a replica (read-only) connection using round-robin
func (pm *PoolManager) GetRO() (net.Conn, *Pool, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if len(pm.ROPool) == 0 {
		// fallback to primary
		conn, err := pm.RWPool.Get()
		return conn, pm.RWPool, err
	}

	pool := pm.ROPool[pm.nextRO]
	pm.nextRO = (pm.nextRO + 1) % len(pm.ROPool)

	conn, err := pool.Get()
	return conn, pool, err
}

// PutRO returns a replica connection to the pool
func (pm *PoolManager) PutRO(conn net.Conn, pool *Pool) {
	pool.Put(conn)
}

// Close shuts down all pools
func (pm *PoolManager) Close() {
	pm.RWPool.Close()
	for _, p := range pm.ROPool {
		p.Close()
	}
}