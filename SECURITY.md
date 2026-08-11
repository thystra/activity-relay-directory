# Security Policy

Report suspected vulnerabilities privately to the repository owner through
GitHub's private vulnerability reporting feature when available.

Do not include active credentials, private keys, personal information, or
unredacted production logs in a public issue.

Lifecycle registration, heartbeat, and unregister routes exist but are disabled
together by default, and durable enrollment starts closed. When lifecycle is
explicitly enabled, requests pass bounded parsing, RFC 9530 content-digest and
RFC 9421 signature verification, timestamp and nonce replay checks, canonical
identity binding, SSRF-resistant actor/key resolution, admission control,
suspension checks, and audited SQLite transitions. Local moderation and storage
administration use operating-system-authorized CLI commands; no administrative
HTTP endpoint is exposed. See `docs/SECURITY.md` and `docs/HANDLERS.md`.
