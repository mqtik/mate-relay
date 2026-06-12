package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/mqtik/mate-relay/internal/storage"
)

type ctlMsg struct {
	Type     string `json:"type"`
	StreamID string `json:"streamId,omitempty"`
}

type Handler struct {
	Registry      *Registry
	DB            *storage.DB
	ControlHost   string
	previewClient *http.Client
}

func NewHandler(reg *Registry, db *storage.DB, controlHost string) *Handler {
	return &Handler{
		Registry:    reg,
		DB:          db,
		ControlHost: controlHost,
		previewClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
			Timeout: 60 * time.Second,
		},
	}
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

	netConn := websocket.NetConn(context.Background(), wsConn, websocket.MessageBinary)

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

var blockedPreviewResponseHeaders = map[string]bool{
	"transfer-encoding": true,
	"connection":        true,
	"keep-alive":        true,
}

func (h *Handler) ServePreview(w http.ResponseWriter, r *http.Request) {
	// URL: /tunnel/preview/{macId}/{port}/{path...}
	// After http.StripPrefix("/tunnel/preview"), r.URL.Path = "/{macId}/{port}/..."
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 {
		http.Error(w, "bad path: expected /macId/port/...", http.StatusBadRequest)
		return
	}
	macId := parts[0]
	portStr := parts[1]
	subPath := "/"
	if len(parts) == 3 && parts[2] != "" {
		subPath = "/" + parts[2]
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	if !h.Registry.IsOnline(macId) {
		http.Error(w, "device offline", http.StatusBadGateway)
		return
	}

	dev, dbErr := h.DB.GetDeviceByMacID(macId)

	targetURL := fmt.Sprintf("https://%s.%s/api/mate/v1/web-preview-relay/%d%s",
		macId, h.ControlHost, port, subPath)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for k, vs := range r.Header {
		if strings.ToLower(k) == "host" {
			continue
		}
		proxyReq.Header[k] = vs
	}
	proxyReq.Header.Set("X-Mate-Request", "1")
	if dbErr == nil {
		proxyReq.Header.Set("X-Device-Fingerprint", dev.Fingerprint)
	}

	resp, err := h.previewClient.Do(proxyReq)
	if err != nil {
		log.Printf("preview proxy %s:%d%s: %v", macId, port, subPath, err)
		http.Error(w, "device unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		if blockedPreviewResponseHeaders[strings.ToLower(k)] {
			continue
		}
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
