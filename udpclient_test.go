package bfcp

import (
	"net"
	"sync"
	"testing"
	"time"
)

func udpServer(t *testing.T, autoGrant bool) *Server {
	t.Helper()
	config := DefaultServerConfig("127.0.0.1:0", 1)
	config.Transport = TransportUDP
	config.AutoGrant = autoGrant
	server := NewServer(config)
	if err := server.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { server.Close() })
	return server
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// The client role (livekit-sip side) talks to the lib's own UDP server
// (the MCU side): Hello, FloorRequest granted, FloorRelease.
func TestUDPClientRequestAndRelease(t *testing.T) {
	remote := udpServer(t, true)
	remote.CreateFloor(0)
	local := udpServer(t, true)

	var mu sync.Mutex
	var remoteGranted, remoteReleased, clientGranted, clientReleased bool
	remote.OnFloorGranted = func(floorID, userID, requestID uint16) {
		mu.Lock()
		defer mu.Unlock()
		remoteGranted = floorID == 0 && userID == 2
	}
	remote.OnFloorReleased = func(floorID, userID uint16) {
		mu.Lock()
		defer mu.Unlock()
		remoteReleased = floorID == 0 && userID == 2
	}
	remote.Serve()
	local.Serve()

	client, err := local.NewUDPClient(UDPClientConfig{
		RemoteAddr:   remote.Addr().String(),
		ConferenceID: 1,
		UserID:       2,
		T1:           100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewUDPClient: %v", err)
	}
	defer client.Close()
	client.OnFloorGranted = func(floorID, requestID uint16) {
		mu.Lock()
		defer mu.Unlock()
		clientGranted = floorID == 0
	}
	client.OnFloorReleased = func(floorID uint16) {
		mu.Lock()
		defer mu.Unlock()
		clientReleased = floorID == 0
	}

	if err := client.Hello(); err != nil {
		t.Fatalf("Hello: %v", err)
	}

	requestID, err := client.RequestFloor(0)
	if err != nil {
		t.Fatalf("RequestFloor: %v", err)
	}
	if requestID == 0 {
		t.Fatal("expected a non-zero floor request ID")
	}
	waitFor(t, "grant on both sides", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return remoteGranted && clientGranted
	})
	if !client.HasFloor(0) {
		t.Fatal("client should hold floor 0")
	}

	if err := client.ReleaseFloor(0); err != nil {
		t.Fatalf("ReleaseFloor: %v", err)
	}
	waitFor(t, "release on both sides", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return remoteReleased && clientReleased
	})
	if client.HasFloor(0) {
		t.Fatal("client should not hold floor 0 after release")
	}
}

// A FloorStatus pushed by the server for another user's request is reported
// through OnFloorStatus and acknowledged.
func TestUDPClientRemoteFloorStatus(t *testing.T) {
	remote := udpServer(t, true)
	remote.CreateFloor(0)
	local := udpServer(t, true)

	var mu sync.Mutex
	var statuses []RequestStatus
	var acked bool
	remote.OnMessageIn = func(_ string, primitive string, _ uint8, _, _ uint32, _, _ uint16) {
		if primitive == PrimitiveFloorStatusAck.String() {
			mu.Lock()
			acked = true
			mu.Unlock()
		}
	}
	remote.Serve()
	local.Serve()

	client, err := local.NewUDPClient(UDPClientConfig{RemoteAddr: remote.Addr().String(), ConferenceID: 1, UserID: 2, T1: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewUDPClient: %v", err)
	}
	defer client.Close()
	client.OnFloorStatus = func(floorID, beneficiaryID uint16, status RequestStatus) {
		mu.Lock()
		defer mu.Unlock()
		if floorID == 0 {
			statuses = append(statuses, status)
		}
	}
	if err := client.Hello(); err != nil {
		t.Fatalf("Hello: %v", err)
	}

	remote.BroadcastFloorState(0, 7, RequestStatusGranted)
	waitFor(t, "remote floor status", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(statuses) > 0 && statuses[0] == RequestStatusGranted && acked
	})
}

