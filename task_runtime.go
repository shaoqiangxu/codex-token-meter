package main

import (
	"database/sql"
	"time"
)

// Runtime evidence is not accounting quality. Exact usage never proves idle.
func applyRuntime(tx *sql.Tx, e UsageEvent) error {
	// Keep each task's evidence independent: a child finishing must not declare
	// its still-running parent idle or clear another response's visible estimate.
	root := e.ConversationID
	var state, turn, at, exact string
	var estimate int64
	err := tx.QueryRow("SELECT state,turn_id,evidence_at,live_estimate,last_exact_at FROM task_runtime WHERE host_id=? AND conversation_id=?", e.HostID, root).Scan(&state, &turn, &at, &estimate, &exact)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if state == "" {
		state = "unknown"
	}
	// Do not let a delayed old response clear a newer turn's estimate/state.
	if e.Timestamp.Before(parseTime(at)) {
		return nil
	}
	newTurn := e.TurnID != "" && e.TurnID != turn
	if newTurn {
		estimate = 0
		turn = e.TurnID
	}
	switch e.EventType {
	case "runtime":
		state = e.RunState
		if state == "idle" {
			estimate = 0
		}
	case "activity":
		state = "running"
		if e.RunState == "idle" {
			state = "idle"
			estimate = 0
		}
	case "live_estimate":
		if !newTurn && !parseTime(exact).IsZero() && !e.Timestamp.After(parseTime(exact)) {
			return nil
		}
		state = "running"
		estimate += e.LiveEstimate
	case "exact_usage":
		estimate = 0
		exact = e.Timestamp.Format(time.RFC3339Nano)
		if newTurn {
			state = "unknown"
		}
	default:
		return nil
	}
	_, err = tx.Exec(`INSERT INTO task_runtime(host_id,conversation_id,state,turn_id,evidence_at,received_at,live_estimate,last_exact_at)VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(host_id,conversation_id) DO UPDATE SET state=excluded.state,turn_id=excluded.turn_id,evidence_at=excluded.evidence_at,received_at=excluded.received_at,live_estimate=excluded.live_estimate,last_exact_at=excluded.last_exact_at`, e.HostID, root, state, turn, e.Timestamp.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), estimate, exact)
	return err
}

func (s *server) runtimeViews() []map[string]any {
	rows, err := s.db.Query(`SELECT r.host_id,r.conversation_id,r.state,r.turn_id,r.evidence_at,r.received_at,r.live_estimate,r.last_exact_at,COALESCE(s.parent_conversation_id,'')
		FROM task_runtime r JOIN agents a ON a.host_id=r.host_id LEFT JOIN sessions s ON s.host_id=r.host_id AND s.conversation_id=r.conversation_id
		WHERE a.revoked_at IS NULL AND r.received_at>=? AND r.state IN ('running','idle')`, time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var host, id, state, turn, evidence, received, exact, parent string
		var live int64
		if rows.Scan(&host, &id, &state, &turn, &evidence, &received, &live, &exact, &parent) != nil {
			continue
		}
		age := time.Since(parseTime(received)).Milliseconds()
		if state == "running" && age > 300000 {
			state = "unknown"
		}
		result = append(result, map[string]any{"host_id": host, "conversation_id": id, "parent_conversation_id": parent, "runtime_state": state, "turn_id": turn, "evidence_at": evidence, "evidence_age_ms": age, "live_estimate": live, "last_exact_at": exact})
	}
	return result
}
