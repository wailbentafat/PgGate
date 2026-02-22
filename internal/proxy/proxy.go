package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/user/pggate/internal/config"
	"github.com/user/pggate/internal/metrics"
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
	clientConn          net.Conn
	backendRWConn       net.Conn
	backendROConn       net.Conn
	backendROPool       *pool.Pool
	inTransaction       bool
	extendedDest        router.Destination 
	hasSessionVariables bool             
	proxy               *Proxy
}

func (p *Proxy) HandleClient(clientConn net.Conn) {
	startupMsg, err := HandleHandshake(clientConn)
	if err != nil {
		log.Printf("handshake error: %v", err)
		return
	}

	session := &Session{
		clientConn: clientConn,
		proxy:      p,
	}
	defer session.Cleanup()

	if err := session.Init(startupMsg); err != nil {
		log.Printf("session init error: %v", err)
		return
	}

	session.Run()
}

func (s *Session) Init(startupMsg []byte) error {
	//auth is handled by the master postgres
	conn, err := s.getBackendConn(router.Primary)
	if err != nil {
		return fmt.Errorf("failed to get primary connection for init: %w", err)
	}
	if _, err := conn.Write(startupMsg); err != nil {
		return fmt.Errorf("failed to forward startup message: %w", err)
	}
	return s.proxyResponse(conn)
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
		if msgType == config.QueryMessage {
			metrics.IncTotalQueries()
			query := string(msgBody[:len(msgBody)-1])
			log.Printf("received query: %s", query)

			dest := s.proxy.router.Route(query, s.inTransaction || s.hasSessionVariables)
			if dest == router.Primary {
				metrics.IncPrimaryQueries()
			} else {
				metrics.IncReplicaQueries()
			}

			conn, err := s.getBackendConn(dest)
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
				s.releaseROIfSafe()
			}

			if router.IsSessionModification(query) {
				s.hasSessionVariables = true
				s.releaseROIfSafe() 
			}
		} else if msgType == config.ParseMessage {
			metrics.IncTotalQueries()
			query := s.extractQueryFromParse(msgBody)
			log.Printf("received Parse: %s", query)
			s.extendedDest = s.proxy.router.Route(query, s.inTransaction || s.hasSessionVariables)
			if s.extendedDest == router.Primary {
				metrics.IncPrimaryQueries()
			} else {
				metrics.IncReplicaQueries()
			}

			if router.IsSessionModification(query) {
				s.hasSessionVariables = true
				s.releaseROIfSafe()
				s.extendedDest = router.Primary
			}

			conn, err := s.getBackendConn(s.extendedDest)
			if err != nil {
				log.Printf("failed to get backend connection for Parse: %v", err)
				return
			}
			if _, err := conn.Write(append([]byte{msgType}, append(buf[1:5], msgBody...)...)); err != nil {
				log.Printf("error forwarding Parse to backend: %v", err)
				return
			}
		} else if msgType == config.BindMessage || msgType == config.ExecuteMessage ||
			msgType == config.DescribeMessage || msgType == config.CloseMessage {
				// we have to send those message to the same connection as we do the parse message
			conn, err := s.getBackendConn(s.extendedDest)
			if err != nil {
				log.Printf("failed to get backend connection: %v", err)
				return
			}
			if _, err := conn.Write(append([]byte{msgType}, append(buf[1:5], msgBody...)...)); err != nil {
				log.Printf("error forwarding message %c to backend: %v", msgType, err)
				return
			}
		} else if msgType == config.SyncMessage || msgType == config.FlushMessage {
			conn, err := s.getBackendConn(s.extendedDest)
			if err != nil {
				log.Printf("failed to get backend connection: %v", err)
				return
			}
			if _, err := conn.Write(append([]byte{msgType}, append(buf[1:5], msgBody...)...)); err != nil {
				log.Printf("error forwarding Sync/Flush to backend: %v", err)
				return
			}
			// Proxy response back
			if err := s.proxyResponse(conn); err != nil {
				log.Printf("error proxying response for Sync/Flush: %v", err)
				return
			}
		} else if msgType == config.TerminateMessage {
			log.Println("client terminated connection")
			return
		} else {
			conn, err := s.getBackendConn(router.Primary)
			if err != nil {
				log.Printf("failed to get backend RW connection: %v", err)
				return
			}
			if _, err := conn.Write(append([]byte{msgType}, append(buf[1:5], msgBody...)...)); err != nil {
				log.Printf("error forwarding message to RW backend: %v", err)
				return
			}
			// We might need to proxy response depending on message type,
			// but for now let's assume it needs a response if it's not handled above.
			if err := s.proxyResponse(conn); err != nil {
				log.Printf("error proxying response from RW: %v", err)
				return
			}
		}
	}
}

