# Persistence

The first persistence contract uses SQLite for one active directory process on
one host. SQLite keeps the initial deployment operationally small while still
providing transactions, uniqueness, and crash-safe migration bookkeeping. It
is not a multi-host database and the database must not be placed on NFS or
other network storage.

The process requires `DIRECTORY_DATABASE_PATH` to be a clean absolute path. It
opens the database and applies all embedded migrations before opening the HTTP
listener. Startup fails closed on file-safety errors, migration failure, drift,
or a schema newer than the binary. The `/readyz` endpoint checks that the
database remains reachable at the current schema version and returns only a
redacted `503 not ready` when that check fails.

No registration, heartbeat, unregister, moderation, or replay handler writes
to this database yet. Runtime wiring and a dormant lifecycle repository make
the schema operational; they do not make the directory protocol available.

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

The Compose service uses
`/var/lib/activity-relay-directory/directory.sqlite` in a named local volume.
The image creates the containing directory as owner-only for its non-root
service account. The root filesystem remains read-only, and the volume is the
only persistent writable service path. `DIRECTORY_DATA_VOLUME` may select the
Compose volume name without changing the in-container database path.

## Schema version 2

The initial migration creates four owned tables:

- `schema_migrations` records each migration name and SHA-256 digest;
- `relays` records the canonical relay identity, public base URL, lifecycle,
  administrative state, and server-owned timestamps;
- `replay_reservations` records only opaque 32-byte replay-key digests and
  their reservation window; and
- `relay_events` records a minimal append-only lifecycle audit trail.

Migration 2 adds `moderation_events`, a private append-only audit trail for
bounded suspend and restore decisions. It upgrades a version 1 database without
changing retained relay, lifecycle-event, or replay-reservation rows.

The relay row retains the first accepted registration timestamp. Unregister is
a lifecycle transition, not a hard deletion, and administrative suspension is
independent of lifecycle state. Database constraints reject contradictory
state and timestamp combinations.

Audit events intentionally have no foreign key to the current relay row. This
permits an idempotent `unregister_absent` event and preserves history across
later state transitions. Moderation events likewise remain independently
retained. Updates and deletes are rejected by database triggers. A future
retention policy must use a reviewed migration rather than bypassing those
triggers.

The schema contains no connected-site identities, followers, user identities,
raw request bodies, raw nonces, or signing key IDs.

## Relay state transitions

`internal/storage.RelayRepository` defines backend-neutral register, heartbeat,
and unregister operations at a server-owned acceptance time. The SQLite
implementation validates canonical bounded identities again at the storage
boundary and applies each state change plus its append-only event within one
immediate transaction.

Register has these outcomes:

- a never-registered canonical actor becomes `created` and administratively
  active;
- an already registered actor with identical metadata is `unchanged`, leaving
  its state timestamp untouched while recording the accepted intent; and
- a retained unregistered actor becomes `updated`, preserving its original
  registration timestamp while clearing unregister and old-heartbeat recency.

Administrative suspension blocks register without clearing suspension.
Heartbeat requires a registered, administratively active relay and records the
server acceptance time as both current liveness and state update time.

Unregister is idempotent. A registered relay becomes `removed`; an unknown or
already unregistered relay is `absent`. Repeated authenticated absent intents
may each receive an audit event but never create or duplicate relay state.
Unregister remains permitted for a suspended relay and preserves its suspension
timestamp and audit history.

Acceptance time must be at or after the actor's current state time and its
latest lifecycle or moderation event time. This prevents a clock regression
from silently moving state or audit history backward. Backend failures remain
wrapped in a stable internal class; public handlers must map that class without
exposing database details.

The repository does not authenticate, resolve, rate-limit, or authorize an
intent. A future handler may call it only after strict body and target parsing,
safe actor/key resolution, signature and actor binding, durable replay
reservation, moderation gates, and server acceptance-time capture.

## Administrative moderation transitions

`internal/storage.ModerationRepository` defines dormant operator-owned
`Suspend` and `Restore` transitions. Both require an existing retained relay
row. An unknown relay returns the internal absent class and creates no state or
audit record; this deliberately does not implement a preemptive blocklist.

Suspend changes only administrative state, suspension time, and state-update
time. It does not alter lifecycle state, registration metadata, or heartbeat
recency. Restore clears suspension without registering an unregistered relay.
Unregister remains permitted while suspended and continues to preserve the
administrative decision.

Each accepted operator decision records one private event, including a repeated
decision that leaves state unchanged. Applied and unchanged outcomes are closed
internal vocabulary rather than version 1 public protocol outcomes. A changed
state and its event commit in one immediate transaction; an event failure rolls
the state change back.

The audit record stores the canonical relay actor, action, server acceptance
time, a 128-byte maximum moderator identifier, and a 64-byte maximum reason
code. Moderator identifiers start with an ASCII alphanumeric character and
otherwise permit only ASCII alphanumerics plus `@._:+-`. Reason codes start
with a lowercase ASCII letter and otherwise permit lowercase letters, digits,
underscore, and hyphen. They are classification tokens, not free-form notes.
Neither value may appear in public responses or listings.

There is no moderation HTTP endpoint, operator CLI, or runtime wiring in this
tranche. See `docs/MODERATION.md` for the reviewed boundary and the remaining
operator-surface work.

## Durable replay reservations

`internal/storage/sqlite.RFC9421ReplayStore` implements the existing version 1
replay interface without changing its cryptographic contract. It accepts only
the opaque 32-byte SHA-256 replay key derived by the verifier and never receives
or stores a raw key ID, nonce, signature, actor, or request body.

Reservation uses an immediate transaction. An expired copy of the same key is
deleted first, a fixed batch of other expired rows is pruned, and the new row
is inserted with primary-key conflict suppression. Exactly one concurrent
caller can reserve an available key; other callers receive a duplicate result.
The reservation survives process restart and independent connections to the
same local database.

The store records its own current Unix time and requires expiry to be later
than that time but no more than the version 1 ten-minute replay TTL. Expiry is
inclusive for cleanup: a row whose expiry equals current time may be replaced.
Each reservation prunes at most 256 unrelated expired rows. The explicit
cleanup method requires a positive caller-selected bound no greater than 4096.
Cleanup and insertion share one transaction, so an insertion failure restores
any rows selected for cleanup.

Expired rows may remain harmlessly when no requests or maintenance calls occur;
they never become valid again. Before handlers are enabled, the dormant
admission policy must be composed with a bounded maintenance schedule so
sustained unique traffic cannot create an operational storage backlog. The
store remains dormant and is not passed to the request verifier in this
tranche.

## Migrations

Migrations are embedded into the binary and applied in version order within an
immediate transaction before the HTTP listener starts. A failed migration
rolls back as a unit and aborts startup.

The migration table records a SHA-256 digest of each embedded SQL file. The
migrator fails closed when an applied migration has changed, history has a gap,
or the database is newer than the binary. Once released, a migration is
immutable: schema changes require a new consecutively numbered migration and
upgrade tests from every supported version.

## Backup and recovery boundary

Before production deployment, the deployment runbook must specify a local,
encrypted backup target, retention, restore testing, and acceptable recovery
point. A backup must use SQLite's online backup API from a compatible tool, or
stop all writers and copy the database together with any `-wal` and `-shm`
files. Copying only the main file while the service is writing is not a valid
backup.

Before upgrading, take and verify a backup. Schema downgrades are not automatic:
rollback after a migration requires restoring the pre-upgrade backup with the
matching binary. Database opening or migration failure prevents the listener
from starting. After startup, `/healthz` remains a process-liveness signal and
`/readyz` fails when the database dependency is unavailable.
