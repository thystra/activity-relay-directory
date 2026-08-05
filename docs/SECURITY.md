# Security Design

## Primary threats

- forged relay registration;
- replayed heartbeat or unregister requests;
- key substitution;
- SSRF through relay-controlled URLs;
- oversized or deeply nested JSON;
- duplicate and conflicting registrations;
- directory poisoning and abusive metadata;
- automated removal of administratively suspended entries.

## Required controls before registration exists

- authenticated signatures and digest verification;
- exact key-to-relay binding;
- bounded clocks and nonce retention;
- allowlisted schemes and normalized HTTPS URLs;
- network-target restrictions for all server-initiated fetches;
- strict payload depth and size limits;
- rate limits by source and relay identity;
- operator suspension and audit history;
- no disclosure of connected-site membership.

## Canonical relay identity

The version 1 contract normalizes HTTPS actor and public-base URLs without
performing network access. It rejects ambiguous authorities and paths and
requires both URLs to share one origin. Validation errors do not echo the
supplied URL.

Canonical syntax is not an SSRF decision. The actor resolver separately
resolves all addresses, rejects prohibited local, private, link-local,
documentation, multicast, and otherwise non-public targets, pins each
connection to an approved address, and repeats the checks on every bounded
redirect. It then binds the resolved actor and signing key to the canonical
identity. Enabled lifecycle handlers reach it only after source admission; see
`docs/RESOLUTION.md`.

The successful-key cache wraps only that production resolver. It
revalidates complete actor/key binding, keeps a fixed entry ceiling and
non-sliding five-minute maximum TTL, returns copied RSA material, never caches
errors, and never serves expired data. Eviction triggers another safe fetch
rather than bypassing resolution. Concurrent cold misses remain subject to the
composed admission ceiling.

## Content digest

Version 1 requires an RFC 9530 `sha-256` Content-Digest over the exact bounded
request body bytes. Verification is constant-time after strict Structured
Fields parsing and never includes an attacker-controlled field value in an
error. JSON whitespace and other byte-level changes therefore invalidate an
otherwise well-formed digest.

A content digest is not sender authentication. Lifecycle handlers
verify that `content-digest` is covered by the accepted RFC 9421 signature,
and must not reserve a nonce or mutate directory state until the signature,
digest, timestamp, identity-binding, and request-bound checks all succeed.

## HTTP message signature

The version 1 RFC 9421 contract fixes one application tag, one RSA algorithm,
the public HTTPS authority, strict JSON content type, bounded creation and
expiration times, a bounded nonce, and mandatory coverage of method,
authority, target URI, digest, content type, and date. A signature over one
operation target cannot therefore be replayed as another operation without
failing cryptographic verification.

Parsing, policy, time, digest, key-resolution, cryptographic, and actor-binding
failures use bounded error classes that never include supplied signature
fields, key IDs, nonces, resolver details, or bodies. Digest validation occurs
before key resolution. Resolved keys must be at least 2048-bit RSA keys whose
exact key ID and canonical owner/actor identity are established by the caller's
authenticated resolver.

The combined verification path atomically reserves a SHA-256-derived replay
key only after digest, key, cryptographic signature, and actor-binding checks
succeed. Stores receive no raw key ID or nonce. Replay markers remain for ten
minutes, and duplicate reservations are distinct from backend or capacity
failures. Storage failures fail closed and their details are not returned.

The bounded memory implementation is package-private test infrastructure. It
proves atomicity and expiry behavior but cannot protect against process restart
or multiple service instances. The SQLite implementation is the
durable single-host store: it accepts only opaque 32-byte keys, persists across
restart, uses exact inclusive expiry, and prunes a bounded number of expired
rows in the reservation transaction. It is not suitable for a multi-host
service. Enabled handlers use it and a separate five-minute schedule performs
at most 4096 additional expired-row removals per run. The resolver enforces
DNS, address, redirect, response-size, media-type, actor-
document, and RSA-key safety before returning resolved key material.

The admission component separately derives a source from the direct
socket peer, trusting an overwritten `X-Real-IP` only from an explicitly
configured proxy prefix. Private and LAN client addresses remain valid;
trusted prefixes identify proxies, not permitted clients. Operation-specific
source buckets run before expensive work, while actor buckets can be reached
only through an active concurrency permit after authentication and actor
binding. Both state tables, cleanup work, concurrency, and retry guidance are
bounded; capacity fails closed. See `docs/ADMISSION.md`.

