package bfcp

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	udpClientT1         = 500 * time.Millisecond
	udpClientMaxRetries = 4
)

// UDPClientConfig configures the client role toward one remote floor control
// server (RFC 8855 over UDP).
type UDPClientConfig struct {
	RemoteAddr   string
	ConferenceID uint32
	UserID       uint16
	Version      uint8
	T1           time.Duration
	MaxRetries   int
}

// UDPClient drives the client role on the UDP socket the Server listens on:
// the remote server sends to the port announced in SDP whatever the role.
type UDPClient struct {
	server    *Server
	config    UDPClientConfig
	transport *UDPTransport
	remote    string

	nextTxID atomic.Uint32
	closed   atomic.Bool
	done     chan struct{}

	pendingMu sync.Mutex
	pending   map[uint16]chan *Message

	requestsMu     sync.RWMutex
	activeRequests map[uint16]*ActiveFloorRequest

	OnFloorGranted  func(floorID, requestID uint16)
	OnFloorDenied   func(floorID, requestID uint16)
	OnFloorReleased func(floorID uint16)
	OnFloorStatus   func(floorID, beneficiaryID uint16, status RequestStatus)
	OnError         func(error)
}

// NewUDPClient registers a client role toward config.RemoteAddr on the
// server's UDP socket; incoming datagrams from that address go to the client.
func (s *Server) NewUDPClient(config UDPClientConfig) (*UDPClient, error) {
	if s.udpListener == nil {
		return nil, fmt.Errorf("no UDP listener")
	}
	addr, err := net.ResolveUDPAddr("udp", config.RemoteAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve remote address %s: %w", config.RemoteAddr, err)
	}
	if config.Version == 0 {
		config.Version = ProtocolVersionRFC8855
	}
	if config.T1 <= 0 {
		config.T1 = udpClientT1
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = udpClientMaxRetries
	}

	c := &UDPClient{
		server:         s,
		config:         config,
		transport:      s.udpListener.getOrCreateTransport(addr),
		remote:         addr.String(),
		pending:        make(map[uint16]chan *Message),
		activeRequests: make(map[uint16]*ActiveFloorRequest),
		done:           make(chan struct{}),
	}

	s.mu.Lock()
	s.udpClients[c.remote] = c
	s.mu.Unlock()

	s.logger().Infow("bfcp.udpclient.created", "remote", c.remote, "confID", config.ConferenceID, "userID", config.UserID, "version", config.Version)
	return c, nil
}

// Close unregisters the client; pending transactions time out.
func (c *UDPClient) Close() {
	if c.closed.Swap(true) {
		return
	}
	close(c.done)
	c.server.mu.Lock()
	delete(c.server.udpClients, c.remote)
	c.server.mu.Unlock()
	c.server.logger().Debugw("bfcp.udpclient.closed", "remote", c.remote)
}

// RemoteAddr returns the floor control server address.
func (c *UDPClient) RemoteAddr() string {
	return c.remote
}

// Hello opens the BFCP association.
func (c *UDPClient) Hello() error {
	msg := NewMessage(PrimitiveHello, c.config.ConferenceID, c.nextTransactionID(), c.config.UserID)
	msg.AddSupportedPrimitives([]Primitive{
		PrimitiveFloorRequest,
		PrimitiveFloorRelease,
		PrimitiveFloorRequestQuery,
		PrimitiveFloorRequestStatus,
		PrimitiveFloorQuery,
		PrimitiveFloorStatus,
		PrimitiveHello,
		PrimitiveHelloAck,
		PrimitiveError,
		PrimitiveFloorRequestStatusAck,
		PrimitiveFloorStatusAck,
		PrimitiveGoodbye,
		PrimitiveGoodbyeAck,
	})
	msg.AddSupportedAttributes([]AttributeType{
		AttrBeneficiaryID,
		AttrFloorID,
		AttrFloorRequestID,
		AttrPriority,
		AttrRequestStatus,
		AttrErrorCode,
		AttrErrorInfo,
		AttrSupportedAttributes,
		AttrSupportedPrimitives,
		AttrBeneficiaryInfo,
		AttrFloorRequestInfo,
		AttrRequestedByInfo,
		AttrFloorRequestStatus,
		AttrOverallRequestStatus,
	})

	response, err := c.transact(msg)
	if err != nil {
		return err
	}
	if response.Primitive == PrimitiveError {
		return responseError(response)
	}
	if response.Primitive != PrimitiveHelloAck {
		return fmt.Errorf("expected HelloAck, got %s", response.Primitive)
	}
	c.server.logger().Infow("bfcp.udpclient.hello_completed", "remote", c.remote)
	return nil
}

