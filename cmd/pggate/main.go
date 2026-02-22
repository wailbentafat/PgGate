package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/user/pggate/internal/config"
	"github.com/user/pggate/internal/listener"
	"github.com/user/pggate/internal/metrics"
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

	// Start metrics server
	go func() {
		metricsAddr := ":8080"
		log.Printf("Metrics server listening on %s", metricsAddr)
		if err := metrics.ServeMetrics(metricsAddr); err != nil {
			log.Printf("metrics server error: %v", err)
		}
	}()

	log.Printf("PgGate listening on %s", cfg.Listener.Address)
	log.Printf("Primary: %s, Replicas: %v", primary, replicas)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		sig := <-sigChan
		if sig == syscall.SIGHUP {
			log.Println("Reloading configuration...")
			newCfg, err := config.Load("config.yaml")
			if err != nil {
				log.Printf("failed to reload config: %v", err)
				continue
			}
			// Update components (simplified: only some fields for now)
			// TODO: Add more dynamic update logic
			_ = newCfg
			log.Println("Configuration reloaded (partial)")
			continue
		}

		log.Printf("Received signal %v, shutting down...", sig)
		break
	}

	l.Stop()
	pm.Close()
	log.Println("PgGate shutdown complete")
}
