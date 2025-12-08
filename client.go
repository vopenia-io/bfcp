package bfcp

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ClientConfig holds configuration for the BFCP client
type ClientConfig struct {
	ServerAddress  string
	ConferenceID   uint32
	UserID         uint16
	Logger         Logger
	ConnectTimeout time.Duration
	ConnectionRole string
}

// DefaultClientConfig returns a default client configuration
func DefaultClientConfig(serverAddr string, conferenceID uint32, userID uint16) *ClientConfig {
	return &ClientConfig{
		ServerAddress:  serverAddr,
		ConferenceID:   conferenceID,
		UserID:         userID,
		Logger:         NopLogger{},
		ConnectTimeout: 10 * time.Second,
	}
}

// Client represents a BFCP floor control client
type Client struct {
	config    *ClientConfig
	transport *Transport

	state      SessionState
	stateMu    sync.RWMutex
	connected  atomic.Bool
	nextTxID   atomic.Uint32

	activeRequests map[uint16]*ActiveFloorRequest
	requestsMu     sync.RWMutex

	pendingResponses map[uint16]chan *Message
	pendingMu        sync.RWMutex

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
		c.log().Warnw("bfcp.client.already_connected", nil)
		return fmt.Errorf("already connected")
	}

	c.log().Debugw("bfcp.client.connecting", "addr", c.config.ServerAddress)

	transport, err := DialWithLogger(c.config.ServerAddress, c.config.Logger)
	if err != nil {
		c.log().Errorw("bfcp.client.connect_failed", err, "addr", c.config.ServerAddress)
		return fmt.Errorf("failed to connect: %w", err)
	}

	c.log().Debugw("bfcp.client.tcp_connected", "addr", c.config.ServerAddress)

	c.transport = transport
	c.setState(StateConnected)

	transport.OnMessage = c.handleMessage
	transport.OnError = func(err error) {
		c.log().Warnw("bfcp.client.transport_error", err)
		if c.OnError != nil {
			c.OnError(err)
		}
	}
	transport.OnClose = func() {
		c.log().Debugw("bfcp.client.connection_closed")
		c.connected.Store(false)
		c.setState(StateDisconnected)
		if c.OnDisconnected != nil {
			c.OnDisconnected()
		}
	}

	transport.Start()
	c.connected.Store(true)

	c.log().Infow("bfcp.client.ready", "confID", c.config.ConferenceID, "userID", c.config.UserID)

	if c.OnConnected != nil {
		c.OnConnected()
	}

	return nil
}

// Disconnect closes the connection to the BFCP server
func (c *Client) Disconnect() error {
	if !c.connected.Load() {
		return nil
	}

	c.log().Debugw("bfcp.client.disconnecting", "addr", c.config.ServerAddress)

	if err := c.Goodbye(); err != nil {
		c.log().Warnw("bfcp.client.goodbye_failed", err)
	}

	if c.transport != nil {
		return c.transport.Close()
	}

	return nil
}

// Hello sends a Hello message to the server
func (c *Client) Hello() error {
	if !c.connected.Load() {
		return fmt.Errorf("not connected")
	}

	txID := c.nextTransactionID()
	c.log().Debugw("bfcp.client.hello_sending", "txID", txID)
	msg := NewMessage(PrimitiveHello, c.config.ConferenceID, txID, c.config.UserID)

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

	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		c.log().Warnw("bfcp.client.hello_failed", err, "txID", txID)
		return fmt.Errorf("failed to send Hello: %w", err)
	}

	if response.Primitive != PrimitiveHelloAck {
		c.log().Warnw("bfcp.client.hello_unexpected_response", nil, "expected", "HelloAck", "got", response.Primitive.String())
		return fmt.Errorf("expected HelloAck, got %s", response.Primitive)
	}

	c.setState(StateWaitFloorRequest)
	c.log().Infow("bfcp.client.hello_completed")
	return nil
}

