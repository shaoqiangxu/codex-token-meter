package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func ingestSynthetic(t *testing.T, s *server, events ...UsageEvent) {
	t.Helper()
	if s.ingestTimes == nil {
		s.ingestTimes = map[string][]time.Time{}
	}
	if _, err := s.db.Exec("UPDATE agents SET token_hash=? WHERE host_id='h'", tokenHash("synthetic")); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(IngestBatch{HostID: "h", Events: events})
	r := httptest.NewRequest("POST", "/api/ingest", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	s.ingest(w, r)
	if w.Code != 200 {
		t.Fatalf("ingest %d: %s", w.Code, w.Body.String())
	}
}

func TestRealtimeAbsoluteFramesAndHeartbeatIsolation(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now().UTC().Add(-2 * time.Second)
	e := UsageEvent{EventID: "first", HostID: "h", ConversationID: groupingRoot, SourceFileID: groupingRoot, EventType: "exact_usage", Timestamp: now, Counts: counts(1000000, 0, 0, 20, 0), Model: "gpt-5.6-sol", DataQuality: "EXACT"}
	ingestSynthetic(t, s, e)
	r, _ := resolveDashboardRange(url.Values{"period": {"today"}}, time.Now())
	base := s.buildSnapshotForRange(r).(map[string]any)
	s.rememberNumericBase(r, base)
	for _, increase := range []int64{1, 23, 99} {
		e.EventID = fmt.Sprint(increase)
		e.Counts.InputTokens += increase
		e.Counts.TotalTokens += increase
		ingestSynthetic(t, s, e)
		frame := s.numericMessage(url.Values{"period": {"today"}})
		if frame.Event != "numbers" {
			t.Fatalf("expected absolute frame: %+v", frame)
		}
		var data map[string]any
		if err := json.Unmarshal(frame.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data["sessions"].([]any)) != 1 || data["totals"].(map[string]any)["total_tokens"] != float64(e.Counts.TotalTokens) {
			t.Fatalf("small increase lost: %s", frame.Data)
		}
		before := s.watermark()
		ingestSynthetic(t, s, e)
		ingestSynthetic(t, s)
		after := s.watermark()
		if before["revision"] != after["revision"] || before["ledger_revision"] != after["ledger_revision"] {
			t.Fatal("duplicate or heartbeat advanced data revision")
		}
		// The cheap heartbeat exposes no historical sessions or project arrays.
		heartbeat := s.heartbeat().(map[string]any)
		if heartbeat["sessions"] != nil || heartbeat["totals"] != nil || heartbeat["project_totals"] != nil {
			t.Fatal("heartbeat carries history")
		}
	}
	prior := s.tokenTotalsBetween(time.Unix(0, 0), time.Now())
	epoch := s.hub.epoch
	s.hub = newHub()
	if s.watermark()["server_epoch"] == epoch || s.tokenTotalsBetween(time.Unix(0, 0), time.Now()) != prior {
		t.Fatal("restart failed to change epoch or changed accounting")
	}
}

func TestNumericHistoricalRangeAndGap(t *testing.T) {
	before := map[string]any{"revision": int64(1), "range_end": time.Unix(3, 0), "sessions": []map[string]any{{"host_id": "h", "conversation_id": "a", "total_tokens": int64(10)}, {"host_id": "h", "conversation_id": "b", "total_tokens": int64(20)}}}
	after := map[string]any{"revision": int64(4), "range_end": time.Unix(5, 0), "totals": map[string]any{"total_tokens": int64(7)}, "sessions": []map[string]any{{"host_id": "h", "conversation_id": "a", "total_tokens": int64(7)}}}
	frame := numericDifference(before, after)
	if frame["base_revision"] != int64(1) || len(frame["removed"].([]string)) != 1 || len(frame["sessions"].([]map[string]any)) != 1 {
		t.Fatal("lost correction or removal")
	}
	s, done := testServerDB(t)
	defer done()
	q := url.Values{"period": {"custom"}, "from": {"2026-01-01T00:00:00Z"}, "to": {"2026-01-02T00:00:00Z"}}
	r, _ := resolveDashboardRange(q, time.Now())
	base := s.buildSnapshotForRange(r).(map[string]any)
	s.rememberNumericBase(r, base)
	ingestSynthetic(t, s, UsageEvent{EventID: "now", HostID: "h", SourceFileID: "now", ConversationID: "now", EventType: "exact_usage", Timestamp: time.Now().Add(-2 * time.Second), Counts: counts(99, 0, 0, 0, 0)})
	message := s.numericMessage(q)
	var data map[string]any
	json.Unmarshal(message.Data, &data)
	if data["query_key"] != r.cacheKey() || data["totals"].(map[string]any)["total_tokens"] != float64(0) {
		t.Fatal("today leaked into historical range")
	}
	missing := s.numericMessage(url.Values{"period": {"24h"}})
	if missing.Event != "resync" {
		t.Fatal("unknown base did not request snapshot")
	}
}

func TestNumericGETDoesNotConsumePublishedBase(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	q := url.Values{"period": {"today"}}
	r, _ := resolveDashboardRange(q, time.Now())
	base := s.rememberNumericBase(r, s.buildSnapshotForRange(r).(map[string]any))
	e := UsageEvent{EventID: "during-get", HostID: "h", SourceFileID: groupingRoot, ConversationID: groupingRoot, EventType: "exact_usage", Timestamp: time.Now().Add(-2 * time.Second), Counts: counts(23, 0, 0, 0, 0)}
	ingestSynthetic(t, s, e)
	newer := s.rememberNumericBase(r, s.buildSnapshotForRange(r).(map[string]any))
	if newer["revision"].(int64) <= base["revision"].(int64) {
		t.Fatal("GET did not advance query revision")
	}
	message := s.numericMessage(q)
	var data map[string]any
	json.Unmarshal(message.Data, &data)
	if data["base_revision"] != float64(base["revision"].(int64)) || data["totals"].(map[string]any)["total_tokens"] != float64(23) {
		t.Fatal("GET consumed the pending stream base")
	}
	if float64(s.rememberNumericBase(r, base)["revision"].(int64)) != data["revision"] {
		t.Fatal("late build replaced current query view")
	}
}

func TestRuntimeChildCompletionDoesNotClearParent(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	e := UsageEvent{EventID: "parent-running", HostID: "h", SourceFileID: groupingRoot, ConversationID: groupingRoot, EventType: "activity", RunState: "running", Timestamp: time.Now()}
	ingestSynthetic(t, s, e)
	e.EventID = "child-idle"
	e.ConversationID = "ctco_child"
	e.ParentConversationID = groupingRoot
	e.RunState = "idle"
	e.Timestamp = e.Timestamp.Add(time.Second)
	ingestSynthetic(t, s, e)
	rows := s.runtimeViews()
	if len(rows) != 2 {
		t.Fatal("child overwrote parent evidence")
	}
	for _, row := range rows {
		if row["conversation_id"] == groupingRoot && row["runtime_state"] != "running" {
			t.Fatal("child declared parent idle")
		}
	}
}

func TestRealtimeConfigThresholds(t *testing.T) {
	c := (RealtimeConfig{HeartbeatMS: 10000, DelayedMS: 50000}).normalized()
	if c.OfflineMS <= c.DelayedMS {
		t.Fatal("offline threshold precedes delayed")
	}
	c = (RealtimeConfig{HeartbeatMS: 10000}).normalized()
	if c.DelayedMS < c.HeartbeatMS*2 {
		t.Fatal("delayed before two heartbeats")
	}
}

func TestRuntimeSeparateFromExactAndVisibleOnly(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	pc := parseContext{conversationID: groupingRoot}
	parse := func(offset int64, kind, payload string) UsageEvent {
		t.Helper()
		raw := fmt.Sprintf(`{"timestamp":%q,"type":%q,"payload":%s}`, time.Now().Add(time.Duration(offset)*time.Second).UTC().Format(time.RFC3339Nano), kind, payload)
		e, ok := parseCodexLine([]byte(raw), "h", groupingRoot, offset, &pc)
		if !ok {
			t.Fatalf("missing %s", kind)
		}
		return *e
	}
	start := parse(0, "event_msg", `{"type":"task_started","turn_id":"one"}`)
	ingestSynthetic(t, s, start)
	live := parse(1, "event_msg", `{"type":"agent_message_delta","delta":"visible answer","turn_id":"one"}`)
	ingestSynthetic(t, s, live)
	ingestSynthetic(t, s, live)
	if got := s.runtimeViews()[0]["live_estimate"].(int64); got != localEstimateTokens("visible answer") {
		t.Fatal("estimate duplicated")
	}
	exact := parse(2, "event_msg", `{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}}`)
	ingestSynthetic(t, s, exact)
	runtime := s.runtimeViews()[0]
	if runtime["live_estimate"] != int64(0) || runtime["runtime_state"] != "running" {
		t.Fatal("EXACT falsely marked idle or failed to replace estimate")
	}
	finish := parse(3, "event_msg", `{"type":"task_complete","turn_id":"one"}`)
	ingestSynthetic(t, s, finish)
	if s.runtimeViews()[0]["runtime_state"] != "idle" {
		t.Fatal("completion not honored")
	}
	newTurn := parse(4, "event_msg", `{"type":"task_started","turn_id":"two"}`)
	ingestSynthetic(t, s, newTurn)
	delayed := exact
	delayed.EventID = "late"
	ingestSynthetic(t, s, delayed)
	if s.runtimeViews()[0]["turn_id"] != "two" {
		t.Fatal("old event replaced new turn")
	}
	for _, payload := range []string{`{"type":"reasoning_text_delta","delta":"private reasoning"}`, `{"type":"function_call_arguments_delta","delta":"private arguments"}`, `{"delta":"untyped"}`} {
		if _, ok := parseCodexLine([]byte(`{"type":"event_msg","payload":`+payload+`}`), "h", groupingRoot, 8, &pc); ok {
			t.Fatal("estimated non-visible content")
		}
	}
	if got := s.tokenTotalsBetween(time.Unix(0, 0), time.Now().Add(time.Hour)); got.TotalTokens != 17 {
		t.Fatalf("runtime/estimate entered ledger: %+v", got)
	}
}

func TestCheckpointSpoolAtomicOnFailure(t *testing.T) {
	dir := t.TempDir()
	db, err := openSQLite(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrateAgent(db); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-"+groupingRoot+".jsonl")
	os.WriteFile(path, []byte("{}\n"), 0600)
	cfg := AgentConfig{HostID: "h"}
	if err := scanOne(db, &cfg, path, false, false); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TRIGGER fail_spool BEFORE INSERT ON spool BEGIN SELECT RAISE(ABORT,'simulated disk failure');END")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"payload":{"info":{"total_token_usage":{"input_tokens":9,"total_tokens":9}}}}` + "\n")
	f.Close()
	if scanOne(db, &cfg, path, false, false) == nil {
		t.Fatal("simulated spool failure ignored")
	}
	var offset int64
	db.QueryRow("SELECT offset FROM files").Scan(&offset)
	if offset != 3 {
		t.Fatal("checkpoint moved past unqueued usage")
	}
	db.Exec("DROP TRIGGER fail_spool")
	if err := scanOne(db, &cfg, path, false, false); err != nil {
		t.Fatal(err)
	}
	if err := scanOne(db, &cfg, path, false, false); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&count)
	if count != 1 {
		t.Fatal("recovery duplicated or lost usage")
	}
}

func TestAgentCollectsThroughoutTenSecondUploadTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("10 second network fault")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "codex")
	os.MkdirAll(filepath.Join(home, "sessions"), 0700)
	path := filepath.Join(home, "sessions", "rollout-"+groupingRoot+".jsonl")
	os.WriteFile(path, nil, 0600)
	var uploads atomic.Int32
	started := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch IngestBatch
		json.NewDecoder(r.Body).Decode(&batch)
		r.Body.Close()
		if len(batch.Events) > 0 {
			uploads.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(12 * time.Second):
				w.WriteHeader(504)
				return
			}
		}
		writeJSON(w, map[string]int{"accepted": 0})
	}))
	defer ts.Close()
	cfg := AgentConfig{HostID: "h", ServerURL: ts.URL, Token: "synthetic", CodexHomes: []string{home}, StateDir: filepath.Join(dir, "state")}
	b, _ := json.Marshal(cfg)
	config := filepath.Join(dir, "config.json")
	os.WriteFile(config, b, 0600)
	db, err := openSQLite(filepath.Join(cfg.StateDir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateAgent(db); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgent(ctx, config, false, false) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("agent exited: %v", err)
		default:
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM meta WHERE key='baseline_complete'").Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("baseline not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var timings []float64
	for i := 1; i <= 5; i++ {
		at := time.Now()
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintf(f, `{"payload":{"info":{"total_token_usage":{"input_tokens":%d,"total_tokens":%d}}}}`+"\n", i, i)
		f.Close()
		for {
			var n int
			db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
			if n >= i {
				break
			}
			if time.Since(at) > time.Second {
				t.Fatal("network blocked collector")
			}
			time.Sleep(10 * time.Millisecond)
		}
		timings = append(timings, float64(time.Since(at).Microseconds())/1000)
		if i == 1 {
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("upload not started")
			}
		}
		if i < 5 {
			time.Sleep(2 * time.Second)
		}
	}
	time.Sleep(2 * time.Second)
	var n int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 5 || uploads.Load() > 2 {
		t.Fatalf("queue lost or retry storm: queued=%d attempts=%d", n, uploads.Load())
	}
	t.Logf("collector_enqueue_ms=%v; queued_after_10s_timeout=%d; upload_attempts=%d", timings, n, uploads.Load())
}

