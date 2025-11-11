package bfcp

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
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

	// Multi-conference support
	conferenceManager *ConferenceManager

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

	s := &Server{
		config: config,
		queue:  NewFloorRequestQueue(),
	}
	s.conferenceManager = NewConferenceManager(s)
	return s
}

// GetConferenceManager returns the conference manager for this server
func (s *Server) GetConferenceManager() *ConferenceManager {
	return s.conferenceManager
}

// AddFloor adds a floor to the default conference (for backward compatibility)
// Deprecated: Use ConferenceManager.AllocateFloor() instead
func (s *Server) AddFloor(floorID uint16) {
	// Use default conference ID from config
	conf, exists := s.conferenceManager.GetConference(s.config.ConferenceID)
	if !exists {
		// Create default conference if it doesn't exist
		conf, _ = s.conferenceManager.CreateConference(s.config.ConferenceID, 0)
	}
	if conf != nil {
		conf.AddFloor(floorID)
		s.logf("Added floor %d to conference %d", floorID, s.config.ConferenceID)
	}
}

// RemoveFloor removes a floor from the default conference (for backward compatibility)
// Deprecated: Use Conference.RemoveFloor() instead
func (s *Server) RemoveFloor(floorID uint16) {
	conf, exists := s.conferenceManager.GetConference(s.config.ConferenceID)
	if exists {
		conf.RemoveFloor(floorID)
		s.logf("Removed floor %d from conference %d", floorID, s.config.ConferenceID)
	}
}

// GetFloor returns a floor state machine from the default conference (for backward compatibility)
// Deprecated: Use Conference.GetFloor() instead
func (s *Server) GetFloor(floorID uint16) (*FloorStateMachine, bool) {
	conf, exists := s.conferenceManager.GetConference(s.config.ConferenceID)
	if !exists {
		return nil, false
	}
	return conf.GetFloor(floorID)
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

	// Close all conferences and their sessions
	s.conferenceManager.mu.RLock()
	conferences := make([]*Conference, 0, len(s.conferenceManager.conferences))
	for _, conf := range s.conferenceManager.conferences {
		conferences = append(conferences, conf)
	}
	s.conferenceManager.mu.RUnlock()

	for _, conf := range conferences {
		conf.mu.Lock()
		for _, session := range conf.sessions {
			session.Transport.Close()
		}
		conf.mu.Unlock()
	}

	// Close listener
	if s.listener != nil {
		return s.listener.Close()
	}

	return nil
}

