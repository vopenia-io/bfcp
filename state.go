package bfcp

import (
	"fmt"
	"sync"
	"time"
)

// FloorStateMachine manages the state of a single floor
type FloorStateMachine struct {
	mu sync.RWMutex

	FloorID        uint16
	ConferenceID   uint32
	CurrentState   RequestStatus
	OwnerUserID    uint16
	FloorRequestID uint16
	Priority       Priority
	CreatedAt      time.Time
	GrantedAt      time.Time
}

// NewFloorStateMachine creates a new floor state machine
func NewFloorStateMachine(floorID uint16, conferenceID uint32) *FloorStateMachine {
	return &FloorStateMachine{
		FloorID:      floorID,
		ConferenceID: conferenceID,
		CurrentState: RequestStatusReleased,
		CreatedAt:    time.Now(),
	}
}

// Request attempts to request the floor for a user
func (fsm *FloorStateMachine) Request(userID, requestID uint16, priority Priority) (RequestStatus, error) {
	fsm.mu.Lock()
	defer fsm.mu.Unlock()

	switch fsm.CurrentState {
	case RequestStatusReleased:
		// Floor is available, grant immediately
		fsm.OwnerUserID = userID
		fsm.FloorRequestID = requestID
		fsm.Priority = priority
		fsm.CurrentState = RequestStatusPending
		return RequestStatusPending, nil

	case RequestStatusPending, RequestStatusGranted:
		// Floor is already in use
		return RequestStatusDenied, fmt.Errorf("floor %d already in use by user %d", fsm.FloorID, fsm.OwnerUserID)

	default:
		return RequestStatusDenied, fmt.Errorf("invalid floor state: %s", fsm.CurrentState)
	}
}

// Grant grants the floor (moves from Pending to Granted)
func (fsm *FloorStateMachine) Grant() error {
	fsm.mu.Lock()
	defer fsm.mu.Unlock()

	if fsm.CurrentState != RequestStatusPending {
		return fmt.Errorf("cannot grant floor in state %s", fsm.CurrentState)
	}

	fsm.CurrentState = RequestStatusGranted
	fsm.GrantedAt = time.Now()
	return nil
}

// Deny denies the floor request
func (fsm *FloorStateMachine) Deny() error {
	fsm.mu.Lock()
	defer fsm.mu.Unlock()

	if fsm.CurrentState != RequestStatusPending {
		return fmt.Errorf("cannot deny floor in state %s", fsm.CurrentState)
	}

	fsm.CurrentState = RequestStatusDenied
	fsm.OwnerUserID = 0
	fsm.FloorRequestID = 0
	return nil
}

// Release releases the floor
func (fsm *FloorStateMachine) Release(userID uint16) error {
	fsm.mu.Lock()
	defer fsm.mu.Unlock()

	if fsm.OwnerUserID != userID && userID != 0 {
		return fmt.Errorf("user %d does not own floor %d (owner: %d)", userID, fsm.FloorID, fsm.OwnerUserID)
	}

	fsm.CurrentState = RequestStatusReleased
	fsm.OwnerUserID = 0
	fsm.FloorRequestID = 0
	fsm.Priority = 0
	return nil
}

// Revoke revokes the floor (server-initiated)
func (fsm *FloorStateMachine) Revoke() error {
	fsm.mu.Lock()
	defer fsm.mu.Unlock()

	fsm.CurrentState = RequestStatusRevoked
	prevOwner := fsm.OwnerUserID
	fsm.OwnerUserID = 0
	fsm.FloorRequestID = 0

	// After revoking, move to released
	fsm.CurrentState = RequestStatusReleased

	if prevOwner == 0 {
		return fmt.Errorf("no user to revoke")
	}

	return nil
}

// GetState returns the current state (thread-safe)
func (fsm *FloorStateMachine) GetState() RequestStatus {
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	return fsm.CurrentState
}

// GetOwner returns the current owner (thread-safe)
func (fsm *FloorStateMachine) GetOwner() uint16 {
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	return fsm.OwnerUserID
}

// GetFloorRequestID returns the current floor request ID (thread-safe)
func (fsm *FloorStateMachine) GetFloorRequestID() uint16 {
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	return fsm.FloorRequestID
}

// IsAvailable checks if the floor is available
func (fsm *FloorStateMachine) IsAvailable() bool {
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	return fsm.CurrentState == RequestStatusReleased
}

// IsGranted checks if the floor is granted
func (fsm *FloorStateMachine) IsGranted() bool {
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	return fsm.CurrentState == RequestStatusGranted
}

// SessionStateMachine manages the BFCP session state
type SessionStateMachine struct {
	mu sync.RWMutex

	CurrentState       SessionState
	ConferenceID       uint32
	UserID             uint16
	RemoteAddr         string
	SupportedPrimitives []Primitive
	SupportedAttributes []AttributeType
	CreatedAt          time.Time
	LastActivityAt     time.Time
}

