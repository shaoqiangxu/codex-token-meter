package main

import (
	"database/sql"
	"time"
)

const parserVersion = "codex-jsonl-v2"

type TokenCounts struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
	CacheWriteVisible     bool  `json:"cache_write_visible"`
}

type UsageEvent struct {
	EventID              string         `json:"event_id"`
	HostID               string         `json:"host_id"`
	SourceFileID         string         `json:"source_file_id"`
	ByteOffset           int64          `json:"byte_offset"`
	SourceEpoch          int            `json:"source_epoch"`
	EventType            string         `json:"event_type"`
	Timestamp            time.Time      `json:"timestamp"`
	ConversationID       string         `json:"conversation_id"`
	ParentConversationID string         `json:"parent_conversation_id,omitempty"`
	TurnID               string         `json:"turn_id,omitempty"`
	ResponseID           string         `json:"response_id,omitempty"`
	ProjectID            string         `json:"project_id,omitempty"`
	RepoName             string         `json:"repo_name,omitempty"`
	Model                string         `json:"model,omitempty"`
	ReasoningEffort      string         `json:"reasoning_effort,omitempty"`
	ModelContextWindow   int64          `json:"model_context_window,omitempty"`
	Counts               TokenCounts    `json:"counts"`
	DataQuality          string         `json:"data_quality"`
	ParserVersion        string         `json:"parser_version"`
	LiveEstimate         int64          `json:"live_estimate,omitempty"`
	RunState             string         `json:"runtime_state,omitempty"`
	Trace                *DeliveryTrace `json:"trace,omitempty"`
}

// SessionMetadata contains display-only identifiers. The agent intentionally
// reads only Codex's explicit thread name and project/repository metadata; it
// never sends prompts, previews, messages, or tool output.
type SessionMetadata struct {
	ConversationID   string `json:"conversation_id"`
	ConversationName string `json:"conversation_name,omitempty"`
	ProjectName      string `json:"project_name,omitempty"`
}

type IngestBatch struct {
	HostID    string            `json:"host_id"`
	Events    []UsageEvent      `json:"events"`
	Metadata  []SessionMetadata `json:"metadata,omitempty"`
	Telemetry *AgentTelemetry   `json:"telemetry,omitempty"`
}

type AgentConfig struct {
	ServerURL           string    `json:"server_url"`
	HostID              string    `json:"host_id"`
	HostAlias           string    `json:"host_alias"`
	Token               string    `json:"token"`
	CodexHomes          []string  `json:"codex_homes"`
	StateDir            string    `json:"state_dir"`
	MonitoringStartedAt time.Time `json:"monitoring_started_at"`
	AbsolutePaths       bool      `json:"absolute_paths"`
	seen                map[string]fileStamp
	metadataSent        map[string]string
	lastMetadataScan    time.Time
	health              *agentHealth
	localDB             *sql.DB
	scheduler           *scanScheduler
}

type fileStamp struct {
	path        string
	size, mtime int64
}

type ServerConfig struct {
	Listen            string            `json:"listen"`
	DataDir           string            `json:"data_dir"`
	AdminUser         string            `json:"admin_user"`
	AdminPasswordHash string            `json:"admin_password_hash"`
	SessionSecret     string            `json:"session_secret"`
	PublicURL         string            `json:"public_url"`
	ArtifactDir       string            `json:"artifact_dir"`
	ProjectAliases    map[string]string `json:"project_aliases,omitempty"`
	Realtime          RealtimeConfig    `json:"realtime,omitempty"`
}
