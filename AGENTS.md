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

## Applicator and verifier defect prevention

Applicators, validators, and release verifiers are part of the release-safety
boundary. A false failure is still operationally expensive, and a brittle
verifier can encourage unnecessary rebuilds or repeated mutation. Treat defects
in these tools as durable engineering lessons rather than one-off script fixes.

- **Record every newly discovered applicator/verifier defect in `AGENTS.md`.**
  When an applicator, validator, builder, or release verifier causes a false
  failure, misses a required failure, mutates more than intended, or requires a
  corrective retry, classify the defect and add the lesson here before that
  corrective work is considered complete. Record at least the failure
  signature, root cause, prevention rule, and a focused regression guard.
  Do not rely on chat/session memory. If the affected candidate/source is
  already frozen and must remain immutable, carry the documentation update in
  the next legitimate source-changing candidate rather than modifying frozen
  bytes.
- **Preserve literal bytes across language/escaping layers.** When Python,
  shell, YAML, Make, or heredocs generate or match another language, reason
  about every parser involved. In particular, a backslash immediately followed
  by a physical newline inside a normal Python string literal is consumed as a
  Python source-line continuation. Do not use such a non-raw triple-quoted
  string to match Makefile or shell continuation bytes. Use a raw string,
  explicit escaped bytes, or a structural parser/edit, and add a focused
  self-test proving the generated matcher contains the intended literal
  backslash/newline sequence.
- **The Bash Debian-tilde rule remains mandatory.** The existing
  `${value//\~/-}` requirement and focused `0.1.0~rcN` -> `0.1.0-rcN`
  regression guard apply to every applicator/builder that performs that
  translation. Do not reintroduce bare `${value//~/-}`.
- **Prefer structural edits over presentation-sensitive text anchors.** Avoid
  whitespace-, indentation-, wrapping-, or prose-sensitive multiline matchers
  when a parser, AST boundary, keyed YAML field, exact line, or other stable
  structure is available. If exact-text replacement is genuinely appropriate,
  require exactly one match against the pinned base before writing and fail
  closed without leaving a partial source mutation.
- **Do not couple version identity to incidental changelog dates.** A prior
  RC3-preparation applicator correctly pinned the RC2 source commit but then
  falsely required the RC2 `CHANGELOG.md` heading to use a guessed date. The
  pinned source used a different valid date, causing a pre-mutation false
  failure. When a prior release heading is needed as an anchor, select exactly
  one heading by release/version identity and validate only the date *shape*
  unless the calendar date itself is part of the contract. Add a regression
  that proves the matcher accepts the pinned base heading without hard-coding a
  guessed date.
- **Normalize generated text file endings.** A later RC3-preparation retry
  appended `"\n"` to raw triple-quoted Markdown additions that already ended in
  a newline, producing `git diff --check` failures for "new blank line at EOF".
  When generating or appending text, normalize to exactly one final newline
  unless the file format explicitly requires otherwise. Run `git diff --check`
  immediately after the source transform, before dependency downloads or
  expensive builds, and add a focused regression for the affected generated
  files so the same extra-EOF-newline defect cannot recur.
- **Normalize prose before semantic verifier assertions.** An RC3-preparation
  static verifier required the sentence fragment `failure signature, root
  cause, prevention rule, and a focused regression guard` to occur on one
  physical Markdown line even though the generated `AGENTS.md` wrapped that
  sentence across lines. This caused a false failure after the source transform
  and whitespace checks had already passed. For documentation/prose semantics,
  normalize runs of whitespace before matching or use a parser/section-aware
  assertion. Reserve exact byte/line matching for syntax whose physical layout
  is itself part of the contract. Add a focused regression proving semantically
  identical wrapped prose passes the verifier.
- **Do not exact-match formatter-owned source spacing.** An RC3-preparation
  verifier looked for `defaultOperatorConfigPath = "..."` as an exact Go source
  substring after `gofmt`. Because `gofmt` aligned assignments inside the
  `const` block with additional spaces, the semantic constant was present but
  the verifier falsely failed. For Go or other formatter-owned source, prefer a
  language parser/AST; when a focused lexical check is sufficient, tolerate
  insignificant whitespace around tokens and anchor the complete construct.
  Add a regression using the formatter's actual aligned output.
- **Verifier self-tests must inspect real state and must not be vacuous.** While
  reviewing the same RC3 static gate, a latent assertion was found that built a
  string containing the very hard-coded changelog date it then asserted was
  absent, which would have guaranteed a later false failure. Another assertion
  checked an empty synthetic collection and therefore always passed without
  validating anything. Do not use tautological, contradictory, or vacuous
  self-tests. Assertions must inspect the actual file/tree/data they claim to
  validate, and focused regression tests must prove both a known-good case and,
  where practical, the historical bad case.
