CREATE TABLE moderation_events (
    moderation_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    relay_actor TEXT NOT NULL
        CHECK (length(CAST(relay_actor AS BLOB)) BETWEEN 1 AND 4096),
    action TEXT NOT NULL CHECK (
        action IN (
            'suspend_applied',
            'suspend_unchanged',
            'restore_applied',
            'restore_unchanged'
        )
    ),
    moderator_id TEXT NOT NULL CHECK (
        length(CAST(moderator_id AS BLOB)) BETWEEN 1 AND 128 AND
        length(moderator_id) = length(CAST(moderator_id AS BLOB)) AND
        substr(moderator_id, 1, 1) GLOB '[A-Za-z0-9]' AND
        moderator_id NOT GLOB '*[^A-Za-z0-9@._:+-]*'
    ),
    reason_code TEXT NOT NULL CHECK (
        length(CAST(reason_code AS BLOB)) BETWEEN 1 AND 64 AND
        length(reason_code) = length(CAST(reason_code AS BLOB)) AND
        substr(reason_code, 1, 1) GLOB '[a-z]' AND
        reason_code NOT GLOB '*[^a-z0-9_-]*'
    ),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0)
) STRICT;

CREATE INDEX moderation_events_actor_time_idx
    ON moderation_events (relay_actor, recorded_at_unix, moderation_event_id);

CREATE TRIGGER moderation_events_no_update
BEFORE UPDATE ON moderation_events
BEGIN
    SELECT RAISE(ABORT, 'moderation events are append-only');
END;

CREATE TRIGGER moderation_events_no_delete
BEFORE DELETE ON moderation_events
BEGIN
    SELECT RAISE(ABORT, 'moderation events are append-only');
END;
