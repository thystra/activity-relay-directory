# Activity-Relay Directory

Activity-Relay Directory (ARD) is a self-hosted directory and health service for
public ActivityPub relays. It accepts authenticated relay lifecycle updates,
allows operators to discover relays locally, checks reachability, and can
publish a human-readable directory, JSON APIs, and plain-text relay lists.

ARD tracks relay instances, not the identities of servers, followers, users, or
communities connected to a relay.

## Highlights

- four operational directory tiers based on authenticated liveness and current
  reachability;
- optional signed register, heartbeat, and unregister lifecycle protocols;
- local discovery/import for relays that do not participate in lifecycle
  heartbeats;
- background actor/inbox reachability maintenance;
- reviewed descriptive relay profiles and bounded participating-site telemetry;
- public human, JSON, and plain-text directory views;
- local moderation, audit, pruning, retention, and storage administration; and
- SQLite persistence intended for one active Directory process on one host.

State-changing network features are disabled by default. Public listing,
lifecycle participation, reachability maintenance, and automatic pruning are
independent opt-in features.

## Directory tiers

The public directory is ordered by operational state, not popularity.
Registration status such as Open, Restricted, or Closed is descriptive and does
not affect tier placement.

| Tier | Meaning |
| --- | --- |
| **Tier 1 — Heartbeat + Online** | Current authenticated relay liveness plus a recent successful reachability check. |
| **Tier 2 — Online, no current heartbeat** | The relay is reachable, but current authenticated liveness evidence is absent. |
| **Tier 3 — Offline / Unreachable** | The relay is currently unavailable but has been seen online within the last 30 days. |
| **Tier 4 — Graveyard** | The relay has not been seen online for at least 30 days and remains retained for recovery/history. |

Recovered relays move back to Tier 1 or Tier 2 automatically.

## Install

Choose the deployment path that fits the host:

- **Debian/Ubuntu package** — installs the dedicated service account, hardened
  systemd unit, state directory, defaults file, and package-managed configuration
  example. The service is not enabled automatically.
- **Docker Compose** — uses the included Compose deployment with conservative,
  loopback-first defaults.
- **Source** — suitable for development and custom deployments.

Start with [`docs/INSTALLATION.md`](docs/INSTALLATION.md). Public deployments
should also review [`docs/REVERSE-PROXY.md`](docs/REVERSE-PROXY.md).

## Operate

The local administrative CLI handles discovery, exports, lifecycle enrollment,
moderation, pruning, retention, storage checks, and private audit reads. For
examples and normal day-to-day procedures, see
[`docs/ADMINISTRATION.md`](docs/ADMINISTRATION.md).

The complete setting matrix is in
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md). The package-managed operator
presentation reference is:

```text
/etc/activity-relay-directory/config.yml.example
```

The live `/etc/activity-relay-directory/config.yml` is operator-owned and is not
created or overwritten by the package.

## Public interfaces

When public listing is enabled, ARD can expose:

```text
/                         human directory
/v1/relays                frozen compatibility API
/v2/relays                richer tier/profile/telemetry/diagnostics API
/downloads/active.txt     Tier 1 + Tier 2 hosts
/downloads/unavailable.txt Tier 3 + Tier 4 hosts
/downloads/all.txt        all public relay hosts
/healthz                  process health
/readyz                   readiness/storage availability
/v1/status                lifecycle capability/enrollment status
```

Public listing does not enable lifecycle registration or enrollment. See
[`docs/PUBLIC-LISTING.md`](docs/PUBLIC-LISTING.md) for the public data contract
and pagination rules.

## Security and privacy

ARD is deliberately conservative about network access and stored data:

- relay actor/key retrieval uses SSRF-resistant resolution and redirect policy;
- lifecycle requests require authenticated signatures, content digests,
  bounded time windows, and nonce replay protection;
- local discovery provenance, moderator/reason tokens, and audit events remain
  private;
- public HTTP requests cannot initiate pruning, retention, or reachability
  work; and
- destructive retention is separate from reversible pruning and defaults off.

See [`SECURITY.md`](SECURITY.md) and [`docs/SECURITY.md`](docs/SECURITY.md).

## Documentation

For operators:

- [`docs/INSTALLATION.md`](docs/INSTALLATION.md) — package, Compose, source, and
  first-start deployment;
- [`docs/ADMINISTRATION.md`](docs/ADMINISTRATION.md) — practical CLI/operator
  manual;
- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) — complete configuration
  classification, defaults, and dependencies;
- [`docs/PUBLIC-LISTING.md`](docs/PUBLIC-LISTING.md) — public views and tier
  projection;
- [`docs/DISCOVERY-REACHABILITY.md`](docs/DISCOVERY-REACHABILITY.md) and
  [`docs/REACHABILITY.md`](docs/REACHABILITY.md) — discovery and health checks;
- [`docs/REACHABILITY-DIAGNOSTICS.md`](docs/REACHABILITY-DIAGNOSTICS.md) —
  1.3.1 DNS/TLS/actor/inbox diagnostic evidence and retry semantics;
- [`docs/MODERATION.md`](docs/MODERATION.md),
  [`docs/RETENTION.md`](docs/RETENTION.md), and
  [`docs/STORAGE-GROWTH.md`](docs/STORAGE-GROWTH.md) — local maintenance and
  data-safety policy; and
- [`docs/RELAY-PROFILES.md`](docs/RELAY-PROFILES.md) — relay-owned, CSV, and
  operator profile metadata.

For implementation and contributors:

- [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) — build/test entry point;
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — component and data-flow design;
- [`docs/PROTOCOL.md`](docs/PROTOCOL.md) and
  [`docs/HANDLERS.md`](docs/HANDLERS.md) — lifecycle wire contract and HTTP
  behavior;
- [`docs/PERSISTENCE.md`](docs/PERSISTENCE.md) — SQLite schema/state semantics;
- [`docs/RELEASING.md`](docs/RELEASING.md) and
  [`docs/RC-ACCEPTANCE.md`](docs/RC-ACCEPTANCE.md) — release engineering and
  operator acceptance; and
- [`AGENTS.md`](AGENTS.md) / [`TODO.md`](TODO.md) — contributor rules and active
  project work.

## Package and data safety

Normal package upgrade and removal preserve Directory state. Package purge is
destructive. In-place SQLite downgrade is unsupported; restore the database
backup associated with the older release before starting an older binary.

Unavailable relays are not automatically hard-deleted merely for remaining
offline. Graveyard entries continue to receive recovery checks. Hard retention
requires explicit configuration.

## Licence

GNU Affero General Public License version 3. See [`LICENCE`](LICENCE).

## Maintenance transparency

Development may use AI-assisted tooling for drafting, analysis, testing, and
review support. A human maintainer reviews and approves changes, runs release
checks, controls deployments, and remains accountable for the project.
