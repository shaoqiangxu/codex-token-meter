package main

import (
	"sort"
	"strings"
	"time"
)

// Re-query absolute aggregates only for affected conversations. A new task,
// metadata/admin change, calendar rollover or large replay uses a full base.
// Moving edges are selected from SQLite, so expirations decrease totals too.
func (s *server) prepareIncremental(r dashboardRange, prior map[string]any) {
	mark := s.watermark()
	if mark["server_epoch"] != prior["server_epoch"] || mark["full_revision"] != prior["full_revision"] || r.cacheKey() != prior["query_key"] {
		return
	}
	oldID, ok := prior["ledger_revision"].(int64)
	if !ok {
		return
	}
	oldStart, ok := prior["range_start"].(time.Time)
	if !ok {
		return
	}
	oldEnd, ok := prior["range_end"].(time.Time)
	if !ok {
		return
	}
	if r.Start.Before(oldStart) || r.End.Before(oldEnd) || (r.Period != "24h" && !r.Start.Equal(oldStart)) {
		return
	}
	priorRows, ok := prior["sessions"].([]map[string]any)
	if !ok {
		return
	}
	known := map[string]bool{}
	for _, row := range priorRows {
		known[rowKey(row)] = true
	}
	rows, err := s.reader().Query(`SELECT DISTINCT host_id,conversation_id FROM usage_events WHERE id>? OR (`+usageClock+`>=? AND `+usageClock+`<?) OR (`+usageClock+`>=? AND `+usageClock+`<?) LIMIT 257`, oldID, usageBound(oldStart), usageBound(r.Start), usageBound(oldEnd), usageBound(r.End))
	if err != nil {
		return
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var host, id string
		if rows.Scan(&host, &id) != nil {
			return
		}
		key := host + "\x00" + id
		if !known[key] {
			return
		}
		keys[key] = true
	}
	if rows.Err() != nil || len(keys) > 256 {
		return
	}
	s.rowScope = keys
	s.priorRows = priorRows
}

func (s *server) scopeSQL(args []any) (string, []any) {
	if s.rowScope == nil {
		return "", args
	}
	if len(s.rowScope) == 0 {
		return " AND 0", args
	}
	keys := make([]string, 0, len(s.rowScope))
	for key := range s.rowScope {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		host, id, _ := strings.Cut(key, "\x00")
		parts = append(parts, "(host_id=? AND conversation_id=?)")
		args = append(args, host, id)
	}
	return " AND (" + strings.Join(parts, " OR ") + ")", args
}
func (s *server) mergeRows(changed []map[string]any) []map[string]any {
	if s.rowScope == nil {
		return changed
	}
	rows := make([]map[string]any, 0, len(s.priorRows)+len(changed))
	for _, old := range s.priorRows {
		if !s.rowScope[rowKey(old)] {
			item := map[string]any{}
			for k, v := range old {
				item[k] = v
			}
			rows = append(rows, item)
		}
	}
	rows = append(rows, changed...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["last_event_at"].(string) > rows[j]["last_event_at"].(string) })
	return rows
}

func projectTotalsFromRows(rows []map[string]any) []map[string]any {
	groups := map[string]*projectAggregate{}
	for _, row := range rows {
		host, project := row["host_id"].(string), row["project"].(string)
		key := host + "\x00" + project
		g := groups[key]
		if g == nil {
			g = &projectAggregate{HostID: host, Project: project, roots: map[string]struct{}{}}
			groups[key] = g
		}
		g.InputTokens += row["input_tokens"].(int64)
		g.OutputTokens += row["output_tokens"].(int64)
		g.TotalTokens += row["total_tokens"].(int64)
		g.RecordCount++
		root := row["parent_conversation_id"].(string)
		if root == "" {
			root = row["conversation_id"].(string)
		}
		g.roots[root] = struct{}{}
		g.APICost += row["api_cost"].(float64)
		g.VercelCost += row["vercel_cost"].(float64)
		g.Credits += row["credits"].(float64)
	}
	ordered := make([]*projectAggregate, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.HostID != b.HostID {
			return a.HostID < b.HostID
		}
		if a.TotalTokens == b.TotalTokens {
			return a.Project < b.Project
		}
		return a.TotalTokens > b.TotalTokens
	})
	result := make([]map[string]any, 0, len(ordered))
	for _, g := range ordered {
		result = append(result, map[string]any{"host_id": g.HostID, "project": g.Project, "sessions": len(g.roots), "records": g.RecordCount, "total_tokens": g.TotalTokens, "input_tokens": g.InputTokens, "output_tokens": g.OutputTokens, "api_cost": g.APICost, "vercel_cost": g.VercelCost, "credits": g.Credits})
	}
	return result
}
