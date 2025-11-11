package virtual

import (
	"testing"
	"time"
)

// TestManagerCreation tests manager creation
func TestManagerCreation(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())

	if manager.ServerAddr() != "localhost:5070" {
		t.Errorf("ServerAddr() = %v, want localhost:5070", manager.ServerAddr())
	}
	if manager.ConferenceID() != 1 {
		t.Errorf("ConferenceID() = %v, want 1", manager.ConferenceID())
	}
	if manager.Count() != 0 {
		t.Errorf("Count() = %v, want 0", manager.Count())
	}
}

// TestManagerConfigDefaults tests that default config values are applied
func TestManagerConfigDefaults(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		wantStart uint16
		wantEnd   uint16
	}{
		{
			name:      "default config",
			config:    DefaultConfig(),
			wantStart: 100,
			wantEnd:   1000,
		},
		{
			name: "zero values use defaults",
			config: Config{
				UserIDRangeStart: 0,
				UserIDRangeEnd:   0,
			},
			wantStart: 100,
			wantEnd:   1000,
		},
		{
			name: "invalid range uses defaults",
			config: Config{
				UserIDRangeStart: 500,
				UserIDRangeEnd:   100,
			},
			wantStart: 100,
			wantEnd:   1000,
		},
		{
			name: "custom range",
			config: Config{
				UserIDRangeStart: 200,
				UserIDRangeEnd:   300,
			},
			wantStart: 200,
			wantEnd:   300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager("localhost:5070", 1, tt.config)
			// Check by calculating expected available count
			expectedAvailable := int(tt.wantEnd - tt.wantStart + 1)
			if manager.AvailableUserIDs() != expectedAvailable {
				t.Errorf("AvailableUserIDs() = %v, want %v", manager.AvailableUserIDs(), expectedAvailable)
			}
		})
	}
}

// TestCreateClient tests basic client creation via manager
func TestCreateClient(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   110,
	})

	client, err := manager.CreateClient("test-client-1", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("CreateClient() error = %v", err)
	}
	defer manager.RemoveAll()

	if client.ID() != "test-client-1" {
		t.Errorf("client.ID() = %v, want test-client-1", client.ID())
	}
	if client.UserID() < 100 || client.UserID() > 110 {
		t.Errorf("client.UserID() = %v, want in range [100, 110]", client.UserID())
	}
	if manager.Count() != 1 {
		t.Errorf("manager.Count() = %v, want 1", manager.Count())
	}
}

// TestCreateDuplicateClient tests that creating duplicate clients fails
func TestCreateDuplicateClient(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())
	defer manager.RemoveAll()

	_, err := manager.CreateClient("test-client", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("First CreateClient() error = %v", err)
	}

	_, err = manager.CreateClient("test-client", &NoOpCallbacks{})
	if err != ErrClientAlreadyExists {
		t.Errorf("Second CreateClient() error = %v, want %v", err, ErrClientAlreadyExists)
	}
}

// TestCreateClientWithEmptyID tests that empty client ID fails
func TestCreateClientWithEmptyID(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())
	defer manager.RemoveAll()

	_, err := manager.CreateClient("", &NoOpCallbacks{})
	if err == nil {
		t.Error("CreateClient('') should return error")
	}
}

// TestUserIDAllocation tests that user IDs are allocated correctly
func TestUserIDAllocation(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   105, // Only 6 IDs available
	})
	defer manager.RemoveAll()

	initialAvailable := manager.AvailableUserIDs()
	if initialAvailable != 6 {
		t.Errorf("Initial AvailableUserIDs() = %v, want 6", initialAvailable)
	}

	// Create 3 clients
	ids := []string{"client1", "client2", "client3"}
	userIDs := make(map[uint16]bool)

	for _, id := range ids {
		client, err := manager.CreateClient(id, &NoOpCallbacks{})
		if err != nil {
			t.Fatalf("CreateClient(%v) error = %v", id, err)
		}
		userIDs[client.UserID()] = true
	}

	// Check that we got 3 unique user IDs
	if len(userIDs) != 3 {
		t.Errorf("Got %v unique user IDs, want 3", len(userIDs))
	}

	// Check that available count decreased
	if manager.AvailableUserIDs() != 3 {
		t.Errorf("AvailableUserIDs() = %v, want 3", manager.AvailableUserIDs())
	}

	// Remove one client and check that user ID is returned to pool
	if err := manager.RemoveClient("client1"); err != nil {
		t.Fatalf("RemoveClient() error = %v", err)
	}

	if manager.AvailableUserIDs() != 4 {
		t.Errorf("After removal, AvailableUserIDs() = %v, want 4", manager.AvailableUserIDs())
	}
}

