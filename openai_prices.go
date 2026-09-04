package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const openAISolPricingURL = "https://developers.openai.com/api/docs/models/gpt-5.6-sol.md"

type openAIPriceSpec struct {
	Input, Cached, CacheWrite, Output float64
	Threshold                         int64
	InputMultiplier, OutputMultiplier float64
}

func (s *server) openAIPriceLoop(ctx context.Context) {
	s.refreshOpenAIPrice(ctx, openAISolPricingURL)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshOpenAIPrice(ctx, openAISolPricingURL)
		}
	}
}

func (s *server) refreshOpenAIPrice(ctx context.Context, endpoint string) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	req.Header.Set("User-Agent", "Codex-Token-Meter/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		s.markOpenAIPriceStale()
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		s.markOpenAIPriceStale()
		return
	}
	spec, err := parseOpenAIPriceMarkdown(string(body))
	if err != nil {
		s.markOpenAIPriceStale()
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	var old openAIPriceSpec
	err = s.db.QueryRow(`SELECT id,input_rate,cached_input_rate,COALESCE(cache_write_rate,0),output_rate,
		COALESCE(long_context_threshold,0),long_input_multiplier,long_output_multiplier
		FROM prices WHERE provider='openai' AND plan_profile='API' AND model='gpt-5.6-sol'
		AND effective_to IS NULL ORDER BY effective_from DESC LIMIT 1`).Scan(
		&id, &old.Input, &old.Cached, &old.CacheWrite, &old.Output, &old.Threshold, &old.InputMultiplier, &old.OutputMultiplier)
	if err == nil && old == spec {
		s.db.Exec("UPDATE prices SET verified_at=?,source_name=?,stale=0 WHERE id=?", now, "OpenAI official GPT-5.6 Sol model documentation", id)
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	_, _ = tx.Exec("UPDATE prices SET effective_to=? WHERE provider='openai' AND plan_profile='API' AND model='gpt-5.6-sol' AND effective_to IS NULL", now)
	_, err = tx.Exec(`INSERT INTO prices(provider,plan_profile,model,effective_from,input_rate,cached_input_rate,cache_write_rate,output_rate,long_context_threshold,long_input_multiplier,long_output_multiplier,currency,source_name,verified_at,stale)
		VALUES('openai','API','gpt-5.6-sol',?,?,?,?,?,?,?,?,'USD','OpenAI official GPT-5.6 Sol model documentation',?,0)`,
		now, spec.Input, spec.Cached, spec.CacheWrite, spec.Output, spec.Threshold, spec.InputMultiplier, spec.OutputMultiplier, now)
	if err == nil {
		_ = tx.Commit()
	}
}

func (s *server) markOpenAIPriceStale() {
	cutoff := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	s.db.Exec("UPDATE prices SET stale=1 WHERE provider='openai' AND effective_to IS NULL AND verified_at<?", cutoff)
}

func parseOpenAIPriceMarkdown(body string) (openAIPriceSpec, error) {
	price := func(label string) (float64, error) {
		re := regexp.MustCompile(`(?mi)^\|\s*` + regexp.QuoteMeta(label) + `\s*\|\s*\$([0-9]+(?:\.[0-9]+)?)\s*\|\s*1M tokens\s*\|`)
		match := re.FindStringSubmatch(body)
		if len(match) != 2 {
			return 0, fmt.Errorf("missing %s price", label)
		}
		return strconv.ParseFloat(match[1], 64)
	}
	input, err := price("Input")
	if err != nil {
		return openAIPriceSpec{}, err
	}
	cached, err := price("Cached input")
	if err != nil {
		return openAIPriceSpec{}, err
	}
	output, err := price("Output")
	if err != nil {
		return openAIPriceSpec{}, err
	}
	tier := regexp.MustCompile(`(?i)Prompts with\s*>\s*([0-9,.]+)K input tokens are priced at\s*([0-9.]+)x input and\s*([0-9.]+)x output`).FindStringSubmatch(body)
	write := regexp.MustCompile(`(?i)Cache writes are billed at\s*([0-9.]+)x`).FindStringSubmatch(body)
	if len(tier) != 4 || len(write) != 2 {
		return openAIPriceSpec{}, fmt.Errorf("missing long-context or cache-write pricing")
	}
	thresholdK, err1 := strconv.ParseFloat(strings.ReplaceAll(tier[1], ",", ""), 64)
	inputMultiplier, err2 := strconv.ParseFloat(tier[2], 64)
	outputMultiplier, err3 := strconv.ParseFloat(tier[3], 64)
	writeMultiplier, err4 := strconv.ParseFloat(write[1], 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || thresholdK <= 0 {
		return openAIPriceSpec{}, fmt.Errorf("invalid pricing multiplier")
	}
	return openAIPriceSpec{
		Input: input, Cached: cached, CacheWrite: input * writeMultiplier, Output: output,
		Threshold: int64(thresholdK * 1000), InputMultiplier: inputMultiplier, OutputMultiplier: outputMultiplier,
	}, nil
}
