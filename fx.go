package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const ecbDailyRatesURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

type exchangeRateView struct {
	Base      string  `json:"base"`
	Quote     string  `json:"quote"`
	Rate      float64 `json:"rate"`
	RateDate  string  `json:"rate_date"`
	Source    string  `json:"source"`
	FetchedAt string  `json:"fetched_at"`
	Stale     bool    `json:"stale"`
}

type ecbEnvelope struct {
	Cube struct {
		Day struct {
			Time  string `xml:"time,attr"`
			Rates []struct {
				Currency string  `xml:"currency,attr"`
				Rate     float64 `xml:"rate,attr"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	} `xml:"Cube"`
}

func (s *server) exchangeRateLoop(ctx context.Context) {
	s.refreshExchangeRate(ctx, ecbDailyRatesURL)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshExchangeRate(ctx, ecbDailyRatesURL)
		}
	}
}

func (s *server) refreshExchangeRate(ctx context.Context, endpoint string) {
	rateDate, rate, err := fetchUSDCNY(ctx, endpoint)
	if err != nil {
		cutoff := time.Now().UTC().Add(-96 * time.Hour).Format(time.RFC3339)
		s.db.Exec("UPDATE exchange_rates SET stale=1 WHERE base_currency='USD' AND quote_currency='CNY' AND fetched_at<?", cutoff)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.db.Exec(`INSERT INTO exchange_rates(base_currency,quote_currency,rate,rate_date,source_name,fetched_at,stale)
		VALUES('USD','CNY',?,?,?, ?,0)
		ON CONFLICT(base_currency,quote_currency) DO UPDATE SET rate=excluded.rate,rate_date=excluded.rate_date,source_name=excluded.source_name,fetched_at=excluded.fetched_at,stale=0`,
		rate, rateDate, "European Central Bank daily reference rates", now)
}

func fetchUSDCNY(ctx context.Context, endpoint string) (string, float64, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "Codex-Token-Meter/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("ECB returned %s", resp.Status)
	}
	var payload ecbEnvelope
	if err = xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", 0, err
	}
	var usd, cny float64
	for _, item := range payload.Cube.Day.Rates {
		switch item.Currency {
		case "USD":
			usd = item.Rate
		case "CNY":
			cny = item.Rate
		}
	}
	if payload.Cube.Day.Time == "" || usd <= 0 || cny <= 0 {
		return "", 0, errors.New("ECB response is missing USD or CNY")
	}
	return payload.Cube.Day.Time, cny / usd, nil
}

func (s *server) latestExchangeRate() exchangeRateView {
	var view exchangeRateView
	var stale int
	err := s.reader().QueryRow(`SELECT base_currency,quote_currency,rate,rate_date,source_name,fetched_at,stale
		FROM exchange_rates WHERE base_currency='USD' AND quote_currency='CNY'`).Scan(
		&view.Base, &view.Quote, &view.Rate, &view.RateDate, &view.Source, &view.FetchedAt, &stale)
	if err != nil {
		return exchangeRateView{Base: "USD", Quote: "CNY", Stale: true}
	}
	view.Stale = stale != 0
	return view
}
