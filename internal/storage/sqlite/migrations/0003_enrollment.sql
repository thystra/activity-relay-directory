CREATE TABLE directory_policy (
    singleton INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
    enrollment_open INTEGER NOT NULL DEFAULT 0
        CHECK (enrollment_open IN (0, 1)),
    updated_at_unix INTEGER NOT NULL DEFAULT 0
        CHECK (updated_at_unix >= 0)
) STRICT, WITHOUT ROWID;

INSERT INTO directory_policy (singleton, enrollment_open, updated_at_unix)
VALUES (1, 0, 0);

CREATE TABLE enrollment_events (
    enrollment_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    action TEXT NOT NULL CHECK (
        action IN (
            'enrollment_opened',
            'enrollment_open_unchanged',
            'enrollment_closed',
            'enrollment_closed_unchanged'
        )
    ),
    operator_id TEXT NOT NULL CHECK (
        length(CAST(operator_id AS BLOB)) BETWEEN 1 AND 128 AND
        length(operator_id) = length(CAST(operator_id AS BLOB)) AND
        substr(operator_id, 1, 1) GLOB '[A-Za-z0-9]' AND
        operator_id NOT GLOB '*[^A-Za-z0-9@._:+-]*'
    ),
    recorded_at_unix INTEGER NOT NULL CHECK (recorded_at_unix >= 0)
) STRICT;

CREATE INDEX enrollment_events_time_idx
    ON enrollment_events (recorded_at_unix, enrollment_event_id);

CREATE TRIGGER enrollment_events_no_update
BEFORE UPDATE ON enrollment_events
BEGIN
    SELECT RAISE(ABORT, 'enrollment events are append-only');
END;

CREATE TRIGGER enrollment_events_no_delete
BEFORE DELETE ON enrollment_events
BEGIN
    SELECT RAISE(ABORT, 'enrollment events are append-only');
END;
