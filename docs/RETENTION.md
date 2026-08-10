# Inactive-record retention

Roadmap Tranche 16 adds a deliberately narrow **purge** policy for durable
inactive relay state. Purge is irreversible. It is not the 30-day **prune**
transition: soft pruning remains reversible, keeps the relay row and history,
and is used by the health/public-visibility lifecycle.

No public HTTP request and no background scheduler can start hard retention.
The initial implementation is local-administrator-only. A positive policy says
which inactive rows are old enough to purge; an operator must still invoke the
local purge command with a verified pre-retention backup.

## Threat model

Hard retention is designed around accidental or stale destructive maintenance,
not around a malicious administrator who already has arbitrary database and
filesystem write access. The main failure cases are:

- an accidental nonzero policy: `0` remains the default and purge refuses to run
  at zero;
- a stale candidate racing a register, unregister, suspend, restore, or other
  lifecycle/moderation decision: row/update/event versions are rechecked under
  the immediate write transaction;
- an interrupted purge: lifecycle-event deletion, relay deletion, trigger
  recreation, and aggregate run checkpoint commit in one transaction;
- loss of audit evidence after a committed batch: the private run row exists
  before scanning and each committed batch checkpoints it atomically;
- a wrong, corrupt, insecure, or lineage-mismatched backup: the command checks
  file type/mode, schema, `quick_check`, persistent database identity, and digest
  before confirmation;
- accidental public triggering: no hard-retention HTTP route or scheduler exists;
  and
- assuming logical deletion immediately shrinks SQLite files: checkpoint/VACUUM
  remain separate explicit operator maintenance.

The database identity proves **lineage**, not backup freshness. The command
cannot infer that no accepted write occurred after an otherwise valid backup was
created. Operators must therefore create and restore-test the pre-retention
backup immediately before a production retention trial and preserve that
operational evidence. The purge command re-verifies lineage, schema, integrity,
security, and digest; it does not claim to prove snapshot freshness.

## Configuration contract

`DIRECTORY_INACTIVE_RETENTION_DAYS` is the only setting that authorizes durable
inactive-record deletion.

- unset or `0`: indefinite retention; purge execution is disabled;
- `1`: an eligible inactive transition becomes purgeable after one complete
  24-hour day;
- `365`: an eligible inactive transition becomes purgeable after 365 complete
  24-hour days;
- values above `36500` are rejected as unreasonably large; and
- negative, fractional, signed (`+1`), leading-zero (`01`), overflowing, and
  nonnumeric values are rejected.

The cutoff is inclusive: `inactive_at_unix <= observed_at - days*86400` is
eligible. The age starts at the most recent transition to the current inactive
state, not at the last healthy heartbeat. Unregistered rows use
`unregistered_at_unix`; soft-pruned rows use `pruned_at_unix`.

Changing a positive setting back to `0` prevents future purge commands from
running. It cannot reconstruct rows already deleted.

## Eligibility and moderation boundary

A row is a purge candidate only when all of these are true at candidate-read
**and** destructive-transaction time:

1. lifecycle state is `unregistered` or `pruned`;
2. administrative state is `active`;
3. the authoritative inactive timestamp is at or before the configured cutoff;
4. the relay row has not changed since the candidate was read; and
5. no new lifecycle or moderation event has been accepted since the candidate
   was read.

Registered rows are never candidates. Suspended rows are never candidates,
even when unregistered or pruned: automatic deletion must not erase an active
moderation decision.

Candidate snapshots include the row update time plus the latest lifecycle-event
and moderation-event IDs. The purge transaction rereads all of them under the
same immediate write lock. A register, repeated unregister, suspend, restore,
or other accepted lifecycle/moderation decision therefore makes an old
candidate skip rather than delete. A later maintenance run may reconsider the
new state from scratch.

After an active inactive relay is purged, no retained relay row remains. If the
actor later registers again, enrollment treats it as a never-accepted actor and
the current enrollment policy applies.

## Data-class consequences

| Data class | Tranche 16 behavior | Restoration consequence |
| --- | --- | --- |
| `relays` row | Eligible active inactive row is deleted | Identity/lifecycle metadata is recovered only from backup; a later register is first-time enrollment |
| `relay_events` | All lifecycle events for the purged actor are deleted in the same batch transaction | Lifecycle history is recovered only from backup |
| `moderation_events` | Never deleted by inactive retention | Private historical moderation evidence remains even after an eligible active relay row is purged |
| `retention_runs` | Guarded aggregate run audit; no relay identity list | Running rows retain committed checkpoints; finalized rows are immutable local evidence |
| `retention_metadata` | Persistent random database identity and policy version remain | Used to prove that a supplied backup belongs to this database |
| `replay_reservations` | Unchanged; independent protocol-bounded ten-minute expiry | Hard retention cannot extend or weaken replay behavior |
| enrollment policy/events | Unchanged | Enrollment history and current policy remain intact |
| public JSON/HTML projection | Unchanged | Purged rows are absent because no relay row exists; public filtering does not depend on purge timing |

