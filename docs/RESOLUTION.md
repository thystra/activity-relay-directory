# ActivityPub Actor and Key Resolution

## Status

`internal/actorresolver.Resolver` is a dormant implementation of the version 1
RFC 9421 key-resolver interface. Constructing it performs no DNS or HTTP work.
The running directory does not construct it, no verifier receives it, and no
public handler can trigger actor retrieval in this tranche.

The resolver follows the ActivityPub and ActivityStreams actor representation
defined by:

- <https://www.w3.org/TR/activitypub/>; and
- <https://www.w3.org/TR/activitystreams-core/>.

## Key-ID boundary

Version 1 accepts only an already canonical HTTPS key ID consisting of a relay
actor URL plus a nonempty unreserved fragment such as `#main-key`. The fragment
is retained as the cryptographic key identity and removed from the one actor
URL sent over HTTP. Key IDs are limited to 2048 bytes and fragments to 128
bytes. Credentials, queries, encoded or ambiguous fragments, surrounding
whitespace, and noncanonical URLs fail before DNS or HTTP access.

## Network-target policy

The production client has no proxy callback, so `HTTP_PROXY`, `HTTPS_PROXY`,
and related environment variables cannot route actor retrieval. Every new
connection resolves at most 16 IP addresses. A direct literal or every DNS
answer must be a permitted public address; one prohibited answer rejects the
entire result. The socket is opened against a selected validated address rather
than resolving the hostname again, while TLS continues to authenticate the
original hostname.

The policy rejects private, loopback, link-local, carrier-grade NAT,
documentation, benchmarking, translation, transition, multicast, unspecified,
reserved, and other non-public ranges. IPv6 is additionally limited to the
allocated global-unicast `2000::/3` space and excludes current IANA
special-purpose ranges. Release review must compare these exclusions with the
current registries:

- <https://www.iana.org/assignments/iana-ipv4-special-registry/>; and
- <https://www.iana.org/assignments/iana-ipv6-special-registry/>.

Only canonical HTTPS redirects are followed. Each redirect is syntactically
revalidated and each resulting connection repeats DNS/address validation and
pinning. At most three redirects are accepted. Canonical non-default HTTPS
ports remain supported. TLS 1.2 is the minimum; connection, TLS, response-
header, whole-request, header-size, and idle bounds are fixed in the client.

## HTTP and document boundary

Actor retrieval sends `GET` with ActivityStreams content negotiation and a
bounded operator-selected User-Agent. Only `200 OK` with either
`application/activity+json` or the ActivityStreams profile of
`application/ld+json` is accepted. A body is limited to 256 KiB after transport
decoding. Generic JSON and ambiguous or repeated Content-Type headers fail
closed.

JSON permits unknown ActivityStreams extensions but rejects duplicate member
names at every object depth, trailing values, nesting beyond 32 levels, and
containers above 4096 entries. The actor must:

- have an `id` exactly equal to the fragment-free requested URL;
- include `Application` or `Service` in its type;
- publish no more than eight embedded public keys; and
- publish exactly one key whose `id` is the requested key ID and whose `owner`
  is the actor ID.

No secondary key URL, JSON-LD context, inbox, image, attachment, or other actor-
controlled URL is fetched.

## RSA key boundary

The selected `publicKeyPem` is limited to 16 KiB and must contain exactly one
header-free PEM block plus trailing whitespace. X.509 SubjectPublicKeyInfo
`PUBLIC KEY` and legacy PKCS#1 `RSA PUBLIC KEY` blocks are accepted. Other key
types, private keys, malformed or trailing data, even exponents, and RSA moduli
outside 2048 through 8192 bits fail closed.

The resolver returns only the exact key ID, canonical owner/actor identity, and
parsed RSA public key. The RFC 9421 verifier maps every resolver failure to its
fixed public key error, so supplied hosts, key IDs, network details, documents,
and parser failures are not exposed to request clients.

## Remaining gates

Before handler composition, the project still requires reviewed source and
actor rate policy, concurrency admission, bounded cache behavior, moderation,
durable replay scheduling, public error/status mapping, and complete handler
tests. The resolver by itself does not authorize registration or deployment.
