# Implementation roadmap

This roadmap starts from commit `0e70c75`, where the directory server has
default-off authenticated register, heartbeat, and unregister handlers backed
by bounded admission, safe actor-key resolution, durable replay protection, and
audited SQLite lifecycle state.

The remaining work is intentionally split into reviewable tranches. A completed
source tranche is not automatically deployed, enabled, released, or adopted by
Activity-Relay. Those remain separate operator-controlled gates.

## Fixed boundaries

Every tranche must preserve these project invariants:

- directory participation remains opt-in on both the server and relay;
- the directory stores relay-instance metadata, never connected-site, follower,
  membership, or user identities;
- only server acceptance time determines directory recency;
- moderation overrides automated health and public visibility;
- pruning is a reversible lifecycle transition, not hard deletion;
- enrollment defaults closed and changes only through a local administrative
  CLI; closing it never mutates a previously accepted relay;
- `DIRECTORY_INACTIVE_RETENTION_DAYS` is the only configuration allowed to
  authorize deletion of inactive durable records; it defaults to `0` for
  indefinite retention, and any positive value requires a verified backup
  before activation;
- SQLite supports one active directory process on one host only;
- public output never contains moderation actors, reason codes, audit records,
  signature material, nonces, resolver details, or storage errors; and
- cross-repository protocol changes require synchronized fixtures and a stated
  compatibility matrix.

## Dependency order

```text
Activity-Relay client contract
        |
        v
manual commands and safe unregister --> scheduler and retry
                                              |
                                              v
                                      health projection --> bounded soft pruning
                                              |                     |
operator moderation CLI ----------------------+---------------------+
                                                                    v
                                                               public views
                                                                    |
                                                                    v
                                      retention and storage-growth safeguards

All completed source tranches --> integration soak --> release-candidate gates
```

Health needs real authenticated heartbeats to validate its operational model.
Public views need both health and moderation filtering. Retention follows soft
pruning so expiry never becomes an accidental substitute for lifecycle state.

"Hard retention" means permanently deleting old durable database rows or audit
events after a retention period. It is distinct from soft pruning: a pruned
relay is hidden but remains known, can preserve a suspension, and can safely
return. Hard-deleted data is recoverable only from backup, and the directory
loses the deleted acceptance and lifecycle history. Protocol replay-row expiry
is short-lived housekeeping and is not inactive-record retention.

| Tranche | Primary repository | Result |
| --- | --- | --- |
| 8 | Both | Directory/client compatibility contract and conformance fixtures |
| 9 | Activity-Relay | Manual lifecycle commands and safe config mutation |
| 10 | Activity-Relay | Automatic register/heartbeat scheduling and retry |
| 11 | Directory | Local operator moderation CLI and private audit reads |
| 12 | Directory | Deterministic health projection |
| 13 | Directory | Bounded reversible soft pruning |
| 14 | Directory | Versioned public JSON view |
| 15 | Directory | Human-readable view from the same projection |
| 16 | Directory | Configurable inactive-record retention |
| 17 | Directory | Database growth guard and administrator notification |
| 18 | Both | Staging soak, packaging, and release-candidate evidence |

Repository history also contains an operational CI change described as “Tranche
14: native Forgejo CI.” That infrastructure milestone is outside this product
feature roadmap and does not renumber the product tranches above.

## Tranche 8: Cross-repository client contract

Completed (2026-08-05): the cross-repository contract is implemented in both
repositories. It remains undeployed and inactive. The pre-release lifecycle
graph setting is now `DIRECTORY_LIFECYCLE_ENABLED`; the retired ambiguous name
is rejected.

Repositories: both projects.

Freeze the server/client recovery and operational-control contract before adding
runtime client behavior. Then define a directory-specific Activity-Relay client
package without activating it. Reuse the relay's existing RSA actor identity,
but do not reuse the ActivityPub RFC 9421 profile unchanged: directory requests
require their own application tag, expiry, nonce, exact targets, and response
vocabulary.

