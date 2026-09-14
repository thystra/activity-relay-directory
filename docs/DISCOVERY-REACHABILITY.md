# Discovery and reachability

## Status and scope

This document defines the approved Activity-Relay Directory 1.1 design for
operator discovery, independent relay reachability, endpoint diagnostics, and
their interaction with the existing version 1 lifecycle contract.

It is a design/implementation contract, not evidence that the 1.1 runtime is
already present. The 1.0.0 lifecycle, public `/v1/relays` representation,
pruning behavior, and schema remain authoritative until their corresponding
post-1.0 implementation tranches are applied and validated.

The motivating interoperability case is a relay that successfully registered
and heartbeated but later stopped sending Directory heartbeats while its public
ActivityPub actor remained valid and reachable. The Directory must distinguish
those two facts rather than converting an independent HTTP observation into an
authenticated heartbeat.

## Evidence model

ARD 1.1 keeps four kinds of evidence separate:

1. **Lifecycle participation** -- accepted signed register, heartbeat, and
   unregister requests under the existing version 1 protocol.
2. **Actor reachability** -- a server-originated bounded fetch proving that the
   canonical ActivityPub actor currently validates.
3. **Endpoint capability** -- actor and inbox facts learned from a validated
   actor plus optional non-mutating inbox diagnostics.
4. **Operator discovery** -- a private local decision authorizing an otherwise
   unregistered public relay to be considered for the Directory.

Only accepted register and heartbeat operations update `last_seen_at_unix`.
Independent actor or inbox probes never write it and never create a synthetic
heartbeat event.

The existing heartbeat-derived states retain their exact version 1 meaning:
`healthy`, `stale`, `dead`, and the 30-day prune boundary. The richer 1.1
projection may additionally use `not observed` where no authenticated heartbeat
has ever been accepted for an entry.

Independent actor reachability has three states:

- `unknown` -- no current observation exists;
- `reachable` -- the latest actor check completed the full validation path; and
- `unreachable` -- the latest bounded actor check failed.

The implementation also retains the last successful actor-observation time so a
failed current check does not erase useful history.

## RFC 9421 diagnostic

RFC 9421 is positive-evidence only. ARD reports `verified` when it has actually
accepted an RFC 9421-signed lifecycle request for the canonical relay actor. A
relay with no such evidence is `not verified`.

`Not verified` is deliberately not `no`: a discovered relay may support RFC
9421 without ever sending ARD's lifecycle profile. Actor public-key material by
itself is not sufficient evidence. ARD must not send a synthetic signed
ActivityPub activity merely to manufacture a diagnostic result.

Existing retained relay rows may be backfilled as RFC 9421 verified at their
server-owned `last_seen_at_unix` because that value can only have been advanced
by an accepted signed register or heartbeat under the released lifecycle
contract. The migration test must prove that assumption against every supported
upgrade path before the backfill is released.

## Candidate input and local-file import

ARD 1.1 supports explicit local operator discovery. It does not automatically
scrape web pages, Git repositories, or third-party relay directories.

The initial bulk format is intentionally simple: a bounded local UTF-8 text file
with one HTTPS candidate per line. Empty lines and lines whose first non-space
character is `#` are ignored. Candidate forms may be:

```text
https://relay.example/
https://relay.example/actor
https://relay.example/inbox
```

The supplied text is a hint, not authority. Before the Directory stores an
active discovery it must independently resolve a canonical actor. Multiple
candidate forms that validate to the same actor are one relay.

Import processing is prospective first: parse and bound the whole input, probe
within the reviewed concurrency/rate limits, report accepted/duplicate/failed
candidates, and require explicit confirmation or `--yes` before durable
discovery mutation. A file disappearing or dropping an entry later never
removes a relay automatically.

Private provenance records a bounded explicit source label such as a curated
list name. It must not persist an operator workstation's absolute path. Public
Directory output contains no source label and does not say whether an entry was
self-registered, manually added, or imported.

## Actor discovery and verification

