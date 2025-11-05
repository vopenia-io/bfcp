package bfcp

import (
	"bytes"
	"testing"
	"time"
)

// TestMessageEncodeDecode tests the basic message encoding and decoding
func TestMessageEncodeDecode(t *testing.T) {
	// Create a test message
	original := NewMessage(PrimitiveFloorRequest, 12345, 1, 100)
	original.AddFloorID(1)
	original.AddBeneficiaryID(200)
	original.AddPriority(PriorityHigh)

	// Encode
	encoded, err := original.Encode()
	if err != nil {
		t.Fatalf("Failed to encode message: %v", err)
	}

	// Decode
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Failed to decode message: %v", err)
	}

	// Verify common header
	if decoded.Version != ProtocolVersion {
		t.Errorf("Version mismatch: got %d, want %d", decoded.Version, ProtocolVersion)
	}
	if decoded.Primitive != PrimitiveFloorRequest {
		t.Errorf("Primitive mismatch: got %s, want %s", decoded.Primitive, PrimitiveFloorRequest)
	}
	if decoded.ConferenceID != 12345 {
		t.Errorf("ConferenceID mismatch: got %d, want %d", decoded.ConferenceID, 12345)
	}
	if decoded.TransactionID != 1 {
		t.Errorf("TransactionID mismatch: got %d, want %d", decoded.TransactionID, 1)
	}
	if decoded.UserID != 100 {
		t.Errorf("UserID mismatch: got %d, want %d", decoded.UserID, 100)
	}

	// Verify attributes
	floorID, ok := decoded.GetFloorID()
	if !ok || floorID != 1 {
		t.Errorf("FloorID mismatch: got %d, want %d", floorID, 1)
	}

	beneficiaryID, ok := decoded.GetBeneficiaryID()
	if !ok || beneficiaryID != 200 {
		t.Errorf("BeneficiaryID mismatch: got %d, want %d", beneficiaryID, 200)
	}
}

// TestMessageHelloEncoding tests Hello message encoding
func TestMessageHelloEncoding(t *testing.T) {
	msg := NewMessage(PrimitiveHello, 1, 1, 1)

	primitives := []Primitive{PrimitiveFloorRequest, PrimitiveFloorRelease, PrimitiveHello}
	msg.AddSupportedPrimitives(primitives)

	attributes := []AttributeType{AttrFloorID, AttrBeneficiaryID, AttrPriority}
	msg.AddSupportedAttributes(attributes)

	// Encode and decode
	encoded, err := msg.Encode()
	if err != nil {
		t.Fatalf("Failed to encode Hello: %v", err)
	}

	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Failed to decode Hello: %v", err)
	}

	// Verify primitives
	primAttr := decoded.GetAttribute(AttrSupportedPrimitives)
	if primAttr == nil {
		t.Fatal("Missing SUPPORTED-PRIMITIVES attribute")
	}
	if len(primAttr.Value) != len(primitives) {
		t.Errorf("Primitives count mismatch: got %d, want %d", len(primAttr.Value), len(primitives))
	}

	// Verify attributes
	attrAttr := decoded.GetAttribute(AttrSupportedAttributes)
	if attrAttr == nil {
		t.Fatal("Missing SUPPORTED-ATTRIBUTES attribute")
	}
	if len(attrAttr.Value) != len(attributes) {
		t.Errorf("Attributes count mismatch: got %d, want %d", len(attrAttr.Value), len(attributes))
	}
}

// TestMessageErrorEncoding tests Error message encoding
func TestMessageErrorEncoding(t *testing.T) {
	msg := NewMessage(PrimitiveError, 1, 1, 1)
	msg.AddErrorCode(ErrorInvalidFloorID)
	msg.AddErrorInfo("Floor does not exist")

	encoded, err := msg.Encode()
	if err != nil {
		t.Fatalf("Failed to encode Error: %v", err)
	}

	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Failed to decode Error: %v", err)
	}

	errorCode, ok := decoded.GetErrorCode()
	if !ok || errorCode != ErrorInvalidFloorID {
		t.Errorf("ErrorCode mismatch: got %s, want %s", errorCode, ErrorInvalidFloorID)
	}

	errorInfo, ok := decoded.GetErrorInfo()
	if !ok || errorInfo != "Floor does not exist" {
		t.Errorf("ErrorInfo mismatch: got %s, want 'Floor does not exist'", errorInfo)
	}
}

