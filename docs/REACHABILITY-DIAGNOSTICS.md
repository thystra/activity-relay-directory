# ARD 1.3.1: Reachability diagnostics

The directory has two independent sources of evidence: **authenticated
participation/liveness** and **non-authenticated background observation**.
Diagnostics in schema 13 are **informational**. Neither source-scoped profiles
nor probe diagnostics affect enrollment, moderation, retention, tier assignment,
public eligibility, or heartbeat timestamps.

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
- An actor is normally eligible for rechecking six hours after the last check.
  Relays offline for at least seven days fall back to seven-day probing. The
  retained `next_eligible_at` is the earliest retry time at observation time;
  the bounded hourly worker may run later.
- Cancellation and internal configuration errors must not fabricate remote
  failure evidence or overwrite previous successful observations.
- The public `/v2/relays` schema 6 adds optional `{stage,code,http_status,assessment}`
  `diagnostic` members under `reachability` and `inbox`. `assessment` is
  a bounded interpretation (`possibly_removed`, `degraded`, `inbox_missing`,
  `method_rejected`, `restricted`, `responsive`, `invalid_actor`, or
  `unavailable`), never an instruction to delete. The API also provides
  `reachability.next_eligible_at`. They contain no raw exception messages,
  DNS addresses, resolved IPs, local file names, or response bodies.
- `/v1/relays`, the four operational tiers, lifecycle Protocols 1/2/3,
  and private-only provenance remain unchanged.

Do not perform root (`/`) probes, synthetic ActivityPub POST requests, or
automatic permanent removal based on diagnostic codes. Activity-Relay ban/block
lists are explicitly deferred outside the ARD 1.3.1 scope.
