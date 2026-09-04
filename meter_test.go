package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func counts(i, c, w, o, r int64) TokenCounts {
	return TokenCounts{InputTokens: i, CachedInputTokens: c, CacheWriteInputTokens: w, OutputTokens: o, ReasoningOutputTokens: r, TotalTokens: i + o, CacheWriteVisible: true}
}

func TestArtifactDownloadsAreVersionedAndNotCached(t *testing.T) {
	dir := t.TempDir()
	name := "codex-meter-windows-amd64.exe"
	payload := []byte("release-binary")
	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: ServerConfig{ArtifactDir: dir, PublicURL: "https://token.example/"}}

	repair := httptest.NewRecorder()
	s.windowsHideRepair(repair, httptest.NewRequest(http.MethodGet, "/install/windows-hide.ps1", nil))
	wantURL := "https://token.example/downloads/" + name + "?sha256=" + sum
	if repair.Code != http.StatusOK || !strings.Contains(repair.Body.String(), wantURL) {
		t.Fatalf("repair installer is not tied to artifact hash: status=%d body=%q", repair.Code, repair.Body.String())
	}

	download := httptest.NewRecorder()
	s.download(download, httptest.NewRequest(http.MethodGet, "/downloads/"+name+"?sha256="+sum, nil))
	if download.Code != http.StatusOK || download.Header().Get("Cache-Control") != "no-store" || download.Body.String() != string(payload) {
		t.Fatalf("download response mismatch: status=%d cache=%q body=%q", download.Code, download.Header().Get("Cache-Control"), download.Body.String())
	}
}

func TestLoginCSRFIsServerRendered(t *testing.T) {
	const password = "correct-horse-battery-staple"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: ServerConfig{AdminUser: "admin", AdminPasswordHash: hash, SessionSecret: "test-secret"}, login: map[string][]time.Time{}}

	get := httptest.NewRequest(http.MethodGet, "https://token.example/login", nil)
	got := httptest.NewRecorder()
	s.loginHandler(got, get)
	result := got.Result()
	var csrf *http.Cookie
	for _, cookie := range result.Cookies() {
		if cookie.Name == "meter_csrf" {
			csrf = cookie
		}
	}
	if csrf == nil || !strings.Contains(got.Body.String(), `name="csrf" value="`+csrf.Value+`"`) {
		t.Fatal("CSRF token was not rendered into the login form")
	}
	reopen := httptest.NewRequest(http.MethodGet, "https://token.example/login", nil)
	reopen.AddCookie(csrf)
	reopened := httptest.NewRecorder()
	s.loginHandler(reopened, reopen)
	if len(reopened.Result().Cookies()) != 0 || !strings.Contains(reopened.Body.String(), `name="csrf" value="`+csrf.Value+`"`) {
		t.Fatal("an existing CSRF cookie was unexpectedly rotated")
	}

	form := url.Values{"csrf": {csrf.Value}, "username": {"admin"}, "password": {password}}
	post := httptest.NewRequest(http.MethodPost, "https://token.example/login", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(csrf)
	loggedIn := httptest.NewRecorder()
	s.loginHandler(loggedIn, post)
	if loggedIn.Code != http.StatusSeeOther || loggedIn.Header().Get("Location") != "/" {
		t.Fatalf("login failed: status=%d location=%q", loggedIn.Code, loggedIn.Header().Get("Location"))
	}

	stale := httptest.NewRequest(http.MethodPost, "https://token.example/login", strings.NewReader(form.Encode()))
	stale.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	refreshed := httptest.NewRecorder()
	s.loginHandler(refreshed, stale)
	if refreshed.Code != http.StatusSeeOther || refreshed.Header().Get("Location") != "/login?error=csrf" {
		t.Fatalf("stale login did not recover: status=%d location=%q", refreshed.Code, refreshed.Header().Get("Location"))
	}
}

