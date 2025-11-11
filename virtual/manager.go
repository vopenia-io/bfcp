package virtual

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Manager handles multiple virtual BFCP clients for a conference.
// It manages user ID allocation, client lifecycle, and provides a central
// point for managing all virtual participants in a conference.
type Manager struct {
	serverAddr   string
	conferenceID uint32

	clients    map[string]*Client // id -> client
	userIDPool *UserIDPool        // Manages unique user ID allocation

	config Config

	mu sync.RWMutex
}

// Config contains configuration options for the Manager
type Config struct {
	// UserIDRangeStart is the start of the virtual user ID range.
	// User IDs will be allocated from this range for virtual clients.
	// Default: 100
	UserIDRangeStart uint16

	// UserIDRangeEnd is the end of the virtual user ID range (inclusive).
	// Default: 1000
	UserIDRangeEnd uint16

	// ReconnectDelay is the delay between reconnection attempts.
	// Set to 0 to disable automatic reconnection.
	// Default: 5 seconds
	ReconnectDelay time.Duration

	// RequestTimeout is the default timeout for floor requests.
	// Default: 30 seconds
	RequestTimeout time.Duration

	// EnableMetrics enables Prometheus metrics collection.
	// Default: false (not yet implemented)
	EnableMetrics bool
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		UserIDRangeStart: 100,
		UserIDRangeEnd:   1000,
		ReconnectDelay:   5 * time.Second,
		RequestTimeout:   30 * time.Second,
		EnableMetrics:    false,
	}
}

// ClientInfo contains summary information about a virtual client
type ClientInfo struct {
	ID           string   // Client identifier
	UserID       uint16   // BFCP User ID
	Connected    bool     // Connection status
	ActiveFloors []uint16 // Currently held floors
}

// NewManager creates a new Manager for managing virtual BFCP clients.
//
// Parameters:
//   serverAddr - BFCP server address (e.g., "localhost:5070")
//   conferenceID - Conference ID for all managed clients
//   config - Configuration options (use DefaultConfig() for defaults)
func NewManager(serverAddr string, conferenceID uint32, config Config) *Manager {
	// Validate and apply defaults
	if config.UserIDRangeStart == 0 {
		config.UserIDRangeStart = 100
	}
	if config.UserIDRangeEnd == 0 {
		config.UserIDRangeEnd = 1000
	}
	if config.UserIDRangeStart > config.UserIDRangeEnd {
		config.UserIDRangeStart = 100
		config.UserIDRangeEnd = 1000
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}

	return &Manager{
		serverAddr:   serverAddr,
		conferenceID: conferenceID,
		clients:      make(map[string]*Client),
		userIDPool:   NewUserIDPool(config.UserIDRangeStart, config.UserIDRangeEnd),
		config:       config,
	}
}

