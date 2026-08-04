CREATE TABLE relays (
    relay_actor TEXT NOT NULL PRIMARY KEY,
    public_base_url TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL
        CHECK (lifecycle_state IN ('registered', 'unregistered')),
    administrative_state TEXT NOT NULL DEFAULT 'active'
        CHECK (administrative_state IN ('active', 'suspended')),
    first_registered_at_unix INTEGER NOT NULL
        CHECK (first_registered_at_unix >= 0),
    updated_at_unix INTEGER NOT NULL
        CHECK (updated_at_unix >= first_registered_at_unix),
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
    suspended_at_unix INTEGER
        CHECK (
            suspended_at_unix IS NULL OR
            suspended_at_unix >= first_registered_at_unix
        ),
    CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    CHECK (length(CAST(public_base_url AS BLOB)) BETWEEN 1 AND 2048),
    CHECK (
        (lifecycle_state = 'registered' AND unregistered_at_unix IS NULL) OR
        (lifecycle_state = 'unregistered' AND unregistered_at_unix IS NOT NULL)
    ),
    CHECK (
        (administrative_state = 'active' AND suspended_at_unix IS NULL) OR
        (administrative_state = 'suspended' AND suspended_at_unix IS NOT NULL)
    ),
    CHECK (last_heartbeat_at_unix IS NULL OR updated_at_unix >= last_heartbeat_at_unix),
    CHECK (unregistered_at_unix IS NULL OR updated_at_unix >= unregistered_at_unix),
    CHECK (suspended_at_unix IS NULL OR updated_at_unix >= suspended_at_unix)
) STRICT, WITHOUT ROWID;

CREATE INDEX relays_listing_state_idx
    ON relays (lifecycle_state, administrative_state, last_heartbeat_at_unix);

CREATE TABLE replay_reservations (
    replay_key BLOB NOT NULL PRIMARY KEY
        CHECK (length(replay_key) = 32),
    reserved_at_unix INTEGER NOT NULL CHECK (reserved_at_unix >= 0),
    expires_at_unix INTEGER NOT NULL CHECK (expires_at_unix > reserved_at_unix)
) STRICT, WITHOUT ROWID;

CREATE INDEX replay_reservations_expiry_idx
    ON replay_reservations (expires_at_unix);

CREATE TABLE relay_events (
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
            'unregister_absent'
        )
    ),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0)
) STRICT;

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
