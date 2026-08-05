# Request admission and source identity

## Status

`internal/admission` is a dormant, in-memory policy component. It is not
constructed by the running service, no lifecycle handler exists, and
registration remains disabled. This tranche defines the bounds needed before
actor resolution, signature verification, replay reservation, or state
mutation can be exposed.

The policy is intentionally process-local. It limits work within one directory
process; it is not a distributed quota and does not replace a reverse proxy,
firewall, or upstream denial-of-service controls.

## Source identity boundary

The source resolver begins with the socket peer in `http.Request.RemoteAddr`.
Private, loopback, link-local, and other ordinary unicast addresses are valid
request sources. A LAN client is therefore not prohibited by the address
policy. The prefix list passed to `NewSourceResolver` identifies trusted proxy
peers; it is not a client allow-list or deny-list.

If the direct peer is not in that explicit list, the resolver uses the peer and
ignores `Forwarded`, `X-Forwarded-For`, and `X-Real-IP`. This prevents a direct
client from selecting another source bucket with a forged header.

If the peer is trusted, the proxy must overwrite `X-Real-IP` with exactly one
IP address derived from its connection metadata. A missing, repeated,
comma-separated, malformed, unspecified, or multicast value fails closed. The
appendable `Forwarded` and `X-Forwarded-For` chains are not security inputs in
version 1. Forwarded fields can be added by any sender unless every hop applies
a reviewed trust policy; see RFC 7239, section 8.1.

Prefer exact `/32` and `/128` proxy host prefixes. Trusting a LAN range means
every host in that range may assert a client identity. The supplied Nginx,
Apache, and Caddy examples overwrite `X-Real-IP`, but runtime construction and
trusted-prefix configuration remain a later handler-composition step.

## Two-stage admission

Admission is separated so unauthenticated input cannot allocate actor-keyed
state:

1. The exact lifecycle route selects its closed protocol operation;
   `Limiter.AdmitSource` validates it and the canonical source address, applies
   the operation-specific source bucket, and acquires one global concurrency
   permit before expensive resolver or signature work.
2. After the complete signature and actor-binding checks succeed, the caller
   invokes `Permit.AdmitActor` once for the same operation with that canonical
   actor. An inactive, released, already-checked, or different-operation permit
   cannot allocate actor state.
3. The caller releases the permit on every exit path. Release is concurrency-
   safe and idempotent.

Each operation has an independent bucket for a given source or actor. Register,
heartbeat, and unregister traffic therefore cannot silently consume one
another's quota. Every accepted stage consumes one token. Buckets hold a fixed
burst and restore one token per configured interval. A rate rejection returns
a deterministic minimum retry duration suitable for a future `Retry-After`
header.

## Bounded overload behavior

Configuration explicitly bounds:

- source and authenticated-actor entries;
- global concurrent admitted requests;
- burst and refill intervals;
- idle retention;
- cleanup work per admission; and
- overload retry guidance.

Entries are retained in least-recently-used order. Each admission removes at
most the configured number of expired oldest entries. Idle retention must be
long enough for a bucket to refill fully, so removing an expired entry cannot
prematurely restore quota. At capacity, if bounded cleanup cannot free an
expired entry, admission fails closed instead of evicting active policy state
or growing memory. A backwards wall-clock adjustment neither refills nor
expires state.

Decisions contain only a fixed class and duration, never a source address or
actor URL. Future HTTP composition is expected to map source, actor,
concurrency, and capacity rejection to the version 1 `rate_limited` error and
HTTP 429. RFC 6585 permits a 429 response to include `Retry-After`; the exact
public mapping and response text remain part of the handler tranche.

## Remaining work

Before a lifecycle endpoint is enabled, handler composition must select and
validate operational limits, construct the trusted-proxy source resolver,
apply source admission before expensive work, apply actor admission only after
authentication, use the durable replay store, enforce moderation and state
rules, map bounded public errors, and test all cleanup paths. Multi-process or
multi-host deployment would additionally require a reviewed shared rate-policy
design if quotas must apply across processes.