// Addr returns the listener's network address
// Returns nil if listener is not started
func (s *Server) Addr() net.Addr {
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

// handleConnection handles a new client connection
func (s *Server) handleConnection(transport *Transport) {
	remoteAddr := transport.RemoteAddr().String()
	s.logf("🔌 [Connection] New TCP connection from %s", remoteAddr)

	// Create session with default conference ID (will be updated in Hello)
	session := &Session{
		Transport:    transport,
		StateMachine: NewSessionStateMachine(uint16(s.config.ConferenceID), 0, remoteAddr),
		Server:       s,
	}

	// Set up transport callbacks
	transport.OnMessage = func(msg *Message) {
		session.handleMessage(msg)
	}

	transport.OnError = func(err error) {
		s.logf("❌ [Connection] Transport error for %s: %v", remoteAddr, err)
	}

	transport.OnClose = func() {
		s.logf("🔌 [Connection] Connection closed: %s", remoteAddr)

		// Remove session from its conference
		conferenceID := uint32(session.StateMachine.ConferenceID)
		if conf, exists := s.conferenceManager.GetConference(conferenceID); exists {
			conf.RemoveSession(remoteAddr)
			s.logf("📊 [Connection] Session removed from conference %d", conferenceID)
		}

		if s.OnClientDisconnect != nil {
			s.OnClientDisconnect(remoteAddr, session.StateMachine.UserID)
		}

		// Notify conference manager
		s.conferenceManager.OnClientDisconnect(conferenceID, remoteAddr, session.StateMachine.UserID)
	}

	// Start reading messages
	s.logf("🚀 [Connection] Starting message reader for %s", remoteAddr)
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
	// Client acknowledgment messages - silently ignore (server doesn't need to process these)
	case PrimitiveFloorRequestStatus:
		sess.Server.logf("📥 [FloorRequestStatus] Received from client (likely acknowledgment) - ignoring")
	case PrimitiveFloorRequestStatusAck:
		sess.Server.logf("📥 [FloorRequestStatusAck] Received from client - ignoring")
	case PrimitiveFloorStatus:
		sess.Server.logf("📥 [FloorStatus] Received from client - ignoring")
	case PrimitiveFloorStatusAck:
		sess.Server.logf("📥 [FloorStatusAck] Received from client - ignoring")
	case PrimitiveError:
		// Client is reporting an error to us - log it but don't respond with another error
		if errorCode, ok := msg.GetErrorCode(); ok {
			errorInfo, _ := msg.GetErrorInfo()
			sess.Server.logf("📥 [Error] Received from client - ErrorCode=%s(%d), ErrorInfo=%q", errorCode, errorCode, errorInfo)
		} else {
			// Log detailed debug info to understand why we can't find the error code
			sess.Server.logf("📥 [Error] Received from client - client encountered error (no error code)")
			sess.Server.logf("🔍 [Error Debug] Message details: version=%d, primitive=%s, confID=%d, txID=%d, userID=%d, numAttrs=%d",
				msg.Version, msg.Primitive, msg.ConferenceID, msg.TransactionID, msg.UserID, len(msg.Attributes))

			// Log all attributes present in the message
			for i, attr := range msg.Attributes {
				sess.Server.logf("🔍 [Error Debug] Attribute[%d]: type=%s(%d), length=%d, value=%X",
					i, attr.Type, attr.Type, attr.Length, attr.Value)
			}

			// Look for ErrorInfo even if ErrorCode is missing
			if errorInfo, ok := msg.GetErrorInfo(); ok {
				sess.Server.logf("🔍 [Error Debug] ErrorInfo text: %q", errorInfo)
			}
		}
	case PrimitiveHelloAck:
		sess.Server.logf("📥 [HelloAck] Received from client - ignoring")
	case PrimitiveGoodbyeAck:
		sess.Server.logf("📥 [GoodbyeAck] Received from client - ignoring")
	default:
		sess.sendError(msg, ErrorUnknownPrimitive, fmt.Sprintf("Unknown primitive: %d", msg.Primitive))
	}
}

