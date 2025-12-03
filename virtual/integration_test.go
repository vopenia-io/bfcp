package virtual

import (
	"sync"
	"testing"
	"time"

	"github.com/vopenia-io/bfcp"
)

// TestIntegrationWithMockServer tests virtual client with a real BFCP server
// This test requires a BFCP server running on localhost:5070
// Skip if server is not available
func TestIntegrationWithMockServer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Try to connect - if it fails, skip the test
	testClient, err := New("test-connectivity", "localhost:5070", 1, 100, &NoOpCallbacks{}, 5*time.Second)
	if err != nil {
		t.Fatalf("Failed to create test client: %v", err)
	}

	if err := testClient.Connect(); err != nil {
		t.Skipf("Skipping integration test: BFCP server not available at localhost:5070: %v", err)
	}
	testClient.Close()

	t.Log("BFCP server detected, running integration tests")

	// Now run actual integration tests
	t.Run("BasicFloorRequest", testBasicFloorRequest)
	t.Run("MultipleClientsSequential", testMultipleClientsSequential)
	t.Run("FloorRelease", testFloorRelease)
	t.Run("ManagerIntegration", testManagerIntegration)
}

func testBasicFloorRequest(t *testing.T) {
	var mu sync.Mutex
	granted := false

	callbacks := &testCallbacks{
		onFloorGranted: func(floorID, requestID uint16) {
			mu.Lock()
			granted = true
			mu.Unlock()
			t.Logf("Floor %d granted (request %d)", floorID, requestID)
		},
		onFloorDenied: func(floorID, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string) {
			t.Errorf("Floor %d denied: %s - %s", floorID, errorCode, errorInfo)
		},
		onConnected: func() {
			t.Log("Connected to server")
		},
		onDisconnected: func(reason error) {
			if reason != nil {
				t.Logf("Disconnected: %v", reason)
			}
		},
	}

	client, err := New("integration-test-1", "localhost:5070", 1, 101, callbacks, 10*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Connect
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Wait for connection to stabilize
	time.Sleep(500 * time.Millisecond)

	if !client.IsConnected() {
		t.Fatal("Client should be connected")
	}

	// Request floor
	requestID, err := client.RequestFloor(1, WithPriority(bfcp.PriorityNormal))
	if err != nil {
		t.Fatalf("Failed to request floor: %v", err)
	}

	t.Logf("Floor requested, request ID: %d", requestID)

	// Wait for floor to be granted (with timeout)
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for floor to be granted")
		case <-ticker.C:
			mu.Lock()
			if granted {
				mu.Unlock()
				t.Log("Floor was granted successfully")
				return
			}
			mu.Unlock()
		}
	}
}

func testMultipleClientsSequential(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 200,
		UserIDRangeEnd:   210,
	})
	defer manager.RemoveAll()

	// Create first client
	client1, err := manager.CreateClient("integration-client-1", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("Failed to create client 1: %v", err)
	}

	if err := client1.Connect(); err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Create second client
	client2, err := manager.CreateClient("integration-client-2", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("Failed to create client 2: %v", err)
	}

	if err := client2.Connect(); err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Both clients should be connected
	if !client1.IsConnected() {
		t.Error("Client 1 should be connected")
	}
	if !client2.IsConnected() {
		t.Error("Client 2 should be connected")
	}

	// Check manager status
	if manager.Count() != 2 {
		t.Errorf("Manager count = %d, want 2", manager.Count())
	}

	infos := manager.ListClients()
	if len(infos) != 2 {
		t.Errorf("ListClients returned %d clients, want 2", len(infos))
	}

	for _, info := range infos {
		if !info.Connected {
			t.Errorf("Client %s should be connected", info.ID)
		}
	}
}

