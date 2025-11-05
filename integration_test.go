package bfcp

import (
	"sync"
	"testing"
	"time"
)

// TestClientServerIntegration tests the full client-server interaction
func TestClientServerIntegration(t *testing.T) {
	// Create server
	serverConfig := DefaultServerConfig("127.0.0.1:0", 1)
	serverConfig.AutoGrant = true
	server := NewServer(serverConfig)
	server.AddFloor(1)

	// Track events
	var (
		serverGranted   bool
		serverReleased  bool
		clientGranted   bool
		clientReleased  bool
		mu              sync.Mutex
	)

	server.OnFloorGranted = func(floorID, userID, requestID uint16) {
		mu.Lock()
		defer mu.Unlock()
		serverGranted = true
		t.Logf("Server: Floor %d granted to user %d (request %d)", floorID, userID, requestID)
	}

	server.OnFloorReleased = func(floorID, userID uint16) {
		mu.Lock()
		defer mu.Unlock()
		serverReleased = true
		t.Logf("Server: Floor %d released by user %d", floorID, userID)
	}

	// Start server
	listener, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	server.listener = listener

	listener.OnConnection = server.handleConnection
	listener.Start()
	defer listener.Close()

	// Get the actual listening address
	serverAddr := listener.Addr().String()
	t.Logf("Server listening on %s", serverAddr)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Create client
	clientConfig := DefaultClientConfig(serverAddr, 1, 100)
	client := NewClient(clientConfig)

	client.OnFloorGranted = func(floorID, requestID uint16) {
		mu.Lock()
		defer mu.Unlock()
		clientGranted = true
		t.Logf("Client: Floor %d granted (request %d)", floorID, requestID)
	}

	client.OnFloorReleased = func(floorID uint16) {
		mu.Lock()
		defer mu.Unlock()
		clientReleased = true
		t.Logf("Client: Floor %d released", floorID)
	}

	// Connect client
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}
	defer client.Disconnect()

	// Give connection time to establish
	time.Sleep(100 * time.Millisecond)

	// Send Hello
	if err := client.Hello(); err != nil {
		t.Fatalf("Hello failed: %v", err)
	}

	// Request floor
	requestID, err := client.RequestFloor(1, 0, PriorityNormal)
	if err != nil {
		t.Fatalf("Failed to request floor: %v", err)
	}
	t.Logf("Client: Requested floor 1 (request ID: %d)", requestID)

	// Wait for grant (need to wait for the asynchronous Granted status update)
	time.Sleep(500 * time.Millisecond)

	// Verify floor was granted
	mu.Lock()
	if !serverGranted {
		t.Error("Server should have granted the floor")
	}
	if !clientGranted {
		t.Error("Client should have received floor grant")
	}
	mu.Unlock()

	// Verify client has active request
	activeReqs := client.GetActiveRequests()
	if len(activeReqs) != 1 {
		t.Errorf("Client should have 1 active request, got %d", len(activeReqs))
	}
	if req, exists := activeReqs[1]; exists {
		if req.Status != RequestStatusGranted {
			t.Errorf("Floor status should be Granted, got %s", req.Status)
		}
	}

	// Release floor
	if err := client.ReleaseFloor(1); err != nil {
		t.Fatalf("Failed to release floor: %v", err)
	}

	// Wait for release
	time.Sleep(200 * time.Millisecond)

	// Verify floor was released
	mu.Lock()
	if !serverReleased {
		t.Error("Server should have received floor release")
	}
	if !clientReleased {
		t.Error("Client should have received floor release confirmation")
	}
	mu.Unlock()

	// Verify client has no active requests
	activeReqs = client.GetActiveRequests()
	if len(activeReqs) != 0 {
		t.Errorf("Client should have 0 active requests after release, got %d", len(activeReqs))
	}

	t.Log("Integration test completed successfully")
}

// TestMultipleFloors tests managing multiple floors
func TestMultipleFloors(t *testing.T) {
	// Create server
	serverConfig := DefaultServerConfig("127.0.0.1:0", 1)
	serverConfig.AutoGrant = true
	server := NewServer(serverConfig)
	server.AddFloor(1)
	server.AddFloor(2)

	// Start server
	listener, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	server.listener = listener
	listener.OnConnection = server.handleConnection
	listener.Start()
	defer listener.Close()

	serverAddr := listener.Addr().String()
	time.Sleep(100 * time.Millisecond)

	// Create client
	clientConfig := DefaultClientConfig(serverAddr, 1, 100)
	client := NewClient(clientConfig)

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}
	defer client.Disconnect()

	time.Sleep(100 * time.Millisecond)

	if err := client.Hello(); err != nil {
		t.Fatalf("Hello failed: %v", err)
	}

	// Request both floors
	_, err = client.RequestFloor(1, 0, PriorityNormal)
	if err != nil {
		t.Fatalf("Failed to request floor 1: %v", err)
	}

	_, err = client.RequestFloor(2, 0, PriorityHigh)
	if err != nil {
		t.Fatalf("Failed to request floor 2: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Verify both floors are active
	activeReqs := client.GetActiveRequests()
	if len(activeReqs) != 2 {
		t.Errorf("Client should have 2 active requests, got %d", len(activeReqs))
	}

	// Release floor 1
	if err := client.ReleaseFloor(1); err != nil {
		t.Fatalf("Failed to release floor 1: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Verify only floor 2 is active
	activeReqs = client.GetActiveRequests()
	if len(activeReqs) != 1 {
		t.Errorf("Client should have 1 active request after releasing floor 1, got %d", len(activeReqs))
	}
	if _, exists := activeReqs[2]; !exists {
		t.Error("Floor 2 should still be active")
	}

	// Release floor 2
	if err := client.ReleaseFloor(2); err != nil {
		t.Fatalf("Failed to release floor 2: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Verify no active floors
	activeReqs = client.GetActiveRequests()
	if len(activeReqs) != 0 {
		t.Errorf("Client should have 0 active requests, got %d", len(activeReqs))
	}

	t.Log("Multiple floors test completed successfully")
}

// TestFloorDenial tests floor request denial
func TestFloorDenial(t *testing.T) {
	// Create server with manual grant mode
	serverConfig := DefaultServerConfig("127.0.0.1:0", 1)
	serverConfig.AutoGrant = false
	server := NewServer(serverConfig)
	server.AddFloor(1)

	// Deny all floor requests
	server.OnFloorRequest = func(floorID, userID, requestID uint16) bool {
		return false // Deny
	}

	var denied bool
	var mu sync.Mutex

	server.OnFloorDenied = func(floorID, userID, requestID uint16) {
		mu.Lock()
		denied = true
		mu.Unlock()
		t.Logf("Server denied floor %d for user %d", floorID, userID)
	}

	// Start server
	listener, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	server.listener = listener
	listener.OnConnection = server.handleConnection
	listener.Start()
	defer listener.Close()

	serverAddr := listener.Addr().String()
	time.Sleep(100 * time.Millisecond)

	// Create client
	clientConfig := DefaultClientConfig(serverAddr, 1, 100)
	client := NewClient(clientConfig)

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}
	defer client.Disconnect()

	time.Sleep(100 * time.Millisecond)

	if err := client.Hello(); err != nil {
		t.Fatalf("Hello failed: %v", err)
	}

	// Request floor (should be denied)
	_, err = client.RequestFloor(1, 0, PriorityNormal)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Verify denial
	mu.Lock()
	if !denied {
		t.Error("Server should have denied the floor request")
	}
	mu.Unlock()

	t.Log("Floor denial test completed successfully")
}
