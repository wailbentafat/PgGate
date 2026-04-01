package listener

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/user/pggate/internal/logging"
	"github.com/user/pggate/internal/metrics"
	"github.com/user/pggate/internal/proxy"
	"github.com/user/pggate/internal/ratelimit"
)

type ListenerConfig struct {
	Address        string
	MaxConnections int
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

type Server struct {
	cfg         ListenerConfig
	listener    net.Listener
	proxy       *proxy.Proxy
	rateLimiter *ratelimit.IPRateLimiter

	sem  chan struct{}
	wg   sync.WaitGroup
	quit chan struct{}
}

func NewServer(cfg ListenerConfig, p *proxy.Proxy, rl *ratelimit.IPRateLimiter) *Server {
	return &Server{
		cfg:         cfg,
		proxy:       p,
		rateLimiter: rl,
		sem:         make(chan struct{}, cfg.MaxConnections),
		quit:        make(chan struct{}),
	}
}

func (s *Server) Start() error {
	var err error
	s.listener, err = net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return err
	}

	log := logging.L()
	log.Info("listener started", "address", s.cfg.Address)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil
			default:
				log.Error("accept error", "error", err)
				continue
			}
		}

		// Rate limiting
		if s.rateLimiter != nil {
			ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			if !s.rateLimiter.Allow(ip) {
				metrics.RateLimitRejections.Inc()
				log.Warn("rate limit exceeded", "ip", ip)
				conn.Close()
				continue
			}
		}

		// Connection limit
		select {
		case s.sem <- struct{}{}:
		default:
			log.Warn("max connections reached, rejecting", "remote", conn.RemoteAddr())
			metrics.IncErrors("max_connections")
			conn.Close()
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) Stop(timeout time.Duration) {
	log := logging.L()
	close(s.quit)

	if s.listener != nil {
		_ = s.listener.Close()
	}

	// Wait for existing connections with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info("all connections drained")
	case <-time.After(timeout):
		log.Warn("shutdown timeout reached, forcing close", "timeout", timeout)
	}

	log.Info("listener stopped")
}

func (s *Server) handleConnection(conn net.Conn) {
	metrics.IncActiveConnections()
	defer s.wg.Done()
	defer func() {
		metrics.DecActiveConnections()
		<-s.sem
		_ = conn.Close()
	}()

	if s.cfg.ReadTimeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout))
	}
	if s.cfg.WriteTimeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))
	}

	log := logging.L()
	log.Debug("accepted connection", "remote", conn.RemoteAddr())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel context when quit signal is received
	go func() {
		select {
		case <-s.quit:
			cancel()
		case <-ctx.Done():
		}
	}()

	s.proxy.HandleClient(ctx, conn)
}
