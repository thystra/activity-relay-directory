# Installing Activity-Relay Directory

This guide covers the normal package, Docker Compose, and source installation
paths. Configuration details are in [`CONFIGURATION.md`](CONFIGURATION.md), and
day-to-day operator commands are in [`ADMINISTRATION.md`](ADMINISTRATION.md).

ARD is conservative by default: lifecycle mutation, public listing, background
reachability, and automatic pruning are disabled until explicitly enabled.

## Debian/Ubuntu package

Install the release package with your normal package tool, for example:

```sh
sudo apt install ./activity-relay-directory_VERSION_amd64.deb
```

The package installs:

- the `activity-relay-directory` system account;
- owner-only state under `/var/lib/activity-relay-directory`;
- `/etc/default/activity-relay-directory`;
- `/etc/activity-relay-directory/config.yml.example`;
- a hardened systemd unit; and
- packaged operator/implementation documentation.

It deliberately does **not** create the operator-owned live
`/etc/activity-relay-directory/config.yml`, and it does not enable or start the
service automatically.

Review the package defaults first:

```sh
sudoedit /etc/default/activity-relay-directory
```

If you want the optional human-directory presentation settings, copy the
package-managed reference once and edit the copy:

```sh
sudo cp -n \
  /etc/activity-relay-directory/config.yml.example \
  /etc/activity-relay-directory/config.yml
sudoedit /etc/activity-relay-directory/config.yml
```

The example remains package-managed and may be refreshed on upgrade. The live
`config.yml` remains operator-owned.

Enable and start only when the configuration is ready:

```sh
sudo systemctl enable --now activity-relay-directory
sudo systemctl status activity-relay-directory
```

Package upgrades reload systemd unit definitions but do not automatically
restart an already-running Directory. Restart after reviewing the upgraded
configuration/package when you are ready to load the new binary:

```sh
sudo systemctl restart activity-relay-directory
```

Normal package removal preserves the state directory and service account.
Package purge is destructive. See `debian/README.Debian` before removal, purge,
or downgrade.

## Docker Compose

Copy the environment example and edit the deployment values:

```sh
cp .env.example .env
editor .env
```

Then start the service:

```sh
docker compose up --build -d
docker compose ps
docker compose logs --tail=100 directory
```

The included deployment keeps its public listener loopback-oriented by default
and does not enable lifecycle or public listing automatically.

Persist the Directory state volume. Do not run two active Directory services
against one SQLite database.

For local administration in a container deployment, use the same service
container and state volume:

```sh
docker compose exec directory \
  activity-relay-directory admin storage status --format json
```

## Source / development start

For a loopback development instance:

```sh
mkdir -p data
chmod 0700 data

export DIRECTORY_PUBLIC_BASE_URL=http://127.0.0.1:8080
export DIRECTORY_DATABASE_PATH="$PWD/data/directory.sqlite"
export DIRECTORY_PUBLIC_LISTING_ENABLED=true

go run ./cmd/activity-relay-directory
```

Then open:

```text
http://127.0.0.1:8080/
```

HTTP public-base URLs are for loopback development only. A public lifecycle
deployment uses HTTPS.

## Public reverse proxy

A public installation should normally keep the application listener private and
terminate HTTPS at Nginx, Apache, Caddy, or another reviewed reverse proxy.

See [`REVERSE-PROXY.md`](REVERSE-PROXY.md) for the supported proxy boundary,
forwarded-client handling, and examples under `contrib/`.

Do not expose private local administration through a reverse proxy. ARD does not
provide a public moderation/admin API.

## First-start checks

Check service health locally:

```sh
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
```

When public listing is enabled, inspect the human page and API:

```sh
curl --fail http://127.0.0.1:8080/v2/relays
```

When lifecycle support is enabled, inspect the advertised capabilities:

```sh
curl --fail http://127.0.0.1:8080/v1/status
```

Opening enrollment is a separate operator decision; enabling lifecycle routes
does not automatically allow previously unseen relays to register.

## Configuration entry points

Runtime/service configuration is primarily controlled through environment
settings such as:

```text
DIRECTORY_PUBLIC_BASE_URL
DIRECTORY_DATABASE_PATH
DIRECTORY_LISTEN_ADDRESS
DIRECTORY_PUBLIC_LISTING_ENABLED
DIRECTORY_LIFECYCLE_ENABLED
DIRECTORY_REACHABILITY_ENABLED
DIRECTORY_SOFT_PRUNING_ENABLED
DIRECTORY_INACTIVE_RETENTION_DAYS
```

The complete state/error matrix is in
[`CONFIGURATION.md`](CONFIGURATION.md). The retired
`DIRECTORY_REGISTRATION_ENABLED` name is rejected rather than accepted as an
alias.

The optional YAML file is presentation-oriented and supports values such as
Directory title/banner, operator contact, Fediverse contact, and support links.
Its package-managed reference is `config.yml.example`.

## Backups and upgrades

Before destructive retention, purge, or a schema-affecting downgrade, back up
the SQLite state using the documented database procedure. In-place database
downgrade is unsupported.

Review [`PERSISTENCE.md`](PERSISTENCE.md), [`RETENTION.md`](RETENTION.md), and
[`STORAGE-GROWTH.md`](STORAGE-GROWTH.md) before changing storage lifecycle
policy.
