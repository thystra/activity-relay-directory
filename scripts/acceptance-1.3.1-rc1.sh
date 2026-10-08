#!/usr/bin/env bash
# ARD 1.3.1 source gate. Historical 1.3.0 acceptance remains unchanged.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_TAG="${ARD_1_3_1_RC1_BASE_TAG:-v1.3.0}"
pass() { printf '%s=PASS\n' "$1"; }

echo '===== ARD 1.3.1-rc1 SOURCE ACCEPTANCE ====='
printf 'head=%s\n' "$(git rev-parse HEAD)"
printf 'tree=%s\n' "$(git rev-parse HEAD^{tree})"
printf 'base_tag=%s\n' "$BASE_TAG"

git rev-parse -q --verify "refs/tags/$BASE_TAG^{commit}" >/dev/null
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
# Schema 13 was deployed on Hermod before RC1. It must remain byte-for-byte
# identical to the reviewed original 1.3.1 merge; schema 14 is strictly new.
SCHEMA13_BASE="${ARD_1_3_1_SCHEMA13_BASE:-eeecd6cb742157fb2fcf6655a8047454df9e2925}"
git rev-parse -q --verify "$SCHEMA13_BASE^{commit}" >/dev/null
cmp -s <(git show "$SCHEMA13_BASE:internal/storage/sqlite/migrations/0013_reachability_diagnostics.sql") \
    internal/storage/sqlite/migrations/0013_reachability_diagnostics.sql || {
    echo 'deployed schema 13 migration drift' >&2
    exit 1
}
pass CASE_01_SCHEMA13_BYTE_IDENTICAL_TO_DEPLOYED

echo
echo '===== CASE 2: RESOLVER DIAGNOSTICS / NETWORK POLICY ====='
go test -count=1 ./internal/actorresolver -run \
    '^(TestClassifyNetworkFailureWithoutErrorDisclosure|TestDetailedActorFailureKeepsDiagnosisButRedactsTransportError|TestDetailedActorHTTPStatusAndContentEvidence|TestDetailedInboxOPTIONSRemainsNonMutating|TestSafeDialerConnectionRefusedDoesNotBecomePolicyRejection|TestSafeDialerRejectsMixedOrInvalidAnswersBeforeDial|TestResolverEnforcesResponseBoundary|TestResolverExplicitTimeoutAndTLSFailuresStayFailClosed)$'
pass CASE_02_RESOLVER_NETWORK_AND_REDACTION

echo
echo '===== CASE 3: OBSERVATION PERSISTENCE / RETRIES ====='
go test -count=1 ./internal/storage/sqlite -run \
    '^(TestMigrateCreatesSchemaAndIsIdempotent|TestMigrateRejectsDriftAndFutureSchema|TestMigrateRollsBackFailedMigration|TestReachabilityDiagnosticEvidenceSurvivesActorFailureWithoutHeartbeatMutation|TestReachabilityDiagnosticLongOfflineRetryAndInputValidation|TestActorFailureRetryEscalationAndRecoveryResetsStreak|TestSchema14PromotesRecentExistingFailureToHourlyRetry|TestReachabilityCandidatesSlowLongOfflineRelaysToWeeklyAndRetainPrunedRecovery)$'
go test -count=1 ./internal/reachability -run \
    '^(TestRunIsBoundedFairPagedAndConcurrent|TestRunClassifiesActorFailureInboxDiagnosticsAndWriteSkip|TestRunHonorsCancellation)$'
pass CASE_03_PERSISTENCE_RETRY_AND_NONINTERFERENCE

echo
echo '===== CASE 4: PUBLIC PROJECTION / COMPATIBILITY ====='
go test -count=1 ./internal/httpapi -run \
    '^(TestReachabilityDiagnosticProjectionIsBoundedAndNonDestructive|TestDirectoryProjectionFixtureAndCacheValidator|TestV2CursorIsRejectedByV1Listing)$'
go test -count=1 ./internal/storage -run '^(TestCurrentHeartbeatKeepsFailedActorOutOfOfflineTier)$'
go test -count=1 ./internal/httpapi -run '^(TestHumanDirectoryHeartbeatAliveWithFailedActorProbe)$'
go test -count=1 ./internal/storage/sqlite -run '^(TestDirectorySummaryCountsPublicAndPendingRelays)$'
go test -count=1 ./internal/protocol/v1 ./internal/protocol/v2 ./internal/protocol/v3
pass CASE_04_PUBLIC_AND_LIFECYCLE_COMPATIBILITY

echo
echo '===== CASE 5: FULL SOURCE VALIDATION ====='
test -z "$(gofmt -l .)"
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
pass CASE_05_FULL_SOURCE_VALIDATION

echo
echo '===== RESULT ====='
echo 'ARD_1_3_1_RC1_SOURCE_ACCEPTANCE=PASS'
