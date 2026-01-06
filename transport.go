package bfcp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ConnectionRole defines whether the endpoint is active (initiates connection) or passive (accepts connection)
type ConnectionRole uint8

const (
	RoleActive  ConnectionRole = 0 // a=setup:active (client connects)
	RolePassive ConnectionRole = 1 // a=setup:passive (server listens)
	RoleActpass ConnectionRole = 2 // a=setup:actpass (can be either)
)

func (r ConnectionRole) String() string {
	switch r {
	case RoleActive:
		return "active"
	case RolePassive:
		return "passive"
	case RoleActpass:
		return "actpass"
	default:
		return fmt.Sprintf("unknown(%d)", r)
	}
}

// Transport represents a BFCP transport connection (TCP or UDP)
type Transport struct {
	conn   net.Conn
	role   ConnectionRole
	mu     sync.RWMutex
	closed bool
	logger Logger

	OnMessage func(*Message)
	OnError   func(error)
	OnClose   func()

	ctx    context.Context
	cancel context.CancelFunc

	keepaliveInterval time.Duration
	keepaliveSender   func() error
	keepaliveWg       sync.WaitGroup
}

func (t *Transport) log() Logger {
	if t.logger != nil {
		return t.logger
	}
	return NopLogger{}
}

// SetLogger sets the logger for this transport
func (t *Transport) SetLogger(l Logger) {
	t.logger = l
}

// NewTransport creates a new BFCP transport from an existing connection
func NewTransport(conn net.Conn, role ConnectionRole) *Transport {
	ctx, cancel := context.WithCancel(context.Background())
	return &Transport{
		conn:   conn,
		role:   role,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Dial creates an active BFCP connection to the specified address
func Dial(address string) (*Transport, error) {
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", address, err)
	}
	return NewTransport(conn, RoleActive), nil
}

// DialWithLogger creates an active BFCP connection with logging
func DialWithLogger(address string, logger Logger) (*Transport, error) {
	if logger != nil {
		logger.Debugw("bfcp.tcp.dialing", "addr", address)
	}
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		if logger != nil {
			logger.Errorw("bfcp.tcp.dial_failed", err, "addr", address)
		}
		return nil, fmt.Errorf("failed to dial %s: %w", address, err)
	}
	if logger != nil {
		logger.Debugw("bfcp.tcp.connected", "local", conn.LocalAddr().String(), "remote", conn.RemoteAddr().String())
	}
	t := NewTransport(conn, RoleActive)
	t.logger = logger
	return t, nil
}

// DialContext creates an active BFCP connection with context
func DialContext(ctx context.Context, address string) (*Transport, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", address, err)
	}

	transport := NewTransport(conn, RoleActive)
	transport.ctx = ctx
	return transport, nil
}

// EnableKeepalive enables keepalive with the specified interval and sender function
func (t *Transport) EnableKeepalive(interval time.Duration, sender func() error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.keepaliveInterval = interval
	t.keepaliveSender = sender
}

// StartKeepalive starts the keepalive goroutine if configured.
func (t *Transport) StartKeepalive() {
	t.mu.RLock()
	interval := t.keepaliveInterval
	sender := t.keepaliveSender
	t.mu.RUnlock()

	if interval > 0 && sender != nil {
		t.log().Debugw("bfcp.keepalive.start", "interval", interval.String())
		t.keepaliveWg.Add(1)
		go t.keepaliveLoop()
	}
}

// Start begins reading messages from the connection
func (t *Transport) Start() {
	t.log().Debugw("bfcp.read.starting", "role", t.role.String())
	go t.readLoop()

	if t.keepaliveInterval > 0 && t.keepaliveSender != nil {
		t.log().Debugw("bfcp.keepalive.start", "interval", t.keepaliveInterval.String())
		t.keepaliveWg.Add(1)
		go t.keepaliveLoop()
	}
}

