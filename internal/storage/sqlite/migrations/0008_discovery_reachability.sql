-- Activity-Relay Directory 1.1: operator discovery, independent reachability,
-- positive RFC 9421 evidence, and hard-retention policy version 2.

CREATE TABLE relay_discoveries (
    relay_actor TEXT NOT NULL PRIMARY KEY
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    public_base_url TEXT NOT NULL
        CHECK (length(CAST(public_base_url AS BLOB)) BETWEEN 1 AND 2048),
    discovery_state TEXT NOT NULL
        CHECK (discovery_state IN ('active', 'removed')),
    first_discovered_at_unix INTEGER NOT NULL
        CHECK (first_discovered_at_unix >= 0),
    updated_at_unix INTEGER NOT NULL
        CHECK (updated_at_unix >= first_discovered_at_unix),
    removed_at_unix INTEGER
        CHECK (
            removed_at_unix IS NULL OR
            removed_at_unix >= first_discovered_at_unix
        ),
    CHECK (
        (discovery_state = 'active' AND removed_at_unix IS NULL) OR
        (discovery_state = 'removed' AND removed_at_unix IS NOT NULL)
    ),
    CHECK (removed_at_unix IS NULL OR updated_at_unix >= removed_at_unix)
) STRICT, WITHOUT ROWID;

CREATE INDEX relay_discoveries_retention_candidates_idx
    ON relay_discoveries (discovery_state, removed_at_unix, relay_actor)
    WHERE discovery_state = 'removed';

CREATE TABLE discovery_events (
    discovery_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    relay_actor TEXT NOT NULL
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    action TEXT NOT NULL CHECK (
        action IN (
            'discovery_added',
            'discovery_updated',
            'discovery_unchanged',
            'discovery_removed',
            'discovery_absent'
        )
    ),
    operator_id TEXT NOT NULL CHECK (
        length(CAST(operator_id AS BLOB)) BETWEEN 1 AND 128 AND
        length(operator_id) = length(CAST(operator_id AS BLOB)) AND
        substr(operator_id, 1, 1) GLOB '[A-Za-z0-9]' AND
        operator_id NOT GLOB '*[^A-Za-z0-9@._:+-]*'
    ),
    reason_code TEXT NOT NULL CHECK (
        length(CAST(reason_code AS BLOB)) BETWEEN 1 AND 64 AND
        length(reason_code) = length(CAST(reason_code AS BLOB)) AND
        substr(reason_code, 1, 1) GLOB '[a-z]' AND
        reason_code NOT GLOB '*[^a-z0-9_-]*'
    ),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('manual', 'file')),
    source_label TEXT CHECK (
        source_label IS NULL OR (
            length(CAST(source_label AS BLOB)) BETWEEN 1 AND 128 AND
            length(source_label) = length(CAST(source_label AS BLOB)) AND
            substr(source_label, 1, 1) GLOB '[A-Za-z0-9]' AND
            source_label NOT GLOB '*[^A-Za-z0-9@._:+-]*'
        )
    ),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0)
) STRICT;

CREATE INDEX discovery_events_actor_time_idx
    ON discovery_events (relay_actor, recorded_at_unix, discovery_event_id);

CREATE INDEX discovery_events_retention_version_idx
    ON discovery_events (relay_actor, discovery_event_id);

CREATE TRIGGER discovery_events_no_update
BEFORE UPDATE ON discovery_events
BEGIN
    SELECT RAISE(ABORT, 'discovery events are append-only');
END;

CREATE TRIGGER discovery_events_no_delete
BEFORE DELETE ON discovery_events
BEGIN
    SELECT RAISE(ABORT, 'discovery events are append-only');
END;

