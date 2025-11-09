package bfcp

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Conference represents a BFCP conference (typically maps to one SIP call)
type Conference struct {
	ID              uint32
	SIPDeviceUserID uint16
	nextVirtualID   uint16
	floors          map[uint16]*FloorStateMachine
	sessions        map[string]*Session // Remote address -> Session
	virtualClients  map[uint16]string   // userID -> participant identity
	mu              sync.RWMutex
}

// NewConference creates a new conference
func NewConference(conferenceID uint32, sipDeviceUserID uint16) *Conference {
	return &Conference{
		ID:              conferenceID,
		SIPDeviceUserID: sipDeviceUserID,
		nextVirtualID:   100, // Start virtual userIDs from 100
		floors:          make(map[uint16]*FloorStateMachine),
		sessions:        make(map[string]*Session),
		virtualClients:  make(map[uint16]string),
	}
}

// AllocateVirtualUserID allocates a unique userID for a WebRTC virtual client
func (c *Conference) AllocateVirtualUserID() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()

	userID := c.nextVirtualID
	c.nextVirtualID++
	return userID
}

// RegisterVirtualClient registers a WebRTC participant as a virtual BFCP client
func (c *Conference) RegisterVirtualClient(userID uint16, participantIdentity string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.virtualClients[userID] = participantIdentity
}

// UnregisterVirtualClient removes a virtual client
func (c *Conference) UnregisterVirtualClient(userID uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.virtualClients, userID)
}

// AddFloor adds a floor to the conference
func (c *Conference) AddFloor(floorID uint16) *FloorStateMachine {
	c.mu.Lock()
	defer c.mu.Unlock()

	if floor, exists := c.floors[floorID]; exists {
		return floor
	}

	floor := NewFloorStateMachine(floorID, c.ID)
	c.floors[floorID] = floor
	return floor
}

// GetFloor retrieves a floor by ID
func (c *Conference) GetFloor(floorID uint16) (*FloorStateMachine, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	floor, exists := c.floors[floorID]
	return floor, exists
}

// RemoveFloor removes a floor from the conference
func (c *Conference) RemoveFloor(floorID uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.floors, floorID)
}

// AddSession registers a BFCP session (TCP connection) with this conference
func (c *Conference) AddSession(remoteAddr string, session *Session) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sessions[remoteAddr] = session
}

// RemoveSession removes a session from the conference
func (c *Conference) RemoveSession(remoteAddr string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.sessions, remoteAddr)
}

// GetSession retrieves a session by remote address
func (c *Conference) GetSession(remoteAddr string) (*Session, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	session, exists := c.sessions[remoteAddr]
	return session, exists
}

// Sessions returns all active sessions for this conference
func (c *Conference) Sessions() []*Session {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// ConferenceManager manages multiple BFCP conferences on a single server
type ConferenceManager struct {
	server       *Server
	conferences  map[uint32]*Conference
	mu           sync.RWMutex
	nextFloorID  atomic.Uint32 // Global floor ID allocator

	// Pending floor requests (processed after client Hello)
	// Key: conferenceID-userID composite
	pendingRequests map[string]*PendingFloorRequest
	pendingMu       sync.Mutex
}

// PendingFloorRequest represents a floor request waiting for BFCP client connection
type PendingFloorRequest struct {
	ConferenceID uint32
	FloorID      uint16
	UserID       uint16
	Priority     Priority
}

// NewConferenceManager creates a new conference manager
func NewConferenceManager(server *Server) *ConferenceManager {
	cm := &ConferenceManager{
		server:          server,
		conferences:     make(map[uint32]*Conference),
		pendingRequests: make(map[string]*PendingFloorRequest),
	}
	cm.nextFloorID.Store(1) // Start floor IDs from 1
	return cm
}

// CreateConference creates a new BFCP conference
func (cm *ConferenceManager) CreateConference(conferenceID uint32, sipDeviceUserID uint16) (*Conference, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.conferences[conferenceID]; exists {
		return nil, fmt.Errorf("conference %d already exists", conferenceID)
	}

	conf := NewConference(conferenceID, sipDeviceUserID)
	cm.conferences[conferenceID] = conf
	return conf, nil
}

// GetConference retrieves a conference by ID
func (cm *ConferenceManager) GetConference(conferenceID uint32) (*Conference, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	conf, exists := cm.conferences[conferenceID]
	return conf, exists
}

// DeleteConference removes a conference and releases all its resources
func (cm *ConferenceManager) DeleteConference(conferenceID uint32) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	conf, exists := cm.conferences[conferenceID]
	if !exists {
		return fmt.Errorf("conference %d not found", conferenceID)
	}

	// Release all floors
	conf.mu.Lock()
	for floorID, floor := range conf.floors {
		floor.mu.Lock()
		floor.CurrentState = RequestStatusReleased
		floor.OwnerUserID = 0
		floor.mu.Unlock()
		delete(conf.floors, floorID)
	}

	// Close all sessions
	for remoteAddr := range conf.sessions {
		delete(conf.sessions, remoteAddr)
	}
	conf.mu.Unlock()

	// Remove pending requests for this conference
	cm.pendingMu.Lock()
	for key := range cm.pendingRequests {
		if req := cm.pendingRequests[key]; req.ConferenceID == conferenceID {
			delete(cm.pendingRequests, key)
		}
	}
	cm.pendingMu.Unlock()

	delete(cm.conferences, conferenceID)
	return nil
}

