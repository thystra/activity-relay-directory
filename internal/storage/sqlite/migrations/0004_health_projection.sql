CREATE TABLE relays_health_v4 (
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
    CHECK (last_heartbeat_at_unix IS NULL OR last_seen_at_unix >= last_heartbeat_at_unix),
    CHECK (unregistered_at_unix IS NULL OR updated_at_unix >= unregistered_at_unix),
    CHECK (suspended_at_unix IS NULL OR updated_at_unix >= suspended_at_unix)
) STRICT, WITHOUT ROWID;

INSERT INTO relays_health_v4 (
    relay_actor,
    public_base_url,
    lifecycle_state,
    administrative_state,
    first_registered_at_unix,
    updated_at_unix,
    last_seen_at_unix,
    last_heartbeat_at_unix,
    unregistered_at_unix,
    suspended_at_unix
)
SELECT
    relays.relay_actor,
    relays.public_base_url,
    relays.lifecycle_state,
    relays.administrative_state,
    relays.first_registered_at_unix,
    relays.updated_at_unix,
    MAX(
        relays.first_registered_at_unix,
        COALESCE(
            relays.last_heartbeat_at_unix,
            relays.first_registered_at_unix
        ),
        COALESCE(
            (
                SELECT MAX(relay_events.recorded_at_unix)
                FROM relay_events
                WHERE relay_events.relay_actor = relays.relay_actor
                  AND relay_events.event_kind IN (
                      'register_created',
                      'register_updated',
                      'register_unchanged',
                      'heartbeat_recorded'
                  )
            ),
            relays.first_registered_at_unix
        )
    ),
    relays.last_heartbeat_at_unix,
    relays.unregistered_at_unix,
    relays.suspended_at_unix
FROM relays;

DROP INDEX relays_listing_state_idx;
DROP TABLE relays;
ALTER TABLE relays_health_v4 RENAME TO relays;

CREATE INDEX relays_health_projection_idx
    ON relays (
        lifecycle_state,
        administrative_state,
        last_seen_at_unix,
        relay_actor
    );
