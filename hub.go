package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type sseMessage struct {
	ID    int64
	Data  []byte
	Event string
}
type eventHub struct {
	mu                sync.Mutex
	seq               int64
	clients           map[chan sseMessage]bool
	dirty             bool
	epoch             string
	lastSent          time.Time
	interval          time.Duration
	heartbeatInterval time.Duration
	notification      func() any
	pulse             func() any
	numbers           func(url.Values) *sseMessage
	subscriptions     map[chan sseMessage]url.Values
	windowDue         func() bool
}

func newHub() *eventHub {
	epoch, err := randomToken(12)
	if err != nil {
		panic("cannot initialize stream epoch")
	}
	return &eventHub{clients: map[chan sseMessage]bool{}, subscriptions: map[chan sseMessage]url.Values{}, epoch: epoch, interval: 200 * time.Millisecond, heartbeatInterval: 5 * time.Second}
}
func (h *eventHub) mark() { h.mu.Lock(); h.seq++; h.dirty = true; h.mu.Unlock() }
func (h *eventHub) run(ctx context.Context, snapshot func() any) {
	t := time.NewTicker(h.interval)
	defer t.Stop()
	lastWindow := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if h.windowDue != nil && time.Since(lastWindow) >= time.Second {
				lastWindow = time.Now()
				if h.windowDue() {
					h.mark()
				}
			}
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
	seq := h.seq
	clients := make(map[chan sseMessage]bool, len(h.clients))
	subscriptions := map[chan sseMessage]url.Values{}
	for c, q := range h.subscriptions {
		subscriptions[c] = q
	}
	full := false
	for c, notify := range h.clients {
		clients[c] = notify
		full = full || !notify
	}
	h.mu.Unlock()
	// Ingest also marks this hub. Do not hold its lock while querying SQLite.
	// With no viewers or notification-only viewers, no snapshot is needed.
	var b []byte
	notification := []byte(`{}`)
	if h.notification != nil {
		notification, _ = json.Marshal(h.notification())
	}
	if full {
		b, _ = json.Marshal(snapshot())
	}
	frames := map[string]*sseMessage{}
	for c, notify := range clients {
		data := b
		if notify {
			data = notification
		}
		m := sseMessage{ID: seq, Data: data}
		if query := subscriptions[c]; query != nil && h.numbers != nil {
			key := query.Encode()
			if normalized, err := resolveDashboardRange(query, time.Now()); err == nil {
				key = normalized.cacheKey()
			}
			frame, exists := frames[key]
			if !exists {
				frame = h.numbers(query)
				frames[key] = frame
			}
			if frame == nil {
				continue
			}
			m = *frame
		}
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
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	stream := r.URL.Query().Get("stream") == "1"
	if stream {
		if _, err := resolveDashboardRange(r.URL.Query(), time.Now()); err != nil {
			http.Error(w, "invalid range", 400)
			return
		}
	}
	notify := r.URL.Query().Get("notify") == "1" || stream
	c := make(chan sseMessage, 1)
	h.mu.Lock()
	h.clients[c] = notify
	if stream {
		q := r.URL.Query()
		q.Del("stream")
		q.Del("notify")
		h.subscriptions[c] = q
	}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.clients, c); delete(h.subscriptions, c); h.mu.Unlock() }()
	if notify {
		data := []byte(`{}`)
		if h.notification != nil {
			data, _ = json.Marshal(h.notification())
		}
		fmt.Fprintf(w, "event: ready\ndata: %s\n\n", data)
	} else {
		b, _ := json.Marshal(snapshot())
		fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
	}
	// Connections start fresh; replaying old full snapshots would roll the UI
	// backwards and trigger hundreds of concurrent range requests.
	f.Flush()
	if h.pulse != nil {
		data, _ := json.Marshal(h.pulse())
		fmt.Fprintf(w, "event: heartbeat\ndata: %s\n\n", data)
		f.Flush()
	}
	ping := time.NewTicker(h.heartbeatInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-c:
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			if m.Event == "numbers" {
				var value map[string]any
				if json.Unmarshal(m.Data, &value) == nil {
					value["sse_emit_at"] = time.Now().UTC()
					m.Data, _ = json.Marshal(value)
				}
			}
			event := "update"
			if notify {
				event = "changed"
			}
			if m.Event != "" {
				event = m.Event
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", m.ID, event, m.Data); err != nil {
				return
			}
			f.Flush()
			h.mu.Lock()
			h.lastSent = time.Now().UTC()
			h.mu.Unlock()
		case <-ping.C:
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			value := any(map[string]any{"server_epoch": h.epoch, "server_time": time.Now().UTC()})
			if h.pulse != nil {
				value = h.pulse()
			}
			data, _ := json.Marshal(value)
			if _, err := fmt.Fprintf(w, "event: heartbeat\ndata: %s\n\n", data); err != nil {
				return
			}
			f.Flush()
			h.mu.Lock()
			h.lastSent = time.Now().UTC()
			h.mu.Unlock()
		}
	}
}
