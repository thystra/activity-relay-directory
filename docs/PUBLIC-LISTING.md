# Public directory views

## Exposed Endpoints

Public directory presentation is independently controlled by
`DIRECTORY_PUBLIC_LISTING_ENABLED` and defaults disabled. When it is disabled,
none of the directory presentation routes are registered. Enabling it registers:

- `GET`/`HEAD` `/v1/relays` — the frozen version 1 compatibility listing;
- `GET`/`HEAD` `/v2/relays` — the richer 1.1 evidence projection;
- `GET`/`HEAD` `/` — the human view of the same v2 projection;
- `GET`/`HEAD` `/downloads/active.txt` — Tier 1 and Tier 2 host names;
- `GET`/`HEAD` `/downloads/all.txt` — all four public tiers;
- `GET`/`HEAD` `/downloads/unavailable.txt` — Tier 3 and Tier 4 host names; and
- `GET`/`HEAD` `/assets/directory.css` — the bundled stylesheet.

This gate does not enable lifecycle registration, open enrollment, enable
background reachability, or create any public mutation route. All public
listing, human-directory, and download views remain read-only and use a
repository constructed with writes denied.

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
30-day cutoff during one page walk.

## `/v2/relays` public projection

`/v2/relays` is the richer public projection introduced in 1.1 and extended in
1.2, 1.3, and 1.3.1. The response schema is now version 6. Each relay object
contains only reviewed public evidence:

- canonical `relay_actor` and `public_base_url`;
- numeric operational `tier` (`1` through `4`);
- `heartbeat.state` plus the retained authenticated liveness/heartbeat time in
  `last_seen_at`, or `not_observed` and `null` when no such evidence exists;
- independent actor `reachability.state`, `last_checked_at`, and
  `last_success_at`, with optional classified `diagnostic` evidence and
  `next_eligible_at` timestamp;
- the validated actor-declared inbox, declaration time, non-mutating inbox probe
  state, last check time, and optional classified `diagnostic` evidence; and
- RFC 9421 `verified|not verified` plus the positive-evidence timestamp when
  verified.

The projection retains the schema-4 `profile` object containing the effective
reviewed descriptive relay profile: `participation_mode`, `availability`,
`relay_type`,
`languages`, `countries`, `regions`, `topics`, `contact_fediverse`,
`contact_email`, `contact_url`, `participation_url`, and `notes`. Missing scalar
values are empty strings and missing multi-value fields are empty arrays. The
public object never includes source kind, source label, source URL, precedence,
pending values, history, operator IDs, or reconciliation state. Profile values
do not affect public eligibility or operational tier placement.

Heartbeat and reachability are deliberately independent. For example, a relay
may truthfully be `Heartbeat: stale` while `Reachability: reachable`. A failed
actor probe does not rewrite authenticated heartbeat recency, and a successful
actor probe never fabricates a heartbeat or RFC 9421 verification.

Administrative suspension and explicit lifecycle/discovery removal still hide
an identity. Otherwise the richer projection retains known verified identities
through temporary and long-term outages and assigns one of four operational
groups (represented by unchanged numeric API `tier` values):

1. **ARD White Pages Protocol Relays** (API tier `1`). The relay reports
   through the existing authenticated lifecycle protocol within the 36-hour
   heartbeat window **and** its ActivityPub Actor was validated within six hours.
2. **Other Known Relays** (API tier `2`). The relay has either a recent
   authenticated heartbeat **or** a successful ActivityPub Actor check, but not
   both. This group can include White Pages participants whose Actor check
   failed; the label does not claim that they never joined the protocol.
3. **Offline or Unreachable Relays** (API tier `3`). Neither of those two
   signals is current, but the relay was last confirmed online within 30 days.
   These checks cannot prove the relay is permanently offline.
4. **Graveyard** (API tier `4`). The relay has not been confirmed online for
   at least 30 days. It remains listed and is checked periodically for recovery.

