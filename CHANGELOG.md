# Changelog

## Unreleased

_No changes yet._

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