// RequestFloor asks the server for floorID and returns the floor request ID.
func (c *UDPClient) RequestFloor(floorID uint16) (uint16, error) {
	msg := NewMessage(PrimitiveFloorRequest, c.config.ConferenceID, c.nextTransactionID(), c.config.UserID)
	msg.AddFloorID(floorID)

	response, err := c.transact(msg)
	if err != nil {
		return 0, err
	}
	if response.Primitive == PrimitiveError {
		return 0, responseError(response)
	}
	if response.Primitive != PrimitiveFloorRequestStatus {
		return 0, fmt.Errorf("expected FloorRequestStatus, got %s", response.Primitive)
	}

	requestID, ok := response.GetFloorRequestID()
	if !ok {
		return 0, fmt.Errorf("missing FLOOR-REQUEST-ID in response")
	}
	status, _, ok := response.GetRequestStatus()
	if !ok {
		return 0, fmt.Errorf("missing REQUEST-STATUS in response")
	}

	c.requestsMu.Lock()
	c.activeRequests[floorID] = &ActiveFloorRequest{
		FloorID:        floorID,
		FloorRequestID: requestID,
		BeneficiaryID:  c.config.UserID,
		Status:         status,
		RequestedAt:    time.Now(),
	}
	c.requestsMu.Unlock()

	c.server.logger().Infow("bfcp.udpclient.floor_requested", "remote", c.remote, "floorID", floorID, "requestID", requestID, "status", status.String())
	c.applyOwnStatus(floorID, requestID, status)
	return requestID, nil
}

// ReleaseFloor releases the request previously granted or pending on floorID.
func (c *UDPClient) ReleaseFloor(floorID uint16) error {
	c.requestsMu.RLock()
	req, exists := c.activeRequests[floorID]
	c.requestsMu.RUnlock()
	if !exists {
		return fmt.Errorf("no active request for floor %d", floorID)
	}

	msg := NewMessage(PrimitiveFloorRelease, c.config.ConferenceID, c.nextTransactionID(), c.config.UserID)
	msg.AddFloorRequestID(req.FloorRequestID)

	response, err := c.transact(msg)
	if err != nil {
		return err
	}
	if response.Primitive == PrimitiveError {
		return responseError(response)
	}

	c.requestsMu.Lock()
	delete(c.activeRequests, floorID)
	c.requestsMu.Unlock()

	c.server.logger().Infow("bfcp.udpclient.floor_released", "remote", c.remote, "floorID", floorID, "requestID", req.FloorRequestID)
	if c.OnFloorReleased != nil {
		c.OnFloorReleased(floorID)
	}
	return nil
}

// Goodbye closes the association; a missing GoodbyeAck is not an error.
func (c *UDPClient) Goodbye() error {
	msg := NewMessage(PrimitiveGoodbye, c.config.ConferenceID, c.nextTransactionID(), c.config.UserID)
	if _, err := c.transact(msg); err != nil {
		c.server.logger().Debugw("bfcp.udpclient.goodbye_unacked", "remote", c.remote, "err", err.Error())
	}
	return nil
}

// HasFloor reports whether the client holds a granted request on floorID.
func (c *UDPClient) HasFloor(floorID uint16) bool {
	c.requestsMu.RLock()
	defer c.requestsMu.RUnlock()
	req, ok := c.activeRequests[floorID]
	return ok && req.Status == RequestStatusGranted
}

func (c *UDPClient) transact(msg *Message) (*Message, error) {
	if c.closed.Load() {
		return nil, fmt.Errorf("client closed")
	}
	respCh := make(chan *Message, 1)
	c.pendingMu.Lock()
	c.pending[msg.TransactionID] = respCh
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, msg.TransactionID)
		c.pendingMu.Unlock()
	}()

	wait := c.config.T1
	for attempt := 1; attempt <= c.config.MaxRetries; attempt++ {
		if err := c.send(msg); err != nil {
			return nil, err
		}
		select {
		case response := <-respCh:
			return response, nil
		case <-c.done:
			return nil, fmt.Errorf("client closed while waiting for %s", msg.Primitive)
		case <-time.After(wait):
			c.server.logger().Debugw("bfcp.udpclient.retransmit", "remote", c.remote, "primitive", msg.Primitive.String(), "txID", msg.TransactionID, "attempt", attempt)
		}
		wait *= 2
	}
	return nil, fmt.Errorf("no response to %s from %s after %d attempts", msg.Primitive, c.remote, c.config.MaxRetries)
}