func TestCounterDeltaDuplicateAndReset(t *testing.T) {
	old := counts(100, 40, 10, 20, 7)
	same := deltaCounts(old, old, false)
	if same.TotalTokens != 0 || same.InputTokens != 0 {
		t.Fatal("duplicate total added tokens")
	}
	next := counts(130, 50, 12, 28, 9)
	d := deltaCounts(next, old, false)
	if d.TotalTokens != 38 || d.InputTokens != 30 || d.CachedInputTokens != 10 || d.CacheWriteInputTokens != 2 || d.OutputTokens != 8 || d.ReasoningOutputTokens != 2 {
		t.Fatalf("bad delta: %+v", d)
	}
	reset := counts(9, 2, 1, 3, 1)
	d = deltaCounts(reset, next, true)
	if d.TotalTokens != 12 || d.ReasoningOutputTokens != 1 {
		t.Fatalf("bad reset: %+v", d)
	}
	if d.TotalTokens == d.InputTokens+d.OutputTokens+d.ReasoningOutputTokens {
		t.Fatal("reasoning was counted outside output")
	}
}

func TestParserUsagePrivacyAndCacheWrite(t *testing.T) {
	line := `{"timestamp":"2026-09-04T00:00:00Z","type":"event_msg","payload":{"session_id":"11111111-1111-1111-1111-111111111111","model":"gpt-5.6-sol","reasoning_effort":"high","prompt":"DO_NOT_STORE","reasoning":"SECRET","info":{"model_context_window":1050000,"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"cache_write_input_tokens":10,"output_tokens":30,"reasoning_output_tokens":7,"total_tokens":130},"last_token_usage":{"total_tokens":99}}}}`
	pc := parseContext{}
	e, ok := parseCodexLine([]byte(line), "h", "f", 10, &pc)
	if !ok || e.Counts.TotalTokens != 130 || !e.Counts.CacheWriteVisible {
		t.Fatalf("parse failed: %+v", e)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "DO_NOT_STORE") || strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "prompt") {
		t.Fatal("sensitive content collected")
	}
	line = strings.Replace(line, `,"cache_write_input_tokens":10`, "", 1)
	e, _ = parseCodexLine([]byte(line), "h", "f", 10, &pc)
	if e.Counts.CacheWriteVisible || e.DataQuality != "CACHE_WRITE_UNKNOWN" {
		t.Fatal("missing cache-write not marked")
	}
}

