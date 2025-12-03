package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vopenia-io/bfcp"
	"github.com/vopenia-io/bfcp/virtual"
)

// MyCallbacks implements the virtual.Callbacks interface to handle
// BFCP events for our virtual client.
type MyCallbacks struct {
	clientID string
}

func (c *MyCallbacks) OnFloorGranted(floorID uint16, requestID uint16) {
	log.Printf("[%s] ✓ Floor %d GRANTED (request %d) - Starting media transmission",
		c.clientID, floorID, requestID)
	// TODO: Start transmitting media for this floor
	// In a real application, this would trigger:
	// - Start GStreamer pipeline for screen share
	// - Enable video track transmission in WebRTC
	// - Update UI to show "presenting" status
}

func (c *MyCallbacks) OnFloorDenied(floorID uint16, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string) {
	log.Printf("[%s] ✗ Floor %d DENIED (request %d) - %s: %s",
		c.clientID, floorID, requestID, errorCode, errorInfo)
	// TODO: Notify user that floor request was denied
}

func (c *MyCallbacks) OnFloorRevoked(floorID uint16, requestID uint16) {
	log.Printf("[%s] ⚠ Floor %d REVOKED (request %d) - Stopping media transmission",
		c.clientID, floorID, requestID)
	// TODO: Immediately stop transmitting media
	// This is a unilateral action by the server/chair
}

func (c *MyCallbacks) OnFloorReleased(floorID uint16, requestID uint16) {
	log.Printf("[%s] ✓ Floor %d RELEASED (request %d)",
		c.clientID, floorID, requestID)
	// TODO: Clean up resources for this floor
}

func (c *MyCallbacks) OnQueuePositionChanged(floorID uint16, requestID uint16, position uint8) {
	if position == 0 {
		log.Printf("[%s] Floor %d: Position = GRANTED (request %d)",
			c.clientID, floorID, requestID)
	} else {
		log.Printf("[%s] Floor %d: Queue position = %d (request %d)",
			c.clientID, floorID, position, requestID)
	}
}

func (c *MyCallbacks) OnFloorStatusChanged(floorID uint16, requestID uint16, status bfcp.RequestStatus) {
	log.Printf("[%s] Floor %d: Status changed to %s (request %d)",
		c.clientID, floorID, status, requestID)
}

func (c *MyCallbacks) OnConnected() {
	log.Printf("[%s] ✓ Connected to BFCP server", c.clientID)
}

func (c *MyCallbacks) OnDisconnected(reason error) {
	if reason != nil {
		log.Printf("[%s] ✗ Disconnected from BFCP server: %v", c.clientID, reason)
	} else {
		log.Printf("[%s] Disconnected from BFCP server (graceful)", c.clientID)
	}
}

func (c *MyCallbacks) OnReconnecting(attempt int) {
	log.Printf("[%s] Reconnecting to BFCP server (attempt %d)", c.clientID, attempt)
}

func (c *MyCallbacks) OnError(err error) {
	log.Printf("[%s] Error: %v", c.clientID, err)
}

func main() {
	log.Println("=== Virtual BFCP Client Example ===")
	log.Println()

	// Configuration
	serverAddr := "localhost:5070"
	conferenceID := uint32(1)

	// Create a manager for the conference
	config := virtual.DefaultConfig()
	config.UserIDRangeStart = 100
	config.UserIDRangeEnd = 200
	config.RequestTimeout = 30 * time.Second

	manager := virtual.NewManager(serverAddr, conferenceID, config)
	log.Printf("Created manager for conference %d at %s", conferenceID, serverAddr)
	log.Printf("User ID range: %d-%d (%d available)",
		config.UserIDRangeStart, config.UserIDRangeEnd, manager.AvailableUserIDs())
	log.Println()

	// Example 1: Single virtual client
	log.Println("--- Example 1: Single Virtual Client ---")

	// Create a virtual client for a WebRTC participant
	client1, err := manager.CreateClient("webrtc-user-alice", &MyCallbacks{clientID: "alice"})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	log.Printf("Created virtual client: %s (user ID: %d)", client1.ID(), client1.UserID())

	// Connect to the BFCP server
	if err := client1.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Request floor control for screen share (floor 1)
	log.Println("\nRequesting floor control for screen share...")
	requestID, err := client1.RequestFloor(1,
		virtual.WithPriority(bfcp.PriorityNormal),
		virtual.WithInformation("Alice's Screen Share"))
	if err != nil {
		log.Fatalf("Failed to request floor: %v", err)
	}
	log.Printf("Floor request sent (request ID: %d)", requestID)

	// Wait for floor to be granted
	time.Sleep(2 * time.Second)

	// Check status
	status := client1.GetStatus()
	log.Printf("\nClient status:")
	log.Printf("  Connected: %v", status.Connected)
	log.Printf("  Active floors: %v", status.ActiveFloors)
	log.Printf("  Pending requests: %v", status.PendingRequests)

	// Hold the floor for a bit (simulating presentation)
	log.Println("\nHolding floor for 3 seconds...")
	time.Sleep(3 * time.Second)

	// Release the floor
	log.Println("\nReleasing floor...")
	if err := client1.ReleaseFloor(requestID); err != nil {
		log.Printf("Error releasing floor: %v", err)
	}
	time.Sleep(1 * time.Second)

	log.Println()

	// Example 2: Multiple concurrent clients
	log.Println("--- Example 2: Multiple Concurrent Clients ---")

	// Create second client
	client2, err := manager.CreateClient("webrtc-user-bob", &MyCallbacks{clientID: "bob"})
	if err != nil {
		log.Fatalf("Failed to create client 2: %v", err)
	}
	log.Printf("Created virtual client: %s (user ID: %d)", client2.ID(), client2.UserID())

	// Connect second client
	if err := client2.Connect(); err != nil {
		log.Fatalf("Failed to connect client 2: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Both clients request the same floor (will queue)
	log.Println("\nBoth clients requesting the same floor...")
	req1, _ := client1.RequestFloor(1, virtual.WithInformation("Alice's second request"))
	req2, _ := client2.RequestFloor(1, virtual.WithInformation("Bob's request"))
	log.Printf("Alice request ID: %d", req1)
	log.Printf("Bob request ID: %d", req2)

	// Wait to see queue behavior
	time.Sleep(3 * time.Second)

	// List all clients
	log.Println("\nAll managed clients:")
	for _, info := range manager.ListClients() {
		log.Printf("  - %s (user ID: %d, connected: %v, floors: %v)",
			info.ID, info.UserID, info.Connected, info.ActiveFloors)
	}
	log.Printf("Total clients: %d", manager.Count())
	log.Printf("Available user IDs: %d", manager.AvailableUserIDs())

	// Release floors
	log.Println("\nReleasing all floors...")
	client1.ReleaseAllFloors()
	client2.ReleaseAllFloors()
	time.Sleep(1 * time.Second)

	// Example 3: Cleanup
	log.Println("\n--- Example 3: Graceful Shutdown ---")

	// Remove specific client
	log.Println("Removing client 1...")
	if err := manager.RemoveClient(client1.ID()); err != nil {
		log.Printf("Error removing client 1: %v", err)
	}
	log.Printf("Remaining clients: %d", manager.Count())

	// Wait for Ctrl+C to exit
	log.Println("\n--- Demo Running ---")
	log.Println("Press Ctrl+C to exit and clean up remaining clients...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\n\nShutting down...")

	// Clean up all remaining clients
	if err := manager.RemoveAll(); err != nil {
		log.Printf("Error during cleanup: %v", err)
	}

	log.Println("All clients cleaned up. Goodbye!")
}
