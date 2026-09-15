#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_TAG="${ARD_1_1_BASE_TAG:-v1.0.0}"
EXPECTED_BASE_TAG_OBJECT="8df73f69e8374247858fea878b6c678e74eed2a1"
EXPECTED_BASE_COMMIT="69af9c1bce23adf2c10afaefd26f163852e6a7e2"

pass() {
    printf '%s=PASS\n' "$1"
}

echo '===== ARD 1.1 TRANCHE 23 ACCEPTANCE ====='
printf 'head=%s\n' "$(git rev-parse HEAD)"
printf 'tree=%s\n' "$(git rev-parse HEAD^{tree})"
printf 'base_tag=%s\n' "$BASE_TAG"

git rev-parse -q --verify "refs/tags/$BASE_TAG^{commit}" >/dev/null

echo
echo '===== CASE 1: EXACT 1.0.0 / SCHEMA-7 UPGRADE ====='

BASE_TAG_OBJECT="$(git rev-parse "$BASE_TAG")"
BASE_COMMIT="$(git rev-parse "$BASE_TAG^{commit}")"

printf 'base_tag_object=%s\n' "$BASE_TAG_OBJECT"
printf 'base_commit=%s\n' "$BASE_COMMIT"

[[ "$BASE_TAG_OBJECT" == "$EXPECTED_BASE_TAG_OBJECT" ]]
[[ "$BASE_COMMIT" == "$EXPECTED_BASE_COMMIT" ]]

mapfile -t RELEASED_MIGRATIONS < <(
    git ls-tree -r --name-only "$BASE_TAG" -- \
        internal/storage/sqlite/migrations |
    LC_ALL=C sort
)

mapfile -t CURRENT_MIGRATIONS < <(
    find internal/storage/sqlite/migrations \
        -maxdepth 1 \
        -type f \
        -printf '%p\n' |
    LC_ALL=C sort
)

[[ "${#RELEASED_MIGRATIONS[@]}" -eq 7 ]]
[[ "${#CURRENT_MIGRATIONS[@]}" -eq 8 ]]

for migration_path in "${RELEASED_MIGRATIONS[@]}"; do
    cmp -s \
        <(git show "$BASE_TAG:$migration_path") \
        "$migration_path" || {
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
    "internal/storage/sqlite/migrations/0008_discovery_reachability.sql" ]]

pass CASE_01_RELEASED_MIGRATIONS_1_TO_7_BYTE_IDENTICAL
pass CASE_01_SCHEMA8_IS_ONLY_NEW_MIGRATION

go test -count=1 ./internal/storage/sqlite \
    -run '^TestDiscoveryReachabilityMigrationUpgradesVersionSevenPreservingRetentionIdentityAndAudit$'

pass CASE_01_EXACT_1_0_0_SCHEMA_UPGRADE

echo '===== CASES 2/3/4/10: PARTICIPATION / PROJECTION MATRIX ====='

go test -count=1 ./internal/storage/sqlite \
    -run '^TestTranche23AcceptanceParticipationMatrix$'

pass CASE_02_HEALTHY_REGISTERED_REACHABLE
pass CASE_03_DEAD_REGISTERED_REACHABLE
pass CASE_04_DISCOVERED_ONLY_NO_FAKE_HEARTBEAT
pass CASE_10_SUSPENSION_OVERRIDES_BOTH_PATHS

echo
echo '===== CASE 5: BASE / ACTOR / INBOX CONVERGENCE ====='

go test -count=1 ./internal/discoverycommand \
    -run '^TestPrepareDeduplicatesCanonicalActorsAndProbesInbox$'

pass CASE_05_BASE_ACTOR_INBOX_CONVERGENCE

echo
echo '===== CASE 6: FAIL-CLOSED RESOLVER / NETWORK MATRIX ====='

go test -count=1 ./internal/actorresolver \
    -run '^(TestResolverEnforcesResponseBoundary|TestResolverRejectsAmbiguousActorDocuments|TestResolverExplicitTimeoutAndTLSFailuresStayFailClosed|TestResolverProbesRejectProhibitedLiteralTargetsBeforeTransport|TestActivityStreamsContentTypes|TestPublicNetworkAddressPolicy|TestSafeDialerPinsApprovedDNSAddress|TestSafeDialerRejectsMixedOrInvalidAnswersBeforeDial|TestSafeDialerBoundsDNSAnswers|TestActorRedirectPolicy|TestProductionHTTPClientSecuritySettings)$'

pass CASE_06_FAIL_CLOSED_NETWORK_MATRIX

echo
echo '===== CASE 7: NORMAL INBOX METHOD REJECTION ====='

go test -count=1 ./internal/actorresolver \
    -run '^TestResolverInboxProbeIsNonMutatingAndConservative$'

pass CASE_07_INBOX_METHOD_REJECTION

echo
echo '===== CASE 8: RFC 9421 POSITIVE EVIDENCE ====='

go test -count=1 ./internal/httpapi \
    -run '^TestTranche23AcceptanceSignedLifecyclePersistsRFC9421Evidence$'

go test -count=1 ./internal/storage/sqlite \
    -run '^(TestLifecycleAcceptedRequestsAdvanceRFC9421EvidenceWithoutChangingHeartbeatSemantics|TestSignedUnregisterCanVerifyDiscoveredOnlyRelayWithoutFabricatingLifecycle)$'

pass CASE_08_SIGNED_REQUEST_TO_SQLITE_EVIDENCE
pass CASE_08_RFC9421_ACCEPTED_LIFECYCLE_ONLY

echo '===== CASE 9: PARTICIPATION PATHS REMAIN INDEPENDENT ====='

go test -count=1 ./internal/storage/sqlite \
    -run '^TestTranche23AcceptanceParticipationTransitionsRemainIndependent$'

pass CASE_09_INDEPENDENT_PARTICIPATION_TRANSITIONS

echo
echo '===== CASE 11: PRUNING RACE / FRESH REACHABILITY ====='

go test -count=1 ./internal/storage/sqlite \
    -run '^TestSoftPruneRevalidatesFreshReachabilityRace$'

pass CASE_11_PRUNING_FRESH_REACHABILITY_RACE

echo
echo '===== CASE 12: SCHEMA-8 INACTIVE RETENTION ====='

go test -count=1 ./internal/storage/sqlite \
    -run '^(TestPurgeCandidatesIncludeRemovedDiscoveryAndPurgePreservesPrivateDiscoveryAudit|TestPurgeLifecyclePreservesObservationOwnedByActiveDiscovery|TestPurgeBatchSkipsCandidateAfterFreshObservation)$'

pass CASE_12_SCHEMA8_INACTIVE_RETENTION

echo
echo '===== RESULT ====='
echo 'ARD_1_1_TRANCHE23_ACCEPTANCE_MATRIX=PASS'
