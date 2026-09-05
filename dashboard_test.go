package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDashboardCalendarAndRollingRanges(t *testing.T) {
	now := parseTime("2026-09-06T16:30:45Z") // Monday 00:30 in Beijing, Sunday in UTC.
	for period, want := range map[string]string{
		"today": "2026-09-06T16:00:00Z", "24h": "2026-09-05T16:30:45Z",
		"week": "2026-09-06T16:00:00Z", "month": "2026-08-31T16:00:00Z", "all": "1970-01-01T00:00:00Z",
	} {
		r, err := resolveDashboardRange(url.Values{"period": {period}}, now)
		if err != nil || !r.Start.Equal(parseTime(want)) || !r.End.Equal(now) {
			t.Fatalf("%s: %+v %v", period, r, err)
		}
	}
	r, err := resolveDashboardRange(url.Values{"period": {"custom"}, "from": {"2026-09-05T00:00:00+08:00"}, "to": {"2026-09-06T00:00:00+08:00"}}, now)
	if err != nil || r.Start.Format(time.RFC3339) != "2026-09-04T16:00:00Z" || r.End.Sub(r.Start) != 24*time.Hour {
		t.Fatalf("custom timezone: %+v %v", r, err)
	}
	for _, raw := range []string{"period=invalid", "period=custom", "from=invalid", "to=2026-09-04T00:00:00Z", "from=2026-09-05T00:00:00Z&to=wrong", "from=2026-09-06T00:00:00Z&to=2026-09-05T00:00:00Z"} {
		q, _ := url.ParseQuery(raw)
		if _, err := resolveDashboardRange(q, now); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestDashboardMidnightIsInclusiveEndExclusive(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	start := parseTime("2026-09-04T16:00:00Z")
	end := start.Add(24 * time.Hour)
	for i, at := range []time.Time{start.Add(-time.Millisecond), start, start.Add(time.Millisecond), end.Add(-time.Millisecond), end, end.Add(time.Millisecond)} {
		id := fmt.Sprintf("boundary-%d", i)
		e := UsageEvent{EventID: id, HostID: "h", ConversationID: id, SourceFileID: id, EventType: "exact_usage", Timestamp: at, Counts: counts(10, 0, 0, 2, 0), DataQuality: "EXACT", ParserVersion: parserVersion}
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.applyEvent(tx, e); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	totals := s.tokenTotalsBetween(start.In(dashboardLocation), end.In(dashboardLocation))
	if totals.TotalTokens != 36 {
		t.Fatalf("midnight totals: %+v", totals)
	}
	v := s.snapshotBetween(start, end).(map[string]any)
	if len(v["sessions"].([]map[string]any)) != 3 {
		t.Fatal("session and total boundaries disagree")
	}
	_, costs := rangeCosts(s.db, start, end)
	if len(costs) != 3 {
		t.Fatal("pricing and total boundaries disagree")
	}
}

func TestBulkPricingPreservesHistoryAndPerEventThreshold(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	boundary := "2026-09-05T00:00:00Z"
	_, err := s.db.Exec(`UPDATE prices SET effective_from='2026-09-01T00:00:00Z';
	UPDATE prices SET effective_to=? WHERE provider='openai';
	INSERT INTO prices(provider,plan_profile,model,effective_from,input_rate,cached_input_rate,cache_write_rate,output_rate,long_context_threshold,long_input_multiplier,long_output_multiplier,currency,source_name,verified_at)
	VALUES('openai','API','gpt-5.6-sol',?,8,0.8,0,40,272000,2,1.5,'USD','test',?)`, boundary, boundary, boundary)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := loadPriceRules(s.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{parseTime(boundary).Add(-time.Second), parseTime(boundary), parseTime(boundary).Add(time.Hour)} {
		for _, c := range []TokenCounts{counts(200000, 120000, 0, 1000, 0), counts(300000, 150000, 20, 3000, 0), {InputTokens: 5000, OutputTokens: 20}} {
			for _, target := range [][3]string{{"openai", "API", "gpt-5.6-sol"}, {"vercel", "AI Gateway public", "openai/gpt-5.6-sol"}, {"codex", "Plus/Pro Current", "gpt-5.6-sol"}} {
				want, e1 := costFor(s.db, target[0], target[1], target[2], c, at)
				got, e2 := costFromRules(rules, target[0], target[1], target[2], c, at)
				if e1 != nil || e2 != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("pricing diverged: %+v %+v %v %v", got, want, e1, e2)
				}
			}
		}
	}
}

func TestSSECoalescesWithoutHistoryOrIngestLock(t *testing.T) {
	h := newHub()
	noSnapshot := func() any { t.Fatal("notification-only or empty hub queried the database"); return nil }
	h.mark()
	h.publish(noSnapshot)
	c := make(chan sseMessage, 1)
	h.clients[c] = true
	for i := 0; i < 600; i++ {
		h.mark()
		h.publish(noSnapshot)
	}
	if len(c) != 1 || (<-c).ID != 601 {
		t.Fatal("slow reader did not receive just the latest update")
	}
	h.clients[c] = false
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.mark()
	go func() { h.publish(func() any { close(started); <-release; return 1 }); close(finished) }()
	<-started
	marked := make(chan struct{})
	go func() { h.mark(); close(marked) }()
	select {
	case <-marked:
	case <-time.After(time.Second):
		t.Error("snapshot holds the ingest notification lock")
	}
	close(release)
	<-finished
	if !h.dirty {
		t.Fatal("lost notification arriving during snapshot generation")
	}
}

func TestNotificationReconnectDoesNotReplaySnapshots(t *testing.T) {
	h := newHub()
	for i := 0; i < 600; i++ {
		h.mark()
		h.publish(func() any { return "old" })
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/events?notify=1", nil).WithContext(ctx)
	r.Header.Set("Last-Event-ID", "1")
	w := httptest.NewRecorder()
	h.serve(w, r, func() any { t.Fatal("notify connection fetched a snapshot"); return nil })
	if w.Body.String() != "event: ready\ndata: {}\n\n" || len(h.clients) != 0 {
		t.Fatalf("replayed history: %s", w.Body.String())
	}
}

func TestSnapshotCompressionAndInvalidRange(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	r := httptest.NewRequest("GET", "/api/snapshot?period=today", nil)
	r.Header.Set("Accept-Encoding", "gzip, br")
	w := httptest.NewRecorder()
	s.serveSnapshot(w, r)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response: %d %v", w.Code, w.Header())
	}
	z, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(z)
	z.Close()
	var body map[string]any
	if json.Unmarshal(b, &body) != nil || body["timezone"] != "Asia/Shanghai" || body["range_end"] == nil {
		t.Fatalf("missing range metadata: %s", b)
	}
	w = httptest.NewRecorder()
	s.serveSnapshot(w, httptest.NewRequest("GET", "/api/snapshot?period=custom&from=wrong", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "开始") {
		t.Fatal("invalid filter did not produce a useful error")
	}
	if acceptsGzip("gzip;q=0.0, br") {
		t.Fatal("gzip explicitly disabled")
	}
}

func TestSnapshotIncludesMoreThan500Records(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 520; i++ {
		id := fmt.Sprintf("many-%d", i)
		e := UsageEvent{EventID: id, HostID: "h", ConversationID: id, ParentConversationID: "root", SourceFileID: id, EventType: "exact_usage", Timestamp: now, Counts: counts(10, 0, 0, 2, 0), DataQuality: "EXACT", ParserVersion: parserVersion}
		if err := s.applyEvent(tx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	v := s.snapshotBetween(now.Add(-time.Minute), now.Add(time.Minute)).(map[string]any)
	sessions := v["sessions"].([]map[string]any)
	var total int64
	for _, item := range sessions {
		total += item["total_tokens"].(int64)
	}
	if len(sessions) != 520 || total != v["totals"].(map[string]any)["total_tokens"].(int64) {
		t.Fatal("session list truncated or incomplete")
	}
}