// TestFloorStateMachine tests the floor state machine
func TestFloorStateMachine(t *testing.T) {
	fsm := NewFloorStateMachine(1, 12345)

	// Initial state should be Released
	if !fsm.IsAvailable() {
		t.Error("Floor should be available initially")
	}
	if fsm.GetState() != RequestStatusReleased {
		t.Errorf("Initial state should be Released, got %s", fsm.GetState())
	}

	// Request floor
	status, err := fsm.Request(100, 1, PriorityNormal)
	if err != nil {
		t.Fatalf("Failed to request floor: %v", err)
	}
	if status != RequestStatusPending {
		t.Errorf("Expected Pending status, got %s", status)
	}
	if fsm.GetOwner() != 100 {
		t.Errorf("Owner should be 100, got %d", fsm.GetOwner())
	}

	// Grant floor
	if err := fsm.Grant(); err != nil {
		t.Fatalf("Failed to grant floor: %v", err)
	}
	if !fsm.IsGranted() {
		t.Error("Floor should be granted")
	}

	// Release floor
	if err := fsm.Release(100); err != nil {
		t.Fatalf("Failed to release floor: %v", err)
	}
	if !fsm.IsAvailable() {
		t.Error("Floor should be available after release")
	}
}

// TestFloorStateMachineDeny tests denying a floor request
func TestFloorStateMachineDeny(t *testing.T) {
	fsm := NewFloorStateMachine(1, 12345)

	// Request floor
	_, err := fsm.Request(100, 1, PriorityNormal)
	if err != nil {
		t.Fatalf("Failed to request floor: %v", err)
	}

	// Deny floor
	if err := fsm.Deny(); err != nil {
		t.Fatalf("Failed to deny floor: %v", err)
	}
	if fsm.GetState() != RequestStatusDenied {
		t.Errorf("State should be Denied, got %s", fsm.GetState())
	}
}

// TestFloorRequestQueue tests the floor request queue
func TestFloorRequestQueue(t *testing.T) {
	queue := NewFloorRequestQueue()

	// Add requests with different priorities
	req1 := FloorRequest{FloorID: 1, FloorRequestID: 1, UserID: 100, Priority: PriorityLow}
	req2 := FloorRequest{FloorID: 1, FloorRequestID: 2, UserID: 101, Priority: PriorityHigh}
	req3 := FloorRequest{FloorID: 1, FloorRequestID: 3, UserID: 102, Priority: PriorityNormal}

	queue.Add(req1)
	queue.Add(req2)
	queue.Add(req3)

	if queue.Size() != 3 {
		t.Errorf("Queue size should be 3, got %d", queue.Size())
	}

	// Highest priority should be first
	next, ok := queue.GetNext()
	if !ok || next.Priority != PriorityHigh {
		t.Errorf("Next should be high priority request, got %d", next.Priority)
	}

	// Pop and verify order
	popped, _ := queue.PopNext()
	if popped.Priority != PriorityHigh {
		t.Errorf("Popped should be high priority, got %d", popped.Priority)
	}

	popped, _ = queue.PopNext()
	if popped.Priority != PriorityNormal {
		t.Errorf("Popped should be normal priority, got %d", popped.Priority)
	}

	popped, _ = queue.PopNext()
	if popped.Priority != PriorityLow {
		t.Errorf("Popped should be low priority, got %d", popped.Priority)
	}

	if queue.Size() != 0 {
		t.Errorf("Queue should be empty, got size %d", queue.Size())
	}
}

// TestMessageReadWrite tests reading and writing messages with io.Reader/Writer
func TestMessageReadWrite(t *testing.T) {
	// Create a message
	original := NewMessage(PrimitiveFloorRequest, 1, 1, 100)
	original.AddFloorID(1)
	original.AddBeneficiaryID(200)

	// Write to buffer
	var buf bytes.Buffer
	if err := WriteMessage(&buf, original); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	// Read from buffer
	decoded, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}

	// Verify
	if decoded.Primitive != original.Primitive {
		t.Errorf("Primitive mismatch: got %s, want %s", decoded.Primitive, original.Primitive)
	}
	if decoded.ConferenceID != original.ConferenceID {
		t.Errorf("ConferenceID mismatch: got %d, want %d", decoded.ConferenceID, original.ConferenceID)
	}
}

// TestSessionStateMachine tests the session state machine
func TestSessionStateMachine(t *testing.T) {
	ssm := NewSessionStateMachine(1, 100, "127.0.0.1:12345")

	// Initial state
	if ssm.GetState() != StateConnected {
		t.Errorf("Initial state should be Connected, got %s", ssm.GetState())
	}

	// Update activity
	time.Sleep(10 * time.Millisecond)
	before := ssm.GetIdleDuration()
	ssm.UpdateActivity()
	after := ssm.GetIdleDuration()

	if after >= before {
		t.Error("Idle duration should decrease after UpdateActivity")
	}

	// Set state
	ssm.SetState(StateFloorGranted)
	if ssm.GetState() != StateFloorGranted {
		t.Errorf("State should be FloorGranted, got %s", ssm.GetState())
	}

	// Set supported primitives
	primitives := []Primitive{PrimitiveFloorRequest, PrimitiveFloorRelease}
	ssm.SetSupportedPrimitives(primitives)

	// Set supported attributes
	attributes := []AttributeType{AttrFloorID, AttrBeneficiaryID}
	ssm.SetSupportedAttributes(attributes)
}