// No server behind the address: Hello retransmits then fails.
func TestUDPClientHelloTimeout(t *testing.T) {
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer silent.Close()
	local := udpServer(t, true)
	local.Serve()

	client, err := local.NewUDPClient(UDPClientConfig{RemoteAddr: silent.LocalAddr().String(), ConferenceID: 1, UserID: 2, T1: 20 * time.Millisecond, MaxRetries: 3})
	if err != nil {
		t.Fatalf("NewUDPClient: %v", err)
	}
	defer client.Close()

	start := time.Now()
	if err := client.Hello(); err == nil {
		t.Fatal("expected Hello to fail without a server")
	}
	if elapsed := time.Since(start); elapsed < 140*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("unexpected retransmission window: %v", elapsed)
	}
}

// A FloorRequestStatus laid out per RFC 8855 (lengths counting the attribute
// header, R flag set) decodes into its grouped attributes.
func TestDecodeRFC8855FloorRequestStatus(t *testing.T) {
	data := []byte{
		0x50, 0x04, 0x00, 0x05, // Ver=2 R=1, FloorRequestStatus, 5 words
		0x00, 0x00, 0x00, 0x01, // conference 1
		0x00, 0x03, 0x00, 0x02, // transaction 3, user 2
		0x1F, 0x14, 0x00, 0x09, // FLOOR-REQUEST-INFORMATION M=1 len 20, request ID 9
		0x24, 0x08, 0x00, 0x09, // OVERALL-REQUEST-STATUS len 8, request ID 9
		0x0A, 0x04, 0x03, 0x00, // REQUEST-STATUS len 4: Granted, queue 0
		0x22, 0x08, 0x00, 0x00, // FLOOR-REQUEST-STATUS len 8, floor 0
		0x0A, 0x04, 0x03, 0x00, // REQUEST-STATUS len 4: Granted
	}
	msg, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !msg.IsResponse() {
		t.Error("R flag not decoded")
	}
	infos := msg.FloorRequestInfos()
	if len(infos) != 1 {
		t.Fatalf("expected 1 FLOOR-REQUEST-INFORMATION, got %d", len(infos))
	}
	info := infos[0]
	if info.FloorRequestID != 9 || !info.HasOverall || info.OverallStatus != RequestStatusGranted {
		t.Errorf("unexpected request info: %+v", info)
	}
	if len(info.Floors) != 1 || info.Floors[0].FloorID != 0 || info.Floors[0].Status != RequestStatusGranted {
		t.Errorf("unexpected floor statuses: %+v", info.Floors)
	}
	if id, ok := msg.GetFloorRequestID(); !ok || id != 9 {
		t.Errorf("GetFloorRequestID fallback: got %d, %v", id, ok)
	}
	if status, _, ok := msg.GetRequestStatus(); !ok || status != RequestStatusGranted {
		t.Errorf("GetRequestStatus fallback: got %s, %v", status, ok)
	}

	encoded, err := msg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if encoded[0] != 0x50 {
		t.Errorf("R flag not encoded: first octet 0x%02X", encoded[0])
	}
}

// Attribute lengths count the two header octets in version 2 too.
func TestEncodeV2AttributeLength(t *testing.T) {
	msg := NewMessage(PrimitiveFloorRequest, 1, 1, 2)
	msg.Version = ProtocolVersionRFC8855
	msg.AddFloorID(0)
	encoded, err := msg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if encoded[12] != 0x05 || encoded[13] != 0x04 {
		t.Errorf("FLOOR-ID TLV: got %02X %02X, want 05 04", encoded[12], encoded[13])
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if id, ok := decoded.GetFloorID(); !ok || id != 0 {
		t.Errorf("GetFloorID: got %d, %v", id, ok)
	}
}

// Close releases a transaction still waiting for its response.
func TestUDPClientCloseUnblocksTransaction(t *testing.T) {
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer silent.Close()
	local := udpServer(t, true)
	local.Serve()

	client, err := local.NewUDPClient(UDPClientConfig{RemoteAddr: silent.LocalAddr().String(), ConferenceID: 1, UserID: 2, T1: 2 * time.Second, MaxRetries: 3})
	if err != nil {
		t.Fatalf("NewUDPClient: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- client.Hello() }()
	time.Sleep(50 * time.Millisecond)
	client.Close()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Hello to fail after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Hello still blocked after Close")
	}
}
