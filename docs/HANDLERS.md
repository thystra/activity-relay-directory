# Signed lifecycle handlers

The server composes the version 1 register, heartbeat, and unregister contracts
into HTTP routes. All three routes remain disabled together by default. This
code does not deploy the service, configure a reverse proxy, or enable an
Activity-Relay client. Public listings use a separate default-off read graph
documented in `docs/PUBLIC-LISTING.md`; they do not share signed lifecycle
admission or authentication.

## Fail-closed activation

`DIRECTORY_LIFECYCLE_ENABLED` defaults to `false`. While false, the process
does not construct the actor resolver, signing-key cache, verifier, replay
store adapter, lifecycle repository, or admission graph. Lifecycle POSTs return
HTTP 503 with `lifecycle_unavailable`, and `/v1/status` reports
`lifecycle_available: false`.

Setting the value to `true` requires an HTTPS `DIRECTORY_PUBLIC_BASE_URL`. The
process exits before listening if any lifecycle dependency cannot be
constructed. Only a complete enabled graph makes `/v1/status` report lifecycle
availability. The retired pre-release `DIRECTORY_REGISTRATION_ENABLED` name is
rejected.

The durable enrollment policy is independent and defaults closed. The status
document reports `lifecycle_enabled`, `lifecycle_available`, and
`enrollment_open` separately. Closing enrollment rejects only a register intent
whose authenticated actor has no retained relay row. Existing retained actors
may register, heartbeat, and unregister while enrollment is closed, subject to
suspension and the remaining gates. Local policy changes use
`activity-relay-directory admin enrollment status|open|close`; open and close
require `--operator ID` and append a private bounded audit event transactionally.
No remote enrollment administration route exists.

## Request order

Each exact operation route performs these gates in order:

1. require POST on the selected exact route;
2. derive the source from the direct peer and explicitly trusted proxy policy;
3. consume an operation-specific source token and concurrency permit;
4. read the exact body into bounded memory;
5. strictly decode the matching version, operation, target, and canonical actor;
6. verify RFC 9530 digest, safely resolve the actor key, verify RFC 9421, bind
   the actor identity, and atomically reserve the opaque replay key in SQLite;
7. consume an actor token exactly once through the still-active source permit;
8. capture server acceptance time and commit the lifecycle state and audit event
   atomically; and
9. return a closed version 1 outcome.

Every exit after source admission releases the global concurrency permit.
Actor-rate rejection occurs after replay reservation, so a later retry requires
a fresh nonce. No client signature time is used as heartbeat or state recency.

## Fixed initial admission bounds

The first enabled graph uses conservative fixed bounds:

- source bucket: burst 60, one token per second, separately per operation;
- authenticated-actor bucket: burst 10, one token per minute, separately per
  operation;
- at most 10,000 source buckets and 10,000 actor buckets;
- at most 32 lifecycle requests doing work concurrently;
- 24-hour idle bucket retention with at most 128 removals per admission; and
- a five-second retry recommendation for capacity or concurrency pressure.

The signing-key cache holds at most 4096 successes for a non-sliding five
minutes. Replay reservation performs bounded cleanup in its transaction, and a
five-minute maintenance ticker removes at most 4096 additional expired rows per
run. These defaults require operational review before substantially different
traffic is expected; making them configurable is separate work.

## Trusted reverse proxies

`DIRECTORY_TRUSTED_PROXY_PREFIXES` is an optional comma-separated list of at
most 32 canonical IP prefixes. An empty value trusts no proxy. Forwarding data
is ignored from every untrusted peer. A trusted peer must supply exactly one
canonical `X-Real-IP`; appendable `Forwarded` and `X-Forwarded-For` chains are
never security identities.

Prefer an exact `/32` or `/128` for each proxy. Use `127.0.0.1/32` or `::1/128`
only when that is the direct address the service actually observes. A proxy
reaching a container through port publishing may appear as a container-network
gateway instead of loopback; verify the direct peer and trust only the exact
required address. Trusted prefixes identify proxies, not allowed clients, and
private or LAN client addresses remain valid after trusted derivation.

## HTTP results

Successful creation returns HTTP 201. Updated, unchanged, recorded, removed,
and absent outcomes return HTTP 200. Errors use fixed JSON messages and never
include request bodies, URLs, signature fields, key IDs, nonces, database
details, or moderation audit data.

| HTTP status | Version 1 code | Meaning |
| --- | --- | --- |
| 400 | `invalid_request` | malformed target/body or invalid source |
| 400 | `unsupported_protocol_version` | unsupported body version |
| 401 | `authentication_failed` | digest, signature, time, key, or actor binding failed |
| 403 | `relay_suspended` | moderation blocks register or heartbeat |
| 403 | `enrollment_closed` | a never-seen actor cannot create its first retained row |
| 409 | `replay_detected` | opaque key/nonce reservation already exists |
| 409 | `relay_not_registered` | heartbeat actor has no active registration |
| 413 | `invalid_request` | transport body exceeds the configured ceiling |
| 429 | `rate_limited` | source, actor, concurrency, or bounded-state admission rejected |
| 500 | `internal_error` | storage or dependency failure |
| 503 | `lifecycle_unavailable` | lifecycle graph is disabled or incomplete |

HTTP 429 includes an integer `Retry-After` when the admission decision provides
one. Unsupported methods return HTTP 405 with `Allow: POST`. This profile does
not emit an authentication challenge; clients authenticate proactively with
the required RFC 9421 message signature.

## Human-readable public directory

When `DIRECTORY_PUBLIC_LISTING_ENABLED=true`, the server also registers
`GET`/`HEAD` `/` and `/assets/directory.css`. The root view calls the same
`PublicListingHandler` projection loader used by `/v1/relays`; it does not
create another repository read or public-eligibility path. HTML and JSON share
the same page limits, authenticated cursor, observation time, repository
deadline, concurrency ceiling, and one-minute cache policy.

The HTML renderer uses Go `html/template` with bundled local assets. Relay
content is emitted only as escaped text and canonical public-base links. The
initial page has no JavaScript, remote fonts, analytics, relay-provided HTML,
relay-controlled image loads, or third-party resources.

## Local retention is not an HTTP handler

Roadmap Tranche 16 adds only local `admin retention dry-run` and backup-gated
`admin retention purge` commands. `/v1/retention`, `/v1/retention/purge`,
`/v1/purge`, and `/admin/retention` are not routes and must remain `404` for
GET, HEAD, POST, and DELETE. A positive retention policy never authorizes a
remote caller or request path to start maintenance. See `docs/RETENTION.md`.

## Database hard state is not a new HTTP administration surface

Roadmap Tranche 17 adds no storage-management HTTP endpoint. At growth `hard`,
`GET`/`HEAD` `/healthz` remains live and `/readyz` returns unavailable. Enabled
public `GET`/`HEAD` `/v1/relays`, `/`, and the bundled stylesheet continue
through their existing bounded read-only paths. Signed lifecycle mutations fail
closed and map the storage hard limit to the existing redacted
`lifecycle_unavailable` protocol class; SQLite paths and capacity details are
not returned.

Storage metrics and test notification remain local-only under
`activity-relay-directory admin storage ...`; there is no `/admin/storage` or
public growth-control route. See `docs/STORAGE-GROWTH.md`.
