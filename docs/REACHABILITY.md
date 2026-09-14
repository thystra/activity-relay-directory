# Background reachability maintenance

## Scope

Activity-Relay Directory 1.1 can optionally maintain independent observations
of retained relay actors. This worker is disabled by default and has no public
request trigger. Enable it only with:

```text
DIRECTORY_REACHABILITY_ENABLED=true
```

The cadence and budgets are release policy, not operator tuning knobs:

- maintenance runs once per hour;
- an actor check is current for six hours;
- candidate pages contain at most 24 actors;
- one run attempts at most 96 actors; and
- no more than eight remote actor/inbox probes run concurrently.

Every remote request uses the same proxy-free, DNS/address-pinning,
redirect-revalidating, bounded HTTPS client used by lifecycle actor resolution.
The worker never uses shell commands or the default HTTP client.

## Eligibility and fairness

An actor is eligible when either its lifecycle state is currently `registered`
or it has an active operator discovery. A retained administrative suspension
overrides both paths. Registration and discovery are deduplicated by canonical
actor identity.

Due actors are ordered as:

1. never checked;
2. oldest actor check; then
3. canonical actor URL as a stable tie-breaker.

The keyset cursor carries whether a prior check exists, its timestamp, and the
actor identity. This prevents low-sorted actors from starving later actors when
the eligible population approaches the fixed run budget.

## Observation semantics

A successful actor fetch records `reachable`, the check time, the success time,
and the actor's current declared inbox. A successful actor document with no
inbox clears an older declaration. A failed actor fetch records `unreachable`
and a new check time while preserving the last successful actor time and prior
inbox evidence.

When a validated actor declares an inbox, the worker performs one bounded
non-mutating `OPTIONS` diagnostic. `responsive`, `method_rejected`, and
`unreachable` are diagnostics only; method rejection is not treated as absence
of ActivityPub inbox support. No synthetic ActivityPub `POST` is sent.

Network probes may run concurrently, but durable writes are serialized. The
write transaction rechecks that the actor is still eligible and rejects a
stale result when an equal or newer actor observation already exists. Discovery
removal, unregister/re-register, suspension, or a newer worker result therefore
cannot be overwritten by a stale candidate read.

Reachability never updates lifecycle `last_seen_at_unix`, heartbeat state,
registration state, or RFC 9421 evidence.

## Soft-pruning safety gate

When reachability and automatic soft pruning are both enabled, pruning is
fail-closed behind complete recent reachability coverage.

A successful non-truncated reachability pass records a process-local coverage
point consisting of the pass's captured observation time and completion time.
Automatic pruning may run only while both remain no more than six hours old.
A restart begins with no coverage; pruning waits for a new complete pass.
Failed or truncated reachability passes do not refresh coverage.

Pruning evaluates its 30-day heartbeat cutoff against the reachability pass's
captured observation time, not a newer wall clock. This prevents evidence from
becoming stale in the gap between a completed reachability pass and the pruning
scan.

Independently, candidate selection and the final immediate pruning transaction
exclude a relay whose *current* actor state is `reachable` and whose last
successful actor check is within the six-hour freshness window. A later
`unreachable` state supersedes historical success for this protection. This
second check closes the candidate-read to prune-write race.

A single unreachable external relay does not fail the maintenance pass: its
negative observation is valid evidence. Repository failures, cancellation, or
other subsystem failures do fail the pass and therefore do not refresh the
coverage gate.

## Logging and public exposure

Worker logs are aggregate only: counts of scanned, reachable, unreachable,
skipped, inbox diagnostic classes, and truncation. Actor URLs and private
discovery provenance are not logged by the scheduler.

This tranche does not change `/v1/relays` or the human directory page. Public
1.1 projection of heartbeat/reachability diagnostics is a separate review
tranche.
