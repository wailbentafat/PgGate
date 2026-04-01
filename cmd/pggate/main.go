package main

import (
	"context"
	"crypto/tls"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/user/pggate/internal/config"
	"github.com/user/pggate/internal/listener"
	"github.com/user/pggate/internal/logging"
	"github.com/user/pggate/internal/metrics"
	"github.com/user/pggate/internal/pool"
	"github.com/user/pggate/internal/proxy"
	"github.com/user/pggate/internal/ratelimit"
	"github.com/user/pggate/internal/router"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		panic("failed to load config: " + err.Error())
	}

	logging.Setup(cfg.Logging.Level, cfg.Logging.Format)
	log := logging.L()

	var backendTLS *tls.Config
	if cfg.TLS.Enabled {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			log.Error("failed to load TLS cert/key", "error", err)
			os.Exit(1)
		}
		backendTLS = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	// TLS config for client-facing connections
	var clientTLS *tls.Config
	if cfg.TLS.Enabled {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			log.Error("failed to load TLS cert/key for clients", "error", err)
			os.Exit(1)
		}
		clientTLS = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	// Build replica addresses
	replicas := make([]string, len(cfg.Backend.Replicas))
	for i, r := range cfg.Backend.Replicas {
		replicas[i] = r.Address
	}

	// Pool manager
	pm := pool.NewPoolManager(pool.PoolManagerConfig{
		PrimaryAddr:     cfg.Backend.Primary.Address,
		ReplicaAddrs:    replicas,
		PrimarySize:     cfg.Pool.PrimarySize,
		ReplicaSize:     cfg.Pool.ReplicaSize,
		IdleTimeout:     cfg.Pool.IdleTimeout,
		CleanupInterval: cfg.Pool.CleanupInterval,
		TLSConfig:       backendTLS,
		MaxReplicaLag:   cfg.Pool.MaxReplicaLag,
	})

	// Router
	r := router.NewRouter()

	// Proxy
	p := proxy.NewProxy(proxy.ProxyConfig{
		PoolManager:  pm,
		Router:       r,
		QueryTimeout: cfg.Pool.QueryTimeout,
		TLSConfig:    clientTLS,
	})

	// Rate limiter
	var rl *ratelimit.IPRateLimiter
	if cfg.RateLimit.Enabled {
		rl = ratelimit.NewIPRateLimiter(cfg.RateLimit.RequestsPerSec, cfg.RateLimit.Burst)
		log.Info("rate limiting enabled", "rps", cfg.RateLimit.RequestsPerSec, "burst", cfg.RateLimit.Burst)
	}

	l := listener.NewServer(listener.ListenerConfig{
		Address:        cfg.Listener.Address,
		MaxConnections: cfg.Listener.MaxConnections,
		ReadTimeout:    cfg.Listener.ReadTimeout,
		WriteTimeout:   cfg.Listener.WriteTimeout,
	}, p, rl)

	ms := metrics.NewMetricsServer(cfg.Metrics.Address)
	go func() {
		log.Info("metrics server listening", "address", cfg.Metrics.Address)
		if err := ms.ListenAndServe(); err != nil {
			log.Error("metrics server error", "error", err)
		}
	}()

	go func() {
		if err := l.Start(); err != nil {
			log.Error("listener error", "error", err)
			os.Exit(1)
		}
	}()

	ms.SetReady(true)
	log.Info("PgGate started",
		"address", cfg.Listener.Address,
		"primary", cfg.Backend.Primary.Address,
		"replicas", replicas,
		"tls", cfg.TLS.Enabled,
	)

	// Signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		sig := <-sigChan
		if sig == syscall.SIGHUP {
			log.Info("reloading configuration...")
			newCfg, err := config.Load("config.yaml")
			if err != nil {
				log.Error("failed to reload config", "error", err)
				continue
			}

			logging.Setup(newCfg.Logging.Level, newCfg.Logging.Format)

			newReplicas := make([]string, len(newCfg.Backend.Replicas))
			for i, r := range newCfg.Backend.Replicas {
				newReplicas[i] = r.Address
			}
			pm.ReloadReplicas(newReplicas, newCfg.Pool.ReplicaSize, newCfg.Pool.IdleTimeout, newCfg.Pool.CleanupInterval, backendTLS)

			log.Info("configuration reloaded")
			continue
		}

		log.Info("received signal, shutting down", "signal", sig)
		break
	}

	// Graceful shutdown
	ms.SetReady(false)

	shutdownTimeout := 30 * time.Second
	l.Stop(shutdownTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ms.Shutdown(ctx)
	cancel()

	pm.Close()
	log.Info("PgGate shutdown complete")
}