// RequestFloor requests control of a floor
func (c *Client) RequestFloor(floorID uint16, beneficiaryID uint16, priority Priority) (uint16, error) {
	if !c.connected.Load() {
		return 0, fmt.Errorf("not connected")
	}

	txID := c.nextTransactionID()
	c.log().Debugw("bfcp.client.floor_requesting", "floorID", floorID, "beneficiaryID", beneficiaryID, "priority", priority, "txID", txID)
	msg := NewMessage(PrimitiveFloorRequest, c.config.ConferenceID, txID, c.config.UserID)

	msg.AddFloorID(floorID)
	if beneficiaryID > 0 {
		msg.AddBeneficiaryID(beneficiaryID)
	}
	if priority > 0 {
		msg.AddPriority(priority)
	}

	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		c.log().Warnw("bfcp.client.floor_request_failed", err, "floorID", floorID, "txID", txID)
		return 0, fmt.Errorf("failed to request floor: %w", err)
	}

	if response.Primitive == PrimitiveError {
		errorCode, _ := response.GetErrorCode()
		errorInfo, _ := response.GetErrorInfo()
		c.log().Warnw("bfcp.client.floor_request_error", nil, "floorID", floorID, "errorCode", errorCode.String(), "errorInfo", errorInfo)
		return 0, fmt.Errorf("floor request failed: %s - %s", errorCode, errorInfo)
	}

	if response.Primitive != PrimitiveFloorRequestStatus {
		c.log().Warnw("bfcp.client.floor_request_unexpected_response", nil, "floorID", floorID, "expected", "FloorRequestStatus", "got", response.Primitive.String())
		return 0, fmt.Errorf("expected FloorRequestStatus, got %s", response.Primitive)
	}

	requestID, ok := response.GetFloorRequestID()
	if !ok {
		c.log().Warnw("bfcp.client.floor_request_missing_id", nil, "floorID", floorID)
		return 0, fmt.Errorf("missing FLOOR-REQUEST-ID in response")
	}

	status, queuePos, ok := response.GetRequestStatus()
	if !ok {
		c.log().Warnw("bfcp.client.floor_request_missing_status", nil, "floorID", floorID, "requestID", requestID)
		return 0, fmt.Errorf("missing REQUEST-STATUS in response")
	}

	c.log().Debugw("bfcp.client.floor_request_status", "floorID", floorID, "requestID", requestID, "status", status.String(), "queuePos", queuePos)

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

	if status == RequestStatusGranted {
		c.setState(StateFloorGranted)
		c.requestsMu.Lock()
		if req, exists := c.activeRequests[floorID]; exists {
			req.GrantedAt = time.Now()
		}
		c.requestsMu.Unlock()

		c.log().Infow("bfcp.client.floor_granted", "floorID", floorID, "requestID", requestID)
		if c.OnFloorGranted != nil {
			c.OnFloorGranted(floorID, requestID)
		}
	} else if status == RequestStatusPending || status == RequestStatusAccepted {
		c.setState(StateFloorRequested)
		c.log().Debugw("bfcp.client.floor_pending", "floorID", floorID, "requestID", requestID, "status", status.String())
	} else {
		c.log().Debugw("bfcp.client.floor_status", "floorID", floorID, "requestID", requestID, "status", status.String())
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
	c.log().Debugw("bfcp.client.floor_releasing", "floorID", floorID, "requestID", activeReq.FloorRequestID, "txID", txID)
	msg := NewMessage(PrimitiveFloorRelease, c.config.ConferenceID, txID, c.config.UserID)

	msg.AddFloorID(floorID)
	msg.AddFloorRequestID(activeReq.FloorRequestID)

	response, err := c.sendAndWait(msg, 5*time.Second)
	if err != nil {
		c.log().Warnw("bfcp.client.floor_release_failed", err, "floorID", floorID)
		return fmt.Errorf("failed to release floor: %w", err)
	}

	if response.Primitive == PrimitiveError {
		errorCode, _ := response.GetErrorCode()
		errorInfo, _ := response.GetErrorInfo()
		c.log().Warnw("bfcp.client.floor_release_error", nil, "floorID", floorID, "errorCode", errorCode.String(), "errorInfo", errorInfo)
		return fmt.Errorf("floor release failed: %s - %s", errorCode, errorInfo)
	}

	c.requestsMu.Lock()
	delete(c.activeRequests, floorID)
	c.requestsMu.Unlock()

	c.setState(StateFloorReleased)
	c.log().Infow("bfcp.client.floor_released", "floorID", floorID)

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
	return c.send(msg)
}

// handleMessage processes incoming messages from the server
func (c *Client) handleMessage(msg *Message) {
	c.log().Debugw("bfcp.client.msg_received", "primitive", msg.Primitive.String(), "txID", msg.TransactionID)

	c.pendingMu.RLock()
	respChan, isPending := c.pendingResponses[msg.TransactionID]
	c.pendingMu.RUnlock()

	if isPending {
		select {
		case respChan <- msg:
			return
		default:
		}
	}

	switch msg.Primitive {
	case PrimitiveFloorRequestStatus:
		c.handleFloorRequestStatus(msg)
	case PrimitiveFloorStatus:
		c.handleFloorStatus(msg)
	case PrimitiveError:
		c.handleError(msg)
	default:
		c.log().Debugw("bfcp.client.unexpected_msg", "primitive", msg.Primitive.String())
	}
}

// handleFloorRequestStatus processes a FloorRequestStatus message
func (c *Client) handleFloorRequestStatus(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		c.log().Warnw("bfcp.client.floor_status_missing_id", nil)
		return
	}

	requestID, _ := msg.GetFloorRequestID()
	status, _, ok := msg.GetRequestStatus()
	if !ok {
		c.log().Warnw("bfcp.client.floor_status_missing_status", nil, "floorID", floorID)
		return
	}

	c.log().Debugw("bfcp.client.floor_status_update", "floorID", floorID, "requestID", requestID, "status", status.String())

	c.requestsMu.Lock()
	if req, exists := c.activeRequests[floorID]; exists {
		req.Status = status
		if status == RequestStatusGranted && req.GrantedAt.IsZero() {
			req.GrantedAt = time.Now()
		}
	}
	c.requestsMu.Unlock()

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
		c.log().Warnw("bfcp.client.floor_status_missing_floor_id", nil)
		return
	}

	status, _, ok := msg.GetRequestStatus()
	if !ok {
		c.log().Warnw("bfcp.client.floor_status_missing_request_status", nil, "floorID", floorID)
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

	c.log().Warnw("bfcp.client.error_received", nil, "errorCode", errorCode.String(), "errorInfo", errorInfo)

	if c.OnError != nil {
		c.OnError(fmt.Errorf("BFCP error: %s - %s", errorCode, errorInfo))
	}
}

// sendAndWait sends a message and waits for a response
func (c *Client) sendAndWait(msg *Message, timeout time.Duration) (*Message, error) {
	respChan := make(chan *Message, 1)

	c.pendingMu.Lock()
	c.pendingResponses[msg.TransactionID] = respChan
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pendingResponses, msg.TransactionID)
		c.pendingMu.Unlock()
		close(respChan)
	}()

	if err := c.send(msg); err != nil {
		return nil, err
	}

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

	c.log().Debugw("bfcp.client.msg_sent", "primitive", msg.Primitive.String(), "txID", msg.TransactionID)
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

func (c *Client) log() Logger {
	if c.config.Logger != nil {
		return c.config.Logger
	}
	return NopLogger{}
}
