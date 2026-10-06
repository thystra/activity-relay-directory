# Relay profiles and 1.3 metadata contract

## Purpose

Activity-Relay Directory 1.3 will add descriptive relay metadata without
mixing operator/imported claims with the Directory's own operational evidence.
The feature has three inputs:

1. local operator imports, including spreadsheet-friendly CSV;
2. authenticated relay self-reporting through lifecycle Protocol v2;
3. optional local operator overrides for exceptional corrections.

The Directory must keep these descriptive inputs separate from heartbeat,
reachability, moderation, enrollment, pruning, tiering, and retention state.
No profile value may make a relay reachable, healthy, registered, admitted,
unsuspended, or publicly eligible.

This document freezes the design boundaries before storage, CSV, public-schema,
or cross-repository protocol implementation begins.

## Compatibility boundaries

The existing contracts remain stable:

- lifecycle Protocol v1 is frozen;
- `POST /v1/relays/register`, `/heartbeat`, and `/unregister` keep their existing
  request and response shapes;
- `/v1/relays` remains the frozen compatibility listing;
- the current newline-delimited discovery import remains supported;
- `admin export --format hosts|actors` keeps its current semantics; and
- public plain-text downloads remain host-only and do not expose profile or
  provenance data.

The read-only `/v2/relays` API is versioned independently from lifecycle
Protocol v2. Public profile projection moves `/v2/relays` from schema 3 to schema
4 while retaining the existing tier/evidence model.

## Identity is not profile data

These values are Directory identity/evidence and are never imported as profile
claims:

- canonical `relay_actor`;
- canonical `public_base_url` established by the reviewed identity path;
- actor-declared inbox and its observation times;
- heartbeat state or timestamps;
- reachability state or timestamps;
- RFC 9421 verification evidence;
- public tier;
- lifecycle/discovery state;
- enrollment or moderation state; and
- pending-candidate state or failure details.

A CSV `relay` cell is only an input hint. It must pass the same canonical actor
validation used by normal discovery before profile data can attach to a
verified relay identity. Retained unavailable candidates remain private and do
not become public merely because a CSV row contains descriptive metadata.

## Initial descriptive fields

The 1.3 profile model is expected to support the following descriptive fields:

| Field | Shape | Intended meaning |
| --- | --- | --- |
| `participation_mode` | single text value | Relay-declared/operator-supplied description of how participation is organized. |
| `availability` | single text value | Descriptive participation availability, **not** observed network availability. |
| `relay_type` | single text value | Human-facing relay category/type. |
| `languages` | multiple values | Languages the relay says it is intended to serve. |
| `countries` | multiple values | Country scope claimed by the profile source. |
| `regions` | multiple values | Regional/geographic scope claimed by the profile source. |
| `topics` | multiple values | Descriptive topical focus. |
| `contact_fediverse` | single text value | Fediverse contact identifier. |
| `contact_email` | single text value | Public relay contact email when intentionally supplied. |
| `contact_url` | single HTTPS URL | Public contact/information page. |
| `participation_url` | single HTTPS URL | Page describing how to participate or join. |
| `notes` | bounded plain text | Short public descriptive note; no HTML/Markdown execution semantics. |

Imported `source_url` is separate private provenance for a CSV assertion. It is
not part of the public profile and must not be serialized by public APIs or the
human Directory.

Schema-10 persistence freezes these initial bounds and normalization rules:

- `participation_mode`, `availability`, and `relay_type`: at most 256 UTF-8
  bytes after surrounding whitespace is trimmed;
- `languages`, `countries`, `regions`, and `topics`: at most 16 non-empty
  values, each at most 128 UTF-8 bytes, trimmed, deduplicated, and sorted
  deterministically;
- `contact_fediverse`: at most 256 UTF-8 bytes;
- `contact_email`: at most 320 UTF-8 bytes and a bare parseable mailbox address,
  not a display-name form;
