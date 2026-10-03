#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_TAG="${ARD_1_2_BASE_TAG:-v1.1.0}"
EXPECTED_BASE_COMMIT="b446891e945e83a119141c29f8de6d86d8772a00"

pass() {
    printf '%s=PASS\n' "$1"
}

echo '===== ARD 1.2 SOURCE ACCEPTANCE ====='
printf 'head=%s\n' "$(git rev-parse HEAD)"
printf 'tree=%s\n' "$(git rev-parse HEAD^{tree})"
printf 'base_tag=%s\n' "$BASE_TAG"

git rev-parse -q --verify "refs/tags/$BASE_TAG^{commit}" >/dev/null
BASE_COMMIT="$(git rev-parse "$BASE_TAG^{commit}")"
printf 'base_commit=%s\n' "$BASE_COMMIT"
[[ "$BASE_COMMIT" == "$EXPECTED_BASE_COMMIT" ]]

mapfile -t RELEASED_MIGRATIONS < <(
    git ls-tree -r --name-only "$BASE_TAG" -- internal/storage/sqlite/migrations |
    LC_ALL=C sort
)
mapfile -t CURRENT_MIGRATIONS < <(
    find internal/storage/sqlite/migrations -maxdepth 1 -type f -printf '%p\n' |
    LC_ALL=C sort
)

[[ "${#RELEASED_MIGRATIONS[@]}" -eq 8 ]]
[[ "${#CURRENT_MIGRATIONS[@]}" -eq 9 ]]
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
    "internal/storage/sqlite/migrations/0009_discovery_candidates.sql" ]]
pass CASE_01_SCHEMA9_IS_ONLY_NEW_MIGRATION
pass CASE_01_MIGRATIONS_1_TO_8_BYTE_IDENTICAL

echo
echo '===== CASE 2: DISCOVERY INPUT AND ACTOR COMPATIBILITY ====='
go test -count=1 ./internal/discoverycommand \
    -run '^(TestCandidateActorURLAcceptsOnlyReviewedForms|TestClassifyKnownImportSeparatesActiveStateFromHistory|TestPrepareAddDeadRelaysRetainsOnlyActorFailures|TestExecuteRetainsDeadRelayWithoutTreatingItAsFailure)$'
go test -count=1 ./internal/actorresolver -run '^TestResolverResolvesBoundActorRSAKeys$'
pass CASE_02_DISCOVERY_COMPATIBILITY

echo
echo '===== CASE 3: RETAINED CANDIDATE RETRY AND PROMOTION ====='
go test -count=1 ./internal/storage/sqlite \
    -run '^(TestDiscoveryCandidateChecksApplyStagedBackoffThenWeekly|TestPromoteDiscoveryCandidateCreatesVerifiedDiscoveryAndResolvesCandidate)$'
go test -count=1 ./internal/candidatemaintenance -run '^TestRunPromotesReachableAndRetainsFailures$'
pass CASE_03_RETRY_AND_PROMOTION

echo
echo '===== CASE 4: PUBLIC TIERS, GRAVEYARD, AND ORDERING ====='
go test -count=1 ./internal/storage/sqlite \
    -run '^(TestDirectoryProjectionOrdersOperationalTiersAndKeepsParticipationPathsIndependent|TestDirectoryProjectionTierOneOrderingIgnoresHeartbeatRecency|TestReachabilityCandidatesSlowLongOfflineRelaysToWeeklyAndRetainPrunedRecovery)$'
go test -count=1 ./internal/httpapi -run '^TestBuildHumanDirectoryTierBlocksPreservesTierAndAlphabeticalOrder$'
pass CASE_04_TIERING_AND_GRAVEYARD

echo
echo '===== CASE 5: EXPORTS AND PUBLIC DOWNLOADS ====='
go test -count=1 ./internal/directoryexport -run '^TestRenderSortsAlphabeticallyInsideTierWithoutRecencyRanking$'
go test -count=1 ./internal/httpapi -run '^TestDirectoryExportDownloadsUseTierScopesAndCacheValidators$'
pass CASE_05_EXPORTS_AND_DOWNLOADS

echo
echo '===== CASE 6: V1 COMPATIBILITY / V2 PROJECTION ====='
go test -count=1 ./internal/httpapi \
    -run '^(TestPublicListingFixtureAndCacheValidator|TestV2CursorIsRejectedByV1Listing|TestDirectoryProjectionFixtureAndCacheValidator)$'
pass CASE_06_PUBLIC_API_COMPATIBILITY

echo
echo '===== RESULT ====='
echo 'ARD_1_2_SOURCE_ACCEPTANCE=PASS'
