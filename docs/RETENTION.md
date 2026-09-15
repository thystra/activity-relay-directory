# Inactive-record retention

Hard retention is a deliberately narrow **purge** policy for durable inactive
Directory state. Purge is irreversible. It is not the 30-day **prune**
transition: soft pruning remains reversible, keeps the relay lifecycle row and
history, and is used by the health/public-visibility lifecycle. Schema version 8
advances the hard-retention contract to policy version 2 so removed discovery
state and unowned observation state are covered without erasing private
discovery audit.

No public HTTP request and no background scheduler can start hard retention.
The initial implementation is local-administrator-only. A positive policy says
which inactive rows are old enough to purge; an operator must still invoke the
local purge command with a verified pre-retention backup.

### Reachability and reversible soft pruning

Hard retention remains independent from actor reachability. For reversible
soft pruning, however, 1.1 treats fresh current actor reachability as separate
evidence from lifecycle heartbeat recency. When background reachability is
enabled, automatic soft pruning waits for recent complete reachability coverage
and both candidate selection and the final pruning transaction protect a relay
whose current actor state is `reachable` with a success inside the fixed
six-hour window. See `docs/REACHABILITY.md`.


## Threat model

Hard retention is designed around accidental or stale destructive maintenance,
not around a malicious administrator who already has arbitrary database and
filesystem write access. The main failure cases are:

- an accidental nonzero policy: `0` remains the default and purge refuses to run
  at zero;
- a stale candidate racing a register, unregister, suspend, restore, discovery
  decision, or fresh reachability/RFC-evidence write: the applicable row/event
  versions plus the observation update version are rechecked under the immediate
  write transaction;
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
state, not at the last healthy heartbeat. Lifecycle candidates use
`unregistered_at_unix` or `pruned_at_unix`; discovery candidates use
`removed_at_unix`.

Changing a positive setting back to `0` prevents future purge commands from
running. It cannot reconstruct rows already deleted.

## Eligibility and moderation boundary

Policy version 2 has two primary candidate kinds. A **lifecycle** candidate must
be `unregistered` or `pruned`, administratively `active`, and old enough at both
candidate-read and destructive-transaction time. A **discovery** candidate must
already be `removed` and its `removed_at_unix` must be old enough. Registered or
suspended lifecycle rows and active discoveries are never primary candidates.

Lifecycle snapshots contain the row update time plus latest lifecycle and
moderation event IDs. Discovery snapshots contain the discovery-row update time
plus latest discovery event ID. Both snapshot the current monotonic `relay_observations.revision`, using `0`
when no observation exists. Every actor/inbox/RFC evidence write increments the
revision. The purge transaction rereads those values under the same immediate
write lock, so even a same-second fresh observation makes an old candidate skip
rather than delete. A later maintenance run may reconsider the new state from
scratch.

Deleting a lifecycle row does not delete a separately retained discovery row,
and deleting a removed discovery row does not delete separately retained
lifecycle state. `relay_observations` is shared evidence: after one primary row
is deleted, its observation is deleted only if no lifecycle **and** no discovery
row remains for that canonical actor. This rule also makes two same-actor
candidate kinds safe when they occur in one page or across a page boundary.

After the final retained identity row is purged, a future authenticated register
is first-time lifecycle enrollment again. Retained private moderation/discovery
audit does not itself authorize public inclusion or preserve an observation row.

## Data-class consequences

| Data class | Policy-version-2 behavior | Restoration consequence |
| --- | --- | --- |
| `relays` row | Eligible active inactive lifecycle row is deleted | Identity/lifecycle metadata is recovered only from backup; a later register is first-time lifecycle enrollment unless discovery independently remains |
| `relay_events` | All lifecycle events for a purged lifecycle actor are deleted in the same batch transaction | Lifecycle history is recovered only from backup |
| `relay_discoveries` | Eligible `removed` discovery row is deleted | Current discovery decision is recovered only from backup; active discovery is never a candidate |
| `discovery_events` | Never deleted by inactive retention | Private operator/source provenance remains append-only even after current discovery state is purged |
| `relay_observations` | Deleted only when a purged primary row leaves no lifecycle or discovery owner | Server-observed reachability/inbox/RFC evidence is recovered only from backup or future observations |
| `moderation_events` | Never deleted by inactive retention | Private historical moderation evidence remains after lifecycle state purge |
| `retention_runs` | Guarded aggregate run audit; historical policy 1 and current policy 2 rows remain | Running rows retain committed checkpoints; finalized rows are immutable local evidence |
| `retention_metadata` | Persistent random database identity remains while current policy version advances | Used to prove that a supplied backup belongs to this database |
| `replay_reservations` | Unchanged; independent protocol-bounded ten-minute expiry | Hard retention cannot extend or weaken replay behavior |
| enrollment policy/events | Unchanged | Enrollment history and current policy remain intact |
| public JSON/HTML projection | No direct mutation | Public eligibility is computed independently; hard purge only removes state already inactive under its own authority |

Historical policy-1 rows remain immutable audit evidence after migration. A
policy-1 row that was left `running` by an interrupted pre-upgrade process is
not eligible for policy-2 destructive batches or finalization; a new policy-2
run must be started for any later maintenance.

The migration keeps lifecycle and discovery events append-only normally. A purge
batch drops only `relay_events_no_delete`, and only inside its immediate
transaction, when lifecycle-event deletion is required. Discovery events and
moderation events keep their append-only triggers throughout hard retention.
SQLite transactional DDL means an error rolls back lifecycle deletions and the
temporary trigger change together.

## Bounded execution and restart behavior

Candidate reads merge two separately bounded indexed sources and use stable
`(inactive_at_unix, relay_actor, candidate_kind)` keyset ordering. Lifecycle
reads use `relays_retention_candidates_idx`; discovery reads use
`relay_discoveries_retention_candidates_idx`. Dedicated event-ID indexes bound
latest lifecycle/moderation/discovery decision snapshots, and the observation
row supplies its single update-version snapshot. Each source is limited to one
page plus lookahead before the merge; one returned page is at most 100
candidates and one command scans at most 1,000 candidates.

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
lifecycle/discovery/observation/event purge counts, outcome, truncation state,
and the verified backup SHA-256. Local JSON output is schema
`activity-relay-directory.retention-admin.v2`; it remains identity-free.

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
