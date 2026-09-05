# Configuration

配置文件包含凭据，必须位于仓库之外并限制为最小读取权限。不要把真实配置提交到 Git。

## Server

默认路径为 `/etc/codex-token-meter/server.json`：

```json
{
  "listen": "127.0.0.1:8787",
  "data_dir": "/var/lib/codex-token-meter",
  "admin_user": "admin",
  "admin_password_hash": "$argon2id$...",
  "session_secret": "replace-with-a-long-random-secret",
  "public_url": "https://meter.example.com",
  "artifact_dir": "/opt/codex-token-meter/dist"
}
```

生成密码哈希和会话密钥：

```bash
codex-meter hash-password --password 'choose-a-strong-password'
openssl rand -hex 32
```

`listen` 只能使用 `127.0.0.1`、`localhost` 或 `::1`。请通过 Cloudflare Tunnel、反向代理或私有网络入口提供 TLS，不要直接将应用端口暴露到公网。

## Agent

Agent 配置由一次性 enrollment 自动生成，不建议手工编写：

```json
{
  "server_url": "https://meter.example.com",
  "host_id": "generated-host-id",
  "host_alias": "workstation",
  "token": "generated-agent-token",
  "codex_homes": ["/home/alice/.codex"],
  "state_dir": "/var/lib/codex-token-meter/agent",
  "monitoring_started_at": "2026-01-01T00:00:00Z",
  "absolute_paths": false
}
```

每个 Agent 使用独立令牌。中央数据库只保存令牌哈希；本地配置保存原始令牌，因此 Linux 上应使用 `0600` 权限，Windows 上应仅授权当前用户。

## Enrollment

管理员登录仪表盘后选择“添加 Windows 电脑”或“添加 Linux VPS”。服务端生成 15 分钟有效、仅能使用一次的安装命令。安装完成后，Agent 会记录 `monitoring_started_at`，并默认从已有日志的 EOF 开始，只统计部署基线之后的新事件。

如确实需要导入基线前历史，显式运行：

```bash
codex-meter backfill --config /etc/codex-token-meter/agent.json
```

回填可能产生大量历史数据，且不可代替备份；不要把它放入自动启动流程。
