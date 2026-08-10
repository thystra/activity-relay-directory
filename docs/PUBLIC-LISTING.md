# Public directory views

## Boundary

The version 1 public listing is `GET`/`HEAD` at `/v1/relays`. It is independently
controlled by `DIRECTORY_PUBLIC_LISTING_ENABLED`, defaults disabled, and does
not depend on lifecycle availability or enrollment being open. A disabled
listing route is not registered.

The response exposes only canonical relay actor, canonical public base URL,
`healthy|stale|dead`, and server-owned `last_seen_at` in UTC, plus schema and
pagination metadata. It never exposes moderation identities/reasons/events,
first-registration time, internal lifecycle state, database identifiers,
client network addresses, signatures, nonces, resolver details, or storage
errors.

## Pagination and projection

The default page size is 50 and the maximum is 100. Ordering is the existing
indexed `(last_seen_at_unix, relay_actor)` keyset order. The opaque URL-safe
cursor carries a version, the first-page observation time, and the last keyset
position. It is authenticated with a process-local random HMAC-SHA-256 key,
expires after five minutes, and is intentionally invalid after process restart.
This prevents clients from rewriting or indefinitely replaying the captured
observation time to recover relays outside the current public-eligibility window.
Later pages reuse the authenticated captured observation time so stable data
does not change health classification or the 30-day eligibility cutoff during
a bounded page walk. Malformed, noncanonical, oversized, expired, future-time,
foreign-process, duplicate, tampered, or otherwise invalid pagination input
fails with the same fixed redacted error.

The SQLite public query uses `relays_health_projection_idx` and enforces all
public eligibility before rows reach HTTP: lifecycle must be `registered`,
administrative state must be `active`, last-seen must be strictly newer than
the fixed 30-day cutoff, and the keyset must be after the supplied cursor.
`pruned`, unregistered, suspended, and exactly-30-day-old rows therefore cannot
reach presentation code.

## HTTP caching and admission

Successful representations are deterministic struct-backed JSON terminated by
a newline. They carry `Cache-Control: public, max-age=60, must-revalidate` and a
strong SHA-256 ETag over the exact response bytes. `If-None-Match` supports
normal and weak entity-tag comparison for `GET`/`HEAD` revalidation. Error
responses are `no-store`.

Public listing work has its own in-process concurrency ceiling of 16 requests,
independent of signed lifecycle source/actor admission. Saturation returns a
fixed HTTP 429 response with a bounded retry hint. Repository reads have a
two-second request deadline. Security headers are inherited from the common
HTTP wrapper and the listing defines no write method or CORS write surface.

## Human-readable view

When the public listing is enabled, `GET`/`HEAD` `/` renders the same bounded
projection through Go `html/template`; it does not have a second repository or
eligibility query. The HTML route and `/assets/directory.css` are registered
under the same `DIRECTORY_PUBLIC_LISTING_ENABLED` gate as `/v1/relays`.

HTML pagination uses the same authenticated five-minute cursor and page-size
bounds as JSON, so ordering, observation time, health classification, and the
30-day public-eligibility cutoff cannot drift between representations. A cursor
issued by one representation is accepted by the other while it remains valid.

The initial HTML page contains only the public JSON relay fields plus explanatory
labels and health definitions. Go templates provide automatic HTML escaping.
Relay public base URLs are the only relay-controlled outbound links; relay HTML,
images, scripts, styles, fonts, and other remote resources are never fetched.
The page uses a bundled same-origin stylesheet and no JavaScript.

HTML responses use the same one-minute cache policy and exact-byte SHA-256 ETag
semantics as JSON. The HTML page overrides the common deny-by-default CSP only
to allow its same-origin stylesheet; scripts, images, fonts, connections,
frames, objects, media, forms, and base-URI changes remain denied. Error
responses are fixed, redacted, and `no-store`.
