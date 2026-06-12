package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newE2EServer(t *testing.T) (*Registry, *httptest.Server) {
	t.Helper()
	reg := NewRegistry()
	h := &Handler{Registry: reg}
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/stream", h.ServeStream)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reg, srv
}

func wsBaseURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestEndToEndRelay(t *testing.T) {
	reg, srv := newE2EServer(t)
	wsBase := wsBaseURL(srv)

	cc := newControlConn("mac-e2e")
	reg.Register("mac-e2e", cc)
	defer reg.Unregister("mac-e2e", cc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case msg, ok := <-cc.Sends:
				if !ok {
					return
				}
				var m ctlMsg
				if err := json.Unmarshal(msg, &m); err != nil || m.Type != "open" {
					continue
				}
				go func(streamID string) {
					streamWs, _, err := websocket.Dial(ctx, wsBase+"/tunnel/stream?id="+streamID, &websocket.DialOptions{
						CompressionMode: websocket.CompressionDisabled,
					})
					if err != nil {
						t.Logf("mac stream dial: %v", err)
						return
					}
					defer streamWs.CloseNow()
					netConn := websocket.NetConn(ctx, streamWs, websocket.MessageBinary)
					io.Copy(netConn, netConn)
				}(m.StreamID)
			case <-ctx.Done():
				return
			}
		}
	}()

	phoneConn, relaySide := net.Pipe()
	defer phoneConn.Close()
	defer relaySide.Close()

	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- Proxy(ctx, reg, "mac-e2e", relaySide, 5*time.Second)
	}()

	select {
	case err := <-proxyDone:
		if err != nil {
			t.Fatalf("Proxy rendezvous failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Proxy to rendezvous")
	}

	want := []byte("hello relay end-to-end")
	if _, err := phoneConn.Write(want); err != nil {
		t.Fatalf("phone write: %v", err)
	}

	got := make([]byte, len(want))
	phoneConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(phoneConn, got); err != nil {
		t.Fatalf("phone read echo: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}
}

func TestEndToEndRelayLargePayload(t *testing.T) {
	reg, srv := newE2EServer(t)
	wsBase := wsBaseURL(srv)

	cc := newControlConn("mac-large")
	reg.Register("mac-large", cc)
	defer reg.Unregister("mac-large", cc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case msg, ok := <-cc.Sends:
				if !ok {
					return
				}
				var m ctlMsg
				if err := json.Unmarshal(msg, &m); err != nil || m.Type != "open" {
					continue
				}
				go func(streamID string) {
					streamWs, _, err := websocket.Dial(ctx, wsBase+"/tunnel/stream?id="+streamID, &websocket.DialOptions{
						CompressionMode: websocket.CompressionDisabled,
					})
					if err != nil {
						return
					}
					defer streamWs.CloseNow()
					netConn := websocket.NetConn(ctx, streamWs, websocket.MessageBinary)
					io.Copy(netConn, netConn)
				}(m.StreamID)
			case <-ctx.Done():
				return
			}
		}
	}()

	phoneConn, relaySide := net.Pipe()
	defer phoneConn.Close()
	defer relaySide.Close()

	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- Proxy(ctx, reg, "mac-large", relaySide, 5*time.Second)
	}()

	if err := <-proxyDone; err != nil {
		t.Fatalf("Proxy: %v", err)
	}

	payload := bytes.Repeat([]byte("X"), 512*1024)
	go func() {
		phoneConn.Write(payload)
	}()

	got := make([]byte, len(payload))
	phoneConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(phoneConn, got); err != nil {
		t.Fatalf("read large payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("large payload mismatch")
	}
}

func TestEndToEndRelayMacOffline(t *testing.T) {
	reg, _ := newE2EServer(t)

	phoneConn, relaySide := net.Pipe()
	defer phoneConn.Close()
	defer relaySide.Close()

	err := Proxy(context.Background(), reg, "not-registered", relaySide, time.Second)
	if !errors.Is(err, ErrMacOffline) {
		t.Fatalf("expected ErrMacOffline, got %v", err)
	}
}

func TestEndToEndRelayTimeout(t *testing.T) {
	reg, _ := newE2EServer(t)

	cc := newControlConn("mac-slow")
	reg.Register("mac-slow", cc)

	phoneConn, relaySide := net.Pipe()
	defer phoneConn.Close()
	defer relaySide.Close()

	err := Proxy(context.Background(), reg, "mac-slow", relaySide, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	select {
	case msg := <-cc.Sends:
		var m ctlMsg
		json.Unmarshal(msg, &m)
		if m.Type != "open" {
			t.Fatalf("expected open message, got %q", m.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("expected open message sent to mac before timeout")
	}
}

func TestEndToEndRelayConcurrentStreams(t *testing.T) {
	const N = 5

	reg, srv := newE2EServer(t)
	wsBase := wsBaseURL(srv)

	cc := newControlConn("mac-concurrent")
	reg.Register("mac-concurrent", cc)
	defer reg.Unregister("mac-concurrent", cc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case msg, ok := <-cc.Sends:
				if !ok {
					return
				}
				var m ctlMsg
				if err := json.Unmarshal(msg, &m); err != nil || m.Type != "open" {
					continue
				}
				go func(streamID string) {
					streamWs, _, err := websocket.Dial(ctx, wsBase+"/tunnel/stream?id="+streamID, &websocket.DialOptions{
						CompressionMode: websocket.CompressionDisabled,
					})
					if err != nil {
						return
					}
					defer streamWs.CloseNow()
					netConn := websocket.NetConn(ctx, streamWs, websocket.MessageBinary)
					io.Copy(netConn, netConn)
				}(m.StreamID)
			case <-ctx.Done():
				return
			}
		}
	}()

	type result struct {
		ok  bool
		idx int
	}
	results := make(chan result, N)

	for i := range N {
		go func(idx int) {
			phoneConn, relaySide := net.Pipe()
			defer phoneConn.Close()
			defer relaySide.Close()

			if err := Proxy(ctx, reg, "mac-concurrent", relaySide, 5*time.Second); err != nil {
				results <- result{false, idx}
				return
			}

			msg := []byte("stream payload for connection")
			phoneConn.Write(msg)
			got := make([]byte, len(msg))
			phoneConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err := io.ReadFull(phoneConn, got)
			results <- result{err == nil && bytes.Equal(got, msg), idx}
		}(i)
	}

	for range N {
		r := <-results
		if !r.ok {
			t.Errorf("stream %d failed", r.idx)
		}
	}
}

func TestEndToEndRelayUnregisterSweepsWaiters(t *testing.T) {
	reg, _ := newE2EServer(t)

	cc := newControlConn("mac-sweep")
	reg.Register("mac-sweep", cc)

	phoneConn, relaySide := net.Pipe()
	defer phoneConn.Close()
	defer relaySide.Close()

	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- Proxy(context.Background(), reg, "mac-sweep", relaySide, 5*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)
	reg.Unregister("mac-sweep", cc)

	select {
	case err := <-proxyDone:
		if err == nil {
			t.Fatal("expected error after mac unregistered, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out: Proxy should have returned after mac unregistered")
	}
}
