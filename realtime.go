package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type RealtimeConfig struct {
	CoalesceMS  int `json:"coalesce_ms,omitempty"`
	HeartbeatMS int `json:"heartbeat_ms,omitempty"`
	DelayedMS   int `json:"delayed_ms,omitempty"`
	OfflineMS   int `json:"offline_ms,omitempty"`
	ProbeMS     int `json:"probe_ms,omitempty"`
}

func (c RealtimeConfig) normalized() RealtimeConfig {
	if c.CoalesceMS < 100 || c.CoalesceMS > 250 {
		c.CoalesceMS = 200
	}
	if c.HeartbeatMS < 1000 || c.HeartbeatMS > 10000 {
		c.HeartbeatMS = 5000
	}
	if c.DelayedMS < c.HeartbeatMS*2 {
		c.DelayedMS = max(12000, c.HeartbeatMS*2)
	}
	if c.OfflineMS <= c.DelayedMS {
		c.OfflineMS = max(30000, c.DelayedMS+10000)
	}
	if c.ProbeMS < 10000 {
		c.ProbeMS = 30000
	}
	return c
}

// Only numeric health metadata. Never attach log lines or model content here.
type AgentTelemetry struct {
	AgentEpoch    string    `json:"agent_epoch"`
	ReportSeq     uint64    `json:"report_seq"`
	AgentVersion  string    `json:"agent_version"`
	LastScanAt    time.Time `json:"last_scan_at"`
	LastUsageAt   time.Time `json:"last_usage_at"`
	LastUploadAt  time.Time `json:"last_upload_at"`
	PendingEvents int64     `json:"pending_events"`
	ScanMS        float64   `json:"scan_ms"`
	UploadMS      float64   `json:"upload_ms"`
	ScanAgeMS     int64     `json:"scan_age_ms"`
	ScanFailed    bool      `json:"scan_failed"`
	UploadFailed  bool      `json:"upload_failed"`
	ActiveFiles   int       `json:"active_files"`
}

type agentHealth struct {
	sync.Mutex
	value AgentTelemetry
}

func (s *server) watermark() map[string]any {
	var ledger int64
	var last string
	_ = s.db.QueryRow("SELECT ledger_revision,last_ledger_at FROM realtime_state WHERE id=1").Scan(&ledger, &last)
	s.hub.mu.Lock()
	revision, epoch, sent := s.hub.seq, s.hub.epoch, s.hub.lastSent
	s.hub.mu.Unlock()
	return map[string]any{"server_epoch": epoch, "revision": revision, "ledger_revision": ledger, "last_ledger_at": last, "server_time": time.Now().UTC(), "sse_last_sent_at": sent}
}

func (s *server) heartbeat() any {
	m := s.watermark()
	m["hosts"] = s.hostViews()
	m["runtime"] = s.runtimeViews()
	m["realtime_config"] = s.cfg.Realtime.normalized()
	return m
}

func (s *server) realtimeProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, s.heartbeat())
}

func (s *server) attachWatermark(value map[string]any, queryKey string, started time.Time) {
	for k, v := range s.watermark() {
		value[k] = v
	}
	value["data_revision"] = value["revision"]
	value["query_key"] = queryKey
	value["server_build_ms"] = float64(time.Since(started).Microseconds()) / 1000
	value["realtime_config"] = s.cfg.Realtime.normalized()
}

func decodeTelemetry(b []byte) *AgentTelemetry {
	var value AgentTelemetry
	if len(b) == 0 || json.Unmarshal(b, &value) != nil {
		return nil
	}
	return &value
}