// handleHello processes a Hello message
func (sess *Session) handleHello(msg *Message) {
	sess.Server.logf("👋 [Hello] Processing Hello from user %d (confID=%d)", msg.UserID, msg.ConferenceID)

	// Update session with conferenceID and UserID from the message
	sess.StateMachine.ConferenceID = msg.ConferenceID
	sess.StateMachine.UserID = msg.UserID
	sess.Server.logf("👤 [Hello] Session set to conference %d, UserID %d", msg.ConferenceID, msg.UserID)

	// Get or create the conference
	conf, exists := sess.Server.conferenceManager.GetConference(msg.ConferenceID)
	if !exists {
		sess.Server.logf("🆕 [Hello] Creating new conference %d", msg.ConferenceID)
		conf, _ = sess.Server.conferenceManager.CreateConference(msg.ConferenceID, msg.UserID)
	}

	// Register session with the conference
	if conf != nil {
		remoteAddr := sess.Transport.RemoteAddr().String()
		conf.AddSession(remoteAddr, sess)
		sess.Server.logf("✅ [Hello] Session registered with conference %d", msg.ConferenceID)
	}

	// Extract supported primitives and attributes if present
	if attr := msg.GetAttribute(AttrSupportedPrimitives); attr != nil {
		primitives := make([]Primitive, len(attr.Value))
		for i, v := range attr.Value {
			primitives[i] = Primitive(v)
		}
		sess.StateMachine.SetSupportedPrimitives(primitives)
		sess.Server.logf("📋 [Hello] Client supports %d primitives", len(primitives))
	}

	if attr := msg.GetAttribute(AttrSupportedAttributes); attr != nil {
		attributes := make([]AttributeType, len(attr.Value))
		for i, v := range attr.Value {
			attributes[i] = AttributeType(v)
		}
		sess.StateMachine.SetSupportedAttributes(attributes)
		sess.Server.logf("📋 [Hello] Client supports %d attributes", len(attributes))
	}

	// Send HelloAck
	sess.Server.logf("📤 [Hello] Sending HelloAck to user %d", msg.UserID)
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

	// Add supported attributes (Phase 5.15: Include grouped attributes)
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
		AttrUserDisplayName,
		AttrUserURI,
		AttrBeneficiaryInfo,
		AttrFloorRequestInfo,        // Grouped attribute (type=15)
		AttrRequestedByInfo,          // Grouped attribute (type=16)
		AttrFloorRequestStatus,       // Grouped attribute (type=17)
		AttrOverallRequestStatus,     // Grouped attribute (type=18)
		AttrParticipantProvidedInfo,
		AttrStatusInfo,
	}
	response.AddSupportedAttributes(supportedAttributes)

	sess.send(response)
	sess.Server.logf("✅ [Hello] HelloAck sent successfully")

	sess.StateMachine.SetState(StateWaitFloorRequest)
	sess.Server.logf("📊 [Hello] State changed to WaitFloorRequest")

	// Enable keepalive to prevent connection timeout (30 second interval)
	sess.Transport.EnableKeepalive(30*time.Second, func() error {
		keepaliveMsg := NewMessage(PrimitiveHello, msg.ConferenceID, uint16(sess.Server.nextTxID.Add(1)), msg.UserID)
		return sess.Transport.SendKeepaliveMessage(keepaliveMsg)
	})
	sess.Server.logf("💓 [Hello] Keepalive enabled (30s interval)")

	// Start the keepalive goroutine
	sess.Transport.StartKeepalive()
	sess.Server.logf("💓 [Hello] Keepalive goroutine started")

	if sess.Server.OnClientConnect != nil {
		sess.Server.logf("🔔 [Hello] Calling OnClientConnect callback")
		sess.Server.OnClientConnect(sess.Transport.RemoteAddr().String(), msg.UserID)
	}

	// Notify conference manager about client connection
	sess.Server.conferenceManager.OnClientConnect(msg.ConferenceID, sess.Transport.RemoteAddr().String(), msg.UserID)

	sess.Server.logf("✅ [Hello] Hello handshake completed - ready for floor requests")
}

