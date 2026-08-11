# Releasing

No release workflow is active yet.

Before the first release:

1. define versioning and compatibility policy;
2. add deterministic binary and container builds;
3. add SBOM and checksum generation;
4. validate clean installation and upgrade behavior;
5. document database backup and migration behavior;
6. perform an integration soak with relay2;
7. verify that registration remains disabled unless explicitly configured;
8. publish release notes and rollback instructions.

SQLite is active during process startup and readiness checks. Explicitly
enabled lifecycle handlers write audited registration, heartbeat, unregister,
and replay state. Before the first release, test fresh creation, idempotent
restart, named-volume persistence, upgrade from every supported schema version,
backup restoration, and refusal of drifted or future schemas. Release notes
must identify the resulting schema version and state that downgrade requires
restoration of the matching pre-upgrade backup.

Schema version 3 adds default-closed enrollment policy and private append-only
enrollment audit events after schema version 2's moderation events. Before
releasing it, verify supported upgrade preservation, atomic state/event rollback,
idempotent suspend and restore concurrency, audit backup restoration, and that
moderator and reason tokens are absent from public output. The local operator
CLI must retain its operating-system authorization and private-audit boundary;
any network administrative transport requires a separate authorization and
audit review.

Before replay-protected handlers are released, validate duplicate suppression
across restart and supported service topology, expiry-boundary replacement,
bounded cleanup scheduling, failure rollback, and the reviewed admission policy
under sustained unique traffic.

Signed lifecycle handlers are present but disabled by default. Before their
first deployment, validate both disabled and explicitly enabled startup,
`lifecycle_available`, `enrollment_open`, exact proxy peer derivation, all HTTP mappings in
`docs/HANDLERS.md`, real Activity-Relay signatures for all three operations,
nonce rejection across restart, suspension behavior, fixed admission bounds,
maintenance cancellation, database backup/restore, and logs for data leakage.
Enabling the server does not activate an Activity-Relay client.

Before the first deployment or release of the enabled actor-resolution path,
compare the prohibited-address policy with the current IANA IPv4 and IPv6
special-purpose registries. Validate
mixed public/private DNS answers, direct literals, connection pinning, custom
HTTPS ports, redirects, proxy exclusion, timeouts, header/body limits,
ActivityStreams media types, duplicate/deep JSON, actor/key ownership, both RSA
PEM forms, cancellation, and public error redaction.
Before activating a positive inactive-retention policy, first deploy/upgrade with
`DIRECTORY_INACTIVE_RETENTION_DAYS=0`, take and restore-test a fresh pre-retention standalone
SQLite backup, then capture identity-free dry-run evidence for the proposed
policy. Exercise exact 1-day/365-day boundaries, suspended and registered
exclusion, stale-candidate concurrency, interrupted/restarted batches, migration
rollback, backup mismatch rejection, exact pre-purge restoration, and append-only
guard restoration. A destructive trial must use the backup-gated local command;
no HTTP route or scheduler may initiate purge. Physical `VACUUM`/checkpoint work
is a separate maintenance operation.

## Database-growth release gate

Before the first release candidate, exercise the Tranche 17 storage guard with
email disabled and with an isolated fake/local test mailer. Validate exact
warning/critical/hard boundaries, five-minute and pre-write sampling,
`max_page_count` across supported SQLite page sizes, WAL/checkpoint growth,
freed-page reuse, near-limit migration rollback, concurrent writers, restart
notification suppression, recovery hysteresis, bounded failure retry, and
full-disk-class refusal. At hard state prove `/healthz` and allowed public/local
reads remain available while `/readyz` and every runtime mutation fail closed.

The stock container deliberately does not install a mail command. A deployment
that enables `DIRECTORY_ADMIN_EMAIL` must explicitly provide and configure the
validated command. A future Debian package may recommend a mail transport, but
must not configure recipients, credentials, relay hosts, or enable alerts. Host
filesystem monitoring remains required independently of the application budget.
No release/deployment gate may silently raise the configured database budget or
activate positive inactive retention.

## RC Go compatibility gate

The pre-RC compatibility pass establishes Go 1.26.0 as the minimum supported
Directory module floor. `go.mod` must declare `go 1.26.0` and must not add a
higher `toolchain` directive. The blocking CI matrix runs exact Go 1.26.0 and
the validated Go 1.26.5 patch lane. A separate Go 1.27rc2 forward-compatibility
job is independently scheduled so it may execute concurrently with the stable
lanes when runner capacity permits; prerelease validation failures must remain
visible for individual triage but do not silently redefine the supported floor
or stable blocking lanes.

Container builds use the validated
`docker.io/library/golang:1.26.5-alpine3.24` builder while preserving the
reviewed runtime stage. After Go 1.27 final is available, rerun full
compatibility before changing the documented supported floor or ongoing CI
matrix.

After the Directory pass is complete, apply the same deliberate Go-version
compatibility/floor review to `thystra/Activity-Relay`, including its
interoperability fixtures and release/container/package builds.
