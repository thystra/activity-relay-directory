# Activity-Relay Directory

Activity-Relay Directory is an independent directory and health service for
discovering public Activity-Relay instances.

## Current state

This repository currently provides a conservative service scaffold only:

- `GET /healthz`
- `GET /readyz`
- `GET /v1/status`
- strict configuration validation
- registration disabled by default
- no registration, heartbeat, unregister, listing, moderation, or pruning API
- version 1 protocol vocabulary and JSON compatibility fixtures
- network-free canonical relay identity and URL validation
- network-free RFC 9530 SHA-256 Content-Digest generation and verification
- stateless RFC 9421 directory-request verification contracts and fixture
- atomic opaque-key replay-store contract and concurrency-tested reference
- strict authenticated registration-request contract without a public handler
- strict authenticated heartbeat-request contract without liveness storage
- strict authenticated unregister-request contract without state deletion
- startup SQLite migration and database-backed readiness checks
- atomic register, heartbeat, and unregister state transitions without handlers
- durable opaque replay reservations with bounded expiry cleanup
- bounded SSRF-resistant ActivityPub actor and signing-key resolution with a
  success-only key cache, without handler wiring
- bounded two-stage source, authenticated-actor, and concurrency admission
  without handler wiring

The directory protocol is being introduced in reviewed contract-first tranches.
The current vocabulary and fixtures do not activate request handlers. No live
directory endpoint is built into Activity-Relay by default. The signature
and replay contracts are not sufficient to enable registration until resolver
composition, moderation, admission wiring, handler composition, and the remaining
request gates are implemented. The process opens and migrates a single-node
SQLite database before listening; dormant repositories can apply audited
lifecycle transitions and durable opaque replay reservations, but no public
request handler calls them. The actor resolver is also dormant and no runtime
component initiates actor retrieval. See `docs/PERSISTENCE.md` and
`docs/RESOLUTION.md`. The dormant in-memory admission policy is documented in
`docs/ADMISSION.md`.

## Privacy boundary

The directory is intended to learn about relay instances, not the identities
or membership of sites connected to those relays. Registration payloads must
not include connected-site identities, relay followers, or user information.

## Development

```sh
mkdir -p data
chmod 0700 data
export DIRECTORY_PUBLIC_BASE_URL=http://127.0.0.1:8080
export DIRECTORY_DATABASE_PATH="$PWD/data/directory.sqlite"
go test -count=1 ./...
go run ./cmd/activity-relay-directory
```

Container development:

```sh
cp .env.example .env
docker compose up --build
```

Optional, host-neutral Nginx, Apache, and Caddy examples are under `contrib/`.
They are not installed or enabled automatically. See
`docs/REVERSE-PROXY.md` before adapting one for a public deployment.

## Licence

GNU Affero General Public License version 3. See `LICENCE`.

## Maintenance transparency

Development may use AI-assisted tooling for drafting, analysis, testing, and
review support. A human maintainer reviews and approves changes, runs release
gates, controls deployments, and remains accountable for the project.
