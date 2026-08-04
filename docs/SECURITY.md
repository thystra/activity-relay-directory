# Security Design

## Primary threats

- forged relay registration;
- replayed heartbeat or unregister requests;
- key substitution;
- SSRF through relay-controlled URLs;
- oversized or deeply nested JSON;
- duplicate and conflicting registrations;
- directory poisoning and abusive metadata;
- automated removal of administratively suspended entries.

## Required controls before registration exists

- authenticated signatures and digest verification;
- exact key-to-relay binding;
- bounded clocks and nonce retention;
- allowlisted schemes and normalized HTTPS URLs;
- network-target restrictions for all server-initiated fetches;
- strict payload depth and size limits;
- rate limits by source and relay identity;
- operator suspension and audit history;
- no disclosure of connected-site membership.
