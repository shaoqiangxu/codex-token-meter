#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "run this installer as root" >&2; exit 1; }
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
project=${PROJECT_DIR:-$script_dir}
public_url=${PUBLIC_URL:-}
listen=${LISTEN:-127.0.0.1:8787}
[ -n "$public_url" ] || { echo "PUBLIC_URL is required (for example https://meter.example.com)" >&2; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) artifact=codex-meter-linux-amd64 ;;
  aarch64|arm64) artifact=codex-meter-linux-arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
[ -x "$project/dist/$artifact" ] || { echo "missing $project/dist/$artifact; build it first" >&2; exit 1; }
[ ! -e /etc/codex-token-meter/server.json ] || { echo "already installed; use the documented update procedure"; exit 1; }
id codex-meter >/dev/null 2>&1 || useradd --system --home /var/lib/codex-token-meter --shell /usr/sbin/nologin codex-meter
install -d -m 0750 -o root -g codex-meter /etc/codex-token-meter
install -d -m 0750 -o codex-meter -g codex-meter /var/lib/codex-token-meter /var/backups/codex-token-meter
install -d -m 0700 -o root -g root /var/lib/codex-token-meter/agent
chown codex-meter:codex-meter /var/lib/codex-token-meter/meter.db 2>/dev/null || true
install -m 0755 "$project/dist/$artifact" /usr/local/bin/codex-meter
install -m 0644 "$project/deploy/codex-meter-server.service" /etc/systemd/system/
install -m 0644 "$project/deploy/codex-meter-agent.service" /etc/systemd/system/
install -m 0644 "$project/deploy/codex-meter-backup.service" /etc/systemd/system/
install -m 0644 "$project/deploy/codex-meter-backup.timer" /etc/systemd/system/
password=$(openssl rand -base64 27 | tr -d '/+=' | cut -c1-24)
session=$(openssl rand -hex 32)
hash=$(/usr/local/bin/codex-meter hash-password --password "$password")
python3 - "$hash" "$session" "$listen" "$public_url" "$project/dist" <<'PY'
import json,sys
cfg={"listen":sys.argv[3],"data_dir":"/var/lib/codex-token-meter","admin_user":"admin","admin_password_hash":sys.argv[1],"session_secret":sys.argv[2],"public_url":sys.argv[4],"artifact_dir":sys.argv[5]}
open('/etc/codex-token-meter/server.json','w').write(json.dumps(cfg,indent=2)+'\n')
PY
chmod 0640 /etc/codex-token-meter/server.json
chown root:codex-meter /etc/codex-token-meter/server.json
printf '%s\n' "$password" > /etc/codex-token-meter/initial-admin-password
chmod 0600 /etc/codex-token-meter/initial-admin-password
systemctl daemon-reload
systemctl enable --now codex-meter-server.service
attempt=0
until curl -fsS http://127.0.0.1:8787/readyz >/dev/null; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 10 ] || { echo "server did not become ready" >&2; exit 1; }
  sleep 1
done
enrollment=$(openssl rand -hex 24)
ehash=$(printf '%s' "$enrollment" | sha256sum | awk '{print $1}')
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
expires=$(date -u -d '+15 minutes' +%Y-%m-%dT%H:%M:%SZ)
runuser -u codex-meter -- sqlite3 /var/lib/codex-token-meter/meter.db "INSERT INTO enrollments(token_hash,platform,expires_at,created_at) VALUES('$ehash','linux','$expires','$now');"
/usr/local/bin/codex-meter enroll --server http://127.0.0.1:8787 --token "$enrollment" --platform linux --alias "$(hostname -s)" --config /etc/codex-token-meter/agent.json >/dev/null
chmod 0600 /etc/codex-token-meter/agent.json
systemctl enable --now codex-meter-agent.service codex-meter-backup.timer
echo "local deployment complete"