func testFloorRelease(t *testing.T) {
	var mu sync.Mutex
	granted := false
	released := false

	callbacks := &testCallbacks{
		onFloorGranted: func(floorID, requestID uint16) {
			mu.Lock()
			granted = true
			mu.Unlock()
			t.Logf("Floor %d granted", floorID)
		},
		onFloorReleased: func(floorID, requestID uint16) {
			mu.Lock()
			released = true
			mu.Unlock()
			t.Logf("Floor %d released", floorID)
		},
	}

	client, err := New("integration-test-release", "localhost:5070", 1, 150, callbacks, 10*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Request floor
	requestID, err := client.RequestFloor(1)
	if err != nil {
		t.Fatalf("Failed to request floor: %v", err)
	}

	// Wait for grant
	time.Sleep(2 * time.Second)

	mu.Lock()
	wasGranted := granted
	mu.Unlock()

	if !wasGranted {
		t.Log("Floor was not granted, skipping release test")
		return
	}

	// Release the floor
	if err := client.ReleaseFloor(requestID); err != nil {
		t.Fatalf("Failed to release floor: %v", err)
	}

	// Wait for release confirmation
	time.Sleep(2 * time.Second)

	mu.Lock()
	wasReleased := released
	mu.Unlock()

	if !wasReleased {
		t.Error("Floor should have been released")
	}
}

func testManagerIntegration(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 300,
		UserIDRangeEnd:   305,
	})

	// Create multiple clients
	for i := 0; i < 3; i++ {
		clientID := string(rune('A' + i))
		client, err := manager.CreateClient(clientID, &NoOpCallbacks{})
		if err != nil {
			t.Fatalf("Failed to create client %s: %v", clientID, err)
		}

		if err := client.Connect(); err != nil {
			t.Fatalf("Failed to connect client %s: %v", clientID, err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// Verify all are created
	if manager.Count() != 3 {
		t.Errorf("Manager count = %d, want 3", manager.Count())
	}

	// Remove one client
	if err := manager.RemoveClient("A"); err != nil {
		t.Errorf("Failed to remove client A: %v", err)
	}

	if manager.Count() != 2 {
		t.Errorf("After removal, manager count = %d, want 2", manager.Count())
	}

	// Remove all
	if err := manager.RemoveAll(); err != nil {
		t.Errorf("Failed to remove all: %v", err)
	}

	if manager.Count() != 0 {
		t.Errorf("After RemoveAll, manager count = %d, want 0", manager.Count())
	}
}

// testCallbacks is a helper for integration tests
type testCallbacks struct {
	onFloorGranted         func(floorID, requestID uint16)
	onFloorDenied          func(floorID, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string)
	onFloorRevoked         func(floorID, requestID uint16)
	onFloorReleased        func(floorID, requestID uint16)
	onQueuePositionChanged func(floorID, requestID uint16, position uint8)
	onFloorStatusChanged   func(floorID, requestID uint16, status bfcp.RequestStatus)
	onConnected            func()
	onDisconnected         func(reason error)
	onReconnecting         func(attempt int)
	onError                func(err error)
}

func (c *testCallbacks) OnFloorGranted(floorID, requestID uint16) {
	if c.onFloorGranted != nil {
		c.onFloorGranted(floorID, requestID)
	}
}

func (c *testCallbacks) OnFloorDenied(floorID, requestID uint16, errorCode bfcp.ErrorCode, errorInfo string) {
	if c.onFloorDenied != nil {
		c.onFloorDenied(floorID, requestID, errorCode, errorInfo)
	}
}

func (c *testCallbacks) OnFloorRevoked(floorID, requestID uint16) {
	if c.onFloorRevoked != nil {
		c.onFloorRevoked(floorID, requestID)
	}
}

func (c *testCallbacks) OnFloorReleased(floorID, requestID uint16) {
	if c.onFloorReleased != nil {
		c.onFloorReleased(floorID, requestID)
	}
}

func (c *testCallbacks) OnQueuePositionChanged(floorID, requestID uint16, position uint8) {
	if c.onQueuePositionChanged != nil {
		c.onQueuePositionChanged(floorID, requestID, position)
	}
}

func (c *testCallbacks) OnFloorStatusChanged(floorID, requestID uint16, status bfcp.RequestStatus) {
	if c.onFloorStatusChanged != nil {
		c.onFloorStatusChanged(floorID, requestID, status)
	}
}

func (c *testCallbacks) OnConnected() {
	if c.onConnected != nil {
		c.onConnected()
	}
}

func (c *testCallbacks) OnDisconnected(reason error) {
	if c.onDisconnected != nil {
		c.onDisconnected(reason)
	}
}

func (c *testCallbacks) OnReconnecting(attempt int) {
	if c.onReconnecting != nil {
		c.onReconnecting(attempt)
	}
}

func (c *testCallbacks) OnError(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}