// handleFloorRequest processes a FloorRequest message
func (sess *Session) handleFloorRequest(msg *Message) {
	sess.Server.logf("📋 [FloorRequest] Processing request from user %d (confID=%d)", msg.UserID, msg.ConferenceID)

	// Get floor ID from message
	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.Server.logf("❌ [FloorRequest] No floor ID in request")
		sess.sendError(msg, ErrorInvalidFloorID, "No floor ID in request")
		return
	}
	sess.Server.logf("📋 [FloorRequest] Requesting floor ID: %d from conference %d", floorID, msg.ConferenceID)

	// Get the floor from the correct conference (use msg.ConferenceID, not default)
	conf, confExists := sess.Server.conferenceManager.GetConference(msg.ConferenceID)
	if !confExists {
		sess.Server.logf("❌ [FloorRequest] Conference %d does not exist", msg.ConferenceID)
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Conference %d does not exist", msg.ConferenceID))
		return
	}

	floor, exists := conf.GetFloor(floorID)
	if !exists {
		sess.Server.logf("❌ [FloorRequest] Floor %d does not exist in conference %d", floorID, msg.ConferenceID)
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Floor %d does not exist", floorID))
		return
	} else {
		sess.Server.logf("✅ [FloorRequest] Floor %d exists in conference %d", floorID, msg.ConferenceID)
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
	sess.Server.logf("🤔 [FloorRequest] Initial grant decision - AutoGrant=%v", shouldGrant)

	if sess.Server.OnFloorRequest != nil {
		shouldGrant = sess.Server.OnFloorRequest(floorID, msg.UserID, requestID)
		sess.Server.logf("🤔 [FloorRequest] Callback decision - ShouldGrant=%v (floorID=%d, userID=%d, requestID=%d)",
			shouldGrant, floorID, msg.UserID, requestID)
	}

	// Request the floor
	status, err := floor.Request(msg.UserID, requestID, priority)
	if err != nil {
		sess.Server.logf("❌ [FloorRequest] Floor request failed: %v", err)
		sess.sendError(msg, ErrorUnauthorizedOperation, err.Error())
		if sess.Server.OnFloorDenied != nil {
			sess.Server.OnFloorDenied(floorID, msg.UserID, requestID)
		}
		return
	}

	sess.Server.logf("📊 [FloorRequest] Floor state after request: %v", status)

	// Send FloorRequestStatus with Pending
	sess.Server.logf("📤 [FloorRequest] Sending FloorRequestStatus with status=%v", status)
	sess.sendFloorStatus(msg, floorID, requestID, status, 0)

	// If auto-grant is enabled, grant immediately
	if shouldGrant && status == RequestStatusPending {
		sess.Server.logf("✅ [FloorRequest] Auto-granting floor %d to user %d (requestID=%d)", floorID, msg.UserID, requestID)

		// Simulate human grant delay (100-200ms) to avoid Polycom UI misinterpreting
		// a Pending/Granted burst as duplicate ACKs
		time.Sleep(150 * time.Millisecond)
		sess.Server.logf("⏱️  [FloorRequest] Human grant delay completed (150ms)")

		if err := floor.Grant(); err == nil {
			// Send FloorRequestStatus with Granted
			sess.Server.logf("📤 [FloorRequest] Sending FloorRequestStatus with Granted")
			sess.sendFloorStatus(msg, floorID, requestID, RequestStatusGranted, 0)
			sess.StateMachine.SetState(StateFloorGranted)

			if sess.Server.OnFloorGranted != nil {
				sess.Server.OnFloorGranted(floorID, msg.UserID, requestID)
			}
		} else {
			sess.Server.logf("❌ [FloorRequest] Failed to grant floor: %v", err)
		}
	} else if !shouldGrant && status == RequestStatusPending {
		// Deny the request
		sess.Server.logf("❌ [FloorRequest] Denying floor %d for user %d (requestID=%d)", floorID, msg.UserID, requestID)
		if err := floor.Deny(); err == nil {
			// Send FloorRequestStatus with Denied
			sess.Server.logf("📤 [FloorRequest] Sending FloorRequestStatus with Denied")
			sess.sendFloorStatus(msg, floorID, requestID, RequestStatusDenied, 0)
			sess.StateMachine.SetState(StateFloorDenied)

			if sess.Server.OnFloorDenied != nil {
				sess.Server.OnFloorDenied(floorID, msg.UserID, requestID)
			}
		} else {
			sess.Server.logf("❌ [FloorRequest] Failed to deny floor: %v", err)
		}
	} else {
		sess.Server.logf("⏳ [FloorRequest] Floor request pending (status=%v, shouldGrant=%v)", status, shouldGrant)
		sess.StateMachine.SetState(StateFloorRequested)
	}
}

