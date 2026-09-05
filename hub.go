package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type sseMessage struct {
	ID   int64
	Data []byte
}
type eventHub struct {
	mu      sync.Mutex
	seq     int64
	clients map[chan sseMessage]bool
	dirty   bool
}

func newHub() *eventHub   { return &eventHub{clients: map[chan sseMessage]bool{}} }
func (h *eventHub) mark() { h.mu.Lock(); h.dirty = true; h.mu.Unlock() }
func (h *eventHub) run(ctx context.Context, snapshot func() any) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.publish(snapshot)
		}
	}
}

func (h *eventHub) publish(snapshot func() any) {
	h.mu.Lock()
	if !h.dirty {
		h.mu.Unlock()
		return
	}
	h.dirty = false
	h.seq++
	seq := h.seq
	clients := make(map[chan sseMessage]bool, len(h.clients))
	full := false
	for c, notify := range h.clients {
		clients[c] = notify
		full = full || !notify
	}
	h.mu.Unlock()
	// Ingest also marks this hub. Do not hold its lock while querying SQLite.
	// With no viewers or notification-only viewers, no snapshot is needed.
	var b []byte
	if full {
		b, _ = json.Marshal(snapshot())
	}
	for c, notify := range clients {
		data := b
		if notify {
			data = []byte(`{}`)
		}
		m := sseMessage{seq, data}
		select {
		case c <- m:
		default:
			// Only the newest snapshot is useful to a slow reader.
			select {
			case <-c:
			default:
			}
			select {
			case c <- m:
			default:
			}
		}
	}
}

func (h *eventHub) serve(w http.ResponseWriter, r *http.Request, snapshot func() any) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	notify := r.URL.Query().Get("notify") == "1"
	c := make(chan sseMessage, 1)
	h.mu.Lock()
	h.clients[c] = notify
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.clients, c); h.mu.Unlock() }()
	if notify {
		fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	} else {
		b, _ := json.Marshal(snapshot())
		fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
	}
	// Connections start fresh; replaying old full snapshots would roll the UI
	// backwards and trigger hundreds of concurrent range requests.
	f.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-c:
			event := "update"
			if notify {
				event = "changed"
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", m.ID, event, m.Data); err != nil {
				return
			}
			f.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			f.Flush()
		}
	}
}