Discovery and periodic reachability reuse the production actor resolver's
network-safety boundary rather than Go's default HTTP client or a shell command.
At minimum the path preserves:

- HTTPS-only canonical URLs;
- environment-proxy bypass;
- all-answer DNS rejection for prohibited/special/private targets;
- approved-address connection pinning;
- redirect revalidation;
- bounded connect/request/response time and response bytes;
- ActivityStreams-compatible response media type;
- bounded valid JSON;
- exact canonical actor `id`; and
- an accepted `Application` or `Service` actor type.

The current RFC 9421 key resolver additionally requires a requested signing key.
The 1.1 actor probe must share the safe fetch/actor-validation primitives without
requiring one particular historical key, because key rotation must not make a
live actor appear unreachable.

For a base or `/inbox` candidate the initial conventional actor candidate is the
same HTTPS origin's `/actor`. An explicit `/actor` candidate is fetched as
supplied after canonical validation. A successful actor document becomes the
authority for canonical actor identity and its declared inbox.

## Inbox capability and diagnostics

A validated actor's canonical HTTPS `inbox` property is positive evidence that
the actor supports an ActivityPub inbox. ARD stores that URL separately from the
candidate hint.

ARD may perform a bounded, non-mutating check against the declared inbox to
improve diagnostics. It must not POST a fabricated Follow, Announce, Undo, or
other ActivityPub activity. HEAD, OPTIONS, or GET responses are diagnostic only:
a method rejection such as HTTP 405 can be consistent with a functioning POST-
only inbox and must not be converted into `unsupported` merely because a read
method was rejected.

Inbox diagnostics do not determine relay public eligibility or soft pruning.
Canonical actor reachability is the independent liveness signal.

## Private persistence contract

Schema version 8 implements three responsibilities without rewriting the
released `relays` lifecycle table.

### `relay_discoveries`

One row per canonical discovered actor stores canonical actor/public-base
identity, `active|removed` discovery state, first-discovery time, current update
time, and removal time. It contains no heartbeat or registration fields. Add,
remove, and reactivation use nonregressing server acceptance times; reactivation
preserves the original first-discovery time.

### `discovery_events`

Private append-only events record every accepted idempotent add/remove decision.
They store canonical actor, closed action vocabulary, bounded operator ID and
reason code, source kind (`manual|file`), optional bounded source label, and
server acceptance time. Source labels use a short token grammar and are not file
paths. There is intentionally no foreign key to the current discovery row so
retention can remove inactive current state without deleting administrative
audit. Update/delete triggers keep the event table append-only.

### `relay_observations`

One current observation row per retained canonical actor stores actor probe
state, last actor check, last successful actor check, current validated inbox
URL/declaration time, non-mutating inbox probe state/time, RFC 9421 verified
time, and aggregate update time. Observation insertion/actor-identity update is
guarded so an actor must have a retained lifecycle or discovery row.

Actor probe and inbox/RFC evidence times are nonregressing. A successful actor
probe replaces the current actor-declared inbox and resets any older inbox probe
evidence; an empty inbox in a valid new actor document deliberately clears the
old declaration. A failed actor probe advances the failure/check state while
preserving last-success and previously validated inbox evidence. Inbox
observations must target the exact currently declared inbox and cannot predate
its current declaration, so actor changes race safely with diagnostics.

Accepted signed lifecycle requests provide only positive RFC 9421 evidence.
Register and heartbeat advance it transactionally with lifecycle persistence.
Authenticated unregister advances it when the actor still has retained
lifecycle or discovery identity; an otherwise unknown unregister does not create
an observation row. None of these operations fabricate actor reachability or
alter the version 1 `last_seen_at_unix` rule.

### Version-7 upgrade

Migration 8 backfills existing lifecycle rows as RFC 9421 `verified` at their
retained `last_seen_at_unix`, because every such value came from an accepted
signed register or heartbeat under the released contract. Actor state remains
`unknown`; no actor check, actor success, inbox, or inbox diagnostic is
fabricated. The migration preserves the existing 16-byte retention database
identity and historical policy-version-1 retention audit rows while advancing
new hard-retention runs to policy version 2.

