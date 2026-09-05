# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use semantic versioning.

## [0.1.3] - 2026-09-05

### Fixed

- Coalesce dashboard aggregation across concurrent browsers and legacy clients, not just within one page. Share pre-encoded JSON/gzip for one second, with a bounded eight-range cache and distinct calendar/custom keys.
- Keep cancelled requests from entering aggregation and prevent reconnect traffic from exhausting the SQLite connection pool with duplicate work.

## [0.1.2] - 2026-09-05

### Fixed

- Use explicit Beijing time (UTC+8) for today/week/month, show interval boundaries, validate custom filters, and label totals for the selected period.
- Replace per-event database price lookups with one historical rate-table read, preserving per-request thresholds and effective dates.
- Remove full-snapshot history replay, aggregate outside the SSE lock, coalesce notifications, and avoid aggregation when nobody is watching.
- Cancel obsolete range requests, preserve selected ranges during live updates, display loading/error/retry feedback, and fetch immediately on page load.
- Return all matching session records instead of truncating at 500; compress snapshots and render raw table rows only when expanded.
- Fit narrow mobile screens and iOS/WebKit controls: top-aligned range filters, labeled dates, stacked input/output numbers, safe-area padding, and collapsible management controls.

Existing Windows and Linux agents remain compatible; this dashboard/server update does not require agent reinstallation.

## [0.1.1] - 2026-09-05

### Fixed

- Make the Windows in-place updater retry a verified copy after stopping the running Agent, avoiding PowerShell 5.1 `Move-Item` collisions when the destination executable already exists.
- Upgrade `golang.org/x/crypto` to the first currently supported security-fixed line and raise the source-build requirement to Go 1.25.

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

[0.1.3]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.3
[0.1.2]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.2
[0.1.1]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.1
[0.1.0]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.0
