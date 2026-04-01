package proxy

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/user/pggate/internal/config"
	"github.com/user/pggate/internal/logging"
	"github.com/user/pggate/internal/metrics"
	"github.com/user/pggate/internal/pool"
	"github.com/user/pggate/internal/router"
)

const (
	SSLRequestCode = 80877103
)

type Proxy struct {
	poolManager  *pool.PoolManager
	router       *router.Router
	queryTimeout time.Duration
	tlsConfig    *tls.Config
}

type ProxyConfig struct {
	PoolManager  *pool.PoolManager
	Router       *router.Router
	QueryTimeout time.Duration
	TLSConfig    *tls.Config
}

func NewProxy(cfg ProxyConfig) *Proxy {
	return &Proxy{
		poolManager:  cfg.PoolManager,
		router:       cfg.Router,
		queryTimeout: cfg.QueryTimeout,
		tlsConfig:    cfg.TLSConfig,
	}
}

type Session struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	clientConn          net.Conn
	backendRWConn       net.Conn
	backendROConn       net.Conn
	backendROPool       *pool.Pool
	inTransaction       bool
	extendedDest        router.Destination
	hasSessionVariables bool
	proxy               *Proxy
	requestID           string
}

func generateRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sendErrorToClient sends a PostgreSQL ErrorResponse message to the client.
// Fields: S=severity, C=code, M=message per PG wire protocol.
func (s *Session) sendErrorToClient(severity, code, message string) {
	// ErrorResponse format: 'E' + int32(len) + fields + '\0'
	// Each field: byte(field_type) + string + '\0'
	// Terminated by '\0'
	var body []byte
	body = append(body, 'S')
	body = append(body, []byte(severity)...)
	body = append(body, 0)
	body = append(body, 'V') // non-localized severity
	body = append(body, []byte(severity)...)
	body = append(body, 0)
	body = append(body, 'C')
	body = append(body, []byte(code)...)
	body = append(body, 0)
	body = append(body, 'M')
	body = append(body, []byte(message)...)
	body = append(body, 0)
	body = append(body, 0) // terminator

	length := int32(4 + len(body))
	msg := make([]byte, 1+4+len(body))
	msg[0] = config.ErrorResponse
	binary.BigEndian.PutUint32(msg[1:5], uint32(length))
	copy(msg[5:], body)

	s.clientConn.Write(msg)
}

// sendReadyForQuery sends a ReadyForQuery message indicating idle state.
func (s *Session) sendReadyForQuery() {
	// ReadyForQuery: 'Z' + int32(5) + byte('I' = idle)
	msg := []byte{'Z', 0, 0, 0, 5, 'I'}
	s.clientConn.Write(msg)
}

