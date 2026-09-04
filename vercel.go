package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type vercelCatalog struct {
	Data []struct {
		ID      string `json:"id"`
		Pricing struct {
			Input           string `json:"input"`
			Output          string `json:"output"`
			InputCacheRead  string `json:"input_cache_read"`
			InputCacheWrite string `json:"input_cache_write"`
			InputTiers      []struct {
				Cost     string
				Min, Max int64
			} `json:"input_tiers"`
			OutputTiers []struct {
				Cost     string
				Min, Max int64
			} `json:"output_tiers"`
		} `json:"pricing"`
	} `json:"data"`
}

func (s *server) vercelPriceLoop(ctx context.Context) {
	s.refreshVercel(ctx)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshVercel(ctx)
		}
	}
}
func (s *server) refreshVercel(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, "GET", "https://ai-gateway.vercel.sh/v1/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.db.Exec("UPDATE prices SET stale=1 WHERE provider='vercel' AND effective_to IS NULL")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		s.db.Exec("UPDATE prices SET stale=1 WHERE provider='vercel' AND effective_to IS NULL")
		return
	}
	var cat vercelCatalog
	if json.NewDecoder(resp.Body).Decode(&cat) != nil {
		return
	}
	for _, m := range cat.Data {
		if m.ID != "openai/gpt-5.6-sol" {
			continue
		}
		input, e1 := rateMillion(m.Pricing.Input)
		cached, e2 := rateMillion(m.Pricing.InputCacheRead)
		write, e3 := rateMillion(m.Pricing.InputCacheWrite)
		output, e4 := rateMillion(m.Pricing.Output)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			return
		}
		threshold := int64(0)
		im, om := 1.0, 1.0
		if len(m.Pricing.InputTiers) > 1 {
			threshold = m.Pricing.InputTiers[0].Max
			long, _ := rateMillion(m.Pricing.InputTiers[1].Cost)
			if input > 0 {
				im = long / input
			}
		}
		if len(m.Pricing.OutputTiers) > 1 {
			long, _ := rateMillion(m.Pricing.OutputTiers[1].Cost)
			if output > 0 {
				om = long / output
			}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		var id int64
		var oi, oc, ow, oo, oim, oom float64
		var ot int64
		err = s.db.QueryRow("SELECT id,input_rate,cached_input_rate,COALESCE(cache_write_rate,0),output_rate,COALESCE(long_context_threshold,0),long_input_multiplier,long_output_multiplier FROM prices WHERE provider='vercel' AND plan_profile='AI Gateway public' AND model=? AND effective_to IS NULL ORDER BY effective_from DESC LIMIT 1", m.ID).Scan(&id, &oi, &oc, &ow, &oo, &ot, &oim, &oom)
		if err == nil && oi == input && oc == cached && ow == write && oo == output && ot == threshold && oim == im && oom == om {
			s.db.Exec("UPDATE prices SET verified_at=?,stale=0 WHERE id=?", now, id)
			return
		}
		tx, _ := s.db.Begin()
		defer tx.Rollback()
		tx.Exec("UPDATE prices SET effective_to=? WHERE provider='vercel' AND plan_profile='AI Gateway public' AND model=? AND effective_to IS NULL", now, m.ID)
		_, err = tx.Exec(`INSERT INTO prices(provider,plan_profile,model,effective_from,input_rate,cached_input_rate,cache_write_rate,output_rate,long_context_threshold,long_input_multiplier,long_output_multiplier,currency,source_name,verified_at,stale)VALUES('vercel','AI Gateway public',?,?,?,?,?,?,?,?,?,'USD','Vercel AI Gateway public model catalog',?,0)`, m.ID, now, input, cached, write, output, threshold, im, om, now)
		if err == nil {
			tx.Commit()
		}
		return
	}
}
func rateMillion(v string) (float64, error) {
	n, e := strconv.ParseFloat(v, 64)
	if e != nil {
		return 0, fmt.Errorf("rate: %w", e)
	}
	return n * 1_000_000, nil
}
