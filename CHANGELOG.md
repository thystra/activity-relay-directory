# Changelog

## 1.3.1-rc2 - 2026-10-09 (release candidate)

- Clarify ActivityPub Actor and ActivityPub Inbox labels and display explanations
  for DNS, connection, HTTPS/TLS, redirect, Actor and Inbox checks.
- Use three human-facing heartbeat labels (Healthy, Stale, No heartbeat
  received) and consistent warning styling while retaining all five existing
  API heartbeat values, tier classification, and pruning behavior.
- Display the recorded Inbox check time instead of a blanket technical warning;
  identify HTTP 405/501 as an unsupported check, not delivery failure.
- Name existing opt-in lifecycle participation **ARD White Pages Protocol**
  and replace numbered public tier headings with descriptive relay groups.
- Update current operator and public documentation. No new migrations, API
  schema changes, lifecycle protocol changes, or worker scheduling changes.

## 1.3.1-rc1 - 2026-10-08 (release candidate)

- Add schema 14 durable actor-failure retry streak with escalating hourly
  eligibility and a reset on recovery; make persisted next-check authoritative.
- Treat healthy authenticated heartbeat as independent positive liveness when
  actor retrieval fails: Tier 2 and online summary include heartbeat-only relays.
- Preserve actor failure diagnostics and the existing seven-day long-offline
  recovery cadence; no changes to lifecycle protocol signatures.

## 1.3.1 - 2026-10-07

- Add stage/code/HTTP-status reachability diagnostics for DNS, connection,
  TLS, redirects, actor documents, and non-mutating inbox OPTIONS responses.
- Persist bounded diagnostic evidence independently of lifecycle/heartbeat
  and tiering in SQLite schema 13, including the earliest eligible recheck.
- Expand `/v2/relays` schema 6 and the human relay details with classified
  diagnostics and cautious "possibly removed" annotations for actor 404/410
  and absent DNS names. No automatic removal or change to `/v1/relays`.
- Redact raw network/TLS errors and distinguish prohibited targets from
  ordinary outages so unreachable discovery candidates can be retained.

## 1.3.0 - 2026-10-06

- Promote the accepted `1.3.0-rc4` behavior to stable `1.3.0` without runtime
  behavior changes.
- Add source-scoped descriptive relay profiles with per-field
  `override > relay > csv` precedence, bounded CSV import/export, and private
  append-only profile history.
- Add lifecycle Protocol v2 profile synchronization and Protocol v3 bounded
  participating-instance telemetry while preserving Protocol v1 compatibility.
- Treat every successful authenticated registration as current liveness evidence
  and drive public heartbeat freshness from the retained heartbeat timestamp.
- Extend `/v2/relays` to schema 5 with reviewed profile/telemetry projection,
  registration badges/filtering, and the existing four operational tiers.
- Allow CSV metadata replacement for exact already-known lifecycle/discovery
  identities even when their current actor probe fails; unknown candidates still
  require normal validation or explicit dead-candidate retention.
- Promote the package-managed `config.yml.example`, Directory title/banner and
  support presentation, concise operator documentation layout, and deterministic
  canonical artifact carrier.

## 1.3.0-rc4 - 2026-10-05

- Add lifecycle Protocol v3 and schema 12 participating-instance telemetry.
  Protocol v3 stores `participating_instance_count` separately from the rc3
  receiving-instance field, while public **Sites** prefers v3 telemetry and
  falls back to the v2 value during rolling upgrades.
- Treat every successful authenticated registration as current relay-liveness
  evidence: create, unchanged/profile-sync, and restore registrations now advance
  the retained heartbeat timestamp. CSV/discovery/profile-only local writes do
  not affect heartbeat evidence.
- Drive public heartbeat health and the human **Last heartbeat** value from the
  retained heartbeat timestamp rather than generic lifecycle `last_seen`.
- Advertise lifecycle versions `[1,2,3]` when v3 is available and add a shared
  cross-repository signed v3 registration fixture.
- Install the package-managed operator example as
  `/etc/activity-relay-directory/config.yml.example` without creating or
  overwriting the operator-owned `config.yml`. Disabled YAML settings omit the
  space after `#`, so enabling one requires deleting only that character.
- Add bounded `DIRECTORY-TITLE` and HTTPS `DIRECTORY-BANNER-URL` presentation
  settings with a correspondingly bounded image CSP.
