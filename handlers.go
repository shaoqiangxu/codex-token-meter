package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func (s *server) loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		token, _ := randomToken(24)
		http.SetCookie(w, &http.Cookie{Name: "meter_csrf", Value: token, Path: "/", Secure: true, HttpOnly: false, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
		b, _ := webFS.ReadFile("web/login.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	ip := clientIP(r)
	if !s.loginAllowed(ip) {
		http.Error(w, "尝试过多，请稍后再试", 429)
		return
	}
	c, err := r.Cookie("meter_csrf")
	if err != nil || !hmac.Equal([]byte(c.Value), []byte(r.FormValue("csrf"))) {
		http.Error(w, "CSRF", 403)
		return
	}
	if r.FormValue("username") != s.cfg.AdminUser || !verifyPassword(s.cfg.AdminPasswordHash, r.FormValue("password")) {
		s.recordLoginFailure(ip)
		http.Error(w, "用户名或密码错误", 401)
		return
	}
	expiry := time.Now().Add(12 * time.Hour).Unix()
	payload := fmt.Sprintf("%s|%d", s.cfg.AdminUser, expiry)
	sig := sign(payload, s.cfg.SessionSecret)
	http.SetCookie(w, &http.Cookie{Name: "meter_session", Value: base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	http.Redirect(w, r, "/", 303)
}
func (s *server) logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "meter_session", Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/login", 303)
}
func sign(v, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *server) authenticated(r *http.Request) bool {
	c, e := r.Cookie("meter_session")
	if e != nil {
		return false
	}
	p := strings.Split(c.Value, ".")
	if len(p) != 2 {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil || !hmac.Equal([]byte(p[1]), []byte(sign(string(b), s.cfg.SessionSecret))) {
		return false
	}
	v := strings.Split(string(b), "|")
	if len(v) != 2 || v[0] != s.cfg.AdminUser {
		return false
	}
	exp, _ := strconv.ParseInt(v[1], 10, 64)
	return exp > time.Now().Unix()
}
func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/events" {
				http.Error(w, "unauthorized", 401)
			} else {
				http.Redirect(w, r, "/login", 303)
			}
			return
		}
		if _, e := r.Cookie("meter_csrf"); e != nil {
			t, _ := randomToken(24)
			http.SetCookie(w, &http.Cookie{Name: "meter_csrf", Value: t, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
		}
		next(w, r)
	}
}
func (s *server) csrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("meter_csrf")
		if e != nil || !hmac.Equal([]byte(c.Value), []byte(r.Header.Get("X-CSRF-Token"))) {
			http.Error(w, "CSRF", 403)
			return
		}
		next(w, r)
	}
}
func clientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	h, _, _ := strings.Cut(r.RemoteAddr, ":")
	return h
}
func (s *server) loginAllowed(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	var keep []time.Time
	for _, t := range s.login[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	s.login[ip] = keep
	return len(keep) < 5
}
func (s *server) recordLoginFailure(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.login[ip] = append(s.login[ip], time.Now())
}

type sessionView struct {
	HostID                                                    string `json:"host_id"`
	Host                                                      string `json:"host"`
	ConversationID                                            string `json:"conversation_id"`
	ParentID                                                  string `json:"parent_conversation_id"`
	Project                                                   string `json:"project"`
	Name                                                      string `json:"name"`
	Model                                                     string `json:"model"`
	Effort                                                    string `json:"reasoning_effort"`
	Status                                                    string `json:"status"`
	StartedAt                                                 string `json:"started_at"`
	LastEvent                                                 string `json:"last_event_at"`
	Quality                                                   string `json:"data_quality"`
	ContextWindow                                             int64  `json:"model_context_window"`
	Input, Cached, CacheWrite, Output, Reasoning, Total, Live int64
	CacheHit, ContextPct                                      float64 `json:"-"`
}

func (s *server) snapshot() any {
	now := time.Now().UTC()
	return s.snapshotSince(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
}
func (s *server) snapshotPeriod(period string) any {
	now := time.Now().UTC()
	var since time.Time
	switch period {
	case "24h":
		since = now.Add(-24 * time.Hour)
	case "week":
		d := int(now.Weekday())
		if d == 0 {
			d = 7
		}
		since = time.Date(now.Year(), now.Month(), now.Day()-d+1, 0, 0, 0, 0, time.UTC)
	case "month":
		since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "all":
		since = time.Unix(0, 0).UTC()
	default:
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	}
	return s.snapshotSince(since)
}
func (s *server) snapshotForRequest(r *http.Request) any {
	if raw := r.URL.Query().Get("from"); raw != "" {
		if from, err := time.Parse(time.RFC3339, raw); err == nil {
			until := time.Now().UTC().AddDate(1, 0, 0)
			if end := r.URL.Query().Get("to"); end != "" {
				if parsed, e := time.Parse(time.RFC3339, end); e == nil {
					until = parsed
				}
			}
			return s.snapshotBetween(from, until)
		}
	}
	return s.snapshotPeriod(r.URL.Query().Get("period"))
}
func (s *server) snapshotSince(since time.Time) any {
	return s.snapshotBetween(since, time.Now().UTC().AddDate(100, 0, 0))
}
func (s *server) snapshotBetween(since, until time.Time) any {
	rows, err := s.db.Query(`WITH u AS (SELECT host_id,conversation_id,SUM(input_tokens) input_tokens,SUM(cached_input_tokens) cached_input_tokens,SUM(cache_write_input_tokens) cache_write_input_tokens,SUM(output_tokens) output_tokens,SUM(reasoning_output_tokens) reasoning_output_tokens,SUM(total_tokens) total_tokens FROM usage_events WHERE timestamp>=? AND timestamp<? GROUP BY host_id,conversation_id) SELECT s.host_id,a.alias,s.conversation_id,COALESCE(s.parent_conversation_id,''),COALESCE(s.repo_name,s.project_id,''),COALESCE(s.display_name,''),COALESCE(s.model,''),COALESCE(s.reasoning_effort,''),s.status,COALESCE(s.started_at,''),COALESCE(s.last_event_at,''),s.data_quality,s.model_context_window,COALESCE(u.input_tokens,0),COALESCE(u.cached_input_tokens,0),COALESCE(u.cache_write_input_tokens,0),COALESCE(u.output_tokens,0),COALESCE(u.reasoning_output_tokens,0),COALESCE(u.total_tokens,0),s.live_estimate FROM sessions s JOIN agents a ON a.host_id=s.host_id LEFT JOIN u ON u.host_id=s.host_id AND u.conversation_id=s.conversation_id WHERE u.total_tokens IS NOT NULL OR s.live_estimate>0 ORDER BY s.last_event_at DESC LIMIT 500`, since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano))
	if err != nil {
		return map[string]any{"error": "database unavailable"}
	}
	defer rows.Close()
	var list []map[string]any
	tot := TokenCounts{CacheWriteVisible: true}
	active := 0
	for rows.Next() {
		var v sessionView
		if rows.Scan(&v.HostID, &v.Host, &v.ConversationID, &v.ParentID, &v.Project, &v.Name, &v.Model, &v.Effort, &v.Status, &v.StartedAt, &v.LastEvent, &v.Quality, &v.ContextWindow, &v.Input, &v.Cached, &v.CacheWrite, &v.Output, &v.Reasoning, &v.Total, &v.Live) != nil {
			continue
		}
		last := parseTime(v.LastEvent)
		if time.Since(last) > 30*time.Second && (v.Status == "ESTIMATED_LIVE" || v.Status == "LOWER_BOUND") {
			v.Status = "STALE"
		}
		if time.Since(last) < 5*time.Minute {
			active++
		}
		tot.InputTokens += v.Input
		tot.CachedInputTokens += v.Cached
		tot.CacheWriteInputTokens += v.CacheWrite
		tot.OutputTokens += v.Output
		tot.ReasoningOutputTokens += v.Reasoning
		tot.TotalTokens += v.Total
		if v.Quality == "CACHE_WRITE_UNKNOWN" {
			tot.CacheWriteVisible = false
		}
		hit := float64(0)
		if v.Input > 0 {
			hit = float64(v.Cached) / float64(v.Input) * 100
		}
		ctx := float64(0)
		if v.ContextWindow > 0 {
			ctx = float64(v.Input) / float64(v.ContextWindow) * 100
		}
		name := v.Name
		if name == "" {
			name = v.Project
		}
		list = append(list, map[string]any{"host_id": v.HostID, "host": v.Host, "conversation_id": v.ConversationID, "parent_conversation_id": v.ParentID, "project": v.Project, "name": name, "model": v.Model, "reasoning_effort": v.Effort, "status": v.Status, "started_at": v.StartedAt, "last_event_at": v.LastEvent, "data_quality": v.Quality, "model_context_window": v.ContextWindow, "input_tokens": v.Input, "cached_input_tokens": v.Cached, "cache_write_input_tokens": v.CacheWrite, "output_tokens": v.Output, "reasoning_output_tokens": v.Reasoning, "total_tokens": v.Total, "live_estimate": v.Live, "cache_hit_rate": hit, "context_percent": ctx})
	}
	rows.Close()
	rangeTotal, rangeBySession := rangeCosts(s.db, since, until)
	for _, item := range list {
		c := rangeBySession[item["host_id"].(string)+"\x00"+item["conversation_id"].(string)]
		item["api_cost"] = c.API.Value
		item["vercel_cost"] = c.Vercel.Value
		item["credits"] = c.Credits.Value
	}
	var online int
	s.db.QueryRow("SELECT COUNT(*) FROM agents WHERE revoked_at IS NULL AND last_seen>=?", time.Now().Add(-15*time.Second).UTC().Format(time.RFC3339Nano)).Scan(&online)
	api, vercel, credits := rangeTotal.API, rangeTotal.Vercel, rangeTotal.Credits
	var paid, purchased, weightedFX float64
	s.db.QueryRow("SELECT COALESCE(SUM(paid_amount+fees),0),COALESCE(SUM(credits_received),0),COALESCE(SUM((paid_amount+fees)*exchange_rate),0) FROM credit_purchases").Scan(&paid, &purchased, &weightedFX)
	creditUSD, rmb := 0.0, 0.0
	if purchased > 0 {
		creditUSD = credits.Value * paid / purchased
		if paid > 0 {
			rmb = creditUSD * weightedFX / paid
		}
	}
	hit := float64(0)
	if tot.InputTokens > 0 {
		hit = float64(tot.CachedInputTokens) / float64(tot.InputTokens) * 100
	}
	return map[string]any{"generated_at": time.Now().UTC(), "range_start": since, "totals": map[string]any{"input_tokens": tot.InputTokens, "cached_input_tokens": tot.CachedInputTokens, "cache_write_input_tokens": tot.CacheWriteInputTokens, "output_tokens": tot.OutputTokens, "reasoning_output_tokens": tot.ReasoningOutputTokens, "total_tokens": tot.TotalTokens, "live_estimate": sumLive(list), "cache_hit_rate": hit, "active_sessions": active, "online_hosts": online, "api": api, "vercel": vercel, "credits": credits, "credits_purchase_usd": creditUSD, "rmb_equivalent": rmb, "actual_incremental_cash": 0}, "sessions": list}
}
func sumLive(list []map[string]any) int64 {
	var n int64
	for _, v := range list {
		if x, ok := v["live_estimate"].(int64); ok {
			n += x
		}
	}
	return n
}

func (s *server) static(w http.ResponseWriter, r *http.Request) {
	name := "web/index.html"
	if r.URL.Path != "/" {
		name = "web/" + strings.TrimPrefix(r.URL.Path, "/")
	}
	b, e := webFS.ReadFile(name)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		w.Header().Set("Content-Type", t)
	}
	w.Write(b)
}
func (s *server) sessionAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(id, "/", 2)
	if len(parts) == 0 {
		return
	}
	conv := parts[0]
	if r.Method == "PATCH" {
		s.csrf(func(w http.ResponseWriter, r *http.Request) {
			var v struct {
				Name string `json:"name"`
			}
			json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&v)
			if len(v.Name) > 100 {
				http.Error(w, "name too long", 400)
				return
			}
			_, _ = s.db.Exec("UPDATE sessions SET display_name=? WHERE conversation_id=?", v.Name, conv)
			s.hub.mark()
			writeJSON(w, map[string]bool{"ok": true})
		})(w, r)
		return
	}
	rows, e := s.db.Query(`SELECT timestamp,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,data_quality,parser_version,turn_id,response_id FROM usage_events WHERE conversation_id=? ORDER BY timestamp`, conv)
	if e != nil {
		http.Error(w, "db", 500)
		return
	}
	defer rows.Close()
	var events []map[string]any
	for rows.Next() {
		var ts, q, p string
		var turn, resp any
		var a, b, c, d, f, g int64
		rows.Scan(&ts, &a, &b, &c, &d, &f, &g, &q, &p, &turn, &resp)
		events = append(events, map[string]any{"timestamp": ts, "input_tokens": a, "cached_input_tokens": b, "cache_write_input_tokens": c, "output_tokens": d, "reasoning_output_tokens": f, "total_tokens": g, "data_quality": q, "parser_version": p, "turn_id": turn, "response_id": resp})
	}
	writeJSON(w, map[string]any{"conversation_id": conv, "events": events})
}

