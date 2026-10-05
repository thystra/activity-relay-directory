# Directory Protocol

## Status and scope

Lifecycle Protocol v1 remains the compatibility baseline defined here and in
`testdata/directory/v1/`. Protocol v2 adds authenticated descriptive profile
synchronization without changing v1 message shapes or outcomes. Protocol v3
retains that profile contract and adds bounded participating-instance telemetry.
All lifecycle versions remain disabled together by default. See `docs/HANDLERS.md` for
activation, ordering, status mapping, and operational bounds.

`GET /v1/status` schema version 4 reports `lifecycle_enabled` (the requested
process configuration), `lifecycle_available` (the complete graph actually
constructed), `enrollment_open` (the current durable first-registration
policy), and `lifecycle_protocol_versions`. The version list is ordered, uses
integer protocol versions, always includes v1, includes v2 when the complete v2
verifier/profile graph is available, and includes v3 only when the complete v3
verifier/profile/telemetry graph is available. These fields are independent
of the separately versioned public relay-listing schema. Failure to read the
durable policy makes the status request fail closed without database detail.

## Public directory projection vocabulary

The read-only `GET /v2/relays` representation is versioned independently from
the signed lifecycle request protocol. On the 1.3 development line its
`schema_version` is `4`. Each relay object includes the closed numeric `tier`
vocabulary `1|2|3|4`, representing heartbeat+online, online without a current
heartbeat, unavailable, and the 30-day graveyard respectively, plus one reviewed
`profile` object containing only the effective descriptive relay fields. Public
ordering is `(tier, relay_actor)` and the authenticated v2 cursor format version
remains `2`, carrying both tier and actor position. Profile data does not affect
tier ordering. See `docs/PUBLIC-LISTING.md` for the complete inclusion, timing,
pagination, profile, and privacy contract.

This public tier is presentation of retained evidence, not a lifecycle state and
not an admission score. It never exposes discovery source, operator metadata,
or unresolved `--add-dead-relays` candidate provenance.

## Versioning and encoding

Every request and response contains the integer `protocol_version`. All
lifecycle versions use UTF-8 JSON with the strict media type
`application/json`. Implementations reject unknown fields, duplicate member
names, trailing JSON values, unsupported versions, and bodies above the
configured size limit.

The signed operations and endpoints are:

| Operation | Protocol v1 | Protocol v2 | Protocol v3 | Purpose |
|---|---|---|---|---|
| `register` | `POST /v1/relays/register` | `POST /v2/relays/register` | `POST /v3/relays/register` | Create, restore, or confirm a relay entry; v2/v3 also replace the relay-owned descriptive profile, and v3 may report participating-instance telemetry. |
| `heartbeat` | `POST /v1/relays/heartbeat` | `POST /v2/relays/heartbeat` | `POST /v3/relays/heartbeat` | Record current liveness; v3 may also refresh participating-instance telemetry. |
| `unregister` | `POST /v1/relays/unregister` | `POST /v2/relays/unregister` | `POST /v3/relays/unregister` | Remove an active listing. |

The request body repeats the operation name. A mismatch between the target
path and body operation is an `invalid_request` error. Protocol v1 clients keep
using the v1 paths unchanged. A capable client selects the highest lifecycle version it implements only after
a valid status document explicitly advertises that version; schema-2/3 status
documents imply v1-only compatibility. A failed or malformed status request is not permission to
silently downgrade.

## Relay identity

`relay_actor` is the canonical ActivityPub relay actor URL and is the durable
directory identity. Register also carries `public_base_url`, the public origin
operators expect people and clients to visit.

All lifecycle versions use the same canonical URL syntax:

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

All lifecycle versions use the same reviewed RFC 9421 HTTP Message Signature
profile and RFC 9530 `Content-Digest` over the exact JSON bytes. Enabled
handlers invoke the complete verifier and durable replay path. The versions are
cryptographically separated by the required signature tag: v1 requires
`tag="activity-relay-directory-v1"`; v2 requires
`tag="activity-relay-directory-v2"`; and v3 requires
`tag="activity-relay-directory-v3"`. A signature for one lifecycle version
cannot be reinterpreted as another.

