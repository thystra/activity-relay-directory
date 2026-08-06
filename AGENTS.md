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
- Do not deploy or enable registration merely because code exists.

## Architectural invariants

- Registration is disabled by default.
- Fresh installations contain no active external directory endpoints.
- The directory stores relay-instance metadata, never connected-site or user
  identities.
- Registration, heartbeat, and unregister requests will require authenticated
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
