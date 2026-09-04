package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func enrollCommand(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	serverURL := fs.String("server", "", "server URL")
	token := fs.String("token", "", "one-time enrollment token")
	platform := fs.String("platform", runtime.GOOS, "windows or linux")
	config := fs.String("config", defaultAgentConfig(), "config path")
	alias := fs.String("alias", "", "host alias")
	if err := fs.Parse(args); err != nil {
		return err
	}
	u, err := cleanServerURL(*serverURL)
	if err != nil {
		return err
	}
	if *token == "" {
		return errors.New("enrollment token required")
	}
	if *alias == "" {
		h, _ := os.Hostname()
		*alias = h
	}
	body, _ := json.Marshal(map[string]string{"token": *token, "platform": *platform, "alias": *alias})
	resp, err := http.Post(u+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("enrollment HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		HostID     string `json:"host_id"`
		AgentToken string `json:"agent_token"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.HostID == "" {
		return errors.New("invalid enrollment response")
	}
	homes := codexHomesDefault()
	if runtime.GOOS != "windows" && os.Getenv("SUDO_USER") != "" && os.Getenv("SUDO_USER") != "root" {
		if u, err := user.Lookup(os.Getenv("SUDO_USER")); err == nil {
			homes = []string{filepath.Join(u.HomeDir, ".codex")}
		}
	}
	state := filepath.Join(filepath.Dir(*config), "state")
	if runtime.GOOS != "windows" {
		state = "/var/lib/codex-token-meter/agent"
	}
	cfg := AgentConfig{ServerURL: u, HostID: out.HostID, HostAlias: *alias, Token: out.AgentToken, CodexHomes: homes, StateDir: state, MonitoringStartedAt: time.Now().UTC()}
	if err := writeJSON0600(*config, cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runAgent(ctx, *config, true, false); err != nil {
		return fmt.Errorf("enrolled but initial connection failed: %w", err)
	}
	fmt.Printf("enrolled host %s; monitoring_started_at=%s\n", out.HostID, cfg.MonitoringStartedAt.Format(time.RFC3339))
	return nil
}

func backupCommand(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dbPath := fs.String("db", "/var/lib/codex-token-meter/meter.db", "database")
	out := fs.String("out", "", "output")
	keep := fs.Int("keep", 14, "retention days")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join("/var/backups/codex-token-meter", "meter-"+time.Now().UTC().Format("20060102-150405")+".db")
	}
	db, e := openSQLite(*dbPath)
	if e != nil {
		return e
	}
	defer db.Close()
	if e = os.MkdirAll(filepath.Dir(*out), 0700); e != nil {
		return e
	}
	safe := strings.ReplaceAll(*out, "'", "''")
	if _, e = db.Exec("VACUUM INTO '" + safe + "'"); e != nil {
		return e
	}
	cut := time.Now().AddDate(0, 0, -*keep)
	entries, _ := os.ReadDir(filepath.Dir(*out))
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), "meter-") && strings.HasSuffix(de.Name(), ".db") {
			if st, e := de.Info(); e == nil && st.ModTime().Before(cut) {
				_ = os.Remove(filepath.Join(filepath.Dir(*out), de.Name()))
			}
		}
	}
	fmt.Println(*out)
	return nil
}
func restoreCommand(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	src := fs.String("from", "", "backup")
	dst := fs.String("db", "/var/lib/codex-token-meter/meter.db", "database")
	force := fs.Bool("force", false, "confirm offline restore")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*force || *src == "" {
		return errors.New("restore requires --from and --force with server stopped")
	}
	check, e := openSQLite(*src)
	if e != nil {
		return e
	}
	var status string
	e = check.QueryRow("PRAGMA integrity_check").Scan(&status)
	check.Close()
	if e != nil || status != "ok" {
		return errors.New("backup integrity check failed")
	}
	in, e := os.Open(*src)
	if e != nil {
		return e
	}
	defer in.Close()
	tmp := *dst + ".restore.tmp"
	out, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp, *dst)
}
func _unusedSQL(_ *sql.DB) {}
