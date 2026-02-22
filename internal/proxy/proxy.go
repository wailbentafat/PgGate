package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/user/pggate/internal/config"
	"github.com/user/pggate/internal/pool"
	"github.com/user/pggate/internal/router"
)

const (
	SSLRequestCode = 80877103
)


type ProxyInt interface {
	HandleClient(clientConn net.Conn)
}

type Proxy struct {
	poolManager *pool.PoolManager
	router      *router.Router
}

func NewProxy(pm *pool.PoolManager, r *router.Router) *Proxy {
	return &Proxy{
		poolManager: pm,
		router:      r,
	}
}

type Session struct {
	clientConn    net.Conn
	backendRWConn net.Conn
	backendROConn net.Conn
	backendROPool *pool.Pool
	inTransaction bool
	proxy         *Proxy
}

func (p *Proxy) HandleClient(clientConn net.Conn) {
	if err := HandleHandshake(clientConn); err != nil {
		log.Printf("handshake error: %v", err)
		return
	}

	session := &Session{
		clientConn: clientConn,
		proxy:      p,
	}
	defer session.Cleanup()

	session.Run()
}

func (s *Session) Run() {
	buf := make([]byte, 8192)
	//header we know its 1 byte for type and then 4 bytes for lendgh in postgress wire protocol
	for {
		if _, err := io.ReadFull(s.clientConn, buf[:1]); err != nil {
			if err != io.EOF {
				log.Printf("error reading message type: %v", err)
			}
			return
		}

		msgType := buf[0]
		if _, err := io.ReadFull(s.clientConn, buf[1:5]); err != nil {
			log.Printf("error reading message length: %v", err)
			return
		}

		length := int32(binary.BigEndian.Uint32(buf[1:5]))
		if length < 4 {
			log.Printf("invalid message length: %d", length)
			return
		}
		msgBody := make([]byte, length-4)
		if _, err := io.ReadFull(s.clientConn, msgBody); err != nil {
			log.Printf("error reading message body: %v", err)
			return
		}
//here we handle query
		if msgType == 'Q' {
			query := string(msgBody[:len(msgBody)-1])
			log.Printf("received query: %s", query)

			dest := s.proxy.router.Route(query, s.inTransaction)
			var conn net.Conn
			var err error

			if dest == router.Primary {
				if s.backendRWConn == nil {
					s.backendRWConn, err = s.proxy.poolManager.GetRW()
				}
				conn = s.backendRWConn
			} else {
				if s.backendROConn == nil {
					s.backendROConn, s.backendROPool, err = s.proxy.poolManager.GetRO()
				}
				conn = s.backendROConn
			}

			if err != nil {
				log.Printf("failed to get backend connection: %v", err)
				return
			}

			// send to backend postgress
			if _, err := conn.Write(append([]byte{msgType}, append(buf[1:5], msgBody...)...)); err != nil {
				log.Printf("error forwarding query to backend: %v", err)
				return
			}

			if err := s.proxyResponse(conn); err != nil {
				log.Printf("error proxying response: %v", err)
				return
			}

			if router.IsTransactionStart(query) {
				s.inTransaction = true
			} else if router.IsTransactionEnd(query) {
				s.inTransaction = false
				if s.backendROConn != nil {
					s.proxy.poolManager.PutRO(s.backendROConn, s.backendROPool)
					s.backendROConn = nil
					s.backendROPool = nil
				}
			}
		} else {
			if s.backendRWConn == nil {
				var err error
				s.backendRWConn, err = s.proxy.poolManager.GetRW()
				if err != nil {
					log.Printf("failed to get backend RW connection: %v", err)
					return
				}
			}
			if _, err := s.backendRWConn.Write(append([]byte{msgType}, append(buf[1:5], msgBody...)...)); err != nil {
				log.Printf("error forwarding message to RW backend: %v", err)
				return
			}
			if err := s.proxyResponse(s.backendRWConn); err != nil {
				log.Printf("error proxying response from RW: %v", err)
				return
			}
		}
	}
}

func (s *Session) proxyResponse(backendConn net.Conn) error {
	buf := make([]byte, 8192)
	for {
		if _, err := io.ReadFull(backendConn, buf[:1]); err != nil {
			return err
		}
		msgType := buf[0]

		if _, err := io.ReadFull(backendConn, buf[1:5]); err != nil {
			return err
		}
		length := int32(binary.BigEndian.Uint32(buf[1:5]))

		body := make([]byte, length-4)
		if _, err := io.ReadFull(backendConn, body); err != nil {
			return err
		}

		if _, err := s.clientConn.Write(append([]byte{msgType}, append(buf[1:5], body...)...)); err != nil {
			return err
		}

		if msgType == config.ReadyForQuery {
			return nil
		}
	}
}

func (s *Session) Cleanup() {
	if s.backendRWConn != nil {
		s.proxy.poolManager.PutRW(s.backendRWConn)
	}
	if s.backendROConn != nil {
		s.proxy.poolManager.PutRO(s.backendROConn, s.backendROPool)
	}
}

func HandleHandshake(clientConn net.Conn) error {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
		return fmt.Errorf("failed to read packet length: %w", err)
	}
	length := int32(binary.BigEndian.Uint32(buf[:4]))
	if length < 8 {
		return fmt.Errorf("packet too short: %d", length)
	}
	if _, err := io.ReadFull(clientConn, buf[4:8]); err != nil {
		return fmt.Errorf("failed to read protocol code: %w", err)
	}
	code := int32(binary.BigEndian.Uint32(buf[4:8]))
	if code == SSLRequestCode {
		if _, err := clientConn.Write([]byte("N")); err != nil {
			return fmt.Errorf("failed to write SSL response: %w", err)
		}
		return HandleHandshake(clientConn)
	}
	rest := make([]byte, length-8)
	if _, err := io.ReadFull(clientConn, rest); err != nil {
		return fmt.Errorf("failed to read rest of StartupMessage: %w", err)
	}
	return nil
}
