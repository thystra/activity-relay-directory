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

Explicitly enabled lifecycle handlers use this database for durable replay and
audited register, heartbeat, and unregister transitions. They remain disabled
together by default. No public listing or operator HTTP handler writes to the
database, and no public request can trigger pruning maintenance.

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

## Schema through version 5

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

Migration 3 adds the singleton `directory_policy` row, initialized with
enrollment closed, and private append-only `enrollment_events`. Upgrading does
not alter any retained relay or earlier audit row. Each local open or close
decision records its bounded operator token and server acceptance time, even
when the requested state is already current; policy and event commit in one
immediate transaction. Regressing decision time is rejected.

Migration 4 rebuilds `relays` with a required `last_seen_at_unix` column and the
composite health-projection index `(lifecycle_state, administrative_state,
last_seen_at_unix, relay_actor)`. Backfill is deterministic: it takes the
maximum of first registration, retained heartbeat time, and accepted register
or heartbeat lifecycle-event time. Unregister and moderation events do not make
a relay appear more recently seen. The migration is transactional and leaves
the prior schema and data intact on failure.

Migration 5 adds the explicit `pruned` lifecycle state, nullable
`pruned_at_unix`, and append-only `relay_pruned` event. It transactionally
rebuilds relay and lifecycle-event tables while preserving relay rows, event IDs,
moderation history, enrollment state, and replay reservations. Constraints bind
pruned state to its timestamp and require pruning time to be at or after the
retained last-seen value. The migration recreates the health index and adds
`(lifecycle_state, last_seen_at_unix, relay_actor)` for bounded candidate scans;
a late index-creation failure rolls the entire migration back to version 4.

The relay row retains the first accepted registration timestamp. Unregister is
a lifecycle transition, not a hard deletion, and administrative suspension is
independent of lifecycle state. Database constraints reject contradictory
state and timestamp combinations.

Audit events intentionally have no foreign key to the current relay row. This
permits an idempotent `unregister_absent` event and preserves history across
later state transitions. Moderation events likewise remain independently
retained. Updates and deletes are rejected by database triggers during normal
operation. Hard retention uses the separately reviewed version 6 delete scope
described below: only `relay_events_no_delete` is dropped and recreated inside
the same immediate purge transaction rather than bypassing append-only
protection generally.

The schema contains no connected-site identities, followers, user identities,
raw request bodies, raw nonces, or signing key IDs.

## Relay state transitions

`internal/storage.RelayRepository` defines backend-neutral register, heartbeat,
and unregister operations at a server-owned acceptance time. The SQLite
implementation validates canonical bounded identities again at the storage
boundary and applies each state change plus its append-only event within one
immediate transaction.

Register first reads enrollment within the same immediate transaction used to
create state. A never-seen actor is rejected while enrollment is closed, so a
concurrent close cannot race a first insert. Retained actors are independent of
the current enrollment setting. Register has these outcomes:

- a never-registered canonical actor becomes `created` and administratively
  active;
- an already registered actor with identical metadata is `unchanged`, leaving
  its state-change timestamp untouched while refreshing `last_seen_at_unix`
  and recording the accepted intent; and
- a retained unregistered or pruned actor becomes `updated`, preserving its
  original registration timestamp while clearing the prior lifecycle timestamp
  and old-heartbeat recency.

Administrative suspension blocks register without clearing suspension.
Heartbeat requires a registered, administratively active relay and records the
server acceptance time as `last_seen_at_unix`, last heartbeat, and state update
time. New and restored registrations also set last seen to their acceptance
time. Unregister leaves the last accepted register-or-heartbeat time retained.

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
intent. Enabled handlers call it only after strict body and target parsing,
safe actor/key resolution, signature and actor binding, durable replay
reservation, actor admission, and server acceptance-time capture.

## Health projection

`internal/storage.HealthProjectionRepository` exposes a read-only bounded page
for later maintenance and public adapters. A query supplies one captured server
observation time and a keyset cursor ordered by `last_seen_at_unix` and canonical
relay actor. The SQLite query uses the complete health-projection index, limits
each page to at most 100 relays plus one lookahead row, filters to active
registered relays before decoding, and performs no writes.

`storage.ClassifyHealth` applies the fixed version 1 windows:

