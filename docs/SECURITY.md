# Security notes

- The public origin is an outbound Cloudflare Tunnel. Port 8787 binds only to loopback and is not exposed by the host firewall or Docker.
- Administrator authentication uses Argon2id (`m=64 MiB`, `t=3`, `p=2`), a signed 12-hour HttpOnly/Secure/SameSite=Strict cookie, CSRF checks, security headers, and an in-memory failed-login limit of five attempts per 15 minutes per client IP.
- Enrollment tokens are single-use and expire after 15 minutes. Every agent receives an independent random bearer token; the central database stores only SHA-256 token hashes. Host ID must match the token record. Tokens can be revoked by setting `agents.revoked_at`.
- Ingest requests are limited to 2 MiB and 256 events. The web server has header/read limits. Debug and database endpoints do not exist.
- The agent's central payload contains only identifiers, reduced project metadata, numeric usage, timestamps, model settings, parser version, and quality flags. Absolute paths stay in the local checkpoint database unless explicitly enabled in a future release.
- Server and agent logs never intentionally print tokens, passwords, cookies, Cloudflare credentials, or Codex credentials.
- Server runs as the unprivileged `codex-meter` account with systemd filesystem/kernel hardening. The local VPS agent runs as root only because the monitored Codex home is `/root/.codex`; remote Linux installation runs as the invoking Codex user when possible.

The API/Vercel/Credits figures are deterministic equivalents, not claims about actual third-party billing. Subscription-included usage reports zero incremental cash spend.