Deliverables:

- split "lifecycle handlers available" from "previously unseen registrations
  accepted" with a persisted enrollment policy controlled by
  `activity-relay-directory admin enrollment status|open|close`;
- keep lifecycle graph construction default-off in process configuration, keep
  enrollment closed by default in durable state, and report their distinct
  states through the status document;
- define an already accepted relay as an actor with a retained relay row, so
  closing enrollment rejects only a never-seen actor and does not mutate,
  suspend, unregister, or prune any accepted relay;
- permit accepted relays to register updated metadata, re-register from a
  retained unregistered or pruned state, heartbeat, and unregister while
  enrollment is closed, subject to moderation and the existing security gates;
- record each accepted open or close decision in a private bounded
  administrative audit event without exposing operator data publicly;
- add a stable machine-readable `relay_not_registered` heartbeat result so a
  client may perform one bounded register reconciliation instead of guessing
  from the generic `invalid_request` class;
- use `DIRECTORY_LIFECYCLE_ENABLED` for lifecycle graph construction, reject
  the retired pre-release `DIRECTORY_REGISTRATION_ENABLED` name, and update
  configuration fixtures as one reviewed compatibility change;
- strict models for the version 1 register, heartbeat, unregister, success, and
  error documents;
- canonical HTTPS directory-origin validation with no credentials, path,
  query, or fragment;
- exact RFC 9530 digest and RFC 9421 signing for the directory profile;
- a fresh cryptographic nonce and fresh signature for every network attempt;
- bounded response bodies, deadlines, redirect refusal, media-type checks, and
  closed response-code parsing;
- a bounded list of independently enabled directory origins, absent and
  disabled by default; and
- byte-compatible fixtures shared with the directory server, including one
  complete request generated by Activity-Relay and accepted by the directory
  verifier.

Acceptance gates:

- network-free tests prove exact signature components, authority, target, tag,
  algorithm, created/expiry window, digest bytes, and actor identity;
- closing enrollment changes no relay row, and an accepted relay can register,
  heartbeat, and unregister while a never-seen actor receives the fixed
  closed-enrollment result;
- enrollment CLI transitions are transactional, idempotent, locally
  authorized, auditable, and safe alongside the running SQLite service;
- the client performs at most one register reconciliation for the explicit
  not-registered result and never treats generic invalid input as absence;
- malformed or oversized responses cannot reach logs as raw content;
- unknown protocol versions, operations, outcomes, and error codes fail closed;
- redirects are never followed for signed lifecycle POSTs; and
- existing ActivityPub delivery signing and default configuration are
  unchanged.

## Tranche 9: Manual Activity-Relay directory commands

Repository: `thystra/Activity-Relay`.

Add explicit operator commands before adding automatic scheduling. The target
surface is `relay directory status`, `register`, `heartbeat`, `unregister`, and
`sync`, with final command names frozen alongside documentation and shell
completion tests.

Configuration and unregister semantics are a hard requirement:

1. a directory entry must be explicitly enabled before register or heartbeat;
2. `unregister` first atomically disables the selected entry in the
   operator-owned configuration, using a same-directory temporary file,
   preserved ownership/mode, `fsync`, rename, and a recoverable backup;
3. only after that durable local disable succeeds may it send the signed remote
   unregister request;
4. a remote failure leaves the entry disabled, returns a nonzero result, and
   prints bounded retry guidance, so restart cannot silently re-register it;
5. after remote success, the entry remains disabled by default and an explicit
   option may remove it from the configuration; and
6. environment-only configuration must refuse automatic mutation unless the
   operator explicitly acknowledges that the external configuration source must
   be disabled separately.

The configuration editor must use YAML structure rather than text replacement,
reject symlinks and unexpected file types, preserve unrelated keys, and test
comments and multi-directory documents. It must never rewrite `actor.pem`.

Acceptance gates:

- register, heartbeat, and unregister pass against an in-process directory
  fixture with the real verifier and isolated SQLite database;