- zero through exactly 36 hours: `healthy`;
- after 36 hours but before 7 days: `stale`;
- exactly 7 days through before 30 days: `dead`; and
- exactly 30 days or more: `prune`.

A `last_seen_at_unix` later than the captured observation time fails the whole
read closed instead of producing a younger state. Administrative suspension and
explicit unregister therefore win before health classification. The internal
projection returns `prune` state to private maintenance. The public JSON listing
uses a separate bounded repository query over the same composite index and
enforces `last_seen_at_unix > observed_at - 30 days` together with registered
and administratively active state before rows reach presentation. Thus exactly
30-day-old, pruned, unregistered, and suspended rows are excluded regardless of
asynchronous maintenance lag. Listing reads remain read-only and delete no rows.

## Reversible soft pruning

`internal/storage.PruningRepository` exposes a bounded private candidate read and
one transactionally revalidated transition. Candidate pages use the pruning
index, include both active and suspended registered rows, start at the exact
30-day health boundary, and contain at most 100 rows plus one lookahead. The
coordinator captures one observation time and processes at most 1,000 candidates
per run. It validates forward cursor progress, honors cancellation between
transitions, and never issues SQL deletion.

`SoftPrune` begins an immediate transaction, rereads the retained relay, and
confirms that it is still registered with `last_seen_at_unix` at or before the
captured cutoff. A heartbeat, register, unregister, or later moderation decision
accepted before that check makes the captured attempt ineligible rather than
allowing a time-regressing transition. A successful transition writes lifecycle
`pruned`, `pruned_at_unix`, and one `relay_pruned` event atomically. Event failure
rolls the row change back. Repeated attempts return `already_pruned` without
another event.

Suspension is independent and remains stored on a pruned relay. A suspended
pruned relay cannot register until an operator restores it. Re-registration then
returns it to `registered`, clears `pruned_at_unix` and obsolete heartbeat
recency, and preserves first registration plus all lifecycle and moderation
audit rows.

The process scheduler is disabled unless
`DIRECTORY_SOFT_PRUNING_ENABLED=true`. Its interval defaults to `24h`, must be at
least `1h`, runs immediately after enabled startup and then waits one interval
after each completed run, so runs cannot overlap. The local command
`activity-relay-directory admin pruning dry-run` requires an existing
current-schema database, opens it through a single query-only connection, and
reads only one bounded page without creating the file or applying migrations. It
supports the same keyset cursor. Neither capability is exposed through HTTP.

## Enrollment administration

`internal/storage.EnrollmentRepository` exposes durable status and one
transactional open/close operation. The local binary surface is:

```text
activity-relay-directory admin enrollment status
activity-relay-directory admin enrollment open --operator ID
activity-relay-directory admin enrollment close --operator ID
```

Only `DIRECTORY_DATABASE_PATH` is required for these commands. Filesystem and
host access to the owner-only local database form the authorization boundary;
there is no HTTP endpoint. Operator IDs use the same bounded private token
grammar as moderator IDs. Command errors and public status never reveal the
operator token or database detail. SQLite immediate transactions and the
configured busy timeout permit this local command to serialize safely with the
running single-host service.

## Administrative moderation transitions

`internal/storage.ModerationRepository` defines operator-owned `Suspend` and
`Restore` transitions used by the local administrative CLI. Both require an
existing retained relay row. An unknown relay returns the internal absent class
and creates no state or audit record; this deliberately does not implement a
preemptive blocklist or a network administrative endpoint.

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

The reviewed adapter is the local
`activity-relay-directory admin suspend|restore|show|audit` CLI operating on the
same owner-only database. No moderation HTTP endpoint exists. See
`docs/MODERATION.md` for the authorization and private-audit boundary.

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

Expired rows may remain harmlessly between requests or maintenance calls; they
never become valid again. Enabled runtime wiring passes the store to the
verifier and runs a five-minute maintenance ticker that deletes at most 4096
expired rows per pass. Reservation still performs its independent 256-row
cleanup batch. Maintenance failure is logged internally and does not weaken
replay rejection.

## Migrations

Migrations are embedded into the binary and applied in version order within an
immediate transaction before the HTTP listener starts. A failed migration
rolls back as a unit and aborts startup.

The migration table records a SHA-256 digest of each embedded SQL file. The
migrator fails closed when an applied migration has changed, history has a gap,
or the database is newer than the binary. Once released, a migration is
immutable: schema changes require a new consecutively numbered migration and
upgrade tests from every supported version.

