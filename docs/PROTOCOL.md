# Directory Protocol

## Status and scope

Version 1 vocabulary and JSON message shapes are defined here and in
`testdata/directory/v1/`. They are contract fixtures, not active HTTP APIs.
Registration remains unavailable until transport, URL, signature, replay,
persistence, moderation, and rate-limit gates are implemented and reviewed.

## Versioning and encoding

Every request and response contains the integer `protocol_version`. Version 1
uses UTF-8 JSON with the strict media type `application/json`. Implementations
must reject unknown fields, trailing JSON values, unsupported versions, and
bodies above the configured size limit.

The signed operations and their planned endpoints are:

| Operation | Method and path | Purpose |
|---|---|---|
| `register` | `POST /v1/relays/register` | Create, update, or confirm a relay entry. |
| `heartbeat` | `POST /v1/relays/heartbeat` | Record current liveness. |
| `unregister` | `POST /v1/relays/unregister` | Remove an active listing. |

The request body repeats the operation name. A mismatch between the target
path and body operation is an `invalid_request` error.

## Relay identity

`relay_actor` is the canonical ActivityPub relay actor URL and is the durable
directory identity. Register also carries `public_base_url`, the public origin
operators expect people and clients to visit.

Version 1 canonical URL syntax requires:

- HTTPS with no credentials, query, or fragment;
- a lower-case fully qualified ASCII DNS name or canonical IP literal;
- no empty port, with explicit port 443 removed and other valid ports retained;
- a public base URL containing only the origin, serialized without `/`;
- an absolute actor path whose percent escapes are canonicalized, while empty
  or dot segments, encoded slash, encoded percent, backslash, invalid UTF-8,
  and control characters are rejected; and
- the actor and public base URL to use the same normalized origin.

Internationalized DNS names must arrive as ASCII A-labels. The canonicalizer
does not resolve DNS, fetch the actor, decide whether an IP address is publicly
routable, or establish actor-key ownership. Those security gates remain
mandatory before registration can be accepted.

Heartbeat and unregister identify a relay only by `relay_actor`. They cannot
silently replace registration metadata.

No request or response contains connected-site identities, follower or
membership lists, user identities, or a site-level relationship graph.

## Authentication envelope

All three operations require the version 1 RFC 9421 HTTP Message Signature
profile and RFC 9530 `Content-Digest` over the exact JSON bytes. These are
implemented as contract primitives but are not connected to HTTP handlers yet.
Registration therefore remains unavailable.

Version 1 accepts exactly one paired `Signature-Input` and `Signature`
dictionary member. The label is chosen by the client, while the signature must
carry `tag="activity-relay-directory-v1"` and
`alg="rsa-v1_5-sha256"`. Component parameters, signature-value parameters,
unknown signature parameters, mismatched labels, and additional signature
members are rejected. The required covered components, in the order clients
should emit them, are:

1. `@method`
2. `@authority`
3. `@target-uri`
4. `content-digest`
5. `content-type`
6. `date`

Verifiers permit additional unique covered components. Requests must be POSTs
for the configured canonical HTTPS authority, and `Content-Type` must be the
single exact value `application/json`. The signed `Date` must be a valid HTTP
date within the accepted clock window.

The `created`, `expires`, `keyid`, and nonce parameters are required. `created`
may be at most five minutes old or thirty seconds in the future. `expires` must
be later than both `created` and verification time and no more than five
minutes after `created`. Key IDs are bounded to 2048 bytes and nonces to 256
bytes. Those values remain HTTP signature metadata rather than duplicate JSON
fields.

Version 1 requires the `sha-256` member of the RFC 9530 Structured Fields
dictionary. Its value is a 32-byte Byte Sequence containing SHA-256 over the
exact message content bytes, before any JSON decoding or reserialization.
Additional digest algorithms may be present and are ignored by this profile.
Multiple field lines are combined as one dictionary; under RFC 8941 duplicate
dictionary keys use the last member. A malformed dictionary, absent or
non-Byte-Sequence `sha-256` member, wrong-length value, or digest mismatch
fails authentication. The signature base covers the complete presented
`Content-Digest` field value, so an ignored algorithm member cannot be added,
removed, or changed without invalidating the HTTP message signature.

The contract layer can generate and verify this digest without HTTP or network
access. Digest verification alone does not authenticate a sender; the future
HTTP transport must cover `content-digest` with a valid RFC 9421 signature.

The signature verifier accepts key material only through a caller-supplied
resolver; it performs no DNS lookup or actor retrieval itself. The resolver
must return the exact requested key ID, a minimum 2048-bit RSA public key, and
canonical identical public-key-owner and actor identities. After successful
cryptographic verification, `BindRelayActor` requires that identity to equal
the canonical `relay_actor` in the request body.

