# AGENTS.md

## Project identity

- Repository: `thystra/activity-relay-directory`
- Primary branch: `master`
- Public service hostname reserved for deployment: `directory.argentwolf.org`

## Safety and workflow

- Verify host, repository, branch, and worktree before mutation.
- Use prospective staging and validation before applying changes.
- Keep backups, validation reports, applicators, and other maintainer-only
  artifacts outside the tracked checkout. If `.project-local/` is used inside
  a checkout, it must be excluded locally and never committed.
- Distinguish applied, tested, committed, pushed, deployed, and released.
- Treat documentation review as part of every implemented change, milestone, and
  tranche. Before declaring work complete, compare affected Markdown, examples,
  configuration references, CLI/API descriptions, roadmap/release notes, and
  source comments with the resulting source; update stale text and verify local
  documentation links.
- Do not deploy or enable registration merely because code exists.

## Architectural invariants

- Registration is disabled by default.
- Fresh installations contain no active external directory endpoints.
- The directory stores relay-instance metadata, never connected-site or user
  identities.
- Registration, heartbeat, and unregister requests require authenticated
  signatures, content digests, bounded timestamps, nonce replay protection,
  and bounded request bodies.
- Public status terminology uses fixed version 1 healthy, stale, dead, and
  prune boundaries at 36 hours, 7 days, and 30 days of server-owned recency.
- Accepted register and heartbeat operations maintain one nondecreasing
  `last_seen_at_unix`; future values fail closed during projection.
- Moderation and administrative suspension override automated health state.
- Protocol compatibility must be versioned and tested with fixtures.

## Validation

