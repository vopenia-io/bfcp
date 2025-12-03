package virtual

import (
	"testing"
	"time"

	"github.com/vopenia-io/bfcp"
)

// TestClientCreation tests basic client creation
func TestClientCreation(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		serverAddr   string
		conferenceID uint32
		userID       uint16
		callbacks    Callbacks
		timeout      time.Duration
		wantErr      bool
	}{
		{
			name:         "valid client",
			id:           "test-client-1",
			serverAddr:   "localhost:5070",
			conferenceID: 1,
			userID:       100,
			callbacks:    &NoOpCallbacks{},
			timeout:      30 * time.Second,
			wantErr:      false,
		},
		{
			name:         "empty id",
			id:           "",
			serverAddr:   "localhost:5070",
			conferenceID: 1,
			userID:       100,
			callbacks:    &NoOpCallbacks{},
			timeout:      30 * time.Second,
			wantErr:      true,
		},
		{
			name:         "empty server address",
			id:           "test-client-2",
			serverAddr:   "",
			conferenceID: 1,
			userID:       100,
			callbacks:    &NoOpCallbacks{},
			timeout:      30 * time.Second,
			wantErr:      true,
		},
		{
			name:         "nil callbacks uses NoOp",
			id:           "test-client-3",
			serverAddr:   "localhost:5070",
			conferenceID: 1,
			userID:       100,
			callbacks:    nil,
			timeout:      30 * time.Second,
			wantErr:      false,
		},
		{
			name:         "zero timeout uses default",
			id:           "test-client-4",
			serverAddr:   "localhost:5070",
			conferenceID: 1,
			userID:       100,
			callbacks:    &NoOpCallbacks{},
			timeout:      0,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.id, tt.serverAddr, tt.conferenceID, tt.userID, tt.callbacks, tt.timeout)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				if client.ID() != tt.id {
					t.Errorf("client.ID() = %v, want %v", client.ID(), tt.id)
				}
				if client.UserID() != tt.userID {
					t.Errorf("client.UserID() = %v, want %v", client.UserID(), tt.userID)
				}
				if client.ConferenceID() != tt.conferenceID {
					t.Errorf("client.ConferenceID() = %v, want %v", client.ConferenceID(), tt.conferenceID)
				}
				if client.IsConnected() {
					t.Error("client should not be connected after creation")
				}

				// Clean up
				_ = client.Close()
			}
		})
	}
}

// TestClientStatus tests the GetStatus method
func TestClientStatus(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	status := client.GetStatus()
	if status.ID != "test-client" {
		t.Errorf("status.ID = %v, want test-client", status.ID)
	}
	if status.UserID != 100 {
		t.Errorf("status.UserID = %v, want 100", status.UserID)
	}
	if status.Connected {
		t.Error("status.Connected should be false")
	}
	if len(status.ActiveFloors) != 0 {
		t.Errorf("status.ActiveFloors should be empty, got %v", status.ActiveFloors)
	}
	if len(status.PendingRequests) != 0 {
		t.Errorf("status.PendingRequests should be empty, got %v", status.PendingRequests)
	}
}

// TestClientCloseTwice tests that closing a client twice is safe
func TestClientCloseTwice(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// First close
	if err := client.Close(); err != nil {
		t.Errorf("First Close() failed: %v", err)
	}

	// Second close should not error
	if err := client.Close(); err != nil {
		t.Errorf("Second Close() failed: %v", err)
	}
}

// TestClientOperationsWhenClosed tests that operations fail when client is closed
func TestClientOperationsWhenClosed(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Close the client
	if err := client.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Try to request floor
	_, err = client.RequestFloor(1)
	if err != ErrClientClosed {
		t.Errorf("RequestFloor() error = %v, want %v", err, ErrClientClosed)
	}

	// Try to release floor
	err = client.ReleaseFloor(1)
	if err != ErrClientClosed {
		t.Errorf("ReleaseFloor() error = %v, want %v", err, ErrClientClosed)
	}

	// Try to release all floors
	err = client.ReleaseAllFloors()
	if err != ErrClientClosed {
		t.Errorf("ReleaseAllFloors() error = %v, want %v", err, ErrClientClosed)
	}
}

