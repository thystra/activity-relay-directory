# Directory Protocol Status

This document records scope and invariants. It is not yet a wire-protocol
specification.

Planned client operations:

- register a relay instance;
- send a daily signed heartbeat with bounded jitter;
- unregister a relay instance.

Planned security properties:

- RFC 9421 HTTP Message Signatures;
- RFC 9530 Content-Digest for requests with content;
- actor-key binding to the registered relay;
- bounded created/expires timestamps;
- unique nonces and replay rejection;
- strict content type, URL, and body-size validation;
- idempotent registration and heartbeat semantics.

The final JSON fields, endpoint paths, response codes, and state-transition
rules will be introduced with fixtures and compatibility tests.