Each version accepts exactly one paired `Signature-Input` and `Signature`
dictionary member. The label is chosen by the client, while the signature must
carry the version-appropriate tag and `alg="rsa-v1_5-sha256"`. Component
parameters, signature-value parameters,
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

All lifecycle versions require the `sha-256` member of the RFC 9530 Structured Fields
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
access. Digest verification alone does not authenticate a sender; the HTTP
transport requires `content-digest` to be covered by a valid RFC 9421 signature.

The signature verifier accepts key material only through a caller-supplied
resolver; it performs no DNS lookup or actor retrieval itself. The resolver
must return the exact requested key ID, a minimum 2048-bit RSA public key, and
canonical identical public-key-owner and actor identities. After successful
cryptographic verification, `BindRelayActor` requires that identity to equal
the canonical `relay_actor` in the request body.

The production resolver accepts a canonical fragment-bearing key ID
whose fragment-free form is the actor URL. It retrieves only that HTTPS actor
document through the bounded network policy in `docs/RESOLUTION.md`. The actor
must be an `Application`, `Service`, or `Group`, its `id` must equal the requested actor
URL, and exactly one embedded public key must have the requested key ID and the
actor as owner. This is authenticated key discovery for signature verification,
not registration authorization. Enabled lifecycle handlers reach it only after
source admission and through the bounded successful-key cache.

The stateless verifier returns the validated nonce for composition and testing.
`VerifyPOSTAndReserve` is the handler-safe contract: it completes signature,
digest, key, and canonical relay-actor binding before atomically reserving an
opaque replay key. Public handlers use that combined path, never the stateless
verifier by itself.

The replay key is SHA-256 over the exact key ID, a zero-byte separator, and the
nonce. Stores therefore never need the raw key ID or nonce. A successful
reservation is retained for ten minutes, beyond the complete signature
acceptance window. Atomic reserve returns false for a duplicate; backend
errors and exhausted capacity fail closed without being classified as a
replay.

The package-private bounded memory implementation remains contract test
infrastructure. The SQLite implementation persists the same opaque
key across process restart and atomically suppresses conflicts across
independent local connections. It rejects expired or overlong retention,
replaces a key exactly at expiry, and performs fixed-size expired-row cleanup
in the reservation transaction. The enabled graph passes it to the verifier
and schedules separate bounded cleanup. SQLite remains a single-host backend.

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
The contract function returns a verified registration intent only. The handler
then applies actor admission and the transactional repository to classify the
operation as created, updated, or unchanged.

The state repository atomically classifies and audits a verified
intent: a new actor is `created`, an identical registered actor is `unchanged`,
and a retained unregistered or pruned actor restored to registered state is
`updated`. Restoration keeps the first registration time and clears the prior
lifecycle timestamp plus obsolete heartbeat recency. Administrative suspension
blocks register and is never silently cleared.

Creating the first retained row also requires the durable enrollment policy to
be open. Closing enrollment never changes an existing row. Any actor with a
retained row remains accepted and may update registration metadata or return
from unregistered or later soft-pruned state while enrollment is closed,
subject to moderation. The separately configured local inactive-retention purge
may later delete an eligible active inactive row after its backup gate; a later
return is then again a never-seen registration. Suspended rows are never
automatic purge candidates.

The register handler uses the complete authenticated composition with the safe
actor resolver and durable replay store before calling the state repository at
a server-owned acceptance time. It remains unavailable unless the complete
lifecycle graph is explicitly enabled.

## Protocol v2 register profile contract

Protocol v2 register preserves the v1 canonical relay identity and lifecycle
outcomes while adding one required `profile` object. The object is a complete
replacement of the relay-owned profile source and contains exactly these twelve
fields: `participation_mode`, `availability`, `relay_type`, `languages`,
`countries`, `regions`, `topics`, `contact_fediverse`, `contact_email`,
`contact_url`, `participation_url`, and `notes`. All fields must be present.
Empty strings and empty arrays explicitly clear the corresponding relay-owned
assertion. Unknown, omitted, `null`, duplicate, noncanonical, or oversized
values are rejected.

