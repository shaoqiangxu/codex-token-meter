# Operations

## Services and health

The server and local agent are systemd services. The server is restricted to `127.0.0.1:8787`; Cloudflare Tunnel is the only public route. `/healthz` checks process health and `/readyz` checks SQLite readiness.

```sh
systemctl restart codex-meter-agent
systemctl restart codex-meter-server
systemctl is-active codex-meter-server codex-meter-agent cloudflared-beszel codex-meter-backup.timer
```

## Backups and restore

`codex-meter-backup.timer` runs an online SQLite `VACUUM INTO` backup daily and retains 14 days.

```sh
codex-meter backup --db /var/lib/codex-token-meter/meter.db
systemctl stop codex-meter-server
codex-meter restore --from /var/backups/codex-token-meter/meter-YYYYMMDD-HHMMSS.db --db /var/lib/codex-token-meter/meter.db --force
chown codex-meter:codex-meter /var/lib/codex-token-meter/meter.db
systemctl start codex-meter-server
```

## Update and rollback

Build, test, atomically stage, then restart:

```sh
cd /opt/codex-token-meter
git pull --ff-only
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/codex-meter-linux-amd64 .
install -m 0755 dist/codex-meter-linux-amd64 /usr/local/bin/codex-meter.new
mv /usr/local/bin/codex-meter /usr/local/bin/codex-meter.previous
mv /usr/local/bin/codex-meter.new /usr/local/bin/codex-meter
systemctl restart codex-meter-server codex-meter-agent
```

Rollback:

```sh
systemctl stop codex-meter-server codex-meter-agent
mv /usr/local/bin/codex-meter /usr/local/bin/codex-meter.failed
mv /usr/local/bin/codex-meter.previous /usr/local/bin/codex-meter
systemctl start codex-meter-server codex-meter-agent
```

## Remote uninstall

Windows (checkpoint retained):

```powershell
Unregister-ScheduledTask -TaskName CodexTokenMeter -Confirm:$false; Stop-Process -Name codex-meter -Force -ErrorAction SilentlyContinue; Remove-Item "$env:LOCALAPPDATA\CodexTokenMeter\codex-meter.exe","$env:LOCALAPPDATA\CodexTokenMeter\agent.json" -Force
```

Linux (checkpoint retained):

```sh
sudo systemctl disable --now codex-meter-agent; sudo mv /etc/systemd/system/codex-meter-agent.service /var/lib/codex-token-meter/codex-meter-agent.service.uninstalled; sudo systemctl daemon-reload
```
