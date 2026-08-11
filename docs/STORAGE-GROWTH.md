# Database growth guard

Roadmap Tranche 17 adds a **non-destructive** SQLite database-growth guard and
optional administrator notification. It does not enable hard retention, choose
a retention policy, delete relay records, or run `VACUUM`.

## Defaults and states

The managed database-family budget defaults to 1 GiB:

```text
DIRECTORY_DATABASE_MAX_BYTES=1073741824
DIRECTORY_DATABASE_WARNING_PERCENT=75
DIRECTORY_ADMIN_EMAIL=
DIRECTORY_MAIL_BACKEND=mail
DIRECTORY_MAIL_COMMAND=/usr/bin/mail
DIRECTORY_MAIL_TIMEOUT_SECONDS=30
```

`DIRECTORY_DATABASE_MAX_BYTES` is a positive bounded integer. The warning
percentage is configurable from 1 through 89. Critical and hard boundaries are
fixed at 90 and 100 percent. `DIRECTORY_ADMIN_EMAIL` is empty by default, so
email is disabled unless an operator explicitly supplies one or more validated
comma-separated recipients. Logging, sampling, write refusal, and readiness
behavior remain active when email is disabled.

The closed storage states are:

- `normal`;
- `warning` at the configured warning threshold (75% initially);
- `critical` at 90%; and
- `hard` at 100%.

Upward transitions are immediate. Downward transitions use a five-percentage-
point hysteresis against the state being left. This prevents a database near a
boundary from repeatedly sending recovery/threshold transitions.

## What is measured

The guard samples immediately at service startup, every five minutes, and
immediately before every admitted runtime database mutation. Each sample reads:

- SQLite `page_size`;
- `page_count`;
- `freelist_count`;
- the effective `max_page_count`;
- the SQLite main-file size;
- the `-wal` sidecar size, when present; and
- the `-shm` sidecar size, when present.

`used_pages = page_count - freelist_count`. Free-list pages therefore count as
**reusable logical capacity**, not fresh growth. This is important after a
positive inactive-retention run: deleting eligible rows can release pages for
reuse without shrinking the main SQLite file.

The reported pressure is the greater of:

1. used logical bytes relative to the reviewed main-page allocation limit; or
2. physical main + WAL + SHM bytes relative to the total configured budget.

Both logical and physical dimensions can independently trigger warning,
critical, or hard state.

## Main-page backstop and reserved headroom

On each writable open, the service computes SQLite `max_page_count` below the
total configured family budget and injects that pragma into the writable DSN.
SQLite treats `max_page_count` as a **connection-local** runtime setting, so the
DSN applies the same effective ceiling to every connection created by the
`database/sql` pool instead of setting one arbitrary pooled connection. Reserved
headroom is:

```text
min(12.5% of DIRECTORY_DATABASE_MAX_BYTES, 128 MiB)
```

with a minimum of four SQLite pages and safe page-size rounding. The main-page
limit is the remaining whole-page capacity. At the default 1 GiB budget and the
normal 4096-byte SQLite page size, the reviewed reservation is 128 MiB and the
main-page allocation limit is 896 MiB.

The **maximum application-reserved headroom is 128 MiB at the default budget**.
It is held outside the normal main-page allocation limit for WAL/checkpoint
activity, the bounded growth-state control row, and migration/runtime transaction
overhead. Smaller configured budgets reserve one eighth of their budget (subject
to the four-page minimum). `max_page_count` prevents a guarded writer connection
from allocating new main-file pages beyond the effective ceiling; already-free
pages may still be reused.

That 128 MiB is **reserved headroom, not the absolute active-transaction WAL
ceiling**. SQLite enables automatic dirty-page cache spilling by default, which
can write a page to the WAL before commit and then write the same page again if
it is modified later in that transaction. The steady-state guarded runtime pool
therefore sets `cache_spill=OFF` on every connection. The service does not invoke
an explicit mid-transaction cache flush. With automatic spilling disabled, dirty
application pages remain in the connection page cache until commit, so the
reviewed steady-state transaction can contribute at most one WAL frame per dirty
main-database page. SQLite WAL frames contain one database page plus a 24-byte
frame header. The conservative **steady-state runtime** transaction WAL bound
used for operator capacity planning is therefore:

```text
transaction_wal_bound =
    32 + effective_max_page_count * (page_size + 24)
```

The WAL-index shared-memory file grows in 32 KiB blocks: the first block indexes
4062 WAL frames and each later block indexes 4096. A conservative SHM bound for
the same transaction is:

