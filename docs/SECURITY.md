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

Canonical syntax is not an SSRF decision. The dormant actor resolver separately
resolves all addresses, rejects prohibited local, private, link-local,
documentation, multicast, and otherwise non-public targets, pins each
connection to an approved address, and repeats the checks on every bounded
redirect. It then binds the resolved actor and signing key to the canonical
identity. Runtime composition remains unavailable; see `docs/RESOLUTION.md`.

## Content digest

Version 1 requires an RFC 9530 `sha-256` Content-Digest over the exact bounded
request body bytes. Verification is constant-time after strict Structured
Fields parsing and never includes an attacker-controlled field value in an
error. JSON whitespace and other byte-level changes therefore invalidate an
otherwise well-formed digest.

A content digest is not sender authentication. Future request handlers must
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
or multiple service instances. The dormant SQLite implementation is the
durable single-host store: it accepts only opaque 32-byte keys, persists across
restart, uses exact inclusive expiry, and prunes a bounded number of expired
rows in the reservation transaction. It is not suitable for a multi-host
service. Rate policy, bounded maintenance, operational failure handling, and
explicit verifier wiring remain mandatory before handler wiring. The dormant
resolver now enforces DNS, address, redirect, response-size, media-type, actor-
document, and RSA-key safety before returning resolved key material.

## Register request boundary

The register contract accepts only a bounded, single top-level JSON object. It
rejects duplicate and unknown member names, trailing values, wrong versions or
operations, noncanonical or cross-origin identities, and any target other than
the exact query-free register endpoint. Errors do not repeat supplied JSON
names, values, URLs, queries, or fragments.

These semantic and target gates run before key resolution and replay
reservation. The complete composition then authenticates the exact body, binds
the signing actor, and atomically reserves the nonce. It returns an intent and
does not write registration state. The dormant repository revalidates canonical
bounded identity and can commit an audited state transition, but safe resolver
composition, durable replay wiring, moderation, rate limiting, and explicit
handler review remain required before registration can be enabled.

## Heartbeat request boundary

Heartbeat reuses the strict bounded-object parser and exact-target checks but
accepts only its own operation plus one canonical actor identity. Registration
metadata and every unknown field are rejected. Invalid bodies and targets do
not trigger key resolution or nonce reservation, and errors do not disclose
supplied JSON or target material.

After those gates, the complete composition verifies the exact body and signing
actor and reserves the nonce atomically. This result is an intent, not a
liveness write. The dormant repository requires an existing nonsuspended
registration and atomically records server-side acceptance time with its audit
event. A future handler must still enforce safe resolution, use the durable
replay store, and apply rate policy before calling it. It must not trust the
client's `Date`, `created`, or `expires` values as the heartbeat-recency
timestamp.

## Unregister request boundary

Unregister reuses the strict bounded identity-object parser but accepts only its
own operation and exact query-free target. Registration metadata, heartbeat
requests, duplicate or unknown fields, noncanonical actors, and ambiguous
targets are rejected before key resolution or nonce reservation. Errors do not
disclose supplied body or target material.

The complete composition then verifies the exact body and signing actor and
reserves the nonce atomically. The result is a removal intent, not a deletion.
The dormant state transition is idempotent, returns only the closed `removed`
or `absent` outcome, and retains suspension, moderation, and audit records. No
handler invokes it, and only a separately reviewed retention policy may permit
history removal.

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
State time cannot precede the actor's current state or latest audit event.
Register and heartbeat cannot clear or bypass suspension; heartbeat cannot
create a missing registration; unregister preserves suspension. Each successful
outcome and event commit in one immediate transaction, and forced event failure
rolls the state mutation back.

Before production deployment, backup/restore must be exercised. Before request
handlers are enabled, durable replay composition, cleanup scheduling, rate
policy, and all remaining request gates must be bounded and tested; database
errors must continue to fail closed without reaching clients.