// handleFloorRelease processes a FloorRelease message
func (sess *Session) handleFloorRelease(msg *Message) {
	sess.Server.logf("🔓 [FloorRelease] Processing release from user %d (confID=%d)", msg.UserID, msg.ConferenceID)

	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.Server.logf("❌ [FloorRelease] Missing FLOOR-ID attribute")
		sess.sendError(msg, ErrorUnknownMandatoryAttribute, "Missing FLOOR-ID attribute")
		return
	}

	// Get the floor from the correct conference
	conf, confExists := sess.Server.conferenceManager.GetConference(msg.ConferenceID)
	if !confExists {
		sess.Server.logf("❌ [FloorRelease] Conference %d does not exist", msg.ConferenceID)
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Conference %d does not exist", msg.ConferenceID))
		return
	}

	floor, exists := conf.GetFloor(floorID)
	if !exists {
		sess.Server.logf("❌ [FloorRelease] Floor %d does not exist in conference %d", floorID, msg.ConferenceID)
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Floor %d does not exist", floorID))
		return
	}

	requestID, _ := msg.GetFloorRequestID()
	sess.Server.logf("🔓 [FloorRelease] Releasing floor %d (requestID=%d) from user %d", floorID, requestID, msg.UserID)

	// Release the floor
	if err := floor.Release(msg.UserID); err != nil {
		sess.Server.logf("❌ [FloorRelease] Release denied: %v", err)
		sess.sendError(msg, ErrorFloorReleaseDenied, err.Error())
		return
	}

	sess.Server.logf("✅ [FloorRelease] Floor %d released successfully", floorID)

	// Send direct response to the requesting client with original transaction ID
	sess.sendFloorStatus(msg, floorID, requestID, RequestStatusReleased, 0)
	sess.StateMachine.SetState(StateFloorReleased)

	if sess.Server.OnFloorReleased != nil {
		sess.Server.logf("🔔 [FloorRelease] Calling OnFloorReleased callback")
		sess.Server.OnFloorReleased(floorID, msg.UserID)
	}

	// Broadcast to other sessions (observers) in the conference
	// Note: The requesting client already received the response above
	sess.Server.logf("📢 [FloorRelease] Broadcasting floor release to other participants")
	sess.Server.broadcastFloorStatus(msg.ConferenceID, msg.UserID, floorID, requestID, RequestStatusReleased, 0)

	sess.Server.logf("✅ [FloorRelease] Floor release completed and broadcast to all participants")
}

// handleFloorQuery processes a FloorQuery message
func (sess *Session) handleFloorQuery(msg *Message) {
	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.sendError(msg, ErrorUnknownMandatoryAttribute, "Missing FLOOR-ID attribute")
		return
	}

	// Get the floor from the correct conference
	conf, confExists := sess.Server.conferenceManager.GetConference(msg.ConferenceID)
	if !confExists {
		sess.sendError(msg, ErrorInvalidFloorID, fmt.Sprintf("Conference %d does not exist", msg.ConferenceID))
		return
	}

	floor, exists := conf.GetFloor(floorID)
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
	sess.Server.logf("👋 [Goodbye] Processing Goodbye from user %d", msg.UserID)

	// Get the conference for this session
	conf, exists := sess.Server.conferenceManager.GetConference(msg.ConferenceID)
	if !exists {
		sess.Server.logf("⚠️ [Goodbye] Conference %d not found", msg.ConferenceID)
		return
	}

	// Release all floors owned by this user in this conference
	conf.mu.RLock()
	for floorID, floor := range conf.floors {
		if floor.GetOwner() == msg.UserID {
			sess.Server.logf("🔓 [Goodbye] Releasing floor %d owned by disconnecting user %d", floorID, msg.UserID)
			if err := floor.Release(msg.UserID); err != nil {
				sess.Server.logf("⚠️ [Goodbye] Failed to release floor %d: %v", floorID, err)
			} else {
				// Broadcast floor release to remaining sessions in this conference
				requestID := floor.GetFloorRequestID()
				sess.Server.broadcastFloorStatus(msg.ConferenceID, msg.UserID, floorID, requestID, RequestStatusReleased, 0)

				if sess.Server.OnFloorReleased != nil {
					sess.Server.OnFloorReleased(floorID, msg.UserID)
				}
			}
		}
	}
	conf.mu.RUnlock()

	// Send GoodbyeAck
	sess.Server.logf("📤 [Goodbye] Sending GoodbyeAck to user %d", msg.UserID)
	response := NewMessage(PrimitiveGoodbyeAck, msg.ConferenceID, msg.TransactionID, msg.UserID)
	sess.send(response)

	sess.Server.logf("✅ [Goodbye] Goodbye handshake completed, closing connection")

	// Update state
	sess.StateMachine.SetState(StateDisconnected)

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