// CreateClient creates a new virtual BFCP client with the given ID and callbacks.
// The manager automatically allocates a unique user ID from the configured range.
//
// Returns an error if:
//   - A client with this ID already exists
//   - No user IDs are available in the configured range
//   - The client cannot be created
func (m *Manager) CreateClient(id string, callbacks Callbacks) (*Client, error) {
	if id == "" {
		return nil, errors.New("client ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if client already exists
	if _, exists := m.clients[id]; exists {
		return nil, ErrClientAlreadyExists
	}

	// Allocate a user ID
	userID, err := m.userIDPool.Allocate()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate user ID: %w", err)
	}

	// Create the client
	client, err := New(id, m.serverAddr, m.conferenceID, userID, callbacks, m.config.RequestTimeout)
	if err != nil {
		m.userIDPool.Release(userID) // Return the user ID to the pool
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Store the client
	m.clients[id] = client

	return client, nil
}

// CreateClientWithUserID creates a new virtual BFCP client with a specific user ID.
// This is useful when you need to use a specific user ID (e.g., from external system).
//
// Note: The user ID must be within the configured range and must not be in use.
func (m *Manager) CreateClientWithUserID(id string, userID uint16, callbacks Callbacks) (*Client, error) {
	if id == "" {
		return nil, errors.New("client ID cannot be empty")
	}
	if userID < m.config.UserIDRangeStart || userID > m.config.UserIDRangeEnd {
		return nil, fmt.Errorf("user ID %d is outside configured range [%d, %d]",
			userID, m.config.UserIDRangeStart, m.config.UserIDRangeEnd)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if client already exists
	if _, exists := m.clients[id]; exists {
		return nil, ErrClientAlreadyExists
	}

	// Try to allocate the specific user ID
	if err := m.userIDPool.AllocateSpecific(userID); err != nil {
		return nil, fmt.Errorf("user ID %d is already in use", userID)
	}

	// Create the client
	client, err := New(id, m.serverAddr, m.conferenceID, userID, callbacks, m.config.RequestTimeout)
	if err != nil {
		m.userIDPool.Release(userID) // Return the user ID to the pool
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Store the client
	m.clients[id] = client

	return client, nil
}

// GetClient retrieves a client by ID.
// Returns the client and true if found, or nil and false if not found.
func (m *Manager) GetClient(id string) (*Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, exists := m.clients[id]
	return client, exists
}

// RemoveClient removes and closes a client by ID.
// The client's user ID is returned to the pool for reuse.
//
// Returns an error if the client doesn't exist or if closing fails.
func (m *Manager) RemoveClient(id string) error {
	m.mu.Lock()
	client, exists := m.clients[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("client %s not found", id)
	}
	delete(m.clients, id)
	m.mu.Unlock()

	// Close the client (releases floors and disconnects)
	if err := client.Close(); err != nil {
		// Even if Close fails, we still return the user ID to the pool
		m.userIDPool.Release(client.UserID())
		return fmt.Errorf("error closing client: %w", err)
	}

	// Return user ID to pool
	m.userIDPool.Release(client.UserID())

	return nil
}

// RemoveAll removes and closes all managed clients.
// Returns the first error encountered, but continues removing all clients.
func (m *Manager) RemoveAll() error {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.clients = make(map[string]*Client)
	m.mu.Unlock()

	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.userIDPool.Release(client.UserID())
	}

	return firstErr
}

// ListClients returns information about all managed clients.
func (m *Manager) ListClients() []*ClientInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]*ClientInfo, 0, len(m.clients))
	for _, client := range m.clients {
		status := client.GetStatus()
		infos = append(infos, &ClientInfo{
			ID:           status.ID,
			UserID:       status.UserID,
			Connected:    status.Connected,
			ActiveFloors: status.ActiveFloors,
		})
	}

	return infos
}

// Count returns the number of currently managed clients.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}

// AvailableUserIDs returns the number of user IDs still available for allocation.
func (m *Manager) AvailableUserIDs() int {
	return m.userIDPool.Available()
}

// ServerAddr returns the BFCP server address used by this manager.
func (m *Manager) ServerAddr() string {
	return m.serverAddr
}

// ConferenceID returns the conference ID used by this manager.
func (m *Manager) ConferenceID() uint32 {
	return m.conferenceID
}

// UserIDPool manages allocation of unique user IDs for virtual clients.
// It ensures that each virtual client gets a unique user ID within the
// configured range.
type UserIDPool struct {
	start     uint16
	end       uint16
	allocated map[uint16]bool
	mu        sync.Mutex
}

// NewUserIDPool creates a new user ID pool with the given range.
func NewUserIDPool(start, end uint16) *UserIDPool {
	return &UserIDPool{
		start:     start,
		end:       end,
		allocated: make(map[uint16]bool),
	}
}

// Allocate allocates the next available user ID from the pool.
// Returns an error if no user IDs are available.
func (p *UserIDPool) Allocate() (uint16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Find first available ID
	for id := p.start; id <= p.end; id++ {
		if !p.allocated[id] {
			p.allocated[id] = true
			return id, nil
		}
	}

	return 0, errors.New("no user IDs available in pool")
}

// AllocateSpecific attempts to allocate a specific user ID.
// Returns an error if the ID is already allocated or outside the range.
func (p *UserIDPool) AllocateSpecific(id uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if id < p.start || id > p.end {
		return fmt.Errorf("user ID %d is outside range [%d, %d]", id, p.start, p.end)
	}

	if p.allocated[id] {
		return fmt.Errorf("user ID %d is already allocated", id)
	}

	p.allocated[id] = true
	return nil
}

// Release returns a user ID to the pool for reuse.
func (p *UserIDPool) Release(id uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allocated, id)
}

// Available returns the number of user IDs still available in the pool.
func (p *UserIDPool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := int(p.end - p.start + 1)
	return total - len(p.allocated)
}

// IsAllocated returns true if the given user ID is currently allocated.
func (p *UserIDPool) IsAllocated(id uint16) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allocated[id]
}