func (c *UDPClient) send(msg *Message) error {
	msg.Version = c.config.Version
	if err := c.transport.SendMessage(msg); err != nil {
		return fmt.Errorf("failed to send %s: %w", msg.Primitive, err)
	}
	if c.server.OnMessageOut != nil {
		floorID, _ := msg.GetFloorID()
		c.server.OnMessageOut(c.remote, msg.Primitive.String(), msg.Version, uint32(msg.TransactionID), msg.ConferenceID, msg.UserID, floorID)
	}
	return nil
}

func (c *UDPClient) handleMessage(msg *Message) {
	c.pendingMu.Lock()
	respCh, isPending := c.pending[msg.TransactionID]
	c.pendingMu.Unlock()
	if isPending {
		select {
		case respCh <- msg:
		default:
		}
		return
	}

	switch msg.Primitive {
	case PrimitiveFloorRequestStatus:
		c.handleNotification(msg)
		c.ack(msg, PrimitiveFloorRequestStatusAck)
	case PrimitiveFloorStatus:
		c.handleNotification(msg)
		c.ack(msg, PrimitiveFloorStatusAck)
	case PrimitiveGoodbye:
		c.ack(msg, PrimitiveGoodbyeAck)
	case PrimitiveError:
		if c.OnError != nil {
			c.OnError(responseError(msg))
		}
	default:
		c.server.logger().Debugw("bfcp.udpclient.unexpected_msg", "remote", c.remote, "primitive", msg.Primitive.String())
	}
}

// handleNotification applies a server-initiated FloorRequestStatus or
// FloorStatus: own requests update their state, other users' requests are
// reported through OnFloorStatus.
func (c *UDPClient) handleNotification(msg *Message) {
	infos := msg.FloorRequestInfos()
	if len(infos) == 0 {
		if msg.Primitive == PrimitiveFloorStatus {
			if floorID, ok := msg.GetFloorID(); ok && c.OnFloorStatus != nil {
				c.OnFloorStatus(floorID, 0, RequestStatusReleased)
			}
		}
		return
	}

	for _, info := range infos {
		status, ok := info.Status()
		if !ok {
			continue
		}
		beneficiaryID := info.BeneficiaryID
		if !info.HasBeneficiary && info.HasRequestedBy {
			beneficiaryID = info.RequestedByID
		}
		for _, fl := range info.Floors {
			if c.ownRequest(fl.FloorID, info.FloorRequestID, beneficiaryID) {
				c.applyOwnStatus(fl.FloorID, info.FloorRequestID, status)
				continue
			}
			if c.OnFloorStatus != nil {
				c.OnFloorStatus(fl.FloorID, beneficiaryID, status)
			}
		}
	}
}

func (c *UDPClient) ownRequest(floorID, requestID, beneficiaryID uint16) bool {
	c.requestsMu.RLock()
	defer c.requestsMu.RUnlock()
	req, ok := c.activeRequests[floorID]
	if ok && req.FloorRequestID == requestID {
		return true
	}
	return beneficiaryID != 0 && beneficiaryID == c.config.UserID
}

func (c *UDPClient) applyOwnStatus(floorID, requestID uint16, status RequestStatus) {
	c.requestsMu.Lock()
	if req, ok := c.activeRequests[floorID]; ok {
		req.Status = status
		if status == RequestStatusGranted && req.GrantedAt.IsZero() {
			req.GrantedAt = time.Now()
		}
		if status == RequestStatusReleased || status == RequestStatusRevoked || status == RequestStatusDenied || status == RequestStatusCancelled {
			delete(c.activeRequests, floorID)
		}
	}
	c.requestsMu.Unlock()

	switch status {
	case RequestStatusGranted:
		if c.OnFloorGranted != nil {
			c.OnFloorGranted(floorID, requestID)
		}
	case RequestStatusDenied:
		if c.OnFloorDenied != nil {
			c.OnFloorDenied(floorID, requestID)
		}
	case RequestStatusReleased, RequestStatusRevoked, RequestStatusCancelled:
		if c.OnFloorReleased != nil {
			c.OnFloorReleased(floorID)
		}
	}
}

func (c *UDPClient) ack(msg *Message, primitive Primitive) {
	ack := NewMessage(primitive, msg.ConferenceID, msg.TransactionID, c.config.UserID)
	ack.SetResponse(true)
	if err := c.send(ack); err != nil {
		c.server.logger().Warnw("bfcp.udpclient.ack_failed", err, "remote", c.remote, "primitive", primitive.String())
	}
}

func (c *UDPClient) nextTransactionID() uint16 {
	id := uint16(c.nextTxID.Add(1))
	if id == 0 {
		id = uint16(c.nextTxID.Add(1))
	}
	return id
}

func responseError(msg *Message) error {
	code, _ := msg.GetErrorCode()
	info, _ := msg.GetErrorInfo()
	return fmt.Errorf("BFCP error: %s - %s", code, info)
}