```text
transaction_shm_bound =
    32768 * (1 + ceil(max(effective_max_page_count - 4062, 0) / 4096))
```

Because a steady-state pre-write sample must be below the configured hard family
budget before the transaction is admitted, a conservative filesystem planning
ceiling while that admitted runtime/admin transaction is in flight is:

```text
configured_max_bytes + transaction_wal_bound + transaction_shm_bound
```

At the default 1 GiB budget with a 4096-byte page and the normal 896 MiB desired
main-page cap (`effective_max_page_count=229376`), that conservative ceiling is
2,020,638,752 bytes (about 1.882 GiB). An older database whose existing allocation
already exceeds the desired 896 MiB cap can have a larger effective no-growth
ceiling; `admin storage status` reports that effective page count so operators can
recalculate the bound. With a 4096-byte page, a database family sampled just below
the 1 GiB hard boundary cannot have more than 262143 allocated main pages, yielding
a conservative ceiling of 2,155,900,936 bytes (about 2.008 GiB).

`wal_autocheckpoint` controls checkpoint cadence and `journal_size_limit` controls
retained WAL size after reset/checkpoint; neither is an active-transaction byte
quota. `cache_spill=OFF` is what makes the frame-count portion of the steady-state
bound enforceable. It can increase memory retained by a write transaction, so it
is used only after schema migration has completed; ordinary service/admin
mutation sizes remain bounded by their existing request, replay-cleanup, pruning,
and retention-batch limits.

Schema migration is intentionally different. The pre-runtime migration pool
keeps SQLite's default cache spilling enabled while applying the same
per-connection `max_page_count` no-new-main-page ceiling. Historical migrations
include whole-table copy/rebuild operations, so disabling spill there could
retain a database-scale dirty-page set in memory. The steady-state WAL/SHM formula
above therefore **does not bound migration WAL growth**. Upgrades remain a
separate operator-capacity event: hard-state preflight blocks a pending migration,
`max_page_count` bounds main-file allocation, and host filesystem monitoring/free
space must cover migration sidecar growth before deployment proceeds.

The guard therefore prevents unbounded *main-file* allocation, reports and blocks
on family pressure between steady-state transactions, and provides a bounded
single-transaction transient envelope for steady-state runtime/admin mutations
without pretending the 1 GiB policy is a hard filesystem quota at every instant. Host filesystem free-space
monitoring must leave room for both that runtime envelope and separately planned
migration work.

The steady-state SQLite connection policy sets:

- WAL mode;
- `cache_spill=OFF`;
- `wal_autocheckpoint=256` pages;
- `journal_size_limit=16777216` (16 MiB); and
- the existing short busy timeout and `synchronous=NORMAL` policy.

The migration pool uses the same WAL/page-limit policy except that cache spilling
remains enabled until migration completes and the pool is closed/reopened for
steady-state service.

The five-minute sampler performs only `PRAGMA wal_checkpoint(PASSIVE)`. It does
not use a truncating checkpoint in a request path and never runs `VACUUM`.
Public database reads are already bounded and short-lived so ordinary
checkpoints are not intentionally held open.

The reserved headroom is an application budget, **not a substitute for host
filesystem monitoring**. Operators must separately monitor filesystem free
space, inode availability, filesystem errors, snapshot growth, and any other
files sharing the volume.

## Write admission and hard-limit behavior

All runtime database mutations use one `WriteAdmission` boundary before their
transaction begins. The admitted lease is held until that transaction commits
or rolls back. The guarded paths include:

- register, heartbeat, and unregister;
- enrollment changes;
- suspend and restore;
- soft pruning;
- retention run creation, purge batches, and run finalization; and
- RFC 9421 replay reservation and bounded replay cleanup.

The guard serializes in-process mutation admission with sampling. SQLite remains
the cross-process single-writer authority for local administrative commands.
The writable steady-state service pool applies its `max_page_count` ceiling and
`cache_spill=OFF` policy on **every connection** through the DSN. Local mutating
admin commands open their own steady-state guarded pool and therefore apply the
same rules. Read-only inspection does not attempt to set connection-local write
pragmas; it reports the policy-effective ceiling derived from the configured
budget and current allocation. This per-connection backstop plus reserved family
headroom prevents a second same-host guarded writer from creating unbounded
main-file growth after a stale pre-write observation.

At `hard`:

- no growth guard path deletes data automatically;
- new domain/application database mutations are refused before their transaction;
- existing data remains intact;
- `/healthz` stays live;
- `/readyz` returns unavailable;
- enabled `GET`/`HEAD` public JSON and human directory reads remain available;
- local `admin storage` inspection remains available read-only; and
- recovery requires reducing logical/physical pressure or deliberately raising
  the configured budget after reviewing host capacity.

