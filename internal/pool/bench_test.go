package pool

import (
	"context"
	"net"
	"testing"
	"time"
)

func BenchmarkPool_GetPut(b *testing.B) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						c.Close()
						return
					}
				}
			}(conn)
		}
	}()

	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         50,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := p.Get(ctx)
		if err != nil {
			b.Fatal(err)
		}
		p.Put(conn)
	}
}

func BenchmarkPool_GetPut_Parallel(b *testing.B) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						c.Close()
						return
					}
				}
			}(conn)
		}
	}()

	p := NewPool(PoolConfig{
		Address:         ln.Addr().String(),
		Role:            "primary",
		MaxSize:         50,
		IdleTimeout:     60 * time.Second,
		CleanupInterval: 30 * time.Second,
	})
	defer p.Close()

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := p.Get(ctx)
			if err != nil {
				b.Fatal(err)
			}
			p.Put(conn)
		}
	})
}