- **Embedded interpreter syntax checks are not runtime dependency checks.**
  An RC3-preparation verifier introduced `re.search()`/`re.findall()` into an
  embedded Python block but forgot `import re`. The block compiled successfully,
  so compile-only applicator self-validation did not detect the runtime
  `NameError`. Every embedded Python/Perl/Ruby/etc. verifier must declare the
  modules/names it uses and receive a focused dependency-closure check. At
  minimum, inspect each embedded block for module-qualified calls and require
  the matching import; when practical, execute the verifier against the
  prospective tree or a representative fixture before handing the applicator to
  the operator. Do not describe `compile()` as proof that an embedded verifier
  is runnable.
- **Historical failure evidence must use reachable, prevalidated markers.**
  An RC3-preparation retry attempted to classify a prior embedded-Python
  `NameError` by requiring a `...=PASS` line that would only have been printed
  *after* the statement that raised the exception. The historical classifier
  therefore failed before any source work even though the prior defect
  diagnosis was correct. When binding a failed report, require only markers
  that were actually reachable at or before the recorded failure point. Before
  handing a retry to the operator, evaluate every proposed evidence marker
  against the exact report being bound; do not ship an untested historical
  marker list. Where order matters, also verify that the failure signature
  follows the prerequisite markers rather than inferring reachability from
  source code alone.
- **Do not invent or abbreviate source symbols in verifier checks.** An
  RC3-preparation verifier checked for a guessed `TestReleaseWorkflow` symbol,
  while the pinned Go source actually declared the more specific
  `TestCanonicalReleaseWorkflowDispatchInputsAreShellData` and
  `TestCanonicalReleaseWorkflowInstallsDebianToolsBeforeUse` tests. The
  verifier therefore failed despite inspecting the real file. Before asserting
  a function/type/key/field name, derive it from the pinned source or a
  language-aware symbol inventory; do not infer a plausible umbrella name from
  memory. For Go source, extract real `func` declarations or use the parser/AST
  and assert the exact reviewed symbols. Add a regression that proves the
  historical guessed symbol is absent while the reviewed real symbols are
  present.
- **Do not make terminal-captured report bytes an authority boundary.** Exact
  SHA-256 binding is appropriate for deliberately generated immutable
  artifacts, patches, manifests, and stable evidence files. Terminal captures
  may contain OSC/control sequences, presentation bytes, or other environment
  noise even when their semantic content is unchanged. For such reports, bind
  stable semantic markers and independently re-prove the current authority,
  security, data-integrity, or pristine-state boundary before mutation. Use a
  raw report-byte hash as a blocking gate only when the report format itself is
  deliberately deterministic.
- **Avoid early-consumer pipelines under `set -o pipefail`.** A producer such
  as `dpkg-deb --contents` can receive SIGPIPE when a downstream `head`, early
  `grep`, or similar consumer exits successfully. Capture complete producer
  output first, verify the producer status, then parse the retained output.
- **Keep machine-readable stdout clean.** If a helper's stdout is consumed by
  command substitution or another parser, progress/status logging must go to
  stderr. Do not let network/progress text contaminate captured SHA, ID, JSON,
  or other machine-readable values.
- **Self-validate the applicator before handing it to the operator.** At
  minimum run shell syntax checks on the outer script and generated shell
  bodies, compile embedded Python, and add focused regression checks for any
  escaping, matcher, parser, or report-evidence behavior that previously
  failed. When practical, apply the exact transform first to a detached
  prospective tree and require the resulting tree/patch to match the staged
  worktree exactly.

A retry caused by an applicator/verifier bug is not complete merely because the
next script works. The durable prevention rule and regression guard must be
captured here so future applicators can avoid repeating the same class of
failure.

### R2K transaction, anchor, and diagnostic regression

- **Preflight every transform before the first source write.** R2K wrote six
  real-worktree files and only then reached a line-wrap-sensitive
  `docs/PUBLIC-LISTING.md` anchor whose semantic text was present but whose
  physical wrapping differed. For a multi-file transform, resolve and validate
  every source anchor and expected output in memory or in a detached prospective
  tree before writing any real source path. Documentation prose anchors must use
  whitespace-normalized semantics or stable section boundaries rather than
  incidental line wrapping. A focused regression must accept the historical R2J
  `does` / `not install` line break and prove a failed preflight leaves the
  source tree byte-identical.