func (t *Transport) keepaliveLoop() {
	defer t.keepaliveWg.Done()
	ticker := time.NewTicker(t.keepaliveInterval)
	defer ticker.Stop()

	t.log().Debugw("bfcp.keepalive.running", "interval", t.keepaliveInterval.String())

	for {
		select {
		case <-t.ctx.Done():
			t.log().Debugw("bfcp.keepalive.stopping", "reason", "context_cancelled")
			return
		case <-ticker.C:
			if t.IsClosed() {
				t.log().Debugw("bfcp.keepalive.stopping", "reason", "transport_closed")
				return
			}
			if err := t.keepaliveSender(); err != nil {
				t.log().Warnw("bfcp.keepalive.failed", err)
			}
		}
	}
}

func (t *Transport) readLoop() {
	t.log().Debugw("bfcp.read.started", "role", t.role.String())
	defer func() {
		t.log().Debugw("bfcp.read.exiting")
		t.Close()
		if t.OnClose != nil {
			t.OnClose()
		}
	}()

	for {
		select {
		case <-t.ctx.Done():
			t.log().Debugw("bfcp.read.stopping", "reason", "context_cancelled")
			return
		default:
		}

		// Set read deadline to allow periodic context checks
		if err := t.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.log().Errorw("bfcp.read.deadline_failed", err)
			if t.OnError != nil {
				t.OnError(fmt.Errorf("failed to set read deadline: %w", err))
			}
			return
		}

		msg, err := ReadMessage(t.conn)
		if err != nil {
			// Check for timeout (both typed and string-based)
			// ReadMessage() may wrap the timeout error, so we check both ways
			isTimeout := false
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				isTimeout = true
			} else if strings.Contains(err.Error(), "i/o timeout") {
				isTimeout = true
			}

			if isTimeout {
				continue
			}

			isEOF := strings.Contains(err.Error(), "EOF")
			isConnClosed := strings.Contains(err.Error(), "use of closed network connection")

			if isEOF || isConnClosed {
				select {
				case <-t.ctx.Done():
					t.log().Debugw("bfcp.read.closed", "reason", "graceful_shutdown")
					return
				default:
					t.log().Debugw("bfcp.read.closed", "reason", "remote_peer")
					return
				}
			}

			if !t.IsClosed() {
				t.log().Warnw("bfcp.read.error", err)
				if t.OnError != nil {
					t.OnError(fmt.Errorf("failed to read message: %w", err))
				}
			}
			return
		}

		t.log().Debugw("bfcp.msg.received",
			"primitive", msg.Primitive.String(),
			"txID", msg.TransactionID,
			"attrCount", len(msg.Attributes))

		if t.OnMessage != nil {
			t.OnMessage(msg)
		}
	}
}

// SendMessage sends a BFCP message over the transport
func (t *Transport) SendMessage(msg *Message) error {
	return t.sendMessage(msg, false)
}

// SendKeepaliveMessage sends a BFCP keepalive message with minimal logging
func (t *Transport) SendKeepaliveMessage(msg *Message) error {
	return t.sendMessage(msg, true)
}

// SendRawData sends pre-encoded raw bytes over the transport
func (t *Transport) SendRawData(data []byte) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.closed {
		return fmt.Errorf("transport is closed")
	}

	if err := t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.log().Errorw("bfcp.write.deadline_failed", err)
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	if _, err := t.conn.Write(data); err != nil {
		t.log().Errorw("bfcp.write.failed", err, "bytes", len(data))
		return fmt.Errorf("failed to write raw data: %w", err)
	}

	t.log().Debugw("bfcp.msg.sent_raw", "bytes", len(data))
	return nil
}

