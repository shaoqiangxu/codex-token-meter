package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type encodedSnapshot struct {
	Body, Gzip []byte
	Created    time.Time
}

// Bound both work and memory across browser tabs, reconnects, and old clients.
// The short cache is internal only; HTTP responses remain private/no-store.
type snapshotCache struct {
	mu      sync.Mutex
	entries map[string]*encodedSnapshot
}

func (c *snapshotCache) get(ctx context.Context, key string, build func() any) (*encodedSnapshot, error) {
	requestedAt := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A response built after this request arrived is fresh for this waiter,
	// even if other ranges used up the TTL while it waited for the build lock.
	if entry := c.entries[key]; entry != nil && (time.Since(entry.Created) < time.Second || !entry.Created.Before(requestedAt)) {
		return entry, nil
	}
	value := build()
	if v, ok := value.(map[string]any); ok && v["error"] != nil {
		return nil, errors.New("snapshot unavailable")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	z, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if _, err := z.Write(body); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	entry := &encodedSnapshot{Body: body, Gzip: compressed.Bytes(), Created: time.Now()}
	if c.entries == nil {
		c.entries = map[string]*encodedSnapshot{}
	}
	if len(c.entries) >= 8 {
		var oldestKey string
		var oldest time.Time
		for k, candidate := range c.entries {
			if oldestKey == "" || candidate.Created.Before(oldest) {
				oldestKey, oldest = k, candidate.Created
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = entry
	return entry, nil
}

func (r dashboardRange) cacheKey() string {
	switch r.Period {
	case "today", "week", "month":
		return r.Period + "|" + rangeBound(r.Start) // A calendar rollover is never cached as yesterday.
	case "24h", "all":
		return r.Period
	default:
		key := "custom|" + rangeBound(r.Start)
		if !r.OpenEnded {
			key += "|" + rangeBound(r.End)
		}
		return key
	}
}

func (s *server) cachedSnapshot(ctx context.Context, r dashboardRange) (*encodedSnapshot, error) {
	s.hub.mu.Lock()
	revision := s.hub.seq
	s.hub.mu.Unlock()
	return s.snapshots.get(ctx, fmt.Sprintf("%s|%d", r.cacheKey(), revision), func() any { return s.buildSnapshotForRange(r) })
}

func (s *server) snapshotForRange(r dashboardRange) any {
	entry, err := s.cachedSnapshot(context.Background(), r)
	if err != nil {
		return map[string]any{"error": "snapshot unavailable"}
	}
	return json.RawMessage(entry.Body)
}
