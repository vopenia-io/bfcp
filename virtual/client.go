package virtual

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vopenia-io/bfcp"
)

// Common errors
var (
	ErrClientNotConnected  = errors.New("virtual client not connected")
	ErrFloorNotHeld        = errors.New("floor not held by this client")
	ErrRequestTimeout      = errors.New("floor request timed out")
	ErrClientAlreadyExists = errors.New("client with this ID already exists")
	ErrClientClosed        = errors.New("client is closed")
	ErrInvalidFloorID      = errors.New("invalid floor ID")
	ErrInvalidRequestID    = errors.New("invalid request ID")
)

// Client represents a virtual BFCP participant that doesn't natively support BFCP.
// Each client maintains its own connection to the BFCP server and manages floor
// requests on behalf of a non-BFCP endpoint (e.g., WebRTC participant).
//
// The client is thread-safe and handles all connection lifecycle, message routing,
// and state management automatically.
type Client struct {
	// Core identifiers
	id           string // Unique identifier for this virtual client
	userID       uint16 // BFCP User ID assigned to this client
	conferenceID uint32 // Conference ID this client belongs to
	serverAddr   string // BFCP server address

	// BFCP client
	client *bfcp.Client

	// State tracking
	activeRequests map[uint16]*FloorRequest // requestID -> request details
	heldFloors     map[uint16]uint16        // floorID -> requestID
	requestTimeout time.Duration            // Timeout for floor requests

	// Callbacks
	callbacks Callbacks

	// Lifecycle
	ctx       context.Context
	cancel    context.CancelFunc
	closed    atomic.Bool
	connected atomic.Bool

	// Synchronization
	mu sync.RWMutex
}

// FloorRequest represents an active or completed floor request
type FloorRequest struct {
	RequestID     uint16             // BFCP floor request ID
	FloorID       uint16             // Floor identifier
	Status        bfcp.RequestStatus // Current status of the request
	Priority      bfcp.Priority      // Request priority
	QueuePosition uint8              // Position in queue (0 = granted)
	RequestedAt   time.Time          // When the request was made
	Information   string             // Participant-provided information
	BeneficiaryID uint16             // Beneficiary user ID (0 if not set)
}

// ClientStatus represents the current status of a virtual client
type ClientStatus struct {
	ID              string    // Client identifier
	UserID          uint16    // BFCP User ID
	Connected       bool      // Connection status
	ActiveFloors    []uint16  // List of currently held floor IDs
	PendingRequests []uint16  // List of pending request IDs
	LastActivity    time.Time // Last activity timestamp
}

// RequestOption is a functional option for configuring floor requests
type RequestOption func(*floorRequestOptions)

type floorRequestOptions struct {
	priority      bfcp.Priority
	information   string
	beneficiaryID uint16
}

// WithPriority sets the priority for a floor request
func WithPriority(p bfcp.Priority) RequestOption {
	return func(opts *floorRequestOptions) {
		opts.priority = p
	}
}

// WithInformation sets participant-provided information for a floor request
func WithInformation(info string) RequestOption {
	return func(opts *floorRequestOptions) {
		opts.information = info
	}
}

// WithBeneficiary sets the beneficiary user ID for a floor request
func WithBeneficiary(userID uint16) RequestOption {
	return func(opts *floorRequestOptions) {
		opts.beneficiaryID = userID
	}
}

// New creates a new virtual BFCP client.
//
// Parameters:
//
//	id - Unique identifier for this client (e.g., "webrtc-user-123")
//	serverAddr - BFCP server address (e.g., "localhost:5070")
//	conferenceID - Conference ID to join
//	userID - BFCP User ID for this client
//	callbacks - Event callbacks (use NoOpCallbacks if none needed)
//	timeout - Timeout for floor requests (use 0 for default 30 seconds)
//
// The client is created in a disconnected state. Call Connect() to establish
// the connection to the BFCP server.
func New(id string, serverAddr string, conferenceID uint32, userID uint16, callbacks Callbacks, timeout time.Duration) (*Client, error) {
	if id == "" {
		return nil, errors.New("client ID cannot be empty")
	}
	if serverAddr == "" {
		return nil, errors.New("server address cannot be empty")
	}
	if callbacks == nil {
		callbacks = &NoOpCallbacks{}
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())

	c := &Client{
		id:             id,
		userID:         userID,
		conferenceID:   conferenceID,
		serverAddr:     serverAddr,
		activeRequests: make(map[uint16]*FloorRequest),
		heldFloors:     make(map[uint16]uint16),
		callbacks:      callbacks,
		ctx:            ctx,
		cancel:         cancel,
		requestTimeout: timeout,
	}

	return c, nil
}

