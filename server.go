package bfcp

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
)

// ServerConfig holds configuration for the BFCP server
type ServerConfig struct {
	Address          string
	ConferenceID     uint32
	AutoGrant        bool // Automatically grant floor requests
	MaxFloors        int
	SessionTimeout   int // Session timeout in seconds (0 = no timeout)
	EnableLogging    bool
}

// DefaultServerConfig returns a default server configuration
func DefaultServerConfig(address string, conferenceID uint32) *ServerConfig {
	return &ServerConfig{
		Address:        address,
		ConferenceID:   conferenceID,
		AutoGrant:      true, // Auto-grant by default for simple conferencing
		MaxFloors:      10,
		SessionTimeout: 300, // 5 minutes
		EnableLogging:  true,
	}
}

// Server represents a BFCP floor control server
type Server struct {
	config   *ServerConfig
	listener *Listener

	// State management
	floors    map[uint16]*FloorStateMachine // FloorID -> FloorStateMachine
	sessions  map[string]*Session           // Remote address -> Session
	floorsLock sync.RWMutex
	sessionsLock sync.RWMutex

	// Transaction ID counter
	nextTxID atomic.Uint32

	// Request queue
	queue *FloorRequestQueue

	// Event callbacks
	OnFloorRequest  func(floorID, userID, requestID uint16) bool // Return true to grant, false to deny
	OnFloorGranted  func(floorID, userID, requestID uint16)
	OnFloorReleased func(floorID, userID uint16)
	OnFloorDenied   func(floorID, userID, requestID uint16)
	OnClientConnect func(remoteAddr string, userID uint16)
	OnClientDisconnect func(remoteAddr string, userID uint16)
	OnError         func(error)
}

// Session represents a client session on the server
type Session struct {
	Transport    *Transport
	StateMachine *SessionStateMachine
	Server       *Server
}

// NewServer creates a new BFCP floor control server
func NewServer(config *ServerConfig) *Server {
	if config == nil {
		config = DefaultServerConfig(":5070", 1)
	}

	return &Server{
		config:   config,
		floors:   make(map[uint16]*FloorStateMachine),
		sessions: make(map[string]*Session),
		queue:    NewFloorRequestQueue(),
	}
}

// AddFloor adds a floor to the server
func (s *Server) AddFloor(floorID uint16) {
	s.floorsLock.Lock()
	defer s.floorsLock.Unlock()

	if _, exists := s.floors[floorID]; !exists {
		s.floors[floorID] = NewFloorStateMachine(floorID, s.config.ConferenceID)
		s.logf("Added floor %d to conference %d", floorID, s.config.ConferenceID)
	}
}

// RemoveFloor removes a floor from the server
func (s *Server) RemoveFloor(floorID uint16) {
	s.floorsLock.Lock()
	defer s.floorsLock.Unlock()

	delete(s.floors, floorID)
	s.logf("Removed floor %d from conference %d", floorID, s.config.ConferenceID)
}

// GetFloor returns a floor state machine
func (s *Server) GetFloor(floorID uint16) (*FloorStateMachine, bool) {
	s.floorsLock.RLock()
	defer s.floorsLock.RUnlock()

	fsm, exists := s.floors[floorID]
	return fsm, exists
}

// ListenAndServe starts the BFCP server
func (s *Server) ListenAndServe() error {
	listener, err := Listen(s.config.Address)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	s.listener = listener
	s.logf("BFCP server listening on %s (Conference ID: %d)", s.config.Address, s.config.ConferenceID)

	listener.OnConnection = s.handleConnection
	listener.OnError = func(err error) {
		if s.OnError != nil {
			s.OnError(err)
		} else {
			s.logf("Listener error: %v", err)
		}
	}

	listener.Start()

	// Block until listener is closed
	select {}
}

// Close closes the server and all connections
func (s *Server) Close() error {
	s.logf("Closing BFCP server")

	// Close all sessions
	s.sessionsLock.Lock()
	for _, session := range s.sessions {
		session.Transport.Close()
	}
	s.sessions = make(map[string]*Session)
	s.sessionsLock.Unlock()

	// Close listener
	if s.listener != nil {
		return s.listener.Close()
	}

	return nil
}

// handleConnection handles a new client connection
func (s *Server) handleConnection(transport *Transport) {
	remoteAddr := transport.RemoteAddr().String()
	s.logf("New connection from %s", remoteAddr)

	session := &Session{
		Transport:    transport,
		StateMachine: NewSessionStateMachine(uint16(s.config.ConferenceID), 0, remoteAddr),
		Server:       s,
	}

	// Register session
	s.sessionsLock.Lock()
	s.sessions[remoteAddr] = session
	s.sessionsLock.Unlock()

	// Set up transport callbacks
	transport.OnMessage = func(msg *Message) {
		session.handleMessage(msg)
	}

	transport.OnError = func(err error) {
		s.logf("Transport error for %s: %v", remoteAddr, err)
	}

	transport.OnClose = func() {
		s.logf("Connection closed: %s", remoteAddr)
		s.sessionsLock.Lock()
		delete(s.sessions, remoteAddr)
		s.sessionsLock.Unlock()

		if s.OnClientDisconnect != nil {
			s.OnClientDisconnect(remoteAddr, session.StateMachine.UserID)
		}
	}

	// Start reading messages
	transport.Start()
}

