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
	"sort"
	"strings"
	"time"
)

func migrateAgent(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS files(source_file_id TEXT PRIMARY KEY,path TEXT NOT NULL,offset INTEGER NOT NULL,size INTEGER NOT NULL,mtime INTEGER NOT NULL,generation INTEGER NOT NULL DEFAULT 0,session_id TEXT,last_event_at TEXT,parser_version TEXT NOT NULL,model TEXT NOT NULL DEFAULT '',reasoning_effort TEXT NOT NULL DEFAULT '',project_id TEXT NOT NULL DEFAULT '',repo_name TEXT NOT NULL DEFAULT '',parent_id TEXT NOT NULL DEFAULT '',context_window INTEGER NOT NULL DEFAULT 0,turn_id TEXT NOT NULL DEFAULT '',response_id TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS spool(seq INTEGER PRIMARY KEY AUTOINCREMENT,event_id TEXT NOT NULL UNIQUE,payload BLOB NOT NULL,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		return err
	}
	for _, col := range []string{"model TEXT NOT NULL DEFAULT ''", "reasoning_effort TEXT NOT NULL DEFAULT ''", "project_id TEXT NOT NULL DEFAULT ''", "repo_name TEXT NOT NULL DEFAULT ''", "parent_id TEXT NOT NULL DEFAULT ''", "context_window INTEGER NOT NULL DEFAULT 0", "turn_id TEXT NOT NULL DEFAULT ''", "response_id TEXT NOT NULL DEFAULT ''"} {
		_, _ = db.Exec("ALTER TABLE files ADD COLUMN " + col)
	}
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
		if _, err := db.Exec("UPDATE files SET offset=0,generation=generation+1"); err != nil {
			return err
		}
	}
	scan := func() error {
		if err := scanCodexFiles(db, &cfg, backfill); err != nil {
			return err
		}
		return flushSpool(ctx, db, &cfg)
	}
	if err := scan(); err != nil && once {
		return err
	}
	if once {
		return nil
	}
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := scan(); err != nil {
				logSafe("agent scan/upload deferred: %v", err)
			}
		}
	}
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
		if cfg.seen == nil {
			cfg.seen = map[string]fileStamp{}
		}
		cfg.seen[sid] = stamp
	}
	if baselineNew {
		_, err := db.Exec("INSERT OR REPLACE INTO meta(key,value)VALUES('baseline_complete',?)", time.Now().UTC().Format(time.RFC3339Nano))
		return err
	}
	return nil
}

func scanOne(db *sql.DB, cfg *AgentConfig, path string, backfill, baselineNew bool) error {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	sid := sourceIdentity(path)
	var offset, size int64
	var generation int
	pc := parseContext{conversationID: sid}
	err = db.QueryRow("SELECT offset,size,generation,COALESCE(session_id,''),model,reasoning_effort,project_id,repo_name,parent_id,context_window,turn_id,response_id FROM files WHERE source_file_id=?", sid).Scan(&offset, &size, &generation, &pc.conversationID, &pc.model, &pc.effort, &pc.projectID, &pc.repoName, &pc.parentID, &pc.contextWindow, &pc.turnID, &pc.responseID)
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
	for {
		line, e := r.ReadBytes('\n')
		if e == io.EOF && len(line) > 0 {
			break
		}
		if e != nil && e != io.EOF {
			return e
		}
		lineStart := current
		current += int64(len(line))
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if ev, ok := parseCodexLine(line, cfg.HostID, sid, lineStart, &pc); ok {
			ev.EventID = stableID(ev.EventID, fmt.Sprint(generation))
			b, _ := json.Marshal(ev)
			_, _ = db.Exec("INSERT OR IGNORE INTO spool(event_id,payload,created_at)VALUES(?,?,?)", ev.EventID, b, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if e == io.EOF {
			break
		}
	}
	_, err = db.Exec("UPDATE files SET path=?,offset=?,size=?,mtime=?,generation=?,session_id=?,last_event_at=?,parser_version=?,model=?,reasoning_effort=?,project_id=?,repo_name=?,parent_id=?,context_window=?,turn_id=?,response_id=? WHERE source_file_id=?", path, current, st.Size(), st.ModTime().UnixNano(), generation, pc.conversationID, time.Now().UTC().Format(time.RFC3339Nano), parserVersion, pc.model, pc.effort, pc.projectID, pc.repoName, pc.parentID, pc.contextWindow, pc.turnID, pc.responseID, sid)
	return err
}

func queueBaseline(db *sql.DB, cfg *AgentConfig, path, sid string, size int64) error {
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
		b, readErr := r.ReadBytes('\n')
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
		rows, err := db.Query("SELECT seq,payload FROM spool ORDER BY seq LIMIT 256")
		if err != nil {
			return err
		}
		var seqs []int64
		batch := IngestBatch{HostID: cfg.HostID}
		for rows.Next() {
			var seq int64
			var b []byte
			if rows.Scan(&seq, &b) == nil {
				var e UsageEvent
				if json.Unmarshal(b, &e) == nil {
					seqs = append(seqs, seq)
					batch.Events = append(batch.Events, e)
				}
			}
		}
		rows.Close()
		if len(batch.Events) == 0 {
			return nil
		}
		body, _ := json.Marshal(batch)
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.ServerURL, "/")+"/api/ingest", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("ingest HTTP %d", resp.StatusCode)
		}
		for _, seq := range seqs {
			_, _ = db.Exec("DELETE FROM spool WHERE seq=?", seq)
		}
	}
}

func logSafe(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
}