Run before every commit:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build ./cmd/activity-relay-directory
docker build --pull=false --build-arg VERSION=validation .
```

Protocol vocabulary is closed and versioned. When a JSON field, operation,
outcome, error code, or lifecycle name changes, update `docs/PROTOCOL.md`, the
Go vocabulary, and `testdata/directory/` together. Fixtures must use reserved
example identities and must never contain production or connected-site data.

RFC 9530 digest changes must remain byte-for-byte compatible with the
Activity-Relay client implementation and `testdata/directory/v1/` fixture.
Digest verification operates on the exact bounded body bytes before JSON
decoding. It must use constant-time comparison and never echo an untrusted
field value in an error.

RFC 9421 changes must remain synchronized with `docs/PROTOCOL.md` and the
complete public-key verification vector in `testdata/directory/v1/`. Signature
errors must remain bounded and redact supplied fields, key IDs, nonces, bodies,
resolver details, and replay-backend errors. Handler code must use the combined
verification, actor-binding, and atomic-reservation path, never the stateless
verifier alone. The package-private memory replay store is test infrastructure,
not a production or multi-instance backend. The SQLite replay store is the
durable implementation for one active directory process on one host. It must
receive only opaque replay keys, enforce the version 1 ten-minute maximum
retention, retain inclusive expiry semantics, and keep automatic cleanup
bounded. Explicit cleanup bounds must remain positive and no greater than 4096.
Its existence does not authorize verifier wiring, handlers, or deployment.

Register request code must use `DecodeRegisterRequest` and
`VerifyRegisterAndReserve`. Keep the JSON body bounded, reject duplicate and
unknown names plus trailing values, require the exact version, operation,
target, and canonical identity, and complete all those gates before nonce
reservation or registration-state mutation. The contract is not authorization
to expose a handler or report registration as available.

Heartbeat request code must use `DecodeHeartbeatRequest` and
`VerifyHeartbeatAndReserve`. It must accept only canonical actor identity,
never registration metadata, and must bind the exact heartbeat
operation and target before replay reservation. Authentication success is only
a heartbeat intent: existence, suspension, rate limits, persistence, and
server-side acceptance time remain separate gates before recording liveness.

Unregister request code must use `DecodeUnregisterRequest` and
`VerifyUnregisterAndReserve`. It must accept only canonical actor identity,
never registration metadata, and must bind the exact unregister operation and
target before replay reservation. Authentication success is only a removal
intent. State transitions must be idempotent and preserve moderation and audit
history unless a separate explicit retention policy has been reviewed.

SQLite is the reviewed persistence foundation for one active directory process
on one host. Keep the database on a local filesystem and never share it between
hosts. Once a migration has been released, do not edit it: add a consecutive
numbered migration, update `CurrentSchemaVersion`, preserve content-hash drift
checks, and test fresh, idempotent, concurrent, rollback, and supported-upgrade
paths. Schema fields must not admit connected-site or user identities, raw
request bodies, raw nonces, or signing key IDs. Startup must migrate before
listening, `/readyz` must fail closed without disclosing database details, and
the container must preserve its read-only root filesystem with only the local
data volume writable. Runtime storage wiring does not authorize state-mutating
handlers or deployment.

Relay lifecycle code must use the `storage.RelayRepository` contract after all
authentication, safe-resolution, replay, and policy gates. Repository inputs
must remain canonical and bounded. Use server acceptance time, reject per-actor
time regression, block register and heartbeat while suspended, require active
registration for heartbeat, allow idempotent unregister without clearing
suspension, and commit every successful outcome with its matching append-only
event in one transaction. Backend details may be logged internally but must
never be exposed to clients. The repository is not authorization to add a
handler or report registration as available.

Administrative state changes must use `storage.ModerationRepository` and apply
only to an existing retained canonical relay; the contract is not a preemptive
blocklist. Moderator identifiers and reason codes are bounded private tokens,
never free-form notes or public response data. Suspend and restore are
idempotent, but every accepted decision receives an append-only private event.
Commit state and its event in one transaction, preserve lifecycle and
registration metadata, never let restore register a relay, and enforce
nonregressing time across lifecycle and moderation events. Local operator
commands must remain in `internal/admincommand`, require exact actor
confirmation or explicit `--yes`, use the fixed exit classes, and expose audit
records only through bounded keyset pages. Operating-system access to the
binary and owner-only SQLite data is the initial authorization boundary. Do not
add moderation HTTP routes, bearer tokens, an administrative group, or wider
container-volume permissions without a separate review.

Canonical URL syntax validation is not network-target validation. Keep DNS,
SSRF controls, redirect checks, actor retrieval, and actor-key binding as
separate explicit gates, and never echo an untrusted supplied URL in an error.

Runtime key resolution must use `internal/actorresolver.Resolver`, never the
default HTTP client or an environment proxy. Preserve all-answer DNS rejection,
public-address connection pinning, redirect revalidation, request and response
bounds, canonical fragment-bearing key IDs, exact `Application` or `Service`
actor identity, exact key ID/owner binding, and the 2048-to-8192-bit RSA limit.
Review the IANA special-purpose address registries before changing or releasing
the address policy. Resolver construction and tests do not authorize verifier
wiring, request handlers, registration availability, or deployment.

Actor-key caching must use `internal/actorresolver.CachedResolver` around the
production resolver only. Preserve canonical key-ID checks on hits, complete
key/owner/actor and RSA revalidation before insertion, a fixed positive entry
limit, a non-sliding TTL no greater than five minutes, exact expiry, LRU
eviction, nondecreasing time, isolated RSA-key copies, and success-only storage.
Never cache failures, serve stale entries, or initiate background refresh.
Cache construction does not authorize verifier wiring, handlers, registration,
or deployment.

Request admission must derive direct peers through
`internal/admission.SourceResolver`; forwarding fields are never trusted from
an unconfigured peer. Trusted prefixes identify exact proxies rather than
allowed clients, and trusted proxies must overwrite one `X-Real-IP` value.
Apply `Limiter.AdmitSource` before expensive resolution or signature work and
`Permit.AdmitActor` exactly once, only after authenticated actor binding, and
only for the same exact-route operation. Preserve closed operation-specific buckets, fixed
source/actor capacity, bounded oldest-idle
cleanup, nondecreasing time, global concurrency permits, idempotent release,
and redacted decisions. Admission construction does not authorize handler
wiring, registration availability, or deployment.

Health projection code must use `storage.ClassifyHealth` and the fixed version 1
boundaries. One caller-captured server time classifies an entire bounded page.
SQLite reads must use the `(lifecycle_state, administrative_state,
last_seen_at_unix, relay_actor)` index and keyset cursor, exclude suspended and
unregistered rows before decoding, perform no writes, and reject future
last-seen values. Do not add a public listing route or pruning transition in the
health-projection tranche.

Soft-pruning code must use `storage.PruningRepository` and remain reversible.
Candidate reads use the `(lifecycle_state, last_seen_at_unix, relay_actor)` index,
include suspension without clearing it, and are bounded to 100 rows. One run may
inspect at most 1,000 candidates against one captured server time. Revalidate
registered state and the exact 30-day cutoff inside the same immediate
transaction that writes `pruned_at_unix` and `relay_pruned`; heartbeat or
register races must prevent pruning. Preserve first registration, moderation,
and all audit events, support cancellation, require at least a one-hour enabled
interval, and never hard-delete. Public adapters must exclude health `prune`
independently of scheduler completion. Keep the scheduler default-off and out of
all HTTP handlers; the local dry-run command must use an existing current-schema
query-only connection and perform no database creation, migration, or writes.

Hard-retention code must use the separate purge vocabulary and keep
`DIRECTORY_INACTIVE_RETENTION_DAYS=0` as indefinite/default. Only active
`unregistered` or `pruned` rows may be candidates, ordered by their current
inactive-transition timestamp; registered or suspended rows must never pass the
destructive predicate. Bind a candidate to row update time and the latest
lifecycle/moderation event IDs and revalidate all of them under the immediate
write transaction so even idempotent concurrent decisions prevent a stale
delete. Keep pages <=100 and one run <=1,000 candidates.

Never delete `moderation_events` in inactive retention. `relay_events` deletion
may bypass its append-only trigger only transaction-locally, with the trigger
recreated before commit and rollback restoring it on every failure. Keep the
aggregate retention audit identity-free: create it before scanning, checkpoint
committed destructive counts in the same purge transaction, and make it
immutable when finalized. No public HTTP handler or automatic scheduler may
invoke hard purge. Purge must preflight an existing current-schema database
read-only and must not create or migrate its target. Every destructive local run
must verify a secure same-database current-schema backup before confirmation and
re-verify the same digest immediately after confirmation; `--yes` must not
bypass those checks. `VACUUM`/manual checkpoint work remains explicit operator
maintenance outside purge/request transactions.

Database-growth safeguards are non-destructive. Keep the default family budget
at 1 GiB unless a separately reviewed compatibility change alters it; warning is
configurable, while critical/hard remain fixed at 90/100. All production runtime
mutators must receive the real `WriteAdmission` guard and acquire it before
`BeginTx`/direct mutation; `storage.AllowWrites` is test-only and read-only
repositories use `storage.DenyWrites`. Hard state must not trigger retention,
pruning, replay deletion, `VACUUM`, or any other automatic data removal. It must
fail readiness and domain/application writes while preserving `/healthz`, allowed
public reads, and local read-only storage inspection. Sampling failure is also a
readiness/write-admission failure until a successful authoritative sample.

The only reviewed post-hard SQLite control/storage operations are: an `UPDATE`
of the bounded singleton `storage_growth_state` row used to persist growth/mail
transition bookkeeping, and `PRAGMA wal_checkpoint(PASSIVE)`. Neither changes
logical domain data. The singleton must contain no relay/operator/audit identity
and may only be updated by the growth guard; the passive checkpoint may only
flush already-committed WAL pages and must never become a truncating checkpoint
or `VACUUM` in the runtime path. These exceptions do not authorize any relay,
enrollment, moderation, pruning, retention, replay, migration, or other domain
mutation after hard state. Static review must keep them narrow.

Growth notifications are opt-in only. Never invoke a shell, accept option-like
or control-character recipients, expose command output in operator errors, or
persist relay/audit identities in alert state. Keep reminder/retry state bounded
and restart-safe. The stock container/package must not invent a recipient,
credentials, relay host, or notification enablement. See
`docs/STORAGE-GROWTH.md`.

Public directory presentation must use the same `httpapi.PublicListingHandler`
projection for JSON and human-readable output. Do not add a second HTML-specific
repository query, health classifier, moderation filter, or eligibility rule.
`GET`/`HEAD` `/` and its bundled static assets remain under the same default-off
`DIRECTORY_PUBLIC_LISTING_ENABLED` gate as `/v1/relays`. HTML must use Go
`html/template`, automatic escaping, local assets only, a strict CSP, the same
bounded authenticated cursor and one-minute cache policy, and no relay-provided
HTML, scripts, fonts, analytics, or relay-controlled image fetches. Health-state
meaning must never depend on hue alone: retain a visible state word plus a
distinct non-color visual cue, preserve automated light/dark text-contrast
coverage, and treat color-vision-deficiency simulation as a review diagnostic
rather than a substitute for operator browser review.

The container workflow must use the reviewed Node-24-compatible Docker action
majors `docker/setup-buildx-action@v4` and `docker/build-push-action@v7`. Keep the
static workflow regression test when copying or updating CI scaffolding so the
retired Node-20 majors are not reintroduced.

Lifecycle HTTP code must use `httpapi.LifecycleHandler` and remain disabled by
default. `DIRECTORY_LIFECYCLE_ENABLED=true` gates register, heartbeat, and
unregister together and requires an HTTPS public base URL. Durable enrollment
defaults closed and gates only first-time relay rows. A
disabled or incomplete graph must not construct the resolver or mutate state
and must report lifecycle unavailable. Preserve this exact order: method and
route, trusted direct-peer source identity, source admission, bounded body read,
operation-specific combined verification and durable replay reservation,
actor admission exactly once, server acceptance time, then repository mutation.
Release the concurrency permit on every exit. Public errors and messages must
use the fixed mapping in `docs/HANDLERS.md` and redact bodies, targets, signature
fields, resolver details, replay details, storage details, and moderation data.
Trusted proxy prefixes must be explicit, canonical, bounded, and interpreted as
proxies rather than client allowlists. Handler availability does not authorize
deployment, client activation, public listing, pruning, or an operator endpoint.

When changing a reverse-proxy example, validate it with the corresponding
Nginx, Apache HTTP Server, or Caddy release. Examples must preserve the public
host and request target, remain optional, and must not install, enable, reload,
or otherwise configure an operator's proxy automatically.

## AI-assisted maintenance

AI-assisted tools may help draft or review changes. The human maintainer must
inspect the result, approve the design, execute validation, control releases
and deployments, and remain accountable for the software.

## Release discipline

- The pre-1.0 candidate line is `v0.1.0-rcN`, with embedded application version
  `0.1.0-rcN` and Debian version `0.1.0~rcN-<revision>`.
- Debian prerelease versions intentionally retain `~` (for example,
  `0.1.0~rc1-1`), while embedded application versions and public artifact names
  use `-` (for example, `0.1.0-rc1`). Keep the Debian version itself unchanged.
- **Bash tilde substitution is a packaging trap:** when translating a Debian
  prerelease version with Bash `${parameter//pattern/replacement}`, do not use a
  bare `~` pattern such as `${value//~/-}`. Escape the literal pattern as
  `${value//\~/-}`. This applies to Debian-to-application version translation
  and public artifact filename translation.
- Any applicator or release builder that creates or changes this translation
  must include a focused regression assertion before the package build. At a
  minimum, prove that `0.1.0~rc1` becomes `0.1.0-rc1`; do not rely only on a
  later package-version assertion to discover this shell-expansion error.
- The first stable release is `v1.0.0`, not `v0.1.0`; its initial Debian
  version is `1.0.0-1`. Do not publish a `v0.1.0` final tag.
- Forgejo is authoritative. Push/PR package jobs are validation only. Canonical
  release bytes are produced once by the manually dispatched Forgejo release
  workflow from an exact reviewed commit, then those same bytes are promoted to
  Forgejo and downstream GitHub release surfaces.
- Keep the canonical RC workflow candidate-generic rather than hard-coding a
  previously published `rcN`. Its requested `0.1.0-rcN` must match the top
  Debian changelog version after the reviewed literal `~` to `-` translation,
  must have a matching `docs/releases/v<version>.md` draft, and must require an
  exact `BUILD <version>` confirmation plus exact reviewed commit identity.
- Treat `workflow_dispatch` strings as shell data, never shell source. Map
  `${{ inputs.* }}` expressions into workflow environment variables and quote
  those variables inside `run:` scripts; do not interpolate dispatch inputs
  directly into trusted release shell bodies before validation.
- Keep release-workflow tool preflight self-contained. Validate dispatch
  identity before dependency installation, explicitly install the Debian tools
  used by the workflow (including `dpkg-dev` for `dpkg-parsechangelog`), then
  validate the source package version before Go/build/package work.
- The canonical RC artifact set must include both supported installation
  paths from that same exact commit: the Debian `.deb` and a Docker-loadable
  `linux/amd64` image archive tagged with the exact application version. The
  archive, `.deb`, standalone binary, SBOM, build metadata, and checksum file
  are one canonical set; do not rebuild the container separately for GitHub or
  publication.
- Before an RC tag or release is published, independently install-test the
  exact canonical `.deb` and container archive. Use separate writable state
  for the two tests; never point both installations at the same SQLite file.
- Installation, service activation, reverse-proxy exposure, lifecycle/public
  listing activation, tagging, and release publication are separate gates.
- Debian package installation must not enable/start the service, invent a mail
  recipient, configure credentials/relay hosts, enable lifecycle/public listing,
  activate positive retention, or raise the reviewed database-growth budget.
- Package upgrades must not stop or restart an operator-activated service
  automatically. Keep the Debian helper policy equivalent to
  `--no-stop-on-upgrade`; loading a newly installed binary into an active
  deployment requires an explicit operator-controlled restart after upgrade
  validation. Package removal may stop the service normally.
- Package removal and purge preserve `/var/lib/activity-relay-directory`; state
  destruction requires a separate explicit operator action after backup review.
- The release builder owns the Debian Lintian policy gate. Invoke Lintian with
  `--show-overrides --fail-on none`, capture its output, fail on any
  unoverridden `E:` or `W:` finding, and fail on any nonzero Lintian exit after
  that explicit policy selection. Lintian exit status `2` is a `--fail-on`
  policy result rather than a runtime failure; never let ambient runner/local
  Lintian configuration choose the release policy implicitly.

## Validation control maturity

- Match validation depth to the maturity and risk of the function being
  validated. New, destructive, security-sensitive, authority,
  data-integrity, and release-immutability boundaries should begin
  fail-closed with strong independent checks.
- Once a function or infrastructure component has been independently proven
  and is operating reliably, retire redundant implementation-level checks and
  rely on the appropriate higher-level contract or outcome unless there is a
  documented reason for continued strict validation.
- Do not retain stale or duplicative controls merely because they were useful
  during initial validation. False failures, brittleness, and avoidable rework
  are themselves operational risks.
- If unusually strict or redundant checks are retained after a function is
  proven, document the specific reason and the condition or review point for
  relaxing them.
- Example: after Forgejo runner identity, service behavior, capacity, and
  isolation have been proven, repository gates should validate the expected
  runner labels and required workflow/job outcomes rather than repeatedly
  re-auditing runner UUIDs, PIDs, restart counters, journals, or DinD
  internals without a new reason to suspect them.

## Release-candidate operator acceptance

- Development validation and release-candidate acceptance are different
  activities. Development validation should be automated, implementation-aware,
  and exhaustive; RC acceptance should be an operator-guided checklist focused
  on real commands, assembled behavior, and human-visible outcomes.
- RC checklist outcomes are `PASS`/`YES`, `FAIL`/`NO`, `RETRY`, optional
  `SKIP`/`N/A`, and `ABORT`. A retry reruns only the current check.
- `FAIL`/`NO` is evidence, not automatically an abort. Unless a check is
  explicitly marked critical, record the failure and supporting evidence, then
  allow the operator to retry, continue, or abort so one session can discover
  the complete set of RC issues.
- Critical checks are identified before execution and stop automatically when
  continuing would be unsafe or meaningless, such as wrong artifact identity,
  service-start/state-open failure, wrong TLS authority, corruption, or a
  destructive/security boundary failure.
- Checklist execution success and RC disposition are separate. A completed
  checklist may still end `NO-GO`; final reports must summarize critical
  failures, noncritical failures, retries, skips, operator notes, and the final
  disposition.
- Human-facing surfaces require human review during RC acceptance even when
  development tests already prove HTTP status, CSS/CSP delivery, DOM markers,
  or API state. Integration RCs should walk the documented real command
  sequence and ask the operator to confirm the resulting public/admin behavior.
- Keep host-neutral checklist semantics in `docs/RC-ACCEPTANCE.md`; private
  hostnames, credentials, and operational transcripts remain outside the public
  repository.