// TestRequestStatusString tests RequestStatus string conversion
func TestRequestStatusString(t *testing.T) {
	tests := []struct {
		status   RequestStatus
		expected string
	}{
		{RequestStatusPending, "Pending"},
		{RequestStatusAccepted, "Accepted"},
		{RequestStatusGranted, "Granted"},
		{RequestStatusDenied, "Denied"},
		{RequestStatusCancelled, "Cancelled"},
		{RequestStatusReleased, "Released"},
		{RequestStatusRevoked, "Revoked"},
	}

	for _, tt := range tests {
		if got := tt.status.String(); got != tt.expected {
			t.Errorf("RequestStatus.String() = %s, want %s", got, tt.expected)
		}
	}
}

// TestPrimitiveString tests Primitive string conversion
func TestPrimitiveString(t *testing.T) {
	tests := []struct {
		primitive Primitive
		expected  string
	}{
		{PrimitiveFloorRequest, "FloorRequest"},
		{PrimitiveFloorRelease, "FloorRelease"},
		{PrimitiveHello, "Hello"},
		{PrimitiveHelloAck, "HelloAck"},
		{PrimitiveError, "Error"},
		{PrimitiveFloorStatus, "FloorStatus"},
	}

	for _, tt := range tests {
		if got := tt.primitive.String(); got != tt.expected {
			t.Errorf("Primitive.String() = %s, want %s", got, tt.expected)
		}
	}
}

// TestErrorCodeString tests ErrorCode string conversion
func TestErrorCodeString(t *testing.T) {
	tests := []struct {
		errorCode ErrorCode
		expected  string
	}{
		{ErrorConferenceDoesNotExist, "ConferenceDoesNotExist"},
		{ErrorUserDoesNotExist, "UserDoesNotExist"},
		{ErrorUnknownPrimitive, "UnknownPrimitive"},
		{ErrorInvalidFloorID, "InvalidFloorID"},
	}

	for _, tt := range tests {
		if got := tt.errorCode.String(); got != tt.expected {
			t.Errorf("ErrorCode.String() = %s, want %s", got, tt.expected)
		}
	}
}

// TestConnectionRole tests ConnectionRole string conversion
func TestConnectionRole(t *testing.T) {
	tests := []struct {
		role     ConnectionRole
		expected string
	}{
		{RoleActive, "active"},
		{RolePassive, "passive"},
		{RoleActpass, "actpass"},
	}

	for _, tt := range tests {
		if got := tt.role.String(); got != tt.expected {
			t.Errorf("ConnectionRole.String() = %s, want %s", got, tt.expected)
		}
	}
}

// TestMessageGetAttribute tests getting attributes from a message
func TestMessageGetAttribute(t *testing.T) {
	msg := NewMessage(PrimitiveFloorRequest, 1, 1, 100)
	msg.AddFloorID(1)
	msg.AddBeneficiaryID(200)
	msg.AddPriority(PriorityHigh)

	// Test getting existing attributes
	if attr := msg.GetAttribute(AttrFloorID); attr == nil {
		t.Error("Should find FloorID attribute")
	}
	if attr := msg.GetAttribute(AttrBeneficiaryID); attr == nil {
		t.Error("Should find BeneficiaryID attribute")
	}
	if attr := msg.GetAttribute(AttrPriority); attr == nil {
		t.Error("Should find Priority attribute")
	}

	// Test getting non-existent attribute
	if attr := msg.GetAttribute(AttrErrorCode); attr != nil {
		t.Error("Should not find ErrorCode attribute")
	}
}

// BenchmarkMessageEncode benchmarks message encoding
func BenchmarkMessageEncode(b *testing.B) {
	msg := NewMessage(PrimitiveFloorRequest, 1, 1, 100)
	msg.AddFloorID(1)
	msg.AddBeneficiaryID(200)
	msg.AddPriority(PriorityHigh)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = msg.Encode()
	}
}

// BenchmarkMessageDecode benchmarks message decoding
func BenchmarkMessageDecode(b *testing.B) {
	msg := NewMessage(PrimitiveFloorRequest, 1, 1, 100)
	msg.AddFloorID(1)
	msg.AddBeneficiaryID(200)
	msg.AddPriority(PriorityHigh)

	encoded, _ := msg.Encode()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Decode(encoded)
	}
}
