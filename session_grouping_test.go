package main

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

const groupingRoot = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const groupingOther = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

func applyGroupingEvent(t *testing.T, s *server, e UsageEvent) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.applyEvent(tx, e); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyGroupingPreservesLedgerAndDistinctTasks(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	now := time.Now().UTC().Truncate(time.Second)
	s.cfg.ProjectAliases = map[string]string{"example-project": "Example Project"}
	e := UsageEvent{HostID: "h", EventType: "exact_usage", Model: "gpt-5.6-sol", DataQuality: "EXACT", ParserVersion: parserVersion, RepoName: "root"}
	e.EventID, e.ConversationID, e.SourceFileID = "root", groupingRoot, groupingRoot
	e.Timestamp, e.Counts = now.Add(-time.Minute), counts(100, 20, 0, 10, 2)
	applyGroupingEvent(t, s, e)
	e.EventID, e.ConversationID = "legacy", "msg_legacy"
	e.Timestamp, e.Counts = now, counts(130, 25, 0, 20, 3)
	applyGroupingEvent(t, s, e)
	e.EventID, e.ConversationID, e.SourceFileID = "other", groupingOther, groupingOther
	e.RepoName, e.Counts = "example-project", counts(9, 0, 0, 1, 0)
	applyGroupingEvent(t, s, e)
	// Recreate a pre-fix database. Production reads must not rewrite these rows.
	if _, err := s.db.Exec(`UPDATE sessions SET parent_conversation_id=NULL WHERE conversation_id='msg_legacy';
		UPDATE usage_events SET parent_conversation_id=NULL WHERE conversation_id='msg_legacy';
		INSERT INTO session_metadata(host_id,conversation_id,conversation_name,project_name,updated_at)
		VALUES('h',?,'Real task title','Example Project',?)`, groupingRoot, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	start, end := now.Add(-2*time.Minute), now.Add(time.Second)
	wantTokens := s.tokenTotalsBetween(start, end)
	wantCosts, _ := rangeCosts(s.db, start, end)
	if wantTokens.TotalTokens != 160 || wantCosts.API.Value <= 0 {
		t.Fatalf("invalid fixture: %+v %+v", wantTokens, wantCosts)
	}
	for i := 0; i < 2; i++ {
		v := s.snapshotBetween(start, end).(map[string]any)
		rows := v["sessions"].([]map[string]any)
		if len(rows) != 3 {
			t.Fatalf("source records disappeared: %+v", rows)
		}
		for _, row := range rows {
			if row["project"] != "Example Project" {
				t.Fatalf("project alias mismatch: %+v", row)
			}
			if row["conversation_id"] == "msg_legacy" && (row["parent_conversation_id"] != groupingRoot || row["name"] != "Real task title" || row["total_tokens"] != int64(40)) {
				t.Fatalf("message was not attached to its source task: %+v", row)
			}
		}
		projects := v["project_totals"].([]map[string]any)
		if len(projects) != 1 || projects[0]["sessions"] != 2 || projects[0]["records"] != int64(3) || projects[0]["total_tokens"] != int64(160) {
			t.Fatalf("two genuine tasks must remain in one project: %+v", projects)
		}
		for key, want := range map[string]float64{"api_cost": wantCosts.API.Value, "vercel_cost": wantCosts.Vercel.Value, "credits": wantCosts.Credits.Value} {
			if math.Abs(projects[0][key].(float64)-want) > 1e-12 {
				t.Fatalf("%s changed during grouping", key)
			}
		}
		if got := s.activeSessionCount(now); got != 2 {
			t.Fatalf("message inflated active task count: %d", got)
		}
	}
	// A historical range can include the message but not the owner's usage.
	v := s.snapshotBetween(now.Add(-time.Second), end).(map[string]any)
	rows := v["sessions"].([]map[string]any)
	if len(rows) != 2 || v["project_totals"].([]map[string]any)[0]["total_tokens"] != int64(50) {
		t.Fatalf("historical range changed: %+v", v)
	}
	for _, row := range rows {
		if row["conversation_id"] == "msg_legacy" && (row["parent_conversation_id"] != groupingRoot || row["project"] != "Example Project") {
			t.Fatal("owner must resolve outside the selected interval")
		}
	}
	var rawParent, sessionParent string
	if err := s.db.QueryRow(`SELECT COALESCE(u.parent_conversation_id,''),COALESCE(s.parent_conversation_id,'')
		FROM usage_events u JOIN sessions s USING(host_id,conversation_id) WHERE u.event_id='legacy'`).Scan(&rawParent, &sessionParent); err != nil {
		t.Fatal(err)
	}
	gotCosts, _ := rangeCosts(s.db, start, end)
	if rawParent != "" || sessionParent != "" || s.tokenTotalsBetween(start, end) != wantTokens || !reflect.DeepEqual(gotCosts, wantCosts) {
		t.Fatal("read-only grouping mutated historical records or prices")
	}
}

func TestLegacyGroupingRequiresUnambiguousSameHostNativeSource(t *testing.T) {
	for _, tc := range []struct {
		name, id, source, secondSource, explicit, ownerHost, want string
	}{
		{"message", "msg_old", groupingRoot, "", "", "h", groupingRoot},
		{"tool", "ctc_old", groupingRoot, "", "", "h", groupingRoot},
		{"ctco", "ctco_old", groupingRoot, "", "", "h", groupingRoot},
		{"fco", "fco_old", groupingRoot, "", "", "h", groupingRoot},
		{"explicit parent", "msg_old", groupingRoot, "", "explicit-task", "h", "explicit-task"},
		{"conflicting sources", "msg_old", groupingRoot, groupingOther, "", "h", ""},
		{"other host", "msg_old", groupingRoot, "", "", "other-host", ""},
		{"missing owner", "msg_old", groupingOther, "", "", "h", ""},
		{"native task stays separate", groupingOther, groupingRoot, "", "", "h", ""},
		{"lookalike prefix", "msgx_old", groupingRoot, "", "", "h", ""},
		{"non-native source", "msg_old", "ctco_source", "", "", "h", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, done := testServerDB(t)
			defer done()
			if _, err := s.db.Exec(`INSERT INTO agents(host_id,alias,token_hash,created_at) VALUES('other-host','other','y','');
				INSERT INTO sessions(host_id,conversation_id) VALUES(?,?);
				INSERT INTO sessions(host_id,conversation_id) VALUES('h','ctco_source');
				INSERT INTO sessions(host_id,conversation_id,parent_conversation_id) VALUES('h',?,?)`, tc.ownerHost, groupingRoot, tc.id, nullstr(tc.explicit)); err != nil {
				t.Fatal(err)
			}
			for i, source := range []string{tc.source, tc.secondSource} {
				if source == "" {
					continue
				}
				applyGroupingEvent(t, s, UsageEvent{EventID: fmt.Sprint(i), TurnID: fmt.Sprint(i), HostID: "h", ConversationID: tc.id, SourceFileID: source, EventType: "exact_usage", Timestamp: time.Now().UTC()})
			}
			// Restore historical ownership instead of exercising the ingest guard.
			if _, err := s.db.Exec("UPDATE sessions SET parent_conversation_id=? WHERE host_id='h' AND conversation_id=?", nullstr(tc.explicit), tc.id); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := s.db.QueryRow("WITH "+sessionParentsCTE+" SELECT display_parent_id FROM resolved_sessions WHERE host_id='h' AND conversation_id=?", tc.id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("parent=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyCheckpointEventsUseExistingOwner(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	if _, err := s.db.Exec("INSERT INTO sessions(host_id,conversation_id) VALUES('h',?)", groupingRoot); err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"activity", "live_estimate", "exact_usage"} {
		e := UsageEvent{EventID: eventType, HostID: "h", ConversationID: "msg_" + eventType, SourceFileID: groupingRoot, EventType: eventType, Timestamp: time.Now().UTC(), Counts: counts(10, 0, 0, 2, 0), LiveEstimate: 3}
		applyGroupingEvent(t, s, e)
		var parent string
		if err := s.db.QueryRow("SELECT COALESCE(parent_conversation_id,'') FROM sessions WHERE conversation_id=?", e.ConversationID).Scan(&parent); err != nil || parent != groupingRoot {
			t.Fatalf("%s checkpoint parent=%q: %v", eventType, parent, err)
		}
	}
	// An event without a parent must not overwrite a previously explicit one.
	if _, err := s.db.Exec("INSERT INTO sessions(host_id,conversation_id,parent_conversation_id) VALUES('h','msg_explicit','explicit-task')"); err != nil {
		t.Fatal(err)
	}
	applyGroupingEvent(t, s, UsageEvent{EventID: "keep-parent", HostID: "h", ConversationID: "msg_explicit", SourceFileID: groupingRoot, EventType: "activity", Timestamp: time.Now().UTC()})
	var parent string
	if err := s.db.QueryRow("SELECT parent_conversation_id FROM sessions WHERE conversation_id='msg_explicit'").Scan(&parent); err != nil || parent != "explicit-task" {
		t.Fatalf("explicit parent overwritten: %q %v", parent, err)
	}
	if got := s.displayProjectName("another-project", "Example Project"); got != "another-project" {
		t.Fatalf("unconfigured project must not be guessed/merged: %q", got)
	}
}