- Refine public presentation: remove the row-level heartbeat glyph while keeping
  the `♥ Heartbeat` heading and use distinct Open/Restricted/Closed/Not reported
  registration badge states.
- Improve CSV import preflight with an explicit processing notice, read-only
  field-level profile-change preview, confirmation text covering profile
  changes, and concise human `profile_changes=N|none` results while retaining
  detailed JSON mutation counters.
- Allow CSV profile replacement for an exact already-known active lifecycle or
  discovery identity even when its current actor probe fails; unknown candidates
  still require the normal validation or explicit dead-candidate retention path.

## 1.3.0-rc3 - 2026-10-04

- Add schema 11 bounded relay telemetry storing a self-reported
  `receiving_instance_count` and Directory receipt time without changing tier,
  reachability, moderation, enrollment, pruning, or eligibility.
- Publish telemetry through `/v2/relays` schema 5 and show **Sites** directly on
  the human Directory row. Move detailed heartbeat/check timestamps into the
  expanded relay details.
- Add registration-status badges, filtering, and within-tier ordering
  `open` -> `restricted` -> `closed` -> unreported.
- Restrict Protocol v2 relay `participation_mode` to the closed vocabulary
  `open`, `restricted`, `closed` (or empty/unasserted); malformed remote input is
  rejected before lifecycle/profile/telemetry mutation.
- Harden hostile-client handling with bounded telemetry, strict JSON/duplicate
  member rejection, parameterized telemetry persistence, SQL-metacharacter
  regression coverage, escaped public rendering, and no-shell administrator
  notification tests.

## 1.3.0-rc2 - 2026-10-04

- Reorganize the human relay profile into `Relay information` and `Relay focus`,
  display `participation_mode` as `Registration status` and `participation_url`
  as `About this relay`, and suppress the language/country/region focus
  subsection when none of those values are declared. Protocol/API field names
  remain unchanged.
- Add an optional collapsed `Support this directory` block backed by up to eight
  provider-neutral `SUPPORT` entries containing a title and exactly one HTTPS
  URL or escaped plain-text value. Support metadata remains presentation-only
  and is excluded from directory/status APIs and operational decisions.

## 1.3.0-rc1 - 2026-10-03

- Add lifecycle Protocol v2 profile synchronization with separately signed v2
  routes, complete relay-owned profile replacement on register, identity-only
  heartbeat/unregister, status-schema-4 capability advertisement, and a shared
  cross-repository signed fixture while preserving Protocol v1 compatibility.
- Publish the effective reviewed relay profile in `/v2/relays` schema 4 and on
  the human Directory page while keeping profile provenance/history private and
  keeping descriptive metadata independent from operational tiering and public
  eligibility.
- Add bounded relay-profile CSV import/export with explicit `--input-format csv`,
  deterministic escaped-semicolon list fields, spreadsheet formula-injection
  neutralization, and source-scoped writes through the schema-10 profile
  repository without changing the existing line import or hosts/actors exports.
- Add schema 10 source-scoped relay profile persistence with per-field
  `override > relay > csv` precedence, bounded canonical values, append-only
  private history, and no effect on lifecycle, moderation, reachability, or
  public eligibility.
- Preserve executable mode and deterministic release timestamps across Forgejo
  artifact download by uploading the canonical release tree inside a
  metadata-normalized tar carrier; canonical public asset bytes and their
  `SHA256SUMS` remain unchanged.

- Show the running application version in the public-facing Directory footer,
  using the same runtime build identity exposed by `/v1/status`.
- Carry forward release-validation verifier-authority, clean-room, and rollback
  safety rules from the retired documentation branch, including authoritative
  object checks, project-neutral baselines, offline rollback, and continuation
  from observed state after partial mutations.

## 1.2.0 - 2026-10-03

- Promote the accepted `1.2.0-rc2` behavior to stable `1.2.0` without runtime
  behavior changes.
- Add resilient relay discovery imports with retained unavailable candidates,
  staged retry/promotion maintenance, and already-known classification.
- Add four public operational tiers with a 30-day Graveyard, alphabetical
  ordering inside each tier, and aggregate online/offline/pending counts on the
  public-facing Directory page.
- Add local tier-scoped exports and public plain-text relay downloads.
- Preserve the V1 Protocol and `/v1/relays` compatibility while extending
  `/v2/relays` to schema 3 and the database to schema 9.
- Reload systemd unit definitions during Debian package configuration without
  automatically restarting an operator-activated Directory.