func (s *server) export(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	rows, e := s.db.Query(`SELECT timestamp,host_id,conversation_id,project_id,model,reasoning_effort,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,data_quality FROM usage_events ORDER BY timestamp`)
	if e != nil {
		http.Error(w, "db", 500)
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=codex-token-meter.csv")
		cw := csv.NewWriter(w)
		cw.Write(cols)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			rows.Scan(ptr...)
			out := make([]string, len(vals))
			for i, v := range vals {
				out[i] = fmt.Sprint(v)
			}
			cw.Write(out)
		}
		cw.Flush()
		return
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		rows.Scan(ptr...)
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	writeJSON(w, out)
}

func (s *server) purchaseAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		rows, err := s.db.Query("SELECT id,purchase_time,paid_amount,currency,credits_received,fees,exchange_rate FROM credit_purchases ORDER BY purchase_time DESC")
		if err != nil {
			http.Error(w, "db", 500)
			return
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var id int64
			var ts, currency string
			var paid, credits, fees, fx float64
			rows.Scan(&id, &ts, &paid, &currency, &credits, &fees, &fx)
			out = append(out, map[string]any{"id": id, "purchase_time": ts, "paid_amount": paid, "currency": currency, "credits_received": credits, "fees": fees, "exchange_rate": fx})
		}
		writeJSON(w, out)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	s.csrf(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			PurchaseTime    string  `json:"purchase_time"`
			PaidAmount      float64 `json:"paid_amount"`
			Currency        string  `json:"currency"`
			CreditsReceived float64 `json:"credits_received"`
			Fees            float64 `json:"fees"`
			ExchangeRate    float64 `json:"exchange_rate"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&v) != nil || v.PaidAmount <= 0 || v.CreditsReceived <= 0 || v.ExchangeRate <= 0 {
			http.Error(w, "invalid purchase", 400)
			return
		}
		if v.PurchaseTime == "" {
			v.PurchaseTime = time.Now().UTC().Format(time.RFC3339)
		}
		if v.Currency == "" {
			v.Currency = "USD"
		}
		_, err := s.db.Exec("INSERT INTO credit_purchases(purchase_time,paid_amount,currency,credits_received,fees,exchange_rate)VALUES(?,?,?,?,?,?)", v.PurchaseTime, v.PaidAmount, v.Currency, v.CreditsReceived, v.Fees, v.ExchangeRate)
		if err != nil {
			http.Error(w, "db", 500)
			return
		}
		s.db.Exec("INSERT INTO audit_log(timestamp,action,actor,details)VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339), "credit_purchase_added", "admin", "non-secret purchase batch")
		s.hub.mark()
		writeJSON(w, map[string]bool{"ok": true})
	})(w, r)
}

func (s *server) createEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	var in struct {
		Platform string `json:"platform"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if in.Platform != "windows" && in.Platform != "linux" {
		http.Error(w, "platform", 400)
		return
	}
	token, _ := randomToken(24)
	exp := time.Now().Add(15 * time.Minute).UTC()
	_, e := s.db.Exec("INSERT INTO enrollments(token_hash,platform,expires_at,created_at)VALUES(?,?,?,?)", tokenHash(token), in.Platform, exp.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		http.Error(w, "db", 500)
		return
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	cmd := "curl -fsSL '" + base + "/install/linux.sh?token=" + token + "' | sudo bash"
	if in.Platform == "windows" {
		cmd = "powershell -NoProfile -ExecutionPolicy Bypass -Command \"irm '" + base + "/install/windows.ps1?token=" + token + "' | iex\""
	}
	writeJSON(w, map[string]any{"command": cmd, "expires_at": exp})
}
func (s *server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var in struct{ Token, Platform, Alias string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "bad", 400)
		return
	}
	var platform, exp string
	var used any
	if s.db.QueryRow("SELECT platform,expires_at,used_at FROM enrollments WHERE token_hash=?", tokenHash(in.Token)).Scan(&platform, &exp, &used) != nil || used != nil || platform != in.Platform || parseTime(exp).Before(time.Now()) {
		http.Error(w, "invalid enrollment", 401)
		return
	}
	host, _ := randomToken(12)
	agentToken, _ := randomToken(32)
	if in.Alias == "" {
		in.Alias = platform + "-" + host[:6]
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	_, e := tx.Exec("INSERT INTO agents(host_id,alias,token_hash,platform,created_at)VALUES(?,?,?,?,?)", host, in.Alias, tokenHash(agentToken), platform, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		http.Error(w, "db", 500)
		return
	}
	tx.Exec("UPDATE enrollments SET used_at=? WHERE token_hash=?", time.Now().UTC().Format(time.RFC3339Nano), tokenHash(in.Token))
	tx.Commit()
	writeJSON(w, map[string]string{"host_id": host, "agent_token": agentToken})
}

func (s *server) windowsInstaller(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.validEnrollment(token, "windows") {
		http.Error(w, "invalid enrollment", 401)
		return
	}
	name := "codex-meter-windows-amd64.exe"
	sum, e := fileSHA(filepath.Join(s.cfg.ArtifactDir, name))
	if e != nil {
		http.Error(w, "artifact unavailable", 503)
		return
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, `$ErrorActionPreference='Stop'
$d=Join-Path $env:LOCALAPPDATA 'CodexTokenMeter'; New-Item -ItemType Directory -Force $d | Out-Null
$exe=Join-Path $d 'codex-meter.exe'; Invoke-WebRequest '%s/downloads/%s' -OutFile $exe
if((Get-FileHash $exe -Algorithm SHA256).Hash.ToLower() -ne '%s'){throw 'SHA-256 mismatch'}
& $exe enroll --server '%s' --token '%s' --platform windows --config (Join-Path $d 'agent.json')
$cfg=Join-Path $d 'agent.json'; icacls $cfg /inheritance:r /grant:r "$($env:USERNAME):(R,W)" | Out-Null
$launcher=Join-Path $d 'agent.vbs'
$vbs='CreateObject("Wscript.Shell").Run """'+$exe+'"" agent --config ""'+$cfg+'""", 0, False'
Set-Content -LiteralPath $launcher -Value $vbs -Encoding ASCII
$action=New-ScheduledTaskAction -Execute (Join-Path $env:SystemRoot 'System32\wscript.exe') -Argument ('"'+$launcher+'"')
$trigger=New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
Register-ScheduledTask -TaskName 'CodexTokenMeter' -Action $action -Trigger $trigger -Force | Out-Null
Start-ScheduledTask -TaskName 'CodexTokenMeter'; Write-Host 'Codex Token Meter installed.'
`, base, name, sum, base, token)
}

func (s *server) windowsHideRepair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, `$ErrorActionPreference='Stop'
$d=Join-Path $env:LOCALAPPDATA 'CodexTokenMeter'
$exe=Join-Path $d 'codex-meter.exe'; $cfg=Join-Path $d 'agent.json'
if(!(Test-Path -LiteralPath $exe) -or !(Test-Path -LiteralPath $cfg)){throw 'Codex Token Meter is not installed for this user'}
$launcher=Join-Path $d 'agent.vbs'
$vbs='CreateObject("Wscript.Shell").Run """'+$exe+'"" agent --config ""'+$cfg+'""", 0, False'
Set-Content -LiteralPath $launcher -Value $vbs -Encoding ASCII
Stop-ScheduledTask -TaskName 'CodexTokenMeter' -ErrorAction SilentlyContinue
$action=New-ScheduledTaskAction -Execute (Join-Path $env:SystemRoot 'System32\wscript.exe') -Argument ('"'+$launcher+'"')
Set-ScheduledTask -TaskName 'CodexTokenMeter' -Action $action | Out-Null
Get-Process -Name 'codex-meter' -ErrorAction SilentlyContinue | Stop-Process -Force
Start-ScheduledTask -TaskName 'CodexTokenMeter'
Write-Host 'Codex Token Meter now runs hidden in the background.'
`)
}

