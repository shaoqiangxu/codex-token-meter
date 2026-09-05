package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

func migrateAgent(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS files(source_file_id TEXT PRIMARY KEY,path TEXT NOT NULL,offset INTEGER NOT NULL,size INTEGER NOT NULL,mtime INTEGER NOT NULL,generation INTEGER NOT NULL DEFAULT 0,session_id TEXT,last_event_at TEXT,parser_version TEXT NOT NULL,model TEXT NOT NULL DEFAULT '',reasoning_effort TEXT NOT NULL DEFAULT '',project_id TEXT NOT NULL DEFAULT '',repo_name TEXT NOT NULL DEFAULT '',parent_id TEXT NOT NULL DEFAULT '',context_window INTEGER NOT NULL DEFAULT 0,turn_id TEXT NOT NULL DEFAULT '',response_id TEXT NOT NULL DEFAULT '',last_counter_total INTEGER NOT NULL DEFAULT 0,counter_initialized INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS spool(seq INTEGER PRIMARY KEY AUTOINCREMENT,event_id TEXT NOT NULL UNIQUE,payload BLOB NOT NULL,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		return err
	}
	for _, col := range []string{"model TEXT NOT NULL DEFAULT ''", "reasoning_effort TEXT NOT NULL DEFAULT ''", "project_id TEXT NOT NULL DEFAULT ''", "repo_name TEXT NOT NULL DEFAULT ''", "parent_id TEXT NOT NULL DEFAULT ''", "context_window INTEGER NOT NULL DEFAULT 0", "turn_id TEXT NOT NULL DEFAULT ''", "response_id TEXT NOT NULL DEFAULT ''", "last_counter_total INTEGER NOT NULL DEFAULT 0", "counter_initialized INTEGER NOT NULL DEFAULT 0"} {
		_, _ = db.Exec("ALTER TABLE files ADD COLUMN " + col)
	}
	_, _ = db.Exec("ALTER TABLE files ADD COLUMN runtime_state TEXT NOT NULL DEFAULT ''")
	return nil
}

func runAgent(ctx context.Context, configPath string, once, backfill bool) error {
	var cfg AgentConfig
	if err := readJSON(configPath, &cfg); err != nil {
		return err
	}
	if cfg.StateDir == "" {
		if runtime.GOOS == "windows" {
			cfg.StateDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "CodexTokenMeter", "state")
		} else {
			cfg.StateDir = "/var/lib/codex-token-meter/agent"
		}
	}
	db, err := openSQLite(filepath.Join(cfg.StateDir, "agent.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = migrateAgent(db); err != nil {
		return err
	}
	if len(cfg.CodexHomes) == 0 {
		cfg.CodexHomes = codexHomesDefault()
	}
	if backfill {
		if _, err := db.Exec("UPDATE files SET offset=0,generation=generation+1,last_counter_total=0,counter_initialized=0"); err != nil {
			return err
		}
	}
	epoch, err := randomToken(12)
	if err != nil {
		return err
	}
	cfg.localDB = db
	cfg.health = &agentHealth{value: AgentTelemetry{AgentEpoch: epoch, AgentVersion: version, ProcessID: os.Getpid(), ProcessStartedAt: time.Now().UTC()}}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				cfg.health.value.BuildCommit = setting.Value
			}
		}
	}
	var lastUsage string
	_ = db.QueryRow("SELECT value FROM meta WHERE key='last_usage_at'").Scan(&lastUsage)
	cfg.health.value.LastUsageAt = parseTime(lastUsage)
	var lastUpload string
	_ = db.QueryRow("SELECT value FROM meta WHERE key='last_upload_at'").Scan(&lastUpload)
	cfg.health.value.LastUploadAt = parseTime(lastUpload)
	if once {
		started := time.Now()
		err := scanCodexFiles(db, &cfg, backfill)
		observeScan(&cfg, started, err)
		if err != nil {
			return err
		}
		if err := flushSpool(ctx, db, &cfg); err != nil {
			return err
		}
		return sendHeartbeat(ctx, &cfg)
	}
	ctx, cancel := context.WithCancel(ctx)
	cfg.watcher, err = watchSources(cfg.CodexHomes)
	var fileWake <-chan struct{}
	if err == nil {
		defer cfg.watcher.watcher.Close()
		fileWake = cfg.watcher.wake
		cfg.health.value.WatchAvailable = true
	} else {
		logSafe("file notifications unavailable; discovery fallback active")
	}
	uploadWake, healthWake := make(chan struct{}, 1), make(chan struct{}, 1)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		agentNetworkLoop(ctx, uploadWake, time.Second, 0, func() error {
			cfg.health.Lock()
			before := cfg.health.value.LastUploadAt
			cfg.health.Unlock()
			err := flushSpool(ctx, db, &cfg)
			cfg.health.Lock()
			changed := cfg.health.value.LastUploadAt != before
			cfg.health.Unlock()
			if changed {
				wakeAgent(healthWake)
			}
			return err
		}, func(at time.Time) { cfg.health.Lock(); cfg.health.value.RetryAt = at; cfg.health.Unlock() })
	}()
	go func() {
		defer workers.Done()
		agentNetworkLoop(ctx, healthWake, 4*time.Second, time.Second, func() error { return sendHeartbeat(ctx, &cfg) })
	}()
	defer func() { cancel(); workers.Wait() }()
	var lastScanError time.Time
	scan := func() {
		started := time.Now()
		err := scanScheduled(db, &cfg)
		observeScan(&cfg, started, err)
		wakeAgent(uploadWake)
		if err != nil && time.Since(lastScanError) >= 30*time.Second {
			logSafe("agent scan blocked: %s (checkpoint retained)", safeScanError(err))
			lastScanError = time.Now()
		}
	}
	scan()
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			scan()
		case <-fileWake:
			scan()
		}
	}
}