## 1.2.0-rc2 - 2026-10-03

- Move verified relays from Tier 3 into the Tier 4 Graveyard after 30 days
  without trustworthy online evidence instead of 180 days; weekly recovery
  checks continue and no relay is deleted by this presentation change.
- Expand the human Directory summary with aggregate verified relay, online,
  offline, and pending-verification candidate counts while keeping unresolved
  candidate identities and provenance private.
- Reload systemd unit definitions during Debian package configuration without
  starting or restarting the Directory; loading an upgraded binary remains an
  explicit operator-controlled restart.

## 1.2.0-rc1 - 2026-10-03

- Rewrite the project README around installation, relay discovery, tiered
  directory use, administration, and operator documentation instead of an
  implementation-status checklist.
- Make packaged local administrator commands load missing `DIRECTORY_*`
  settings from `/etc/default/activity-relay-directory`, so normal shell use
  does not require manually sourcing the systemd environment file.
- Accept bare relay host names in discovery add/import input by treating them
  as HTTPS relay candidates while continuing to reject explicit HTTP URLs.
- Accept ActivityStreams `Group` actors as relay actors, matching deployed
  relay software such as Fedibird.
- Distinguish duplicate input from relay identities that are already active in
  lifecycle or discovery state during file imports, and skip redundant
  discovery writes for those already-known identities.
- Add `--add-dead-relays` to discovery imports so unreachable or incompatible
  relay candidates can be retained for later rechecks without treating them as
  verified relay actors.
- Retry retained unavailable relay candidates after 6 hours, 12 hours, 24 hours,
  3 days, and then weekly; promote a candidate to a normal verified discovery
  only after its canonical actor validates successfully.
- Continue periodic recovery checks for known unavailable relays, reducing
  long-term offline actors to weekly checks instead of dropping them.
- Rank the richer public directory into four operational tiers: current
  heartbeat plus online, online without a current heartbeat, unavailable, and a
  180-day graveyard. Relays remain alphabetical within each tier, and the v2
  public response schema/cursor now include the tier key.
- Add read-only relay exports for operators and public host-list downloads,
  with active, unavailable, and all-known scopes that preserve tier ordering
  and can be fed back into discovery import.
- Accept bare discovery hosts with explicit non-default HTTPS ports, allowing
  exported `host:port` entries to round-trip through discovery import.

## 1.1.0 - 2026-09-16

- Promote the accepted `1.1.0-rc2` behavior to stable `1.1.0` without runtime
  behavior changes.
- Add operator-controlled relay discovery and independent reachability checks
  while keeping the V1 Protocol and `/v1/relays` compatible with 1.0.
- Add the default-off V2 public API at `/v2/relays` with richer discovery and
  reachability information.
- Improve the human-facing directory with compact responsive relay rows and
  Previous/Next navigation.
- Preserve Directory state and the dedicated service account during normal
  package upgrade or removal; explicit purge remains the destructive package
  operation, and operator-owned `config.yml` remains outside package conffiles.

## 1.1.0-rc2 - 2026-09-16

- Scale the human directory into compact responsive relay rows with clearer
  heartbeat/reachability presentation, while keeping the underlying v2 evidence
  model and frozen `/v1/relays` compatibility unchanged.
- Add signed bidirectional pagination to the human directory so operators and
  visitors can move to previous as well as next pages; `/v2/relays` remains
  forward-only.
- Make Debian package lifecycle boundaries explicit: ordinary removal and
  upgrade preserve directory state and the dedicated system account, while
  purge is the destructive boundary. Operator-owned `config.yml` remains
  outside the package conffile set.
- Validate the generated Debian maintainer scripts and package ownership
  boundaries from the built artifact, and require Debian Trixie with
  `debhelper >= 13.25` for canonical package construction.
- Converge Forgejo build, package, test, and canonical-release jobs on the
  shared `forgejo-workstation` execution profile.

## 1.1.0-rc1 - 2026-09-15

- Add default-off `GET /v2/relays` public 1.1 evidence projection and switch
  the human directory to the same bounded actor-keyset projection while keeping
  `/v1/relays` byte/semantic compatibility. Heartbeat, actor reachability,
  inbox diagnostics, and positive RFC 9421 evidence remain independent, and
  discovery provenance stays private.
