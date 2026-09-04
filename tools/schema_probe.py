#!/usr/bin/env python3
"""Inspect at most the two newest Codex JSONL files without emitting values."""
import json
import os
from pathlib import Path

root = Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex")))
files = []
for folder in (root / "sessions", root / "archived_sessions"):
    if folder.exists():
        files.extend(folder.rglob("*.jsonl"))
files = sorted(files, key=lambda p: p.stat().st_mtime, reverse=True)[:2]

interesting = {
    "id", "session_id", "conversation_id", "thread_id", "parent_id",
    "parent_session_id", "parent_conversation_id", "cwd", "model",
    "reasoning_effort", "input_tokens", "cached_input_tokens",
    "cache_write_input_tokens", "output_tokens", "reasoning_output_tokens",
    "total_tokens", "total_token_usage", "last_token_usage",
    "model_context_window", "response_id", "turn_id", "delta", "text_delta",
}
found = {}
top = {}
records = 0
bad = 0
usage_semantics = {"total_equals_input_plus_output": 0, "cache_fields_within_input": 0, "usage_samples": 0}

def typename(value):
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, int):
        return "integer"
    if isinstance(value, float):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__

def walk(value, path=""):
    if isinstance(value, dict):
        for key, child in value.items():
            child_path = f"{path}.{key}" if path else key
            if key in interesting or "token" in key.lower() or "delta" in key.lower():
                found.setdefault(child_path, set()).add(typename(child))
            walk(child, child_path)
    elif isinstance(value, list):
        for child in value[:1]:
            walk(child, path + "[]")

for path in files:
    with path.open("rb") as handle:
        for raw in handle:
            try:
                obj = json.loads(raw)
            except (json.JSONDecodeError, UnicodeDecodeError):
                bad += 1
                continue
            records += 1
            if isinstance(obj, dict):
                for key, value in obj.items():
                    top.setdefault(key, set()).add(typename(value))
            walk(obj)
            try:
                usage = obj["payload"]["info"]["total_token_usage"]
                if isinstance(usage, dict):
                    usage_semantics["usage_samples"] += 1
                    inp = int(usage.get("input_tokens", 0))
                    out = int(usage.get("output_tokens", 0))
                    if int(usage.get("total_tokens", -1)) == inp + out:
                        usage_semantics["total_equals_input_plus_output"] += 1
                    if int(usage.get("cached_input_tokens", 0)) <= inp and int(usage.get("cache_write_input_tokens", 0)) <= inp:
                        usage_semantics["cache_fields_within_input"] += 1
            except (KeyError, TypeError, ValueError):
                pass

print(f"files_scanned: {len(files)}")
print(f"records_scanned: {records}")
print(f"invalid_or_partial_lines: {bad}")
print("top_level_keys:")
for key in sorted(top):
    print(f"  {key}: {'|'.join(sorted(top[key]))}")
print("relevant_field_paths:")
for key in sorted(found):
    print(f"  {key}: {'|'.join(sorted(found[key]))}")
print("usage_semantics_counts:")
for key in sorted(usage_semantics):
    print(f"  {key}: {usage_semantics[key]}")