func (t *Transport) sendMessage(msg *Message, isKeepalive bool) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.closed {
		return fmt.Errorf("transport is closed")
	}

	data, err := msg.Encode()
	if err != nil {
		t.log().Errorw("bfcp.encode.failed", err)
		return fmt.Errorf("failed to encode message: %w", err)
	}

	if err := t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.log().Errorw("bfcp.write.deadline_failed", err)
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	if _, err := t.conn.Write(data); err != nil {
		t.log().Errorw("bfcp.write.failed", err)
		return fmt.Errorf("failed to write message: %w", err)
	}

	if isKeepalive {
		t.log().Debugw("bfcp.keepalive.sent", "userID", msg.UserID, "txID", msg.TransactionID)
	} else {
		t.log().Debugw("bfcp.msg.sending",
			"primitive", msg.Primitive.String(),
			"txID", msg.TransactionID,
			"confID", msg.ConferenceID,
			"userID", msg.UserID)
	}

	return nil
}

// Close closes the transport connection
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}

	t.closed = true
	t.cancel()
	t.mu.Unlock()

	// Wait for keepalive goroutine to finish
	t.keepaliveWg.Wait()

	if t.conn != nil {
		return t.conn.Close()
	}

	return nil
}

// IsClosed returns whether the transport is closed
func (t *Transport) IsClosed() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.closed
}

// LocalAddr returns the local network address
func (t *Transport) LocalAddr() net.Addr {
	if t.conn == nil {
		return nil
	}
	return t.conn.LocalAddr()
}

// RemoteAddr returns the remote network address
func (t *Transport) RemoteAddr() net.Addr {
	if t.conn == nil {
		return nil
	}
	return t.conn.RemoteAddr()
}

// Role returns the connection role
func (t *Transport) Role() ConnectionRole {
	return t.role
}

// Listener represents a BFCP server that accepts connections
type Listener struct {
	listener net.Listener
	mu       sync.RWMutex
	closed   bool
	logger   Logger

	OnConnection func(*Transport)
	OnError      func(error)

	ctx    context.Context
	cancel context.CancelFunc
}

// SetLogger sets the logger for this listener
func (l *Listener) SetLogger(log Logger) {
	l.logger = log
}

func (l *Listener) log() Logger {
	if l.logger != nil {
		return l.logger
	}
	return NopLogger{}
}

// Listen creates a new BFCP listener on the specified address
func Listen(address string) (*Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", address, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Listener{
		listener: listener,
		ctx:      ctx,
		cancel:   cancel,
	}, nil
}

// ListenContext creates a new BFCP listener with context
func ListenContext(ctx context.Context, address string) (*Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", address, err)
	}

	return &Listener{
		listener: listener,
		ctx:      ctx,
	}, nil
}

// Start begins accepting connections
func (l *Listener) Start() {
	go l.acceptLoop()
}

// acceptLoop continuously accepts new connections
func (l *Listener) acceptLoop() {
	for {
		select {
		case <-l.ctx.Done():
			return
		default:
		}

		// Set accept deadline to allow periodic context checks
		if tcpListener, ok := l.listener.(*net.TCPListener); ok {
			tcpListener.SetDeadline(time.Now().Add(1 * time.Second))
		}

		conn, err := l.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if !l.IsClosed() && l.OnError != nil {
				l.OnError(fmt.Errorf("failed to accept connection: %w", err))
			}
			return
		}

		transport := NewTransport(conn, RolePassive)

		if l.OnConnection != nil {
			go l.OnConnection(transport)
		}
	}
}

// Close closes the listener
func (l *Listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}

	if l.listener != nil {
		return l.listener.Close()
	}

	return nil
}

// IsClosed returns whether the listener is closed
func (l *Listener) IsClosed() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.closed
}

// Addr returns the listener's network address
func (l *Listener) Addr() net.Addr {
	if l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

// ListenPortRange creates a new BFCP listener on the first available port in the given range.
func ListenPortRange(ip string, portMin, portMax int) (*Listener, error) {
	var lastErr error
	for port := portMin; port < portMax; port++ {
		address := fmt.Sprintf("%s:%d", ip, port)
		listener, err := net.Listen("tcp", address)
		if err == nil {
			ctx, cancel := context.WithCancel(context.Background())
			return &Listener{
				listener: listener,
				ctx:      ctx,
				cancel:   cancel,
			}, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no available TCP port in range %d-%d: %w", portMin, portMax, lastErr)
}
