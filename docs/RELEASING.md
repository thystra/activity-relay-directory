# Releasing

## Versioning and authority

The pre-1.0 release-candidate series is `v0.1.0-rcN`, with embedded version
`0.1.0-rcN` and Debian version `0.1.0~rcN-<revision>`. The first stable release
will jump directly to `v1.0.0` / `1.0.0` with Debian
`1.0.0-1`. Do not publish a `v0.1.0` final tag.

Forgejo is authoritative. `.forgejo/workflows/package.yml` and the GitHub
package workflow are validation only. `.forgejo/workflows/release.yml` is a
manual, exact-commit artifact gate: it builds the canonical candidate bytes
once and stores them as one Forgejo Actions artifact. That set includes both
supported installation paths: the Debian package and a Docker-loadable
`linux/amd64` image archive tagged `activity-relay-directory:0.1.0-rc1`.
After the exact artifact set is independently install-tested through both
paths, a later publication gate tags the same commit and promotes those exact
bytes to Forgejo and GitHub release surfaces. GitHub runners do not manufacture
a second official release set or rebuild the container image.

## Debian package contract

The first candidate package is `activity-relay-directory` version `0.1.0~rc1-1` for
application `0.1.0-rc1`. It installs a dedicated system account, owner-only
`/var/lib/activity-relay-directory`, `/etc/default/activity-relay-directory`,
the binary, documentation, and a hardened systemd unit. Debhelper is invoked
with `dh_installsystemd --no-enable --no-start --no-stop-on-upgrade`.
Fresh package installation must leave the unit disabled and inactive, while a
package upgrade must not stop or restart an operator-activated service. Loading
the newly installed binary into an active deployment is a separate,
operator-controlled restart gate after upgrade validation.

Fresh package defaults bind only to `127.0.0.1:8080`, use a loopback public
base URL, and keep lifecycle, public listing, automatic soft pruning, positive
inactive retention, and administrator email disabled. Installing the package
does not configure Nginx/Apache/Caddy, DNS, recipients, credentials, or a mail
relay. Activation and public exposure are later explicit gates.

Package removal and purge intentionally preserve the SQLite state directory
and dedicated system account. Purge removes dpkg-managed conffiles but does not
destroy `/var/lib/activity-relay-directory`; destructive state removal requires
a verified backup and explicit operator action. In-place database downgrade is
unsupported and requires restoring the backup matching the older binary.

The public candidate artifact set consists of the `.deb`, the exact packaged
standalone binary, CycloneDX JSON SBOM, build metadata, the Docker-loadable
`activity-relay-directory_0.1.0-rc1_linux_amd64.docker.tar`, and one
`SHA256SUMS` covering all five public assets. Loading the archive with
`docker load` must produce image tag `activity-relay-directory:0.1.0-rc1`.
`.changes`, `.buildinfo`, package control scripts, Lintian output, and package
inventory are retained as build evidence rather than promoted as end-user
release assets.

RC acceptance requires independent installation tests of the exact canonical
`.deb` and the exact canonical Docker archive before tagging or publication.
Give the two tests separate SQLite state and separate bind ports if they run
concurrently; they must not share one writable database.


Before the first release:

1. define versioning and compatibility policy;
2. add deterministic binary and container builds;
3. add SBOM and checksum generation;
4. validate clean installation and upgrade behavior;
5. document database backup and migration behavior;
6. perform an integration soak with relay2;
7. verify that registration remains disabled unless explicitly configured;
8. publish release notes and rollback instructions.

SQLite is active during process startup and readiness checks. Explicitly
enabled lifecycle handlers write audited registration, heartbeat, unregister,
and replay state. Before the first release, test fresh creation, idempotent
restart, named-volume persistence, upgrade from every supported schema version,
backup restoration, and refusal of drifted or future schemas. Release notes
must identify the resulting schema version and state that downgrade requires
restoration of the matching pre-upgrade backup.

Schema version 3 adds default-closed enrollment policy and private append-only
enrollment audit events after schema version 2's moderation events. Before
releasing it, verify supported upgrade preservation, atomic state/event rollback,
idempotent suspend and restore concurrency, audit backup restoration, and that
moderator and reason tokens are absent from public output. The local operator
CLI must retain its operating-system authorization and private-audit boundary;
any network administrative transport requires a separate authorization and
audit review.

Before replay-protected handlers are released, validate duplicate suppression
across restart and supported service topology, expiry-boundary replacement,
bounded cleanup scheduling, failure rollback, and the reviewed admission policy
under sustained unique traffic.

Signed lifecycle handlers are present but disabled by default. Before their
first deployment, validate both disabled and explicitly enabled startup,
`lifecycle_available`, `enrollment_open`, exact proxy peer derivation, all HTTP mappings in
`docs/HANDLERS.md`, real Activity-Relay signatures for all three operations,
nonce rejection across restart, suspension behavior, fixed admission bounds,
maintenance cancellation, database backup/restore, and logs for data leakage.
Enabling the server does not activate an Activity-Relay client.

