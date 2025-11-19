package bfcp

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type ServerConfig struct {
	Address          string
	ConferenceID     uint32
	AutoGrant        bool
	MaxFloors        int
	SessionTimeout   int
	EnableLogging    bool
}

func DefaultServerConfig(address string, conferenceID uint32) *ServerConfig {
	return &ServerConfig{
		Address:        address,
		ConferenceID:   conferenceID,
		AutoGrant:      true,
		MaxFloors:      10,
		SessionTimeout: 300,
		EnableLogging:  true,
	}
}

type Server struct {
	config   *ServerConfig
	listener *Listener

	floors   map[uint16]*FloorStateMachine
	sessions map[string]*Session
	mu       sync.RWMutex

	nextTxID atomic.Uint32

	OnFloorRequest     func(floorID, userID, requestID uint16) bool
	OnFloorGranted     func(floorID, userID, requestID uint16)
	OnFloorReleased    func(floorID, userID uint16)
	OnFloorDenied      func(floorID, userID, requestID uint16)
	OnClientConnect    func(remoteAddr string, userID uint16)
	OnClientDisconnect func(remoteAddr string, userID uint16)
	OnError            func(error)
}

type Session struct {
	Transport    *Transport
	StateMachine *SessionStateMachine
	Server       *Server
}

func NewServer(config *ServerConfig) *Server {
	if config == nil {
		config = DefaultServerConfig(":5070", 1)
	}

	return &Server{
		config:   config,
		floors:   make(map[uint16]*FloorStateMachine),
		sessions: make(map[string]*Session),
	}
}

func (s *Server) CreateFloor(floorID uint16) *FloorStateMachine {
	s.mu.Lock()
	defer s.mu.Unlock()

	if floor, exists := s.floors[floorID]; exists {
		return floor
	}

	floor := NewFloorStateMachine(floorID, s.config.ConferenceID)
	s.floors[floorID] = floor
	s.logf("Created floor %d", floorID)
	return floor
}

func (s *Server) GetFloor(floorID uint16) (*FloorStateMachine, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	floor, exists := s.floors[floorID]
	return floor, exists
}

func (s *Server) ReleaseFloor(floorID uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.floors, floorID)
	s.logf("Released floor %d", floorID)
}

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

	select {}
}

func (s *Server) Close() error {
	s.logf("Closing BFCP server")

	s.mu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.RUnlock()

	for _, session := range sessions {
		session.Transport.Close()
	}

	if s.listener != nil {
		return s.listener.Close()
	}

	return nil
}

func (s *Server) Addr() net.Addr {
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

func (s *Server) handleConnection(transport *Transport) {
	remoteAddr := transport.RemoteAddr().String()
	s.logf("New TCP connection from %s", remoteAddr)

	session := &Session{
		Transport:    transport,
		StateMachine: NewSessionStateMachine(uint16(s.config.ConferenceID), 0, remoteAddr),
		Server:       s,
	}

	transport.OnMessage = func(msg *Message) {
		session.handleMessage(msg)
	}

	transport.OnError = func(err error) {
		s.logf("Transport error for %s: %v", remoteAddr, err)
	}

	transport.OnClose = func() {
		s.logf("Connection closed: %s", remoteAddr)

		s.mu.Lock()
		delete(s.sessions, remoteAddr)
		s.mu.Unlock()

		if s.OnClientDisconnect != nil {
			s.OnClientDisconnect(remoteAddr, session.StateMachine.UserID)
		}
	}

	transport.Start()
}

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
	case PrimitiveFloorRequestStatus,
		PrimitiveFloorRequestStatusAck,
		PrimitiveFloorStatus,
		PrimitiveFloorStatusAck,
		PrimitiveHelloAck,
		PrimitiveGoodbyeAck:
		sess.Server.logf("Received client acknowledgment - ignoring")
	case PrimitiveError:
		if errorCode, ok := msg.GetErrorCode(); ok {
			errorInfo, _ := msg.GetErrorInfo()
			sess.Server.logf("Received error from client - ErrorCode=%s(%d), ErrorInfo=%q", errorCode, errorCode, errorInfo)
		} else {
			sess.Server.logf("Received error from client (no error code)")
		}
	default:
		sess.sendError(msg, ErrorUnknownPrimitive, fmt.Sprintf("Unknown primitive: %d", msg.Primitive))
	}
}

