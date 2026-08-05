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
- Public status terminology will use healthy, stale, dead, and prune windows
  defined by recency.
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

When changing a reverse-proxy example, validate it with the corresponding
Nginx, Apache HTTP Server, or Caddy release. Examples must preserve the public
host and request target, remain optional, and must not install, enable, reload,
or otherwise configure an operator's proxy automatically.

## AI-assisted maintenance

AI-assisted tools may help draft or review changes. The human maintainer must
inspect the result, approve the design, execute validation, control releases
and deployments, and remain accountable for the software.
