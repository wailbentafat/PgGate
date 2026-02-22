package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/user/pggate/internal/config"
	"github.com/user/pggate/internal/listener"
	"github.com/user/pggate/internal/pool"
	"github.com/user/pggate/internal/proxy"
	"github.com/user/pggate/internal/router"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
		panic(err)
	}
	primary := cfg.Backend.Primary.Address
	replicas := make([]string, len(cfg.Backend.Replicas))
	for i, r := range cfg.Backend.Replicas {
		replicas[i] = r.Address
	}
	pm := pool.NewPoolManager(
		primary,
		replicas,
		cfg.Pool.PrimarySize,
		cfg.Pool.ReplicaSize,
		60,
	)
	r := router.NewRouter()
	p := proxy.NewProxy(pm, r)
	l := listener.NewServer(listener.ListenerConfig{
		Address:        cfg.Listener.Address,
		MaxConnections: cfg.Listener.MaxConnections,
		ReadTimeout:    cfg.Listener.ReadTimeout,
		WriteTimeout:   cfg.Listener.WriteTimeout,
	}, p)
	go func() {
		if err := l.Start(); err != nil {
			log.Fatalf("failed to start listener: %v", err)
		}
	}()

	log.Printf("PgGate listening on %s", cfg.Listener.Address)
	log.Printf("Primary: %s, Replicas: %v", primary, replicas)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down PgGate...")
	l.Stop()
}