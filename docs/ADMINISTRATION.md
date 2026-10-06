# Activity-Relay Directory operator manual

This guide is the practical command reference for a running Directory. It keeps
examples together so normal administration does not require searching the
project README or protocol documentation.

Administrative commands are local-only. Run them as an account that already has
access to the Directory database, or inside the existing Directory container.
Do not broaden SQLite permissions merely to make CLI use more convenient.

## Inspect a retained relay

```sh
activity-relay-directory admin show \
  --actor https://relay.example/actor \
  --format json
```

This reports retained lifecycle and administrative state. Private historical
moderator/reason events are available only through the audit command.

## Add one relay by discovery

ARD accepts a relay base URL, `/actor`, or `/inbox` URL and resolves the
canonical relay actor before storing an active discovery:

```sh
activity-relay-directory admin discovery add \
  --url https://relay.example/ \
  --operator operator-id \
  --reason public_relay
```

Discovery is independent of authenticated relay lifecycle participation. A
successful reachability check does not fabricate a relay heartbeat.

See [`DISCOVERY-REACHABILITY.md`](DISCOVERY-REACHABILITY.md).

## Import a relay list

For newline-delimited candidates:

```sh
activity-relay-directory admin discovery import \
  --file ./relay-candidates.txt \
  --operator operator-id \
  --reason public_list \
  --source-label curated_list
```

Blank lines and `#` comments are accepted. Bare hostnames are treated as HTTPS
candidates; explicit HTTP URLs are rejected.

To retain unresolved/dead candidates privately for later retry:

```sh
activity-relay-directory admin discovery import \
  --file ./relay-candidates.txt \
  --operator operator-id \
  --reason public_list \
  --source-label curated_list \
  --add-dead-relays
```

Unresolved candidates do not enter the public directory until later validation
succeeds.

## Import descriptive profiles from CSV

Use CSV explicitly:

```sh
activity-relay-directory admin discovery import \
  --file ./relay-candidates.csv \
  --input-format csv \
  --operator operator-id \
  --reason public_list \
  --source-label curated_list
```

ARD previews field-level profile changes before confirmation. CSV profile data
has lower precedence than relay-provided profile data and explicit operator
overrides:

```text
override > relay > csv
```

For the public registration badge and filters, CSV `participation_mode` values
are currently case-sensitive. Use the exact lowercase values `open`,
`restricted`, or `closed` (or leave the field empty); for example, `Open` is
not equivalent to `open`.

An exact relay actor that is already retained as an active lifecycle or
discovery identity may receive CSV profile updates even when its current actor
probe is unreachable or otherwise fails. That profile write does not make the
relay reachable, refresh a heartbeat, or change its operational tier. Unknown
candidates still require the normal actor-validation path.

CSV/profile writes never refresh lifecycle heartbeat evidence. See
[`RELAY-PROFILES.md`](RELAY-PROFILES.md) for columns, provenance, validation,
and spreadsheet-safety rules.

## Export relay lists

Exports do not require the public directory to be enabled:

```sh
activity-relay-directory admin export --scope active --format hosts
activity-relay-directory admin export --scope unavailable --format hosts
activity-relay-directory admin export --scope all --format actors
activity-relay-directory admin export --scope all --format csv > relays.csv
```

Scopes are:

- `active` — Tier 1 and Tier 2;
- `unavailable` — Tier 3 and Tier 4;
- `all` — all four public tiers.

`hosts` emits normalized host names, `actors` emits canonical actor URLs, and
`csv` includes effective reviewed profile fields while omitting private
provenance.

## Enable background reachability

Set:

```text
DIRECTORY_REACHABILITY_ENABLED=true
```

Reachability is separate from authenticated lifecycle liveness. A successful
probe can move an otherwise known relay into an online tier, but it does not
create authenticated heartbeat evidence. Unavailable relays are checked on a
bounded retry schedule and long-term offline relays remain recoverable.

See [`REACHABILITY.md`](REACHABILITY.md).

## Publish the directory

Enable public directory views with:

```text
DIRECTORY_PUBLIC_LISTING_ENABLED=true
```

The principal routes are:

```text
/                          human directory
/v1/relays                 compatibility API
/v2/relays                 current rich projection
/downloads/active.txt      Tier 1 + Tier 2
/downloads/unavailable.txt Tier 3 + Tier 4
/downloads/all.txt         all public hosts
```

