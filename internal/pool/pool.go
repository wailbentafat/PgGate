package pool

import (
	"errors"
	"net"
	"sync"
	"time"
)

type PooledConn struct {
	Conn     net.Conn
	lastUsed time.Time
}

type Pool struct {
	address     string
	connections chan *PooledConn
	maxSize     int
	idleTimeout time.Duration
	mu          sync.Mutex
}

func NewPool(address string, maxSize int, idleTimeout time.Duration) *Pool {
	p := &Pool{
		address:     address,
		maxSize:     maxSize,
		idleTimeout: idleTimeout,
		connections: make(chan *PooledConn, maxSize),
	}

	for i := 0; i < maxSize/2; i++ {
		conn, err := p.createConn()
		if err == nil {
			p.connections <- &PooledConn{Conn: conn, lastUsed: time.Now()}
		}
	}

	go p.cleanupIdleConnections()

	return p
}

func (p *Pool) createConn() (net.Conn, error) {
	return net.Dial("tcp", p.address)
}

func (p *Pool) Get() (net.Conn, error) {
	select {
	case pooled := <-p.connections:
		if !p.isConnAlive(pooled.Conn) {
			pooled.Conn.Close()
			return p.createConn()
		}
		pooled.lastUsed = time.Now()
		return pooled.Conn, nil
	default:
		return p.createConn()
	}
}

func (p *Pool) Put(conn net.Conn) {
	if conn == nil {
		return
	}

	pooled := &PooledConn{Conn: conn, lastUsed: time.Now()}

	select {
	case p.connections <- pooled:
	default:
		conn.Close()
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.connections)
	for c := range p.connections {
		c.Conn.Close()
	}
}

func (p *Pool) isConnAlive(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	// i set here deadline for reading tcp content if i get any data without err i remove the deadline 
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Millisecond))
	var b [1]byte
	_, err := conn.Read(b[:0])
	conn.SetReadDeadline(time.Time{})
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return false
	}
	return true
}

func (p *Pool) cleanupIdleConnections() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
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
				break
			}
		}
	}
}