The migration keeps lifecycle events append-only normally. A purge batch drops
the `relay_events_no_delete` trigger only inside its immediate transaction,
deletes the revalidated actors' lifecycle events and relay rows, recreates the
trigger, then commits. SQLite transactional DDL means any error or cancellation
rolls back both the deletions and the temporary trigger removal.

Private moderation events are intentionally outside that delete scope and keep
their append-only triggers at all times.

## Bounded execution and restart behavior

Candidate reads use stable `(inactive_at_unix, relay_actor)` keyset ordering and
the `relays_retention_candidates_idx` index. Dedicated actor/event-ID indexes
bound the latest lifecycle/moderation decision snapshots used for stale-candidate
revalidation. One page is at most 100 candidates;
one command scans at most 1,000 candidates. A one-row lookahead determines
whether more work remains.

A private `retention_runs` row is created with outcome `running` before any
destructive scan. Every successfully committed purge page updates aggregate
scanned/purged/skipped/event/batch counts in that same transaction. A crash can
therefore leave a `running` row, but it cannot leave a committed deletion without
durable run/checkpoint evidence. A restart needs no persisted relay-identity
cursor: a new run rescans from the beginning, and rows already deleted are absent.
Every candidate is transactionally revalidated before deletion, so retry is
idempotent. Normal completion, cancellation, or handled failure finalizes the run
row, after which database guards make it immutable and nondeletable.

Dry-run output and the private retention-run audit contain aggregate counts and
oldest/newest inactive timestamps, never relay identity lists. The run audit
also records policy version, retention days, observation/cutoff times, batch and
purge counts, outcome, truncation state, and the verified backup SHA-256.

## Verified backup gate

A positive production policy must not be activated until the operator has made
and restore-tested a fresh pre-retention SQLite backup. The destructive command imposes a
second, code-enforced gate: every purge invocation requires `--backup PATH` and
verifies that file before asking for destructive confirmation.

The purge command itself will not create or migrate the target database. The
configured database must already exist at the current schema through the same
read-only readiness boundary used by local inspection commands.

The supplied backup must:

- be an absolute, nonsymlink, regular owner-only SQLite file;
- be a **standalone** consistent backup with no `-wal`, `-shm`, or rollback
  `-journal` sidecar, preferably produced with SQLite's online backup API while
  the service is running;
- contain the current schema and the same 16-byte `retention_metadata` database
  identity as the live database; and
- pass `PRAGMA quick_check`.

The command then records the backup file's lowercase SHA-256 in the private run
audit. A backup from another directory database, a pre-retention schema, an
insecure file, or a corrupt database is rejected before confirmation and before
any destructive transaction.

A practical pre-activation sequence is:

```sh
# 1. Deploy/upgrade the binary with retention still disabled.
export DIRECTORY_INACTIVE_RETENTION_DAYS=0

# 2. Produce a standalone SQLite online backup to an owner-only local path.
#    Example when the sqlite3 CLI is available:
sqlite3 "$DIRECTORY_DATABASE_PATH" ".backup '/secure/backups/directory-pre-retention.sqlite'"
chmod 0600 /secure/backups/directory-pre-retention.sqlite

# 3. Restore that backup to an isolated test path and verify it with the same
#    binary/runbook before changing production policy.

# 4. Inspect the proposed policy without writes.
DIRECTORY_INACTIVE_RETENTION_DAYS=365 \
  activity-relay-directory admin retention dry-run --format json

# 5. Only after the backup/restore evidence is accepted, activate the positive
#    policy in the operator-managed service configuration.
```

The purge command verifies the backup before presenting destructive confirmation
and verifies it again immediately after confirmation, requiring the same digest
both times. It also re-verifies the backup even if the operator already tested it:

```sh
activity-relay-directory admin retention purge \
  --backup /secure/backups/directory-pre-retention.sqlite
```

Without `--yes`, the operator must type the exact policy-specific phrase such as
`PURGE 365`. `--yes` is available only for separately reviewed automation; it
does not bypass backup verification.

## Checkpoint and VACUUM

Deleting rows makes SQLite pages reusable; it does **not** promise an immediate
smaller main database file. Hard retention never runs `VACUUM` or a manual WAL
checkpoint in an HTTP request or purge batch.

If physical compaction is needed, treat it as separate operator maintenance:

1. complete and retain a fresh verified backup;
2. stop normal writers or otherwise enter the documented single-host
   maintenance window;
3. run a deliberate WAL checkpoint if required by the chosen SQLite procedure;
4. run `VACUUM` only with enough free filesystem space for SQLite's temporary
   rewrite; and
5. restart and re-run readiness/integrity checks.

Database-size warning/refusal policy is Roadmap Tranche 17. Tranche 16 must not
silently change retention or delete extra records merely because the SQLite file
is large.
