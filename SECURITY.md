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
suspension checks, and audited SQLite transitions. Local moderation, 1.1 relay
discovery/import, and storage administration use operating-system-authorized CLI
commands; no administrative HTTP endpoint is exposed. Discovery reuses the
proxy-free SSRF-resistant actor resolver and never sends synthetic ActivityPub
POSTs. See `docs/SECURITY.md`, `docs/DISCOVERY-REACHABILITY.md`, and
`docs/HANDLERS.md`.

## Background reachability

The optional 1.1 background reachability worker is disabled by default, reuses the proxy-free SSRF-resistant actor resolver, has fixed bounded scheduling/concurrency, logs aggregate results only, and never substitutes its observations for authenticated lifecycle heartbeat recency. Automatic soft pruning is fail-closed behind recent complete reachability coverage when the worker is enabled.

## Public 1.1 projection

The default-off `/v2/relays` projection and human `/` page are read-only and
share one bounded public-read concurrency budget. They expose only canonical
relay identity plus reviewed heartbeat/reachability/inbox/RFC 9421 evidence.
They do not expose discovery source kind/label, operator or reason tokens,
audit events, resolver failures, signing-key identifiers, client addresses, or
internal registration/discovery participation flags. `/v1/relays` remains the
frozen 1.0 compatibility representation. See `docs/PUBLIC-LISTING.md`.
