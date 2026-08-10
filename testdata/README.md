# Test Data

`directory/v1/` contains the normative version 1 JSON contract fixtures.
Fixture tests decode every file strictly and verify the closed operation,
outcome, and error vocabulary.

The Activity-Relay generated registration vector is copied byte-for-byte into
both repositories and is accepted by the Directory's real verifier.

Fixtures use reserved `.example` names. They must exclude production keys,
identities, host inventories, connected-site membership, and user data.

`public/v1/` contains public-view representation fixtures. They freeze the
listing schema separately from the signed lifecycle protocol vocabulary under
`directory/v1/`.
