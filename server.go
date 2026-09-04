package main

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

type server struct {
	cfg         ServerConfig
	db          *sql.DB
	hub         *eventHub
	loginMu     sync.Mutex
	login       map[string][]time.Time
	ingestMu    sync.Mutex
	ingestTimes map[string][]time.Time
}

func runServer(ctx context.Context, configPath string) error {
	var cfg ServerConfig
	if err := readJSON(configPath, &cfg); err != nil {
		return err
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8787"
	}
	host, _, _ := net.SplitHostPort(cfg.Listen)
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("server must listen on loopback")
	}
	db, err := openSQLite(filepath.Join(cfg.DataDir, "meter.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = migrateServer(db); err != nil {
		return err
	}
	s := &server{cfg: cfg, db: db, hub: newHub(), login: map[string][]time.Time{}, ingestTimes: map[string][]time.Time{}}
	go s.hub.run(s.snapshot)
	go s.vercelPriceLoop(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/readyz", s.ready)
	mux.HandleFunc("/login", s.loginHandler)
	mux.HandleFunc("/logout", s.logoutHandler)
	mux.HandleFunc("/api/ingest", s.ingest)
	mux.HandleFunc("/api/enroll", s.enrollAgent)
	mux.HandleFunc("/install/windows.ps1", s.windowsInstaller)
	mux.HandleFunc("/install/linux.sh", s.linuxInstaller)
	mux.HandleFunc("/downloads/", s.download)
	mux.HandleFunc("/events", s.requireAuth(func(w http.ResponseWriter, r *http.Request) { s.hub.serve(w, r, s.snapshot) }))
	mux.HandleFunc("/api/snapshot", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.snapshotForRequest(r))
	}))
	mux.HandleFunc("/api/enrollments", s.requireAuth(s.csrf(s.createEnrollment)))
	mux.HandleFunc("/api/purchases", s.requireAuth(s.purchaseAPI))
	mux.HandleFunc("/api/sessions/", s.requireAuth(s.sessionAPI))
	mux.HandleFunc("/api/export", s.requireAuth(s.export))
	mux.HandleFunc("/", s.requireAuth(s.static))
	hs := &http.Server{Addr: cfg.Listen, Handler: s.security(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(c)
	}()
	log.Printf("server listening on %s", cfg.Listen)
	err = hs.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}
func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		http.Error(w, "not ready", 503)
		return
	}
	writeJSON(w, map[string]string{"status": "ready"})
}

func (s *server) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var b IngestBatch
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.HostID == "" || len(b.Events) > 256 {
		http.Error(w, "bad batch", 400)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var revoked sql.NullString
	var want string
	if s.db.QueryRow("SELECT token_hash,revoked_at FROM agents WHERE host_id=?", b.HostID).Scan(&want, &revoked) != nil || revoked.Valid || !hmac.Equal([]byte(want), []byte(tokenHash(tok))) {
		http.Error(w, "unauthorized", 401)
		return
	}
	if !s.ingestAllowed(b.HostID) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "db", 500)
		return
	}
	defer tx.Rollback()
	for _, e := range b.Events {
		if e.HostID != b.HostID || !validUsageEvent(e) {
			http.Error(w, "host mismatch", 403)
			return
		}
		if err = s.applyEvent(tx, e); err != nil {
			http.Error(w, "ingest failed", 500)
			return
		}
	}
	_, _ = tx.Exec("UPDATE agents SET last_seen=? WHERE host_id=?", time.Now().UTC().Format(time.RFC3339Nano), b.HostID)
	if err = tx.Commit(); err != nil {
		http.Error(w, "db", 500)
		return
	}
	s.hub.mark()
	writeJSON(w, map[string]any{"accepted": len(b.Events)})
}

func validUsageEvent(e UsageEvent) bool {
	if e.EventID == "" || len(e.EventID) > 128 || e.ConversationID == "" || len(e.ConversationID) > 256 || len(e.SourceFileID) > 256 {
		return false
	}
	switch e.EventType {
	case "baseline", "exact_usage", "live_estimate", "activity":
	default:
		return false
	}
	vals := []int64{e.Counts.InputTokens, e.Counts.CachedInputTokens, e.Counts.CacheWriteInputTokens, e.Counts.OutputTokens, e.Counts.ReasoningOutputTokens, e.Counts.TotalTokens, e.LiveEstimate, e.ModelContextWindow}
	for _, n := range vals {
		if n < 0 || n > 1_000_000_000_000 {
			return false
		}
	}
	return true
}

func (s *server) ingestAllowed(host string) bool {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	cut := time.Now().Add(-time.Second)
	keep := s.ingestTimes[host][:0]
	for _, t := range s.ingestTimes[host] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 20 {
		s.ingestTimes[host] = keep
		return false
	}
	s.ingestTimes[host] = append(keep, time.Now())
	return true
}

