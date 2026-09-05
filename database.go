package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func openSQLite(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return db, nil
}

func migrateServer(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS agents (
 host_id TEXT PRIMARY KEY, alias TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
 platform TEXT, created_at TEXT NOT NULL, last_seen TEXT, revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS realtime_state (
 id INTEGER PRIMARY KEY CHECK(id=1), ledger_revision INTEGER NOT NULL DEFAULT 0, last_ledger_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS agent_telemetry (
 host_id TEXT PRIMARY KEY, report BLOB NOT NULL, received_at TEXT NOT NULL,
 FOREIGN KEY(host_id) REFERENCES agents(host_id)
);
CREATE TABLE IF NOT EXISTS task_runtime (
 host_id TEXT NOT NULL, conversation_id TEXT NOT NULL, state TEXT NOT NULL,
 turn_id TEXT NOT NULL DEFAULT '', evidence_at TEXT NOT NULL,received_at TEXT NOT NULL,
 live_estimate INTEGER NOT NULL DEFAULT 0,last_exact_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(host_id,conversation_id),FOREIGN KEY(host_id) REFERENCES agents(host_id)
);
CREATE TABLE IF NOT EXISTS enrollments (
 token_hash TEXT PRIMARY KEY, platform TEXT NOT NULL, expires_at TEXT NOT NULL,
 used_at TEXT, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
 host_id TEXT NOT NULL, conversation_id TEXT NOT NULL, parent_conversation_id TEXT,
 project_id TEXT, repo_name TEXT, display_name TEXT, model TEXT, reasoning_effort TEXT,
 model_context_window INTEGER NOT NULL DEFAULT 0, started_at TEXT, last_event_at TEXT,
 status TEXT NOT NULL DEFAULT 'EXACT', data_quality TEXT NOT NULL DEFAULT 'EXACT',
 input_tokens INTEGER NOT NULL DEFAULT 0, cached_input_tokens INTEGER NOT NULL DEFAULT 0,
 cache_write_input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
 reasoning_output_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
 live_estimate INTEGER NOT NULL DEFAULT 0, last_exact_at TEXT,
 PRIMARY KEY(host_id, conversation_id), FOREIGN KEY(host_id) REFERENCES agents(host_id)
);
CREATE TABLE IF NOT EXISTS session_metadata (
 host_id TEXT NOT NULL, conversation_id TEXT NOT NULL, conversation_name TEXT,
 project_name TEXT, updated_at TEXT NOT NULL,
 PRIMARY KEY(host_id, conversation_id), FOREIGN KEY(host_id) REFERENCES agents(host_id)
);
CREATE TABLE IF NOT EXISTS conversation_counters (
 host_id TEXT NOT NULL, conversation_id TEXT NOT NULL, epoch INTEGER NOT NULL DEFAULT 0,
 input_tokens INTEGER NOT NULL, cached_input_tokens INTEGER NOT NULL,
 cache_write_input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
 reasoning_output_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL,
 updated_at TEXT NOT NULL, PRIMARY KEY(host_id, conversation_id)
);
CREATE TABLE IF NOT EXISTS source_counters (
 host_id TEXT NOT NULL, source_file_id TEXT NOT NULL, epoch INTEGER NOT NULL DEFAULT 0,
 input_tokens INTEGER NOT NULL, cached_input_tokens INTEGER NOT NULL,
 cache_write_input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
 reasoning_output_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL,
 updated_at TEXT NOT NULL, PRIMARY KEY(host_id,source_file_id)
);
CREATE TABLE IF NOT EXISTS usage_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
 dedup_key TEXT, host_id TEXT NOT NULL, conversation_id TEXT NOT NULL,
 parent_conversation_id TEXT, source_file_id TEXT NOT NULL, byte_offset INTEGER NOT NULL,
 event_type TEXT NOT NULL, timestamp TEXT NOT NULL, turn_id TEXT, response_id TEXT,
 project_id TEXT, model TEXT, reasoning_effort TEXT, model_context_window INTEGER,
 raw_input_tokens INTEGER NOT NULL, raw_cached_input_tokens INTEGER NOT NULL,
 raw_cache_write_input_tokens INTEGER NOT NULL, raw_output_tokens INTEGER NOT NULL,
 raw_reasoning_output_tokens INTEGER NOT NULL, raw_total_tokens INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL, cached_input_tokens INTEGER NOT NULL,
 cache_write_input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
 reasoning_output_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL,
 cache_write_visible INTEGER NOT NULL, data_quality TEXT NOT NULL, parser_version TEXT NOT NULL,
 epoch INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ingested_events (
 event_id TEXT PRIMARY KEY, host_id TEXT NOT NULL, event_type TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS usage_dedup ON usage_events(host_id, dedup_key) WHERE dedup_key IS NOT NULL AND dedup_key <> '';
CREATE INDEX IF NOT EXISTS usage_time ON usage_events(timestamp);
CREATE INDEX IF NOT EXISTS runtime_received ON task_runtime(received_at);
CREATE INDEX IF NOT EXISTS usage_session ON usage_events(host_id, conversation_id, timestamp);
INSERT OR IGNORE INTO realtime_state(id,ledger_revision,last_ledger_at)
 SELECT 1,COALESCE(MAX(id),0),COALESCE(MAX(created_at),'') FROM usage_events;
CREATE TABLE IF NOT EXISTS reconciliation (
 id INTEGER PRIMARY KEY AUTOINCREMENT, host_id TEXT NOT NULL, conversation_id TEXT NOT NULL,
 response_id TEXT, estimated_tokens INTEGER NOT NULL, exact_output_tokens INTEGER NOT NULL,
 error_tokens INTEGER NOT NULL, timestamp TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS prices (
 id INTEGER PRIMARY KEY AUTOINCREMENT, provider TEXT NOT NULL, plan_profile TEXT NOT NULL,
 model TEXT NOT NULL, effective_from TEXT NOT NULL, effective_to TEXT,
 input_rate REAL NOT NULL, cached_input_rate REAL NOT NULL, cache_write_rate REAL,
 output_rate REAL NOT NULL, long_context_threshold INTEGER,
 long_input_multiplier REAL NOT NULL DEFAULT 1, long_output_multiplier REAL NOT NULL DEFAULT 1,
 currency TEXT NOT NULL, source_name TEXT NOT NULL, verified_at TEXT NOT NULL,
 stale INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS exchange_rates (
 base_currency TEXT NOT NULL, quote_currency TEXT NOT NULL, rate REAL NOT NULL,
 rate_date TEXT NOT NULL, source_name TEXT NOT NULL, fetched_at TEXT NOT NULL,
 stale INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(base_currency, quote_currency)
);
CREATE TABLE IF NOT EXISTS credit_purchases (
 id INTEGER PRIMARY KEY AUTOINCREMENT, purchase_time TEXT NOT NULL, paid_amount REAL NOT NULL,
 currency TEXT NOT NULL, credits_received REAL NOT NULL, fees REAL NOT NULL DEFAULT 0,
 exchange_rate REAL NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS audit_log (
 id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL, action TEXT NOT NULL,
 actor TEXT NOT NULL, details TEXT NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if err := seedPrices(db); err != nil {
		return err
	}
	return ensureCodexProfiles(db)
}

func seedPrices(db *sql.DB) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM prices").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows := []struct {
		provider, profile, model string
		input, cached            float64
		write                    *float64
		output                   float64
		threshold                *int64
		im, om                   float64
		currency, source         string
	}{
		{"openai", "API", "gpt-5.6-sol", 4, .40, fptr(5), 20, iptr(272000), 2, 1.5, "USD", "OpenAI API pricing supplied for deployment"},
		{"vercel", "AI Gateway public", "openai/gpt-5.6-sol", 2, .20, fptr(2.5), 10, iptr(272000), 2, 1.5, "USD", "Vercel AI Gateway public model catalog"},
		{"codex", "Plus/Pro Current", "gpt-5.6-sol", 100, 10, fptr(0), 500, nil, 1, 1, "credits", "OpenAI personal-plan credit pricing"},
		{"codex", "Plus/Pro Legacy 125", "gpt-5.6-sol", 125, 12.5, fptr(0), 750, nil, 1, 1, "credits", "Deployment-supplied legacy Codex rate card"},
		{"codex", "Business/Enterprise Current", "gpt-5.6-sol", 100, 10, fptr(0), 500, nil, 1, 1, "credits", "OpenAI Business/Enterprise credit rate card"},
		{"codex", "Manual", "gpt-5.6-sol", 0, 0, nil, 0, nil, 1, 1, "credits", "Administrator custom profile"},
	}
	for _, r := range rows {
		_, err := db.Exec(`INSERT INTO prices(provider,plan_profile,model,effective_from,input_rate,cached_input_rate,cache_write_rate,output_rate,long_context_threshold,long_input_multiplier,long_output_multiplier,currency,source_name,verified_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.provider, r.profile, r.model, now, r.input, r.cached, r.write, r.output, r.threshold, r.im, r.om, r.currency, r.source, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func ensureCodexProfiles(db *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec("UPDATE prices SET plan_profile='Plus/Pro Legacy 125',effective_to=COALESCE(effective_to,?) WHERE provider='codex' AND plan_profile='Plus/Pro' AND input_rate=125 AND output_rate=750", now)
	for _, r := range []struct {
		profile               string
		input, cached, output float64
		source                string
	}{{"Plus/Pro Current", 100, 10, 500, "OpenAI personal-plan credit pricing"}, {"Business/Enterprise Current", 100, 10, 500, "OpenAI Business/Enterprise credit rate card"}} {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM prices WHERE provider='codex' AND plan_profile=? AND model='gpt-5.6-sol' AND effective_to IS NULL", r.profile).Scan(&n)
		if n == 0 {
			if _, err := db.Exec(`INSERT INTO prices(provider,plan_profile,model,effective_from,input_rate,cached_input_rate,cache_write_rate,output_rate,long_input_multiplier,long_output_multiplier,currency,source_name,verified_at)VALUES('codex',?,'gpt-5.6-sol',?,?,?,0,?,1,1,'credits',?,?)`, r.profile, now, r.input, r.cached, r.output, r.source, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func fptr(v float64) *float64 { return &v }
func iptr(v int64) *int64     { return &v }