// handleMessage processes incoming BFCP messages
func (sess *Session) handleMessage(msg *Message) {
	sess.StateMachine.UpdateActivity()
	sess.Server.logf("Received %s from %s (TxID: %d)", msg.Primitive, sess.Transport.RemoteAddr(), msg.TransactionID)

	switch msg.Primitive {
	case PrimitiveHello:
		sess.handleHello(msg)
	case PrimitiveFloorRequest:
		sess.handleFloorRequest(msg)
	case PrimitiveFloorRelease:
		sess.handleFloorRelease(msg)
	case PrimitiveFloorQuery:
		sess.handleFloorQuery(msg)
	case PrimitiveGoodbye:
		sess.handleGoodbye(msg)
	default:
		sess.sendError(msg, ErrorUnknownPrimitive, fmt.Sprintf("Unknown primitive: %d", msg.Primitive))
	}
}

// handleHello processes a Hello message
func (sess *Session) handleHello(msg *Message) {
	// Update session UserID from the message
	sess.StateMachine.UserID = msg.UserID

	// Extract supported primitives and attributes if present
	if attr := msg.GetAttribute(AttrSupportedPrimitives); attr != nil {
		primitives := make([]Primitive, len(attr.Value))
		for i, v := range attr.Value {
			primitives[i] = Primitive(v)
		}
		sess.StateMachine.SetSupportedPrimitives(primitives)
	}

	if attr := msg.GetAttribute(AttrSupportedAttributes); attr != nil {
		attributes := make([]AttributeType, len(attr.Value))
		for i, v := range attr.Value {
			attributes[i] = AttributeType(v)
		}
		sess.StateMachine.SetSupportedAttributes(attributes)
	}

	// Send HelloAck
	response := NewMessage(PrimitiveHelloAck, msg.ConferenceID, msg.TransactionID, msg.UserID)

	// Add our supported primitives
	supportedPrimitives := []Primitive{
		PrimitiveFloorRequest,
		PrimitiveFloorRelease,
		PrimitiveFloorRequestStatus,
		PrimitiveFloorStatus,
		PrimitiveHello,
		PrimitiveHelloAck,
		PrimitiveError,
		PrimitiveGoodbye,
		PrimitiveGoodbyeAck,
	}
	response.AddSupportedPrimitives(supportedPrimitives)

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
	response.AddSupportedAttributes(supportedAttributes)

	sess.send(response)
	sess.StateMachine.SetState(StateWaitFloorRequest)

	if sess.Server.OnClientConnect != nil {
		sess.Server.OnClientConnect(sess.Transport.RemoteAddr().String(), msg.UserID)
	}
}

// handleFloorRequest processes a FloorRequest message
func (sess *Session) handleFloorRequest(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.sendError(msg, ErrorUnknownMandatoryAttribute, "Missing FLOOR-ID attribute")
		return
	}

	// Get or create floor
	floor, exists := sess.Server.GetFloor(floorID)
	if !exists {
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Floor %d does not exist", floorID))
		return
	}

	// Generate a floor request ID
	requestID := uint16(sess.Server.nextTxID.Add(1))

	// Get beneficiary ID (defaults to requesting user)
	beneficiaryID, _ := msg.GetBeneficiaryID()
	if beneficiaryID == 0 {
		beneficiaryID = msg.UserID
	}

	// Get priority (defaults to normal)
	priority := PriorityNormal
	if attr := msg.GetAttribute(AttrPriority); attr != nil && len(attr.Value) >= 2 {
		priority = Priority(uint16(attr.Value[0])<<8 | uint16(attr.Value[1]))
	}

	// Check if we should grant
	shouldGrant := sess.Server.config.AutoGrant
	if sess.Server.OnFloorRequest != nil {
		shouldGrant = sess.Server.OnFloorRequest(floorID, msg.UserID, requestID)
	}

	// Request the floor
	status, err := floor.Request(msg.UserID, requestID, priority)
	if err != nil {
		sess.sendError(msg, ErrorUnauthorizedOperation, err.Error())
		if sess.Server.OnFloorDenied != nil {
			sess.Server.OnFloorDenied(floorID, msg.UserID, requestID)
		}
		return
	}

	// Send FloorRequestStatus with Pending
	sess.sendFloorStatus(msg, floorID, requestID, status, 0)

	// If auto-grant is enabled, grant immediately
	if shouldGrant && status == RequestStatusPending {
		if err := floor.Grant(); err == nil {
			// Send FloorRequestStatus with Granted
			sess.sendFloorStatus(msg, floorID, requestID, RequestStatusGranted, 0)
			sess.StateMachine.SetState(StateFloorGranted)

			if sess.Server.OnFloorGranted != nil {
				sess.Server.OnFloorGranted(floorID, msg.UserID, requestID)
			}
		}
	} else if !shouldGrant && status == RequestStatusPending {
		// Deny the request
		if err := floor.Deny(); err == nil {
			// Send FloorRequestStatus with Denied
			sess.sendFloorStatus(msg, floorID, requestID, RequestStatusDenied, 0)
			sess.StateMachine.SetState(StateFloorDenied)

			if sess.Server.OnFloorDenied != nil {
				sess.Server.OnFloorDenied(floorID, msg.UserID, requestID)
			}
		}
	} else {
		sess.StateMachine.SetState(StateFloorRequested)
	}
}