func (p *Proxy) HandleClient(ctx context.Context, clientConn net.Conn) {
	reqID := generateRequestID()
	ctx = logging.WithRequestID(ctx, reqID)
	log := logging.FromContext(ctx)

	startupMsg, err := HandleHandshake(clientConn, p.tlsConfig)
	if err != nil {
		log.Error("handshake failed", "error", err)
		metrics.IncErrors("handshake")
		return
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	session := &Session{
		ctx:        sessionCtx,
		cancel:     cancel,
		clientConn: clientConn,
		proxy:      p,
		requestID:  reqID,
	}
	defer session.Cleanup()

	if err := session.Init(startupMsg); err != nil {
		log.Error("session init failed", "error", err)
		metrics.IncErrors("session_init")
		return
	}

	metrics.AuthAttempts.WithLabelValues("success").Inc()
	session.Run()
}

func (s *Session) Init(startupMsg []byte) error {
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
	log := logging.FromContext(s.ctx)
	headerBuf := make([]byte, 5)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		if _, err := io.ReadFull(s.clientConn, headerBuf[:1]); err != nil {
			if err != io.EOF {
				log.Debug("client read error", "error", err)
			}
			return
		}

		msgType := headerBuf[0]
		if _, err := io.ReadFull(s.clientConn, headerBuf[1:5]); err != nil {
			log.Error("error reading message length", "error", err)
			return
		}

		length := int32(binary.BigEndian.Uint32(headerBuf[1:5]))
		if length < 4 {
			log.Error("invalid message length", "length", length)
			s.sendErrorToClient("FATAL", "08P01", "invalid message length")
			return
		}
		if length > config.MaxMessageSize {
			log.Error("message too large", "length", length, "max", config.MaxMessageSize)
			s.sendErrorToClient("FATAL", "08P01", fmt.Sprintf("message size %d exceeds maximum %d", length, config.MaxMessageSize))
			return
		}

		msgBody := make([]byte, length-4)
		if len(msgBody) > 0 {
			if _, err := io.ReadFull(s.clientConn, msgBody); err != nil {
				log.Error("error reading message body", "error", err)
				return
			}
		}

		metrics.BytesTransferred.WithLabelValues("client_to_proxy").Add(float64(5 + len(msgBody)))

		fullMsg := buildMessage(msgType, headerBuf[1:5], msgBody)

		switch msgType {
		case config.QueryMessage:
			if err := s.handleSimpleQuery(fullMsg, msgBody); err != nil {
				log.Error("query handling error", "error", err)
				return
			}

		case config.ParseMessage:
			if err := s.handleParse(fullMsg, msgBody); err != nil {
				log.Error("parse handling error", "error", err)
				return
			}

		case config.BindMessage, config.ExecuteMessage,
			config.DescribeMessage, config.CloseMessage:
			if err := s.forwardExtended(fullMsg); err != nil {
				log.Error("extended protocol error", "msg_type", string(msgType), "error", err)
				return
			}

		case config.SyncMessage, config.FlushMessage:
			if err := s.handleSyncFlush(fullMsg); err != nil {
				log.Error("sync/flush error", "error", err)
				return
			}

		case config.TerminateMessage:
			log.Debug("client terminated connection")
			return

		// COPY protocol: client sends CopyData/CopyDone/CopyFail during COPY FROM
		case config.CopyData, config.CopyDone, config.CopyFail:
			if err := s.forwardCopyFromClient(fullMsg); err != nil {
				log.Error("copy protocol error", "error", err)
				return
			}

		default:
			if err := s.forwardToPrimary(fullMsg); err != nil {
				log.Error("forward to primary error", "msg_type", string(msgType), "error", err)
				return
			}
		}
	}
}

func (s *Session) handleSimpleQuery(fullMsg, msgBody []byte) error {
	metrics.IncTotalQueries()
	query := string(msgBody[:len(msgBody)-1])
	log := logging.FromContext(s.ctx)
	log.Debug("query", "sql", query)

	dest := s.proxy.router.Route(query, s.inTransaction || s.hasSessionVariables)
	if dest == router.Primary {
		metrics.IncPrimaryQueries()
	} else {
		metrics.IncReplicaQueries()
	}

	conn, err := s.getBackendConn(dest)
	if err != nil {
		metrics.IncErrors("backend_conn")
		s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection failed: %v", err))
		s.sendReadyForQuery()
		return nil // don't kill session — client can retry
	}

	if s.proxy.queryTimeout > 0 {
		deadline := time.Now().Add(s.proxy.queryTimeout)
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}

	start := time.Now()
	if _, err := conn.Write(fullMsg); err != nil {
		metrics.IncErrors("backend_write")
		return s.handleBackendDisconnect(dest, fmt.Errorf("error forwarding query to backend: %w", err))
	}

	if err := s.proxyResponse(conn); err != nil {
		metrics.IncErrors("proxy_response")
		return s.handleBackendDisconnect(dest, fmt.Errorf("error proxying response: %w", err))
	}
	metrics.ObserveQueryDuration(dest.String(), time.Since(start))

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

	// Connection multiplexing: release connections back to pool when safe
	s.releaseConnsIfSafe()

	return nil
}

