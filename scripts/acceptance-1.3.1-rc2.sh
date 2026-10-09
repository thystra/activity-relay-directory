#!/usr/bin/env bash
# ARD RC2 source gate: retain all RC1 behavior and assert public wording changes.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
pass() { printf '%s=PASS\n' "$1"; }

echo '===== ARD 1.3.1-rc2 SOURCE ACCEPTANCE ====='
git rev-parse -q --verify refs/tags/v1.3.1-rc1^{commit} >/dev/null
[[ "$(dpkg-parsechangelog -S Version)" == '1.3.1~rc2-1' ]]
pass CASE_00_RC2_VERSION

# Both newly published migrations and the compatibility files remain unchanged.
for name in 0013_reachability_diagnostics.sql 0014_reachability_retries.sql; do
  test "$(git show "v1.3.1-rc1:internal/storage/sqlite/migrations/$name" | sha256sum | cut -d' ' -f1)" = \
       "$(sha256sum "internal/storage/sqlite/migrations/$name" | cut -d' ' -f1)"
done
[[ -z "$(git diff --name-only v1.3.1-rc1 -- internal/storage internal/actorresolver internal/protocol)" ]]
pass CASE_01_RC1_MIGRATIONS_AND_CORE_BEHAVIOR_UNCHANGED

./scripts/acceptance-1.3.1-rc1.sh
pass CASE_02_RC1_SOURCE_ACCEPTANCE_RETAINED

go test -count=1 ./internal/httpapi -run \
  '^(TestHumanDiagnosticMessagesUsePlainActivityPubLanguage|TestHumanDirectoryPlainLanguageHelpers|TestHumanDirectoryFixtureEscapingCachingAndAccessibility|TestHumanDirectoryEvidenceStateDoesNotDependOnColor|TestHumanDirectoryHeartbeatAliveWithFailedActorProbe|TestDirectoryProjectionFixtureAndCacheValidator)$'
pass CASE_03_RC2_PUBLIC_WORDING_AND_API_FIXTURES

# Keep the current docs and shipping template in agreement.
grep -Fq 'ActivityPub Inbox' internal/httpapi/templates/directory.html
grep -Fq 'ActivityPub Actor' internal/httpapi/templates/directory.html
grep -Fq 'No heartbeat received' internal/httpapi/human_directory.go
grep -Fq 'TLS error' internal/httpapi/directory_projection.go
grep -Fq 'ActivityPub Inbox' docs/PUBLIC-LISTING.md
grep -Fq 'ActivityPub Actor' docs/REACHABILITY-DIAGNOSTICS.md
grep -Fq 'ARD White Pages Protocol Relays' README.md
grep -Fq 'Other Known Relays' internal/httpapi/templates/directory.html
grep -Fq 'Offline or Unreachable Relays' internal/httpapi/templates/directory.html
grep -Fq '<dt>Graveyard</dt>' internal/httpapi/templates/directory.html
! grep -Eq 'Tier [1-4]|How the relay tiers work|Participating relays' internal/httpapi/templates/directory.html
grep -Fxq 'docs/releases/v1.3.1-rc2.md' debian/docs
! grep -Eq 'non-mutating|mutating|bounded|OPTIONS not permitted|delivery capability unknown' internal/httpapi/templates/directory.html
pass CASE_04_PUBLIC_DOCS_AND_TEMPLATE_CONSISTENT

test -z "$(gofmt -l .)"
git diff --check
pass CASE_05_FORMAT_AND_WHITESPACE

echo '===== RESULT ====='
echo 'ARD_1_3_1_RC2_SOURCE_ACCEPTANCE=PASS'
