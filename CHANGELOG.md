# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use semantic versioning.

## [0.1.7] - 2026-09-05

### Fixed

- Wake new and quiet JSONL files through bounded filesystem notifications; retain running files in the active tail set across silence and restarts. Keep periodic discovery as a fallback.
- Recover from observed compaction records larger than the former 8MiB line cap. Read bounded chunks up to 64MiB, preserve atomic spool/checkpoint commits, and report the source ID and blocked offset instead of silently skipping usage.
- Send started/completed task evidence independently of historical usage and pricing. Started-only tasks appear as unsettled; completion does not wait for final usage.
- Pin coherent SQLite WAL read snapshots without holding the ingest lock during history queries. Cache immutable per-event historical prices and re-query absolute totals for changed conversations during numeric updates.
- Include already-readable fractional-second usage immediately in live ranges; preserve second-precision custom filters and historical timestamps.
- Report process build/PID/start time, oldest queue age, retry status and redacted scan errors. Add bounded event-ID delivery traces, real-data-copy profiling and source-to-browser regression coverage.

Existing device identity, credentials, baseline and spool must be retained during the in-place Agent upgrade. Monitoring still makes no model calls. No new numeric animation or visual redesign is included.

## [0.1.5] - 2026-09-05

### Fixed

- Attach orphaned legacy message/tool records to their unambiguous native source task on the same device, including historical filters and active-task counts. Preserve every raw usage record and historical cost.
- Recognize old agent message-ID checkpoints during ingestion so they cannot create an extra standalone task when the source owner is known.
- Add optional explicit project display aliases to combine a repository folder name with its confirmed project name without merging distinct real tasks or devices.

## [0.1.4] - 2026-09-05

### Fixed

- Apply custom date/time changes automatically after a short debounce; no Apply button is required. Cancel old requests immediately while dates are being edited, and keep invalid ranges from silently showing unrelated results.
- Show the active filter and the earliest collected-data timestamp so identical totals across today/week/month can be understood. Avoid caching the dashboard HTML across UI upgrades.

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

[0.1.5]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.5
[0.1.4]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.4
[0.1.3]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.3
[0.1.2]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.2
[0.1.1]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.1
[0.1.0]: https://github.com/shaoqiangxu/codex-token-meter/releases/tag/v0.1.0
