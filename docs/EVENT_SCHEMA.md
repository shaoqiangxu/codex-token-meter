# Codex JSONL event schema

Observed top-level keys and types:

| Key | Type |
|---|---|
| `timestamp` | string |
| `type` | string |
| `payload` | object |
| `ordinal` | integer |

Relevant observed paths:

| Path | Type / semantics |
|---|---|
| `payload.session_id`, `payload.thread_id`, `payload.id` | string identifiers |
| `payload.turn_id`, nested metadata `turn_id` | string |
| `payload.cwd`, `payload.thread_settings.cwd` | string; reduced to repository/project identity before upload |
| `payload.model`, `payload.thread_settings.model` | string |
| `payload.thread_settings.reasoning_effort` | string |
| `payload.info.total_token_usage` | object; authoritative cumulative counter |
| `payload.info.last_token_usage` | object; retained only for schema detection, never independently accumulated |
| `input_tokens` | integer |
| `cached_input_tokens` | integer; subset of input |
| `cache_write_input_tokens` | integer; present in all 61 sampled usage records and subset of input |
| `output_tokens` | integer |
| `reasoning_output_tokens` | integer; subset of output |
| `total_tokens` | integer; equaled input + output in all 61 sampled records |
| `payload.info.model_context_window` | integer |

No visible `delta` or `text_delta` path was observed in the two-file sample. The parser supports such a field if it appears later, tokenizes the value locally, immediately discards the text, and uploads only the token count.

Some derived sub-conversations in current Codex logs are identified as `ctco_*` or `fco_*` and do not carry an explicit parent field. For these verified forms only, the containing main log's stable conversation UUID is used as the parent. Their copied cumulative prefix is subtracted from the parent's latest counter.

Parser version: `codex-jsonl-v1`. Unknown or malformed records are skipped without stopping collection. A partial final line is held until its newline arrives.