## Retention boundary

Hard-retention policy version 2 covers all current identity-bearing state added
by migration 8. It merges two separately bounded candidate sources:
administratively active unregistered/pruned lifecycle rows and `removed`
discovery rows. Active discoveries are never hard-retention candidates.

The merged keyset is `(inactive_at_unix, relay_actor, candidate_kind)`. A
lifecycle snapshot binds row update time and latest lifecycle/moderation event
IDs; a discovery snapshot binds row update time and latest discovery-event ID.
Both bind the current monotonic observation revision (`0` if absent). Every
actor/inbox/RFC evidence write increments that revision, so even same-second
observation activity invalidates an old candidate under the same immediate
transaction.

Deleting one current identity row does not remove another provenance path. An
observation row is deleted only after a successfully purged primary row leaves
no `relays` and no `relay_discoveries` row for that actor. Private
`discovery_events` and `moderation_events` are never deleted by this policy;
only lifecycle event history uses the existing narrow transaction-local delete
scope. Aggregate retention audit schema v2 records lifecycle, discovery,
observation, lifecycle-event, skipped, and batch counts without relay identity.

## Public representation

`GET /v1/relays` remains unchanged for backward compatibility. It continues to
contain only registered/active relays inside the version 1 heartbeat eligibility
window and retains its current cursor and health semantics.

The 1.1 human Directory and its matching new JSON projection use one shared
repository/projection path. Public fields may include:

- canonical relay actor and public base URL;
- heartbeat state and last authenticated observation, or `not observed`;
- actor reachability plus last check/last success;
- actor and inbox capability diagnostics; and
- RFC 9421 `verified` or `not verified`.

They do not expose discovery source, source label, operator ID, reason code,
private probe errors, client IP address, signing key IDs, or audit events.

Public eligibility is an OR of separately justified participation paths, with
administrative suspension overriding both:

- an authenticated registration may authorize an entry under its reviewed
  lifecycle/reachability rules; or
- an active operator discovery may authorize an entry while sufficiently recent
  successful actor evidence exists.

The exact reachability freshness window is fixed in the implementation tranche
and tested with controlled clocks. It must be long enough to tolerate bounded
maintenance failures without allowing an indefinitely unobserved discovered
entry to remain public.

## Soft pruning interaction

Soft pruning still applies only to retained lifecycle rows and never creates or
removes discovery authorization. The 30-day heartbeat boundary remains visible
as protocol-participation evidence, but Tranche 21 changes pruning eligibility
so a registered relay is not transitioned to `pruned` solely because heartbeat
recency is old while a sufficiently recent independent actor observation proves
the relay reachable.

The pruning transaction must revalidate the relevant actor-observation evidence
inside the same write decision that revalidates lifecycle state and heartbeat
cutoff, so a fresh probe racing a pruning candidate cannot be ignored.

## Background maintenance

Reachability maintenance is default-off until explicitly configured. It runs in
the service process, never in a public HTTP request, and uses bounded keyset
candidate pages, fixed per-run work, bounded concurrency, and the shared
storage-growth write-admission boundary for observation persistence.

The reviewed implementation sets a fixed default interval and minimum interval
and captures one server observation time per bounded run. Failure of one relay
does not abort unrelated candidates. Cancellation stops new work promptly and
does not convert canceled probes into negative reachability evidence.

## Required implementation order

1. Freeze this contract and the exact migration/retention behavior.
2. Add and test the migration plus backend-neutral discovery/observation storage.
3. Add safe actor-probe primitives by factoring the existing resolver rather
   than duplicating its network policy.
4. Add local single-entry discovery and bounded file import.
5. Add default-off background reachability maintenance.
6. Add the new JSON projection and switch the human Directory to the same richer
   projection while preserving `/v1/relays`.
7. Integrate reachability into soft-pruning eligibility transactionally.
8. Run the 1.1 acceptance matrix before packaging, publication, deployment, or
   production activation.