func (sess *Session) handleHello(msg *Message) {
	sess.Server.logf("Processing Hello from user %d (confID=%d)", msg.UserID, msg.ConferenceID)

	sess.StateMachine.ConferenceID = msg.ConferenceID
	sess.StateMachine.UserID = msg.UserID

	remoteAddr := sess.Transport.RemoteAddr().String()
	sess.Server.mu.Lock()
	sess.Server.sessions[remoteAddr] = sess
	sess.Server.mu.Unlock()

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

	response := NewMessage(PrimitiveHelloAck, msg.ConferenceID, msg.TransactionID, msg.UserID)

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
		AttrFloorRequestInfo,
		AttrRequestedByInfo,
		AttrFloorRequestStatus,
		AttrOverallRequestStatus,
		AttrParticipantProvidedInfo,
		AttrStatusInfo,
	}
	response.AddSupportedAttributes(supportedAttributes)

	sess.send(response)
	sess.StateMachine.SetState(StateWaitFloorRequest)

	sess.Transport.EnableKeepalive(30*time.Second, func() error {
		keepaliveMsg := NewMessage(PrimitiveHello, msg.ConferenceID, uint16(sess.Server.nextTxID.Add(1)), msg.UserID)
		return sess.Transport.SendKeepaliveMessage(keepaliveMsg)
	})
	sess.Transport.StartKeepalive()

	if sess.Server.OnClientConnect != nil {
		sess.Server.OnClientConnect(sess.Transport.RemoteAddr().String(), msg.UserID)
	}
}

