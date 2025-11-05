package bfcp

import (
	"context"
	"fmt"
	"net"
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

// Start begins reading messages from the connection
func (t *Transport) Start() {
	go t.readLoop()
}

// readLoop continuously reads messages from the connection
func (t *Transport) readLoop() {
	defer func() {
		t.Close()
		if t.OnClose != nil {
			t.OnClose()
		}
	}()

	for {
		select {
		case <-t.ctx.Done():
			return
		default:
		}

		// Set read deadline to allow periodic context checks
		if err := t.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			if t.OnError != nil {
				t.OnError(fmt.Errorf("failed to set read deadline: %w", err))
			}
			return
		}

		msg, err := ReadMessage(t.conn)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Timeout is expected, continue
				continue
			}
			if !t.IsClosed() && t.OnError != nil {
				t.OnError(fmt.Errorf("failed to read message: %w", err))
			}
			return
		}

		if t.OnMessage != nil {
			t.OnMessage(msg)
		}
	}
}

// SendMessage sends a BFCP message over the transport
func (t *Transport) SendMessage(msg *Message) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.closed {
		return fmt.Errorf("transport is closed")
	}

	// Set write deadline
	if err := t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	if err := WriteMessage(t.conn, msg); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

// Close closes the transport connection
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	t.closed = true
	t.cancel()

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
