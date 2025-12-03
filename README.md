# BFCP - Binary Floor Control Protocol (RFC 8855/8856)

A pure Go implementation of the Binary Floor Control Protocol (BFCP) enabling SIP video systems (Poly, Cisco, Yealink) to manage content-sharing "slides" streams, featuring full client/server roles, TCP transport, and auto-grant floor control for seamless dual-video conferencing integration.

## Overview

BFCP is a protocol for coordinating access to shared resources ("floors") in a conference. In the context of video conferencing, it's primarily used to manage secondary video streams for content sharing (presentation slides, screen sharing, etc.).

This library implements:
- **RFC 8855**: Binary Floor Control Protocol (BFCP)
- **RFC 8856**: Session Description Protocol (SDP) Parameters for BFCP

## Features

- Pure Go implementation (no CGO dependencies)
- Full client and server roles
- Single conference per server for simple deployment
- TCP transport with proper framing
- Complete message encoder/decoder with TLV (Type-Length-Value) support
- Thread-safe state machine for floor management
- Priority-based floor request queuing
- Auto-grant mode for simple conferencing scenarios
- Virtual client support for WebRTC participants
- Comprehensive test suite
- Production-ready examples

## Installation

```bash
go get github.com/vopenia-io/bfcp
```

## Quick Start

### Server Example

```go
package main

import (
    "log"
    "github.com/vopenia-io/bfcp"
)

func main() {
    // Create server configuration
    config := bfcp.DefaultServerConfig(":5070", 1)
    config.AutoGrant = true  // Auto-grant floor requests

    server := bfcp.NewServer(config)

    // Create floors for content sharing
    server.CreateFloor(1)  // Floor 1: Main slides stream

    // Set up event callbacks
    server.OnFloorGranted = func(floorID, userID, requestID uint16) {
        log.Printf("Floor %d granted to user %d", floorID, userID)
        // Activate the corresponding media stream in your SIP gateway
    }

    server.OnFloorReleased = func(floorID, userID uint16) {
        log.Printf("Floor %d released by user %d", floorID, userID)
        // Deactivate the media stream
    }

    // Start server (blocking)
    log.Fatal(server.ListenAndServe())
}
```

### Client Example

```go
package main

import (
    "log"
    "github.com/vopenia-io/bfcp"
)

func main() {
    // Create client configuration
    config := bfcp.DefaultClientConfig("localhost:5070", 1, 100)

    client := bfcp.NewClient(config)

    // Set up event callbacks
    client.OnFloorGranted = func(floorID, requestID uint16) {
        log.Printf("Floor %d granted! Start sending slides video", floorID)
        // Start sending RTP packets for the slides stream
    }

    client.OnFloorReleased = func(floorID uint16) {
        log.Printf("Floor %d released, stop sending slides", floorID)
        // Stop sending RTP packets
    }

    // Connect and negotiate
    if err := client.Connect(); err != nil {
        log.Fatal(err)
    }
    defer client.Disconnect()

    // Send Hello handshake
    if err := client.Hello(); err != nil {
        log.Fatal(err)
    }

    // Request floor control
    requestID, err := client.RequestFloor(1, 0, bfcp.PriorityNormal)
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Floor requested (ID: %d)", requestID)

    // ... do your content sharing ...

    // Release floor when done
    client.ReleaseFloor(1)
}
```

## Architecture

### Message Structure

BFCP messages follow the TLV (Type-Length-Value) format with a common 12-byte header:

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|Ver|  Resv |P|  Primitive |            Length                   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         ConferenceID          |        TransactionID          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|           UserID              |       (Start of Attributes)   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

### Supported Primitives

| Primitive | Direction | Purpose |
|-----------|-----------|---------|
| `Hello` / `HelloAck` | Both | Capability negotiation |
| `FloorRequest` | Client → Server | Request floor control |
| `FloorRelease` | Client → Server | Release floor |
| `FloorRequestStatus` | Server → Client | Status of floor request |
| `FloorStatus` | Server → Client | Floor state notification |
| `Error` | Both | Error reporting |
| `Goodbye` / `GoodbyeAck` | Both | Graceful disconnect |

### Floor State Machine

Server-side floor state transitions:

```
                   WAIT_HELLO_ACK
                         ↓ HelloAck
                   WAIT_FLOOR_REQUEST
                         ↓ FloorRequest
                   FLOOR_REQUESTED (pending)
                    ↙              ↘
         (auto-grant)              (deny)
              ↓                        ↓
       FLOOR_GRANTED              FLOOR_DENIED
              ↓ FloorRelease
       FLOOR_RELEASED
```

## Integration with SIP/WebRTC Gateway

### SDP Mapping

BFCP is negotiated in SDP (Session Description Protocol):

```sdp
m=application 5070 TCP/BFCP *
a=setup:active
a=connection:new
a=floorctrl:c-only
a=confid:1
a=userid:100
a=floorid:1 mstrm:3
```

- `a=setup:active` → Client connects (BFCP client role)
- `a=setup:passive` → Server listens (BFCP server role)
- `a=floorid:1 mstrm:3` → Floor ID 1 maps to media stream 3 (the slides video)