- `contact_url` and `participation_url`: at most 2048 bytes and canonical HTTPS
  URLs using the existing strict canonical relay-URL syntax (canonical FQDN/IP,
  no credentials/query/fragment, canonical path/port);
- `notes`: at most 1024 UTF-8 bytes; and
- private imported `source_url`: at most 2048 bytes under the same canonical
  HTTPS URL rules.

All text rejects control characters. Presence of a profile URL never authorizes
network retrieval; it is descriptive data only. Current scalar/list values are
stored as bounded canonical JSON with a 4096-byte storage ceiling.

`participation_mode` is descriptive profile metadata. For authenticated
Protocol v2 relay self-report its non-empty values are restricted to `open`,
`restricted`, or `closed`. The CSV-facing registration badge/filter semantics
are currently case-sensitive as well: operators should use those exact
lowercase values (or empty); `Open` is not equivalent to `open`. It must not be
inferred from Activity-Relay's broad address-distribution/fan-out settings and
must not claim hashtag filtering or another routing policy unless that behavior
is actually implemented and explicitly declared by the relay/operator.

## Source precedence

Profile precedence is evaluated **per field**, not per whole profile:

1. local Directory operator override;
2. authenticated lifecycle Protocol v2 relay self-report;
3. imported CSV assertion;
4. absent.

A higher-priority source that has no value for a field allows the next source
to supply it. This prevents a one-field local correction from hiding unrelated
relay-authenticated fields.

The implementation must preserve a distinction between:

- no assertion from a source;
- a source replacing its previous value; and
- a local operator intentionally suppressing a lower-priority value when that
  capability is added.

Source precedence affects only descriptive presentation. Moderation always
wins over profile presentation for relay visibility, and profile data can never
override Directory-observed operational evidence.

## Private provenance and history

The Directory must retain enough private history to explain where a profile
value came from and when a source changed it. At minimum, persistence must be
able to distinguish:

- local operator override;
- authenticated relay self-report;
- CSV import;
- the bounded local operator/source label for imported data;
- optional imported `source_url`;
- server acceptance time; and
- source replacement/clear operations.

Public responses expose only the effective reviewed profile values. They never
expose source labels, source priority, import paths, source URLs, operator IDs,
reason codes, profile event history, or protocol reconciliation state.

Earlier migrations remain immutable. Schema 10 is
`0010_relay_profiles.sql`; it adds source-scoped current profile state plus
append-only private history without rewriting migrations 0001 through 0009.
`relay_profile_values` is keyed by actor/source/field and exists only while at
least one retained lifecycle or verified-discovery identity remains.
`relay_profile_events` records `set|clear`, private provenance, server acceptance
time, and a monotonic per-actor/source/field revision. Current assertion cleanup
does not erase this history; a later reappearance continues the retained event
revision sequence.

Source replacement is atomic for one source: all twelve fields are normalized
and compared in one write-admitted transaction. An omitted/empty value clears
that source's current assertion, revealing the next lower-priority source when
one exists. Unchanged fields create no event. Server acceptance time may not
regress for a source. Schema 10 does not yet add the future explicit local
operator suppression/tombstone capability described above; clearing an override
therefore reveals the next source rather than suppressing it.

## CSV import contract

The existing line-oriented import remains the default and is not reinterpreted
as CSV. Because `discovery import --format` already selects human/JSON command
output, input syntax receives a separate option:

```text
activity-relay-directory admin discovery import \
  --file relaylist.csv \
  --input-format csv \
  --operator OPERATOR \
  --reason REASON \
  --source-label SOURCE \
  [--add-dead-relays] \
  [--yes] \
  [--format human|json]
```

`--input-format lines` remains the default compatibility mode.

CSV parsing uses Go's CSV parser rather than splitting on commas. Header order
may vary, but unknown/duplicate headers and duplicate `relay` rows fail closed.
`relay` is required. Candidate hints that canonicalize to the same actor are
treated as duplicate rows before remote probing. The implemented header set is:

