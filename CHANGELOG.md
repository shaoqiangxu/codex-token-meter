# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use semantic versioning.

## [0.1.0] - 2026-09-05

### Added

- Single Go binary with server, agent, enrollment, explicit backfill, backup, restore, and password-hash commands.
- Linux and Windows Codex JSONL collection with deployment baselines, persistent checkpoints, offline spool, archive handling, and stable event deduplication.
- Parent/child task aggregation and privacy-preserving project/task display metadata.
- SQLite WAL central store, authenticated Chinese dashboard, SSE updates, REST fallback, CSV/JSON export, and per-device enrollment.
- Historical OpenAI API/Vercel equivalents, Codex Credits equivalents, and ECB USD/CNY reference rates.
- Hardened systemd units, daily online backups, hidden Windows startup, and versioned release downloads.
- Chinese and English open-source documentation, MIT license, CI, Dependabot, contribution guide, and private vulnerability-reporting policy.

### Fixed

- Keep response-item IDs (`msg_*`, `ctc_*`, `ctco_*`) separate from the owning Codex task ID so tool calls cannot create fake sessions.
- Detect cumulative Token counter restarts inside an append-only rollout file and begin a new source epoch without losing usage.

[0.1.0]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.0