// InjectFloorRequest simulates a FloorRequest from a virtual client (e.g., WebRTC screen share)
// This maintains protocol correctness by following the standard request-grant flow
// Phase 5.17: Implement proper BFCP flow instead of proactive grants
func (s *Server) InjectFloorRequest(floorID, sharerUserID uint16) (requestID uint16, err error) {
	s.logf("📥 [InjectFloorRequest] Simulating FloorRequest from virtual sharer userID=%d for floor %d", sharerUserID, floorID)

	// Get or create floor
	floor, exists := s.GetFloor(floorID)
	if !exists {
		s.logf("🆕 [InjectFloorRequest] Floor %d doesn't exist, creating it", floorID)
		s.AddFloor(floorID)
		floor, exists = s.GetFloor(floorID)
		if !exists {
			return 0, fmt.Errorf("failed to create floor %d", floorID)
		}
	}

	// Generate request ID
	requestID = uint16(s.nextTxID.Add(1))
	s.logf("📋 [InjectFloorRequest] Generated requestID=%d for floor %d", requestID, floorID)

	// Request the floor
	status, err := floor.Request(sharerUserID, requestID, PriorityNormal)
	if err != nil {
		s.logf("❌ [InjectFloorRequest] Floor request failed: %v", err)
		return 0, fmt.Errorf("floor request failed: %w", err)
	}

	s.logf("📊 [InjectFloorRequest] Floor state after request: %v", status)

	// Notify all BFCP clients (controllers like Polycom) about the pending request
	// Use default conference ID from config
	conf, exists := s.conferenceManager.GetConference(s.config.ConferenceID)
	if !exists {
		s.logf("⚠️ [InjectFloorRequest] Conference %d not found, creating it", s.config.ConferenceID)
		conf, _ = s.conferenceManager.CreateConference(s.config.ConferenceID, 0)
	}

	conf.mu.RLock()
	controllers := make([]*Session, 0, len(conf.sessions))
	for _, session := range conf.sessions {
		controllers = append(controllers, session)
	}
	conf.mu.RUnlock()

	// Send FloorRequestStatus(Pending) to all controllers
	for _, session := range controllers {
		// Use unique txID for each server-initiated notification
		txID := uint16(s.nextTxID.Add(1))
		msg := NewMessage(PrimitiveFloorRequestStatus, s.config.ConferenceID, txID, sharerUserID)
		msg.Version = ProtocolVersionRFC4582
		msg.AddFloorID(floorID)
		msg.AddFloorRequestID(requestID)

		// Build grouped Floor-Request-Info with Pending status
		var floorRequestInfoSubs []Attribute
		beneficiaryValue := make([]byte, 2)
		binary.BigEndian.PutUint16(beneficiaryValue, sharerUserID)
		floorRequestInfoSubs = append(floorRequestInfoSubs, Attribute{
			Type:   AttrBeneficiaryID,
			Length: 2,
			Value:  beneficiaryValue,
		})

		requestStatusValue := make([]byte, 2)
		requestStatusValue[0] = uint8(RequestStatusPending)
		requestStatusValue[1] = 0 // Queue position
		floorRequestInfoSubs = append(floorRequestInfoSubs, Attribute{
			Type:   AttrRequestStatus,
			Length: 2,
			Value:  requestStatusValue,
		})

		msg.AddGroupedAttribute(AttrFloorRequestInfo, floorRequestInfoSubs)
		s.logf("📤 [InjectFloorRequest] Sending FloorRequestStatus(Pending) to %s", session.Transport.RemoteAddr())
		session.send(msg)
	}

	// Auto-grant the floor (server decision)
	if s.config.AutoGrant {
		s.logf("✅ [InjectFloorRequest] Auto-granting floor %d to userID=%d", floorID, sharerUserID)

		// Simulate human grant delay (100-200ms) to avoid Polycom UI misinterpreting
		// a Pending/Granted burst as duplicate ACKs
		time.Sleep(150 * time.Millisecond)
		s.logf("⏱️  [InjectFloorRequest] Human grant delay completed (150ms)")

		if err := floor.Grant(); err == nil {
			// Send FloorRequestStatus(Granted) to all controllers
			for _, session := range controllers {
				// Use unique txID for each server-initiated notification
				txID := uint16(s.nextTxID.Add(1))
				msg := NewMessage(PrimitiveFloorRequestStatus, s.config.ConferenceID, txID, sharerUserID)
				msg.Version = ProtocolVersionRFC4582
				msg.AddFloorID(floorID)
				msg.AddFloorRequestID(requestID)

				// Build grouped Floor-Request-Info with Granted status
				var floorRequestInfoSubs []Attribute
				beneficiaryValue := make([]byte, 2)
				binary.BigEndian.PutUint16(beneficiaryValue, sharerUserID)
				floorRequestInfoSubs = append(floorRequestInfoSubs, Attribute{
					Type:   AttrBeneficiaryID,
					Length: 2,
					Value:  beneficiaryValue,
				})

				requestStatusValue := make([]byte, 2)
				requestStatusValue[0] = uint8(RequestStatusGranted)
				requestStatusValue[1] = 0 // Queue position
				floorRequestInfoSubs = append(floorRequestInfoSubs, Attribute{
					Type:   AttrRequestStatus,
					Length: 2,
					Value:  requestStatusValue,
				})

				msg.AddGroupedAttribute(AttrFloorRequestInfo, floorRequestInfoSubs)
				s.logf("📤 [InjectFloorRequest] Sending FloorRequestStatus(Granted) to %s", session.Transport.RemoteAddr())
				session.send(msg)
			}

			if s.OnFloorGranted != nil {
				s.OnFloorGranted(floorID, sharerUserID, requestID)
			}
		} else {
			s.logf("❌ [InjectFloorRequest] Failed to grant floor: %v", err)
			return requestID, fmt.Errorf("failed to grant floor: %w", err)
		}
	}

	return requestID, nil
}