The only reviewed post-hard SQLite control/storage operations are:

1. an `UPDATE` of the pre-created singleton `storage_growth_state` row so the
   state transition and bounded mail/reminder retry metadata survive restart; and
2. `PRAGMA wal_checkpoint(PASSIVE)`, which may flush already-committed WAL pages
   but performs no new logical/domain mutation.

The singleton contains no relay/operator/audit identities, cannot be deleted, and
does not grant write admission to any domain repository. The runtime checkpoint
may never become `TRUNCATE`, `RESTART`, or `VACUUM`. Growth-control persistence or
checkpoint failure is logged and readiness remains/fails unavailable as applicable;
neither exception triggers destructive fallback.

A database already at the hard boundary may start on the current schema so the
process can remain live/readable and report not-ready. A pending schema upgrade
is not admitted from a hard pre-migration sample. A near-limit migration is
still protected by `max_page_count`; if SQLite cannot complete it within the
reserved allocation, its transaction rolls back and the prior schema remains
intact.

## Retention and physical compaction

`DIRECTORY_INACTIVE_RETENTION_DAYS=0` remains indefinite retention. The growth
guard never changes it. With a positive retention policy, a separately
confirmed, verified-backup-gated local purge may release logical pages. Those
free-list pages immediately reduce logical used-page pressure and are available
for later reuse.

Hard retention deliberately does not promise physical shrinkage. To make the
main file smaller, use the explicit offline/operator procedure in
[`RETENTION.md`](RETENTION.md): stop writers, preserve a verified backup,
checkpoint as appropriate, and run `VACUUM` only with adequate temporary free
space. Do not automate `VACUUM` as a response to a growth alert.

## Administrator notifications

When `DIRECTORY_ADMIN_EMAIL` is empty, no mail command is executed. When email
is explicitly enabled, startup requires:

- backend exactly `mail`;
- a clean absolute executable path;
- a configured path that resolves once at startup to a regular executable file;
- one through eight canonical recipients with no display names, whitespace,
  control characters, duplicates, or option-like leading `-`; and
- a timeout from 1 through 300 seconds (30 seconds by default).

The mailer executes the command directly with `exec.CommandContext`; no shell is
used. The fixed invocation is equivalent to:

```text
/usr/bin/mail -s "Activity-Relay-Directory storage <kind>" recipient ...
```

The message body is supplied on standard input. Subject/body sizes and captured
stdout/stderr are bounded. Mail failure is logged using a redacted class and
does not stop the service.

Alert state is one bounded singleton database row; it contains no relay identity
list or operator/audit detail. Notifications are scheduled for transitions into
warning, critical, hard-limit, and recovered states. A successful notification
suppresses another reminder for that state for 24 hours. A failed delivery uses
the bounded retry sequence 5 minutes, 15 minutes, 60 minutes, then no more than
once per 24 hours while it remains pending. Restart reloads this singleton so it
does not create a mail storm.

Alert messages contain only storage measurements, configured maximum, inactive
retention days, write-admission state, and a fixed remediation checklist.

### Container note

The stock minimal Alpine runtime image does **not** install or configure a mail
transport. Enabling `DIRECTORY_ADMIN_EMAIL` therefore requires an operator to
use a derived/custom image (or another reviewed deployment arrangement) that
explicitly installs and configures the selected local `mail` command. The
project never supplies a recipient, credentials, SMTP relay host, or automatic
notification enablement.

A future Debian package may recommend a suitable mail transport, but package
installation must likewise leave recipient, credentials, relay host, and alert
enablement entirely operator-controlled.

## Local storage commands

The local-only inspection surface is:

```text
activity-relay-directory admin storage status [--format human|json]
activity-relay-directory admin storage check [--format human|json]
activity-relay-directory admin storage test-alert [--format human|json]
```

`status` always reports the current bounded sample when inspection succeeds.
`check` reports the same data and uses exit status 0/3/4/5 for
normal/warning/critical/hard. Operational failure is exit 1 and command misuse
is exit 2. `test-alert` requires explicitly enabled email, sends one current
sample through the configured mailer, and does not consume or mutate persistent
transition/reminder state.

Human and JSON output include state, pressure, logical allocation/use/reuse,
physical family sizes, configured maximum, thresholds, retention setting, and
write-admission status. They do not include relay actors, moderation/audit
identities, database paths, SQL text, or mail credentials.
