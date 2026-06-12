package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"sync"
)

var ErrMacOffline = errors.New("mac offline")

type ControlConn struct {
	MacID  string
	Sends  chan []byte
	ctx    context.Context
	cancel context.CancelFunc
}

func newControlConn(macID string) *ControlConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &ControlConn{
		MacID:  macID,
		Sends:  make(chan []byte, 64),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (cc *ControlConn) Context() context.Context {
	return cc.ctx
}

func (cc *ControlConn) Cancel() {
	cc.cancel()
}

type streamWaiter struct {
	ch    chan net.Conn
	macID string
}

type Registry struct {
	mu         sync.Mutex
	macs       map[string]*ControlConn
	waiters    map[string]*streamWaiter
	macStreams map[string]map[string]bool
}

func NewRegistry() *Registry {
	return &Registry{
		macs:      make(map[string]*ControlConn),
		waiters:   make(map[string]*streamWaiter),
		macStreams: make(map[string]map[string]bool),
	}
}

func (r *Registry) Register(macID string, cc *ControlConn) (replaced *ControlConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replaced = r.macs[macID]
	r.macs[macID] = cc
	if r.macStreams[macID] == nil {
		r.macStreams[macID] = make(map[string]bool)
	}
	return replaced
}

func (r *Registry) Unregister(macID string, cc *ControlConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.macs[macID]; ok && current == cc {
		delete(r.macs, macID)
	}
	if streams, ok := r.macStreams[macID]; ok {
		for streamID := range streams {
			if w, ok2 := r.waiters[streamID]; ok2 {
				close(w.ch)
				delete(r.waiters, streamID)
			}
		}
		delete(r.macStreams, macID)
	}
}

func (r *Registry) OpenStream(macID string) (streamID string, waiter <-chan net.Conn, err error) {
	b := make([]byte, 16)
	if _, randErr := rand.Read(b); randErr != nil {
		return "", nil, randErr
	}
	streamID = hex.EncodeToString(b)
	ch := make(chan net.Conn, 1)

	r.mu.Lock()
	cc, ok := r.macs[macID]
	if !ok {
		r.mu.Unlock()
		return "", nil, ErrMacOffline
	}
	r.waiters[streamID] = &streamWaiter{ch: ch, macID: macID}
	if r.macStreams[macID] == nil {
		r.macStreams[macID] = make(map[string]bool)
	}
	r.macStreams[macID][streamID] = true
	r.mu.Unlock()

	msg := []byte(`{"type":"open","streamId":"` + streamID + `"}`)
	select {
	case cc.Sends <- msg:
	default:
	}

	return streamID, ch, nil
}

func (r *Registry) AcceptStream(streamID string, conn net.Conn) error {
	r.mu.Lock()
	w, ok := r.waiters[streamID]
	if !ok {
		r.mu.Unlock()
		return errors.New("stream not found: " + streamID)
	}
	delete(r.waiters, streamID)
	if w.macID != "" {
		if streams, ok2 := r.macStreams[w.macID]; ok2 {
			delete(streams, streamID)
		}
	}
	r.mu.Unlock()

	select {
	case w.ch <- conn:
		return nil
	default:
		return errors.New("stream waiter full: " + streamID)
	}
}

func (r *Registry) IsOnline(macID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.macs[macID]
	return ok
}
