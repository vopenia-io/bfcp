package bfcp

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// releaseTestClient speaks BFCP version 1 over UDP, like a Poly X30.
type releaseTestClient struct {
	t    *testing.T
	conn *net.UDPConn
	tid  uint16
}

func newReleaseTestClient(t *testing.T) (*Server, *releaseTestClient) {
	t.Helper()
	server := udpServer(t, true)
	server.CreateFloor(1)
	server.Serve()

	conn, err := net.DialUDP("udp", nil, server.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &releaseTestClient{t: t, conn: conn}

	c.tid++
	c.send(NewMessage(PrimitiveHello, 1, c.tid, 1))
	if msg := c.read(time.Second); msg.Primitive != PrimitiveHelloAck {
		t.Fatalf("got %s, want HelloAck", msg.Primitive)
	}
	return server, c
}

func (c *releaseTestClient) send(msg *Message) {
	c.t.Helper()
	data, err := msg.Encode()
	if err != nil {
		c.t.Fatalf("encode %s: %v", msg.Primitive, err)
	}
	if _, err := c.conn.Write(data); err != nil {
		c.t.Fatalf("send %s: %v", msg.Primitive, err)
	}
}

func (c *releaseTestClient) readMaybe(within time.Duration) *Message {
	c.t.Helper()
	buf := make([]byte, 2048)
	if err := c.conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		c.t.Fatalf("deadline: %v", err)
	}
	n, err := c.conn.Read(buf)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return nil
	}
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	msg, err := Decode(buf[:n])
	if err != nil {
		c.t.Fatalf("decode: %v", err)
	}
	return msg
}

func (c *releaseTestClient) read(within time.Duration) *Message {
	c.t.Helper()
	msg := c.readMaybe(within)
	if msg == nil {
		c.t.Fatalf("no BFCP message within %v", within)
	}
	return msg
}

// expectStatus reads a FloorRequestStatus and returns its transaction and floor request IDs.
func (c *releaseTestClient) expectStatus(want RequestStatus) (uint16, uint16) {
	c.t.Helper()
	msg := c.read(time.Second)
	if msg.Primitive != PrimitiveFloorRequestStatus {
		code, _ := msg.GetErrorCode()
		c.t.Fatalf("got %s (error=%s), want FloorRequestStatus %s", msg.Primitive, code, want)
	}
	infos := msg.FloorRequestInfos()
	if len(infos) == 0 {
		c.t.Fatalf("FloorRequestStatus without FLOOR-REQUEST-INFORMATION")
	}
	if got, _ := infos[0].Status(); got != want {
		c.t.Fatalf("FloorRequestStatus %s, want %s", got, want)
	}
	return msg.TransactionID, infos[0].FloorRequestID
}

func (c *releaseTestClient) requestGranted() uint16 {
	c.t.Helper()
	c.tid++
	msg := NewMessage(PrimitiveFloorRequest, 1, c.tid, 1)
	msg.AddFloorID(1)
	c.send(msg)
	c.expectStatus(RequestStatusPending)
	_, requestID := c.expectStatus(RequestStatusGranted)
	return requestID
}

func (c *releaseTestClient) release(requestID uint16) uint16 {
	c.tid++
	msg := NewMessage(PrimitiveFloorRelease, 1, c.tid, 1)
	msg.AddFloorRequestID(requestID)
	c.send(msg)
	return c.tid
}

func TestUDPFloorRelease_OnlyAnswersTheReleasingSession(t *testing.T) {
	_, c := newReleaseTestClient(t)

	for range 3 {
		requestID := c.requestGranted()
		tid := c.release(requestID)
		gotTID, gotID := c.expectStatus(RequestStatusReleased)
		if gotTID != tid || gotID != requestID {
			t.Fatalf("released transaction %d request %d, want transaction %d request %d", gotTID, gotID, tid, requestID)
		}
		if extra := c.readMaybe(300 * time.Millisecond); extra != nil {
			t.Fatalf("unexpected %s (transaction %d) after the release response", extra.Primitive, extra.TransactionID)
		}
	}
}

func TestUDPFloorRelease_StaleRequestIDFromOwner(t *testing.T) {
	server, c := newReleaseTestClient(t)

	requestID := c.requestGranted()
	stale := requestID + 100
	c.release(stale)
	if _, gotID := c.expectStatus(RequestStatusReleased); gotID != stale {
		t.Fatalf("released request %d, want %d", gotID, stale)
	}
	floor, _ := server.GetFloor(1)
	if !floor.IsAvailable() {
		t.Fatalf("floor still %s after release", floor.GetState())
	}

	c.release(stale)
	msg := c.read(time.Second)
	if code, _ := msg.GetErrorCode(); msg.Primitive != PrimitiveError || code != ErrorFloorRequestIDDoesNotExist {
		t.Fatalf("got %s (error=%s) for a release with no floor held, want Error FloorRequestIDDoesNotExist", msg.Primitive, code)
	}
}