For graveyard age, the Directory uses the newest trustworthy online evidence:
authenticated lifecycle `last_seen_at`, successful actor reachability, or the
first known time when no later successful evidence exists. Unverified retained
`--add-dead-relays` candidates are private maintenance state and do **not** enter
any public tier until a later actor check successfully validates and promotes
them to an active discovery. The human `/` page may publish only their aggregate
pending-verification count alongside aggregate known/online/offline relay
counts. Candidate identities, failure details, source labels, and other
provenance remain private.

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
If the 400-identity scan limit is reached before a public row is found, the page 
may contain zero rows with a continuation cursor; following that cursor resumes 
the tier scan from that position without exposing the skipped identity.

The authenticated v2 cursor format is version 2 and contains its issue time,
tier, and canonical actor position. It uses the same process-local
HMAC-SHA-256 key and five-minute maximum walk age as v1, but v1 and v2 cursors
remain intentionally non-interchangeable. The tier is part of the keyset because
tier is now the primary public ordering key.

Unlike v1's observation-pinned compatibility walk, each v2 page evaluates the
latest retained evidence against that request's current server time. Tier and
reachability can therefore legitimately change between pages. The keyset ensures 
each page resumes after the previous tier-and-actor position, but v2 remains a
live view rather than a historical snapshot. Tier and reachability may change 
while a client is paging through the directory. Clients that require a fresh 
complete ordering should begin again from the first page.

HTTP presentation revalidates canonical identities, evidence relationships,
strict `(tier, actor)` ordering, page bounds, and direction-appropriate
repository keysets before serialization. `/v2/relays` remains forward-only;
reverse keysets are used only by the human directory's **Previous page**
control. Malformed, noncanonical, oversized, expired, future-time,
foreign-process, duplicate, tampered, cross-version, or otherwise invalid
pagination fails with a fixed redacted error.

## Plain-text downloads and local export

The three `/downloads/*.txt` routes use the same reviewed v2 public projection
and are enabled only with the public listing. They contain one normalized host
per line with no comments or private provenance. A non-default HTTPS port is
retained as `host:port`. The files preserve public tier order and alphabetical
actor order inside each tier:

- `active.txt` contains Tier 1 and Tier 2;
- `unavailable.txt` contains Tier 3 and Tier 4; and
- `all.txt` contains Tier 1 through Tier 4.

The download renderer walks v2 pages using one captured observation
time, rejects non-progressing pagination, rejects duplicate actor rows, and
fails closed rather than returning a partial list after 256 pages or more than
10,000 exported relays. Query parameters are not accepted. Successful downloads
use the same one-minute public cache and strong ETag policy as the other public
representations.

Local operators can use the same projection without enabling public listing:

```sh
activity-relay-directory admin export --scope active --format hosts
activity-relay-directory admin export --scope all --format actors
activity-relay-directory admin export --scope unavailable --format hosts
activity-relay-directory admin export --scope all --format csv
```

The local command opens the current database read-only. `hosts` is suitable as
input to a later discovery import, including canonical non-default HTTPS ports;
`actors` emits the canonical `/actor` URLs instead. `csv` adds the effective
reviewed descriptive profile for those same verified public-tier identities,
omits private provenance and unresolved candidates, and uses the spreadsheet-safe
round-trip rules in `docs/RELAY-PROFILES.md`. Public `/downloads/*.txt` routes
remain host-only.

## HTTP caching and admission

Successful JSON, HTML, and text-download representations are deterministic bytes with
`Cache-Control: public, max-age=60, must-revalidate` and a strong SHA-256 ETag
over the exact body. `If-None-Match` supports normal and weak entity-tag
comparison for `GET`/`HEAD` revalidation. Error responses are `no-store`.

The v1 JSON, v2 JSON, human page, and text downloads share one in-process
public-read concurrency ceiling of 16 requests, independent of signed lifecycle
source/actor admission.
Saturation returns a fixed HTTP 429 response with a limited retry hint.
Repository reads have a two-second request deadline. Security headers are
inherited from the common HTTP wrapper and there is no CORS write surface.

## Public-facing directory

