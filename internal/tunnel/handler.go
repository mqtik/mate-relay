package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/mqtik/mate-relay/internal/storage"
)

type ctlMsg struct {
	Type     string `json:"type"`
	StreamID string `json:"streamId,omitempty"`
}

type Handler struct {
	Registry *Registry
	DB       *storage.DB
}

func NewHandler(reg *Registry, db *storage.DB) *Handler {
	return &Handler{Registry: reg, DB: db}
}

func (h *Handler) ServeControl(w http.ResponseWriter, r *http.Request) {
	dev, ok := deviceFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		log.Printf("control ws accept: %v", err)
		return
	}

	cc := newControlConn(dev.MacID)
	replaced := h.Registry.Register(dev.MacID, cc)
	if replaced != nil {
		replaced.Cancel()
	}
	defer h.Registry.Unregister(dev.MacID, cc)
	defer cc.Cancel()

	if err := h.DB.UpdateLastSeen(dev.ID); err != nil {
		log.Printf("update last seen %s: %v", dev.ID, err)
	}

	log.Printf("control: mac %s connected (%s)", dev.MacID, dev.Name)

	ctx := cc.Context()
	go func() {
		for {
			select {
			case msg, ok := <-cc.Sends:
				if !ok {
					return
				}
				writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := wsConn.Write(writeCtx, websocket.MessageText, msg)
				cancel()
				if err != nil {
					cc.Cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, data, err := wsConn.Read(ctx)
		if err != nil {
			break
		}
		var msg ctlMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "ping":
			pong := []byte(`{"type":"pong"}`)
			cc.Sends <- pong
		}
	}

	wsConn.Close(websocket.StatusNormalClosure, "")
	log.Printf("control: mac %s disconnected", dev.MacID)
}

func (h *Handler) ServeStream(w http.ResponseWriter, r *http.Request) {
	streamID := r.URL.Query().Get("id")
	if streamID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		log.Printf("stream ws accept: %v", err)
		return
	}

	netConn := websocket.NetConn(r.Context(), wsConn, websocket.MessageBinary)

	if err := h.Registry.AcceptStream(streamID, netConn); err != nil {
		wsConn.Close(websocket.StatusPolicyViolation, "unknown stream")
		log.Printf("accept stream %s: %v", streamID, err)
		return
	}
}

func proxyConns(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copy := func(dst, src net.Conn) {
		io.Copy(dst, src)
		dst.Close()
		done <- struct{}{}
	}
	go copy(a, b)
	go copy(b, a)
	<-done
}

func Proxy(ctx context.Context, reg *Registry, macID string, clientConn net.Conn, timeout time.Duration) error {
	streamID, waiter, err := reg.OpenStream(macID)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	_ = streamID

	select {
	case macConn, ok := <-waiter:
		if !ok || macConn == nil {
			return ErrMacOffline
		}
		go proxyConns(clientConn, macConn)
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for mac to connect stream")
	case <-ctx.Done():
		return ctx.Err()
	}
}

type contextKey int

const deviceContextKey contextKey = 0

func ContextWithDevice(ctx context.Context, dev *storage.Device) context.Context {
	return context.WithValue(ctx, deviceContextKey, dev)
}

func deviceFromContext(ctx context.Context) (*storage.Device, bool) {
	dev, ok := ctx.Value(deviceContextKey).(*storage.Device)
	return dev, ok && dev != nil
}