```text
relay,participation_mode,availability,relay_type,languages,countries,regions,topics,contact_fediverse,contact_email,contact_url,participation_url,notes,source_url
```

Multi-value fields use semicolon-separated values inside the CSV cell. A literal
semicolon inside one list item is escaped as `\;`, and a literal backslash is
escaped as `\\`; other backslash escapes are rejected. Values are trimmed,
bounded, deduplicated deterministically, and exported in a stable order. Empty
cells remove that CSV source's previous assertion for that field; they do not
delete the relay or suppress a higher-priority source.

A later file omitting a relay never removes the relay automatically, matching
the existing discovery-file rule. CSV import is prospective: the complete file
is parsed and bounded before mutation, actor validation is performed through
the existing safe network path, and the operator receives the normal
confirmation boundary before durable changes. Existing line imports retain the
`activity-relay-directory.discovery-admin.v1` JSON result shape. CSV imports use
`activity-relay-directory.discovery-admin.v2` so their optional profile-mutation
summary does not silently extend the existing v1 command schema.

For an already-known verified relay, a CSV row may update or clear that CSV
source's current profile assertions without creating another discovery row. A
current actor-probe failure does not block that profile update when the exact
canonical actor is already retained as an active lifecycle or discovery
identity; the failed probe does not create positive reachability evidence and
the profile write cannot refresh heartbeat/reachability or change tier. A newly
verified discovery receives its CSV profile only after actor validation and identity
persistence succeed. With `--add-dead-relays`, an unreachable or incompatible
unknown candidate may still be retained privately, but schema 10 does not
attach current profile assertions to an unresolved candidate identity; the CSV
profile must be reapplied after that candidate becomes verified.

CSV is intended to be safe to open in common spreadsheet programs. Export
neutralizes cells whose normalized value begins with `=`, `+`, `-`, `@`, or a
literal apostrophe by prefixing one apostrophe before normal CSV quoting. Import
reverses exactly that escape, including doubled leading apostrophes, so
spreadsheet safety is round-trip stable. Control characters are rejected by the
profile grammar; ordinary CSV quoting alone is not treated as sufficient
formula-injection protection.

## CSV export contract

The existing local export command grows one format without changing current
host/actor output:

```text
activity-relay-directory admin export --scope all --format csv
```

The CSV form is a local operator export of the effective verified relay catalog
for the selected public tier scope. It contains canonical identity plus the
effective descriptive profile fields, but not private source/provenance,
moderation, pending-candidate, or raw observation details.

Export preserves the existing tier-then-actor ordering used by hosts/actors
exports. The emitted header omits `source_url` because provenance is private; an
exported file can therefore be re-imported directly and will create a new CSV
assertion under the operator-supplied `--source-label`.

The first implementation does not change public `/downloads/*.txt` routes and
does not turn private pending candidates into public/exported verified relays.
If a future private inventory export needs unresolved candidates, it receives a
separate explicit scope/command rather than silently changing `--scope all`.

## Lifecycle Protocol v2 profile synchronization

Lifecycle Protocol v2 is the authenticated self-report contract and is distinct
from the independently versioned `/v2/relays` read API. Protocol v1 remains
supported and unchanged.

Protocol v2 registration carries the relay's complete current descriptive
profile together with its canonical identity. All twelve profile fields are
required in the wire object; empty strings or arrays explicitly clear the
relay-owned assertion for that field. Heartbeat remains a liveness-only
operation and does not resend or mutate profile fields. Unregister remains a
lifecycle transition and does not erase retained profile history by itself.

Activity-Relay sends a v2 registration when:

- first registering;
- reconciling an explicit `relay_not_registered` result;
- startup/reconciliation discovers that its normalized profile changed; or
- an operator explicitly requests synchronization.

