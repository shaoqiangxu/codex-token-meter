#!/bin/sh
set -eu
project=/opt/codex-token-meter
[ ! -e /etc/codex-token-meter/server.json ] || { echo "already installed; use the documented update procedure"; exit 1; }
id codex-meter >/dev/null 2>&1 || useradd --system --home /var/lib/codex-token-meter --shell /usr/sbin/nologin codex-meter
install -d -m 0750 -o root -g codex-meter /etc/codex-token-meter
install -d -m 0750 -o codex-meter -g codex-meter /var/lib/codex-token-meter /var/backups/codex-token-meter
install -d -m 0700 -o root -g root /var/lib/codex-token-meter/agent
chown codex-meter:codex-meter /var/lib/codex-token-meter/meter.db 2>/dev/null || true
install -m 0755 "$project/dist/codex-meter-linux-amd64" /usr/local/bin/codex-meter
install -m 0644 "$project/deploy/codex-meter-server.service" /etc/systemd/system/
install -m 0644 "$project/deploy/codex-meter-agent.service" /etc/systemd/system/
install -m 0644 "$project/deploy/codex-meter-backup.service" /etc/systemd/system/
install -m 0644 "$project/deploy/codex-meter-backup.timer" /etc/systemd/system/
password=$(openssl rand -base64 27 | tr -d '/+=' | cut -c1-24)
session=$(openssl rand -hex 32)
hash=$(/usr/local/bin/codex-meter hash-password --password "$password")
python3 - "$hash" "$session" <<'PY'
import json,sys
cfg={"listen":"127.0.0.1:8787","data_dir":"/var/lib/codex-token-meter","admin_user":"admin","admin_password_hash":sys.argv[1],"session_secret":sys.argv[2],"public_url":"https://token.xsqhub.com","artifact_dir":"/opt/codex-token-meter/dist"}
open('/etc/codex-token-meter/server.json','w').write(json.dumps(cfg,indent=2)+'\n')
PY
chmod 0640 /etc/codex-token-meter/server.json
chown root:codex-meter /etc/codex-token-meter/server.json
printf '%s\n' "$password" > /etc/codex-token-meter/initial-admin-password
chmod 0600 /etc/codex-token-meter/initial-admin-password
systemctl daemon-reload
systemctl enable --now codex-meter-server.service
for i in 1 2 3 4 5 6 7 8 9 10; do curl -fsS http://127.0.0.1:8787/readyz >/dev/null && break; sleep 1; done
enrollment=$(openssl rand -hex 24)
ehash=$(printf '%s' "$enrollment" | sha256sum | awk '{print $1}')
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
expires=$(date -u -d '+15 minutes' +%Y-%m-%dT%H:%M:%SZ)
runuser -u codex-meter -- sqlite3 /var/lib/codex-token-meter/meter.db "INSERT INTO enrollments(token_hash,platform,expires_at,created_at) VALUES('$ehash','linux','$expires','$now');"
/usr/local/bin/codex-meter enroll --server http://127.0.0.1:8787 --token "$enrollment" --platform linux --alias "$(hostname -s)" --config /etc/codex-token-meter/agent.json >/dev/null
chmod 0600 /etc/codex-token-meter/agent.json
systemctl enable --now codex-meter-agent.service codex-meter-backup.timer
echo "local deployment complete"
