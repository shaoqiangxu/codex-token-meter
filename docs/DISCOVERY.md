# Phase 0 discovery

Discovery completed on 2026-09-04 UTC. Only metadata and schema keys/types were inspected; no prompt, reasoning body, tool result, source code, environment value, or credential was recorded.

- Host: Ubuntu/Linux 6.8, x86_64, root; 12.2 GB RAM (about 5.0 GB available during discovery); root filesystem 193 GB with 86 GB free.
- Tools: Git 2.43.0, Codex CLI 0.149.1, cloudflared 2026.8.2. Go was absent and Go 1.22.2 was installed for the build.
- `CODEX_HOME`: `/root/.codex`; live logs: `/root/.codex/sessions/**/*.jsonl`; archived logs: `/root/.codex/archived_sessions/**/*.jsonl`; config: `/root/.codex/config.toml`.
- Probe scope: the two newest JSONL files, 33,060 records. The probe emitted keys and field types only. Script: `tools/schema_probe.py`.
- Cloudflare: origin certificate login was already available; three tunnels existed. The healthy, locally managed `beszel-vps` tunnel was safe to reuse. `token.xsqhub.com` did not exist before deployment.
- Ports: 8787 was free. Public 443 was already used by Docker, which does not conflict with an outbound Cloudflare Tunnel. The application now listens only on `127.0.0.1:8787`.

The schema sample exposed exact cumulative and last-usage counters, including cache-write and reasoning output. No visible text-delta field occurred in the sampled files.
