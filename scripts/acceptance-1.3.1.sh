#!/usr/bin/env bash
# ARD 1.3.1 stable-promotion source gate. Stable 1.3.1 promotes the accepted
# signed v1.3.1-rc2 source: migrations 13 (diagnostics) and 14 (retry streak)
# are the only additions over v1.3.0 and runtime source is unchanged from RC2.
# Historical 1.3.0 / 1.3.1-rc1 / 1.3.1-rc2 acceptance scripts remain unchanged.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_TAG="${ARD_1_3_1_BASE_TAG:-v1.3.0}"
RC_TAG="${ARD_1_3_1_RC_TAG:-v1.3.1-rc2}"
# Schema 13 was deployed on Hermod before RC1. It must remain byte-for-byte
# identical to the reviewed original 1.3.1 merge (PR #44).
SCHEMA13_BASE="${ARD_1_3_1_SCHEMA13_BASE:-eeecd6cb742157fb2fcf6655a8047454df9e2925}"
EXPECTED_VERSION='1.3.1'
EXPECTED_DEB_VERSION='1.3.1-2'
pass() { printf '%s=PASS\n' "$1"; }

echo '===== ARD 1.3.1 SOURCE ACCEPTANCE ====='
printf 'head=%s\n' "$(git rev-parse HEAD)"
printf 'tree=%s\n' "$(git rev-parse HEAD^{tree})"
printf 'base_tag=%s\n' "$BASE_TAG"
printf 'rc_tag=%s\n' "$RC_TAG"

echo
echo '===== CASE 0: STABLE RELEASE IDENTITY ====='
[[ "$(dpkg-parsechangelog -S Version)" == "$EXPECTED_DEB_VERSION" ]]
[[ "$(dpkg-parsechangelog -S Distribution)" == 'stable' ]]
NOTES="docs/releases/v${EXPECTED_VERSION}.md"
grep -Fxq "# Activity-Relay Directory ${EXPECTED_VERSION}" "$NOTES"
grep -Fq -- "- Application version: \`${EXPECTED_VERSION}\`" "$NOTES"
grep -Fq -- "- Debian package version: \`${EXPECTED_DEB_VERSION}\`" "$NOTES"
grep -Fxq "$NOTES" debian/docs
# The abandoned 1.3.1-1 candidate entry remains as history, never on top.
grep -Fxq 'activity-relay-directory (1.3.1-1) stable; urgency=medium' debian/changelog
dpkg --compare-versions "$EXPECTED_DEB_VERSION" gt '1.3.1-1'
dpkg --compare-versions "$EXPECTED_DEB_VERSION" gt '1.3.1~rc2-1'
pass CASE_00_STABLE_VERSION_AND_RELEASE_NOTES

echo
echo '===== CASE 1: MIGRATIONS ====='
git rev-parse -q --verify "refs/tags/$BASE_TAG^{commit}" >/dev/null
git rev-parse -q --verify "refs/tags/$RC_TAG^{commit}" >/dev/null
mapfile -t RELEASED_MIGRATIONS < <(
    git ls-tree -r --name-only "$BASE_TAG" -- internal/storage/sqlite/migrations |
    LC_ALL=C sort
)
mapfile -t CURRENT_MIGRATIONS < <(
    find internal/storage/sqlite/migrations -maxdepth 1 -type f -printf '%p\n' |
    LC_ALL=C sort
)

[[ "${#RELEASED_MIGRATIONS[@]}" -eq 12 ]]
[[ "${#CURRENT_MIGRATIONS[@]}" -eq 14 ]]
for migration_path in "${RELEASED_MIGRATIONS[@]}"; do
    cmp -s <(git show "$BASE_TAG:$migration_path") "$migration_path" || {
        echo "released migration drift: $migration_path" >&2
        exit 1
    }
done
EXTRA_MIGRATIONS="$(
    comm -13 \
        <(printf '%s\n' "${RELEASED_MIGRATIONS[@]}") \
        <(printf '%s\n' "${CURRENT_MIGRATIONS[@]}")
)"
[[ "$EXTRA_MIGRATIONS" == \
    $'internal/storage/sqlite/migrations/0013_reachability_diagnostics.sql\ninternal/storage/sqlite/migrations/0014_reachability_retries.sql' ]]
pass CASE_01_SCHEMA13_14_ONLY_ADDITIONAL_MIGRATIONS
pass CASE_01_MIGRATIONS_1_TO_12_BYTE_IDENTICAL

git rev-parse -q --verify "$SCHEMA13_BASE^{commit}" >/dev/null
cmp -s <(git show "$SCHEMA13_BASE:internal/storage/sqlite/migrations/0013_reachability_diagnostics.sql") \
    internal/storage/sqlite/migrations/0013_reachability_diagnostics.sql || {
    echo 'deployed schema 13 migration drift' >&2
    exit 1
}
pass CASE_01_SCHEMA13_BYTE_IDENTICAL_TO_DEPLOYED