// Connect establishes a connection to the BFCP server and performs the
// Hello handshake. This must be called before any floor operations.
func (c *Client) Connect() error {
	if c.closed.Load() {
		return ErrClientClosed
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Create BFCP client
	config := bfcp.DefaultClientConfig(c.serverAddr, c.conferenceID, c.userID)
	config.EnableLogging = false

	client := bfcp.NewClient(config)

	// Set up callbacks to translate BFCP events to virtual client events
	client.OnConnected = func() {
		c.connected.Store(true)
		c.callbacks.OnConnected()
	}

	client.OnDisconnected = func() {
		wasConnected := c.connected.Swap(false)
		if wasConnected {
			c.callbacks.OnDisconnected(errors.New("connection lost"))
		}
	}

	client.OnFloorGranted = func(floorID, requestID uint16) {
		c.mu.Lock()
		c.heldFloors[floorID] = requestID
		if req, exists := c.activeRequests[requestID]; exists {
			req.Status = bfcp.RequestStatusGranted
			req.QueuePosition = 0
		}
		c.mu.Unlock()
		c.callbacks.OnFloorGranted(floorID, requestID)
	}

	client.OnFloorDenied = func(floorID, requestID uint16, errorCode bfcp.ErrorCode) {
		c.mu.Lock()
		if req, exists := c.activeRequests[requestID]; exists {
			req.Status = bfcp.RequestStatusDenied
			delete(c.activeRequests, requestID)
		}
		c.mu.Unlock()
		c.callbacks.OnFloorDenied(floorID, requestID, errorCode, errorCode.String())
	}

	client.OnFloorRevoked = func(floorID uint16) {
		c.mu.Lock()
		requestID, held := c.heldFloors[floorID]
		if held {
			delete(c.heldFloors, floorID)
			if req, exists := c.activeRequests[requestID]; exists {
				req.Status = bfcp.RequestStatusRevoked
				delete(c.activeRequests, requestID)
			}
		}
		c.mu.Unlock()
		if held {
			c.callbacks.OnFloorRevoked(floorID, requestID)
		}
	}

	client.OnFloorReleased = func(floorID uint16) {
		c.mu.Lock()
		requestID, held := c.heldFloors[floorID]
		if held {
			delete(c.heldFloors, floorID)
			if req, exists := c.activeRequests[requestID]; exists {
				req.Status = bfcp.RequestStatusReleased
				delete(c.activeRequests, requestID)
			}
		}
		c.mu.Unlock()
		if held {
			c.callbacks.OnFloorReleased(floorID, requestID)
		}
	}

	client.OnFloorStatus = func(floorID uint16, status bfcp.RequestStatus) {
		c.mu.RLock()
		var requestID uint16
		for reqID, req := range c.activeRequests {
			if req.FloorID == floorID {
				requestID = reqID
				break
			}
		}
		c.mu.RUnlock()

		if requestID != 0 {
			c.callbacks.OnFloorStatusChanged(floorID, requestID, status)
		}
	}

	client.OnError = func(err error) {
		c.callbacks.OnError(err)
	}

	c.client = client

	// Connect and perform Hello handshake
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	if err := client.Hello(); err != nil {
		client.Disconnect()
		return fmt.Errorf("failed to perform Hello handshake: %w", err)
	}

	return nil
}

// RequestFloor requests control of a floor from the BFCP server.
// Returns the request ID that can be used to track and release the floor.
//
// The request is asynchronous - use callbacks to determine when the floor
// is granted, denied, or queued.
func (c *Client) RequestFloor(floorID uint16, opts ...RequestOption) (uint16, error) {
	if c.closed.Load() {
		return 0, ErrClientClosed
	}
	if !c.connected.Load() {
		return 0, ErrClientNotConnected
	}
	if floorID == 0 {
		return 0, ErrInvalidFloorID
	}

	// Apply options
	options := &floorRequestOptions{
		priority: bfcp.PriorityNormal,
	}
	for _, opt := range opts {
		opt(options)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Send floor request via underlying BFCP client
	requestID, err := c.client.RequestFloor(floorID, options.beneficiaryID, options.priority)
	if err != nil {
		return 0, fmt.Errorf("failed to request floor: %w", err)
	}

	// Track the request
	c.activeRequests[requestID] = &FloorRequest{
		RequestID:     requestID,
		FloorID:       floorID,
		Status:        bfcp.RequestStatusPending,
		Priority:      options.priority,
		RequestedAt:   time.Now(),
		Information:   options.information,
		BeneficiaryID: options.beneficiaryID,
	}

	return requestID, nil
}

// ReleaseFloor releases a previously granted floor.
func (c *Client) ReleaseFloor(requestID uint16) error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	if !c.connected.Load() {
		return ErrClientNotConnected
	}
	if requestID == 0 {
		return ErrInvalidRequestID
	}

	c.mu.Lock()
	req, exists := c.activeRequests[requestID]
	if !exists {
		c.mu.Unlock()
		return errors.New("request ID not found")
	}
	floorID := req.FloorID
	c.mu.Unlock()

	// Release via underlying BFCP client
	if err := c.client.ReleaseFloor(floorID); err != nil {
		return fmt.Errorf("failed to release floor: %w", err)
	}

	return nil
}

// ReleaseAllFloors releases all floors currently held by this client.
func (c *Client) ReleaseAllFloors() error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	if !c.connected.Load() {
		return ErrClientNotConnected
	}

	c.mu.RLock()
	floorIDs := make([]uint16, 0, len(c.heldFloors))
	for floorID := range c.heldFloors {
		floorIDs = append(floorIDs, floorID)
	}
	c.mu.RUnlock()

	var lastErr error
	for _, floorID := range floorIDs {
		if err := c.client.ReleaseFloor(floorID); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// GetStatus returns the current status of this client.
func (c *Client) GetStatus() *ClientStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := &ClientStatus{
		ID:              c.id,
		UserID:          c.userID,
		Connected:       c.connected.Load(),
		ActiveFloors:    make([]uint16, 0, len(c.heldFloors)),
		PendingRequests: make([]uint16, 0, len(c.activeRequests)),
		LastActivity:    time.Now(),
	}

	for floorID := range c.heldFloors {
		status.ActiveFloors = append(status.ActiveFloors, floorID)
	}

	for requestID, req := range c.activeRequests {
		if req.Status == bfcp.RequestStatusPending || req.Status == bfcp.RequestStatusAccepted {
			status.PendingRequests = append(status.PendingRequests, requestID)
		}
	}

	return status
}

// IsConnected returns true if the client is currently connected to the BFCP server.
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// ID returns the unique identifier for this client.
func (c *Client) ID() string {
	return c.id
}

// UserID returns the BFCP User ID for this client.
func (c *Client) UserID() uint16 {
	return c.userID
}

// ConferenceID returns the conference ID this client is participating in.
func (c *Client) ConferenceID() uint32 {
	return c.conferenceID
}

// Close gracefully shuts down the client, releasing all floors and
// disconnecting from the server.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil // Already closed
	}

	c.cancel()

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client != nil {
		// Try to release all floors before disconnecting
		_ = c.ReleaseAllFloors()

		// Disconnect gracefully
		if err := client.Disconnect(); err != nil {
			return fmt.Errorf("error during disconnect: %w", err)
		}
	}

	return nil
}
