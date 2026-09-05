package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Reads a private online backup, copies it to t.TempDir, and never touches the
// source DB. Requests below operate ONLY on the isolated copy, never HTTP.
func TestRealDataLatencyProfile(t *testing.T) {
	source := os.Getenv("METER_PROFILE_COPY")
	if source == "" {
		t.Skip("opt-in real-size backup profile")
	}
	b, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "meter.db")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrateServer(db); err != nil {
		t.Fatal(err)
	}
	readPool, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readPool.Close()
	s := &server{db: db, readPool: readPool, accounting: &accountingCache{}, hub: newHub(), ingestTimes: map[string][]time.Time{}}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&n)
	db.Exec("INSERT INTO agents(host_id,alias,token_hash,created_at)VALUES('profile','synthetic',?,?)", tokenHash("synthetic-profile"), time.Now().Format(time.RFC3339Nano))
	r, _ := resolveDashboardRange(url.Values{"period": {"all"}}, time.Now())
	for i := 0; i < 3; i++ {
		value := s.buildSnapshotForRange(r).(map[string]any)
		t.Logf("rows=%d profile=%v", n, value["work_profile"])
		started := make(chan struct{})
		finished := make(chan struct{})
		go func() { close(started); s.buildSnapshotForRange(r); close(finished) }()
		<-started
		time.Sleep(5 * time.Millisecond)
		e := UsageEvent{HostID: "profile", EventID: time.Now().Format(time.RFC3339Nano), ConversationID: "profile", SourceFileID: "profile", EventType: "activity", RunState: "running", Timestamp: time.Now()}
		body, _ := json.Marshal(IngestBatch{HostID: "profile", Events: []UsageEvent{e}})
		request := httptest.NewRequest("POST", "/api/ingest", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer synthetic-profile")
		w := httptest.NewRecorder()
		at := time.Now()
		s.ingest(w, request)
		elapsed := time.Since(at)
		<-finished
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		trace := s.traces.recent()
		last := trace[len(trace)-1]
		t.Logf("concurrent_ingest_ms=%.3f view_lock_ms=%.3f commit_ms=%.3f", float64(elapsed.Microseconds())/1000, last.LockMS, last.CommitMS)
	}
	var base map[string]any
	for i := 0; i < 4; i++ {
		e := UsageEvent{HostID: "profile", EventID: time.Now().Format(time.RFC3339Nano), ConversationID: "profile", SourceFileID: "profile", EventType: "exact_usage", Timestamp: r.End.Add(-time.Second), Counts: counts(int64(100+i), 20, 0, 2, 0), DataQuality: "EXACT"}
		body, _ := json.Marshal(IngestBatch{HostID: "profile", Events: []UsageEvent{e}})
		request := httptest.NewRequest("POST", "/api/ingest", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer synthetic-profile")
		w := httptest.NewRecorder()
		s.ingest(w, request)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		at := time.Now()
		value := s.buildSnapshot(r, base).(map[string]any)
		elapsed := time.Since(at)
		full := s.buildSnapshotForRange(r).(map[string]any)
		for _, key := range []string{"total_tokens", "input_tokens", "output_tokens", "api", "vercel", "credits"} {
			if !reflect.DeepEqual(value["totals"].(map[string]any)[key], full["totals"].(map[string]any)[key]) {
				t.Fatalf("real-copy incremental mismatch: %s", key)
			}
		}
		if !reflect.DeepEqual(value["sessions"], full["sessions"]) {
			t.Fatal("real-copy session aggregates changed")
		}
		t.Logf("incremental=%t build_ms=%.3f profile=%v exact_matches_full=true", base != nil, float64(elapsed.Microseconds())/1000, value["work_profile"])
		base = value
	}
}
