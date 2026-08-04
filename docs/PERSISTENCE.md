# Persistence

The first persistence contract uses SQLite for one active directory process on
one host. SQLite keeps the initial deployment operationally small while still
providing transactions, uniqueness, and crash-safe migration bookkeeping. It
is not a multi-host database and the database must not be placed on NFS or
other network storage.

This tranche defines and tests the storage package only. The HTTP server does
not open the database, run migrations, persist requests, or report persistence
readiness yet. No configuration variable or container volume is active until
that wiring receives a separate review.

## Database opening

`internal/storage/sqlite.Open` requires an absolute path to a regular,
nonsymlink file. It creates a missing file with mode `0600` and refuses an
existing file with any group or other permissions. Operators must also keep
the parent directory owner-only and on a local filesystem.

Every connection enables:

- foreign-key enforcement;
- write-ahead logging (WAL);
- `synchronous=NORMAL`;
- a five-second busy timeout; and
- immediate write transactions.

Those settings favor a single service process with a small bounded connection
pool. A future multi-process or multi-host deployment requires a reviewed
database backend designed for that topology, expected to be PostgreSQL. It
must not share this SQLite file between hosts.

## Version 1 schema

The initial migration creates four owned tables:

- `schema_migrations` records each migration name and SHA-256 digest;
- `relays` records the canonical relay identity, public base URL, lifecycle,
  administrative state, and server-owned timestamps;
- `replay_reservations` records only opaque 32-byte replay-key digests and
  their reservation window; and
- `relay_events` records a minimal append-only lifecycle audit trail.

The relay row retains the first accepted registration timestamp. Unregister is
a lifecycle transition, not a hard deletion, and administrative suspension is
independent of lifecycle state. Database constraints reject contradictory
state and timestamp combinations.

Audit events intentionally have no foreign key to the current relay row. This
permits an idempotent `unregister_absent` event and preserves history across
later state transitions. Updates and deletes are rejected by database
triggers. A future retention policy must use a reviewed migration rather than
bypassing those triggers.

The schema contains no connected-site identities, followers, user identities,
raw request bodies, raw nonces, or signing key IDs.

## Migrations

Migrations are embedded into the binary and applied in version order within an
immediate transaction. Startup wiring will be required to migrate before the
service becomes ready. A failed migration rolls back as a unit.

The migration table records a SHA-256 digest of each embedded SQL file. The
migrator fails closed when an applied migration has changed, history has a gap,
or the database is newer than the binary. Once released, a migration is
immutable: schema changes require a new consecutively numbered migration and
upgrade tests from every supported version.

## Backup and recovery boundary

Before persistence is activated, the deployment runbook must specify a local,
encrypted backup target, retention, restore testing, and acceptable recovery
point. A backup must use SQLite's online backup API from a compatible tool, or
stop all writers and copy the database together with any `-wal` and `-shm`
files. Copying only the main file while the service is writing is not a valid
backup.

Before upgrading, take and verify a backup. Schema downgrades are not automatic:
rollback after a migration requires restoring the pre-upgrade backup with the
matching binary. Health and readiness must remain unavailable when database
opening or migration fails.