## Register request boundary

The register contract accepts only a bounded, single top-level JSON object. It
rejects duplicate and unknown member names, trailing values, wrong versions or
operations, noncanonical or cross-origin identities, and any target other than
the exact query-free register endpoint. Errors do not repeat supplied JSON
names, values, URLs, queries, or fragments.

These semantic and target gates run before key resolution and replay
reservation. The complete composition then authenticates the exact body, binds
the signing actor, and atomically reserves the nonce. Actor admission follows,
then the repository revalidates canonical bounded identity and commits the
audited state transition at server acceptance time. The complete graph remains
disabled by default.

## Heartbeat request boundary

Heartbeat reuses the strict bounded-object parser and exact-target checks but
accepts only its own operation plus one canonical actor identity. Registration
metadata and every unknown field are rejected. Invalid bodies and targets do
not trigger key resolution or nonce reservation, and errors do not disclose
supplied JSON or target material.

After those gates, the complete composition verifies the exact body and signing
actor and reserves the nonce atomically. This result is an intent, not a
liveness write. The repository requires an existing nonsuspended registration
and atomically records server-side acceptance time with its audit event. The
handler enforces safe resolution, durable replay, and admission before calling
it. It never trusts the client's `Date`, `created`, or `expires` values as the
heartbeat-recency timestamp.

## Unregister request boundary

Unregister reuses the strict bounded identity-object parser but accepts only its
own operation and exact query-free target. Registration metadata, heartbeat
requests, duplicate or unknown fields, noncanonical actors, and ambiguous
targets are rejected before key resolution or nonce reservation. Errors do not
disclose supplied body or target material.

The complete composition then verifies the exact body and signing actor and
reserves the nonce atomically. The result is a removal intent, not a deletion.
The state transition is idempotent, returns only the closed `removed` or
`absent` outcome, and retains suspension, moderation, and audit records. The
handler invokes it only after authenticated replay-protected admission, and
only a separately reviewed retention policy may permit history removal.

## Persistence boundary

The SQLite foundation is restricted to one active service process on one host
and local storage. The opener requires an absolute, nonsymlink regular file,
creates it with mode `0600`, and rejects group- or world-accessible existing
files. The containing directory must also be owner-only. SQLite files must not
be shared over NFS or between hosts.

Embedded migrations run transactionally and record a SHA-256 digest. Changed,
missing, or future migration history fails closed. Relay lifecycle and
administrative state remain separate, unregister preserves state, and minimal
audit events are append-only. Replay storage receives only opaque 32-byte
digests, never raw key IDs or nonces. The schema does not admit connected-site,
follower, user, or raw request-body data.

Startup now requires a secure database path, migrates before listening, and
keeps readiness dependent on the current reachable schema. Public readiness
errors are fixed and do not expose filesystem paths or database details. The
container confines persistent writes to an owner-only named volume while its
root filesystem remains read-only.

Lifecycle inputs are canonicalized and byte-bounded again at the repository.
State time cannot precede the actor's current state or latest lifecycle or
moderation audit event.
Register and heartbeat cannot clear or bypass suspension; heartbeat cannot
create a missing registration; unregister preserves suspension. Each successful
outcome and event commit in one immediate transaction, and forced event failure
rolls the state mutation back.

The dormant moderation repository requires an existing retained relay, so it
cannot preemptively suspend an identity the directory has never recorded.
Suspend and restore are idempotent and each accepted operator decision receives
an append-only private event in the same transaction as any state change.
Moderator identifiers and reason codes use bounded token alphabets; free-form
notes are not stored. Those fields, database details, and internal moderation
outcomes must not reach public errors or listing data. No HTTP endpoint or CLI
invokes this repository yet. See `docs/MODERATION.md`.

Before production deployment, backup/restore and the disabled/enabled lifecycle
boundary must be exercised. Database errors must continue to fail closed
without reaching clients. See `docs/HANDLERS.md` for the reviewed handler order,
fixed initial limits, proxy trust boundary, and HTTP mappings.
