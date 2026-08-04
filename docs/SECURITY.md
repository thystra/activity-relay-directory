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

Canonical syntax is not an SSRF decision. Before any actor fetch exists, the
runtime must separately resolve all addresses, reject prohibited local,
private, link-local, documentation, multicast, and otherwise non-public
targets, pin each connection to an approved address, and repeat the checks on
every redirect. It must then bind the resolved actor and signing key to the
canonical registered identity.

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
or multiple service instances. A shared durable implementation and its
operational failure policy remain mandatory before handler wiring. Runtime key
retrieval must separately enforce DNS, address, redirect, response-size, media-
type, and actor-document safety before returning resolved key material.
