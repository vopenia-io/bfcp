# Changelog

## [Unreleased] - Simplified Single Conference

### Changed

- **BREAKING**: Removed multi-conference management features for simplicity
- Server now manages a single conference directly with floor and session maps
- Simplified API: `CreateFloor()`, `GetFloor()`, `ReleaseFloor()` instead of conference manager
- Direct floor management without conference isolation overhead

### Removed

- `ConferenceManager` class and related infrastructure
- `GetConferenceManager()` method
- Multi-conference coordination logic
- `Conference` class (functionality merged into Server)
- Complex conference routing and virtual client tracking

### Migration Guide

#### Old Multi-Conference API
```go
server := NewServer(config)
cm := server.GetConferenceManager()
cm.CreateConference(conferenceID, sipUserID)
floorID, _ := cm.AllocateFloor(conferenceID)
```

#### New Simplified API
```go
server := bfcp.NewServer(config)
server.CreateFloor(1)  // Direct floor creation
floor, exists := server.GetFloor(1)
```

### Architecture Changes

**Before (Multi-Conference):**
```
Server
  └─ ConferenceManager
      ├─ Conference 1
      │   ├─ floors
      │   └─ sessions
      └─ Conference 2
          ├─ floors
          └─ sessions
```

**After (Simplified):**
```
Server
  ├─ floors (map[uint16]*FloorStateMachine)
  └─ sessions (map[string]*Session)
```

## Previous Releases

See git history for previous changelog entries.
