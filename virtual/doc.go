// Package virtual provides a virtual BFCP client abstraction that allows
// non-BFCP systems (like WebRTC) to participate in BFCP floor control through
// a proxy pattern.
//
// # Overview
//
// The virtual package extends the standard BFCP library to support scenarios where
// the actual media endpoints don't natively support BFCP protocol. This is common
// in WebRTC-to-SIP gateway scenarios where WebRTC participants need to participate
// in BFCP-controlled conferences.
//
// # Architecture
//
// The package provides two main components:
//
//  1. Virtual Client: Represents a single virtual BFCP participant. Each client
//     maintains its own connection to the BFCP server and manages floor requests
//     on behalf of a non-BFCP endpoint.
//
//  2. Manager: Manages multiple virtual clients for a conference, handling user ID
//     allocation and lifecycle management.
//
// # Example Usage
//
//	// Create a manager for a conference
//	manager := virtual.NewManager("localhost:5070", 1, virtual.Config{
//	    UserIDRangeStart: 100,
//	    UserIDRangeEnd:   200,
//	})
//
//	// Create a virtual client for a WebRTC participant
//	client, err := manager.CreateClient("webrtc-user-123", &MyCallbacks{})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Request floor control
//	requestID, err := client.RequestFloor(1,
//	    virtual.WithPriority(bfcp.PriorityNormal),
//	    virtual.WithInformation("WebRTC Screen Share"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Later: release the floor
//	if err := client.ReleaseFloor(requestID); err != nil {
//	    log.Fatal(err)
//	}
//
// # Thread Safety
//
// All public methods in this package are thread-safe and can be called
// concurrently from multiple goroutines. Internal state is protected by
// read-write mutexes.
//
// # Connection Management
//
// Virtual clients automatically handle connection lifecycle:
//   - Initial connection and BFCP handshake
//   - Automatic reconnection on connection failures (optional)
//   - Graceful shutdown with proper cleanup
//
// # Callbacks
//
// Clients communicate events through the Callbacks interface, allowing
// the application to respond to floor control events, status changes,
// and connection events.
package virtual
