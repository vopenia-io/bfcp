# Virtual BFCP Client

The `virtual` package provides a virtual BFCP client abstraction that allows non-BFCP systems (like WebRTC) to participate in BFCP floor control through a proxy pattern.

## Overview

This package extends the standard BFCP library to support scenarios where the actual media endpoints don't natively support the BFCP protocol. This is particularly useful in WebRTC-to-SIP gateway scenarios where WebRTC participants need to participate in BFCP-controlled conferences.

## Features

- **Virtual Client**: Represents a single virtual BFCP participant with automatic connection management
- **Manager**: Manages multiple virtual clients with automatic user ID allocation
- **Thread-Safe**: All operations are thread-safe and can be called concurrently
- **Event-Driven**: Comprehensive callback system for all BFCP events
- **Production-Ready**: Includes comprehensive tests and error handling

## Architecture

### Components

1. **Virtual Client** (`Client`): Represents a single virtual BFCP participant. Each client maintains its own connection to the BFCP server and manages floor requests on behalf of a non-BFCP endpoint.

2. **Manager** (`Manager`): Manages multiple virtual clients for a conference, handling user ID allocation and lifecycle management.

3. **Callbacks** (`Callbacks` interface): Event notification system for floor control events, status changes, and connection events.

## Installation

```bash
go get github.com/vopenia/bfcp/virtual
```

## Quick Start

### Basic Usage

```go
package main

import (
    "log"
    "github.com/vopenia/bfcp"
    "github.com/vopenia/bfcp/virtual"
)

// Implement callbacks
type MyCallbacks struct {
    virtual.NoOpCallbacks // Embed to get default implementations
}

func (c *MyCallbacks) OnFloorGranted(floorID, requestID uint16) {
    log.Printf("Floor %d granted! Start transmitting media.", floorID)
    // Start your media transmission here
}

func (c *MyCallbacks) OnFloorRevoked(floorID, requestID uint16) {
    log.Printf("Floor %d revoked! Stop transmitting media.", floorID)
    // Stop your media transmission here
}

func main() {
    // Create a virtual client
    client, err := virtual.New(
        "webrtc-user-123",           // Unique client ID
        "localhost:5070",             // BFCP server address
        1,                            // Conference ID
        100,                          // User ID
        &MyCallbacks{},               // Event callbacks
        30*time.Second,               // Request timeout
    )
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // Connect to BFCP server
    if err := client.Connect(); err != nil {
        log.Fatal(err)
    }

    // Request floor control
    requestID, err := client.RequestFloor(1,
        virtual.WithPriority(bfcp.PriorityNormal),
        virtual.WithInformation("Screen Share"))
    if err != nil {
        log.Fatal(err)
    }

    // ... wait for OnFloorGranted callback ...

    // Release floor when done
    client.ReleaseFloor(requestID)
}
```

### Using the Manager

For managing multiple virtual clients in a conference:

```go
// Create manager
config := virtual.DefaultConfig()
config.UserIDRangeStart = 100
config.UserIDRangeEnd = 200

manager := virtual.NewManager("localhost:5070", 1, config)
defer manager.RemoveAll()

// Create virtual clients as WebRTC participants join
client1, _ := manager.CreateClient("webrtc-user-alice", &MyCallbacks{})
client1.Connect()

client2, _ := manager.CreateClient("webrtc-user-bob", &MyCallbacks{})
client2.Connect()

// List all clients
for _, info := range manager.ListClients() {
    log.Printf("Client: %s (UserID: %d, Connected: %v)",
        info.ID, info.UserID, info.Connected)
}

// Remove client when participant leaves
manager.RemoveClient("webrtc-user-alice")
```

## API Reference

### Client

#### Creating a Client

```go
func New(
    id string,                  // Unique identifier for this client
    serverAddr string,          // BFCP server address (e.g., "localhost:5070")
    conferenceID uint32,        // Conference ID to join
    userID uint16,              // BFCP User ID
    callbacks Callbacks,        // Event callbacks
    timeout time.Duration,      // Timeout for floor requests (0 = 30s default)
) (*Client, error)
```

#### Client Methods

```go
// Connection management
func (c *Client) Connect() error
func (c *Client) Close() error
func (c *Client) IsConnected() bool

// Floor operations
func (c *Client) RequestFloor(floorID uint16, opts ...RequestOption) (uint16, error)
func (c *Client) ReleaseFloor(requestID uint16) error
func (c *Client) ReleaseAllFloors() error

// Status and information
func (c *Client) GetStatus() *ClientStatus
func (c *Client) ID() string
func (c *Client) UserID() uint16
func (c *Client) ConferenceID() uint32
```

#### Request Options

```go
// Set priority for floor request
virtual.WithPriority(bfcp.PriorityHigh)

// Add participant information
virtual.WithInformation("Alice's Screen Share")

// Set beneficiary user ID
virtual.WithBeneficiary(200)
```

### Manager

#### Creating a Manager

```go
func NewManager(
    serverAddr string,      // BFCP server address
    conferenceID uint32,    // Conference ID for all clients
    config Config,          // Configuration options
) *Manager
```

#### Manager Methods

```go
// Client management
func (m *Manager) CreateClient(id string, callbacks Callbacks) (*Client, error)
func (m *Manager) CreateClientWithUserID(id string, userID uint16, callbacks Callbacks) (*Client, error)
func (m *Manager) GetClient(id string) (*Client, bool)
func (m *Manager) RemoveClient(id string) error
func (m *Manager) RemoveAll() error

// Information
func (m *Manager) ListClients() []*ClientInfo
func (m *Manager) Count() int
func (m *Manager) AvailableUserIDs() int
func (m *Manager) ServerAddr() string
func (m *Manager) ConferenceID() uint32
```

