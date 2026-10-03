# Public directory views

## Boundary

Public directory presentation is independently controlled by
`DIRECTORY_PUBLIC_LISTING_ENABLED` and defaults disabled. When it is disabled,
none of the directory presentation routes are registered. Enabling it registers:

- `GET`/`HEAD` `/v1/relays` — the frozen version 1 compatibility listing;
- `GET`/`HEAD` `/v2/relays` — the richer 1.1 evidence projection;
- `GET`/`HEAD` `/` — the human view of the same v2 projection; and
- `GET`/`HEAD` `/assets/directory.css` — the bundled stylesheet.

This gate does not enable lifecycle registration, open enrollment, enable
background reachability, or create any public mutation route. All three public
views remain read-only and use a repository constructed with writes denied.

The public boundary never exposes moderation identities/reasons/events,
discovery source kind or source label, operator IDs or reason codes, private
probe errors, database identifiers or paths, client addresses, signatures,
nonces, signing-key IDs, resolver details, or storage errors. Optional human
operator-contact metadata is presentation-only and is never inserted into either
JSON projection.

## Frozen `/v1/relays` compatibility listing

`/v1/relays` remains byte- and semantic-compatible with the 1.0 contract. It
contains only canonical relay actor, canonical public base URL,
`healthy|stale|dead`, and server-owned UTC `last_seen_at`, plus schema and
pagination metadata. It never includes discovered-only relays or any of the 1.1
reachability fields.

The default page size is 50 and the maximum is 100. Ordering is the existing
indexed `(last_seen_at_unix, relay_actor)` keyset order. SQLite enforces all v1
public eligibility before rows reach HTTP: lifecycle is `registered`,
administrative state is `active`, last-seen is strictly newer than the fixed
30-day cutoff, and the row is after the supplied keyset. `pruned`, unregistered,
suspended, and exactly-30-day-old rows therefore do not appear in v1.

The opaque URL-safe v1 cursor contains its version, the first-page observation
time, and the last keyset position. It is authenticated with a process-local
random HMAC-SHA-256 key, expires after five minutes, and is intentionally invalid
after process restart. Later v1 pages reuse the authenticated captured
observation time so stable data does not change health classification or the
30-day cutoff during one bounded page walk.

## `/v2/relays` operational evidence projection

`/v2/relays` is the richer public projection introduced in 1.1 and extended in
1.2. The response schema is now version 3. Each relay object contains only
reviewed public evidence:

- canonical `relay_actor` and `public_base_url`;
- numeric operational `tier` (`1` through `4`);
- `heartbeat.state` plus authenticated `last_seen_at`, or `not_observed` and
  `null` when no retained authenticated Directory observation exists;
- independent actor `reachability.state`, `last_checked_at`, and
  `last_success_at`;
- the validated actor-declared inbox, declaration time, non-mutating inbox probe
  state, and last diagnostic time; and
- RFC 9421 `verified|not verified` plus the positive-evidence timestamp when
  verified.

Heartbeat and reachability are deliberately independent. For example, a relay
may truthfully be `Heartbeat: stale` while `Reachability: reachable`. A failed
actor probe does not rewrite authenticated heartbeat recency, and a successful
actor probe never fabricates a heartbeat or RFC 9421 verification.

Administrative suspension and explicit lifecycle/discovery removal still hide
an identity. Otherwise the richer projection retains known verified identities
through temporary and long-term outages and assigns one of four operational,
non-prestige tiers:

1. **Tier 1 — Heartbeat + Online.** The relay has an actual accepted heartbeat
   within the fixed 36-hour healthy window and a successful actor check within
   the fixed six-hour reachability freshness window.
2. **Tier 2 — Online, no current heartbeat.** The actor is currently reachable,
   but there is no current accepted heartbeat. This includes discovered relays
   and lifecycle relays whose heartbeat is absent or stale.
3. **Tier 3 — Offline / Unreachable.** The relay is known to the Directory but
   does not have a current successful actor check and has been seen online
   within the last 180 days.
4. **Tier 4 — Graveyard.** The relay has not been seen online for at least 180
   days. It remains retained and is still checked periodically so recovery can
   move it back to Tier 1 or Tier 2.

For graveyard age, the Directory uses the newest trustworthy online evidence:
authenticated lifecycle `last_seen_at`, successful actor reachability, or the
first known time when no later successful evidence exists. Unverified retained
`--add-dead-relays` candidates are private maintenance state and do **not** enter
any public tier until a later actor check successfully validates and promotes
them to an active discovery.

Discovery provenance is not part of the projection. Manual discovery, file
import, self-registration, operator/source labels, and reason codes remain
intentionally indistinguishable to public clients.

### v2 pagination and tier ordering

The page size defaults to 50 and is capped at 100. One request examines at most
400 retained actor identities, even when the requested tier is sparse. Results
are ordered first by operational tier (`1`, `2`, `3`, `4`) and then
alphabetically by canonical relay actor, which keeps normalized hostnames
alphabetical inside each tier. Heartbeat frequency, check recency, popularity,
traffic, or any other mutable activity metric never affects rank within a tier.
If the 400-identity scan bound is reached before a public row is found, the page
may contain zero rows with a continuation cursor; following that cursor resumes
the bounded tier scan without exposing the skipped identity.

