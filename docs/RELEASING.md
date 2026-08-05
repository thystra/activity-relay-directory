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

Before replay-protected handlers are released, validate duplicate suppression
across restart and supported service topology, expiry-boundary replacement,
bounded cleanup scheduling, failure rollback, and rate policy under sustained
unique traffic.

Before actor resolution is wired or released, compare the prohibited-address
policy with the current IANA IPv4 and IPv6 special-purpose registries. Validate
mixed public/private DNS answers, direct literals, connection pinning, custom
HTTPS ports, redirects, proxy exclusion, timeouts, header/body limits,
ActivityStreams media types, duplicate/deep JSON, actor/key ownership, both RSA
PEM forms, cancellation, and public error redaction.
