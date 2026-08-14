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

The relay-listing portion of the HTML page contains only the public JSON relay
fields plus explanatory labels and health definitions. Optional operator contact
links are separate operator-owned presentation metadata described below and do
not widen the relay projection. Go templates provide automatic HTML escaping.
Relay public base URLs are the only relay-controlled outbound links; relay HTML,
images, scripts, styles, fonts, and other remote resources are never fetched.
The page uses a bundled same-origin stylesheet and no JavaScript.

HTML responses use the same one-minute cache policy and exact-byte SHA-256 ETag
semantics as JSON. The HTML page overrides the common deny-by-default CSP only
to allow its same-origin stylesheet; scripts, images, fonts, connections,
frames, objects, media, forms, and base-URI changes remain denied. Error
responses are fixed, redacted, and `no-store`.

## Public-facing presentation

`GET /` is a public product surface, not an administrative diagnostic. It uses
the same bounded, filtered projection as `GET /v1/relays`, but presents that
data in an Activity-Relay-family layout with a branded header, responsive relay
cards, human-readable health context, and an intentional empty state. The
versioned JSON API remains available at `GET /v1/relays`, but the human page
does not advertise or link to it.

The view remains dependency-free and privacy-bounded:

- no JavaScript is required;
- no remote fonts, analytics, images, third-party scripts, or relay-controlled
  resources are fetched;
- the stylesheet is embedded in the binary and served at
  `/assets/directory.css`;
- the HTML route's CSP permits that same-origin stylesheet with
  `style-src 'self'`; and
- reverse proxies must not override the application's route-specific CSP.

Development tests verify HTML, CSS delivery, CSP compatibility, accessibility
markers, escaping, caching, and the shared JSON/HTML projection automatically.
Release-candidate acceptance separately includes a human browser review of the
rendered public page.

### Color-vision accessibility

Health state meaning must not depend on hue alone. Every public relay badge
retains the visible text `healthy`, `stale`, or `dead`. The stylesheet also
reinforces those states with distinct non-color cues: a check mark and solid
border for healthy, an exclamation mark and dashed border for stale, and a
multiplication mark and double border for dead.

Development validation maintains at least 4.5:1 text contrast for the reviewed
light and dark palette combinations and regression-tests the visible text plus
non-color cues. Color-vision-deficiency simulation is useful as a design
diagnostic, including protanopia, deuteranopia, and tritanopia review, but it is
not a substitute for the color-independent semantic cues or for human RC
browser review.

Relevant references:

- WCAG 2.2, Success Criterion 1.4.1, Use of Color:
  https://www.w3.org/WAI/WCAG22/Understanding/use-of-color
- WCAG 2.2, Success Criterion 1.4.3, Contrast (Minimum):
  https://www.w3.org/TR/WCAG22/#contrast-minimum
- Machado, Oliveira, and Fernandes (2009), *A physiologically-based model for
  simulation of color vision deficiency*, DOI `10.1109/TVCG.2009.113`.

## Optional public operator contact

The human `GET /` page may display operator-owned contact links from the optional
YAML file `/etc/activity-relay-directory/config.yml`. The Debian package owns the
empty parent directory and installs an example at
`/usr/share/doc/activity-relay-directory/examples/config.yml.example`; it does not install an active `config.yml`. The stock container image follows the
same model: it creates the empty default parent and includes the example under
`/usr/share/doc/activity-relay-directory/examples/`, while the base Compose file
forwards `DIRECTORY_CONFIG_PATH` without binding any host file. Set
`DIRECTORY_CONFIG_PATH` to a clean absolute path to use another file, such as a
read-only container mount. When the default path is absent, or all supported
values are empty, no operator-contact label or placeholder is rendered.

Supported keys are:

```yaml
OPERATOR-WEBSITE: "https://operator.example/"
OPERATOR-EMAIL: "operator@example.org"
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
```

`OPERATOR-WEBSITE` and `OPERATOR-EMAIL` are independently optional. The two
Fediverse values are a pair: either both are present or both are absent. The
displayed `@user@host` identifier links to the explicit HTTPS profile URL; the
Directory never derives a profile URL because Friendica, Mastodon, and other
Fediverse applications use different URL layouts.

This YAML is public-presentation metadata only. It does not replace the
`DIRECTORY_*` runtime environment, is not emitted by `/v1/relays` or
`/v1/status`, and does not make `DIRECTORY_ADMIN_EMAIL` public. Unknown YAML
fields, unsafe URLs, ambiguous email syntax, and incomplete Fediverse pairs fail
startup when the file is explicitly configured.

The absence of the former on-page "Privacy boundary" panel does not widen the
projection. The public data boundary remains enforced by the same repository,
eligibility, moderation, health, and serialization contracts described above.