func (sess *Session) handleFloorRequest(msg *Message) {
	sess.Server.logf("Processing FloorRequest from user %d", msg.UserID)

	floorID, ok := msg.GetFloorID()
	if !ok {
		sess.Server.logf("No floor ID in request")
		sess.sendError(msg, ErrorInvalidFloorID, "No floor ID in request")
		return
	}

	floor, exists := sess.Server.GetFloor(floorID)
	if !exists {
		sess.Server.logf("Floor %d does not exist, creating it", floorID)
		floor = sess.Server.CreateFloor(floorID)
	}

	requestID := uint16(sess.Server.nextTxID.Add(1))

	beneficiaryID, _ := msg.GetBeneficiaryID()
	if beneficiaryID == 0 {
		beneficiaryID = msg.UserID
	}

	priority := PriorityNormal
	if attr := msg.GetAttribute(AttrPriority); attr != nil && len(attr.Value) >= 2 {
		priority = Priority(uint16(attr.Value[0])<<8 | uint16(attr.Value[1]))
	}

	shouldGrant := sess.Server.config.AutoGrant
	if sess.Server.OnFloorRequest != nil {
		shouldGrant = sess.Server.OnFloorRequest(floorID, msg.UserID, requestID)
	}

	status, err := floor.Request(msg.UserID, requestID, priority)
	if err != nil {
		sess.Server.logf("Floor request failed: %v", err)
		sess.sendError(msg, ErrorUnauthorizedOperation, err.Error())
		if sess.Server.OnFloorDenied != nil {
			sess.Server.OnFloorDenied(floorID, msg.UserID, requestID)
		}
		return
	}

	sess.sendFloorStatus(msg, floorID, requestID, status, 0)

	if shouldGrant && status == RequestStatusPending {
		time.Sleep(150 * time.Millisecond)

		if err := floor.Grant(); err == nil {
			sess.sendFloorStatus(msg, floorID, requestID, RequestStatusGranted, 0)
			sess.StateMachine.SetState(StateFloorGranted)

			if sess.Server.OnFloorGranted != nil {
				sess.Server.OnFloorGranted(floorID, msg.UserID, requestID)
			}
		}
	} else if !shouldGrant && status == RequestStatusPending {
		if err := floor.Deny(); err == nil {
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

func (sess *Session) handleFloorRelease(msg *Message) {
	sess.Server.logf("Processing FloorRelease from user %d", msg.UserID)

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

	if err := floor.Release(msg.UserID); err != nil {
		sess.Server.logf("Release denied: %v", err)
		sess.sendError(msg, ErrorFloorReleaseDenied, err.Error())
		return
	}

	sess.sendFloorStatus(msg, floorID, requestID, RequestStatusReleased, 0)
	sess.StateMachine.SetState(StateFloorReleased)

	if sess.Server.OnFloorReleased != nil {
		sess.Server.OnFloorReleased(floorID, msg.UserID)
	}

	sess.Server.broadcastFloorStatus(msg.UserID, floorID, requestID, RequestStatusReleased, 0)
}

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

	response := NewMessage(PrimitiveFloorStatus, msg.ConferenceID, msg.TransactionID, msg.UserID)
	response.AddFloorID(floorID)
	response.AddFloorRequestID(floor.GetFloorRequestID())
	response.AddRequestStatus(floor.GetState(), 0)

	sess.send(response)
}

func (sess *Session) handleGoodbye(msg *Message) {
	sess.Server.logf("Processing Goodbye from user %d", msg.UserID)

	sess.Server.mu.RLock()
	floors := make([]*FloorStateMachine, 0)
	for _, floor := range sess.Server.floors {
		if floor.GetOwner() == msg.UserID {
			floors = append(floors, floor)
		}
	}
	sess.Server.mu.RUnlock()

	for _, floor := range floors {
		if err := floor.Release(msg.UserID); err == nil {
			requestID := floor.GetFloorRequestID()
			sess.Server.broadcastFloorStatus(msg.UserID, floor.FloorID, requestID, RequestStatusReleased, 0)

			if sess.Server.OnFloorReleased != nil {
				sess.Server.OnFloorReleased(floor.FloorID, msg.UserID)
			}
		}
	}

	response := NewMessage(PrimitiveGoodbyeAck, msg.ConferenceID, msg.TransactionID, msg.UserID)
	sess.send(response)

	sess.StateMachine.SetState(StateDisconnected)
	sess.Transport.Close()
}

func (sess *Session) sendFloorStatus(req *Message, floorID, requestID uint16, status RequestStatus, queuePos uint8) {
	response := NewMessage(PrimitiveFloorRequestStatus, req.ConferenceID, req.TransactionID, req.UserID)
	response.AddFloorID(floorID)
	if requestID > 0 {
		response.AddFloorRequestID(requestID)
	}
	response.AddRequestStatus(status, queuePos)
	sess.send(response)
}

func (sess *Session) sendError(req *Message, errorCode ErrorCode, errorInfo string) {
	response := NewMessage(PrimitiveError, req.ConferenceID, req.TransactionID, req.UserID)
	response.AddErrorCode(errorCode)
	if errorInfo != "" {
		response.AddErrorInfo(errorInfo)
	}
	sess.send(response)
	sess.Server.logf("Sent error to %s: %s - %s", sess.Transport.RemoteAddr(), errorCode, errorInfo)
}

func (sess *Session) send(msg *Message) {
	if err := sess.Transport.SendMessage(msg); err != nil {
		sess.Server.logf("Failed to send %s to %s: %v", msg.Primitive, sess.Transport.RemoteAddr(), err)
	} else {
		sess.Server.logf("Sent %s to %s (TxID: %d)", msg.Primitive, sess.Transport.RemoteAddr(), msg.TransactionID)
	}
}

func (s *Server) GrantFloor(floorID, userID uint16) error {
	floor, exists := s.GetFloor(floorID)
	if !exists {
		return fmt.Errorf("floor %d does not exist", floorID)
	}
	return floor.Grant()
}

func (s *Server) broadcastFloorStatus(userID uint16, floorID, requestID uint16, status RequestStatus, queuePos uint8) {
	s.mu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.RUnlock()

	for _, session := range sessions {
		txID := uint16(s.nextTxID.Add(1))
		msg := NewMessage(PrimitiveFloorRequestStatus, s.config.ConferenceID, txID, userID)
		msg.AddFloorID(floorID)
		if requestID > 0 {
			msg.AddFloorRequestID(requestID)
		}
		msg.AddRequestStatus(status, queuePos)

		session.send(msg)
	}
}

func (s *Server) logf(format string, args ...interface{}) {
	if s.config.EnableLogging {
		log.Printf("[BFCP Server] "+format, args...)
	}
}