- **An `EXIT` cleanup trap is not a source transaction by itself.** R2K
  correctly preserved the original exit status and cleaned Docker/tmp resources,
  but it had no source rollback after direct writes to the real feature
  worktree. Once real source mutation begins, the transaction guard must be able
  to restore the exact verified starting index and worktree, including an
  explicitly accepted dirty/partial state. Prefer to complete all transformation
  and validation in a detached prospective tree, self-test the resulting patch,
  and make the real worktree mutation one final transactional step.

- **Failing assertions must identify the invariant that failed.** R2K's bare
  Python `assert` produced only `AssertionError` plus an embedded line number,
  forcing a second source-level reconstruction to identify the bad
  `PUBLIC-LISTING.md` matcher. Applicator/verifier failures must name the file or
  subsystem, the expected invariant, and useful observed state such as match
  count or value. Focused historical-failure tests should verify the diagnostic
  itself as well as the pass/fail result.

### R2L recovery-fixture reconstruction regression

- **Recovery fixtures must reproduce historical bytes, not intended semantics.**
  R2L correctly refused to overwrite an unauthenticated dirty worktree, but its
  manually retyped model of R2K's `README.md` mutation omitted one blank line.
  R2K had concatenated an anchor ending in a newline with a triple-quoted string
  beginning with two physical newlines, so the current on-disk README was the
  exact historical R2K output and R2L falsely rejected it. When authenticating a
  failed applicator's partial state, replay the exact historical transformation
  when it is available and compare byte-for-byte; do not simplify whitespace
  because the expected mutation bytes are the contract.

- **Inventory the full current dirty state before rejecting recovery.** A
  recovery gate must report every accepted dirty path plus expected and actual
  hashes, and on mismatch should identify all differing paths rather than stop at
  the first one. Current on-disk bytes are evidence to authenticate and preserve,
  not something to normalize speculatively. Any mismatch outside an explicitly
  recognized historical state still fails closed before source mutation.

### R2M generated-text whitespace regression

- **Generated text has an explicit whitespace contract.** R2M correctly
  authenticated the historical R2K partial state and completed its detached
  transform preflight, but its `AGENTS.md` append produced a second newline at
  EOF and `git diff --check` rejected the prospective tree. For normal LF text
  files generated or rewritten by an applicator, require exactly one final
  newline, reject trailing spaces/tabs, and reject unexpected CR bytes unless
  the file's established format explicitly requires otherwise. Validate these
  invariants in memory before the first write and run `git diff --check`
  immediately after writing the detached prospective result.

- **Use explicit newline helpers instead of visually ambiguous concatenation.**
  Do not combine `rstrip()` with triple-quoted additions and an extra `"\n"`.
  Bare `rstrip()` can also remove spaces or tabs that were not intended to be
  normalized. Use `rstrip("\n")` when only EOF newlines are being normalized,
  and use a reviewed helper for section appends that deliberately constructs the
  required blank line between sections plus exactly one final newline. Focused
  helper regressions must cover zero, one, and multiple final newlines, wrapped
  prose, trailing horizontal whitespace, and blank-line-at-EOF rejection.

- **Whitespace significance depends on the artifact.** Normalize Markdown and
  other prose for semantic matching, but do not normalize byte-sensitive shell,
  Make, patch, checksum, fixture, or recovery evidence. Exact-byte and semantic
  comparisons are separate tools; choose the one that matches the actual
  contract rather than treating all whitespace as either significant or
  insignificant.

### Repository-state authority and Git checkpoints

- **Never guess the state of a repository.** Before a transform or recovery,
  inspect the actual checkout, branch, HEAD, index tree, staged patch, unstaged
  paths, untracked paths, and any remote authority state that matters to the
  gate. Chat/session memory, a different checkout, a previously uploaded source
  archive, or the expected result of a failed applicator is not a substitute for
  current repository evidence. If the required checkout cannot be inspected, or
  the observed state does not match an explicitly accepted state, stop and ask
  the operator for a fresh source snapshot/report rather than reconstructing or
  guessing the missing bytes.

- **Use Git as the recovery boundary after validation, not before it.** Detached
  worktrees and prospective trees remain the preferred place for unvalidated
  transforms. After an exact tree has passed the required validation and human
  review, a local commit may be used as an explicit rollback/checkpoint boundary;
  a bad later commit can then be abandoned, reset, or reverted according to the
  reviewed workflow. Do not commit merely to make an unvalidated transform
  easier to recover, and do not push, tag, publish, or deploy a checkpoint until
  the corresponding release gate explicitly authorizes it.