The profile is normalized through the same storage contract used by CSV and
operator profile writes. List values are trimmed, deduplicated, sorted, and
limited by the shared profile bounds; email and HTTPS URL fields use the same
canonical validation rules. Replacing the relay source does not alter CSV or
operator assertions. Effective values continue to resolve field by field as
operator override, authenticated relay self-report, CSV, then absent.

Profile mutation is descriptive only. It does not establish liveness, change
moderation or enrollment, affect reachability or pruning, or influence public
eligibility/tiering. The server uses one acceptance time for the lifecycle and
profile portions of a v2 register. A storage failure is returned as a protocol
error so a client can safely retry registration to converge.

Protocol v2 heartbeat and unregister are identity-only. Supplying profile,
telemetry, or registration fields to either operation is invalid; heartbeat
never refreshes or mutates descriptive profile data, and unregister does not
erase private profile history. Protocol v3 retains the same complete register
profile and identity-only unregister shape while allowing the bounded telemetry
object described below on register and heartbeat.

## Heartbeat request contract

The version-specific heartbeat decoder applies the same strict single-object
and configurable 1 MiB maximum body rules as registration. It accepts only the
selected protocol version, the `heartbeat` operation, and an already canonical
`relay_actor`. Registration metadata such as `public_base_url` and `profile` is
an unknown field and is rejected, so a heartbeat cannot create or silently
alter a registration or profile.

Authenticated composition accepts only the matching versioned heartbeat path
with no query or fragment. Body, version, operation, target, and canonical actor
checks finish before key resolution or nonce reservation. The signing key must
bind to the exact actor, and the resulting nonce is reserved atomically.

The contract function establishes only an authenticated heartbeat intent. It
does not prove that the actor is registered or administratively active, record
liveness, or produce the `recorded` outcome. The state repository enforces an
existing active registration, rejects suspension, and records server-side
acceptance time atomically with a `heartbeat_recorded` event. Local
administrative commands can apply or clear suspension for an existing retained
relay. The enabled handler composes durable replay, admission, and repository
persistence; moderation is intentionally not a version 1 network operation and
no moderation HTTP target exists. Liveness recency must never use a
client-supplied signature timestamp.
An actor without an active registration receives the stable
`relay_not_registered` code. Clients may use only that code—not
`invalid_request`—to initiate one bounded register reconciliation.

## Unregister request contract

The version-specific unregister decoder applies the shared strict single-object
and configurable 1 MiB maximum body rules. It accepts only the selected protocol
version, the `unregister` operation, and an already canonical `relay_actor`.
Registration metadata, profile data, and every other unknown field are rejected.

Authenticated composition accepts only the matching versioned unregister path
with no query or fragment. Body, version, operation, target, and canonical actor
checks finish before key resolution or nonce reservation. The signing key must
bind to the exact actor, and the resulting nonce is reserved atomically. A
signed heartbeat or registration request cannot satisfy this contract.

The contract function establishes only an authenticated removal intent. It
neither decides whether the actor is present nor produces the `removed` or
`absent` outcome.
The repository implements the idempotent transition: a registered
entry becomes `removed`, while an unknown, already unregistered, or pruned entry
remains `absent`. It records the outcome atomically and preserves suspension,
moderation, and audit history. The enabled unregister handler calls it only
after the complete authenticated and admitted request path.

Replay rejection and state-based idempotence are distinct. Reusing a nonce is
an error. Repeating an already completed operation with a fresh valid signature
returns the current state without duplicating it.

## Request admission contract

The in-memory admission component derives the client source from the
direct socket peer and accepts an overwritten `X-Real-IP` only from an
explicitly trusted proxy. Trusted prefixes name proxies rather than permitted
clients, so private and LAN client addresses are valid sources. Appendable
forwarding chains are ignored for the version 1 security identity.

Operation-specific source buckets and a global concurrency ceiling are applied
before expensive actor resolution or signature work. Only after successful
signature verification and canonical actor binding may the active source
permit allocate and consume an operation-specific actor bucket. Source state,
authenticated-actor state, cleanup work, and concurrent work are all bounded;
capacity and limit exhaustion fail closed with fixed decisions and retry
guidance. See `docs/ADMISSION.md`.

