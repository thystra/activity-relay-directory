# ARD 1.3.1: Reachability diagnostics

The directory has two independent sources of evidence: **authenticated
participation/liveness** and **non-authenticated background observation**.
Diagnostic failure evidence is **informational** and cannot change authenticated
heartbeat timestamps, enrollment, moderation or retention. Public tiers use
authenticated liveness and actor reachability as separate positive signals:
The **ARD White Pages Protocol Relays** group (API tier 1) requires both;
**Other Known Relays** (tier 2) requires either; the offline and Graveyard groups
(tiers 3/4) have neither. A healthy heartbeat with an Actor check failure is
still evidence of White Pages participation and belongs in group 2.

## Observation stages

A background run fetches the canonical HTTPS relay actor with the established
SSRF-resistant pinned-address resolver. Its status distinguishes:

| Stage | Codes / evidence | Interpretation |
| --- | --- | --- |
| DNS | `nxdomain`, `no_address`, `temporary`, `timeout`, `policy`, `error` | No usable name/address or DNS/policy issue; a name-not-found result can suggest a relay was removed but is not proof |
| Connect | `refused`, `timeout`, `failed` | Connection cannot be established; do not infer relay removal |
| TLS | `certificate`, `handshake` | Strict verification or protocol negotiation failure; never bypass TLS |
| Redirect | `rejected` | Redirect failed the pinned/HTTPS safety policy or redirect limit |
| Actor | `http_status` + integer, `content_type`, `invalid_document`, `invalid_response` | HTTP 404/410 may indicate a missing/decommissioned actor; 5xx is normally transient |
| Inbox | `http_status` + integer, `invalid_response` | `OPTIONS` 404/410 suggests missing/gone inbox; 405/501 is method rejection, **not** lack of ActivityPub delivery capability |
| Network / policy | `timeout`, `error`, `prohibited_target` | Bounded generic outcome without raw network details |

Go's standard resolver `IsNotFound` may encompass both NXDOMAIN and DNS
NODATA, depending on the platform. The public display uses cautious language;
`no_address` represents an explicitly empty successful address lookup. Do not
interpret a DNS code as authoritative proof of relay decommissioning.

## Storage, retries and compatibility

- Schema 13 adds `relay_probe_diagnostics` referenced by the retained
  `relay_observations` identity with cascading deletion upon authorized hard
  retention. No historical migration backfills fabricated diagnostics.
- Successful actor validation clears the actor-failure diagnostic, checks
  declared inboxes using **OPTIONS only**, and updates the inbox diagnostic.
- Actor failure retains earlier successful actor timestamps and previously
  observed inbox evidence. An inbox diagnostic may be older than the last actor
  check; the human page labels it accordingly.
- Schema 14 adds a bounded durable actor-failure streak (0–4). A successful
  actor check clears it. Consecutive failures use an escalating 1h, 2h, 4h,
  then 6h eligibility sequence. Relays without any positive online evidence
  for seven days continue at the 7-day retry interval. Existing recent failed
  observations are promoted to the first retry on migration, without fabricating
  a successful probe. The stored `next_check_at_unix` controls actual worker
  candidate selection; `next_eligible_at` reflects it publicly. The hourly
  worker may run later than the earliest eligible timestamp.
- Cancellation and internal configuration errors must not fabricate remote
  failure evidence or overwrite previous successful observations.
- The public `/v2/relays` schema 6 adds optional `{stage,code,http_status,assessment}`
  `diagnostic` members under `reachability` and `inbox`. `assessment` is
  a bounded interpretation (`possibly_removed`, `degraded`, `inbox_missing`,
  `method_rejected`, `restricted`, `responsive`, `invalid_actor`, or
  `unavailable`), never an instruction to delete. The API also provides
  `reachability.next_eligible_at`. They contain no raw exception messages,
  DNS addresses, resolved IPs, local file names, or response bodies.
- `/v1/relays` and lifecycle Protocols 1/2/3 remain unchanged. The four
  numeric API `tier` values remain, while the website uses descriptive group
  headings. API tier 2 includes healthy-heartbeat/failed-Actor relays; they
  are not classified as offline solely because an Actor check failed.

Do not perform root (`/`) probes, synthetic ActivityPub POST requests, or
automatic permanent removal based on diagnostic codes. Activity-Relay ban/block
lists are explicitly deferred outside the ARD 1.3.1 scope.

## Human-readable presentation in 1.3.1-rc2

The website labels these endpoints **ActivityPub Actor** and **ActivityPub
Inbox**. The Actor check validates the identity document; the Inbox check
uses HTTP `OPTIONS`, not ActivityPub delivery. Inbox `405`/`501` means the
server does not support that check; it does **not** prove the Inbox cannot
accept ActivityPub messages. When an Inbox diagnostic exists, the page shows
the recorded check time (if available) and says **Message delivery was not
tested.** Results are not automatically called outdated.

The page uses these plain-language messages for classified outcomes. The
`stage`, `code`, `http_status`, and `assessment` API fields remain unchanged.

| Check category | Evidence | Public explanation |
| --- | --- | --- |
| DNS | `nxdomain` / `no_address` | No DNS address found for this relay |
| DNS | `temporary` / `timeout` / `error` | DNS lookup unavailable, timed out, or failed |
| DNS | `policy` | DNS address did not pass security checks |
| Connection | `refused` / `timeout` / `failed` | Server refused the connection, timed out, or could not be reached |
| HTTPS | `tls/certificate` | Connected, but the HTTPS certificate could not be verified (TLS error) |
| HTTPS | `tls/handshake` | Connected, but a secure HTTPS connection could not be established (TLS error) |
| Redirect / address policy | `redirect/rejected` / `policy/prohibited_target` | Could not safely follow the Actor address or check that address |
| ActivityPub Actor | HTTP 404 / 410 | Actor address not found / no longer available |
| ActivityPub Actor | other HTTP, unsupported format, invalid document / response | Actor check HTTP status or validation failure |
| ActivityPub Inbox | HTTP 405 / 501 | Inbox does not support this check (not a delivery failure) |
| ActivityPub Inbox | other HTTP / invalid response | Inbox check HTTP status or invalid response |
| Network | `timeout` / `error` | Network request timed out / connection failed |

HTML heartbeat labels are **Healthy**, **Stale**, or **No heartbeat received**.
The internal/API codes `healthy`, `stale`, `dead`, `prune`, and
`not_observed` remain distinct and compatible with RC1; the website maps
`stale`, `dead`, and `prune` to the one visible label **Stale**. This has no
effect on tier classification, retry scheduling, or actual soft pruning.