CREATE TABLE relay_observations (
    relay_actor TEXT NOT NULL PRIMARY KEY
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    actor_state TEXT NOT NULL DEFAULT 'unknown'
        CHECK (actor_state IN ('unknown', 'reachable', 'unreachable')),
    actor_last_checked_at_unix INTEGER
        CHECK (actor_last_checked_at_unix IS NULL OR actor_last_checked_at_unix >= 0),
    actor_last_success_at_unix INTEGER
        CHECK (actor_last_success_at_unix IS NULL OR actor_last_success_at_unix >= 0),
    inbox_url TEXT
        CHECK (
            inbox_url IS NULL OR
            length(CAST(inbox_url AS BLOB)) BETWEEN 1 AND 4096
        ),
    inbox_declared_at_unix INTEGER
        CHECK (inbox_declared_at_unix IS NULL OR inbox_declared_at_unix >= 0),
    inbox_probe_state TEXT NOT NULL DEFAULT 'not_checked'
        CHECK (inbox_probe_state IN ('not_checked', 'responsive', 'method_rejected', 'unreachable')),
    inbox_last_checked_at_unix INTEGER
        CHECK (inbox_last_checked_at_unix IS NULL OR inbox_last_checked_at_unix >= 0),
    rfc9421_verified_at_unix INTEGER
        CHECK (rfc9421_verified_at_unix IS NULL OR rfc9421_verified_at_unix >= 0),
    updated_at_unix INTEGER NOT NULL CHECK (updated_at_unix >= 0),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    CHECK (
        (actor_state = 'unknown' AND actor_last_checked_at_unix IS NULL AND actor_last_success_at_unix IS NULL) OR
        (actor_state = 'reachable' AND actor_last_checked_at_unix IS NOT NULL AND actor_last_success_at_unix = actor_last_checked_at_unix) OR
        (actor_state = 'unreachable' AND actor_last_checked_at_unix IS NOT NULL AND
            (actor_last_success_at_unix IS NULL OR actor_last_success_at_unix <= actor_last_checked_at_unix))
    ),
    CHECK ((inbox_url IS NULL) = (inbox_declared_at_unix IS NULL)),
    CHECK (
        (inbox_probe_state = 'not_checked' AND inbox_last_checked_at_unix IS NULL) OR
        (inbox_probe_state != 'not_checked' AND inbox_last_checked_at_unix IS NOT NULL)
    ),
    CHECK (actor_last_checked_at_unix IS NULL OR updated_at_unix >= actor_last_checked_at_unix),
    CHECK (actor_last_success_at_unix IS NULL OR updated_at_unix >= actor_last_success_at_unix),
    CHECK (inbox_declared_at_unix IS NULL OR updated_at_unix >= inbox_declared_at_unix),
    CHECK (inbox_last_checked_at_unix IS NULL OR updated_at_unix >= inbox_last_checked_at_unix),
    CHECK (rfc9421_verified_at_unix IS NULL OR updated_at_unix >= rfc9421_verified_at_unix)
) STRICT, WITHOUT ROWID;

CREATE INDEX relay_observations_updated_idx
    ON relay_observations (updated_at_unix, relay_actor);

CREATE TRIGGER relay_observations_identity_guard_insert
BEFORE INSERT ON relay_observations
WHEN NOT EXISTS (SELECT 1 FROM relays WHERE relay_actor = NEW.relay_actor)
 AND NOT EXISTS (SELECT 1 FROM relay_discoveries WHERE relay_actor = NEW.relay_actor)
BEGIN
    SELECT RAISE(ABORT, 'relay observation requires retained identity');
END;

CREATE TRIGGER relay_observations_identity_guard_actor_update
BEFORE UPDATE OF relay_actor ON relay_observations
WHEN NOT EXISTS (SELECT 1 FROM relays WHERE relay_actor = NEW.relay_actor)
 AND NOT EXISTS (SELECT 1 FROM relay_discoveries WHERE relay_actor = NEW.relay_actor)
BEGIN
    SELECT RAISE(ABORT, 'relay observation requires retained identity');
END;

-- Every retained version-1 lifecycle row was created by an accepted RFC 9421
-- request. Backfill only that positive evidence; do not fabricate actor checks.
INSERT INTO relay_observations (
    relay_actor,
    actor_state,
    rfc9421_verified_at_unix,
    updated_at_unix
)
SELECT
    relay_actor,
    'unknown',
    last_seen_at_unix,
    last_seen_at_unix
FROM relays;

-- Hard retention now also removes inactive discovery state and an observation
-- row when no lifecycle/discovery identity remains. Preserve the database
-- identity while advancing the current policy contract to version 2.
CREATE TABLE retention_metadata_v8 (
    singleton INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
    database_identity BLOB NOT NULL CHECK (length(database_identity) = 16),
    policy_version INTEGER NOT NULL CHECK (policy_version = 2)
) STRICT, WITHOUT ROWID;

INSERT INTO retention_metadata_v8 (singleton, database_identity, policy_version)
SELECT singleton, database_identity, 2
FROM retention_metadata;

DROP TRIGGER retention_metadata_no_update;
DROP TRIGGER retention_metadata_no_delete;
DROP TABLE retention_metadata;
ALTER TABLE retention_metadata_v8 RENAME TO retention_metadata;

CREATE TRIGGER retention_metadata_no_update
BEFORE UPDATE ON retention_metadata
BEGIN
    SELECT RAISE(ABORT, 'retention metadata is immutable');
END;

CREATE TRIGGER retention_metadata_no_delete
BEFORE DELETE ON retention_metadata
BEGIN
    SELECT RAISE(ABORT, 'retention metadata is immutable');
END;