- every retry uses a fresh nonce while preserving idempotent operation intent;
- authentication and policy errors are not retried automatically;
- HTTP 429 honors bounded `Retry-After`; transient failures use bounded backoff;
- a crash or injected failure at every configuration-update stage either leaves
  the original file intact or leaves the directory entry durably disabled; and
- restarting Activity-Relay after unregister cannot re-register the entry.

## Tranche 10: Automatic registration and heartbeat scheduling

Repository: `thystra/Activity-Relay`.

The API-server process, never every worker, owns directory scheduling. A
Redis-backed lease prevents duplicate schedulers when multiple API processes
share relay state. Directory unavailability must not block ActivityPub service
startup or delivery.

Deliverables:

- startup reconciliation that registers enabled entries only when needed;
- one heartbeat per enabled directory on a nominal daily interval with bounded,
  stable jitter per relay and directory;
- persisted per-directory last success, next attempt, last closed outcome, and
  bounded diagnostic class, without storing raw signatures or nonces;
- bounded exponential retry with jitter, fresh signing per attempt, and
  `Retry-After` support;
- lease acquisition, renewal, loss, and clean shutdown semantics;
- a durable local suppression marker consulted before every scheduled action,
  with unregister taking the same per-directory lease before disabling config
  and sending the remote request;
- operator-visible status that distinguishes configured, registered,
  heartbeat-current, retrying, disabled, and unregister-pending states; and
- metrics that use bounded labels and never place relay or directory URLs in
  label values.

Acceptance gates:

- fake-clock tests cover exact interval and jitter boundaries, restart, clock
  regression, lease contention, lease loss, cancellation, and retry classes;
- two API processes produce at most one scheduled operation per due slot;
- workers never schedule directory traffic;
- disabling an entry cancels future register and heartbeat work in a currently
  running API process as well as after restart;
- an unregister command racing the scheduler cannot be followed by a new
  automatic registration; and
- an explicit not-registered heartbeat result triggers at most one register
  reconciliation, while authentication, suspension, and malformed-request
  results do not; and
- a multi-day accelerated soak shows bounded Redis state and no nonce reuse.

## Tranche 11: Local operator moderation access

Completed (2026-08-05): the local moderation command surface, private state
reads, and bounded audit pagination are implemented in source. They remain
undeployed and provide no network administrative endpoint.

Repository: `thystra/activity-relay-directory`.

Expose the existing moderation repository through a local administrative CLI,
not a public HTTP endpoint. Initial authorization is operating-system access to
the database and executable. A web panel, administrative HTTP API, bearer-token
scheme, and network administration framework are explicitly out of current
scope. Keep command handling separate from the repository so a future effort
can add another adapter, but do not add routes, dependencies, authentication
scaffolding, or placeholder API hooks now.

Implemented commands:

- `activity-relay-directory admin suspend`;
- `activity-relay-directory admin restore`;
- `activity-relay-directory admin show`; and
- `activity-relay-directory admin audit` with bounded keyset pagination.

Deliverables:

- strict actor, moderator-token, reason-code, and database-path parsing;
- explicit confirmation for state-changing commands, plus a noninteractive
  flag suitable for reviewed automation;
- stable human and JSON output modes with fixed exit classes;
- bounded audit reads that keep moderator and reason fields private;
- safe concurrent use with the running single-host SQLite service; and
- packaging and container instructions that do not broaden database access or
  automatically grant an administrative group.

Acceptance gates:

- absent, active, suspended, restored, unregistered, idempotent, concurrent,
  canceled, and regressing-time cases are covered;
- state and audit event remain one transaction;
- public status, lifecycle errors, and future listing projections contain none
  of the private moderation fields; and
- no preemptive blocklist is implied. If one is wanted later, it receives its
  own schema, threat model, and review.

## Tranche 12: Health-state projection

