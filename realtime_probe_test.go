package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Opt-in, repeatable probes. Synthetic events and isolated SQLite only.
func TestRealtimeDeliveryProbe(t *testing.T) {
	if os.Getenv("METER_PROBE") == "" {
		t.Skip("set METER_PROBE=1 for timing probes")
	}
	h := newHub()
	c := make(chan sseMessage, 1)
	h.clients[c] = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.run(ctx, func() any { t.Error("notify must not aggregate"); return nil })
	var elapsed []float64
	for i := 0; i < 8; i++ {
		time.Sleep(time.Duration(37+i*29) * time.Millisecond)
		start := time.Now()
		h.mark()
		select {
		case <-c:
			elapsed = append(elapsed, float64(time.Since(start).Microseconds())/1000)
		case <-time.After(3 * time.Second):
			t.Fatal("no notification")
		}
	}
	sort.Float64s(elapsed)
	t.Logf("hub_mark_to_notification_ms n=%d p50=%.1f p95=%.1f", len(elapsed), elapsed[4], elapsed[7])
}

func TestSlowNetworkCollectionProbe(t *testing.T) {
	if os.Getenv("METER_PROBE") == "" {
		t.Skip("set METER_PROBE=1 for timing probes")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "codex")
	logs := filepath.Join(root, "sessions")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, "rollout-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-time.After(10 * time.Second):
			w.WriteHeader(504)
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	cfg := AgentConfig{HostID: "probe", Token: "synthetic-test", ServerURL: ts.URL, CodexHomes: []string{root}, StateDir: filepath.Join(dir, "state")}
	b, _ := json.Marshal(cfg)
	configPath := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- runAgent(ctx, configPath, false, false) }()
	defer func() { cancel(); <-finished }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not start")
	}
	start := time.Now()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"timestamp":"2026-09-05T00:00:00Z","payload":{"info":{"total_token_usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(filepath.Join(cfg.StateDir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var queued int
	for time.Since(start) < 12*time.Second {
		if err := db.QueryRow("SELECT COUNT(*) FROM spool").Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("scan_enqueue_during_10s_network_timeout_ms=%.1f queued=%d", float64(time.Since(start).Microseconds())/1000, queued)
	if queued == 0 {
		t.Fatal("usage was not eventually queued")
	}
}