// TestUserIDPoolExhaustion tests behavior when user ID pool is exhausted
func TestUserIDPoolExhaustion(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   102, // Only 3 IDs available
	})
	defer manager.RemoveAll()

	// Create 3 clients (exhausts pool)
	for i := 1; i <= 3; i++ {
		_, err := manager.CreateClient(string(rune('A'+i-1)), &NoOpCallbacks{})
		if err != nil {
			t.Fatalf("CreateClient(%d) error = %v", i, err)
		}
	}

	// Try to create 4th client (should fail)
	_, err := manager.CreateClient("client4", &NoOpCallbacks{})
	if err == nil {
		t.Error("CreateClient() should fail when user ID pool is exhausted")
	}

	if manager.AvailableUserIDs() != 0 {
		t.Errorf("AvailableUserIDs() = %v, want 0", manager.AvailableUserIDs())
	}
}

// TestCreateClientWithUserID tests creating client with specific user ID
func TestCreateClientWithUserID(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   200,
	})
	defer manager.RemoveAll()

	client, err := manager.CreateClientWithUserID("test-client", 150, &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("CreateClientWithUserID() error = %v", err)
	}

	if client.UserID() != 150 {
		t.Errorf("client.UserID() = %v, want 150", client.UserID())
	}

	// Try to create another client with the same user ID (should fail)
	_, err = manager.CreateClientWithUserID("test-client-2", 150, &NoOpCallbacks{})
	if err == nil {
		t.Error("CreateClientWithUserID() with duplicate user ID should fail")
	}
}

// TestCreateClientWithUserIDOutOfRange tests that out-of-range user IDs fail
func TestCreateClientWithUserIDOutOfRange(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   200,
	})
	defer manager.RemoveAll()

	tests := []struct {
		name   string
		userID uint16
	}{
		{"below range", 99},
		{"above range", 201},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manager.CreateClientWithUserID("test-client", tt.userID, &NoOpCallbacks{})
			if err == nil {
				t.Errorf("CreateClientWithUserID(%v) should fail for out-of-range user ID", tt.userID)
			}
		})
	}
}

// TestGetClient tests retrieving clients by ID
func TestGetClient(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())
	defer manager.RemoveAll()

	// Create client
	original, err := manager.CreateClient("test-client", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("CreateClient() error = %v", err)
	}

	// Retrieve client
	retrieved, exists := manager.GetClient("test-client")
	if !exists {
		t.Fatal("GetClient() exists = false, want true")
	}
	if retrieved.ID() != original.ID() {
		t.Errorf("Retrieved client ID = %v, want %v", retrieved.ID(), original.ID())
	}

	// Try to get non-existent client
	_, exists = manager.GetClient("non-existent")
	if exists {
		t.Error("GetClient('non-existent') exists = true, want false")
	}
}

// TestRemoveClient tests removing clients
func TestRemoveClient(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())
	defer manager.RemoveAll()

	// Create client
	_, err := manager.CreateClient("test-client", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("CreateClient() error = %v", err)
	}

	if manager.Count() != 1 {
		t.Errorf("Count() = %v, want 1", manager.Count())
	}

	// Remove client
	if err := manager.RemoveClient("test-client"); err != nil {
		t.Errorf("RemoveClient() error = %v", err)
	}

	if manager.Count() != 0 {
		t.Errorf("After removal, Count() = %v, want 0", manager.Count())
	}

	// Try to remove non-existent client
	err = manager.RemoveClient("non-existent")
	if err == nil {
		t.Error("RemoveClient('non-existent') should return error")
	}
}

// TestRemoveAll tests removing all clients
func TestRemoveAll(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())

	// Create multiple clients
	for i := 1; i <= 5; i++ {
		_, err := manager.CreateClient(string(rune('A'+i-1)), &NoOpCallbacks{})
		if err != nil {
			t.Fatalf("CreateClient(%d) error = %v", i, err)
		}
	}

	if manager.Count() != 5 {
		t.Errorf("Count() = %v, want 5", manager.Count())
	}

	// Remove all
	if err := manager.RemoveAll(); err != nil {
		t.Errorf("RemoveAll() error = %v", err)
	}

	if manager.Count() != 0 {
		t.Errorf("After RemoveAll(), Count() = %v, want 0", manager.Count())
	}
}

// TestListClients tests listing all clients
func TestListClients(t *testing.T) {
	manager := NewManager("localhost:5070", 1, DefaultConfig())
	defer manager.RemoveAll()

	// Initially empty
	list := manager.ListClients()
	if len(list) != 0 {
		t.Errorf("Initial ListClients() length = %v, want 0", len(list))
	}

	// Create clients
	_, err := manager.CreateClient("client1", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("CreateClient(client1) error = %v", err)
	}
	_, err = manager.CreateClient("client2", &NoOpCallbacks{})
	if err != nil {
		t.Fatalf("CreateClient(client2) error = %v", err)
	}

	// List should have 2 clients
	list = manager.ListClients()
	if len(list) != 2 {
		t.Errorf("ListClients() length = %v, want 2", len(list))
	}

	// Check that IDs are present
	foundIDs := make(map[string]bool)
	for _, info := range list {
		foundIDs[info.ID] = true
	}
	if !foundIDs["client1"] || !foundIDs["client2"] {
		t.Errorf("ListClients() missing expected client IDs, got %v", foundIDs)
	}
}

