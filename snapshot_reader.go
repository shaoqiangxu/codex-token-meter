package main

import (
	"database/sql"
	"time"
)

type sqlReader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func (s *server) reader() sqlReader {
	if s.read != nil {
		return s.read
	}
	return s.db
}

// Acquire the independent WAL reader BEFORE the short view lock. Pin a SQLite
// read transaction and the matching revision together, then release the lock.
// Pricing and historical queries can no longer delay the ingest write lock or
// monopolize its single connection. Each response still sees ONE ledger view.
func (s *server) snapshotReader() (*server, func(), error) {
	if s.readPool == nil {
		s.viewMu.RLock()
		return s, func() { s.viewMu.RUnlock() }, nil
	}
	tx, err := s.readPool.Begin()
	if err != nil {
		return nil, nil, err
	}
	s.viewMu.RLock()
	var ledger int64
	var last string
	err = tx.QueryRow("SELECT ledger_revision,last_ledger_at FROM realtime_state WHERE id=1").Scan(&ledger, &last)
	s.hub.mu.Lock()
	seq, epoch, sent, taskSeq, fullSeq := s.hub.seq, s.hub.epoch, s.hub.lastSent, s.hub.taskSeq, s.hub.fullSeq
	s.hub.mu.Unlock()
	traces := s.traces.recent()
	s.viewMu.RUnlock()
	if err != nil {
		tx.Rollback()
		return nil, nil, err
	}
	v := &server{cfg: s.cfg, db: s.db, read: tx, hub: s.hub, accounting: s.accounting}
	v.pinnedWatermark = map[string]any{"server_epoch": epoch, "revision": seq, "runtime_revision": taskSeq, "full_revision": fullSeq, "ledger_revision": ledger, "last_ledger_at": last, "server_time": time.Now().UTC(), "sse_last_sent_at": sent}
	v.traces.entries = traces
	return v, func() { tx.Rollback() }, nil
}
