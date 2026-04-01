package pool

import (
	"context"
	"net"
	"testing"
	"time"
)

func startMockBackend(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Keep connections alive (don't close immediately)
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					_, err := c.Read(buf)
					if err != nil {
						c.Close()
						return
					}
				}
			}(conn)
		}
	}()
	return ln
}

func TestPool_GetPut(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         5,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx := context.Background()

	conn, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Pool.Get() error = %v", err)
	}
	if conn == nil {
		t.Fatal("Pool.Get() returned nil connection")
	}

	p.Put(conn)

	conn2, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Pool.Get() second call error = %v", err)
	}
	if conn2 == nil {
		t.Fatal("Pool.Get() second call returned nil connection")
	}
	p.Put(conn2)
}

func TestPool_Retries(t *testing.T) {
	p := NewPool(PoolConfig{
		Address:         "127.0.0.1:1",
		Role:            "primary",
		MaxSize:         5,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx := context.Background()
	start := time.Now()
	_, err := p.Get(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Pool.Get() expected error on non-existent port, got nil")
	}

	if elapsed < 200*time.Millisecond {
		t.Errorf("Pool.Get() retry delay too short, took %v", elapsed)
	}
}

func TestPool_ContextCancellation(t *testing.T) {
	p := NewPool(PoolConfig{
		Address:         "127.0.0.1:1",
		Role:            "primary",
		MaxSize:         5,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Get(ctx)
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

func TestPool_MaxSize(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	maxSize := 3
	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         maxSize,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx := context.Background()

	// Acquire all connections (pre-warmed + new)
	conns := make([]net.Conn, 0, maxSize)
	for i := 0; i < maxSize; i++ {
		conn, err := p.Get(ctx)
		if err != nil {
			t.Fatalf("Get() #%d error: %v", i, err)
		}
		conns = append(conns, conn)
	}

	// Active count should equal maxSize
	if p.ActiveCount() != int64(maxSize) {
		t.Errorf("ActiveCount = %d, want %d", p.ActiveCount(), maxSize)
	}

	// Return them all
	for _, c := range conns {
		p.Put(c)
	}

	if p.ActiveCount() != 0 {
		t.Errorf("ActiveCount after Put = %d, want 0", p.ActiveCount())
	}
}

func TestPool_Close(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         5,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})

	ctx := context.Background()
	conn, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Put(conn)

	p.Close()

	if !p.IsClosed() {
		t.Error("pool should be closed")
	}

	// Double close should not panic
	p.Close()
}

func TestPool_HealthCheck(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         5,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	if !p.Healthy() {
		t.Error("new pool should be healthy")
	}

	ctx := context.Background()
	if !p.CheckHealth(ctx) {
		t.Error("health check should pass for running backend")
	}
}

func TestPool_CircuitBreaker(t *testing.T) {
	p := NewPool(PoolConfig{
		Address:         "127.0.0.1:1",
		Role:            "replica",
		MaxSize:         5,
		IdleTimeout:     1 * time.Minute,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	// Record failures to trip circuit breaker
	p.RecordFailure()
	p.RecordFailure()
	p.RecordFailure()

	if p.Healthy() {
		t.Error("pool should be unhealthy after 3 failures")
	}

	// Recovery
	p.RecordSuccess()
	if !p.Healthy() {
		t.Error("pool should recover after success")
	}
}

func TestPool_IdleCleanup(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         10,
		IdleTimeout:     100 * time.Millisecond,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx := context.Background()
	conn, _ := p.Get(ctx)
	p.Put(conn)

	// Verify connection is in pool
	if p.IdleCount() == 0 {
		t.Error("expected at least one idle connection")
	}
}