// AllocateFloor allocates a globally unique floor ID for a conference
func (cm *ConferenceManager) AllocateFloor(conferenceID uint32) (uint16, error) {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return 0, fmt.Errorf("conference %d not found", conferenceID)
	}

	// Globally unique floor ID (no collisions between conferences)
	floorID := uint16(cm.nextFloorID.Add(1))

	// Create floor state machine in this conference
	conf.AddFloor(floorID)

	return floorID, nil
}

// AllocateVirtualUserID allocates a userID for a WebRTC virtual client
func (cm *ConferenceManager) AllocateVirtualUserID(conferenceID uint32) (uint16, error) {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return 0, fmt.Errorf("conference %d not found", conferenceID)
	}

	return conf.AllocateVirtualUserID(), nil
}

// RegisterVirtualClient registers a WebRTC participant as a virtual BFCP client
func (cm *ConferenceManager) RegisterVirtualClient(conferenceID uint32, userID uint16, participantIdentity string) error {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return fmt.Errorf("conference %d not found", conferenceID)
	}

	conf.RegisterVirtualClient(userID, participantIdentity)
	return nil
}

// UnregisterVirtualClient removes a virtual client
func (cm *ConferenceManager) UnregisterVirtualClient(conferenceID uint32, userID uint16) error {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return fmt.Errorf("conference %d not found", conferenceID)
	}

	conf.UnregisterVirtualClient(userID)
	return nil
}

// InjectFloorRequest simulates a floor request for a user in a conference
// This is used when WebRTC participants share screens, or when SIP devices
// need floor requests injected on their behalf
func (cm *ConferenceManager) InjectFloorRequest(conferenceID uint32, floorID, userID uint16) (requestID uint16, err error) {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return 0, fmt.Errorf("conference %d not found", conferenceID)
	}

	// Check if client is connected (has an active session)
	conf.mu.RLock()
	hasSession := len(conf.sessions) > 0
	conf.mu.RUnlock()

	if !hasSession {
		// Client not connected yet - queue the request
		key := fmt.Sprintf("%d-%d", conferenceID, userID)
		cm.pendingMu.Lock()
		cm.pendingRequests[key] = &PendingFloorRequest{
			ConferenceID: conferenceID,
			FloorID:      floorID,
			UserID:       userID,
			Priority:     PriorityNormal,
		}
		cm.pendingMu.Unlock()

		return 0, nil
	}

	// Client is connected - inject floor request immediately via server
	if cm.server != nil {
		return cm.server.InjectFloorRequest(floorID, userID)
	}

	return 0, fmt.Errorf("BFCP server not available")
}

// OnClientConnect is called when a BFCP client completes Hello handshake
// It processes any pending floor requests for that client's conference
func (cm *ConferenceManager) OnClientConnect(conferenceID uint32, remoteAddr string, userID uint16) {
	// Get pending requests for this conference
	cm.pendingMu.Lock()
	var pending []*PendingFloorRequest
	for key, req := range cm.pendingRequests {
		if req.ConferenceID == conferenceID {
			pending = append(pending, req)
			delete(cm.pendingRequests, key)
		}
	}
	cm.pendingMu.Unlock()

	// Process pending requests via server
	if cm.server != nil {
		for _, req := range pending {
			cm.server.InjectFloorRequest(req.FloorID, req.UserID)
		}
	}
}

// OnClientDisconnect is called when a BFCP client disconnects
func (cm *ConferenceManager) OnClientDisconnect(conferenceID uint32, remoteAddr string, userID uint16) {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return
	}

	// Remove session from conference
	conf.RemoveSession(remoteAddr)
}

// RouteSession assigns an incoming BFCP session to the correct conference
func (cm *ConferenceManager) RouteSession(conferenceID uint32, remoteAddr string, session *Session) error {
	conf, exists := cm.GetConference(conferenceID)
	if !exists {
		return fmt.Errorf("conference %d not found for session %s", conferenceID, remoteAddr)
	}

	conf.AddSession(remoteAddr, session)
	return nil
}
