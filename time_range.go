package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var dashboardLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

type dashboardRange struct {
	Period     string
	Start, End time.Time
	OpenEnded  bool
}

func resolveDashboardRange(q url.Values, now time.Time) (dashboardRange, error) {
	// User-entered filters remain second-precision. Live ranges end NOW, not at
	// the previous whole second: a readable fractional-second usage must not
	// wait for the next window timer merely because its timestamp was rounded.
	now = now.UTC()
	period := q.Get("period")
	if period == "" {
		period = "today"
	}
	r := dashboardRange{Period: period, End: now}
	local := now.In(dashboardLocation)
	if q.Get("from") != "" || period == "custom" {
		from, err := time.Parse(time.RFC3339, q.Get("from"))
		if err != nil {
			return r, fmt.Errorf("请选择有效的开始时间")
		}
		r.Period, r.Start = "custom", from.UTC().Truncate(time.Second)
		r.OpenEnded = q.Get("to") == ""
		if raw := q.Get("to"); raw != "" {
			end, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return r, fmt.Errorf("请选择有效的结束时间")
			}
			r.End = end.UTC().Truncate(time.Second)
		}
		if !r.End.After(r.Start) {
			return r, fmt.Errorf("结束时间必须晚于开始时间")
		}
		return r, nil
	}
	switch period {
	case "today":
		r.Start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, dashboardLocation)
	case "24h":
		r.Start = now.Add(-24 * time.Hour)
	case "week":
		days := (int(local.Weekday()) + 6) % 7
		r.Start = time.Date(local.Year(), local.Month(), local.Day()-days, 0, 0, 0, 0, dashboardLocation)
	case "month":
		r.Start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, dashboardLocation)
	case "all":
		r.Start = time.Unix(0, 0)
	default:
		return r, fmt.Errorf("不支持的时间范围")
	}
	if q.Get("to") != "" {
		return r, fmt.Errorf("自定义时间需要开始时间")
	}
	r.Start = r.Start.UTC()
	return r, nil
}

// Prefix bounds include both 00:00:00Z and 00:00:00.123Z at the start,
// and exclude both at the end. A trailing Z incorrectly sorts after '.'.
func rangeBound(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05") }

// Normalize for comparison only, without rewriting historical timestamps.
// RFC3339's trailing Z sorts after fractional seconds; pad to fixed nanoseconds.
const usageClock = `(substr(timestamp,1,19)||'.'||substr(CASE WHEN substr(timestamp,20,1)='.' THEN substr(timestamp,21,length(timestamp)-21) ELSE '' END||'000000000',1,9))`
const usageWindow = usageClock + ">=? AND " + usageClock + "<?"

func usageBound(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000") }

func (s *server) buildSnapshotForRange(r dashboardRange) any {
	return s.buildSnapshot(r, nil)
}
func (s *server) buildSnapshot(r dashboardRange, previous map[string]any) any {
	started := time.Now()
	view, closeView, err := s.snapshotReader()
	if err != nil {
		return map[string]any{"error": "database unavailable"}
	}
	defer closeView()
	if previous != nil {
		view.prepareIncremental(r, previous)
	}
	v := view.snapshotBetween(r.Start, r.End).(map[string]any)
	var firstEvent string
	_ = view.reader().QueryRow("SELECT timestamp FROM usage_events ORDER BY timestamp LIMIT 1").Scan(&firstEvent)
	v["data_start"] = firstEvent
	v["period"] = r.Period
	v["timezone"] = "Asia/Shanghai"
	v["timezone_label"] = "北京时间 UTC+8"
	v["range_end"] = r.End
	v["runtime"] = view.runtimeViews()
	s.hub.mu.Lock()
	v["runtime_revision"] = s.hub.taskSeq
	s.hub.mu.Unlock()
	view.attachWatermark(v, r.cacheKey(), started)
	return v
}

func (s *server) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	rng, err := resolveDashboardRange(r.URL.Query(), time.Now())
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	entry, err := s.cachedSnapshot(r.Context(), rng)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		http.Error(w, "数据暂不可用，请稍后重试", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Server-Timing", fmt.Sprintf("snapshot;dur=%.3f", float64(time.Since(started).Microseconds())/1000))
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(entry.Gzip)
		return
	}
	_, _ = w.Write(entry.Body)
}

func acceptsGzip(header string) bool {
	for _, encoding := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(encoding), ";")
		if parts[0] != "gzip" {
			continue
		}
		for _, p := range parts[1:] {
			if value, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
				quality, err := strconv.ParseFloat(value, 64)
				if err != nil || quality <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}