// TestUserIDPoolBasics tests basic UserIDPool operations
func TestUserIDPoolBasics(t *testing.T) {
	pool := NewUserIDPool(100, 105)

	// Check initial available
	if pool.Available() != 6 {
		t.Errorf("Initial Available() = %v, want 6", pool.Available())
	}

	// Allocate IDs
	id1, err := pool.Allocate()
	if err != nil {
		t.Fatalf("First Allocate() error = %v", err)
	}
	if id1 < 100 || id1 > 105 {
		t.Errorf("Allocated ID %v is out of range [100, 105]", id1)
	}

	id2, err := pool.Allocate()
	if err != nil {
		t.Fatalf("Second Allocate() error = %v", err)
	}
	if id1 == id2 {
		t.Error("Allocated IDs should be unique")
	}

	// Check available decreased
	if pool.Available() != 4 {
		t.Errorf("After 2 allocations, Available() = %v, want 4", pool.Available())
	}

	// Check IsAllocated
	if !pool.IsAllocated(id1) {
		t.Errorf("IsAllocated(%v) = false, want true", id1)
	}
	if pool.IsAllocated(99) {
		t.Error("IsAllocated(99) = true, want false")
	}

	// Release ID
	pool.Release(id1)
	if pool.Available() != 5 {
		t.Errorf("After release, Available() = %v, want 5", pool.Available())
	}
	if pool.IsAllocated(id1) {
		t.Errorf("After release, IsAllocated(%v) should be false", id1)
	}
}

// TestUserIDPoolExhaustion tests pool exhaustion
func TestUserIDPoolExhaustion_Pool(t *testing.T) {
	pool := NewUserIDPool(100, 102) // Only 3 IDs

	// Allocate all IDs
	for i := 0; i < 3; i++ {
		_, err := pool.Allocate()
		if err != nil {
			t.Fatalf("Allocate(%d) error = %v", i, err)
		}
	}

	if pool.Available() != 0 {
		t.Errorf("After exhaustion, Available() = %v, want 0", pool.Available())
	}

	// Try to allocate when exhausted
	_, err := pool.Allocate()
	if err == nil {
		t.Error("Allocate() should fail when pool is exhausted")
	}
}

// TestUserIDPoolAllocateSpecific tests allocating specific IDs
func TestUserIDPoolAllocateSpecific(t *testing.T) {
	pool := NewUserIDPool(100, 110)

	// Allocate specific ID
	if err := pool.AllocateSpecific(105); err != nil {
		t.Errorf("AllocateSpecific(105) error = %v", err)
	}

	if !pool.IsAllocated(105) {
		t.Error("After AllocateSpecific(105), IsAllocated(105) should be true")
	}

	// Try to allocate same ID again
	if err := pool.AllocateSpecific(105); err == nil {
		t.Error("AllocateSpecific(105) should fail when ID is already allocated")
	}

	// Try to allocate out-of-range ID
	if err := pool.AllocateSpecific(200); err == nil {
		t.Error("AllocateSpecific(200) should fail for out-of-range ID")
	}
}

// TestConcurrentManagerOperations tests thread safety of manager
func TestConcurrentManagerOperations(t *testing.T) {
	manager := NewManager("localhost:5070", 1, Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   200,
	})
	defer manager.RemoveAll()

	// Create clients concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			clientID := string(rune('A' + id))
			_, _ = manager.CreateClient(clientID, &NoOpCallbacks{})
			_, _ = manager.GetClient(clientID)
			_ = manager.ListClients()
			_ = manager.Count()
			_ = manager.AvailableUserIDs()
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Clean up
	_ = manager.RemoveAll()
}

// TestDefaultConfigValues tests that DefaultConfig returns expected values
func TestDefaultConfigValues(t *testing.T) {
	config := DefaultConfig()

	if config.UserIDRangeStart != 100 {
		t.Errorf("UserIDRangeStart = %v, want 100", config.UserIDRangeStart)
	}
	if config.UserIDRangeEnd != 1000 {
		t.Errorf("UserIDRangeEnd = %v, want 1000", config.UserIDRangeEnd)
	}
	if config.ReconnectDelay != 5*time.Second {
		t.Errorf("ReconnectDelay = %v, want 5s", config.ReconnectDelay)
	}
	if config.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %v, want 30s", config.RequestTimeout)
	}
	if config.EnableMetrics {
		t.Error("EnableMetrics should be false by default")
	}
}
