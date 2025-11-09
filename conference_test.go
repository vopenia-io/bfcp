package bfcp

import (
	"sync"
	"testing"
)

func TestConferenceManager_CreateConference(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	conferenceID := uint32(100)
	userID := uint16(10)

	conf, err := cm.CreateConference(conferenceID, userID)
	if err != nil {
		t.Fatalf("CreateConference failed: %v", err)
	}

	if conf.ID != conferenceID {
		t.Errorf("Expected conference ID %d, got %d", conferenceID, conf.ID)
	}

	if conf.SIPDeviceUserID != userID {
		t.Errorf("Expected SIP device UserID %d, got %d", userID, conf.SIPDeviceUserID)
	}

	// Verify conference is retrievable
	retrieved, exists := cm.GetConference(conferenceID)
	if !exists {
		t.Fatal("Conference not found after creation")
	}

	if retrieved.ID != conferenceID {
		t.Errorf("Retrieved conference has wrong ID: expected %d, got %d", conferenceID, retrieved.ID)
	}
}

func TestConferenceManager_CreateConference_Duplicate(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	conferenceID := uint32(100)

	_, err := cm.CreateConference(conferenceID, 10)
	if err != nil {
		t.Fatalf("First CreateConference failed: %v", err)
	}

	// Try to create duplicate
	_, err = cm.CreateConference(conferenceID, 20)
	if err == nil {
		t.Error("Expected error when creating duplicate conference, got nil")
	}
}

func TestConferenceManager_DeleteConference(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	conferenceID := uint32(100)

	_, err := cm.CreateConference(conferenceID, 10)
	if err != nil {
		t.Fatalf("CreateConference failed: %v", err)
	}

	// Delete conference
	err = cm.DeleteConference(conferenceID)
	if err != nil {
		t.Fatalf("DeleteConference failed: %v", err)
	}

	// Verify it's gone
	_, exists := cm.GetConference(conferenceID)
	if exists {
		t.Error("Conference still exists after deletion")
	}
}

func TestConferenceManager_AllocateFloor_UniqueIDs(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	conf1ID := uint32(100)
	conf2ID := uint32(200)

	cm.CreateConference(conf1ID, 10)
	cm.CreateConference(conf2ID, 20)

	// Allocate multiple floors
	allocated := make(map[uint16]bool)

	for i := 0; i < 100; i++ {
		var floorID uint16
		var err error

		if i%2 == 0 {
			floorID, err = cm.AllocateFloor(conf1ID)
		} else {
			floorID, err = cm.AllocateFloor(conf2ID)
		}

		if err != nil {
			t.Fatalf("AllocateFloor failed on iteration %d: %v", i, err)
		}

		if allocated[floorID] {
			t.Fatalf("Collision detected: floor %d allocated twice", floorID)
		}

		allocated[floorID] = true
	}

	if len(allocated) != 100 {
		t.Errorf("Expected 100 unique floor IDs, got %d", len(allocated))
	}
}

func TestConferenceManager_AllocateFloor_ConferenceNotFound(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	nonExistentConf := uint32(999)

	_, err := cm.AllocateFloor(nonExistentConf)
	if err == nil {
		t.Error("Expected error when allocating floor for non-existent conference")
	}
}

func TestConference_AllocateVirtualUserID(t *testing.T) {
	conf := NewConference(100, 10)

	// Allocate multiple virtual user IDs
	allocated := make(map[uint16]bool)

	for i := 0; i < 50; i++ {
		userID := conf.AllocateVirtualUserID()

		if allocated[userID] {
			t.Fatalf("Collision detected: virtual userID %d allocated twice", userID)
		}

		if userID < 100 {
			t.Fatalf("Virtual userID %d is less than 100 (should start at 100)", userID)
		}

		allocated[userID] = true
	}

	if len(allocated) != 50 {
		t.Errorf("Expected 50 unique virtual userIDs, got %d", len(allocated))
	}
}

