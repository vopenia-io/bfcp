package bfcp

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ClientConfig holds configuration for the BFCP client
type ClientConfig struct {
	ServerAddress  string
	ConferenceID   uint32
	UserID         uint16
	EnableLogging  bool
	ConnectTimeout time.Duration
	ConnectionRole string
}

// DefaultClientConfig returns a default client configuration
func DefaultClientConfig(serverAddr string, conferenceID uint32, userID uint16) *ClientConfig {
	return &ClientConfig{
		ServerAddress:  serverAddr,
		ConferenceID:   conferenceID,
		UserID:         userID,
		EnableLogging:  true,
		ConnectTimeout: 10 * time.Second,
	}
}

// Client represents a BFCP floor control client
type Client struct {
	config    *ClientConfig
	transport *Transport

	// State
	state      SessionState
	stateMu    sync.RWMutex
	connected  atomic.Bool
	nextTxID   atomic.Uint32

	// Active floor requests
	activeRequests map[uint16]*ActiveFloorRequest // FloorID -> ActiveFloorRequest
	requestsMu     sync.RWMutex

	// Pending responses (waiting for server response)
	pendingResponses map[uint16]chan *Message // TransactionID -> response channel
	pendingMu        sync.RWMutex

	// Event callbacks
	OnConnected      func()
	OnDisconnected   func()
	OnFloorGranted   func(floorID, requestID uint16)
	OnFloorDenied    func(floorID, requestID uint16, errorCode ErrorCode)
	OnFloorReleased  func(floorID uint16)
	OnFloorRevoked   func(floorID uint16)
	OnFloorStatus    func(floorID uint16, status RequestStatus)
	OnError          func(error)
}

// ActiveFloorRequest represents an active floor request
type ActiveFloorRequest struct {
	FloorID        uint16
	FloorRequestID uint16
	BeneficiaryID  uint16
	Priority       Priority
	Status         RequestStatus
	RequestedAt    time.Time
	GrantedAt      time.Time
}

// NewClient creates a new BFCP floor control client
func NewClient(config *ClientConfig) *Client {
	if config == nil {
		config = DefaultClientConfig("localhost:5070", 1, 1)
	}

	return &Client{
		config:           config,
		state:            StateDisconnected,
		activeRequests:   make(map[uint16]*ActiveFloorRequest),
		pendingResponses: make(map[uint16]chan *Message),
	}
}

// Connect establishes a connection to the BFCP server
func (c *Client) Connect() error {
	if c.connected.Load() {
		c.logf("⚠️ Already connected to BFCP server")
		return fmt.Errorf("already connected")
	}

	c.logf("🔌 [BFCP] Attempting to connect to %s", c.config.ServerAddress)

	transport, err := Dial(c.config.ServerAddress)
	if err != nil {
		c.logf("❌ [BFCP] Failed to connect to %s: %v", c.config.ServerAddress, err)
		return fmt.Errorf("failed to connect: %w", err)
	}

	c.logf("✅ [BFCP] TCP connection established to %s", c.config.ServerAddress)

	c.transport = transport
	c.setState(StateConnected)
	c.logf("🔄 [BFCP] State changed to: %s", StateConnected)

	// Set up transport callbacks
	transport.OnMessage = c.handleMessage
	transport.OnError = func(err error) {
		c.logf("Transport error: %v", err)
		if c.OnError != nil {
			c.OnError(err)
		}
	}
	transport.OnClose = func() {
		c.logf("Connection closed")
		c.connected.Store(false)
		c.setState(StateDisconnected)
		if c.OnDisconnected != nil {
			c.OnDisconnected()
		}
	}

	// Start reading messages
	c.logf("📖 [BFCP] Starting transport read loop")
	transport.Start()
	c.connected.Store(true)

	c.logf("✅ [BFCP] Client connected and ready (ConferenceID: %d, UserID: %d)", c.config.ConferenceID, c.config.UserID)

	if c.OnConnected != nil {
		c.logf("📞 [BFCP] Calling OnConnected callback")
		c.OnConnected()
	}

	return nil
}

// Disconnect closes the connection to the BFCP server
func (c *Client) Disconnect() error {
	if !c.connected.Load() {
		return nil
	}

	c.logf("Disconnecting from %s", c.config.ServerAddress)

	// Send Goodbye
	if err := c.Goodbye(); err != nil {
		c.logf("Failed to send Goodbye: %v", err)
	}

	// Close transport
	if c.transport != nil {
		return c.transport.Close()
	}

	return nil
}