- Add default-off bounded background reachability maintenance with fair oldest-check-first scheduling, fixed hourly/six-hour policy, at most 96 actors per run and eight concurrent probes.
- Protect soft pruning with fresh current reachability plus a fail-closed process-local complete-coverage gate; reachability never rewrites authenticated heartbeat recency.
- Add schema version 8 discovery/reachability persistence with private
  operator-discovery provenance, independent actor/inbox observations,
  positive-only RFC 9421 evidence, and retention-policy version 2 integration.
- Add local `admin discovery add|remove|import` commands. Candidate actor checks
  reuse the proxy-free SSRF-resistant resolver, local file imports are bounded
  and prospective-first, and inbox diagnostics use non-mutating `OPTIONS` only.
- Keep discovery provenance private and lifecycle heartbeat recency unchanged;
  operator discovery never fabricates registration or RFC 9421 participation.
- Add footer links advertising the Activity-Relay Directory and Activity-Relay
  source repositories on the human directory page.

## 1.0.0 - 2026-09-02

- Publish the first stable Activity-Relay Directory release directly from the
  accepted `0.1.0-rc4` runtime line; there is no final `0.1.0` release.
- Record successful live integration with Activity-Relay 3.0, including two
  independently registered relays, natural daily heartbeat refresh, public
  health projection, and authenticated unregister/re-register lifecycle
  acceptance.
- Keep lifecycle, public listing, automatic soft pruning, positive inactive
  retention, and administrator email disabled by default; durable enrollment
  remains closed by default.
- Generalize the canonical Forgejo artifact workflow to accept stable semantic
  versions while retaining the reviewed pre-1.0 RC form and exact-commit gate.
- No Go runtime behavior changes from the accepted RC4 source.

## 0.1.0-rc4 - 2026-08-22

- Correct stale `docs/PUBLIC-LISTING.md` wording so semantic Nice-to-have
  operator-value failures and incomplete Fediverse pairs are documented as
  non-blocking suppression plus human-page diagnostics, matching the already
  accepted implementation.
- Record RC3 as an accepted-runtime but unpublished NO-GO candidate and carry
  its machine/browser acceptance evidence forward to focused RC4 revalidation.
- No Go runtime behavior changes from the accepted RC3 source.

## 0.1.0-rc3 - 2026-08-13

- Add optional public operator website, email, and explicit Fediverse contact
  links from a strict presentation-only `config.yml`; absent values are fully
  suppressed and the public admin email is never inferred.
- Keep `GET /v1/relays` as the supported bounded JSON API while removing JSON
  navigation from the human page.
- Remove the standalone privacy-boundary presentation panel while retaining the
  same bounded public projection and privacy tests.
- Carry forward the accepted RC2 application-owned CSP, bundled stylesheet,
  color-independent health cues, and operator-centric acceptance contract.

## 0.1.0-rc2 - 2026-08-12

- Fix reverse-proxy examples so they do not override the Directory's
  route-specific Content-Security-Policy and block its bundled stylesheet.
- Refresh the human public directory with the Activity-Relay visual language,
  responsive relay cards, clearer health context, and an intentional empty
  state while preserving the shared public projection and no-JavaScript
  privacy boundary.
- Reinforce public health states with visible text, distinct symbols and border
  styles, automated light/dark contrast coverage, and color-vision-deficiency
  review diagnostics so status meaning never depends on hue alone.
- Document operator-centric RC acceptance: noncritical `FAIL`/`NO` results are
  recorded and normally continue, while explicitly critical failures abort.
- Generalize the canonical release-candidate workflow so an exact `0.1.0-rcN`
  input must match the source-controlled Debian changelog and versioned release
  draft instead of hard-coding a previously published candidate.
- Harden manual release-workflow input handling by passing dispatch strings as
  quoted environment data instead of interpolating them directly into shell
  script bodies before candidate/commit confirmation.
- Make canonical release preflight independent of ambient runner packages by
  explicitly installing `dpkg-dev` before parsing the Debian changelog and
  validating the source candidate version before build work.

## 0.1.0-rc1 - 2026-08-11

### Added

- Schema version 7 bounded database-growth state plus a non-destructive 1 GiB
  default SQLite family budget, common pre-write admission, fixed 90/100
  critical/hard thresholds, readiness refusal, local storage status/check/test
  commands, bounded WAL/page-allocation policy, and opt-in no-shell administrator
  notification with restart-safe transition/reminder/retry state.