func sendHeartbeat(ctx context.Context, cfg *AgentConfig) error {
	metadata := pendingSessionMetadata(cfg)
	body, _ := json.Marshal(IngestBatch{HostID: cfg.HostID, Metadata: metadata, Telemetry: agentTelemetry(cfg)})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.ServerURL, "/")+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("heartbeat HTTP %d", resp.StatusCode)
	}
	if cfg.metadataSent == nil {
		cfg.metadataSent = map[string]string{}
	}
	for _, item := range metadata {
		cfg.metadataSent[item.ConversationID] = metadataSignature(item)
	}
	return nil
}

func pendingSessionMetadata(cfg *AgentConfig) []SessionMetadata {
	if !cfg.lastMetadataScan.IsZero() && time.Since(cfg.lastMetadataScan) < 30*time.Second {
		return nil
	}
	cfg.lastMetadataScan = time.Now()
	all := collectSessionMetadata(cfg.CodexHomes)
	pending := make([]SessionMetadata, 0, len(all))
	for _, item := range all {
		if cfg.metadataSent == nil || cfg.metadataSent[item.ConversationID] != metadataSignature(item) {
			pending = append(pending, item)
		}
	}
	if len(pending) > 256 {
		pending = pending[:256]
	}
	return pending
}

func metadataSignature(item SessionMetadata) string {
	return item.ConversationName + "\x00" + item.ProjectName
}