// Hello sends a Hello message to the server
func (c *Client) Hello() error {
	if !c.connected.Load() {
		c.logf("❌ [BFCP] Cannot send Hello - not connected")
		return fmt.Errorf("not connected")
	}

	txID := c.nextTransactionID()
	c.logf("👋 [BFCP] Sending Hello message (TxID: %d)", txID)
	msg := NewMessage(PrimitiveHello, c.config.ConferenceID, txID, c.config.UserID)

	// Add supported primitives
	supportedPrimitives := []Primitive{
		PrimitiveFloorRequest,
		PrimitiveFloorRelease,
		PrimitiveFloorRequestQuery,
		PrimitiveFloorRequestStatus,
		PrimitiveFloorStatus,
		PrimitiveHello,
		PrimitiveHelloAck,
		PrimitiveError,
		PrimitiveGoodbye,
		PrimitiveGoodbyeAck,
	}
	msg.AddSupportedPrimitives(supportedPrimitives)

	// Add supported attributes
	supportedAttributes := []AttributeType{
		AttrBeneficiaryID,
		AttrFloorID,
		AttrFloorRequestID,
		AttrPriority,
		AttrRequestStatus,
		AttrErrorCode,
		AttrErrorInfo,
		AttrSupportedAttributes,
		AttrSupportedPrimitives,
	}
	msg.AddSupportedAttributes(supportedAttributes)

	// Send and wait for HelloAck
	c.logf("⏳ [BFCP] Waiting for HelloAck (timeout: 5s)")
	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		c.logf("❌ [BFCP] Hello handshake failed: %v", err)
		return fmt.Errorf("failed to send Hello: %w", err)
	}

	if response.Primitive != PrimitiveHelloAck {
		c.logf("❌ [BFCP] Expected HelloAck, got %s", response.Primitive)
		return fmt.Errorf("expected HelloAck, got %s", response.Primitive)
	}

	c.setState(StateWaitFloorRequest)
	c.logf("✅ [BFCP] Hello handshake completed - ready to request floor")
	c.logf("🔄 [BFCP] State changed to: %s", StateWaitFloorRequest)
	return nil
}

// RequestFloor requests control of a floor
func (c *Client) RequestFloor(floorID uint16, beneficiaryID uint16, priority Priority) (uint16, error) {
	if !c.connected.Load() {
		c.logf("❌ [BFCP] Cannot request floor - not connected")
		return 0, fmt.Errorf("not connected")
	}

	txID := c.nextTransactionID()
	c.logf("🎤 [BFCP] Requesting floor (FloorID: %d, BeneficiaryID: %d, Priority: %d, TxID: %d)", floorID, beneficiaryID, priority, txID)
	msg := NewMessage(PrimitiveFloorRequest, c.config.ConferenceID, txID, c.config.UserID)

	msg.AddFloorID(floorID)
	if beneficiaryID > 0 {
		msg.AddBeneficiaryID(beneficiaryID)
		c.logf("   ➜ Added BENEFICIARY-ID: %d", beneficiaryID)
	}
	if priority > 0 {
		msg.AddPriority(priority)
		c.logf("   ➜ Added PRIORITY: %d", priority)
	}

	// Send and wait for FloorRequestStatus
	c.logf("⏳ [BFCP] Waiting for FloorRequestStatus (timeout: 5s)")
	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		c.logf("❌ [BFCP] Floor request failed: %v", err)
		return 0, fmt.Errorf("failed to request floor: %w", err)
	}

	if response.Primitive == PrimitiveError {
		errorCode, _ := response.GetErrorCode()
		errorInfo, _ := response.GetErrorInfo()
		c.logf("❌ [BFCP] Server returned error: %s - %s", errorCode, errorInfo)
		return 0, fmt.Errorf("floor request failed: %s - %s", errorCode, errorInfo)
	}

	if response.Primitive != PrimitiveFloorRequestStatus {
		c.logf("❌ [BFCP] Expected FloorRequestStatus, got %s", response.Primitive)
		return 0, fmt.Errorf("expected FloorRequestStatus, got %s", response.Primitive)
	}

	c.logf("📩 [BFCP] Received FloorRequestStatus response")

	// Extract floor request ID and status
	requestID, ok := response.GetFloorRequestID()
	if !ok {
		c.logf("❌ [BFCP] Response missing FLOOR-REQUEST-ID attribute")
		return 0, fmt.Errorf("missing FLOOR-REQUEST-ID in response")
	}
	c.logf("   ➜ Floor Request ID: %d", requestID)

	status, queuePos, ok := response.GetRequestStatus()
	if !ok {
		c.logf("❌ [BFCP] Response missing REQUEST-STATUS attribute")
		return 0, fmt.Errorf("missing REQUEST-STATUS in response")
	}
	c.logf("   ➜ Request Status: %s (Queue Position: %d)", status, queuePos)

	// Track the request
	c.requestsMu.Lock()
	c.activeRequests[floorID] = &ActiveFloorRequest{
		FloorID:        floorID,
		FloorRequestID: requestID,
		BeneficiaryID:  beneficiaryID,
		Priority:       priority,
		Status:         status,
		RequestedAt:    time.Now(),
	}
	c.requestsMu.Unlock()

	c.logf("📝 [BFCP] Floor %d request tracked (RequestID: %d, Status: %s)", floorID, requestID, status)

	if status == RequestStatusGranted {
		c.setState(StateFloorGranted)
		c.logf("✅ [BFCP] Floor %d GRANTED immediately! 🎉", floorID)
		c.logf("🔄 [BFCP] State changed to: %s", StateFloorGranted)
		c.requestsMu.Lock()
		if req, exists := c.activeRequests[floorID]; exists {
			req.GrantedAt = time.Now()
			c.logf("   ➜ Grant time recorded: %s", req.GrantedAt.Format("15:04:05.000"))
		}
		c.requestsMu.Unlock()

		if c.OnFloorGranted != nil {
			c.logf("📞 [BFCP] Calling OnFloorGranted callback (FloorID: %d, RequestID: %d)", floorID, requestID)
			c.OnFloorGranted(floorID, requestID)
		}
	} else if status == RequestStatusPending || status == RequestStatusAccepted {
		c.setState(StateFloorRequested)
		c.logf("⏳ [BFCP] Floor %d request %s - waiting for grant", floorID, status)
		c.logf("🔄 [BFCP] State changed to: %s", StateFloorRequested)
	} else {
		c.logf("⚠️ [BFCP] Floor %d request status: %s", floorID, status)
	}

	return requestID, nil
}