// handleFloorRelease processes a FloorRelease message
func (sess *Session) handleFloorRelease(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.sendError(msg, ErrorUnknownMandatoryAttribute, "Missing FLOOR-ID attribute")
		return
	}

	floor, exists := sess.Server.GetFloor(floorID)
	if !exists {
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Floor %d does not exist", floorID))
		return
	}

	requestID, _ := msg.GetFloorRequestID()

	// Release the floor
	if err := floor.Release(msg.UserID); err != nil {
		sess.sendError(msg, ErrorFloorReleaseDenied, err.Error())
		return
	}

	// Send FloorRequestStatus with Released
	sess.sendFloorStatus(msg, floorID, requestID, RequestStatusReleased, 0)
	sess.StateMachine.SetState(StateFloorReleased)

	if sess.Server.OnFloorReleased != nil {
		sess.Server.OnFloorReleased(floorID, msg.UserID)
	}
}

// handleFloorQuery processes a FloorQuery message
func (sess *Session) handleFloorQuery(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.sendError(msg, ErrorUnknownMandatoryAttribute, "Missing FLOOR-ID attribute")
		return
	}

	floor, exists := sess.Server.GetFloor(floorID)
	if !exists {
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Floor %d does not exist", floorID))
		return
	}

	// Send FloorStatus
	response := NewMessage(PrimitiveFloorStatus, msg.ConferenceID, msg.TransactionID, msg.UserID)
	response.AddFloorID(floorID)
	response.AddFloorRequestID(floor.GetFloorRequestID())
	response.AddRequestStatus(floor.GetState(), 0)

	sess.send(response)
}

// handleGoodbye processes a Goodbye message
func (sess *Session) handleGoodbye(msg *Message) {
	// Send GoodbyeAck
	response := NewMessage(PrimitiveGoodbyeAck, msg.ConferenceID, msg.TransactionID, msg.UserID)
	sess.send(response)

	// Close the connection
	sess.Transport.Close()
}

// sendFloorStatus sends a FloorRequestStatus message
func (sess *Session) sendFloorStatus(req *Message, floorID, requestID uint16, status RequestStatus, queuePos uint8) {
	response := NewMessage(PrimitiveFloorRequestStatus, req.ConferenceID, req.TransactionID, req.UserID)
	response.AddFloorID(floorID)
	if requestID > 0 {
		response.AddFloorRequestID(requestID)
	}
	response.AddRequestStatus(status, queuePos)
	sess.send(response)
}

// sendError sends an Error message
func (sess *Session) sendError(req *Message, errorCode ErrorCode, errorInfo string) {
	response := NewMessage(PrimitiveError, req.ConferenceID, req.TransactionID, req.UserID)
	response.AddErrorCode(errorCode)
	if errorInfo != "" {
		response.AddErrorInfo(errorInfo)
	}
	sess.send(response)
	sess.Server.logf("Sent error to %s: %s - %s", sess.Transport.RemoteAddr(), errorCode, errorInfo)
}

// send sends a message to the client
func (sess *Session) send(msg *Message) {
	if err := sess.Transport.SendMessage(msg); err != nil {
		sess.Server.logf("Failed to send %s to %s: %v", msg.Primitive, sess.Transport.RemoteAddr(), err)
	} else {
		sess.Server.logf("Sent %s to %s (TxID: %d)", msg.Primitive, sess.Transport.RemoteAddr(), msg.TransactionID)
	}
}

// CanGrant checks if a floor can be granted
func (s *Server) CanGrant(floorID uint16) bool {
	floor, exists := s.GetFloor(floorID)
	if !exists {
		return false
	}
	return floor.IsAvailable()
}

// Grant grants a floor to a user
func (s *Server) Grant(floorID, userID uint16) error {
	floor, exists := s.GetFloor(floorID)
	if !exists {
		return fmt.Errorf("floor %d does not exist", floorID)
	}
	return floor.Grant()
}

// Deny denies a floor request
func (s *Server) Deny(floorID, userID uint16) error {
	floor, exists := s.GetFloor(floorID)
	if !exists {
		return fmt.Errorf("floor %d does not exist", floorID)
	}
	return floor.Deny()
}

// logf logs a message if logging is enabled
func (s *Server) logf(format string, args ...interface{}) {
	if s.config.EnableLogging {
		log.Printf("[BFCP Server] "+format, args...)
	}
}
