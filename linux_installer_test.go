package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLinuxInstallerServiceUserCanOpenState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux installer")
	}
	s, done := testServerDB(t)
	defer done()
	root := t.TempDir()
	s.cfg.ArtifactDir = filepath.Join(root, "artifacts")
	s.cfg.PublicURL = "https://synthetic.example"
	os.MkdirAll(s.cfg.ArtifactDir, 0755)
	// Simulate only enrollment and download. Real install/chown/chmod/runuser
	// exercise the service user's access, entirely under the isolated root.
	artifact := []byte(`#!/bin/sh
set -eu
state="$METER_INSTALL_FIXTURE_ROOT/var/lib/codex-token-meter"
mkdir -p "$METER_INSTALL_FIXTURE_ROOT/etc/codex-token-meter"
if [ ! -d "$state" ]; then mkdir -m 0700 "$state"; fi
mkdir -p "$state/agent"
printf '{}\n' > "$METER_INSTALL_FIXTURE_ROOT/etc/codex-token-meter/agent.json"
printf 'synthetic checkpoint\n' > "$state/agent/agent.db"
printf 'synthetic sidecar\n' > "$state/agent/agent.db-wal"
`)
	for _, name := range []string{"codex-meter-linux-amd64", "codex-meter-linux-arm64"} {
		if err := os.WriteFile(filepath.Join(s.cfg.ArtifactDir, name), artifact, 0755); err != nil {
			t.Fatal(err)
		}
	}
	const token = "synthetic-installer"
	_, err := s.db.Exec("INSERT INTO enrollments(token_hash,platform,expires_at,created_at)VALUES(?,'linux',?,?)", tokenHash(token), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.linuxInstaller(w, httptest.NewRequest("GET", "/install/linux.sh?token="+token, nil))
	if w.Code != 200 {
		t.Fatalf("installer status=%d", w.Code)
	}
	script := w.Body.String()
	for _, path := range []string{"/var/lib/codex-token-meter", "/etc/codex-token-meter", "/etc/systemd/system", "/usr/local/bin/codex-meter"} {
		script = strings.ReplaceAll(script, path, root+path)
	}
	check := exec.Command("sh", "-n")
	check.Stdin = strings.NewReader(script)
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("invalid rendered shell: %v %s", err, output)
	}
	if os.Geteuid() != 0 {
		t.Skip("rendered shell validated; real unprivileged-user check requires root")
	}
	if _, err := exec.LookPath("runuser"); err != nil {
		t.Skip("runuser unavailable")
	}
	// Permit traversal to the test root; all contents are synthetic.
	os.Chmod(filepath.Dir(root), 0755)
	os.Chmod(root, 0755)
	for _, dir := range []string{"mock-bin", "usr/local/bin", "etc/systemd/system", "var/lib"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	mocks := map[string]string{
		"curl":      "#!/bin/sh\nset -eu\ncp \"$METER_INSTALL_FIXTURE_ARTIFACT\" \"$2\"\n",
		"systemctl": "#!/bin/sh\nexit 0\n",
	}
	for name, body := range mocks {
		if err := os.WriteFile(filepath.Join(root, "mock-bin", name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	run := func() ([]byte, error) {
		cmd := exec.Command("sh")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), "SUDO_USER=nobody", "PATH="+filepath.Join(root, "mock-bin")+":"+os.Getenv("PATH"), "METER_INSTALL_FIXTURE_ROOT="+root, "METER_INSTALL_FIXTURE_ARTIFACT="+filepath.Join(s.cfg.ArtifactDir, "codex-meter-linux-amd64"))
		return cmd.CombinedOutput()
	}
	if output, err := run(); err != nil {
		t.Fatalf("fresh install failed: %v %s", err, output)
	}
	config := root + "/etc/codex-token-meter/agent.json"
	db := root + "/var/lib/codex-token-meter/agent/agent.db"
	for _, path := range []string{config, db, db + "-wal"} {
		cmd := exec.Command("runuser", "-u", "nobody", "--", "test", "-r", path)
		if err := cmd.Run(); err != nil {
			t.Fatalf("service user cannot traverse/read %s: %v", path, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("configuration or state file is not private")
		}
	}
	if err := exec.Command("runuser", "-u", "nobody", "--", "test", "-w", filepath.Dir(db)).Run(); err != nil {
		t.Fatal("service user cannot write SQLite sidecars")
	}
	if _, err := run(); err == nil {
		t.Fatal("existing installation was re-enrolled")
	}
	b, _ := os.ReadFile(db)
	if string(b) != "synthetic checkpoint\n" {
		t.Fatal("existing checkpoint was overwritten")
	}
}