Completed (2026-08-06): the indexed server-owned last-seen value, deterministic
migration backfill, fixed version 1 classifier, and bounded private projection
read are implemented in source. They remain undeployed and expose no public
listing or pruning transition.

Repository: `thystra/activity-relay-directory`.

Add a read model with one indexed server-owned `last_seen_at` value maintained
by accepted register and heartbeat operations. Migration backfill must be
deterministic from existing server-owned lifecycle data.

The fixed version 1 windows are:

| State | Age since `last_seen_at` |
| --- | --- |
| `healthy` | 0 through and including 36 hours |
| `stale` | over 36 hours but less than 7 days |
| `dead` | 7 days through, but not including, 30 days |
| prune required | 30 days or greater |

These are fixed version 1 boundaries: exactly 7 days is dead and exactly 30
days requires pruning. Changing them requires an explicit policy and fixture
review rather than an unchecked runtime value. One captured server time
classifies an entire query or maintenance batch.

Administrative and lifecycle state remain separate from health:

- suspended relays are excluded from public projections regardless of recency;
- explicitly unregistered and automatically pruned relays are excluded;
- a newly accepted registration is healthy even before its first heartbeat;
- an unchanged authenticated registration refreshes `last_seen_at`; and
- future or regressing timestamps fail closed rather than producing a younger
  public state.

Acceptance gates:

- migration tests cover fresh, version-2 upgrade, drift, rollback, and backfill;
- fake-clock tests cover zero, exactly 36 hours, immediately after 36 hours,
  immediately before and exactly 7 days, immediately before and exactly 30
  days, negative age, and nondecreasing time;
- health reads are bounded, indexed, and do not write on ordinary requests;
- suspension wins over health in every public projection; and
- accelerated client/server tests exercise healthy, stale, and dead states.

## Tranche 13: Bounded soft pruning

Completed in source (2026-08-06): bounded reversible soft pruning, default-off
scheduled maintenance, local dry-run inspection, and Node-24-compatible Docker
build actions are implemented. The feature remains undeployed and inactive by
default.

Repository: `thystra/activity-relay-directory`.

Pruning is a reversible system-owned lifecycle transition. It removes an
expired relay from public eligibility while retaining its row, moderation state,
and audit history. It is not retention and never executes SQL hard deletion.

Deliverables:

- an explicit pruned lifecycle state and server-owned `pruned_at` timestamp;
- a distinct append-only `relay_pruned` event;
- a repository transition that revalidates eligibility within its transaction;
- bounded keyset batches, a fixed maximum per run, cancellation, and a minimum
  interval;
- automatic scheduled maintenance once the health/pruning tranche is deployed,
  plus an administrative dry-run command; and
- re-registration that returns a pruned relay to registered state while
  preserving first-registration and moderation history.

Suspension remains independent: a suspended relay may be soft-pruned while its
suspension is preserved, and cannot re-register until explicitly restored.

Acceptance gates:

- exact threshold, race-with-heartbeat, race-with-register, suspension,
  restart, rollback, cancellation, and batch-limit tests pass;
- a heartbeat or register accepted before transactional eligibility checking
  prevents the prune;
- repeated pruning is idempotent and does not duplicate state changes; and
- public projections independently exclude any relay at or beyond 30 days even
  when a just-due pruning batch has not committed its soft transition; and
- no public request can trigger an unbounded scan or maintenance run.

## Tranche 14: Public JSON directory view

Completed in source (2026-08-10): the default-off versioned JSON listing,
indexed public-eligibility query, opaque bounded pagination, cache validator,
and independent public admission ceiling are implemented. It remains
undeployed and inactive by default.

Repository: `thystra/activity-relay-directory`.

Add `GET /v1/relays` as a separately gated, default-off read surface. Lifecycle
service may be enabled while public listing remains disabled. The activation
control is independent of both lifecycle availability and new-registration
admission.

The initial versioned JSON projection contains only:

- canonical relay actor;
- canonical public base URL;
- `healthy`, `stale`, or `dead` health state;
- server-owned `last_seen_at` in UTC; and
- schema/version and bounded pagination metadata.

