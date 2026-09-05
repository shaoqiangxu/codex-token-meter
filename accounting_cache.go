package main

import (
	"reflect"
	"sort"
	"sync"
	"time"
)

// Cache only immutable ledger IDs and their versioned prices. This is not a
// second ledger: restart discards it; SQLite remains the sole authority.
// New snapshots read only appended IDs, not all historical usage/prices again.
// A fixed bound falls back to the original query rather than dropping records.
const maxCachedCostEvents = 100000

type pricedEvent struct {
	id                 int64
	stamp, key, moment string
	cost               providerCosts
}
type accountingCache struct {
	sync.Mutex
	rules  []priceRule
	events []pricedEvent
	maxID  int64
}

func (s *server) cachedRangeCosts(since, until time.Time) (providerCosts, map[string]providerCosts) {
	if s.accounting == nil {
		return rangeCosts(s.reader(), since, until)
	}
	c := s.accounting
	c.Lock()
	defer c.Unlock()
	rules, err := loadPriceRules(s.reader())
	if err != nil {
		return rangeCosts(s.reader(), since, until)
	}
	var maxID, count int64
	if err = s.reader().QueryRow("SELECT COALESCE(MAX(id),0),COUNT(*) FROM usage_events").Scan(&maxID, &count); err != nil || count > maxCachedCostEvents {
		return rangeCosts(s.reader(), since, until)
	}
	// An older concurrent read transaction must not see a newer cache's rows.
	if maxID < c.maxID {
		return rangeCosts(s.reader(), since, until)
	}
	if !reflect.DeepEqual(rules, c.rules) || count < int64(len(c.events)) {
		c.rules = rules
		c.events = nil
		c.maxID = 0
	}
	if maxID > c.maxID {
		rows, err := s.reader().Query(`SELECT id,host_id,conversation_id,timestamp,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cache_write_visible FROM usage_events WHERE id>? ORDER BY id`, c.maxID)
		if err != nil {
			return rangeCosts(s.reader(), since, until)
		}
		var fresh []pricedEvent
		for rows.Next() {
			var id int64
			var host, conversation, ts string
			var counts TokenCounts
			var visible int
			if err = rows.Scan(&id, &host, &conversation, &ts, &counts.InputTokens, &counts.CachedInputTokens, &counts.CacheWriteInputTokens, &counts.OutputTokens, &counts.ReasoningOutputTokens, &counts.TotalTokens, &visible); err != nil {
				break
			}
			counts.CacheWriteVisible = visible != 0
			at := parseTime(ts)
			api, _ := costFromRules(rules, "openai", "API", "gpt-5.6-sol", counts, at)
			vercel, _ := costFromRules(rules, "vercel", "AI Gateway public", "openai/gpt-5.6-sol", counts, at)
			credits, _ := costFromRules(rules, "codex", "Plus/Pro Current", "gpt-5.6-sol", counts, at)
			fresh = append(fresh, pricedEvent{id, ts, host + "\x00" + conversation, usageBound(at), providerCosts{api, vercel, credits}})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return rangeCosts(s.reader(), since, until)
		}
		c.events = append(c.events, fresh...)
		c.maxID = maxID
		sort.Slice(c.events, func(i, j int) bool {
			a, b := c.events[i], c.events[j]
			if a.stamp == b.stamp {
				return a.id < b.id
			}
			return a.stamp < b.stamp
		})
	}
	// Same chronological accumulation as the reference algorithm, including
	// per-event long-context thresholds, unknown prices and upper bounds. Never
	// price summed tokens with one rate. Range expiration/corrections may decrease.
	start, end := usageBound(since), usageBound(until)
	total := providerCosts{}
	sessions := map[string]providerCosts{}
	for _, e := range c.events {
		if e.moment < start || e.moment >= end {
			continue
		}
		addProviderCosts(&total, e.cost)
		item := sessions[e.key]
		addProviderCosts(&item, e.cost)
		sessions[e.key] = item
	}
	return total, sessions
}
func addProviderCosts(a *providerCosts, b providerCosts) {
	addCost(&a.API, b.API)
	addCost(&a.Vercel, b.Vercel)
	addCost(&a.Credits, b.Credits)
}