func (s *Session) handleParse(fullMsg, msgBody []byte) error {
	metrics.IncTotalQueries()
	query := s.extractQueryFromParse(msgBody)
	log := logging.FromContext(s.ctx)
	log.Debug("parse", "sql", query)

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
		metrics.IncErrors("backend_conn")
		s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection failed: %v", err))
		s.sendReadyForQuery()
		return nil
	}

	if _, err := conn.Write(fullMsg); err != nil {
		metrics.IncErrors("backend_write")
		return s.handleBackendDisconnect(s.extendedDest, fmt.Errorf("error forwarding Parse to backend: %w", err))
	}
	return nil
}

func (s *Session) forwardExtended(fullMsg []byte) error {
	conn, err := s.getBackendConn(s.extendedDest)
	if err != nil {
		s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection failed: %v", err))
		s.sendReadyForQuery()
		return nil
	}
	if _, err := conn.Write(fullMsg); err != nil {
		return s.handleBackendDisconnect(s.extendedDest, fmt.Errorf("error forwarding extended message: %w", err))
	}
	return nil
}

func (s *Session) handleSyncFlush(fullMsg []byte) error {
	conn, err := s.getBackendConn(s.extendedDest)
	if err != nil {
		s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection failed: %v", err))
		s.sendReadyForQuery()
		return nil
	}

	if s.proxy.queryTimeout > 0 {
		deadline := time.Now().Add(s.proxy.queryTimeout)
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}

	if _, err := conn.Write(fullMsg); err != nil {
		return s.handleBackendDisconnect(s.extendedDest, fmt.Errorf("error forwarding Sync/Flush: %w", err))
	}
	if err := s.proxyResponse(conn); err != nil {
		return s.handleBackendDisconnect(s.extendedDest, fmt.Errorf("error proxying Sync/Flush response: %w", err))
	}

	// Connection multiplexing: release after extended protocol cycle completes
	s.releaseConnsIfSafe()

	return nil
}

func (s *Session) forwardToPrimary(fullMsg []byte) error {
	conn, err := s.getBackendConn(router.Primary)
	if err != nil {
		s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection failed: %v", err))
		s.sendReadyForQuery()
		return nil
	}
	if _, err := conn.Write(fullMsg); err != nil {
		return s.handleBackendDisconnect(router.Primary, fmt.Errorf("error forwarding message to RW backend: %w", err))
	}
	return s.proxyResponse(conn)
}

// forwardCopyFromClient forwards COPY data messages (CopyData, CopyDone, CopyFail)
// from the client to the backend during a COPY FROM STDIN operation.
func (s *Session) forwardCopyFromClient(fullMsg []byte) error {
	// COPY always goes to primary (it's a write operation)
	conn, err := s.getBackendConn(router.Primary)
	if err != nil {
		s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection failed: %v", err))
		s.sendReadyForQuery()
		return nil
	}
	if _, err := conn.Write(fullMsg); err != nil {
		return s.handleBackendDisconnect(router.Primary, fmt.Errorf("error forwarding COPY data: %w", err))
	}

	// After CopyDone or CopyFail, the backend sends CommandComplete + ReadyForQuery
	// (or ErrorResponse + ReadyForQuery). We need to proxy that response.
	msgType := fullMsg[0]
	if msgType == config.CopyDone || msgType == config.CopyFail {
		if err := s.proxyResponse(conn); err != nil {
			return s.handleBackendDisconnect(router.Primary, fmt.Errorf("error proxying COPY response: %w", err))
		}
	}
	return nil
}

// proxyCopyToClient handles COPY TO STDOUT: streams CopyData messages from backend
// to client until CopyDone or ErrorResponse is received.
func (s *Session) proxyCopyToClient(backendConn net.Conn) error {
	headerBuf := make([]byte, 5)
	for {
		if _, err := io.ReadFull(backendConn, headerBuf[:1]); err != nil {
			return err
		}
		msgType := headerBuf[0]

		if _, err := io.ReadFull(backendConn, headerBuf[1:5]); err != nil {
			return err
		}
		length := int32(binary.BigEndian.Uint32(headerBuf[1:5]))

		body := make([]byte, length-4)
		if len(body) > 0 {
			if _, err := io.ReadFull(backendConn, body); err != nil {
				return err
			}
		}

		resp := buildMessage(msgType, headerBuf[1:5], body)
		if _, err := s.clientConn.Write(resp); err != nil {
			return err
		}
		metrics.BytesTransferred.WithLabelValues("proxy_to_client").Add(float64(len(resp)))

		// CopyDone or ErrorResponse ends the COPY stream.
		// After that, backend sends CommandComplete + ReadyForQuery via normal proxyResponse.
		if msgType == config.CopyDone || msgType == config.ErrorResponse {
			return nil
		}
	}
}