Deliverables:

- `GET`/`HEAD` only, bounded page size, stable keyset ordering, and an opaque
  bounded cursor;
- one repository query that excludes suspended, unregistered, pruned, and
  30-day-or-older rows before data reaches the presentation layer;
- deterministic JSON, short explicit cache policy, validators, security
  headers, and no cross-origin write surface;
- bounded public admission independent of signed lifecycle admission; and
- fixtures that freeze the schema without expanding lifecycle protocol
  vocabulary accidentally.

Acceptance gates:

- pagination has no duplicates or omissions across stable data;
- invalid cursors and limits return fixed redacted errors;
- moderation identities, reasons, events, first-registration time, internal
  state, database identifiers, and client network addresses never appear;
- status reports listing unavailable while the listing graph is disabled; and
- query plans demonstrate indexed bounded reads at the maximum test dataset.

## Tranche 15: Human-readable directory view

Completed (2026-08-10): the human-readable `GET`/`HEAD` `/` view is implemented
from the same public projection as `/v1/relays`, with shared authenticated
pagination, deterministic caching, bundled same-origin styling, automatic
template escaping, and a strict CSP. It remains undeployed and default-off with
the public listing gate.

Repository: `thystra/activity-relay-directory`.

Build `GET /` from the same public projection as JSON so filtering and privacy
rules cannot drift. Use Go templates and static assets; JavaScript is optional
enhancement rather than a functional requirement.

Deliverables and gates:

- accessible semantic markup, keyboard navigation, responsive layout, and
  visible health definitions;
- automatic HTML escaping and strict content-security policy;
- bounded pagination matching JSON ordering;
- no remote fonts, analytics, third-party scripts, relay-provided HTML, or
  relay-controlled image fetches in the initial view;
- deterministic template tests, escaping tests, and accessibility review; and
- cache invalidation consistent with the JSON projection.

## Tranche 16: Configurable inactive-record retention

Completed (2026-08-10): the source implementation adds strict default-zero
inactive retention, a local identity-free dry-run, backup-gated confirmed purge,
bounded transactionally revalidated batches, crash-safe aggregate run audit,
and schema version 6 retention metadata/indexes. It remains undeployed; no
positive production policy or purge is activated by this source completion.

Repository: `thystra/activity-relay-directory`.

Retention distinguishes protocol replay expiry, 30-day soft pruning, inactive
relay records, lifecycle events, and private moderation events. The public term
remains "prune" only for the reversible 30-day transition; irreversible
retention work is called "purge" in code, CLI output, and documentation.

Configuration contract:

- `DIRECTORY_INACTIVE_RETENTION_DAYS=0` is the default and retains inactive
  records indefinitely;
- a positive integer means an administratively active, unregistered or pruned
  relay becomes purge-eligible after that many complete days from its most
  recent inactive transition;
- for example, `365` retains an inactive record for one year after unregister
  or soft prune, rather than counting from its last healthy heartbeat;
- negative, fractional, overflowing, and unreasonably large values fail startup
  validation;
- changing a positive value back to `0` stops future purges but cannot recreate
  records already deleted; and
- replay reservations retain their independent protocol-bounded ten-minute
  maximum regardless of this setting.

Registered relays are never inactive candidates. Suspended records are also
never automatic purge candidates, even if unregistered or soft-pruned, because
deleting them would erase an active moderation decision. A retained actor that
registers again before its cutoff leaves the candidate set transactionally.
After an inactive relay is purged, enrollment treats a later return as a
never-accepted actor.

Deliverables:

- documented threat model and restoration consequences for every data class;
- strict configuration parsing and documented examples for `0`, `365`, and
  invalid values;
- a migration that preserves append-only enforcement outside a narrowly scoped
  retention transaction;
- dry-run counts and oldest/newest candidate times without dumping identities;
- bounded keyset batches, cancellation, progress checkpoints, transactional
  eligibility rechecking, and idempotent restart;
