package main

import (
	"encoding/json"
	"net/url"
	"reflect"
	"sync"
	"time"
)

type numericView struct {
	query     url.Values
	value     map[string]any
	published map[string]any
	used      time.Time
}
type numericViews struct {
	sync.Mutex
	entries  map[string]*numericView
	sequence int64
}

func (v *numericViews) put(key string, query url.Values, value map[string]any) {
	if v.entries == nil {
		v.entries = map[string]*numericView{}
	}
	if _, ok := v.entries[key]; !ok && len(v.entries) >= 8 {
		oldest := ""
		var at time.Time
		for k, entry := range v.entries {
			if oldest == "" || entry.used.Before(at) {
				oldest, at = k, entry.used
			}
		}
		delete(v.entries, oldest)
	}
	v.entries[key] = &numericView{query: query, value: value, published: value, used: time.Now()}
}

func queryForRange(r dashboardRange) url.Values {
	q := url.Values{"period": {r.Period}}
	if r.Period == "custom" {
		q.Set("from", r.Start.Format(time.RFC3339))
		if !r.OpenEnded {
			q.Set("to", r.End.Format(time.RFC3339))
		}
	}
	return q
}

func (s *server) rememberNumericBase(r dashboardRange, value map[string]any) map[string]any {
	s.numeric.Lock()
	defer s.numeric.Unlock()
	return s.adoptNumericLocked(r, value)
}

// Each materialized query view has a monotonically increasing revision, even
// when a rolling/time edge changes without new ledger entries. Keep a separate
// published base: a concurrent GET must not silently consume another client's
// pending patch. The data_revision is the independent ingest watermark.
func (s *server) adoptNumericLocked(r dashboardRange, value map[string]any) map[string]any {
	key := r.cacheKey()
	previous := s.numeric.entries[key]
	if previous != nil {
		old := previous.value
		dataSeq, oldSeq := value["data_revision"].(int64), old["data_revision"].(int64)
		end, oldEnd := value["range_end"].(time.Time), old["range_end"].(time.Time)
		if dataSeq < oldSeq || (dataSeq == oldSeq && !end.After(oldEnd)) {
			return old
		}
		// A GET after a published frame must not create an intermediate version
		// just because wall time advanced. Otherwise its reader is forever one
		// version ahead of the shared published base and resyncs on every frame.
		// Compare authoritative accounting, not only ledger ID: rolling expiry,
		// prices, metadata and legitimate decreases still create a new revision.
		if dataSeq == oldSeq && sameAccounting(old, value) {
			return old
		}
	}
	s.numeric.sequence++
	value["revision"] = s.numeric.sequence
	if previous == nil {
		s.numeric.put(key, queryForRange(r), value)
	} else {
		previous.value = value
		previous.used = time.Now()
	}
	return value
}

func sameAccounting(a, b map[string]any) bool {
	for _, key := range []string{"range_start", "period", "sessions", "totals", "project_totals", "exchange_rate"} {
		if !reflect.DeepEqual(a[key], b[key]) {
			return false
		}
	}
	return true
}

func rowKey(row map[string]any) string {
	return row["host_id"].(string) + "\x00" + row["conversation_id"].(string)
}

// Changed rows carry absolute server values, not arithmetic deltas. A missing
// base (restart, gap, slow reader or eviction) always requires a full GET.
func numericDifference(before, after map[string]any) map[string]any {
	result := map[string]any{}
	for k, v := range after {
		if k != "sessions" {
			result[k] = v
		}
	}
	result["base_revision"] = before["revision"]
	result["base_range_end"] = before["range_end"]
	old := map[string]map[string]any{}
	for _, row := range before["sessions"].([]map[string]any) {
		old[rowKey(row)] = row
	}
	changed := []map[string]any{}
	for _, row := range after["sessions"].([]map[string]any) {
		key := rowKey(row)
		if !reflect.DeepEqual(old[key], row) {
			changed = append(changed, row)
		}
		delete(old, key)
	}
	removed := []string{}
	for key := range old {
		removed = append(removed, key)
	}
	result["sessions"] = changed
	result["removed"] = removed
	return result
}

func (s *server) numericMessage(query url.Values) *sseMessage {
	r, err := resolveDashboardRange(query, time.Now())
	if err != nil {
		return nil
	}
	s.numeric.Lock()
	defer s.numeric.Unlock()
	previous := s.numeric.entries[r.cacheKey()]
	if previous == nil {
		data, _ := json.Marshal(map[string]any{"query_key": r.cacheKey(), "server_epoch": s.hub.epoch, "reason": "missing_base"})
		return &sseMessage{Event: "resync", Data: data}
	}
	next := s.buildSnapshot(r, previous.value).(map[string]any)
	if next["error"] != nil {
		return &sseMessage{Event: "resync", Data: []byte(`{"reason":"database_unavailable"}`)}
	}
	next = s.adoptNumericLocked(r, next)
	if previous.published["revision"] == next["revision"] {
		return nil
	}
	message := numericDifference(previous.published, next)
	previous.published = next
	data, _ := json.Marshal(message)
	if len(data) > 256*1024 {
		data, _ = json.Marshal(map[string]any{"query_key": r.cacheKey(), "server_epoch": next["server_epoch"], "revision": next["revision"], "reason": "large_change"})
		return &sseMessage{Event: "resync", Data: data}
	}
	return &sseMessage{Event: "numbers", ID: next["revision"].(int64), Data: data}
}

// Time advances independently of usage. Check indexed range edges only, not
// full history: this catches the next second's usage, rolling expirations and
// Beijing midnight without making the application heartbeat aggregate data.
func (s *server) numericWindowDue() bool {
	s.numeric.Lock()
	defer s.numeric.Unlock()
	s.hub.mu.Lock()
	active := map[string]bool{}
	for _, q := range s.hub.subscriptions {
		r, err := resolveDashboardRange(q, time.Now())
		if err == nil {
			active[r.cacheKey()] = true
		}
	}
	s.hub.mu.Unlock()
	for key, view := range s.numeric.entries {
		r, err := resolveDashboardRange(view.query, time.Now())
		if err != nil || !active[r.cacheKey()] {
			continue
		}
		if key != r.cacheKey() {
			delete(s.numeric.entries, key)
			return true
		}
		oldStart := view.value["range_start"].(time.Time)
		oldEnd := view.value["range_end"].(time.Time)
		if r.Period != "24h" && !oldStart.Equal(r.Start) {
			return true
		}
		var changed bool
		if r.Start.After(oldStart) {
			_ = s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM usage_events WHERE "+usageWindow+")", usageBound(oldStart), usageBound(r.Start)).Scan(&changed)
			if changed {
				return true
			}
		}
		if r.End.After(oldEnd) {
			_ = s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM usage_events WHERE "+usageWindow+")", usageBound(oldEnd), usageBound(r.End)).Scan(&changed)
			if changed {
				return true
			}
		}
	}
	return false
}
