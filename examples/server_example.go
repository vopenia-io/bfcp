package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/vopenia-io/bfcp"
)

func main() {
	config := bfcp.DefaultServerConfig(":5070", 1)
	config.AutoGrant = true
	config.MaxFloors = 5

	// Create the server
	server := bfcp.NewServer(config)

	// Create floors for the conference
	// Floor 1: Slides/Content sharing
	server.CreateFloor(1)
	// Floor 2: Additional content stream
	server.CreateFloor(2)

	// Set up event callbacks
	server.OnClientConnect = func(remoteAddr string, userID uint16) {
		log.Printf("Client connected: %s (UserID: %d)", remoteAddr, userID)
	}

	server.OnClientDisconnect = func(remoteAddr string, userID uint16) {
		log.Printf("Client disconnected: %s (UserID: %d)", remoteAddr, userID)
	}

	server.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		log.Printf("Floor request received: FloorID=%d, UserID=%d, RequestID=%d", floorID, userID, requestID)

		// Custom logic: auto-grant for floor 1, manual grant for floor 2
		if floorID == 1 {
			log.Printf("Auto-granting floor %d to user %d", floorID, userID)
			return true
		}

		// For floor 2, check if it's available
		floor, exists := server.GetFloor(floorID)
		if !exists || !floor.IsAvailable() {
			log.Printf("Floor %d is busy, denying request from user %d", floorID, userID)
			return false
		}
		log.Printf("Granting floor %d to user %d", floorID, userID)
		return true
	}

	server.OnFloorGranted = func(floorID, userID, requestID uint16) {
		log.Printf("Floor granted: FloorID=%d, UserID=%d, RequestID=%d", floorID, userID, requestID)
		log.Printf("→ SIP endpoint can now start sending slides video stream (a=floorid:%d)", floorID)
	}

	server.OnFloorReleased = func(floorID, userID uint16) {
		log.Printf("Floor released: FloorID=%d, UserID=%d", floorID, userID)
		log.Printf("→ SIP endpoint should stop sending slides video stream")
	}

	server.OnFloorDenied = func(floorID, userID, requestID uint16) {
		log.Printf("Floor denied: FloorID=%d, UserID=%d, RequestID=%d", floorID, userID, requestID)
	}

	server.OnError = func(err error) {
		log.Printf("Server error: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down server...")
		if err := server.Close(); err != nil {
			log.Printf("Error closing server: %v", err)
		}
		os.Exit(0)
	}()

	// Start the server (blocking)
	log.Printf("Starting BFCP server on %s (Conference ID: %d)", config.Address, config.ConferenceID)
	log.Println("Press Ctrl+C to stop")
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