- a private retention-run audit containing policy version, cutoff, counts, and
  outcome but no relay identity list;
- deletion of eligible inactive relay rows and their eligible lifecycle events
  without deleting a suspended record or its private moderation evidence;
- verified SQLite backup guidance before changing from `0` to a positive value;
  and
- manual, separately documented checkpoint/VACUUM behavior outside request
  handling.

Acceptance gates:

- fresh, upgrade, rollback, interruption, restart, concurrent lifecycle,
  suspension, boundary, and batch-limit tests pass;
- exact zero, one-day, 365-day, and cutoff-boundary behavior is covered with a
  fake clock;
- active or suspended records can never satisfy automatic deletion predicates;
- a concurrent register, restore, or moderation decision prevents an obsolete
  candidate from being deleted;
- backup restoration reproduces the exact pre-retention database;
- retention cannot weaken public filtering or replay guarantees; and
- upgrading with the default `0` performs no inactive-record deletion.

## Tranche 17: Database growth guard and administrator notification

Repository: `thystra/activity-relay-directory`.

Merged and verified (2026-08-10): Forgejo PR #5 merged as signed commit
`8b7b8cedc813cfc18793e235b6df83e0b1d1325a` with tree
`b60901da49f42ac5fba965adf479b6559ff21b04`; native master Test 2/2 and Build
1/1 passed and the downstream GitHub mirror matched. The bounded non-destructive
SQLite growth guard, common write admission, readiness behavior, local storage
inspection, and opt-in no-shell administrator notifications remain undeployed:
the administrator recipient is still empty/default-off, no production
notification has been sent, and no positive retention policy or deployment
activation occurred as part of the source merge.

Retention reduces reusable data only when a positive policy is selected. The
growth guard provides a non-destructive upper bound so an unattended
instance with indefinite retention cannot consume a VPS filesystem without
warning. Reaching the bound stops new database writes and fails readiness; it
never silently
deletes records or changes the configured retention policy.

Configuration contract:

- `DIRECTORY_DATABASE_MAX_BYTES=1073741824` sets a default 1 GiB managed budget
  for the SQLite main file and its WAL/shared-memory sidecars, and must remain a
  positive bounded integer;
- `DIRECTORY_DATABASE_WARNING_PERCENT=75` sends/logs the first warning;
- a fixed critical transition occurs at 90 percent and the hard transition at
  100 percent;
- `DIRECTORY_ADMIN_EMAIL=` is empty by default and disables email while keeping
  logs and readiness behavior active;
- `DIRECTORY_MAIL_BACKEND=mail`, `DIRECTORY_MAIL_COMMAND=/usr/bin/mail`, and
  `DIRECTORY_MAIL_TIMEOUT_SECONDS=30` define an optional local command mailer;
  and
- configured recipients, commands, timeouts, percentages, and byte limits are
  strictly validated. If email is enabled, invalid mail configuration fails
  startup rather than promising alerts that cannot run.

The guard samples immediately at startup, on a fixed five-minute interval, and
before admitting a database write. It uses `page_count`, `freelist_count`, and
page size to distinguish used, reusable, and newly allocatable logical pages.
It also measures the main file, WAL, and shared-memory sidecars. The SQLite
`max_page_count` backstop must be set below the total budget with reviewed
headroom for bounded WAL and migration work, rounded safely for page size.
Bounded checkpoints, short public reads, and a journal-size policy limit WAL
growth. The documentation must state the maximum bounded in-flight overhead and
that this guard complements rather than replaces host filesystem monitoring.

Alert behavior:

- send on transitions into warning, critical, hard-limit, and recovered states;
- include current used and allocated logical pages, reusable-page count,
  physical database-family size, growth since the preceding sample, configured
  maximum, retention setting, and a fixed remediation checklist, but no relay
  identities or audit details;