The client computes a deterministic digest of the normalized complete profile so
an unchanged profile does not cause config-driven re-registration. Every
network attempt still receives a fresh nonce and fresh RFC 9421 signature.

Protocol v2 uses `/v2/relays/register`, `/v2/relays/heartbeat`, and
`/v2/relays/unregister`, with `protocol_version: 2` and the dedicated RFC 9421
tag `activity-relay-directory-v2`. The required components, digest algorithm,
actor/key binding, replay reservation, time bounds, and outcome/error vocabulary
otherwise remain aligned with Protocol v1.

`GET /v1/status` schema 4 advertises ordered `lifecycle_protocol_versions`. A
relay selects the highest lifecycle version it implements only when that version
is explicitly advertised; schema-2/3 status documents are treated as v1-only.
A status transport/validation failure is not a downgrade signal. Identical
shared signed registration fixtures under `testdata/directory/v2/` and
`testdata/directory/v3/` are retained in both repositories.

An authenticated relay profile supersedes lower-priority imported CSV values
field by field, but it never supersedes local moderation or Directory-observed
operational evidence.

## Public projection and human page

`/v2/relays` schema 5 retains the reviewed `profile` object and adds separate bounded relay telemetry. The object contains
only the twelve effective public descriptive fields. Missing scalar fields are
serialized as empty strings and missing multi-value fields as empty arrays so
the profile shape remains deterministic. It does not reveal which source won a
field or whether another lower-priority assertion exists.

The public-facing Directory page renders from that same reviewed projection,
not from a second profile query or independent eligibility rule. Empty profiles
do not add a profile section to the human page. Non-empty profiles are grouped
for human readability without changing wire names: `participation_mode` is
displayed as **Registration status**, `participation_url` as **About this relay**,
operator/contact/notes information appears first, and relay type/topics appear
under **Relay focus**. Languages, countries, and regions are shown below the
focus introduction only when at least one of those lists is nonempty; empty
lists mean that no focus has been asserted and do not render synthetic `any`,
`all`, or `global` values. Profile text is escaped plain text and the two profile
URL fields are already-normalized HTTPS links. No profile value authorizes a
fetch. Operational tier ordering remains based solely on Directory heartbeat
and reachability evidence.

The SQLite public projection resolves source precedence in one bounded batch
for the same retained actor set already being projected. Public serializers
receive only the normalized effective profile and never receive source labels,
source URLs, source kinds, precedence ranks, or profile history.

## 1.3 implementation order

The implementation order is intentionally staged:

1. freeze this descriptive-profile/precedence/CSV/protocol contract;
2. add schema-10 source-scoped profile persistence and private history;
3. add CSV import and local CSV export while preserving line import and
   hosts/actors export compatibility;
4. add the effective profile to `/v2/relays` schema 4 and the public-facing
   Directory page;
5. implement lifecycle Protocol v2 and matching Activity-Relay negotiation,
   profile reconciliation, and shared fixtures; and
6. run a 1.3 acceptance matrix covering migration identity, precedence,
   spreadsheet-safe round-trip, public privacy, Protocol v1 compatibility, and
   Protocol v2 cross-repository behavior.

No later stage may use profile data to shortcut the identity, moderation,
reachability, tiering, or public-eligibility rules established by earlier
releases.


### Participating-site telemetry

Site telemetry is not a profile field and does not participate in
`override > relay > csv` precedence. Protocol v3 register/heartbeat may report
a bounded `participating_instance_count`; ARD stores it separately with server
receipt time. Schema 11's older `receiving_instance_count` remains available for
rolling compatibility with rc3 Protocol-v2 clients. The human Directory's
**Sites** display prefers the v3 participating count and falls back to the older
receiving count when no participating report exists. Missing telemetry leaves
the previous report unchanged. It is informational only and is excluded from
all operational tier, reachability, moderation, enrollment, pruning, and
eligibility decisions.