// handleBackendDisconnect handles a backend connection failure.
// If not in a transaction, it drops the broken connection and sends an error to the client.
// If in a transaction, the session must end because transaction state is lost.
func (s *Session) handleBackendDisconnect(dest router.Destination, originalErr error) error {
	log := logging.FromContext(s.ctx)
	log.Warn("backend disconnected", "destination", dest.String(), "error", originalErr)

	if s.inTransaction {
		// Transaction state is lost — must kill session
		s.sendErrorToClient("FATAL", "08006", fmt.Sprintf("backend disconnected during transaction: %v", originalErr))
		return originalErr
	}

	// Not in transaction — we can drop the broken connection and let
	// the next query acquire a fresh one
	if dest == router.Primary {
		if s.backendRWConn != nil {
			s.backendRWConn.Close()
			s.backendRWConn = nil
		}
	} else {
		if s.backendROConn != nil {
			s.backendROConn.Close()
			s.backendROConn = nil
			s.backendROPool = nil
		}
	}

	s.sendErrorToClient("ERROR", "08006", fmt.Sprintf("backend connection lost: %v", originalErr))
	s.sendReadyForQuery()
	return nil // session survives
}

func buildMessage(msgType byte, lengthBytes, body []byte) []byte {
	msg := make([]byte, 1+4+len(body))
	msg[0] = msgType
	copy(msg[1:5], lengthBytes)
	copy(msg[5:], body)
	return msg
}

func (s *Session) getBackendConn(dest router.Destination) (net.Conn, error) {
	var err error
	if dest == router.Primary {
		if s.backendRWConn == nil {
			s.backendRWConn, err = s.proxy.poolManager.GetRW(s.ctx)
		}
		return s.backendRWConn, err
	}
	if s.backendROConn == nil {
		s.backendROConn, s.backendROPool, err = s.proxy.poolManager.GetRO(s.ctx)
	}
	return s.backendROConn, err
}

func (s *Session) releaseROIfSafe() {
	if s.backendROConn != nil {
		s.proxy.poolManager.PutRO(s.backendROConn, s.backendROPool)
		s.backendROConn = nil
		s.backendROPool = nil
	}
}

// releaseConnsIfSafe releases both RW and RO connections back to the pool
// when the session is not in a transaction and has no session variables.
// This enables connection multiplexing: other sessions can reuse these connections.
func (s *Session) releaseConnsIfSafe() {
	if s.inTransaction || s.hasSessionVariables {
		return
	}
	if s.backendRWConn != nil {
		s.proxy.poolManager.PutRW(s.backendRWConn)
		s.backendRWConn = nil
	}
	s.releaseROIfSafe()
}

func (s *Session) extractQueryFromParse(msgBody []byte) string {
	var i int
	for i = 0; i < len(msgBody); i++ {
		if msgBody[i] == 0 {
			break
		}
	}
	i++

	if i >= len(msgBody) {
		return ""
	}

	var j int
	for j = i; j < len(msgBody); j++ {
		if msgBody[j] == 0 {
			break
		}
	}

	return string(msgBody[i:j])
}