- **Different checkouts are different evidence.** A clean primary `master`
  checkout proves the base checkout only; it does not prove the state of a
  separate feature worktree. Always name which checkout supplied each branch,
  tree, index, or worktree observation.

### Cross-interpreter byte semantics

- **Do not assume shell-visible text equals bytes written to a file.** Shell
  parsing, quoted versus unquoted heredocs, command substitution, Python string
  escapes, raw strings, Make continuations, YAML quoting, and formatter behavior
  can all alter bytes between the applicator source and the target file. Model
  every interpretation layer explicitly and keep file generation in one language
  where practical.

- **Quoted heredocs are the default for literal embedded source.** Use a quoted
  delimiter such as `<<'PY'` or `<<'EOF'` when the body must not undergo shell
  expansion. If expansion is intentionally required, make that boundary narrow
  and add a byte-level regression for the resulting file. Embedded interpreter
  blocks must compile, declare/import their runtime dependencies, and when they
  generate byte-sensitive syntax must be exercised against a focused fixture.

- **Round-trip byte tests belong next to historical escaping failures.** The
  existing Debian-tilde and Python raw-string/backslash-newline lessons remain
  mandatory. Applicators that cross shell/Python/heredoc/Make boundaries must
  add focused tests proving the intended literal bytes survive those layers;
  successful display in a terminal is not sufficient evidence.

### R2N container-copy mode and diagnostic regression

- **Declare final-image file modes explicitly when the mode is part of the contract.**
  R2N built its detached worktree under `umask 077`; Git records only executable
  versus non-executable for ordinary files, while `git worktree add` may create a
  non-executable file as mode `0600` under that umask. Docker `COPY` can preserve
  the build-context filesystem mode, so a plain copy made the packaged operator
  example environment-dependent even though the intended image contract was
  `0644`. Use `COPY --chmod=...` or an explicit `RUN chmod` for final-image files
  whose permissions matter, and test the resulting image mode. Do not assume a
  Git mode of `100644` proves the checkout file is physically `0644`.

- **Post-build container assertions must report observed values.** R2N used a
  single `docker run ... /bin/sh -c` block containing bare `test` commands under
  `set -e`; when the example mode differed, the report stopped after a successful
  image build without naming the failed path or observed mode. Capture the
  container observations as machine-readable evidence and validate each invariant
  in the outer applicator with a named `die` message. A fail-closed verifier is
  only operationally useful when its failure identifies what must be investigated.


### R2O / R2P container visibility and source-authority regression

- **Do not rely on implicitly created container destination parents.** R2O
  explicitly set copied documentation files to mode `0644`, and the build showed
  both COPY layers succeeding, yet the configured non-root runtime user could not
  see either copied file. When a runtime-visible image path is part of the
  contract, explicitly create every relevant destination parent, set its
  traversal mode (normally `0755` for public documentation), and explicitly set
  file ownership/mode. Verify the final image both as root and as the configured
  runtime user; a successful COPY layer alone is not sufficient evidence.

- **A diagnostic must consume the authenticated current source, not replay an
  assumed predecessor.** R2P correctly authenticated the live RC3 worktree and
  then falsely attempted to add a config-example COPY that the current Dockerfile
  already contained. If the current repository is small enough to snapshot,
  capture the complete tracked working-tree bytes plus branch, HEAD, index tree,
  staged patch, unstaged patch, physical modes, and checksums after each gate.
  The next applicator must compare the live checkout against that snapshot before
  relying on historical reconstruction.

- **Emit a fresh source-of-truth bundle after every applicator.** For this
  repository, each gate should automatically produce a compact source tarball
  containing all tracked working-tree files and Git-state evidence, regardless
  of success or fail-closed exit. Review and subsequent transforms should prefer
  that exact snapshot over chat memory, intended transforms, or manually retyped
  fixtures. Historical applicators remain useful as regression evidence, not as
  substitutes for current source authority.


### R2Q bespoke-runtime environment regression

- **A bespoke runtime probe must start from the application's minimum required
  process contract.** R2Q passed source authority, generated-text, static,
  Compose, Debian, and container-layout validation, then its operator-link
  `docker run` helper omitted `DIRECTORY_DATABASE_PATH`; the application
  correctly failed before serving HTTP. When a verifier launches the binary
  outside Compose/systemd, explicitly provide every required baseline process
  setting and compare the launch contract against the maintained deployment
  surface. A focused regression must prove the runtime helper supplies
  `DIRECTORY_DATABASE_PATH=/var/lib/activity-relay-directory/directory.sqlite`
  before exercising optional operator configuration.
