package virtual

import "github.com/vopenia/bfcp"

// Callbacks defines the interface for handling virtual client events.
// Applications implement this interface to respond to floor control events,
// status changes, and connection lifecycle events.
//
// All callbacks are invoked asynchronously from the client's message handler
// goroutine. Implementations should avoid blocking operations to prevent
// delaying message processing.
type Callbacks interface {
	// Floor Control Events

	// OnFloorGranted is called when the server grants floor control.
	// The application should begin transmitting media for this floor.
	//
	// Parameters:
	//   floorID - The floor identifier that was granted
	//   requestID - The request ID associated with this grant
	OnFloorGranted(floorID uint16, requestID uint16)

	// OnFloorDenied is called when the server denies a floor request.
	// This can happen due to floor conflicts, quota limits, or policy decisions.
	//
	// Parameters:
	//   floorID - The floor identifier that was denied
	//   requestID - The request ID associated with this denial
	//   errorCode - BFCP error code indicating the denial reason
	//   errorInfo - Human-readable description of the error
	OnFloorDenied(floorID uint16, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string)

	// OnFloorRevoked is called when the server revokes a previously granted floor.
	// This is a unilateral action by the server (e.g., chair intervention).
	// The application should immediately stop transmitting media for this floor.
	//
	// Parameters:
	//   floorID - The floor identifier that was revoked
	//   requestID - The request ID associated with this revocation
	OnFloorRevoked(floorID uint16, requestID uint16)

	// OnFloorReleased is called when the floor is successfully released.
	// This confirms that the server has processed the release request.
	//
	// Parameters:
	//   floorID - The floor identifier that was released
	//   requestID - The request ID associated with this release
	OnFloorReleased(floorID uint16, requestID uint16)

	// Status Events

	// OnQueuePositionChanged is called when the client's position in the
	// floor request queue changes. Position 0 means the floor is granted.
	//
	// Parameters:
	//   floorID - The floor identifier
	//   requestID - The request ID in the queue
	//   position - Current position in the queue (0 = granted, 1 = next in line, etc.)
	OnQueuePositionChanged(floorID uint16, requestID uint16, position uint8)

	// OnFloorStatusChanged is called when the status of a floor request changes.
	// This provides detailed information about the request lifecycle.
	//
	// Parameters:
	//   floorID - The floor identifier
	//   requestID - The request ID
	//   status - The new status of the request
	OnFloorStatusChanged(floorID uint16, requestID uint16, status bfcp.RequestStatus)

	// Connection Events

	// OnConnected is called when the client successfully connects to the
	// BFCP server and completes the Hello handshake.
	OnConnected()

	// OnDisconnected is called when the connection to the BFCP server is lost.
	// The reason parameter contains the error that caused the disconnection,
	// or nil if it was a graceful shutdown.
	//
	// If automatic reconnection is enabled, the client will attempt to
	// reconnect and OnReconnecting will be called before each attempt.
	OnDisconnected(reason error)

	// OnReconnecting is called before each reconnection attempt.
	// The attempt parameter indicates the number of reconnection attempts
	// made so far (starting from 1).
	OnReconnecting(attempt int)

	// OnError is called when a non-fatal error occurs during client operation.
	// The client remains operational after these errors.
	OnError(err error)
}

// NoOpCallbacks provides a default implementation of the Callbacks interface
// that does nothing. Applications can embed this type and override only the
// callbacks they need.
//
// Example:
//
//	type MyCallbacks struct {
//	    virtual.NoOpCallbacks
//	}
//
//	func (c *MyCallbacks) OnFloorGranted(floorID, requestID uint16) {
//	    fmt.Printf("Floor %d granted (request %d)\n", floorID, requestID)
//	}
type NoOpCallbacks struct{}

// Ensure NoOpCallbacks implements Callbacks interface
var _ Callbacks = (*NoOpCallbacks)(nil)

func (NoOpCallbacks) OnFloorGranted(floorID uint16, requestID uint16)                             {}
func (NoOpCallbacks) OnFloorDenied(floorID uint16, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string) {
}
func (NoOpCallbacks) OnFloorRevoked(floorID uint16, requestID uint16)                     {}
func (NoOpCallbacks) OnFloorReleased(floorID uint16, requestID uint16)                    {}
func (NoOpCallbacks) OnQueuePositionChanged(floorID uint16, requestID uint16, position uint8) {}
func (NoOpCallbacks) OnFloorStatusChanged(floorID uint16, requestID uint16, status bfcp.RequestStatus) {
}
func (NoOpCallbacks) OnConnected()               {}
func (NoOpCallbacks) OnDisconnected(reason error) {}
func (NoOpCallbacks) OnReconnecting(attempt int)  {}
func (NoOpCallbacks) OnError(err error)           {}
