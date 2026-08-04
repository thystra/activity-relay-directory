# Architecture

Activity-Relay Directory is a separate service and release artifact from
Activity-Relay itself.

The initial process contains:

- environment-backed configuration with strict validation;
- an HTTP server with health, readiness, and public status endpoints;
- immutable build-version metadata;
- no persistence;
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
HTTP fetch and has no handler or persistence dependency; network-target
enforcement and a shared durable replay backend remain later gates.

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

Persistent storage and public registration remain out of scope for the initial
scaffold.
