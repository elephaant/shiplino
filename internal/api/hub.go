// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/elephaant/shiplino/pkg/engine"
)

// flushEvery coalesces updates: at most one push per session per interval
// (≤ 10 messages/s per session), however busy the agents are.
const flushEvery = 100 * time.Millisecond

// Message is what live clients receive.
type Message struct {
	T       string          `json:"t"`
	Session *engine.Session `json:"session,omitempty"`
	Project string          `json:"project,omitempty"`
}

// Hub fans session updates out to WebSocket clients.
type Hub struct {
	mu      sync.Mutex
	pending map[string]Message // coalesced by key: one message per key per flush
	clients map[chan []byte]struct{}
}

// NewHub returns an empty hub. Call Run to start delivering.
func NewHub() *Hub {
	return &Hub{pending: map[string]Message{}, clients: map[chan []byte]struct{}{}}
}

// Signal queues a message under key; a later signal with the same key
// before the next flush replaces it.
func (h *Hub) Signal(key string, m Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending[key] = m
}

// Publish queues session updates; later updates to the same session
// replace earlier ones until the next flush. Sessions are copied, so the
// caller may keep mutating its own.
func (h *Hub) Publish(list []*engine.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range list {
		c := *s
		c.Files = append([]string(nil), s.Files...)
		h.pending["session:"+s.ID] = Message{T: "session.update", Session: &c}
	}
}

// Run delivers queued updates until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.flush()
		}
	}
}

func (h *Hub) flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) == 0 {
		return
	}
	for _, m := range h.pending {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		for c := range h.clients {
			select {
			case c <- b:
			default: // slow client: drop it rather than block everyone
				delete(h.clients, c)
				close(c)
			}
		}
	}
	clear(h.pending)
}

func (h *Hub) subscribe() chan []byte {
	c := make(chan []byte, 256)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) unsubscribe(c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c)
	}
}

// live upgrades to a WebSocket and streams updates. websocket.Accept
// rejects cross-origin requests (Origin must match Host).
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	var opts *websocket.AcceptOptions
	if s.DevOrigin != "" {
		opts = &websocket.AcceptOptions{OriginPatterns: []string{strings.TrimPrefix(strings.TrimPrefix(s.DevOrigin, "http://"), "https://")}}
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := conn.CloseRead(r.Context()) // we never read; this handles pings and close
	c := s.hub.subscribe()
	defer s.hub.unsubscribe(c)

	hello, _ := json.Marshal(Message{T: "hello"})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-c:
			if !ok {
				conn.Close(websocket.StatusPolicyViolation, "too slow")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
