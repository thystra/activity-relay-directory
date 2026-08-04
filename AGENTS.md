# AGENTS.md

## Project identity

- Repository: `thystra/activity-relay-directory`
- Primary branch: `master`
- Public service hostname reserved for deployment: `directory.argentwolf.org`

## Safety and workflow

- Verify host, repository, branch, and worktree before mutation.
- Use prospective staging and validation before applying changes.
- Keep backups, validation reports, applicators, and other maintainer-only
  artifacts outside the tracked checkout. If `.project-local/` is used inside
  a checkout, it must be excluded locally and never committed.
- Distinguish applied, tested, committed, pushed, deployed, and released.
- Do not deploy or enable registration merely because code exists.

## Architectural invariants

- Registration is disabled by default.
- Fresh installations contain no active external directory endpoints.
- The directory stores relay-instance metadata, never connected-site or user
  identities.
- Registration, heartbeat, and unregister requests will require authenticated
  signatures, content digests, bounded timestamps, nonce replay protection,
  and bounded request bodies.
- Public status terminology will use healthy, stale, dead, and prune windows
  defined by recency.
- Moderation and administrative suspension override automated health state.
- Protocol compatibility must be versioned and tested with fixtures.

## Validation

Run before every commit:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build ./cmd/activity-relay-directory
docker build --pull=false --build-arg VERSION=validation .
```

Protocol vocabulary is closed and versioned. When a JSON field, operation,
outcome, error code, or lifecycle name changes, update `docs/PROTOCOL.md`, the
Go vocabulary, and `testdata/directory/` together. Fixtures must use reserved
example identities and must never contain production or connected-site data.

RFC 9530 digest changes must remain byte-for-byte compatible with the
Activity-Relay client implementation and `testdata/directory/v1/` fixture.
Digest verification operates on the exact bounded body bytes before JSON
decoding. It must use constant-time comparison and never echo an untrusted
field value in an error.

Canonical URL syntax validation is not network-target validation. Keep DNS,
SSRF controls, redirect checks, actor retrieval, and actor-key binding as
separate explicit gates, and never echo an untrusted supplied URL in an error.

When changing a reverse-proxy example, validate it with the corresponding
Nginx, Apache HTTP Server, or Caddy release. Examples must preserve the public
host and request target, remain optional, and must not install, enable, reload,
or otherwise configure an operator's proxy automatically.

## AI-assisted maintenance

AI-assisted tools may help draft or review changes. The human maintainer must
inspect the result, approve the design, execute validation, control releases
and deployments, and remain accountable for the software.
