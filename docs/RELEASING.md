# Releasing

No release workflow is active in the initial scaffold.

Before the first release:

1. define versioning and compatibility policy;
2. add deterministic binary and container builds;
3. add SBOM and checksum generation;
4. validate clean installation and upgrade behavior;
5. document database backup and migration behavior;
6. perform an integration soak with relay2;
7. verify that registration remains disabled unless explicitly configured;
8. publish release notes and rollback instructions.

SQLite is active during process startup and readiness checks, but no request
handler writes directory state yet. Before the first release, test fresh
creation, idempotent restart, named-volume persistence, upgrade from every
supported schema version, backup restoration, and refusal of drifted or future
schemas. Release notes must identify the resulting schema version and state
that downgrade requires restoration of the matching pre-upgrade backup.

Schema version 2 adds private append-only moderation events. Before releasing
it, verify version 1-to-2 upgrade preservation, atomic state/event rollback,
idempotent suspend and restore concurrency, audit backup restoration, and that
moderator and reason tokens are absent from public output. An operator CLI or
administrative transport requires its own authorization and audit review.

Before replay-protected handlers are released, validate duplicate suppression
across restart and supported service topology, expiry-boundary replacement,
bounded cleanup scheduling, failure rollback, and the reviewed admission policy
under sustained unique traffic.

Signed lifecycle handlers are present but disabled by default. Before their
first deployment, validate both disabled and explicitly enabled startup,
`registration_available`, exact proxy peer derivation, all HTTP mappings in
`docs/HANDLERS.md`, real Activity-Relay signatures for all three operations,
nonce rejection across restart, suspension behavior, fixed admission bounds,
maintenance cancellation, database backup/restore, and logs for data leakage.
Enabling the server does not activate an Activity-Relay client.

Before actor resolution is wired or released, compare the prohibited-address
policy with the current IANA IPv4 and IPv6 special-purpose registries. Validate
mixed public/private DNS answers, direct literals, connection pinning, custom
HTTPS ports, redirects, proxy exclusion, timeouts, header/body limits,
ActivityStreams media types, duplicate/deep JSON, actor/key ownership, both RSA
PEM forms, cancellation, and public error redaction.
