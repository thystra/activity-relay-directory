# Relay profiles and 1.3 metadata contract

## Purpose

Activity-Relay Directory 1.3 will add descriptive relay metadata without
mixing operator/imported claims with the Directory's own operational evidence.
The feature has three inputs:

1. local operator imports, including spreadsheet-friendly CSV;
2. authenticated relay self-reporting through a future lifecycle Protocol v2;
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
Protocol v2. When profile fields become public, `/v2/relays` is expected to move
from schema 3 to schema 4 while retaining the existing tier/evidence model.

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

Exact value grammars and byte/item limits are frozen with the implementation
tranche, but all values must remain bounded, UTF-8, free of control characters,
and safe to render as plain text. URL fields must use the same conservative
canonical-HTTPS principles used elsewhere in the project and must not trigger
remote retrieval merely because they are present in profile data.

`participation_mode` is descriptive profile metadata. It must not be inferred
from Activity-Relay's broad address-distribution/fan-out settings and must not
claim hashtag filtering or another routing policy unless that behavior is
actually implemented and explicitly declared by the relay/operator.

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

Earlier migrations remain immutable. The first profile-persistence migration
is expected to become schema 10 and must add source-scoped current profile state
plus append-only private history without rewriting migrations 0001 through
0009.

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

CSV parsing will use a real CSV parser rather than splitting on commas. Header
order may vary, but unknown/duplicate headers and duplicate `relay` rows fail
closed. `relay` is required. The initial reviewed header set is:

```text
relay,participation_mode,availability,relay_type,languages,countries,regions,topics,contact_fediverse,contact_email,contact_url,participation_url,notes,source_url
```

Multi-value fields use semicolon-separated values inside the CSV cell. Values
are trimmed, bounded, deduplicated deterministically, and exported in a stable
order. Empty cells remove that CSV source's previous assertion for that field;
they do not delete the relay or suppress a higher-priority source.

A later file omitting a relay never removes the relay automatically, matching
the existing discovery-file rule. CSV import is prospective: the complete file
is parsed and bounded before mutation, actor validation is performed through
the existing safe network path, and the operator receives the normal
confirmation boundary before durable changes.

CSV is intended to be safe to open in common spreadsheet programs. Because
relay-controlled/public text may begin with spreadsheet formula trigger
characters, export implementation must include a reversible formula-injection
neutralization and regression tests; ordinary CSV quoting alone is not treated
as sufficient protection.

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

The first implementation does not change public `/downloads/*.txt` routes and
does not turn private pending candidates into public/exported verified relays.
If a future private inventory export needs unresolved candidates, it receives a
separate explicit scope/command rather than silently changing `--scope all`.

## Lifecycle Protocol v2 profile synchronization

Lifecycle Protocol v2 is a future cross-repository extension and is distinct
from the existing `/v2/relays` read API. Protocol v1 remains supported and
unchanged.

Protocol v2 registration will carry the relay's complete current descriptive
profile together with its canonical identity. Heartbeat remains a liveness
operation and must not resend or mutate profile fields. Unregister remains a
lifecycle transition and does not erase retained profile history by itself.

Activity-Relay should send a v2 registration when:

- first registering;
- reconciling an explicit `relay_not_registered` result;
- startup/reconciliation discovers that its normalized profile changed; or
- an operator explicitly requests synchronization.

The client should compute a deterministic normalized-profile digest/revision so
an unchanged profile does not cause config-driven re-registration. Every
network attempt still receives a fresh nonce and fresh RFC 9421 signature.

The v2 signature profile, endpoint/version negotiation, strict JSON shape, and
cross-repository fixtures must be frozen together before either repository
activates Protocol v2. A v2-capable relay must be able to fall back to Protocol
v1 when a Directory does not advertise v2 support. Capability negotiation must
be proven not to break existing v1 clients before it is added to a currently
versioned status document.

An authenticated relay profile supersedes lower-priority imported CSV values
field by field, but it never supersedes local moderation or Directory-observed
operational evidence.

## Public projection and human page

When profile persistence/import is stable, the richer public API may add one
reviewed `profile` object in `/v2/relays` schema 4. The object contains only the
effective public descriptive fields. It must not reveal which source won a
field or whether another lower-priority assertion exists.

The public-facing Directory page must render from the same reviewed projection,
not from a second profile query or independent eligibility rule. Profile values
must be escaped plain text/links under the existing CSP and privacy boundary.
Operational tier ordering remains based solely on Directory heartbeat and
reachability evidence.

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