`GET`/`HEAD` `/` renders the **v2** projection through Go
`html/template`; it does not have a second repository query or eligibility rule.
Its pagination uses the same signed v2 tier-plus-actor keyset token format,
current-per-page evidence evaluation, page-size bounds, and five-minute walk
lifetime. Forward links use the existing `cursor` query parameter. The public
page additionally accepts a `before` query parameter for reverse
pagination; `/v2/relays` remains forward-only and rejects `before`. Both
directions preserve the cursor's original issue time, so moving backward does
not restart the five-minute walk lifetime. A signed cursor token issued by
`/v2/relays` is accepted by `/` and vice versa; neither token format is accepted
by `/v1/relays`.

The public page uses compact responsive relay rows. Each row shows the relay,
heartbeat, reachability, last heartbeat, and last reachability check. Expanding
a row shows the actor, inbox, last successful check, and any non-empty effective
profile fields. Descriptive profile URLs are rendered only as their already
validated HTTPS values; profile text is rendered as escaped plain text. Inbox
probe diagnostics and RFC 9421 verification details remain part of the v2 JSON
projection but are intentionally omitted from the human directory.

A page containing no public rows but at least one previous/next
continuation cursor is presented as an empty **page**, not as an empty
directory.

Go templates provide automatic HTML escaping. Relay public base URLs are the
primary relay-controlled outbound links; schema-4 profile `contact_url` and
`participation_url` may also appear as validated HTTPS links. Relay HTML,
images, scripts, styles, fonts, and other remote resources are never fetched.
The page uses a bundled same-origin stylesheet and no JavaScript.

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

The footer displays the running application version beside the Directory name.
It uses the same runtime build identity exposed by `/v1/status`; there is no
separate public-page version setting or hard-coded template version. This
presentation detail does not change either relay-listing JSON schema.

The view remains dependency-free and privacy-focused:

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

The public `GET /` page may display operator-owned contact information from the optional
YAML file `/etc/activity-relay-directory/config.yml`. The Debian package owns the
empty parent directory and installs the package-managed example at
`/etc/activity-relay-directory/config.yml.example`; it does not install an
active `config.yml`. The stock container image follows the same model and places
the example at the same `/etc/activity-relay-directory/config.yml.example`
path, while the base Compose file
forwards `DIRECTORY_CONFIG_PATH` without binding any host file. Set
`DIRECTORY_CONFIG_PATH` to a clean absolute path to use another file, such as a
read-only container mount. When the default path is absent, or all supported
values are empty, no operator-contact label or placeholder is rendered.

Supported keys are:

```yaml
DIRECTORY-TITLE: "Community Relay Directory"
DIRECTORY-BANNER-URL: "https://directory.example.org/banner.jpg"
OPERATOR-WEBSITE: "https://operator.example/"
OPERATOR-EMAIL: "operator@example.org"
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
SUPPORT:
  - title: "Liberapay"
    url: "https://liberapay.com/example/"
  - title: "Bitcoin"
    value: "bc1qexample"
```

`OPERATOR-WEBSITE` and `OPERATOR-EMAIL` are independently optional values. The two
Fediverse values are a pair: either both are present or both are absent. The
displayed `@user@host` identifier links to the explicit HTTPS profile URL; the
Directory never derives a profile URL because Friendica, Mastodon, and other
Fediverse applications use different URL layouts.

`SUPPORT` is an optional ordered list of at most eight presentation-only methods.
Each entry requires a title and exactly one absolute HTTPS `url` or escaped
plain-text `value`. The human page renders valid entries in a collapsed `Support
this directory` block near the top. The Directory does not accept arbitrary
HTML, Markdown, scripts, embeds, pixels, or other third-party support content.

This YAML is public-presentation metadata only. It does not replace the
`DIRECTORY_*` runtime environment, is not emitted by `/v1/relays`,
`/v2/relays`, or `/v1/status`, and does not make `DIRECTORY_ADMIN_EMAIL` public.
Unknown YAML fields and structural file failures remain startup errors. Once the
file parses successfully, malformed Nice-to-have values and incomplete
Fediverse pairs are non-blocking: unsafe or partial values are suppressed and
the human page shows deterministic configuration diagnostics.

