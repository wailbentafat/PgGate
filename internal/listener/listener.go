package listener

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/user/pggate/internal/metrics"
	"github.com/user/pggate/internal/proxy"
)

type ListenerConfig struct {
	Address        string
	MaxConnections int
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

type Server struct {
	cfg      ListenerConfig
	listener net.Listener
	proxy    *proxy.Proxy

	sem  chan struct{}  // connection limiter
	wg   sync.WaitGroup // graceful shutdown
	quit chan struct{}  // stop signal
}

func NewServer(cfg ListenerConfig, p *proxy.Proxy) *Server {
	return &Server{
		cfg:   cfg,
		proxy: p,
		sem:   make(chan struct{}, cfg.MaxConnections),
		quit:  make(chan struct{}),
	}
}

func (s *Server) Start() error {
	var err error
	s.listener, err = net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return err
	}

	log.Printf("Listener started on %s", s.cfg.Address)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}

		s.sem <- struct{}{}
		s.wg.Add(1)

		go s.handleConnection(conn)
	}
}

func (s *Server) Stop() {
	close(s.quit)

	if s.listener != nil {
		_ = s.listener.Close()
	}

	s.wg.Wait()
	log.Println("Listener stopped")
}

func (s *Server) handleConnection(conn net.Conn) {
	metrics.IncActiveConnections()
	defer s.wg.Done()
	defer func() {
		metrics.DecActiveConnections()
		<-s.sem
		_ = conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout))
	_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))

	log.Printf("Accepted connection from %s", conn.RemoteAddr())

	s.proxy.HandleClient(conn)
}