func (s *server) linuxInstaller(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.validEnrollment(token, "linux") {
		http.Error(w, "invalid enrollment", 401)
		return
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	name := "codex-meter-linux-amd64"
	sum, _ := fileSHA(filepath.Join(s.cfg.ArtifactDir, name))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	fmt.Fprintf(w, `#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'run through sudo'; exit 1; }
run_user=${SUDO_USER:-root}; run_group=$(id -gn "$run_user")
case "$(uname -m)" in x86_64|amd64) a=amd64;; aarch64|arm64) a=arm64;; *) echo unsupported; exit 1;; esac
n="codex-meter-linux-$a"; curl -fsSLo /usr/local/bin/codex-meter "%s/downloads/$n"; case "$a" in amd64) sum='%s';; *) sum=$(curl -fsSL "%s/downloads/$n.sha256" | awk '{print $1}');; esac; echo "$sum  /usr/local/bin/codex-meter" | sha256sum -c -; chmod 0755 /usr/local/bin/codex-meter
/usr/local/bin/codex-meter enroll --server '%s' --token '%s' --platform linux --config /etc/codex-token-meter/agent.json
install -d -m 0700 -o "$run_user" -g "$run_group" /var/lib/codex-token-meter/agent
chown root:"$run_group" /etc/codex-token-meter; chmod 0750 /etc/codex-token-meter
chown "$run_user:$run_group" /etc/codex-token-meter/agent.json /var/lib/codex-token-meter/agent/agent.db
cat >/etc/systemd/system/codex-meter-agent.service <<UNIT
[Unit]
Description=Codex Token Meter Agent
After=network-online.target
[Service]
User=$run_user
Group=$run_group
ExecStart=/usr/local/bin/codex-meter agent --config /etc/codex-token-meter/agent.json
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload; systemctl enable --now codex-meter-agent
`, base, sum, base, base, token)
}
func (s *server) validEnrollment(token, platform string) bool {
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM enrollments WHERE token_hash=? AND platform=? AND used_at IS NULL AND expires_at>?", tokenHash(token), platform, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&n)
	return n == 1
}
func (s *server) download(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Path)
	if !strings.HasPrefix(name, "codex-meter-") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.cfg.ArtifactDir, name))
}
func fileSHA(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}

func init() { _ = fs.ValidPath; _ = runtime.GOOS }