func scanCodexFiles(db *sql.DB, cfg *AgentConfig, backfill bool) error {
	baselineNew := false
	if !backfill {
		var done string
		if err := db.QueryRow("SELECT value FROM meta WHERE key='baseline_complete'").Scan(&done); errors.Is(err, sql.ErrNoRows) {
			baselineNew = true
		}
	}
	var paths []string
	for _, home := range cfg.CodexHomes {
		for _, d := range []string{"sessions", "archived_sessions"} {
			_ = filepath.WalkDir(filepath.Join(home, d), func(path string, de os.DirEntry, err error) error {
				if err == nil && !de.IsDir() && strings.HasSuffix(strings.ToLower(path), ".jsonl") {
					paths = append(paths, path)
				}
				return nil
			})
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		st, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		sid := sourceIdentity(path)
		stamp := fileStamp{path: path, size: st.Size(), mtime: st.ModTime().UnixNano()}
		if cfg.seen != nil && cfg.seen[sid] == stamp {
			continue
		}
		if err := scanOne(db, cfg, path, backfill, baselineNew); err != nil {
			return fmt.Errorf("scan %s: %w", filepath.Base(path), err)
		}
		if err := rememberScannedFile(db, cfg, sid, stamp); err != nil {
			return err
		}
	}
	if baselineNew {
		_, err := db.Exec("INSERT OR REPLACE INTO meta(key,value)VALUES('baseline_complete',?)", time.Now().UTC().Format(time.RFC3339Nano))
		return err
	}
	return nil
}

func scanOne(db *sql.DB, cfg *AgentConfig, path string, backfill, baselineNew bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := scanOneTx(tx, cfg, path, backfill, baselineNew); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if cfg.scheduler != nil {
		var state string
		_ = db.QueryRow("SELECT runtime_state FROM files WHERE source_file_id=?", sourceIdentity(path)).Scan(&state)
		if cfg.scheduler.running == nil {
			cfg.scheduler.running = map[string]bool{}
		}
		cfg.scheduler.running[path] = state == "running"
	}
	if cfg.health != nil {
		var usage string
		_ = db.QueryRow("SELECT value FROM meta WHERE key='last_usage_at'").Scan(&usage)
		cfg.health.Lock()
		cfg.health.value.LastUsageAt = parseTime(usage)
		cfg.health.Unlock()
	}
	return nil
}

func scanOneTx(db *sql.Tx, cfg *AgentConfig, path string, backfill, baselineNew bool) error {
	observedAt := time.Now().UTC()
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	sid := sourceIdentity(path)
	var offset, size int64
	var generation int
	var lastCounterTotal int64
	var counterInitialized bool
	pc := parseContext{conversationID: sid}
	err = db.QueryRow("SELECT offset,size,generation,COALESCE(session_id,''),model,reasoning_effort,project_id,repo_name,parent_id,context_window,turn_id,response_id,last_counter_total,counter_initialized FROM files WHERE source_file_id=?", sid).Scan(&offset, &size, &generation, &pc.conversationID, &pc.model, &pc.effort, &pc.projectID, &pc.repoName, &pc.parentID, &pc.contextWindow, &pc.turnID, &pc.responseID, &lastCounterTotal, &counterInitialized)
	if errors.Is(err, sql.ErrNoRows) {
		start := int64(0)
		if baselineNew {
			start = st.Size()
		}
		_, err = db.Exec("INSERT INTO files(source_file_id,path,offset,size,mtime,generation,session_id,last_event_at,parser_version)VALUES(?,?,?,?,?,?,?,?,?)", sid, path, start, st.Size(), st.ModTime().UnixNano(), 0, sid, "", parserVersion)
		if err == nil && baselineNew {
			err = queueBaseline(db, cfg, path, sid, st.Size())
		}
		if err != nil || baselineNew {
			return err
		}
		offset = 0
		size = st.Size()
		generation = 0
	} else if err != nil {
		return err
	}
	if st.Size() < offset {
		offset = 0
		generation++
		lastCounterTotal = 0
		counterInitialized = false
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 256*1024)
	current := offset
	lines := 0
	for {
		line, e := readMeterLine(r)
		if e == io.EOF && len(line) > 0 {
			break
		}
		if e != nil && e != io.EOF {
			return &scanBlock{sid, current, "line_read_blocked_limit_64MiB"}
		}
		lineStart := current
		current += int64(len(line))
		lines++
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if ev, ok := parseCodexLine(line, cfg.HostID, sid, lineStart, &pc); ok {
			if ev.EventType == "activity" || ev.EventType == "runtime" || ev.EventType == "live_estimate" {
				state := "running"
				if ev.RunState == "idle" {
					state = "idle"
				}
				if _, err := db.Exec("UPDATE files SET runtime_state=? WHERE source_file_id=?", state, sid); err != nil {
					return err
				}
			}
			if ev.EventType == "exact_usage" {
				// Codex can restart its cumulative token counter inside the same
				// append-only rollout file (for example after a task is resumed).
				// Advance the source epoch so the server counts the new run instead
				// of mistaking every smaller cumulative value for stale data.
				if counterInitialized && ev.Counts.TotalTokens < lastCounterTotal {
					generation++
				}
				lastCounterTotal = ev.Counts.TotalTokens
				counterInitialized = true
			}
			ev.SourceEpoch = generation
			ev.EventID = stableID(ev.EventID, fmt.Sprint(generation))
			ev.Trace = &DeliveryTrace{ObservedAt: observedAt, QueuedAt: time.Now().UTC()}
			b, _ := json.Marshal(ev)
			if _, err := db.Exec("INSERT OR IGNORE INTO spool(event_id,payload,created_at)VALUES(?,?,?)", ev.EventID, b, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
			if ev.EventType == "exact_usage" {
				if _, err := db.Exec("INSERT OR REPLACE INTO meta(key,value)VALUES('last_usage_at',?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
					return err
				}
			}
		}
		if e == io.EOF {
			break
		}
		if cfg.scheduler != nil && !backfill && (lines >= 512 || current-offset >= 1024*1024) {
			break
		}
	}
	_, err = db.Exec("UPDATE files SET path=?,offset=?,size=?,mtime=?,generation=?,session_id=?,last_event_at=?,parser_version=?,model=?,reasoning_effort=?,project_id=?,repo_name=?,parent_id=?,context_window=?,turn_id=?,response_id=?,last_counter_total=?,counter_initialized=? WHERE source_file_id=?", path, current, st.Size(), st.ModTime().UnixNano(), generation, pc.conversationID, time.Now().UTC().Format(time.RFC3339Nano), parserVersion, pc.model, pc.effort, pc.projectID, pc.repoName, pc.parentID, pc.contextWindow, pc.turnID, pc.responseID, lastCounterTotal, counterInitialized, sid)
	return err
}

type scanBlock struct {
	source string
	offset int64
	code   string
}

func (e *scanBlock) Error() string {
	return fmt.Sprintf("source_id=%s offset=%d code=%s", e.source, e.offset, e.code)
}
func safeScanError(err error) string {
	var blocked *scanBlock
	if errors.As(err, &blocked) {
		return blocked.Error()
	}
	// Paths and arbitrary parser/OS error text never leave the collector.
	return "code=scan_io_or_database_error"
}

func queueBaseline(db interface {
	Exec(string, ...any) (sql.Result, error)
}, cfg *AgentConfig, path, sid string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256*1024)
	pc := parseContext{conversationID: sid}
	lastByConversation := map[string]*UsageEvent{}
	off := int64(0)
	for {
		b, readErr := readMeterLine(r)
		lineStart := off
		off += int64(len(b))
		// Most records contain messages or tool data. Avoid decoding them entirely.
		if bytes.Contains(b, []byte(`"total_token_usage"`)) {
			b = bytes.TrimSuffix(b, []byte{'\n'})
			b = bytes.TrimSuffix(b, []byte{'\r'})
			if ev, ok := parseCodexLine(b, cfg.HostID, sid, lineStart, &pc); ok && ev.EventType == "exact_usage" {
				lastByConversation[ev.ConversationID] = ev
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	for conversationID, last := range lastByConversation {
		last.EventType = "baseline"
		last.EventID = stableID(cfg.HostID, sid, conversationID, "baseline", fmt.Sprint(size))
		b, _ := json.Marshal(last)
		if _, err = db.Exec("INSERT OR IGNORE INTO spool(event_id,payload,created_at)VALUES(?,?,?)", last.EventID, b, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

func flushSpool(ctx context.Context, db *sql.DB, cfg *AgentConfig) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := db.Query("SELECT seq,payload FROM spool ORDER BY seq LIMIT 256")
		if err != nil {
			return err
		}
		var seqs []int64
		batch := IngestBatch{HostID: cfg.HostID}
		for rows.Next() {
			var seq int64
			var b []byte
			if err := rows.Scan(&seq, &b); err != nil {
				rows.Close()
				return err
			}
			var e UsageEvent
			if err := json.Unmarshal(b, &e); err != nil {
				rows.Close()
				return fmt.Errorf("invalid spool event at sequence %d; retained", seq)
			}
			seqs = append(seqs, seq)
			batch.Events = append(batch.Events, e)
		}
		rows.Close()
		if len(batch.Events) == 0 {
			return nil
		}
		batch.Telemetry = agentTelemetry(cfg)
		for i := range batch.Events {
			if batch.Events[i].Trace != nil {
				batch.Events[i].Trace.SentAt = time.Now().UTC()
			}
		}
		body, _ := json.Marshal(batch)
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.ServerURL, "/")+"/api/ingest", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		started := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			observeUpload(cfg, started, false)
			return err
		}
		var acknowledgement struct {
			Accepted int `json:"accepted"`
		}
		ackErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&acknowledgement)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			observeUpload(cfg, started, false)
			return fmt.Errorf("ingest HTTP %d", resp.StatusCode)
		}
		if ackErr != nil || acknowledgement.Accepted != len(batch.Events) {
			observeUpload(cfg, started, false)
			return errors.New("upload acknowledgement missing; spool retained")
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, seq := range seqs {
			if _, err = tx.Exec("DELETE FROM spool WHERE seq=?", seq); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err = tx.Exec("INSERT OR REPLACE INTO meta(key,value)VALUES('last_upload_at',?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		observeUpload(cfg, started, true)
	}
}

func logSafe(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
}

// Read in 256KiB chunks with a hard bound, not Scanner's small token limit.
// Real Codex compaction records can exceed 8MiB (observed 8,970,082 bytes).
// Do not skip them: the existing parser and checkpoint/spool transaction still
// see every complete record, including any usage. Larger records remain an
// explicit, source/offset-addressable block, never silent data loss.
func readMeterLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > 64*1024*1024 {
			return nil, errors.New("JSONL line exceeds 64MiB; checkpoint retained")
		}
		line = append(line, part...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}