- persist only bounded alert state so restart does not create an email storm;
- send at most one reminder per state in 24 hours and use hysteresis before a
  recovery notification;
- execute the selected mailer directly with `exec.CommandContext`, fixed
  backend arguments, message body on standard input, and no shell;
- reject control characters and option-like recipient values; and
- bound mail runtime and output. Mail failure is logged and retried with bounded
  delay but does not crash the service.

At the hard limit, health remains live, read-only public views and local storage
inspection remain available, readiness reports unavailable, and every mutating
lifecycle, enrollment, moderation, pruning, and retention operation fails
closed with no partial transaction. Existing data is preserved. A configured
positive retention policy may free logical pages for reuse, but SQLite file
shrinkage remains an explicit offline/operator maintenance decision.

Deliverables and gates:

- `activity-relay-directory admin storage status`, `check`, and `test-alert`
  commands with human and bounded JSON output;
- isolated fake-mailer tests proving argv, stdin, timeout, redaction,
  deduplication, reminder, recovery, and failure behavior;
- exact page-limit, near-limit migration, concurrent writer, WAL growth,
  checkpoint, freed-page reuse, restart, and full-disk-class failure tests;
- readiness and handler tests proving that writes fail closed while allowed
  reads remain bounded;
- container documentation explaining that local command mail requires an
  explicitly installed/configured mailer in the image or deployment; and
- Debian packaging may recommend a mail transport but must not configure a
  recipient, credentials, relay host, or enable notifications automatically.

## Tranche 18: Cross-repository staging and release-candidate gate

Repositories: both projects.

Run a layered staging soak only after the relevant source tranches are reviewed,
committed, pushed, and green in their own CI systems.

Required evidence:

1. directory starts with lifecycle and listings disabled;
2. explicit enablement reports the complete handler graph available;
3. two independent relays register with their existing actor keys;
4. heartbeats advance only server-owned recency;
5. replay rejection survives directory restart;
6. suspend hides and blocks a relay without exposing private audit fields;
7. restore preserves identity and permits a fresh signed registration;
8. stale, dead, and prune boundaries are exercised with controlled clocks or
   an accelerated test build, not by weakening production defaults;
9. unregister disables local configuration before the network operation and a
   relay restart does not re-register;
10. soft-pruned relay re-registration preserves retained history;
11. backup and restore are exercised before any retention trial; and
12. zero and positive retention modes, near-cap write refusal, recovery, and
   backup restoration are exercised;
13. warning, critical, hard-limit, recovery, reminder suppression, and mail
   failure paths are captured without sending to an unintended recipient; and
14. logs, JSON, HTML, metrics, alerts, and release artifacts are scanned for
   private or attacker-controlled data leakage; and
15. preserve the development Go 1.23/1.25 matrix, then at RC run full Directory
   validation on Go 1.26 and the latest Go 1.27 RC, treating 1.26 failures as
   blockers and 1.27-RC failures as individually triaged compatibility evidence.
   After Go 1.27 final, rerun and choose the supported floor/matrix; then perform
   the same compatibility/floor pass for Activity-Relay.

After a sustained soak, prepare release-candidate metadata, deterministic
binary/container builds, SBOMs, checksums, rollback instructions, schema notes,
and Debian packages. Package publication, deployment, feature activation, and
stable promotion remain distinct approvals.

## Completion definition

The remaining-feature roadmap is complete only when:

- Activity-Relay can opt into one or more directories and reliably register,
  heartbeat, and unregister without config-driven re-registration surprises;
- operators can privately inspect and apply moderation decisions;
- directory health and soft pruning are deterministic and bounded;
- JSON and HTML views expose only the reviewed relay projection;
- inactive retention is indefinite at `0` or performs backed-up, bounded,
  audited, and restorable-before-purge maintenance after a configured positive
  number of days;
- database growth is bounded, observable, readiness-aware, and able to notify a
  configured administrator without shell execution or alert storms; and
- the cross-repository compatibility and staging matrix passes before the first
  release candidate.
