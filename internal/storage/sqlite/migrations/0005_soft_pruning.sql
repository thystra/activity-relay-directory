CREATE TABLE relays_pruning_v5 (
    relay_actor TEXT NOT NULL PRIMARY KEY,
    public_base_url TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL
        CHECK (lifecycle_state IN ('registered', 'unregistered', 'pruned')),
    administrative_state TEXT NOT NULL DEFAULT 'active'
        CHECK (administrative_state IN ('active', 'suspended')),
    first_registered_at_unix INTEGER NOT NULL
        CHECK (first_registered_at_unix >= 0),
    updated_at_unix INTEGER NOT NULL
        CHECK (updated_at_unix >= first_registered_at_unix),
    last_seen_at_unix INTEGER NOT NULL
        CHECK (last_seen_at_unix >= first_registered_at_unix),
    last_heartbeat_at_unix INTEGER
        CHECK (
            last_heartbeat_at_unix IS NULL OR
            last_heartbeat_at_unix >= first_registered_at_unix
        ),
    unregistered_at_unix INTEGER
        CHECK (
            unregistered_at_unix IS NULL OR
            unregistered_at_unix >= first_registered_at_unix
        ),
    pruned_at_unix INTEGER
        CHECK (
            pruned_at_unix IS NULL OR
            pruned_at_unix >= first_registered_at_unix
        ),
    suspended_at_unix INTEGER
        CHECK (
            suspended_at_unix IS NULL OR
            suspended_at_unix >= first_registered_at_unix
        ),
    CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    CHECK (length(CAST(public_base_url AS BLOB)) BETWEEN 1 AND 2048),
    CHECK (
        (lifecycle_state = 'registered' AND
            unregistered_at_unix IS NULL AND pruned_at_unix IS NULL) OR
        (lifecycle_state = 'unregistered' AND
            unregistered_at_unix IS NOT NULL AND pruned_at_unix IS NULL) OR
        (lifecycle_state = 'pruned' AND
            unregistered_at_unix IS NULL AND pruned_at_unix IS NOT NULL)
    ),
    CHECK (
        (administrative_state = 'active' AND suspended_at_unix IS NULL) OR
        (administrative_state = 'suspended' AND suspended_at_unix IS NOT NULL)
    ),
    CHECK (last_heartbeat_at_unix IS NULL OR updated_at_unix >= last_heartbeat_at_unix),
    CHECK (last_heartbeat_at_unix IS NULL OR last_seen_at_unix >= last_heartbeat_at_unix),
    CHECK (unregistered_at_unix IS NULL OR updated_at_unix >= unregistered_at_unix),
    CHECK (pruned_at_unix IS NULL OR updated_at_unix >= pruned_at_unix),
    CHECK (pruned_at_unix IS NULL OR pruned_at_unix >= last_seen_at_unix),
    CHECK (suspended_at_unix IS NULL OR updated_at_unix >= suspended_at_unix)
) STRICT, WITHOUT ROWID;

INSERT INTO relays_pruning_v5 (
    relay_actor,
    public_base_url,
    lifecycle_state,
    administrative_state,
    first_registered_at_unix,
    updated_at_unix,
    last_seen_at_unix,
    last_heartbeat_at_unix,
    unregistered_at_unix,
    pruned_at_unix,
    suspended_at_unix
)
SELECT
    relay_actor,
    public_base_url,
    lifecycle_state,
    administrative_state,
    first_registered_at_unix,
    updated_at_unix,
    last_seen_at_unix,
    last_heartbeat_at_unix,
    unregistered_at_unix,
    NULL,
    suspended_at_unix
FROM relays;

CREATE TABLE relay_events_pruning_v5 (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    relay_actor TEXT NOT NULL
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    event_kind TEXT NOT NULL CHECK (
        event_kind IN (
            'register_created',
            'register_updated',
            'register_unchanged',
            'heartbeat_recorded',
            'unregister_removed',
            'unregister_absent',
            'relay_pruned'
        )
    ),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0)
) STRICT;

INSERT INTO relay_events_pruning_v5 (
    event_id,
    relay_actor,
    event_kind,
    recorded_at_unix
)
SELECT event_id, relay_actor, event_kind, recorded_at_unix
FROM relay_events
ORDER BY event_id;

DROP TRIGGER relay_events_no_update;
DROP TRIGGER relay_events_no_delete;
DROP INDEX relay_events_actor_time_idx;
DROP INDEX relays_health_projection_idx;
DROP TABLE relay_events;
DROP TABLE relays;
ALTER TABLE relays_pruning_v5 RENAME TO relays;
ALTER TABLE relay_events_pruning_v5 RENAME TO relay_events;

CREATE INDEX relays_health_projection_idx
    ON relays (
        lifecycle_state,
        administrative_state,
        last_seen_at_unix,
        relay_actor
    );

CREATE INDEX relays_pruning_candidates_idx
    ON relays (
        lifecycle_state,
        last_seen_at_unix,
        relay_actor
    );

CREATE INDEX relay_events_actor_time_idx
    ON relay_events (relay_actor, recorded_at_unix, event_id);

CREATE TRIGGER relay_events_no_update
BEFORE UPDATE ON relay_events
BEGIN
    SELECT RAISE(ABORT, 'relay events are append-only');
END;

CREATE TRIGGER relay_events_no_delete
BEFORE DELETE ON relay_events
BEGIN
    SELECT RAISE(ABORT, 'relay events are append-only');
END;