func (s *Session) proxyResponse(backendConn net.Conn) error {
	headerBuf := make([]byte, 5)
	for {
		if _, err := io.ReadFull(backendConn, headerBuf[:1]); err != nil {
			return err
		}
		msgType := headerBuf[0]

		if _, err := io.ReadFull(backendConn, headerBuf[1:5]); err != nil {
			return err
		}
		length := int32(binary.BigEndian.Uint32(headerBuf[1:5]))

		body := make([]byte, length-4)
		if len(body) > 0 {
			if _, err := io.ReadFull(backendConn, body); err != nil {
				return err
			}
		}

		resp := buildMessage(msgType, headerBuf[1:5], body)
		if _, err := s.clientConn.Write(resp); err != nil {
			return err
		}

		metrics.BytesTransferred.WithLabelValues("proxy_to_client").Add(float64(len(resp)))

		// Handle Authentication Request
		if msgType == config.Authentification {
			if len(body) >= 4 {
				authType := binary.BigEndian.Uint32(body[:4])
				if authType != 0 {
					if err := s.handleAuthResponse(backendConn); err != nil {
						return err
					}
					continue
				}
			}
		}

		// COPY TO STDOUT: backend sends CopyOutResponse, then streams CopyData
		if msgType == config.CopyOutResponse {
			if err := s.proxyCopyToClient(backendConn); err != nil {
				return err
			}
			// After CopyDone, backend sends CommandComplete + ReadyForQuery
			// which will be caught by the next iteration
			continue
		}

		// COPY FROM STDIN: backend sends CopyInResponse, then waits for client data
		if msgType == config.CopyInResponse {
			// Return control to Run() — client will send CopyData/CopyDone/CopyFail
			// which are handled by the CopyData/CopyDone/CopyFail case in Run()
			return nil
		}

		if msgType == config.ReadyForQuery {
			return nil
		}
	}
}

func (s *Session) handleAuthResponse(backendConn net.Conn) error {
	headerBuf := make([]byte, 5)
	if _, err := io.ReadFull(s.clientConn, headerBuf[:1]); err != nil {
		return err
	}
	msgType := headerBuf[0]

	if _, err := io.ReadFull(s.clientConn, headerBuf[1:5]); err != nil {
		return err
	}
	length := int32(binary.BigEndian.Uint32(headerBuf[1:5]))

	body := make([]byte, length-4)
	if len(body) > 0 {
		if _, err := io.ReadFull(s.clientConn, body); err != nil {
			return err
		}
	}

	msg := buildMessage(msgType, headerBuf[1:5], body)
	if _, err := backendConn.Write(msg); err != nil {
		return err
	}

	return nil
}

func (s *Session) Cleanup() {
	s.cancel()
	if s.backendRWConn != nil {
		s.proxy.poolManager.PutRW(s.backendRWConn)
	}
	if s.backendROConn != nil {
		s.proxy.poolManager.PutRO(s.backendROConn, s.backendROPool)
	}
}

func HandleHandshake(clientConn net.Conn, tlsConfig *tls.Config) ([]byte, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
		return nil, fmt.Errorf("failed to read packet length: %w", err)
	}
	length := int32(binary.BigEndian.Uint32(buf[:4]))
	if length < 8 {
		return nil, fmt.Errorf("packet too short: %d", length)
	}
	if length > config.MaxMessageSize {
		return nil, fmt.Errorf("startup message too large: %d", length)
	}
	if _, err := io.ReadFull(clientConn, buf[4:8]); err != nil {
		return nil, fmt.Errorf("failed to read protocol code: %w", err)
	}
	code := int32(binary.BigEndian.Uint32(buf[4:8]))
	if code == SSLRequestCode {
		if tlsConfig != nil {
			if _, err := clientConn.Write([]byte("S")); err != nil {
				return nil, fmt.Errorf("failed to write SSL accept: %w", err)
			}
			tlsConn := tls.Server(clientConn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return nil, fmt.Errorf("TLS handshake failed: %w", err)
			}
			return HandleHandshake(tlsConn, nil)
		}
		if _, err := clientConn.Write([]byte("N")); err != nil {
			return nil, fmt.Errorf("failed to write SSL response: %w", err)
		}
		return HandleHandshake(clientConn, tlsConfig)
	}

	rest := make([]byte, length-8)
	if _, err := io.ReadFull(clientConn, rest); err != nil {
		return nil, fmt.Errorf("failed to read rest of StartupMessage: %w", err)
	}

	startupMsg := append(buf[:8], rest...)
	return startupMsg, nil
}