## Inactive retention and narrow delete scope

Schema version 6 adds `retention_metadata`, guarded aggregate `retention_runs`,
`relays_retention_candidates_idx`, and actor/event-ID indexes used to snapshot
latest lifecycle/moderation decisions without scanning full actor histories. It
does not delete any existing row during upgrade. The singleton retention metadata
row is guarded against update/delete; its persistent random database identity lets
the local purge command distinguish a backup of this database from a different
directory database.

Hard retention is local-admin-only and disabled by the default
`DIRECTORY_INACTIVE_RETENTION_DAYS=0`. Candidate reads select only active
unregistered/pruned rows from their unregister/prune timestamp. Destructive
batches snapshot and revalidate row update time plus latest lifecycle and
moderation event IDs before opening the narrow lifecycle-event delete scope. A
concurrent accepted lifecycle or moderation decision therefore skips the stale
candidate.

The normal `relay_events_no_delete` trigger is dropped and recreated only inside
the immediate purge transaction. Eligible lifecycle events and the relay row
commit together; any error or cancellation rolls back the deletions and trigger
DDL. `moderation_events`, enrollment events/policy, replay reservations, and the
retention-run audit row are outside the lifecycle-event delete scope. Each
committed purge batch updates that run row in the same transaction, so a crash
cannot leave a committed deletion without durable aggregate checkpoint evidence.
Finalization makes the run row immutable. See `docs/RETENTION.md` for
bounds, backup verification, restore consequences, and manual compaction.

## Schema version 7 and database growth state

Schema version 7 adds exactly one `storage_growth_state` singleton used for the
bounded warning/critical/hard/recovery notification state. It stores only state,
sample/transition times, physical-byte baseline, last successful alert metadata,
and one pending retry record. It contains no relay identity, moderation token,
or unbounded history, and a trigger prevents deletion of the singleton.

Writable startup computes SQLite `max_page_count` before any pending migration,
injects the effective ceiling into every migration-pool connection, and preflights
the current database-family pressure. Migration retains SQLite's default cache
spilling to avoid holding a database-scale dirty set in memory. After migration,
the pool is closed and reopened with the same per-connection page ceiling plus
`cache_spill=OFF` for steady-state service/admin mutations. The configured total
budget covers the main file plus `-wal` and `-shm`; the main-page cap reserves a
page-aligned planning envelope for WAL/control/migration work. Runtime samples
separately report `page_count`, `freelist_count`, used pages, allocated pages,
main/WAL/SHM bytes, and the configured total. Free-list pages are reusable and
reduce logical-use pressure without claiming a smaller physical file.

Every production runtime mutator uses a common write-admission lease before its
transaction. The lease remains held through commit or rollback so another
in-process writer cannot pass a stale sample concurrently. Replay reservation
and replay cleanup use the same boundary. Schema migrations remain a startup
operation: hard preflight refuses a pending migration, and SQLite's page cap
rolls back a near-limit migration that cannot allocate safely.

A five-minute passive checkpoint plus WAL autocheckpoint and journal-size policy
bounds ordinary retained sidecar growth; neither setting is an active-transaction
WAL quota. Steady-state `cache_spill=OFF` supplies the additional invariant used
by `docs/STORAGE-GROWTH.md` to derive its conservative runtime single-transaction
WAL/SHM filesystem planning bound. That formula intentionally excludes schema
migration, whose spill-enabled pool requires separately planned host free space.
At hard state current-schema data remains readable and `/healthz` stays live, but
readiness and mutating operations fail closed. See `docs/STORAGE-GROWTH.md`.

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

## Local moderation reads

The local moderation CLI uses the same owner-only database and does not create a
second persistence service. `show` performs one exact actor lookup. `audit`
uses a maximum page size of 100, queries at most one additional row, and follows
`moderation_events_actor_time_idx` with a `(recorded_at_unix,
moderation_event_id)` keyset cursor. These reads perform no cleanup or mutation.

State-changing local commands continue to use the existing immediate
transaction, five-second busy timeout, and atomic state-plus-event commit. The
CLI may run beside the one active same-host service, but SQLite must not be
shared across hosts and database/WAL permissions must not be broadened to create
an administrative group.