Lifecycle handlers map policy rejection to the closed `rate_limited` code and
HTTP 429, with bounded response text and an integer `Retry-After` when the
decision provides one. See `docs/HANDLERS.md`.

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
on the code, never the message. All lifecycle versions use the same closed error codes:

- `invalid_request`
- `unsupported_protocol_version`
- `authentication_failed`
- `replay_detected`
- `lifecycle_unavailable`
- `enrollment_closed`
- `relay_not_registered`
- `relay_suspended`
- `rate_limited`
- `internal_error`

Status-code mappings are fixed in `docs/HANDLERS.md`. Error responses must not
disclose key material, signatures, nonces, internal storage identifiers, or
moderation notes. Clients authenticate proactively; version 1 does not emit an
authentication challenge.

## Lifecycle vocabulary

Automatic health state is named by server-owned last-seen recency. Accepted
register and heartbeat operations refresh the same nondecreasing value, so a
new registration is healthy before its first heartbeat:

- `healthy`
- `stale`
- `dead`
- `prune`

Version 1 boundaries are fixed: age through 36 hours is `healthy`; over 36
hours but before 7 days is `stale`; exactly 7 days through before 30 days is
`dead`; and exactly 30 days or more is `prune`. They are not runtime
configuration. Administrative state is separately `active` or `suspended`.
`suspended` overrides automatic health and listing decisions. Reaching `prune`
excludes a relay from future public projection independently of scheduler lag.
The private reversible transition stores lifecycle `pruned`, a server-owned
pruning time, and one append-only event; it does not erase moderation or audit
history. A later register may restore the lifecycle state, subject to suspension.
Administrative transition outcomes and their moderator and reason tokens are
private storage vocabulary. They are not version 1 operations, outcomes, or
public response fields; no moderation HTTP target is defined in this document.

## Fixtures

Files under `testdata/directory/v1/` remain the normative Protocol v1 examples.
Files under `testdata/directory/v2/` freeze the Protocol v2 profile-sync wire
contract. Files under `testdata/directory/v3/` freeze the Protocol v3 profile
plus participating-telemetry wire contract. Tests decode with strict unknown/duplicate-field rejection, require
canonical identity, and check digest/signature material against the fixture's
exact body bytes.

`rfc9421-register.valid.json` is the original complete v1 verification vector.
The v1 `activity-relay-register.valid.json` remains byte-compatible with the
Activity-Relay v1 client. Neither compatibility vector changes for Protocol v2.

`testdata/directory/v2/activity-relay-register.valid.json` is the shared v2
registration vector. It contains the complete twelve-field profile, exact v2
target, RFC 9530 digest, RFC 9421 signature with the v2 tag, and public test
key; it contains no private key. An identical copy is retained in the
Activity-Relay repository. Both repositories must verify the exact shared
fixture before the v2 path is activated or released.

`testdata/directory/v3/activity-relay-register.valid.json` is the corresponding
shared v3 vector. It retains the complete profile, uses the v3 endpoint/tag, and
contains `participating_instance_count` telemetry. Both repositories retain the
same bytes.

## Protocol v3 registration status and participating telemetry (1.3 RC4)

`profile.participation_mode` is a controlled relay self-report. Its canonical
non-empty values are exactly `open`, `restricted`, and `closed`; the empty
string means the relay makes no assertion. Other remote values are invalid.

Protocol v3 register and heartbeat may contain one optional object:

```json
"telemetry": { "participating_instance_count": 12 }
```

The object, when present, is complete and contains exactly one integer in the
range 0 through 10,000,000. Absence means unknown/no update; zero is an
explicitly reported zero. Unregister does not accept telemetry. ARD records the
Directory acceptance time rather than trusting a client timestamp. Protocol v2
remains compatible with rc3 senders that report `receiving_instance_count`, but
rc4 Activity-Relay clients do not send telemetry on v2. Both telemetry forms are
self-reported informational data and cannot influence reachability, heartbeat
classification, tier, moderation, enrollment, pruning, or public eligibility.

All remote lifecycle bodies are untrusted. ARD bounds request bodies, rejects
unknown and duplicate JSON member names (including nested members), validates
canonical identities and closed vocabularies before mutation, and persists
remote strings only through parameterized SQL. Error responses use bounded
closed messages and do not echo attacker-controlled request content.
