#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_TAG="${ARD_1_3_BASE_TAG:-v1.2.0}"

pass() {
    printf '%s=PASS\n' "$1"
}

echo '===== ARD 1.3 SOURCE ACCEPTANCE ====='
printf 'head=%s\n' "$(git rev-parse HEAD)"
printf 'tree=%s\n' "$(git rev-parse HEAD^{tree})"
printf 'base_tag=%s\n' "$BASE_TAG"

git rev-parse -q --verify "refs/tags/$BASE_TAG^{commit}" >/dev/null
BASE_COMMIT="$(git rev-parse "$BASE_TAG^{commit}")"
printf 'base_commit=%s\n' "$BASE_COMMIT"

mapfile -t RELEASED_MIGRATIONS < <(
    git ls-tree -r --name-only "$BASE_TAG" -- internal/storage/sqlite/migrations |
    LC_ALL=C sort
)
mapfile -t CURRENT_MIGRATIONS < <(
    find internal/storage/sqlite/migrations -maxdepth 1 -type f -printf '%p\n' |
    LC_ALL=C sort
)

[[ "${#RELEASED_MIGRATIONS[@]}" -eq 9 ]]
[[ "${#CURRENT_MIGRATIONS[@]}" -eq 11 ]]
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
[[ "$EXTRA_MIGRATIONS" == $'internal/storage/sqlite/migrations/0010_relay_profiles.sql\ninternal/storage/sqlite/migrations/0011_relay_telemetry.sql' ]]
pass CASE_01_SCHEMA10_AND_11_ARE_ONLY_NEW_MIGRATIONS
pass CASE_01_MIGRATIONS_1_TO_9_BYTE_IDENTICAL

echo
echo '===== CASE 2: PROFILE STORAGE / PRECEDENCE / CLEAR ====='
go test -count=1 ./internal/storage/sqlite \
    -run '^(TestMigrateVersionNineAddsProfilesWithoutChangingRetainedRelay|TestProfileRoundTripsAllFields|TestProfileSourcePrecedenceReplacementAndClear|TestProfileReplacementIsAtomicPrivateAndOperationallyIndependent|TestProfileRejectsAbsentIdentityAndRegressingSourceTime|TestProfileMutationUsesSharedWriteAdmission|TestRelayTelemetryPersistsBoundedCountWithoutChangingLifecycle|TestRelayTelemetryRejectsInvalidOrAbsentIdentity|TestProfilePersistenceTreatsSQLMetacharactersAsData)$'
pass CASE_02_PROFILE_STORAGE_PRECEDENCE_AND_CLEAR

echo
echo '===== CASE 3: CSV ROUND-TRIP / IMPORT / FORMULA PROTECTION ====='
go test -count=1 ./internal/profilecsv \
    -run '^(TestEncodeDecodeRoundTripAndFormulaProtection|TestListEncodingRoundTripsSemicolonAndBackslash|TestDecodeHeaderOrderOptionalSourceURLAndNormalization|TestDecodeRejectsInvalidShapeAndBounds|TestProtectCellIsReversibleForLeadingApostrophes)$'
go test -count=1 ./internal/discoverycommand \
    -run '^(TestParseCSVInputFormat|TestLoadCSVCandidatesParsesProfilesAndRejectsCanonicalDuplicates|TestPrepareCSVRetainsProfileRowsByPhysicalLine|TestExecuteCSVAppliesProfilesToReadyAndAlreadyKnownRelays|TestConfirmCSVKnownOnlyCountsProfileMutation|TestCSVJSONUsesV2ResultSchemaWithoutChangingLineImportSchema)$'
go test -count=1 ./internal/exportcommand \
    -run '^(TestRenderCSVUsesEffectiveProfilesAndProtectsSpreadsheetCells|TestRenderCSVRequiresProfileRepository)$'
pass CASE_03_CSV_COMPATIBILITY

echo
echo '===== CASE 4: PUBLIC EFFECTIVE PROFILE / PRIVACY / TIER INDEPENDENCE ====='
go test -count=1 ./internal/storage/sqlite \
    -run '^TestDirectoryProjectionPublishesEffectiveProfileWithoutChangingOperationalTier$'
go test -count=1 ./internal/httpapi \
    -run '^(TestHumanDirectoryRendersEffectiveProfileAsEscapedTextAndHTTPSLinks|TestHumanDirectorySharesPublicListingGateAndProjection|TestPublicHTTPDoesNotExposeLocalMaintenance|TestPublicStatusOmitsPrivateModerationFields|TestPublicListingIsIndependentOfLifecycleAvailability)$'
pass CASE_04_PUBLIC_PROFILE_PRIVACY_AND_NONINTERFERENCE

echo
echo '===== CASE 5: PROTOCOL V1 COMPATIBILITY / PROTOCOL V2 FIXTURE ====='
go test -count=1 ./internal/protocol/v1
go test -count=1 ./internal/protocol/v2
go test -count=1 ./internal/httpapi \
    -run '^(TestLifecycleRegisterRouteAcceptsActivityRelayClientFixture|TestLifecycleV2SharedFixturePersistsRelayProfile|TestLifecycleRoutesComposeVerificationAdmissionAndPersistence|TestLifecycleStatusReportsAvailabilityOnlyWithCompleteEnabledGraph|TestStatusReportsLifecycleAndEnrollmentUnavailable)$'
pass CASE_05_PROTOCOL_V1_AND_V2

echo
echo '===== CASE 6: FULL SOURCE TEST SUITE ====='
go test -count=1 ./...
pass CASE_06_FULL_SOURCE_SUITE

echo
echo '===== RESULT ====='
echo 'ARD_1_3_SOURCE_ACCEPTANCE=PASS'