func (s *server) applyEvent(tx *sql.Tx, e UsageEvent) error {
	if e.ParentConversationID == "" && (strings.HasPrefix(e.ConversationID, "ctco_") || strings.HasPrefix(e.ConversationID, "fco_")) && e.SourceFileID != "" && e.SourceFileID != e.ConversationID {
		e.ParentConversationID = e.SourceFileID
	}
	seen, err := tx.Exec("INSERT OR IGNORE INTO ingested_events(event_id,host_id,event_type,created_at)VALUES(?,?,?,?)", e.EventID, e.HostID, e.EventType, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	inserted, _ := seen.RowsAffected()
	if inserted == 0 {
		return nil
	}
	if e.EventType == "baseline" {
		_, err := tx.Exec(`INSERT INTO conversation_counters(host_id,conversation_id,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(host_id,conversation_id) DO UPDATE SET input_tokens=excluded.input_tokens,cached_input_tokens=excluded.cached_input_tokens,cache_write_input_tokens=excluded.cache_write_input_tokens,output_tokens=excluded.output_tokens,reasoning_output_tokens=excluded.reasoning_output_tokens,total_tokens=excluded.total_tokens,updated_at=excluded.updated_at`, e.HostID, e.ConversationID, e.Counts.InputTokens, e.Counts.CachedInputTokens, e.Counts.CacheWriteInputTokens, e.Counts.OutputTokens, e.Counts.ReasoningOutputTokens, e.Counts.TotalTokens, e.Timestamp.Format(time.RFC3339Nano))
		return err
	}
	if e.EventType == "live_estimate" {
		_, err := tx.Exec(`INSERT INTO sessions(host_id,conversation_id,parent_conversation_id,project_id,repo_name,model,reasoning_effort,model_context_window,started_at,last_event_at,status,data_quality,live_estimate) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(host_id,conversation_id) DO UPDATE SET live_estimate=live_estimate+excluded.live_estimate,last_event_at=excluded.last_event_at,status='ESTIMATED_LIVE',data_quality='ESTIMATED_LIVE'`, e.HostID, e.ConversationID, nullstr(e.ParentConversationID), e.ProjectID, e.RepoName, e.Model, e.ReasoningEffort, e.ModelContextWindow, e.Timestamp.Format(time.RFC3339Nano), e.Timestamp.Format(time.RFC3339Nano), "ESTIMATED_LIVE", "ESTIMATED_LIVE", e.LiveEstimate)
		return err
	}
	if e.EventType == "activity" {
		_, err := tx.Exec(`INSERT INTO sessions(host_id,conversation_id,parent_conversation_id,project_id,repo_name,model,reasoning_effort,model_context_window,started_at,last_event_at,status,data_quality) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(host_id,conversation_id) DO UPDATE SET parent_conversation_id=COALESCE(excluded.parent_conversation_id,parent_conversation_id),project_id=excluded.project_id,repo_name=excluded.repo_name,model=excluded.model,reasoning_effort=excluded.reasoning_effort,model_context_window=excluded.model_context_window,last_event_at=excluded.last_event_at,status='LOWER_BOUND',data_quality='LOWER_BOUND'`, e.HostID, e.ConversationID, nullstr(e.ParentConversationID), e.ProjectID, e.RepoName, e.Model, e.ReasoningEffort, e.ModelContextWindow, e.Timestamp.Format(time.RFC3339Nano), e.Timestamp.Format(time.RFC3339Nano), "LOWER_BOUND", "LOWER_BOUND")
		return err
	}
	if e.EventType != "exact_usage" {
		return nil
	}
	old := TokenCounts{}
	epoch := 0
	err = tx.QueryRow("SELECT epoch,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens FROM conversation_counters WHERE host_id=? AND conversation_id=?", e.HostID, e.ConversationID).Scan(&epoch, &old.InputTokens, &old.CachedInputTokens, &old.CacheWriteInputTokens, &old.OutputTokens, &old.ReasoningOutputTokens, &old.TotalTokens)
	if errors.Is(err, sql.ErrNoRows) && e.ParentConversationID != "" {
		_ = tx.QueryRow("SELECT input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens FROM conversation_counters WHERE host_id=? AND conversation_id=?", e.HostID, e.ParentConversationID).Scan(&old.InputTokens, &old.CachedInputTokens, &old.CacheWriteInputTokens, &old.OutputTokens, &old.ReasoningOutputTokens, &old.TotalTokens)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	reset := e.Counts.TotalTokens < old.TotalTokens
	d := deltaCounts(e.Counts, old, reset)
	if reset {
		epoch++
	}
	dedup := e.ResponseID
	if dedup == "" {
		dedup = e.TurnID
	}
	if dedup != "" {
		dedup = stableID("response", dedup, strconv.FormatInt(e.Counts.TotalTokens, 10))
	} else {
		dedup = stableID("counter", e.ConversationID, strconv.FormatInt(e.Counts.TotalTokens, 10), strconv.Itoa(epoch))
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO usage_events(event_id,dedup_key,host_id,conversation_id,parent_conversation_id,source_file_id,byte_offset,event_type,timestamp,turn_id,response_id,project_id,model,reasoning_effort,model_context_window,raw_input_tokens,raw_cached_input_tokens,raw_cache_write_input_tokens,raw_output_tokens,raw_reasoning_output_tokens,raw_total_tokens,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cache_write_visible,data_quality,parser_version,epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.EventID, dedup, e.HostID, e.ConversationID, nullstr(e.ParentConversationID), e.SourceFileID, e.ByteOffset, e.EventType, e.Timestamp.Format(time.RFC3339Nano), nullstr(e.TurnID), nullstr(e.ResponseID), e.ProjectID, e.Model, e.ReasoningEffort, e.ModelContextWindow, e.Counts.InputTokens, e.Counts.CachedInputTokens, e.Counts.CacheWriteInputTokens, e.Counts.OutputTokens, e.Counts.ReasoningOutputTokens, e.Counts.TotalTokens, d.InputTokens, d.CachedInputTokens, d.CacheWriteInputTokens, d.OutputTokens, d.ReasoningOutputTokens, d.TotalTokens, boolint(e.Counts.CacheWriteVisible), e.DataQuality, e.ParserVersion, epoch, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	_, err = tx.Exec(`INSERT INTO conversation_counters(host_id,conversation_id,epoch,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,updated_at)VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(host_id,conversation_id) DO UPDATE SET epoch=excluded.epoch,input_tokens=excluded.input_tokens,cached_input_tokens=excluded.cached_input_tokens,cache_write_input_tokens=excluded.cache_write_input_tokens,output_tokens=excluded.output_tokens,reasoning_output_tokens=excluded.reasoning_output_tokens,total_tokens=excluded.total_tokens,updated_at=excluded.updated_at`, e.HostID, e.ConversationID, epoch, e.Counts.InputTokens, e.Counts.CachedInputTokens, e.Counts.CacheWriteInputTokens, e.Counts.OutputTokens, e.Counts.ReasoningOutputTokens, e.Counts.TotalTokens, e.Timestamp.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var estimate int64
	_ = tx.QueryRow("SELECT live_estimate FROM sessions WHERE host_id=? AND conversation_id=?", e.HostID, e.ConversationID).Scan(&estimate)
	if estimate > 0 {
		_, _ = tx.Exec("INSERT INTO reconciliation(host_id,conversation_id,response_id,estimated_tokens,exact_output_tokens,error_tokens,timestamp)VALUES(?,?,?,?,?,?,?)", e.HostID, e.ConversationID, e.ResponseID, estimate, d.OutputTokens, estimate-d.OutputTokens, e.Timestamp.Format(time.RFC3339Nano))
	}
	_, err = tx.Exec(`INSERT INTO sessions(host_id,conversation_id,parent_conversation_id,project_id,repo_name,model,reasoning_effort,model_context_window,started_at,last_event_at,status,data_quality,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,live_estimate,last_exact_at)VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?) ON CONFLICT(host_id,conversation_id) DO UPDATE SET parent_conversation_id=COALESCE(excluded.parent_conversation_id,parent_conversation_id),project_id=excluded.project_id,repo_name=excluded.repo_name,model=excluded.model,reasoning_effort=excluded.reasoning_effort,model_context_window=excluded.model_context_window,last_event_at=excluded.last_event_at,status='EXACT',data_quality=excluded.data_quality,input_tokens=input_tokens+excluded.input_tokens,cached_input_tokens=cached_input_tokens+excluded.cached_input_tokens,cache_write_input_tokens=cache_write_input_tokens+excluded.cache_write_input_tokens,output_tokens=output_tokens+excluded.output_tokens,reasoning_output_tokens=reasoning_output_tokens+excluded.reasoning_output_tokens,total_tokens=total_tokens+excluded.total_tokens,live_estimate=0,last_exact_at=excluded.last_exact_at`, e.HostID, e.ConversationID, nullstr(e.ParentConversationID), e.ProjectID, e.RepoName, e.Model, e.ReasoningEffort, e.ModelContextWindow, e.Timestamp.Format(time.RFC3339Nano), e.Timestamp.Format(time.RFC3339Nano), "EXACT", e.DataQuality, d.InputTokens, d.CachedInputTokens, d.CacheWriteInputTokens, d.OutputTokens, d.ReasoningOutputTokens, d.TotalTokens, e.Timestamp.Format(time.RFC3339Nano))
	return err
}

func deltaCounts(n, o TokenCounts, reset bool) TokenCounts {
	d := TokenCounts{CacheWriteVisible: n.CacheWriteVisible}
	f := func(a, b int64) int64 {
		if reset || a < b {
			return a
		}
		return a - b
	}
	d.InputTokens = f(n.InputTokens, o.InputTokens)
	d.CachedInputTokens = f(n.CachedInputTokens, o.CachedInputTokens)
	d.CacheWriteInputTokens = f(n.CacheWriteInputTokens, o.CacheWriteInputTokens)
	d.OutputTokens = f(n.OutputTokens, o.OutputTokens)
	d.ReasoningOutputTokens = f(n.ReasoningOutputTokens, o.ReasoningOutputTokens)
	d.TotalTokens = f(n.TotalTokens, o.TotalTokens)
	return d
}
func nullstr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func boolint(v bool) int {
	if v {
		return 1
	}
	return 0
}
