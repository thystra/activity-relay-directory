# Releasing

No release workflow is active in the initial scaffold.

Before the first release:

1. define versioning and compatibility policy;
2. add deterministic binary and container builds;
3. add SBOM and checksum generation;
4. validate clean installation and upgrade behavior;
5. document database backup and migration behavior;
6. perform an integration soak with relay2;
7. verify that registration remains disabled unless explicitly configured;
8. publish release notes and rollback instructions.