func TestPrepareRealtimeBrowserFixture(t *testing.T) {
	dir := os.Getenv("METER_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("opt-in synthetic fixture")
	}
	if _, err := os.Stat(filepath.Join(dir, "meter.db")); !os.IsNotExist(err) {
		t.Fatal("fixture requires a new database; refuse to reuse or modify existing data")
	}
	db, err := openSQLite(filepath.Join(dir, "meter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrateServer(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO agents(host_id,alias,platform,token_hash,created_at,last_seen)VALUES('h','合成测试设备','linux',?,?,?)", tokenHash("synthetic"), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	s := &server{db: db, hub: newHub()}
	tx, _ := db.Begin()
	for i := 0; i < 4100; i++ {
		id := fmt.Sprintf("fixture-%d", i)
		e := UsageEvent{EventID: id, HostID: "h", ConversationID: id, ParentConversationID: groupingRoot, SourceFileID: id, EventType: "exact_usage", Timestamp: time.Now().Add(-time.Hour).UTC(), Counts: counts(10, 2, 0, 2, 0), Model: "gpt-5.6-sol", RepoName: "Realtime Test", DataQuality: "EXACT"}
		if i == 0 {
			e.ConversationID = groupingRoot
			e.SourceFileID = groupingRoot
			e.ParentConversationID = ""
			e.Counts = counts(1000000, 100, 0, 20, 0)
		}
		if err = s.applyEvent(tx, e); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cfg := ServerConfig{Listen: "127.0.0.1:18787", DataDir: dir, AdminUser: "test", SessionSecret: "synthetic-browser-secret", PublicURL: "http://127.0.0.1:18787"}
	b, _ := json.Marshal(cfg)
	if err = os.WriteFile(filepath.Join(dir, "server.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	// Confirm the fixture contains only generated identifiers/counters.
	var rows int
	db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&rows)
	if !reflect.DeepEqual(rows, 4100) {
		t.Fatal("fixture incomplete")
	}
	t.Log("synthetic 4100-record fixture ready")
}