func (s *Session) getBackendConn(dest router.Destination) (net.Conn, error) {
	var err error
	if dest == router.Primary {
		if s.backendRWConn == nil {
			s.backendRWConn, err = s.proxy.poolManager.GetRW()
		}
		return s.backendRWConn, err
	} else {
		if s.backendROConn == nil {
			s.backendROConn, s.backendROPool, err = s.proxy.poolManager.GetRO()
		}
		return s.backendROConn, err
	}
}

func (s *Session) releaseROIfSafe() {
	if s.backendROConn != nil {
		s.proxy.poolManager.PutRO(s.backendROConn, s.backendROPool)
		s.backendROConn = nil
		s.backendROPool = nil
	}
}

func (s *Session) extractQueryFromParse(msgBody []byte) string {
	// Parse message format:
	// String: Name of destination prepared statement (empty string selects unnamed)
	// String: Query string to be parsed
	// Int16: Number of parameter data types specified
	// Int32[]: Parameter data types

	// Skip the prepared statement name (it's a null-terminated string)
	var i int
	for i = 0; i < len(msgBody); i++ {
		if msgBody[i] == 0 {
			break
		}
	}
	i++ // skip null byte

	if i >= len(msgBody) {
		return ""
	}

	// The query string is next, also null-terminated
	var j int
	for j = i; j < len(msgBody); j++ {
		if msgBody[j] == 0 {
			break
		}
	}

	return string(msgBody[i:j])
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

		// Handle Authentication Request
		if msgType == config.Authentification {
			authType := binary.BigEndian.Uint32(body[:4])
			if authType != 0 {
				// We need a response from the client (e.g., PasswordMessage)
				if err := s.handleAuthResponse(backendConn); err != nil {
					return err
				}
				// After forwarding client response, continue reading backend responses
				continue
			}
		}

		if msgType == config.ReadyForQuery {
			return nil
		}
	}
}

func (s *Session) handleAuthResponse(backendConn net.Conn) error {
	buf := make([]byte, 8192)
	// Read message type from client
	if _, err := io.ReadFull(s.clientConn, buf[:1]); err != nil {
		return err
	}
	msgType := buf[0]

	// Read length
	if _, err := io.ReadFull(s.clientConn, buf[1:5]); err != nil {
		return err
	}
	length := int32(binary.BigEndian.Uint32(buf[1:5]))

	body := make([]byte, length-4)
	if _, err := io.ReadFull(s.clientConn, body); err != nil {
		return err
	}

	// Forward to backend
	if _, err := backendConn.Write(append([]byte{msgType}, append(buf[1:5], body...)...)); err != nil {
		return err
	}

	return nil
}

func (s *Session) Cleanup() {
	if s.backendRWConn != nil {
		s.proxy.poolManager.PutRW(s.backendRWConn)
	}
	if s.backendROConn != nil {
		s.proxy.poolManager.PutRO(s.backendROConn, s.backendROPool)
	}
}

func HandleHandshake(clientConn net.Conn) ([]byte, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
		return nil, fmt.Errorf("failed to read packet length: %w", err)
	}
	length := int32(binary.BigEndian.Uint32(buf[:4]))
	if length < 8 {
		return nil, fmt.Errorf("packet too short: %d", length)
	}
	if _, err := io.ReadFull(clientConn, buf[4:8]); err != nil {
		return nil, fmt.Errorf("failed to read protocol code: %w", err)
	}
	code := int32(binary.BigEndian.Uint32(buf[4:8]))
	if code == SSLRequestCode {
		if _, err := clientConn.Write([]byte("N")); err != nil {
			return nil, fmt.Errorf("failed to write SSL response: %w", err)
		}
		return HandleHandshake(clientConn)
	}

	rest := make([]byte, length-8)
	if _, err := io.ReadFull(clientConn, rest); err != nil {
		return nil, fmt.Errorf("failed to read rest of StartupMessage: %w", err)
	}

	// Combine components back into original StartupMessage
	startupMsg := append(buf[:8], rest...)
	return startupMsg, nil
}