The public data exposed remains limited by the repository, eligibility, moderation, 
evidence-validation, and serialization rules described above.


### Schema 5 relay telemetry

Schema 5 adds a `telemetry` object to each `/v2/relays` relay containing
`receiving_instance_count` and `reported_at`. Both are nullable when a relay has
not reported telemetry. The count is self-reported and informational; it is not
a ranking or tier input. The human Directory exposes the count as **Sites** on
the compact row and keeps the report timestamp in expanded details.

The human page also permits `?registration=open`, `restricted`, or `closed`.
Within each operational tier, rows are ordered open, restricted, closed, then
unreported; this secondary ordering never permits a self-reported registration
status to change a relay's operational tier.


### Schema 6 reachability diagnostics (1.3.1)

Schema 6 adds optional `reachability.diagnostic` and `inbox.diagnostic` objects
with `stage`, `code`, `assessment`, and `http_status` (when an HTTP response
was received). It also adds optional `reachability.next_eligible_at`, the
earliest normal background recheck eligibility time, not a promised execution
time. The regular worker may run later. Missing diagnostic objects mean no
classified observation is retained; they do not imply a healthy endpoint.

Diagnostics distinguish DNS, connection, TLS, redirect, actor-document, and
non-mutating inbox `OPTIONS` outcomes. The `assessment` values are
`possibly_removed`, `degraded`, `inbox_missing`, `method_rejected`,
`restricted`, `responsive`, `invalid_actor`, and `unavailable`. Actor HTTP
404/410 and absent DNS names are only evidence of *possible* removal.
Method-rejected `OPTIONS` responses do not prove an inbox cannot receive
ActivityPub delivery. A failed actor check preserves earlier successful
observations and may leave older inbox diagnostic evidence visible.

No diagnostic field alters public eligibility, the four tiers, authenticated
heartbeat recency, soft pruning, moderation, or retention. Raw remote error
messages, response bodies, addresses, and private sources are not published.
See [`REACHABILITY-DIAGNOSTICS.md`](REACHABILITY-DIAGNOSTICS.md) for the closed
classification vocabulary, retention, and retry rules.


## Human-facing status and diagnostic labels (1.3.1-rc2)

The public page names the identity endpoint **ActivityPub Actor** (which
identifies the relay and describes its capabilities) and the message endpoint
**ActivityPub Inbox** (which receives ActivityPub messages). The Actor check
fetches and validates the actor; the Inbox check sends an HTTP `OPTIONS` request
without delivering an activity. An Inbox response of HTTP 405 or 501 does not
mean ActivityPub message delivery has failed.

The HTML heartbeat badges are **Healthy**, **Stale**, and **No heartbeat
received**. Five API states remain unchanged: `healthy` (within 36 hours),
`stale` (over 36 hours and under 7 days), `dead` (7 to under 30 days),
`prune` (30 days or older), and `not_observed` (never received). All three
overdue API states appear as **Stale** on the website. `prune` is a
heartbeat-age code, **not** an instruction to remove a relay; grouping and
maintenance are determined separately. A reachable Actor can keep an
old-heartbeat relay in **Other Known Relays** (API tier `2`); after 30 days
without positive evidence, it can appear in the **Graveyard** (API tier `4`)
with the heartbeat label **Stale**.

The HTML Actor status badges are **Reachable**, **Check failed**, and
**Not checked yet**. A failed Actor check reports the specific safe diagnostic
(DNS, connection, HTTPS/TLS, redirect, HTTP or document validation). The
Inbox details show the last check time when recorded and note that message
delivery was not tested. There is no generic warning that every Inbox result
may be outdated; timestamps show actual observation recency.

These are **presentation changes only**. The `/v2/relays` schema stays at 6:
`tier`, `heartbeat.state`, `reachability.state`, `inbox.probe_state`, and
optional structured `diagnostic` fields retain their existing values.
`/v1/relays` is unchanged. See [Reachability diagnostics](REACHABILITY-DIAGNOSTICS.md)
for the machine-readable outcome matrix.
