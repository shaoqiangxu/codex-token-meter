package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSilentRunningFileRemainsHot(t *testing.T) {
	dir := t.TempDir()
	db, err := openSQLite(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrateAgent(db); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "codex")
	sessions := filepath.Join(home, "sessions")
	os.MkdirAll(sessions, 0700)
	path := filepath.Join(sessions, "quiet.jsonl")
	os.WriteFile(path, []byte(`{"timestamp":"2026-09-05T01:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"one"}}`+"\n"), 0600)
	db.Exec("INSERT INTO meta(key,value) VALUES('baseline_complete','yes')")
	cfg := &AgentConfig{HostID: "h", CodexHomes: []string{home}}
	if err = scanScheduled(db, cfg); err != nil {
		t.Fatal(err)
	}
	if seconds, _ := strconv.Atoi(os.Getenv("METER_QUIET_SECONDS")); seconds > 0 {
		start := time.Now()
		for time.Since(start) < time.Duration(seconds)*time.Second {
			if err = scanScheduled(db, cfg); err != nil {
				t.Fatal(err)
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Logf("actual_silence_seconds=%.3f", time.Since(start).Seconds())
	}
	// Advance only the scheduler's mtime age. No artificial usage timestamps or
	// model requests; the regression must work even if notifications are absent.
	cfg.scheduler.active[path] = time.Now().Add(-121 * time.Second)
	cfg.scheduler.history = time.Now()
	cfg.scheduler.discovered = time.Now()
	if err = scanScheduled(db, cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.scheduler.active[path]; !ok {
		t.Fatal("running file evicted solely for silence")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"timestamp":"2026-09-05T01:03:00Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"one"}}` + "\n")
	f.Close()
	at := time.Now()
	if err = scanScheduled(db, cfg); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 2 || cfg.scheduler.running[path] {
		t.Fatalf("completion waited for cold scan: n=%d", n)
	}
	t.Logf("quiet_completion_scan_ms=%.3f", float64(time.Since(at).Microseconds())/1000)
}

func TestLargeCompactionKeepsCheckpointAndUsage(t *testing.T) {
	dir := t.TempDir()
	db, _ := openSQLite(filepath.Join(dir, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	path := filepath.Join(dir, "large.jsonl")
	// Above the actual production blocker. The large text is synthetic and is
	// never queued; the following cumulative usage must still arrive exactly once.
	large := `{"type":"compacted","payload":{"message":"` + strings.Repeat("x", 9*1024*1024) + `"}}` + "\n"
	usage := `{"timestamp":"2026-09-05T01:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":41,"output_tokens":1,"total_tokens":42}}}}` + "\n"
	os.WriteFile(path, []byte(large+usage), 0600)
	cfg := &AgentConfig{HostID: "h", scheduler: &scanScheduler{active: map[string]time.Time{}}}
	for i := 0; i < 3; i++ {
		if err := scanOne(db, cfg, path, false, false); err != nil {
			t.Fatal(err)
		}
	}
	var offset int64
	db.QueryRow("SELECT offset FROM files").Scan(&offset)
	if offset != int64(len(large+usage)) {
		t.Fatalf("checkpoint stuck at %d", offset)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate/lost usage: %d", n)
	}
	var payload []byte
	db.QueryRow("SELECT payload FROM spool").Scan(&payload)
	var e UsageEvent
	json.Unmarshal(payload, &e)
	if e.Counts.TotalTokens != 42 || e.ByteOffset != int64(len(large)) || len(payload) > 4096 {
		t.Fatal("usage changed or large body uploaded")
	}
}

func TestSourceNotificationWakesNewAndColdFiles(t *testing.T) {
	dir := t.TempDir()
	if inLogTree(filepath.Join(dir, "history.jsonl"), []string{dir}) || inLogTree(filepath.Join(dir, "sessions-other", "file.jsonl"), []string{dir}) {
		t.Fatal("watcher expanded beyond configured log subtrees")
	}
	os.MkdirAll(filepath.Join(dir, "sessions"), 0700)
	w, err := watchSources([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer w.watcher.Close()
	w.drain()
	path := filepath.Join(dir, "sessions", "new.jsonl")
	at := time.Now()
	os.WriteFile(path, []byte("{}\n"), 0600)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-w.wake:
			paths, _ := w.drain()
			if paths[path] {
				t.Logf("notification_ms=%.3f", float64(time.Since(at).Microseconds())/1000)
				return
			}
		case <-timer.C:
			t.Fatal("new file not notified")
		}
	}
}

func TestOversizedRecordReportsBlockWithoutSkippingUsage(t *testing.T) {
	dir := t.TempDir()
	db, _ := openSQLite(filepath.Join(dir, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	path := filepath.Join(dir, "oversized.jsonl")
	os.WriteFile(path, nil, 0600)
	cfg := &AgentConfig{HostID: "h"}
	if err := scanOne(db, cfg, path, false, false); err != nil {
		t.Fatal(err)
	}
	prefix := `{"type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(prefix + `{"type":"compacted","payload":{"message":"`)
	chunk := strings.Repeat("x", 64*1024)
	for i := 0; i < 1024; i++ {
		if _, err = f.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
	}
	f.WriteString(`"}}` + "\n")
	f.Close()
	err = scanOne(db, cfg, path, false, false)
	if err == nil {
		t.Fatal("oversized line silently passed")
	}
	message := safeScanError(err)
	if !strings.Contains(message, "source_id="+sourceIdentity(path)) || !strings.Contains(message, "offset="+strconv.Itoa(len(prefix))) || strings.Contains(message, dir) {
		t.Fatalf("unsafe/unactionable scan error: %s", message)
	}
	var offset, n int64
	db.QueryRow("SELECT offset FROM files").Scan(&offset)
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if offset != 0 || n != 0 {
		t.Fatal("failed transaction advanced checkpoint or partially queued events")
	}
}
