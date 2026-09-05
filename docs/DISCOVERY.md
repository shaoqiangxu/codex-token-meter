# Reference schema discovery

The initial discovery was performed against a recent Codex CLI installation on Linux. Only metadata, JSON key names, field types, and aggregate occurrence counts were inspected. Prompt text, reasoning bodies, tool results, source code, environment values, credentials, and complete JSONL records were never recorded.

## Locations

The default locations observed and supported by the agent are:

- Linux/macOS-style home: `${CODEX_HOME:-$HOME/.codex}`
- Windows home: `%USERPROFILE%\.codex`
- Live logs: `sessions/**/*.jsonl`
- Archived logs: `archived_sessions/**/*.jsonl`
- Optional Codex state database: discovered under the same Codex home and queried only for an explicit task name plus reduced project metadata

The agent derives these paths at runtime. Use `codex_homes` in the Agent configuration when Codex data lives elsewhere.

## Discovery method

`tools/schema_probe.py` is a local-only helper. It reads a small number of recent files and emits key/type summaries rather than values. Do not attach its source files or real session files to an Issue.

The reference sample confirmed:

- conversation/session/thread and turn identifiers;
- working directory and project context;
- model and reasoning effort;
- cumulative `total_token_usage` and non-authoritative `last_token_usage`;
- input, cached input, cache-write input, output, reasoning output, total, and context-window counters;
- archive movement using the same underlying conversation/source identity.

No visible `delta` or `text_delta` field occurred in the reference sample. The parser supports a future visible delta defensively, but exact usage remains the primary accounting source.

See [`EVENT_SCHEMA.md`](EVENT_SCHEMA.md) for the normalized schema and accounting semantics.
