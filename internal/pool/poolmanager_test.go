package pool

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPoolManager_RoundRobin(t *testing.T) {
	ln1 := startMockBackend(t)
	defer ln1.Close()
	ln2 := startMockBackend(t)
	defer ln2.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln1.Addr().String(),
		ReplicaAddrs:    []string{ln1.Addr().String(), ln2.Addr().String()},
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	ctx := context.Background()

	// Get two RO connections — should round-robin between replicas
	conn1, pool1, err := pm.GetRO(ctx)
	if err != nil {
		t.Fatalf("GetRO() #1 error: %v", err)
	}
	pm.PutRO(conn1, pool1)

	conn2, pool2, err := pm.GetRO(ctx)
	if err != nil {
		t.Fatalf("GetRO() #2 error: %v", err)
	}
	pm.PutRO(conn2, pool2)

	if pool1.Address() == pool2.Address() {
		t.Error("expected round-robin to different replicas")
	}
}

func TestPoolManager_FallbackToPrimary(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln.Addr().String(),
		ReplicaAddrs:    []string{}, // no replicas
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	ctx := context.Background()
	conn, _, err := pm.GetRO(ctx)
	if err != nil {
		t.Fatalf("GetRO() should fallback to primary: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection from primary fallback")
	}
	pm.PutRW(conn)
}

func TestPoolManager_UnhealthyReplicaSkipped(t *testing.T) {
	ln1 := startMockBackend(t)
	defer ln1.Close()
	ln2 := startMockBackend(t)
	defer ln2.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln1.Addr().String(),
		ReplicaAddrs:    []string{ln1.Addr().String(), ln2.Addr().String()},
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	// Mark first replica as unhealthy
	pm.ROPool[0].SetHealthy(false)

	ctx := context.Background()
	conn, p, err := pm.GetRO(ctx)
	if err != nil {
		t.Fatalf("GetRO() error: %v", err)
	}
	defer pm.PutRO(conn, p)

	// Should have gotten from the healthy replica (index 1)
	if p.Address() != ln2.Addr().String() {
		t.Errorf("expected connection from healthy replica %s, got %s", ln2.Addr().String(), p.Address())
	}
}

func TestPoolManager_HealthStatus(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln.Addr().String(),
		ReplicaAddrs:    []string{ln.Addr().String()},
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	status := pm.HealthStatus()
	for name, healthy := range status {
		if !healthy {
			t.Errorf("backend %s should be healthy", name)
		}
	}
}

func TestPoolManager_ReloadReplicas(t *testing.T) {
	ln1 := startMockBackend(t)
	defer ln1.Close()
	ln2 := startMockBackend(t)
	defer ln2.Close()
	ln3 := startMockBackend(t)
	defer ln3.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln1.Addr().String(),
		ReplicaAddrs:    []string{ln2.Addr().String()},
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	// Verify 1 replica
	if len(pm.ROPool) != 1 {
		t.Fatalf("expected 1 replica, got %d", len(pm.ROPool))
	}

	// Reload with 2 replicas
	pm.ReloadReplicas(
		[]string{ln2.Addr().String(), ln3.Addr().String()},
		5, 60*time.Second, 30*time.Second, nil,
	)

	if len(pm.ROPool) != 2 {
		t.Fatalf("expected 2 replicas after reload, got %d", len(pm.ROPool))
	}
}

func TestPoolManager_GetRW(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln.Addr().String(),
		ReplicaAddrs:    []string{},
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	ctx := context.Background()
	conn, err := pm.GetRW(ctx)
	if err != nil {
		t.Fatalf("GetRW() error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	pm.PutRW(conn)
}

// Integration test: simulate a proxy-like flow
func TestPoolManager_ProxyFlow(t *testing.T) {
	primary := startMockBackend(t)
	defer primary.Close()
	replica := startMockBackend(t)
	defer replica.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     primary.Addr().String(),
		ReplicaAddrs:    []string{replica.Addr().String()},
		PrimarySize:     5,
		ReplicaSize:     5,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	ctx := context.Background()

	// Simulate: auth goes to primary
	rwConn, err := pm.GetRW(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate: SELECT goes to replica
	roConn, roPool, err := pm.GetRO(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate: write data
	_, err = rwConn.Write([]byte("INSERT INTO test VALUES (1)"))
	if err != nil {
		t.Fatal(err)
	}

	// Simulate: read data
	_, err = roConn.Write([]byte("SELECT * FROM test"))
	if err != nil {
		t.Fatal(err)
	}

	// Return connections
	pm.PutRW(rwConn)
	pm.PutRO(roConn, roPool)

	// Verify connections are back in pool
	if pm.RWPool.IdleCount() == 0 {
		t.Error("expected idle RW connection after put")
	}
}

// Concurrent access test
func TestPoolManager_Concurrent(t *testing.T) {
	ln := startMockBackend(t)
	defer ln.Close()

	pm := NewPoolManager(PoolManagerConfig{
		PrimaryAddr:     ln.Addr().String(),
		ReplicaAddrs:    []string{ln.Addr().String()},
		PrimarySize:     10,
		ReplicaSize:     10,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer pm.Close()

	ctx := context.Background()
	done := make(chan struct{})

	for i := 0; i < 20; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 10; j++ {
				var conn net.Conn
				var err error
				var p *Pool

				if j%2 == 0 {
					conn, err = pm.GetRW(ctx)
					if err == nil {
						pm.PutRW(conn)
					}
				} else {
					conn, p, err = pm.GetRO(ctx)
					if err == nil {
						pm.PutRO(conn, p)
					}
				}
			}
		}()
	}

	for i := 0; i < 20; i++ {
		<-done
	}
}
