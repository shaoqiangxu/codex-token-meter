package main

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestStartedAndCompletedIndependentOfUsage(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	r, _ := resolveDashboardRange(url.Values{"period": {"custom"}, "from": {"2026-01-01T00:00:00Z"}, "to": {"2026-01-02T00:00:00Z"}}, time.Now())
	before := s.buildSnapshotForRange(r).(map[string]any)
	e := UsageEvent{EventID: "started-only", HostID: "h", SourceFileID: groupingRoot, ConversationID: groupingRoot, EventType: "activity", RunState: "running", TurnID: "new-turn", Timestamp: time.Now()}
	queued := make(chan struct{}, 1)
	s.hub.taskClients = map[chan struct{}]bool{queued: true}
	ingestSynthetic(t, s, e)
	select {
	case <-queued:
	default:
		t.Fatal("start did not wake independent state lane")
	}
	if s.hub.dirty {
		t.Fatal("start without usage triggered historical accounting")
	}
	pulse := s.taskMessage().(map[string]any)
	tasks := pulse["runtime"].([]map[string]any)
	if len(tasks) != 1 || tasks[0]["runtime_state"] != "running" || tasks[0]["settled_this_turn"] != false {
		t.Fatal("started-only task invisible or falsely settled")
	}
	if len(s.snapshotBetween(time.Unix(0, 0), time.Now()).(map[string]any)["sessions"].([]map[string]any)) != 0 {
		t.Fatal("started-only polluted history")
	}
	e.EventID = "completed-no-usage"
	e.RunState = "idle"
	e.Timestamp = e.Timestamp.Add(time.Millisecond)
	ingestSynthetic(t, s, e)
	if s.runtimeViews()[0]["runtime_state"] != "idle" || s.runtimeViews()[0]["settled_this_turn"] != false {
		t.Fatal("completion waited for usage")
	}
	e.EventID = "final-usage"
	e.EventType = "exact_usage"
	e.Timestamp = e.Timestamp.Add(time.Millisecond)
	e.Counts = counts(41, 0, 0, 1, 0)
	ingestSynthetic(t, s, e)
	if s.runtimeViews()[0]["runtime_state"] != "idle" || s.runtimeViews()[0]["settled_this_turn"] != true {
		t.Fatal("final usage lost completion or did not settle")
	}
	after := s.buildSnapshotForRange(r).(map[string]any)
	for _, key := range []string{"total_tokens", "input_tokens", "output_tokens", "api", "vercel", "credits"} {
		if !reflect.DeepEqual(before["totals"].(map[string]any)[key], after["totals"].(map[string]any)[key]) {
			t.Fatalf("history changed: %s", key)
		}
	}
}

func TestIncrementalEqualsFullIncludingExpiryAndReprice(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now().UTC()
	q := url.Values{"period": {"24h"}}
	r, _ := resolveDashboardRange(q, now)
	e := UsageEvent{EventID: "base-in-range", HostID: "h", SourceFileID: groupingRoot, ConversationID: groupingRoot, EventType: "exact_usage", Timestamp: r.Start.Add(time.Second), Counts: counts(100, 20, 0, 2, 0), DataQuality: "EXACT"}
	ingestSynthetic(t, s, e)
	base := s.buildSnapshotForRange(r).(map[string]any)
	for i := 0; i < 4; i++ {
		e.EventID = itoa(int64(i))
		e.Timestamp = now.Add(-time.Second)
		e.Counts.InputTokens += int64(i + 1)
		e.Counts.TotalTokens += int64(i + 1)
		ingestSynthetic(t, s, e)
		if i == 2 {
			s.db.Exec("UPDATE prices SET input_rate=input_rate*1.1")
		}
		if i == 3 {
			r.Start = r.Start.Add(2 * time.Second)
			r.End = r.End.Add(2 * time.Second)
		}
		fast := s.buildSnapshot(r, base).(map[string]any)
		full := s.buildSnapshotForRange(r).(map[string]any)
		for _, key := range []string{"totals", "sessions", "project_totals"} {
			// Host/runtime activity is time-dependent, not usage arithmetic.
			if key == "totals" {
				for _, f := range []string{"total_tokens", "input_tokens", "output_tokens", "api", "vercel", "credits"} {
					if !reflect.DeepEqual(fast[key].(map[string]any)[f], full[key].(map[string]any)[f]) {
						t.Fatalf("incremental %s diverged", f)
					}
				}
			} else if !reflect.DeepEqual(fast[key], full[key]) {
				t.Fatalf("incremental %s diverged", key)
			}
		}
		reference, bySession := rangeCosts(s.db, r.Start, r.End)
		cached, cachedSession := s.cachedRangeCosts(r.Start, r.End)
		if !reflect.DeepEqual(reference, cached) || !reflect.DeepEqual(bySession, cachedSession) {
			t.Fatal("cached prices differ from reference")
		}
		base = fast
	}
}

func TestLiveRangeIncludesFractionalUsageImmediately(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Date(2026, 9, 5, 14, 0, 0, 900000000, time.UTC)
	e := UsageEvent{EventID: "fractional", HostID: "h", SourceFileID: groupingRoot, ConversationID: groupingRoot, EventType: "exact_usage", Timestamp: now.Add(-100 * time.Millisecond), Counts: counts(99, 0, 0, 0, 0)}
	ingestSynthetic(t, s, e)
	r, _ := resolveDashboardRange(url.Values{"period": {"today"}}, now)
	value := s.buildSnapshotForRange(r).(map[string]any)
	if value["totals"].(map[string]any)["total_tokens"] != int64(99) {
		t.Fatal("current-second usage waits for the next second")
	}
	// Old seconds-only timestamps and fractional variants share exact bounds.
	for _, stamp := range []string{"2026-09-05T14:00:00Z", "2026-09-05T14:00:00.1Z", "2026-09-05T14:00:00.100000001Z"} {
		var got string
		s.db.QueryRow("SELECT "+usageClock+" FROM (SELECT ? timestamp)", stamp).Scan(&got)
		if got != usageBound(parseTime(stamp)) {
			t.Fatalf("bad timestamp normalization: %s", stamp)
		}
	}
	b, _ := json.Marshal(value)
	if !json.Valid(b) {
		t.Fatal("invalid frame")
	}
}
