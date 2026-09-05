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
  "artifact_dir": "/opt/codex-token-meter/dist",
  "realtime": {
    "coalesce_ms": 200,
    "heartbeat_ms": 5000,
    "delayed_ms": 12000,
    "offline_ms": 30000,
    "probe_ms": 30000
  },
  "project_aliases": {
    "example-project": "Example Project"
  }
}
```

生成密码哈希和会话密钥：

```bash
codex-meter hash-password --password 'choose-a-strong-password'
openssl rand -hex 32
```

`listen` 只能使用 `127.0.0.1`、`localhost` 或 `::1`。请通过 Cloudflare Tunnel、反向代理或私有网络入口提供 TLS，不要直接将应用端口暴露到公网。

`project_aliases` 可省略。只有确认是同一个项目时，才将旧名称或仓库目录名映射到统一显示名；匹配区分大小写，服务重启后生效。它只统一项目展示名称，不合并两个真实任务、不跨设备聚合，也不修改原始用量和历史价格。不要为名称相似但实际不同的项目设置同一别名。

`realtime` 可省略，以上为默认值。合并周期限定100–250ms；心跳1–10秒；延迟阈值至少两个心跳周期，失联阈值必须晚于延迟阈值。配置由服务端下发网页，修改后只需重启 Meter 服务。轻量认证探测测量实际请求往返耗时，不是浏览器时间减服务器时间。完整协议及兼容性见[实时同步说明](REALTIME.md)。

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
