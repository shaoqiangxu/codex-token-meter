package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var uuidInName = regexp.MustCompile(`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

type parseContext struct {
	conversationID, parentID, model, effort, projectID, repoName, turnID, responseID string
	contextWindow                                                                    int64
}

func sourceIdentity(path string) string {
	m := uuidInName.FindAllString(filepath.Base(path), -1)
	if len(m) > 0 {
		return strings.ToLower(m[len(m)-1])
	}
	return stableID(strings.ToLower(filepath.Clean(path)))[:32]
}

func parseCodexLine(raw []byte, hostID, sourceID string, offset int64, pc *parseContext) (*UsageEvent, bool) {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil, false
	}
	payload, _ := root["payload"].(map[string]any)
	recordType, _ := root["type"].(string)
	if payload == nil && recordType == "response.output_text.delta" {
		payload = root
	}
	previousTurn := pc.turnID
	updateParseContext(recordType, payload, pc)
	if pc.conversationID == "" {
		pc.conversationID = sourceID
	}
	ts, _ := root["timestamp"].(string)
	when := parseTime(ts)
	if when.IsZero() {
		when = time.Now().UTC()
	}
	if info, ok := payload["info"].(map[string]any); ok {
		if total, ok := info["total_token_usage"].(map[string]any); ok {
			counts := countsFromMap(total)
			if v, ok := number(info["model_context_window"]); ok {
				pc.contextWindow = v
			}
			e := &UsageEvent{HostID: hostID, SourceFileID: sourceID, ByteOffset: offset, EventType: "exact_usage", Timestamp: when, ConversationID: pc.conversationID, ParentConversationID: pc.parentID, TurnID: pc.turnID, ResponseID: pc.responseID, ProjectID: pc.projectID, RepoName: pc.repoName, Model: pc.model, ReasoningEffort: pc.effort, ModelContextWindow: pc.contextWindow, Counts: counts, DataQuality: "EXACT", ParserVersion: parserVersion}
			if !counts.CacheWriteVisible {
				e.DataQuality = "CACHE_WRITE_UNKNOWN"
			}
			e.EventID = stableID(hostID, sourceID, itoa(offset), e.EventType, when.Format(time.RFC3339Nano), e.ResponseID, e.TurnID)
			return e, true
		}
	}
	payloadType, _ := payload["type"].(string)
	runState := ""
	if recordType == "event_msg" {
		switch payloadType {
		case "task_started", "turn_started":
			runState = "running"
		case "task_complete", "turn_completed", "turn_aborted", "task_aborted":
			runState = "idle"
		}
	}
	if runState != "" {
		e := &UsageEvent{HostID: hostID, SourceFileID: sourceID, ByteOffset: offset, EventType: "activity", Timestamp: when, ConversationID: pc.conversationID, ParentConversationID: pc.parentID, TurnID: pc.turnID, ResponseID: pc.responseID, ProjectID: pc.projectID, RepoName: pc.repoName, Model: pc.model, ReasoningEffort: pc.effort, ModelContextWindow: pc.contextWindow, RunState: runState, DataQuality: "UNAVAILABLE", ParserVersion: parserVersion}
		e.EventID = stableID(hostID, sourceID, itoa(offset), e.EventType, when.Format(time.RFC3339Nano), runState)
		return e, true
	}
	// Only explicitly visible answer deltas, never arbitrary tool arguments or
	// reasoning deltas. Text is estimated locally and never retained/uploaded.
	visible := recordType == "response.output_text.delta" || payloadType == "response.output_text.delta" || payloadType == "agent_message_delta"
	if delta, ok := findStringKey(payload, "text_delta", "delta"); visible && ok && delta != "" {
		n := localEstimateTokens(delta)
		if n > 0 {
			e := &UsageEvent{HostID: hostID, SourceFileID: sourceID, ByteOffset: offset, EventType: "live_estimate", Timestamp: when, ConversationID: pc.conversationID, ParentConversationID: pc.parentID, TurnID: pc.turnID, ResponseID: pc.responseID, ProjectID: pc.projectID, RepoName: pc.repoName, Model: pc.model, ReasoningEffort: pc.effort, ModelContextWindow: pc.contextWindow, LiveEstimate: n, DataQuality: "ESTIMATED_LIVE", ParserVersion: parserVersion}
			e.EventID = stableID(hostID, sourceID, itoa(offset), e.EventType, when.Format(time.RFC3339Nano), e.ResponseID, e.TurnID)
			return e, true
		}
	}
	if directTurn, _ := payload["turn_id"].(string); directTurn != "" && directTurn != previousTurn {
		e := &UsageEvent{HostID: hostID, SourceFileID: sourceID, ByteOffset: offset, EventType: "activity", Timestamp: when, ConversationID: pc.conversationID, ParentConversationID: pc.parentID, TurnID: directTurn, ProjectID: pc.projectID, RepoName: pc.repoName, Model: pc.model, ReasoningEffort: pc.effort, ModelContextWindow: pc.contextWindow, DataQuality: "LOWER_BOUND", ParserVersion: parserVersion}
		e.EventID = stableID(hostID, sourceID, itoa(offset), e.EventType, when.Format(time.RFC3339Nano), directTurn)
		return e, true
	}
	return nil, false
}

func updateParseContext(recordType string, p map[string]any, pc *parseContext) {
	// Response items also have an "id" (msg_*, ctc_*, ctco_*, and similar),
	// but those identify an item rather than a Codex conversation. Treating an
	// arbitrary item ID as a session splits one task into hundreds of fake
	// sessions and prevents cumulative counters from reconciling.
	keys := []string{"session_id", "conversation_id", "thread_id"}
	if recordType == "session_meta" {
		keys = append(keys, "id")
	}
	for _, key := range keys {
		if s, _ := p[key].(string); s != "" && uuidInName.MatchString(s) {
			pc.conversationID = s
			break
		}
	}
	if s, ok := findStringKey(p, "parent_conversation_id", "parent_session_id", "parent_thread_id"); ok {
		pc.parentID = s
	}
	if s, ok := findStringKey(p, "response_id"); ok {
		pc.responseID = s
	}
	if s, ok := findStringKey(p, "turn_id"); ok {
		pc.turnID = s
	}
	if s, _ := p["model"].(string); s != "" {
		pc.model = s
	}
	if s, ok := findStringKey(p, "reasoning_effort"); ok {
		pc.effort = s
	}
	if n, ok := number(p["model_context_window"]); ok {
		pc.contextWindow = n
	}
	if cwd, _ := p["cwd"].(string); cwd != "" && pc.projectID == "" {
		pc.projectID, pc.repoName = projectFor(cwd)
	}
	if git, ok := p["git"].(map[string]any); ok {
		if remote, _ := git["repository_url"].(string); remote != "" {
			pc.projectID = stableID(strings.TrimSpace(remote))[:16]
			if name := projectNameFromRemote(remote); name != "" {
				pc.repoName = name
			}
		}
	}
	if ts, ok := p["thread_settings"].(map[string]any); ok {
		if s, _ := ts["model"].(string); s != "" {
			pc.model = s
		}
		if s, _ := ts["reasoning_effort"].(string); s != "" {
			pc.effort = s
		}
		if cwd, _ := ts["cwd"].(string); cwd != "" && pc.projectID == "" {
			pc.projectID, pc.repoName = projectFor(cwd)
		}
	}
}

func countsFromMap(m map[string]any) TokenCounts {
	c := TokenCounts{}
	c.InputTokens, _ = number(m["input_tokens"])
	c.CachedInputTokens, _ = number(m["cached_input_tokens"])
	c.CacheWriteInputTokens, c.CacheWriteVisible = number(m["cache_write_input_tokens"])
	c.OutputTokens, _ = number(m["output_tokens"])
	c.ReasoningOutputTokens, _ = number(m["reasoning_output_tokens"])
	c.TotalTokens, _ = number(m["total_tokens"])
	return c
}

func number(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		x, e := n.Int64()
		return x, e == nil
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}
func itoa(n int64) string   { return json.Number(strings.TrimSpace(string(mustJSON(n)))).String() }
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func findStringKey(v any, keys ...string) (string, bool) {
	wanted := map[string]bool{}
	for _, k := range keys {
		wanted[k] = true
	}
	var walk func(any) (string, bool)
	walk = func(x any) (string, bool) {
		switch z := x.(type) {
		case map[string]any:
			for k, c := range z {
				if wanted[k] {
					if s, ok := c.(string); ok {
						return s, true
					}
				}
				if k == "content" || k == "text" || k == "message" || k == "result" || k == "arguments" {
					continue
				}
				if s, ok := walk(c); ok {
					return s, true
				}
			}
		case []any:
			for _, c := range z {
				if s, ok := walk(c); ok {
					return s, true
				}
			}
		}
		return "", false
	}
	return walk(v)
}

func projectFor(cwd string) (string, string) {
	repo := filepath.Base(filepath.Clean(cwd))
	identity := repo
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(out))
		repo = filepath.Base(root)
		identity = repo
		if remote, err := exec.Command("git", "-C", root, "config", "--get", "remote.origin.url").Output(); err == nil && len(remote) < 2048 {
			identity = strings.TrimSpace(string(remote))
			if name := projectNameFromRemote(identity); name != "" {
				repo = name
			}
		}
	}
	if genericProjectNames[strings.ToLower(repo)] {
		repo = ""
	}
	return stableID(identity)[:16], repo
}

func localEstimateTokens(s string) int64 {
	var tokens int64
	inWord := false
	asciiBytes := 0
	flush := func() {
		if asciiBytes > 0 {
			tokens += int64((asciiBytes + 3) / 4)
			asciiBytes = 0
		}
	}
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			flush()
			tokens++
			inWord = false
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			asciiBytes += n
			inWord = true
		} else {
			flush()
			if !unicode.IsSpace(r) {
				tokens++
			}
			inWord = false
		}
	}
	_ = inWord
	flush()
	return tokens
}

func codexHomesDefault() []string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return []string{v}
	}
	h, _ := os.UserHomeDir()
	return []string{filepath.Join(h, ".codex")}
}
