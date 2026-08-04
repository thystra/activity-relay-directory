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

## Canonical relay identity

The version 1 contract normalizes HTTPS actor and public-base URLs without
performing network access. It rejects ambiguous authorities and paths and
requires both URLs to share one origin. Validation errors do not echo the
supplied URL.

Canonical syntax is not an SSRF decision. Before any actor fetch exists, the
runtime must separately resolve all addresses, reject prohibited local,
private, link-local, documentation, multicast, and otherwise non-public
targets, pin each connection to an approved address, and repeat the checks on
every redirect. It must then bind the resolved actor and signing key to the
canonical registered identity.
