# Codex Token Meter

Codex Token Meter is a single cross-platform Go binary with `server`, `agent`, `backfill`, `enroll`, `backup`, and `restore` modes. The central server uses SQLite WAL, an embedded Chinese web UI, REST ingestion, and SSE. It never stores prompt text, reasoning bodies, tool-result bodies, source contents, or credentials from Codex logs.

Production URL: <https://token.xsqhub.com>

## Installed layout

- Source and release artifacts: `/opt/codex-token-meter`
- Executable: `/usr/local/bin/codex-meter`
- Server config: `/etc/codex-token-meter/server.json`
- Local agent config: `/etc/codex-token-meter/agent.json`
- Central database: `/var/lib/codex-token-meter/meter.db`
- Agent checkpoint/spool: `/var/lib/codex-token-meter/agent/agent.db`
- Backups: `/var/backups/codex-token-meter`

## Commands

```sh
systemctl status codex-meter-server codex-meter-agent cloudflared-beszel
journalctl -u codex-meter-server -u codex-meter-agent --since today
curl -fsS http://127.0.0.1:8787/healthz
codex-meter backup
```

Historical import is deliberately opt-in:

```sh
systemctl stop codex-meter-agent
codex-meter backfill --config /etc/codex-token-meter/agent.json
systemctl start codex-meter-agent
```

Do not run `backfill` unless importing pre-baseline history is intended.

Windows agents run through a hidden `wscript.exe` launcher. The following command upgrades an existing Windows agent in place, preserves its enrollment/configuration, and repairs hidden launching:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm 'https://token.xsqhub.com/install/windows-hide.ps1' | iex"
```

## Accuracy model

- Exact events use positive deltas of `total_token_usage`. Presentation state is keyed by host and conversation; the accounting watermark is keyed by host, source log, and file epoch so copied `ctco_*`/`fco_*` prefixes are counted once.
- Equal totals add zero. A lower total starts a new counter epoch.
- Stable event IDs and response/turn deduplication prevent replay and path duplication.
- Archived moves retain the source UUID and byte checkpoint.
- Cached input/cache-write are input subsets; reasoning output is an output subset and is never added again to total.
- When visible deltas exist, the agent's deterministic local tokenizer emits `ESTIMATED_LIVE`; exact usage reconciles and records the error. With no delta, the UI says that the response is generating and waits for exact usage.
- SSE updates patch existing numeric DOM nodes in place. Fold state and text selection are preserved; the structural tree changes only for a new/removed logical session, a project assignment change, or an explicit filter/range action.
- The meter daemon never invokes an LLM. It reads already-produced local numeric telemetry and sends it to this server; API/Vercel amounts are comparison prices rather than monitoring traffic.
- An active session is a distinct `(host, parent-or-self conversation)` with a metering event in the last five minutes. Online devices use a separate 15-second heartbeat window.
- Conversation labels come only from Codex's explicit `threads.name` display field. Project labels prefer Codex project metadata and Git remote repository names; generic folder names such as `repo` and `root` are not presented as projects.

See `docs/OPERATIONS.md`, `docs/PRICING.md`, and `docs/SECURITY.md`.
