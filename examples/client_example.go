package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vopenia/bfcp"
)

func main() {
	// Create client configuration
	// In a real scenario, these would come from SDP negotiation
	serverAddr := "localhost:5070"
	conferenceID := uint32(1)
	userID := uint16(100)

	config := bfcp.DefaultClientConfig(serverAddr, conferenceID, userID)
	config.EnableLogging = true

	// Create the client
	client := bfcp.NewClient(config)

	// Set up event callbacks
	client.OnConnected = func() {
		log.Println("Connected to BFCP server")
	}

	client.OnDisconnected = func() {
		log.Println("Disconnected from BFCP server")
	}

	client.OnFloorGranted = func(floorID, requestID uint16) {
		log.Printf("Floor GRANTED: FloorID=%d, RequestID=%d", floorID, requestID)
		log.Printf("→ Now you can start sending slides video stream (a=content:slides)")
		log.Println("→ In SIP/WebRTC gateway: activate m-line with a=floorid:", floorID)
	}

	client.OnFloorDenied = func(floorID, requestID uint16, errorCode bfcp.ErrorCode) {
		log.Printf("Floor DENIED: FloorID=%d, RequestID=%d, Error=%s", floorID, requestID, errorCode)
	}

	client.OnFloorReleased = func(floorID uint16) {
		log.Printf("Floor RELEASED: FloorID=%d", floorID)
		log.Println("→ Stop sending slides video stream")
	}

	client.OnFloorRevoked = func(floorID uint16) {
		log.Printf("Floor REVOKED: FloorID=%d", floorID)
		log.Println("→ Server revoked your floor control, stop sending immediately")
	}

	client.OnFloorStatus = func(floorID uint16, status bfcp.RequestStatus) {
		log.Printf("Floor status update: FloorID=%d, Status=%s", floorID, status)
	}

	client.OnError = func(err error) {
		log.Printf("Client error: %v", err)
	}

	// Connect to server
	log.Printf("Connecting to BFCP server at %s (Conference ID: %d, User ID: %d)", serverAddr, conferenceID, userID)
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	// Wait a bit for connection to establish
	time.Sleep(100 * time.Millisecond)

	// Send Hello handshake
	log.Println("Sending Hello...")
	if err := client.Hello(); err != nil {
		log.Fatalf("Hello failed: %v", err)
	}

	log.Println("\nBFCP Client ready!")
	log.Println("Commands:")
	log.Println("  request <floorID>     - Request a floor")
	log.Println("  release <floorID>     - Release a floor")
	log.Println("  query <floorID>       - Query floor status")
	log.Println("  status                - Show active requests")
	log.Println("  quit                  - Disconnect and exit")
	log.Println()

	// Interactive command loop
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]

		switch cmd {
		case "request":
			if len(parts) < 2 {
				log.Println("Usage: request <floorID>")
				continue
			}
			floorID, err := strconv.ParseUint(parts[1], 10, 16)
			if err != nil {
				log.Printf("Invalid floor ID: %v", err)
				continue
			}

			log.Printf("Requesting floor %d...", floorID)
			requestID, err := client.RequestFloor(uint16(floorID), 0, bfcp.PriorityNormal)
			if err != nil {
				log.Printf("Failed to request floor: %v", err)
			} else {
				log.Printf("Floor request submitted (RequestID: %d)", requestID)
			}

		case "release":
			if len(parts) < 2 {
				log.Println("Usage: release <floorID>")
				continue
			}
			floorID, err := strconv.ParseUint(parts[1], 10, 16)
			if err != nil {
				log.Printf("Invalid floor ID: %v", err)
				continue
			}

			log.Printf("Releasing floor %d...", floorID)
			if err := client.ReleaseFloor(uint16(floorID)); err != nil {
				log.Printf("Failed to release floor: %v", err)
			} else {
				log.Printf("Floor %d released", floorID)
			}

		case "query":
			if len(parts) < 2 {
				log.Println("Usage: query <floorID>")
				continue
			}
			floorID, err := strconv.ParseUint(parts[1], 10, 16)
			if err != nil {
				log.Printf("Invalid floor ID: %v", err)
				continue
			}

			log.Printf("Querying floor %d...", floorID)
			response, err := client.QueryFloor(uint16(floorID))
			if err != nil {
				log.Printf("Failed to query floor: %v", err)
			} else {
				status, _, _ := response.GetRequestStatus()
				reqID, _ := response.GetFloorRequestID()
				log.Printf("Floor %d: Status=%s, RequestID=%d", floorID, status, reqID)
			}

		case "status":
			activeReqs := client.GetActiveRequests()
			if len(activeReqs) == 0 {
				log.Println("No active floor requests")
			} else {
				log.Println("Active floor requests:")
				for floorID, req := range activeReqs {
					grantedInfo := ""
					if !req.GrantedAt.IsZero() {
						grantedInfo = fmt.Sprintf(", Granted at: %s", req.GrantedAt.Format("15:04:05"))
					}
					log.Printf("  Floor %d: RequestID=%d, Status=%s, Priority=%d%s",
						floorID, req.FloorRequestID, req.Status, req.Priority, grantedInfo)
				}
			}

		case "quit", "exit":
			log.Println("Disconnecting...")
			client.Disconnect()
			log.Println("Goodbye!")
			return

		default:
			log.Printf("Unknown command: %s", cmd)
			log.Println("Available commands: request, release, query, status, quit")
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Scanner error: %v", err)
	}
}