### Typical Flow

1. **SDP Negotiation**: SIP endpoints exchange SDP with BFCP parameters
2. **BFCP Connection**: Active endpoint connects to passive endpoint
3. **Hello Handshake**: Capability negotiation
4. **Floor Request**: Client requests floor for slides
5. **Floor Granted**: Server grants floor
6. **Media Activation**: Gateway activates the slides video m-line
7. **RTP Streaming**: Client sends slides video via RTP
8. **Floor Release**: Client releases floor when done
9. **Media Deactivation**: Gateway deactivates the slides m-line

## API Reference

### Server API

```go
// Create a server
config := bfcp.DefaultServerConfig(":5070", conferenceID)
server := bfcp.NewServer(config)

// Create/release floors
server.CreateFloor(floorID uint16)
server.ReleaseFloor(floorID uint16)

// Get floor state
floor, exists := server.GetFloor(floorID)

// Manual grant (when AutoGrant is false)
server.GrantFloor(floorID, userID)

// Start server
server.ListenAndServe()

// Event callbacks
server.OnFloorRequest = func(floorID, userID, requestID uint16) bool
server.OnFloorGranted = func(floorID, userID, requestID uint16)
server.OnFloorReleased = func(floorID, userID uint16)
server.OnFloorDenied = func(floorID, userID, requestID uint16)
server.OnClientConnect = func(remoteAddr string, userID uint16)
server.OnClientDisconnect = func(remoteAddr string, userID uint16)
```

### Client API

```go
// Create a client
config := bfcp.DefaultClientConfig(serverAddr, conferenceID, userID)
client := bfcp.NewClient(config)

// Connect and negotiate
client.Connect()
client.Hello()

// Request floor
requestID, err := client.RequestFloor(floorID, beneficiaryID, priority)

// Release floor
client.ReleaseFloor(floorID)

// Query floor status
response, err := client.QueryFloor(floorID)

// Disconnect
client.Disconnect()

// Event callbacks
client.OnFloorGranted = func(floorID, requestID uint16)
client.OnFloorDenied = func(floorID, requestID uint16, errorCode ErrorCode)
client.OnFloorReleased = func(floorID uint16)
client.OnFloorRevoked = func(floorID uint16)
```

## Running Examples

### Start the Server

```bash
cd examples
go run server_example.go
```

The server will listen on `:5070` and manage floor requests.

### Run the Client

```bash
cd examples
go run client_example.go
```

Interactive commands:
- `request <floorID>` - Request a floor
- `release <floorID>` - Release a floor
- `query <floorID>` - Query floor status
- `status` - Show active requests
- `quit` - Exit

## Testing

Run the comprehensive test suite:

```bash
go test -v
```

Run benchmarks:

```bash
go test -bench=. -benchmem
```

## Simple Floor Management

The server manages floors directly without conference isolation overhead.

### Key Features

- **Direct Floor Access**: Create, get, and release floors with simple API
- **Thread-Safe**: All operations are concurrent-safe using proper locking
- **Single Conference**: One server instance manages one conference

### Quick Example

```go
config := bfcp.DefaultServerConfig(":5070", 1)
server := bfcp.NewServer(config)

// Create floor
floor := server.CreateFloor(1)

// Get floor
floor, exists := server.GetFloor(1)

// Release floor
server.ReleaseFloor(1)
```

### Server API

- `CreateFloor(floorID uint16)` - Create new floor
- `GetFloor(floorID uint16)` - Get existing floor
- `ReleaseFloor(floorID uint16)` - Release floor
- `GrantFloor(floorID, userID uint16)` - Grant floor to user

## Use Cases

1. **SIP-to-WebRTC Gateway**: Enable Poly/Cisco endpoints to share slides in WebRTC conferences
2. **Conference Bridge**: Manage floor control in multi-party conferences
3. **Multi-Call SIP Server**: Handle multiple concurrent SIP calls with isolated floor control
4. **Lecture Systems**: Control presentation rights in educational settings
5. **Collaborative Tools**: Coordinate screen sharing in virtual meetings

## Roadmap

- [ ] TLS transport support (BFCP/TLS)
- [ ] UDP transport (for unreliable networks)
- [ ] Floor chair role implementation
- [ ] Multi-floor coordination
- [ ] Metrics and monitoring hooks
- [ ] OpenTelemetry integration

## Contributing

Contributions are welcome! Please ensure:
- All tests pass (`go test -v`)
- Code follows Go conventions (`go fmt`, `go vet`)
- New features include tests
- Public APIs are documented

## License

See [LICENSE](LICENSE) file.

## References

- [RFC 8855: The Binary Floor Control Protocol (BFCP)](https://www.rfc-editor.org/rfc/rfc8855.html)
- [RFC 8856: Session Description Protocol (SDP) Format for BFCP Streams](https://www.rfc-editor.org/rfc/rfc8856.html)
- [RFC 4582: The Binary Floor Control Protocol (BFCP) - Original](https://www.rfc-editor.org/rfc/rfc4582.html)

## Support

For issues, questions, or contributions, please open an issue on GitHub.
