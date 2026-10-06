# Developing Activity-Relay Directory

This is the short entry point for contributors. Repository rules live in
[`../AGENTS.md`](../AGENTS.md), architecture in
[`../ARCHITECTURE.md`](../ARCHITECTURE.md), and active work in
[`../TODO.md`](../TODO.md).

## Build and test

Run formatting checks before committing:

```sh
gofmt -w <changed-go-files>
test -z "$(gofmt -l .)"
```

Run the normal Go validation:

```sh
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build ./cmd/activity-relay-directory
```

For a local container build:

```sh
docker build --pull=false --build-arg VERSION=development .
```

Always finish with:

```sh
git diff --check
```

Release/package work has additional gates. Follow
[`RELEASING.md`](RELEASING.md) rather than treating the commands above as a
release acceptance substitute.

## Safe local state

Use disposable development databases. Do not point tests at a production
Directory database. SQLite is designed for one active Directory process on one
host; tests that exercise service behavior should use independent state paths.

For loopback development startup, see [`INSTALLATION.md`](INSTALLATION.md).

## Protocol and HTTP work

Relevant references are:

- [`PROTOCOL.md`](PROTOCOL.md) — lifecycle protocol versions and signatures;
- [`HANDLERS.md`](HANDLERS.md) — HTTP handler/error behavior;
- [`SECURITY.md`](SECURITY.md) — security boundaries;
- [`RESOLUTION.md`](RESOLUTION.md) — actor/key resolution;
- [`PERSISTENCE.md`](PERSISTENCE.md) — SQLite state and migrations;
- [`PUBLIC-LISTING.md`](PUBLIC-LISTING.md) — public projection contract; and
- [`RELAY-PROFILES.md`](RELAY-PROFILES.md) — profile source precedence and CSV.

Protocol v1 compatibility and the frozen `/v1/relays` contract must not be
silently changed by later-version work. Additive protocol/API changes belong in
new negotiated/versioned surfaces as documented by the applicable design.

## Release and acceptance work

Use:

- [`RELEASING.md`](RELEASING.md) for canonical artifacts, package/container
  gates, versioning, and publication;
- [`RC-ACCEPTANCE.md`](RC-ACCEPTANCE.md) for operator-visible RC acceptance;
- [`STORAGE-GROWTH.md`](STORAGE-GROWTH.md) for database-budget validation; and
- versioned files under [`releases/`](releases/) for published/RC behavior.

Forgejo is the authoritative development and release repository. GitHub is a
public mirror/independent-validation surface.

## Reverse-proxy examples

Host-neutral Nginx, Apache, and Caddy examples live under `contrib/`. Validate a
changed example with the actual server/parser when practical; do not treat
example configuration as runtime code owned by ARD.

## Documentation ownership

Keep the top-level README concise. Put deployment procedures in
`INSTALLATION.md`, operator command examples in `ADMINISTRATION.md`, and detailed
protocol/storage/security material in the existing topic documents. Developer
and release mechanics should not be moved back into the project overview.