Public listing remains read-only and does not open lifecycle enrollment. See
[`PUBLIC-LISTING.md`](PUBLIC-LISTING.md).

## Enable relay lifecycle participation

Authenticated register/heartbeat/unregister routes are controlled by:

```text
DIRECTORY_LIFECYCLE_ENABLED=true
```

Enrollment is separate and starts closed. Inspect or change it locally:

```sh
activity-relay-directory admin enrollment status
activity-relay-directory admin enrollment open --operator operator-id
activity-relay-directory admin enrollment close --operator operator-id
```

Closing enrollment blocks registration by previously unseen relays; it does not
remove already known relays.

A successful authenticated registration is current relay-liveness evidence in
RC4, just like an explicit heartbeat for freshness purposes. Local CSV,
discovery, moderation, or presentation changes do not refresh that evidence.

The wire contract is documented in [`PROTOCOL.md`](PROTOCOL.md).

## Suspend and restore a relay

```sh
activity-relay-directory admin suspend \
  --actor https://relay.example/actor \
  --moderator operator-id \
  --reason security_review

activity-relay-directory admin restore \
  --actor https://relay.example/actor \
  --moderator operator-id \
  --reason review_complete
```

Suspension blocks register/heartbeat but does not erase lifecycle history.
Restore clears the administrative suspension; it does not itself register an
unregistered relay.

See [`MODERATION.md`](MODERATION.md).

## Review private moderation audit

```sh
activity-relay-directory admin audit \
  --actor https://relay.example/actor \
  --limit 50 \
  --format json
```

Audit output contains private moderator/reason tokens. Do not publish it without
appropriate redaction.

## Pruning, retention, and storage

Useful read-only checks include:

```sh
activity-relay-directory admin pruning dry-run --format json
activity-relay-directory admin retention dry-run --format json
activity-relay-directory admin storage status --format json
activity-relay-directory admin storage check
```

Automatic soft pruning is reversible and disabled by default. Hard inactive
retention is destructive and also defaults disabled (`0`). Review the dedicated
documents before enabling either policy:

- [`RETENTION.md`](RETENTION.md)
- [`STORAGE-GROWTH.md`](STORAGE-GROWTH.md)
- [`PERSISTENCE.md`](PERSISTENCE.md)

## Human-directory title, operator contact, and support links

The package-managed reference is:

```text
/etc/activity-relay-directory/config.yml.example
```

Copy it to the operator-owned `config.yml` if you want presentation overrides.
Typical values include:

```yaml
DIRECTORY-TITLE: "Community ActivityPub Relay Directory"
DIRECTORY-BANNER-URL: "https://directory.example/banner.webp"
OPERATOR-WEBSITE: "https://operator.example/"
OPERATOR-EMAIL: "operator@example.org"
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
SUPPORT:
  - title: "Liberapay"
    url: "https://liberapay.com/example/"
```

These are presentation-only. They do not alter tiering, lifecycle,
reachability, enrollment, or moderation state. The full validation matrix is in
[`CONFIGURATION.md`](CONFIGURATION.md).

## Service checks for package installations

```sh
sudo systemctl status activity-relay-directory
sudo journalctl -u activity-relay-directory -n 100 --no-pager
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
```

After a package upgrade, review the package/configuration and restart when ready
to load the new binary:

```sh
sudo systemctl restart activity-relay-directory
```

## Container administration

Run the local CLI against the active container/state volume:

```sh
docker compose exec directory \
  activity-relay-directory admin show \
  --actor https://relay.example/actor
```

Do not mount the same SQLite database into a second active Directory host.

## Where to look next

- configuration: [`CONFIGURATION.md`](CONFIGURATION.md)
- public projection/tiering: [`PUBLIC-LISTING.md`](PUBLIC-LISTING.md)
- discovery/reachability: [`DISCOVERY-REACHABILITY.md`](DISCOVERY-REACHABILITY.md)
- relay profiles/CSV: [`RELAY-PROFILES.md`](RELAY-PROFILES.md)
- moderation: [`MODERATION.md`](MODERATION.md)
- retention: [`RETENTION.md`](RETENTION.md)
- protocol: [`PROTOCOL.md`](PROTOCOL.md)