// ReleaseFloor releases control of a floor
func (c *Client) ReleaseFloor(floorID uint16) error {
	if !c.connected.Load() {
		return fmt.Errorf("not connected")
	}

	c.requestsMu.RLock()
	activeReq, exists := c.activeRequests[floorID]
	c.requestsMu.RUnlock()

	if !exists {
		return fmt.Errorf("no active request for floor %d", floorID)
	}

	txID := c.nextTransactionID()
	msg := NewMessage(PrimitiveFloorRelease, c.config.ConferenceID, txID, c.config.UserID)

	msg.AddFloorID(floorID)
	msg.AddFloorRequestID(activeReq.FloorRequestID)

	// Send and wait for FloorRequestStatus
	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to release floor: %w", err)
	}

	if response.Primitive == PrimitiveError {
		errorCode, _ := response.GetErrorCode()
		errorInfo, _ := response.GetErrorInfo()
		return fmt.Errorf("floor release failed: %s - %s", errorCode, errorInfo)
	}

	// Remove from active requests
	c.requestsMu.Lock()
	delete(c.activeRequests, floorID)
	c.requestsMu.Unlock()

	c.setState(StateFloorReleased)
	c.logf("Floor %d released", floorID)

	if c.OnFloorReleased != nil {
		c.OnFloorReleased(floorID)
	}

	return nil
}

// QueryFloor queries the status of a floor
func (c *Client) QueryFloor(floorID uint16) (*Message, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	txID := c.nextTransactionID()
	msg := NewMessage(PrimitiveFloorQuery, c.config.ConferenceID, txID, c.config.UserID)
	msg.AddFloorID(floorID)

	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to query floor: %w", err)
	}

	return response, nil
}

// Goodbye sends a Goodbye message to the server
func (c *Client) Goodbye() error {
	if !c.connected.Load() {
		return fmt.Errorf("not connected")
	}

	txID := c.nextTransactionID()
	msg := NewMessage(PrimitiveGoodbye, c.config.ConferenceID, txID, c.config.UserID)

	// Send without waiting for response (best effort)
	return c.send(msg)
}

// handleMessage processes incoming messages from the server
func (c *Client) handleMessage(msg *Message) {
	c.logf("Received %s (TxID: %d)", msg.Primitive, msg.TransactionID)

	// Check if this is a response to a pending request
	c.pendingMu.RLock()
	respChan, isPending := c.pendingResponses[msg.TransactionID]
	c.pendingMu.RUnlock()

	if isPending {
		// Try to send to waiting goroutine (non-blocking)
		select {
		case respChan <- msg:
			// Successfully sent to waiting goroutine, done
			return
		default:
			// Channel full (already has a response), fall through to handle as unsolicited
		}
	}

	// Handle unsolicited messages (server-initiated)
	switch msg.Primitive {
	case PrimitiveFloorRequestStatus:
		c.handleFloorRequestStatus(msg)
	case PrimitiveFloorStatus:
		c.handleFloorStatus(msg)
	case PrimitiveError:
		c.handleError(msg)
	default:
		c.logf("Unexpected message: %s", msg.Primitive)
	}
}