#### Manager Configuration

```go
type Config struct {
    UserIDRangeStart uint16        // Start of virtual user ID range (default: 100)
    UserIDRangeEnd   uint16        // End of virtual user ID range (default: 1000)
    ReconnectDelay   time.Duration // Delay between reconnection attempts (default: 5s)
    RequestTimeout   time.Duration // Timeout for floor requests (default: 30s)
    EnableMetrics    bool          // Enable Prometheus metrics (default: false, not yet implemented)
}

// Get default configuration
config := virtual.DefaultConfig()
```

### Callbacks

Implement the `Callbacks` interface to receive events:

```go
type Callbacks interface {
    // Floor Control Events
    OnFloorGranted(floorID uint16, requestID uint16)
    OnFloorDenied(floorID uint16, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string)
    OnFloorRevoked(floorID uint16, requestID uint16)
    OnFloorReleased(floorID uint16, requestID uint16)

    // Status Events
    OnQueuePositionChanged(floorID uint16, requestID uint16, position uint8)
    OnFloorStatusChanged(floorID uint16, requestID uint16, status bfcp.RequestStatus)

    // Connection Events
    OnConnected()
    OnDisconnected(reason error)
    OnReconnecting(attempt int)
    OnError(err error)
}
```

**Tip**: Embed `NoOpCallbacks` and override only the methods you need:

```go
type MyCallbacks struct {
    virtual.NoOpCallbacks
}

func (c *MyCallbacks) OnFloorGranted(floorID, requestID uint16) {
    // Your implementation
}
```

## Error Handling

The package defines several error types:

```go
var (
    ErrClientNotConnected  = errors.New("virtual client not connected")
    ErrFloorNotHeld        = errors.New("floor not held by this client")
    ErrRequestTimeout      = errors.New("floor request timed out")
    ErrClientAlreadyExists = errors.New("client with this ID already exists")
    ErrClientClosed        = errors.New("client is closed")
    ErrInvalidFloorID      = errors.New("invalid floor ID")
    ErrInvalidRequestID    = errors.New("invalid request ID")
)
```

## Use Cases

### WebRTC-to-SIP Gateway

```go
// When a WebRTC participant joins and wants to share screen
func onWebRTCParticipantJoined(participantID string, sipConferenceID uint32) {
    client, err := manager.CreateClient(participantID, &WebRTCCallbacks{
        participantID: participantID,
    })
    if err != nil {
        return err
    }
    return client.Connect()
}

// When participant requests to share screen
func onScreenShareRequested(participantID string) {
    client, exists := manager.GetClient(participantID)
    if !exists {
        return errors.New("client not found")
    }

    requestID, err := client.RequestFloor(1,
        virtual.WithInformation("Screen Share from " + participantID))
    // Store requestID for later release
    return err
}

// When participant stops sharing
func onScreenShareStopped(participantID string, requestID uint16) {
    client, exists := manager.GetClient(participantID)
    if !exists {
        return
    }
    client.ReleaseFloor(requestID)
}

// When participant leaves
func onWebRTCParticipantLeft(participantID string) {
    manager.RemoveClient(participantID)
}
```

### Conference Recording System

```go
// Create a virtual client for the recording system
recorder, _ := virtual.New("conference-recorder",
    serverAddr, conferenceID, 999,
    &RecorderCallbacks{}, 30*time.Second)
recorder.Connect()

// Request floor to indicate active recording
requestID, _ := recorder.RequestFloor(2,
    virtual.WithInformation("Conference Recording Active"))

// When recording stops
recorder.ReleaseFloor(requestID)
recorder.Close()
```

## Testing

### Running Unit Tests

```bash
cd virtual
go test -v
```

### Running Integration Tests

Integration tests require a BFCP server running on `localhost:5070`:

```bash
# Start BFCP server first
go test -v  # Integration tests will auto-detect server

# Or skip integration tests
go test -v -short
```

### Test Coverage

```bash
go test -cover
```

## Thread Safety

All public methods are thread-safe and can be called concurrently:

- Client state is protected by read-write mutexes
- Manager operations are synchronized
- User ID allocation is atomic
- Multiple clients can be created/removed concurrently

## Performance Considerations

1. **User ID Pool**: The manager allocates user IDs sequentially. For large deployments, configure an appropriate range.

2. **Connection Pooling**: Each virtual client maintains its own TCP connection to the BFCP server.

3. **Callback Latency**: Callbacks are invoked from the message handler goroutine. Avoid blocking operations in callbacks.

4. **Resource Cleanup**: Always call `Close()` or `RemoveClient()` to properly clean up resources.

## Limitations

1. **Reconnection**: Automatic reconnection is not yet implemented. If the connection is lost, you must manually reconnect.

2. **Metrics**: Prometheus metrics support is not yet implemented (EnableMetrics flag is reserved for future use).

3. **Multiple Floors**: While the client supports requesting multiple floors, most BFCP servers limit clients to one floor at a time.

## Examples

See [examples/virtual_client/main.go](../examples/virtual_client/main.go) for a complete working example demonstrating:
- Creating and managing multiple virtual clients
- Floor request and release operations
- Status monitoring
- Graceful shutdown

## Contributing

Contributions are welcome! Please ensure:
- All tests pass: `go test ./...`
- Code is properly formatted: `go fmt ./...`
- New features include tests
- Documentation is updated

## License

See the main BFCP library LICENSE file.

## Support

For issues, questions, or contributions, please visit:
https://github.com/vopenia/bfcp
