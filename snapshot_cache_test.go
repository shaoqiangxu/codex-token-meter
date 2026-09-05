package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotCacheCoalescesConcurrentBrowsers(t *testing.T) {
	var cache snapshotCache
	var builds atomic.Int32
	start, release := make(chan struct{}), make(chan struct{})
	build := func() any {
		if builds.Add(1) == 1 {
			close(start)
		}
		<-release
		return map[string]any{"total": 123, "period": "today"}
	}
	var wg sync.WaitGroup
	results := make(chan *encodedSnapshot, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := cache.get(context.Background(), "today", build)
			if err != nil {
				t.Error(err)
				return
			}
			results <- entry
		}()
	}
	<-start
	close(release)
	wg.Wait()
	close(results)
	if builds.Load() != 1 {
		t.Fatalf("100 viewers caused %d builds", builds.Load())
	}
	var first *encodedSnapshot
	for entry := range results {
		if first == nil {
			first = entry
		}
		if entry != first || len(entry.Gzip) == 0 {
			t.Fatal("responses did not share pre-encoded data")
		}
	}
	// Changing range is isolated; expired data must rebuild, not live forever.
	other, _ := cache.get(context.Background(), "24h", func() any { return 24 })
	if other == first {
		t.Fatal("different ranges shared the same cached response")
	}
	first.Created = time.Now().Add(-2 * time.Second)
	fresh, _ := cache.get(context.Background(), "today", func() any { return 456 })
	if fresh == first {
		t.Fatal("expired snapshot reused")
	}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		cache.get(context.Background(), key, func() any { return key })
	}
	if len(cache.entries) > 8 {
		t.Fatal("snapshot cache grew without a bound")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.get(ctx, "cancelled", func() any { t.Fatal("cancelled request built data"); return nil }); err == nil {
		t.Fatal("cancelled request accepted")
	}
}

func TestSnapshotCacheKeysSeparateDaysAndCustomRanges(t *testing.T) {
	today := dashboardRange{Period: "today", Start: parseTime("2026-09-04T16:00:00Z")}
	tomorrow := today
	tomorrow.Start = tomorrow.Start.Add(24 * time.Hour)
	if today.cacheKey() == tomorrow.cacheKey() {
		t.Fatal("calendar rollover would show yesterday")
	}
	fixed := dashboardRange{Period: "custom", Start: today.Start, End: today.Start.Add(time.Hour)}
	live := fixed
	live.OpenEnded = true
	if fixed.cacheKey() == live.cacheKey() {
		t.Fatal("fixed custom end confused with now")
	}
}
