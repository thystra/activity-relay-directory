# Releasing


### 1.1 acceptance prerequisite

Before producing canonical 1.1 release bytes or validating release packages,
run:

    ./scripts/acceptance-1.1.sh

The acceptance runner is intentionally separate from packaging and deployment.
It pins the released `v1.0.0` tag and schema-7 migration history and exercises
the reviewed Tranche 23 discovery/reachability acceptance matrix. A passing
acceptance matrix is required before canonical release bytes, package/container
validation, publication, deployment, activation, or production verification.


## Versioning and authority

The historical pre-1.0 release-candidate series is `v0.1.0-rcN`, with
embedded version `0.1.0-rcN` and Debian version
`0.1.0~rcN-<revision>`. The first stable release jumps directly to `v1.0.0` /
`1.0.0` with Debian `1.0.0-1`; there is no final `v0.1.0` tag.

After 1.0.0, release candidates use normal semantic-version prerelease
identity: `vX.Y.Z-rcN` / `X.Y.Z-rcN` with Debian
`X.Y.Z~rcN-<revision>`. Stable releases remain `vX.Y.Z` / `X.Y.Z` with the
normal Debian revision form.

Forgejo is authoritative. `.forgejo/workflows/package.yml` and the GitHub
package workflow are validation only. `.forgejo/workflows/release.yml` is a
manual, exact-commit artifact gate: it builds the canonical candidate bytes
once and stores them as one Forgejo Actions artifact. That set includes both
supported installation paths: the Debian package and a Docker-loadable
`linux/amd64` image archive tagged
`activity-relay-directory:<application-version>`. After the exact artifact set
is independently install-tested through both paths, a later publication gate
tags the same commit and promotes those exact bytes to Forgejo and GitHub
release surfaces. GitHub runners do not manufacture a second official release
set or rebuild the container image.

## Release source identity

Every release is a new source state and immutable version; do not move or
rebuild an already-published tag. The top `debian/changelog` entry is the source
of truth for the current Debian version. The embedded/public application
version is that upstream Debian version with a literal prerelease `~`
translated to `-` when present.

The manually dispatched Forgejo canonical workflow is release-generic but
fail-closed. Its `version` input must:

- match the historical pre-1.0 form `0.1.0-rcN`, or for major version 1
  or greater match either release candidate `X.Y.Z-rcN` or stable `X.Y.Z`;
- equal the application version derived from the top Debian changelog entry;
- have a matching `docs/releases/v<version>.md` draft;
- use exact confirmation `BUILD <version>`; and
- build the exact reviewed `expected_commit`.

The workflow derives filenames and the Docker image/archive identity from that
validated source version. A later release therefore requires a normal reviewed
source/version-preparation commit, but not a workflow edit merely to replace one
hard-coded version with another.

Treat manual dispatch strings as data rather than shell source. Map
`workflow_dispatch` input expressions into workflow environment variables and
quote those variables in every `run:` body. Candidate/commit validation must
occur on those environment values; do not embed raw `${{ inputs.* }}` text in
trusted release shell scripts.

The canonical workflow must not depend on ambient runner packaging tools.
Validate dispatch identity first, explicitly install the Debian tools it uses
(including `dpkg-dev` for `dpkg-parsechangelog`), then validate the changelog
candidate version before Go setup, package construction, or container work.


## Debian package contract

Pre-1.0 release-candidate packages use `activity-relay-directory` with Debian
version `0.1.0~rcN-<revision>` for application `0.1.0-rcN`. For major version 1
or greater, release candidates use application `X.Y.Z-rcN` with Debian
`X.Y.Z~rcN-<revision>`. Stable packages use the normal Debian revision form,
for example application `1.0.0` with package version `1.0.0-1`. The package
installs a
dedicated system account, owner-only
`/var/lib/activity-relay-directory`, `/etc/default/activity-relay-directory`,
the binary, documentation, and a hardened systemd unit. Debhelper is invoked
with `dh_installsystemd --no-enable --no-start --no-stop-on-upgrade`.

Debian package construction requires debhelper 13.11.6 or newer. Debhelper 13.11.4, as shipped in Debian Bookworm, does not generate the required systemd maintainer-script integration for units under `/usr/lib/systemd/system`; Forgejo Bookworm packaging jobs therefore obtain a fixed debhelper from `bookworm-backports` and the release builder rejects older versions.
Fresh package installation must leave the unit disabled and inactive, while a
package upgrade must not stop or restart an operator-activated service. Loading
the newly installed binary into an active deployment is a separate,
operator-controlled restart gate after upgrade validation.

Fresh package defaults bind only to `127.0.0.1:8080`, use a loopback public
base URL, and keep lifecycle, public listing, automatic soft pruning, positive
inactive retention, and administrator email disabled. Installing the package
does not configure Nginx/Apache/Caddy, DNS, recipients, credentials, or a mail
relay. Activation and public exposure are later explicit gates.

Ordinary package removal intentionally preserves the SQLite state directory
and dedicated system account so a later reinstall retains the instance.
Package purge is the explicit destructive package-lifecycle boundary: after a
verified backup, purge removes `/var/lib/activity-relay-directory` and the
dedicated system user/group while dpkg removes package-managed conffiles.
Operator-owned `/etc/activity-relay-directory/config.yml` is not a package
conffile and is not deleted by package maintainer scripts. In-place database
downgrade is unsupported and requires restoring the backup matching the older
binary.

The public canonical artifact set consists of the `.deb`, the exact packaged
standalone binary, CycloneDX JSON SBOM, build metadata, a Docker-loadable
`activity-relay-directory_<application-version>_linux_amd64.docker.tar`, and
one `SHA256SUMS` covering all five public assets. Loading the archive must
produce image tag `activity-relay-directory:<application-version>`. `.changes`,
`.buildinfo`, package control scripts, Lintian output, and package inventory
are retained as build evidence rather than promoted as end-user release assets.

Release acceptance requires independent installation tests of the exact
canonical `.deb` and exact canonical Docker archive before tagging or
publication.
Give the two tests separate SQLite state and separate bind ports if they run
concurrently; they must not share one writable database.


The 1.0.0 acceptance program completed the following first-release gates:

1. define versioning and compatibility policy;
2. add deterministic binary and container builds;
3. add SBOM and checksum generation;
4. validate clean installation and upgrade behavior;
5. document database backup and migration behavior;
6. perform an integration soak with relay2;
7. verify that registration remains disabled unless explicitly configured;
8. verify natural daily heartbeats plus authenticated unregister/re-register
   lifecycle behavior against the live Directory; and
9. publish release notes and rollback instructions.

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

## Operator acceptance

Development validation and operator acceptance are separate gates.
Development automation should prove low-level implementation contracts first.
For an assembled release candidate or stable build, follow `docs/RC-ACCEPTANCE.md`
and record operator-visible outcomes, retries, noncritical failures, critical
aborts, and the final `GO` / `GO WITH NOTES` / `NO-GO` disposition.

Do not treat successful checklist execution as automatic release approval, and
do not convert every recorded noncritical `FAIL` / `NO` result into a script
abort.