Before the first deployment or release of the enabled actor-resolution path,
compare the prohibited-address policy with the current IANA IPv4 and IPv6
special-purpose registries. Validate
mixed public/private DNS answers, direct literals, connection pinning, custom
HTTPS ports, redirects, proxy exclusion, timeouts, header/body limits,
ActivityStreams media types, duplicate/deep JSON, actor/key ownership, both RSA
PEM forms, cancellation, and public error redaction.
Before activating a positive inactive-retention policy, first deploy/upgrade with
`DIRECTORY_INACTIVE_RETENTION_DAYS=0`, take and restore-test a fresh pre-retention standalone
SQLite backup, then capture identity-free dry-run evidence for the proposed
policy. Exercise exact 1-day/365-day boundaries, suspended and registered
exclusion, stale-candidate concurrency, interrupted/restarted batches, migration
rollback, backup mismatch rejection, exact pre-purge restoration, and append-only
guard restoration. A destructive trial must use the backup-gated local command;
no HTTP route or scheduler may initiate purge. Physical `VACUUM`/checkpoint work
is a separate maintenance operation.

## Database-growth release gate

Before the first release candidate, exercise the Tranche 17 storage guard with
email disabled and with an isolated fake/local test mailer. Validate exact
warning/critical/hard boundaries, five-minute and pre-write sampling,
`max_page_count` across supported SQLite page sizes, WAL/checkpoint growth,
freed-page reuse, near-limit migration rollback, concurrent writers, restart
notification suppression, recovery hysteresis, bounded failure retry, and
full-disk-class refusal. At hard state prove `/healthz` and allowed public/local
reads remain available while `/readyz` and every runtime mutation fail closed.

The stock container deliberately does not install a mail command. A deployment
that enables `DIRECTORY_ADMIN_EMAIL` must explicitly provide and configure the
validated command. A future Debian package may recommend a mail transport, but
must not configure recipients, credentials, relay hosts, or enable alerts. Host
filesystem monitoring remains required independently of the application budget.
No release/deployment gate may silently raise the configured database budget or
activate positive inactive retention.

## RC Go compatibility gate

The pre-RC compatibility pass establishes Go 1.26.0 as the minimum supported
Directory module floor. `go.mod` must declare `go 1.26.0` and must not add a
higher `toolchain` directive. The blocking CI matrix runs exact Go 1.26.0 and
the validated Go 1.26.5 patch lane. A separate Go 1.27rc2 forward-compatibility
job is independently scheduled so it may execute concurrently with the stable
lanes when runner capacity permits; prerelease validation failures must remain
visible for individual triage but do not silently redefine the supported floor
or stable blocking lanes.

Container builds use the validated
`docker.io/library/golang:1.26.5-alpine3.24` builder while preserving the
reviewed runtime stage. After Go 1.27 final is available, rerun full
compatibility before changing the documented supported floor or ongoing CI
matrix.

After the Directory pass is complete, apply the same deliberate Go-version
compatibility/floor review to `thystra/Activity-Relay`, including its
interoperability fixtures and release/container/package builds.

## Debian Lintian exceptions

The project-owned Debian package keeps Lintian strict: every unoverridden
error or warning is a release-build failure. The release builder explicitly
invokes `lintian --show-overrides --fail-on none` and owns the policy decision
itself: captured `E:` or `W:` findings fail the build, while documented `O:`
override findings remain visible review evidence. With the explicit
`--fail-on none` selection, any nonzero Lintian exit is treated as a
runtime/unexpected failure. Lintian exit status `2` is a `--fail-on` policy
result, not a runtime error; selecting `none` prevents runner-local or
version/configuration-specific fail-on defaults from changing release behavior.

The package carries four narrow binary overrides with comments:

- `statically-linked-binary` is intentional because the daemon is built with
  `CGO_ENABLED=0` as a self-contained Go release binary;
- `non-standard-file-perm` is intentionally scoped only to
  `/etc/default/activity-relay-directory`: this operator-editable systemd
  environment file is kept root-owned and mode `0640` so deployment-local
  values are not world-readable; `debian/rules` runs normal `dh_fixperms`
  first and then restores only this file to `0640`;
- `initial-upload-closes-no-bugs` does not apply because these artifacts are
  published on the Activity-Relay project release surfaces rather than as an
  initial upload to the Debian archive; and
- `copyright-without-copyright-notice` reflects the upstream repository-wide
  AGPL/contributor ownership model rather than per-source-file copyright
  headers.

Do not add a Lintian override merely to make a release gate green. Resolve a
finding normally when the package can reasonably comply, as with the packaged
manual page and Debian changelog line wrapping in the first RC.
