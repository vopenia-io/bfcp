package bfcp

import (
	"context"
	"fmt"
	"log"
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

	// Message handlers
	OnMessage func(*Message)
	OnError   func(error)
	OnClose   func()

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Keepalive settings
	keepaliveInterval time.Duration
	keepaliveSender   func() error // Function to send keepalive message
	keepaliveWg       sync.WaitGroup
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
	log.Printf("🔌 [BFCP Transport] Dialing TCP connection to %s (timeout: 10s)", address)
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		log.Printf("❌ [BFCP Transport] TCP dial failed to %s: %v", address, err)
		return nil, fmt.Errorf("failed to dial %s: %w", address, err)
	}

	log.Printf("✅ [BFCP Transport] TCP connection established to %s", address)
	log.Printf("   ➜ Local address: %s", conn.LocalAddr())
	log.Printf("   ➜ Remote address: %s", conn.RemoteAddr())

	return NewTransport(conn, RoleActive), nil
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

// StartKeepalive starts the keepalive goroutine if configured
// This should be called after EnableKeepalive() to actually start sending keepalives
func (t *Transport) StartKeepalive() {
	t.mu.RLock()
	interval := t.keepaliveInterval
	sender := t.keepaliveSender
	t.mu.RUnlock()

	if interval > 0 && sender != nil {
		log.Printf("💓 [BFCP Transport] Starting keepalive (interval: %v)", interval)
		t.keepaliveWg.Add(1)
		go t.keepaliveLoop()
	}
}

// Start begins reading messages from the connection
func (t *Transport) Start() {
	log.Printf("📖 [BFCP Transport] Starting read loop (role: %s)", t.role)
	go t.readLoop()

	// Start keepalive if configured
	if t.keepaliveInterval > 0 && t.keepaliveSender != nil {
		log.Printf("💓 [BFCP Transport] Starting keepalive (interval: %v)", t.keepaliveInterval)
		t.keepaliveWg.Add(1)
		go t.keepaliveLoop()
	}
}

// keepaliveLoop sends periodic keepalive messages
func (t *Transport) keepaliveLoop() {
	defer t.keepaliveWg.Done()
	ticker := time.NewTicker(t.keepaliveInterval)
	defer ticker.Stop()

	log.Printf("💓 [BFCP Transport] Keepalive loop started (interval: %v)", t.keepaliveInterval)

	for {
		select {
		case <-t.ctx.Done():
			log.Printf("⏹️ [BFCP Transport] Keepalive loop stopping (context cancelled)")
			return
		case <-ticker.C:
			if t.IsClosed() {
				log.Printf("⏹️ [BFCP Transport] Keepalive loop stopping (transport closed)")
				return
			}

			// Call keepalive sender without the extra log (sender will handle logging)
			if err := t.keepaliveSender(); err != nil {
				log.Printf("⚠️ [BFCP Transport] Keepalive send failed: %v", err)
				// Don't stop on keepalive failure - connection might still be alive
			}
		}
	}
}

// readLoop continuously reads messages from the connection
func (t *Transport) readLoop() {
	log.Printf("🔄 [BFCP Transport] Read loop started")
	defer func() {
		log.Printf("🛑 [BFCP Transport] Read loop exiting, closing connection")
		t.Close()
		if t.OnClose != nil {
			t.OnClose()
		}
	}()

	for {
		select {
		case <-t.ctx.Done():
			log.Printf("⏹️ [BFCP Transport] Context cancelled, stopping read loop")
			return
		default:
		}

		// Set read deadline to allow periodic context checks
		if err := t.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			log.Printf("❌ [BFCP Transport] Failed to set read deadline: %v", err)
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
				// Timeout is expected when using keepalives
				// Just continue reading, keepalive will maintain connection health
				continue
			}

			// Check if this is EOF or connection closed (graceful shutdown scenarios)
			isEOF := strings.Contains(err.Error(), "EOF")
			isConnClosed := strings.Contains(err.Error(), "use of closed network connection")

			if isEOF || isConnClosed {
				// EOF or closed connection - check if it's expected
				select {
				case <-t.ctx.Done():
					// Context cancelled - this is intentional close
					log.Printf("⏹️ [BFCP Transport] Connection closed during graceful shutdown")
					return
				default:
					// Remote peer closed connection - this is normal, not an error
					log.Printf("⏹️ [BFCP Transport] Connection closed by remote peer")
					return
				}
			}

			// Real error - log and close connection
			if !t.IsClosed() {
				log.Printf("❌ [BFCP Transport] Read error: %v", err)
				if t.OnError != nil {
					t.OnError(fmt.Errorf("failed to read message: %w", err))
				}
			}
			return
		}

		log.Printf("📨 [BFCP Transport] Message received: Primitive=%s, TxID=%d, Length=%d bytes",
			msg.Primitive, msg.TransactionID, len(msg.Attributes))

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

// sendMessage sends a BFCP message over the transport with optional verbose flag
func (t *Transport) sendMessage(msg *Message, isKeepalive bool) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.closed {
		log.Printf("❌ [BFCP Transport] Cannot send - transport is closed")
		return fmt.Errorf("transport is closed")
	}

	// Encode message
	data, err := msg.Encode()
	if err != nil {
		log.Printf("❌ [BFCP Transport] Failed to encode message: %v", err)
		return fmt.Errorf("failed to encode message: %w", err)
	}

	// Set write deadline
	if err := t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Printf("❌ [BFCP Transport] Failed to set write deadline: %v", err)
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	if _, err := t.conn.Write(data); err != nil {
		log.Printf("❌ [BFCP Transport] Failed to write message: %v", err)
		return fmt.Errorf("failed to write message: %w", err)
	}

	// For keepalive messages, log a single concise line
	if isKeepalive {
		log.Printf("💓 [BFCP] Keepalive sent to user %d (TxID=%d, %d bytes)", msg.UserID, msg.TransactionID, len(data))
	} else {
		// For non-keepalive messages, log verbose details
		log.Printf("📤 [BFCP Transport] Sending message: Primitive=%s, TxID=%d, ConferenceID=%d, UserID=%d",
			msg.Primitive, msg.TransactionID, msg.ConferenceID, msg.UserID)
		log.Printf("🔍 [BFCP Transport] Message hex dump (%d bytes): %X", len(data), data)
		log.Printf("✅ [BFCP Transport] Message sent successfully")
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

	// Connection handler
	OnConnection func(*Transport)
	OnError      func(error)

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
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
				// Timeout is expected, continue
				continue
			}
			if !l.IsClosed() && l.OnError != nil {
				l.OnError(fmt.Errorf("failed to accept connection: %w", err))
			}
			return
		}

		// Create transport for this connection
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