The stateless verifier returns the validated nonce for composition and testing.
`VerifyPOSTAndReserve` is the handler-safe contract: it completes signature,
digest, key, and canonical relay-actor binding before atomically reserving an
opaque replay key. Public handlers must eventually use that combined path,
never the stateless verifier by itself.

The replay key is SHA-256 over the exact key ID, a zero-byte separator, and the
nonce. Stores therefore never need the raw key ID or nonce. A successful
reservation is retained for ten minutes, beyond the complete signature
acceptance window. Atomic reserve returns false for a duplicate; backend
errors and exhausted capacity fail closed without being classified as a
replay. The package-private bounded memory implementation exists only to prove
these semantics under concurrency. A shared durable backend remains mandatory
before any public handler or multi-process deployment can use the contract.

## Register request contract

`DecodeRegisterRequest` accepts exactly one top-level JSON object within an
operator-selected positive limit no greater than 1 MiB. It rejects malformed
JSON, duplicate or unknown member names, trailing values, unsupported protocol
versions, any operation other than `register`, and identities that are invalid
or not already in canonical same-origin form. Parser errors are bounded classes
and never include supplied member names, values, or URLs.

The authenticated composition accepts only `POST /v1/relays/register` with no
query or fragment and then calls the signature, actor-binding, and atomic replay
contract over the exact body bytes. Request parsing, version, operation, target,
and identity checks all finish before key resolution or nonce reservation.
Success returns a verified registration intent only; it performs no persistence
and does not classify the operation as created, updated, or unchanged.

Future register handlers must use this complete composition with a safely
resolved actor key and a shared durable replay store. This contract does not
make registration available and is not connected to the HTTP server.

## Heartbeat request contract

`DecodeHeartbeatRequest` applies the same strict single-object and configurable
1 MiB maximum body rules as registration. It accepts only protocol version 1,
the `heartbeat` operation, and an already canonical `relay_actor`. Registration
metadata such as `public_base_url` is an unknown field and is rejected, so a
heartbeat cannot create or silently alter a registration.

The authenticated composition accepts only `POST /v1/relays/heartbeat` with no
query or fragment. Body, version, operation, target, and canonical actor checks
finish before key resolution or nonce reservation. The signing key must bind to
the exact actor, and the resulting nonce is reserved atomically.

Success establishes only an authenticated heartbeat intent. It does not prove
that the actor is registered or administratively active, record liveness, or
produce the `recorded` outcome. Those checks and the state transition require
the later persistence and moderation contracts. Liveness recency must use the
server-side acceptance time, not a client-supplied signature timestamp.

No heartbeat handler is connected to the HTTP server.

Replay rejection and state-based idempotence are distinct. Reusing a nonce is
an error. Repeating an already completed operation with a fresh valid signature
returns the current state without duplicating it.

## Outcomes

Successful responses use a closed, operation-specific outcome vocabulary:

| Operation | Outcomes |
|---|---|
| `register` | `created`, `updated`, `unchanged` |
| `heartbeat` | `recorded` |
| `unregister` | `removed`, `absent` |

`created` is intended for HTTP 201. The other successful outcomes use HTTP 200.
`updated` replaces mutable registration metadata for the same canonical actor;
it does not replace the actor identity.

Errors use a stable code and a bounded human-readable message. Clients branch
on the code, never the message. Version 1 codes are:

- `invalid_request`
- `unsupported_protocol_version`
- `authentication_failed`
- `replay_detected`
- `registration_unavailable`
- `relay_suspended`
- `rate_limited`
- `internal_error`

Detailed status-code mappings and authentication challenges will be fixed when
the HTTP transport is implemented. Error responses must not disclose key
material, signatures, nonces, internal storage identifiers, or moderation
notes.

## Lifecycle vocabulary

Automatic health state is named only by heartbeat recency:

- `healthy`
- `stale`
- `dead`
- `prune`

Threshold durations remain operator configuration and are not encoded in these
names. Administrative state is separately `active` or `suspended`.
`suspended` overrides automatic health and listing decisions. Reaching `prune`
does not erase moderation or audit history without a separate explicit policy.

## Fixtures

Files under `testdata/directory/v1/` are normative examples for the fields,
digest encoding, and closed vocabulary defined in this tranche. Go tests
decode them with unknown field rejection, verify that outcomes match their
operations, require the registration identity to already be canonical, and
check the digest against the fixture's exact body string bytes. Later server
and Activity-Relay client implementations must reuse these fixtures or prove
byte-for-byte digest and semantic message compatibility with them.

`rfc9421-register.valid.json` is a complete verification vector containing the
exact request target, fields, body, signature, and public test key. It contains
no private key. Tests fix verification time inside the vector's validity window
and require cryptographic verification plus relay-actor binding.
