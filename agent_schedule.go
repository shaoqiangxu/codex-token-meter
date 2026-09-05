package main

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type scanScheduler struct {
	active                        map[string]time.Time
	running                       map[string]bool
	discovered, history, archived time.Time
}

// Network waits never own a SQLite transaction or the collector goroutine.
// Wakeups coalesce in a one-slot channel; failures ignore wakeups until the
// exponential backoff (with jitter) expires. The events remain in SQLite.
func agentNetworkLoop(ctx context.Context, wake <-chan struct{}, idle, minimum time.Duration, operation func() error, retry ...func(time.Time)) {
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := operation()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
			base := time.Second * time.Duration(1<<min(failures-1, 5))
			delay := base + time.Duration(rand.Float64()*float64(base)/4)
			for _, f := range retry {
				f(time.Now().Add(delay))
			}
			logSafe("agent network deferred; retry in %s", delay.Round(time.Millisecond))
			if !agentWait(ctx, delay) {
				return
			}
			continue
		}
		failures = 0
		for _, f := range retry {
			f(time.Time{})
		}
		if !agentWait(ctx, minimum) {
			return
		}
		timer := time.NewTimer(idle)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func agentWait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func wakeAgent(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func agentTelemetry(cfg *AgentConfig) *AgentTelemetry {
	if cfg.health == nil || cfg.localDB == nil {
		return nil
	}
	var pending int64
	var oldest string
	if err := cfg.localDB.QueryRow("SELECT COUNT(*),COALESCE(MIN(created_at),'') FROM spool").Scan(&pending, &oldest); err != nil {
		return nil
	}
	cfg.health.Lock()
	defer cfg.health.Unlock()
	cfg.health.value.ReportSeq++
	value := cfg.health.value
	value.PendingEvents = pending
	if oldest != "" {
		value.OldestPendingMS = max(0, time.Since(parseTime(oldest)).Milliseconds())
	}
	value.RetryRemainingMS = max(0, time.Until(value.RetryAt).Milliseconds())
	value.ScanAgeMS = -1
	if !value.LastScanAt.IsZero() {
		value.ScanAgeMS = time.Since(value.LastScanAt).Milliseconds()
	}
	return &value
}

func observeScan(cfg *AgentConfig, started time.Time, err error) {
	if cfg.health == nil {
		return
	}
	cfg.health.Lock()
	defer cfg.health.Unlock()
	cfg.health.value.ScanMS = float64(time.Since(started).Microseconds()) / 1000
	cfg.health.value.ScanFailed = err != nil
	cfg.health.value.ScanError = ""
	if err != nil {
		cfg.health.value.ScanError = safeScanError(err)
	}
	if err == nil {
		cfg.health.value.LastScanAt = time.Now().UTC()
	}
	if cfg.scheduler != nil {
		cfg.health.value.ActiveFiles = len(cfg.scheduler.active)
	}
}

func observeUpload(cfg *AgentConfig, started time.Time, success bool, code ...string) {
	if cfg.health == nil {
		return
	}
	cfg.health.Lock()
	defer cfg.health.Unlock()
	cfg.health.value.UploadMS = float64(time.Since(started).Microseconds()) / 1000
	cfg.health.value.UploadFailed = !success
	cfg.health.value.UploadError = ""
	if !success {
		cfg.health.value.UploadError = "network_or_ack_failure"
		if len(code) > 0 {
			cfg.health.value.UploadError = code[0]
		}
	}
	if success {
		cfg.health.value.LastUploadAt = time.Now().UTC()
	}
}

func scanScheduled(db *sql.DB, cfg *AgentConfig) error {
	now := time.Now()
	if cfg.scheduler == nil {
		cfg.scheduler = &scanScheduler{active: map[string]time.Time{}, running: map[string]bool{}, discovered: now, history: now, archived: now}
		if err := scanCodexFiles(db, cfg, false); err != nil {
			cfg.scheduler = nil
			return err
		}
		for _, stamp := range cfg.seen {
			if now.Sub(time.Unix(0, stamp.mtime)) < 2*time.Minute {
				cfg.scheduler.active[stamp.path] = now
			}
		}
		rows, err := db.Query("SELECT path FROM files WHERE runtime_state='running'")
		if err != nil {
			return err
		}
		for rows.Next() {
			var path string
			if rows.Scan(&path) == nil {
				cfg.scheduler.running[path] = true
				cfg.scheduler.active[path] = now
			}
		}
		rows.Close()
		return nil
	}
	s := cfg.scheduler
	paths := map[string]bool{}
	if cfg.watcher != nil {
		var rescan bool
		paths, rescan = cfg.watcher.drain()
		if rescan {
			s.discovered = time.Time{}
			s.history = time.Time{}
			s.archived = time.Time{}
		}
	}
	for path, at := range s.active {
		if s.running[path] || now.Sub(at) < 2*time.Minute {
			paths[path] = true
		} else {
			delete(s.active, path)
		}
	}
	discover := func(directory string) error {
		for _, home := range cfg.CodexHomes {
			err := filepath.WalkDir(filepath.Join(home, directory), func(path string, de os.DirEntry, err error) error {
				if err != nil {
					if os.IsNotExist(err) {
						return nil
					}
					return err
				}
				if !de.IsDir() && strings.HasSuffix(strings.ToLower(path), ".jsonl") {
					known, ok := cfg.seen[sourceIdentity(path)]
					if !ok || known.path != path {
						paths[path] = true
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	if now.Sub(s.discovered) >= 3*time.Second {
		if err := discover("sessions"); err != nil {
			return err
		}
		s.discovered = now
	}
	if now.Sub(s.history) >= 10*time.Second {
		for _, stamp := range cfg.seen {
			if !strings.Contains(filepath.ToSlash(stamp.path), "/archived_sessions/") {
				paths[stamp.path] = true
			}
		}
		s.history = now
	}
	if now.Sub(s.archived) >= time.Minute {
		if err := discover("archived_sessions"); err != nil {
			return err
		}
		for _, stamp := range cfg.seen {
			if strings.Contains(filepath.ToSlash(stamp.path), "/archived_sessions/") {
				paths[stamp.path] = true
			}
		}
		s.archived = now
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	var firstError error
	for _, path := range ordered {
		st, err := os.Stat(path)
		if os.IsNotExist(err) {
			delete(s.active, path)
			delete(s.running, path)
			continue
		}
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		sid := sourceIdentity(path)
		stamp := fileStamp{path: path, size: st.Size(), mtime: st.ModTime().UnixNano()}
		if cfg.seen[sid] == stamp {
			continue
		}
		if err := scanOne(db, cfg, path, false, false); err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		s.active[path] = now
		if err := rememberScannedFile(db, cfg, sid, stamp); err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
	}
	return firstError
}

func rememberScannedFile(db *sql.DB, cfg *AgentConfig, sid string, stamp fileStamp) error {
	var offset int64
	if err := db.QueryRow("SELECT offset FROM files WHERE source_file_id=?", sid).Scan(&offset); err != nil {
		return err
	}
	if cfg.seen == nil {
		cfg.seen = map[string]fileStamp{}
	}
	if offset >= stamp.size {
		cfg.seen[sid] = stamp
	} else {
		delete(cfg.seen, sid)
		if cfg.scheduler != nil {
			cfg.scheduler.active[stamp.path] = time.Now()
		}
	}
	return nil
}
