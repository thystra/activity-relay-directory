# Architecture

Activity-Relay Directory is a separate service and release artifact from
Activity-Relay itself.

The initial process contains:

- environment-backed configuration with strict validation;
- an HTTP server with health, readiness, and public status endpoints;
- immutable build-version metadata;
- single-node SQLite startup migration and readiness checks;
- no registration protocol implementation.

The version 1 contract layer contains closed operation, outcome, error,
health, and administrative-state vocabulary plus strictly decoded JSON
fixtures. It also provides network-free canonical HTTPS relay identity and
same-origin binding plus RFC 9530 SHA-256 Content-Digest generation and
verification. Its RFC 9421 contract verifies bounded directory POST profiles
against caller-resolved RSA key material and binds the verified key identity to
the canonical relay actor. An atomic replay interface stores only opaque
SHA-256-derived key-ID/nonce keys, with a package-private bounded memory
implementation for contract tests. The combined safe path reserves only after
all stateless and actor-binding gates succeed. The contract performs no DNS or
HTTP fetch. Its register-specific composition strictly decodes a bounded body,
binds the exact operation target and canonical identity, then invokes signature
verification and atomic replay reservation. Heartbeat uses the same shared JSON
and target primitives while accepting only canonical actor identity and its own
operation path; it produces no liveness update. Unregister applies the same
identity-only boundary to a distinct removal target and produces no deletion.
These contracts have no handler or persistence dependency; network-target
enforcement and the durable replay adapter remain later gates.

The first persistence foundation is an embedded SQLite migration set for one
active directory process on one host. It defines strict relay lifecycle and
administrative state, opaque replay reservations, and append-only lifecycle
events. Migration history is transactional and content-hashed so drift,
missing history, and databases newer than the binary fail closed. Process
startup requires an absolute database path, applies migrations before opening
the HTTP listener, and keeps readiness dependent on the current reachable
schema. The container supplies an owner-only data directory through a named
local volume while retaining a read-only root filesystem. No request handler
mutates storage. SQLite files must remain local; multi-host service topology
requires a later database backend rather than shared SQLite storage. See
`docs/PERSISTENCE.md`.

An optional Nginx, Apache, or Caddy reverse proxy may terminate public HTTPS
and forward to the loopback Go listener. Proxy configuration is an operator-
owned deployment layer and must preserve the public authority and request
target required by future HTTP message-signature verification.

Runtime components will be added behind those explicit contracts:

1. signed relay registration and replacement;
2. signed daily heartbeat with bounded jitter;
3. signed unregister;
4. replay and duplicate suppression;
5. moderation and suspension state;
6. health-state calculation and pruning;
7. public JSON and human-readable directory views;
8. operator CLI and bounded administrative actions.

Storage state transitions and public registration remain out of scope for the
initial scaffold.