func TestPartialLineCheckpointMoveAndRestart(t *testing.T) {
	d := t.TempDir()
	home := filepath.Join(d, ".codex")
	p := filepath.Join(home, "sessions", "2026", "x-22222222-2222-2222-2222-222222222222.jsonl")
	os.MkdirAll(filepath.Dir(p), 0700)
	os.WriteFile(p, []byte("{}\n"), 0600)
	db, _ := openSQLite(filepath.Join(d, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	cfg := AgentConfig{HostID: "h", CodexHomes: []string{home}, StateDir: d}
	scanCodexFiles(db, &cfg, false)
	usage := `{"timestamp":"2026-09-04T00:00:00Z","payload":{"info":{"total_token_usage":{"input_tokens":2,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":1,"total_tokens":3}}}}`
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(usage[:len(usage)/2])
	f.Close()
	scanCodexFiles(db, &cfg, false)
	var n int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 0 {
		t.Fatal("partial line emitted")
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(usage[len(usage)/2:] + "\n")
	f.Close()
	scanCodexFiles(db, &cfg, false)
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 1 {
		t.Fatalf("complete line count=%d", n)
	}
	scanCodexFiles(db, &cfg, false)
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 1 {
		t.Fatal("restart duplicated checkpoint")
	}
	arch := filepath.Join(home, "archived_sessions", filepath.Base(p))
	os.MkdirAll(filepath.Dir(arch), 0700)
	os.Rename(p, arch)
	scanCodexFiles(db, &cfg, false)
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 1 {
		t.Fatal("archive move duplicated")
	}
}

func TestBaselineSurvivesLargeTrailingRecords(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "x-44444444-4444-4444-4444-444444444444.jsonl")
	usage := `{"timestamp":"2026-09-04T00:00:00Z","payload":{"info":{"total_token_usage":{"input_tokens":50,"cached_input_tokens":10,"cache_write_input_tokens":2,"output_tokens":5,"reasoning_output_tokens":1,"total_tokens":55}}}}` + "\n"
	child := `{"timestamp":"2026-09-04T00:00:01Z","payload":{"thread_id":"ctco_55555555-5555-5555-5555-555555555555","info":{"total_token_usage":{"input_tokens":70,"cached_input_tokens":20,"cache_write_input_tokens":3,"output_tokens":8,"reasoning_output_tokens":2,"total_tokens":78}}}}` + "\n"
	large := `{"payload":{"tool_result":"` + strings.Repeat("x", 5<<20) + `"}}` + "\n"
	if err := os.WriteFile(p, []byte(usage+child+large), 0600); err != nil {
		t.Fatal(err)
	}
	db, _ := openSQLite(filepath.Join(d, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	cfg := AgentConfig{HostID: "h"}
	if err := queueBaseline(db, &cfg, p, sourceIdentity(p), int64(len(usage)+len(child)+len(large))); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 2 {
		t.Fatalf("expected two conversation baselines, got %d", n)
	}
	var payload []byte
	if err := db.QueryRow("SELECT payload FROM spool WHERE CAST(payload AS TEXT) LIKE '%ctco_55555555%'").Scan(&payload); err != nil {
		t.Fatal("baseline not queued")
	}
	var e UsageEvent
	json.Unmarshal(payload, &e)
	if e.EventType != "baseline" || e.Counts.TotalTokens != 78 {
		t.Fatalf("wrong baseline: %+v", e)
	}
}

func TestPostBaselineNewFileStartsAtZero(t *testing.T) {
	d := t.TempDir()
	home := filepath.Join(d, ".codex")
	os.MkdirAll(filepath.Join(home, "sessions"), 0700)
	old := filepath.Join(home, "sessions", "x-66666666-6666-6666-6666-666666666666.jsonl")
	os.WriteFile(old, []byte("{}\n"), 0600)
	db, _ := openSQLite(filepath.Join(d, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	cfg := AgentConfig{HostID: "h", CodexHomes: []string{home}}
	if err := scanCodexFiles(db, &cfg, false); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(home, "sessions", "x-77777777-7777-7777-7777-777777777777.jsonl")
	line := `{"timestamp":"2026-09-04T00:00:00Z","payload":{"model":"gpt-5.6-sol","reasoning_effort":"low","info":{"total_token_usage":{"input_tokens":4,"cached_input_tokens":1,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1,"total_tokens":6}}}}` + "\n"
	os.WriteFile(newPath, []byte(line), 0600)
	if err := scanCodexFiles(db, &cfg, false); err != nil {
		t.Fatal(err)
	}
	var p []byte
	if err := db.QueryRow("SELECT payload FROM spool").Scan(&p); err != nil {
		t.Fatal("new file was not read")
	}
	var e UsageEvent
	json.Unmarshal(p, &e)
	if e.Counts.TotalTokens != 6 || e.Model != "gpt-5.6-sol" || e.ReasoningEffort != "low" {
		t.Fatalf("new file metadata missing: %+v", e)
	}
}

func TestMetadataPersistsAcrossPollingScans(t *testing.T) {
	d := t.TempDir()
	home := filepath.Join(d, ".codex")
	dir := filepath.Join(home, "sessions")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "old-88888888-8888-8888-8888-888888888888.jsonl"), []byte("{}\n"), 0600)
	db, _ := openSQLite(filepath.Join(d, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	cfg := AgentConfig{HostID: "h", CodexHomes: []string{home}}
	scanCodexFiles(db, &cfg, false)
	p := filepath.Join(dir, "new-99999999-9999-9999-9999-999999999999.jsonl")
	os.WriteFile(p, []byte(`{"timestamp":"2026-09-04T00:00:00Z","payload":{"session_id":"99999999-9999-9999-9999-999999999999","model":"gpt-5.6-sol","reasoning_effort":"xhigh"}}`+"\n"), 0600)
	scanCodexFiles(db, &cfg, false)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"timestamp":"2026-09-04T00:00:01Z","payload":{"info":{"total_token_usage":{"input_tokens":8,"cached_input_tokens":2,"cache_write_input_tokens":1,"output_tokens":3,"reasoning_output_tokens":1,"total_tokens":11}}}}` + "\n")
	f.Close()
	scanCodexFiles(db, &cfg, false)
	var b []byte
	db.QueryRow("SELECT payload FROM spool").Scan(&b)
	var e UsageEvent
	json.Unmarshal(b, &e)
	if e.Model != "gpt-5.6-sol" || e.ReasoningEffort != "xhigh" {
		t.Fatalf("metadata lost across scans: %+v", e)
	}
}

func testServerDB(t *testing.T) (*server, func()) {
	d := t.TempDir()
	db, e := openSQLite(filepath.Join(d, "meter.db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = migrateServer(db); e != nil {
		t.Fatal(e)
	}
	db.Exec("INSERT INTO agents(host_id,alias,token_hash,created_at)VALUES('h','host','x',?)", time.Now().UTC().Format(time.RFC3339))
	s := &server{db: db, hub: newHub()}
	return s, func() { db.Close() }
}

func TestServerDedupParentPrefixLiveReconcile(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	base := UsageEvent{EventID: "b", HostID: "h", ConversationID: "parent", SourceFileID: "p", EventType: "baseline", Timestamp: time.Now(), Counts: counts(100, 20, 5, 30, 8)}
	tx, _ := s.db.Begin()
	if e := s.applyEvent(tx, base); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	child := base
	child.EventID = "c"
	child.EventType = "exact_usage"
	child.ConversationID = "child"
	child.ParentConversationID = "parent"
	child.TurnID = "turn1"
	child.Counts = counts(110, 22, 6, 35, 9)
	tx, _ = s.db.Begin()
	s.applyEvent(tx, child)
	tx.Commit()
	var total int64
	s.db.QueryRow("SELECT total_tokens FROM sessions WHERE conversation_id='child'").Scan(&total)
	if total != 15 {
		t.Fatalf("parent prefix counted: %d", total)
	}
	dup := child
	dup.EventID = "different-path"
	tx, _ = s.db.Begin()
	s.applyEvent(tx, dup)
	tx.Commit()
	s.db.QueryRow("SELECT total_tokens FROM sessions WHERE conversation_id='child'").Scan(&total)
	if total != 15 {
		t.Fatal("response dedup failed")
	}
	growth := child
	growth.EventID = "growing"
	growth.Counts = counts(112, 22, 6, 38, 10)
	tx, _ = s.db.Begin()
	s.applyEvent(tx, growth)
	tx.Commit()
	s.db.QueryRow("SELECT total_tokens FROM sessions WHERE conversation_id='child'").Scan(&total)
	if total != 20 {
		t.Fatalf("same-turn growing counter lost: %d", total)
	}
	live := child
	live.EventID = "live"
	live.EventType = "live_estimate"
	live.LiveEstimate = 4
	tx, _ = s.db.Begin()
	s.applyEvent(tx, live)
	tx.Commit()
	tx, _ = s.db.Begin()
	s.applyEvent(tx, live)
	tx.Commit()
	var liveDedup int64
	s.db.QueryRow("SELECT live_estimate FROM sessions WHERE conversation_id='child'").Scan(&liveDedup)
	if liveDedup != 4 {
		t.Fatalf("live replay duplicated: %d", liveDedup)
	}
	exact := child
	exact.EventID = "e2"
	exact.TurnID = "turn2"
	exact.Counts = counts(114, 23, 6, 40, 11)
	tx, _ = s.db.Begin()
	s.applyEvent(tx, exact)
	tx.Commit()
	var liveNow int64
	var status string
	s.db.QueryRow("SELECT live_estimate,status,total_tokens FROM sessions WHERE conversation_id='child'").Scan(&liveNow, &status, &total)
	if liveNow != 0 || status != "EXACT" || total != 24 {
		t.Fatalf("reconcile failed %d %s %d", liveNow, status, total)
	}
}

func TestImplicitCTCOParentPrefix(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	base := UsageEvent{EventID: "b", HostID: "h", ConversationID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", SourceFileID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", EventType: "baseline", Timestamp: time.Now(), Counts: counts(1000, 700, 20, 100, 30)}
	tx, _ := s.db.Begin()
	s.applyEvent(tx, base)
	tx.Commit()
	child := base
	child.EventID = "c"
	child.EventType = "exact_usage"
	child.ConversationID = "ctco_bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	child.TurnID = "new"
	child.Counts = counts(1010, 705, 21, 104, 31)
	tx, _ = s.db.Begin()
	s.applyEvent(tx, child)
	tx.Commit()
	var total int64
	var parent string
	s.db.QueryRow("SELECT total_tokens,parent_conversation_id FROM sessions WHERE conversation_id=?", child.ConversationID).Scan(&total, &parent)
	if total != 14 || parent != base.ConversationID {
		t.Fatalf("implicit child prefix failed total=%d parent=%s", total, parent)
	}
}

func TestImplicitFCOParentPrefix(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	base := UsageEvent{EventID: "b", HostID: "h", ConversationID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", SourceFileID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", EventType: "baseline", Timestamp: time.Now(), Counts: counts(2000, 1200, 30, 200, 50)}
	tx, _ := s.db.Begin()
	s.applyEvent(tx, base)
	tx.Commit()
	child := base
	child.EventID = "f"
	child.EventType = "exact_usage"
	child.ConversationID = "fco_cccccccc-cccc-cccc-cccc-cccccccccccc"
	child.TurnID = "fork"
	child.Counts = counts(2003, 1201, 30, 202, 50)
	tx, _ = s.db.Begin()
	s.applyEvent(tx, child)
	tx.Commit()
	var total int64
	s.db.QueryRow("SELECT total_tokens FROM sessions WHERE conversation_id=?", child.ConversationID).Scan(&total)
	if total != 5 {
		t.Fatalf("fco prefix counted: %d", total)
	}
}

func TestSharedSourceCounterAcrossManyChildren(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	source := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	base := UsageEvent{EventID: "b", HostID: "h", ConversationID: source, SourceFileID: source, EventType: "baseline", Timestamp: time.Now(), Counts: counts(1000, 500, 20, 100, 20)}
	tx, _ := s.db.Begin()
	s.applyEvent(tx, base)
	tx.Commit()
	for i, raw := range []int64{1100, 1200} {
		e := base
		e.EventID = fmt.Sprintf("child%d", i)
		e.EventType = "exact_usage"
		e.ConversationID = fmt.Sprintf("ctco_child%d", i)
		e.TurnID = fmt.Sprintf("turn%d", i)
		e.Counts = counts(raw, 500+int64(i+1)*20, 20, 100+int64(i+1)*10, 20)
		tx, _ = s.db.Begin()
		s.applyEvent(tx, e)
		tx.Commit()
	}
	var sum int64
	s.db.QueryRow("SELECT SUM(total_tokens) FROM sessions").Scan(&sum)
	if sum != 220 {
		t.Fatalf("shared source prefix repeated: %d", sum)
	}
}

func TestLongContextPricingAndHistory(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	at := time.Now()
	short, _ := costFor(s.db, "openai", "API", "gpt-5.6-sol", counts(272000, 0, 0, 1000, 0), at)
	long, _ := costFor(s.db, "openai", "API", "gpt-5.6-sol", counts(272001, 0, 0, 1000, 0), at)
	if !(long.Value > short.Value*1.9) {
		t.Fatalf("long multiplier missing: %f %f", short.Value, long.Value)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM prices WHERE provider='openai'").Scan(&n)
	if n != 1 {
		t.Fatal("seed price history")
	}
}

func TestLongContextThresholdAppliedPerRequest(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now()
	for i, conv := range []string{"r1", "r2"} {
		e := UsageEvent{EventID: fmt.Sprintf("e%d", i), HostID: "h", ConversationID: conv, SourceFileID: conv, EventType: "exact_usage", Timestamp: now, TurnID: conv, Counts: counts(200000, 0, 0, 1000, 0), DataQuality: "EXACT", ParserVersion: parserVersion}
		tx, _ := s.db.Begin()
		s.applyEvent(tx, e)
		tx.Commit()
	}
	total, _ := rangeCosts(s.db, now.Add(-time.Minute), now.Add(time.Minute))
	aggregated, _ := costFor(s.db, "openai", "API", "gpt-5.6-sol", counts(400000, 0, 0, 2000, 0), now)
	if !(total.API.Value < aggregated.Value*.7) {
		t.Fatalf("range incorrectly tiered as one request: per=%f aggregate=%f", total.API.Value, aggregated.Value)
	}
}

func TestSSEInitialSnapshotAndUpdate(t *testing.T) {
	h := newHub()
	rr := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { h.serve(rr, req, func() any { return map[string]int{"v": 1} }); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if !strings.Contains(rr.Body.String(), "event: snapshot") {
		t.Fatal("missing reconnect snapshot")
	}
}

func TestBackupRestore(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "live.db")
	db, _ := openSQLite(src)
	migrateServer(db)
	db.Close()
	bak := filepath.Join(d, "backup.db")
	if e := backupCommand([]string{"--db", src, "--out", bak}); e != nil {
		t.Fatal(e)
	}
	dst := filepath.Join(d, "restored.db")
	if e := restoreCommand([]string{"--from", bak, "--db", dst, "--force"}); e != nil {
		t.Fatal(e)
	}
	check, _ := openSQLite(dst)
	defer check.Close()
	var ok string
	if e := check.QueryRow("PRAGMA integrity_check").Scan(&ok); e != nil || ok != "ok" {
		t.Fatal("restore invalid")
	}
}

func TestAgentAuthRejected(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	body := `{"host_id":"h","events":[]}`
	r := httptest.NewRequest("POST", "/api/ingest", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	s.ingest(w, r)
	if w.Code != 401 {
		t.Fatalf("expected 401 got %d", w.Code)
	}
}

func TestWindowsAndLinuxSourcePaths(t *testing.T) {
	id := "33333333-3333-3333-3333-333333333333"
	if sourceIdentity(`C:\Users\me\.codex\sessions\x-`+id+`.jsonl`) != id {
		t.Fatal("windows id")
	}
	if sourceIdentity(`/root/.codex/sessions/x-`+id+`.jsonl`) != id {
		t.Fatal("linux id")
	}
}

func TestLocalTokenizerNoTextRetention(t *testing.T) {
	if localEstimateTokens("hello 世界!") < 3 {
		t.Fatal("bad local estimate")
	}
	r := bufio.NewReader(strings.NewReader("ok"))
	b, _ := io.ReadAll(r)
	if string(b) != "ok" {
		t.Fatal()
	}
}

func TestActivityWithoutDeltaThenExact(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	e := UsageEvent{EventID: "a", HostID: "h", ConversationID: "conv", SourceFileID: "conv", EventType: "activity", Timestamp: time.Now(), TurnID: "turn", Model: "gpt-5.6-sol", ReasoningEffort: "low", DataQuality: "LOWER_BOUND"}
	tx, _ := s.db.Begin()
	s.applyEvent(tx, e)
	tx.Commit()
	var status string
	s.db.QueryRow("SELECT status FROM sessions WHERE conversation_id='conv'").Scan(&status)
	if status != "LOWER_BOUND" {
		t.Fatal("activity status")
	}
	e.EventID = "x"
	e.EventType = "exact_usage"
	e.Counts = counts(10, 2, 1, 3, 1)
	e.Counts.CacheWriteVisible = true
	tx, _ = s.db.Begin()
	s.applyEvent(tx, e)
	tx.Commit()
	s.db.QueryRow("SELECT status FROM sessions WHERE conversation_id='conv'").Scan(&status)
	if status != "EXACT" {
		t.Fatal("exact did not reconcile activity")
	}
	if _, ok := s.snapshotSince(time.Now().Add(-time.Hour)).(map[string]any); !ok {
		t.Fatal("range snapshot")
	}
}

func TestAgentSpoolOfflineThenRecovery(t *testing.T) {
	d := t.TempDir()
	db, _ := openSQLite(filepath.Join(d, "agent.db"))
	defer db.Close()
	migrateAgent(db)
	e := UsageEvent{EventID: "queued", HostID: "h", ConversationID: "c"}
	b, _ := json.Marshal(e)
	db.Exec("INSERT INTO spool(event_id,payload,created_at)VALUES(?,?,?)", e.EventID, b, time.Now().UTC().Format(time.RFC3339))
	cfg := AgentConfig{HostID: "h", Token: "token", ServerURL: "http://127.0.0.1:1"}
	if flushSpool(context.Background(), db, &cfg) == nil {
		t.Fatal("offline upload unexpectedly succeeded")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 1 {
		t.Fatal("offline spool lost")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	cfg.ServerURL = srv.URL
	if err := flushSpool(context.Background(), db, &cfg); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&n)
	if n != 0 {
		t.Fatal("spool did not drain")
	}
}

func TestAgentConfigPermission(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	if err := writeJSON0600(p, AgentConfig{Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}

func TestECBUSDToCNYCrossRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<?xml version="1.0"?><Envelope><Cube><Cube time="2026-09-04"><Cube currency="USD" rate="1.1622"/><Cube currency="CNY" rate="7.7994"/></Cube></Cube></Envelope>`)
	}))
	defer srv.Close()
	date, rate, err := fetchUSDCNY(context.Background(), srv.URL)
	if err != nil || date != "2026-09-04" || rate < 6.70 || rate > 6.72 {
		t.Fatalf("unexpected cross rate date=%s rate=%f err=%v", date, rate, err)
	}
}

func TestParseOpenAIPriceMarkdown(t *testing.T) {
	body := `
| Input | $4 | 1M tokens |
| Cached input | $0.4 | 1M tokens |
| Output | $20 | 1M tokens |
Prompts with >272K input tokens are priced at 2x input and 1.5x output for the full request.
Cache writes are billed at 1.25x the uncached input token rate.`
	spec, err := parseOpenAIPriceMarkdown(body)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Input != 4 || spec.Cached != .4 || spec.CacheWrite != 5 || spec.Output != 20 || spec.Threshold != 272000 || spec.InputMultiplier != 2 || spec.OutputMultiplier != 1.5 {
		t.Fatalf("unexpected OpenAI pricing: %+v", spec)
	}
}

func TestCollectSessionMetadataUsesExplicitNameOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	state, err := openSQLite(filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,name TEXT,title TEXT,first_user_message TEXT,preview TEXT,
		cwd TEXT,git_origin_url TEXT,project_id TEXT,agent_nickname TEXT,agent_path TEXT,updated_at_ms INTEGER
	); CREATE TABLE projects(id TEXT PRIMARY KEY,name TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = state.Exec(`INSERT INTO threads VALUES
		('named','  显示任务名称'||char(10),'DO_NOT_UPLOAD_TITLE','DO_NOT_UPLOAD_PROMPT','DO_NOT_UPLOAD_PREVIEW','/tmp/repo','git@github.com:owner/useful-project.git','','','',3),
		('agent','','DO_NOT_UPLOAD_TITLE_2','DO_NOT_UPLOAD_PROMPT_2','DO_NOT_UPLOAD_PREVIEW_2','/tmp/repo','git@github.com:owner/useful-project.git','','Russell','/private/path/retail_attention_clock_fix',2),
		('unnamed','','DO_NOT_UPLOAD_TITLE_3','DO_NOT_UPLOAD_PROMPT_3','DO_NOT_UPLOAD_PREVIEW_3','/root','','','','',1)`)
	state.Close()
	items := collectSessionMetadata([]string{home})
	if len(items) != 2 || items[0].ConversationID != "named" || items[0].ConversationName != "显示任务名称" || items[0].ProjectName != "useful-project" || items[1].ConversationName != "retail attention clock fix · Russell" {
		t.Fatalf("unexpected safe metadata: %+v", items)
	}
	b, _ := json.Marshal(items)
	if strings.Contains(string(b), "DO_NOT_UPLOAD") {
		t.Fatal("non-display thread content escaped metadata boundary")
	}
	if got := effectiveProjectName("root", "你是“Codex Token Meter”项目的维护者"); got != "Codex Token Meter" {
		t.Fatalf("quoted project inference=%q", got)
	}
}

func TestSnapshotUsesParentDisplayMetadata(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now().UTC()
	e := UsageEvent{EventID: "named-event", HostID: "h", ConversationID: "child", ParentConversationID: "parent", SourceFileID: "parent", EventType: "exact_usage", Timestamp: now, TurnID: "turn", Model: "gpt-5.6-sol", Counts: counts(10, 2, 0, 3, 1), DataQuality: "EXACT", ParserVersion: parserVersion}
	tx, _ := s.db.Begin()
	if err := s.applyEvent(tx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.applySessionMetadata(tx, "h", SessionMetadata{ConversationID: "parent", ConversationName: "真实任务名称", ProjectName: "真实项目名称"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snapshot := s.snapshotBetween(now.Add(-time.Minute), now.Add(time.Minute)).(map[string]any)
	sessions := snapshot["sessions"].([]map[string]any)
	if len(sessions) != 1 || sessions[0]["name"] != "真实任务名称" || sessions[0]["project"] != "真实项目名称" {
		t.Fatalf("display metadata not joined: %+v", sessions)
	}
	projects := snapshot["project_totals"].([]map[string]any)
	if len(projects) != 1 || projects[0]["project"] != "真实项目名称" || projects[0]["total_tokens"] != int64(13) {
		t.Fatalf("project totals not computed: %+v", projects)
	}
	totals := snapshot["totals"].(map[string]any)
	if totals["cache_write_visible"] != true || sessions[0]["cache_write_visible"] != true {
		t.Fatalf("cache-write visibility missing: totals=%+v session=%+v", totals, sessions[0])
	}
}

func TestActiveSessionsAreFiveMinuteParentRoots(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now().UTC()
	for _, row := range []struct {
		id, parent string
		at         time.Time
	}{
		{"child-a", "parent-a", now.Add(-time.Minute)},
		{"child-b", "parent-a", now.Add(-2 * time.Minute)},
		{"root-b", "", now.Add(-4 * time.Minute)},
		{"stale", "", now.Add(-6 * time.Minute)},
	} {
		_, err := s.db.Exec("INSERT INTO sessions(host_id,conversation_id,parent_conversation_id,last_event_at)VALUES(?,?,?,?)", "h", row.id, nullstr(row.parent), row.at.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := s.activeSessionCount(now); got != 2 {
		t.Fatalf("active roots=%d, want 2", got)
	}
}
