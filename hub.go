package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
	history []sseMessage
	clients map[chan sseMessage]struct{}
	dirty   bool
}

func newHub() *eventHub   { return &eventHub{clients: map[chan sseMessage]struct{}{}} }
func (h *eventHub) mark() { h.mu.Lock(); h.dirty = true; h.mu.Unlock() }
func (h *eventHub) run(snapshot func() any) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		h.mu.Lock()
		if !h.dirty {
			h.mu.Unlock()
			continue
		}
		h.dirty = false
		h.seq++
		b, _ := json.Marshal(snapshot())
		m := sseMessage{h.seq, b}
		h.history = append(h.history, m)
		if len(h.history) > 512 {
			h.history = h.history[len(h.history)-512:]
		}
		for c := range h.clients {
			select {
			case c <- m:
			default:
			}
		}
		h.mu.Unlock()
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
	b, _ := json.Marshal(snapshot())
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
	c := make(chan sseMessage, 16)
	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	h.mu.Lock()
	var replay []sseMessage
	for _, m := range h.history {
		if m.ID > last {
			replay = append(replay, m)
		}
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	for _, m := range replay {
		fmt.Fprintf(w, "id: %d\nevent: update\ndata: %s\n\n", m.ID, m.Data)
	}
	f.Flush()
	defer func() { h.mu.Lock(); delete(h.clients, c); h.mu.Unlock() }()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-c:
			fmt.Fprintf(w, "id: %d\nevent: update\ndata: %s\n\n", m.ID, m.Data)
			f.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			f.Flush()
		}
	}
}