- Schema version 6 inactive-record retention with a persistent database identity,
  indexed active-inactive candidate reads, strict default-zero policy parsing,
  identity-free local dry-run, backup-gated confirmed purge, bounded
  transactionally revalidated batches, retained moderation evidence, and
  crash-safe transactionally checkpointed aggregate retention-run audits.
- Human-readable `GET`/`HEAD` `/` directory view rendered from the same bounded public
  projection as `/v1/relays`, with shared authenticated pagination, bundled
  same-origin styling, automatic HTML escaping, strict CSP, and matching cache
  validators.
- Initial Go service scaffold.
- Health, readiness, and schema-versioned status endpoints.
- Strict environment configuration validation.
- Non-root, read-only container runtime.
- Test and container-build workflows.
- Version 1 lifecycle, outcome, error, health, and administrative vocabulary.
- Strictly decoded JSON request and response contract fixtures.
- Canonical HTTPS relay actor/public-base normalization and origin binding.
- RFC 9530 SHA-256 Content-Digest generation, verification, and fixtures.
- Stateless RFC 9421 directory-request verification and RSA fixture.
- Atomic opaque-key nonce reservation and replay-rejection contracts.
- Strict bounded registration parsing, target binding, and authenticated composition.
- Strict bounded heartbeat parsing, target binding, and authenticated composition.
- Strict bounded unregister parsing, target binding, and authenticated composition.
- Single-node SQLite opener with secure file checks and bounded connection settings.
- Transactional, content-hashed initial persistence migration for relay state,
  opaque replay reservations, and append-only lifecycle events.
- Required SQLite startup migration, database-backed readiness, graceful close,
  and persistent owner-only Compose data volume.
- Backend-neutral relay repository contract and atomic SQLite register,
  heartbeat, unregister, and append-only audit transitions.
- Durable SQLite RFC 9421 replay reservations with atomic conflict handling,
  restart persistence, ten-minute retention enforcement, and bounded cleanup.
- SSRF-resistant ActivityPub actor and RSA signing-key resolver wired into the
  explicitly enabled lifecycle graph, with pinned public DNS targets, redirect
  revalidation, bounded documents, and strict actor ownership.
- Optional Nginx, Apache, and Caddy reverse-proxy examples.
- GitHub funding links aligned with Activity-Relay.
- Local audited enrollment-policy administration commands.
- Local `admin suspend`, `restore`, `show`, and bounded `audit` moderation
  commands with exact confirmation, JSON output, and fixed exit classes.
- Backend-neutral private moderation state and audit-read contracts with indexed
  SQLite keyset pagination.
- Transactional schema version 4 with deterministic `last_seen_at_unix`
  backfill and an indexed bounded health-projection read model.
- Fixed version 1 health classification at 36 hours, 7 days, and 30 days with
  fail-closed future-time handling and suspended/unregistered exclusion.
- Transactional schema version 5 with reversible `pruned` lifecycle state,
  `pruned_at_unix`, append-only `relay_pruned` events, and an indexed candidate
  scan that preserves suspension and audit history.
- Bounded soft-pruning coordinator with transactional eligibility revalidation,
  cancellation, a fixed 1,000-candidate-attempt run budget, a one-hour minimum
  scheduling interval, and default-off automatic maintenance.
- Local read-only `admin pruning dry-run` with bounded keyset pagination and
  stable human/JSON output.
- Node-24-compatible Docker Buildx and Build Push GitHub Actions majors with a
  regression test against reintroducing the retired versions.
- Default-off version 1 public JSON relay listing with indexed public-eligibility
  filtering, opaque observation-pinned keyset pagination, deterministic UTC
  fields, strong ETags, a one-minute cache policy, and independent bounded
  concurrency.

### Security

- Lifecycle registration, heartbeat, and unregister routes are disabled by
  default; durable enrollment also starts closed.
- Non-loopback public URLs require HTTPS, and enabled lifecycle requires HTTPS.
- Lifecycle request-body limits are configurable but always bounded.
- Database initialization and schema mismatch fail before the HTTP listener starts.
- Readiness failures do not disclose database errors or filesystem paths.
- Relay transitions reject noncanonical identities, backward acceptance time,
  absent or suspended heartbeat targets, and suspended registration.
- State mutation rolls back when its corresponding audit event cannot commit.
- Replay cleanup rolls back when the associated reservation cannot commit.
- Actor resolution rejects prohibited mixed DNS answers, proxy routing,
  excessive redirects, ambiguous JSON, key substitution, and unsafe key forms.
