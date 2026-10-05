# Activity-Relay Directory

Activity-Relay Directory is a self-hosted directory and health service for
public ActivityPub relays. It can learn about relays from authenticated
lifecycle heartbeats or local operator discovery, check whether those relays are
reachable, and publish a human-readable directory, JSON APIs, and plain-text
host lists.

The service tracks relay instances only. It is not intended to collect the
identities of servers, followers, users, or communities connected to a relay.

## What it provides

- a public relay directory with four clear availability tiers;
- optional authenticated register, heartbeat, and unregister support for relays;
- local commands to add or import relays that do not send directory heartbeats;
- background reachability checks and retry of unavailable relays;
- JSON APIs at `/v1/relays` and `/v2/relays`, including bounded self-reported receiving-site telemetry when available;
- plain-text relay lists suitable for reuse in discovery imports;
- local moderation, pruning, retention, storage, and audit commands; and
- SQLite persistence designed for one active directory process on one host.

All network-facing features that can change state are disabled by default.
Public listing, relay lifecycle participation, background reachability checks,
and automatic pruning are enabled independently.

## Directory tiers

The public directory groups relays by operational status rather than popularity.
Within each tier, relays that self-report registration as open are shown first,
then restricted, closed, and unreported status, with normalized hostname as the
stable tie-breaker. Registration status is descriptive and never changes a
relay's operational tier. Heartbeat frequency and check recency do not improve a
relay's tier.

| Tier | Meaning |
| --- | --- |
| **Tier 1 — Heartbeat + Online** | The relay is sending a current directory heartbeat and was reachable at the latest check. |
| **Tier 2 — Online, no current heartbeat** | The relay is known to the directory and was reachable at the latest check, but is not currently sending directory heartbeats. |
| **Tier 3 — Offline / Unreachable** | The relay could not be reached at the latest check but has been seen online within the last 30 days. |
| **Tier 4 — Graveyard** | The relay has not been seen online for at least 30 days. It remains listed for historical reference and is still checked periodically in case it returns. |

A recovered relay moves back to Tier 1 or Tier 2 automatically according to
whether it has a current heartbeat.

## Quick start from source

For a local development instance:

```sh
mkdir -p data
chmod 0700 data

export DIRECTORY_PUBLIC_BASE_URL=http://127.0.0.1:8080
export DIRECTORY_DATABASE_PATH="$PWD/data/directory.sqlite"
export DIRECTORY_PUBLIC_LISTING_ENABLED=true

go run ./cmd/activity-relay-directory
```

Then open `http://127.0.0.1:8080/`.

The local HTTP base URL above is for loopback development only. A public
installation should use HTTPS and a reverse proxy. See
[`docs/REVERSE-PROXY.md`](docs/REVERSE-PROXY.md).

## Docker Compose

The included Compose file keeps the same conservative defaults as the service.
Copy the example environment file, set the public URL and the features you want,
then start the service:

```sh
cp .env.example .env

# Edit .env, then:
docker compose up --build
```

By default the published port is bound to `127.0.0.1`, lifecycle participation
is disabled, and the public directory is disabled.

## Debian/Ubuntu package

The Debian package installs the service account, state directory, defaults file,
and hardened systemd unit, but deliberately does **not** enable or start the
service automatically.

After installing the package, review:

```text
/etc/default/activity-relay-directory
```

Then enable the service when ready:

```sh
sudo systemctl enable --now activity-relay-directory
```

Package upgrades reload systemd's unit definitions but do not restart an
already-running Directory. Review the upgrade and restart the service manually
when you are ready to load the new binary and unit settings.

Package removal preserves the database and service account. Package purge is
destructive. See [`debian/README.Debian`](debian/README.Debian) before upgrade,
downgrade, removal, or purge.

## Add relays manually

A relay may be added from its base URL, `/actor`, or `/inbox` URL. Activity-Relay
Directory resolves and validates the relay actor before storing it as an active
discovery.

```sh
activity-relay-directory admin discovery add \
  --url https://relay.example/ \
  --operator operator-id \
  --reason public_relay
```

For a list of relays:

```sh
activity-relay-directory admin discovery import \
  --file ./relay-candidates.txt \
  --operator operator-id \
  --reason public_list \
  --source-label curated_list
```

Bare hostnames are accepted and treated as HTTPS candidates. Explicit HTTP URLs
remain rejected. A file may contain blank lines and `#` comments.

For spreadsheet-friendly descriptive relay profiles, use CSV input explicitly:

```sh
activity-relay-directory admin discovery import \
  --file ./relay-candidates.csv \
  --input-format csv \
  --operator operator-id \
  --reason public_list \
  --source-label curated_list
```

CSV import keeps the same actor-validation and confirmation boundary as normal
discovery. The line-oriented format remains the default. See
[`docs/RELAY-PROFILES.md`](docs/RELAY-PROFILES.md) for the reviewed columns,
profile precedence, private `source_url` provenance, and spreadsheet-safety
rules.

If you also want to remember relays that are currently unreachable or return an
incompatible actor document, add `--add-dead-relays`:

```sh
activity-relay-directory admin discovery import \
  --file ./relay-candidates.txt \
  --operator operator-id \
  --reason public_list \
  --source-label curated_list \
  --add-dead-relays
```

Those unresolved candidates remain private until a later check successfully
validates the relay actor. Retry timing is 6 hours, 12 hours, 24 hours, 3 days,
and then once a week.

See [`docs/DISCOVERY-REACHABILITY.md`](docs/DISCOVERY-REACHABILITY.md) for the
full discovery behavior.

## Keep relay status current

Enable background reachability maintenance with:

```text
DIRECTORY_REACHABILITY_ENABLED=true
```

Reachability checks are independent of authenticated heartbeats. A relay cannot
move into the heartbeat tier merely because it responds to a network check, and
a failed network check does not rewrite its heartbeat history.

Known relays that stay offline are eventually checked weekly. Relays that have
not been seen online for 30 days remain in the database and move into the
Graveyard tier rather than being silently deleted.

See [`docs/REACHABILITY.md`](docs/REACHABILITY.md).

## Publish the directory

Enable public views with:

```text
DIRECTORY_PUBLIC_LISTING_ENABLED=true
```

That exposes these read-only routes:

| Route | Purpose |
| --- | --- |
| `/` | Public facing relay directory |
| `/v1/relays` | Version 1 compatibility API |
| `/v2/relays` | Richer tiered relay API |
| `/downloads/active.txt` | Tier 1 and Tier 2 hosts |
| `/downloads/unavailable.txt` | Tier 3 and Tier 4 hosts |
| `/downloads/all.txt` | All public relay hosts |
| `/healthz` | Process health |
| `/readyz` | Readiness, including storage availability |
| `/v1/status` | Directory lifecycle/enrollment status |

Enabling the public directory does **not** enable lifecycle registration or open
enrollment. The human page summarizes known relays as online/offline and may
show the aggregate number of unresolved candidates still pending verification;
it does not disclose those candidate identities.

The public APIs and downloads intentionally omit local discovery provenance,
operator reasons, moderation records, audit events, request signatures, and
other private administrative data. See
[`docs/PUBLIC-LISTING.md`](docs/PUBLIC-LISTING.md).

## Export relay lists locally

You can generate the same tier-scoped relay lists without enabling public
listing:

```sh
activity-relay-directory admin export --scope active --format hosts
activity-relay-directory admin export --scope unavailable --format hosts
activity-relay-directory admin export --scope all --format actors
activity-relay-directory admin export --scope all --format csv > relays.csv
```

Scopes are:

- `active`: Tier 1 and Tier 2;
- `unavailable`: Tier 3 and Tier 4; and
- `all`: all four public tiers.

`hosts` writes one normalized host per line, including non-default HTTPS ports.
`actors` writes normalized relay actor URLs. `csv` writes canonical relay actor
identity plus the effective descriptive profile fields, with private provenance
omitted and spreadsheet-formula cells reversibly neutralized. Host and CSV
exports can be fed back into a later `discovery import`; CSV re-import requires
`--input-format csv`.

## Optional relay lifecycle heartbeats

Authenticated register, heartbeat, and unregister routes are controlled by:

```text
DIRECTORY_LIFECYCLE_ENABLED=true
```

They are disabled by default. Enrollment is a separate policy and starts
closed. A local operator can inspect or change it with:

```sh
activity-relay-directory admin enrollment status
activity-relay-directory admin enrollment open --operator operator-id
activity-relay-directory admin enrollment close --operator operator-id
```

Closing enrollment prevents previously unseen relays from registering; it does
not remove or disable relays already known to the directory.

The lifecycle protocol is documented in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md). The companion relay implementation is
[`thystra/Activity-Relay`](https://github.com/thystra/Activity-Relay).

## Local administration

Administrative commands are local-only; there is no public moderation API.
Examples:

```sh
activity-relay-directory admin show \
  --actor https://relay.example/actor \
  --format json

activity-relay-directory admin suspend \
  --actor https://relay.example/actor \
  --moderator operator-id \
  --reason security_review

activity-relay-directory admin restore \
  --actor https://relay.example/actor \
  --moderator operator-id \
  --reason review_complete

activity-relay-directory admin audit \
  --actor https://relay.example/actor \
  --limit 50 \
  --format json
```

Other local maintenance commands include:

```sh
activity-relay-directory admin pruning dry-run --format json
activity-relay-directory admin retention dry-run --format json
activity-relay-directory admin storage status --format json
activity-relay-directory admin storage check
```

Hard retention is disabled by default. Automatic pruning is also disabled by
default and is reversible rather than destructive. Review
[`docs/MODERATION.md`](docs/MODERATION.md),
[`docs/RETENTION.md`](docs/RETENTION.md), and
[`docs/STORAGE-GROWTH.md`](docs/STORAGE-GROWTH.md) before changing those
policies.

## Core configuration

The full configuration matrix is in
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md). The settings most operators
will encounter first are:

| Variable | Default / requirement | Purpose |
| --- | --- | --- |
| `DIRECTORY_PUBLIC_BASE_URL` | required | Public directory URL; HTTPS for lifecycle use |
| `DIRECTORY_DATABASE_PATH` | required | Local SQLite database path |
| `DIRECTORY_LISTEN_ADDRESS` | `127.0.0.1:8080` | HTTP listener |
| `DIRECTORY_PUBLIC_LISTING_ENABLED` | `false` | Publish the human, JSON, and text directory views |
| `DIRECTORY_LIFECYCLE_ENABLED` | `false` | Enable authenticated register/heartbeat/unregister routes |
| `DIRECTORY_REACHABILITY_ENABLED` | `false` | Enable periodic actor/inbox reachability checks |
| `DIRECTORY_SOFT_PRUNING_ENABLED` | `false` | Enable automatic reversible pruning |
| `DIRECTORY_INACTIVE_RETENTION_DAYS` | `0` | Hard retention age; `0` keeps inactive records indefinitely |

The retired `DIRECTORY_REGISTRATION_ENABLED` setting is rejected rather than
accepted as an alias.

## Optional operator contact

The human directory can show operator-owned contact links without exposing
private administrator-alert settings. Copy `config.yml.example` to the normal
operator configuration location and set any values you want to publish:

```yaml
OPERATOR-WEBSITE: "https://operator.example/"
OPERATOR-EMAIL: "operator@example.org"
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
SUPPORT:
  - title: "Liberapay"
    url: "https://liberapay.com/example/"
  - title: "Bitcoin"
    value: "bc1qexample"
```

Empty values are omitted. `SUPPORT` is optional and renders a collapsed,
presentation-only `Support this directory` block near the top of the human page.
Each support entry has a title and exactly one HTTPS URL or plain-text value.
 The Fediverse ID and profile URL must be supplied
together. See [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) for validation
rules and alternate configuration paths.

## API compatibility

`/v1/relays` retains the version 1 public-listing contract used by Activity-Relay
Directory 1.0. Newer directory information, including the four public tiers and
independent reachability evidence, is exposed through `/v2/relays` rather than
changing the V1 response shape.

Protocol and API compatibility details live in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md) and
[`docs/PUBLIC-LISTING.md`](docs/PUBLIC-LISTING.md).

## Security and privacy

The project is intentionally conservative about network access and stored data:

- relay actor and signing-key retrieval uses SSRF-resistant DNS/address and
  redirect checks rather than the default HTTP client;
- lifecycle requests require authenticated signatures, content digests,
  limited timestamp windows, and nonce replay protection;
- moderation and operator provenance stay private;
- the database contains relay-instance state, not connected-site or user lists;
- public HTTP requests cannot start reachability, pruning, or retention work;
- SQLite is intended for one active directory process on one host; and
- public listing, lifecycle, reachability, and pruning all default to off.

See [`SECURITY.md`](SECURITY.md) and [`docs/SECURITY.md`](docs/SECURITY.md) for
the complete security model.

## Development

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go build ./cmd/activity-relay-directory
```

For a container build:

```sh
docker build --pull=false --build-arg VERSION=development .
```

Host-neutral Nginx, Apache, and Caddy examples are under `contrib/`.

## Documentation

Operator and implementation details are split into topic-specific documents:

- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) — settings and defaults;
- [`docs/PUBLIC-LISTING.md`](docs/PUBLIC-LISTING.md) — public directory, APIs,
  downloads, tiers, and pagination;
- [`docs/DISCOVERY-REACHABILITY.md`](docs/DISCOVERY-REACHABILITY.md) — local
  discovery and retained unavailable candidates;
- [`docs/REACHABILITY.md`](docs/REACHABILITY.md) — periodic reachability checks;
- [`docs/MODERATION.md`](docs/MODERATION.md) — suspend, restore, and audit;
- [`docs/RETENTION.md`](docs/RETENTION.md) — inactive-record retention;
- [`docs/STORAGE-GROWTH.md`](docs/STORAGE-GROWTH.md) — SQLite growth protection;
- [`docs/REVERSE-PROXY.md`](docs/REVERSE-PROXY.md) — public proxy deployment;
- [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — lifecycle protocol; and
- [`docs/RELEASING.md`](docs/RELEASING.md) — maintainer release procedure.

`ARCHITECTURE.md`, `AGENTS.md`, and `TODO.md` are maintainer-oriented references
and are not required to operate a normal directory instance.

## Package and data safety

The service does not automatically delete unavailable relays merely because they
remain offline. Graveyard entries continue to receive periodic recovery checks.
Hard deletion of inactive database records is a separate, explicitly configured
retention operation and defaults to disabled.

For packaged installations, normal upgrade and removal preserve directory state.
Explicit package purge is destructive. Always back up the SQLite database before
purging or deliberately enabling hard retention.

## Licence

GNU Affero General Public License version 3. See [`LICENCE`](LICENCE).

## Maintenance transparency

Development may use AI-assisted tooling for drafting, analysis, testing, and
review support. A human maintainer reviews and approves changes, runs release
checks, controls deployments, and remains accountable for the project.
