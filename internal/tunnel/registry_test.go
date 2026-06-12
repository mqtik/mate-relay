package tunnel

import (
	"net"
	"testing"
	"time"
)

func TestRegistryRegisterUnregister(t *testing.T) {
	reg := NewRegistry()
	cc := newControlConn("mac1")
	replaced := reg.Register("mac1", cc)
	if replaced != nil {
		t.Fatal("expected no replaced conn on first register")
	}
	if !reg.IsOnline("mac1") {
		t.Fatal("expected mac1 online")
	}

	cc2 := newControlConn("mac1")
	replaced = reg.Register("mac1", cc2)
	if replaced != cc {
		t.Fatal("expected cc to be replaced")
	}

	reg.Unregister("mac1", cc2)
	if reg.IsOnline("mac1") {
		t.Fatal("expected mac1 offline after unregister")
	}
}

func TestOpenStreamOffline(t *testing.T) {
	reg := NewRegistry()
	_, _, err := reg.OpenStream("nonexistent")
	if err != ErrMacOffline {
		t.Fatalf("expected ErrMacOffline, got %v", err)
	}
}

func TestAcceptStreamHappyPath(t *testing.T) {
	reg := NewRegistry()
	cc := newControlConn("mac2")
	reg.Register("mac2", cc)

	streamID, waiter, err := reg.OpenStream("mac2")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	if err := reg.AcceptStream(streamID, server); err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}

	select {
	case conn := <-waiter:
		if conn != server {
			t.Fatal("expected the same conn")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for conn")
	}
}

func TestUnregisterClosesWaiters(t *testing.T) {
	reg := NewRegistry()
	cc := newControlConn("mac3")
	reg.Register("mac3", cc)

	_, waiter, err := reg.OpenStream("mac3")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	reg.Unregister("mac3", cc)

	select {
	case conn, ok := <-waiter:
		if ok && conn != nil {
			t.Fatal("expected closed channel or nil conn")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for waiter to close")
	}
}

func TestOpenStreamSendsMessage(t *testing.T) {
	reg := NewRegistry()
	cc := newControlConn("mac4")
	reg.Register("mac4", cc)

	streamID, _, err := reg.OpenStream("mac4")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	select {
	case msg := <-cc.Sends:
		if len(msg) == 0 {
			t.Fatal("expected non-empty message")
		}
		if string(msg[:len(`{"type":"open","streamId":"`)]) != `{"type":"open","streamId":"` {
			t.Fatalf("unexpected message format: %s", msg)
		}
		_ = streamID
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for send message")
	}
}