// broadcastFloorStatus broadcasts a FloorRequestStatus message to all active sessions in a conference
func (s *Server) broadcastFloorStatus(conferenceID uint32, userID uint16, floorID, requestID uint16, status RequestStatus, queuePos uint8) {
	s.logf("📢 [Broadcast] Broadcasting FloorRequestStatus(status=%v) for floor %d in conference %d", status, floorID, conferenceID)

	// Get the conference
	conf, exists := s.conferenceManager.GetConference(conferenceID)
	if !exists {
		s.logf("⚠️ [Broadcast] Conference %d not found", conferenceID)
		return
	}

	// Get all sessions in this conference
	conf.mu.RLock()
	sessions := make([]*Session, 0, len(conf.sessions))
	for _, session := range conf.sessions {
		sessions = append(sessions, session)
	}
	conf.mu.RUnlock()

	// Broadcast to all sessions in the conference
	for _, session := range sessions {
		addr := session.Transport.RemoteAddr().String()
		s.logf("📤 [Broadcast] Sending FloorRequestStatus(status=%v) to %s", status, addr)

		// Create message with new transaction ID for each recipient
		txID := uint16(s.nextTxID.Add(1))
		msg := NewMessage(PrimitiveFloorRequestStatus, conferenceID, txID, userID)
		msg.AddFloorID(floorID)
		if requestID > 0 {
			msg.AddFloorRequestID(requestID)
		}
		msg.AddRequestStatus(status, queuePos)

		session.send(msg)
	}

	s.logf("✅ [Broadcast] FloorRequestStatus broadcast completed to %d session(s) in conference %d", len(sessions), conferenceID)
}

// logf logs a message if logging is enabled
func (s *Server) logf(format string, args ...interface{}) {
	if s.config.EnableLogging {
		log.Printf("[BFCP Server] "+format, args...)
	}
}