// NewSessionStateMachine creates a new session state machine
func NewSessionStateMachine(conferenceID, userID uint16, remoteAddr string) *SessionStateMachine {
	now := time.Now()
	return &SessionStateMachine{
		CurrentState:   StateConnected,
		ConferenceID:   uint32(conferenceID),
		UserID:         userID,
		RemoteAddr:     remoteAddr,
		CreatedAt:      now,
		LastActivityAt: now,
	}
}

// UpdateActivity updates the last activity timestamp
func (ssm *SessionStateMachine) UpdateActivity() {
	ssm.mu.Lock()
	defer ssm.mu.Unlock()
	ssm.LastActivityAt = time.Now()
}

// SetState sets the session state
func (ssm *SessionStateMachine) SetState(state SessionState) {
	ssm.mu.Lock()
	defer ssm.mu.Unlock()
	ssm.CurrentState = state
	ssm.LastActivityAt = time.Now()
}

// GetState returns the current session state (thread-safe)
func (ssm *SessionStateMachine) GetState() SessionState {
	ssm.mu.RLock()
	defer ssm.mu.RUnlock()
	return ssm.CurrentState
}

// SetSupportedPrimitives sets the supported primitives
func (ssm *SessionStateMachine) SetSupportedPrimitives(primitives []Primitive) {
	ssm.mu.Lock()
	defer ssm.mu.Unlock()
	ssm.SupportedPrimitives = primitives
}

// SetSupportedAttributes sets the supported attributes
func (ssm *SessionStateMachine) SetSupportedAttributes(attributes []AttributeType) {
	ssm.mu.Lock()
	defer ssm.mu.Unlock()
	ssm.SupportedAttributes = attributes
}

// GetIdleDuration returns how long the session has been idle
func (ssm *SessionStateMachine) GetIdleDuration() time.Duration {
	ssm.mu.RLock()
	defer ssm.mu.RUnlock()
	return time.Since(ssm.LastActivityAt)
}

// FloorRequestQueue manages a queue of pending floor requests
type FloorRequestQueue struct {
	mu       sync.RWMutex
	requests []FloorRequest
}

// FloorRequest represents a floor request in the queue
type FloorRequest struct {
	FloorID        uint16
	FloorRequestID uint16
	UserID         uint16
	BeneficiaryID  uint16
	Priority       Priority
	Timestamp      time.Time
}

// NewFloorRequestQueue creates a new floor request queue
func NewFloorRequestQueue() *FloorRequestQueue {
	return &FloorRequestQueue{
		requests: make([]FloorRequest, 0),
	}
}

// Add adds a request to the queue
func (q *FloorRequestQueue) Add(req FloorRequest) {
	q.mu.Lock()
	defer q.mu.Unlock()

	req.Timestamp = time.Now()
	q.requests = append(q.requests, req)

	// Sort by priority (higher priority first) and timestamp
	for i := len(q.requests) - 1; i > 0; i-- {
		if q.requests[i].Priority > q.requests[i-1].Priority {
			q.requests[i], q.requests[i-1] = q.requests[i-1], q.requests[i]
		} else if q.requests[i].Priority == q.requests[i-1].Priority &&
			q.requests[i].Timestamp.Before(q.requests[i-1].Timestamp) {
			q.requests[i], q.requests[i-1] = q.requests[i-1], q.requests[i]
		}
	}
}

// Remove removes a request from the queue by floor request ID
func (q *FloorRequestQueue) Remove(floorRequestID uint16) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, req := range q.requests {
		if req.FloorRequestID == floorRequestID {
			q.requests = append(q.requests[:i], q.requests[i+1:]...)
			return true
		}
	}
	return false
}

// GetNext returns the next request in the queue (highest priority, earliest timestamp)
func (q *FloorRequestQueue) GetNext() (*FloorRequest, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if len(q.requests) == 0 {
		return nil, false
	}
	req := q.requests[0]
	return &req, true
}

// PopNext removes and returns the next request in the queue
func (q *FloorRequestQueue) PopNext() (*FloorRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.requests) == 0 {
		return nil, false
	}

	req := q.requests[0]
	q.requests = q.requests[1:]
	return &req, true
}

// Size returns the number of requests in the queue
func (q *FloorRequestQueue) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.requests)
}

// GetPosition returns the queue position of a request (1-based), or 0 if not found
func (q *FloorRequestQueue) GetPosition(floorRequestID uint16) int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	for i, req := range q.requests {
		if req.FloorRequestID == floorRequestID {
			return i + 1 // 1-based position
		}
	}
	return 0
}