CREATE TABLE retention_runs_v8 (
    retention_run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_version INTEGER NOT NULL CHECK (policy_version IN (1, 2)),
    retention_days INTEGER NOT NULL CHECK (retention_days BETWEEN 1 AND 36500),
    observed_at_unix INTEGER NOT NULL CHECK (observed_at_unix >= 0),
    cutoff_at_unix INTEGER NOT NULL CHECK (
        cutoff_at_unix >= 0 AND cutoff_at_unix <= observed_at_unix AND
        observed_at_unix >= retention_days * 86400 AND
        cutoff_at_unix = observed_at_unix - retention_days * 86400
    ),
    candidates_scanned INTEGER NOT NULL DEFAULT 0
        CHECK (candidates_scanned BETWEEN 0 AND 1000),
    purged_relays INTEGER NOT NULL DEFAULT 0
        CHECK (purged_relays BETWEEN 0 AND candidates_scanned),
    purged_discoveries INTEGER NOT NULL DEFAULT 0
        CHECK (purged_discoveries BETWEEN 0 AND candidates_scanned),
    purged_observations INTEGER NOT NULL DEFAULT 0
        CHECK (purged_observations BETWEEN 0 AND candidates_scanned),
    purged_lifecycle_events INTEGER NOT NULL DEFAULT 0
        CHECK (purged_lifecycle_events >= 0),
    skipped INTEGER NOT NULL DEFAULT 0 CHECK (
        skipped BETWEEN 0 AND candidates_scanned AND
        purged_relays + purged_discoveries + skipped <= candidates_scanned
    ),
    batches INTEGER NOT NULL DEFAULT 0 CHECK (
        batches BETWEEN 0 AND 1000 AND
        ((candidates_scanned = 0 AND batches = 0) OR
         (candidates_scanned > 0 AND batches BETWEEN 1 AND candidates_scanned))
    ),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    outcome TEXT NOT NULL DEFAULT 'running'
        CHECK (outcome IN ('running', 'completed', 'canceled', 'failed')),
    backup_sha256 TEXT NOT NULL CHECK (
        length(backup_sha256) = 64 AND
        backup_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    started_at_unix INTEGER NOT NULL CHECK (
        started_at_unix >= observed_at_unix
    ),
    finished_at_unix INTEGER CHECK (
        finished_at_unix IS NULL OR finished_at_unix >= started_at_unix
    ),
    CHECK (
        (outcome = 'running' AND finished_at_unix IS NULL) OR
        (outcome != 'running' AND finished_at_unix IS NOT NULL)
    ),
    CHECK (
        outcome NOT IN ('running', 'completed') OR
        purged_relays + purged_discoveries + skipped = candidates_scanned
    ),
    CHECK (purged_observations <= purged_relays + purged_discoveries),
    CHECK (
        policy_version = 2 OR
        (purged_discoveries = 0 AND purged_observations = 0)
    )
) STRICT;

INSERT INTO retention_runs_v8 (
    retention_run_id,
    policy_version,
    retention_days,
    observed_at_unix,
    cutoff_at_unix,
    candidates_scanned,
    purged_relays,
    purged_discoveries,
    purged_observations,
    purged_lifecycle_events,
    skipped,
    batches,
    truncated,
    outcome,
    backup_sha256,
    started_at_unix,
    finished_at_unix
)
SELECT
    retention_run_id,
    policy_version,
    retention_days,
    observed_at_unix,
    cutoff_at_unix,
    candidates_scanned,
    purged_relays,
    0,
    0,
    purged_lifecycle_events,
    skipped,
    batches,
    truncated,
    outcome,
    backup_sha256,
    started_at_unix,
    finished_at_unix
FROM retention_runs
ORDER BY retention_run_id;

DROP TRIGGER retention_runs_guard_update;
DROP TRIGGER retention_runs_no_delete;
DROP INDEX retention_runs_time_idx;
DROP TABLE retention_runs;
ALTER TABLE retention_runs_v8 RENAME TO retention_runs;

CREATE INDEX retention_runs_time_idx
    ON retention_runs (started_at_unix, retention_run_id);

CREATE TRIGGER retention_runs_guard_update
BEFORE UPDATE ON retention_runs
WHEN
    OLD.policy_version != NEW.policy_version OR
    OLD.retention_days != NEW.retention_days OR
    OLD.observed_at_unix != NEW.observed_at_unix OR
    OLD.cutoff_at_unix != NEW.cutoff_at_unix OR
    OLD.backup_sha256 != NEW.backup_sha256 OR
    OLD.started_at_unix != NEW.started_at_unix OR
    OLD.outcome != 'running' OR
    NEW.candidates_scanned < OLD.candidates_scanned OR
    NEW.purged_relays < OLD.purged_relays OR
    NEW.purged_discoveries < OLD.purged_discoveries OR
    NEW.purged_observations < OLD.purged_observations OR
    NEW.purged_lifecycle_events < OLD.purged_lifecycle_events OR
    NEW.skipped < OLD.skipped OR
    NEW.batches < OLD.batches OR
    (OLD.truncated = 1 AND NEW.truncated = 0)
BEGIN
    SELECT RAISE(ABORT, 'retention run audit update is invalid');
END;

CREATE TRIGGER retention_runs_no_delete
BEFORE DELETE ON retention_runs
BEGIN
    SELECT RAISE(ABORT, 'retention runs cannot be deleted');
END;
