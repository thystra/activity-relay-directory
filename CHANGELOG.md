# Changelog

## Unreleased

### Added

- Initial Go service scaffold.
- Health, readiness, and schema-versioned status endpoints.
- Strict environment configuration validation.
- Non-root, read-only container runtime.
- Test and container-build workflows.
- Version 1 lifecycle, outcome, error, health, and administrative vocabulary.
- Strictly decoded JSON request and response contract fixtures.
- Canonical HTTPS relay actor/public-base normalization and origin binding.
- RFC 9530 SHA-256 Content-Digest generation, verification, and fixtures.
- Stateless RFC 9421 directory-request verification and RSA fixture.
- Atomic opaque-key nonce reservation and replay-rejection contracts.
- Strict bounded registration parsing, target binding, and authenticated composition.
- Strict bounded heartbeat parsing, target binding, and authenticated composition.
- Strict bounded unregister parsing, target binding, and authenticated composition.
- Single-node SQLite opener with secure file checks and bounded connection settings.
- Transactional, content-hashed initial persistence migration for relay state,
  opaque replay reservations, and append-only lifecycle events.
- Required SQLite startup migration, database-backed readiness, graceful close,
  and persistent owner-only Compose data volume.
- Backend-neutral relay repository contract and atomic SQLite register,
  heartbeat, unregister, and append-only audit transitions.
- Durable SQLite RFC 9421 replay reservations with atomic conflict handling,
  restart persistence, ten-minute retention enforcement, and bounded cleanup.
- Optional Nginx, Apache, and Caddy reverse-proxy examples.
- GitHub funding links aligned with Activity-Relay.

### Security

- Registration is disabled and unavailable in the initial scaffold.
- Non-loopback public URLs require HTTPS.
- Request-body limits are bounded even before request endpoints exist.
- Database initialization and schema mismatch fail before the HTTP listener starts.
- Readiness failures do not disclose database errors or filesystem paths.
- Relay transitions reject noncanonical identities, backward acceptance time,
  absent or suspended heartbeat targets, and suspended registration.
- State mutation rolls back when its corresponding audit event cannot commit.
- Replay cleanup rolls back when the associated reservation cannot commit.
