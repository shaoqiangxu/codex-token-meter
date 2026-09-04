package main

import (
	"database/sql"
	"math"
	"time"
)

type CostBreakdown struct {
	Provider   string   `json:"provider"`
	Profile    string   `json:"profile"`
	Value      float64  `json:"value"`
	UpperBound *float64 `json:"upper_bound,omitempty"`
	Currency   string   `json:"currency"`
	Quality    string   `json:"quality"`
	VerifiedAt string   `json:"verified_at"`
}

type providerCosts struct{ API, Vercel, Credits CostBreakdown }
type costEvent struct {
	host, conversation string
	at                 time.Time
	counts             TokenCounts
}

func rangeCosts(db *sql.DB, since, until time.Time) (providerCosts, map[string]providerCosts) {
	rows, err := db.Query(`SELECT host_id,conversation_id,timestamp,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cache_write_visible FROM usage_events WHERE timestamp>=? AND timestamp<? ORDER BY timestamp`, since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano))
	if err != nil {
		return providerCosts{}, map[string]providerCosts{}
	}
	var events []costEvent
	for rows.Next() {
		var e costEvent
		var ts string
		var visible int
		rows.Scan(&e.host, &e.conversation, &ts, &e.counts.InputTokens, &e.counts.CachedInputTokens, &e.counts.CacheWriteInputTokens, &e.counts.OutputTokens, &e.counts.ReasoningOutputTokens, &e.counts.TotalTokens, &visible)
		e.at = parseTime(ts)
		e.counts.CacheWriteVisible = visible != 0
		events = append(events, e)
	}
	rows.Close()
	total := providerCosts{}
	bySession := map[string]providerCosts{}
	for _, e := range events {
		api, _ := costFor(db, "openai", "API", "gpt-5.6-sol", e.counts, e.at)
		vercel, _ := costFor(db, "vercel", "AI Gateway public", "openai/gpt-5.6-sol", e.counts, e.at)
		credits, _ := costFor(db, "codex", "Plus/Pro Current", "gpt-5.6-sol", e.counts, e.at)
		addCost(&total.API, api)
		addCost(&total.Vercel, vercel)
		addCost(&total.Credits, credits)
		key := e.host + "\x00" + e.conversation
		x := bySession[key]
		addCost(&x.API, api)
		addCost(&x.Vercel, vercel)
		addCost(&x.Credits, credits)
		bySession[key] = x
	}
	return total, bySession
}
func addCost(dst *CostBreakdown, src CostBreakdown) {
	if dst.Provider == "" {
		*dst = src
		return
	}
	old := dst.Value
	dst.Value += src.Value
	if dst.UpperBound != nil || src.UpperBound != nil {
		upper := old
		if dst.UpperBound != nil {
			upper = *dst.UpperBound
		}
		if src.UpperBound != nil {
			upper += *src.UpperBound
		} else {
			upper += src.Value
		}
		dst.UpperBound = &upper
	}
	if src.Quality != "EXACT" {
		dst.Quality = src.Quality
	}
	if src.VerifiedAt > dst.VerifiedAt {
		dst.VerifiedAt = src.VerifiedAt
	}
}

func costFor(db *sql.DB, provider, profile, model string, c TokenCounts, at time.Time) (CostBreakdown, error) {
	var r struct {
		input, cached, output, im, om float64
		write                         sql.NullFloat64
		threshold                     sql.NullInt64
		currency, verified            string
		stale                         int
	}
	err := db.QueryRow(`SELECT input_rate,cached_input_rate,cache_write_rate,output_rate,long_input_multiplier,long_output_multiplier,long_context_threshold,currency,verified_at,stale FROM prices WHERE provider=? AND plan_profile=? AND model=? AND effective_from<=? AND (effective_to IS NULL OR effective_to>?) ORDER BY effective_from DESC LIMIT 1`, provider, profile, model, at.Format(time.RFC3339), at.Format(time.RFC3339)).Scan(&r.input, &r.cached, &r.write, &r.output, &r.im, &r.om, &r.threshold, &r.currency, &r.verified, &r.stale)
	if err != nil {
		return CostBreakdown{}, err
	}
	fresh := c.InputTokens - c.CachedInputTokens - c.CacheWriteInputTokens
	if fresh < 0 {
		fresh = 0
	}
	im, om := 1.0, 1.0
	if r.threshold.Valid && c.InputTokens > r.threshold.Int64 {
		im, om = r.im, r.om
	}
	base := (float64(fresh)*r.input+float64(c.CachedInputTokens)*r.cached)*im + float64(c.OutputTokens)*r.output*om
	q := "EXACT"
	var upper *float64
	if r.write.Valid && c.CacheWriteVisible {
		base += float64(c.CacheWriteInputTokens) * r.write.Float64 * im
	} else if !r.write.Valid || !c.CacheWriteVisible {
		q = "CACHE_WRITE_UNKNOWN"
		u := base + float64(c.InputTokens)*math.Max(r.input, 5)*im
		upper = &u
	}
	base /= 1_000_000
	if upper != nil {
		u := *upper / 1_000_000
		upper = &u
	}
	if r.stale != 0 {
		q = "STALE"
	}
	return CostBreakdown{provider, profile, base, upper, r.currency, q, r.verified}, nil
}