The authenticated v2 cursor format is version 2 and contains its issue time,
tier, and canonical actor position. It uses the same process-local
HMAC-SHA-256 key and five-minute maximum walk age as v1, but v1 and v2 cursors
remain intentionally non-interchangeable. The tier is part of the keyset because
tier is now the primary public ordering key.

Unlike v1's observation-pinned compatibility walk, each v2 page evaluates the
latest retained evidence against that request's current server time. Tier and
reachability can therefore legitimately change between pages. The keyset keeps
forward progress bounded, but v2 remains a live view rather than a historical
snapshot; clients that require a fresh complete ordering should begin a new walk
from the first page.

HTTP presentation revalidates canonical identities, evidence relationships,
strict `(tier, actor)` ordering, page bounds, and direction-appropriate
repository keysets before serialization. `/v2/relays` remains forward-only;
reverse keysets are used only by the human directory's **Previous page**
control. Malformed, noncanonical, oversized, expired, future-time,
foreign-process, duplicate, tampered, cross-version, or otherwise invalid
pagination fails with a fixed redacted error.

## HTTP caching and admission

Successful JSON and HTML representations are deterministic bytes with
`Cache-Control: public, max-age=60, must-revalidate` and a strong SHA-256 ETag
over the exact body. `If-None-Match` supports normal and weak entity-tag
comparison for `GET`/`HEAD` revalidation. Error responses are `no-store`.

The v1 JSON, v2 JSON, and human page share one in-process public-read concurrency
ceiling of 16 requests, independent of signed lifecycle source/actor admission.
Saturation returns a fixed HTTP 429 response with a bounded retry hint.
Repository reads have a two-second request deadline. Security headers are
inherited from the common HTTP wrapper and there is no CORS write surface.

## Human-readable view

`GET`/`HEAD` `/` renders the **v2** bounded projection through Go
`html/template`; it does not have a second repository query or eligibility rule.
Its pagination uses the same signed v2 tier-plus-actor keyset token format,
current-per-page evidence evaluation, page-size bounds, and five-minute walk
lifetime. Forward links use the existing `cursor` query parameter. The human
page additionally accepts a `before` query parameter for bounded reverse
traversal; `/v2/relays` remains forward-only and rejects `before`. Both
directions preserve the cursor's original issue time, so moving backward does
not restart the five-minute walk lifetime. A signed cursor token issued by
`/v2/relays` is accepted by `/` and vice versa; neither token format is accepted
by `/v1/relays`.

The human page uses compact responsive relay rows. Each row shows the relay,
heartbeat, reachability, last heartbeat, and last reachability check. Expanding
a row shows the actor, inbox, and last successful check. Inbox probe diagnostics
and RFC 9421 verification details remain part of the v2 JSON projection but are
intentionally omitted from the human directory.

A bounded page containing no public rows but at least one previous/next
continuation cursor is presented as an empty **page**, not as an empty
directory.

Go templates provide automatic HTML escaping. Relay public base URLs are the
only relay-controlled outbound links; relay HTML, images, scripts, styles,
fonts, and other remote resources are never fetched. The page uses a bundled
same-origin stylesheet and no JavaScript.

HTML responses use the same one-minute cache policy and exact-byte SHA-256 ETag
semantics as JSON. The HTML page overrides the common deny-by-default CSP only
to allow its same-origin stylesheet; scripts, images, fonts, connections,
frames, objects, media, forms, and base-URI changes remain denied. Error
responses are fixed, redacted, and `no-store`.

## Public-facing presentation

`GET /` is a public product surface, not an administrative diagnostic. It uses
an Activity-Relay-family layout with a branded header, responsive relay cards,
human-readable evidence context, and intentional empty states. The versioned
JSON APIs remain available but the human page does not advertise or link to
them.

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
markers, escaping, caching, privacy-field absence, and the shared v2 JSON/HTML
projection automatically. Release-candidate acceptance separately includes a
human browser review of the rendered public page.

### Color-vision accessibility

Evidence-state meaning must not depend on hue alone. Heartbeat and reachability
badges retain visible state words. The stylesheet reinforces them with distinct
non-color cues: check marks and solid borders for positive states, punctuation
and dashed/double borders for aging states, multiplication marks for negative
states, and question marks/dotted borders for unknown/not-observed states.

Development validation maintains at least 4.5:1 text contrast for the reviewed
light and dark palette combinations and regression-tests visible text plus
non-color cues. Color-vision-deficiency simulation is useful as a design
diagnostic, including protanopia, deuteranopia, and tritanopia review, but it is
not a substitute for color-independent semantic cues or human RC browser review.

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
`/usr/share/doc/activity-relay-directory/examples/config.yml.example`; it does
not install an active `config.yml`. The stock container image follows the same
model: it creates the empty default parent and includes the example under
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
`DIRECTORY_*` runtime environment, is not emitted by `/v1/relays`,
`/v2/relays`, or `/v1/status`, and does not make `DIRECTORY_ADMIN_EMAIL` public.
Unknown YAML fields and structural file failures remain startup errors. Once the
file parses successfully, malformed Nice-to-have values and incomplete
Fediverse pairs are non-blocking: unsafe or partial values are suppressed and
the human page shows deterministic configuration diagnostics.

The absence of the former on-page "Privacy boundary" panel does not widen the
projection. The public data boundary remains enforced by the repository,
eligibility, moderation, evidence-validation, and serialization contracts above.
