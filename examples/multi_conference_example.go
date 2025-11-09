package main

import (
	"fmt"
	"log"
	"time"

	"github.com/vopenia/bfcp"
)

// This example demonstrates how to use the BFCP server with multiple conferences
// Each conference represents a separate SIP call with isolated floor control

func main() {
	// Create BFCP server (single server handles multiple conferences)
	config := bfcp.DefaultServerConfig(":5070", 1) // Default conferenceID is not used with ConferenceManager
	config.AutoGrant = true
	config.MaxFloors = 10
	config.EnableLogging = true

	server := bfcp.NewServer(config)

	// Get the conference manager
	cm := server.GetConferenceManager()

	// Start server in background
	go func() {
		if err := server.ListenAndServe(); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	// Simulate two concurrent SIP calls
	fmt.Println("\n=== Simulating two concurrent SIP calls ===\n")

	// Call 1: Conference for first SIP call
	conference1ID := uint32(100)
	sipUserID1 := uint16(10)

	conf1, err := cm.CreateConference(conference1ID, sipUserID1)
	if err != nil {
		log.Fatalf("Failed to create conference 1: %v", err)
	}
	fmt.Printf("✅ Created Conference 1 (ID: %d, SIP UserID: %d)\n", conference1ID, sipUserID1)

	// Call 2: Conference for second SIP call
	conference2ID := uint32(200)
	sipUserID2 := uint16(20)

	conf2, err := cm.CreateConference(conference2ID, sipUserID2)
	if err != nil {
		log.Fatalf("Failed to create conference 2: %v", err)
	}
	fmt.Printf("✅ Created Conference 2 (ID: %d, SIP UserID: %d)\n", conference2ID, sipUserID2)

	// Allocate floors for each conference
	fmt.Println("\n=== Allocating floors ===\n")

	// Conference 1: Allocate floor for WebRTC→SIP screen share
	floor1, err := cm.AllocateFloor(conference1ID)
	if err != nil {
		log.Fatalf("Failed to allocate floor for conference 1: %v", err)
	}
	fmt.Printf("📋 Conference 1 allocated Floor %d (globally unique)\n", floor1)

	// Conference 2: Allocate floor for WebRTC→SIP screen share
	floor2, err := cm.AllocateFloor(conference2ID)
	if err != nil {
		log.Fatalf("Failed to allocate floor for conference 2: %v", err)
	}
	fmt.Printf("📋 Conference 2 allocated Floor %d (globally unique)\n", floor2)

	// Verify floors are different (no collision)
	if floor1 == floor2 {
		log.Fatalf("❌ COLLISION: Both conferences got the same floor ID!")
	}
	fmt.Printf("✅ Verified: Floors are unique (no collision)\n")

	// Allocate virtual client UserIDs for WebRTC participants
	fmt.Println("\n=== Allocating virtual client UserIDs ===\n")

	virtualUserID1, err := cm.AllocateVirtualUserID(conference1ID)
	if err != nil {
		log.Fatalf("Failed to allocate virtual userID for conference 1: %v", err)
	}
	fmt.Printf("👤 Conference 1: Virtual client UserID %d (for WebRTC participant)\n", virtualUserID1)

	virtualUserID2, err := cm.AllocateVirtualUserID(conference2ID)
	if err != nil {
		log.Fatalf("Failed to allocate virtual userID for conference 2: %v", err)
	}
	fmt.Printf("👤 Conference 2: Virtual client UserID %d (for WebRTC participant)\n", virtualUserID2)

	// Register virtual clients
	fmt.Println("\n=== Registering virtual clients ===\n")

	err = cm.RegisterVirtualClient(conference1ID, virtualUserID1, "webrtc_participant_1")
	if err != nil {
		log.Fatalf("Failed to register virtual client 1: %v", err)
	}
	fmt.Printf("✅ Registered virtual client 1: UserID %d, Identity: 'webrtc_participant_1'\n", virtualUserID1)

	err = cm.RegisterVirtualClient(conference2ID, virtualUserID2, "webrtc_participant_2")
	if err != nil {
		log.Fatalf("Failed to register virtual client 2: %v", err)
	}
	fmt.Printf("✅ Registered virtual client 2: UserID %d, Identity: 'webrtc_participant_2'\n", virtualUserID2)

	// Inject floor requests (simulates WebRTC screen share starting)
	fmt.Println("\n=== Injecting floor requests ===\n")

	// Note: InjectFloorRequest will queue the request if BFCP client not connected yet
	requestID1, err := cm.InjectFloorRequest(conference1ID, floor1, virtualUserID1)
	if err != nil {
		log.Printf("Floor request for conference 1 queued (client not connected): %v", err)
	} else {
		fmt.Printf("📤 Conference 1: Floor request injected (FloorID: %d, UserID: %d, RequestID: %d)\n",
			floor1, virtualUserID1, requestID1)
	}

	requestID2, err := cm.InjectFloorRequest(conference2ID, floor2, virtualUserID2)
	if err != nil {
		log.Printf("Floor request for conference 2 queued (client not connected): %v", err)
	} else {
		fmt.Printf("📤 Conference 2: Floor request injected (FloorID: %d, UserID: %d, RequestID: %d)\n",
			floor2, virtualUserID2, requestID2)
	}

	// Verify conference isolation
	fmt.Println("\n=== Verifying conference isolation ===\n")

	// Check conference 1 state
	c1, exists := cm.GetConference(conference1ID)
	if !exists {
		log.Fatalf("Conference 1 disappeared!")
	}

	c1.mu.RLock()
	fmt.Printf("Conference 1: %d floors, %d sessions, %d virtual clients\n",
		len(c1.floors), len(c1.sessions), len(c1.virtualClients))
	c1.mu.RUnlock()

	// Check conference 2 state
	c2, exists := cm.GetConference(conference2ID)
	if !exists {
		log.Fatalf("Conference 2 disappeared!")
	}

	c2.mu.RLock()
	fmt.Printf("Conference 2: %d floors, %d sessions, %d virtual clients\n",
		len(c2.floors), len(c2.sessions), len(c2.virtualClients))
	c2.mu.RUnlock()

	// Wait a bit to see logs
	time.Sleep(2 * time.Second)

	// Clean up conferences (simulates SIP calls ending)
	fmt.Println("\n=== Cleaning up conferences ===\n")

	err = cm.DeleteConference(conference1ID)
	if err != nil {
		log.Fatalf("Failed to delete conference 1: %v", err)
	}
	fmt.Printf("🗑️  Deleted Conference 1 (ID: %d)\n", conference1ID)

	err = cm.DeleteConference(conference2ID)
	if err != nil {
		log.Fatalf("Failed to delete conference 2: %v", err)
	}
	fmt.Printf("🗑️  Deleted Conference 2 (ID: %d)\n", conference2ID)

	// Verify cleanup
	_, exists = cm.GetConference(conference1ID)
	if exists {
		log.Fatalf("Conference 1 still exists after deletion!")
	}
	fmt.Printf("✅ Verified: Conference 1 successfully deleted\n")

	_, exists = cm.GetConference(conference2ID)
	if exists {
		log.Fatalf("Conference 2 still exists after deletion!")
	}
	fmt.Printf("✅ Verified: Conference 2 successfully deleted\n")

	// Close server
	server.Close()
	fmt.Println("\n=== Server closed ===")
}