func TestConference_VirtualClientManagement(t *testing.T) {
	conf := NewConference(100, 10)

	userID := uint16(101)
	identity := "participant_1"

	// Register virtual client
	conf.RegisterVirtualClient(userID, identity)

	// Verify registration
	conf.mu.RLock()
	storedIdentity, exists := conf.virtualClients[userID]
	conf.mu.RUnlock()

	if !exists {
		t.Fatal("Virtual client not found after registration")
	}

	if storedIdentity != identity {
		t.Errorf("Expected identity '%s', got '%s'", identity, storedIdentity)
	}

	// Unregister virtual client
	conf.UnregisterVirtualClient(userID)

	// Verify unregistration
	conf.mu.RLock()
	_, exists = conf.virtualClients[userID]
	conf.mu.RUnlock()

	if exists {
		t.Error("Virtual client still exists after unregistration")
	}
}

func TestConference_FloorManagement(t *testing.T) {
	conf := NewConference(100, 10)

	floorID := uint16(50)

	// Add floor
	floor := conf.AddFloor(floorID)
	if floor == nil {
		t.Fatal("AddFloor returned nil")
	}

	if floor.FloorID != floorID {
		t.Errorf("Expected floor ID %d, got %d", floorID, floor.FloorID)
	}

	// Get floor
	retrieved, exists := conf.GetFloor(floorID)
	if !exists {
		t.Fatal("Floor not found after adding")
	}

	if retrieved.FloorID != floorID {
		t.Errorf("Retrieved floor has wrong ID: expected %d, got %d", floorID, retrieved.FloorID)
	}

	// Remove floor
	conf.RemoveFloor(floorID)

	// Verify removal
	_, exists = conf.GetFloor(floorID)
	if exists {
		t.Error("Floor still exists after removal")
	}
}

func TestConferenceManager_ConcurrentAccess(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	const numGoroutines = 10
	const operationsPerGoroutine = 50

	var wg sync.WaitGroup

	// Concurrent conference creation
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				confID := uint32(index*1000 + j)

				_, err := cm.CreateConference(confID, uint16(index))
				if err != nil {
					t.Errorf("CreateConference failed: %v", err)
					return
				}

				// Allocate floor
				_, err = cm.AllocateFloor(confID)
				if err != nil {
					t.Errorf("AllocateFloor failed: %v", err)
					return
				}

				// Delete conference
				err = cm.DeleteConference(confID)
				if err != nil {
					t.Errorf("DeleteConference failed: %v", err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestConferenceManager_InjectFloorRequest_Queuing(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	conferenceID := uint32(100)
	cm.CreateConference(conferenceID, 10)

	floorID := uint16(50)
	userID := uint16(101)

	// Inject floor request (should be queued since no client connected)
	_, err := cm.InjectFloorRequest(conferenceID, floorID, userID)
	if err != nil {
		// This is okay - request is queued
		t.Logf("Floor request queued (expected): %v", err)
	}

	// Verify pending request was created
	cm.pendingMu.Lock()
	key := "100-101"
	_, exists := cm.pendingRequests[key]
	cm.pendingMu.Unlock()

	if !exists {
		t.Error("Expected pending request to be queued, but it wasn't found")
	}
}

func TestConferenceManager_GetConference_NotFound(t *testing.T) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	_, exists := cm.GetConference(999)
	if exists {
		t.Error("Expected GetConference to return false for non-existent conference")
	}
}

func BenchmarkConferenceManager_AllocateFloor(b *testing.B) {
	server := NewServer(DefaultServerConfig(":0", 1))
	cm := server.GetConferenceManager()

	conferenceID := uint32(100)
	cm.CreateConference(conferenceID, 10)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := cm.AllocateFloor(conferenceID)
		if err != nil {
			b.Fatalf("AllocateFloor failed: %v", err)
		}
	}
}

func BenchmarkConference_AllocateVirtualUserID(b *testing.B) {
	conf := NewConference(100, 10)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conf.AllocateVirtualUserID()
	}
}