for name in 0013_reachability_diagnostics.sql 0014_reachability_retries.sql; do
    cmp -s <(git show "$RC_TAG:internal/storage/sqlite/migrations/$name") \
        "internal/storage/sqlite/migrations/$name" || {
        echo "published RC migration drift: $name" >&2
        exit 1
    }
done
pass CASE_01_SCHEMA13_14_BYTE_IDENTICAL_TO_RC

echo
echo '===== CASE 2: NO RUNTIME CHANGE FROM ACCEPTED RC ====='
RUNTIME_DRIFT="$(git diff --name-only "$RC_TAG" -- cmd internal go.mod go.sum Dockerfile compose.yml config.yml.example contrib)"
if [[ -n "$RUNTIME_DRIFT" ]]; then
    printf 'runtime drift from %s:\n%s\n' "$RC_TAG" "$RUNTIME_DRIFT" >&2
    exit 1
fi
pass CASE_02_RUNTIME_SOURCE_IDENTICAL_TO_RC

echo
echo '===== CASE 3: RESOLVER DIAGNOSTICS / NETWORK POLICY ====='
go test -count=1 ./internal/actorresolver -run \
    '^(TestClassifyNetworkFailureWithoutErrorDisclosure|TestDetailedActorFailureKeepsDiagnosisButRedactsTransportError|TestDetailedActorHTTPStatusAndContentEvidence|TestDetailedInboxOPTIONSRemainsNonMutating|TestSafeDialerConnectionRefusedDoesNotBecomePolicyRejection|TestSafeDialerRejectsMixedOrInvalidAnswersBeforeDial|TestResolverEnforcesResponseBoundary|TestResolverExplicitTimeoutAndTLSFailuresStayFailClosed)$'
pass CASE_03_RESOLVER_NETWORK_AND_REDACTION

echo
echo '===== CASE 4: OBSERVATION PERSISTENCE / RETRIES ====='
go test -count=1 ./internal/storage/sqlite -run \
    '^(TestMigrateCreatesSchemaAndIsIdempotent|TestMigrateRejectsDriftAndFutureSchema|TestMigrateRollsBackFailedMigration|TestReachabilityDiagnosticEvidenceSurvivesActorFailureWithoutHeartbeatMutation|TestReachabilityDiagnosticLongOfflineRetryAndInputValidation|TestActorFailureRetryEscalationAndRecoveryResetsStreak|TestSchema14PromotesRecentExistingFailureToHourlyRetry|TestReachabilityCandidatesSlowLongOfflineRelaysToWeeklyAndRetainPrunedRecovery)$'
go test -count=1 ./internal/reachability -run \
    '^(TestRunIsBoundedFairPagedAndConcurrent|TestRunClassifiesActorFailureInboxDiagnosticsAndWriteSkip|TestRunHonorsCancellation)$'
pass CASE_04_PERSISTENCE_RETRY_AND_NONINTERFERENCE

echo
echo '===== CASE 5: HEARTBEAT LIVENESS / PUBLIC PROJECTION / COMPATIBILITY ====='
go test -count=1 ./internal/httpapi -run \
    '^(TestReachabilityDiagnosticProjectionIsBoundedAndNonDestructive|TestDirectoryProjectionFixtureAndCacheValidator|TestV2CursorIsRejectedByV1Listing|TestHumanDirectoryHeartbeatAliveWithFailedActorProbe)$'
go test -count=1 ./internal/storage -run '^(TestCurrentHeartbeatKeepsFailedActorOutOfOfflineTier)$'
go test -count=1 ./internal/storage/sqlite -run '^(TestDirectorySummaryCountsPublicAndPendingRelays)$'
go test -count=1 ./internal/protocol/v1 ./internal/protocol/v2 ./internal/protocol/v3
pass CASE_05_LIVENESS_PUBLIC_AND_LIFECYCLE_COMPATIBILITY

echo
echo '===== CASE 6: RC2 PUBLIC WORDING / DOCS ====='
go test -count=1 ./internal/httpapi -run \
    '^(TestHumanDiagnosticMessagesUsePlainActivityPubLanguage|TestHumanDirectoryPlainLanguageHelpers|TestHumanDirectoryFixtureEscapingCachingAndAccessibility|TestHumanDirectoryEvidenceStateDoesNotDependOnColor)$'
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
! grep -Eq 'non-mutating|mutating|bounded|OPTIONS not permitted|delivery capability unknown' internal/httpapi/templates/directory.html
pass CASE_06_PUBLIC_WORDING_DOCS_AND_TEMPLATE_CONSISTENT

echo
echo '===== CASE 7: FULL SOURCE VALIDATION ====='
test -z "$(gofmt -l .)"
git diff --check
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
pass CASE_07_FULL_SOURCE_VALIDATION

echo
echo '===== RESULT ====='
echo 'ARD_1_3_1_SOURCE_ACCEPTANCE=PASS'