// handleFloorRequestStatus processes a FloorRequestStatus message
func (c *Client) handleFloorRequestStatus(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		c.logf("FloorRequestStatus missing FLOOR-ID")
		return
	}

	requestID, _ := msg.GetFloorRequestID()
	status, _, ok := msg.GetRequestStatus()
	if !ok {
		c.logf("FloorRequestStatus missing REQUEST-STATUS")
		return
	}

	c.logf("Floor %d status update: %s (RequestID: %d)", floorID, status, requestID)

	// Update active request
	c.requestsMu.Lock()
	if req, exists := c.activeRequests[floorID]; exists {
		req.Status = status
		if status == RequestStatusGranted && req.GrantedAt.IsZero() {
			req.GrantedAt = time.Now()
		}
	}
	c.requestsMu.Unlock()

	// Handle status changes
	switch status {
	case RequestStatusGranted:
		c.setState(StateFloorGranted)
		if c.OnFloorGranted != nil {
			c.OnFloorGranted(floorID, requestID)
		}
	case RequestStatusDenied:
		c.setState(StateFloorDenied)
		if c.OnFloorDenied != nil {
			c.OnFloorDenied(floorID, requestID, 0)
		}
	case RequestStatusReleased:
		c.setState(StateFloorReleased)
		if c.OnFloorReleased != nil {
			c.OnFloorReleased(floorID)
		}
	case RequestStatusRevoked:
		c.setState(StateFloorReleased)
		if c.OnFloorRevoked != nil {
			c.OnFloorRevoked(floorID)
		}
	}
}

// handleFloorStatus processes a FloorStatus message
func (c *Client) handleFloorStatus(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		c.logf("FloorStatus missing FLOOR-ID")
		return
	}

	status, _, ok := msg.GetRequestStatus()
	if !ok {
		c.logf("FloorStatus missing REQUEST-STATUS")
		return
	}

	if c.OnFloorStatus != nil {
		c.OnFloorStatus(floorID, status)
	}
}

// handleError processes an Error message
func (c *Client) handleError(msg *Message) {
	errorCode, _ := msg.GetErrorCode()
	errorInfo, _ := msg.GetErrorInfo()

	c.logf("Received error: %s - %s", errorCode, errorInfo)

	if c.OnError != nil {
		c.OnError(fmt.Errorf("BFCP error: %s - %s", errorCode, errorInfo))
	}
}

// sendAndWait sends a message and waits for a response
func (c *Client) sendAndWait(msg *Message, timeout time.Duration) (*Message, error) {
	// Create response channel
	respChan := make(chan *Message, 1)

	c.pendingMu.Lock()
	c.pendingResponses[msg.TransactionID] = respChan
	c.pendingMu.Unlock()

	// Clean up on return
	defer func() {
		c.pendingMu.Lock()
		delete(c.pendingResponses, msg.TransactionID)
		c.pendingMu.Unlock()
		close(respChan)
	}()

	// Send message
	if err := c.send(msg); err != nil {
		return nil, err
	}

	// Wait for response
	select {
	case response := <-respChan:
		return response, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for response to %s", msg.Primitive)
	}
}

// send sends a message to the server
func (c *Client) send(msg *Message) error {
	if c.transport == nil {
		return fmt.Errorf("transport not initialized")
	}

	if err := c.transport.SendMessage(msg); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	c.logf("Sent %s (TxID: %d)", msg.Primitive, msg.TransactionID)
	return nil
}

// GetActiveRequests returns all active floor requests
func (c *Client) GetActiveRequests() map[uint16]*ActiveFloorRequest {
	c.requestsMu.RLock()
	defer c.requestsMu.RUnlock()

	result := make(map[uint16]*ActiveFloorRequest)
	for k, v := range c.activeRequests {
		result[k] = v
	}
	return result
}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// GetState returns the current session state
func (c *Client) GetState() SessionState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

// setState sets the session state
func (c *Client) setState(state SessionState) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.state = state
}

// nextTransactionID generates a new transaction ID
func (c *Client) nextTransactionID() uint16 {
	return uint16(c.nextTxID.Add(1))
}

// logf logs a message if logging is enabled
func (c *Client) logf(format string, args ...interface{}) {
	if c.config.EnableLogging {
		log.Printf("[BFCP Client] "+format, args...)
	}
}
