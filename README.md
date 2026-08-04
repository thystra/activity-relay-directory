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

The directory protocol is being introduced in reviewed contract-first tranches.
The current vocabulary and fixtures do not activate request handlers. No live
directory endpoint is built into Activity-Relay by default.

## Privacy boundary

The directory is intended to learn about relay instances, not the identities
or membership of sites connected to those relays. Registration payloads must
not include connected-site identities, relay followers, or user information.

## Development

```sh
export DIRECTORY_PUBLIC_BASE_URL=http://127.0.0.1:8080
go test -count=1 ./...
go run ./cmd/activity-relay-directory
```

Container development:

```sh
cp .env.example .env
docker compose up --build
```

## Licence

GNU Affero General Public License version 3. See `LICENCE`.

## Maintenance transparency

Development may use AI-assisted tooling for drafting, analysis, testing, and
review support. A human maintainer reviews and approves changes, runs release
gates, controls deployments, and remains accountable for the project.