// TestRequestOptions tests floor request options
func TestRequestOptions(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Test that options are applied correctly (we can't test actual behavior without a server,
	// but we can ensure the options don't cause errors)
	opts := []RequestOption{
		WithPriority(bfcp.PriorityHigh),
		WithInformation("Test screen share"),
		WithBeneficiary(200),
	}

	// Apply options to verify no panics
	options := &floorRequestOptions{
		priority: bfcp.PriorityNormal,
	}
	for _, opt := range opts {
		opt(options)
	}

	if options.priority != bfcp.PriorityHigh {
		t.Errorf("priority = %v, want %v", options.priority, bfcp.PriorityHigh)
	}
	if options.information != "Test screen share" {
		t.Errorf("information = %v, want 'Test screen share'", options.information)
	}
	if options.beneficiaryID != 200 {
		t.Errorf("beneficiaryID = %v, want 200", options.beneficiaryID)
	}
}

// TestClientOperationsWhenNotConnected tests that operations fail gracefully when not connected
func TestClientOperationsWhenNotConnected(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Try to request floor without connecting
	_, err = client.RequestFloor(1)
	if err != ErrClientNotConnected {
		t.Errorf("RequestFloor() error = %v, want %v", err, ErrClientNotConnected)
	}

	// Try to release floor without connecting
	err = client.ReleaseFloor(1)
	if err != ErrClientNotConnected {
		t.Errorf("ReleaseFloor() error = %v, want %v", err, ErrClientNotConnected)
	}

	// Try to release all floors without connecting
	err = client.ReleaseAllFloors()
	if err != ErrClientNotConnected {
		t.Errorf("ReleaseAllFloors() error = %v, want %v", err, ErrClientNotConnected)
	}
}

// TestFloorRequestValidation tests floor request validation
func TestFloorRequestValidation(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Force connected state for testing (normally would call Connect())
	client.connected.Store(true)

	// Try to request floor with invalid floor ID
	_, err = client.RequestFloor(0)
	if err != ErrInvalidFloorID {
		t.Errorf("RequestFloor(0) error = %v, want %v", err, ErrInvalidFloorID)
	}
}

// TestReleaseFloorValidation tests floor release validation
func TestReleaseFloorValidation(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Force connected state for testing
	client.connected.Store(true)

	// Try to release floor with invalid request ID
	err = client.ReleaseFloor(0)
	if err != ErrInvalidRequestID {
		t.Errorf("ReleaseFloor(0) error = %v, want %v", err, ErrInvalidRequestID)
	}

	// Try to release non-existent request
	err = client.ReleaseFloor(999)
	if err == nil {
		t.Error("ReleaseFloor(999) should return error for non-existent request")
	}
}

// TestNoOpCallbacks verifies that NoOpCallbacks doesn't panic
func TestNoOpCallbacks(t *testing.T) {
	cb := &NoOpCallbacks{}

	// Call all callbacks to ensure no panics
	cb.OnFloorGranted(1, 1)
	cb.OnFloorDenied(1, 1, bfcp.ErrorInvalidFloorID, "test")
	cb.OnFloorRevoked(1, 1)
	cb.OnFloorReleased(1, 1)
	cb.OnQueuePositionChanged(1, 1, 1)
	cb.OnFloorStatusChanged(1, 1, bfcp.RequestStatusGranted)
	cb.OnConnected()
	cb.OnDisconnected(nil)
	cb.OnReconnecting(1)
	cb.OnError(nil)

	// If we get here without panicking, test passes
}

// TestConcurrentOperations tests thread safety of client operations
func TestConcurrentOperations(t *testing.T) {
	client, err := New("test-client", "localhost:5070", 1, 100, &NoOpCallbacks{}, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Run multiple concurrent GetStatus calls
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_ = client.GetStatus()
			_ = client.IsConnected()
			_ = client.ID()
			_ = client.UserID()
			_ = client.ConferenceID()
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}
