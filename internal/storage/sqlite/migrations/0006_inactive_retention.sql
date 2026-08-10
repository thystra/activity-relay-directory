CREATE TABLE retention_metadata (
    singleton INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
    database_identity BLOB NOT NULL CHECK (length(database_identity) = 16),
    policy_version INTEGER NOT NULL CHECK (policy_version = 1)
) STRICT, WITHOUT ROWID;

INSERT INTO retention_metadata (singleton, database_identity, policy_version)
VALUES (1, randomblob(16), 1);

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

CREATE TABLE retention_runs (
    retention_run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_version INTEGER NOT NULL CHECK (policy_version = 1),
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
    purged_lifecycle_events INTEGER NOT NULL DEFAULT 0
        CHECK (purged_lifecycle_events >= 0),
    skipped INTEGER NOT NULL DEFAULT 0 CHECK (
        skipped BETWEEN 0 AND candidates_scanned AND
        purged_relays + skipped <= candidates_scanned
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
        purged_relays + skipped = candidates_scanned
    )
) STRICT;

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

CREATE INDEX relay_events_retention_version_idx
    ON relay_events (relay_actor, event_id);

CREATE INDEX moderation_events_retention_version_idx
    ON moderation_events (relay_actor, moderation_event_id);

CREATE INDEX relays_retention_candidates_idx
    ON relays (
        administrative_state,
        (
            CASE lifecycle_state
                WHEN 'unregistered' THEN unregistered_at_unix
                WHEN 'pruned' THEN pruned_at_unix
                ELSE NULL
            END
        ),
        relay_actor
    )
    WHERE lifecycle_state IN ('unregistered', 'pruned');
