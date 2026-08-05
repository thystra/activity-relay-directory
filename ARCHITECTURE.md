# Architecture

Activity-Relay Directory is a separate service and release artifact from
Activity-Relay itself.

The initial process contains:

- environment-backed configuration with strict validation;
- an HTTP server with health, readiness, and public status endpoints;
- immutable build-version metadata;
- single-node SQLite startup migration and readiness checks;
- fail-closed signed lifecycle handlers that remain disabled by default.

The version 1 contract layer contains closed operation, outcome, error,
health, and administrative-state vocabulary plus strictly decoded JSON
fixtures. It also provides network-free canonical HTTPS relay identity and
same-origin binding plus RFC 9530 SHA-256 Content-Digest generation and
verification. Its RFC 9421 contract verifies bounded directory POST profiles
against caller-resolved RSA key material and binds the verified key identity to
the canonical relay actor. An atomic replay interface stores only opaque
SHA-256-derived key-ID/nonce keys, with a package-private bounded memory
implementation for contract tests. The combined safe path reserves only after
all stateless and actor-binding gates succeed. The contract performs no DNS or
HTTP fetch. Its register-specific composition strictly decodes a bounded body,
binds the exact operation target and canonical identity, then invokes signature
verification and atomic replay reservation. Heartbeat uses the same shared JSON
and target primitives while accepting only canonical actor identity and its own
operation path; it produces no liveness update. Unregister applies the same
identity-only boundary to a distinct removal target and produces no deletion.
The contract packages remain independently testable. The runtime composes them
with the safe actor resolver, durable replay adapter, admission policy, and
state repository only when lifecycle service is explicitly enabled.

The first persistence foundation is an embedded SQLite migration set for one
active directory process on one host. It defines strict relay lifecycle and
administrative state, opaque replay reservations, and separate append-only
lifecycle and private moderation events. Migration history is transactional
and content-hashed so drift,
missing history, and databases newer than the binary fail closed. Process
startup requires an absolute database path, applies migrations before opening
the HTTP listener, and keeps readiness dependent on the current reachable
schema. The container supplies an owner-only data directory through a named
local volume while retaining a read-only root filesystem. A backend-neutral
repository contract and SQLite implementation provide transactional register,
heartbeat, and unregister outcomes with an
append-only event in the same commit. They reject noncanonical input, regressing
server time, absent heartbeat targets, and suspended register or heartbeat
intents. A separate dormant moderation contract applies idempotent suspend and
restore decisions only to existing retained relays, preserving lifecycle and
registration metadata while committing private bounded audit tokens with any
state change. It does not provide preemptive blocking, an operator CLI, or an
HTTP endpoint. SQLite files must remain local; multi-host service topology
requires a later database backend rather than shared SQLite storage. See
`docs/PERSISTENCE.md` and `docs/MODERATION.md`.

The SQLite replay store implements the RFC 9421 opaque-key interface.
It uses the schema's 32-byte primary key for atomic duplicate suppression across
connections and process restart. Each reservation removes an expired copy of
its exact key, prunes only a fixed batch of other expired rows, and enforces the
protocol's ten-minute maximum retention. Enabled runtime wiring passes it to
the verifier and also performs a bounded 4096-row cleanup every five minutes.

The ActivityPub actor resolver implements the verifier's key-resolver
interface. It derives one fragment-free actor
URL from a canonical fragment-bearing key ID, performs proxy-free bounded HTTPS
retrieval through DNS and redirect SSRF checks, and pins each connection to an
approved address. The actor document must identify the requested `Application`
or `Service`, publish the exact requested key, and bind its canonical owner to
the actor. Both SubjectPublicKeyInfo and legacy PKCS#1 RSA public keys are
accepted within reviewed size and strength limits. See `docs/RESOLUTION.md`.

A fixed-capacity, non-sliding, success-only cache wraps that exact resolver in
the enabled lifecycle graph. It revalidates the fully bound result, never caches
failures or serves expired data, and returns isolated RSA-key copies. Cache
eviction can only cause another safe retrieval. Source admission and the global
concurrency ceiling run before a cache miss can initiate retrieval.

The lifecycle HTTP composition is one fail-closed graph: exact routes and body
bounds, source admission, strict decoding, signature/digest/actor verification,
durable replay reservation, actor admission, and transactional persistence.
The lifecycle flag gates register, heartbeat, and unregister together. A
disabled or incomplete graph reports lifecycle unavailable and does not
construct the resolver. The separate durable enrollment policy controls only
first-time retained actors and defaults closed. See `docs/HANDLERS.md`.

An optional Nginx, Apache, or Caddy reverse proxy may terminate public HTTPS
and forward to the loopback Go listener. Proxy configuration is an operator-
owned deployment layer and must preserve the public authority and request
target required by HTTP message-signature verification.

Remaining components will be added behind explicit contracts:

1. authenticated operator access to moderation state;
2. health-state calculation and pruning;
3. public JSON and human-readable directory views;
4. bounded retention policy;
5. Activity-Relay client integration and soak testing.

`TODO.md` defines the dependency order, cross-repository ownership, review
tranches, and acceptance gates for these components.
