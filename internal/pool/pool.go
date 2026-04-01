package pool

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/pggate/internal/logging"
	"github.com/user/pggate/internal/metrics"
)

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 8192)
		return &b
	},
}

func GetBuf() *[]byte  { return bufPool.Get().(*[]byte) }
func PutBuf(b *[]byte) { bufPool.Put(b) }

type PooledConn struct {
	Conn     net.Conn
	lastUsed time.Time
}

type Pool struct {
	address         string
	role            string // "primary" or "replica"
	connections     chan *PooledConn
	maxSize         int
	idleTimeout     time.Duration
	cleanupInterval time.Duration
	activeCount     atomic.Int64
	tlsConfig       *tls.Config

	mu     sync.Mutex
	closed bool
	done   chan struct{}

	// Circuit breaker
	healthy      atomic.Bool
	failures     atomic.Int64
	lastCheck    atomic.Int64 // unix nano
	failThreshold int64
}

type PoolConfig struct {
	Address         string
	Role            string
	MaxSize         int
	IdleTimeout     time.Duration
	CleanupInterval time.Duration
	TLSConfig       *tls.Config
}

func NewPool(cfg PoolConfig) *Pool {
	p := &Pool{
		address:         cfg.Address,
		role:            cfg.Role,
		maxSize:         cfg.MaxSize,
		idleTimeout:     cfg.IdleTimeout,
		cleanupInterval: cfg.CleanupInterval,
		connections:     make(chan *PooledConn, cfg.MaxSize),
		done:            make(chan struct{}),
		tlsConfig:       cfg.TLSConfig,
		failThreshold:   3,
	}
	p.healthy.Store(true)

	// Pre-warm half the pool
	for i := 0; i < cfg.MaxSize/2; i++ {
		conn, err := p.createConn()
		if err != nil {
			logging.L().Warn("pool pre-warm failed", "address", cfg.Address, "error", err)
			break
		}
		p.connections <- &PooledConn{Conn: conn, lastUsed: time.Now()}
	}

	p.updateMetrics()
	go p.cleanupIdleConnections()

	return p
}

func (p *Pool) Address() string { return p.address }
func (p *Pool) Role() string    { return p.role }
func (p *Pool) Healthy() bool   { return p.healthy.Load() }

func (p *Pool) SetHealthy(h bool) {
	was := p.healthy.Swap(h)
	if was != h {
		v := float64(0)
		if h {
			v = 1
			p.failures.Store(0)
		}
		metrics.BackendHealth.WithLabelValues(p.role, p.address).Set(v)
		logging.L().Info("backend health changed", "address", p.address, "healthy", h)
	}
}

func (p *Pool) RecordFailure() {
	count := p.failures.Add(1)
	if count >= p.failThreshold {
		p.SetHealthy(false)
	}
}

func (p *Pool) RecordSuccess() {
	p.failures.Store(0)
	p.SetHealthy(true)
}

func (p *Pool) createConn() (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", p.address, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if p.tlsConfig != nil {
		tlsConn := tls.Client(conn, p.tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake failed: %w", err)
		}
		return tlsConn, nil
	}
	return conn, nil
}

func (p *Pool) Get(ctx context.Context) (net.Conn, error) {
	start := time.Now()
	defer func() {
		metrics.PoolGetDuration.WithLabelValues(p.role).Observe(time.Since(start).Seconds())
	}()

	maxRetries := 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Try to get from pool
		select {
		case pooled := <-p.connections:
			if !p.isConnAlive(pooled.Conn) {
				pooled.Conn.Close()
				conn, err := p.createConn()
				if err != nil {
					lastErr = err
					p.RecordFailure()
					break // retry
				}
				p.RecordSuccess()
				p.activeCount.Add(1)
				p.updateMetrics()
				return conn, nil
			}
			pooled.lastUsed = time.Now()
			p.activeCount.Add(1)
			p.updateMetrics()
			p.RecordSuccess()
			return pooled.Conn, nil
		default:
			// No idle connection — check if we can create a new one
			// Only create if under maxSize
			total := int(p.activeCount.Load()) + len(p.connections)
			if total < p.maxSize {
				conn, err := p.createConn()
				if err != nil {
					lastErr = err
					p.RecordFailure()
					break // retry
				}
				p.RecordSuccess()
				p.activeCount.Add(1)
				p.updateMetrics()
				return conn, nil
			}
			// Pool exhausted — wait briefly for a return
			lastErr = fmt.Errorf("pool exhausted (max %d)", p.maxSize)
		}

		if i < maxRetries-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil, fmt.Errorf("failed to get connection after %d retries: %w", maxRetries, lastErr)
}

func (p *Pool) Put(conn net.Conn) {
	if conn == nil {
		return
	}

	p.activeCount.Add(-1)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		conn.Close()
		p.updateMetrics()
		return
	}
	p.mu.Unlock()

	pooled := &PooledConn{Conn: conn, lastUsed: time.Now()}

	select {
	case p.connections <- pooled:
	default:
		conn.Close()
	}
	p.updateMetrics()
}

func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.done)
	p.mu.Unlock()

	// Drain all idle connections
	for {
		select {
		case pooled := <-p.connections:
			pooled.Conn.Close()
		default:
			return
		}
	}
}

func (p *Pool) isConnAlive(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Millisecond))
	var b [1]byte
	_, err := conn.Read(b[:])
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return true // timeout means connection is alive, just no data
		}
		return false
	}
	return true
}

func (p *Pool) cleanupIdleConnections() {
	ticker := time.NewTicker(p.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			n := len(p.connections)
			for i := 0; i < n; i++ {
				select {
				case pooled := <-p.connections:
					if time.Since(pooled.lastUsed) > p.idleTimeout {
						pooled.Conn.Close()
					} else {
						p.connections <- pooled
					}
				default:
					goto done
				}
			}
		done:
			p.updateMetrics()
		}
	}
}

func (p *Pool) updateMetrics() {
	idle := float64(len(p.connections))
	active := float64(p.activeCount.Load())
	metrics.PoolIdleConnections.WithLabelValues(p.role, p.address).Set(idle)
	metrics.PoolActiveConnections.WithLabelValues(p.role, p.address).Set(active)
}

func (p *Pool) CheckHealth(ctx context.Context) bool {
	conn, err := net.DialTimeout("tcp", p.address, 3*time.Second)
	if err != nil {
		p.SetHealthy(false)
		return false
	}
	conn.Close()
	p.SetHealthy(true)
	return true
}

// IdleCount returns the number of idle connections in the pool.
func (p *Pool) IdleCount() int {
	return len(p.connections)
}

// ActiveCount returns the number of active (checked-out) connections.
func (p *Pool) ActiveCount() int64 {
	return p.activeCount.Load()
}

// IsClosed reports whether the pool has been closed.
func (p *Pool) IsClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
